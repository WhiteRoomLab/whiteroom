package dbserver

import (
	"io/fs"
	"syscall"
)

// spaceChecked tells that freeSpace works on this system.
const spaceChecked = true

// freeSpace returns the bytes that a process that is not the superuser can still write in the file system of dir.
func freeSpace(dir string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, &fs.PathError{Op: "statfs", Path: dir, Err: err}
	}
	return st.Bavail * uint64(st.Bsize), nil
}
