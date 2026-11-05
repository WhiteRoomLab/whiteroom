package simfs_test

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/logdb/simfs"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// mustLock locks name in fsys with mode, and returns its unlock.
func mustLock(t *testing.T, fsys vfs.FS, name string, mode vfs.LockMode) func() error {
	t.Helper()
	unlock, err := fsys.Lock(name, mode)
	if err != nil {
		t.Fatal(err)
	}
	return unlock
}

// checkLocked checks that a lock of name in fsys with mode fails with ErrLocked.
func checkLocked(t *testing.T, fsys vfs.FS, name string, mode vfs.LockMode) {
	t.Helper()
	if unlock, err := fsys.Lock(name, mode); !errors.Is(err, vfs.ErrLocked) {
		if err == nil {
			_ = unlock()
		}
		t.Errorf("Lock(%q, %d): %v, want ErrLocked", name, mode, err)
	}
}

// TestKillAt checks that the operations of a process up to the counted operation of its kill do what they do, and that
// every later one fails with ErrKilled, also on the files and the Subs that it has opened before, and changes nothing:
// what it has written stays as it is, in the page cache.
func TestKillAt(t *testing.T) {
	fsys := simfs.New("/data")
	mustMkdir(t, fsys, "d") // counted 1, by another process
	p := fsys.KillAt(4)
	pfs := p.FS()
	if pfs.Name() != "/data" {
		t.Errorf("Name: %q", pfs.Name())
	}
	sub, err := pfs.Sub("d")
	if err != nil {
		t.Fatal(err)
	}
	f, err := sub.OpenFile("f", os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600) // counted 2
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("data")); err != nil { // 3
		t.Fatal(err)
	}
	if err := f.Sync(); err != nil { // 4, after which the process is killed
		t.Fatalf("Sync, the last operation of the process: %v", err)
	}
	buf := make([]byte, 4)
	for _, op := range []struct {
		name string
		run  func() error
	}{
		{"Open", func() error { _, err := pfs.Open("d/f"); return err }},
		{"OpenFile", func() error { _, err := pfs.OpenFile("g", os.O_WRONLY|os.O_CREATE, 0o600); return err }},
		{"Mkdir", func() error { return pfs.Mkdir("e", 0o700) }},
		{"Mkdir of a Sub", func() error { return sub.Mkdir("e", 0o700) }},
		{"Remove", func() error { return pfs.Remove("d/f") }},
		{"Rename", func() error { return pfs.Rename("d/f", "g") }},
		{"Lstat", func() error { _, err := pfs.Lstat("d"); return err }},
		{"ReadDir", func() error { _, err := pfs.ReadDir("."); return err }},
		{"SyncDir", func() error { return pfs.SyncDir(".") }},
		{"Sub", func() error { _, err := pfs.Sub("d"); return err }},
		{"Lock", func() error { _, err := pfs.Lock("d/f", vfs.Shared); return err }},
		{"Read", func() error { _, err := f.Read(buf); return err }},
		{"ReadAt", func() error { _, err := f.ReadAt(buf, 0); return err }},
		{"Write", func() error { _, err := f.Write(buf); return err }},
		{"WriteAt", func() error { _, err := f.WriteAt(buf, 0); return err }},
		{"Seek", func() error { _, err := f.Seek(0, io.SeekStart); return err }},
		{"Sync", f.Sync},
		{"Truncate", func() error { return f.Truncate(0) }},
		{"Stat", func() error { _, err := f.Stat(); return err }},
		{"ReadDir of a file", func() error { _, err := f.ReadDir(-1); return err }},
		{"Close of a file", f.Close},
		{"Close of a Sub", sub.Close},
		{"Close", pfs.Close},
	} {
		if err := op.run(); !errors.Is(err, simfs.ErrKilled) {
			t.Errorf("%s after the kill: %v, want ErrKilled", op.name, err)
		}
	}
	if n := fsys.Counted(); n != 4 {
		t.Errorf("%d operations counted, want 4", n)
	}
	if got := files(t, fsys); got != "d/ d/f=data" {
		t.Errorf("files: %q", got)
	}
	checkPending(t, fsys, "entry d; entry d/f")
}

