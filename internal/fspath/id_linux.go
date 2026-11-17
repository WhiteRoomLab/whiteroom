package fspath

import (
	"io/fs"
	"syscall"
)

// FileID returns the device and inode numbers of the file name, after its symbolic links, as stat(2) tells them. ok is
// false on a system other than Linux, where FileID does not look.
func FileID(name string) (dev, ino uint64, ok bool, err error) {
	var st syscall.Stat_t
	if err := syscall.Stat(name, &st); err != nil {
		return 0, 0, false, &fs.PathError{Op: "stat", Path: name, Err: err}
	}
	return uint64(st.Dev), st.Ino, true, nil //nolint:unconvert // Dev is a uint32 on mips
}
