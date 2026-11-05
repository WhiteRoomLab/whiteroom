package dmlog

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"testing"
)

// builder makes a log byte by byte as Linux v6.18 writes it (the package comment), with sectors of ss bytes. It fills
// the bytes that the format does not use with 0xff, where Linux writes zeros, so that Parse cannot depend on them.
type builder struct {
	ss      int
	entries uint64
	b       []byte
}

func newBuilder(ss int) *builder {
	return &builder{ss: ss, b: bytes.Repeat([]byte{0xff}, ss)}
}

// entry appends an entry and data after it.
func (b *builder) entry(sector, sectors, flags, dataLen uint64, text string, data []byte) {
	s := bytes.Repeat([]byte{0xff}, b.ss)
	binary.LittleEndian.PutUint64(s[0:], sector)
	binary.LittleEndian.PutUint64(s[8:], sectors)
	binary.LittleEndian.PutUint64(s[16:], flags)
	binary.LittleEndian.PutUint64(s[24:], dataLen)
	copy(s[32:], text)
	b.b = append(append(b.b, s...), data...)
	b.entries++
}

// write appends a write of len(fill) sectors from sector, the sector i of which is filled with the byte fill[i].
func (b *builder) write(sector, flags uint64, fill string) {
	var data []byte
	for _, c := range []byte(fill) {
		data = append(data, bytes.Repeat([]byte{c}, b.ss)...)
	}
	b.entry(sector, uint64(len(fill)), flags, 0, "", data)
}

func (b *builder) mark(text string) {
	b.entry(0, 0, FlagMark, uint64(len(text)), text, nil)
}

// bytes returns the log, with the super block of a log of the version 1 with the entries of b.
func (b *builder) bytes() []byte {
	log := slices.Clone(b.b)
	binary.LittleEndian.PutUint64(log[0:], Magic)
	binary.LittleEndian.PutUint64(log[8:], Version)
	binary.LittleEndian.PutUint64(log[16:], b.entries)
	binary.LittleEndian.PutUint32(log[24:], uint32(b.ss))
	return log
}

// sample returns the log of the tests: writes, a flush without data, a write with metadata, a write with FUA, a discard,
// a flush with data, and the marks.
func sample(ss int) []byte {
	b := newBuilder(ss)
	b.write(0, 0, "aa")
	b.write(4, 0, "b")
	b.entry(0, 0, FlagFlush, 0, "", nil)
	b.mark("mkfs")
	b.write(2, FlagMetadata, "c")
	b.write(0, FlagFUA, "d")
	b.entry(4, 1, FlagDiscard, 0, "", nil)
	b.write(6, FlagFlush, "ee")
	b.mark("ack-1")
	b.mark("dm-log-writes-end")
	return b.bytes()
}

// sampleEntries returns the entries of sample(ss), with the offsets in sectors of the log as written here.
func sampleEntries(ss int64) []Entry {
	entries := []Entry{
		{Offset: 1, Sector: 0, Sectors: 2, Data: 2},
		{Offset: 4, Sector: 4, Sectors: 1, Data: 5},
		{Offset: 6, Flags: FlagFlush, Data: -1},
		{Offset: 7, Flags: FlagMark, DataLen: 4, Mark: "mkfs", Data: -1},
		{Offset: 8, Sector: 2, Sectors: 1, Flags: FlagMetadata, Data: 9},
		{Offset: 10, Sector: 0, Sectors: 1, Flags: FlagFUA, Data: 11},
		{Offset: 12, Sector: 4, Sectors: 1, Flags: FlagDiscard, Data: -1},
		{Offset: 13, Sector: 6, Sectors: 2, Flags: FlagFlush, Data: 14},
		{Offset: 16, Flags: FlagMark, DataLen: 5, Mark: "ack-1", Data: -1},
		{Offset: 17, Flags: FlagMark, DataLen: 17, Mark: "dm-log-writes-end", Data: -1},
	}
	for i := range entries {
		entries[i].Offset *= ss
		if entries[i].Data > 0 {
			entries[i].Data *= ss
		}
	}
	return entries
}

func parse(t *testing.T, log []byte) *Log {
	t.Helper()
	l, err := Parse(bytes.NewReader(log), int64(len(log)))
	if err != nil {
		t.Fatalf("Parse() = %v", err)
	}
	return l
}

