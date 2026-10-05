package apple_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/apple"
)

func machine() apple.Machine {
	return apple.Machine{
		Name:           "aibox-1234",
		Image:          "aibox:1",
		State:          "aibox-state-home-someone-project",
		MemoryMiB:      2048,
		CPUs:           2,
		Project:        "/Users/someone/project",
		Home:           "/Users/someone/.aibox/projects/p/home",
		TerminalSocket: "/tmp/aibox-1234/terminal.sock",
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
		"--cpus", "2",
		"--memory", "2048M",
		"--mount", "type=bind,source=/Users/someone/project,target=/project",
		"--mount", "type=bind,source=/Users/someone/.aibox/projects/p/home,target=/home/user",
		"--volume", "aibox-state-home-someone-project:/var/lib/aibox/state",
		"--publish-socket", "/tmp/aibox-1234/terminal.sock:/run/aibox/terminal.sock",
		"aibox:1",
	}, args)
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
		"--mount", "type=bind,source=/Users/someone/.aibox/projects/p/home,target=/home/user",
		"--mount", "type=bind,source=/sdk/go,target=/opt/go,readonly",
		"--mount", "type=bind,source=/opt/homebrew/bin,target=/opt/bin,readonly",
		"--volume",
	}, args[12:19])
}

func TestRunArgsTellTheGuestToOpenAShell(t *testing.T) {
	// arrange
	m := machine()
	m.Shell = true

	// act
	args, err := m.RunArgs()

	// assert
	require.NoError(t, err)
	assert.Equal(t, []string{"--kernel-arg", "aibox.shell", "aibox:1"}, args[len(args)-3:])
}

func TestRunArgsRefuseAFolderWithACommaInItsPath(t *testing.T) {
	// arrange
	m := machine()
	m.Mounts = []apple.Mount{{Host: "/sdk/go,readonly=false", Guest: "/opt/go"}}

	// act
	_, err := m.RunArgs()

	// assert
	require.ErrorIs(t, err, apple.ErrComma)
	assert.ErrorContains(t, err, "/sdk/go,readonly=false")
}

func TestRunArgsRefuseAProjectWithACommaInItsPath(t *testing.T) {
	// arrange
	m := machine()
	m.Project = "/Users/someone/a,b"

	// act
	_, err := m.RunArgs()

	// assert
	require.ErrorIs(t, err, apple.ErrComma)
}

func TestRunArgsRefuseASocketWithAColonInItsPath(t *testing.T) {
	// arrange
	m := machine()
	m.TerminalSocket = "/tmp/a:b/terminal.sock"

	// act
	_, err := m.RunArgs()

	// assert
	require.ErrorIs(t, err, apple.ErrColon)
}
