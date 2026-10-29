package proxy

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"testing"
)

// FuzzReadConnect reads the start of requests the VM could send. When
// readConnect accepts one, the method and the target have no space or
// newline, and the reader stops right after the blank line that ends the
// headers. splitTarget checks the target further.
func FuzzReadConnect(f *testing.F) {
	f.Add([]byte("CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n"))
	f.Add([]byte("CONNECT example.com:443 HTTP/1.1\n\n"))
	f.Add([]byte("GET / HTTP/2\r\n\r\n"))
	f.Add([]byte("CONNECT  HTTP/1.1\r\n\r\n"))
	f.Add([]byte("CONNECT a b HTTP/1.1\r\nX: " + strings.Repeat("y", 5000) + "\r\n\r\n"))

	f.Fuzz(func(t *testing.T, data []byte) {
		reader := bufio.NewReader(bytes.NewReader(data))

		method, target, err := readConnect(reader)
		if err != nil {
			return
		}

		if method == "" || target == "" || strings.ContainsAny(method+target, " \n") {
			t.Fatalf("took the method %q and the target %q", method, target)
		}

		rest, _ := io.ReadAll(reader)
		if !bytes.Equal(rest, afterHeaders(data)) {
			t.Fatalf("left %q of %q", rest, data)
		}
	})
}

// afterHeaders is what follows the first empty line after the request line.
func afterHeaders(data []byte) []byte {
	_, rest, _ := bytes.Cut(data, []byte("\n"))

	for len(rest) > 0 {
		var line []byte

		line, rest, _ = bytes.Cut(rest, []byte("\n"))
		if len(line) == 0 || string(line) == "\r" {
			return rest
		}
	}

	return nil
}

// FuzzSplitTarget splits targets the VM asks for. splitTarget accepts only
// a port from 1 to 65535 written without zeros in front, and a host that
// is an address or a name of letters, digits, hyphens and underscores. No
// target can carry a user, a path or a second host.
func FuzzSplitTarget(f *testing.F) {
	f.Add("example.com:443")
	f.Add("[::1]:3000")
	f.Add("127.0.0.1:0443")
	f.Add("a..b:443")
	f.Add("user@example.com:443")
	f.Add("[example.com]:443")

	f.Fuzz(func(t *testing.T, target string) {
		host, port, valid := splitTarget(target)
		if !valid {
			return
		}

		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
			t.Fatalf("took the port %q of %q", port, target)
		}

		if net.ParseIP(host) != nil {
			return
		}

		if host == "" || len(host) > 253 || strings.ContainsFunc(host, func(r rune) bool { return r != '.' && notInLabel(r) }) {
			t.Fatalf("took the host %q of %q", host, target)
		}

		for label := range strings.SplitSeq(strings.TrimSuffix(host, "."), ".") {
			if label == "" || len(label) > 63 {
				t.Fatalf("took the label %q of %q", label, target)
			}
		}
	})
}

// FuzzIsPublic decides about addresses a name resolves to, or the VM
// names. isPublic accepts none that the net package counts as loopback,
// private, link local, multicast or unspecified, and no IPv6 address that
// holds such an IPv4 one. It gives an IPv4 address the same answer in its
// short and its IPv6 form.
func FuzzIsPublic(f *testing.F) {
	for _, address := range []string{"93.184.216.34", "127.0.0.1", "10.1.2.3", "100.64.0.1", "::1", "fe80::1", "64:ff9b::a01:203", "2002:a01:203::", "2001:db8::1", "2606:4700::1111", "::ffff:192.168.1.1"} {
		f.Add([]byte(net.ParseIP(address)))
	}

	f.Fuzz(func(t *testing.T, raw []byte) {
		ip := net.IP(raw)
		if len(raw) != net.IPv4len && len(raw) != net.IPv6len {
			if isPublic(ip) {
				t.Fatalf("took %d bytes as an address", len(raw))
			}

			return
		}

		if v4 := ip.To4(); v4 != nil && isPublic(v4) != isPublic(v4.To16()) {
			t.Fatalf("%v is public in one form only", ip)
		}

		if !isPublic(ip) {
			return
		}

		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
			t.Fatalf("took %v as public", ip)
		}

		addr, _ := netip.AddrFromSlice(raw)
		if inner, holds := heldIPv4(addr.Unmap()); holds && !isPublic(net.IP(inner.AsSlice())) {
			t.Fatalf("took %v, which holds %v", ip, inner)
		}
	})
}

// FuzzConnectZone asks the proxy to reach hosts the VM names. connect
// refuses an address with a zone before it looks anything up. The
// resolver would hand back the address in it unchecked.
func FuzzConnectZone(f *testing.F) {
	f.Add("fe80::1%eth0")
	f.Add("::1%1")
	f.Add("10.0.0.1%x")

	f.Fuzz(func(t *testing.T, host string) {
		if addr, err := netip.ParseAddr(host); err != nil || addr.Zone() == "" {
			return
		}

		resolve := func(context.Context, string) ([]net.IP, error) {
			t.Fatalf("looked %q up", host)

			return nil, nil
		}

		if _, err := connect(t.Context(), host, "443", Options{Resolve: resolve}); !errors.Is(err, errZone) {
			t.Fatalf("connect of %q gave %v", host, err)
		}
	})
}
