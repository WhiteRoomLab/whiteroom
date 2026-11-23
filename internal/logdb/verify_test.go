package logdb

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/logdb/simfs"
)

var verifyOptions = Options{Placement: Hybrid, Threshold: 100, SegmentBytes: 300}

var verifyRoles = []struct {
	name string
	open func(t *testing.T, fsys *simfs.FS, o Options) *DB
}{
	{"writer", func(t *testing.T, fsys *simfs.FS, o Options) *DB { return reopen(t, fsys, o) }},
	{"reader", func(t *testing.T, fsys *simfs.FS, o Options) *DB {
		t.Helper()
		db, err := OpenReader(fsys, o)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		return db
	}},
}

func problemsOf(t *testing.T, db *DB) []Problem {
	t.Helper()
	var ps []Problem
	err := db.VerifyEach(context.Background(), func(p Problem) bool {
		ps = append(ps, p)
		return true
	})
	if err != nil {
		t.Fatalf("VerifyEach: %v", err)
	}
	return ps
}

func problemLines(ps []Problem) string {
	var b strings.Builder
	for _, p := range ps {
		fmt.Fprintf(&b, "\n%s", p)
	}
	return b.String()
}

// verifyStore is a closed store of nine Updates in four or more segments, with its checkpoint in the last, and what
// the tests need to know of its objects: the hashes in order, which are in files and which inline, and their data. Its
// last Update has no object, so no object is in the record that head and the checkpoint name.
type verifyStore struct {
	fsys                  *simfs.FS
	hashes, files, inline [][32]byte
	locs                  map[[32]byte]objLoc
	data                  map[[32]byte][]byte
}

func newVerifyStore(t *testing.T) *verifyStore {
	t.Helper()
	fsys, db := newSim(t, verifyOptions)
	v := &verifyStore{fsys: fsys, locs: map[[32]byte]objLoc{}, data: map[[32]byte][]byte{}}
	for i := range 9 {
		change(t, db, i)
		d := objData(i, 50+100*(i%2))
		v.data[sha256.Sum256(d)] = d
	}
	putKey(t, db, 5, "KEYMARK", "VALMARK")
	db.cur.Load().objects.Ascend("", func(k string, loc objLoc) bool {
		h := [32]byte([]byte(k))
		v.hashes = append(v.hashes, h)
		v.locs[h] = loc
		if loc.placement == placeInline {
			v.inline = append(v.inline, h)
		} else {
			v.files = append(v.files, h)
		}
		return true
	})
	closeDB(t, db)
	if n := len(logNames(t, fsys)); n < 4 || ckpOf(t, fsys).seg != segNum(n) || len(v.files) != 4 {
		t.Fatalf("%d segments, the checkpoint in segment %d, %d objects in files", n, ckpOf(t, fsys).seg, len(v.files))
	}
	return v
}

// afterFirst returns an inline object that is not in the first segment.
func (v *verifyStore) afterFirst(t *testing.T) [32]byte {
	t.Helper()
	for _, h := range v.inline {
		if v.locs[h].seg > 1 {
			return h
		}
	}
	t.Fatal("no inline object after the first segment")
	return [32]byte{}
}

func fileProblem(h [32]byte, reason string) Problem {
	return Problem{Path: simPath(objName(h)), Offset: -1, Reason: reason}
}

func hashReason(h, got [32]byte) string {
	return fmt.Sprintf("object %x: the data has the hash %x", h, got)
}

