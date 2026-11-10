package logdb

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"math/rand/v2"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// osTree returns the files and directories under dir with their permissions.
func osTree(t *testing.T, dir string) string {
	t.Helper()
	var lines []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == dir {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		if d.IsDir() {
			rel += "/"
		}
		lines = append(lines, fmt.Sprintf("%s %o", filepath.ToSlash(rel), info.Mode().Perm()))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(lines, "\n")
}

func TestCreateLayout(t *testing.T) {
	dir := t.TempDir()
	fsys := osFS(t, dir)
	db, err := Create(fsys, Options{})
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"head 600",
		"lock/ 700", "lock/entry 600", "lock/readers 600", "lock/writer 600",
		"log/ 700", "log/0000000000000001-0000000000000001.log 600",
		"objects/ 700",
	}, "\n")
	if got := osTree(t, dir); got != want {
		t.Errorf("after Create:\n%s\nwant:\n%s", got, want)
	}
	b := fileData(t, fsys, seg1)
	h, err := parseSegHeader(path.Join(dir, seg1), b, 1, 1)
	if err != nil || len(b) != segHeaderSize || h.firstSeq != 1 {
		t.Errorf("segment: %d bytes, %+v, %v", len(b), h, err)
	}
	if s := readHeadFile(t, fsys); s != (headSlot{counter: 2, gen: 1, seg: 1, end: segHeaderSize}) {
		t.Errorf("head after Create: %+v", s)
	}
	if info := db.Recovery(); !equalInfo(info, RecoveryInfo{Generation: 1, Checkpoint: "none", TruncatedAt: -1}) {
		t.Errorf("Recovery: %+v", info)
	}
	putKey(t, db, 1, "k", "v")
	closeDB(t, db)
	if got, want := osTree(t, dir), "checkpoint 600\n"+want; got != want {
		t.Errorf("after Close:\n%s\nwant:\n%s", got, want)
	}
	_, offs := frames(t, fsys, seg1)
	if s := readHeadFile(t, fsys); s.counter != 4 || s.seq != 1 || s.seg != 1 || s.end != fileSize(t, fsys, seg1) ||
		s.crc != crcAt(t, fsys, seg1, offs[0]) {
		t.Errorf("head after Close: %+v", s)
	}
	if _, err := Create(fsys, Options{}); err == nil || err.Error() != dir+": not empty" {
		t.Errorf("Create in a store: %v", err)
	}
}

// crcAt returns the CRC of the frame at off in the segment name.
func crcAt(t *testing.T, fsys vfs.FS, name string, off int64) uint32 {
	t.Helper()
	return binary.LittleEndian.Uint32(fileData(t, fsys, name)[off+4:])
}

func equalInfo(a, b RecoveryInfo) bool {
	return a.Generation == b.Generation && a.Seq == b.Seq && a.Checkpoint == b.Checkpoint && a.Replayed == b.Replayed &&
		a.TruncatedAt == b.TruncatedAt && a.RemovedSegment == b.RemovedSegment &&
		slices.Equal(a.IgnoredGenerations, b.IgnoredGenerations)
}

