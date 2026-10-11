//go:build darwin && cgo

package fence

import (
	"path/filepath"

	"github.com/the127/aibox/internal/seatbelt"
)

func apply(dir string) error {
	if dir != "" {
		resolved, err := filepath.EvalSymlinks(dir)
		if err != nil {
			return err
		}

		dir = resolved
	}

	return seatbelt.ApplyProfile(seatbelt.WriteProfile(dir))
}
