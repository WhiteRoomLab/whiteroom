package logdb

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path"
	"slices"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/logdb/simfs"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// simDir is the name of the simulated file systems.
const simDir = "/data"

var (
	seg1 = segName(1, 1)
	seg2 = segName(1, 2)
	seg3 = segName(1, 3)
	seg4 = segName(1, 4)
)

var loseAll = simfs.Model{Name: "lose-all"}

func keepAll(fsys *simfs.FS) simfs.Model {
	keep := make([]bool, len(fsys.Pending()))
	for i := range keep {
		keep[i] = true
	}
	return simfs.Model{Name: "keep-all", Keep: keep}
}

// copyFS returns a new file system with what fsys has now.
func copyFS(fsys *simfs.FS) *simfs.FS {
	return fsys.Crash(keepAll(fsys))
}

func newSim(t *testing.T, o Options) (*simfs.FS, *DB) {
	t.Helper()
	fsys := simfs.New(simDir)
	db, err := Create(fsys, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return fsys, db
}

func reopen(t *testing.T, fsys vfs.FS, o Options) *DB {
	t.Helper()
	db, err := Open(fsys, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func closeDB(t *testing.T, db *DB) {
	t.Helper()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func osFS(t *testing.T, dir string) vfs.FS {
	t.Helper()
	fsys, err := vfs.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fsys.Close() })
	return fsys
}

// dump returns every key of every table, as lines "table/key=value" in the order of tables and keys.
func dump(t *testing.T, db *DB) string {
	t.Helper()
	var lines []string
	err := db.View(context.Background(), func(tx *Tx) error {
		tables := tx.snapshot().tables
		for _, table := range slices.Sorted(maps.Keys(tables)) {
			tx.Scan(table, nil, func(k, v []byte) bool {
				lines = append(lines, fmt.Sprintf("%d/%s=%s", table, k, v))
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(lines, "\n")
}

// putF returns a function for Update that puts key in table.
func putF(table uint32, key, value string) func(*Tx) error {
	return func(tx *Tx) error { return tx.Put(table, []byte(key), []byte(value)) }
}

func putKey(t *testing.T, db *DB, table uint32, key, value string) {
	t.Helper()
	if err := db.Update(context.Background(), putF(table, key, value)); err != nil {
		t.Fatal(err)
	}
}

func delKey(t *testing.T, db *DB, table uint32, key string) {
	t.Helper()
	if err := db.Update(context.Background(), func(tx *Tx) error { return tx.Delete(table, []byte(key)) }); err != nil {
		t.Fatal(err)
	}
}

func seqOf(t *testing.T, db *DB) uint64 {
	t.Helper()
	var seq uint64
	if err := db.View(context.Background(), func(tx *Tx) error {
		seq = tx.Seq()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return seq
}

// build makes a store with n commits that each put a key, closes it, removes the checkpoint that Close has written, so
// that Open replays the log from its start, and returns it with the dumps after 0..n commits. Each record has a frame
// of 40 bytes, 7 of them padding (for n < 10).
func build(t *testing.T, o Options, n int) (*simfs.FS, []string) {
	t.Helper()
	fsys, db := newSim(t, o)
	dumps := []string{dump(t, db)}
	for i := range n {
		putKey(t, db, 1, fmt.Sprintf("k%d", i+1), fmt.Sprintf("value %d.", i+1))
		dumps = append(dumps, dump(t, db))
	}
	closeDB(t, db)
	dropCheckpoint(t, fsys)
	return fsys, dumps
}

// dropCheckpoint removes the checkpoint of the store in fsys, durably, if it has one.
func dropCheckpoint(t *testing.T, fsys vfs.FS) {
	t.Helper()
	if exists(t, fsys, "checkpoint") {
		removeFile(t, fsys, "checkpoint")
	}
}

func fileData(t *testing.T, fsys vfs.FS, name string) []byte {
	t.Helper()
	f, err := fsys.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func fileSize(t *testing.T, fsys vfs.FS, name string) int64 {
	t.Helper()
	info, err := fsys.Lstat(name)
	if err != nil {
		t.Fatal(err)
	}
	return info.Size()
}

func exists(t *testing.T, fsys vfs.FS, name string) bool {
	t.Helper()
	_, err := fsys.Lstat(name)
	switch {
	case err == nil:
		return true
	case errors.Is(err, fs.ErrNotExist):
		return false
	}
	t.Fatal(err)
	return false
}

// setFile makes name hold data, durably, as a damage or a crash may leave it.
func setFile(t *testing.T, fsys vfs.FS, name string, data []byte) {
	t.Helper()
	f, err := fsys.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > 0 {
		if _, err := f.WriteAt(data, 0); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := fsys.SyncDir(path.Dir(name)); err != nil {
		t.Fatal(err)
	}
}

func removeFile(t *testing.T, fsys vfs.FS, name string) {
	t.Helper()
	if err := fsys.Remove(name); err != nil {
		t.Fatal(err)
	}
	if err := fsys.SyncDir(path.Dir(name)); err != nil {
		t.Fatal(err)
	}
}

// fileMap returns what reads see of every file.
func fileMap(fsys *simfs.FS) map[string]string {
	m := map[string]string{}
	fsys.Walk(func(p string, data []byte) { m[p] = string(data) })
	return m
}

func checkFilesUnchanged(t *testing.T, fsys *simfs.FS, before map[string]string) {
	t.Helper()
	after := fileMap(fsys)
	for _, p := range slices.Sorted(maps.Keys(before)) {
		if after[p] != before[p] {
			t.Errorf("%s changed: %d bytes, then %d", p, len(before[p]), len(after[p]))
		}
	}
	for p := range after {
		if _, ok := before[p]; !ok {
			t.Errorf("%s was made", p)
		}
	}
}

// frames returns the header of the segment name and the offsets of its frames, which must be valid.
func frames(t *testing.T, fsys vfs.FS, name string) (segHeader, []int64) {
	t.Helper()
	b := fileData(t, fsys, name)
	gen, index, _ := parseSegName(path.Base(name))
	h, err := parseSegHeader(path.Join(simDir, name), b, gen, index)
	if err != nil {
		t.Fatal(err)
	}
	var offs []int64
	for off := int64(segHeaderSize); off < int64(len(b)); {
		_, n, reason := parseFrame(b[off:], h.salt)
		if reason != "" {
			t.Fatalf("%s at %d: %s", name, off, reason)
		}
		offs = append(offs, off)
		off += int64(n)
	}
	return h, offs
}

// commitFrame returns the frame of a commit that puts key in table, for a segment with salt.
func commitFrame(salt, seq uint64, table uint32, key, value string) []byte {
	return appendFrame(nil, salt, appendCommit(nil, seq, []op{{code: opPut, table: table, key: key, value: []byte(value)}}))
}

// logNames returns the names of the files in log/.
func logNames(t *testing.T, fsys vfs.FS) []string {
	t.Helper()
	entries, err := fsys.ReadDir("log")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, "log/"+e.Name())
	}
	return names
}

// on returns a Fault.Match for the operations of kind on name.
func on(kind simfs.OpKind, name string) func(simfs.Op) bool {
	return func(op simfs.Op) bool { return op.Kind == kind && op.Path == name }
}

// pendingOn returns the changes of name that are not durable.
func pendingOn(fsys *simfs.FS, name string) []simfs.Pending {
	var p []simfs.Pending
	for _, u := range fsys.Pending() {
		if u.Path == name {
			p = append(p, u)
		}
	}
	return p
}

func readHeadFile(t *testing.T, fsys vfs.FS) headSlot {
	t.Helper()
	f, err := fsys.Open("head")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	s, err := readHead(f, path.Join(fsys.Name(), "head"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}
