package dbwire

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
	"testing/iotest"
)

// secret is in the payload of the frames that the tests of refusals send: no error text may hold it.
const secret = "SECRET-PAYLOAD"

// rawFrame is a frame as it is on the wire, whatever it holds.
func rawFrame(kind, flags byte, payload string) []byte {
	return slices.Concat([]byte{kind, flags, 0, 0}, binary.BigEndian.AppendUint32(nil, uint32(len(payload))), []byte(payload))
}

// errorFrame is an Error frame with the end flag.
func errorFrame(code uint16, msg string) []byte {
	p := binary.BigEndian.AppendUint16(nil, code)
	p = binary.BigEndian.AppendUint16(p, uint16(len(msg)))
	return rawFrame(byte(KindError), flagEnd, string(p)+msg)
}

// testBytes returns n bytes that differ from place to place.
func testBytes(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*31 + i>>8)
	}
	return b
}

// frameLog records each Write call, which is one frame.
type frameLog struct{ frames [][]byte }

func (l *frameLog) Write(p []byte) (int, error) {
	l.frames = append(l.frames, bytes.Clone(p))
	return len(p), nil
}

func (l *frameLog) wire() []byte { return slices.Concat(l.frames...) }

// failWriter fails after n bytes.
type failWriter struct{ n int }

var errDisk = errors.New("disk is gone")

func (w *failWriter) Write(p []byte) (int, error) {
	if w.n < len(p) {
		n := w.n
		w.n = 0
		return n, errDisk
	}
	w.n -= len(p)
	return len(p), nil
}

// message writes body as a message of kind with a Writer of the limit.
func message(t testing.TB, limit int, kind Kind, body []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := NewWriter(&buf, limit)
	w.Begin(kind)
	if _, err := w.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := w.End(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// drain reads every message of stream, and returns their bodies and the first error, which is nil at a clean end.
func drain(stream []byte, limit int) (*Reader, [][]byte, error) {
	r := NewReader(bytes.NewReader(stream), limit)
	var bodies [][]byte
	for {
		if _, err := r.Next(); err != nil {
			if errors.Is(err, io.EOF) {
				err = nil
			}
			return r, bodies, err
		}
		b, err := io.ReadAll(r)
		if err != nil {
			return r, bodies, err
		}
		bodies = append(bodies, b)
	}
}

func mustPanic(t *testing.T, name string, f func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Errorf("%s did not panic", name)
		}
	}()
	f()
}

func TestFrames(t *testing.T) {
	for _, limit := range []int{16, 1 << 20} {
		piece := 7
		if limit > 16 {
			piece = 4099
		}
		for _, n := range []int{0, 1, limit - 1, limit, limit + 1, 3*limit + 5} {
			t.Run(fmt.Sprintf("limit %d body %d", limit, n), func(t *testing.T) {
				body := testBytes(n)
				log := &frameLog{}
				w := NewWriter(log, limit)
				w.Begin(KindScan)
				for p := body; len(p) > 0; p = p[min(piece, len(p)):] {
					if _, err := w.Write(p[:min(piece, len(p))]); err != nil {
						t.Fatal(err)
					}
				}
				if err := w.End(); err != nil {
					t.Fatal(err)
				}
				if want := n/limit + 1; len(log.frames) != want {
					t.Fatalf("%d frames, want %d", len(log.frames), want)
				}
				var joined []byte
				for i, f := range log.frames {
					last := i == len(log.frames)-1
					length := int(binary.BigEndian.Uint32(f[4:]))
					switch {
					case Kind(f[0]) != KindScan, f[2] != 0, f[3] != 0:
						t.Fatalf("frame %d: header %x", i, f[:HeaderSize])
					case (f[1] == flagEnd) != last || f[1]&^flagEnd != 0:
						t.Fatalf("frame %d of %d: flags %#x", i, len(log.frames), f[1])
					case length != len(f)-HeaderSize || length > limit:
						t.Fatalf("frame %d: a length of %d in %d bytes, limit %d", i, length, len(f), limit)
					case !last && length != limit:
						t.Fatalf("frame %d is not full: %d bytes", i, length)
					}
					joined = append(joined, f[HeaderSize:]...)
				}
				if !bytes.Equal(joined, body) {
					t.Fatal("the payloads do not make the body")
				}

				readers := map[string]io.Reader{"plain": bytes.NewReader(log.wire())}
				if limit == 16 {
					readers["one byte at a time"] = iotest.OneByteReader(bytes.NewReader(log.wire()))
				}
				for name, src := range readers {
					r := NewReader(src, limit)
					kind, err := r.Next()
					if err != nil || kind != KindScan {
						t.Fatalf("%s: Next = %v, %v", name, kind, err)
					}
					got, err := io.ReadAll(r)
					if err != nil || !bytes.Equal(got, body) {
						t.Fatalf("%s: read %d bytes, %v; want the body of %d", name, len(got), err, n)
					}
					if _, err := r.Next(); !errors.Is(err, io.EOF) {
						t.Fatalf("%s: Next after the message = %v, want io.EOF", name, err)
					}
				}
				r := NewReader(bytes.NewReader(log.wire()), limit)
				if _, err := r.Next(); err != nil {
					t.Fatal(err)
				}
				if got, err := io.ReadAll(iotest.OneByteReader(r)); err != nil || !bytes.Equal(got, body) {
					t.Fatalf("read one byte at a time: %d bytes, %v", len(got), err)
				}
			})
		}
	}
}

