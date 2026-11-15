//go:build !linux

package dbserver

import (
	"errors"
	"net"
)

// peerCreds tells that this system gives the Unix user of a peer of a socket.
const peerCreds = false

var peerUID = func(net.Conn) (uint32, error) {
	return 0, errors.New("the user of a peer is not known on this system")
}
