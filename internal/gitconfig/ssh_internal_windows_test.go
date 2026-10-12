package gitconfig

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Windows tells where ProgramData is, whatever the variable says.
func TestExpandProgramDataPutsInTheFolderWindowsKnows(t *testing.T) {
	// arrange
	data, err := programData()
	require.NoError(t, err)
	t.Setenv("ProgramData", "")

	// act
	path := expandProgramData(`__PROGRAMDATA__\ssh/ssh_known_hosts`)

	// assert
	assert.Equal(t, data+`\ssh/ssh_known_hosts`, path)
	assert.Equal(t, []string{filepath.Join(data, "ssh", "ssh_known_hosts")}, systemKnownHosts())
}

func TestTheDefaultAgentIsThePipeOfOpenSSHWithoutSSHAuthSock(t *testing.T) {
	// arrange
	t.Setenv("SSH_AUTH_SOCK", "")

	// act
	socket := defaultAgent()

	// assert
	assert.Equal(t, `\\.\pipe\openssh-ssh-agent`, socket)
}