func TestFrameLayout(t *testing.T) {
	for _, c := range []struct {
		name  string
		limit int
		kind  Kind
		body  string
		want  []byte
	}{
		{"empty reply", 16, KindOK, "", []byte{0x80, 1, 0, 0, 0, 0, 0, 0}},
		{"short", 4, KindGet, "ab", []byte{0x04, 1, 0, 0, 0, 0, 0, 2, 'a', 'b'}},
		{"full frame, then an empty one", 4, KindGet, "abcd",
			[]byte{0x04, 0, 0, 0, 0, 0, 0, 4, 'a', 'b', 'c', 'd', 0x04, 1, 0, 0, 0, 0, 0, 0}},
		{"two frames", 4, KindCommit, "abcde",
			[]byte{0x0D, 0, 0, 0, 0, 0, 0, 4, 'a', 'b', 'c', 'd', 0x0D, 1, 0, 0, 0, 0, 0, 1, 'e'}},
		{"length of 258", 1000, KindOK, strings.Repeat("x", 258),
			slices.Concat([]byte{0x80, 1, 0, 0, 0, 0, 1, 2}, bytes.Repeat([]byte("x"), 258))},
	} {
		if got := message(t, c.limit, c.kind, []byte(c.body)); !bytes.Equal(got, c.want) {
			t.Errorf("%s: %x, want %x", c.name, got, c.want)
		}
	}

	var buf bytes.Buffer
	w := NewWriter(&buf, 0)
	w.Begin(KindGet)
	_, _ = w.Write([]byte("ab"))
	if err := w.End(); err != nil {
		t.Fatal(err)
	}
	if want := []byte{4, 0, 0, 0, 0, 0, 0, 1, 'a', 4, 0, 0, 0, 0, 0, 0, 1, 'b', 4, 1, 0, 0, 0, 0, 0, 0}; !bytes.Equal(buf.Bytes(), want) {
		t.Errorf("a limit of 0 is clamped to 1: %x, want %x", buf.Bytes(), want)
	}
}

func TestNextSkipsTheRest(t *testing.T) {
	stream := slices.Concat(
		message(t, 4, KindGet, []byte("0123456789")),
		message(t, 4, KindScan, nil),
		message(t, 4, KindObject, []byte("tail")),
	)
	r := NewReader(bytes.NewReader(stream), 4)
	if k, err := r.Next(); err != nil || k != KindGet {
		t.Fatalf("Next = %v, %v", k, err)
	}
	p := make([]byte, 5)
	if _, err := io.ReadFull(r, p); err != nil || string(p) != "01234" {
		t.Fatalf("read %q, %v", p, err)
	}
	if k, err := r.Next(); err != nil || k != KindScan {
		t.Fatalf("Next after a partial read = %v, %v", k, err)
	}
	if k, err := r.Next(); err != nil || k != KindObject {
		t.Fatalf("Next after an unread message = %v, %v", k, err)
	}
	if err := r.Discard(); err != nil {
		t.Fatal(err)
	}
	if n, err := r.Read(p); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("Read after Discard = %d, %v", n, err)
	}
	if _, err := r.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("Next at the end = %v", err)
	}
}