func TestLayoutErrors(t *testing.T) {
	for _, c := range []struct {
		name   string
		change func(dir string) error
		want   string // after the directory
	}{
		{"missing log", func(dir string) error { return os.RemoveAll(filepath.Join(dir, "log")) }, "/log: missing directory"},
		{"missing lock file", func(dir string) error { return os.Remove(filepath.Join(dir, "lock/writer")) },
			"/lock/writer: missing file"},
		{"missing head", func(dir string) error { return os.Remove(filepath.Join(dir, "head")) }, "/head: missing file"},
		{"objects is a file", func(dir string) error {
			if err := os.Remove(filepath.Join(dir, "objects")); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(dir, "objects"), nil, 0o600)
		}, "/objects: must be a directory"},
		{"head is a directory", func(dir string) error {
			if err := os.Remove(filepath.Join(dir, "head")); err != nil {
				return err
			}
			return os.Mkdir(filepath.Join(dir, "head"), 0o700)
		}, "/head: must be a regular file"},
		{"head is a link", func(dir string) error {
			if err := os.Rename(filepath.Join(dir, "head"), filepath.Join(dir, "head2")); err != nil {
				return err
			}
			return os.Symlink("head2", filepath.Join(dir, "head"))
		}, "/head: symbolic links are not allowed"},
		{"a segment is a link", func(dir string) error {
			old := filepath.Join(dir, seg1)
			if err := os.Rename(old, filepath.Join(dir, "seg")); err != nil {
				return err
			}
			return os.Symlink("../seg", old)
		}, "/" + seg1 + ": symbolic links are not allowed"},
		{"a segment is a directory", func(dir string) error { return os.Mkdir(filepath.Join(dir, seg2), 0o700) },
			"/" + seg2 + ": must be a regular file"},
		{"lock is a file", func(dir string) error {
			if err := os.RemoveAll(filepath.Join(dir, "lock")); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(dir, "lock"), nil, 0o600)
		}, "/lock: must be a directory"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			fsys := osFS(t, dir)
			db, err := Create(fsys, Options{})
			if err != nil {
				t.Fatal(err)
			}
			closeDB(t, db)
			if err := c.change(dir); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(fsys, Options{}); err == nil || err.Error() != dir+c.want {
				t.Errorf("Open: %v, want %s", err, dir+c.want)
			}
		})
	}
}

