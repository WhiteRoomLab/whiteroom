package logdb

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"math"
	"slices"
	"strings"
	"testing"
)

const (
	segPath  = "/data/logdb/log/0000000000000001-0000000000000002.log"
	headPath = "/data/logdb/head"
	objPath  = "/data/logdb/objects/ab/ab00000000000000000000000000000000000000000000000000000000000000"
	ckpPath  = "/data/logdb/checkpoint"
)

// crc32c is the CRC of concept section 3.5, computed without the package's table.
func crc32c(parts ...[]byte) uint32 {
	return crc32.Checksum(slices.Concat(parts...), crc32.MakeTable(crc32.Castagnoli))
}

// eachChange calls fn with b changed in one byte, for every byte and every other value of it. c is reused.
func eachChange(b []byte, fn func(i int, c []byte)) {
	c := slices.Clone(b)
	for i := range c {
		for v := range 256 {
			if byte(v) == b[i] {
				continue
			}
			c[i] = byte(v)
			fn(i, c)
		}
		c[i] = b[i]
	}
}

func checkCorrupt(t *testing.T, err error, path string, off int64, reason string) {
	t.Helper()
	var ce *CorruptError
	if !errors.As(err, &ce) || ce.Path != path || ce.Offset != off || ce.Reason != reason {
		t.Fatalf("error %v, want %s at offset %d: %s", err, path, off, reason)
	}
}

func checkVersion(t *testing.T, err error, path string, version uint32) {
	t.Helper()
	var ve *VersionError
	if !errors.As(err, &ve) || *ve != (VersionError{Path: path, Version: version, Known: 1}) {
		t.Fatalf("error %v, want version %d of %s", err, version, path)
	}
}

// checkStartChange checks the error of a header changed in byte i, which is in its magic or version, or after them
// and so is found by the CRC.
func checkStartChange(t *testing.T, err error, path string, c []byte, i int, what, crcReason string) {
	t.Helper()
	switch {
	case i < 8:
		checkCorrupt(t, err, path, -1, "not "+what+" of whiteroom's log")
	case i < 12:
		if v := binary.LittleEndian.Uint32(c[8:]); v == 0 {
			checkCorrupt(t, err, path, -1, "bad format version 0")
		} else {
			checkVersion(t, err, path, v)
		}
	default:
		checkCorrupt(t, err, path, -1, crcReason)
	}
}

func TestErrorTexts(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{&CorruptError{Path: segPath, Offset: 4096, Reason: "bad record CRC"}, segPath + ": corrupt at offset 4096: bad record CRC"},
		{&CorruptError{Path: segPath, Offset: 0, Reason: "x"}, segPath + ": corrupt at offset 0: x"},
		{&CorruptError{Path: segPath, Offset: -1, Reason: "bad header CRC"}, segPath + ": corrupt: bad header CRC"},
		{&VersionError{Path: segPath, Version: 2, Known: 1}, segPath + ": format version 2, newer than this program knows (1)"},
		{ErrFailed, "the store has stopped after a sync failed; restart the machine before opening it again"},
		{ErrReadOnly, "read-only transaction"},
		{errEnded, "the transaction has ended"},
		{errClosed, "the store is closed"},
		{tooLarge(268435457), "the transaction is too large: 268435457 bytes (max 268435456)"},
	} {
		if got := c.err.Error(); got != c.want {
			t.Errorf("%q, want %q", got, c.want)
		}
	}
	defer func(n int) { maxBody = n }(maxBody)
	maxBody = 100
	if got, want := tooLarge(101).Error(), "the transaction is too large: 101 bytes (max 100)"; got != want {
		t.Errorf("%q, want %q", got, want)
	}
}

