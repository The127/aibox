package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/anthropic"
	"github.com/the127/aibox/internal/backend"
	"github.com/the127/aibox/internal/gitconfig"
	"github.com/the127/aibox/internal/project"
)

type fakeLaunch struct {
	called bool
	spec   backend.Spec
	err    error
	// refuse is a host the fake reports as refused while it runs
	refuse string
	// vm plays the VM, deadline is when the run had to end
	vm       func(ctx context.Context, spec backend.Spec) error
	deadline time.Time
}

func (f *fakeLaunch) Run(ctx context.Context, spec backend.Spec) error {
	f.called = true
	f.spec = spec
	f.deadline, _ = ctx.Deadline()

	if f.refuse != "" {
		spec.Proxy.OnRefused(f.refuse)
	}

	if f.vm != nil {
		return f.vm(ctx, spec)
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
	homeDir  string
	launch   *fakeLaunch
	// version is the version of aibox, fetched the images it downloaded
	version string
	digests map[string]string
	fetched []fetch
	editor  *fakeEditor
	deps    dependencies
	// stdout and stderr are where a task reports
	stdout, stderr bytes.Buffer
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	f := &fixture{cwd: t.TempDir(), aiboxDir: t.TempDir(), homeDir: t.TempDir(), launch: &fakeLaunch{}, editor: &fakeEditor{}}
	f.deps = dependencies{
		edit:            f.editor.edit,
		getwd:           func() (string, error) { return f.cwd, nil },
		aiboxDir:        func() (string, error) { return f.aiboxDir, nil },
		homeDir:         func() (string, error) { return f.homeDir, nil },
		uid:             func() int { return 1234 },
		stdinIsTerminal: func() bool { return true },
		stdin:           strings.NewReader(""),
		lookupEnv:       func(string) (string, bool) { return "", false },
		gitIdentity:     func(string) gitconfig.Identity { return gitconfig.Identity{} },
		backend:         f.launch,
		version:         func() string { return f.version },
		imageDigest: func(arch string) (string, bool) {
			digest, ok := f.digests[arch]

			return digest, ok
		},
		fetchImage: func(_ context.Context, version, arch, digest, dir string) error {
			f.fetched = append(f.fetched, fetch{version: version, arch: arch, digest: digest, dir: dir})
			writeImage(t, dir, "vmlinuz", "os.ext4")

			return nil
		},
	}
	f.deps.stdout, f.deps.stderr = &f.stdout, &f.stderr
	f.version = "(devel)"
	f.digests = map[string]string{runtime.GOARCH: "the digest"}

	return f
}

// fetch is a download of an image.
type fetch struct {
	version, arch, digest, dir string
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
	assert.Equal(t, image, f.launch.spec.Image)
	assert.Equal(t, 1024, f.launch.spec.MemoryMiB)
	assert.Equal(t, 3, f.launch.spec.CPUs)
	assert.False(t, f.launch.spec.Shell)
}

func TestRunWithShell(t *testing.T) {
	// arrange
	f := newFixture(t)
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")

	// act
	err := f.run("--image", image, "--shell")

	// assert
	require.NoError(t, err)
	assert.True(t, f.launch.spec.Shell)
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
	assert.Equal(t, f.cwd, f.launch.spec.Project)
	assert.Equal(t, home, f.launch.spec.Home)
	assert.Empty(t, f.launch.spec.Mounts)
	assert.DirExists(t, home)
}

func TestRunWritesTheConsoleIntoTheProjectFolder(t *testing.T) {
	// arrange
	f := newFixture(t)
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")

	// act
	err := f.run("--image", image)

	// assert
	require.NoError(t, err)
	assert.Equal(t, f.project(t).ConsoleLog, f.launch.spec.ConsoleLog)
}

func TestRunAttachesTheTerminal(t *testing.T) {
	// arrange
	f := newFixture(t)
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")

	// act
	err := f.run("--image", image)

	// assert
	require.NoError(t, err)
	assert.Same(t, os.Stdin, f.launch.spec.Stdin)
	assert.Same(t, os.Stdout, f.launch.spec.Stdout)
	assert.Same(t, os.Stderr, f.launch.spec.Stderr)
}

func TestRunDefaults(t *testing.T) {
	// arrange
	f := newFixture(t)
	image := writeImage(t, filepath.Join(f.aiboxDir, "image"), "vmlinuz", "os.ext4")

	// act
	err := f.run()

	// assert
	require.NoError(t, err)
	assert.Equal(t, image, f.launch.spec.Image)
	assert.Equal(t, 2048, f.launch.spec.MemoryMiB)
	assert.Equal(t, 2, f.launch.spec.CPUs)
}

func TestRunTakesTheSizeFromTheConfigUnlessAFlagIsGiven(t *testing.T) {
	tests := map[string]struct {
		flags        []string
		memory, cpus int
	}{
		"no flags":                   {nil, 4096, 4},
		"memory flag":                {[]string{"--memory", "1024"}, 1024, 4},
		"cpus flag":                  {[]string{"--cpus", "1"}, 4096, 1},
		"memory flag at its default": {[]string{"--memory", "2048"}, 2048, 4},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			// arrange
			f := newFixture(t)
			image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")
			f.writeConfig(t, "memory: 4096\ncpus: 4\n")

			// act
			err := f.run(append([]string{"--image", image}, test.flags...)...)

			// assert
			require.NoError(t, err)
			assert.Equal(t, test.memory, f.launch.spec.MemoryMiB)
			assert.Equal(t, test.cpus, f.launch.spec.CPUs)
		})
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

func TestRunPassesThePortsOfTheAllowListForTheConfinement(t *testing.T) {
	// arrange
	f := newFixture(t)
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")
	f.writeConfig(t, "allow:\n  - example.com\n  - git.example:22\n")

	// act
	err := f.run("--image", image)

	// assert
	require.NoError(t, err)
	assert.Equal(t, []uint16{22, 443}, f.launch.spec.Ports)
}

func TestRunSandboxesQEMUByDefault(t *testing.T) {
	// arrange
	f := newFixture(t)
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")

	// act
	err := f.run("--image", image)

	// assert
	require.NoError(t, err)
	assert.False(t, f.launch.spec.Unsandboxed)
}

func TestRunLeavesTheSandboxOffWhenAsked(t *testing.T) {
	// arrange
	f := newFixture(t)
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")

	// act
	err := f.run("--image", image, "--no-sandbox")

	// assert
	require.NoError(t, err)
	assert.True(t, f.launch.spec.Unsandboxed)
}

func TestRunAsksForAStateDiskOfTheProjectWith16GiB(t *testing.T) {
	// arrange
	f := newFixture(t)
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")

	// act
	err := f.run("--image", image)

	// assert
	require.NoError(t, err)
	assert.Equal(t, f.project(t).State, f.launch.spec.State)
	assert.Equal(t, int64(16<<30), f.launch.spec.StateBytes)
}

func TestRunSizesTheStateDiskFromTheConfig(t *testing.T) {
	// arrange
	f := newFixture(t)
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")
	f.writeConfig(t, "disk: 2\n")

	// act
	err := f.run("--image", image)

	// assert
	require.NoError(t, err)
	assert.Equal(t, int64(2<<30), f.launch.spec.StateBytes)
}

func TestRunRefusesToRunAsRoot(t *testing.T) {
	// arrange
	f := newFixture(t)
	f.deps.uid = func() int { return 0 }
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")

	// act
	err := f.run("--image", image)

	// assert
	require.ErrorIs(t, err, errRoot)
	assert.False(t, f.launch.called)
	assert.NoDirExists(t, filepath.Join(f.aiboxDir, "projects"))
}

func TestRunRefusesToRunInTheRootFolder(t *testing.T) {
	// arrange
	f := newFixture(t)
	f.cwd = "/"

	// act
	err := f.run("--image", writeImage(t, t.TempDir(), "vmlinuz", "os.ext4"))

	// assert
	require.ErrorIs(t, err, errNotAProject)
	assert.ErrorContains(t, err, "root of the file system")
	assert.False(t, f.launch.called)
}

func TestRunRefusesToRunInTheHomeFolder(t *testing.T) {
	// arrange
	f := newFixture(t)
	f.cwd = f.homeDir

	// act
	err := f.run("--image", writeImage(t, t.TempDir(), "vmlinuz", "os.ext4"))

	// assert
	require.ErrorIs(t, err, errNotAProject)
	assert.ErrorContains(t, err, "home directory")
	assert.False(t, f.launch.called)
}

func TestRunRefusesToRunAboveTheHomeFolder(t *testing.T) {
	// arrange
	f := newFixture(t)
	f.homeDir = filepath.Join(f.cwd, "users", "someone")
	require.NoError(t, os.MkdirAll(f.homeDir, 0o700))

	// act
	err := f.run("--image", writeImage(t, t.TempDir(), "vmlinuz", "os.ext4"))

	// assert
	require.ErrorIs(t, err, errNotAProject)
	assert.False(t, f.launch.called)
}

func TestRunRefusesToRunAboveTheAiboxFolder(t *testing.T) {
	// arrange
	f := newFixture(t)
	f.aiboxDir = filepath.Join(f.cwd, "state", ".aibox")
	require.NoError(t, os.MkdirAll(f.aiboxDir, 0o700))

	// act
	err := f.run("--image", writeImage(t, t.TempDir(), "vmlinuz", "os.ext4"))

	// assert
	require.ErrorIs(t, err, errNotAProject)
	assert.ErrorContains(t, err, ".aibox")
	assert.False(t, f.launch.called)
}

func TestRunRefusesToRunInTheHomeFolderBehindASymlink(t *testing.T) {
	// arrange
	f := newFixture(t)
	link := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(f.homeDir, link))
	f.cwd = link

	// act
	err := f.run("--image", writeImage(t, t.TempDir(), "vmlinuz", "os.ext4"))

	// assert
	require.ErrorIs(t, err, errNotAProject)
	assert.False(t, f.launch.called)
}

