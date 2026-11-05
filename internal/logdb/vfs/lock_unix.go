//go:build darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd

package vfs

import (
	"errors"
	"io/fs"
	"os"
	"syscall"
)

// Lock takes a flock(2) lock on the file name, opened to read only, and unlock closes the file.
func (f *rootFS) Lock(name string, mode LockMode) (unlock func() error, err error) {
	var how int
	switch mode {
	case Shared:
		how = syscall.LOCK_SH
	case Exclusive:
		how = syscall.LOCK_EX
	default:
		return nil, &fs.PathError{Op: "flock", Path: name, Err: syscall.EINVAL}
	}
	file, err := f.r.OpenFile(name, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	for {
		err = syscall.Flock(int(file.Fd()), how|syscall.LOCK_NB)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			err = ErrLocked
		}
		return nil, &fs.PathError{Op: "flock", Path: name, Err: err}
	}
	return file.Close, nil
}
