package unixsock

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// listenWithUmask runs Listen with the umask mask, which it sets back.
func listenWithUmask(t *testing.T, mask int, path string) net.Listener {
	t.Helper()
	old := syscall.Umask(mask)
	defer syscall.Umask(old)
	l, err := Listen(path)
	if err != nil {
		t.Fatalf("Listen(%s) with the umask %#o: %v", path, mask, err)
	}
	return l
}

// TestListenMode checks that the socket file has the mode 0660 whatever the umask, and that closing the listener removes
// it. The umask is that of the process, so no test that runs at the same time may be parallel.
func TestListenMode(t *testing.T) {
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

// TestListenLength checks the longest path that a socket can have, which is 107 bytes on Linux: it listens, and one byte
// more is refused, with the limit and the length, before the path is looked at.
func TestListenLength(t *testing.T) {
	dir := socketDir(t)
	path := filepath.Join(dir, strings.Repeat("x", 107-len(dir)-1))
	if len(path) != 107 || MaxPath != 107 {
		t.Fatalf("the path has %d bytes, and MaxPath is %d, want 107 and 107", len(path), MaxPath)
	}
	l, err := Listen(path)
	if err != nil {
		t.Fatalf("Listen of a path of 107 bytes: %v", err)
	}
	socketMode(t, "a path of 107 bytes", path)
	_ = l.Close()
	noFile(t, "after the listener of 107 bytes is closed", path)

	long := path + "x"
	want := "listen unix " + long + ": the path of a socket has at most 107 bytes, and this one has 108"
	if _, err := Listen(long); err == nil || err.Error() != want {
		t.Errorf("Listen of a path of 108 bytes = %v, want %q", err, want)
	}
	if err := CheckPath(long); err == nil || err.Error() != want {
		t.Errorf("CheckPath of a path of 108 bytes = %v, want %q", err, want)
	}
}

// TestListenStale checks that a socket file that nobody listens on is replaced, and that any other file, and a socket
// that another process listens on, is left as it is.
func TestListenStale(t *testing.T) {
	stale := socketPath(t)
	m, err := net.Listen("unix", stale)
	if err != nil {
		t.Fatal(err)
	}
	m.(*net.UnixListener).SetUnlinkOnClose(false)
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	l, err := Listen(stale)
	if err != nil {
		t.Fatalf("Listen() of a stale socket file = %v", err)
	}
	socketMode(t, "a stale socket file", stale)

	// The same path now: another listener holds it.
	want := "listen unix " + stale + ": another process listens on the socket, which is left as it is"
	if _, err := Listen(stale); err == nil || err.Error() != want {
		t.Errorf("Listen() of a socket that is listened on = %v, want %q", err, want)
	}
	if info, err := os.Lstat(stale); err != nil || info.Mode().Type() != os.ModeSocket {
		t.Errorf("the socket file that is listened on is %v (%v), want it as it was", info, err)
	}
	_ = l.Close()
	noFile(t, "after the listener is closed", stale)

	file := socketPath(t)
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	want = "listen unix " + file + ": the path exists and is not a socket, and is left as it is"
	if _, err := Listen(file); err == nil || err.Error() != want {
		t.Errorf("Listen() of a file = %v, want %q", err, want)
	}
	if data, err := os.ReadFile(file); err != nil || string(data) != "x" {
		t.Errorf("the file after Listen() reads %q, %v; want it unchanged", data, err)
	}

	want = "listen unix @x: a path that starts with @ is of an abstract socket, which has no file: start the path of a " +
		"file with ./"
	if _, err := Listen("@x"); err == nil || err.Error() != want {
		t.Errorf("Listen() of an abstract socket = %v, want %q", err, want)
	}
}

func socketDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if len(dir) > 100 {
		var err error
		if dir, err = os.MkdirTemp("/tmp", "wr"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
	}
	return dir
}

func socketPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(socketDir(t), "s")
}

func socketMode(t *testing.T, what, path string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Errorf("%s: %v", what, err)
		return
	}
	if info.Mode().Type() != os.ModeSocket || info.Mode().Perm() != 0o660 {
		t.Errorf("%s: %s has the mode %v, want a socket with 0660", what, path, info.Mode())
	}
}

func noFile(t *testing.T, what, path string) {
	t.Helper()
	if _, err := os.Lstat(path); err == nil {
		t.Errorf("%s: %s exists", what, path)
	}
}
