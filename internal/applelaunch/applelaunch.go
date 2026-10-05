// Package applelaunch runs the aibox VM with Apple's container tool: the
// state volume of the project, the container, the link to its guest and
// the proxy and the terminal over that link.
package applelaunch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/the127/aibox/internal/apple"
	"github.com/the127/aibox/internal/backend"
	"github.com/the127/aibox/internal/host"
	"github.com/the127/aibox/internal/link"
)

const (
	// imageRefFile is the file in the image folder that names the image in
	// the store of the tool.
	imageRefFile = "container-image"

	defaultBootTimeout     = time.Minute
	defaultSessionEndDelay = 3 * time.Second

	dialInterval = 100 * time.Millisecond
	// killWait is how long the tool may take to end a VM it was told to
	// kill before aibox stops waiting for it.
	killWait = 10 * time.Second
)

var (
	// ErrNoImage is an image folder that names no image the tool has.
	ErrNoImage = errors.New("no image for Apple's container tool, build it with just install-container-image or pass --image")
	// ErrNoBoot is a VM whose guest did not answer in time.
	ErrNoBoot = errors.New("the VM did not come up")
)

// Backend runs the VM with the container tool at Program. BootTimeout is how
// long the guest may take to answer, SessionEndDelay how long the session
// may go on after the VM stopped, to show its last output.
type Backend struct {
	Program         string
	BootTimeout     time.Duration
	SessionEndDelay time.Duration
}

var _ backend.Backend = Backend{}

// NewBackend returns the Backend for the container tool on the PATH.
func NewBackend() Backend {
	return Backend{Program: "container", BootTimeout: defaultBootTimeout, SessionEndDelay: defaultSessionEndDelay}
}

// CheckImage says whether the folder names an image the tool has.
func (b Backend) CheckImage(dir string) error {
	ref, err := imageRef(dir)
	if err != nil {
		return err
	}

	if err := b.tool(context.Background(), "image", "inspect", ref); err != nil {
		return fmt.Errorf("%w: the tool has no %s: %w", ErrNoImage, ref, err)
	}

	return nil
}

func imageRef(dir string) (string, error) {
	path := filepath.Join(dir, imageRefFile)

	content, err := os.ReadFile(path) //nolint:gosec // the image folder is the person's
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrNoImage, err)
	}

	ref := strings.TrimSpace(string(content))
	if ref == "" {
		return "", fmt.Errorf("%w: %s is empty", ErrNoImage, path)
	}

	return ref, nil
}

// Run boots the VM of the spec and returns when it has stopped. The state
// volume and the socket folder are made first, and the socket folder is
// removed again.
func (b Backend) Run(ctx context.Context, spec backend.Spec) error {
	ref, err := imageRef(spec.Image)
	if err != nil {
		return err
	}

	volume := volumeName(spec.State)
	if err := b.makeVolume(ctx, volume, spec.StateBytes); err != nil {
		return err
	}

	// macOS takes socket paths of up to 104 bytes, and the temporary folder
	// of the person already takes half of that
	dir, err := os.MkdirTemp("/tmp", "aibox-")
	if err != nil {
		return fmt.Errorf("create the socket folder: %w", err)
	}

	defer func() { _ = os.RemoveAll(dir) }()

	machine := apple.Machine{
		Name:      filepath.Base(dir),
		Image:     ref,
		State:     volume,
		MemoryMiB: spec.MemoryMiB,
		CPUs:      spec.CPUs,
		Project:   spec.Project,
		Home:      spec.Home,
		Shell:     spec.Shell,
		Socket:    filepath.Join(dir, "link.sock"),
	}

	for _, mount := range spec.Mounts {
		machine.Mounts = append(machine.Mounts, apple.Mount{Host: mount.Host, Guest: mount.Guest})
	}

	args, err := machine.RunArgs()
	if err != nil {
		return err
	}

	console, err := openConsole(spec.ConsoleLog)
	if err != nil {
		return err
	}

	defer func() { _ = console.Close() }()

	vm, err := b.start(args, console)
	if err != nil {
		return err
	}

	end, err := b.connect(ctx, machine.Socket, vm)
	if err != nil {
		b.kill(machine.Name, vm)

		if errors.Is(err, errVMEnded) {
			return host.Result(nil, host.Outcome{}, spec.ConsoleLog)
		}

		return err
	}

	defer func() { _ = end.Close() }()

	stopProxy := host.ServeProxy(ctx, end.Proxy(), spec.Proxy, spec.Stderr)
	defer stopProxy()

	stopTerminal := host.ServeTerminal(ctx, end.Terminal(), host.Session{
		Stdin:    spec.Stdin,
		Stdout:   spec.Stdout,
		Env:      spec.Env,
		EndDelay: b.SessionEndDelay,
	})

	// the init ends the VM with a restart, which the tool reports as a
	// signal, so how the tool exits says nothing and the session says it all
	var vmErr error

	select {
	case <-vm.done:
	case <-ctx.Done():
		b.kill(machine.Name, vm)

		vmErr = ctx.Err()
	}

	return host.Result(vmErr, stopTerminal(), spec.ConsoleLog)
}

