//go:build windows

// Package hcs talks to the Host Compute Service of Windows, which runs VMs
// on Hyper-V, through computecore.dll and nothing else: it creates, starts,
// ends and lists compute systems and grants a VM access to files on the
// host. The service serves Administrators and Hyper-V Administrators only,
// so this is the part of aibox that runs with their rights. It knows
// nothing of aibox.
package hcs

// Plan9Flags are the flags of a Plan 9 share, which say how the service
// serves the folder to the guest.
type Plan9Flags uint32

const (
	// Plan9ReadOnly is a share the guest may only read. The host refuses
	// its writes, whether or not the guest mounted the share read-only.
	Plan9ReadOnly Plan9Flags = 0x1
	// Plan9LinuxMetadata is a share that keeps the owner, the mode and the
	// links of Linux files, which NTFS has no place for, in the metadata
	// of the files.
	Plan9LinuxMetadata Plan9Flags = 0x4
	// Plan9UnlimitedConnections is a share the guest may connect to as
	// often as it needs. Without it the third 9p connection of a VM fails,
	// whichever share it goes to, so every share of a VM with more than
	// two mounts needs it.
	Plan9UnlimitedConnections Plan9Flags = 0x100
)

// AttachmentVirtualDisk is the Type of a SCSI attachment that is a disk
// image, a VHD or VHDX, on the host.
const AttachmentVirtualDisk = "VirtualDisk"

// Document describes a compute system to the service, as the JSON document
// of Create. Its field names are the ones of the schema of the service.
type Document struct {
	// Owner names the program the compute system belongs to. The service
	// lists it with the system, which is how a program finds its own.
	Owner string `json:"Owner"`
	// SchemaVersion is the version of the schema the document follows.
	SchemaVersion SchemaVersion `json:"SchemaVersion"`
	// ShouldTerminateOnLastHandleClosed ends the VM once the last handle
	// to it is closed, which includes the handle of a process that died.
	ShouldTerminateOnLastHandleClosed bool `json:"ShouldTerminateOnLastHandleClosed"`
	// VirtualMachine is the VM the compute system runs.
	VirtualMachine VirtualMachine `json:"VirtualMachine"`
}

// SchemaVersion is a version of the schema of the service. Version 2.1 is
// the one the documents of this package follow.
type SchemaVersion struct {
	Major uint32 `json:"Major"`
	Minor uint32 `json:"Minor"`
}

// VirtualMachine is the VM of a compute system: how it boots, what it has
// and what it is connected to.
type VirtualMachine struct {
	// StopOnReset ends the VM when the guest resets, instead of booting
	// it again.
	StopOnReset bool `json:"StopOnReset"`
	// Chipset is how the VM boots.
	Chipset Chipset `json:"Chipset"`
	// ComputeTopology is the memory and the processors of the VM.
	ComputeTopology ComputeTopology `json:"ComputeTopology"`
	// Devices are the devices of the VM.
	Devices Devices `json:"Devices"`
}

// Chipset is how a VM boots. The only way this package knows is a Linux
// kernel loaded straight from a file on the host.
type Chipset struct {
	LinuxKernelDirect LinuxKernelDirect `json:"LinuxKernelDirect"`
}

// LinuxKernelDirect boots a Linux kernel from a file on the host, without
// firmware, with an initrd if there is one.
type LinuxKernelDirect struct {
	// KernelFilePath is the kernel image on the host.
	KernelFilePath string `json:"KernelFilePath"`
	// InitRdPath is the initrd on the host, if the kernel boots with one.
	InitRdPath string `json:"InitRdPath,omitempty"`
	// KernelCmdLine is the command line of the kernel.
	KernelCmdLine string `json:"KernelCmdLine"`
}

// ComputeTopology is the memory and the processors of a VM.
type ComputeTopology struct {
	Memory    Memory    `json:"Memory"`
	Processor Processor `json:"Processor"`
}

// Memory is the memory of a VM.
type Memory struct {
	// SizeInMB is the size of the memory in MiB.
	SizeInMB uint64 `json:"SizeInMB"`
	// AllowOvercommit lets the host back the memory as the guest uses it,
	// instead of all of it from the start.
	AllowOvercommit bool `json:"AllowOvercommit"`
}

// Processor is the processors of a VM.
type Processor struct {
	Count uint32 `json:"Count"`
}

// Devices are the devices of a VM, each by the name the service knows it
// by. A map is keyed by the number of the device as a string, "0" for the
// first, which is how the schema numbers them.
type Devices struct {
	// ComPorts are the serial ports of the VM, each served as a named pipe
	// on the host.
	ComPorts map[string]ComPort `json:"ComPorts,omitempty"`
	// Scsi are the SCSI controllers of the VM, with their disks.
	Scsi map[string]Scsi `json:"Scsi,omitempty"`
	// HvSocket is the Hyper-V socket of the VM, the vsock of the guest.
	HvSocket *HvSocket `json:"HvSocket,omitempty"`
	// Plan9 are the folders of the host shared with the guest over 9p.
	Plan9 *Plan9 `json:"Plan9,omitempty"`
}

// ComPort is a serial port of a VM, served as a named pipe on the host,
// which is where the console of the guest comes out.
type ComPort struct {
	NamedPipe string `json:"NamedPipe"`
}

// Scsi is a SCSI controller of a VM with its attachments, keyed by their
// number as a string.
type Scsi struct {
	Attachments map[string]Attachment `json:"Attachments,omitempty"`
}

// Attachment is a disk on a SCSI controller.
type Attachment struct {
	// Type is what the disk is, AttachmentVirtualDisk for a disk image.
	Type string `json:"Type"`
	// Path is the disk image on the host.
	Path string `json:"Path"`
	// ReadOnly keeps the guest from writing to the disk.
	ReadOnly bool `json:"ReadOnly,omitempty"`
}

// HvSocket is the Hyper-V socket of a VM, the vsock of the guest, and who
// on the host may use it.
type HvSocket struct {
	HvSocketConfig HvSocketConfig `json:"HvSocketConfig"`
}

// HvSocketConfig says who on the host may listen for the guest and connect
// to it, as security descriptors in SDDL. Without them only SYSTEM and
// Administrators may.
type HvSocketConfig struct {
	// DefaultBindSecurityDescriptor says who may listen on a port of the
	// VM for the guest to connect to.
	DefaultBindSecurityDescriptor string `json:"DefaultBindSecurityDescriptor,omitempty"`
	// DefaultConnectSecurityDescriptor says who may connect to a port the
	// guest listens on.
	DefaultConnectSecurityDescriptor string `json:"DefaultConnectSecurityDescriptor,omitempty"`
}

// Plan9 are the folders of the host shared with the guest over 9p, which
// the guest reaches over its vsock.
type Plan9 struct {
	Shares []Plan9Share `json:"Shares"`
}

// Plan9Share is a folder of the host shared with the guest over 9p.
type Plan9Share struct {
	// Name names the share to the service.
	Name string `json:"Name"`
	// AccessName is the name the guest mounts the share by, the aname of
	// its 9p attach.
	AccessName string `json:"AccessName"`
	// Path is the folder on the host.
	Path string `json:"Path"`
	// Port is the vsock port the guest connects to for the share. The
	// shares of a VM may all use the same port, since the aname tells
	// them apart.
	Port uint32 `json:"Port"`
	// Flags say how the service serves the share.
	Flags Plan9Flags `json:"Flags"`
}
