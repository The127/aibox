package cli

import (
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"

	"github.com/the127/aibox/internal/backend"
	"github.com/the127/aibox/internal/config"
	"github.com/the127/aibox/internal/vm"
)

// vmProxyPort is the port of the proxy on the loopback of the VM.
const vmProxyPort = 3128

// errVMPortTaken is a ports entry for a port the VM needs for aibox. From
// this machine, the forwarder and the git broker would hand any program
// here the API key and the git login of the person.
var errVMPortTaken = errors.New("the VM needs this port for aibox")

// errVMPortAllowed is a ports entry for a port of the allow list, which
// leads back to this machine.
var errVMPortAllowed = errors.New("allow names this port of the loopback, which leads back to this machine")

// listenPorts refuses the entries for a port the VM needs or one of the
// allow list, and listens on the loopback of the host for each. Ports are
// refused whether the feature that needs them is on or not, so that a
// config does not stop working once it is.
func listenPorts(ports []config.Port, loopback []uint16) ([]backend.Forward, error) {
	reserved := []uint16{vmProxyPort, forwarderPort, brokerPort, idePort, vm.ClipboardPort}

	for _, port := range ports {
		if slices.Contains(reserved, port.VM) {
			return nil, fmt.Errorf("ports entry for port %d of the VM: %w", port.VM, errVMPortTaken)
		}

		if slices.Contains(loopback, port.VM) {
			return nil, fmt.Errorf("ports entry for port %d of the VM: %w", port.VM, errVMPortAllowed)
		}
	}

	forwards := make([]backend.Forward, 0, len(ports))

	for _, port := range ports {
		listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port.Host))))
		if err != nil {
			closeForwards(forwards)

			return nil, fmt.Errorf("ports entry for port %d of this machine: %w", port.Host, err)
		}

		forwards = append(forwards, backend.Forward{Listener: listener, Port: port.VM})
	}

	return forwards, nil
}

func closeForwards(forwards []backend.Forward) {
	for _, f := range forwards {
		_ = f.Listener.Close()
	}
}
