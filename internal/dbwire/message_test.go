package dbwire

import (
	"bytes"
	"errors"
	"io"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

const (
	testMaxKey   = MaxKey
	testMaxValue = 1 << 16
)

// codec reads the whole body of one message and returns what it has: the value, and a function that writes the same
// body again. exact is false for the bodies that have more than one form for the same value (the chunks of a file).
type codec struct {
	name  string
	kind  Kind
	reply bool
	exact bool
	dec   func(d *Dec) (v any, enc func(e *Enc), err error)
}

func emptyCodec(name string, kind Kind, reply bool) codec {
	return codec{name, kind, reply, true, func(d *Dec) (any, func(*Enc), error) {
		return nil, func(*Enc) {}, ReadEmpty(d)
	}}
}

func readAllChunks(d *Dec) ([]byte, error) {
	return io.ReadAll(NewChunkReader(d, testMaxValue))
}

var codecs = []codec{
	{"Hello request", KindHello, false, true, func(d *Dec) (any, func(*Enc), error) {
		v, err := ReadHelloRequest(d)
		if err == nil {
			err = d.End()
		}
		return v, func(e *Enc) { e.Raw(Magic[:]); e.U32(v) }, err
	}},
	{"Hello reply", KindHello, true, true, func(d *Dec) (any, func(*Enc), error) {
		h, err := ReadHelloReply(d)
		return h, func(e *Enc) { e.HelloReply(h) }, err
	}},
	emptyCodec("Snapshot request", KindSnapshot, false),
	{"Snapshot reply", KindSnapshot, true, true, func(d *Dec) (any, func(*Enc), error) {
		seq, err := ReadSnapshotReply(d)
		return seq, func(e *Enc) { e.SnapshotReply(seq) }, err
	}},
	emptyCodec("Release request", KindRelease, false),
	emptyCodec("Release reply", KindRelease, true),
	{"Get request", KindGet, false, true, func(d *Dec) (any, func(*Enc), error) {
		table, key, err := ReadGetRequest(d, testMaxKey)
		return []any{table, key}, func(e *Enc) { e.GetRequest(table, key) }, err
	}},
	{"Get reply", KindGet, true, true, func(d *Dec) (any, func(*Enc), error) {
		found, value, err := ReadGetReply(d, testMaxValue)
		return []any{found, value}, func(e *Enc) { e.GetReply(found, value) }, err
	}},
	{"Scan request", KindScan, false, true, func(d *Dec) (any, func(*Enc), error) {
		r, err := ReadScanRequest(d, testMaxKey)
		return r, func(e *Enc) { e.ScanRequest(r) }, err
	}},
	{"Scan reply", KindScan, true, true, func(d *Dec) (any, func(*Enc), error) {
		var rows [][2][]byte
		for {
			k, v, ok := ReadScanRow(d, testMaxKey, testMaxValue)
			if !ok {
				break
			}
			rows = append(rows, [2][]byte{k, v})
		}
		more, err := ReadScanEnd(d)
		return []any{rows, more}, func(e *Enc) {
			for _, r := range rows {
				e.ScanRow(r[0], r[1])
			}
			e.ScanEnd(more)
		}, err
	}},
	{"Object request", KindObject, false, true, func(d *Dec) (any, func(*Enc), error) {
		h, err := ReadObjectRequest(d)
		return h, func(e *Enc) { e.ObjectRequest(h) }, err
	}},
	{"Object reply", KindObject, true, true, func(d *Dec) (any, func(*Enc), error) {
		found, data, err := ReadObjectReply(d, testMaxValue)
		return []any{found, data}, func(e *Enc) { e.ObjectReply(found, data) }, err
	}},
	{"Original request", KindOriginal, false, true, func(d *Dec) (any, func(*Enc), error) {
		h, err := ReadOriginalRequest(d)
		return h, func(e *Enc) { e.OriginalRequest(h) }, err
	}},
	{"Original reply", KindOriginal, true, true, func(d *Dec) (any, func(*Enc), error) {
		size, err := ReadOriginalReply(d, testMaxValue)
		var data []byte
		if err == nil {
			data, err = io.ReadAll(d.Stream(size))
		}
		if err == nil {
			err = d.End()
		}
		return []any{size, data}, func(e *Enc) { e.OriginalReply(size); e.Raw(data) }, err
	}},
	emptyCodec("Events request", KindEvents, false),
	{"Events reply", KindEvents, true, true, func(d *Dec) (any, func(*Enc), error) {
		var events []Event
		for {
			ev, ok := ReadEvent(d, testMaxValue)
			if !ok {
				break
			}
			events = append(events, ev)
		}
		return events, func(e *Enc) {
			for _, ev := range events {
				e.Event(ev)
			}
			e.EventsEnd()
		}, d.Err
	}},
	emptyCodec("Verify request", KindVerify, false),
	emptyCodec("Verify reply", KindVerify, true),
	emptyCodec("Backup request", KindBackup, false),
	{"Backup reply", KindBackup, true, false, func(d *Dec) (any, func(*Enc), error) {
		var got []any
		for {
			en, ok := ReadBackupEntry(d)
			if !ok {
				break
			}
			got = append(got, en)
			if en.Kind == EntryFile {
				content, err := readAllChunks(d)
				if err != nil {
					return got, func(*Enc) {}, err
				}
				got = append(got, content)
			}
		}
		return got, func(e *Enc) {
			for _, x := range got {
				switch x := x.(type) {
				case BackupEntry:
					e.BackupEntry(x)
				case []byte:
					c := e.Chunks()
					_, _ = c.Write(x)
					_ = c.Close()
				}
			}
			e.BackupEntry(BackupEntry{Kind: EntryEnd})
		}, d.Err
	}},
	{"PutObject request", KindPutObject, false, true, func(d *Dec) (any, func(*Enc), error) {
		h, data, err := ReadPutObjectRequest(d, testMaxValue)
		return []any{h, data}, func(e *Enc) { e.PutObjectRequest(h, data) }, err
	}},
	emptyCodec("PutObject reply", KindPutObject, true),
	{"PutOriginal request", KindPutOriginal, false, false, func(d *Dec) (any, func(*Enc), error) {
		content, err := readAllChunks(d)
		if err != nil {
			return nil, func(*Enc) {}, err
		}
		h, size, err := ReadPutOriginalTrailer(d)
		return []any{content, h, size}, func(e *Enc) {
			c := e.Chunks()
			_, _ = c.Write(content)
			_ = c.Close()
			e.PutOriginalTrailer(h, size)
		}, err
	}},
	emptyCodec("PutOriginal reply", KindPutOriginal, true),
	{"Commit request", KindCommit, false, true, func(d *Dec) (any, func(*Enc), error) {
		expected, err := ReadCommitRequest(d)
		if err != nil {
			return nil, func(*Enc) {}, err
		}
		var ops []Op
		for {
			o, ok := ReadCommitOp(d, testMaxKey, testMaxValue)
			if !ok {
				break
			}
			ops = append(ops, o)
		}
		return []any{expected, ops}, func(e *Enc) {
			e.CommitRequest(expected)
			for _, o := range ops {
				e.CommitOp(o)
			}
			e.CommitEnd()
		}, d.Err
	}},
	{"Commit reply", KindCommit, true, true, func(d *Dec) (any, func(*Enc), error) {
		seq, mark, err := ReadCommitReply(d)
		return []any{seq, mark}, func(e *Enc) { e.CommitReply(seq, mark) }, err
	}},
}

func codecNamed(t *testing.T, name string) codec {
	t.Helper()
	for _, c := range codecs {
		if c.name == name {
			return c
		}
	}
	t.Fatalf("no codec %q", name)
	return codec{}
}

func rep(c byte, n int) []byte { return bytes.Repeat([]byte{c}, n) }

func hash(b byte) [32]byte {
	var h [32]byte
	for i := range h {
		h[i] = b + byte(i)
	}
	return h
}

// encodeBody returns the body that write makes.
func encodeBody(t testing.TB, write func(e *Enc)) []byte {
	t.Helper()
	var buf bytes.Buffer
	e := &Enc{W: &buf}
	write(e)
	if e.Err != nil {
		t.Fatal(e.Err)
	}
	return buf.Bytes()
}

// send writes the body as a message of kind, through a Writer of the limit, and returns a Dec of what a Reader gives.
func send(t testing.TB, limit int, c codec, body []byte) *Dec {
	t.Helper()
	wire := message(t, limit, cond(c.reply, KindOK, c.kind), body)
	r := NewReader(bytes.NewReader(wire), limit)
	if c.reply {
		if err := r.Reply(); err != nil {
			t.Fatal(err)
		}
	} else if kind, err := r.Next(); err != nil || kind != c.kind {
		t.Fatalf("Next = %v, %v", kind, err)
	}
	return &Dec{R: r}
}

func cond[T any](c bool, a, b T) T {
	if c {
		return a
	}
	return b
}

var (
	t1969 = time.Date(1969, 12, 31, 23, 59, 59, 999999999, time.UTC)
	t2026 = time.Date(2026, 11, 15, 10, 20, 30, 123456789, time.UTC)
)

func TestMessages(t *testing.T) {
	key4096 := rep('k', MaxKey)
	val200 := testBytes(200)
	content := testBytes(3000)
	events := []Event{
		{Number: 1, Seq: 1, Data: []byte{}},
		{Number: 2, Seq: 1, Data: AppendMark(nil, t2026)},
		{Number: math.MaxUint64, Seq: math.MaxUint64, Data: testBytes(300)},
	}
	ops := []Op{
		{Kind: OpPut, Table: 1, Key: []byte{}, Value: []byte{}},
		{Kind: OpPut, Table: math.MaxUint32, Key: key4096, Value: val200},
		{Kind: OpDelete, Table: 3, Key: key4096},
		{Kind: OpDelete, Table: 4, Key: []byte{}},
		{Kind: OpEvent, Value: testBytes(300)},
		{Kind: OpEvent, Value: []byte{}},
	}
	position := BackupPosition{Generation: 3, Segment: 2, End: 1 << 40, Seq: 77, CRC: 0xDEADBEEF}
	backup := []any{
		BackupEntry{Kind: EntryPosition, Position: position},
		BackupEntry{Kind: EntryHead, Line: "audit head login 12 sha256:" + strings.Repeat("a", 64)},
		BackupEntry{Kind: EntryDir, Path: "files"},
		BackupEntry{Kind: EntryDir, Path: "files/ab"},
		BackupEntry{Kind: EntryFile, Path: "files/ab/" + strings.Repeat("c", 64)},
		content,
		BackupEntry{Kind: EntryFile, Path: "logdb/empty"},
		[]byte{},
	}

	for _, c := range []struct {
		codec string
		write func(e *Enc)
		want  any
	}{
		{"Hello request", func(e *Enc) { e.HelloRequest() }, Version},
		{"Hello reply", func(e *Enc) {
			e.HelloReply(HelloReply{Version: Version, Role: RoleWriter, BootID: [16]byte{1, 15: 16}, Seq: 1 << 40, Stopped: true,
				Frame: 1 << 20, Commit: 268435446, Object: 64 << 20, Staged: 268435446, MaxKey: MaxKey, DataDir: DirID{Dev: 66306, Ino: 2}})
		}, HelloReply{Version: Version, Role: RoleWriter, BootID: [16]byte{1, 15: 16}, Seq: 1 << 40, Stopped: true,
			Frame: 1 << 20, Commit: 268435446, Object: 64 << 20, Staged: 268435446, MaxKey: MaxKey, DataDir: DirID{Dev: 66306, Ino: 2}}},
		{"Hello reply", func(e *Enc) {
			e.HelloReply(HelloReply{Version: Version, Role: RoleReader, Frame: 1})
		}, HelloReply{Version: Version, Role: RoleReader, Frame: 1}},
		{"Hello reply", func(e *Enc) {
			e.HelloReply(HelloReply{Version: Version, Role: RoleReader, Frame: MaxFrame, Commit: math.MaxInt64, MaxKey: math.MaxInt32,
				DataDir: DirID{Dev: math.MaxUint64, Ino: math.MaxUint64}})
		}, HelloReply{Version: Version, Role: RoleReader, Frame: MaxFrame, Commit: math.MaxInt64, MaxKey: math.MaxInt32,
			DataDir: DirID{Dev: math.MaxUint64, Ino: math.MaxUint64}}},
		{"Snapshot request", func(*Enc) {}, nil},
		{"Snapshot reply", func(e *Enc) { e.SnapshotReply(0) }, uint64(0)},
		{"Snapshot reply", func(e *Enc) { e.SnapshotReply(math.MaxUint64) }, uint64(math.MaxUint64)},
		{"Release request", func(*Enc) {}, nil},
		{"Release reply", func(*Enc) {}, nil},
		{"Get request", func(e *Enc) { e.GetRequest(1, nil) }, []any{uint32(1), []byte{}}},
		{"Get request", func(e *Enc) { e.GetRequest(math.MaxUint32, key4096) }, []any{uint32(math.MaxUint32), key4096}},
		{"Get reply", func(e *Enc) { e.GetReply(false, nil) }, []any{false, []byte(nil)}},
		{"Get reply", func(e *Enc) { e.GetReply(true, nil) }, []any{true, []byte{}}},
		{"Get reply", func(e *Enc) { e.GetReply(true, val200) }, []any{true, val200}},
		{"Scan request", func(e *Enc) { e.ScanRequest(ScanRequest{Table: 2}) }, ScanRequest{Table: 2, Prefix: []byte{}, After: []byte{}}},
		{"Scan request", func(e *Enc) { e.ScanRequest(ScanRequest{Table: 2, Prefix: []byte("p"), HasAfter: true, Limit: 10}) },
			ScanRequest{Table: 2, Prefix: []byte("p"), HasAfter: true, After: []byte{}, Limit: 10}},
		{"Scan request", func(e *Enc) {
			e.ScanRequest(ScanRequest{Table: math.MaxUint32, Prefix: key4096, HasAfter: true, After: key4096, Limit: math.MaxUint32})
		}, ScanRequest{Table: math.MaxUint32, Prefix: key4096, HasAfter: true, After: key4096, Limit: math.MaxUint32}},
		{"Scan request", func(e *Enc) { e.ScanRequest(ScanRequest{Table: 2, After: []byte("ignored")}) },
			ScanRequest{Table: 2, Prefix: []byte{}, After: []byte{}}},
		{"Scan reply", func(e *Enc) { e.ScanEnd(false) }, []any{[][2][]byte(nil), false}},
		{"Scan reply", func(e *Enc) { e.ScanEnd(true) }, []any{[][2][]byte(nil), true}},
		{"Scan reply", func(e *Enc) {
			e.ScanRow(nil, []byte("v"))
			e.ScanRow(key4096, nil)
			e.ScanRow([]byte("k"), val200)
			e.ScanEnd(true)
		}, []any{[][2][]byte{{[]byte{}, []byte("v")}, {key4096, []byte{}}, {[]byte("k"), val200}}, true}},
		{"Object request", func(e *Enc) { e.ObjectRequest(hash(1)) }, hash(1)},
		{"Object reply", func(e *Enc) { e.ObjectReply(false, nil) }, []any{false, []byte(nil)}},
		{"Object reply", func(e *Enc) { e.ObjectReply(true, nil) }, []any{true, []byte{}}},
		{"Object reply", func(e *Enc) { e.ObjectReply(true, content) }, []any{true, content}},
		{"Original request", func(e *Enc) { e.OriginalRequest(hash(2)) }, hash(2)},
		{"Original reply", func(e *Enc) { e.OriginalReply(0) }, []any{int64(0), []byte{}}},
		{"Original reply", func(e *Enc) { e.OriginalReply(int64(len(content))); e.Raw(content) }, []any{int64(len(content)), content}},
		{"Events request", func(*Enc) {}, nil},
		{"Events reply", func(e *Enc) { e.EventsEnd() }, []Event(nil)},
		{"Events reply", func(e *Enc) {
			for _, ev := range events {
				e.Event(ev)
			}
			e.EventsEnd()
		}, events},
		{"Verify request", func(*Enc) {}, nil},
		{"Verify reply", func(*Enc) {}, nil},
		{"Backup request", func(*Enc) {}, nil},
		{"Backup reply", func(e *Enc) { e.BackupEntry(BackupEntry{Kind: EntryEnd}) }, []any(nil)},
		{"Backup reply", func(e *Enc) {
			for _, x := range backup {
				switch x := x.(type) {
				case BackupEntry:
					e.BackupEntry(x)
				case []byte:
					c := e.Chunks()
					for p := x; len(p) > 0; p = p[min(1000, len(p)):] { // three chunks
						_, _ = c.Write(p[:min(1000, len(p))])
					}
					_ = c.Close()
				}
			}
			e.BackupEntry(BackupEntry{Kind: EntryEnd})
		}, backup},
		{"PutObject request", func(e *Enc) { e.PutObjectRequest(hash(3), nil) }, []any{hash(3), []byte{}}},
		{"PutObject request", func(e *Enc) { e.PutObjectRequest(hash(4), content) }, []any{hash(4), content}},
		{"PutObject reply", func(*Enc) {}, nil},
		{"PutOriginal request", func(e *Enc) {
			c := e.Chunks()
			_ = c.Close()
			e.PutOriginalTrailer(hash(5), 0)
		}, []any{[]byte{}, hash(5), int64(0)}},
		{"PutOriginal request", func(e *Enc) {
			c := e.Chunks()
			_, _ = c.Write(content[:1])
			_, _ = c.Write(nil)
			_, _ = c.Write(content[1:])
			_ = c.Close()
			e.PutOriginalTrailer(hash(6), int64(len(content)))
		}, []any{content, hash(6), int64(len(content))}},
		{"PutOriginal reply", func(*Enc) {}, nil},
		{"Commit request", func(e *Enc) { e.CommitRequest(0); e.CommitEnd() }, []any{uint64(0), []Op(nil)}},
		{"Commit request", func(e *Enc) {
			e.CommitRequest(math.MaxUint64)
			for _, o := range ops {
				e.CommitOp(o)
			}
			e.CommitEnd()
		}, []any{uint64(math.MaxUint64), ops}},
		{"Commit reply", func(e *Enc) { e.CommitReply(5, t1969) }, []any{uint64(5), t1969}},
		{"Commit reply", func(e *Enc) { e.CommitReply(math.MaxUint64, t2026.In(time.FixedZone("x", 9*3600))) }, []any{uint64(math.MaxUint64), t2026}},
		{"Commit reply", func(e *Enc) { e.CommitReply(0, time.Time{}) }, []any{uint64(0), time.Time{}}},
	} {
		cd := codecNamed(t, c.codec)
		t.Run(c.codec, func(t *testing.T) {
			body := encodeBody(t, c.write)
			for _, limit := range []int{7, 64, 1 << 20} {
				v, enc, err := cd.dec(send(t, limit, cd, body))
				if err != nil {
					t.Fatalf("limit %d: %v", limit, err)
				}
				if !reflect.DeepEqual(v, c.want) {
					t.Fatalf("limit %d: got %#v, want %#v", limit, v, c.want)
				}
				if cd.exact {
					if again := encodeBody(t, enc); !bytes.Equal(again, body) {
						t.Fatalf("limit %d: written again as %x, was %x", limit, again, body)
					}
				}
			}
			for i := range body {
				if _, _, err := cd.dec(&Dec{R: bytes.NewReader(body[:i])}); err == nil {
					t.Fatalf("the first %d bytes of %d are read without an error", i, len(body))
				}
			}
			if _, _, err := cd.dec(&Dec{R: bytes.NewReader(append(slices.Clone(body), 0))}); !errors.Is(err, ErrProtocol) {
				t.Fatalf("a body with a byte too many: %v", err)
			}
		})
	}
}

func TestMessagesCoverEveryKind(t *testing.T) {
	seen := map[Kind][2]bool{}
	for _, c := range codecs {
		s := seen[c.kind]
		s[cond(c.reply, 1, 0)] = true
		seen[c.kind] = s
	}
	for k := KindHello; k <= KindCommit; k++ {
		if s := seen[k]; !s[0] || !s[1] {
			t.Errorf("%v: request and reply %v", k, s)
		}
	}
}

func TestOpSize(t *testing.T) {
	for _, o := range []Op{
		{Kind: OpPut, Table: 1, Key: []byte("key"), Value: []byte("value")},
		{Kind: OpPut},
		{Kind: OpDelete, Table: 1, Key: rep('k', 100)},
		{Kind: OpEvent, Value: rep('e', 77)},
	} {
		if got, want := o.Size(), int64(len(encodeBody(t, func(e *Enc) { e.CommitOp(o) }))); got != want {
			t.Errorf("Size of %v = %d, the op is %d bytes", o.Kind, got, want)
		}
	}
	if (Op{Kind: 9}).Size() != 0 {
		t.Error("an op of an unknown kind has a size")
	}
	e := &Enc{W: io.Discard}
	e.CommitOp(Op{Kind: 0})
	if !errors.Is(e.Err, ErrProtocol) {
		t.Errorf("an op of the kind 0: %v", e.Err)
	}
}

func TestMessageRefusals(t *testing.T) {
	read := func(in []byte) *Dec { return &Dec{R: bytes.NewReader(in)} }

	t.Run("Hello", func(t *testing.T) {
		_, err := ReadHelloRequest(read([]byte(secret)))
		if !errors.Is(err, ErrProtocol) || strings.Contains(err.Error(), "SECR") {
			t.Fatalf("a Hello without the magic: %v", err)
		}
		// A Hello of another version may have a longer body: its version comes back, and the caller refuses it.
		body := append(encodeBody(t, func(e *Enc) { e.Raw(Magic[:]); e.U32(Version + 1) }), "more of a later version"...)
		if v, err := ReadHelloRequest(read(body)); err != nil || v != Version+1 {
			t.Fatalf("a Hello of version %d: %d, %v", Version+1, v, err)
		}
		if _, err := ReadHelloRequest(read(body[:5])); !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("a short Hello: %v", err)
		}

		good := HelloReply{Version: Version, Role: RoleReader, Frame: 100, MaxKey: 10}
		for _, c := range []struct {
			name string
			h    HelloReply
			want string
		}{
			{"another version", HelloReply{Version: 1, Role: RoleReader, Frame: 100}, "version 1"},
			{"role 0", HelloReply{Version: Version, Frame: 100}, "role 0"},
			{"role 3", HelloReply{Version: Version, Role: 3, Frame: 100}, "role 3"},
			{"frame 0", HelloReply{Version: Version, Role: RoleReader}, "limit 0"},
			{"frame over MaxFrame", HelloReply{Version: Version, Role: RoleReader, Frame: MaxFrame + 1}, "limit 1073741825"},
			{"key limit over int32", HelloReply{Version: Version, Role: RoleReader, Frame: 1, MaxKey: math.MaxInt32 + 1}, "key limit"},
		} {
			_, err := ReadHelloReply(read(encodeBody(t, func(e *Enc) { e.HelloReply(c.h) })))
			if !errors.Is(err, ErrProtocol) || !strings.Contains(err.Error(), c.want) {
				t.Errorf("%s: %v, want an error with %q", c.name, err, c.want)
			}
		}
		body = encodeBody(t, func(e *Enc) { e.HelloReply(good) })
		body[34] = 0x80 // the commit limit, 2^63
		if h, err := ReadHelloReply(read(body)); err == nil {
			t.Errorf("a commit limit over 2^63: %+v", h)
		}
		// Another version is refused before the rest of the body is read.
		if _, err := ReadHelloReply(read([]byte{0, 0, 0, 9})); !errors.Is(err, ErrProtocol) {
			t.Errorf("the version alone: %v", err)
		}
		for _, h := range []HelloReply{{Frame: -1}, {Frame: math.MaxUint32 + 1}, {MaxKey: -1}} {
			e := &Enc{W: io.Discard}
			if e.HelloReply(h); e.Err == nil {
				t.Errorf("HelloReply of %+v is written", h)
			}
		}
	})

	t.Run("lists", func(t *testing.T) {
		if _, _, ok := ReadScanRow(read([]byte{2}), 10, 10); ok {
			t.Error("a list tag of 2 is a row")
		}
		d := read([]byte{2})
		ReadScanRow(d, 10, 10)
		if !errors.Is(d.Err, ErrProtocol) {
			t.Errorf("a list tag of 2: %v", d.Err)
		}
		d = read([]byte{0})
		if _, _, ok := ReadScanRow(d, 10, 10); ok || d.Err != nil {
			t.Errorf("the end of the rows: %v", d.Err)
		}
		d = read(nil)
		if _, _, ok := ReadScanRow(d, 10, 10); ok || !errors.Is(d.Err, io.ErrUnexpectedEOF) {
			t.Errorf("no end of the rows: %v", d.Err)
		}
		if ev, ok := ReadEvent(read([]byte{3}), 10); ok || ev.Data != nil {
			t.Error("a list tag of 3 is an event")
		}
		if _, ok := ReadCommitOp(read([]byte{4}), 10, 10); ok {
			t.Error("an op of the kind 4")
		}
		d = read([]byte{4})
		ReadCommitOp(d, 10, 10)
		if !errors.Is(d.Err, ErrProtocol) {
			t.Errorf("an op of the kind 4: %v", d.Err)
		}
		d = read([]byte{0, 1})
		if _, ok := ReadCommitOp(d, 10, 10); ok || !errors.Is(d.Err, ErrProtocol) {
			t.Errorf("a byte after the end of the ops: %v", d.Err)
		}
		d = read([]byte{0, 1})
		if _, ok := ReadEvent(d, 10); ok || !errors.Is(d.Err, ErrProtocol) {
			t.Errorf("a byte after the end of the events: %v", d.Err)
		}
	})

	t.Run("limits", func(t *testing.T) {
		long := encodeBody(t, func(e *Enc) { e.GetRequest(1, rep('k', MaxKey+1)) })
		if _, _, err := ReadGetRequest(read(long), MaxKey); !errors.Is(err, ErrLimit) || !errors.Is(err, ErrProtocol) {
			t.Errorf("a key of 4097 bytes: %v", err)
		}
		long = encodeBody(t, func(e *Enc) { e.ScanRequest(ScanRequest{Table: 1, Prefix: rep('k', MaxKey+1)}) })
		if _, err := ReadScanRequest(read(long), MaxKey); !errors.Is(err, ErrLimit) {
			t.Errorf("a prefix of 4097 bytes: %v", err)
		}
		long = encodeBody(t, func(e *Enc) { e.ScanRequest(ScanRequest{Table: 1, HasAfter: true, After: rep('k', MaxKey+1)}) })
		if _, err := ReadScanRequest(read(long), MaxKey); !errors.Is(err, ErrLimit) {
			t.Errorf("a key after of 4097 bytes: %v", err)
		}
		long = encodeBody(t, func(e *Enc) { e.CommitOp(Op{Kind: OpPut, Table: 1, Key: []byte("k"), Value: rep('v', 11)}) })
		if _, ok := ReadCommitOp(&Dec{R: bytes.NewReader(long)}, 10, 10); ok {
			t.Error("a value over the limit")
		}
		d := read(long)
		ReadCommitOp(d, 10, 10)
		if !errors.Is(d.Err, ErrLimit) {
			t.Errorf("a value over the limit: %v", d.Err)
		}

		huge := func(f func(e *Enc)) *Dec { return read(encodeBody(t, f)) }
		n := allocated(func() {
			_, _, err := ReadObjectReply(huge(func(e *Enc) { e.Bool(true); e.U64(1 << 40) }), testMaxValue)
			if !errors.Is(err, ErrLimit) {
				t.Errorf("an object of 1 TiB: %v", err)
			}
			_, _, err = ReadPutObjectRequest(huge(func(e *Enc) { e.Hash(hash(1)); e.U64(testMaxValue + 1) }), testMaxValue)
			if !errors.Is(err, ErrLimit) {
				t.Errorf("an object over the limit: %v", err)
			}
			_, err = ReadOriginalReply(huge(func(e *Enc) { e.U64(testMaxValue + 1) }), testMaxValue)
			if !errors.Is(err, ErrLimit) {
				t.Errorf("an original over the limit: %v", err)
			}
		})
		if n > 1<<16 {
			t.Errorf("the refusals allocated %d bytes", n)
		}
		for name, f := range map[string]func(d *Dec) error{
			"an object over 2^63": func(d *Dec) error { _, _, err := ReadObjectReply(d, math.MaxInt64); return err },
			"an original":         func(d *Dec) error { _, err := ReadOriginalReply(d, math.MaxInt64); return err },
			"a trailer":           func(d *Dec) error { _, _, err := ReadPutOriginalTrailer(d); return err },
		} {
			body := []byte{1, 0x80, 0, 0, 0, 0, 0, 0, 0}
			if name != "an object over 2^63" {
				body = body[1:]
			}
			if name == "a trailer" {
				body = slices.Concat(make([]byte, 32), body)
			}
			if err := f(read(body)); !errors.Is(err, ErrProtocol) || errors.Is(err, ErrLimit) {
				t.Errorf("%s: %v, want ErrProtocol and not ErrLimit", name, err)
			}
		}
		e := &Enc{W: io.Discard}
		if e.OriginalReply(-1); e.Err == nil {
			t.Error("a size of -1 is written")
		}
	})

	t.Run("Scan after", func(t *testing.T) {
		body := encodeBody(t, func(e *Enc) {
			e.U32(1)
			e.Bytes(nil)
			e.Bool(false)
			e.Bytes([]byte("after"))
			e.U32(0)
		})
		if _, err := ReadScanRequest(read(body), MaxKey); !errors.Is(err, ErrProtocol) {
			t.Errorf("a key after without asking: %v", err)
		}
	})

	t.Run("Commit reply", func(t *testing.T) {
		for name, c := range map[string]struct {
			sec  int64
			nsec uint32
		}{
			"1e9 nanoseconds": {0, 1e9},
			"all nanoseconds": {0, math.MaxUint32},
		} {
			body := encodeBody(t, func(e *Enc) { e.U64(1); e.I64(c.sec); e.U32(c.nsec) })
			if _, _, err := ReadCommitReply(read(body)); !errors.Is(err, ErrProtocol) {
				t.Errorf("%s: %v", name, err)
			}
		}
	})
}