// otherCase is the folder spelled in upper case, which names the same folder
// only on a file system that does not tell case apart, as that of a Mac.
func otherCase(t *testing.T, dir string) string {
	t.Helper()

	upper := filepath.Join(filepath.Dir(dir), strings.ToUpper(filepath.Base(dir)))
	if _, err := os.Stat(upper); err != nil {
		t.Skip("the file system of the test tells case apart, so the folder has one spelling only")
	}

	return upper
}

func TestRunRefusesToRunInTheHomeFolderSpelledInAnotherCase(t *testing.T) {
	// arrange
	f := newFixture(t)
	f.homeDir = filepath.Join(t.TempDir(), "home")
	require.NoError(t, os.MkdirAll(f.homeDir, 0o700))
	f.cwd = otherCase(t, f.homeDir)

	// act
	err := f.run("--image", writeImage(t, t.TempDir(), "vmlinuz", "os.ext4"))

	// assert
	require.ErrorIs(t, err, errNotAProject)
	assert.False(t, f.launch.called)
}

func TestRunRefusesToRunAboveTheAiboxFolderSpelledInAnotherCase(t *testing.T) {
	// arrange
	f := newFixture(t)
	f.cwd = filepath.Join(t.TempDir(), "state")
	f.aiboxDir = filepath.Join(f.cwd, ".aibox")
	require.NoError(t, os.MkdirAll(f.aiboxDir, 0o700))
	f.cwd = otherCase(t, f.cwd)

	// act
	err := f.run("--image", writeImage(t, t.TempDir(), "vmlinuz", "os.ext4"))

	// assert
	require.ErrorIs(t, err, errNotAProject)
	assert.False(t, f.launch.called)
}

