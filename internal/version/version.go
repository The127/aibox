// Package version reports the version of the aibox binary.
package version

import "runtime/debug"

// version can be set at build time with
// -ldflags "-X github.com/the127/aibox/internal/version.version=v1.2.3".
// If it is empty, the version Go stamps into the binary from the git tag is used.
var version string

// Get returns the version of the running binary.
func Get() string {
	if version != "" {
		return version
	}

	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}

	return "(devel)"
}