// TestValues pins the constants of the format to the numbers of concept section 3.5.
func TestValues(t *testing.T) {
	if got := crc32.Checksum([]byte("123456789"), castagnoli); got != 0xe3069283 {
		t.Errorf("check value %#x, want the one of CRC-32C", got)
	}
	for _, c := range []struct {
		name      string
		got, want int
	}{
		{"formatVersion", formatVersion, 1},
		{"segHeaderSize", segHeaderSize, 64},
		{"headSlotSize", headSlotSize, 64},
		{"headSize", headSize, 128},
		{"objHeaderSize", objHeaderSize, 16},
		{"ckpHeaderSize", ckpHeaderSize, 64},
		{"maxBody", maxBody, 1 << 28},
		{"MaxKey", MaxKey, 4096},
		{"minBody", minBody, 9},
		{"recCommit", int(recCommit), 1},
		{"recBase", int(recBase), 2},
		{"recBaseEnd", int(recBaseEnd), 3},
		{"opPut", int(opPut), 1},
		{"opDelete", int(opDelete), 2},
		{"opInline", int(opInline), 3},
		{"opFile", int(opFile), 4},
		{"opEvent", int(opEvent), 5},
		{"envPlain", int(envPlain), 0},
		{"envSealed", int(envSealed), 1},
		{"placeInline", int(placeInline), 1},
		{"placeFile", int(placeFile), 2},
	} {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}
	for got, want := range map[string]string{segMagic: "whrm-log", ckpMagic: "whrm-ckp", headMagic: "whrm-hed", objMagic: "whrm-obj"} {
		if got != want {
			t.Errorf("magic %q, want %q", got, want)
		}
	}
}

func TestSegHeader(t *testing.T) {
	h := segHeader{gen: 0x0102, index: 0x0304, firstSeq: 0x0506, salt: 0x0708090a0b0c0d0e}
	b := appendSegHeader([]byte("prefix"), h)[6:]

	want := make([]byte, 64)
	copy(want, "whrm-log")
	binary.LittleEndian.PutUint32(want[8:], 1)
	binary.LittleEndian.PutUint32(want[12:], 64)
	binary.LittleEndian.PutUint64(want[16:], 0x0102)
	binary.LittleEndian.PutUint64(want[24:], 0x0304)
	binary.LittleEndian.PutUint64(want[32:], 0x0506)
	binary.LittleEndian.PutUint64(want[40:], 0x0708090a0b0c0d0e)
	binary.LittleEndian.PutUint32(want[60:], crc32c(want[:60]))
	if !bytes.Equal(b, want) {
		t.Fatalf("header\n%x, want\n%x", b, want)
	}

	if got, err := parseSegHeader(segPath, b, 0x0102, 0x0304); err != nil || got != h {
		t.Fatalf("%+v, %v", got, err)
	}
	if got, err := parseSegHeader(segPath, append(slices.Clone(b), 1, 2, 3), 0x0102, 0x0304); err != nil || got != h {
		t.Fatalf("with records after it: %+v, %v", got, err)
	}

	eachChange(b, func(i int, c []byte) {
		_, err := parseSegHeader(segPath, c, 0x0102, 0x0304)
		checkStartChange(t, err, segPath, c, i, "a segment", "bad header CRC")
	})
	for n := range len(b) {
		_, err := parseSegHeader(segPath, b[:n], 0x0102, 0x0304)
		checkCorrupt(t, err, segPath, -1, "short file")
	}

	_, err := parseSegHeader(segPath, b, 0x0102, 0x0305)
	checkCorrupt(t, err, segPath, -1, "the header names generation 258, index 772")
	_, err = parseSegHeader(segPath, b, 1, 0x0304)
	checkCorrupt(t, err, segPath, -1, "the header names generation 258, index 772")

	// Fields that a valid CRC covers but that version 1 fixes.
	resign := func(c []byte) []byte {
		binary.LittleEndian.PutUint32(c[60:], crc32c(c[:60]))
		return c
	}
	c := slices.Clone(b)
	binary.LittleEndian.PutUint32(c[12:], 63)
	_, err = parseSegHeader(segPath, resign(c), 0x0102, 0x0304)
	checkCorrupt(t, err, segPath, -1, "bad header size 63")
	for i := 48; i < 60; i++ {
		c := slices.Clone(b)
		c[i] = 1
		_, err = parseSegHeader(segPath, resign(c), 0x0102, 0x0304)
		checkCorrupt(t, err, segPath, -1, "nonzero reserved bytes")
	}

	// A newer version is refused before the CRC, which it may have moved.
	c = slices.Clone(b)
	binary.LittleEndian.PutUint32(c[8:], 2)
	if _, err := parseSegHeader(segPath, c, 0x0102, 0x0304); err == nil ||
		err.Error() != segPath+": format version 2, newer than this program knows (1)" {
		t.Errorf("newer version: %v", err)
	}
	c = append([]byte("whrm-ckp"), b[8:]...)
	_, err = parseSegHeader(segPath, c, 0x0102, 0x0304)
	if err == nil || err.Error() != segPath+": corrupt: not a segment of whiteroom's log" {
		t.Errorf("magic: %v", err)
	}
}

