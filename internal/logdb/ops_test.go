package logdb

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
)

func hashOf(b byte) (h [32]byte) {
	for i := range h {
		h[i] = b + byte(i)
	}
	return h
}

// sampleOps has an op of every code, with empty, large and small fields.
func sampleOps() []op {
	return []op{
		{code: opPut, table: 1, key: "k", value: []byte("value")},
		{code: opPut, table: 300, key: "", value: nil},
		{code: opDelete, table: math.MaxUint32, key: strings.Repeat("d", MaxKey)},
		{code: opDelete, table: 1, key: "k"},
		{code: opInline, hash: hashOf(1), value: []byte("object")},
		{code: opInline, hash: hashOf(2), value: []byte{}},
		{code: opFile, hash: hashOf(3), size: 1 << 40},
		{code: opFile, hash: hashOf(4), size: 0},
		{code: opFile, hash: hashOf(5), size: math.MaxInt64},
		{code: opEvent, number: 1, seq: 7, value: []byte("event")},
		{code: opEvent, number: 1 << 50, seq: math.MaxUint64, value: nil},
	}
}

func randomOps(rng *rand.Rand, n int) []op {
	ops := make([]op, n)
	for i := range ops {
		value := make([]byte, rng.IntN(300))
		for j := range value {
			value[j] = byte(rng.Uint32())
		}
		key := string(value[:min(len(value), rng.IntN(20))])
		switch rng.IntN(5) {
		case 0:
			ops[i] = op{code: opPut, table: 1 + rng.Uint32N(5), key: key, value: value}
		case 1:
			ops[i] = op{code: opDelete, table: 1 + rng.Uint32N(5), key: key}
		case 2:
			ops[i] = op{code: opInline, hash: hashOf(byte(i)), value: value}
		case 3:
			ops[i] = op{code: opFile, hash: hashOf(byte(i)), size: rng.Int64()}
		default:
			ops[i] = op{code: opEvent, number: rng.Uint64(), seq: seqNum(rng.Uint64()), value: value}
		}
	}
	return ops
}

func opsEqual(a, b []op) bool {
	return slices.EqualFunc(a, b, func(x, y op) bool {
		return x.code == y.code && x.table == y.table && x.key == y.key && bytes.Equal(x.value, y.value) &&
			x.hash == y.hash && x.size == y.size && x.number == y.number && x.seq == y.seq && x.valueOff == y.valueOff
	})
}

func encodeRecord(r record) []byte {
	switch r.typ {
	case recCommit:
		return appendCommit(nil, r.seq, r.ops)
	case recBase:
		return appendBase(nil, r.seq, r.part, r.ops)
	case recBaseEnd:
		return appendBaseEnd(nil, r.seq, r.end)
	}
	panic(fmt.Sprintf("record type %d", r.typ))
}

func checkValues(t *testing.T, body []byte, ops []op) {
	t.Helper()
	for i, o := range ops {
		switch o.code {
		case opPut, opInline, opEvent:
			if got := body[o.valueOff : o.valueOff+len(o.value)]; !bytes.Equal(got, o.value) {
				t.Fatalf("op %d: value at %d is %q, want %q", i, o.valueOff, got, o.value)
			}
		case opDelete, opFile:
			if o.valueOff != 0 {
				t.Fatalf("op %d of code %d: valueOff %d", i, o.code, o.valueOff)
			}
		}
	}
}

