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
	"strings"
	"time"

	"github.com/Code-Hex/vz/v3"

	"github.com/the127/aibox/internal/backend"
	"github.com/the127/aibox/internal/host"
	"github.com/the127/aibox/internal/machine"
	"github.com/the127/aibox/internal/vm"
)

const (
	// the vsock ports of the host. Only the process that owns the VM can
	// reach its vsock, so fixed ports are no one else's.
	terminalPort = 1024
	proxyPort    = 1025

	// baseCmdline boots the root disk read-only, as under QEMU. There is no
	// panic=: a panicking guest would reboot, which Virtualization.framework
	// carries out instead of ending the VM, over and over. A panicked guest
	// hangs instead, and Run stops it.
	baseCmdline = "root=/dev/vda rootfstype=ext4 ro console=hvc0 quiet"

	defaultBootTimeout     = time.Minute
	defaultSessionEndDelay = 3 * time.Second
	defaultPowerOffWait    = 10 * time.Second
	// stopTimeout is how long a VM may take to stop once told to.
	stopTimeout = 10 * time.Second
)

// ErrNoBoot is a VM whose guest did not connect in time.
var ErrNoBoot = errors.New("the VM did not come up")

// Backend runs the VM with Virtualization.framework. BootTimeout is how long
// the guest may take to connect, SessionEndDelay how long the session may go
// on after the VM stopped and PowerOffWait how long the VM may take to power
// off after the session before it is stopped.
type Backend struct {
	BootTimeout     time.Duration
	SessionEndDelay time.Duration
	PowerOffWait    time.Duration
}

var _ backend.Backend = Backend{}

// NewBackend returns the Backend with its default timeouts.
func NewBackend() Backend {
	return Backend{BootTimeout: defaultBootTimeout, SessionEndDelay: defaultSessionEndDelay, PowerOffWait: defaultPowerOffWait}
}

// CheckImage says whether the folder holds the kernel and the root disk.
func (b Backend) CheckImage(dir string) error {
	_, _, err := machine.Image(dir)

	return err
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

	defer func() { _ = console.Close() }()

	// the guest reads nothing from its console, and vz takes only the
	// number of the file, so the file stays open until the VM is gone
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		return err
	}

	defer func() { _ = devNull.Close() }()

	config, err := configure(m, devNull, console)
	if err != nil {
		return err
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
		Env:      spec.Env,
		EndDelay: b.SessionEndDelay,
	})

	// Virtualization.framework locks the state disk as it starts the VM and
	// for as long as it runs, and refuses to start on a disk that is locked,
	// which keeps a second run of the project off
	if err := v.Start(); err != nil {
		return host.Result(startError(err, m.State), stopTerminal(), spec.ConsoleLog)
	}

	vmErr := b.wait(ctx, v, terminal)
	if errors.Is(vmErr, ErrNoBoot) && spec.ConsoleLog != "" {
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
		case <-boot.C:
			if err := stop(v, states); err != nil {
				return err
			}

			return fmt.Errorf("%w in %v", ErrNoBoot, b.BootTimeout)
		case <-ctx.Done():
			if err := stop(v, states); err != nil {
				return err
			}

			return ctx.Err()
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
	cmdline := strings.Join(append([]string{baseCmdline}, m.GuestWords()...), " ")

	boot, err := vz.NewLinuxBootLoader(m.Kernel, vz.WithCommandLine(cmdline))
	if err != nil {
		return nil, fmt.Errorf("load the kernel %s: %w", m.Kernel, err)
	}

	config, err := vz.NewVirtualMachineConfiguration(boot, uint(m.CPUs), uint64(m.MemoryMiB)<<20) //nolint:gosec // the cli makes both at least 1
	if err != nil {
		return nil, fmt.Errorf("configure the VM: %w", err)
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

	if ok, err := config.Validate(); !ok {
		return nil, fmt.Errorf("the configuration of the VM is not valid: %w", err)
	}

	return config, nil
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

// addDisks adds the root disk, read-only, as /dev/vda and the state disk as
// /dev/vdb, the order the guest expects them in.
func addDisks(config *vz.VirtualMachineConfiguration, m vm.Machine) error {
	var disks []vz.StorageDeviceConfiguration

	for _, disk := range []struct {
		path     string
		readOnly bool
	}{{m.Rootfs, true}, {m.State, false}} {
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

// addShares shares each folder by its tag. A share the guest mounts at a
// path of its own is read-only, which Virtualization.framework enforces on
// the host, as virtiofsd does on Linux.
func addShares(config *vz.VirtualMachineConfiguration, shares []vm.Share) error {
	devices := make([]vz.DirectorySharingDeviceConfiguration, 0, len(shares))

	for _, share := range shares {
		dir, err := vz.NewSharedDirectory(share.Dir, share.Guest != "")
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

func openConsole(path string) (*os.File, error) {
	if path == "" {
		path = os.DevNull
	}

	console, err := os.Create(path) //nolint:gosec // the console log of the project
	if err != nil {
		return nil, fmt.Errorf("open the console log: %w", err)
	}

	return console, nil
}