// objectDamages are the damages of the file of an object. Each returns the problem that VerifyEach finds, and the error
// that Verify returns.
var objectDamages = []struct {
	name  string
	apply func(t *testing.T, fsys *simfs.FS, h [32]byte) (Problem, string)
}{
	{"a missing file", func(t *testing.T, fsys *simfs.FS, h [32]byte) (Problem, string) {
		removeFile(t, fsys, objName(h))
		return fileProblem(h, "missing file"), simPath(objName(h)) + ": missing file"
	}},
	{"a short file", func(t *testing.T, fsys *simfs.FS, h [32]byte) (Problem, string) {
		b := fileData(t, fsys, objName(h))
		setFile(t, fsys, objName(h), b[:len(b)-1])
		reason := fmt.Sprintf("has %d bytes, want %d", len(b)-1, len(b))
		return fileProblem(h, reason), simPath(objName(h)) + ": corrupt: " + reason
	}},
	{"a bad header", func(t *testing.T, fsys *simfs.FS, h [32]byte) (Problem, string) {
		b := fileData(t, fsys, objName(h))
		b[0] = 'W'
		setFile(t, fsys, objName(h), b)
		const reason = "not an object of whiteroom's log"
		return fileProblem(h, reason), simPath(objName(h)) + ": corrupt: " + reason
	}},
	{"data that do not have their hash", func(t *testing.T, fsys *simfs.FS, h [32]byte) (Problem, string) {
		b := fileData(t, fsys, objName(h))
		b[len(b)-1] ^= 1
		setFile(t, fsys, objName(h), b)
		reason := hashReason(h, sha256.Sum256(b[objHeaderSize:]))
		return fileProblem(h, reason), reason
	}},
	{"a header of a newer version", func(t *testing.T, fsys *simfs.FS, h [32]byte) (Problem, string) {
		b := fileData(t, fsys, objName(h))
		binary.LittleEndian.PutUint32(b[8:], formatVersion+1)
		setFile(t, fsys, objName(h), b)
		reason := fmt.Sprintf("format version %d, newer than this program knows (%d)", formatVersion+1, formatVersion)
		return fileProblem(h, reason), simPath(objName(h)) + ": " + reason
	}},
	{"a directory", func(t *testing.T, fsys *simfs.FS, h [32]byte) (Problem, string) {
		removeFile(t, fsys, objName(h))
		if err := fsys.Mkdir(objName(h), 0o700); err != nil {
			t.Fatal(err)
		}
		return fileProblem(h, "must be a regular file"), simPath(objName(h)) + ": must be a regular file"
	}},
}

// damageInline changes a byte of the data of the inline object h in its record, which keeps a valid CRC, so that only
// the hash of the data tells.
func damageInline(t *testing.T, fsys *simfs.FS, v *verifyStore, h [32]byte) (Problem, string) {
	t.Helper()
	loc := v.locs[h]
	name := segName(1, loc.seg)
	hdr, offs := frames(t, fsys, name)
	off := int64(0)
	for _, o := range offs {
		if o <= int64(loc.off) {
			off = o
		}
	}
	b := fileData(t, fsys, name)
	body, n, reason := parseFrame(b[off:], hdr.salt)
	if reason != "" {
		t.Fatal(reason)
	}
	body = slices.Clone(body)
	body[int64(loc.off)-off-8] ^= 1
	frame := appendFrame(nil, hdr.salt, body)
	if len(frame) != n {
		t.Fatalf("the frame has %d bytes, was %d", len(frame), n)
	}
	setFile(t, fsys, name, slices.Concat(b[:off], frame, b[off+int64(n):]))
	data := slices.Clone(v.data[h])
	data[0] ^= 1
	reason = hashReason(h, sha256.Sum256(data))
	return Problem{Path: simPath(name), Offset: int64(loc.off), Reason: reason}, reason
}

// damageRecord changes the first record of the first segment, which is before the checkpoint.
func damageRecord(t *testing.T, fsys *simfs.FS) (Problem, string) {
	t.Helper()
	_, offs := frames(t, fsys, seg1)
	b := fileData(t, fsys, seg1)
	b[offs[0]+8] ^= 1
	setFile(t, fsys, seg1, b)
	return Problem{Path: simPath(seg1), Offset: offs[0], Reason: "bad record CRC"},
		fmt.Sprintf("%s: corrupt at offset %d: bad record CRC", simPath(seg1), offs[0])
}

// damageAll damages the first record, the four files of objects in four ways, and an inline object; the problems
// are those of the log, then those of the objects in the order of their hashes.
func damageAll(t *testing.T, fsys *simfs.FS, v *verifyStore) ([]Problem, string) {
	t.Helper()
	first, text := damageRecord(t, fsys)
	byHash := map[[32]byte]Problem{}
	for i, h := range v.files {
		byHash[h], _ = objectDamages[i].apply(t, fsys, h)
	}
	ih := v.afterFirst(t)
	byHash[ih], _ = damageInline(t, fsys, v, ih)
	want := []Problem{first}
	for _, h := range v.hashes {
		if p, ok := byHash[h]; ok {
			want = append(want, p)
		}
	}
	if len(want) != 6 {
		t.Fatalf("%d problems wanted", len(want))
	}
	return want, text
}

func stateProblem(detail string) Problem {
	return Problem{Path: simDir, Offset: -1, Reason: "the state in memory differs from the log: " + detail}
}

