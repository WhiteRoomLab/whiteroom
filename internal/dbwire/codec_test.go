package dbwire

import (
	"bytes"
	"errors"
	"io"
	"runtime"
	"strings"
	"testing"
	"testing/iotest"
)

// allocated returns the bytes that f allocates.
func allocated(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

func TestCodec(t *testing.T) {
	var buf bytes.Buffer
	e := &Enc{W: &buf}
	e.U8(0xAB)
	e.U16(0xABCD)
	e.U32(0xDEADBEEF)
	e.U64(0x0102030405060708)
	e.I64(-2)
	e.Bool(true)
	e.Bool(false)
	e.Bytes([]byte("hello"))
	e.Bytes(nil)
	e.Raw([]byte{9, 8})
	e.Hash([32]byte{1, 31: 2})
	if e.Err != nil {
		t.Fatal(e.Err)
	}
	want := []byte{
		0xAB, 0xAB, 0xCD, 0xDE, 0xAD, 0xBE, 0xEF, 1, 2, 3, 4, 5, 6, 7, 8,
		0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFE, 1, 0,
		0, 0, 0, 5, 'h', 'e', 'l', 'l', 'o', 0, 0, 0, 0, 9, 8,
		1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 2,
	}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Fatalf("encoded %x, want %x", buf.Bytes(), want)
	}

	d := &Dec{R: bytes.NewReader(buf.Bytes())}
	if got := d.U8(); got != 0xAB {
		t.Errorf("U8 = %#x", got)
	}
	if got := d.U16(); got != 0xABCD {
		t.Errorf("U16 = %#x", got)
	}
	if got := d.U32(); got != 0xDEADBEEF {
		t.Errorf("U32 = %#x", got)
	}
	if got := d.U64(); got != 0x0102030405060708 {
		t.Errorf("U64 = %#x", got)
	}
	if got := d.I64(); got != -2 {
		t.Errorf("I64 = %d", got)
	}
	if !d.Bool() || d.Bool() {
		t.Error("Bool")
	}
	if got := d.Bytes(5); string(got) != "hello" {
		t.Errorf("Bytes = %q", got)
	}
	if got := d.Bytes(0); got == nil || len(got) != 0 {
		t.Errorf("Bytes of none = %#v, want a slice that is not nil", got)
	}
	var raw [2]byte
	d.Raw(raw[:])
	if raw != [2]byte{9, 8} {
		t.Errorf("Raw = %v", raw)
	}
	if got := d.Hash(); got != [32]byte{1, 31: 2} {
		t.Errorf("Hash = %x", got)
	}
	if err := d.End(); err != nil {
		t.Fatalf("End = %v", err)
	}
}

