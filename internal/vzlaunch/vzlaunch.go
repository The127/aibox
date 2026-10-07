//go:build darwin && cgo

// Package vzlaunch runs the aibox VM with Virtualization.framework of macOS,
// as launch runs it with QEMU on Linux: aibox owns the VM, the shares are
// virtiofs, the state disk is a block device and the terminal and the proxy
// are on vsock ports only aibox can reach.
package vzlaunch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/Code-Hex/vz/v3"

	"github.com/the127/aibox/internal/backend"
	"github.com/the127/aibox/internal/host"
	"github.com/the127/aibox/internal/machine"
	"github.com/the127/aibox/internal/seatbelt"
	"github.com/the127/aibox/internal/vm"
)

const (
	// the vsock ports of the host. Only the process that owns the VM can
	// reach its vsock, so fixed ports are no one else's.
	terminalPort = 1024
	proxyPort    = 1025

	// baseCmdline boots the root disk read-only, as under QEMU. A panicking
	// guest reboots, which Virtualization.framework carries out rather than
	// ending the VM. The guest then connects to the terminal again, which is
	// how Run learns that it started over and stops it.
	baseCmdline = vm.RootCmdline + " panic=1"

	defaultBootTimeout  = time.Minute
	defaultPowerOffWait = 10 * time.Second
	// stopTimeout is how long a VM may take to stop once told to.
	stopTimeout = 10 * time.Second
	// consoleDrainTimeout is how long the console log waits for the end
	// of the console once the VM is gone.
	consoleDrainTimeout = time.Second
)

var (
	// ErrNoBoot is a VM whose guest did not connect in time.
	ErrNoBoot = errors.New("the VM did not come up")
	// ErrStartedOver is a guest that connected again, after it started
	// over, most likely from a kernel panic.
	ErrStartedOver = errors.New("the guest started over, most likely after a kernel panic")
)

// Backend runs the VM with Virtualization.framework. BootTimeout is how long
// the guest may take to connect, SessionEndDelay how long the session may go
// on after the VM stopped and PowerOffWait how long the VM may take to power
// off after the session before it is stopped. Confine is called once the VM
// runs, with the TCP ports the proxy may still connect to.
type Backend struct {
	BootTimeout     time.Duration
	SessionEndDelay time.Duration
	PowerOffWait    time.Duration
	Confine         func(ports []uint16) error
}

var _ backend.Backend = Backend{}

// NewBackend returns the Backend with its default timeouts.
func NewBackend() Backend {
	return Backend{
		BootTimeout:     defaultBootTimeout,
		SessionEndDelay: host.DefaultSessionEndDelay,
		PowerOffWait:    defaultPowerOffWait,
		Confine:         seatbelt.Apply,
	}
}

