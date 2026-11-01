//go:build !(darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd)

package build

import (
	"fmt"
	"os"
	"runtime"
)

// lockOut fails: builds lock their output directories with flock(2), which this system does not have.
func lockOut(root *os.Root) (unlock func(), err error) {
	return nil, fmt.Errorf("cannot lock %s: builds need flock(2), which %s does not have", root.Name(), runtime.GOOS)
}