// volumeName is the volume that keeps the state of the project whose state
// disk the spec places at the path. Names of volumes are short, paths are
// not, so it is named after a hash.
func volumeName(state string) string {
	sum := sha256.Sum256([]byte(state))

	return "aibox-state-" + hex.EncodeToString(sum[:8])
}

// makeVolume makes the volume with the size unless it exists. An existing
// volume keeps its size.
func (b Backend) makeVolume(ctx context.Context, name string, size int64) error {
	if err := b.tool(ctx, "volume", "inspect", name); err == nil {
		return nil
	}

	if err := b.tool(ctx, "volume", "create", "-s", strconv.FormatInt(size, 10), name); err != nil {
		return fmt.Errorf("create the state volume %s: %w", name, err)
	}

	return nil
}

// tool runs the tool and returns what it printed as the error when it fails,
// or the error of the context when that ended it.
func (b Backend) tool(ctx context.Context, args ...string) error {
	out, err := exec.CommandContext(ctx, b.Program, args...).CombinedOutput() //nolint:gosec // the arguments are aibox's
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}

	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}

	return nil
}

func openConsole(path string) (io.WriteCloser, error) {
	if path == "" {
		return nopCloser{io.Discard}, nil
	}

	console, err := os.Create(path) //nolint:gosec // the console log of the project
	if err != nil {
		return nil, fmt.Errorf("open the console log: %w", err)
	}

	return console, nil
}

type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }

// running is the tool running the VM. done is closed when it exited.
type running struct {
	cmd  *exec.Cmd
	done chan struct{}
}

// start runs the tool with its output in the console log. It never gets the
// terminal, which belongs to the session.
func (b Backend) start(args []string, console io.Writer) (*running, error) {
	cmd := exec.Command(b.Program, args...) //nolint:gosec // the arguments are aibox's
	cmd.Stdout = console
	cmd.Stderr = console
	// a Ctrl-C from the terminal reaches the session, not the tool
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", b.Program, err)
	}

	vm := &running{cmd: cmd, done: make(chan struct{})}

	go func() {
		_ = cmd.Wait()

		close(vm.done)
	}()

	return vm, nil
}

// errVMEnded is a VM that ended before its guest answered.
var errVMEnded = errors.New("the VM ended")

// connect connects to the guest over the socket until it greets, the VM
// ends, the boot timeout passes or the context ends. The tool takes and
// drops connections while the guest does not listen yet, so those are
// tried again.
func (b Backend) connect(ctx context.Context, socket string, vm *running) (*link.HostEnd, error) {
	timeout := time.NewTimer(b.BootTimeout)
	defer timeout.Stop()

	for {
		if conn, err := net.Dial("unix", socket); err == nil {
			end, err := link.Host(conn)
			if err == nil {
				return end, nil
			}

			if !errors.Is(err, link.ErrNoGuest) {
				return nil, err
			}
		}

		select {
		case <-vm.done:
			return nil, errVMEnded
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timeout.C:
			return nil, fmt.Errorf("%w in %v", ErrNoBoot, b.BootTimeout)
		case <-time.After(dialInterval):
		}
	}
}

// kill tells the tool to end the VM and waits for it, for a while.
func (b Backend) kill(name string, vm *running) {
	select {
	case <-vm.done:
		return
	default:
	}

	ctx, cancel := context.WithTimeout(context.Background(), killWait)
	defer cancel()

	_ = b.tool(ctx, "kill", name)

	select {
	case <-vm.done:
	case <-ctx.Done():
		_ = vm.cmd.Process.Kill()

		<-vm.done
	}
}
