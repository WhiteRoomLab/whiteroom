package dbclient_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/dbclient"
	"github.com/WhiteRoomLab/whiteroom/internal/dbserver"
	"github.com/WhiteRoomLab/whiteroom/internal/dbserver/dbtest"
	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/simfs"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// headLine is the line of a head of the log activity that a commit of the tests puts.
func headLine(n uint64) string { return fmt.Sprintf("audit head activity %d sha256:00", n) }

// populate makes commits that have rows, objects (one in a file of the engine), events, two lines of heads, and originals.
func populate(t *testing.T, f *fixture, w *dbclient.Client) (originals [][32]byte) {
	t.Helper()
	big := strings.Repeat("an object in a file ", 100)
	must(t, w.Update(bg, func(tx *dbclient.Tx) error {
		put(t, tx, 2, "a", "1")
		put(t, tx, 2, "b", "2")
		put(t, tx, dbwire.TableHeads, "activity", headLine(2))
		put(t, tx, dbwire.TableHeads, "other", "audit head other 1 sha256:11")
		must(t, tx.PutObject(sha256.Sum256([]byte(big)), []byte(big)))
		must(t, tx.PutObject(sha256.Sum256([]byte("small")), []byte("small")))
		return tx.AppendEvent([]byte(`{"populate":1}`))
	}))
	for i, n := range []int{6, 5 * testFrame, 20_000} {
		originals = append(originals, putOriginal(t, w, data(n, byte(i))))
	}
	commit(t, w, func(tx *dbclient.Tx) {
		put(t, tx, 2, "c", "3")
		put(t, tx, dbwire.TableHeads, "activity", headLine(3))
	})
	return originals
}

// opFS is a file system that logs the changes that are made in it, with the modes.
type opFS struct {
	vfs.FS
	mu  sync.Mutex
	ops []string
}

func (o *opFS) log(format string, args ...any) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.ops = append(o.ops, fmt.Sprintf(format, args...))
}

func (o *opFS) logged() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.ops)
}

func (o *opFS) Mkdir(name string, perm fs.FileMode) error {
	o.log("mkdir %s %o", name, perm)
	return o.FS.Mkdir(name, perm)
}

func (o *opFS) OpenFile(name string, flag int, perm fs.FileMode) (vfs.File, error) {
	o.log("open %s %o excl=%v", name, perm, flag&os.O_EXCL != 0)
	f, err := o.FS.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return &opFile{File: f, o: o, name: name}, nil
}

func (o *opFS) SyncDir(name string) error {
	o.log("syncdir %s", name)
	return o.FS.SyncDir(name)
}

type opFile struct {
	vfs.File
	o    *opFS
	name string
}

func (f *opFile) Sync() error {
	f.o.log("sync %s", f.name)
	return f.File.Sync()
}

func (f *opFile) Close() error {
	f.o.log("close %s", f.name)
	return f.File.Close()
}

// filesIn returns the files of fsys that are in the directory dir, or all if dir is empty, with what they hold.
func filesIn(fsys *simfs.FS, dir string) map[string]string {
	m := map[string]string{}
	fsys.Walk(func(p string, data []byte) {
		if dir == "" || strings.HasPrefix(p, dir+"/") {
			m[p] = string(data)
		}
	})
	return m
}

var loseAll = simfs.Model{Name: "lose-all"}

// openCopy opens the data directory that is in fsys, with a daemon of its own.
func openCopy(t *testing.T, fsys *simfs.FS) *dbtest.Daemon {
	t.Helper()
	opts := testOptions(fsys)
	h, err := dbtest.Acquire("copy", opts)
	if err != nil {
		t.Fatalf("the copy does not open: %v", err)
	}
	t.Cleanup(func() { _ = h.Release() })
	return h
}

