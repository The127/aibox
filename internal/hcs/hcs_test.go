package hcs_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/hcs"
)

// document is a VM as aibox would describe one: a kernel booted straight
// from the host with the root disk on SCSI, a console on a pipe, a vsock
// the user may listen on and two shares.
func document() hcs.Document {
	sddl := "D:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;FA;;;S-1-5-21-1-2-3-1001)"

	return hcs.Document{
		Owner:                             "aibox",
		SchemaVersion:                     hcs.SchemaVersion{Major: 2, Minor: 1},
		ShouldTerminateOnLastHandleClosed: true,
		VirtualMachine: hcs.VirtualMachine{
			StopOnReset: true,
			Chipset: hcs.Chipset{LinuxKernelDirect: hcs.LinuxKernelDirect{
				KernelFilePath: `C:\Users\me\.aibox\image\vmlinuz`,
				KernelCmdLine:  "console=ttyS0 root=/dev/sda ro",
			}},
			ComputeTopology: hcs.ComputeTopology{
				Memory:    hcs.Memory{SizeInMB: 2048, AllowOvercommit: true},
				Processor: hcs.Processor{Count: 2},
			},
			Devices: hcs.Devices{
				ComPorts: map[string]hcs.ComPort{"0": {NamedPipe: `\\.\pipe\aibox-console`}},
				Scsi: map[string]hcs.Scsi{"0": {Attachments: map[string]hcs.Attachment{
					"0": {Type: hcs.AttachmentVirtualDisk, Path: `C:\Users\me\.aibox\image\os.vhdx`, ReadOnly: true},
					"1": {Type: hcs.AttachmentVirtualDisk, Path: `C:\Users\me\project\.aibox\state.vhdx`},
				}}},
				HvSocket: &hcs.HvSocket{HvSocketConfig: hcs.HvSocketConfig{
					DefaultBindSecurityDescriptor:    sddl,
					DefaultConnectSecurityDescriptor: sddl,
				}},
				Plan9: &hcs.Plan9{Shares: []hcs.Plan9Share{
					{
						Name: "project", AccessName: "project", Path: `C:\Users\me\project`, Port: 564,
						Flags: hcs.Plan9LinuxMetadata | hcs.Plan9UnlimitedConnections,
					},
					{
						Name: "task", AccessName: "task", Path: `C:\Users\me\project\.aibox\task`, Port: 564,
						Flags: hcs.Plan9ReadOnly | hcs.Plan9LinuxMetadata | hcs.Plan9UnlimitedConnections,
					},
				}},
			},
		},
	}
}

// golden is the document as the service expects it, with its field names.
const golden = `{
  "Owner": "aibox",
  "SchemaVersion": {"Major": 2, "Minor": 1},
  "ShouldTerminateOnLastHandleClosed": true,
  "VirtualMachine": {
    "StopOnReset": true,
    "Chipset": {
      "LinuxKernelDirect": {
        "KernelFilePath": "C:\\Users\\me\\.aibox\\image\\vmlinuz",
        "KernelCmdLine": "console=ttyS0 root=/dev/sda ro"
      }
    },
    "ComputeTopology": {
      "Memory": {"SizeInMB": 2048, "AllowOvercommit": true},
      "Processor": {"Count": 2}
    },
    "Devices": {
      "ComPorts": {"0": {"NamedPipe": "\\\\.\\pipe\\aibox-console"}},
      "Scsi": {
        "0": {
          "Attachments": {
            "0": {"Type": "VirtualDisk", "Path": "C:\\Users\\me\\.aibox\\image\\os.vhdx", "ReadOnly": true},
            "1": {"Type": "VirtualDisk", "Path": "C:\\Users\\me\\project\\.aibox\\state.vhdx"}
          }
        }
      },
      "HvSocket": {
        "HvSocketConfig": {
          "DefaultBindSecurityDescriptor": "D:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;FA;;;S-1-5-21-1-2-3-1001)",
          "DefaultConnectSecurityDescriptor": "D:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;FA;;;S-1-5-21-1-2-3-1001)"
        }
      },
      "Plan9": {
        "Shares": [
          {"Name": "project", "AccessName": "project", "Path": "C:\\Users\\me\\project", "Port": 564, "Flags": 260},
          {"Name": "task", "AccessName": "task", "Path": "C:\\Users\\me\\project\\.aibox\\task", "Port": 564, "Flags": 261}
        ]
      }
    }
  }
}`