func testBody(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*7 + 1)
	}
	return b
}

func TestFrame(t *testing.T) {
	const salt = 0x1122334455667788
	for l := minBody; l <= 64; l++ {
		body := testBody(l)
		f := appendFrame([]byte("prefix"), salt, body)[6:]
		n := frameLen(l)
		if len(f) != n || n%8 != 0 || n < 8+l || n >= 16+l {
			t.Fatalf("body of %d bytes: frame of %d bytes, frameLen %d", l, len(f), n)
		}
		var lb [4]byte
		binary.LittleEndian.PutUint32(lb[:], uint32(l))
		var sb [8]byte
		binary.LittleEndian.PutUint64(sb[:], salt)
		want := binary.LittleEndian.AppendUint32(lb[:], crc32c(sb[:], lb[:], body))
		want = append(append(want, body...), make([]byte, n-8-l)...)
		if !bytes.Equal(f, want) {
			t.Fatalf("frame\n%x, want\n%x", f, want)
		}

		got, gotN, reason := parseFrame(f, salt)
		if reason != "" || gotN != n || !bytes.Equal(got, body) {
			t.Fatalf("body of %d bytes: %x, %d, %q", l, got, gotN, reason)
		}
		next := appendFrame(slices.Clone(f), salt, testBody(20))
		if got, gotN, reason := parseFrame(next, salt); reason != "" || gotN != n || !bytes.Equal(got, body) {
			t.Fatalf("followed by a frame: %x, %d, %q", got, gotN, reason)
		}
		if _, _, reason := parseFrame(f, salt+1); reason != "bad record CRC" {
			t.Fatalf("another salt: %q", reason)
		}

		for k := range n {
			wantN := n
			if k < 8 {
				wantN = 0
			}
			if got, gotN, reason := parseFrame(f[:k], salt); got != nil || gotN != wantN || reason != "truncated record" {
				t.Fatalf("cut at %d of %d: %x, %d, %q", k, n, got, gotN, reason)
			}
		}

		eachChange(f, func(i int, c []byte) {
			got, _, reason := parseFrame(c, salt)
			switch {
			case got != nil || reason == "":
				t.Fatalf("body of %d bytes, byte %d changed to %#x: accepted %x", l, i, c[i], got)
			case i >= 4 && i < 8+l && reason != "bad record CRC":
				t.Fatalf("byte %d: %q, want bad record CRC", i, reason)
			case i >= 8+l && reason != "nonzero padding":
				t.Fatalf("byte %d of the padding: %q", i, reason)
			}
		})
		// Only a changed length reads beyond the frame.
		for _, b := range [][]byte{next, append(slices.Clone(f), make([]byte, 64)...)} {
			eachChange(b[:4], func(i int, c []byte) {
				if got, _, reason := parseFrame(append(c, b[4:]...), salt); got != nil || reason == "" {
					t.Fatalf("body of %d bytes, byte %d changed to %#x before more bytes: accepted %x", l, i, c[i], got)
				}
			})
		}
	}

	f := appendFrame(nil, salt, testBody(9))
	for l := range minBody {
		c := slices.Clone(f)
		binary.LittleEndian.PutUint32(c, uint32(l))
		if got, n, reason := parseFrame(c, salt); got != nil || n != 0 || reason != fmt.Sprintf("bad record length %d", l) {
			t.Errorf("length %d: %x, %d, %q", l, got, n, reason)
		}
	}
	for _, l := range []uint32{1<<28 + 1, math.MaxUint32} {
		c := binary.LittleEndian.AppendUint32(nil, l)
		if _, n, reason := parseFrame(append(c, f[4:]...), salt); n != 0 || reason != fmt.Sprintf("bad record length %d", l) {
			t.Errorf("length %d: %d, %q", l, n, reason)
		}
	}
	if _, _, reason := parseFrame(make([]byte, 64), salt); reason != "bad record length 0" {
		t.Errorf("zeros: %q", reason)
	}

	defer func(n int) { maxBody = n }(maxBody)
	maxBody = 100
	f = appendFrame(nil, salt, testBody(100))
	if got, n, reason := parseFrame(f, salt); reason != "" || n != 112 || len(got) != 100 {
		t.Errorf("body of maxBody bytes: %d, %d, %q", len(got), n, reason)
	}
	binary.LittleEndian.PutUint32(f, 101)
	if _, _, reason := parseFrame(f, salt); reason != "bad record length 101" {
		t.Errorf("body of maxBody+1 bytes: %q", reason)
	}
	for _, l := range []int{0, minBody - 1, 101} {
		func() {
			defer func() {
				if r := recover(); r != fmt.Sprintf("logdb: a record body of %d bytes", l) {
					t.Errorf("appendFrame of %d bytes: %v", l, r)
				}
			}()
			appendFrame(nil, salt, make([]byte, l))
		}()
	}
}

