package apple_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/apple"
)

func machine() apple.Machine {
	return apple.Machine{
		Name:      "aibox-1234",
		Image:     "aibox:1",
		State:     "aibox-state-home-someone-project",
		MemoryMiB: 2048,
		CPUs:      2,
		Project:   "/Users/someone/project",
		Home:      "/Users/someone/.aibox/projects/p/home",
		Socket:    "/tmp/aibox-1234/link.sock",
	}
}

func TestRunArgs(t *testing.T) {
	// act
	args, err := machine().RunArgs()

	// assert
	require.NoError(t, err)
	assert.Equal(t, []string{
		"run", "--rm", "--name", "aibox-1234",
		"--network", "none",
		"--cap-add", "ALL",
		"--cpus", "2",
		"--memory", "2048M",
		"--mount", "type=bind,source=/Users/someone/project,target=/project",
		"--mount", "type=bind,source=/Users/someone/.aibox/projects/p/home,target=/home/user",
		"--volume", "aibox-state-home-someone-project:/var/lib/aibox/state",
		"--publish-socket", "/tmp/aibox-1234/link.sock:/var/lib/aibox/link.sock",
		"--kernel-arg", "aibox.proxy",
		"--", "aibox:1",
	}, args)
}

// values are the values of every occurrence of the flag, in order.
func values(args []string, flag string) []string {
	var result []string

	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			result = append(result, args[i+1])
		}
	}

	return result
}

func TestRunArgsMountTheFurtherFoldersReadOnlyAfterTheHome(t *testing.T) {
	// arrange
	m := machine()
	m.Mounts = []apple.Mount{{Host: "/sdk/go", Guest: "/opt/go"}, {Host: "/opt/homebrew/bin", Guest: "/opt/bin"}}

	// act
	args, err := m.RunArgs()

	// assert
	require.NoError(t, err)
	assert.Equal(t, []string{
		"type=bind,source=/Users/someone/project,target=/project",
		"type=bind,source=/Users/someone/.aibox/projects/p/home,target=/home/user",
		"type=bind,source=/sdk/go,target=/opt/go,readonly",
		"type=bind,source=/opt/homebrew/bin,target=/opt/bin,readonly",
	}, values(args, "--mount"))
}

func TestRunArgsTellTheGuestToOpenAShell(t *testing.T) {
	// arrange
	m := machine()
	m.Shell = true

	// act
	args, err := m.RunArgs()

	// assert
	require.NoError(t, err)
	assert.Equal(t, []string{"aibox.proxy", "aibox.shell"}, values(args, "--kernel-arg"))
}

func TestRunArgsTellTheGuestOnlyAboutTheProxyWithoutAShell(t *testing.T) {
	// act
	args, err := machine().RunArgs()

	// assert
	require.NoError(t, err)
	assert.Equal(t, []string{"aibox.proxy"}, values(args, "--kernel-arg"))
}

func TestRunArgsRefuseAFolderWithACommaInItsPath(t *testing.T) {
	// arrange
	m := machine()
	m.Mounts = []apple.Mount{{Host: "/sdk/go,readonly=false", Guest: "/opt/go"}}

	// act
	_, err := m.RunArgs()

	// assert
	require.ErrorIs(t, err, apple.ErrMountPath)
	assert.ErrorContains(t, err, "/sdk/go,readonly=false")
}

func TestRunArgsRefuseAFolderWithAnEqualsSignInItsPath(t *testing.T) {
	// arrange
	m := machine()
	m.Mounts = []apple.Mount{{Host: "/sdk/a=b", Guest: "/opt/go"}}

	// act
	_, err := m.RunArgs()

	// assert
	require.ErrorIs(t, err, apple.ErrMountPath)
}

func TestRunArgsRefuseAGuestPathWithAComma(t *testing.T) {
	// arrange
	m := machine()
	m.Mounts = []apple.Mount{{Host: "/sdk/go", Guest: "/opt/go,x"}}

	// act
	_, err := m.RunArgs()

	// assert
	require.ErrorIs(t, err, apple.ErrMountPath)
}

func TestRunArgsRefuseAProjectOrHomeThatCannotBeMounted(t *testing.T) {
	// arrange
	project, home, empty := machine(), machine(), machine()
	project.Project = "/Users/someone/a,b"
	home.Home = "/Users/someone/a=b"
	empty.Home = ""

	// act
	_, projectErr := project.RunArgs()
	_, homeErr := home.RunArgs()
	_, emptyErr := empty.RunArgs()

	// assert
	require.ErrorIs(t, projectErr, apple.ErrMountPath)
	require.ErrorIs(t, homeErr, apple.ErrMountPath)
	require.ErrorIs(t, emptyErr, apple.ErrMountPath)
}

func TestRunArgsRefuseASocketPathWithAColonOrNone(t *testing.T) {
	// arrange
	colon, empty := machine(), machine()
	colon.Socket = "/tmp/a:b/link.sock"
	empty.Socket = ""

	// act
	_, colonErr := colon.RunArgs()
	_, emptyErr := empty.RunArgs()

	// assert
	require.ErrorIs(t, colonErr, apple.ErrSocketPath)
	require.ErrorIs(t, emptyErr, apple.ErrSocketPath)
}

func TestRunArgsRefuseNamesTheToolWouldMisread(t *testing.T) {
	// arrange
	dash, colon, noImage := machine(), machine(), machine()
	dash.Name = "-x"
	colon.State = "state:ro"
	noImage.Image = ""

	// act
	_, dashErr := dash.RunArgs()
	_, colonErr := colon.RunArgs()
	_, noImageErr := noImage.RunArgs()

	// assert
	require.ErrorIs(t, dashErr, apple.ErrName)
	assert.ErrorContains(t, dashErr, "-x")
	require.ErrorIs(t, colonErr, apple.ErrName)
	require.ErrorIs(t, noImageErr, apple.ErrName)
}

func TestRunArgsTakeAnImageWithATag(t *testing.T) {
	// arrange
	m := machine()
	m.Image = "ghcr.io/someone/aibox:1.2"

	// act
	args, err := m.RunArgs()

	// assert
	require.NoError(t, err)
	assert.Equal(t, []string{"--", "ghcr.io/someone/aibox:1.2"}, args[len(args)-2:])
}

func TestRunArgsRefuseAVMWithoutCPUsOrMemory(t *testing.T) {
	// arrange
	cpus, memory := machine(), machine()
	cpus.CPUs = 0
	memory.MemoryMiB = -1

	// act
	_, cpusErr := cpus.RunArgs()
	_, memoryErr := memory.RunArgs()

	// assert
	require.ErrorIs(t, cpusErr, apple.ErrSize)
	require.ErrorIs(t, memoryErr, apple.ErrSize)
}