func TestParse(t *testing.T) {
	for _, ss := range []int{512, 4096, 64 << 10} {
		t.Run(fmt.Sprint(ss), func(t *testing.T) {
			log := sample(ss)
			if len(log) != 18*ss {
				t.Fatalf("the log has %d bytes, want %d", len(log), 18*ss)
			}
			l := parse(t, log)
			want := Super{Magic: 0x6a736677736872, Version: 1, Entries: 10, SectorSize: uint32(ss)}
			if l.Super != want {
				t.Errorf("Super = %+v, want %+v", l.Super, want)
			}
			if !slices.Equal(l.Entries, sampleEntries(int64(ss))) {
				t.Errorf("Entries =\n%+v\nwant\n%+v", l.Entries, sampleEntries(int64(ss)))
			}
		})
	}
}

// TestParseCount checks that Parse reads the entries that the super block counts, and none of the bytes after them.
func TestParseCount(t *testing.T) {
	for _, n := range []int{0, 1, 3} {
		log := sample(512)
		binary.LittleEndian.PutUint64(log[16:], uint64(n))
		end := sampleEntries(512)[n].Offset
		l := parse(t, log[:end])
		if l.Super.Entries != uint64(n) || !slices.Equal(l.Entries, sampleEntries(512)[:n]) {
			t.Errorf("Parse() of %d entries = %d entries %+v", n, len(l.Entries), l.Entries)
		}
	}
}

// TestParseLongestMark checks a mark that takes the whole of its sector after the entry.
func TestParseLongestMark(t *testing.T) {
	text := string(bytes.Repeat([]byte("m"), 512-32))
	b := newBuilder(512)
	b.mark(text)
	b.write(1, 0, "z")
	l := parse(t, b.bytes())
	if len(l.Entries) != 2 || l.Entries[0].Mark != text || l.Entries[1].Data != 3*512 {
		t.Errorf("Entries = %+v", l.Entries)
	}
}

// eofReader returns io.EOF with the last bytes of r, as io.ReaderAt allows.
type eofReader struct {
	r *bytes.Reader
}

func (e eofReader) ReadAt(p []byte, off int64) (int, error) {
	n, err := e.r.ReadAt(p, off)
	if err == nil && off+int64(n) == e.r.Size() {
		err = io.EOF
	}
	return n, err
}

// TestReadAtEOF checks the reads that end at the end of the log, of the text of a mark that takes its whole sector, and
// of the data of a write.
func TestReadAtEOF(t *testing.T) {
	text := string(bytes.Repeat([]byte("m"), 512-32))
	b := newBuilder(512)
	b.write(0, 0, "w")
	b.mark(text)
	log := b.bytes()
	l, err := Parse(eofReader{bytes.NewReader(log)}, int64(len(log)))
	if err != nil || len(l.Entries) != 2 || l.Entries[1].Mark != text {
		t.Fatalf("Parse() = %+v, %v", l, err)
	}
	log = sample(512)
	binary.LittleEndian.PutUint64(log[16:], 8)
	log = log[:sampleEntries(512)[8].Offset]
	l = parse(t, log)
	dev := make(image, 8*512)
	if err := l.Replay(dev, eofReader{bytes.NewReader(log)}, 0, 8); err != nil ||
		!bytes.Equal(dev, device(512, "dac...ee")) {
		t.Errorf("Replay() = %v, the device %q", err, sectors(dev, 512))
	}
}

// failReader fails each read that reaches the byte at.
type failReader struct {
	r  io.ReaderAt
	at int64
}

var errRead = errors.New("read error")

func (f failReader) ReadAt(p []byte, off int64) (int, error) {
	if off <= f.at && f.at < off+int64(len(p)) {
		return 0, errRead
	}
	return f.r.ReadAt(p, off)
}