func TestBackup(t *testing.T) {
	for _, role := range []dbwire.Role{dbwire.RoleWriter, dbwire.RoleReader} {
		t.Run(role.String(), func(t *testing.T) {
			f := newFixture(t, nil)
			w, _ := f.writer(dbclient.Options{})
			originals := populate(t, f, w)
			c := w
			if role == dbwire.RoleReader {
				c, _ = f.open(role, dbclient.Options{})
			}
			want := f.state()
			// A temporary file of a PutOriginal that was killed is not sent.
			tmp, err := f.fs.OpenFile("files/.tmp-0123456789abcdef", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			must(t, err)
			_, _ = tmp.Write([]byte("half"))
			must(t, tmp.Close())

			copied := simfs.New("copy")
			b, err := c.Backup(bg, copied)
			if err != nil {
				t.Fatalf("Backup: %v", err)
			}
			if b.Seq != want.Seq || b.End <= 0 {
				t.Errorf("the position is %+v, want the commit %d", b, want.Seq)
			}
			if lines := []string{headLine(3), "audit head other 1 sha256:11"}; !slices.Equal(b.Heads, lines) {
				t.Errorf("the lines of the heads are %q, want %q", b.Heads, lines)
			}
			// The copy is the store at the commit of the position.
			h := openCopy(t, copied)
			got, err := readState(dbserver.EngineOf(h.Server()))
			must(t, err)
			if !reflect.DeepEqual(want, got) {
				t.Errorf("the copy is not the store:\n store %+v\n copy  %+v", want, got)
			}
			must(t, checkObjects(dbserver.EngineOf(h.Server()), want.Objects))
			wantFiles := f.originalFiles()
			delete(wantFiles, "files/.tmp-0123456789abcdef")
			if got := filesIn(copied, "files"); !maps.Equal(got, wantFiles) {
				t.Errorf("the files of the copy are %q, want %q", slices.Sorted(maps.Keys(got)), slices.Sorted(maps.Keys(wantFiles)))
			}
			cc, err := dbclient.Open(bg, h.Dialer(), dbwire.RoleReader, dbclient.Options{})
			must(t, err)
			defer func() { _ = cc.Close() }()
			for i, o := range originals {
				if got := readOriginal(t, cc, o); !bytes.Equal(got, data([]int{6, 5 * testFrame, 20_000}[i], byte(i))) {
					t.Errorf("the original %d of the copy has %d bytes", i, len(got))
				}
			}
			// The modes.
			for _, name := range []string{"files", "logdb", "logdb/log"} {
				if info, err := copied.Lstat(name); err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
					t.Errorf("%s: %v, %v", name, info, err)
				}
			}
			if info, err := copied.Lstat(originalPath(originals[0])); err != nil || info.Mode().Perm() != 0o600 {
				t.Errorf("an original: %v, %v", info, err)
			}
			// The client goes on, and the store has not changed.
			if got := f.state(); !reflect.DeepEqual(want, got) {
				t.Error("the Backup changed the store")
			}
			commit(t, w, func(tx *dbclient.Tx) { put(t, tx, 2, "after", "x") })
		})
	}
}

// TestBackupOrderOfChanges checks the changes that Backup makes in the destination, and their order.
func TestBackupOrderOfChanges(t *testing.T) {
	f := newFixture(t, nil)
	w, _ := f.writer(dbclient.Options{})
	populate(t, f, w)
	dst := &opFS{FS: simfs.New("copy")}
	if _, err := w.Backup(bg, dst); err != nil {
		t.Fatal(err)
	}
	ops := dst.logged()
	var dirs, syncs []string
	lastFile, firstSyncDir := -1, len(ops)
	opened := map[string]bool{}
	for i, op := range ops {
		kind, rest, _ := strings.Cut(op, " ")
		name, _, _ := strings.Cut(rest, " ")
		switch kind {
		case "mkdir":
			if rest != name+" 700" {
				t.Errorf("%q: a directory is made with the mode 0700", op)
			}
			if parent := parentOf(name); parent != "." && !slices.Contains(dirs, parent) {
				t.Errorf("%q: the parent is not made before", op)
			}
			dirs = append(dirs, name)
		case "open":
			if rest != name+" 600 excl=true" {
				t.Errorf("%q: a file is made with the mode 0600, and O_EXCL", op)
			}
			if parent := parentOf(name); !slices.Contains(dirs, parent) {
				t.Errorf("%q: the directory is not made before", op)
			}
			opened[name] = true
			lastFile = i
		case "sync":
			if !opened[name] {
				t.Errorf("%q: not a file that is open", op)
			}
			delete(opened, name)
			lastFile = i
		case "close":
			lastFile = i
			if opened[name] {
				t.Errorf("%q: the file was closed before it was synced", op)
			}
		case "syncdir":
			firstSyncDir = min(firstSyncDir, i)
			syncs = append(syncs, name)
		}
	}
	if len(opened) != 0 {
		t.Errorf("files that were not synced: %v", slices.Sorted(maps.Keys(opened)))
	}
	if firstSyncDir <= lastFile {
		t.Error("a directory was synced before the last file was written")
	}
	// Every directory that was made, the deepest first (the later one first, as a directory is made after its parent), and
	// the destination last.
	var wantSyncs []string
	for _, d := range slices.Backward(dirs) {
		wantSyncs = append(wantSyncs, d)
	}
	wantSyncs = append(wantSyncs, ".")
	if !slices.Equal(syncs, wantSyncs) {
		t.Errorf("the directories were synced in the order %q, want %q", syncs, wantSyncs)
	}
	if len(dirs) < 6 {
		t.Errorf("only %d directories were made: %q", len(dirs), dirs)
	}
}

