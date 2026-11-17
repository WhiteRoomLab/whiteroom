//go:build !(darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd)

package dbclient

import (
	"fmt"
	"runtime"
)

// lockFile fails: the lock of the writer is a flock(2) lock, which this system does not have.
func lockFile(name string) (unlock func() error, err error) {
	return nil, fmt.Errorf("cannot lock %s: the lock of the writer needs flock(2), which %s does not have", name, runtime.GOOS)
}
