package dbserver

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/simfs"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// headLine is the line of a head of the log activity that a commit of the tests puts.
func headLine(n uint64) string { return fmt.Sprintf("audit head activity %d sha256:00", n) }

// populate makes commits in f: rows, an object that is a file of the engine, two lines of heads, and two originals.
func populate(t *testing.T, f *fixture) (originals [][32]byte) {
	t.Helper()
	w := f.connect(dbwire.RoleWriter)
	seq := w.mustSnapshot()
	if _, err := w.putObject(strings.Repeat("o", 1000)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.commit(seq, put(2, "a", "1"), put(2, "b", "2"), put(dbwire.TableHeads, "activity", headLine(seq+1)),
		put(dbwire.TableHeads, "other", "audit head other 1 sha256:11"), event("e1")); err != nil {
		t.Fatal(err)
	}
	for _, data := range [][]byte{[]byte("GIF89a"), bytes.Repeat([]byte("wxyz"), 3000)} {
		originals = append(originals, mustPut(t, w, data))
	}
	w.mustCommit(put(2, "c", "3"), put(dbwire.TableHeads, "activity", headLine(2)))
	_ = f.connectAfter(w).conn.Close() // the writer is gone: a test connects its own
	f.waitSessions(0)
	return originals
}

// restoredServer opens the data directory that a backup has made in fsys.
func restoredServer(t *testing.T, fsys vfs.FS, f *fixture) *fixture {
	t.Helper()
	srv, err := Open(fsys, "copy", f.opts)
	if err != nil {
		t.Fatalf("Open() of the copy = %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return &fixture{t: t, srv: srv, logs: f.logs}
}

// checkCopy checks that the data directory in copied is the store of f at the commit that the position of the reply has: it
// opens, its tables, objects and events are those that f had at that commit (f.dump() if it has not changed since), and
// the lines of the heads of the reply are the rows of its table of heads.
func checkCopy(t *testing.T, f *fixture, reply *backupReply, copied vfs.FS, want *dump) *fixture {
	t.Helper()
	g := restoredServer(t, copied, f)
	got := g.dump()
	if got.Seq != reply.pos.Seq {
		t.Errorf("the copy has the sequence number %d, but the position says %d", got.Seq, reply.pos.Seq)
	}
	if want != nil && !reflect.DeepEqual(*want, got) {
		t.Errorf("the copy is not the store at that commit:\n store %+v\n copy  %+v", *want, got)
	}
	var rows []string
	r := g.connect(dbwire.RoleReader)
	r.mustSnapshot()
	heads, _ := r.scanAll(dbwire.TableHeads, "", 0)
	for _, h := range heads {
		rows = append(rows, h.Value)
	}
	if !slices.Equal(rows, reply.heads) {
		t.Errorf("the lines of the heads are %q, but the table of the copy has %q", reply.heads, rows)
	}
	return g
}

func TestBackup(t *testing.T) {
	f := newFixture(t, nil)
	originals := populate(t, f)
	// What a killed PutOriginal leaves, and what is not an original.
	for name, data := range map[string]string{"files/.tmp-0123456789abcdef": "half", "files/.tmp-x": "x", "files/notes": "n"} {
		file, err := f.fs.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = file.Write([]byte(data))
		_ = file.Close()
	}
	want := f.dump()

	for _, role := range []dbwire.Role{dbwire.RoleReader, dbwire.RoleWriter} {
		t.Run(role.String(), func(t *testing.T) {
			c := f.connect(role)
			copied := simfs.New("copy")
			reply, err := c.backup(copied)
			if err != nil {
				t.Fatalf("Backup: %v", err)
			}
			if reply.pos.Seq != want.Seq {
				t.Errorf("the position is at %d, want %d", reply.pos.Seq, want.Seq)
			}
			if lines := []string{headLine(2), "audit head other 1 sha256:11"}; !slices.Equal(reply.heads, lines) {
				t.Errorf("the lines of the heads are %q, want %q", reply.heads, lines)
			}
			checkCopy(t, f, reply, copied, &want)
			// The originals of the copy are those of the store, but for what is not an original.
			wantFiles := filesOf(f.fs, "files")
			for p := range wantFiles {
				if strings.HasPrefix(path.Base(p), tempPrefix) {
					delete(wantFiles, p)
				}
			}
			if got := filesOf(copied, "files"); !maps.Equal(got, wantFiles) {
				t.Errorf("the files of the copy are %q, want %q", slices.Sorted(maps.Keys(got)), slices.Sorted(maps.Keys(wantFiles)))
			}
			for _, h := range originals {
				if _, ok := reply.files[originalPath(h)]; !ok {
					t.Errorf("the reply has no original %x", h)
				}
			}
			// The connection goes on.
			c.mustSnapshot()
		})
	}
}

// TestBackupOrder checks the order of the entries: the position, the lines of the heads, the files/ directory with what is
// in it, and then logdb/ with the files of the engine, and the end last; and that every file and directory is made after its
// parent.
func TestBackupOrder(t *testing.T) {
	f := newFixture(t, nil)
	populate(t, f)
	r := f.connect(dbwire.RoleReader)
	reply, err := r.backup(nil)
	if err != nil {
		t.Fatal(err)
	}
	order := reply.order
	if order[0] != "position" || order[len(order)-1] != "end" {
		t.Fatalf("the entries begin with %q and end with %q, want the position and the end", order[0], order[len(order)-1])
	}
	if order[1] != "head" || order[2] != "head" || order[3] != "dir files" {
		t.Errorf("the entries after the position are %q, want the two heads and files/", order[1:4])
	}
	logdbAt := slices.Index(order, "dir logdb")
	if logdbAt < 0 {
		t.Fatalf("no entry for logdb/ in %q", order)
	}
	made := map[string]bool{".": true}
	for i, e := range order {
		kind, p, ok := strings.Cut(e, " ")
		if !ok || kind == "head" {
			continue
		}
		if parent := path.Dir(p); !made[parent] {
			t.Errorf("%s is made before its parent in %q", e, order)
		}
		if kind == "dir" {
			made[p] = true
		}
		if strings.HasPrefix(p, "files/") && i > logdbAt {
			t.Errorf("%s comes after logdb/", e)
		}
		if strings.HasPrefix(p, "logdb/") && i < logdbAt {
			t.Errorf("%s comes before logdb/", e)
		}
	}
	for _, p := range []string{"logdb/lock", "logdb/log", "logdb/objects", "logdb/head", "logdb/lock/writer"} {
		if !slices.Contains(order, "dir "+p) && !slices.Contains(order, "file "+p) {
			t.Errorf("the reply has no entry for %s", p)
		}
	}
}

// TestBackupSkipsTemps checks that the temporary files of PutOriginal are not sent, and the directories of that name, and
// what is neither a directory nor a regular file.
func TestBackupSkipsTemps(t *testing.T) {
	f := newDirFixture(t, nil)
	w := f.connect(dbwire.RoleWriter)
	h := mustPut(t, w, []byte("GIF89a"))
	files := filepath.Join(f.dir, "files")
	for _, name := range []string{".tmp-0123456789abcdef", ".tmp-x", "notes"} {
		if err := os.WriteFile(filepath.Join(files, name), []byte("data"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mkdir(t, filepath.Join(files, ".tmp-dir"))
	if err := os.WriteFile(filepath.Join(files, ".tmp-dir", "inside"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	mkdir(t, filepath.Join(files, "zz"))
	symlink(t, filepath.Join(files, "notes"), filepath.Join(files, "zz", "link"))
	mkfifo(t, filepath.Join(files, "zz", "pipe"))
	r := f.connect(dbwire.RoleReader)
	copyDir := filepath.Join(t.TempDir(), "copy")
	mkdir(t, copyDir)
	root, err := vfs.OpenRoot(copyDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	reply, err := r.backup(root)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range reply.order {
		if strings.Contains(e, "files/") {
			got = append(got, e)
		}
	}
	want := []string{"file files/notes", fmt.Sprintf("dir files/%x", h[:1]), fmt.Sprintf("file %s", originalPath(h)), "dir files/zz"}
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("the entries of files/ are %q, want %q", got, want)
	}
	// The copy of the directory on the OS is a data directory.
	srv, err := OpenDir(copyDir, f.opts)
	if err != nil {
		t.Fatalf("OpenDir() of the copy = %v", err)
	}
	g := &fixture{t: t, srv: srv, logs: f.logs}
	t.Cleanup(func() { _ = srv.Close() })
	if data, err := g.connect(dbwire.RoleReader).original(h); err != nil || string(data) != "GIF89a" {
		t.Errorf("the original of the copy reads %q, %v", data, err)
	}
	for _, name := range names(t, filepath.Join(copyDir, "files")) {
		if strings.HasPrefix(name, tempPrefix) {
			t.Errorf("the copy has %s", name)
		}
	}
}

// TestBackupError checks that an error in the middle ends the reply with an error frame, which has no data of the store,
// and that the connection goes on and the commits are not held.
func TestBackupError(t *testing.T) {
	f := newFixture(t, nil)
	originals := populate(t, f)
	f.fs.Inject(simfs.Fault{Match: func(op simfs.Op) bool {
		return op.Kind == simfs.OpOpen && op.Path == originalPath(originals[1])
	}, Err: syscall.EIO, Times: 1})
	r := f.connect(dbwire.RoleReader)
	_, err := r.backup(nil)
	re := wantCode(t, err, dbwire.CodeFailed)
	if !strings.Contains(re.Msg, "input/output error") || !strings.Contains(re.Msg, originalName(originals[1])) {
		t.Errorf("the text is %q, want the file and the error of the file system", re.Msg)
	}
	for _, v := range []string{"GIF89a", "wxyz", "1000", "audit head"} {
		if strings.Contains(re.Msg, v) {
			t.Errorf("the text %q has %q, which the store holds", re.Msg, v)
		}
	}
	r.mustSnapshot()
	w := f.connect(dbwire.RoleWriter)
	w.mustCommit(put(2, "after", "x"))
	if _, err := r.backup(nil); err != nil {
		t.Errorf("Backup after the error: %v", err)
	}
}

// TestBackupHeadBehind checks that a Backup fails, and says why, when the head file of the engine does not have the last
// commit, as when a write of it has failed, and that it does not hold the commits then; and that it works after the next
// commit.
func TestBackupHeadBehind(t *testing.T) {
	f := newFixture(t, nil)
	w := f.connect(dbwire.RoleWriter)
	w.mustCommit(put(2, "a", "1"))
	f.fs.Inject(simfs.Fault{Match: func(op simfs.Op) bool {
		return op.Kind == simfs.OpWrite && op.Path == "logdb/head"
	}, Err: syscall.EIO, Times: 1})
	w.mustCommit(put(2, "b", "2"))
	r := f.connect(dbwire.RoleReader)
	_, err := r.backup(nil)
	if re := wantCode(t, err, dbwire.CodeFailed); re.Msg != headBehindText {
		t.Errorf("the text is %q, want %q", re.Msg, headBehindText)
	}
	w.mustCommit(put(2, "c", "3"))
	copied := simfs.New("copy")
	reply, err := r.backup(copied)
	if err != nil {
		t.Fatalf("Backup after the next commit: %v", err)
	}
	if reply.pos.Seq != 3 {
		t.Errorf("the position is at %d, want 3", reply.pos.Seq)
	}
	want := f.dump()
	checkCopy(t, f, reply, copied, &want)
}

// TestBackupGateReleased checks that the commits are held only while the reader of the engine opens: a Backup that fails
// before the reader has opened leaves them free, and so does one that ends in the middle of its files.
func TestBackupGateReleased(t *testing.T) {
	f := newFixture(t, nil)
	populate(t, f)
	w := f.connect(dbwire.RoleWriter)
	r := f.connect(dbwire.RoleReader)
	if err := f.fs.Remove("logdb/head"); err != nil { // the reader cannot open
		t.Fatal(err)
	}
	_, err := r.backup(nil)
	wantCode(t, err, dbwire.CodeFailed)
	w.mustCommit(put(2, "after", "x"))
	if !f.srv.gate.TryLock() {
		t.Fatal("the gate is held after a Backup that failed")
	}
	f.srv.gate.Unlock()

	// The commits do not wait for the receiver to read the error either.
	r = f.connect(dbwire.RoleReader)
	_ = r.send(dbwire.KindBackup, nil)
	eventually(t, "the Backup to write its error", func() bool {
		return blockedIn("dbserver.(*session).backupRequest", "dbwire.(*Writer).Fail")
	})
	w.mustCommit(put(2, "while", "failing"))
}

// TestBackupCommitsGoOn checks that the commits wait only while the reader of the engine opens, and not while the files
// are sent: a receiver that has read the position and goes no further does not stop a commit; and that the copy is the
// store at the position, without that commit.
func TestBackupCommitsGoOn(t *testing.T) {
	f := newFixture(t, nil)
	populate(t, f)
	want := f.dump()
	w := f.connect(dbwire.RoleWriter)
	r := f.connect(dbwire.RoleReader)
	sent := r.send(dbwire.KindBackup, nil)
	if err := r.rd.Reply(); err != nil {
		t.Fatal(err)
	}
	d := &dbwire.Dec{R: r.rd}
	copied := simfs.New("copy")
	reply := &backupReply{files: map[string][]byte{}}
	var once sync.Once
	committed := make(chan error, 1)
	// The reply is read an entry at a time; after the first entry, which has the position, a commit is made, and the
	// receiver waits for it before it goes on.
	err := reply.read(d, copied, func(en dbwire.BackupEntry) {
		once.Do(func() {
			if en.Kind != dbwire.EntryPosition {
				t.Errorf("the first entry is of the kind %d, want the position", en.Kind)
			}
			go func() {
				_, _, err := w.tryCommit(put(2, "while", "sending"))
				committed <- err
			}()
			if err := <-committed; err != nil {
				t.Errorf("a commit while the files are sent: %v", err)
			}
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := <-sent; err != nil {
		t.Fatal(err)
	}
	if reply.pos.Seq != want.Seq {
		t.Errorf("the position is at %d, want %d", reply.pos.Seq, want.Seq)
	}
	checkCopy(t, f, reply, copied, &want)
	if after := f.dump(); after.Seq != want.Seq+1 {
		t.Errorf("the store has the sequence number %d, want %d", after.Seq, want.Seq+1)
	}
}

// TestBackupWhileWriting takes backups while a writer commits, and checks that each copy is the store at the commit of its
// position, whole, with the lines of the heads of that commit.
func TestBackupWhileWriting(t *testing.T) {
	f := newFixture(t, nil)
	populate(t, f)
	w := f.connect(dbwire.RoleWriter)
	stop := make(chan struct{})
	var committed atomic.Int64
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			seq, err := w.snapshot()
			if err != nil {
				t.Error(err)
				return
			}
			if _, _, err := w.commit(seq, put(2, fmt.Sprintf("k%d", seq), strings.Repeat("v", int(seq%7)*40)),
				put(dbwire.TableHeads, "activity", headLine(seq+1)), event("e")); err != nil {
				t.Error(err)
				return
			}
			committed.Add(1)
		}
	}()
	r := f.connect(dbwire.RoleReader)
	for i := range 6 {
		start := committed.Load()
		copied := simfs.New("copy")
		reply, err := r.backup(copied)
		if err != nil {
			t.Fatalf("Backup %d: %v", i, err)
		}
		g := checkCopy(t, f, reply, copied, nil)
		// The heads of that commit: the line of the log activity has the sequence number of the commit, if it is one of the
		// writer's, and the tables are those that the commits before it have made.
		c := g.connect(dbwire.RoleReader)
		c.mustSnapshot()
		if found, line := c.mustGet(dbwire.TableHeads, "activity"); !found || line != headLine(reply.pos.Seq) {
			t.Errorf("backup %d: the head of activity is %q, want %q", i, line, headLine(reply.pos.Seq))
		}
		eventually(t, "commits while the backups go on", func() bool { return committed.Load() > start+1 })
	}
	close(stop)
	wg.Wait()
}

// TestBackupConnectionEnds checks that a connection that ends in the middle of a Backup frees the commits and what the
// Backup holds: the engine's reader, its files, and the session.
func TestBackupConnectionEnds(t *testing.T) {
	f := newFixture(t, nil)
	populate(t, f)
	w := f.connect(dbwire.RoleWriter)
	r := f.connect(dbwire.RoleReader)
	_ = r.send(dbwire.KindBackup, nil)
	if err := r.rd.Reply(); err != nil {
		t.Fatal(err)
	}
	if _, ok := dbwire.ReadBackupEntry(&dbwire.Dec{R: r.rd}); !ok {
		t.Fatal("no first entry")
	}
	_ = r.conn.Close()
	eventually(t, "the session to end", func() bool {
		f.srv.mu.Lock()
		defer f.srv.mu.Unlock()
		return f.srv.readers == 0
	})
	w.mustCommit(put(2, "after", "x"))
	if !f.srv.gate.TryLock() {
		t.Fatal("the gate is held")
	}
	f.srv.gate.Unlock()
	// The readers of the engine are gone: a Backup works again.
	r = f.connect(dbwire.RoleReader)
	if _, err := r.backup(nil); err != nil {
		t.Errorf("Backup after a connection that ended: %v", err)
	}
}

// TestBackupNoSecrets checks that what the reply says of an error has no value of a row, an object or an original.
func TestBackupNoSecrets(t *testing.T) {
	f := newFixture(t, nil)
	w := f.connect(dbwire.RoleWriter)
	seq := w.mustSnapshot()
	if _, err := w.putObject(secret + strings.Repeat("o", 500)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.commit(seq, put(2, "k"+secret, "v"+secret), event("e"+secret)); err != nil {
		t.Fatal(err)
	}
	h := mustPut(t, w, []byte(secret))
	f.fs.Inject(simfs.Fault{Match: func(op simfs.Op) bool {
		return op.Kind == simfs.OpRead && op.Path == originalPath(h)
	}, Err: errors.New("failed"), Times: 1})
	r := f.connect(dbwire.RoleReader)
	_, err := r.backup(nil)
	re := wantCode(t, err, dbwire.CodeFailed)
	if strings.Contains(re.Msg, secret) {
		t.Errorf("the text %q has a value", re.Msg)
	}
	for _, l := range f.logs.all() {
		if strings.Contains(l, secret) {
			t.Errorf("the log has %q", l)
		}
	}
}

// TestBackupGate checks that the commits are held while the reader of the engine opens, until the position is known, and are
// free from then on: the gate is held at the first point of the hook, and let go at the second.
func TestBackupGate(t *testing.T) {
	var held, freed atomic.Int64
	var f *fixture
	f = newFixture(t, func(o *Options) {
		o.Hook = func(point string) error {
			switch point {
			case "backup:gate":
				if !f.srv.gate.TryLock() {
					held.Add(1)
				} else {
					f.srv.gate.Unlock()
				}
			case "backup:sending":
				if f.srv.gate.TryLock() {
					f.srv.gate.Unlock()
					freed.Add(1)
				}
			}
			return nil
		}
	})
	populate(t, f)
	r := f.connect(dbwire.RoleReader)
	if _, err := r.backup(nil); err != nil {
		t.Fatal(err)
	}
	if held.Load() != 1 || freed.Load() != 1 {
		t.Errorf("the gate was held at the first point %d times and free at the second %d times, want 1 and 1", held.Load(),
			freed.Load())
	}
}