func parentOf(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[:i]
	}
	return "."
}

// TestBackupCrash checks that a copy that Backup has written is whole after a crash that loses everything that was not
// synced.
func TestBackupCrash(t *testing.T) {
	f := newFixture(t, nil)
	w, _ := f.writer(dbclient.Options{})
	originals := populate(t, f, w)
	want := f.state()
	copied := simfs.New("copy")
	if _, err := w.Backup(bg, copied); err != nil {
		t.Fatal(err)
	}
	crashed := copied.Crash(loseAll)
	h := openCopy(t, crashed)
	got, err := readState(dbserver.EngineOf(h.Server()))
	must(t, err)
	if !reflect.DeepEqual(want, got) {
		t.Errorf("the copy after the crash is not the store:\n store %+v\n copy  %+v", want, got)
	}
	must(t, checkObjects(dbserver.EngineOf(h.Server()), want.Objects))
	if gotFiles, wantFiles := filesIn(crashed, "files"), f.originalFiles(); !maps.Equal(gotFiles, wantFiles) {
		t.Errorf("the files of the copy after the crash are %q, want %q", slices.Sorted(maps.Keys(gotFiles)), slices.Sorted(maps.Keys(wantFiles)))
	}
	for _, o := range originals {
		if _, ok := filesIn(crashed, "files")[originalPath(o)]; !ok {
			t.Errorf("the copy has lost the original %x", o)
		}
	}
	// A copy that was not synced would have lost its files: the model does lose what is not synced.
	loose := simfs.New("loose")
	file, err := loose.OpenFile("x", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	must(t, err)
	_, _ = file.Write([]byte("x"))
	must(t, file.Close())
	if got := filesIn(loose.Crash(loseAll), ""); len(got) != 0 {
		t.Fatalf("the model keeps %v", got)
	}
}

func TestBackupDestination(t *testing.T) {
	t.Run("not empty", func(t *testing.T) {
		f := newFixture(t, nil)
		w, rec := f.writer(dbclient.Options{})
		populate(t, f, w)
		dst := simfs.New("copy")
		file, err := dst.OpenFile("stay", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		must(t, err)
		must(t, file.Close())
		rec.reset()
		_, err = w.Backup(bg, &opFS{FS: dst})
		if err == nil || err.Error() != "copy: not empty" {
			t.Errorf("got %v", err)
		}
		if rec.dialed(dbwire.RoleReader) != 0 {
			t.Error("a Backup to a destination that is not empty made a connection")
		}
		wantRequests(t, rec)
		if got := filesIn(dst, ""); len(got) != 1 {
			t.Errorf("the destination has %v", got)
		}
	})

	t.Run("a directory in it", func(t *testing.T) {
		f := newFixture(t, nil)
		w, _ := f.writer(dbclient.Options{})
		dst := &opFS{FS: simfs.New("copy")}
		must(t, dst.FS.Mkdir("stay", 0o700))
		if _, err := w.Backup(bg, dst); err == nil || err.Error() != "copy: not empty" {
			t.Errorf("got %v", err)
		}
		if ops := dst.logged(); len(ops) != 0 {
			t.Errorf("Backup changed the destination: %q", ops)
		}
	})

	t.Run("a context that has ended", func(t *testing.T) {
		f := newFixture(t, nil)
		w, _ := f.writer(dbclient.Options{})
		ctx, cancel := context.WithCancel(bg)
		cancel()
		dst := &opFS{FS: simfs.New("copy")}
		if _, err := w.Backup(ctx, dst); !errors.Is(err, context.Canceled) {
			t.Errorf("got %v", err)
		}
		if ops := dst.logged(); len(ops) != 0 {
			t.Errorf("Backup changed the destination: %q", ops)
		}
	})

	t.Run("a closed client", func(t *testing.T) {
		f := newFixture(t, nil)
		w, _ := f.writer(dbclient.Options{})
		must(t, w.Close())
		if _, err := w.Backup(bg, simfs.New("copy")); !errors.Is(err, dbclient.ErrClosed) {
			t.Errorf("got %v", err)
		}
	})

	t.Run("a file system that fails", func(t *testing.T) {
		f := newFixture(t, nil)
		w, _ := f.writer(dbclient.Options{})
		populate(t, f, w)
		errDisk := errors.New("the disk is full")
		for _, c := range []struct {
			name string
			fail func(fsys *simfs.FS)
		}{
			{"a directory", func(fsys *simfs.FS) {
				fsys.Inject(simfs.Fault{Match: func(op simfs.Op) bool { return op.Kind == simfs.OpMkdir && strings.HasSuffix(op.Path, "/objects") }, Err: errDisk, Times: 1})
			}},
			{"a write", func(fsys *simfs.FS) {
				fsys.Inject(simfs.Fault{Match: func(op simfs.Op) bool { return op.Kind == simfs.OpWrite && strings.Contains(op.Path, "files/") }, Err: errDisk, Times: 1})
			}},
			{"a sync of a file", func(fsys *simfs.FS) {
				fsys.Inject(simfs.Fault{Match: func(op simfs.Op) bool { return op.Kind == simfs.OpSync }, Err: errDisk, Times: 1})
			}},
			{"a sync of a directory", func(fsys *simfs.FS) {
				fsys.Inject(simfs.Fault{Match: func(op simfs.Op) bool { return op.Kind == simfs.OpSyncDir }, Err: errDisk, Times: 1})
			}},
		} {
			t.Run(c.name, func(t *testing.T) {
				dst := simfs.New("copy")
				c.fail(dst)
				_, err := w.Backup(bg, dst)
				if !errors.Is(err, errDisk) || errors.Is(err, dbclient.ErrUnavailable) {
					t.Errorf("got %v, want the error of the file system, which is not that of the daemon", err)
				}
				// The reply was not read to its end: its connection is closed, and the next View is on another.
				commit(t, w, func(tx *dbclient.Tx) { put(t, tx, 2, "k", c.name) })
				wantRows(t, rows(t, w, 2, "k"), "k="+c.name)
			})
		}
	})
}

// backupDaemon is a daemon of a test that answers a Backup with what reply writes, after the Hello.
func backupDaemon(t *testing.T, reply func(wr *dbwire.Writer)) *fakeDialer {
	return &fakeDialer{serve: func(rd *dbwire.Reader, wr *dbwire.Writer) {
		fakeHello(t, rd, wr, dbwire.RoleReader)
		for {
			kind, err := rd.Next()
			if err != nil {
				return
			}
			if kind != dbwire.KindBackup {
				t.Errorf("the request is a %v", kind)
				return
			}
			_ = rd.Discard()
			reply(wr)
		}
	}}
}

// beginReply starts an OK reply of a daemon of a test, with frames of at most frame bytes if it is not 0: what is written is
// then on the connection, and not held in the buffer of the writer, when the reply ends with an error.
func beginReply(wr *dbwire.Writer, frame int) *dbwire.Enc {
	if frame > 0 {
		wr.SetMaxFrame(frame)
	}
	wr.Begin(dbwire.KindOK)
	return &dbwire.Enc{W: wr}
}

// rawEntry writes an entry of the kind with the path p, as dbwire does not let a daemon write it if it is not valid.
func rawEntry(e *dbwire.Enc, kind dbwire.EntryKind, p string) {
	e.U8(uint8(kind))
	e.U16(uint16(len(p)))
	e.Raw([]byte(p))
}

func position(e *dbwire.Enc) {
	e.BackupEntry(dbwire.BackupEntry{Kind: dbwire.EntryPosition, Position: dbwire.BackupPosition{Generation: 1, Segment: 1, End: 10, Seq: 3, CRC: 7}})
}

// TestBackupHostileDaemon checks that the entries of a daemon that names paths outside the destination are refused, and
// that nothing is written for them.
func TestBackupHostileDaemon(t *testing.T) {
	for _, c := range []struct {
		name     string
		kind     dbwire.EntryKind
		path     string
		protocol bool // dbwire refuses the path before Backup sees it
	}{
		{"parent of the destination", dbwire.EntryDir, "../escape", true},
		{"parent in the middle", dbwire.EntryDir, "files/../../escape", true},
		{"a file in the parent", dbwire.EntryFile, "files/../x", true},
		{"absolute", dbwire.EntryDir, "/etc", true},
		{"absolute file", dbwire.EntryFile, "/etc/passwd", true},
		{"the root", dbwire.EntryDir, ".", true},
		{"a backslash", dbwire.EntryDir, `files\..\x`, true},
		{"a directory outside", dbwire.EntryDir, "other", false},
		{"a file outside", dbwire.EntryFile, "other/x", false},
		{"a name that starts as files", dbwire.EntryDir, "filesystem", false},
		{"a name that starts as logdb", dbwire.EntryFile, "logdb2/x", false},
		{"upper case", dbwire.EntryDir, "Files", false},
		{"a file at the top", dbwire.EntryFile, "x", false},
	} {
		for _, after := range []bool{false, true} {
			name := c.name
			if after {
				name += " after a directory"
			}
			t.Run(name, func(t *testing.T) {
				d := backupDaemon(t, func(wr *dbwire.Writer) {
					e := beginReply(wr, 0)
					position(e)
					if after {
						e.BackupEntry(dbwire.BackupEntry{Kind: dbwire.EntryDir, Path: "files"})
					}
					rawEntry(e, c.kind, c.path)
					if c.kind == dbwire.EntryFile {
						chunks := e.Chunks()
						_, _ = chunks.Write([]byte("evil"))
						_ = chunks.Close()
					}
					e.BackupEntry(dbwire.BackupEntry{Kind: dbwire.EntryEnd})
					_ = wr.End()
				})
				noLeaks(t)
				r, err := dbclient.Open(bg, d, dbwire.RoleReader, dbclient.Options{DialWait: -1})
				must(t, err)
				defer func() { _ = r.Close() }()
				dst := &opFS{FS: simfs.New("copy")}
				_, err = r.Backup(bg, dst)
				if err == nil {
					t.Fatal("Backup succeeded")
				}
				if c.protocol && !errors.Is(err, dbwire.ErrProtocol) {
					t.Errorf("got %v, want a protocol error", err)
				}
				if !c.protocol && (!strings.Contains(err.Error(), "copy: the backup has an entry for ") ||
					!strings.Contains(err.Error(), "which is not a path in files or logdb") || errors.Is(err, dbclient.ErrUnavailable)) {
					t.Errorf("got %v", err)
				}
				var want []string
				if after {
					want = []string{"mkdir files 700"}
				}
				if got := dst.logged(); !slices.Equal(got, want) {
					t.Errorf("the destination was changed: %q, want %q", got, want)
				}
				if got := filesIn(dst.FS.(*simfs.FS), ""); len(got) != 0 {
					t.Errorf("the destination has the files %v", got)
				}
			})
		}
	}
}

func TestBackupBadReplies(t *testing.T) {
	open := func(t *testing.T, d *fakeDialer) *dbclient.Client {
		noLeaks(t)
		r, err := dbclient.Open(bg, d, dbwire.RoleReader, dbclient.Options{DialWait: -1})
		must(t, err)
		t.Cleanup(func() { _ = r.Close() })
		return r
	}
	dirEntry := func(e *dbwire.Enc, p string) { e.BackupEntry(dbwire.BackupEntry{Kind: dbwire.EntryDir, Path: p}) }

	t.Run("a reply that ends before the end entry", func(t *testing.T) {
		d := backupDaemon(t, func(wr *dbwire.Writer) {
			e := beginReply(wr, 0)
			position(e)
			dirEntry(e, "files")
			_ = wr.End()
		})
		r := open(t, d)
		dst := &opFS{FS: simfs.New("copy")}
		_, err := r.Backup(bg, dst)
		if !errors.Is(err, io.ErrUnexpectedEOF) || !errors.Is(err, dbclient.ErrUnavailable) {
			t.Errorf("got %v", err)
		}
		// What it wrote stays, for the caller to remove.
		if got := dst.logged(); !slices.Equal(got, []string{"mkdir files 700"}) {
			t.Errorf("the destination has %q", got)
		}
	})

	t.Run("a file that ends before its chunks do", func(t *testing.T) {
		d := backupDaemon(t, func(wr *dbwire.Writer) {
			e := beginReply(wr, 0)
			position(e)
			dirEntry(e, "files")
			e.BackupEntry(dbwire.BackupEntry{Kind: dbwire.EntryFile, Path: "files/x"})
			e.U32(10)
			e.Raw([]byte("abc"))
			_ = wr.End()
		})
		r := open(t, d)
		_, err := r.Backup(bg, simfs.New("copy"))
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Errorf("got %v", err)
		}
	})

	t.Run("an error reply after some entries", func(t *testing.T) {
		d := backupDaemon(t, func(wr *dbwire.Writer) {
			e := beginReply(wr, 4)
			position(e)
			dirEntry(e, "files")
			_ = wr.Fail(dbwire.CodeFailed, "files/ab/cd: cannot read: input/output error")
		})
		r := open(t, d)
		_, err := r.Backup(bg, simfs.New("copy"))
		re := remote(t, err)
		if re.Code != dbwire.CodeFailed || re.Msg != "files/ab/cd: cannot read: input/output error" {
			t.Errorf("got %v", err)
		}
		// The reply was whole: its connection goes back, and the next Backup is on a new one all the same.
		if _, err := r.Backup(bg, simfs.New("copy")); err == nil {
			t.Error("the second Backup succeeded")
		}
		if d.dials != 3 {
			t.Errorf("%d dials, want the connection of Open and one for each Backup", d.dials)
		}
	})

	t.Run("no position", func(t *testing.T) {
		d := backupDaemon(t, func(wr *dbwire.Writer) {
			e := beginReply(wr, 0)
			dirEntry(e, "files")
			e.BackupEntry(dbwire.BackupEntry{Kind: dbwire.EntryEnd})
			_ = wr.End()
		})
		r := open(t, d)
		if _, err := r.Backup(bg, simfs.New("copy")); !errors.Is(err, dbwire.ErrProtocol) || !strings.Contains(err.Error(), "no position") {
			t.Errorf("got %v", err)
		}
	})

	t.Run("two positions", func(t *testing.T) {
		d := backupDaemon(t, func(wr *dbwire.Writer) {
			e := beginReply(wr, 0)
			position(e)
			position(e)
			e.BackupEntry(dbwire.BackupEntry{Kind: dbwire.EntryEnd})
			_ = wr.End()
		})
		r := open(t, d)
		if _, err := r.Backup(bg, simfs.New("copy")); !errors.Is(err, dbwire.ErrProtocol) || !strings.Contains(err.Error(), "two positions") {
			t.Errorf("got %v", err)
		}
	})

	t.Run("a directory that exists", func(t *testing.T) {
		d := backupDaemon(t, func(wr *dbwire.Writer) {
			e := beginReply(wr, 0)
			position(e)
			dirEntry(e, "files")
			dirEntry(e, "files")
			e.BackupEntry(dbwire.BackupEntry{Kind: dbwire.EntryEnd})
			_ = wr.End()
		})
		r := open(t, d)
		if _, err := r.Backup(bg, simfs.New("copy")); !errors.Is(err, fs.ErrExist) {
			t.Errorf("got %v", err)
		}
	})

	t.Run("a file that exists", func(t *testing.T) {
		d := backupDaemon(t, func(wr *dbwire.Writer) {
			e := beginReply(wr, 0)
			position(e)
			dirEntry(e, "files")
			for range 2 {
				e.BackupEntry(dbwire.BackupEntry{Kind: dbwire.EntryFile, Path: "files/x"})
				chunks := e.Chunks()
				_, _ = chunks.Write([]byte("abc"))
				_ = chunks.Close()
			}
			e.BackupEntry(dbwire.BackupEntry{Kind: dbwire.EntryEnd})
			_ = wr.End()
		})
		r := open(t, d)
		if _, err := r.Backup(bg, simfs.New("copy")); !errors.Is(err, fs.ErrExist) {
			t.Errorf("got %v", err)
		}
	})

	t.Run("a file with no directory", func(t *testing.T) {
		d := backupDaemon(t, func(wr *dbwire.Writer) {
			e := beginReply(wr, 0)
			position(e)
			e.BackupEntry(dbwire.BackupEntry{Kind: dbwire.EntryFile, Path: "files/x"})
			chunks := e.Chunks()
			_, _ = chunks.Write([]byte("abc"))
			_ = chunks.Close()
			e.BackupEntry(dbwire.BackupEntry{Kind: dbwire.EntryEnd})
			_ = wr.End()
		})
		r := open(t, d)
		if _, err := r.Backup(bg, simfs.New("copy")); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("got %v", err)
		}
	})

	t.Run("a context that ends in the middle", func(t *testing.T) {
		stop := make(chan struct{})
		d := backupDaemon(t, func(wr *dbwire.Writer) {
			e := beginReply(wr, 4)
			position(e)
			dirEntry(e, "files")
			<-stop // the client cuts the connection
		})
		defer close(stop)
		r := open(t, d)
		ctx, cancel := context.WithTimeout(bg, 50*time.Millisecond)
		defer cancel()
		if _, err := r.Backup(ctx, simfs.New("copy")); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("got %v", err)
		}
	})
}