func TestRunRefusesToRunInTheHomeFolderReachedThroughTheDataVolume(t *testing.T) {
	// arrange
	f := newFixture(t)
	home, err := filepath.EvalSymlinks(f.homeDir)
	require.NoError(t, err)

	// macOS reaches the folders of the user through a firmlink to this volume
	throughData := filepath.Join("/System/Volumes/Data", home)
	if _, err := os.Stat(throughData); err != nil {
		t.Skip("there is no data volume of macOS here")
	}

	f.cwd = throughData

	// act
	err = f.run("--image", writeImage(t, t.TempDir(), "vmlinuz", "os.ext4"))

	// assert
	require.ErrorIs(t, err, errNotAProject)
	assert.False(t, f.launch.called)
}

func TestRunRefusesToRunInTheDataVolume(t *testing.T) {
	// arrange
	f := newFixture(t)

	// the volume holds the home, though .. of /Users is /, not the volume
	if _, err := os.Stat("/System/Volumes/Data"); err != nil {
		t.Skip("there is no data volume of macOS here")
	}

	f.cwd = "/System/Volumes/Data"

	// act
	err := f.run("--image", writeImage(t, t.TempDir(), "vmlinuz", "os.ext4"))

	// assert
	require.ErrorIs(t, err, errNotAProject)
	assert.False(t, f.launch.called)
}