// Run boots the VM of the spec with the proxy and the terminal served to it
// and returns when it stopped, or once it was stopped because the context
// ended, the guest did not connect in time or it stayed up after its
// session.
func (b Backend) Run(ctx context.Context, spec backend.Spec) error {
	if spec.Stderr == nil {
		spec.Stderr = io.Discard
	}

	m, err := machine.Prepare(spec)
	if err != nil {
		return err
	}

	m.TerminalPort = terminalPort
	m.ProxyPort = proxyPort

	console, err := openConsole(spec.ConsoleLog)
	if err != nil {
		return err
	}

	defer console.close()

	// the guest reads nothing from its console, and vz takes only the
	// number of the file, so the file stays open until the VM is gone
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		return err
	}

	defer func() { _ = devNull.Close() }()

	// the VM writes its console into a pipe, so that aibox can cut the log
	// off at its limit
	consoleIn, finishConsole, err := pipeConsole(host.ConsoleLog(console.File), consoleDrainTimeout)
	if err != nil {
		return err
	}

	defer finishConsole()

	config, err := configure(m, devNull, consoleIn)
	if err != nil {
		return err
	}

	if ok, err := config.Validate(); !ok {
		return fmt.Errorf("the configuration of the VM is not valid: %w", err)
	}

	v, err := vz.NewVirtualMachine(config)
	if err != nil {
		return fmt.Errorf("create the VM: %w", err)
	}

	socket := v.SocketDevices()[0]

	terminalListener, err := socket.Listen(terminalPort)
	if err != nil {
		return fmt.Errorf("listen for the terminal: %w", err)
	}

	proxyListener, err := socket.Listen(proxyPort)
	if err != nil {
		_ = newListener(terminalListener).Close()

		return fmt.Errorf("listen for the proxy: %w", err)
	}

	stopProxy := host.ServeProxy(ctx, newListener(proxyListener), spec.Proxy, spec.Stderr)
	defer stopProxy()

	terminal := watch(newListener(terminalListener))
	stopTerminal := host.ServeTerminal(ctx, terminal, host.Session{
		Stdin:    spec.Stdin,
		Stdout:   spec.Stdout,
		Errors:   spec.Progress,
		Env:      spec.Env,
		EndDelay: b.SessionEndDelay,
	})

	// Virtualization.framework locks the state disk as it starts the VM and
	// for as long as it runs, and refuses to start on a disk that is locked,
	// which keeps a second run of the project off
	if err := v.Start(); err != nil {
		return host.Result(startError(err, m.State), stopTerminal(), spec.ConsoleLog)
	}

	if err := console.keep(); err != nil {
		_, _ = fmt.Fprintf(spec.Stderr, "aibox: %v\n", err)
	}

	// the VM has the disk open, which keeps the file until it is gone
	if spec.RemoveState {
		if err := os.Remove(m.State); err != nil {
			err = errors.Join(fmt.Errorf("remove the state disk: %w", err), stop(v, v.StateChangedNotify()))

			return host.Result(err, stopTerminal(), spec.ConsoleLog)
		}
	}

	// aibox needs nothing else of the machine once the VM runs, as on Linux
	// once QEMU runs. The VM lives in a process of Virtualization.framework,
	// which aibox already reaches.
	if err := b.Confine(spec.Ports); err != nil {
		err = errors.Join(fmt.Errorf("confine aibox: %w", err), stop(v, v.StateChangedNotify()))

		return host.Result(err, stopTerminal(), spec.ConsoleLog)
	}

	vmErr := b.wait(ctx, v, terminal)
	if (errors.Is(vmErr, ErrNoBoot) || errors.Is(vmErr, ErrStartedOver)) && spec.ConsoleLog != "" {
		vmErr = fmt.Errorf("%w, see %s", vmErr, spec.ConsoleLog)
	}

	return host.Result(vmErr, stopTerminal(), spec.ConsoleLog)
}

// startError says why the VM did not start. Virtualization.framework says
// only that the disk is invalid when another run has it locked, so a
// locked disk is named as such. The lock decides nothing here, it explains
// a start that failed already.
func startError(err error, state string) error {
	lock, lockErr := machine.LockState(state)
	if errors.Is(lockErr, machine.ErrStateBusy) {
		return lockErr
	}

	if lockErr == nil {
		_ = lock.Close()
	}

	return fmt.Errorf("start the VM: %w", err)
}

// runningVM is what wait needs of a VM of vz.
type runningVM interface {
	State() vz.VirtualMachineState
	CanStop() bool
	Stop() error
	StateChangedNotify() <-chan vz.VirtualMachineState
}

// wait waits for the VM to stop by itself, and stops it when the context
// ends, the guest does not connect in time or the VM stays up after the
// session.
func (b Backend) wait(ctx context.Context, v runningVM, terminal *watched) error {
	states := v.StateChangedNotify()
	boot := time.NewTimer(b.BootTimeout)

	defer boot.Stop()

	connected, ended := terminal.connected, terminal.ended

	var afterSession <-chan time.Time

	for {
		select {
		case state := <-states:
			switch state { //nolint:exhaustive // the VM goes through the other states on its way
			case vz.VirtualMachineStateStopped:
				return nil
			case vz.VirtualMachineStateError:
				return errors.New("the VM failed")
			}
		case <-connected:
			boot.Stop()

			connected = nil
		case <-ended:
			afterSession = time.After(b.PowerOffWait)
			ended = nil
		case <-afterSession:
			return stop(v, states)
		case <-terminal.again:
			return errors.Join(ErrStartedOver, stop(v, states))
		case <-boot.C:
			return errors.Join(fmt.Errorf("%w in %v", ErrNoBoot, b.BootTimeout), stop(v, states))
		case <-ctx.Done():
			return errors.Join(ctx.Err(), stop(v, states))
		}
	}
}

