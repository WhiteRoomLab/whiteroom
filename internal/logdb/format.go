package logdb

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"math"
)

// The files of the engine start with these magics, followed by formatVersion.
const (
	segMagic  = "whrm-log"
	ckpMagic  = "whrm-ckp"
	headMagic = "whrm-hed"
	objMagic  = "whrm-obj"

	formatVersion = 1

	segHeaderSize = 64
	headSlotSize  = 64
	headSize      = 2 * headSlotSize
	objHeaderSize = 16
	ckpHeaderSize = 64
)

// MaxKey is the largest key, in bytes.
const MaxKey = 4096

// maxBody bounds the body of a record, and so a transaction. It is a var so that tests can lower it.
var maxBody = 1 << 28

// minBody is the length of a body without payload: its type and sequence number.
const minBody = 9

// recType is the type of a record.
type recType byte

const (
	recCommit  recType = 1
	recBase    recType = 2
	recBaseEnd recType = 3
)

// opCode is the code of an op.
type opCode byte

const (
	opPut    opCode = 1
	opDelete opCode = 2
	opInline opCode = 3 // an object in the record
	opFile   opCode = 4 // an object in a file of objects/
	opEvent  opCode = 5
)

// envKind is the envelope of a value.
type envKind byte

const (
	envPlain  envKind = 0
	envSealed envKind = 1
)

// objPlace is the placement of an object in a checkpoint.
type objPlace byte

const (
	placeInline objPlace = 1
	placeFile   objPlace = 2
)

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

func corruptFile(path, reason string) error {
	return &CorruptError{Path: path, Offset: -1, Reason: reason}
}

func allZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

// newer refuses b if it starts with magic and a version newer than formatVersion, whatever follows: a newer version
// may change everything after its version, the size too.
func newer(path string, b []byte, magic string) error {
	if len(b) >= 12 && string(b[:8]) == magic {
		if v := binary.LittleEndian.Uint32(b[8:]); v > formatVersion {
			return &VersionError{Path: path, Version: v, Known: formatVersion}
		}
	}
	return nil
}

// checkStart checks what comes before the CRC of a header: a newer version, the size, the magic and the version.
func checkStart(path string, b []byte, size int, magic, what string) error {
	if err := newer(path, b, magic); err != nil {
		return err
	}
	if len(b) < size {
		return corruptFile(path, "short file")
	}
	if string(b[:8]) != magic {
		return corruptFile(path, "not "+what+" of whiteroom's log")
	}
	if v := binary.LittleEndian.Uint32(b[8:]); v != formatVersion {
		return corruptFile(path, fmt.Sprintf("bad format version %d", v))
	}
	return nil
}

func checkCRC(b []byte) bool {
	return crc32.Checksum(b[:60], castagnoli) == binary.LittleEndian.Uint32(b[60:])
}

// envelopeReason says why a value of envelope env cannot be read, or returns "".
func envelopeReason(env envKind) string {
	switch env {
	case envPlain:
		return ""
	case envSealed:
		return "sealed values are not supported by this version"
	}
	return fmt.Sprintf("unknown value envelope %d", env)
}

// segHeader is the header of a segment: the segment index of generation gen, whose first record has the sequence
// number firstSeq. salt is in the CRC of every record of the segment.
type segHeader struct {
	gen, index, firstSeq, salt uint64
}

func appendSegHeader(dst []byte, h segHeader) []byte {
	start := len(dst)
	dst = append(dst, segMagic...)
	dst = binary.LittleEndian.AppendUint32(dst, formatVersion)
	dst = binary.LittleEndian.AppendUint32(dst, segHeaderSize)
	dst = binary.LittleEndian.AppendUint64(dst, h.gen)
	dst = binary.LittleEndian.AppendUint64(dst, h.index)
	dst = binary.LittleEndian.AppendUint64(dst, h.firstSeq)
	dst = binary.LittleEndian.AppendUint64(dst, h.salt)
	dst = append(dst, make([]byte, 12)...)
	return binary.LittleEndian.AppendUint32(dst, crc32.Checksum(dst[start:], castagnoli))
}

// parseSegHeader checks the header at the start of b, the content of the segment at path, which is named for gen
// and index. The continuity of firstSeq is for the caller to check.
func parseSegHeader(path string, b []byte, gen, index uint64) (segHeader, error) {
	if err := checkStart(path, b, segHeaderSize, segMagic, "a segment"); err != nil {
		return segHeader{}, err
	}
	if !checkCRC(b) {
		return segHeader{}, corruptFile(path, "bad header CRC")
	}
	if n := binary.LittleEndian.Uint32(b[12:]); n != segHeaderSize {
		return segHeader{}, corruptFile(path, fmt.Sprintf("bad header size %d", n))
	}
	if !allZero(b[48:60]) {
		return segHeader{}, corruptFile(path, "nonzero reserved bytes")
	}
	h := segHeader{
		gen:      binary.LittleEndian.Uint64(b[16:]),
		index:    binary.LittleEndian.Uint64(b[24:]),
		firstSeq: binary.LittleEndian.Uint64(b[32:]),
		salt:     binary.LittleEndian.Uint64(b[40:]),
	}
	if h.gen != gen || h.index != index {
		return segHeader{}, corruptFile(path, fmt.Sprintf("the header names generation %d, index %d", h.gen, h.index))
	}
	return h, nil
}

