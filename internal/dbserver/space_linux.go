package dbserver

import (
	"io/fs"
	"math"
	"syscall"
)

// spaceChecked tells that freeSpace works on this system.
const spaceChecked = true

// freeSpace returns what a process that is not the superuser can still use of the file system of dir.
func freeSpace(dir string) (Space, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return Space{}, &fs.PathError{Op: "statfs", Path: dir, Err: err}
	}
	return spaceOf(&st), nil
}

// spaceOf returns the free space that st tells. A file system that does not count its inodes, such as btrfs, tells 0 of
// them, and some tell -1.
func spaceOf(st *syscall.Statfs_t) Space {
	sp := Space{Bytes: st.Bavail * uint64(st.Bsize)}
	if st.Files != 0 && st.Files != math.MaxUint64 && st.Ffree != math.MaxUint64 {
		sp.Inodes, sp.HasInodes = st.Ffree, true
	}
	return sp
}
