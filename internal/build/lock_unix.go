//go:build darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd

package build

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// lockOut locks root, an output directory, for a build: it takes an exclusive flock(2) lock on its file .lock, which it
// makes if it is missing, and returns the function that unlocks it. It fails at once if another build holds the lock.
func lockOut(root *os.Root) (unlock func(), err error) {
	f, err := root.OpenFile(lockFile, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, outError(root, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("another build is running in %s", root.Name())
		}
		return nil, outError(root, fmt.Errorf("cannot lock %s: %w", lockFile, err))
	}
	return func() { _ = f.Close() }, nil
}
