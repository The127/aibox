//go:build !windows

package gitconfig

import (
	"net"
	"os"
)

// systemKnownHosts are the files of known hosts ssh reads for everyone.
func systemKnownHosts() []string { return []string{"/etc/ssh/ssh_known_hosts"} }

// defaultAgent is the agent ssh asks when its config names none.
func defaultAgent() string { return os.Getenv("SSH_AUTH_SOCK") }

// dialAgent connects to the agent at the socket.
func dialAgent(socket string) (net.Conn, error) { return net.Dial("unix", socket) }

// expandProgramData leaves the path as it is: only the ssh of Windows
// writes a placeholder into its paths.
func expandProgramData(value string) string { return value }