func TestBackupEntryRefusals(t *testing.T) {
	read := func(in []byte) *Dec { return &Dec{R: bytes.NewReader(in)} }
	rawEntry := func(kind EntryKind, s string) []byte {
		return slices.Concat([]byte{byte(kind), byte(len(s) >> 8), byte(len(s))}, []byte(s))
	}

	for _, p := range []string{"", ".", "..", "/", "/a", "a/", "a//b", "a/./b", "a/../b", "../a", "a\\b", "a:b", "a\x00b", "../" + secret} {
		for _, kind := range []EntryKind{EntryDir, EntryFile} {
			e := &Enc{W: io.Discard}
			if e.BackupEntry(BackupEntry{Kind: kind, Path: p}); !errors.Is(e.Err, ErrProtocol) || strings.Contains(e.Err.Error(), "SECRET") {
				t.Errorf("the path %q of kind %d is written: %v", p, kind, e.Err)
			}
			d := read(rawEntry(kind, p))
			if en, ok := ReadBackupEntry(d); ok || !errors.Is(d.Err, ErrProtocol) || strings.Contains(d.Err.Error(), "SECRET") {
				t.Errorf("the path %q of kind %d is read: %+v, %v", p, kind, en, d.Err)
			}
		}
	}
	for _, p := range []string{"a", "a/b", "logdb/log/0000000000000001-0000000000000001.log", "files/ab/" + strings.Repeat("0", 64), "a b", "..a", "a.."} {
		d := read(rawEntry(EntryFile, p))
		if en, ok := ReadBackupEntry(d); !ok || en.Path != p {
			t.Errorf("the path %q is refused: %v", p, d.Err)
		}
	}

	for _, line := range []string{"", strings.Repeat("a", MaxHeadLine+1), "a\nb", "a\x7fb", "a\x1fb", "café", secret + "\n"} {
		e := &Enc{W: io.Discard}
		if e.BackupEntry(BackupEntry{Kind: EntryHead, Line: line}); !errors.Is(e.Err, ErrProtocol) || strings.Contains(e.Err.Error(), "SECRET") {
			t.Errorf("the head line %q is written: %v", line, e.Err)
		}
		d := read(rawEntry(EntryHead, line))
		if _, ok := ReadBackupEntry(d); ok || d.Err == nil || strings.Contains(d.Err.Error(), "SECRET") {
			t.Errorf("the head line %q is read: %v", line, d.Err)
		}
	}
	d := read(rawEntry(EntryHead, strings.Repeat("a", MaxHeadLine)))
	if en, ok := ReadBackupEntry(d); !ok || len(en.Line) != MaxHeadLine {
		t.Errorf("a head line of 255 bytes: %v", d.Err)
	}

	e := &Enc{W: io.Discard}
	if e.BackupEntry(BackupEntry{Kind: EntryPosition, Position: BackupPosition{End: -1}}); e.Err == nil {
		t.Error("a negative end is written")
	}
	e = &Enc{W: io.Discard}
	if e.BackupEntry(BackupEntry{Kind: 5}); !errors.Is(e.Err, ErrProtocol) {
		t.Errorf("an entry of the kind 5: %v", e.Err)
	}
	d = read([]byte{5})
	if _, ok := ReadBackupEntry(d); ok || !errors.Is(d.Err, ErrProtocol) {
		t.Errorf("an entry of the kind 5: %v", d.Err)
	}
	d = read(slices.Concat([]byte{byte(EntryPosition)}, make([]byte, 16), []byte{0x80, 0, 0, 0, 0, 0, 0, 0}, make([]byte, 12)))
	if _, ok := ReadBackupEntry(d); ok || !errors.Is(d.Err, ErrProtocol) {
		t.Errorf("an end over 2^63: %v", d.Err)
	}
	d = read(rawEntry(EntryDir, strings.Repeat("a/", 2049)[:4097]))
	if _, ok := ReadBackupEntry(d); ok || !errors.Is(d.Err, ErrLimit) {
		t.Errorf("a path of 4097 bytes: %v", d.Err)
	}
}