func TestReaderRefuses(t *testing.T) {
	big := strings.Repeat("S", 17)
	for _, c := range []struct {
		name   string
		stream []byte
		limit  int
		want   error // nil: the stream is read to its clean end
		limitE bool  // the error is also ErrLimit
	}{
		{"reserved byte 2", []byte{4, 1, 1, 0, 0, 0, 0, 0}, 16, ErrProtocol, false},
		{"reserved byte 3", []byte{4, 1, 0, 1, 0, 0, 0, 0}, 16, ErrProtocol, false},
		{"reserved bytes with a payload", slices.Concat([]byte{4, 1, 0xFF, 0xFF, 0, 0, 0, 14}, []byte(secret)), 16, ErrProtocol, false},
		{"flag 0x02", rawFrame(4, 0x02, secret), 16, ErrProtocol, false},
		{"flags 0x03", rawFrame(4, 0x03, secret), 16, ErrProtocol, false},
		{"flag 0x80", rawFrame(4, 0x80, ""), 16, ErrProtocol, false},
		{"length over the limit", rawFrame(4, 1, big), 16, ErrProtocol, true},
		{"length of 2^32-1", []byte{4, 1, 0, 0, 0xFF, 0xFF, 0xFF, 0xFF}, 16, ErrProtocol, true},
		{"kind 0x00", rawFrame(0x00, 1, secret), 16, ErrProtocol, false},
		{"kind 0x0E", rawFrame(0x0E, 1, secret), 16, ErrProtocol, false},
		{"kind 0x7F", rawFrame(0x7F, 1, secret), 16, ErrProtocol, false},
		{"kind 0x82", rawFrame(0x82, 1, secret), 16, ErrProtocol, false},
		{"kind 0xFF", rawFrame(0xFF, 1, secret), 16, ErrProtocol, false},
		{"another kind in the middle", slices.Concat(rawFrame(4, 0, secret), rawFrame(5, 1, "x")), 16, ErrProtocol, false},
		{"another kind: OK in a request", slices.Concat(rawFrame(4, 0, secret), rawFrame(0x80, 1, "x")), 16, ErrProtocol, false},
		{"another kind: a request in an OK", slices.Concat(rawFrame(0x80, 0, secret), rawFrame(4, 1, "x")), 16, ErrProtocol, false},
		{"Error in a request", slices.Concat(rawFrame(0x0D, 0, secret), errorFrame(1, "x")), 16, ErrProtocol, false},
		{"Error without the end flag", rawFrame(0x81, 0, "\x00\x01\x00\x00"), 16, ErrProtocol, false},
		{"Error without the end flag after OK", slices.Concat(rawFrame(0x80, 0, secret), rawFrame(0x81, 0, "\x00\x01\x00\x00")), 16, ErrProtocol, false},
		{"Error of 3 bytes", rawFrame(0x81, 1, "\x00\x01\x00"), 16, ErrProtocol, false},
		{"Error of 0 bytes", rawFrame(0x81, 1, ""), 16, ErrProtocol, false},
		{"Error of 1029 bytes", rawFrame(0x81, 1, "\x00\x01\x04\x01"+strings.Repeat("S", 1025)), 16, ErrProtocol, false},
		{"Error with a text that is longer than it says", rawFrame(0x81, 1, "\x00\x01\x00\x05"+secret), 16, ErrProtocol, false},
		{"Error with a text that is shorter than it says", rawFrame(0x81, 1, "\x00\x01\x00\x20"+secret), 16, ErrProtocol, false},
		{"empty frame without the end flag", rawFrame(4, 0, ""), 16, ErrProtocol, false},
		{"empty frame without the end flag, later", slices.Concat(rawFrame(4, 0, "a"), rawFrame(4, 0, "")), 16, ErrProtocol, false},

		{"nothing", nil, 16, nil, false},
		{"one byte", []byte{4}, 16, io.ErrUnexpectedEOF, false},
		{"seven bytes", []byte{4, 1, 0, 0, 0, 0, 0}, 16, io.ErrUnexpectedEOF, false},
		{"header only, for a payload", []byte{4, 1, 0, 0, 0, 0, 0, 6}, 16, io.ErrUnexpectedEOF, false},
		{"a payload that stops", slices.Concat([]byte{4, 1, 0, 0, 0, 0, 0, 6}, []byte("abc")), 16, io.ErrUnexpectedEOF, false},
		{"no end flag, then nothing", rawFrame(4, 0, "abc"), 16, io.ErrUnexpectedEOF, false},
		{"no end flag, then a part of a header", slices.Concat(rawFrame(4, 0, "abc"), []byte{4, 1, 0}), 16, io.ErrUnexpectedEOF, false},
		{"an Error frame that stops", slices.Concat([]byte{0x81, 1, 0, 0, 0, 0, 0, 6}, []byte{0, 1, 0}), 16, io.ErrUnexpectedEOF, false},
		{"an Error frame that stops after OK", slices.Concat(rawFrame(0x80, 0, "a"), []byte{0x81, 1, 0, 0, 0, 0, 0, 6, 0}), 16, io.ErrUnexpectedEOF, false},
		{"a message and then nothing", rawFrame(4, 1, "abc"), 16, nil, false},
		{"a message and then a part of a header", slices.Concat(rawFrame(4, 1, "abc"), []byte{4, 1}), 16, io.ErrUnexpectedEOF, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, _, err := drain(c.stream, c.limit)
			if c.want == nil {
				if err != nil {
					t.Fatalf("error %v", err)
				}
				return
			}
			if !errors.Is(err, c.want) {
				t.Fatalf("error %v, want %v", err, c.want)
			}
			if errors.Is(err, ErrLimit) != c.limitE {
				t.Fatalf("error %v: ErrLimit is %v", err, !c.limitE)
			}
			if strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "SSSS") {
				t.Fatalf("the error holds payload bytes: %q", err)
			}
			if _, err2 := r.Next(); !errors.Is(err2, err) {
				t.Fatalf("Next after the error = %v, want %v again", err2, err)
			}
			if _, err2 := r.Read(make([]byte, 1)); !errors.Is(err2, err) {
				t.Fatalf("Read after the error = %v, want %v again", err2, err)
			}
		})
	}
}

