package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/launch"
	"github.com/the127/aibox/internal/project"
	"github.com/the127/aibox/internal/vm"
)

type fakeLaunch struct {
	called  bool
	machine vm.Machine
	options launch.Options
	err     error
	// refuse is a host the fake reports as refused while it runs
	refuse string
}

func (f *fakeLaunch) run(_ context.Context, machine vm.Machine, options launch.Options) error {
	f.called = true
	f.machine = machine
	f.options = options

	if f.refuse != "" {
		options.Proxy.OnRefused(f.refuse)
	}

	return f.err
}

func writeImage(t *testing.T, dir string, names ...string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o700))

	for _, name := range names {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), nil, 0o600))
	}

	return dir
}

type fixture struct {
	cwd      string
	aiboxDir string
	launch   *fakeLaunch
	deps     dependencies
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	f := &fixture{cwd: t.TempDir(), aiboxDir: t.TempDir(), launch: &fakeLaunch{}}
	f.deps = dependencies{
		getwd:    func() (string, error) { return f.cwd, nil },
		aiboxDir: func() (string, error) { return f.aiboxDir, nil },
		run:      f.launch.run,
	}

	return f
}

func (f *fixture) run(args ...string) error {
	return newRootCommand(f.deps).Run(context.Background(), append([]string{"aibox", "run"}, args...))
}

// project is the folder aibox keeps for the fixture's project.
func (f *fixture) project(t *testing.T) project.Project {
	t.Helper()

	p, err := project.Open(filepath.Join(f.aiboxDir, "projects"), f.cwd)
	require.NoError(t, err)

	return p
}

func (f *fixture) writeConfig(t *testing.T, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(f.project(t).Config, []byte(content), 0o600))
}

func TestRunPassesTheFlagsToTheMachine(t *testing.T) {
	// arrange
	f := newFixture(t)
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")

	// act
	err := f.run("--image", image, "--memory", "1024", "--cpus", "3")

	// assert
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(image, "vmlinuz"), f.launch.machine.Kernel)
	assert.Equal(t, filepath.Join(image, "os.ext4"), f.launch.machine.Rootfs)
	assert.Equal(t, 1024, f.launch.machine.MemoryMiB)
	assert.Equal(t, 3, f.launch.machine.CPUs)
	assert.False(t, f.launch.machine.Shell)
}

func TestRunWithShell(t *testing.T) {
	// arrange
	f := newFixture(t)
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")

	// act
	err := f.run("--image", image, "--shell")

	// assert
	require.NoError(t, err)
	assert.True(t, f.launch.machine.Shell)
}

func TestRunSharesTheProjectAndItsHome(t *testing.T) {
	// arrange
	f := newFixture(t)
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")
	home := f.project(t).Home

	// act
	err := f.run("--image", image)

	// assert
	require.NoError(t, err)
	assert.Equal(t, []vm.Share{{Tag: "project", Dir: f.cwd}, {Tag: "home", Dir: home}}, f.launch.machine.Shares)
	assert.DirExists(t, home)
}

func TestRunAttachesTheTerminal(t *testing.T) {
	// arrange
	f := newFixture(t)
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")

	// act
	err := f.run("--image", image)

	// assert
	require.NoError(t, err)
	assert.Equal(t, "qemu-system-x86_64", f.launch.options.QEMU)
	assert.Equal(t, "/usr/libexec/virtiofsd", f.launch.options.Virtiofsd)
	assert.Same(t, os.Stdin, f.launch.options.Stdin)
	assert.Same(t, os.Stdout, f.launch.options.Stdout)
	assert.Same(t, os.Stderr, f.launch.options.Stderr)
}

func TestRunDefaults(t *testing.T) {
	// arrange
	f := newFixture(t)
	image := writeImage(t, filepath.Join(f.aiboxDir, "image"), "vmlinuz", "os.ext4")

	// act
	err := f.run()

	// assert
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(image, "vmlinuz"), f.launch.machine.Kernel)
	assert.Equal(t, 2048, f.launch.machine.MemoryMiB)
	assert.Equal(t, 2, f.launch.machine.CPUs)
}