func TestParseErrors(t *testing.T) {
	le64 := func(log []byte, off int, v uint64) { binary.LittleEndian.PutUint64(log[off:], v) }
	// in changes the field at off of the entry i of the sample with sectors of 512 bytes.
	in := func(i, off int) int { return int(sampleEntries(512)[i].Offset) + off }
	for _, c := range []struct {
		name   string
		change func(log []byte) []byte
		want   string
	}{
		{"short", func(log []byte) []byte { return log[:27] }, "27 bytes, shorter than a super block"},
		{"magic", func(log []byte) []byte { log[0]++; return log },
			"not a log of dm-log-writes (the magic is 0x6a736677736873, not 0x6a736677736872)"},
		{"version 2", func(log []byte) []byte { le64(log, 8, 2); return log },
			"a log of the version 2; this package reads the version 1"},
		{"version 0", func(log []byte) []byte { le64(log, 8, 0); return log },
			"a log of the version 0; this package reads the version 1"},
		{"sector size 0", func(log []byte) []byte { binary.LittleEndian.PutUint32(log[24:], 0); return log },
			"the sector size 0 is not a power of 2 from 512 to 65536"},
		{"sector size 256", func(log []byte) []byte { binary.LittleEndian.PutUint32(log[24:], 256); return log },
			"the sector size 256 is not a power of 2 from 512 to 65536"},
		{"sector size 1000", func(log []byte) []byte { binary.LittleEndian.PutUint32(log[24:], 1000); return log },
			"the sector size 1000 is not a power of 2 from 512 to 65536"},
		{"sector size 2^17", func(log []byte) []byte { binary.LittleEndian.PutUint32(log[24:], 1<<17); return log },
			"the sector size 131072 is not a power of 2 from 512 to 65536"},
		{"cut in an entry", func(log []byte) []byte { return log[:in(3, 100)] },
			"entry 3 at 3584: the log ends inside the entry"},
		{"cut after an entry", func(log []byte) []byte { return log[:in(3, 0)] },
			"entry 3 at 3584: the log ends inside the entry"},
		{"cut in the data", func(log []byte) []byte { return log[:in(7, 2*512-1)] },
			"entry 7 at 6656: the log ends inside the data of the entry"},
		{"cut before the data", func(log []byte) []byte { return log[:in(7, 512)] },
			"entry 7 at 6656: the log ends inside the data of the entry"},
		{"more entries counted", func(log []byte) []byte { le64(log, 16, 11); return log },
			"entry 10 at 9216: the log ends inside the entry"},
		{"mark too long", func(log []byte) []byte { le64(log, in(3, 24), 512-32+1); return log },
			"entry 3 at 3584: a mark of 481 bytes, longer than the 480 bytes of its sector after the entry"},
		{"mark with sectors", func(log []byte) []byte { le64(log, in(3, 8), 1); return log },
			"entry 3 at 3584: a mark with the sectors 0+1"},
		{"unknown flag", func(log []byte) []byte { le64(log, in(1, 16), 1<<5); return log },
			"entry 1 at 2048: unknown flags 0x20"},
		{"unknown flags", func(log []byte) []byte { le64(log, in(2, 16), FlagFlush|1<<63); return log },
			"entry 2 at 3072: unknown flags 0x8000000000000000"},
		{"data_len of a write", func(log []byte) []byte { le64(log, in(1, 24), 1); return log },
			"entry 1 at 2048: data_len 1 in an entry that is not a mark"},
		{"data_len of a discard", func(log []byte) []byte { le64(log, in(6, 24), 512); return log },
			"entry 6 at 6144: data_len 512 in an entry that is not a mark"},
		{"sectors past the largest device", func(log []byte) []byte {
			le64(log, in(1, 0), math.MaxInt64/512)
			return log
		}, "entry 1 at 2048: the sectors 18014398509481983+1 end past the largest device"},
		{"sector past the largest device", func(log []byte) []byte {
			le64(log, in(6, 0), math.MaxUint64)
			return log
		}, "entry 6 at 6144: the sectors 18446744073709551615+1 end past the largest device"},
		{"sectors that overflow", func(log []byte) []byte {
			le64(log, in(6, 0), 1)
			le64(log, in(6, 8), math.MaxUint64)
			return log
		}, "entry 6 at 6144: the sectors 1+18446744073709551615 end past the largest device"},
	} {
		t.Run(c.name, func(t *testing.T) {
			log := c.change(sample(512))
			if l, err := Parse(bytes.NewReader(log), int64(len(log))); err == nil || err.Error() != c.want {
				t.Errorf("Parse() = %+v, %v; want %s", l, err, c.want)
			}
		})
	}

	t.Run("sectors at the end of the largest device", func(t *testing.T) {
		log := sample(512)
		le64(log, in(6, 0), math.MaxInt64/512-1)
		if _, err := Parse(bytes.NewReader(log), int64(len(log))); err != nil {
			t.Errorf("Parse() = %v", err)
		}
	})
	t.Run("read errors", func(t *testing.T) {
		log := sample(512)
		for _, c := range []struct {
			at   int64
			want string
		}{
			{3, "the super block: read error"},
			{int64(in(2, 31)), "entry 2 at 3072: read error"},
			{int64(in(3, 32+3)), "entry 3 at 3584: read error"},
		} {
			r := failReader{bytes.NewReader(log), c.at}
			if _, err := Parse(r, int64(len(log))); !errors.Is(err, errRead) || err.Error() != c.want {
				t.Errorf("Parse() with an error at %d = %v; want %s", c.at, err, c.want)
			}
		}
		// The data of writes are not read.
		if _, err := Parse(failReader{bytes.NewReader(log), int64(in(0, 512))}, int64(len(log))); err != nil {
			t.Errorf("Parse() with an error in the data = %v", err)
		}
	})
	t.Run("shorter than its size", func(t *testing.T) {
		log := sample(512)
		if _, err := Parse(bytes.NewReader(log[:in(5, 0)]), int64(len(log))); err == nil ||
			err.Error() != "entry 5 at 5120: unexpected EOF" {
			t.Errorf("Parse() = %v", err)
		}
	})
}