func TestReaderLimit(t *testing.T) {
	stream := slices.Concat(message(t, 32, KindGet, testBytes(20)), message(t, 32, KindGet, testBytes(20)))
	r := NewReader(bytes.NewReader(stream), 16)
	r.SetMaxFrame(32)
	for i := range 2 {
		if _, err := r.Next(); err != nil {
			t.Fatalf("message %d: %v", i, err)
		}
		if got, err := io.ReadAll(r); err != nil || len(got) != 20 {
			t.Fatalf("message %d: %d bytes, %v", i, len(got), err)
		}
	}

	r = NewReader(bytes.NewReader(rawFrame(4, 1, "ab")), 0)
	if _, err := r.Next(); !errors.Is(err, ErrLimit) {
		t.Fatalf("a frame of 2 bytes under a limit of 0, which is 1: %v", err)
	}
}

func TestErrorFrame(t *testing.T) {
	t.Run("a reply of its own", func(t *testing.T) {
		var buf bytes.Buffer
		w := NewWriter(&buf, 16)
		if err := w.Fail(CodeSeq, "the sequence number is 5"); err != nil {
			t.Fatal(err)
		}
		msg := "the sequence number is 5"
		want := slices.Concat([]byte{0x81, 1, 0, 0, 0, 0, 0, byte(4 + len(msg)), 0, 7, 0, byte(len(msg))}, []byte(msg))
		if !bytes.Equal(buf.Bytes(), want) {
			t.Fatalf("%x, want %x", buf.Bytes(), want)
		}
		r := NewReader(&buf, 16) // a limit under the size of the frame
		var re *RemoteError
		if err := r.Reply(); !errors.As(err, &re) || re.Code != CodeSeq || re.Msg != msg || err.Error() != msg {
			t.Fatalf("Reply = %v", err)
		}
		if n, err := r.Read(make([]byte, 4)); n != 0 || !errors.As(err, &re) || re.Code != CodeSeq {
			t.Fatalf("Read = %d, %v", n, err)
		}
		if err := r.Discard(); !errors.As(err, &re) {
			t.Fatalf("Discard = %v", err)
		}
		if _, err := r.Next(); !errors.Is(err, io.EOF) {
			t.Fatalf("Next = %v", err)
		}
	})

	t.Run("after frames", func(t *testing.T) {
		var buf bytes.Buffer
		w := NewWriter(&buf, 4)
		w.Begin(KindOK)
		_, _ = w.Write([]byte("abcdefghxy")) // two frames are written, and "xy" is buffered
		if err := w.Fail(CodeFailed, "the engine failed"); err != nil {
			t.Fatal(err)
		}
		w.Begin(KindOK) // the writer is ready for the next reply
		_, _ = w.Write([]byte("next"))
		if err := w.End(); err != nil {
			t.Fatal(err)
		}

		r := NewReader(bytes.NewReader(buf.Bytes()), 4)
		if err := r.Reply(); err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(r)
		var re *RemoteError
		if string(got) != "abcdefgh" || !errors.As(err, &re) || re.Code != CodeFailed || re.Msg != "the engine failed" {
			t.Fatalf("read %q, %v", got, err)
		}
		if _, err := io.ReadAll(r); !errors.As(err, &re) {
			t.Fatalf("the error is not kept: %v", err)
		}
		if err := r.Reply(); err != nil {
			t.Fatalf("the reply after the error: %v", err)
		}
		if got, err := io.ReadAll(r); err != nil || string(got) != "next" {
			t.Fatalf("read %q, %v", got, err)
		}

		// Through a Dec: a value that the Error frame cuts short, and End.
		r = NewReader(bytes.NewReader(buf.Bytes()), 4)
		if err := r.Reply(); err != nil {
			t.Fatal(err)
		}
		d := &Dec{R: r}
		if got := d.U64(); d.Err != nil || !bytes.Equal(binary.BigEndian.AppendUint64(nil, got), []byte("abcdefgh")) {
			t.Fatalf("U64 = %x, %v", got, d.Err)
		}
		if err := d.End(); !errors.As(err, &re) || re.Code != CodeFailed {
			t.Fatalf("End = %v", err)
		}
		r = NewReader(bytes.NewReader(buf.Bytes()), 4)
		if err := r.Reply(); err != nil {
			t.Fatal(err)
		}
		d = &Dec{R: r}
		if d.U64(); d.Err != nil {
			t.Fatal(d.Err)
		}
		if d.U8(); !errors.As(d.Err, &re) {
			t.Fatalf("a value after the Error frame: %v", d.Err)
		}

		// Next after a message that ends in an Error frame, with some of it unread.
		r = NewReader(bytes.NewReader(buf.Bytes()), 4)
		if err := r.Reply(); err != nil {
			t.Fatal(err)
		}
		if k, err := r.Next(); err != nil || k != KindOK {
			t.Fatalf("Next = %v, %v", k, err)
		}
		if got, err := io.ReadAll(r); err != nil || string(got) != "next" {
			t.Fatalf("read %q, %v", got, err)
		}
		if err := r.Discard(); err != nil {
			t.Fatalf("the error of the message before is given again: %v", err)
		}
	})

	t.Run("nothing flushed", func(t *testing.T) {
		var buf bytes.Buffer
		w := NewWriter(&buf, 64)
		w.Begin(KindOK)
		_, _ = w.Write([]byte("dropped"))
		if err := w.Fail(CodeLimit, "too much"); err != nil {
			t.Fatal(err)
		}
		r := NewReader(&buf, 64)
		var re *RemoteError
		if err := r.Reply(); !errors.As(err, &re) || re.Code != CodeLimit {
			t.Fatalf("Reply = %v", err)
		}
	})

	t.Run("text", func(t *testing.T) {
		for _, c := range []struct{ name, msg, want string }{
			{"short", "ok", "ok"},
			{"empty", "", ""},
			{"1024 bytes", strings.Repeat("a", 1024), strings.Repeat("a", 1024)},
			{"1025 bytes", strings.Repeat("a", 1025), strings.Repeat("a", 1024)},
			{"a rune ends at 1024", strings.Repeat("a", 1022) + "é" + "b", strings.Repeat("a", 1022) + "é"},
			{"a rune straddles 1024", strings.Repeat("a", 1023) + "é", strings.Repeat("a", 1023)},
			{"a rune of 3 bytes straddles", strings.Repeat("a", 1022) + "日", strings.Repeat("a", 1022)},
			{"a rune of 4 bytes straddles", strings.Repeat("a", 1021) + "😀", strings.Repeat("a", 1021)},
			{"not UTF-8", "a\xffb", "a\ufffdb"},
		} {
			var buf bytes.Buffer
			w := NewWriter(&buf, 16)
			if err := w.Fail(CodeInternal, c.msg); err != nil {
				t.Fatal(err)
			}
			if text := buf.Bytes()[HeaderSize+4:]; string(text) != c.want {
				t.Errorf("%s: the text on the wire has %d bytes, want %d", c.name, len(text), len(c.want))
			}
			var re *RemoteError
			if err := NewReader(&buf, 16).Reply(); !errors.As(err, &re) || re.Msg != c.want {
				t.Errorf("%s: %v, want the text of %d bytes", c.name, err, len(c.want))
			}
		}
	})

	t.Run("the text of the peer is checked", func(t *testing.T) {
		var re *RemoteError
		if err := NewReader(bytes.NewReader(errorFrame(99, "a\xffb")), 16).Reply(); !errors.As(err, &re) || re.Msg != "a\ufffdb" || re.Code != 99 {
			t.Fatalf("%v", err)
		}
		if got := Code(99).String(); got != "code 99" {
			t.Fatalf("String = %q", got)
		}
	})

	t.Run("Reply", func(t *testing.T) {
		if err := NewReader(bytes.NewReader(nil), 16).Reply(); !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("no reply: %v", err)
		}
		if err := NewReader(bytes.NewReader(message(t, 16, KindGet, nil)), 16).Reply(); !errors.Is(err, ErrProtocol) {
			t.Fatalf("a request where a reply is due: %v", err)
		}
		if err := NewReader(bytes.NewReader(message(t, 16, KindOK, []byte("x"))), 16).Reply(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestWriterMisuse(t *testing.T) {
	w := NewWriter(io.Discard, 16)
	mustPanic(t, "Write outside a message", func() { _, _ = w.Write([]byte("x")) })
	mustPanic(t, "End outside a message", func() { _ = w.End() })
	mustPanic(t, "Begin of Error", func() { w.Begin(KindError) })
	mustPanic(t, "Begin of an unknown kind", func() { w.Begin(0) })
	w.Begin(KindGet)
	mustPanic(t, "Begin in an open message", func() { w.Begin(KindGet) })
	mustPanic(t, "SetMaxFrame in an open message", func() { w.SetMaxFrame(8) })
	if err := w.End(); err != nil {
		t.Fatal(err)
	}
	w.SetMaxFrame(8)
	w.Begin(KindGet)
	_ = w.End()
}

func TestWriterError(t *testing.T) {
	// The frame of 8+4 bytes does not fit, and nothing more is written.
	fw := &failWriter{n: 10}
	w := NewWriter(fw, 4)
	w.Begin(KindOK)
	if n, err := w.Write([]byte("abcdefgh")); !errors.Is(err, errDisk) || n != 4 {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if _, err := w.Write([]byte("x")); !errors.Is(err, errDisk) {
		t.Fatalf("Write again = %v", err)
	}
	if err := w.End(); !errors.Is(err, errDisk) {
		t.Fatalf("End = %v", err)
	}
	if err := w.Fail(CodeFailed, "x"); !errors.Is(err, errDisk) {
		t.Fatalf("Fail = %v", err)
	}

	for _, n := range []int{0, 5, 7} {
		w := NewWriter(&failWriter{n: n}, 16)
		w.Begin(KindOK)
		if err := w.End(); !errors.Is(err, errDisk) {
			t.Fatalf("End with %d bytes of room = %v", n, err)
		}
		w = NewWriter(&failWriter{n: n}, 16)
		if err := w.Fail(CodeFailed, "x"); !errors.Is(err, errDisk) {
			t.Fatalf("Fail with %d bytes of room = %v", n, err)
		}
	}
}

func TestKinds(t *testing.T) {
	var requests, writerOnly []Kind
	for k := range Kind(0xFF) {
		if k.IsRequest() {
			requests = append(requests, k)
		}
		if k.WriterOnly() {
			writerOnly = append(writerOnly, k)
		}
	}
	if len(requests) != 13 || requests[0] != KindHello || requests[12] != KindCommit {
		t.Errorf("requests: %v", requests)
	}
	if want := []Kind{KindPutObject, KindPutOriginal, KindCommit}; !slices.Equal(writerOnly, want) {
		t.Errorf("writer only: %v, want %v", writerOnly, want)
	}
	for k, want := range map[Kind]string{KindHello: "Hello", KindPutOriginal: "PutOriginal", KindOK: "OK", KindError: "Error", 0: "kind 0x00", 0x7F: "kind 0x7f"} {
		if got := k.String(); got != want {
			t.Errorf("String of %#x = %q, want %q", uint8(k), got, want)
		}
	}
	if KindOK.IsRequest() || KindError.IsRequest() || Kind(0).IsRequest() || Kind(0x0E).IsRequest() {
		t.Error("a reply or an unknown kind is a request")
	}
	if RoleReader.String() != "reader" || RoleWriter.String() != "writer" || Role(0).String() != "role 0" {
		t.Error("Role.String")
	}
	for c := CodeProtocol; c <= CodeInternal; c++ {
		if strings.HasPrefix(c.String(), "code ") {
			t.Errorf("code %d has no name", c)
		}
	}
	if CodeProtocol != 1 || CodeInternal != 14 || CodeStopped != 11 || CodeHash != 12 {
		t.Error("the codes are not those of the plan")
	}
}
