package hostlock

import (
	"errors"
	"io/fs"
	"os"
	"syscall"
)

const supported = true

// ufImmutable is UF_IMMUTABLE from sys/stat.h, the flag chflags uchg sets.
const ufImmutable = 0x2

func flags(info fs.FileInfo) uint32 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return st.Flags
	}
	return 0
}

func locked(info fs.FileInfo) bool { return flags(info)&ufImmutable != 0 }

// setLocked sets or clears the flag on path, keeping its other flags.
// info is path's Lstat. The flag is set through a descriptor that refuses
// a symlink and must be the file info describes, so a file swapped in
// since isn't touched.
func setLocked(path string, info fs.FileInfo, on bool) error {
	fd, err := syscall.Open(path, syscall.O_EVTONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if errors.Is(err, syscall.EACCES) {
		return setLockedByPath(path, info, on)
	}
	if err != nil {
		return &fs.PathError{Op: "open", Path: path, Err: err}
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	now, err := f.Stat()
	if err != nil {
		return err
	}
	if !now.Mode().IsRegular() || !os.SameFile(now, info) {
		return &fs.PathError{Op: "chflags", Path: path, Err: errors.New("replaced since it was checked")}
	}
	flags := flags(now) &^ ufImmutable
	if on {
		flags |= ufImmutable
	}
	if err := syscall.Fchflags(fd, int(flags)); err != nil {
		return &fs.PathError{Op: "chflags", Path: path, Err: err}
	}
	return nil
}

// setLockedByPath is setLocked for a file the user can't open, such as a
// secret with mode 000. chflags(2) follows a symlink, and Go has no
// lchflags without cgo, so path is checked again right before: only a
// host process could swap it in between, since no session runs then.
func setLockedByPath(path string, info fs.FileInfo, on bool) error {
	now, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !now.Mode().IsRegular() || !os.SameFile(now, info) {
		return &fs.PathError{Op: "chflags", Path: path, Err: errors.New("replaced since it was checked")}
	}
	flags := flags(now) &^ ufImmutable
	if on {
		flags |= ufImmutable
	}
	if err := syscall.Chflags(path, int(flags)); err != nil {
		return &fs.PathError{Op: "chflags", Path: path, Err: err}
	}
	return nil
}