func TestChunks(t *testing.T) {
	for _, n := range []int{0, 1, chunkMax - 1, chunkMax, chunkMax + 1, 3*chunkMax + 5} {
		data := testBytes(n)
		var buf bytes.Buffer
		e := &Enc{W: &buf}
		c := e.Chunks()
		if m, err := io.Copy(c, bytes.NewReader(data)); err != nil || m != int64(n) {
			t.Fatalf("%d bytes: wrote %d, %v", n, m, err)
		}
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}

		// Every chunk has 1 to chunkMax bytes, and a length of 0 ends them.
		raw := buf.Bytes()
		total := 0
		for {
			l := int(raw[0])<<24 | int(raw[1])<<16 | int(raw[2])<<8 | int(raw[3])
			raw = raw[4:]
			if l == 0 {
				break
			}
			if l > chunkMax {
				t.Fatalf("%d bytes: a chunk of %d bytes", n, l)
			}
			total += l
			raw = raw[l:]
		}
		if total != n || len(raw) != 0 {
			t.Fatalf("%d bytes: the chunks have %d, and %d bytes follow them", n, total, len(raw))
		}

		d := &Dec{R: bytes.NewReader(append(buf.Bytes(), "tail"...))}
		cr := NewChunkReader(d, int64(n))
		got, err := io.ReadAll(cr)
		if err != nil || !bytes.Equal(got, data) || cr.Size() != int64(n) {
			t.Fatalf("%d bytes: read %d, %v, size %d", n, len(got), err, cr.Size())
		}
		if _, err := cr.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
			t.Fatalf("Read after the end = %v", err)
		}
		tail := make([]byte, 4)
		d.Raw(tail)
		if string(tail) != "tail" {
			t.Fatalf("after the chunks: %q, %v", tail, d.Err)
		}
	}

	t.Run("limit", func(t *testing.T) {
		var buf bytes.Buffer
		e := &Enc{W: &buf}
		c := e.Chunks()
		_, _ = c.Write(testBytes(100))
		_, _ = c.Write(testBytes(100))
		_ = c.Close()
		d := &Dec{R: bytes.NewReader(buf.Bytes())}
		got, err := io.ReadAll(NewChunkReader(d, 150))
		if !errors.Is(err, ErrLimit) || !errors.Is(err, ErrProtocol) || len(got) != 100 {
			t.Fatalf("read %d bytes, %v", len(got), err)
		}
		d = &Dec{R: bytes.NewReader(buf.Bytes())}
		if _, err := io.ReadAll(NewChunkReader(d, 200)); err != nil {
			t.Fatalf("exactly the limit: %v", err)
		}
		d = &Dec{R: bytes.NewReader([]byte{0xFF, 0xFF, 0xFF, 0xFF})}
		n := allocated(func() { _, err = io.ReadAll(NewChunkReader(d, 1<<20)) })
		if !errors.Is(err, ErrLimit) || n > 1<<16 {
			t.Fatalf("a chunk of 4 GiB: %v, %d bytes allocated", err, n)
		}
	})
	t.Run("short", func(t *testing.T) {
		for _, in := range []string{"", "\x00\x00", "\x00\x00\x00\x05abc", "\x00\x00\x00\x03abc", "\x00\x00\x00\x03abc\x00\x00"} {
			d := &Dec{R: strings.NewReader(in)}
			if _, err := io.ReadAll(NewChunkReader(d, 100)); !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Errorf("%q: %v", in, err)
			}
		}
	})
	t.Run("write error", func(t *testing.T) {
		e := &Enc{W: &failWriter{n: 10}}
		c := e.Chunks()
		if _, err := c.Write(testBytes(100)); !errors.Is(err, errDisk) {
			t.Fatalf("Write = %v", err)
		}
		if err := c.Close(); !errors.Is(err, errDisk) {
			t.Fatalf("Close = %v", err)
		}
	})
	t.Run("an empty read", func(t *testing.T) {
		cr := NewChunkReader(&Dec{R: strings.NewReader("")}, 10)
		if n, err := cr.Read(nil); n != 0 || err != nil {
			t.Fatalf("Read of nothing = %d, %v", n, err)
		}
	})
}
