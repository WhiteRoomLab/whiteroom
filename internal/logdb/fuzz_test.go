package logdb

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"os"
	"slices"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/logdb/simfs"
)

// checkRefusal checks that err, of a parser of a file of the engine, is a CorruptError of the whole file at path, or a
// VersionError of a newer version.
func checkRefusal(t *testing.T, err error, path string) {
	t.Helper()
	var ce *CorruptError
	var ve *VersionError
	switch {
	case errors.As(err, &ce):
		if ce.Path != path || ce.Offset != -1 || ce.Reason == "" {
			t.Fatalf("error %+v", ce)
		}
	case errors.As(err, &ve):
		if ve.Path != path || ve.Version <= formatVersion || ve.Known != formatVersion {
			t.Fatalf("error %+v", ve)
		}
	default:
		t.Fatalf("error %v of type %T", err, err)
	}
}

// signed returns b with the CRC of the header of 64 bytes at off set, in a copy, or b if it is too short. The fuzzer
// cannot guess a CRC: with it, it reaches the checks that come after the CRC.
func signed(b []byte, off int) []byte {
	if len(b) < off+64 {
		return b
	}
	c := slices.Clone(b)
	binary.LittleEndian.PutUint32(c[off+60:], crc32.Checksum(c[off:off+60], castagnoli))
	return c
}

// FuzzSegHeader checks that parseSegHeader never panics, refuses with a CorruptError or a VersionError, and accepts
// only a header that appendSegHeader makes, for the generation and the index that it names.
func FuzzSegHeader(f *testing.F) {
	h := appendSegHeader(nil, segHeader{gen: 1, index: 2, firstSeq: 3, salt: 4})
	f.Add(h)
	f.Add(append(slices.Clone(h), 1, 2, 3))
	f.Add(h[:63])
	f.Add([]byte{})
	newer := slices.Clone(h)
	binary.LittleEndian.PutUint32(newer[8:], 2)
	f.Add(newer)
	f.Fuzz(func(t *testing.T, b []byte) {
		for _, c := range [][]byte{b, signed(b, 0)} {
			gen, index := genNum(1), segNum(1)
			if len(c) >= 32 {
				gen, index = genNum(binary.LittleEndian.Uint64(c[16:])), segNum(binary.LittleEndian.Uint64(c[24:]))
			}
			h, err := parseSegHeader(segPath, c, gen, index)
			if err != nil {
				checkRefusal(t, err, segPath)
				continue
			}
			if again := appendSegHeader(nil, h); !bytes.Equal(again, c[:segHeaderSize]) {
				t.Fatalf("encoded again\n%x, was\n%x", again, c[:segHeaderSize])
			}
			if h.gen != gen || h.index != index {
				t.Fatalf("header %+v for generation %d, index %d", h, gen, index)
			}
			if _, err := parseSegHeader(segPath, c, gen+1, index); err == nil {
				t.Fatalf("accepted for another generation")
			}
			if _, err := parseSegHeader(segPath, c, gen, index+1); err == nil {
				t.Fatalf("accepted for another index")
			}
		}
	})
}

// signedFrame returns b with the CRC of the frame at its start set for salt, in a copy, or b if it has no whole frame.
func signedFrame(b []byte, salt uint64) []byte {
	if len(b) < 8 {
		return b
	}
	l := binary.LittleEndian.Uint32(b)
	if l < minBody || int64(l) > int64(len(b)-8) {
		return b
	}
	c := slices.Clone(b)
	binary.LittleEndian.PutUint32(c[4:], frameCRC(salt, c[:4], c[8:8+l]))
	return c
}

// FuzzFrame checks that parseFrame never panics, and accepts only a frame that appendFrame makes for the salt.
func FuzzFrame(f *testing.F) {
	for _, body := range sampleBodies() {
		f.Add(appendFrame(nil, 7, body), uint64(7))
	}
	frame := appendFrame(nil, 7, testBody(9))
	f.Add(append(slices.Clone(frame), frame...), uint64(7))
	f.Add(frame[:len(frame)-1], uint64(7))
	f.Add(frame, uint64(8))
	f.Add(make([]byte, 64), uint64(0))
	f.Fuzz(func(t *testing.T, b []byte, salt uint64) {
		for _, c := range [][]byte{b, signedFrame(b, salt)} {
			body, n, reason := parseFrame(c, salt)
			if reason != "" {
				if body != nil || !mayBeTorn(reason) && reason != "nonzero padding" {
					t.Fatalf("body %x, reason %q", body, reason)
				}
				if n != 0 && (n%8 != 0 || n < frameLen(minBody)) {
					t.Fatalf("length %d with the reason %q", n, reason)
				}
				continue
			}
			if n != frameLen(len(body)) || n > len(c) {
				t.Fatalf("a frame of %d bytes for a body of %d in %d bytes", n, len(body), len(c))
			}
			if again := appendFrame(nil, salt, body); !bytes.Equal(again, c[:n]) {
				t.Fatalf("encoded again\n%x, was\n%x", again, c[:n])
			}
		}
	})
}

