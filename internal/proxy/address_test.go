package proxy

import (
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsPublic(t *testing.T) {
	// arrange
	tests := map[string]bool{
		"93.184.216.34":        true,
		"2606:4700::1111":      true,
		"127.0.0.1":            false,
		"127.5.5.5":            false,
		"::1":                  false,
		"10.0.0.5":             false,
		"172.16.0.1":           false,
		"172.31.255.255":       false,
		"192.168.1.1":          false,
		"169.254.1.1":          false,
		"fe80::1":              false,
		"fd00::1":              false,
		"0.0.0.0":              false,
		"0.1.2.3":              false,
		"::":                   false,
		"224.0.0.1":            false,
		"255.255.255.255":      false,
		"100.100.1.1":          false,
		"198.18.0.1":           false,
		"192.0.2.1":            false,
		"::ffff:127.0.0.1":     false,
		"::ffff:10.0.0.1":      false,
		"172.32.0.1":           true,
		"240.0.0.1":            false,
		"::7f00:1":             false,
		"::a00:1":              false,
		"::5db8:d822":          false,
		"64:ff9b::a00:1":       false,
		"64:ff9b::7f00:1":      false,
		"64:ff9b::c0a8:101":    false,
		"64:ff9b::5db8:d822":   true,
		"64:ff9b:1::5db8:d822": false,
		"2002:a00:1::1":        false,
		"2002:c0a8:101::1":     false,
		"2002:5db8:d822::1":    true,
		"2001:0:4136:e378::1":  false,
		"2001:2::1":            false,
		"2001:db8::1":          false,
		"3fff::1":              false,
		"100::1":               false,
		"5f00::1":              false,
	}

	for address, want := range tests {
		t.Run(address, func(t *testing.T) {
			// act
			public := isPublic(net.ParseIP(address))

			// assert
			assert.Equal(t, want, public)
		})
	}
}