// differState changes the state in memory of db in every kind of way that Verify tells, and returns the problems that
// VerifyEach must find: the differences, then that of the object that memory has and no file has.
func differState(t *testing.T, db *DB, v *verifyStore) []Problem {
	t.Helper()
	cur := db.cur.Load()
	f0, fake := v.files[0], [32]byte{0xff, 1, 2}
	n2 := cur.tables[2].Len()
	changeState(db, func(st *state) {
		st.seq++
		st.lastCRC++
		st.nextEvent++
		st.events += 2
		st.tables[1] = st.tables[1].Put(nil, "k08", []byte("MEMVAL1")) // a key that the log has
		st.tables[1] = st.tables[1].Put(nil, "MEMKEY1", []byte("MEMVAL1"))
		delete(st.tables, 2)
		st.tables[3] = pmap[[]byte]{}.Put(nil, "MEMKEY3", []byte("MEMVAL3"))
		st.tables[5] = st.tables[5].Put(nil, "KEYMARK", []byte("MEMVAL5"))
		st.objects, _ = st.objects.Delete(nil, string(f0[:]))
		st.objects = st.objects.Put(nil, string(fake[:]), objLoc{placement: placeFile, size: 7})
	})
	return []Problem{
		stateProblem(fmt.Sprintf("sequence: %d in the log, %d in memory", cur.seq, cur.seq+1)),
		stateProblem(fmt.Sprintf("CRC of the last record: %d in the log, %d in memory", cur.lastCRC, cur.lastCRC+1)),
		stateProblem(fmt.Sprintf("next event: %d in the log, %d in memory", cur.nextEvent, cur.nextEvent+1)),
		stateProblem(fmt.Sprintf("events: %d in the log, %d in memory", cur.events, cur.events+2)),
		stateProblem("table 1, keys: 1 with another value, 0 missing in memory, 1 missing in the log"),
		stateProblem(fmt.Sprintf("table 2, keys: 0 with another value, %d missing in memory, 0 missing in the log", n2)),
		stateProblem("table 3, keys: 0 with another value, 0 missing in memory, 1 missing in the log"),
		stateProblem("table 5, keys: 1 with another value, 0 missing in memory, 0 missing in the log"),
		stateProblem(fmt.Sprintf("object %x: in a file (%d bytes) in the log, missing in memory", f0, v.locs[f0].size)),
		stateProblem(fmt.Sprintf("object %x: missing in the log, in a file (7 bytes) in memory", fake)),
		fileProblem(fake, "missing file"),
	}
}

func TestVerifyEachSound(t *testing.T) {
	for _, pl := range []struct {
		name string
		p    Placement
	}{{"inline", Inline}, {"files", Files}, {"hybrid", Hybrid}} {
		for _, gen := range []genNum{1, 2} {
			for _, ckp := range []bool{true, false} {
				t.Run(fmt.Sprintf("%s, generation %d, checkpoint %v", pl.name, gen, ckp), func(t *testing.T) {
					o := Options{Placement: pl.p, Threshold: 100, SegmentBytes: 300}
					fsys, db := newSim(t, o)
					for i := range 9 {
						change(t, db, i)
					}
					if gen == 2 {
						if err := compactDB(db, Rewrite{}); err != nil {
							t.Fatal(err)
						}
						for i := 9; i < 12; i++ {
							change(t, db, i)
						}
					}
					closeDB(t, db)
					if !ckp {
						dropCheckpoint(t, fsys)
					}
					for _, role := range verifyRoles {
						db := role.open(t, copyFS(fsys), o)
						if got := db.cur.Load().gen; got != gen {
							t.Fatalf("%s: generation %d", role.name, got)
						}
						if used := db.Recovery().Checkpoint == "used"; used != ckp {
							t.Errorf("%s: Recovery: %+v", role.name, db.Recovery())
						}
						if ps := problemsOf(t, db); len(ps) > 0 {
							t.Errorf("%s: problems of a sound store:%s", role.name, problemLines(ps))
						}
						if err := db.Verify(context.Background()); err != nil {
							t.Errorf("%s: Verify: %v", role.name, err)
						}
					}
				})
			}
		}
	}
}

