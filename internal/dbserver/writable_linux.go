package dbserver

import (
	"io/fs"
	"syscall"
)

const wOK = 2 // W_OK, which package syscall does not have

// writable returns an error if this process cannot write in the directory dir. It makes no file.
func writable(dir string) error {
	if err := syscall.Access(dir, wOK); err != nil {
		return &fs.PathError{Op: "access", Path: dir, Err: err}
	}
	return nil
}
