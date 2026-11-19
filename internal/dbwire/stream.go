package dbwire

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"unicode/utf8"
)

func clampFrame(n int) int { return min(max(n, 1), MaxFrame) }

// Writer writes messages to w. A frame has at most the limit of payload bytes, which Writer keeps between 1 and
// MaxFrame, and a Write call of the underlying writer carries one or more whole frames: with SetHold, the messages that
// are finished stay in the buffer, and are written with the next Write call. The buffer is never larger than HeaderSize
// plus the larger of the limit and the hold.
type Writer struct {
	w     io.Writer
	limit int
	buf   []byte // the messages that are held, then the header of the frame that is being filled and its payload
	start int    // the offset of that header in buf
	hold  int
	kind  Kind
	open  bool
	err   error
}

func NewWriter(w io.Writer, maxFrame int) *Writer {
	return &Writer{w: w, limit: clampFrame(maxFrame), buf: make([]byte, 0, HeaderSize)}
}

// SetMaxFrame changes the limit of a frame. It panics if a message is open.
func (w *Writer) SetMaxFrame(n int) {
	if w.open {
		panic("dbwire: SetMaxFrame in an open message")
	}
	w.limit = clampFrame(n)
}

// SetHold makes End and Fail keep the message that they finish, to be written with the next Write call or by Flush, while
// the buffer then has fewer than n bytes, whatever the limit of a frame is; 0, the default, writes every message at once.
// It panics if a message is open.
func (w *Writer) SetHold(n int) {
	if w.open {
		panic("dbwire: SetHold in an open message")
	}
	w.hold = n
}

// Held returns the bytes of the finished messages that are in the buffer.
func (w *Writer) Held() int {
	if w.open {
		return w.start
	}
	return len(w.buf)
}

// Open reports whether a message is open: Begin has been called, and neither End nor Fail has.
func (w *Writer) Open() bool { return w.open }

// Flush writes the messages that are held with one Write call. It panics if a message is open.
func (w *Writer) Flush() error {
	if w.open {
		panic("dbwire: Flush in an open message")
	}
	if w.err != nil {
		return w.err
	}
	if len(w.buf) == 0 {
		return nil
	}
	_, err := w.w.Write(w.buf)
	w.buf = w.buf[:0]
	return w.keep(err)
}

func (w *Writer) keep(err error) error {
	if err != nil {
		w.err = err
	}
	return err
}

// Begin starts a message of kind, which is a request or KindOK. It panics if a message is open.
func (w *Writer) Begin(kind Kind) {
	if w.open {
		panic("dbwire: Begin in an open message")
	}
	if !kind.known() || kind == KindError {
		panic("dbwire: Begin of " + kind.String())
	}
	w.kind, w.open = kind, true
	w.header()
}

// room is the most bytes that the buffer may have.
func (w *Writer) room() int { return HeaderSize + max(w.limit, w.hold) }

// header starts a frame after what is held.
func (w *Writer) header() {
	w.start = len(w.buf)
	w.buf = append(w.buf, make([]byte, HeaderSize)...)
}

// Write appends p to the body of the open message, and writes a frame each time the limit is reached. It panics if no
// message is open.
func (w *Writer) Write(p []byte) (int, error) {
	if !w.open {
		panic("dbwire: Write outside a message")
	}
	if w.err != nil {
		return 0, w.err
	}
	total := len(p)
	for len(p) > 0 {
		n := min(w.limit-(len(w.buf)-w.start-HeaderSize), len(p))
		if w.start > 0 && len(w.buf)+n > w.room() {
			if err := w.writeHeld(w.start); err != nil {
				return total - len(p), err
			}
			w.start = 0
		}
		w.buf = append(w.buf, p[:n]...)
		p = p[n:]
		if len(w.buf)-w.start-HeaderSize == w.limit {
			if err := w.flush(false); err != nil {
				return total - len(p), err
			}
		}
	}
	return total, nil
}

// writeHeld writes the first n bytes of the buffer, which are whole messages, and moves the rest to the front.
func (w *Writer) writeHeld(n int) error {
	if _, err := w.w.Write(w.buf[:n]); err != nil {
		return w.keep(err)
	}
	w.buf = w.buf[:copy(w.buf, w.buf[n:])]
	return nil
}

// End writes the last frame of the message, which has the end flag and may be empty. It panics if no message is open.
func (w *Writer) End() error {
	if !w.open {
		panic("dbwire: End outside a message")
	}
	w.open = false
	if w.err != nil {
		return w.err
	}
	return w.flush(true)
}

