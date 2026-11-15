//go:build darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/pprof"
	"strings"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/dbclient"
	"github.com/WhiteRoomLab/whiteroom/internal/dbserver/dbtest"
	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
)

// TestMain gives the tests a temporary directory of their own, and fails the run if one leaves something in it, a directory
// of sockets or a file, or leaves a goroutine of a daemon or of a client of it, which an end to end test does not leave: the
// daemons run inside the process, and nothing else starts.
func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "wc")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.Setenv("TMPDIR", tmp); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	var leaked []string
	for range 200 {
		if leaked = leakedGoroutines(); leaked == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if leaked != nil && code == 0 {
		fmt.Fprintf(os.Stderr, "FAIL: %d goroutines of a daemon or of its client are left:\n%s\n", len(leaked),
			strings.Join(leaked, "\n\n"))
		code = 1
	}
	if entries, err := os.ReadDir(tmp); err == nil && len(entries) > 0 && code == 0 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		fmt.Fprintf(os.Stderr, "FAIL: the tests have left %s in their temporary directory\n", strings.Join(names, ", "))
		code = 1
	}
	_ = os.RemoveAll(tmp)
	os.Exit(code)
}

// leakedGoroutines returns the stacks of the goroutines that are in the code of a daemon or of its client.
func leakedGoroutines() []string {
	var buf strings.Builder
	if err := pprof.Lookup("goroutine").WriteTo(&buf, 2); err != nil {
		return nil
	}
	var leaked []string
	for _, g := range strings.Split(buf.String(), "\n\n") {
		if strings.Contains(g, "internal/dbserver.") || strings.Contains(g, "internal/dbclient.") {
			leaked = append(leaked, g)
		}
	}
	return leaked
}

// dbLimits are the limits that the daemons of the tests run with: no limit of commits per second and no check of the free
// space, where whiteroom-db serve limits the commits to 50 a second and wants 1 GiB free by default. A test must not depend
// on how fast the commands go, or on the disk that its temporary directory is on. TestInitDefaultCommitRate runs a daemon with
// the default commit rate of whiteroom-db.
var dbLimits = dbwire.Limits{CommitsPerSecond: -1, MinFree: -1}

// testDB is a daemon that runs inside the test process, until the test ends: the data directory that it serves, and the
// directory of its sockets, which the commands get as -db. The tests that serve admin give it the data directory, as -data,
// until it connects to whiteroom-db too; the daemon of a data directory is one in the process, so that the server and the
// commands of the test share it.
type testDB struct {
	data    string
	sockets string
}

// shortDir makes a directory with a path short enough for a socket in it, and removes it at the end of the test.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "wr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// newDB makes the data directory of a new store, as whiteroom-db init does, and runs a daemon on it.
func newDB(t *testing.T) testDB {
	t.Helper()
	return newDBWith(t, dbLimits)
}

// newDBWith is newDB, with the daemon running with the limits l, which are those of whiteroom-db where a field is 0.
func newDBWith(t *testing.T, l dbwire.Limits) testDB {
	t.Helper()
	return newDBOptions(t, dbtest.Options{Limits: &l})
}

// newDBOptions is newDB, with the daemon running with the options o.
func newDBOptions(t *testing.T, o dbtest.Options) testDB {
	t.Helper()
	data := filepath.Join(t.TempDir(), "data")
	if err := dbtest.Init(data, o); err != nil {
		t.Fatal(err)
	}
	return testDB{data: data, sockets: listenOn(t, data, o, "")}
}

// newDBAt is newDB, with the sockets in the directory sockets, which it makes: a path from which a config file can find
// them.
func newDBAt(t *testing.T, sockets string) testDB {
	t.Helper()
	data := filepath.Join(t.TempDir(), "data")
	o := dbtest.Options{Limits: &dbLimits}
	if err := dbtest.Init(data, o); err != nil {
		t.Fatal(err)
	}
	return testDB{data: data, sockets: listenOn(t, data, o, sockets)}
}

// dbOn runs a daemon on the data directory data, which exists: a copy of a backup, or the directory of another test, which a
// daemon of the process may serve already.
func dbOn(t *testing.T, data string) testDB {
	t.Helper()
	return testDB{data: data, sockets: listenOn(t, data, dbtest.Options{Limits: &dbLimits}, "")}
}

// listenOn runs the daemon of the data directory data, which the first Acquire opens with o, and makes it listen on the
// sockets in the directory sockets, which it makes, or in a new short one if it is "". It returns the directory. The end of the
// test releases the daemon and removes the directory.
func listenOn(t *testing.T, data string, o dbtest.Options, sockets string) string {
	t.Helper()
	if sockets == "" {
		var err error
		if sockets, err = os.MkdirTemp("", "wr"); err != nil {
			t.Fatal(err)
		}
	} else if err := os.MkdirAll(sockets, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockets) })
	if n := len(filepath.Join(sockets, dbwire.WriterSocket)); n > 100 {
		t.Fatalf("the path of a socket would have %d bytes: %s", n, sockets)
	}
	h, err := dbtest.Acquire(data, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := h.Release(); err != nil {
			t.Errorf("closing the daemon of %s: %v", data, err)
		}
	})
	if err := h.Listen(sockets); err != nil {
		t.Fatal(err)
	}
	return sockets
}

// commitsOf returns the number of the last commit of the daemon of db, 0 for a store that has none, as a reader that
// connects to its sockets sees it.
func commitsOf(t *testing.T, db testDB) uint64 {
	t.Helper()
	c, err := dbclient.Open(t.Context(), dbclient.SocketDir(db.sockets), dbwire.RoleReader, dbclient.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var seq uint64
	err = c.View(t.Context(), func(tx *dbclient.Tx) error {
		seq = tx.Seq()
		return nil
	})
	if err != nil {
		_ = c.Close()
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	return seq
}