func TestRunDownloadsTheImageOfItsReleaseWhenItIsMissing(t *testing.T) {
	// arrange
	f := newFixture(t)
	f.version = "v0.2.0"
	old := writeImage(t, filepath.Join(f.aiboxDir, "image", "v0.1.0"), "vmlinuz", "os.ext4")

	// act
	err := f.run()

	// assert
	require.NoError(t, err)

	dir := filepath.Join(f.aiboxDir, "image", "v0.2.0")
	assert.Equal(t, []fetch{{version: "v0.2.0", arch: runtime.GOARCH, digest: "the digest", dir: dir}}, f.fetched)
	assert.Equal(t, dir, f.launch.spec.Image)
	assert.NoDirExists(t, old, "the image of the release before is gone")
}

func TestRunUsesTheImageOfItsReleaseItDownloadedBefore(t *testing.T) {
	// arrange
	f := newFixture(t)
	f.version = "v0.2.0"
	dir := writeImage(t, filepath.Join(f.aiboxDir, "image", "v0.2.0"), "vmlinuz", "os.ext4")

	// act
	err := f.run()

	// assert
	require.NoError(t, err)
	assert.Empty(t, f.fetched)
	assert.Equal(t, dir, f.launch.spec.Image)
}

func TestRunDownloadsNoImageForABuildFromACheckout(t *testing.T) {
	// arrange
	f := newFixture(t)

	// act
	err := f.run()

	// assert
	require.ErrorContains(t, err, "just install-image")
	assert.Empty(t, f.fetched)
	assert.False(t, f.launch.called)
}

func TestRunDownloadsNoImageWhenOneIsGiven(t *testing.T) {
	// arrange
	f := newFixture(t)
	f.version = "v0.2.0"

	// act
	err := f.run("--image", filepath.Join(t.TempDir(), "missing"))

	// assert
	require.Error(t, err)
	assert.Empty(t, f.fetched)
}

func TestRunSaysWhenTheImageCannotBeDownloaded(t *testing.T) {
	// arrange
	f := newFixture(t)
	f.version = "v0.2.0"
	f.deps.fetchImage = func(context.Context, string, string, string, string) error { return errors.New("no network") }

	// act
	err := f.run()

	// assert
	require.ErrorContains(t, err, "download the VM image of v0.2.0: no network")
	assert.ErrorContains(t, err, "--image")
	assert.False(t, f.launch.called)
}

func TestRunDownloadsNoImageForABuildAtATagWithoutTheDigest(t *testing.T) {
	// arrange
	f := newFixture(t)
	f.version = "v0.2.0"
	f.digests = nil
	dir := writeImage(t, filepath.Join(f.aiboxDir, "image"), "vmlinuz", "os.ext4")

	// act
	err := f.run()

	// assert
	require.NoError(t, err)
	assert.Empty(t, f.fetched)
	assert.Equal(t, dir, f.launch.spec.Image, "the image of just install-image")
}

