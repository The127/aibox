package hvvm

import (
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/Microsoft/go-winio/pkg/guid"

	"github.com/the127/aibox/internal/hvchild"
	"github.com/the127/aibox/internal/proxy"
)

// Config is what the helper serves: the proxy on the port of the VM, with
// the allow list and the file it comes from, which a refusal names, and
// the ports of the loopback of the VM whose services aibox answers, at
// Services, an address on the loopback of the host, which wants the Token
// with each request.
type Config struct {
	VM        guid.GUID `json:"vm"`
	ProxyPort uint32    `json:"proxyPort"`
	AllowList []string  `json:"allowList,omitempty"`
	AllowFile string    `json:"allowFile,omitempty"`
	Local     []uint16  `json:"local,omitempty"`
	Services  string    `json:"services,omitempty"`
	Token     string    `json:"token,omitempty"`
}

// The message aibox sends the helper: its config. aibox ends the helper
// by closing the pipes.
const kindConfig = "config"

// The messages the helper sends aibox: that it serves, or why it can't,
// and what the proxy connected to, refused or closed.
const (
	kindReady  = "ready"
	kindFailed = "failed"
	kindProxy  = "proxy"
)

// The events of the proxy, as proxy.Options calls them.
const (
	proxyConnected = "connected"
	proxyRefused   = "refused"
	proxyClosed    = "closed"
)

type proxyEvent struct {
	Event  string `json:"event"`
	Target string `json:"target"`
}

// The headers of a request the helper hands to aibox: the token that
// tells it from a request of another process of the host, and the port of
// the loopback of the VM whose service answers it.
const (
	tokenHeader = "Aibox-Token"
	portHeader  = "Aibox-Port"
)

// maxHost is the longest name DNS has, longer than any address.
const maxHost = 253

// maxFromHelper is the longest message aibox reads from the helper, which
// a VM may have taken over: an event of the proxy with the longest target,
// a host and a port, each byte of which JSON may write in six. Why the
// helper can't serve it cuts to fit. It is worked out once aibox starts a
// helper, which most runs of aibox never do.
var maxFromHelper = sync.OnceValue(func() uint32 {
	size, err := hvchild.Size(kindProxy, proxyEvent{Event: proxyConnected, Target: strings.Repeat("<", maxHost) + ":65535"})
	if err != nil {
		panic(err)
	}

	return size
})

// fit is the text as much of it as a message of why the helper can't
// serve takes, its end cut off: aibox would get no message, and so no
// reason, rather than part of one.
func fit(text string) string {
	empty, err := hvchild.Size(kindFailed, "")
	if err != nil {
		panic(err)
	}

	// JSON writes no byte longer than \u0000, so a text of the room left,
	// over that, fits whatever its bytes are
	room := int(maxFromHelper()-empty) / len(`\u0000`)
	text = strings.ToValidUTF8(text, "?")

	if len(text) <= room {
		return text
	}

	for room > 0 && !utf8.RuneStart(text[room]) {
		room--
	}

	return text[:room]
}

// isTarget tells whether the target of an event of the proxy is a host and
// port, the longest of which bounds what aibox keeps of each and what a
// message of the helper holds. The helper sends no other, and aibox takes
// no other from a helper a VM may have taken over.
func isTarget(target string) bool {
	return target == proxy.InvalidTarget || proxy.ValidTarget(target)
}