// image is the memory of a device of a fixed size.
type image []byte

func (m image) WriteAt(p []byte, off int64) (int, error) {
	if off < 0 || off+int64(len(p)) > int64(len(m)) {
		return 0, errors.New("past the end of the image")
	}
	return copy(m[off:], p), nil
}

// device returns the bytes of a device of len(sectors) sectors of ss bytes, the sector i of which is filled with the
// byte sectors[i], or zeros for '.'.
func device(ss int, sectors string) []byte {
	var b []byte
	for _, c := range []byte(sectors) {
		if c == '.' {
			c = 0
		}
		b = append(b, bytes.Repeat([]byte{c}, ss)...)
	}
	return b
}

func TestPoints(t *testing.T) {
	if p := parse(t, sample(512)).Points(); !slices.Equal(p, []int{2, 3, 5, 7, 8, 9}) {
		t.Errorf("Points() = %v", p)
	}
	if p := parse(t, newBuilder(512).bytes()).Points(); p != nil {
		t.Errorf("Points() of an empty log = %v", p)
	}
}

// TestReplay checks the device at each point of the sample, replayed from the start and from the point before.
func TestReplay(t *testing.T) {
	for _, ss := range []int{512, 4096} {
		t.Run(fmt.Sprint(ss), func(t *testing.T) {
			log := sample(ss)
			l := parse(t, log)
			wants := map[int]string{
				2: "aa..b...", // the first flush
				3: "aa..b...",
				5: "dac.b...", // the write with FUA, after one with metadata
				7: "dac...ee", // a discard, and a flush with data
				8: "dac...ee",
				9: "dac...ee",
			}
			step := make(image, 8*ss)
			from := 0
			for _, p := range l.Points() {
				want := device(ss, wants[p])
				whole := make(image, 8*ss)
				if err := l.Replay(whole, bytes.NewReader(log), 0, p+1); err != nil || !bytes.Equal(whole, want) {
					t.Errorf("Replay(0, %d) = %v, the device %q; want %q", p+1, err, sectors(whole, ss), wants[p])
				}
				if err := l.Replay(step, bytes.NewReader(log), from, p+1); err != nil || !bytes.Equal(step, want) {
					t.Errorf("Replay(%d, %d) = %v, the device %q; want %q", from, p+1, err, sectors(step, ss), wants[p])
				}
				from = p + 1
			}
			// Only the entries from 4 to 6: the write with metadata and the one with FUA.
			part := make(image, 8*ss)
			if err := l.Replay(part, bytes.NewReader(log), 4, 6); err != nil || !bytes.Equal(part, device(ss, "d.c.....")) {
				t.Errorf("Replay(4, 6) = %v, the device %q", err, sectors(part, ss))
			}
			if err := l.Replay(part, bytes.NewReader(log), 3, 3); err != nil || !bytes.Equal(part, device(ss, "d.c.....")) {
				t.Errorf("Replay(3, 3) = %v, the device %q", err, sectors(part, ss))
			}
		})
	}
}

