package dbwire

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
)

// readStep bounds what Dec.Bytes allocates before the bytes have arrived.
const readStep = 64 << 10

// Enc writes the values of a body to W, and keeps the first error in Err: after it, the methods write nothing.
type Enc struct {
	W   io.Writer
	Err error
	buf [8]byte
}

func (e *Enc) write(p []byte) {
	if e.Err != nil {
		return
	}
	if _, err := e.W.Write(p); err != nil {
		e.Err = err
	}
}

func (e *Enc) fail(err error) {
	if e.Err == nil {
		e.Err = err
	}
}

func (e *Enc) U8(v uint8) {
	e.buf[0] = v
	e.write(e.buf[:1])
}

func (e *Enc) U16(v uint16) {
	binary.BigEndian.PutUint16(e.buf[:], v)
	e.write(e.buf[:2])
}

func (e *Enc) U32(v uint32) {
	binary.BigEndian.PutUint32(e.buf[:], v)
	e.write(e.buf[:4])
}

func (e *Enc) U64(v uint64) {
	binary.BigEndian.PutUint64(e.buf[:], v)
	e.write(e.buf[:8])
}

func (e *Enc) I64(v int64) { e.U64(uint64(v)) }

func (e *Enc) Bool(v bool) {
	if v {
		e.U8(1)
	} else {
		e.U8(0)
	}
}

// Bytes writes b after its length as a uint32.
func (e *Enc) Bytes(b []byte) {
	if uint64(len(b)) > math.MaxUint32 {
		e.fail(fmt.Errorf("dbwire: a byte string of %d bytes is too long", len(b)))
		return
	}
	e.U32(uint32(len(b)))
	e.write(b)
}

// Raw writes b as it is.
func (e *Enc) Raw(b []byte) { e.write(b) }

func (e *Enc) Hash(h [32]byte) { e.write(h[:]) }

// size writes a size or a count that is not negative, as a uint64.
func (e *Enc) size(n int64) {
	if n < 0 {
		e.fail(fmt.Errorf("dbwire: a size of %d", n))
		return
	}
	e.U64(uint64(n))
}

// str16 writes s after its length as a uint16.
func (e *Enc) str16(s string) {
	if len(s) > math.MaxUint16 {
		e.fail(fmt.Errorf("dbwire: a string of %d bytes is too long", len(s)))
		return
	}
	e.U16(uint16(len(s)))
	e.write([]byte(s))
}

// Dec reads the values of a body from R, and keeps the first error in Err: after it, the methods read nothing and
// return zero values. A stream that ends inside a value is io.ErrUnexpectedEOF, and an error of R other than io.EOF,
// such as the *RemoteError of a Reader, is kept as it is.
type Dec struct {
	R   io.Reader
	Err error
	buf [8]byte
}

func (d *Dec) fail(err error) {
	if d.Err == nil {
		d.Err = err
	}
}

func (d *Dec) readFull(p []byte) bool {
	if d.Err != nil {
		return false
	}
	if _, err := io.ReadFull(d.R, p); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		d.fail(err)
		return false
	}
	return true
}

func (d *Dec) U8() uint8 {
	if !d.readFull(d.buf[:1]) {
		return 0
	}
	return d.buf[0]
}

func (d *Dec) U16() uint16 {
	if !d.readFull(d.buf[:2]) {
		return 0
	}
	return binary.BigEndian.Uint16(d.buf[:])
}

func (d *Dec) U32() uint32 {
	if !d.readFull(d.buf[:4]) {
		return 0
	}
	return binary.BigEndian.Uint32(d.buf[:])
}

func (d *Dec) U64() uint64 {
	if !d.readFull(d.buf[:8]) {
		return 0
	}
	return binary.BigEndian.Uint64(d.buf[:])
}

func (d *Dec) I64() int64 { return int64(d.U64()) }

// Bool reads a byte that is 0 or 1.
func (d *Dec) Bool() bool {
	switch v := d.U8(); {
	case d.Err != nil:
	case v == 0:
		return false
	case v == 1:
		return true
	default:
		d.fail(protocolf("a boolean of %d", v))
	}
	return false
}

// Bytes reads a byte string of at most max bytes. It allocates for the bytes that have arrived, a step of 64 KiB at a
// time, not for the length that the peer says.
func (d *Dec) Bytes(max int64) []byte {
	n := d.U32()
	if d.Err != nil {
		return nil
	}
	if int64(n) > max {
		d.fail(limitf("a byte string of %d bytes, over the limit of %d", n, max))
		return nil
	}
	return d.fill(int64(n))
}

// fill reads n bytes into a new slice, which is not nil.
func (d *Dec) fill(n int64) []byte {
	if n <= readStep {
		b := make([]byte, n)
		if !d.readFull(b) {
			return nil
		}
		return b
	}
	var parts [][]byte
	for got := int64(0); got < n; {
		p := make([]byte, min(readStep, n-got))
		if !d.readFull(p) {
			return nil
		}
		parts = append(parts, p)
		got += int64(len(p))
	}
	b := make([]byte, 0, n)
	for _, p := range parts {
		b = append(b, p...)
	}
	return b
}

// Raw reads len(p) bytes into p.
func (d *Dec) Raw(p []byte) { d.readFull(p) }

func (d *Dec) Hash() [32]byte {
	var h [32]byte
	d.readFull(h[:])
	return h
}

// size reads a size or a count that fits an int64.
func (d *Dec) size() int64 {
	n := d.U64()
	if d.Err == nil && n > math.MaxInt64 {
		d.fail(protocolf("a size of %d bytes", n))
		return 0
	}
	return int64(n)
}

// str16 reads a string of at most max bytes after its length as a uint16.
func (d *Dec) str16(max int) string {
	n := d.U16()
	if d.Err != nil {
		return ""
	}
	if int(n) > max {
		d.fail(limitf("a string of %d bytes, over the limit of %d", n, max))
		return ""
	}
	return string(d.fill(int64(n)))
}

// Stream returns a reader of the next n bytes of the body: they are not read before the reader is. If the body ends
// first, Err is io.ErrUnexpectedEOF.
func (d *Dec) Stream(n int64) io.Reader { return &streamReader{d: d, left: n} }

type streamReader struct {
	d    *Dec
	left int64
}

func (s *streamReader) Read(p []byte) (int, error) {
	if s.left <= 0 {
		return 0, io.EOF
	}
	if s.d.Err != nil {
		return 0, s.d.Err
	}
	if int64(len(p)) > s.left {
		p = p[:s.left]
	}
	n, err := s.d.R.Read(p)
	s.left -= int64(n)
	if err != nil && (s.left != 0 || !errors.Is(err, io.EOF)) {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		s.d.fail(err)
		return n, s.d.Err
	}
	return n, nil
}

// End returns Err, or ErrProtocol if the body has more bytes.
func (d *Dec) End() error {
	if d.Err != nil {
		return d.Err
	}
	var b [1]byte
	n, err := io.ReadFull(d.R, b[:])
	switch {
	case n > 0:
		d.fail(protocolf("the body has more bytes than the message says"))
	case !errors.Is(err, io.EOF):
		d.fail(err)
	}
	return d.Err
}
