//go:build !windows

package session

// altVPastes takes Alt+V for Ctrl+V, which only Windows needs.
const altVPastes = false