// Pulse writes the frame that is being filled at once, without the end flag, together with the messages that are held:
// an empty frame in a message of kind OK is a pulse, which a long request writes before its result, and a Reader skips
// it. The message stays open. It panics if no message is open.
func (w *Writer) Pulse() error {
	if !w.open {
		panic("dbwire: Pulse outside a message")
	}
	if w.err != nil {
		return w.err
	}
	return w.flush(false)
}

// Fail drops what is buffered of the open message, if there is one, and writes one Error frame: it ends a reply that
// has begun, and is a reply of its own when no message is open. Frames that are written stay, and so do the messages
// that are held. The text is cut to MaxErrorText bytes.
func (w *Writer) Fail(code Code, msg string) error {
	if w.open {
		w.buf = w.buf[:w.start]
		w.open = false
	}
	if w.err != nil {
		return w.err
	}
	msg = cutText(msg, MaxErrorText)
	if len(w.buf) > 0 && len(w.buf)+HeaderSize+4+len(msg) > w.room() {
		if err := w.writeHeld(len(w.buf)); err != nil {
			return err
		}
	}
	w.header()
	w.buf = binary.BigEndian.AppendUint16(w.buf, uint16(code))
	w.buf = binary.BigEndian.AppendUint16(w.buf, uint16(len(msg)))
	w.buf = append(w.buf, msg...)
	w.kind = KindError
	return w.flush(true)
}

// flush completes the frame at start, and writes the buffer, unless the frame is the last of its message and the buffer
// is to be held.
func (w *Writer) flush(last bool) error {
	flags := byte(0)
	if last {
		flags = flagEnd
	}
	h := w.buf[w.start:]
	h[0], h[1], h[2], h[3] = byte(w.kind), flags, 0, 0
	binary.BigEndian.PutUint32(h[4:], uint32(len(h)-HeaderSize)) // at most MaxFrame
	if last && len(w.buf) < w.hold {
		return nil
	}
	_, err := w.w.Write(w.buf)
	w.buf = w.buf[:0]
	if !last {
		w.header()
	}
	return w.keep(err)
}

// cutText returns s as valid UTF-8, cut to at most n bytes at a character boundary.
func cutText(s string, n int) string {
	s = strings.ToValidUTF8(s, "\ufffd")
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// Reader reads messages from a stream of frames. It checks every header, and keeps its first error: after it, the
// stream is not read any more. It buffers what it reads, so the stream is for it alone. In a message of kind OK, it skips
// the pulses, which are the empty frames without the end flag that a daemon writes while a long request goes on.
type Reader struct {
	br     *bufio.Reader
	limit  int
	kind   Kind  // of the message that is open
	left   int   // the bytes of the payload of the current frame that are not read
	last   bool  // the current frame has the end flag
	msg    bool  // a message is open: its last frame is not all read
	final  error // what Read returns at the end of the message: io.EOF, or the error that the message ends with
	remote *RemoteError
	err    error
	hdr    [HeaderSize]byte
}

func NewReader(r io.Reader, maxFrame int) *Reader {
	return &Reader{br: bufio.NewReaderSize(r, ReadBuffer), limit: clampFrame(maxFrame), final: io.EOF}
}

// SetMaxFrame changes the limit of a frame, from the next frame header on.
func (r *Reader) SetMaxFrame(n int) { r.limit = clampFrame(n) }

func (r *Reader) fatal(err error) error {
	if r.err == nil {
		r.err = err
	}
	r.msg = false
	return r.err
}

// readHeader reads and checks the header of a frame. A stream that ends before the header of the first frame of a
// message is io.EOF, and one that ends inside a message is io.ErrUnexpectedEOF.
func (r *Reader) readHeader(first bool) (kind Kind, last bool, n int, err error) {
	if _, err := io.ReadFull(r.br, r.hdr[:]); err != nil {
		if errors.Is(err, io.EOF) && !first {
			err = io.ErrUnexpectedEOF
		}
		return 0, false, 0, err
	}
	kind, flags := Kind(r.hdr[0]), r.hdr[1]
	last = flags&flagEnd != 0
	length := binary.BigEndian.Uint32(r.hdr[4:])
	switch {
	case r.hdr[2] != 0 || r.hdr[3] != 0:
		err = protocolf("a frame of kind %v with reserved bytes that are not zero", kind)
	case flags&^flagEnd != 0:
		err = protocolf("a frame of kind %v with the flags 0x%02x", kind, flags)
	case !kind.known():
		err = protocolf("a frame of an unknown kind 0x%02x", uint8(kind))
	case kind == KindError && !last:
		err = protocolf("an Error frame without the end flag")
	case kind == KindError && (length < 4 || length > 4+MaxErrorText):
		err = protocolf("an Error frame of %d bytes", length)
	case kind != KindError && int64(length) > int64(r.limit):
		err = limitf("a frame of %d bytes, over the limit of %d", length, r.limit)
	case length == 0 && !last && kind != KindOK: // an empty OK frame without the end flag is a pulse
		err = protocolf("an empty %v frame without the end flag", kind)
	}
	if err != nil {
		return 0, false, 0, err
	}
	return kind, last, int(length), nil
}

// readError reads the payload of an Error frame, which has n bytes.
func (r *Reader) readError(n int) (*RemoteError, error) {
	var b [4 + MaxErrorText]byte
	if _, err := io.ReadFull(r.br, b[:n]); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return nil, err
	}
	if m := int(binary.BigEndian.Uint16(b[2:])); m != n-4 {
		return nil, protocolf("an Error frame of %d bytes with a text of %d bytes", n, m)
	}
	return &RemoteError{
		Code: Code(binary.BigEndian.Uint16(b[:])),
		Msg:  strings.ToValidUTF8(string(b[4:n]), "\ufffd"),
	}, nil
}