// FuzzHead checks that parseHead never panics, and takes only a slot that appendHeadSlot makes: the valid one with
// the larger counter.
func FuzzHead(f *testing.F) {
	s0 := headSlot{counter: 6, gen: 1, seg: 2, end: 128, seq: 5, crc: 9}
	s1 := headSlot{counter: 7, gen: 1, seg: 2, end: 168, seq: 6, crc: 10}
	f.Add(headBytes(s0, s1))
	f.Add(append(appendHeadSlot(nil, s0), make([]byte, 64)...))
	f.Add(append(make([]byte, 64), appendHeadSlot(nil, s1)...))
	f.Add(make([]byte, 128))
	f.Add(headBytes(s0, s1)[:127])
	newer := headBytes(s0, s1)
	binary.LittleEndian.PutUint32(newer[64+8:], 2)
	f.Add(newer)
	f.Fuzz(func(t *testing.T, b []byte) {
		for _, c := range [][]byte{b, signed(signed(b, 0), headSlotSize)} {
			s, err := parseHead(headPath, c)
			if err != nil {
				checkRefusal(t, err, headPath)
				continue
			}
			if len(c) != headSize {
				t.Fatalf("accepted %d bytes", len(c))
			}
			i := 0
			if s.counter%2 == 1 {
				i = 1
			}
			if again := appendHeadSlot(nil, s); !bytes.Equal(again, c[i*headSlotSize:][:headSlotSize]) {
				t.Fatalf("slot %d encoded again\n%x, was\n%x", i, again, c[i*headSlotSize:][:headSlotSize])
			}
			// Only the slot that is taken is encoded again: the other one is not valid, or older.
			if o, err := parseHeadSlot(headPath, c[(1-i)*headSlotSize:][:headSlotSize], 1-i); err == nil && o.counter > s.counter {
				t.Fatalf("slot %d has the counter %d, but slot %d with %d was taken", 1-i, o.counter, i, s.counter)
			}
		}
	})
}

// FuzzObjHeader checks that parseObjHeader never panics, and accepts only the header that appendObjHeader makes.
func FuzzObjHeader(f *testing.F) {
	h := appendObjHeader(nil)
	f.Add(h)
	f.Add(append(slices.Clone(h), "payload"...))
	f.Add(h[:15])
	sealed := slices.Clone(h)
	sealed[12] = byte(envSealed)
	f.Add(sealed)
	f.Fuzz(func(t *testing.T, b []byte) {
		err := parseObjHeader(objPath, b)
		if want := bytes.HasPrefix(b, appendObjHeader(nil)); want != (err == nil) {
			t.Fatalf("%x: %v", b, err)
		}
		if err != nil {
			checkRefusal(t, err, objPath)
		}
	})
}

// signedCkp returns b as a checkpoint whose header names the length and the CRC of the bytes after it, with its own
// CRC, in a copy, or b if it is too short.
func signedCkp(b []byte) []byte {
	if len(b) < ckpHeaderSize {
		return b
	}
	c := slices.Clone(b)
	binary.LittleEndian.PutUint64(c[48:], uint64(len(c)-ckpHeaderSize))
	binary.LittleEndian.PutUint32(c[56:], crc32.Checksum(c[ckpHeaderSize:], castagnoli))
	return signed(c, 0)
}

// FuzzCheckpoint checks that parseCheckpoint and decodeCheckpoint never panic, and accept only a file that
// writeCheckpoint makes of the state that they return.
func FuzzCheckpoint(f *testing.F) {
	h := ckpHeader{gen: 2, seq: 9, seg: 3, off: 4096, recCRC: 7}
	h1, h2 := hashOf(1), hashOf(2)
	f.Add(rawCkp(h, uv(1, 0, 0, 0)))
	f.Add(rawCkp(h, slices.Concat(uv(5, 2, 2, 1, 2), kv("a", "1"), kv("b", ""), uv(7, 1), kv("", "x"), uv(2),
		h1[:], []byte{byte(placeInline)}, uv(3, 80, 10), h2[:], []byte{byte(placeFile)}, uv(1<<40))))
	f.Add(append(rawCkp(h, uv(1, 0, 0, 0)), 0, 0))
	f.Add(rawCkp(h, uv(0, 0, 0, 0)))
	f.Fuzz(func(t *testing.T, b []byte) {
		for _, c := range [][]byte{b, signedCkp(b)} {
			h, body, err := parseCheckpoint(ckpPath, c)
			if err != nil {
				checkRefusal(t, err, ckpPath)
				continue
			}
			if again := appendCkpHeader(nil, h); !bytes.Equal(again, c[:ckpHeaderSize]) {
				t.Fatalf("header encoded again\n%x, was\n%x", again, c[:ckpHeaderSize])
			}
			st, reason := decodeCheckpoint(h, body, new(owner))
			if reason != "" {
				if st != nil {
					t.Fatalf("a state with the reason %q", reason)
				}
				continue
			}
			fsys := simfs.New(simDir)
			file, err := fsys.OpenFile("checkpoint", os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			if err := errors.Join(writeCheckpoint(file, st), file.Close()); err != nil {
				t.Fatal(err)
			}
			if again := fileData(t, fsys, "checkpoint"); !bytes.Equal(again, c) {
				t.Fatalf("written again\n%x, was\n%x", again, c)
			}
		}
	})
}