// frameLen is the length of the frame of a body of n bytes: 8 bytes of length and CRC, the body, and zeros to a
// multiple of 8.
func frameLen(n int) int {
	return 8 + (n+7)&^7
}

func frameCRC(salt uint64, length, body []byte) uint32 {
	var s [8]byte
	binary.LittleEndian.PutUint64(s[:], salt)
	c := crc32.Update(0, castagnoli, s[:])
	c = crc32.Update(c, castagnoli, length)
	return crc32.Update(c, castagnoli, body)
}

// appendFrame appends the frame of body for a segment with salt. It panics if the body could not be read again.
func appendFrame(dst []byte, salt uint64, body []byte) []byte {
	if len(body) < minBody || len(body) > maxBody {
		panic(fmt.Sprintf("logdb: a record body of %d bytes", len(body)))
	}
	start := len(dst)
	dst = binary.LittleEndian.AppendUint32(dst, uint32(len(body)))
	dst = binary.LittleEndian.AppendUint32(dst, frameCRC(salt, dst[start:], body))
	dst = append(dst, body...)
	return append(dst, make([]byte, frameLen(len(body))-8-len(body))...)
}

// parseFrame reads the frame at the start of b in a segment with salt, and returns its body, which aliases b, and its
// length, or why b does not start with a frame: "truncated record", "bad record length %d" or "bad record CRC", which
// is also what a crash can leave, or "nonzero padding" after a valid CRC. n is set whenever the length field is
// valid, also with a reason, so that a caller that has read the start of a frame knows how much to read.
func parseFrame(b []byte, salt uint64) (body []byte, n int, reason string) {
	if len(b) < 8 {
		return nil, 0, "truncated record"
	}
	l := binary.LittleEndian.Uint32(b)
	if l < minBody || uint64(l) > uint64(maxBody) {
		return nil, 0, fmt.Sprintf("bad record length %d", l)
	}
	n = frameLen(int(l))
	if len(b) < n {
		return nil, n, "truncated record"
	}
	body = b[8 : 8+l]
	if binary.LittleEndian.Uint32(b[4:]) != frameCRC(salt, b[:4], body) {
		return nil, n, "bad record CRC"
	}
	if !allZero(b[8+l : n]) {
		return nil, n, "nonzero padding"
	}
	return body, n, ""
}

// headSlot is a slot of the file head: the durable end of the log after the head update numbered counter, in the
// slot counter%2. crc is the CRC (the frame's) of the record that ends at end, or 0 if there is none.
type headSlot struct {
	counter  uint64
	gen, seg uint64
	end      int64
	seq      uint64
	crc      uint32
}

func appendHeadSlot(dst []byte, s headSlot) []byte {
	start := len(dst)
	dst = append(dst, headMagic...)
	dst = binary.LittleEndian.AppendUint32(dst, formatVersion)
	dst = binary.LittleEndian.AppendUint32(dst, 0)
	dst = binary.LittleEndian.AppendUint64(dst, s.counter)
	dst = binary.LittleEndian.AppendUint64(dst, s.gen)
	dst = binary.LittleEndian.AppendUint64(dst, s.seg)
	dst = binary.LittleEndian.AppendUint64(dst, uint64(s.end))
	dst = binary.LittleEndian.AppendUint64(dst, s.seq)
	dst = binary.LittleEndian.AppendUint32(dst, s.crc)
	return binary.LittleEndian.AppendUint32(dst, crc32.Checksum(dst[start:], castagnoli))
}

// parseHeadSlot checks slot i of the file head at path, whose bytes are b.
func parseHeadSlot(path string, b []byte, i int) (headSlot, error) {
	if err := checkStart(path, b, headSlotSize, headMagic, "a head"); err != nil {
		return headSlot{}, err
	}
	if !checkCRC(b) {
		return headSlot{}, corruptFile(path, "bad slot CRC")
	}
	if binary.LittleEndian.Uint32(b[12:]) != 0 {
		return headSlot{}, corruptFile(path, "nonzero reserved bytes")
	}
	s := headSlot{
		counter: binary.LittleEndian.Uint64(b[16:]),
		gen:     binary.LittleEndian.Uint64(b[24:]),
		seg:     binary.LittleEndian.Uint64(b[32:]),
		seq:     binary.LittleEndian.Uint64(b[48:]),
		crc:     binary.LittleEndian.Uint32(b[56:]),
	}
	end := binary.LittleEndian.Uint64(b[40:])
	if s.counter%2 != uint64(i) || s.gen == 0 || s.seg == 0 || end < segHeaderSize || end%8 != 0 ||
		end > math.MaxInt64 {
		return headSlot{}, corruptFile(path, "bad slot fields")
	}
	s.end = int64(end)
	return s, nil
}

