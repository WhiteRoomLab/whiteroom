//go:build !(darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd)

package vfs

import (
	"fmt"
	"path/filepath"
	"runtime"
)

// Lock fails: logdb locks its files with flock(2), which this system does not have.
func (f *rootFS) Lock(name string, mode LockMode) (unlock func() error, err error) {
	path := filepath.Join(f.r.Name(), name)
	return nil, fmt.Errorf("cannot lock %s: logdb needs flock(2), which %s does not have", path, runtime.GOOS)
}
