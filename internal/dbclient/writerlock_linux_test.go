package dbclient_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/dbclient"
	"github.com/WhiteRoomLab/whiteroom/internal/dbserver/dbtest"
	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// TestWriterLock: a client of the writer holds the lock of the writer of a SocketDir while it is open, and the daemon
// keeps its file, so that the writer is one process also while it has no connection, as after a restart of the daemon,
// when another writer could connect in its place before its next write (#126).
func TestWriterLock(t *testing.T) {
	openWriter := func(dir string, wait time.Duration) (*dbclient.Client, error) {
		return dbclient.Open(bg, dbclient.SocketDir(dir), dbwire.RoleWriter, dbclient.Options{DialWait: wait})
	}
	wantLocked := func(t *testing.T, err error) {
		t.Helper()
		if err == nil || !strings.HasSuffix(err.Error(), ": another process is connected as the writer") ||
			!errors.Is(err, vfs.ErrLocked) || errors.Is(err, dbclient.ErrUnavailable) {
			t.Errorf("a second writer: %v, want the refusal that says that another process is the writer", err)
		}
	}

	t.Run("a second writer", func(t *testing.T) {
		f := newFixture(t, nil)
		dir := socketDir(t)
		must(t, f.root.Listen(dir))
		a, err := openWriter(dir, 0)
		must(t, err)
		start := time.Now()
		_, err = openWriter(dir, 200*time.Millisecond)
		wantLocked(t, err)
		if d := time.Since(start); d < 200*time.Millisecond {
			t.Errorf("the second writer gave up after %v, want it to try for its DialWait", d)
		}
		// A reader takes no lock.
		r, err := dbclient.Open(bg, dbclient.SocketDir(dir), dbwire.RoleReader, dbclient.Options{})
		must(t, err)
		must(t, r.Close())
		must(t, a.Close())
		// The daemon ends the session of a after its connection: the next writer waits for it.
		b, err := openWriter(dir, 0)
		must(t, err)
		must(t, b.Close())
	})

	t.Run("across a restart of the daemon", func(t *testing.T) {
		f := newFixture(t, nil)
		dir := socketDir(t)
		must(t, f.root.Listen(dir))
		a, err := openWriter(dir, 5*time.Second)
		must(t, err)
		t.Cleanup(func() { _ = a.Close() })
		seed(t, a)
		lock, err := os.Stat(filepath.Join(dir, dbwire.WriterLock))
		must(t, err)

		// The daemon stops, which ends the connection of a, and starts again: no process has its writer connection.
		must(t, f.root.Release())
		h, err := dbtest.Acquire("data", f.opts)
		must(t, err)
		t.Cleanup(func() { _ = h.Release() })
		must(t, h.Listen(dir))
		if again, err := os.Stat(filepath.Join(dir, dbwire.WriterLock)); err != nil || !os.SameFile(lock, again) {
			t.Fatalf("the lock of the writer after the restart: %v, want the same file", err)
		}
		_, err = openWriter(dir, 100*time.Millisecond)
		wantLocked(t, err)
		// a connects at its next write.
		commit(t, a, func(tx *dbclient.Tx) { put(t, tx, 2, "after", "1") })
		wantRows(t, rows(t, a, 2, ""), "after=1")
	})

	t.Run("the last connection", func(t *testing.T) {
		// A writer that takes no lock, over net.Pipe, has the connection: to the holder of the lock, it is the last connection of
		// the writer, its own or that of a writer that has just ended, whose session the daemon has not ended, as while a Commit
		// waits for a Verify.
		f := newFixture(t, nil)
		dir := socketDir(t)
		must(t, f.root.Listen(dir))
		f.open(dbwire.RoleWriter, dbclient.Options{})
		_, err := openWriter(dir, 100*time.Millisecond)
		const want = ": cannot reach the daemon: the last connection of the writer has not ended yet"
		if err == nil || !strings.HasSuffix(err.Error(), want) || !errors.Is(err, dbclient.ErrUnavailable) {
			t.Errorf("a writer that holds the lock: %v, want an error that ends with %q", err, want)
		}
		if re, ok := errors.AsType[*dbwire.RemoteError](err); !ok || re.Code != dbwire.CodeBusy {
			t.Errorf("the error %v does not wrap the refusal of the daemon", err)
		}
		// It does not keep the lock.
		must(t, f.root.Release())
		if unlock, err := dbclient.SocketDir(dir).LockWriter(); err != nil {
			t.Errorf("the lock after the failed Open: %v", err)
		} else {
			must(t, unlock())
		}
	})

	t.Run("a directory that is not readable", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads every directory")
		}
		f := newFixture(t, nil)
		dir := socketDir(t)
		must(t, f.root.Listen(dir))
		must(t, os.Chmod(dir, 0o100))
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		a, err := openWriter(dir, 0)
		must(t, err)
		must(t, a.Close())
	})

	t.Run("no lock", func(t *testing.T) {
		// The daemon makes the file of the lock when it starts: until then, there is no daemon to reach either, and the
		// error says why, as without a lock.
		dir := socketDir(t)
		_, err := openWriter(dir, 100*time.Millisecond)
		want := dir + ": cannot reach the daemon: dial unix " + filepath.Join(dir, dbwire.WriterSocket) + ": connect: no such file or directory"
		if err == nil || err.Error() != want || !errors.Is(err, dbclient.ErrUnavailable) || errors.Is(err, fs.ErrNotExist) {
			t.Errorf("a writer without a daemon: %v, want %q", err, want)
		}
		// A daemon without the file, as one of another program: the error is that of the lock, at once, as such a daemon
		// does not make it later, and the place of the writer is free again.
		f := newFixture(t, nil)
		must(t, f.root.Listen(dir))
		must(t, os.Remove(filepath.Join(dir, dbwire.WriterLock)))
		start := time.Now()
		_, err = openWriter(dir, 5*time.Second)
		want = dir + ": cannot reach the daemon: open " + filepath.Join(dir, dbwire.WriterLock) + ": no such file or directory"
		if err == nil || err.Error() != want || !errors.Is(err, dbclient.ErrUnavailable) || errors.Is(err, fs.ErrNotExist) {
			t.Errorf("a writer of a daemon without the file of the lock: %v, want %q", err, want)
		}
		if d := time.Since(start); d > 2*time.Second {
			t.Errorf("a writer of a daemon without the file of the lock gave up after %v, want at once", d)
		}
		f.open(dbwire.RoleWriter, dbclient.Options{DialWait: 5 * time.Second})
	})
}