func TestReopen(t *testing.T) {
	ctx := context.Background()
	fsys, db := newSim(t, Options{})
	steps := []func(tx *Tx) error{
		func(tx *Tx) error {
			return errors.Join(tx.Put(1, []byte("b"), []byte("2")), tx.Put(1, []byte("a"), []byte("1")),
				tx.Put(2, []byte("x"), []byte("9")), tx.Put(1, nil, []byte("empty key")))
		},
		func(tx *Tx) error {
			return errors.Join(tx.Put(1, []byte("ab"), []byte("3")), tx.Delete(1, []byte("b")),
				tx.Put(1, []byte("c"), nil), tx.Put(1, []byte("a"), []byte("one")))
		},
		func(tx *Tx) error { return tx.Delete(2, []byte("x")) },
	}
	for _, f := range steps {
		if err := db.Update(ctx, f); err != nil {
			t.Fatal(err)
		}
	}
	want := "1/=empty key\n1/a=one\n1/ab=3\n1/c="
	if got := dump(t, db); got != want {
		t.Fatalf("dump:\n%s\nwant:\n%s", got, want)
	}
	closeDB(t, db)
	// Without the checkpoint of Close, Open replays the log.
	replayed := copyFS(fsys)
	dropCheckpoint(t, replayed)
	db = reopen(t, replayed, Options{})
	if got := dump(t, db); got != want {
		t.Errorf("after reopening without the checkpoint:\n%s\nwant:\n%s", got, want)
	}
	if info := db.Recovery(); !equalInfo(info, RecoveryInfo{Generation: 1, Seq: 3, Checkpoint: "none", Replayed: 3, TruncatedAt: -1}) {
		t.Errorf("Recovery: %+v", info)
	}
	db = reopen(t, fsys, Options{})
	if got := dump(t, db); got != want {
		t.Errorf("after reopening:\n%s\nwant:\n%s", got, want)
	}
	if info := db.Recovery(); !equalInfo(info, RecoveryInfo{Generation: 1, Seq: 3, Checkpoint: "used", TruncatedAt: -1}) {
		t.Errorf("Recovery: %+v", info)
	}
	err := db.View(ctx, func(tx *Tx) error {
		if tx.Seq() != 3 {
			t.Errorf("Seq %d", tx.Seq())
		}
		for _, c := range []struct {
			table  uint32
			prefix string
			limit  int
			want   string
		}{
			{1, "", 0, "=empty key a=one ab=3 c="},
			{1, "a", 0, "a=one ab=3"},
			{1, "ab", 0, "ab=3"},
			{1, "abc", 0, ""},
			{1, "b", 0, ""},
			{1, "", 2, "=empty key a=one"},
			{2, "", 0, ""},
			{3, "", 0, ""},
		} {
			var got []string
			tx.Scan(c.table, []byte(c.prefix), func(k, v []byte) bool {
				got = append(got, string(k)+"="+string(v))
				return c.limit == 0 || len(got) < c.limit
			})
			if s := strings.Join(got, " "); s != c.want {
				t.Errorf("Scan(%d, %q): %q, want %q", c.table, c.prefix, s, c.want)
			}
		}
		if v, ok := tx.Get(1, []byte("c")); !ok || len(v) != 0 {
			t.Errorf("Get of an empty value: %q, %v", v, ok)
		}
		if v, ok := tx.Get(2, []byte("x")); ok {
			t.Errorf("Get of a deleted key: %q", v)
		}
		if _, ok := tx.snapshot().tables[2]; ok {
			t.Error("a table without keys is kept")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	delKey(t, db, 1, "")
	delKey(t, db, 1, "ab")
	closeDB(t, db)
	for _, ckp := range []bool{true, false} {
		if !ckp {
			closeDB(t, db)
			dropCheckpoint(t, fsys)
		}
		db = reopen(t, fsys, Options{})
		if got := dump(t, db); got != "1/a=one\n1/c=" || seqOf(t, db) != 5 {
			t.Errorf("after deleting and reopening, checkpoint %v:\n%s", ckp, got)
		}
		// Values read from the log or the checkpoint do not keep its buffer in memory.
		if v, _ := db.cur.Load().tables[1].Get("a"); cap(v) > 8 {
			t.Errorf("a value of %d bytes has a capacity of %d, checkpoint %v", len(v), cap(v), ckp)
		}
	}
	// head names the last record.
	_, offs := frames(t, fsys, seg1)
	if s := readHeadFile(t, fsys); s.seq != 5 || s.seg != 1 || s.end != fileSize(t, fsys, seg1) ||
		s.crc != crcAt(t, fsys, seg1, offs[len(offs)-1]) || s.gen != 1 {
		t.Errorf("head after reopening: %+v", s)
	}
}

// TestRandomUpdates compares a store with a map after random Updates, across rollovers and reopening.
func TestRandomUpdates(t *testing.T) {
	ctx := context.Background()
	o := Options{SegmentBytes: 1024}
	fsys, db := newSim(t, o)
	rng := rand.New(rand.NewPCG(3, 63))
	model := map[uint32]map[string]string{}
	modelDump := func() string {
		var lines []string
		for _, table := range slices.Sorted(maps.Keys(model)) {
			for _, k := range slices.Sorted(maps.Keys(model[table])) {
				lines = append(lines, fmt.Sprintf("%d/%s=%s", table, k, model[table][k]))
			}
		}
		return strings.Join(lines, "\n")
	}
	for i := range 300 {
		next := map[uint32]map[string]string{}
		for table, m := range model {
			next[table] = maps.Clone(m)
		}
		err := db.Update(ctx, func(tx *Tx) error {
			for range rng.IntN(6) {
				table := 1 + rng.Uint32N(3)
				key := fmt.Sprintf("%c%d", 'a'+rng.IntN(3), rng.IntN(20))
				if rng.IntN(3) == 0 {
					if err := tx.Delete(table, []byte(key)); err != nil {
						return err
					}
					delete(next[table], key)
					if len(next[table]) == 0 {
						delete(next, table)
					}
					continue
				}
				value := strings.Repeat(string(rune('A'+rng.IntN(26))), rng.IntN(40))
				if err := tx.Put(table, []byte(key), []byte(value)); err != nil {
					return err
				}
				if next[table] == nil {
					next[table] = map[string]string{}
				}
				next[table][key] = value
			}
			if rng.IntN(10) == 0 {
				return errors.New("discarded")
			}
			return nil
		})
		switch {
		case err == nil:
			model = next
		case err.Error() != "discarded":
			t.Fatal(err)
		}
		if got, want := dump(t, db), modelDump(); got != want {
			t.Fatalf("after Update %d:\n%s\nwant:\n%s", i, got, want)
		}
	}
	if n := len(logNames(t, fsys)); n < 3 {
		t.Errorf("%d segments", n)
	}
	seq := seqOf(t, db)
	closeDB(t, db)
	db = reopen(t, fsys, o)
	if got, want := dump(t, db), modelDump(); got != want {
		t.Errorf("after reopening:\n%s\nwant:\n%s", got, want)
	}
	if got := seqOf(t, db); got != seq {
		t.Errorf("Seq %d, want %d", got, seq)
	}
}

func TestSnapshotAtFirstRead(t *testing.T) {
	ctx := context.Background()
	_, db := newSim(t, Options{})
	putKey(t, db, 1, "k", "1")
	begun, first, read, second := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	type result struct {
		v1, v2     string
		seq1, seq2 uint64
		err        error
	}
	done := make(chan result)
	go func() {
		var r result
		r.err = db.View(ctx, func(tx *Tx) error {
			close(begun)
			<-first
			v, _ := tx.Get(1, []byte("k"))
			r.v1, r.seq1 = string(v), tx.Seq()
			close(read)
			<-second
			v, _ = tx.Get(1, []byte("k"))
			r.v2, r.seq2 = string(v), tx.Seq()
			return nil
		})
		done <- r
	}()
	<-begun
	putKey(t, db, 1, "k", "2") // before the first read of the View
	close(first)
	<-read
	putKey(t, db, 1, "k", "3") // after it
	close(second)
	r := <-done
	if r.err != nil || r.v1 != "2" || r.seq1 != 2 || r.v2 != "2" || r.seq2 != 2 {
		t.Errorf("the View read %+v, want 2 at seq 2 twice", r)
	}
	if got := dump(t, db); got != "1/k=3" {
		t.Errorf("dump: %s", got)
	}
}

func TestOwnWrites(t *testing.T) {
	ctx := context.Background()
	_, db := newSim(t, Options{})
	putKey(t, db, 1, "base", "0")
	err := db.Update(ctx, func(tx *Tx) error {
		key, value := []byte("a"), []byte("1")
		if err := tx.Put(1, key, value); err != nil {
			return err
		}
		key[0], value[0] = 'b', '2' // Put has copied them
		if v, ok := tx.Get(1, []byte("a")); !ok || string(v) != "1" || cap(v) != len(v) {
			t.Errorf("Get of an own write: %q, %v, cap %d", v, ok, cap(v))
		}
		if err := tx.Delete(1, []byte("base")); err != nil {
			return err
		}
		if _, ok := tx.Get(1, []byte("base")); ok {
			t.Error("Get of an own delete")
		}
		if tx.Seq() != 1 {
			t.Errorf("Seq %d", tx.Seq())
		}
		if got := dump(t, db); got != "1/base=0" {
			t.Errorf("a View during the Update: %s", got)
		}
		// writes while Scan runs do not change what it scans
		var keys []string
		for i := range 40 {
			key := fmt.Sprintf("k%02d", 2*i)
			keys = append(keys, key)
			if err := tx.Put(1, []byte(key), []byte(key)); err != nil {
				return err
			}
		}
		var seen []string
		tx.Scan(1, []byte("k"), func(k, v []byte) bool {
			seen = append(seen, string(k))
			if !bytes.Equal(k, v) {
				t.Errorf("Scan: %s=%s", k, v)
			}
			for _, w := range []error{
				tx.Put(1, []byte(string(k)+"x"), v),
				tx.Put(1, []byte(fmt.Sprintf("k%02d", len(seen)*2+1)), v),
				tx.Delete(1, []byte("k78")),
			} {
				if w != nil {
					t.Error(w)
				}
			}
			return true
		})
		if !slices.Equal(seen, keys) {
			t.Errorf("Scan with writes saw %q", seen)
		}
		if _, ok := tx.Get(1, []byte("k78")); ok {
			t.Error("a delete in Scan is lost")
		}
		if v, ok := tx.Get(1, []byte("k00x")); !ok || string(v) != "k00" {
			t.Errorf("a put in Scan is lost: %q, %v", v, ok)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"a": "1"}
	for i := range 40 {
		key := fmt.Sprintf("k%02d", 2*i)
		want[key], want[key+"x"], want[fmt.Sprintf("k%02d", 2*i+3)] = key, key, key
	}
	delete(want, "k78")
	var lines []string
	for _, k := range slices.Sorted(maps.Keys(want)) {
		lines = append(lines, "1/"+k+"="+want[k])
	}
	if got := dump(t, db); got != strings.Join(lines, "\n") {
		t.Errorf("dump after the Update:\n%s", got)
	}
}

func TestErrorAndPanicDiscard(t *testing.T) {
	ctx := context.Background()
	fsys, db := newSim(t, Options{})
	putKey(t, db, 1, "a", "1")
	before, counted := dump(t, db), fsys.Counted()
	boom := errors.New("boom")
	var ended *Tx
	if err := db.Update(ctx, func(tx *Tx) error {
		ended = tx
		if err := tx.Put(1, []byte("b"), []byte("2")); err != nil {
			return err
		}
		return boom
	}); !errors.Is(err, boom) {
		t.Errorf("Update: %v", err)
	}
	if err := ended.Put(1, []byte("c"), nil); !errors.Is(err, errEnded) {
		t.Errorf("Put after the end: %v", err)
	}
	if err := ended.Delete(1, []byte("a")); !errors.Is(err, errEnded) {
		t.Errorf("Delete after the end: %v", err)
	}
	if v, ok := ended.Get(1, []byte("b")); ok || v != nil {
		t.Errorf("Get after the end: %q, %v", v, ok)
	}
	ended.Scan(1, nil, func(k, _ []byte) bool {
		t.Errorf("Scan after the end: %s", k)
		return true
	})
	if ended.Seq() != 0 {
		t.Errorf("Seq after the end: %d", ended.Seq())
	}
	for _, update := range []bool{true, false} {
		func() {
			defer func() {
				if r := recover(); r != "panic in f" {
					t.Errorf("recovered %v", r)
				}
			}()
			f := func(tx *Tx) error {
				ended = tx
				_ = tx.Put(1, []byte("p"), []byte("3"))
				panic("panic in f")
			}
			if update {
				_ = db.Update(ctx, f)
			} else {
				_ = db.View(ctx, f)
			}
			t.Error("no panic")
		}()
		if err := ended.Put(1, []byte("c"), nil); !errors.Is(err, errEnded) {
			t.Errorf("Put after a panic: %v", err)
		}
	}
	if err := db.View(ctx, func(*Tx) error { return boom }); !errors.Is(err, boom) {
		t.Errorf("View: %v", err)
	}
	if got := dump(t, db); got != before || fsys.Counted() != counted {
		t.Errorf("after the discarded Updates: %s, %d operations", got, fsys.Counted()-counted)
	}
	putKey(t, db, 1, "d", "4")
	if got := dump(t, db); got != "1/a=1\n1/d=4" {
		t.Errorf("dump: %s", got)
	}
}

func TestReadOnly(t *testing.T) {
	_, db := newSim(t, Options{})
	err := db.View(context.Background(), func(tx *Tx) error {
		if err := tx.Put(1, []byte("a"), nil); !errors.Is(err, ErrReadOnly) {
			t.Errorf("Put: %v", err)
		}
		if err := tx.Delete(1, []byte("a")); !errors.Is(err, ErrReadOnly) {
			t.Errorf("Delete: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	err = db.Update(context.Background(), func(tx *Tx) error {
		for _, c := range []struct {
			err  error
			want string
		}{
			{tx.Put(0, []byte("a"), nil), "invalid table 0"},
			{tx.Delete(0, []byte("a")), "invalid table 0"},
			{tx.Put(1, make([]byte, MaxKey+1), nil), "key too long: 4097 bytes (max 4096)"},
			{tx.Delete(1, make([]byte, MaxKey+1)), "key too long: 4097 bytes (max 4096)"},
			{tx.Put(1, make([]byte, MaxKey), nil), ""},
			{tx.Put(1<<32-1, nil, nil), ""},
		} {
			if got := fmt.Sprint(c.err); c.want == "" && c.err != nil || c.want != "" && got != c.want {
				t.Errorf("error %v, want %q", c.err, c.want)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(dump(t, db)); got != len("1/=")+MaxKey+len("\n4294967295/=") {
		t.Errorf("dump of %d bytes", got)
	}
}

func TestCanceled(t *testing.T) {
	ctx := context.Background()
	fsys, db := newSim(t, Options{})
	called := false
	f := func(tx *Tx) error {
		called = true
		return tx.Put(1, []byte("x"), []byte("1"))
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := db.Update(canceled, f); !errors.Is(err, context.Canceled) || called {
		t.Errorf("Update with a canceled context: %v, f called %v", err, called)
	}
	if err := db.View(canceled, f); !errors.Is(err, context.Canceled) || called {
		t.Errorf("View with a canceled context: %v, f called %v", err, called)
	}
	counted := fsys.Counted()
	inF, cancel := context.WithCancel(ctx)
	if err := db.Update(inF, func(tx *Tx) error {
		if err := tx.Put(1, []byte("x"), []byte("1")); err != nil {
			return err
		}
		cancel()
		return nil
	}); !errors.Is(err, context.Canceled) {
		t.Errorf("Update canceled in f: %v", err)
	}
	inF, cancel = context.WithCancel(ctx)
	if err := db.View(inF, func(*Tx) error {
		cancel()
		return nil
	}); !errors.Is(err, context.Canceled) {
		t.Errorf("View canceled in f: %v", err)
	}
	if fsys.Counted() != counted {
		t.Error("a canceled Update wrote")
	}
	held, release := make(chan struct{}), make(chan struct{})
	done := make(chan error)
	go func() {
		done <- db.Update(ctx, func(tx *Tx) error {
			close(held)
			<-release
			return tx.Put(1, []byte("a"), []byte("1"))
		})
	}()
	<-held
	wait := 30 * time.Millisecond
	start := time.Now() // before the deadline is set
	waiting, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	if err := db.Update(waiting, f); !errors.Is(err, context.DeadlineExceeded) || called {
		t.Errorf("a waiting Update: %v, f called %v", err, called)
	}
	if d := time.Since(start); d < wait {
		t.Errorf("the Update waited %v", d)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := dump(t, db); got != "1/a=1" {
		t.Errorf("dump: %s", got)
	}
	// A context that ends while the Update takes its turn.
	if err := db.Update(&endingContext{Context: ctx}, f); !errors.Is(err, context.Canceled) || called {
		t.Errorf("Update with a context that ends while it waits: %v, f called %v", err, called)
	}
}

// endingContext ends after its first Err, without closing Done.
type endingContext struct {
	context.Context
	errs int
}

func (c *endingContext) Err() error {
	if c.errs++; c.errs > 1 {
		return context.Canceled
	}
	return nil
}

func TestEmptyUpdate(t *testing.T) {
	fsys, db := newSim(t, Options{})
	putKey(t, db, 1, "a", "1")
	counted := fsys.Counted()
	for _, f := range []func(tx *Tx) error{
		func(*Tx) error { return nil },
		func(tx *Tx) error {
			_, _ = tx.Get(1, []byte("a"))
			return nil
		},
		func(tx *Tx) error { return errors.Join(tx.Delete(1, []byte("missing")), tx.Delete(2, []byte("a"))) },
	} {
		if err := db.Update(context.Background(), f); err != nil {
			t.Fatal(err)
		}
	}
	if n := fsys.Counted() - counted; n != 0 {
		t.Errorf("empty Updates made %d operations", n)
	}
	if seqOf(t, db) != 1 || fileSize(t, fsys, seg1) != segHeaderSize+int64(frameLen(17)) {
		t.Errorf("seq %d, segment of %d bytes", seqOf(t, db), fileSize(t, fsys, seg1))
	}
}

func TestSecondWriter(t *testing.T) {
	dir := t.TempDir()
	fsys := osFS(t, dir)
	db, err := Create(fsys, Options{})
	if err != nil {
		t.Fatal(err)
	}
	o := Options{WriterWait: 50 * time.Millisecond}
	for _, other := range []vfs.FS{fsys, osFS(t, dir)} {
		start := time.Now()
		if _, err := Open(other, o); err == nil || err.Error() != dir+": another process holds the writer lock" {
			t.Errorf("a second writer: %v", err)
		}
		if d := time.Since(start); d < o.WriterWait {
			t.Errorf("the second writer waited %v", d)
		}
	}
	putKey(t, db, 1, "a", "1")
	closeDB(t, db)
	db = reopen(t, fsys, o)
	if got := dump(t, db); got != "1/a=1" {
		t.Errorf("dump: %s", got)
	}
}

func TestLockErrors(t *testing.T) {
	fsys, db := newSim(t, Options{})
	// The writer keeps lock/readers shared and lock/writer exclusive, and releases lock/entry.
	for _, c := range []struct {
		name string
		mode vfs.LockMode
		held bool
	}{
		{"lock/entry", vfs.Exclusive, false},
		{"lock/readers", vfs.Shared, false},
		{"lock/readers", vfs.Exclusive, true},
		{"lock/writer", vfs.Shared, true},
	} {
		unlock, err := fsys.Lock(c.name, c.mode)
		if c.held != errors.Is(err, vfs.ErrLocked) || !c.held && err != nil {
			t.Errorf("%s while the writer is open: %v", c.name, err)
		}
		if err == nil {
			if err := unlock(); err != nil {
				t.Fatal(err)
			}
		}
	}
	closeDB(t, db)
	for _, name := range []string{"lock/entry", "lock/readers"} {
		unlock, err := fsys.Lock(name, vfs.Exclusive)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Open(fsys, Options{WriterWait: time.Millisecond}); err == nil || err.Error() != simDir+": maintenance is running" {
			t.Errorf("Open while %s is held: %v", name, err)
		}
		if err := unlock(); err != nil {
			t.Fatal(err)
		}
	}
	// the locks of a failed Open are released
	db = reopen(t, fsys, Options{WriterWait: time.Millisecond})
	closeDB(t, db)
	unlock, err := fsys.Lock("lock/writer", vfs.Shared)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(fsys, Options{WriterWait: time.Millisecond}); err == nil ||
		err.Error() != simDir+": another process holds the writer lock" {
		t.Errorf("Open while a reader probes: %v", err)
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"lock/entry", "lock/readers", "lock/writer"} {
		unlock, err := fsys.Lock(name, vfs.Exclusive)
		if err != nil {
			t.Fatalf("%s is still locked: %v", name, err)
		}
		if err := unlock(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTooLarge(t *testing.T) {
	defer func(n int) { maxBody = n }(maxBody)
	maxBody = 200
	fsys, db := newSim(t, Options{})
	counted := fsys.Counted()
	value := bytes.Repeat([]byte("v"), 300)
	n := len(appendCommit(nil, 1, []op{{code: opPut, table: 1, key: "k", value: value}}))
	err := db.Update(context.Background(), func(tx *Tx) error { return tx.Put(1, []byte("k"), value) })
	if want := fmt.Sprintf("the transaction is too large: %d bytes (max 200)", n); err == nil || err.Error() != want {
		t.Errorf("Update: %v, want %s", err, want)
	}
	if fsys.Counted() != counted || dump(t, db) != "" {
		t.Error("a transaction that is too large wrote")
	}
	putKey(t, db, 1, "k", strings.Repeat("v", 150))
	if seqOf(t, db) != 1 {
		t.Errorf("seq %d", seqOf(t, db))
	}
}

func TestRollover(t *testing.T) {
	o := Options{SegmentBytes: 4096}
	fsys, db := newSim(t, o)
	for i := range 100 {
		putKey(t, db, 1, fmt.Sprintf("k%03d", i), strings.Repeat("v", 200))
	}
	putKey(t, db, 2, "big", strings.Repeat("b", 10000)) // more than a segment: in one of its own
	putKey(t, db, 1, "after", "x")
	want := dump(t, db)
	names := logNames(t, fsys)
	if len(names) < 7 {
		t.Fatalf("%d segments", len(names))
	}
	first := uint64(1)
	for i, name := range names {
		if name != segName(1, uint64(i+1)) {
			t.Fatalf("segment %d: %s", i+1, name)
		}
		h, offs := frames(t, fsys, name)
		if h.firstSeq != first || len(offs) == 0 {
			t.Errorf("%s: first seq %d, want %d; %d records", name, h.firstSeq, first, len(offs))
		}
		if size := fileSize(t, fsys, name); size > o.SegmentBytes && len(offs) > 1 {
			t.Errorf("%s: %d bytes in %d records", name, size, len(offs))
		}
		first += uint64(len(offs))
	}
	if first != 103 {
		t.Errorf("%d records", first-1)
	}
	var size int64
	for _, name := range names {
		size += fileSize(t, fsys, name)
	}
	if db.logBytes != size || db.cur.Load().logBytes != size {
		t.Errorf("log of %d and %d bytes, want %d", db.logBytes, db.cur.Load().logBytes, size)
	}
	closeDB(t, db)
	db = reopen(t, fsys, o)
	if got := dump(t, db); got != want {
		t.Error("the dump differs after reopening")
	}
	if info := db.Recovery(); info.Seq != 102 || info.Checkpoint != "used" || info.Replayed != 0 {
		t.Errorf("Recovery: %+v", info)
	}
	putKey(t, db, 1, "reopened", "y")
	if seqOf(t, db) != 103 || len(logNames(t, fsys)) != len(names) {
		t.Errorf("seq %d, %d segments", seqOf(t, db), len(logNames(t, fsys)))
	}
	// The first record of a segment stays in it, however large it is.
	fsys, db = newSim(t, Options{SegmentBytes: 100})
	putKey(t, db, 1, "big", strings.Repeat("b", 200))
	if names := logNames(t, fsys); len(names) != 1 {
		t.Errorf("segments %v", names)
	}
	putKey(t, db, 1, "next", "x")
	closeDB(t, db)
	if names := logNames(t, fsys); len(names) != 2 {
		t.Errorf("segments %v", names)
	}
	reopen(t, fsys, Options{SegmentBytes: 100})
}

func TestClosed(t *testing.T) {
	_, db := newSim(t, Options{})
	putKey(t, db, 1, "a", "1")
	closeDB(t, db)
	called := false
	f := func(*Tx) error {
		called = true
		return nil
	}
	if err := db.View(context.Background(), f); !errors.Is(err, errClosed) {
		t.Errorf("View: %v", err)
	}
	if err := db.Update(context.Background(), f); !errors.Is(err, errClosed) {
		t.Errorf("Update: %v", err)
	}
	if called {
		t.Error("f called")
	}
	if err := db.Close(); err != nil {
		t.Errorf("a second Close: %v", err)
	}
	if info := db.Recovery(); info.Generation != 1 || info.Checkpoint != "none" {
		t.Errorf("Recovery: %+v", info)
	}
}

// TestCloseWhileUpdating checks that Close waits for the Update that runs, and that Updates after it fail.
func TestCloseWhileUpdating(t *testing.T) {
	ctx := context.Background()
	fsys, db := newSim(t, Options{})
	held, release := make(chan struct{}), make(chan struct{})
	done := make(chan error)
	go func() {
		done <- db.Update(ctx, func(tx *Tx) error {
			close(held)
			<-release
			return tx.Put(1, []byte("a"), []byte("1"))
		})
	}()
	<-held
	closed := make(chan error)
	go func() { closed <- db.Close() }()
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if err := db.Update(ctx, putF(1, "b", "2")); !errors.Is(err, errClosed) {
		t.Errorf("Update after Close: %v", err)
	}
	db = reopen(t, fsys, Options{})
	if got := dump(t, db); got != "1/a=1" {
		t.Errorf("dump: %s", got)
	}
}
