package dbserver

import (
	"errors"
	"io/fs"
	"syscall"

	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// pathFlag is O_PATH, which the syscall package has not on every architecture: a directory that is opened with it needs no
// permission to read it, only to search the one below it.
const pathFlag = 0x200000

// maxLevels bounds the walk up, which ends at the root of the file system.
const maxLevels = 4096

// dataLevel returns how many levels above the open directory d the data directory is, whose directory is data: 0 if d is it, 1
// if it holds d, and so on; -1 if none of the directories from d up to the root of the file system is. It looks at the
// directories that the kernel has open, by ".", and not at any name, which can lead elsewhere between one look and the next.
func dataLevel(d vfs.FS, data fs.FileInfo) (int, error) {
	want, ok := data.Sys().(*syscall.Stat_t)
	if !ok {
		return -1, errors.New("the device and the inode of the data directory are not known")
	}
	f, err := d.Open(".")
	if err != nil {
		return -1, err
	}
	defer func() { _ = f.Close() }()
	c, ok := f.(syscall.Conn)
	if !ok {
		return -1, errors.New("the directory is not one that the operating system has open")
	}
	rc, err := c.SyscallConn()
	if err != nil {
		return -1, err
	}
	level, walkErr := -1, error(nil)
	if err := rc.Control(func(fd uintptr) { level, walkErr = walkUp(int(fd), want) }); err != nil {
		return -1, err
	}
	return level, walkErr
}

// walkUp is dataLevel from the open directory fd, which stays the caller's: each directory above it is opened by "..", and
// closed again once the next is.
func walkUp(fd int, want *syscall.Stat_t) (int, error) {
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		return -1, err
	}
	cur, owned := fd, false
	defer func() {
		if owned {
			_ = syscall.Close(cur)
		}
	}()
	for level := 0; level < maxLevels; level++ {
		if st.Dev == want.Dev && st.Ino == want.Ino {
			return level, nil
		}
		up, err := openAbove(cur)
		if err != nil {
			return -1, err
		}
		var above syscall.Stat_t
		if err := syscall.Fstat(up, &above); err != nil {
			_ = syscall.Close(up)
			return -1, err
		}
		if above.Dev == st.Dev && above.Ino == st.Ino {
			_ = syscall.Close(up) // the root of the file system, which is its own parent
			return -1, nil
		}
		if owned {
			_ = syscall.Close(cur)
		}
		cur, owned, st = up, true, above
	}
	return -1, errors.New("too many directories above it")
}

func openAbove(fd int) (int, error) {
	for {
		up, err := syscall.Openat(fd, "..", pathFlag|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		if err != syscall.EINTR {
			return up, err
		}
	}
}
