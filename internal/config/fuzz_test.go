package config

import (
	"net"
	"strings"
	"testing"
)

// asciiUpper is the text with its ASCII letters upper case.
func asciiUpper(text string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' {
			return r - 'a' + 'A'
		}

		return r
	}, text)
}

// FuzzAllows decides about hosts and ports the VM asks the proxy for. An
// entry allows only its own host, or names below it for *., on its own
// port. The case of ASCII letters and a dot at the end don't matter, and
// localhost passes only for an entry that pins a loopback address. The
// proxy passes on only hosts of ASCII letters, so other letters don't
// count.
func FuzzAllows(f *testing.F) {
	f.Add("example.com", "example.com", "443")
	f.Add("*.example.com", "api.example.com", "443")
	f.Add("*.example.com", "example.com", "443")
	f.Add("*.3.4", "1.2.3.4", "443")
	f.Add("127.0.0.1:3000", "localhost", "3000")
	f.Add("[::1]:8080", "LOCALHOST.", "8080")
	f.Add("Example.COM.:8443", "example.com", "8443")
	f.Add("s.com", "\u017f.com", "443")

	f.Fuzz(func(t *testing.T, text, host, port string) {
		allowed := Hosts{text}.Allows(host, port)

		if allowed != (Hosts{text}.Allows(asciiUpper(host)+".", port)) {
			t.Fatalf("%q allows %q, but not the same in other case or with a dot", text, host)
		}

		if !allowed {
			return
		}

		e, err := parseEntry(text)
		if err != nil {
			t.Fatalf("%q allows %q, though it is no entry", text, host)
		}

		name := normalize(host)

		switch {
		case port != e.port:
			t.Fatalf("%q allows the port %q", text, port)
		case name == localhost:
			if e.loopback() == nil {
				t.Fatalf("%q allows localhost", text)
			}
		case strings.HasPrefix(e.host, "*."):
			if !strings.HasSuffix(name, e.host[1:]) || name == e.host[2:] || net.ParseIP(name) != nil {
				t.Fatalf("%q allows %q", text, name)
			}
		case name != e.host:
			t.Fatalf("%q allows %q", text, name)
		}
	})
}
