package logdb

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/logdb/simfs"
)

func simPath(name string) string {
	return path.Join(simDir, name)
}

// checkTorn opens fsys, whose segment name has a torn tail at off, and checks that Open cuts it off.
func checkTorn(t *testing.T, fsys *simfs.FS, name string, off int64, want string, seq uint64) {
	t.Helper()
	db, err := Open(fsys, Options{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer closeDB(t, db)
	if info := db.Recovery(); info.TruncatedAt != off || info.Seq != seq || info.Replayed != int(seq) {
		t.Errorf("Recovery: %+v, want the cut at %d", info, off)
	}
	if got := dump(t, db); got != want {
		t.Errorf("dump:\n%s\nwant:\n%s", got, want)
	}
	if size := fileSize(t, fsys, name); size != off || len(pendingOn(fsys, name)) != 0 {
		t.Errorf("%s has %d bytes, %v", name, size, pendingOn(fsys, name))
	}
}

// checkRefused checks that Open fails with a CorruptError at off of the file name, and changes no file.
func checkRefused(t *testing.T, fsys *simfs.FS, name string, off int64, reason string) {
	t.Helper()
	before := fileMap(fsys)
	_, err := Open(fsys, Options{})
	checkCorrupt(t, err, simPath(name), off, reason)
	checkFilesUnchanged(t, fsys, before)
}

func TestTornTail(t *testing.T) {
	base, dumps := build(t, Options{}, 2)
	db := reopen(t, base, Options{})
	putKey(t, db, 1, "last", strings.Repeat("x", 600)) // a frame of more than 600 bytes
	closeDB(t, db)
	_, offs := frames(t, base, seg1)
	full := fileData(t, base, seg1)
	last, end := offs[2], int64(len(full))
	for cut := last + 1; cut < end; cut++ {
		fsys := copyFS(base)
		setFile(t, fsys, seg1, full[:cut])
		checkTorn(t, fsys, seg1, last, dumps[2], 2)
	}
	for _, c := range []struct {
		name string
		data []byte
	}{
		{"zeros", append(slices.Clone(full[:last]), make([]byte, end-last)...)},
		{"zeros at the end", append(slices.Clone(full[:end-100]), make([]byte, 100)...)},
		{"a page of zeros", append(slices.Clone(full[:last+8]), make([]byte, 4096)...)},
		{"a damaged byte", func() []byte { b := slices.Clone(full); b[last+20] ^= 1; return b }()},
		{"a damaged length", func() []byte { b := slices.Clone(full); b[last+1] ^= 0x40; return b }()},
		{"a damaged CRC", func() []byte { b := slices.Clone(full); b[last+4] ^= 1; return b }()},
	} {
		t.Run(c.name, func(t *testing.T) {
			fsys := copyFS(base)
			setFile(t, fsys, seg1, c.data)
			checkTorn(t, fsys, seg1, last, dumps[2], 2)
		})
	}
	t.Run("zeros after the last record", func(t *testing.T) {
		fsys := copyFS(base)
		setFile(t, fsys, seg1, append(slices.Clone(full), make([]byte, 100)...))
		db := reopen(t, fsys, Options{})
		if info := db.Recovery(); info.TruncatedAt != end || info.Seq != 3 {
			t.Errorf("Recovery: %+v", info)
		}
	})
	t.Run("no tail", func(t *testing.T) {
		fsys := copyFS(base)
		setFile(t, fsys, seg1, full[:last])
		db := reopen(t, fsys, Options{})
		if info := db.Recovery(); info.TruncatedAt != -1 || info.Seq != 2 {
			t.Errorf("Recovery: %+v", info)
		}
		putKey(t, db, 1, "next", "x") // the store goes on from there
		closeDB(t, db)
		db = reopen(t, fsys, Options{})
		if got, want := dump(t, db), dumps[2]+"\n1/next=x"; got != want {
			t.Errorf("dump:\n%s\nwant:\n%s", got, want)
		}
	})
}

func TestCorruptionNotTail(t *testing.T) {
	base, _ := build(t, Options{}, 3)
	h, offs := frames(t, base, seg1)
	full := fileData(t, base, seg1)
	// Each byte of a frame that a valid frame follows: corruption, whatever the byte is.
	for j := range len(offs) - 1 {
		body := binary.LittleEndian.Uint32(full[offs[j]:])
		for i := offs[j]; i < offs[j+1]; i++ {
			fsys := copyFS(base)
			b := slices.Clone(full)
			b[i] ^= 0x81
			setFile(t, fsys, seg1, b)
			reason := fmt.Sprintf("a valid record follows at offset %d", offs[j+1])
			if i >= offs[j]+8+int64(body) {
				reason = "nonzero padding"
			}
			checkRefused(t, fsys, seg1, offs[j], reason)
		}
	}
	// A last frame whose CRC verifies was written completely: if it is wrong, it is corruption, not a torn tail.
	last := offs[len(offs)-1]
	frame := func(body []byte) []byte { return append(slices.Clone(full[:last]), appendFrame(nil, h.salt, body)...) }
	for _, c := range []struct {
		name   string
		data   []byte
		reason string
	}{
		{"wrong seq", frame(appendCommit(nil, 5, nil)), "sequence 5, want 3"},
		{"old seq", frame(appendCommit(nil, 2, nil)), "sequence 2, want 3"},
		{"unknown op", frame(rawBody(recCommit, []byte{1, 9})), "unknown op 9"},
		{"trailing bytes", frame(append(appendCommit(nil, 3, nil), 0)), "trailing bytes"},
		{"base record", frame(appendBase(nil, 3, 0, nil)), "a base record in generation 1"},
		{"object", frame(appendCommit(nil, 3, []op{{code: opInline, hash: hashOf(1), value: []byte("x")}})), "op 3 is not supported yet"},
		{"nonzero padding", func() []byte {
			b := slices.Clone(full)
			b[len(b)-1] = 1
			return b
		}(), "nonzero padding"},
		{"garbage, then the next record", slices.Concat(full[:last], []byte("garbage!"),
			appendFrame(nil, h.salt, appendCommit(nil, 3, nil))), fmt.Sprintf("a valid record follows at offset %d", last+8)},
		{"garbage, then a record with nonzero padding", slices.Concat(full[:last], []byte("garbage!"), func() []byte {
			f := appendFrame(nil, h.salt, appendCommit(nil, 4, nil))
			f[len(f)-1] = 1
			return f
		}()), fmt.Sprintf("a valid record follows at offset %d", last+8)},
	} {
		t.Run(c.name, func(t *testing.T) {
			fsys := copyFS(base)
			setFile(t, fsys, seg1, c.data)
			checkRefused(t, fsys, seg1, last, c.reason)
		})
	}
	// A damaged record of a segment that is not the last one.
	multi, _ := build(t, Options{SegmentBytes: 100}, 3)
	b := fileData(t, multi, seg2)
	for _, c := range []struct {
		name   string
		data   []byte
		off    int64
		reason string
	}{
		{"damaged", func() []byte { d := slices.Clone(b); d[80] ^= 1; return d }(), segHeaderSize, "bad record CRC"},
		{"cut", b[:80], segHeaderSize, "truncated record"},
		{"zeros after", append(slices.Clone(b), make([]byte, 8)...), int64(len(b)), "bad record length 0"},
		{"no records", b[:segHeaderSize], -1, "no records"},
		{"a torn header", b[:30], -1, "short file"},
		{"a header of zeros", make([]byte, segHeaderSize), -1, "not a segment of whiteroom's log"},
	} {
		t.Run("segment 2 of 3: "+c.name, func(t *testing.T) {
			fsys := copyFS(multi)
			setFile(t, fsys, seg2, c.data)
			checkRefused(t, fsys, seg2, c.off, c.reason)
		})
	}
}

func TestTornLastHeader(t *testing.T) {
	o := Options{SegmentBytes: 100} // a segment of one record
	base, dumps := build(t, o, 3)
	header := appendSegHeader(nil, segHeader{gen: 1, index: 4, firstSeq: 4, salt: 7})
	damaged := slices.Clone(header)
	damaged[61] ^= 1
	for _, c := range []struct {
		name string
		data []byte
	}{
		{"empty", nil},
		{"torn", header[:30]},
		{"torn, with a byte in the header", slices.Concat(header[:30], []byte{1})},
		{"all but the CRC", header[:60]},
		{"zeros", make([]byte, segHeaderSize)},
		{"bad CRC", damaged},
		{"a page of zeros", make([]byte, 4096)},
		{"torn, then zeros", append(slices.Clone(header[:20]), make([]byte, 200)...)},
	} {
		t.Run(c.name, func(t *testing.T) {
			fsys := copyFS(base)
			setFile(t, fsys, seg4, c.data)
			db := reopen(t, fsys, o)
			if info := db.Recovery(); info.RemovedSegment != simPath(seg4) || info.Seq != 3 || info.TruncatedAt != -1 {
				t.Errorf("Recovery: %+v", info)
			}
			if got := dump(t, db); got != dumps[3] {
				t.Errorf("dump:\n%s", got)
			}
			if exists(t, fsys, seg4) || exists(t, fsys.Crash(loseAll), seg4) {
				t.Errorf("%s is left", seg4)
			}
			putKey(t, db, 1, "next", "x") // a new segment 4
			closeDB(t, db)
			db = reopen(t, fsys, o)
			if got := dump(t, db); got != dumps[3]+"\n1/next=x" || !exists(t, fsys, seg4) {
				t.Errorf("dump after the next Update:\n%s", got)
			}
		})
	}
	// Bytes after 64 that are not zeros were written after the header was synced: it was not torn.
	for _, c := range []struct {
		name, reason string
		data         []byte
	}{
		{"torn, then a byte", "bad header CRC", slices.Concat(header[:30], make([]byte, 70), []byte{1})},
		{"bad CRC, then a frame", "bad header CRC", slices.Concat(damaged, commitFrame(7, 4, 1, "k", "v"))},
		{"zeros, then a byte", "not a segment of whiteroom's log", slices.Concat(make([]byte, 100), []byte{1})},
	} {
		t.Run(c.name, func(t *testing.T) {
			fsys := copyFS(base)
			setFile(t, fsys, seg4, c.data)
			checkRefused(t, fsys, seg4, -1, c.reason)
		})
	}
	t.Run("the only segment", func(t *testing.T) {
		fsys, _ := build(t, o, 0)
		setFile(t, fsys, seg1, header[:30])
		checkRefused(t, fsys, seg1, -1, "short file")
	})
	t.Run("wrong first seq", func(t *testing.T) {
		fsys := copyFS(base)
		setFile(t, fsys, seg4, appendSegHeader(nil, segHeader{gen: 1, index: 4, firstSeq: 9, salt: 7}))
		checkRefused(t, fsys, seg4, -1, "the header names first sequence 9, want 4")
	})
}

func TestMissingSegment(t *testing.T) {
	base, _ := build(t, Options{SegmentBytes: 100}, 3)
	for _, c := range []struct {
		remove []string
		reason string
	}{
		{[]string{seg2}, "missing segment 2 of generation 1"},
		{[]string{seg1}, "missing segment 1 of generation 1"},
		{[]string{seg1, seg2}, "missing segment 1 of generation 1"},
		{[]string{seg1, seg2, seg3}, "no segments"},
	} {
		fsys := copyFS(base)
		for _, name := range c.remove {
			removeFile(t, fsys, name)
		}
		checkRefused(t, fsys, "log", -1, c.reason)
	}
	// the last one missing: the commits in it are lost, which Open cannot tell
	fsys := copyFS(base)
	removeFile(t, fsys, seg3)
	if db := reopen(t, fsys, Options{}); seqOf(t, db) != 2 {
		t.Errorf("seq %d", seqOf(t, db))
	}
}

func TestNameMismatch(t *testing.T) {
	base, _ := build(t, Options{SegmentBytes: 100}, 3)
	t.Run("swapped", func(t *testing.T) {
		fsys := copyFS(base)
		b2, b3 := fileData(t, fsys, seg2), fileData(t, fsys, seg3)
		setFile(t, fsys, seg2, b3)
		setFile(t, fsys, seg3, b2)
		checkRefused(t, fsys, seg2, -1, "the header names generation 1, index 3")
	})
	t.Run("another generation in the header", func(t *testing.T) {
		fsys := copyFS(base)
		b := fileData(t, fsys, seg3)
		h, _ := frames(t, fsys, seg3)
		h.gen = 2
		setFile(t, fsys, seg3, slices.Concat(appendSegHeader(nil, h), b[segHeaderSize:]))
		checkRefused(t, fsys, seg3, -1, "the header names generation 2, index 3")
	})
	for _, name := range []string{
		"log/notes.txt",
		"log/000000000000000A-0000000000000001.log",
		"log/0000000000000000-0000000000000001.log",
		"log/0000000000000001-0000000000000000.log",
		"log/0000000000000001-0000000000000001.log.tmp",
		"log/1-1.log",
	} {
		t.Run(name, func(t *testing.T) {
			fsys := copyFS(base)
			setFile(t, fsys, name, []byte("x"))
			checkRefused(t, fsys, name, -1, "not the name of a segment")
		})
	}
	t.Run("another generation", func(t *testing.T) {
		fsys := copyFS(base)
		name := segName(2, 1)
		setFile(t, fsys, name, appendSegHeader(nil, segHeader{gen: 2, index: 1, firstSeq: 4}))
		checkRefused(t, fsys, name, -1, "unsupported generation 2")
	})
	t.Run("a directory", func(t *testing.T) {
		fsys := copyFS(base)
		if err := fsys.Mkdir(seg4, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(fsys, Options{}); err == nil || err.Error() != simPath(seg4)+": must be a regular file" {
			t.Errorf("Open: %v", err)
		}
	})
}

func TestOpenNewerVersion(t *testing.T) {
	base, _ := build(t, Options{SegmentBytes: 100}, 3)
	for _, c := range []struct {
		name string
		at   int // of the version
	}{
		{seg1, 8},
		{seg3, 8},
		{"head", 8},
		{"head", headSlotSize + 8},
	} {
		t.Run(fmt.Sprintf("%s at %d", c.name, c.at), func(t *testing.T) {
			fsys := copyFS(base)
			b := fileData(t, fsys, c.name)
			binary.LittleEndian.PutUint32(b[c.at:], 2)
			setFile(t, fsys, c.name, b)
			before := fileMap(fsys)
			_, err := Open(fsys, Options{})
			checkVersion(t, err, simPath(c.name), 2)
			checkFilesUnchanged(t, fsys, before)
		})
	}
	t.Run("a torn last segment of a newer version", func(t *testing.T) {
		fsys := copyFS(base)
		h := appendSegHeader(nil, segHeader{gen: 1, index: 4, firstSeq: 4})
		binary.LittleEndian.PutUint32(h[8:], 2)
		setFile(t, fsys, seg4, h[:20])
		before := fileMap(fsys)
		_, err := Open(fsys, Options{})
		checkVersion(t, err, simPath(seg4), 2)
		checkFilesUnchanged(t, fsys, before)
	})
}

func TestDamagedHead(t *testing.T) {
	base, dumps := build(t, Options{}, 2)
	for _, c := range []struct {
		name string
		data []byte
	}{
		{"empty", nil},
		{"zeros", make([]byte, headSize)},
		{"too long", append(fileData(t, base, "head"), 0)},
		{"both slots damaged", func() []byte {
			b := fileData(t, base, "head")
			b[20]++
			b[headSlotSize+20]++
			return b
		}()},
	} {
		t.Run(c.name, func(t *testing.T) {
			fsys := copyFS(base)
			setFile(t, fsys, "head", c.data)
			db := reopen(t, fsys, Options{})
			if got := dump(t, db); got != dumps[2] {
				t.Errorf("dump:\n%s", got)
			}
			if s := readHeadFile(t, fsys); s.seq != 2 || s.counter != 1 || fileSize(t, fsys, "head") != headSize {
				t.Errorf("head written again: %+v", s)
			}
			putKey(t, db, 1, "next", "x")
			if s := readHeadFile(t, fsys); s.seq != 3 || s.counter != 2 {
				t.Errorf("head after an Update: %+v", s)
			}
		})
	}
}

func TestHeadWriteErrors(t *testing.T) {
	fsys, db := newSim(t, Options{})
	fsys.Inject(simfs.Fault{Match: on(simfs.OpWrite, "head"), Err: syscall.ENOSPC, Short: 30, Times: 1})
	putKey(t, db, 1, "a", "1") // the head write fails and damages its slot; the commit does not fail
	if n := db.headErrors.Load(); n != 1 {
		t.Errorf("%d head errors", n)
	}
	if s := readHeadFile(t, fsys); s.seq != 0 {
		t.Errorf("head after its write failed: %+v", s)
	}
	putKey(t, db, 1, "b", "2") // in the same slot again, so that the other one stays valid
	if s := readHeadFile(t, fsys); s.seq != 2 || s.counter != 3 {
		t.Errorf("head: %+v", s)
	}
	closeDB(t, db)
	if s := readHeadFile(t, fsys); s.seq != 2 || s.counter != 4 || len(pendingOn(fsys, "head")) != 0 {
		t.Errorf("head after Close: %+v", s)
	}
}

func TestReadErrorNoTruncate(t *testing.T) {
	base, dumps := build(t, Options{}, 2)
	full := fileData(t, base, seg1)
	torn := append(slices.Clone(full), commitFrame(0, 3, 1, "torn", "x")[:20]...)
	for _, c := range []struct {
		name  string
		match func(simfs.Op) bool
		want  string
	}{
		{"read of the segment", on(simfs.OpRead, seg1), "read " + simPath(seg1) + ": input/output error"},
		{"read of head", on(simfs.OpRead, "head"), "read " + simPath("head") + ": input/output error"},
		{"readdir of log", on(simfs.OpReadDir, "log"), "readdirent " + simPath("log") + ": input/output error"},
		{"lstat", on(simfs.OpLstat, "objects"), simPath("objects") + ": cannot read: input/output error"},
	} {
		t.Run(c.name, func(t *testing.T) {
			fsys := copyFS(base)
			setFile(t, fsys, seg1, torn)
			fsys.Inject(simfs.Fault{Match: c.match, Err: syscall.EIO, Times: 1})
			before := fileMap(fsys)
			if _, err := Open(fsys, Options{}); !errors.Is(err, syscall.EIO) || err.Error() != c.want {
				t.Errorf("Open: %v, want %s", err, c.want)
			}
			checkFilesUnchanged(t, fsys, before)
			checkTorn(t, fsys, seg1, int64(len(full)), dumps[2], 2)
		})
	}
}

func TestRecoverySyncsTail(t *testing.T) {
	t.Run("sync fails", func(t *testing.T) {
		base, _ := build(t, Options{}, 2)
		fsys := copyFS(base)
		fsys.Inject(simfs.Fault{Match: on(simfs.OpSync, seg1), Err: syscall.EIO, Times: 1})
		if _, err := Open(fsys, Options{WriterWait: 1}); !errors.Is(err, syscall.EIO) {
			t.Errorf("Open: %v", err)
		}
		reopen(t, fsys, Options{WriterWait: 1}) // the locks were released
	})
	t.Run("a record in the page cache", func(t *testing.T) {
		// A writer that was killed after it wrote a record and before its Sync.
		fsys, dumps := build(t, Options{}, 2)
		h, _ := frames(t, fsys, seg1)
		f, err := fsys.OpenFile(seg1, os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		size := fileSize(t, fsys, seg1)
		if _, err := f.WriteAt(commitFrame(h.salt, 3, 1, "unsynced", "x"), size); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		if db := reopen(t, fsys.Crash(loseAll), Options{}); seqOf(t, db) != 2 {
			t.Fatal("the record is durable before Open")
		}
		db := reopen(t, fsys, Options{})
		if seqOf(t, db) != 3 {
			t.Fatalf("seq %d", seqOf(t, db))
		}
		// Readers may be told of it now, so a crash must keep it.
		db = reopen(t, fsys.Crash(loseAll), Options{})
		if got := dump(t, db); got != dumps[2]+"\n1/unsynced=x" {
			t.Errorf("after a crash:\n%s", got)
		}
	})
	t.Run("a segment whose name is not durable", func(t *testing.T) {
		// A writer that was killed after it made a segment and synced its header, before it synced log/.
		fsys, dumps := build(t, Options{}, 1)
		f, err := fsys.OpenFile(seg2, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteAt(appendSegHeader(nil, segHeader{gen: 1, index: 2, firstSeq: 2, salt: 9}), 0); err != nil {
			t.Fatal(err)
		}
		if err := errors.Join(f.Sync(), f.Close()); err != nil {
			t.Fatal(err)
		}
		db := reopen(t, fsys, Options{})
		putKey(t, db, 1, "z in segment 2", "x") // acknowledged: it must survive a crash
		db = reopen(t, fsys.Crash(loseAll), Options{})
		if got := dump(t, db); got != dumps[1]+"\n1/z in segment 2=x" {
			t.Errorf("after a crash:\n%s", got)
		}
	})
}

func TestAccept(t *testing.T) {
	base, dumps := build(t, Options{}, 2)
	full := fileData(t, base, seg1)
	torn := append(slices.Clone(full), commitFrame(0, 3, 1, "torn", "x")[:20]...)
	refused := errors.New("the store schema is newer")
	t.Run("refused", func(t *testing.T) {
		fsys := copyFS(base)
		setFile(t, fsys, seg1, torn)
		before := fileMap(fsys)
		_, err := Open(fsys, Options{WriterWait: 1, Accept: func(tx *Tx) error {
			if v, ok := tx.Get(1, []byte("k2")); tx.Seq() != 2 || !ok || string(v) != "value 2." {
				t.Errorf("Accept sees seq %d, %q", tx.Seq(), v)
			}
			if err := tx.Put(1, []byte("k"), nil); !errors.Is(err, ErrReadOnly) {
				t.Errorf("Put in Accept: %v", err)
			}
			return refused
		}})
		if !errors.Is(err, refused) {
			t.Errorf("Open: %v", err)
		}
		checkFilesUnchanged(t, fsys, before)
		checkTorn(t, fsys, seg1, int64(len(full)), dumps[2], 2)
	})
	t.Run("panics", func(t *testing.T) {
		fsys := copyFS(base)
		func() {
			defer func() {
				if r := recover(); r != "in Accept" {
					t.Errorf("recovered %v", r)
				}
			}()
			_, _ = Open(fsys, Options{Accept: func(*Tx) error { panic("in Accept") }})
		}()
		reopen(t, fsys, Options{WriterWait: 1}) // the locks were released
	})
	t.Run("accepted", func(t *testing.T) {
		fsys := copyFS(base)
		called := false
		db := reopen(t, fsys, Options{Accept: func(*Tx) error {
			called = true
			return nil
		}})
		if !called || dump(t, db) != dumps[2] {
			t.Error("Accept was not called")
		}
		if err := db.View(context.Background(), func(*Tx) error { return nil }); err != nil {
			t.Error(err)
		}
	})
}

// TestCrashDuringRecovery crashes after each operation of an Open that cuts a torn tail or removes a torn segment, and
// checks that every crash that simfs can make of it opens to the same state.
func TestCrashDuringRecovery(t *testing.T) {
	o := Options{SegmentBytes: 100} // a segment of one record
	built, dumps := build(t, o, 2)
	for _, c := range []struct {
		name   string
		damage func(fsys *simfs.FS)
	}{
		{"torn tail", func(fsys *simfs.FS) {
			h, _ := frames(t, fsys, seg2)
			setFile(t, fsys, seg2, slices.Concat(fileData(t, fsys, seg2), commitFrame(h.salt, 3, 1, "torn", "x")[:20]))
		}},
		{"torn segment", func(fsys *simfs.FS) { setFile(t, fsys, seg3, make([]byte, 30)) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			base := copyFS(built)
			c.damage(base)
			probe := copyFS(base)
			db := reopen(t, probe, o)
			ops := probe.Counted()
			closeDB(t, db)
			crashes := 0
			for k := range ops + 1 {
				fsys := copyFS(base)
				fsys.CrashAt(k)
				if db, err := Open(fsys, o); err == nil {
					_ = db.Close()
				}
				for _, m := range simfs.Models(fsys.Pending(), 16, nil) {
					db, err := Open(fsys.Crash(m), o)
					if err != nil {
						t.Fatalf("a crash after %d of %d operations, %s: Open: %v", k, ops, m.Name, err)
					}
					if got := dump(t, db); got != dumps[2] {
						t.Errorf("a crash after %d of %d operations, %s:\n%s", k, ops, m.Name, got)
					}
					closeDB(t, db)
					crashes++
				}
			}
			t.Logf("%d crash points, %d crashes", ops+1, crashes)
		})
	}
}
