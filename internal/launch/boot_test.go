package launch_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/launch"
	"github.com/the127/aibox/internal/vm"
)

const virtiofsdPath = "/usr/libexec/virtiofsd"

// console collects what QEMU writes and types a command once the shell
// prompt shows up.
type console struct {
	mu      sync.Mutex
	output  bytes.Buffer
	input   io.Writer
	command string
	typed   bool
}

func (c *console) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.output.Write(p)

	if !c.typed && bytes.HasSuffix(c.output.Bytes(), []byte("# ")) {
		c.typed = true
		_, _ = io.WriteString(c.input, c.command+"\n")
	}

	return len(p), nil
}

func (c *console) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.output.String()
}

func TestRunBootsTheImage(t *testing.T) {
	// arrange
	image := os.Getenv("AIBOX_TEST_IMAGE")
	if image == "" {
		t.Skip("AIBOX_TEST_IMAGE is not set to a folder with vmlinuz and os.ext4")
	}

	if _, err := os.Stat("/dev/kvm"); err != nil {
		t.Skip("no /dev/kvm")
	}

	if _, err := os.Stat(virtiofsdPath); err != nil {
		t.Skip("no " + virtiofsdPath)
	}

	qemu, err := exec.LookPath("qemu-system-x86_64")
	if err != nil {
		t.Skip("no qemu-system-x86_64")
	}

	project := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(project, "hello"), []byte("hello from the host"), 0o600))

	machine := vm.Machine{
		Kernel:    filepath.Join(image, "vmlinuz"),
		Rootfs:    filepath.Join(image, "os.ext4"),
		MemoryMiB: 1024,
		CPUs:      1,
		Shares: []vm.Share{
			{Tag: "project", Dir: project},
			{Tag: "home", Dir: t.TempDir()},
		},
		GuestCID: 42,
	}

	stdin, input, err := os.Pipe()
	require.NoError(t, err)

	defer func() { _ = input.Close() }()

	out := &console{input: input, command: "cat /project/hello; exit"}
	options := launch.Options{QEMU: qemu, Virtiofsd: virtiofsdPath, Stdin: stdin, Stdout: out, Stderr: out}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	// act
	err = launch.Run(ctx, machine, options)

	// assert
	require.NoError(t, err, out.String())
	assert.Contains(t, out.String(), "hello from the host")
}