func TestBodyLayout(t *testing.T) {
	ops := []op{
		{code: opPut, table: 3, key: "k", value: []byte("vw")},
		{code: opDelete, table: 128, key: ""},
		{code: opInline, hash: hashOf(0x10), value: []byte("x")},
		{code: opFile, hash: hashOf(0x20), size: 300},
		{code: opEvent, number: 1, seq: 7, value: nil},
	}
	h1, h2 := hashOf(0x10), hashOf(0x20)
	want := slices.Concat(
		[]byte{1, 7, 0, 0, 0, 0, 0, 0, 0, 5},
		[]byte{1, 3, 1, 'k', 0, 2, 'v', 'w'},
		[]byte{2, 0x80, 0x01, 0},
		[]byte{3}, h1[:], []byte{0, 1, 'x'},
		[]byte{4}, h2[:], []byte{0xac, 0x02},
		[]byte{5, 1, 7, 0, 0},
	)
	if got := appendCommit(nil, 7, ops); !bytes.Equal(got, want) {
		t.Fatalf("commit\n%x, want\n%x", got, want)
	}
	for i, off := range []int{16, 0, 57, 0, 98} {
		if ops[i].valueOff != off {
			t.Errorf("op %d: valueOff %d, want %d", i, ops[i].valueOff, off)
		}
	}
	if len(want) != 98 {
		t.Errorf("commit of %d bytes", len(want))
	}

	want = []byte{2, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x96, 0x01, 1, 1, 1, 1, 'k', 0, 0}
	if got := appendBase(nil, math.MaxUint64, 150, []op{{code: opPut, table: 1, key: "k"}}); !bytes.Equal(got, want) {
		t.Errorf("base\n%x, want\n%x", got, want)
	}
	want = []byte{3, 0x10, 0, 0, 0, 0, 0, 0, 0, 1, 2, 3, 0x80, 0x01, 0xe8, 0x07}
	if got := appendBaseEnd(nil, 16, baseEnd{parts: 1, keys: 2, objects: 3, events: 128, nextEvent: 1000}); !bytes.Equal(got, want) {
		t.Errorf("base-end\n%x, want\n%x", got, want)
	}
}

func TestOpsRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewPCG(63, 1))
	for _, c := range []struct {
		name string
		ops  []op
	}{
		{"no ops", nil},
		{"every code", sampleOps()},
		{"large value", []op{
			{code: opPut, table: 2, key: "big", value: bytes.Repeat([]byte("x"), 1<<20)},
			{code: opInline, hash: hashOf(9), value: bytes.Repeat([]byte("y"), 300_000)},
		}},
		{"many", randomOps(rng, 1000)},
	} {
		for _, typ := range []recType{recCommit, recBase} {
			ops := slices.Clone(c.ops)
			r := record{typ: typ, seq: 42, part: 0, ops: ops}
			if typ == recBase {
				r.part = 1 << 20
			}
			body := encodeRecord(r)
			checkValues(t, body, ops)
			got, reason := parseBody(body)
			if reason != "" || got.typ != typ || got.seq != 42 || got.part != r.part || !opsEqual(got.ops, ops) {
				t.Fatalf("%s, type %d: %q, %+v", c.name, typ, reason, got.end)
			}
			if again := encodeRecord(got); !bytes.Equal(again, body) {
				t.Fatalf("%s, type %d: encoded again, the body differs", c.name, typ)
			}

			// valueOff counts from the start of the body, also when it is appended to other bytes.
			pops := slices.Clone(c.ops)
			prefixed := appendCommit([]byte("prefix"), 42, pops)
			if typ == recBase {
				prefixed = appendBase([]byte("prefix"), 42, r.part, pops)
			}
			if !bytes.Equal(prefixed[6:], body) || !opsEqual(pops, ops) {
				t.Fatalf("%s, type %d: appended to a prefix, the body or valueOff differs", c.name, typ)
			}

			// An inline object is at the offset of its frame + 8 + valueOff.
			frame := appendFrame(make([]byte, 64), 5, body)
			fb, n, reason := parseFrame(frame[64:], 5)
			if reason != "" || n != len(frame)-64 {
				t.Fatalf("frame: %q", reason)
			}
			fr, _ := parseBody(fb)
			checkValues(t, frame[64+8:], fr.ops)

			payload := body[minBody:]
			if typ == recBase {
				payload = payload[3:]
			}
			if dops, reason := decodeOps(payload, len(body)-len(payload)); reason != "" || !opsEqual(dops, ops) {
				t.Fatalf("%s, type %d: decodeOps: %q", c.name, typ, reason)
			}
		}
	}

	for _, e := range []baseEnd{{}, {parts: 1, keys: 2, objects: 3, events: 4, nextEvent: 5},
		{math.MaxUint64, math.MaxUint64, math.MaxUint64, math.MaxUint64, math.MaxUint64}} {
		body := appendBaseEnd(nil, 99, e)
		if got, reason := parseBody(body); reason != "" || got.typ != recBaseEnd || got.seq != 99 || got.end != e ||
			got.ops != nil {
			t.Fatalf("base-end %+v: %+v, %q", e, got, reason)
		}
	}
}

