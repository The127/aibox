//go:build !windows

package cli

import "errors"

// errSymlinkPrivilege is no error on Unix, where anyone makes a link.
var errSymlinkPrivilege = errors.New("no privilege is needed")
