package dbserver

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/fspath"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

func noErr(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// TestMakeWriterLock: the file of the lock of the writer is made once, and kept as it is, so that a lock that a client
// holds across a restart of the daemon is that of the next daemon too.
func TestMakeWriterLock(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, dbwire.WriterLock)
	wantFile := func(what string) os.FileInfo {
		t.Helper()
		info, err := os.Lstat(p)
		switch {
		case err != nil:
			t.Fatalf("%s: %v", what, err)
		case !info.Mode().IsRegular() || info.Mode().Perm() != 0o660:
			t.Errorf("%s: the lock is %v, want a regular file with the mode 0660", what, info.Mode())
		case int(info.Sys().(*syscall.Stat_t).Gid) != os.Getegid():
			t.Errorf("%s: the group of the lock is %d, want %d", what, info.Sys().(*syscall.Stat_t).Gid, os.Getegid())
		}
		return info
	}
	if made, err := MakeWriterLock(dir, os.Getegid()); err != nil || !made {
		t.Fatalf("MakeWriterLock() = %v, %v, want a file made", made, err)
	}
	made := wantFile("made")

	// A client holds the lock; the daemon starts again, with the file as another left it.
	root, err := vfs.OpenRoot(dir)
	noErr(t, err)
	defer func() { _ = root.Close() }()
	unlock, err := root.Lock(dbwire.WriterLock, vfs.Exclusive)
	noErr(t, err)
	noErr(t, os.Chmod(p, 0o600))
	noErr(t, os.WriteFile(p, []byte("kept"), 0o600))
	if again, err := MakeWriterLock(dir, -1); err != nil || again {
		t.Fatalf("MakeWriterLock() of a file that is there = %v, %v, want none made", again, err)
	}
	if again := wantFile("kept"); !os.SameFile(made, again) {
		t.Error("the file of the lock was made anew")
	}
	if data, err := os.ReadFile(p); err != nil || string(data) != "kept" {
		t.Errorf("the file of the lock has %q (%v), want what it had", data, err)
	}
	if _, err := root.Lock(dbwire.WriterLock, vfs.Exclusive); !errors.Is(err, vfs.ErrLocked) {
		t.Errorf("a second lock after MakeWriterLock: %v, want ErrLocked", err)
	}
	noErr(t, unlock())

	t.Run("not a file", func(t *testing.T) {
		for name, mk := range map[string]func(p string) error{
			"a directory":       func(p string) error { return os.Mkdir(p, 0o700) },
			"a symbolic link":   func(p string) error { return os.Symlink(filepath.Join(filepath.Dir(p), "target"), p) },
			"a link to nothing": func(p string) error { return os.Symlink("missing", p) },
		} {
			dir := t.TempDir()
			p := filepath.Join(dir, dbwire.WriterLock)
			noErr(t, os.WriteFile(filepath.Join(dir, "target"), nil, 0o600))
			noErr(t, mk(p))
			_, err := MakeWriterLock(dir, -1)
			if want := p + ": not a regular file; it is the lock of the writer"; err == nil || err.Error() != want {
				t.Errorf("%s: %v, want %q", name, err, want)
			}
			if info, err := os.Stat(filepath.Join(dir, "target")); err != nil || info.Mode().Perm() != 0o600 {
				t.Errorf("%s: the target of the link is %v (%v), want it as it was", name, info, err)
			}
		}
	})

	t.Run("a failure leaves no file that it made", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root may give a file any group")
		}
		dir := t.TempDir()
		_, err := MakeWriterLock(dir, 1<<30) // a group that the process is not in
		if !errors.Is(err, os.ErrPermission) && !errors.Is(err, syscall.EINVAL) {
			t.Fatalf("MakeWriterLock with a group that the process is not in: %v", err)
		}
		if _, err := os.Lstat(filepath.Join(dir, dbwire.WriterLock)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the file of the lock after the failure: %v, want it removed", err)
		}
		// One that was there stays.
		_, err = MakeWriterLock(dir, -1)
		noErr(t, err)
		if _, err := MakeWriterLock(dir, 1<<30); err == nil {
			t.Fatal("MakeWriterLock with a group that the process is not in succeeded")
		}
		if _, err := os.Lstat(filepath.Join(dir, dbwire.WriterLock)); err != nil {
			t.Errorf("the file of the lock that was there: %v, want it kept", err)
		}
	})
}

// TestOpenDirIdentity: the reply to a Hello tells the identity of the data directory that OpenDir opens, or the one in
// the options.
func TestOpenDirIdentity(t *testing.T) {
	t.Run("OpenDir", func(t *testing.T) {
		f := newDirFixture(t, nil)
		dev, ino, ok, err := fspath.FileID(f.dir)
		if err != nil || !ok {
			t.Fatalf("FileID() = %v, %v", ok, err)
		}
		for _, role := range []dbwire.Role{dbwire.RoleWriter, dbwire.RoleReader} {
			if got := f.connect(role).hello.DataDir; got != (dbwire.DirID{Dev: dev, Ino: ino}) {
				t.Errorf("the %v is told %+v, want the device %d and the inode %d", role, got, dev, ino)
			}
		}
	})
	t.Run("the options", func(t *testing.T) {
		given := dbwire.DirID{Dev: 7, Ino: 9}
		f := newDirFixture(t, func(o *Options) { o.DataDir = given })
		if got := f.connect(dbwire.RoleReader).hello.DataDir; got != given {
			t.Errorf("the reader is told %+v, want %+v of the options", got, given)
		}
	})
	t.Run("Open", func(t *testing.T) {
		// Of a directory that it does not know: it tells none.
		f := newFixture(t, nil)
		if got := f.connect(dbwire.RoleReader).hello.DataDir; got != (dbwire.DirID{}) {
			t.Errorf("the reader of Open is told %+v, want none", got)
		}
	})
}