// setU64 returns a copy of b, a header of 64 bytes, with v at off and the CRC of that.
func setU64(b []byte, off int, v uint64) []byte {
	c := slices.Clone(b)
	binary.LittleEndian.PutUint64(c[off:], v)
	binary.LittleEndian.PutUint32(c[60:], crc32c(c[:60]))
	return c
}

func headBytes(s0, s1 headSlot) []byte {
	return appendHeadSlot(appendHeadSlot(nil, s0), s1)
}

func TestHead(t *testing.T) {
	s := headSlot{counter: 6, gen: 0x0102, seg: 0x0304, end: 0x0508, seq: 0x0607, crc: 0x08090a0b}
	b := appendHeadSlot([]byte("prefix"), s)[6:]
	want := make([]byte, 64)
	copy(want, "whrm-hed")
	binary.LittleEndian.PutUint32(want[8:], 1)
	binary.LittleEndian.PutUint64(want[16:], 6)
	binary.LittleEndian.PutUint64(want[24:], 0x0102)
	binary.LittleEndian.PutUint64(want[32:], 0x0304)
	binary.LittleEndian.PutUint64(want[40:], 0x0508)
	binary.LittleEndian.PutUint64(want[48:], 0x0607)
	binary.LittleEndian.PutUint32(want[56:], 0x08090a0b)
	binary.LittleEndian.PutUint32(want[60:], crc32c(want[:60]))
	if !bytes.Equal(b, want) {
		t.Fatalf("slot\n%x, want\n%x", b, want)
	}
	if got, err := parseHeadSlot(headPath, b, 0); err != nil || got != s {
		t.Fatalf("%+v, %v", got, err)
	}

	eachChange(b, func(i int, c []byte) {
		_, err := parseHeadSlot(headPath, c, 0)
		checkStartChange(t, err, headPath, c, i, "a head", "bad slot CRC")
	})

	older, newer := s, s
	older.counter, older.end, older.seq = 5, 0x0500, 0x0606
	newer.counter = 6
	if got, err := parseHead(headPath, headBytes(newer, older)); err != nil || got != newer {
		t.Errorf("newer in slot 0: %+v, %v", got, err)
	}
	newer.counter, older.counter = 7, 6
	if got, err := parseHead(headPath, headBytes(older, newer)); err != nil || got != newer {
		t.Errorf("newer in slot 1: %+v, %v", got, err)
	}
	h := headBytes(older, newer)
	eachChange(h[64:], func(i int, c []byte) {
		got, err := parseHead(headPath, append(h[:64:64], c...))
		if i >= 8 && i < 12 && binary.LittleEndian.Uint32(c[8:]) > 1 {
			checkVersion(t, err, headPath, binary.LittleEndian.Uint32(c[8:]))
		} else if err != nil || got != older {
			t.Fatalf("slot 1 changed in byte %d: %+v, %v", i, got, err)
		}
	})
	eachChange(h[:64], func(i int, c []byte) {
		got, err := parseHead(headPath, append(slices.Clone(c), h[64:]...))
		if i >= 8 && i < 12 && binary.LittleEndian.Uint32(c[8:]) > 1 {
			checkVersion(t, err, headPath, binary.LittleEndian.Uint32(c[8:]))
		} else if err != nil || got != newer {
			t.Fatalf("slot 0 changed in byte %d: %+v, %v", i, got, err)
		}
	})

	_, err := parseHead(headPath, make([]byte, 128))
	checkCorrupt(t, err, headPath, -1, "no valid slot")
	for _, n := range []int{0, 1, 64, 127, 129, 256} {
		_, err := parseHead(headPath, make([]byte, n))
		checkCorrupt(t, err, headPath, -1, fmt.Sprintf("has %d bytes, want 128", n))
	}
	h = headBytes(older, newer)
	for n := range len(h) {
		_, err := parseHead(headPath, h[:n])
		checkCorrupt(t, err, headPath, -1, fmt.Sprintf("has %d bytes, want 128", n))
	}

	for _, c := range []struct {
		name string
		s    headSlot
		i    int
	}{
		{"even counter in slot 1", headSlot{counter: 6, gen: 1, seg: 1, end: 64}, 1},
		{"odd counter in slot 0", headSlot{counter: 7, gen: 1, seg: 1, end: 64}, 0},
		{"generation 0", headSlot{counter: 0, gen: 0, seg: 1, end: 64}, 0},
		{"segment 0", headSlot{counter: 0, gen: 1, seg: 0, end: 64}, 0},
		{"end in the header", headSlot{counter: 0, gen: 1, seg: 1, end: 56}, 0},
		{"end not aligned", headSlot{counter: 0, gen: 1, seg: 1, end: 68}, 0},
	} {
		_, err := parseHeadSlot(headPath, appendHeadSlot(nil, c.s), c.i)
		checkCorrupt(t, err, headPath, -1, "bad slot fields")
	}
	// appendHeadSlot does not write an end beyond int64.
	beyond := setU64(appendHeadSlot(nil, headSlot{counter: 0, gen: 1, seg: 1, end: 64}), 40, math.MaxUint64-7)
	_, err = parseHeadSlot(headPath, beyond, 0)
	checkCorrupt(t, err, headPath, -1, "bad slot fields")
	c := appendHeadSlot(nil, headSlot{counter: 0, gen: 1, seg: 1, end: 64})
	c[12] = 1
	binary.LittleEndian.PutUint32(c[60:], crc32c(c[:60]))
	_, err = parseHeadSlot(headPath, c, 0)
	checkCorrupt(t, err, headPath, -1, "nonzero reserved bytes")

	created := headSlot{counter: 0, gen: 1, seg: 1, end: 64}
	if got, err := parseHead(headPath, append(appendHeadSlot(nil, created), make([]byte, 64)...)); err != nil || got != created {
		t.Errorf("only slot 0: %+v, %v", got, err)
	}
}

