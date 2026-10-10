package cli

import "golang.org/x/sys/windows"

// errSymlinkPrivilege is what Windows says when the user may not make a
// link.
var errSymlinkPrivilege = windows.ERROR_PRIVILEGE_NOT_HELD