// parseHead returns the valid slot with the larger counter of the file head at path, whose content is b. A slot of a
// newer version makes it fail, whatever the other slot holds.
func parseHead(path string, b []byte) (headSlot, error) {
	for i := 0; i < min(len(b), headSize); i += headSlotSize {
		if err := newer(path, b[i:], headMagic); err != nil {
			return headSlot{}, err
		}
	}
	if len(b) != headSize {
		return headSlot{}, corruptFile(path, fmt.Sprintf("has %d bytes, want %d", len(b), headSize))
	}
	var slots []headSlot
	for i := range 2 {
		if s, err := parseHeadSlot(path, b[i*headSlotSize:(i+1)*headSlotSize], i); err == nil {
			slots = append(slots, s)
		}
	}
	switch len(slots) {
	case 0:
		return headSlot{}, corruptFile(path, "no valid slot")
	case 2:
		if slots[0].counter < slots[1].counter {
			return slots[1], nil
		}
	}
	return slots[0], nil
}

func appendObjHeader(dst []byte) []byte {
	dst = append(dst, objMagic...)
	dst = binary.LittleEndian.AppendUint32(dst, formatVersion)
	return append(dst, byte(envPlain), 0, 0, 0)
}

// parseObjHeader checks the header at the start of b, the content of the object file at path. The size is for the
// caller to check.
func parseObjHeader(path string, b []byte) error {
	if err := checkStart(path, b, objHeaderSize, objMagic, "an object"); err != nil {
		return err
	}
	if r := envelopeReason(envKind(b[12])); r != "" {
		return corruptFile(path, r)
	}
	if !allZero(b[13:16]) {
		return corruptFile(path, "nonzero reserved bytes")
	}
	return nil
}

// ckpHeader is the header of a checkpoint: the state after the record seq of generation gen, whose frame is at off in
// the segment seg and has the CRC recCRC, in a body of bodyLen bytes with the CRC bodyCRC.
type ckpHeader struct {
	gen, seq, seg uint64
	off           int64
	recCRC        uint32
	bodyLen       int64
	bodyCRC       uint32
}

func appendCkpHeader(dst []byte, h ckpHeader) []byte {
	start := len(dst)
	dst = append(dst, ckpMagic...)
	dst = binary.LittleEndian.AppendUint32(dst, formatVersion)
	dst = binary.LittleEndian.AppendUint32(dst, h.recCRC)
	dst = binary.LittleEndian.AppendUint64(dst, h.gen)
	dst = binary.LittleEndian.AppendUint64(dst, h.seq)
	dst = binary.LittleEndian.AppendUint64(dst, h.seg)
	dst = binary.LittleEndian.AppendUint64(dst, uint64(h.off))
	dst = binary.LittleEndian.AppendUint64(dst, uint64(h.bodyLen))
	dst = binary.LittleEndian.AppendUint32(dst, h.bodyCRC)
	return binary.LittleEndian.AppendUint32(dst, crc32.Checksum(dst[start:], castagnoli))
}

// parseCkpHeader checks the header at the start of b, the content of the checkpoint at path.
func parseCkpHeader(path string, b []byte) (ckpHeader, error) {
	if err := checkStart(path, b, ckpHeaderSize, ckpMagic, "a checkpoint"); err != nil {
		return ckpHeader{}, err
	}
	if !checkCRC(b) {
		return ckpHeader{}, corruptFile(path, "bad header CRC")
	}
	h := ckpHeader{
		recCRC:  binary.LittleEndian.Uint32(b[12:]),
		gen:     binary.LittleEndian.Uint64(b[16:]),
		seq:     binary.LittleEndian.Uint64(b[24:]),
		seg:     binary.LittleEndian.Uint64(b[32:]),
		bodyCRC: binary.LittleEndian.Uint32(b[56:]),
	}
	off, n := binary.LittleEndian.Uint64(b[40:]), binary.LittleEndian.Uint64(b[48:])
	if h.gen == 0 || h.seq == 0 || h.seg == 0 || off < segHeaderSize || off%8 != 0 || off > math.MaxInt64 ||
		n > math.MaxInt64-ckpHeaderSize {
		return ckpHeader{}, corruptFile(path, "bad header fields")
	}
	h.off, h.bodyLen = int64(off), int64(n)
	return h, nil
}

// parseCheckpoint checks the checkpoint at path, whose content is b, and returns its header and its body, which
// aliases b.
func parseCheckpoint(path string, b []byte) (ckpHeader, []byte, error) {
	h, err := parseCkpHeader(path, b)
	if err != nil {
		return ckpHeader{}, nil, err
	}
	if want := ckpHeaderSize + h.bodyLen; int64(len(b)) != want {
		return ckpHeader{}, nil, corruptFile(path, fmt.Sprintf("has %d bytes, want %d", len(b), want))
	}
	body := b[ckpHeaderSize:]
	if crc32.Checksum(body, castagnoli) != h.bodyCRC {
		return ckpHeader{}, nil, corruptFile(path, "bad body CRC")
	}
	return h, body, nil
}