func sampleBodies() [][]byte {
	return [][]byte{
		appendCommit(nil, 1, nil),
		appendCommit(nil, 300, sampleOps()),
		appendBase(nil, 300, 200, sampleOps()),
		appendBase(nil, 0, 0, nil),
		appendBaseEnd(nil, 300, baseEnd{parts: 200, keys: 1 << 30, objects: 3, events: 0, nextEvent: 1}),
	}
}

func TestBodyTruncated(t *testing.T) {
	for _, body := range sampleBodies() {
		r, reason := parseBody(body)
		if reason != "" {
			t.Fatal(reason)
		}
		opsAt := minBody
		switch r.typ {
		case recCommit:
		case recBase:
			opsAt += len(binary.AppendUvarint(nil, r.part))
		case recBaseEnd:
			opsAt = len(body)
		}
		for k := range len(body) {
			want := "truncated body"
			if k >= opsAt {
				want = "truncated op"
			}
			if got, reason := parseBody(body[:k]); reason != want || got.typ != 0 || got.ops != nil {
				t.Fatalf("type %d cut at %d of %d: %q, want %q", body[0], k, len(body), reason, want)
			}
		}
	}
}

func rawBody(typ recType, payload ...[]byte) []byte {
	return append(binary.LittleEndian.AppendUint64([]byte{byte(typ)}, 7), slices.Concat(payload...)...)
}

func TestBodyErrors(t *testing.T) {
	uv := func(v uint64) []byte { return binary.AppendUvarint(nil, v) }
	b := func(bs ...byte) []byte { return bs }
	h := hashOf(0)
	sealed := slices.Concat(b(1, 7), make([]byte, 12), b(16), make([]byte, 16))
	for _, c := range []struct {
		name   string
		body   []byte
		reason string
	}{
		{"empty", nil, "truncated body"},
		{"type 0", rawBody(0, b(0)), "unknown record type 0"},
		{"type 4", rawBody(4), "unknown record type 4"},
		{"type 255", rawBody(255, b(0)), "unknown record type 255"},
		{"commit without ops", rawBody(recCommit), "truncated op"},
		{"base without part", rawBody(recBase), "truncated body"},
		{"base-end with 4 counts", rawBody(recBaseEnd, b(1, 2, 3, 4)), "truncated body"},
		{"trailing bytes after the ops", rawBody(recCommit, b(0, 0)), "trailing bytes"},
		{"trailing bytes after a base", rawBody(recBase, b(0, 0, 1)), "trailing bytes"},
		{"trailing bytes after a base-end", rawBody(recBaseEnd, b(1, 2, 3, 4, 5, 6)), "trailing bytes"},
		{"trailing bytes after an op", rawBody(recCommit, b(1, byte(opDelete), 1, 0, 0)), "trailing bytes"},
		{"unknown op 0", rawBody(recCommit, b(1, 0)), "unknown op 0"},
		{"unknown op 6", rawBody(recCommit, b(1, 6, 0, 0)), "unknown op 6"},
		{"unknown op 255", rawBody(recCommit, b(2, byte(opDelete), 1, 0, 255)), "unknown op 255"},
		{"table 0 of a put", rawBody(recCommit, b(1, byte(opPut), 0, 0, 0, 0)), "invalid table 0"},
		{"table 0 of a delete", rawBody(recCommit, b(1, byte(opDelete), 0, 0)), "invalid table 0"},
		{"table 2^32", rawBody(recCommit, b(1, byte(opDelete)), uv(1<<32), b(0)), "invalid table 4294967296"},
		{"largest table", rawBody(recCommit, b(1, byte(opDelete)), uv(math.MaxUint32), b(0)), ""},
		{"key too long", rawBody(recCommit, b(1, byte(opDelete), 1), uv(MaxKey+1), make([]byte, MaxKey+1)),
			"key too long: 4097 bytes (max 4096)"},
		{"longest key", rawBody(recCommit, b(1, byte(opDelete), 1), uv(MaxKey), make([]byte, MaxKey)), ""},
		{"huge key length", rawBody(recCommit, b(1, byte(opDelete), 1), uv(math.MaxUint64)),
			"key too long: 18446744073709551615 bytes (max 4096)"},
		{"sealed put", rawBody(recCommit, b(1, byte(opPut), 1, 1, 'k'), sealed), "sealed values are not supported by this version"},
		{"sealed object", rawBody(recCommit, b(1, byte(opInline)), h[:], sealed), "sealed values are not supported by this version"},
		{"sealed event", rawBody(recCommit, b(1, byte(opEvent), 1, 7), sealed), "sealed values are not supported by this version"},
		{"envelope 7", rawBody(recCommit, b(1, byte(opPut), 1, 1, 'k', 7, 0)), "unknown value envelope 7"},
		{"envelope 255", rawBody(recCommit, b(1, byte(opInline)), h[:], b(255, 0)), "unknown value envelope 255"},
		{"object size 2^63", rawBody(recCommit, b(1, byte(opFile)), h[:], uv(1<<63)), "bad object size 9223372036854775808"},
		{"value past the end", rawBody(recCommit, b(1, byte(opPut), 1, 1, 'k', 0, 2, 'v')), "truncated op"},
		{"value length beyond int", rawBody(recCommit, b(1, byte(opPut), 1, 1, 'k', 0), uv(math.MaxUint64)), "truncated op"},
		{"hash past the end", rawBody(recCommit, b(1, byte(opFile)), h[:31]), "truncated op"},
		{"second op missing", rawBody(recCommit, b(2, byte(opDelete), 1, 0)), "truncated op"},
		{"huge count", rawBody(recCommit, uv(1<<62), b(byte(opDelete), 1, 0)), "truncated op"},
		{"count longer than needed", rawBody(recCommit, b(0x80, 0)), "bad uvarint"},
		{"table longer than needed", rawBody(recCommit, b(1, byte(opDelete), 0x81, 0, 0)), "bad uvarint"},
		{"part longer than needed", rawBody(recBase, b(0x80, 0, 0)), "bad uvarint"},
		{"count over 64 bits", rawBody(recCommit, bytes.Repeat(b(0xff), 9), b(2)), "bad uvarint"},
		{"count of 11 bytes", rawBody(recCommit, bytes.Repeat(b(0xff), 10), b(1)), "bad uvarint"},
		{"base-end count over 64 bits", rawBody(recBaseEnd, b(1, 2, 3, 4), bytes.Repeat(b(0x80), 10), b(1)),
			"bad uvarint"},
	} {
		got, reason := parseBody(c.body)
		if reason != c.reason {
			t.Errorf("%s: %q, want %q", c.name, reason, c.reason)
		} else if reason != "" && (got.typ != 0 || got.ops != nil) {
			t.Errorf("%s: returned %+v with the reason", c.name, got)
		}
	}
}

