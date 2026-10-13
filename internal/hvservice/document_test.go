package hvservice_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/hcs"
	"github.com/the127/aibox/internal/hvservice"
)

var install = hvservice.Install{Kernel: `C:\Program Files\aibox\vmlinuz-hyperv`, Rootfs: `C:\Program Files\aibox\os.vhd`}

const consolePipe = `\\.\pipe\aibox-console-1`

// state is the path the service read from the handle of the client's
// state disk.
const state = `C:\Users\me\.aibox\projects\x\state.ext4`

// documentOf is the document of a VM the host can take, for a client with
// the SIDs.
func documentOf(t *testing.T, sids ...string) hcs.Document {
	t.Helper()

	doc, err := vm(func(*hvservice.Create) {}).Document(host, install, consolePipe, sids)
	require.NoError(t, err)

	return doc
}

func TestDocumentBootsTheKernelOfTheInstallationWithTheClientsCommandLine(t *testing.T) {
	t.Parallel()

	// act
	doc := documentOf(t)

	// assert
	assert.Equal(t, hcs.LinuxKernelDirect{
		KernelFilePath: install.Kernel,
		KernelCmdLine:  "console=ttyS0 aibox.hyperv",
	}, doc.VirtualMachine.Chipset.LinuxKernelDirect)
}

func TestDocumentMakesAVMTheServiceFindsAndThatEndsWithItsLastHandle(t *testing.T) {
	t.Parallel()

	// act
	doc := documentOf(t)

	// assert
	assert.Equal(t, hvservice.Owner, doc.Owner)
	assert.True(t, doc.ShouldTerminateOnLastHandleClosed)
}

func TestDocumentHasOnlyTheRootOfTheInstallationReadOnly(t *testing.T) {
	t.Parallel()

	// act
	doc := documentOf(t)

	// assert: the state disk comes once the VM runs
	assert.Equal(t, map[string]hcs.Scsi{"0": {Attachments: map[string]hcs.Attachment{
		"0": {Type: hcs.AttachmentVirtualDisk, Path: install.Rootfs, ReadOnly: true},
	}}}, doc.VirtualMachine.Devices.Scsi)
	assert.Nil(t, doc.VirtualMachine.Devices.Plan9, "the client serves the folders itself")
}

func TestTheStateDiskComesAtTheLUNTheGuestLooksItUpBy(t *testing.T) {
	t.Parallel()

	// act
	m := hvservice.AddStateDisk(state)

	// assert
	assert.Equal(t, hcs.Modification{
		ResourcePath: "VirtualMachine/Devices/Scsi/0/Attachments/1",
		RequestType:  "Add",
		Settings:     hcs.Attachment{Type: hcs.AttachmentVirtualDisk, Path: state},
	}, m)
}

func TestDocumentLetsTheClientListenOnTheSocketsAndNoOneElse(t *testing.T) {
	t.Parallel()

	// act
	doc := documentOf(t, "S-1-5-21-1-2-3-1001", "S-1-15-2-1-2")

	// assert
	assert.Equal(t, &hcs.HvSocket{HvSocketConfig: hcs.HvSocketConfig{
		DefaultBindSecurityDescriptor:    "D:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;FA;;;S-1-5-21-1-2-3-1001)(A;;FA;;;S-1-15-2-1-2)",
		DefaultConnectSecurityDescriptor: "D:P(A;;FA;;;SY)(A;;FA;;;BA)",
	}}, doc.VirtualMachine.Devices.HvSocket)
}

func TestDocumentWithoutClientSIDsOpensTheSocketsToSystemAndAdministratorsOnly(t *testing.T) {
	t.Parallel()

	// act
	doc := documentOf(t)

	// assert
	assert.Equal(t, "D:P(A;;FA;;;SY)(A;;FA;;;BA)", doc.VirtualMachine.Devices.HvSocket.HvSocketConfig.DefaultBindSecurityDescriptor)
}

func TestDocumentRefusesWhatTheSDDLWouldTakeForMoreThanASID(t *testing.T) {
	t.Parallel()

	for _, sid := range []string{"WD", "AU", "", "S-1-5-21-1)(A;;FA;;;WD", "S-1", "S-1-5-", "s-1-5-18", "S-1-5-18\n"} {
		t.Run(sid, func(t *testing.T) {
			t.Parallel()

			// act
			_, err := vm(func(*hvservice.Create) {}).Document(host, install, consolePipe, []string{sid})

			// assert
			assert.ErrorIs(t, err, hvservice.ErrBadSID)
		})
	}
}

func TestDocumentSendsTheConsoleToThePipe(t *testing.T) {
	t.Parallel()

	// act
	doc := documentOf(t)

	// assert
	assert.Equal(t, map[string]hcs.ComPort{"0": {NamedPipe: consolePipe}}, doc.VirtualMachine.Devices.ComPorts)
}

func TestDocumentSizesTheVMAsAsked(t *testing.T) {
	t.Parallel()

	// act
	doc := documentOf(t)

	// assert
	assert.Equal(t, hcs.ComputeTopology{
		Memory:    hcs.Memory{SizeInMB: 2048, AllowOvercommit: true},
		Processor: hcs.Processor{Count: 2},
	}, doc.VirtualMachine.ComputeTopology)
}

func TestDocumentRefusesAVMThatDoesNotPassCheck(t *testing.T) {
	t.Parallel()

	// act
	_, err := vm(func(c *hvservice.Create) { c.MemoryMiB = -1 }).Document(host, install, consolePipe, nil)

	// assert
	assert.ErrorIs(t, err, hvservice.ErrBadRequest)
}

func TestDocumentKeepsTheClockOfTheVMInUTC(t *testing.T) {
	t.Parallel()

	// act
	doc := documentOf(t)

	// assert: Linux takes the clock for UTC
	assert.True(t, doc.VirtualMachine.Chipset.UseUtc)
}
