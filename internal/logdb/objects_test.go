package logdb

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/logdb/simfs"
)

// objData returns n bytes, which differ for each i.
func objData(i, n int) []byte {
	b := make([]byte, n)
	for j := range b {
		b[j] = byte(i*31 + j)
	}
	return b
}

// sameDir returns n data of size bytes whose objects have their files in the same directory.
func sameDir(n, size int) [][]byte {
	byDir := map[string][][]byte{}
	for i := 0; ; i++ {
		d := objData(i, size)
		dir := path.Dir(objName(sha256.Sum256(d)))
		if byDir[dir] = append(byDir[dir], d); len(byDir[dir]) == n {
			return byDir[dir]
		}
	}
}

// putObjects puts each of data as an object in one Update, and returns their hashes.
func putObjects(t *testing.T, db *DB, data ...[]byte) [][32]byte {
	t.Helper()
	var hashes [][32]byte
	err := db.Update(context.Background(), func(tx *Tx) error {
		hashes = nil
		for _, d := range data {
			h, err := tx.PutObject(d)
			if err != nil {
				return err
			}
			hashes = append(hashes, h)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return hashes
}

// readObject returns the data of the object hash, read in a View.
func readObject(db *DB, hash [32]byte) ([]byte, error) {
	var data []byte
	err := db.View(context.Background(), func(tx *Tx) error {
		var err error
		data, err = tx.Object(hash)
		return err
	})
	return data, err
}

// objectList returns the objects of db, as lines "<hash>:<size>" in order.
func objectList(t *testing.T, db *DB) string {
	t.Helper()
	var lines []string
	err := db.View(context.Background(), func(tx *Tx) error {
		tx.Objects(func(h [32]byte, size int64) bool {
			lines = append(lines, fmt.Sprintf("%x:%d", h, size))
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(lines, "\n")
}

// checkObjects checks that the data of every object of db has its hash and size, and that Verify passes.
func checkObjects(t *testing.T, db *DB) {
	t.Helper()
	err := db.View(context.Background(), func(tx *Tx) error {
		var err error
		tx.Objects(func(h [32]byte, size int64) bool {
			var data []byte
			if data, err = tx.Object(h); err == nil && (sha256.Sum256(data) != h || int64(len(data)) != size) {
				err = fmt.Errorf("object %x of %d bytes: %d bytes, another hash", h, size, len(data))
			}
			return err == nil
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Verify(context.Background()); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

// objectFiles returns what reads see of the files in objects/.
func objectFiles(fsys *simfs.FS) map[string]string {
	m := map[string]string{}
	fsys.Walk(func(p string, data []byte) {
		if strings.HasPrefix(p, "objects/") {
			m[p] = string(data)
		}
	})
	return m
}

func objectFile(data []byte) string {
	return string(appendObjHeader(nil)) + string(data)
}

func temps(fsys *simfs.FS) []string {
	var names []string
	for p := range objectFiles(fsys) {
		if strings.Contains(p, "/.tmp-") {
			names = append(names, p)
		}
	}
	return names
}

// tempOp returns a Fault.Match for the operations of kind on temporary files of objects.
func tempOp(kind simfs.OpKind) func(simfs.Op) bool {
	return func(op simfs.Op) bool { return op.Kind == kind && strings.Contains(op.Path, "/.tmp-") }
}

// objDirOp returns a Fault.Match for the operations of kind on the directories in objects/.
func objDirOp(kind simfs.OpKind) func(simfs.Op) bool {
	return func(op simfs.Op) bool {
		return op.Kind == kind && strings.HasPrefix(op.Path, "objects/") && strings.Count(op.Path, "/") == 1
	}
}

// recordOps records the operations of fsys from now on, which the returned function returns.
func recordOps(fsys *simfs.FS) func() []simfs.Op {
	var ops []simfs.Op
	fsys.Inject(simfs.Fault{Err: errors.New("never"), Match: func(op simfs.Op) bool {
		ops = append(ops, op)
		return false
	}})
	return func() []simfs.Op { return slices.Clone(ops) }
}

func countOps(ops []simfs.Op, match func(simfs.Op) bool) int {
	n := 0
	for _, op := range ops {
		if match(op) {
			n++
		}
	}
	return n
}

func TestPlacements(t *testing.T) {
	sizes := []int{0, 1, 99, 100, 1000}
	for _, c := range []struct {
		name  string
		o     Options
		files []bool // by size
	}{
		{"inline", Options{}, []bool{false, false, false, false, false}},
		{"files", Options{Placement: Files}, []bool{true, true, true, true, true}},
		{"hybrid", Options{Placement: Hybrid, Threshold: 100}, []bool{false, false, false, true, true}},
	} {
		t.Run(c.name, func(t *testing.T) {
			fsys, db := newSim(t, c.o)
			putKey(t, db, 1, "first", "record") // so that the objects are not at the start of the segment
			var data [][]byte
			for i, n := range sizes {
				data = append(data, objData(i, n))
			}
			hashes := putObjects(t, db, data...)
			want := map[string]string{}
			var inline, files int
			for i, h := range hashes {
				if h != sha256.Sum256(data[i]) {
					t.Errorf("PutObject returned %x", h)
				}
				if c.files[i] {
					want[objName(h)] = objectFile(data[i])
					files++
				} else {
					inline++
				}
			}
			if got := objectFiles(fsys); !maps.Equal(got, want) {
				t.Errorf("object files %q, want %q", slices.Sorted(maps.Keys(got)), slices.Sorted(maps.Keys(want)))
			}
			for name := range want {
				for p, perm := range map[string]fs.FileMode{name: 0o600, path.Dir(name): fs.ModeDir | 0o700} {
					if info, err := fsys.Lstat(p); err != nil || info.Mode() != perm {
						t.Errorf("%s: %v, %v", p, info.Mode(), err)
					}
				}
			}
			check := func(db *DB) {
				t.Helper()
				for i, h := range hashes {
					if got, err := readObject(db, h); err != nil || !bytes.Equal(got, data[i]) {
						t.Errorf("object %d of %d bytes: %d bytes, %v", i, len(data[i]), len(got), err)
					}
				}
				if s := db.Stats(); s.InlineObjects != inline || s.FileObjects != files {
					t.Errorf("Stats: %d inline, %d in files", s.InlineObjects, s.FileObjects)
				}
				checkObjects(t, db)
			}
			check(db)
			// The objects are durable once Update has returned.
			check(reopen(t, fsys.Crash(loseAll), c.o))
			closeDB(t, db)
			check(reopen(t, fsys, Options{}))
		})
	}
	t.Run("the default threshold", func(t *testing.T) {
		fsys, db := newSim(t, Options{Placement: Hybrid})
		hashes := putObjects(t, db, objData(1, 64<<10-1), objData(2, 64<<10))
		if got := slices.Sorted(maps.Keys(objectFiles(fsys))); !slices.Equal(got, []string{objName(hashes[1])}) {
			t.Errorf("object files %q", got)
		}
	})
}

// TestObjectFilesBeforeRecord checks that the file of an object is durable, with its name, before the record that
// names it is written.
func TestObjectFilesBeforeRecord(t *testing.T) {
	data := sameDir(2, 50)
	fsys := simfs.New(simDir)
	var points []string
	o := Options{Placement: Files, Hook: func(point string) error {
		if point == "update:objects" || point == "update:written" {
			points = append(points, point)
			crashed := fsys.Crash(loseAll)
			for _, d := range data {
				if got, want := objectFiles(crashed)[objName(sha256.Sum256(d))], objectFile(d); got != want {
					t.Errorf("at %s, a crash leaves %d bytes of the object file, want %d", point, len(got), len(want))
				}
			}
			if db := reopen(t, crashed, Options{}); objectList(t, db) != "" {
				t.Errorf("at %s, a crash leaves the record", point)
			}
		}
		return nil
	}}
	db, err := Create(fsys, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	hashes := putObjects(t, db, data...)
	if len(points) != 2 {
		t.Errorf("points %q", points)
	}
	got, err := readObject(reopen(t, fsys.Crash(loseAll), Options{}), hashes[1])
	if err != nil || !bytes.Equal(got, data[1]) {
		t.Errorf("after a crash: %q, %v", got, err)
	}
}

func TestObjectFilesOS(t *testing.T) {
	dir := t.TempDir()
	fsys := osFS(t, dir)
	db, err := Create(fsys, Options{Placement: Hybrid, Threshold: 10})
	if err != nil {
		t.Fatal(err)
	}
	data := [][]byte{objData(1, 5), objData(2, 500)}
	hashes := putObjects(t, db, data...)
	name := objName(hashes[1])
	want := strings.Join([]string{
		"head 600",
		"lock/ 700", "lock/entry 600", "lock/readers 600", "lock/writer 600",
		"log/ 700", "log/0000000000000001-0000000000000001.log 600",
		"objects/ 700", path.Dir(name) + "/ 700", name + " 600",
	}, "\n")
	if got := osTree(t, dir); got != want {
		t.Errorf("files:\n%s\nwant:\n%s", got, want)
	}
	if b, err := os.ReadFile(filepath.Join(dir, name)); err != nil || string(b) != objectFile(data[1]) {
		t.Errorf("%s: %q, %v", name, b, err)
	}
	closeDB(t, db)
	db = reopen(t, fsys, Options{})
	for i, h := range hashes {
		if got, err := readObject(db, h); err != nil || !bytes.Equal(got, data[i]) {
			t.Errorf("object %d: %q, %v", i, got, err)
		}
	}
	checkObjects(t, db)
}

func TestDedup(t *testing.T) {
	ctx := context.Background()
	for _, o := range []Options{{}, {Placement: Files}} {
		t.Run(fmt.Sprintf("placement %d", o.Placement), func(t *testing.T) {
			fsys, db := newSim(t, o)
			ops := recordOps(fsys)
			x, y := objData(1, 300), objData(2, 300)
			var hashes [][32]byte
			if err := db.Update(ctx, func(tx *Tx) error {
				for _, d := range [][]byte{x, y, x, slices.Clone(y)} {
					h, err := tx.PutObject(d)
					if err != nil {
						return err
					}
					hashes = append(hashes, h)
				}
				if len(tx.ops) != 2 {
					t.Errorf("%d ops", len(tx.ops))
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if hashes[0] != hashes[2] || hashes[1] != hashes[3] || hashes[0] == hashes[1] {
				t.Errorf("hashes %x", hashes)
			}
			want := 0
			if o.Placement == Files {
				want = 2
			}
			if n := countOps(ops(), tempOp(simfs.OpCreate)); n != want {
				t.Errorf("%d object files written", n)
			}
			_, offs := frames(t, fsys, seg1)
			again := func(db *DB) {
				t.Helper()
				counted := fsys.Counted()
				if got := putObjects(t, db, y, x); got[0] != hashes[1] || got[1] != hashes[0] {
					t.Errorf("hashes %x", got)
				}
				if fsys.Counted() != counted {
					t.Error("an Update of objects that the store has wrote")
				}
			}
			again(db)
			closeDB(t, db)
			db = reopen(t, fsys, o)
			again(db)
			// With other writes, the record has only those.
			if err := db.Update(ctx, func(tx *Tx) error {
				if _, err := tx.PutObject(x); err != nil {
					return err
				}
				return tx.Put(1, []byte("k"), nil)
			}); err != nil {
				t.Fatal(err)
			}
			n := int64(frameLen(len(appendCommit(nil, 2, []op{{code: opPut, table: 1, key: "k"}}))))
			if _, now := frames(t, fsys, seg1); len(now) != len(offs)+1 || fileSize(t, fsys, seg1)-now[len(offs)] != n {
				t.Errorf("records at %d, %d bytes", now, fileSize(t, fsys, seg1))
			}
			want2 := []string{fmt.Sprintf("%x:300", hashes[0]), fmt.Sprintf("%x:300", hashes[1])}
			slices.Sort(want2)
			if got := objectList(t, db); got != strings.Join(want2, "\n") {
				t.Errorf("objects:\n%s", got)
			}
		})
	}
}

func TestObjectMissing(t *testing.T) {
	fsys, db := newSim(t, Options{Placement: Files})
	h := putObjects(t, db, objData(1, 10))[0]
	// A file of an object that the store has, which cannot be opened, is not a missing object.
	for errno, want := range map[syscall.Errno]string{
		syscall.ENOENT: simPath(objName(h)) + ": missing file",
		syscall.EIO:    simPath(objName(h)) + ": cannot read: input/output error",
	} {
		fsys.Inject(simfs.Fault{Match: on(simfs.OpOpen, objName(h)), Err: errno, Times: 1})
		if _, err := readObject(db, h); errors.Is(err, fs.ErrNotExist) || err == nil || err.Error() != want {
			t.Errorf("Object when the open fails with %v: %v", errno, err)
		}
	}
	missing := sha256.Sum256([]byte("missing"))
	want := fmt.Sprintf("object %x: file does not exist", missing)
	for _, update := range []bool{false, true} {
		f := func(tx *Tx) error {
			if tx.HasObject(missing) {
				t.Error("HasObject of a missing object")
			}
			data, err := tx.Object(missing)
			if !errors.Is(err, fs.ErrNotExist) || err.Error() != want || data != nil {
				t.Errorf("Object of a missing object: %q, %v", data, err)
			}
			return nil
		}
		var err error
		if update {
			err = db.Update(context.Background(), f)
		} else {
			err = db.View(context.Background(), f)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestTxObjects(t *testing.T) {
	ctx := context.Background()
	_, db := newSim(t, Options{Placement: Hybrid, Threshold: 100})
	var all [][]byte
	for i := range 8 {
		all = append(all, objData(i, 50+50*i))
	}
	base := putObjects(t, db, all[:4]...)
	err := db.Update(ctx, func(tx *Tx) error {
		input := slices.Clone(all[4])
		h, err := tx.PutObject(input)
		if err != nil {
			return err
		}
		input[0]++ // PutObject has copied it
		got, err := tx.Object(h)
		if err != nil || !bytes.Equal(got, all[4]) || !tx.HasObject(h) {
			t.Errorf("Object of an object of the Update: %v", err)
		}
		got[0]++ // Object returns a copy
		if again, _ := tx.Object(h); !bytes.Equal(again, all[4]) {
			t.Error("Object returned the data that the Update keeps")
		}
		for _, d := range all[5:] {
			if _, err := tx.PutObject(d); err != nil {
				return err
			}
		}
		if got, err := tx.Object(base[2]); err != nil || !bytes.Equal(got, all[2]) || !tx.HasObject(base[2]) {
			t.Errorf("Object of an object of the store: %v", err)
		}
		// Objects merges those of the store and those of the Update, in order.
		var want []string
		for _, d := range all {
			want = append(want, fmt.Sprintf("%x:%d", sha256.Sum256(d), len(d)))
		}
		slices.Sort(want)
		var got2 []string
		tx.Objects(func(h [32]byte, size int64) bool {
			got2 = append(got2, fmt.Sprintf("%x:%d", h, size))
			return true
		})
		if !slices.Equal(got2, want) {
			t.Errorf("Objects:\n%s\nwant:\n%s", strings.Join(got2, "\n"), strings.Join(want, "\n"))
		}
		for limit := range len(want) {
			n := 0
			tx.Objects(func([32]byte, int64) bool {
				n++
				return n <= limit
			})
			if n != limit+1 {
				t.Errorf("Objects stopping after %d: %d calls", limit+1, n)
			}
		}
		if got := objectList(t, db); strings.Count(got, "\n") != 3 {
			t.Errorf("a View during the Update sees:\n%s", got)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(objectList(t, db), "\n") + 1; n != len(all) {
		t.Errorf("%d objects", n)
	}
	checkObjects(t, db)
}

func TestObjectTxErrors(t *testing.T) {
	ctx := context.Background()
	_, db := newSim(t, Options{})
	h := putObjects(t, db, objData(1, 10))[0]
	var ended *Tx
	if err := db.View(ctx, func(tx *Tx) error {
		ended = tx
		if _, err := tx.PutObject(nil); !errors.Is(err, ErrReadOnly) {
			t.Errorf("PutObject in a View: %v", err)
		}
		if err := tx.AppendEvent(nil); !errors.Is(err, ErrReadOnly) {
			t.Errorf("AppendEvent in a View: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, update := range []bool{false, true} {
		if update {
			if err := db.Update(ctx, func(tx *Tx) error {
				ended = tx
				_, err := tx.PutObject([]byte("discarded"))
				return err
			}); err != nil {
				t.Fatal(err)
			}
		}
		if got, err := ended.PutObject(nil); !errors.Is(err, errEnded) || got != [32]byte{} {
			t.Errorf("PutObject after the end: %x, %v", got, err)
		}
		if err := ended.AppendEvent(nil); !errors.Is(err, errEnded) {
			t.Errorf("AppendEvent after the end: %v", err)
		}
		if data, err := ended.Object(h); !errors.Is(err, errEnded) || data != nil {
			t.Errorf("Object after the end: %q, %v", data, err)
		}
		if ended.HasObject(h) {
			t.Error("HasObject after the end")
		}
		ended.Objects(func([32]byte, int64) bool {
			t.Error("Objects after the end")
			return true
		})
	}
	if got := objectList(t, db); strings.Count(got, "\n") != 1 {
		t.Errorf("objects:\n%s", got)
	}
}

func TestObjectFaults(t *testing.T) {
	ctx := context.Background()
	hookErr := errors.New("hook failed")
	for _, c := range []struct {
		name   string
		fault  simfs.Fault
		hook   bool // the hook update:objects fails instead
		failed bool
	}{
		{name: "mkdir", fault: simfs.Fault{Match: objDirOp(simfs.OpMkdir), Err: syscall.EIO}},
		{name: "temp create", fault: simfs.Fault{Match: tempOp(simfs.OpCreate), Err: syscall.ENOSPC}},
		{name: "temp write", fault: simfs.Fault{Match: tempOp(simfs.OpWrite), Err: syscall.EIO}},
		{name: "short temp write", fault: simfs.Fault{Match: tempOp(simfs.OpWrite), Err: syscall.ENOSPC, Short: 10}},
		{name: "rename", fault: simfs.Fault{Match: tempOp(simfs.OpRename), Err: syscall.EIO}},
		{name: "hook", hook: true},
		{name: "temp name taken", fault: simfs.Fault{Match: tempOp(simfs.OpCreate), Err: syscall.EEXIST}},
		{name: "temp sync", fault: simfs.Fault{Match: tempOp(simfs.OpSync), Err: syscall.EIO}, failed: true},
		{name: "dropped temp sync", fault: simfs.Fault{Match: tempOp(simfs.OpSync), Err: syscall.EIO, Drop: true}, failed: true},
		{name: "sync of objects", fault: simfs.Fault{Match: on(simfs.OpSyncDir, "objects"), Err: syscall.EIO}, failed: true},
		{name: "sync of the directory", fault: simfs.Fault{Match: objDirOp(simfs.OpSyncDir), Err: syscall.EIO}, failed: true},
		{name: "dropped sync of the directory", fault: simfs.Fault{Match: objDirOp(simfs.OpSyncDir), Err: syscall.EIO,
			Drop: true}, failed: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			failHook := false
			o := Options{Placement: Files, Hook: func(point string) error {
				if failHook && point == "update:objects" {
					return hookErr
				}
				return nil
			}}
			fsys, db := newSim(t, o)
			putKey(t, db, 1, "a", "1")
			before := objectList(t, db)
			data := objData(1, 200)
			update := func(tx *Tx) error {
				if _, err := tx.PutObject(data); err != nil {
					return err
				}
				return tx.Put(1, []byte("b"), []byte("2"))
			}
			cause := hookErr
			if c.hook {
				failHook = true
			} else {
				f := c.fault
				f.Times = 1
				fsys.Inject(f)
				cause = c.fault.Err
			}
			err := db.Update(ctx, update)
			failHook = false
			if got := objectList(t, db); err != nil && (got != before || dump(t, db) != "1/a=1") {
				t.Errorf("the failed Update is seen: %s", got)
			}
			if c.failed {
				checkFailed(t, db, err, cause)
				closeDB(t, db)
				// The record was not written: no crash has the object.
				for _, m := range []simfs.Model{keepAll(fsys), loseAll} {
					db := reopen(t, fsys.Crash(m), o)
					if got := objectList(t, db); got != before || dump(t, db) != "1/a=1" {
						t.Errorf("after the crash %s: %s", m.Name, got)
					}
					checkObjects(t, db)
					closeDB(t, db)
				}
				return
			}
			if c.name == "temp name taken" { // another name is tried
				if err != nil || len(objectList(t, db)) == 0 {
					t.Fatalf("Update: %v", err)
				}
				checkObjects(t, db)
				return
			}
			if !errors.Is(err, cause) || errors.Is(err, ErrFailed) {
				t.Fatalf("Update: %v", err)
			}
			if names := temps(fsys); len(names) != 0 {
				t.Errorf("temporary files are left: %q", names)
			}
			if err := db.Update(ctx, update); err != nil {
				t.Fatalf("the Update again: %v", err)
			}
			for _, m := range []simfs.Model{keepAll(fsys), loseAll} {
				db := reopen(t, fsys.Crash(m), o)
				if got, err := readObject(db, sha256.Sum256(data)); err != nil || !bytes.Equal(got, data) {
					t.Errorf("after the crash %s: %v", m.Name, err)
				}
				checkObjects(t, db)
				closeDB(t, db)
			}
		})
	}
}

// TestObjectDirDurable checks that a directory of objects that is there already is synced in objects/ before a
// record names an object in it, unless the store knows that its name is durable.
func TestObjectDirDurable(t *testing.T) {
	ctx := context.Background()
	data := sameDir(3, 50)
	dir := path.Dir(objName(sha256.Sum256(data[0])))
	o := Options{Placement: Files}
	check := func(t *testing.T, fsys *simfs.FS, d []byte) {
		t.Helper()
		db := reopen(t, fsys.Crash(loseAll), o)
		if got, err := readObject(db, sha256.Sum256(d)); err != nil || !bytes.Equal(got, d) {
			t.Errorf("after a crash: %v", err)
		}
		checkObjects(t, db)
	}
	t.Run("made by an Update that failed", func(t *testing.T) {
		fsys, db := newSim(t, o)
		fsys.Inject(simfs.Fault{Match: tempOp(simfs.OpWrite), Err: syscall.EIO, Times: 1})
		err := db.Update(ctx, func(tx *Tx) error {
			_, err := tx.PutObject(data[0])
			return err
		})
		if !errors.Is(err, syscall.EIO) || !exists(t, fsys, dir) {
			t.Fatalf("the failed Update: %v; %s made: %v", err, dir, exists(t, fsys, dir))
		}
		ops := recordOps(fsys)
		putObjects(t, db, data[1])
		if n := countOps(ops(), on(simfs.OpSyncDir, "objects")); n != 1 {
			t.Errorf("objects synced %d times", n)
		}
		check(t, fsys, data[1])
		// Once synced, the directory is known to be durable.
		ops = recordOps(fsys)
		putObjects(t, db, data[2])
		onObjects := func(op simfs.Op) bool { return op.Kind == simfs.OpMkdir || on(simfs.OpSyncDir, "objects")(op) }
		if n := countOps(ops(), onObjects); n != 0 {
			t.Errorf("%d operations on objects", n)
		}
		check(t, fsys, data[2])
	})
	t.Run("made by another process", func(t *testing.T) {
		fsys, db := newSim(t, o)
		closeDB(t, db)
		if err := fsys.Mkdir(dir, 0o700); err != nil { // a writer that was killed before it synced objects
			t.Fatal(err)
		}
		db = reopen(t, fsys, o)
		putObjects(t, db, data[1])
		check(t, fsys, data[1])
	})
}

func TestObjectsAcrossSegments(t *testing.T) {
	o := Options{SegmentBytes: 600}
	fsys, db := newSim(t, o)
	var hashes [][32]byte
	for i := range 12 {
		hashes = append(hashes, putObjects(t, db, objData(i, 100+i))...)
	}
	if n := len(logNames(t, fsys)); n < 3 {
		t.Fatalf("%d segments", n)
	}
	check := func(db *DB) {
		t.Helper()
		for i, h := range hashes {
			if got, err := readObject(db, h); err != nil || !bytes.Equal(got, objData(i, 100+i)) {
				t.Errorf("object %d: %v", i, err)
			}
		}
		checkObjects(t, db)
	}
	check(db)
	closeDB(t, db)
	db = reopen(t, fsys, o)
	check(db)
	db.handles.mu.Lock()
	n := len(db.handles.files)
	db.handles.mu.Unlock()
	if n != len(logNames(t, fsys)) {
		t.Errorf("%d open segments", n)
	}
	// A missing segment is damage, not a missing object.
	closeDB(t, db)
	db = reopen(t, fsys, o)
	removeFile(t, fsys, seg1)
	if _, err := readObject(db, hashes[0]); errors.Is(err, fs.ErrNotExist) || err == nil ||
		err.Error() != simPath(seg1)+": missing file" {
		t.Errorf("Object in a missing segment: %v", err)
	}
}

// TestCloseReleasesFiles checks that Close closes every file that the engine has opened, also those that a View
// opened after Close was called.
func TestCloseReleasesFiles(t *testing.T) {
	if _, err := os.Stat("/proc/self/fd"); err != nil {
		t.Skip("no /proc/self/fd")
	}
	ctx := context.Background()
	open := func() int {
		entries, err := os.ReadDir("/proc/self/fd")
		if err != nil {
			t.Fatal(err)
		}
		return len(entries)
	}
	use := func(dir string) {
		fsys := osFS(t, dir)
		o := Options{SegmentBytes: 300, Placement: Hybrid, Threshold: 150}
		db, err := Create(fsys, o)
		if err != nil {
			t.Fatal(err)
		}
		var hashes [][32]byte
		for i := range 8 {
			hashes = append(hashes, putObjects(t, db, objData(i, 100+20*i))...)
		}
		if err := compactDB(db, Rewrite{}); err != nil { // the files of both generations are closed too
			t.Fatal(err)
		}
		for i := 8; i < 10; i++ {
			hashes = append(hashes, putObjects(t, db, objData(i, 100+20*i))...)
		}
		read, closed, done := make(chan struct{}), make(chan struct{}), make(chan error)
		go func() {
			done <- db.View(ctx, func(tx *Tx) error {
				close(read)
				<-closed
				for _, h := range hashes {
					if _, err := tx.Object(h); err != nil {
						return err
					}
				}
				return nil
			})
		}()
		<-read
		closeDB(t, db)
		close(closed)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		db = reopen(t, fsys, o)
		for _, h := range hashes {
			if _, err := readObject(db, h); err != nil {
				t.Fatal(err)
			}
		}
		closeDB(t, db)
		if err := fsys.Close(); err != nil {
			t.Fatal(err)
		}
	}
	use(t.TempDir()) // the runtime may open files once
	before := open()
	use(t.TempDir())
	if after := open(); after != before {
		t.Errorf("%d files open after Close, %d before", after, before)
	}
}

// TestCloseDuringView checks that a View that runs when Close is called can go on reading objects, and that the files
// are closed when it ends.
func TestCloseDuringView(t *testing.T) {
	ctx := context.Background()
	_, db := newSim(t, Options{SegmentBytes: 300, Placement: Hybrid, Threshold: 150})
	var data [][]byte
	var hashes [][32]byte
	for i := range 6 {
		data = append(data, objData(i, 100+20*i))
		hashes = append(hashes, putObjects(t, db, data[i])...)
	}
	read, closed := make(chan struct{}), make(chan struct{})
	done := make(chan error)
	go func() {
		done <- db.View(ctx, func(tx *Tx) error {
			if _, err := tx.Object(hashes[5]); err != nil {
				return err
			}
			close(read)
			<-closed
			for i, h := range hashes {
				if got, err := tx.Object(h); err != nil || !bytes.Equal(got, data[i]) {
					return fmt.Errorf("object %d: %w", i, err)
				}
			}
			return nil
		})
	}()
	<-read
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.View(ctx, func(*Tx) error { return nil }); !errors.Is(err, errClosed) {
		t.Errorf("a View after Close: %v", err)
	}
	close(closed)
	if err := <-done; err != nil {
		t.Errorf("the View that ran during Close: %v", err)
	}
	db.handles.mu.Lock()
	files := db.handles.files
	db.handles.mu.Unlock()
	if files != nil {
		t.Errorf("%d files are open after the last View", len(files))
	}
}

func TestAcceptReadsObjects(t *testing.T) {
	o := Options{Placement: Hybrid, Threshold: 100}
	fsys, db := newSim(t, o)
	data := [][]byte{objData(1, 10), objData(2, 200)}
	hashes := putObjects(t, db, data...)
	closeDB(t, db)
	o.Accept = func(tx *Tx) error {
		for i, h := range hashes {
			if got, err := tx.Object(h); err != nil || !bytes.Equal(got, data[i]) || !tx.HasObject(h) {
				t.Errorf("object %d in Accept: %v", i, err)
			}
		}
		if _, err := tx.PutObject(nil); !errors.Is(err, ErrReadOnly) {
			t.Errorf("PutObject in Accept: %v", err)
		}
		return errors.New("refused")
	}
	if _, err := Open(fsys, o); err == nil || err.Error() != "refused" {
		t.Errorf("Open: %v", err)
	}
	o.Accept = nil
	checkObjects(t, reopen(t, fsys, o))
}

func TestOptionsErrors(t *testing.T) {
	for _, c := range []struct {
		o    Options
		want string
	}{
		{Options{Placement: 3}, "unknown placement 3"},
		{Options{Placement: -1}, "unknown placement -1"},
		{Options{Threshold: -1}, "negative threshold -1"},
	} {
		fsys := simfs.New(simDir)
		if _, err := Create(fsys, c.o); err == nil || err.Error() != c.want {
			t.Errorf("Create: %v, want %s", err, c.want)
		}
		if len(fileMap(fsys)) != 0 {
			t.Error("Create made files")
		}
		db, err := Create(fsys, Options{})
		if err != nil {
			t.Fatal(err)
		}
		closeDB(t, db)
		if _, err := Open(fsys, c.o); err == nil || err.Error() != c.want {
			t.Errorf("Open: %v, want %s", err, c.want)
		}
	}
}

// TestOptionsDefaults checks the defaults of the zero Options, which the store uses but for the placement (3.4).
func TestOptionsDefaults(t *testing.T) {
	var o Options
	if err := o.fill(); err != nil {
		t.Fatal(err)
	}
	if o.Placement != Inline || o.Threshold != 64<<10 || o.SegmentBytes != 64<<20 || o.WriterWait != 2*time.Second ||
		o.CheckpointBytes != 64<<20 || o.SyncCheckpoints {
		t.Errorf("the defaults are placement %d, threshold %d, segments %d, writer wait %v, checkpoints %d (sync %t); "+
			"want 0, 65536, 67108864, 2s, 67108864 (false)", o.Placement, o.Threshold, o.SegmentBytes, o.WriterWait,
			o.CheckpointBytes, o.SyncCheckpoints)
	}
}

// stateOf returns the tables, the objects and the events of db, and checks its objects.
func stateOf(t *testing.T, db *DB) string {
	t.Helper()
	checkObjects(t, db)
	return dump(t, db) + "\nobjects:\n" + objectList(t, db) + "\nevents:\n" + eventLines(t, db)
}

// TestCrashWithObjects crashes after each operation of an Update with objects and an event, and checks the crash models
// that simfs.Models gives: Open never refuses, every object that the store has is there with its data, and the Update
// is there or not at all.
func TestCrashWithObjects(t *testing.T) {
	ctx := context.Background()
	mates := sameDir(2, 150)
	for _, c := range []struct {
		name string
		o    Options
	}{
		{"files", Options{Placement: Files}},
		{"hybrid with a rollover", Options{Placement: Hybrid, Threshold: 100, SegmentBytes: 200}},
	} {
		t.Run(c.name, func(t *testing.T) {
			setup := func() (*simfs.FS, *DB) {
				fsys, db := newSim(t, c.o)
				if err := db.Update(ctx, func(tx *Tx) error {
					_, err := tx.PutObject(mates[0]) // its directory is then known to be durable
					return errors.Join(err, tx.Put(1, []byte("a"), []byte("1")))
				}); err != nil {
					t.Fatal(err)
				}
				return fsys, db
			}
			step := func(db *DB) error {
				return db.Update(ctx, func(tx *Tx) error {
					var errs []error
					for _, d := range [][]byte{mates[1], objData(7, 300), objData(8, 20)} {
						_, err := tx.PutObject(d)
						errs = append(errs, err)
					}
					return errors.Join(append(errs, tx.AppendEvent([]byte("e")), tx.Put(1, []byte("b"), []byte("2")))...)
				})
			}
			state := func(fsys *simfs.FS) string {
				db, err := Open(fsys, c.o)
				if err != nil {
					t.Fatalf("Open: %v", err)
				}
				defer closeDB(t, db)
				return stateOf(t, db)
			}
			fsys, db := setup()
			before, start, segs := stateOf(t, db), fsys.Counted(), len(logNames(t, fsys))
			if err := step(db); err != nil {
				t.Fatal(err)
			}
			ops := fsys.Counted() - start
			after := state(copyFS(fsys))
			if c.o.SegmentBytes > 0 && len(logNames(t, fsys)) == segs {
				t.Fatal("no rollover")
			}
			crashes := 0
			for k := range ops + 1 {
				fsys, db := setup()
				fsys.CrashAt(fsys.Counted() + k)
				err := step(db)
				for _, m := range simfs.Models(fsys.Pending(), 8, nil) {
					got := state(fsys.Crash(m))
					if got != after && (err == nil || got != before) {
						t.Errorf("a crash after %d of %d operations (the Update returned %v), %s:\n%s", k, ops, err,
							m.Name, got)
					}
					crashes++
				}
			}
			t.Logf("%d crash points, %d crashes", ops+1, crashes)
		})
	}
}

func TestVerify(t *testing.T) {
	ctx := context.Background()
	o := Options{Placement: Hybrid, Threshold: 100}
	base, db := newSim(t, o)
	inline, file := objData(1, 50), objData(2, 200)
	if err := db.Update(ctx, func(tx *Tx) error {
		_, err1 := tx.PutObject(inline)
		_, err2 := tx.PutObject(file)
		return errors.Join(err1, err2, tx.Put(1, []byte("k1"), []byte("v1")), tx.AppendEvent([]byte("e1")))
	}); err != nil {
		t.Fatal(err)
	}
	putKey(t, db, 2, "k2", "v2")
	counted := base.Counted()
	if err := db.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	if base.Counted() != counted {
		t.Error("Verify wrote")
	}
	closeDB(t, db)
	ih, fh := sha256.Sum256(inline), sha256.Sum256(file)
	fname, ioff := objName(fh), inlineOff(db, ih)
	changed := sha256.Sum256(append([]byte{file[0] ^ 1}, file[1:]...))
	_, offs := frames(t, base, seg1)
	const differs = simDir + ": the state in memory differs from the log: "
	for _, c := range []struct {
		name   string
		change func(fsys *simfs.FS, db *DB)
		want   string
	}{
		{"a byte of an inline object", func(fsys *simfs.FS, db *DB) {
			loc, _ := db.cur.Load().objects.Get(string(ih[:]))
			b := fileData(t, fsys, seg1)
			b[loc.off+3] ^= 1
			setFile(t, fsys, seg1, b)
			// Reads see what is on disk.
			if got, err := readObject(db, ih); err != nil || got[3] != inline[3]^1 {
				t.Errorf("the changed inline object: %q, %v", got, err)
			}
		}, fmt.Sprintf("%s: corrupt at offset %d: bad record CRC", simPath(seg1), offs[0])},
		{"a missing object file", func(fsys *simfs.FS, db *DB) {
			removeFile(t, fsys, fname)
			_, err := readObject(db, fh)
			if errors.Is(err, fs.ErrNotExist) || err == nil || err.Error() != simPath(fname)+": missing file" {
				t.Errorf("Object: %v", err)
			}
		}, simPath(fname) + ": missing file"},
		{"a byte of an object file", func(fsys *simfs.FS, db *DB) {
			b := fileData(t, fsys, fname)
			b[objHeaderSize] ^= 1
			setFile(t, fsys, fname, b)
		}, fmt.Sprintf("object %x: the data has the hash %x", fh, changed)},
		{"a short object file", func(fsys *simfs.FS, db *DB) {
			setFile(t, fsys, fname, fileData(t, fsys, fname)[:objHeaderSize+199])
		}, simPath(fname) + ": corrupt: has 215 bytes, want 216"},
		{"a long object file", func(fsys *simfs.FS, db *DB) {
			setFile(t, fsys, fname, append(fileData(t, fsys, fname), 0))
		}, simPath(fname) + ": corrupt: has 217 bytes, want 216"},
		{"the header of an object file", func(fsys *simfs.FS, db *DB) {
			b := fileData(t, fsys, fname)
			b[0] = 'W'
			setFile(t, fsys, fname, b)
		}, simPath(fname) + ": corrupt: not an object of whiteroom's log"},
		{"an object file that is a directory", func(fsys *simfs.FS, db *DB) {
			removeFile(t, fsys, fname)
			if err := fsys.Mkdir(fname, 0o700); err != nil {
				t.Fatal(err)
			}
		}, simPath(fname) + ": must be a regular file"},
		{"a value in memory", func(_ *simfs.FS, db *DB) {
			changeState(db, func(st *state) { st.tables[1] = st.tables[1].Put(nil, "k1", []byte("x")) })
		}, differs + `table 1, key "k1": "v1" in the log, "x" in memory`},
		{"a key missing in memory", func(_ *simfs.FS, db *DB) {
			changeState(db, func(st *state) { delete(st.tables, 2) })
		}, differs + `table 2, key "k2": "v2" in the log, missing in memory`},
		{"a key missing in the log", func(_ *simfs.FS, db *DB) {
			changeState(db, func(st *state) { st.tables[3] = pmap[[]byte]{}.Put(nil, strings.Repeat("k", 50), nil) })
		}, differs + `table 3, key "` + strings.Repeat("k", 40) + `...": missing in the log, "" in memory`},
		{"an object in memory", func(_ *simfs.FS, db *DB) {
			changeState(db, func(st *state) {
				loc, _ := st.objects.Get(string(ih[:]))
				loc.off += 8
				st.objects = st.objects.Put(nil, string(ih[:]), loc)
			})
		}, differs + fmt.Sprintf("object %x: at %d of segment 1 (50 bytes) in the log, "+
			"at %d of segment 1 (50 bytes) in memory", ih, ioff, ioff+8)},
		{"a file object in memory", func(_ *simfs.FS, db *DB) {
			changeState(db, func(st *state) { st.objects, _ = st.objects.Delete(nil, string(fh[:])) })
		}, differs + fmt.Sprintf("object %x: in a file (200 bytes) in the log, missing in memory", fh)},
		{"the events in memory", func(_ *simfs.FS, db *DB) { changeState(db, func(st *state) { st.events++ }) },
			differs + "events: 1 in the log, 2 in memory"},
		{"the next event in memory", func(_ *simfs.FS, db *DB) { changeState(db, func(st *state) { st.nextEvent = 5 }) },
			differs + "next event: 2 in the log, 5 in memory"},
		{"the sequence in memory", func(_ *simfs.FS, db *DB) { changeState(db, func(st *state) { st.seq = 3 }) },
			differs + "sequence: 2 in the log, 3 in memory"},
		{"the offset in memory", func(_ *simfs.FS, db *DB) { changeState(db, func(st *state) { st.off += 8 }) },
			differs + fmt.Sprintf("offset of the last record: %d in the log, %d in memory", offs[1], offs[1]+8)},
		{"the CRC in memory", func(_ *simfs.FS, db *DB) { changeState(db, func(st *state) { st.lastCRC++ }) },
			differs + fmt.Sprintf("CRC of the last record: %d in the log, %d in memory", crcAt(t, base, seg1, offs[1]),
				crcAt(t, base, seg1, offs[1])+1)},
	} {
		t.Run(c.name, func(t *testing.T) {
			fsys := copyFS(base)
			db := reopen(t, fsys, o)
			c.change(fsys, db)
			err := db.Verify(ctx)
			if err == nil || err.Error() != c.want {
				t.Errorf("Verify: %v\nwant: %s", err, c.want)
			}
		})
	}
	t.Run("segments", func(t *testing.T) {
		o := Options{SegmentBytes: 100}
		multi, _ := build(t, o, 3)
		for _, c := range []struct {
			name, seg string
			change    func(b []byte) []byte
			want      string
		}{
			{"the first sequence of a header", seg2, func(b []byte) []byte {
				h, _ := parseSegHeader(simPath(seg2), b, 1, 2)
				h.firstSeq = 7
				return slices.Concat(appendSegHeader(nil, h), b[segHeaderSize:])
			}, simPath(seg2) + ": corrupt: the header names first sequence 7, want 2"},
			{"a segment without records", seg2, func(b []byte) []byte { return b[:segHeaderSize] },
				simPath(seg2) + ": corrupt: no records"},
			{"a cut last record", seg3, func(b []byte) []byte { return b[:len(b)-8] },
				simPath(seg3) + ": corrupt at offset 64: truncated record"},
			{"a record that is not a commit", seg2, func(b []byte) []byte {
				h, _ := parseSegHeader(simPath(seg2), b, 1, 2)
				return slices.Concat(b[:segHeaderSize], appendFrame(nil, h.salt, appendBase(nil, 2, 0, nil)))
			}, simPath(seg2) + ": corrupt at offset 64: a base record in generation 1"},
		} {
			t.Run(c.name, func(t *testing.T) {
				fsys := copyFS(multi)
				db := reopen(t, fsys, o)
				setFile(t, fsys, c.seg, c.change(fileData(t, fsys, c.seg)))
				if err := db.Verify(ctx); err == nil || err.Error() != c.want {
					t.Errorf("Verify: %v\nwant: %s", err, c.want)
				}
			})
		}
	})
	t.Run("ended", func(t *testing.T) {
		db := reopen(t, copyFS(base), o)
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		if err := db.Verify(canceled); !errors.Is(err, context.Canceled) {
			t.Errorf("Verify with a canceled context: %v", err)
		}
		// ctx is checked before each record and each object: 2 records and 2 objects.
		for left := range 5 {
			if err := db.Verify(&countdownContext{Context: ctx, left: left}); !errors.Is(err, context.Canceled) {
				t.Errorf("Verify with a context that ends after %d checks: %v", left, err)
			}
		}
		if err := db.Verify(&countdownContext{Context: ctx, left: 5}); err != nil {
			t.Errorf("Verify with a context that ends after 5 checks: %v", err)
		}
		held, release := make(chan struct{}), make(chan struct{})
		done := make(chan error)
		go func() {
			done <- db.Update(ctx, func(tx *Tx) error {
				close(held)
				<-release
				return nil
			})
		}()
		<-held
		waiting, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
		defer cancel()
		if err := db.Verify(waiting); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Verify while an Update runs: %v", err)
		}
		close(release)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		closeDB(t, db)
		if err := db.Verify(ctx); !errors.Is(err, errClosed) {
			t.Errorf("Verify after Close: %v", err)
		}
	})
}

// changeState publishes a copy of the state of db changed by fn, as a bug in memory would.
func changeState(db *DB, fn func(st *state)) {
	st := *db.cur.Load()
	st.tables = maps.Clone(st.tables)
	fn(&st)
	db.cur.Store(&st)
}

func inlineOff(db *DB, h [32]byte) int64 {
	loc, _ := db.cur.Load().objects.Get(string(h[:]))
	return loc.off
}
