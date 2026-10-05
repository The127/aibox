//go:build darwin && cgo

// Package seatbelt takes from the aibox process on macOS what it no longer
// needs once the VM runs, as confine does with Landlock on Linux: the file
// system but for the resolver files, TCP but for connecting to the allowed
// ports, and starting programs. It uses the sandbox of macOS, Seatbelt.
package seatbelt

/*
#include <sandbox.h>
#include <stdlib.h>

// sandbox_init is deprecated for apps, which have the App Sandbox, but it
// is how a command line program confines itself
#pragma clang diagnostic ignored "-Wdeprecated-declarations"

static int confine(const char *profile, char **err) {
	return sandbox_init(profile, 0, err);
}
*/
import "C"

import (
	"errors"
	"fmt"
	"strings"
	"unsafe"
)

// Profile is the sandbox of the process: nothing but what the proxy and the
// terminal need once the VM runs. Ports are the TCP ports the proxy may
// connect to. Files aibox opened before keep working.
func Profile(ports []uint16) string {
	var b strings.Builder

	b.WriteString("(version 1)\n(deny default)\n")

	// deny default lets a process read what macOS says of other processes,
	// their arguments and environment among it, for any process of the user
	b.WriteString("(deny process-info*)\n(allow process-info* (target self))\n")

	// what the system libraries look up of files. sysctl stays closed: it
	// hands out the arguments and the environment of every process of the
	// user, and aibox runs without it once the VM runs.
	b.WriteString("(allow file-read-metadata)\n")

	// the name service: the files of the resolver, and the daemon that
	// resolves for the system. resolv.conf links to a file under /var/run,
	// and the sandbox checks where a link leads.
	b.WriteString(`(allow file-read* (literal "/private/etc/hosts") (literal "/private/etc/resolv.conf") (literal "/private/var/run/resolv.conf") (literal "/private/etc/services") (literal "/private/etc/protocols"))` + "\n")
	b.WriteString(`(allow mach-lookup (global-name "com.apple.SystemConfiguration.DNSConfiguration") (global-name "com.apple.mDNSResponder") (global-name "com.apple.dnssd.service"))` + "\n")
	b.WriteString(`(allow network-outbound (remote unix-socket (path-literal "/private/var/run/mDNSResponder")))` + "\n")

	// the terminal of the person, which the session puts into raw mode and
	// asks for its size, under whatever name it has. The sandbox lets
	// aibox open no file, so only the terminal it has open is reached.
	b.WriteString(`(allow file-ioctl (regex #"^/dev/tty"))` + "\n")

	for _, port := range ports {
		fmt.Fprintf(&b, "(allow network-outbound (remote tcp \"*:%d\"))\n", port)
	}

	return b.String()
}

// Apply confines the whole process for good, every thread of it. It must
// run after every child of the process has started, because children
// inherit it.
func Apply(ports []uint16) error {
	profile := C.CString(Profile(ports))
	defer C.free(unsafe.Pointer(profile))

	var reason *C.char

	failed := C.confine(profile, &reason) //nolint:gocritic // the comparison is in the code cgo generates for the call
	if failed != 0 {
		defer C.sandbox_free_error(reason)

		return fmt.Errorf("%w: %s", errSandbox, C.GoString(reason))
	}

	return nil
}

var errSandbox = errors.New("macOS refused the sandbox")