func TestRunPicksADifferentCIDEachTime(t *testing.T) {
	// arrange
	f := newFixture(t)
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")
	cids := map[uint32]bool{}

	// act
	for range 5 {
		require.NoError(t, f.run("--image", image))
		cids[f.launch.machine.GuestCID] = true
	}

	// assert
	assert.Greater(t, len(cids), 1)

	for cid := range cids {
		assert.GreaterOrEqual(t, cid, uint32(3))
	}
}

func TestRunWithoutImage(t *testing.T) {
	// arrange
	f := newFixture(t)
	image := writeImage(t, t.TempDir(), "vmlinuz")

	// act
	err := f.run("--image", image)

	// assert
	require.ErrorContains(t, err, filepath.Join(image, "os.ext4"))
	assert.ErrorContains(t, err, "just install-image")
	assert.False(t, f.launch.called)
	assert.NoDirExists(t, filepath.Join(f.aiboxDir, "projects"))
}

func TestRunRejectsBadFlagValues(t *testing.T) {
	// arrange
	f := newFixture(t)
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")

	for _, args := range [][]string{{"--memory", "0"}, {"--cpus", "0"}, {"--memory", "-5"}} {
		t.Run(args[0]+args[1], func(t *testing.T) {
			// act
			err := f.run(append([]string{"--image", image}, args...)...)

			// assert
			assert.ErrorContains(t, err, args[0])
			assert.False(t, f.launch.called)
		})
	}
}

func TestRunWhenTheProjectFolderCannotBeCreated(t *testing.T) {
	// arrange
	f := newFixture(t)
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")
	require.NoError(t, os.WriteFile(filepath.Join(f.aiboxDir, "projects"), nil, 0o600))

	// act
	err := f.run("--image", image)

	// assert
	assert.ErrorContains(t, err, "project folder")
	assert.False(t, f.launch.called)
}

func TestRunWhenTheCurrentFolderIsUnknown(t *testing.T) {
	// arrange
	f := newFixture(t)
	failure := errors.New("getwd failed")
	f.deps.getwd = func() (string, error) { return "", failure }

	// act
	err := f.run()

	// assert
	assert.ErrorIs(t, err, failure)
	assert.False(t, f.launch.called)
}

func TestRunPassesTheAllowListToTheProxy(t *testing.T) {
	// arrange
	f := newFixture(t)
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")
	f.writeConfig(t, "allow:\n  - example.com\n")

	// act
	err := f.run("--image", image)

	// assert
	require.NoError(t, err)
	require.NotNil(t, f.launch.options.Proxy.Allow)
	assert.True(t, f.launch.options.Proxy.Allow("example.com"))
	assert.False(t, f.launch.options.Proxy.Allow("api.anthropic.com"))
}

func TestRunWritesTheDefaultConfigForANewProject(t *testing.T) {
	// arrange
	f := newFixture(t)
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")

	// act
	err := f.run("--image", image)

	// assert
	require.NoError(t, err)
	assert.FileExists(t, f.project(t).Config)
}

func TestRunLogsRefusedHosts(t *testing.T) {
	// arrange
	f := newFixture(t)
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")
	f.launch.refuse = "evil.example"

	// act
	err := f.run("--image", image)

	// assert
	require.NoError(t, err)

	log, err := os.ReadFile(f.project(t).Log)
	require.NoError(t, err)
	assert.Contains(t, string(log), `refused "evil.example"`)
}

func TestRunWhenTheConfigIsBroken(t *testing.T) {
	// arrange
	f := newFixture(t)
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")
	f.writeConfig(t, "allow: [\n")

	// act
	err := f.run("--image", image)

	// assert
	assert.ErrorContains(t, err, "config.yaml")
	assert.False(t, f.launch.called)
}

func TestRunReturnsTheLaunchError(t *testing.T) {
	// arrange
	f := newFixture(t)
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")
	f.launch.err = errors.New("qemu failed")

	// act
	err := f.run("--image", image)

	// assert
	assert.ErrorIs(t, err, f.launch.err)
}