// stop stops the VM at once and waits for it, for a while. A VM that
// stopped by itself meanwhile is stopped, its state may still be on its way.
func stop(v runningVM, states <-chan vz.VirtualMachineState) error {
	if v.State() == vz.VirtualMachineStateStopped {
		return nil
	}

	if !v.CanStop() {
		return fmt.Errorf("the VM cannot be stopped in the state %v", v.State())
	}

	if err := v.Stop(); err != nil {
		return fmt.Errorf("stop the VM: %w", err)
	}

	timeout := time.After(stopTimeout)

	for {
		select {
		case state := <-states:
			if state == vz.VirtualMachineStateStopped || state == vz.VirtualMachineStateError {
				return nil
			}
		case <-timeout:
			return errors.New("the VM did not stop")
		}
	}
}

// configure describes the machine to Virtualization.framework: the kernel
// with the words of the guest, the root disk read-only and the state disk,
// the shares, the console, vsock and entropy, and no network device.
func configure(m vm.Machine, devNull, console *os.File) (*vz.VirtualMachineConfiguration, error) {
	boot, err := vz.NewLinuxBootLoader(m.Kernel, vz.WithCommandLine(m.Cmdline(baseCmdline)))
	if err != nil {
		return nil, fmt.Errorf("load the kernel %s: %w", m.Kernel, err)
	}

	config, err := vz.NewVirtualMachineConfiguration(boot, uint(m.CPUs), uint64(m.MemoryMiB)<<20) //nolint:gosec // the cli makes both at least 1
	if err != nil {
		return nil, fmt.Errorf("configure the VM: %w", err)
	}

	if err := allowNestedVMs(config); err != nil {
		return nil, err
	}

	if err := addConsole(config, devNull, console); err != nil {
		return nil, err
	}

	if err := addDisks(config, m); err != nil {
		return nil, err
	}

	if err := addShares(config, m.Shares); err != nil {
		return nil, err
	}

	vsock, err := vz.NewVirtioSocketDeviceConfiguration()
	if err != nil {
		return nil, fmt.Errorf("configure vsock: %w", err)
	}

	config.SetSocketDevicesVirtualMachineConfiguration([]vz.SocketDeviceConfiguration{vsock})

	entropy, err := vz.NewVirtioEntropyDeviceConfiguration()
	if err != nil {
		return nil, fmt.Errorf("configure the entropy device: %w", err)
	}

	config.SetEntropyDevicesVirtualMachineConfiguration([]*vz.VirtioEntropyDeviceConfiguration{entropy})

	return config, nil
}

// allowNestedVMs lets the guest run VMs of its own with hardware support,
// as KVM does inside the VM on Linux, where the Mac offers it. Without it
// the guest runs them slower, in software.
func allowNestedVMs(config *vz.VirtualMachineConfiguration) error {
	if !vz.IsNestedVirtualizationSupported() {
		return nil
	}

	platform, err := vz.NewGenericPlatformConfiguration()
	if err != nil {
		return fmt.Errorf("configure the platform: %w", err)
	}

	if err := platform.SetNestedVirtualizationEnabled(true); err != nil {
		return fmt.Errorf("allow nested VMs: %w", err)
	}

	config.SetPlatformVirtualMachineConfiguration(platform)

	return nil
}

func addConsole(config *vz.VirtualMachineConfiguration, devNull, console *os.File) error {
	attachment, err := vz.NewFileHandleSerialPortAttachment(devNull, console)
	if err != nil {
		return fmt.Errorf("attach the console: %w", err)
	}

	port, err := vz.NewVirtioConsoleDeviceSerialPortConfiguration(attachment)
	if err != nil {
		return fmt.Errorf("configure the console: %w", err)
	}

	config.SetSerialPortsVirtualMachineConfiguration([]*vz.VirtioConsoleDeviceSerialPortConfiguration{port})

	return nil
}

// disk is a disk image of the VM and whether the guest may write to it.
type disk struct {
	path     string
	readOnly bool
}

