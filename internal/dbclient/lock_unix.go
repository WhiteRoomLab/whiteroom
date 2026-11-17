//go:build darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd

package dbclient

import (
	"errors"
	"io/fs"
	"os"
	"syscall"

	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// lockFile takes an exclusive flock(2) lock on the file name, opened to read only, and unlock closes the file. It never
// waits: while another open file holds the lock, it fails with an error that wraps vfs.ErrLocked.
func lockFile(name string) (unlock func() error, err error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			err = vfs.ErrLocked
		}
		return nil, &fs.PathError{Op: "flock", Path: name, Err: err}
	}
	return f.Close, nil
}