func TestDecErrors(t *testing.T) {
	t.Run("a boolean", func(t *testing.T) {
		d := &Dec{R: strings.NewReader("\x02")}
		if d.Bool() || !errors.Is(d.Err, ErrProtocol) {
			t.Fatalf("Bool of 2 = %v", d.Err)
		}
	})
	t.Run("over the max", func(t *testing.T) {
		d := &Dec{R: strings.NewReader("\x00\x00\x00\x06" + secret)}
		if got := d.Bytes(5); got != nil || !errors.Is(d.Err, ErrProtocol) || !errors.Is(d.Err, ErrLimit) {
			t.Fatalf("Bytes = %q, %v", got, d.Err)
		}
		if msg := d.Err.Error(); !strings.Contains(msg, "6") || !strings.Contains(msg, "5") || strings.Contains(msg, "SECRET") {
			t.Fatalf("the error is %q: it names the two numbers and no payload", msg)
		}
	})
	t.Run("at the max", func(t *testing.T) {
		d := &Dec{R: strings.NewReader("\x00\x00\x00\x05hello")}
		if got := d.Bytes(5); string(got) != "hello" || d.Err != nil {
			t.Fatalf("Bytes = %q, %v", got, d.Err)
		}
	})
	t.Run("ends inside a value", func(t *testing.T) {
		for _, c := range []struct {
			name, in string
			f        func(d *Dec)
		}{
			{"U8", "", func(d *Dec) { d.U8() }},
			{"Bool", "", func(d *Dec) { d.Bool() }},
			{"U16", "\x00", func(d *Dec) { d.U16() }},
			{"U32", "\x00\x00\x00", func(d *Dec) { d.U32() }},
			{"U64", "\x00\x00\x00\x00\x00\x00\x00", func(d *Dec) { d.U64() }},
			{"Hash", strings.Repeat("h", 31), func(d *Dec) { d.Hash() }},
			{"Raw", "x", func(d *Dec) { d.Raw(make([]byte, 2)) }},
			{"Bytes without its length", "\x00\x00", func(d *Dec) { d.Bytes(10) }},
			{"Bytes that stops", "\x00\x00\x00\x05hel", func(d *Dec) { d.Bytes(10) }},
			{"Bytes of a length of 2^31", "\x80\x00\x00\x00", func(d *Dec) { d.Bytes(1 << 32) }},
		} {
			d := &Dec{R: strings.NewReader(c.in)}
			if c.f(d); !errors.Is(d.Err, io.ErrUnexpectedEOF) {
				t.Errorf("%s: %v, want io.ErrUnexpectedEOF", c.name, d.Err)
			}
		}
	})
	t.Run("sticky", func(t *testing.T) {
		d := &Dec{R: strings.NewReader("\x01\x02\x03")}
		d.U32()
		first := d.Err
		if d.U8(); !errors.Is(d.Err, first) {
			t.Fatalf("the error changed: %v", d.Err)
		}
		if d.U64() != 0 || d.Bytes(5) != nil || d.Bool() || d.Hash() != [32]byte{} {
			t.Fatal("a method read after an error")
		}
		if err := d.End(); !errors.Is(err, first) {
			t.Fatalf("End = %v, want the first error", err)
		}
	})
	t.Run("an error of the reader stays", func(t *testing.T) {
		boom := errors.New("boom")
		d := &Dec{R: io.MultiReader(strings.NewReader("\x01"), iotest.ErrReader(boom))}
		d.U8()
		if d.U16(); !errors.Is(d.Err, boom) {
			t.Fatalf("%v", d.Err)
		}
	})
	t.Run("End", func(t *testing.T) {
		d := &Dec{R: strings.NewReader("")}
		if err := d.End(); err != nil {
			t.Fatalf("End of nothing = %v", err)
		}
		d = &Dec{R: strings.NewReader("x")}
		if err := d.End(); !errors.Is(err, ErrProtocol) {
			t.Fatalf("End with a byte left = %v", err)
		}
		boom := errors.New("boom")
		d = &Dec{R: iotest.ErrReader(boom)}
		if err := d.End(); !errors.Is(err, boom) {
			t.Fatalf("End of a broken stream = %v", err)
		}
	})
}

func TestBytesAllocation(t *testing.T) {
	claim := func(n uint32) []byte { return []byte{byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)} }

	t.Run("a length of 2^31 and no data", func(t *testing.T) {
		d := &Dec{R: bytes.NewReader(claim(1 << 31))}
		var got []byte
		n := allocated(func() { got = d.Bytes(1 << 32) })
		if got != nil || !errors.Is(d.Err, io.ErrUnexpectedEOF) {
			t.Fatalf("Bytes = %d bytes, %v", len(got), d.Err)
		}
		if n >= 1<<17 {
			t.Fatalf("allocated %d bytes for a claim and no data", n)
		}
	})
	t.Run("a length of 1 GiB, and some data", func(t *testing.T) {
		const arrived = 300<<10 + 17
		src := io.MultiReader(bytes.NewReader(claim(1<<30)), bytes.NewReader(make([]byte, arrived)))
		d := &Dec{R: src}
		n := allocated(func() { d.Bytes(1 << 32) })
		if !errors.Is(d.Err, io.ErrUnexpectedEOF) {
			t.Fatalf("Err = %v", d.Err)
		}
		if n > arrived+readStep+1<<12 {
			t.Fatalf("allocated %d bytes for %d that arrived", n, arrived)
		}
	})
	t.Run("a value of 5 MiB", func(t *testing.T) {
		want := testBytes(5 << 20)
		d := &Dec{R: io.MultiReader(bytes.NewReader(claim(uint32(len(want)))), bytes.NewReader(want))}
		got := d.Bytes(1 << 30)
		if d.Err != nil || !bytes.Equal(got, want) || cap(got) != len(want) {
			t.Fatalf("Bytes of %d bytes, %v", len(got), d.Err)
		}
	})
	t.Run("a value of 64 KiB and one byte over", func(t *testing.T) {
		for _, n := range []int{readStep - 1, readStep, readStep + 1, 2 * readStep, 2*readStep + 1} {
			want := testBytes(n)
			d := &Dec{R: io.MultiReader(bytes.NewReader(claim(uint32(n))), iotest.OneByteReader(bytes.NewReader(want)))}
			if got := d.Bytes(1 << 20); d.Err != nil || !bytes.Equal(got, want) {
				t.Fatalf("Bytes of %d bytes: %v", n, d.Err)
			}
		}
	})
}