// disksOf are the root disk, read-only, as /dev/vda and the state disk as
// /dev/vdb, the order the guest expects them in.
func disksOf(m vm.Machine) []disk {
	return []disk{{path: m.Rootfs, readOnly: true}, {path: m.State, readOnly: false}}
}

func addDisks(config *vz.VirtualMachineConfiguration, m vm.Machine) error {
	var disks []vz.StorageDeviceConfiguration

	for _, disk := range disksOf(m) {
		attachment, err := vz.NewDiskImageStorageDeviceAttachment(disk.path, disk.readOnly)
		if err != nil {
			return fmt.Errorf("attach the disk %s: %w", disk.path, err)
		}

		device, err := vz.NewVirtioBlockDeviceConfiguration(attachment)
		if err != nil {
			return fmt.Errorf("configure the disk %s: %w", disk.path, err)
		}

		disks = append(disks, device)
	}

	config.SetStorageDevicesVirtualMachineConfiguration(disks)

	return nil
}

// readOnly says whether the guest may only read the share: the guest mounts
// it at a path of its own. Virtualization.framework enforces that on the
// host, as virtiofsd does on Linux.
func readOnly(share vm.Share) bool {
	return share.IsReadOnly()
}

// addShares shares each folder by its tag.
func addShares(config *vz.VirtualMachineConfiguration, shares []vm.Share) error {
	devices := make([]vz.DirectorySharingDeviceConfiguration, 0, len(shares))

	for _, share := range shares {
		dir, err := vz.NewSharedDirectory(share.Dir, readOnly(share))
		if err != nil {
			return fmt.Errorf("share %s: %w", share.Dir, err)
		}

		single, err := vz.NewSingleDirectoryShare(dir)
		if err != nil {
			return fmt.Errorf("share %s: %w", share.Dir, err)
		}

		device, err := vz.NewVirtioFileSystemDeviceConfiguration(share.Tag)
		if err != nil {
			return fmt.Errorf("share %s as %s: %w", share.Dir, share.Tag, err)
		}

		device.SetDirectoryShare(single)
		devices = append(devices, device)
	}

	config.SetDirectorySharingDevicesVirtualMachineConfiguration(devices)

	return nil
}

// pipeConsole returns the end of a pipe for the VM to write its console
// into, and copies what comes out of the pipe to log. finish closes the
// pipe once the VM is gone. The last lines of the console tell most when
// the VM failed, so the copy goes on until the VM let go of the pipe too,
// or for wait at most.
func pipeConsole(log io.Writer, wait time.Duration) (*os.File, func(), error) {
	out, in, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}

	copied := make(chan struct{})

	go func() {
		defer close(copied)

		_, _ = io.Copy(log, out)
	}()

	finish := func() {
		_ = in.Close()

		select {
		case <-copied:
		case <-time.After(wait):
		}

		_ = out.Close()
	}

	return in, finish, nil
}

// consoleLog is where the console of the VM goes. It is a file of its own
// next to the log until the VM started, and only then takes the place of
// the log, since a run that the lock of another refuses must leave the log
// of the running VM alone.
type consoleLog struct {
	*os.File
	path string
	kept bool
}

// openConsole opens the console log at path, or nowhere if path is empty.
func openConsole(path string) (*consoleLog, error) {
	if path == "" {
		nowhere, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
		if err != nil {
			return nil, fmt.Errorf("open the console log: %w", err)
		}

		return &consoleLog{File: nowhere, kept: true}, nil
	}

	// CreateTemp makes the file 0600, the console log is the project's
	file, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return nil, fmt.Errorf("open the console log: %w", err)
	}

	return &consoleLog{File: file, path: path}, nil
}

// keep puts the log in place of the one before, once the VM started. The
// VM goes on writing to it there. The file holds the console of the VM from
// then on, so it stays where it is if it cannot take that place.
func (c *consoleLog) keep() error {
	if c.kept {
		return nil
	}

	c.kept = true

	if err := os.Rename(c.Name(), c.path); err != nil {
		return fmt.Errorf("the console log stays at %s: %w", c.Name(), err)
	}

	return nil
}

// close closes the log, and removes it if it was not kept.
func (c *consoleLog) close() {
	_ = c.Close()

	if !c.kept {
		_ = os.Remove(c.Name())
	}
}