// sectors returns the bytes of the sectors of a device as device takes them, or '?' for a sector of other bytes.
func sectors(dev []byte, ss int) string {
	var b []byte
	for s := range slices.Chunk(dev, ss) {
		c := s[0]
		if bytes.Count(s, s[:1]) != ss {
			c = '?'
		} else if c == 0 {
			c = '.'
		}
		b = append(b, c)
	}
	return string(b)
}

// TestReplayLarge checks a write and a discard of more than maxChunk bytes.
func TestReplayLarge(t *testing.T) {
	const ss, n = 64 << 10, 40 // 2.5 MiB
	data := make([]byte, n*ss)
	for i := range data {
		data[i] = byte(i*7 + i/251)
	}
	b := newBuilder(ss)
	b.entry(1, n, 0, 0, "", data)
	b.entry(0, 0, FlagFlush, 0, "", nil)
	b.entry(5, n-10, FlagDiscard, 0, "", nil)
	log := b.bytes()
	l := parse(t, log)
	dev := make(image, (n+2)*ss)
	if err := l.Replay(dev, bytes.NewReader(log), 0, 2); err != nil || !bytes.Equal(dev[ss:(n+1)*ss], data) ||
		!bytes.Equal(dev[:ss], make([]byte, ss)) || !bytes.Equal(dev[(n+1)*ss:], make([]byte, ss)) {
		t.Fatalf("Replay() of the write = %v, or other bytes", err)
	}
	if err := l.Replay(dev, bytes.NewReader(log), 2, 3); err != nil {
		t.Fatalf("Replay() of the discard = %v", err)
	}
	want := slices.Concat(make([]byte, ss), data[:4*ss], make([]byte, (n-10)*ss), data[(n-6)*ss:], make([]byte, ss))
	if !bytes.Equal(dev, want) {
		t.Error("Replay() of the discard: other bytes")
	}
}

func TestReplayErrors(t *testing.T) {
	log := sample(512)
	l := parse(t, log)
	for _, r := range [][2]int{{-1, 2}, {3, 2}, {0, 11}} {
		want := fmt.Sprintf("no entries from %d to %d in a log of 10", r[0], r[1])
		if err := l.Replay(make(image, 8*512), bytes.NewReader(log), r[0], r[1]); err == nil || err.Error() != want {
			t.Errorf("Replay(%d, %d) = %v; want %s", r[0], r[1], err, want)
		}
	}
	if err := l.Replay(make(image, 4*512), bytes.NewReader(log), 0, 2); err == nil ||
		err.Error() != "entry 1: past the end of the image" {
		t.Errorf("Replay() past the end of the image = %v", err)
	}
	if err := l.Replay(make(image, 8*512), failReader{bytes.NewReader(log), 5*512 + 7}, 0, 2); !errors.Is(err, errRead) ||
		err.Error() != "entry 1: reading its data: read error" {
		t.Errorf("Replay() with a read error = %v", err)
	}
	if err := l.Replay(make(image, 8*512), bytes.NewReader(log[:5*512+7]), 0, 2); err == nil ||
		err.Error() != "entry 1: reading its data: unexpected EOF" {
		t.Errorf("Replay() of a log cut after Parse = %v", err)
	}
}

func TestRange(t *testing.T) {
	l := parse(t, sample(512))
	var got [][2]int64
	for i := range l.Entries {
		off, n := l.Range(i)
		got = append(got, [2]int64{off, n})
	}
	want := [][2]int64{{0, 1024}, {2048, 512}, {0, 0}, {0, 0}, {1024, 512}, {0, 512}, {2048, 512}, {3072, 1024},
		{0, 0}, {0, 0}}
	if !slices.Equal(got, want) {
		t.Errorf("Range() = %v, want %v", got, want)
	}
}

func TestString(t *testing.T) {
	var got []string
	for _, e := range parse(t, sample(512)).Entries {
		got = append(got, e.String())
	}
	want := []string{"write 0+2", "write 4+1", "flush", `mark "mkfs"`, "write 2+1, metadata", "write 0+1, fua",
		"discard 4+1", "write 6+2, flush", `mark "ack-1"`, `mark "dm-log-writes-end"`}
	if !slices.Equal(got, want) {
		t.Errorf("String() = %q, want %q", got, want)
	}
	if s := (Entry{Flags: FlagFlush | FlagFUA, Data: -1}).String(); s != "flush, fua" {
		t.Errorf("String() of a flush with FUA = %q", s)
	}
}
