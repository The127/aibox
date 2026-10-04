package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/gitconfig"
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
	homeDir  string
	launch   *fakeLaunch
	editor   *fakeEditor
	deps     dependencies
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	f := &fixture{cwd: t.TempDir(), aiboxDir: t.TempDir(), homeDir: t.TempDir(), launch: &fakeLaunch{}, editor: &fakeEditor{}}
	f.deps = dependencies{
		edit:            f.editor.edit,
		getwd:           func() (string, error) { return f.cwd, nil },
		aiboxDir:        func() (string, error) { return f.aiboxDir, nil },
		homeDir:         func() (string, error) { return f.homeDir, nil },
		owner:           func() vm.Owner { return vm.Owner{UID: 1234, GID: 100} },
		stdinIsTerminal: func() bool { return true },
		lookupEnv:       func(string) (string, bool) { return "", false },
		gitIdentity:     func(string) gitconfig.Identity { return gitconfig.Identity{} },
		run:             f.launch.run,
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
	assert.Equal(t, &vm.Owner{UID: 1234, GID: 100}, f.launch.machine.Owner)
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
	assert.Equal(t, f.project(t).ConsoleLog, f.launch.options.ConsoleLog)
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
			assert.Equal(t, test.memory, f.launch.machine.MemoryMiB)
			assert.Equal(t, test.cpus, f.launch.machine.CPUs)
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
	assert.Equal(t, []uint16{22, 443}, f.launch.options.Ports)
}

func TestRunSandboxesQEMUByDefault(t *testing.T) {
	// arrange
	f := newFixture(t)
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")

	// act
	err := f.run("--image", image)

	// assert
	require.NoError(t, err)
	assert.False(t, f.launch.options.NoSandbox)
}

func TestRunLeavesTheSandboxOffWhenAsked(t *testing.T) {
	// arrange
	f := newFixture(t)
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")

	// act
	err := f.run("--image", image, "--no-sandbox")

	// assert
	require.NoError(t, err)
	assert.True(t, f.launch.options.NoSandbox)
}

func TestRunCreatesTheStateDiskOfTheProjectWith16GiB(t *testing.T) {
	// arrange
	f := newFixture(t)
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")

	// act
	err := f.run("--image", image)

	// assert
	require.NoError(t, err)
	assert.Equal(t, f.project(t).State, f.launch.machine.State)
	assert.Equal(t, int64(16<<30), f.stateSize(t))
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
	assert.Equal(t, int64(2<<30), f.stateSize(t))
}

// stateSize is the size of the state disk the run created.
func (f *fixture) stateSize(t *testing.T) int64 {
	t.Helper()

	info, err := os.Stat(f.project(t).State)
	require.NoError(t, err)

	return info.Size()
}

func TestRunRefusesToRunAsRoot(t *testing.T) {
	// arrange
	f := newFixture(t)
	f.deps.owner = func() vm.Owner { return vm.Owner{UID: 0, GID: 0} }
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")

	// act
	err := f.run("--image", image)

	// assert
	require.ErrorIs(t, err, errRoot)
	assert.False(t, f.launch.called)
	assert.NoDirExists(t, filepath.Join(f.aiboxDir, "projects"))
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
	assert.Equal(t, []vm.Share{
		{Tag: "project", Dir: f.cwd},
		{Tag: "home", Dir: f.project(t).Home},
		{Tag: "mount0", Dir: sdk, Guest: "/opt/go"},
		{Tag: "mount1", Dir: bin, Guest: "/opt/bin"},
	}, f.launch.machine.Shares)
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
	assert.Equal(t, []string{"GOFLAGS=-mod=mod", "PATH=/opt/go/bin:/opt/bin"}, f.launch.options.Env)
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
	assert.Equal(t, []string{"PATH=/opt/bin:/nix/store/abc-go/bin:/usr/bin:/home/me/bin"}, f.launch.options.Env)
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
	assert.Equal(t, []string{"GOFLAGS=-mod=mod"}, f.launch.options.Env)
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
	assert.Equal(t, []string{"PATH=/opt/bin:/usr/bin"}, f.launch.options.Env)
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
	assert.Equal(t, []string{"GOFLAGS=-mod=mod", "GITHUB_TOKEN=s3cret"}, f.launch.options.Env)
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

func tags(shares []vm.Share) []string {
	result := make([]string, 0, len(shares))
	for _, share := range shares {
		result = append(result, share.Tag)
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
	assert.Equal(t, []string{"project", "home", "skills", "mount0"}, tags(f.launch.machine.Shares))
	assert.Equal(t, vm.Share{Tag: "skills", Dir: skills, Guest: "/home/user/.claude/skills"}, f.launch.machine.Shares[2])
}

func TestRunSharesNoSkillsWhenThePersonHasNone(t *testing.T) {
	// arrange
	f := newFixture(t)
	image := writeImage(t, t.TempDir(), "vmlinuz", "os.ext4")

	// act
	err := f.run("--image", image)

	// assert
	require.NoError(t, err)
	assert.Equal(t, []string{"project", "home"}, tags(f.launch.machine.Shares))
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
	assert.Equal(t, []string{"project", "home"}, tags(f.launch.machine.Shares))
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
			assert.Equal(t, []string{"project", "home", "mount0"}, tags(f.launch.machine.Shares))
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
	require.NotNil(t, f.launch.options.Proxy.Allow)
	assert.True(t, f.launch.options.Proxy.Allow("example.com", "443"))
	assert.False(t, f.launch.options.Proxy.Allow("example.com", "80"))
	assert.False(t, f.launch.options.Proxy.Allow("api.anthropic.com", "443"))
	assert.Contains(t, f.launch.options.Proxy.Hint, f.project(t).Config)
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