func TestEncErrors(t *testing.T) {
	e := &Enc{W: &failWriter{n: 5}}
	e.U32(1)
	if e.Err != nil {
		t.Fatalf("Err = %v", e.Err)
	}
	e.U32(2)
	if !errors.Is(e.Err, errDisk) {
		t.Fatalf("Err = %v", e.Err)
	}
	log := &frameLog{}
	e.W = log
	e.U8(1)
	e.Bytes([]byte("x"))
	e.Raw([]byte("x"))
	if len(log.frames) != 0 || !errors.Is(e.Err, errDisk) {
		t.Fatalf("an Enc with an error wrote %d times, Err = %v", len(log.frames), e.Err)
	}

	e = &Enc{W: log}
	e.size(-1)
	e.U8(1)
	if e.Err == nil || len(log.frames) != 0 {
		t.Fatalf("a negative size: %v, %d writes", e.Err, len(log.frames))
	}
	e = &Enc{W: log}
	e.str16(strings.Repeat("x", 1<<16))
	if e.Err == nil || len(log.frames) != 0 {
		t.Fatalf("a string of 65536 bytes: %v, %d writes", e.Err, len(log.frames))
	}
}

func TestStream(t *testing.T) {
	d := &Dec{R: strings.NewReader("abcdefgh")}
	got, err := io.ReadAll(d.Stream(5))
	if err != nil || string(got) != "abcde" || d.Err != nil {
		t.Fatalf("read %q, %v, %v", got, err, d.Err)
	}
	if n := d.U16(); n != 'f'<<8|'g' || d.Err != nil {
		t.Fatalf("what follows: %#x, %v", n, d.Err)
	}

	d = &Dec{R: strings.NewReader("abc")}
	got, err = io.ReadAll(d.Stream(5))
	if !errors.Is(err, io.ErrUnexpectedEOF) || string(got) != "abc" || !errors.Is(d.Err, io.ErrUnexpectedEOF) {
		t.Fatalf("a body that is too short: read %q, %v, %v", got, err, d.Err)
	}

	d = &Dec{R: strings.NewReader("abc")}
	if got, err := io.ReadAll(d.Stream(0)); err != nil || len(got) != 0 {
		t.Fatalf("a stream of nothing: %q, %v", got, err)
	}

	boom := errors.New("boom")
	d = &Dec{R: io.MultiReader(strings.NewReader("ab"), iotest.ErrReader(boom))}
	if _, err := io.ReadAll(d.Stream(5)); !errors.Is(err, boom) || !errors.Is(d.Err, boom) {
		t.Fatalf("an error of the reader: %v, %v", err, d.Err)
	}

	d = &Dec{R: strings.NewReader("abc"), Err: boom}
	if _, err := d.Stream(1).Read(make([]byte, 1)); !errors.Is(err, boom) {
		t.Fatalf("after an error: %v", err)
	}
}
