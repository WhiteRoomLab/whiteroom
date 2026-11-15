package dbserver

import (
	"errors"
	"net"
	"syscall"
)

// peerCreds tells that this system gives the Unix user of a peer of a socket.
const peerCreds = true

// peerUID is the user of the process on the other end of conn, as the kernel has recorded it when the connection was made.
// It is a variable so that the tests can stand in for another user.
var peerUID = func(conn net.Conn) (uint32, error) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, errors.New("the connection is not one of a Unix domain socket")
	}
	rc, err := uc.SyscallConn()
	if err != nil {
		return 0, err
	}
	var cred *syscall.Ucred
	var credErr error
	if err := rc.Control(func(fd uintptr) {
		cred, credErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return 0, err
	}
	if credErr != nil {
		return 0, credErr
	}
	return cred.Uid, nil
}
