//go:build !linux

package dbserver

import "errors"

// spaceChecked tells that freeSpace works on this system.
const spaceChecked = false

func freeSpace(string) (uint64, error) {
	return 0, errors.New("the free space is not checked on this system")
}