func TestRunRefusesToRunWithoutATerminal(t *testing.T) {
	// arrange
	f := newFixture(t)
	f.deps.stdinIsTerminal = func() bool { return false }
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")

	// act
	err := f.run("--image", image)

	// assert
	require.ErrorIs(t, err, errNoTerminal)
	assert.False(t, f.launch.called)
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

func TestRunSharesTheMountsOfTheConfigReadOnly(t *testing.T) {
	// arrange
	f := newFixture(t)
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")
	sdk, bin := t.TempDir(), t.TempDir()
	f.writeConfig(t, "mounts:\n  - "+sdk+":/opt/go\n  - "+bin+":/opt/bin\n")

	// act
	err := f.run("--image", image)

	// assert
	require.NoError(t, err)
	assert.Equal(t, []backend.Mount{
		{Host: sdk, Guest: "/opt/go"},
		{Host: bin, Guest: "/opt/bin"},
	}, f.launch.spec.Mounts)
}

func TestRunSendsThePathOfTheConfigAsAVariable(t *testing.T) {
	// arrange
	f := newFixture(t)
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")
	f.writeConfig(t, "path:\n  - /opt/go/bin\n  - /opt/bin\nenv:\n  - GOFLAGS=-mod=mod\n")

	// act
	err := f.run("--image", image)

	// assert
	require.NoError(t, err)
	assert.Equal(t, []string{"GOFLAGS=-mod=mod", "PATH=/opt/go/bin:/opt/bin"}, f.launch.spec.Env)
}

func TestRunKeepsTheAPIKeyOnTheHost(t *testing.T) {
	for _, entry := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_API_KEY=sk-real"} {
		t.Run(entry, func(t *testing.T) {
			// arrange
			f := newFixture(t)
			image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")
			f.writeConfig(t, "allow:\n  - example.com:8443\nenv:\n  - GOFLAGS=-mod=mod\n  - "+entry+"\n")
			f.deps.lookupEnv = func(name string) (string, bool) { return "sk-real", name == "ANTHROPIC_API_KEY" }

			// act
			err := f.run("--image", image)

			// assert
			require.NoError(t, err)

			spec := f.launch.spec
			assert.Equal(t, []string{"GOFLAGS=-mod=mod", "ANTHROPIC_API_KEY=" + anthropic.Placeholder, "ANTHROPIC_BASE_URL=http://127.0.0.1:3129"}, spec.Env)
			assert.Equal(t, []uint16{3129}, spec.Loopback)
			assert.Equal(t, []uint16{8443, 443}, spec.Ports)
			require.NotNil(t, spec.Proxy.Local)
			assert.NotNil(t, spec.Proxy.Local("localhost", "3129"))
			assert.Nil(t, spec.Proxy.Local("localhost", "8080"))
			assert.Nil(t, spec.Proxy.Local("example.com", "3129"))
		})
	}
}

func TestRunPutsThePlaceholderInEveryEntryOfTheAPIKey(t *testing.T) {
	// arrange
	f := newFixture(t)
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")
	f.writeConfig(t, "env:\n  - ANTHROPIC_API_KEY=sk-one\n  - ANTHROPIC_API_KEY=sk-two\n")

	// act
	err := f.run("--image", image)

	// assert
	require.NoError(t, err)
	assert.NotContains(t, strings.Join(f.launch.spec.Env, "\n"), "sk-one")
	assert.NotContains(t, strings.Join(f.launch.spec.Env, "\n"), "sk-two")
	assert.Contains(t, f.launch.spec.Env, "ANTHROPIC_API_KEY="+anthropic.Placeholder)
}

func TestRunPassesATokenOfASubscriptionAsItIs(t *testing.T) {
	// arrange
	f := newFixture(t)
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")
	f.writeConfig(t, "env:\n  - CLAUDE_CODE_OAUTH_TOKEN\n")
	f.deps.lookupEnv = func(string) (string, bool) { return "the-token", true }

	// act
	err := f.run("--image", image)

	// assert
	require.NoError(t, err)
	assert.Equal(t, []string{"CLAUDE_CODE_OAUTH_TOKEN=the-token"}, f.launch.spec.Env)
	assert.Nil(t, f.launch.spec.Proxy.Local)
	assert.Empty(t, f.launch.spec.Loopback)
}

func TestRunRefusesABaseURLNextToTheAPIKey(t *testing.T) {
	// arrange
	f := newFixture(t)
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")
	f.writeConfig(t, "env:\n  - ANTHROPIC_API_KEY=sk-real\n  - ANTHROPIC_BASE_URL=https://gateway.example\n")

	// act
	err := f.run("--image", image)

	// assert
	require.ErrorIs(t, err, errOwnBaseURL)
	assert.False(t, f.launch.called)
}

func TestRunRefusesTheLoopbackPortOfTheForwarder(t *testing.T) {
	// arrange
	f := newFixture(t)
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")
	f.writeConfig(t, "allow:\n  - 127.0.0.1:3129\nenv:\n  - ANTHROPIC_API_KEY=sk-real\n")

	// act
	err := f.run("--image", image)

	// assert
	require.ErrorIs(t, err, errForwarderPort)
	assert.False(t, f.launch.called)
}

