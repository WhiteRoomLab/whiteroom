package main

import (
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// listenWithUmask runs listenSocket with the umask mask, which it sets back.
func listenWithUmask(t *testing.T, mask int, path string) net.Listener {
	t.Helper()
	old := syscall.Umask(mask)
	defer syscall.Umask(old)
	l, err := listenSocket(path)
	if err != nil {
		t.Fatalf("listenSocket(%s) with the umask %#o: %v", path, mask, err)
	}
	return l
}

// TestListenSocketMode checks that the socket file has the mode 0660 whatever the umask, and that closing the listener
// removes it. The umask is that of the process, so no test that runs at the same time may be parallel.
func TestListenSocketMode(t *testing.T) {
	for _, mask := range []int{0, 0o077, 0o022, 0o117} {
		path := socketPath(t)
		l := listenWithUmask(t, mask, path)
		socketMode(t, fmt.Sprintf("the umask %#o", mask), path)
		if err := l.Close(); err != nil {
			t.Error(err)
		}
		noFile(t, "after the listener is closed", path)
	}
}

// TestListenSocketLength checks the longest path that a socket can have, which is 107 bytes on Linux: it listens, and one
// byte more fails, with the reason.
func TestListenSocketLength(t *testing.T) {
	dir := socketDir(t)
	path := filepath.Join(dir, strings.Repeat("x", 107-len(dir)-1))
	if len(path) != 107 || maxSocketPath != 107 {
		t.Fatalf("the path has %d bytes, and maxSocketPath is %d, want 107 and 107", len(path), maxSocketPath)
	}
	l, err := listenSocket(path)
	if err != nil {
		t.Fatalf("listenSocket of a path of 107 bytes: %v", err)
	}
	socketMode(t, "a path of 107 bytes", path)
	_ = l.Close()
	noFile(t, "after the listener of 107 bytes is closed", path)

	if _, err := listenSocket(path + "x"); err == nil ||
		!strings.HasSuffix(err.Error(), ": bind: invalid argument (the path of a socket has at most 107 bytes, and this one has 108)") {
		t.Errorf("listenSocket of a path of 108 bytes = %v, want the failure of bind, and the limit", err)
	}
}