func TestObjHeader(t *testing.T) {
	b := appendObjHeader([]byte("prefix"))[6:]
	if want := []byte("whrm-obj\x01\x00\x00\x00\x00\x00\x00\x00"); !bytes.Equal(b, want) {
		t.Fatalf("header %x, want %x", b, want)
	}
	if err := parseObjHeader(objPath, append(slices.Clone(b), "payload"...)); err != nil {
		t.Fatal(err)
	}
	eachChange(b, func(i int, c []byte) {
		err := parseObjHeader(objPath, c)
		switch {
		case i < 12:
			checkStartChange(t, err, objPath, c, i, "an object", "")
		case i == 12 && c[i] == 1:
			checkCorrupt(t, err, objPath, -1, "sealed values are not supported by this version")
		case i == 12:
			checkCorrupt(t, err, objPath, -1, fmt.Sprintf("unknown value envelope %d", c[i]))
		default:
			checkCorrupt(t, err, objPath, -1, "nonzero reserved bytes")
		}
	})
	for n := range len(b) {
		checkCorrupt(t, parseObjHeader(objPath, b[:n]), objPath, -1, "short file")
	}
}

func TestCheckpoint(t *testing.T) {
	body := testBody(100)
	h := ckpHeader{gen: 0x0102, seq: 0x0304, seg: 0x0506, off: 0x0708, recCRC: 0x090a0b0c, bodyLen: 100,
		bodyCRC: crc32c(body)}
	hb := appendCkpHeader([]byte("prefix"), h)[6:]
	want := make([]byte, 64)
	copy(want, "whrm-ckp")
	binary.LittleEndian.PutUint32(want[8:], 1)
	binary.LittleEndian.PutUint32(want[12:], 0x090a0b0c)
	binary.LittleEndian.PutUint64(want[16:], 0x0102)
	binary.LittleEndian.PutUint64(want[24:], 0x0304)
	binary.LittleEndian.PutUint64(want[32:], 0x0506)
	binary.LittleEndian.PutUint64(want[40:], 0x0708)
	binary.LittleEndian.PutUint64(want[48:], 100)
	binary.LittleEndian.PutUint32(want[56:], crc32c(body))
	binary.LittleEndian.PutUint32(want[60:], crc32c(want[:60]))
	if !bytes.Equal(hb, want) {
		t.Fatalf("header\n%x, want\n%x", hb, want)
	}
	file := slices.Concat(hb, body)
	if got, err := parseCkpHeader(ckpPath, hb); err != nil || got != h {
		t.Fatalf("header: %+v, %v", got, err)
	}
	if got, gotBody, err := parseCheckpoint(ckpPath, file); err != nil || got != h || !bytes.Equal(gotBody, body) {
		t.Fatalf("file: %+v, %v", got, err)
	}

	eachChange(hb, func(i int, c []byte) {
		_, _, err := parseCheckpoint(ckpPath, append(slices.Clone(c), body...))
		checkStartChange(t, err, ckpPath, c, i, "a checkpoint", "bad header CRC")
	})
	eachChange(body, func(i int, c []byte) {
		_, _, err := parseCheckpoint(ckpPath, slices.Concat(hb, c))
		checkCorrupt(t, err, ckpPath, -1, "bad body CRC")
	})
	for n := range len(file) {
		_, _, err := parseCheckpoint(ckpPath, file[:n])
		if n < 64 {
			checkCorrupt(t, err, ckpPath, -1, "short file")
		} else {
			checkCorrupt(t, err, ckpPath, -1, fmt.Sprintf("has %d bytes, want 164", n))
		}
	}
	_, _, err := parseCheckpoint(ckpPath, append(slices.Clone(file), 0))
	checkCorrupt(t, err, ckpPath, -1, "has 165 bytes, want 164")

	empty := ckpHeader{gen: 1, seq: 1, seg: 1, off: 64, bodyCRC: crc32c(nil)}
	if got, gotBody, err := parseCheckpoint(ckpPath, appendCkpHeader(nil, empty)); err != nil || got != empty ||
		len(gotBody) != 0 {
		t.Errorf("empty body: %+v, %v", got, err)
	}

	for _, c := range []struct {
		name string
		h    ckpHeader
	}{
		{"generation 0", ckpHeader{gen: 0, seq: 1, seg: 1, off: 64}},
		{"sequence 0", ckpHeader{gen: 1, seq: 0, seg: 1, off: 64}},
		{"segment 0", ckpHeader{gen: 1, seq: 1, seg: 0, off: 64}},
		{"offset in the header", ckpHeader{gen: 1, seq: 1, seg: 1, off: 56}},
		{"offset not aligned", ckpHeader{gen: 1, seq: 1, seg: 1, off: 68}},
		{"body too long", ckpHeader{gen: 1, seq: 1, seg: 1, off: 64, bodyLen: math.MaxInt64 - 63}},
	} {
		_, err := parseCkpHeader(ckpPath, appendCkpHeader(nil, c.h))
		checkCorrupt(t, err, ckpPath, -1, "bad header fields")
	}
	// appendCkpHeader does not write an offset or a length beyond int64.
	valid := appendCkpHeader(nil, ckpHeader{gen: 1, seq: 1, seg: 1, off: 64})
	for _, c := range [][]byte{setU64(valid, 40, math.MaxUint64-7), setU64(valid, 48, math.MaxUint64)} {
		_, err := parseCkpHeader(ckpPath, c)
		checkCorrupt(t, err, ckpPath, -1, "bad header fields")
	}
	longest := ckpHeader{gen: 1, seq: 1, seg: 1, off: 64, bodyLen: math.MaxInt64 - 64}
	if _, err := parseCkpHeader(ckpPath, appendCkpHeader(nil, longest)); err != nil {
		t.Errorf("longest body: %v", err)
	}

	c := slices.Clone(file)
	binary.LittleEndian.PutUint32(c[8:], 2)
	binary.LittleEndian.PutUint32(c[60:], 0)
	if _, _, err := parseCheckpoint(ckpPath, c); err == nil ||
		err.Error() != ckpPath+": format version 2, newer than this program knows (1)" {
		t.Errorf("newer version with a bad CRC: %v", err)
	}
	if _, _, err := parseCheckpoint(ckpPath, slices.Concat([]byte("whrm-log"), file[8:])); err == nil ||
		!strings.HasSuffix(err.Error(), ": corrupt: not a checkpoint of whiteroom's log") {
		t.Errorf("magic: %v", err)
	}
}