func TestRunTakesTheFoldersOfAVariableInThePath(t *testing.T) {
	// arrange
	f := newFixture(t)
	f.deps.lookupEnv = func(name string) (string, bool) {
		if name == "PATH" {
			return "/nix/store/abc-go/bin:/usr/bin:rel/bin::/home/me/bin/", true
		}

		return "", false
	}
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")
	f.writeConfig(t, "path:\n  - /opt/bin\n  - PATH\n")

	// act
	err := f.run("--image", image)

	// assert
	require.NoError(t, err)
	assert.Equal(t, []string{"PATH=/opt/bin:/nix/store/abc-go/bin:/usr/bin:/home/me/bin"}, f.launch.spec.Env)
}

func TestRunWithoutAPathSendsNoPathVariable(t *testing.T) {
	// arrange
	f := newFixture(t)
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")
	f.writeConfig(t, "env:\n  - GOFLAGS=-mod=mod\n")

	// act
	err := f.run("--image", image)

	// assert
	require.NoError(t, err)
	assert.Equal(t, []string{"GOFLAGS=-mod=mod"}, f.launch.spec.Env)
}

func TestRunSendsEachFolderOfThePathOnce(t *testing.T) {
	// arrange
	f := newFixture(t)
	f.deps.lookupEnv = func(string) (string, bool) { return "/usr/bin:/opt/bin:/usr/bin", true }
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")
	f.writeConfig(t, "path:\n  - /opt/bin\n  - PATH\n")

	// act
	err := f.run("--image", image)

	// assert
	require.NoError(t, err)
	assert.Equal(t, []string{"PATH=/opt/bin:/usr/bin"}, f.launch.spec.Env)
}

func TestRunFailsWhenThePathIsTooLongForTheVM(t *testing.T) {
	// arrange
	f := newFixture(t)
	folders := make([]string, 200)
	for i := range folders {
		folders[i] = fmt.Sprintf("/%0100d", i)
	}

	f.deps.lookupEnv = func(string) (string, bool) { return strings.Join(folders, ":"), true }
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")
	f.writeConfig(t, "path:\n  - PATH\n")

	// act
	err := f.run("--image", image)

	// assert
	require.ErrorContains(t, err, "path")
	assert.False(t, f.launch.called)
}

func TestRunFailsWhenAVariableInThePathIsNotSetOnTheHost(t *testing.T) {
	// arrange
	f := newFixture(t)
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")
	f.writeConfig(t, "path:\n  - DIRENV_PATH\n")

	// act
	err := f.run("--image", image)

	// assert
	require.ErrorContains(t, err, "DIRENV_PATH")
	assert.False(t, f.launch.called)
}

func TestRunPassesTheEnvOfTheConfigToTheSession(t *testing.T) {
	// arrange
	f := newFixture(t)
	f.deps.lookupEnv = func(name string) (string, bool) {
		if name == "GITHUB_TOKEN" {
			return "s3cret", true
		}

		return "", false
	}
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")
	f.writeConfig(t, "env:\n  - GOFLAGS=-mod=mod\n  - GITHUB_TOKEN\n")

	// act
	err := f.run("--image", image)

	// assert
	require.NoError(t, err)
	assert.Equal(t, []string{"GOFLAGS=-mod=mod", "GITHUB_TOKEN=s3cret"}, f.launch.spec.Env)
}

func TestRunWhenAVariableToPassThroughIsNotSetOnTheHost(t *testing.T) {
	// arrange
	f := newFixture(t)
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")
	f.writeConfig(t, "env:\n  - GITHUB_TOKEN\n")

	// act
	err := f.run("--image", image)

	// assert
	require.ErrorContains(t, err, "GITHUB_TOKEN")
	assert.False(t, f.launch.called)
}

func (f *fixture) skillsFolder(t *testing.T) string {
	t.Helper()

	skills := filepath.Join(f.homeDir, ".claude", "skills")
	require.NoError(t, os.MkdirAll(skills, 0o700))

	return skills
}

func guests(mounts []backend.Mount) []string {
	result := make([]string, 0, len(mounts))
	for _, mount := range mounts {
		result = append(result, mount.Guest)
	}

	return result
}

func TestRunSharesTheSkillsOfThePersonBeforeTheMounts(t *testing.T) {
	// arrange
	f := newFixture(t)
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")
	skills := f.skillsFolder(t)
	f.writeConfig(t, "mounts:\n  - "+t.TempDir()+":/opt/go\n")

	// act
	err := f.run("--image", image)

	// assert
	require.NoError(t, err)
	assert.Equal(t, []string{"/home/user/.claude/skills", "/opt/go"}, guests(f.launch.spec.Mounts))
	assert.Equal(t, backend.Mount{Host: skills, Guest: "/home/user/.claude/skills"}, f.launch.spec.Mounts[0])
}