func TestDocument(t *testing.T) {
	t.Parallel()

	b, err := json.Marshal(document())
	require.NoError(t, err)

	assert.JSONEq(t, golden, string(b))
}

// TestDocumentInitrd is a VM booted from an initrd with nothing else: the
// devices it has not are left out, not sent empty.
func TestDocumentInitrd(t *testing.T) {
	t.Parallel()

	doc := hcs.Document{
		Owner:         "aibox",
		SchemaVersion: hcs.SchemaVersion{Major: 2, Minor: 1},
		VirtualMachine: hcs.VirtualMachine{
			Chipset: hcs.Chipset{LinuxKernelDirect: hcs.LinuxKernelDirect{
				KernelFilePath: `C:\image\kernel`,
				InitRdPath:     `C:\image\initrd.img`,
				KernelCmdLine:  "rdinit=/init",
			}},
			ComputeTopology: hcs.ComputeTopology{
				Memory:    hcs.Memory{SizeInMB: 512},
				Processor: hcs.Processor{Count: 1},
			},
		},
	}

	b, err := json.Marshal(doc)
	require.NoError(t, err)

	assert.JSONEq(t, `{
	  "Owner": "aibox",
	  "SchemaVersion": {"Major": 2, "Minor": 1},
	  "ShouldTerminateOnLastHandleClosed": false,
	  "VirtualMachine": {
	    "StopOnReset": false,
	    "Chipset": {
	      "LinuxKernelDirect": {
	        "KernelFilePath": "C:\\image\\kernel",
	        "InitRdPath": "C:\\image\\initrd.img",
	        "KernelCmdLine": "rdinit=/init"
	      }
	    },
	    "ComputeTopology": {
	      "Memory": {"SizeInMB": 512, "AllowOvercommit": false},
	      "Processor": {"Count": 1}
	    },
	    "Devices": {}
	  }
	}`, string(b))
}

func TestError(t *testing.T) {
	t.Parallel()

	withResult := &hcs.Error{
		Op:      "create the compute system 1",
		HRESULT: 0xC0370102,
		Result:  `{"Error":-1070137086,"ErrorMessage":"The virtual machine could not be started"}`,
	}
	assert.Equal(t, `create the compute system 1: HRESULT 0xC0370102: {"Error":-1070137086,"ErrorMessage":"The virtual machine could not be started"}`, withResult.Error())

	withoutResult := &hcs.Error{Op: "list the compute systems", HRESULT: 0x8037011B}
	assert.Equal(t, "list the compute systems: HRESULT 0x8037011B", withoutResult.Error())
}

func TestErrAccessDenied(t *testing.T) {
	t.Parallel()

	denied := &hcs.Error{Op: "list the compute systems", HRESULT: 0x8037011B}
	assert.ErrorIs(t, denied, hcs.ErrAccessDenied)
	assert.ErrorIs(t, fmt.Errorf("start: %w", denied), hcs.ErrAccessDenied)

	other := &hcs.Error{Op: "list the compute systems", HRESULT: 0xC0370102}
	assert.NotErrorIs(t, other, hcs.ErrAccessDenied)
	assert.NotErrorIs(t, errors.New("list the compute systems"), hcs.ErrAccessDenied)

	// the error is still an *Error, to read the HRESULT from
	var e *hcs.Error

	require.ErrorAs(t, fmt.Errorf("start: %w", denied), &e)
	assert.Equal(t, uint32(0x8037011B), e.HRESULT)
}
