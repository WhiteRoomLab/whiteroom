// Package unixsock listens on Unix domain socket files, as the servers of this program do: the admin server and the
// daemon of the store.
package unixsock

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"strings"
	"syscall"
	"time"
)

// MaxPath is the most bytes of the path of a Unix domain socket: 107 on Linux, whose address has room for 108 with the
// NUL that ends it.
const MaxPath = len(syscall.RawSockaddrUnix{}.Path) - 1

// CheckPath returns the error that Listen gives for a path that it refuses before it looks at the file system: one that
// starts with @, and one that is too long.
func CheckPath(path string) error {
	// Linux takes it for an abstract socket, which has no file: the dial of Listen would miss a file of that name.
	if strings.HasPrefix(path, "@") {
		return fmt.Errorf("listen unix %s: a path that starts with @ is of an abstract socket, which has no file: "+
			"start the path of a file with ./", path)
	}
	if len(path) > MaxPath {
		return fmt.Errorf("listen unix %s: the path of a socket has at most %d bytes, and this one has %d", path, MaxPath,
			len(path))
	}
	return nil
}

// Listen listens on the Unix domain socket file at path, which it makes with the mode 0660: the owner and the group
// connect to it, as a reverse proxy does. A socket file that nobody listens on, as a server that was killed leaves, is
// replaced; any other file there is left as it is, and is an error. Closing the listener removes the file.
func Listen(path string) (net.Listener, error) {
	// Before the path is looked at: a socket file at a path that is too long may be there, and a dial of it fails.
	if err := CheckPath(path); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return nil, err
	case info.Mode()&os.ModeSocket == 0:
		return nil, fmt.Errorf("listen unix %s: the path exists and is not a socket, and is left as it is", path)
	default:
		conn, err := net.DialTimeout("unix", path, time.Second)
		switch {
		case err == nil:
			_ = conn.Close()
			return nil, fmt.Errorf("listen unix %s: another process listens on the socket, which is left as it is", path)
		case !errors.Is(err, syscall.ECONNREFUSED):
			return nil, fmt.Errorf("listen unix %s: cannot tell whether another process listens on the socket, which is left "+
				"as it is: %w", path, err)
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o660); err != nil {
		_ = l.Close()
		return nil, err
	}
	return l, nil
}