func TestRunSharesNoSkillsWhenThePersonHasNone(t *testing.T) {
	// arrange
	f := newFixture(t)
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")

	// act
	err := f.run("--image", image)

	// assert
	require.NoError(t, err)
	assert.Empty(t, f.launch.spec.Mounts)
}

func TestRunSharesNoSkillsWhenTheSkillsAreAFile(t *testing.T) {
	// arrange
	f := newFixture(t)
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")
	require.NoError(t, os.MkdirAll(filepath.Join(f.homeDir, ".claude"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(f.homeDir, ".claude", "skills"), nil, 0o600))

	// act
	err := f.run("--image", image)

	// assert
	require.NoError(t, err)
	assert.Empty(t, f.launch.spec.Mounts)
}

func TestRunLetsAMountOfTheConfigTakeThePlaceOfTheSkills(t *testing.T) {
	for name, guest := range map[string]string{"on the skills": "/home/user/.claude/skills", "above the skills": "/home/user/.claude", "inside the skills": "/home/user/.claude/skills/mine"} {
		t.Run(name, func(t *testing.T) {
			// arrange
			f := newFixture(t)
			image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")
			f.skillsFolder(t)
			f.writeConfig(t, "mounts:\n  - "+t.TempDir()+":"+guest+"\n")

			// act
			err := f.run("--image", image)

			// assert
			require.NoError(t, err)
			assert.Equal(t, []string{guest}, guests(f.launch.spec.Mounts))
		})
	}
}

func TestRunWhenAMountedFolderIsMissing(t *testing.T) {
	// arrange
	f := newFixture(t)
	f.deps.gitIdentity = func(string) gitconfig.Identity {
		return gitconfig.Identity{Name: "Someone", Email: "someone@example.com"}
	}
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")
	missing := filepath.Join(t.TempDir(), "gone")
	f.writeConfig(t, "mounts:\n  - "+missing+":/opt/go\n")

	// act
	err := f.run("--image", image)

	// assert
	require.ErrorContains(t, err, missing)
	assert.False(t, f.launch.called)
	assert.NoFileExists(t, filepath.Join(f.project(t).Home, ".config", "git", "config"))
}

func TestRunWhenAMountedPathIsAFile(t *testing.T) {
	// arrange
	f := newFixture(t)
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")
	file := filepath.Join(t.TempDir(), "tool")
	require.NoError(t, os.WriteFile(file, nil, 0o600))
	f.writeConfig(t, "mounts:\n  - "+file+":/opt/go\n")

	// act
	err := f.run("--image", image)

	// assert
	require.ErrorContains(t, err, "not a folder")
	assert.ErrorContains(t, err, file)
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
	require.NotNil(t, f.launch.spec.Proxy.Allow)
	assert.True(t, f.launch.spec.Proxy.Allow("example.com", "443"))
	assert.False(t, f.launch.spec.Proxy.Allow("example.com", "80"))
	assert.False(t, f.launch.spec.Proxy.Allow("api.anthropic.com", "443"))
	assert.Contains(t, f.launch.spec.Proxy.Hint, f.project(t).Config)
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

func TestRunWritesTheGitIdentityIntoTheHome(t *testing.T) {
	// arrange
	f := newFixture(t)
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")
	f.deps.gitIdentity = func(dir string) gitconfig.Identity {
		assert.Equal(t, f.cwd, dir)

		return gitconfig.Identity{Name: "Some One", Email: "someone@example.com"}
	}

	// act
	err := f.run("--image", image)

	// assert
	require.NoError(t, err)

	file := filepath.Join(f.project(t).Home, ".config", "git", "config")
	name, err := exec.Command("git", "config", "-f", file, "--get", "user.name").Output() //nolint:gosec // the file is the test's own
	require.NoError(t, err)
	assert.Equal(t, "Some One\n", string(name))
}

func TestRunWritesNoGitIdentityWhenTheHostHasNone(t *testing.T) {
	// arrange
	f := newFixture(t)
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")

	// act
	err := f.run("--image", image)

	// assert
	require.NoError(t, err)
	assert.NoFileExists(t, filepath.Join(f.project(t).Home, ".config", "git", "config"))
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
