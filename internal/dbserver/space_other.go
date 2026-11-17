//go:build !linux

package dbserver

import "errors"

// spaceChecked tells that freeSpace works on this system.
const spaceChecked = false

func freeSpace(string) (Space, error) {
	return Space{}, errors.New("the free space is not checked on this system")
}