func TestVerifyEach(t *testing.T) {
	v := newVerifyStore(t)
	type damage struct {
		name   string
		change func(t *testing.T, fsys *simfs.FS, db *DB) ([]Problem, string)
	}
	cases := []damage{
		{"a record before the checkpoint", func(t *testing.T, fsys *simfs.FS, _ *DB) ([]Problem, string) {
			p, text := damageRecord(t, fsys)
			return []Problem{p}, text
		}},
		{"a record before the checkpoint and a state that differs", func(t *testing.T, fsys *simfs.FS, db *DB) ([]Problem, string) {
			changeState(db, func(st *state) { st.seq++ })
			p, text := damageRecord(t, fsys)
			return []Problem{p}, text
		}},
		{"an inline object that has another hash", func(t *testing.T, fsys *simfs.FS, _ *DB) ([]Problem, string) {
			p, text := damageInline(t, fsys, v, v.inline[0])
			return []Problem{p}, text
		}},
		{"a segment that is gone", func(t *testing.T, fsys *simfs.FS, _ *DB) ([]Problem, string) {
			removeFile(t, fsys, seg1)
			text := simPath(seg1) + ": missing file"
			want := []Problem{{Offset: -1, Reason: text}}
			for _, h := range v.inline {
				if v.locs[h].seg == 1 {
					want = append(want, Problem{Path: simPath(seg1), Offset: -1, Reason: "missing file"})
				}
			}
			return want, text
		}},
		{"a record and objects of every kind", func(t *testing.T, fsys *simfs.FS, _ *DB) ([]Problem, string) {
			return damageAll(t, fsys, v)
		}},
	}
	for _, d := range objectDamages {
		cases = append(cases, damage{d.name, func(t *testing.T, fsys *simfs.FS, _ *DB) ([]Problem, string) {
			p, text := d.apply(t, fsys, v.files[0])
			return []Problem{p}, text
		}})
	}
	for _, role := range verifyRoles {
		t.Run(role.name, func(t *testing.T) {
			for _, c := range cases {
				t.Run(c.name, func(t *testing.T) {
					fsys := copyFS(v.fsys)
					db := role.open(t, fsys, verifyOptions)
					want, text := c.change(t, fsys, db)
					got := problemsOf(t, db)
					if !slices.Equal(got, want) {
						t.Errorf("problems:%s\nwant:%s", problemLines(got), problemLines(want))
					}
					err := db.Verify(context.Background())
					if err == nil || err.Error() != text {
						t.Errorf("Verify: %v\nwant: %s", err, text)
					}
					if len(want) > 0 && want[0].Offset >= 0 && want[0].Reason == "bad record CRC" {
						var ce *CorruptError
						if !errors.As(err, &ce) || ce.Path != want[0].Path || ce.Offset != want[0].Offset {
							t.Errorf("Verify: %v, want a CorruptError at %s", err, want[0])
						}
					}
				})
			}
		})
	}
}

func TestVerifyEachBeforeCheckpoint(t *testing.T) {
	v := newVerifyStore(t)
	fsys := copyFS(v.fsys)
	want, _ := damageRecord(t, fsys)
	db := reopen(t, fsys, verifyOptions)
	if info := db.Recovery(); info.Checkpoint != "used" || info.Replayed != 0 {
		t.Fatalf("Open read the damaged segment: %+v", info)
	}
	if got := problemsOf(t, db); !slices.Equal(got, []Problem{want}) {
		t.Errorf("problems:%s\nwant: %s", problemLines(got), want)
	}
}

func TestVerifyEachState(t *testing.T) {
	v := newVerifyStore(t)
	for _, role := range verifyRoles {
		t.Run(role.name, func(t *testing.T) {
			db := role.open(t, copyFS(v.fsys), verifyOptions)
			want := differState(t, db, v)
			got := problemsOf(t, db)
			if !slices.Equal(got, want) {
				t.Errorf("problems:%s\nwant:%s", problemLines(got), problemLines(want))
			}
			for _, p := range got {
				for _, secret := range []string{"KEYMARK", "VALMARK", "MEMKEY", "MEMVAL", "k08"} {
					if strings.Contains(p.String(), secret) {
						t.Errorf("the problem %q has %q", p, secret)
					}
				}
			}
			if err := db.Verify(context.Background()); err == nil || err.Error() != want[0].String() {
				t.Errorf("Verify: %v\nwant: %s", err, want[0])
			}
		})
	}
}

func TestVerifyEachStops(t *testing.T) {
	v := newVerifyStore(t)
	check := func(t *testing.T, db *DB, want []Problem) {
		t.Helper()
		for k := 1; k <= len(want)+1; k++ {
			var got []Problem
			err := db.VerifyEach(context.Background(), func(p Problem) bool {
				got = append(got, p)
				return len(got) < k
			})
			if err != nil || !slices.Equal(got, want[:min(k, len(want))]) {
				t.Errorf("found returns false at call %d: %v, problems:%s\nwant the first %d of:%s", k, err, problemLines(got), k,
					problemLines(want))
			}
		}
	}
	t.Run("a log and objects", func(t *testing.T) {
		fsys := copyFS(v.fsys)
		db := reopen(t, fsys, verifyOptions)
		want, _ := damageAll(t, fsys, v)
		check(t, db, want)
	})
	t.Run("a state and objects", func(t *testing.T) {
		db := reopen(t, copyFS(v.fsys), verifyOptions)
		check(t, db, differState(t, db, v))
	})
}

