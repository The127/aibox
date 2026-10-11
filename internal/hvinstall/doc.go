// Package hvinstall installs and uninstalls the Windows service of aibox,
// as an Administrator does from an elevated prompt. It is apart from the
// service, which runs as SYSTEM, so that the service takes in nothing of
// what only installing it needs.
package hvinstall