// Next returns the kind of the next message, after it discards what is left of the one before. It is io.EOF if the
// stream ends between messages. An Error frame is a message of its own: the error is for Reply or Read.
func (r *Reader) Next() (Kind, error) {
	if r.err != nil {
		return 0, r.err
	}
	if err := r.skip(); err != nil {
		return 0, err
	}
	kind, last, n, err := r.readHeader(true)
	if err != nil {
		return 0, r.fatal(err)
	}
	r.kind, r.final, r.remote = kind, io.EOF, nil
	if kind == KindError {
		re, err := r.readError(n)
		if err != nil {
			return 0, r.fatal(err)
		}
		r.msg, r.left, r.last, r.final, r.remote = false, 0, true, re, re
		return kind, nil
	}
	r.msg, r.left, r.last = true, n, last
	return kind, nil
}

// Reply is Next for a client: it returns nil for a message of kind OK, whose body Read gives, and the *RemoteError of
// a message of kind Error. A stream that ends before the reply is io.ErrUnexpectedEOF.
func (r *Reader) Reply() error {
	kind, err := r.Next()
	switch {
	case errors.Is(err, io.EOF):
		return io.ErrUnexpectedEOF
	case err != nil:
		return err
	case kind == KindOK:
		return nil
	case kind == KindError:
		return r.remote
	}
	return r.fatal(protocolf("a message of kind %v where a reply is due", kind))
}

// continuation reads the header of the next frame of the open message. An Error frame in a message of kind OK ends it.
func (r *Reader) continuation() error {
	kind, last, n, err := r.readHeader(false)
	if err != nil {
		return r.fatal(err)
	}
	switch {
	case kind == r.kind:
		r.left, r.last = n, last
	case kind == KindError && r.kind == KindOK:
		re, err := r.readError(n)
		if err != nil {
			return r.fatal(err)
		}
		r.msg, r.left, r.last, r.final, r.remote = false, 0, true, re, re
	default:
		return r.fatal(protocolf("a frame of kind %v in a message of kind %v", kind, r.kind))
	}
	return nil
}

// Read reads the body of the open message across its frames. It returns io.EOF after the last frame, and the
// *RemoteError of an Error frame that ends an OK message.
func (r *Reader) Read(p []byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	if len(p) == 0 {
		return 0, nil
	}
	for r.left == 0 {
		if !r.msg {
			return 0, r.final
		}
		if r.last {
			r.msg = false
			return 0, r.final
		}
		if err := r.continuation(); err != nil {
			return 0, err
		}
	}
	n, err := r.br.Read(p[:min(len(p), r.left)])
	r.left -= n
	if err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return n, r.fatal(err)
	}
	return n, nil
}

// skip reads to the end of the open message, if there is one.
func (r *Reader) skip() error {
	for r.msg {
		if r.left > 0 {
			n, err := r.br.Discard(r.left)
			r.left -= n
			if err != nil {
				if errors.Is(err, io.EOF) {
					err = io.ErrUnexpectedEOF
				}
				return r.fatal(err)
			}
		}
		if r.last {
			r.msg = false
		} else if err := r.continuation(); err != nil {
			return err
		}
	}
	return nil
}

// Discard reads to the end of the open message. It returns the *RemoteError that the message ends with, if it does.
func (r *Reader) Discard() error {
	if r.err != nil && !errors.Is(r.err, io.EOF) {
		return r.err
	}
	if err := r.skip(); err != nil {
		return err
	}
	if r.remote != nil {
		return r.remote
	}
	return nil
}