// TestNewerVersion checks that a file of a newer version is refused as such whatever its size and CRC, which that
// version may have changed.
func TestNewerVersion(t *testing.T) {
	slot := appendHeadSlot(nil, headSlot{counter: 0, gen: 1, seg: 1, end: 64})
	for _, c := range []struct {
		path  string
		file  []byte
		parse func([]byte) error
	}{
		{segPath, appendSegHeader(nil, segHeader{gen: 1, index: 2}), func(b []byte) error {
			_, err := parseSegHeader(segPath, b, 1, 2)
			return err
		}},
		{ckpPath, appendCkpHeader(nil, ckpHeader{gen: 1, seq: 1, seg: 1, off: 64, bodyCRC: crc32c(nil)}), func(b []byte) error {
			_, _, err := parseCheckpoint(ckpPath, b)
			return err
		}},
		{objPath, appendObjHeader(nil), func(b []byte) error { return parseObjHeader(objPath, b) }},
		{headPath, slices.Concat(slot, slot), func(b []byte) error {
			_, err := parseHead(headPath, b)
			return err
		}},
	} {
		if err := c.parse(c.file); err != nil {
			t.Fatalf("%s: %v", c.path, err)
		}
		for _, v := range []uint32{2, 0x100, math.MaxUint32} {
			newer := slices.Clone(c.file)
			binary.LittleEndian.PutUint32(newer[8:], v)
			for n := 12; n <= len(newer)+64; n++ {
				b := append(slices.Clone(newer), make([]byte, 64)...)[:n]
				checkVersion(t, c.parse(b), c.path, v)
			}
		}
	}
}