func TestVerifyEachContext(t *testing.T) {
	v := newVerifyStore(t)
	fsys := copyFS(v.fsys)
	db := reopen(t, fsys, verifyOptions)
	want, _ := damageAll(t, fsys, v)
	collect := func(ctx context.Context) ([]Problem, error) {
		var got []Problem
		err := db.VerifyEach(ctx, func(p Problem) bool {
			got = append(got, p)
			return true
		})
		return got, err
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := collect(canceled); !errors.Is(err, context.Canceled) || len(got) > 0 {
		t.Errorf("a canceled context: %v, problems:%s", err, problemLines(got))
	}
	// ctx is checked at the start, before each record that is read, and before each object.
	total := 0
	for left := 0; ; left++ {
		got, err := collect(&countdownContext{Context: context.Background(), left: left})
		if err == nil {
			total = left
			if !slices.Equal(got, want) {
				t.Errorf("problems:%s\nwant:%s", problemLines(got), problemLines(want))
			}
			break
		}
		if !errors.Is(err, context.Canceled) || !slices.Equal(got, want[:len(got)]) {
			t.Fatalf("a context that ends after %d checks: %v, problems:%s", left, err, problemLines(got))
		}
		if left > 1000 {
			t.Fatal("the context never lets VerifyEach end")
		}
	}
	if least := 1 + 1 + len(v.hashes); total < least {
		t.Errorf("%d checks of the context, want %d or more", total, least)
	}
}

func TestVerifyEachHoldsUpdates(t *testing.T) {
	v := newVerifyStore(t)
	fsys := copyFS(v.fsys)
	db := reopen(t, fsys, verifyOptions)
	removeFile(t, fsys, objName(v.files[0]))
	ctx := context.Background()
	inFound, release := make(chan struct{}), make(chan struct{})
	verified, updated := make(chan error, 1), make(chan error, 1)
	calls := 0
	go func() {
		verified <- db.VerifyEach(ctx, func(Problem) bool {
			if calls++; calls == 1 {
				close(inFound)
				<-release
			}
			return true
		})
	}()
	wait(t, inFound, "problem")
	go func() { updated <- db.Update(ctx, putF(7, "k", "v")) }()
	early := false
	select {
	case err := <-updated:
		early = true
		t.Errorf("an Update ended while VerifyEach ran: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := wait(t, verified, "VerifyEach"); err != nil {
		t.Errorf("VerifyEach: %v", err)
	}
	if !early {
		if err := wait(t, updated, "the Update"); err != nil {
			t.Errorf("Update: %v", err)
		}
	}
	if calls != 1 {
		t.Errorf("found was called %d times", calls)
	}
}

func TestVerifyEachClosed(t *testing.T) {
	v := newVerifyStore(t)
	for _, role := range verifyRoles {
		db := role.open(t, copyFS(v.fsys), verifyOptions)
		closeDB(t, db)
		err := db.VerifyEach(context.Background(), func(Problem) bool {
			t.Errorf("%s: found was called", role.name)
			return true
		})
		if !errors.Is(err, errClosed) {
			t.Errorf("%s: VerifyEach after Close: %v", role.name, err)
		}
	}
}

func TestProblemString(t *testing.T) {
	for _, c := range []struct {
		p    Problem
		want string
	}{
		{Problem{Path: "/d/log/a", Offset: 64, Reason: "bad record CRC"}, "/d/log/a at offset 64: bad record CRC"},
		{Problem{Path: "/d/log/a", Offset: -1, Reason: "missing file"}, "/d/log/a: missing file"},
		{Problem{Offset: -1, Reason: "something"}, "something"},
		{Problem{Offset: 8, Reason: "something"}, "offset 8: something"},
		{Problem{Path: "/d/log/a", Offset: 0, Reason: "x"}, "/d/log/a at offset 0: x"},
	} {
		if got := c.p.String(); got != c.want {
			t.Errorf("%+v: %q, want %q", c.p, got, c.want)
		}
	}
}
