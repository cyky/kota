//go:build !darwin

package hostlock

import (
	"errors"
	"io/fs"
)

// Locks are macOS only; elsewhere Lock locks nothing.
const supported = false

func locked(fs.FileInfo) bool { return false }

func setLocked(string, fs.FileInfo, bool) error { return errors.ErrUnsupported }
