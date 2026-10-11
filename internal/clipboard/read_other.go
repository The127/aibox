//go:build !darwin && !linux && !windows

package clipboard

import "errors"

func read() ([]byte, error) { return nil, errors.New("aibox reads no clipboard on this system") }
