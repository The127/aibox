package hvservice

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/the127/aibox/internal/hcs"
)

// Owner is the owner of every VM of the service, which is how the service
// finds the VMs a crash of it left behind.
const Owner = "aibox"

// Install is what the service boots every VM from: files of its own
// installation, never of a client.
type Install struct {
	// Kernel is the kernel for Hyper-V.
	Kernel string
	// Rootfs is the root disk as a VHD.
	Rootfs string
}

// ErrBadSID is a SID that is not one in the S-1-... form, which the
// socket SDDL would take for something else.
var ErrBadSID = errors.New("not a SID in the S-1- form")

// sidPattern refuses the aliases SDDL also takes in place of a SID, such
// as WD for everyone, and anything that would end the ACE. It checks the
// form only: S-1-1-0 is everyone too, so the SIDs must come from the
// client's token.
var sidPattern = regexp.MustCompile(`^S-1-[0-9]+(-[0-9]+)+$`)

// Document describes the VM to the Host Compute Service: booted from the
// installation with the client's kernel command line, its console on the
// pipe, its state disk at the path, and the client let listen on its
// Hyper-V sockets, over which it serves the guest its folders. The state
// path is the only one of the client's in it, and the service reads it
// from the handle the client sent, never from the request. Every word of
// the command line is the client's to choose, as the guest is the
// client's own, and the root disk is read-only. The console pipe is one
// the service named and made itself, never after the request: HCS
// connects to it as the VM. Document refuses a VM that doesn't pass
// Check.
func (c Create) Document(host Host, install Install, consolePipe, state string, clientSIDs []string) (hcs.Document, error) {
	if err := c.Check(host); err != nil {
		return hcs.Document{}, err
	}

	if state == "" {
		return hcs.Document{}, fmt.Errorf("%w: no state disk", ErrBadRequest)
	}

	bind, err := socketSDDL(clientSIDs)
	if err != nil {
		return hcs.Document{}, err
	}

	return hcs.Document{
		Owner:                             Owner,
		SchemaVersion:                     hcs.SchemaVersion{Major: 2, Minor: 1},
		ShouldTerminateOnLastHandleClosed: true,
		VirtualMachine: hcs.VirtualMachine{
			StopOnReset: true,
			Chipset: hcs.Chipset{LinuxKernelDirect: hcs.LinuxKernelDirect{
				KernelFilePath: install.Kernel,
				KernelCmdLine:  c.Cmdline,
			}},
			// the host backs the memory as the guest uses it, so that a VM
			// that waits for a person holds little of the host's memory
			ComputeTopology: hcs.ComputeTopology{
				Memory:    hcs.Memory{SizeInMB: uint64(c.MemoryMiB), AllowOvercommit: true}, //nolint:gosec // Check keeps the memory positive
				Processor: hcs.Processor{Count: uint32(c.CPUs)},                             //nolint:gosec // Check keeps the processors positive
			},
			Devices: hcs.Devices{
				ComPorts: map[string]hcs.ComPort{"0": {NamedPipe: consolePipe}},
				// the guest finds the state disk at LUN 1
				Scsi: map[string]hcs.Scsi{"0": {Attachments: map[string]hcs.Attachment{
					"0": {Type: hcs.AttachmentVirtualDisk, Path: install.Rootfs, ReadOnly: true},
					"1": {Type: hcs.AttachmentVirtualDisk, Path: state},
				}}},
				HvSocket: &hcs.HvSocket{HvSocketConfig: hcs.HvSocketConfig{
					DefaultBindSecurityDescriptor: bind,
					// the guest dials the host, and nothing of the client
					// dials the guest
					DefaultConnectSecurityDescriptor: adminsSDDL,
				}},
			},
		},
	}, nil
}

// adminsSDDL lets SYSTEM and Administrators use a Hyper-V socket of the
// VM, and no one else.
const adminsSDDL = "D:P(A;;FA;;;SY)(A;;FA;;;BA)"

// socketSDDL is adminsSDDL that lets the SIDs use the socket too.
func socketSDDL(sids []string) (string, error) {
	var b strings.Builder

	b.WriteString(adminsSDDL)

	for _, sid := range sids {
		if !sidPattern.MatchString(sid) {
			return "", fmt.Errorf("%w: %q", ErrBadSID, sid)
		}

		b.WriteString("(A;;FA;;;" + sid + ")")
	}

	return b.String(), nil
}