func TestEncodePanics(t *testing.T) {
	for _, c := range []struct {
		op   op
		want string
	}{
		{op{code: opPut, table: 0, key: "k"}, "logdb: op 1 with table 0 and a key of 1 bytes"},
		{op{code: opDelete, table: 1, key: strings.Repeat("k", MaxKey+1)}, "logdb: op 2 with table 1 and a key of 4097 bytes"},
		{op{code: opFile, size: -1}, "logdb: file object of -1 bytes"},
		{op{code: 0}, "logdb: unknown op 0"},
		{op{code: 6}, "logdb: unknown op 6"},
	} {
		func() {
			defer func() {
				if r := recover(); r != c.want {
					t.Errorf("%+v: %v, want %q", c.op, r, c.want)
				}
			}()
			appendCommit(nil, 1, []op{{code: opPut, table: 1}, c.op})
		}()
	}
}

// FuzzBody checks that parseBody never panics and accepts only bodies that the encoders make.
func FuzzBody(f *testing.F) {
	for _, b := range sampleBodies() {
		f.Add(b)
	}
	f.Add(rawBody(recCommit, []byte{0x80, 0}))
	f.Fuzz(func(t *testing.T, b []byte) {
		r, reason := parseBody(b)
		if reason != "" {
			return
		}
		checkValues(t, b, r.ops)
		if again := encodeRecord(r); !bytes.Equal(again, b) {
			t.Fatalf("encoded again\n%x, was\n%x", again, b)
		}
	})
}
