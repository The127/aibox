package gitconfig

import (
	"net"
	"os"
	"path/filepath"
	"strings"

	winio "github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// systemKnownHosts are the files of known hosts the OpenSSH of Windows
// reads for everyone: none when Windows can't say where ProgramData is,
// rather than a path relative to the project.
func systemKnownHosts() []string {
	data, err := programData()
	if err != nil {
		return nil
	}

	return []string{filepath.Join(data, "ssh", "ssh_known_hosts")}
}

// programData is the folder of the data of all users, as Windows knows
// it, not as a variable anyone can set says.
func programData() (string, error) {
	return windows.KnownFolderPath(windows.FOLDERID_ProgramData, 0)
}

// agentPipe is the pipe of the agent of the OpenSSH of Windows, which it
// asks when SSH_AUTH_SOCK is not set, as it mostly isn't there.
const agentPipe = pipePrefix + "openssh-ssh-agent"

// pipePrefix starts the name of every pipe.
const pipePrefix = `\\.\pipe\`

// defaultAgent is the agent ssh asks when its config names none.
func defaultAgent() string {
	if socket := os.Getenv("SSH_AUTH_SOCK"); socket != "" {
		return socket
	}

	return agentPipe
}

// dialAgent connects to the agent at the socket: a pipe, whose name
// Windows takes in any case and with either slash, or a Unix socket as
// Windows has them too. A pipe the agent is busy on is waited for as long
// as ssh -G is, not for ever.
func dialAgent(socket string) (net.Conn, error) {
	name := strings.ReplaceAll(socket, "/", `\`)
	if strings.HasPrefix(strings.ToLower(name), pipePrefix) {
		timeout := sshConfigTimeout

		return winio.DialPipe(name, &timeout)
	}

	return net.Dial("unix", socket)
}

// expandProgramData puts the folder of the data of all users into a path
// of the ssh config, which ssh -G of Windows writes as __PROGRAMDATA__. A
// path it can't expand stays as it is, which is no absolute path.
func expandProgramData(value string) string {
	if !strings.Contains(value, "__PROGRAMDATA__") {
		return value
	}

	data, err := programData()
	if err != nil {
		return value
	}

	return strings.ReplaceAll(value, "__PROGRAMDATA__", data)
}