// TestKillReleasesLocks checks that the kill releases the locks that the process holds, also those of its Subs, and
// that its unlocks then fail with ErrKilled; an unlock before the kill releases its lock once.
func TestKillReleasesLocks(t *testing.T) {
	fsys := simfs.New("/data")
	mustMkdir(t, fsys, "d")
	mustCreate(t, fsys, "a", "")
	mustCreate(t, fsys, "d/b", "")
	p := fsys.KillAt(fsys.Counted() + 1)
	pfs := p.FS()
	sub, err := pfs.Sub("d")
	if err != nil {
		t.Fatal(err)
	}
	unlockA := mustLock(t, pfs, "a", vfs.Exclusive)
	mustLock(t, sub, "b", vfs.Shared)
	unlockD := mustLock(t, pfs, "d", vfs.Exclusive)
	if err := unlockD(); err != nil {
		t.Fatalf("an unlock before the kill: %v", err)
	}
	if err := unlockD(); !errors.Is(err, fs.ErrClosed) {
		t.Errorf("a second unlock: %v, want fs.ErrClosed", err)
	}
	checkLocked(t, fsys, "a", vfs.Shared)
	checkLocked(t, fsys, "d/b", vfs.Exclusive)
	if err := mustLock(t, fsys, "d", vfs.Exclusive)(); err != nil {
		t.Fatal(err)
	}
	mustMkdir(t, pfs, "e") // the counted operation after which the process is killed
	mustLock(t, fsys, "a", vfs.Exclusive)
	mustLock(t, fsys, "d/b", vfs.Exclusive)
	if err := unlockA(); !errors.Is(err, simfs.ErrKilled) {
		t.Errorf("an unlock after the kill: %v, want ErrKilled", err)
	}
}

// TestKillAtCounted checks that a process whose operation has been counted is dead from the start, and that a process
// dies at the end of its first operation after its operation has been counted, by any process.
func TestKillAtCounted(t *testing.T) {
	fsys := simfs.New("/data")
	mustMkdir(t, fsys, "d")
	if _, err := fsys.KillAt(1).FS().Lstat("d"); !errors.Is(err, simfs.ErrKilled) {
		t.Errorf("a process killed at an operation that has been counted: %v, want ErrKilled", err)
	}
	pfs := fsys.KillAt(2).FS()
	if _, err := pfs.Lstat("d"); err != nil {
		t.Fatalf("Lstat before the kill: %v", err)
	}
	mustMkdir(t, fsys, "e") // the operation of the kill, by another process
	if _, err := pfs.Lstat("e"); err != nil {
		t.Fatalf("Lstat, after which the process is killed: %v", err)
	}
	if _, err := pfs.Lstat("d"); !errors.Is(err, simfs.ErrKilled) {
		t.Errorf("Lstat after the kill: %v, want ErrKilled", err)
	}
	if n := fsys.Counted(); n != 2 {
		t.Errorf("%d operations counted, want 2", n)
	}
}

// TestHangAt checks that a process hangs after the operation of HangAt, which has done what it does, with its locks,
// while its other operations fail without changing the world; after Release the operation returns, and the process is
// dead with its locks held. HangAt of an operation that has been counted panics; a Release before the hang lets the
// process die without hanging.
func TestHangAt(t *testing.T) {
	fsys := simfs.New("/data")
	mustCreate(t, fsys, "lock", "")
	p := fsys.HangAt(fsys.Counted() + 1)
	pfs := p.FS()
	unlock := mustLock(t, pfs, "lock", vfs.Exclusive)
	done := make(chan error, 1)
	go func() { done <- pfs.Mkdir("d", 0o700) }()
	<-p.Hung()
	select {
	case err := <-done:
		t.Fatalf("Mkdir has returned %v while the process hangs", err)
	default:
	}
	if got := files(t, fsys); got != "d/ lock=" {
		t.Errorf("files while the process hangs: %q", got)
	}
	checkLocked(t, fsys, "lock", vfs.Shared)
	if err := pfs.Mkdir("e", 0o700); !errors.Is(err, simfs.ErrKilled) {
		t.Errorf("another operation while the process hangs: %v, want ErrKilled", err)
	}
	if err := unlock(); !errors.Is(err, simfs.ErrKilled) {
		t.Errorf("an unlock while the process hangs: %v, want ErrKilled", err)
	}
	p.Release()
	if err := <-done; err != nil {
		t.Fatalf("Mkdir after Release: %v", err)
	}
	p.Release()
	if _, err := pfs.Lstat("d"); !errors.Is(err, simfs.ErrKilled) {
		t.Errorf("Lstat after Release: %v, want ErrKilled", err)
	}
	checkLocked(t, fsys, "lock", vfs.Shared)
	if got := files(t, fsys); got != "d/ lock=" {
		t.Errorf("files after Release: %q", got)
	}

	func() {
		defer func() {
			if recover() == nil {
				t.Error("HangAt of an operation that has been counted does not panic")
			}
		}()
		fsys.HangAt(fsys.Counted())
	}()

	q := fsys.HangAt(fsys.Counted() + 1)
	q.Release()
	qfs := q.FS()
	if err := qfs.Mkdir("f", 0o700); err != nil {
		t.Fatalf("Mkdir of a process released before its hang: %v", err)
	}
	select {
	case <-q.Hung():
	default:
		t.Error("Hung is not closed after the operation of the hang")
	}
	if err := qfs.Mkdir("g", 0o700); !errors.Is(err, simfs.ErrKilled) {
		t.Errorf("Mkdir after the hang: %v, want ErrKilled", err)
	}
}
