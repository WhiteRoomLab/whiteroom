// Package dmlog reads and replays the logs of dm-log-writes, the target of the device mapper of Linux that copies every
// write to a block device into a log, for the check of internal/store on ext4 (3.5): record.sh records a workload
// through the target, as root, and the tests of internal/store replay its log, without root.
//
// The format is that of Linux v6.18: drivers/md/dm-log-writes.c (struct log_write_super, struct log_write_entry,
// log_one_block, log_writes_kthread) and Documentation/admin-guide/device-mapper/log-writes.rst. The integers are
// little-endian:
//
//	byte 0           the super block: the magic, the version and the number of entries (8 bytes each), and the size
//	                 of a sector (4 bytes)
//	byte SectorSize  the entries, each at the start of a sector of its own: the first sector that it writes, the number
//	                 of sectors, the flags and data_len (8 bytes each), and for a mark, its text in data_len bytes;
//	                 after a write, its data in the sectors that it writes
//
// Sectors are in units of SectorSize, the logical block size of the device. Discards, marks and flushes without data
// have no data in the log.
//
// The target logs a write with FUA when it completes, and any other write before the first flush that is issued after
// it has completed, when that flush completes. It logs a mark when it is set, and the mark dm-log-writes-end at the
// end. Replaying the log to a flush, a FUA write or a mark gives what the device held then without its cache: the
// worst case for the order of writes after a loss of power, not a model of the cache of a device.
package dmlog

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
)

// Magic and Version are those of the logs that Parse reads.
const (
	Magic   = 0x6a736677736872
	Version = 1
)

// The flags of an entry.
const (
	FlagFlush    = 1 << iota // REQ_PREFLUSH: a flush, with the data of a write or without
	FlagFUA                  // REQ_FUA
	FlagDiscard              // a discard of the sectors
	FlagMark                 // a mark, with its text
	FlagMetadata             // REQ_META
)

const knownFlags = FlagFlush | FlagFUA | FlagDiscard | FlagMark | FlagMetadata

// The sizes of struct log_write_super without its padding, and of struct log_write_entry.
const (
	superSize = 28
	entrySize = 32
)

// Super is the super block of a log.
type Super struct {
	Magic, Version, Entries uint64
	SectorSize              uint32
}

// Entry is an entry of a log.
type Entry struct {
	Offset          int64  // of the entry in the log
	Sector, Sectors uint64 // that it writes or discards, in units of SectorSize
	Flags           uint64
	DataLen         uint64 // of the text of a mark; Linux sets it only for marks
	Mark            string
	Data            int64 // the offset in the log of the data of a write, or -1 if the entry has none
}

// Log is a log of dm-log-writes.
type Log struct {
	Super   Super
	Entries []Entry
}

// Parse reads the log in r, of size bytes: the super block, and the entries that it counts. It does not read the bytes
// after them.
func Parse(r io.ReaderAt, size int64) (*Log, error) {
	var b [superSize]byte
	if size < superSize {
		return nil, fmt.Errorf("%d bytes, shorter than a super block", size)
	}
	if err := readAt(r, b[:], 0); err != nil {
		return nil, fmt.Errorf("the super block: %w", err)
	}
	s := Super{
		Magic:      binary.LittleEndian.Uint64(b[0:]),
		Version:    binary.LittleEndian.Uint64(b[8:]),
		Entries:    binary.LittleEndian.Uint64(b[16:]),
		SectorSize: binary.LittleEndian.Uint32(b[24:]),
	}
	switch ss := s.SectorSize; {
	case s.Magic != Magic:
		return nil, fmt.Errorf("not a log of dm-log-writes (the magic is %#x, not %#x)", s.Magic, Magic)
	case s.Version != Version:
		return nil, fmt.Errorf("a log of the version %d; this package reads the version %d", s.Version, Version)
	case ss < 512 || ss > 64<<10 || ss&(ss-1) != 0:
		return nil, fmt.Errorf("the sector size %d is not a power of 2 from 512 to 65536", ss)
	}
	l := &Log{Super: s}
	ss := int64(s.SectorSize)
	off := ss
	for i := range s.Entries {
		e, next, err := parseEntry(r, size, ss, off)
		if err != nil {
			return nil, fmt.Errorf("entry %d at %d: %w", i, off, err)
		}
		l.Entries = append(l.Entries, e)
		off = next
	}
	return l, nil
}

// parseEntry reads the entry at off in the log r of size bytes with sectors of ss bytes, and returns it with the offset
// of the next one.
func parseEntry(r io.ReaderAt, size, ss, off int64) (Entry, int64, error) {
	if size-off < ss {
		return Entry{}, 0, errors.New("the log ends inside the entry")
	}
	var b [entrySize]byte
	if err := readAt(r, b[:], off); err != nil {
		return Entry{}, 0, err
	}
	e := Entry{
		Offset:  off,
		Sector:  binary.LittleEndian.Uint64(b[0:]),
		Sectors: binary.LittleEndian.Uint64(b[8:]),
		Flags:   binary.LittleEndian.Uint64(b[16:]),
		DataLen: binary.LittleEndian.Uint64(b[24:]),
		Data:    -1,
	}
	next := off + ss
	switch {
	case e.Flags&^knownFlags != 0:
		return e, 0, fmt.Errorf("unknown flags %#x", e.Flags&^knownFlags)
	case e.Flags&FlagMark != 0:
		if e.DataLen > uint64(ss-entrySize) { //nolint:gosec // G115: ss is a sector size, at least 512
			return e, 0, fmt.Errorf("a mark of %d bytes, longer than the %d bytes of its sector after the entry",
				e.DataLen, ss-entrySize)
		}
		if e.Sectors != 0 {
			return e, 0, fmt.Errorf("a mark with the sectors %d+%d", e.Sector, e.Sectors)
		}
		text := make([]byte, e.DataLen)
		if err := readAt(r, text, off+entrySize); err != nil {
			return e, 0, err
		}
		e.Mark = string(text)
	case e.DataLen != 0:
		return e, 0, fmt.Errorf("data_len %d in an entry that is not a mark", e.DataLen)
	case e.Flags&FlagDiscard == 0 && e.Sectors > 0:
		if e.Sectors > uint64((size-next)/ss) { //nolint:gosec // G115: next is at most size, checked at the start
			return e, 0, errors.New("the log ends inside the data of the entry")
		}
		e.Data = next
		next += int64(e.Sectors) * ss //nolint:gosec // G115: at most (size-next)/ss, checked above
	}
	//nolint:gosec // G115: ss is positive
	if limit := uint64(math.MaxInt64 / ss); e.Sector > limit || e.Sectors > limit-e.Sector {
		return e, 0, fmt.Errorf("the sectors %d+%d end past the largest device", e.Sector, e.Sectors)
	}
	return e, next, nil
}

// readAt reads len(p) bytes from r at off.
func readAt(r io.ReaderAt, p []byte, off int64) error {
	n, err := r.ReadAt(p, off)
	if n == len(p) {
		return nil
	}
	if err == nil || errors.Is(err, io.EOF) {
		err = io.ErrUnexpectedEOF
	}
	return err
}

// Range returns the bytes of the device that the entry i writes or discards: n from off. n is 0 for a mark and for a
// flush without data.
func (l *Log) Range(i int) (off, n int64) {
	e := &l.Entries[i]
	if e.Data < 0 && e.Flags&FlagDiscard == 0 {
		return 0, 0
	}
	ss := int64(l.Super.SectorSize)
	return int64(e.Sector) * ss, int64(e.Sectors) * ss //nolint:gosec // G115: Parse refuses sectors that end past MaxInt64
}

// maxChunk is the most that Replay reads or writes at once.
const maxChunk = 1 << 20

// Replay writes to img what the entries from from to to, not included, did to the device: the data of each write in
// its sectors, and zeros in those of each discard. r is the log of l.
func (l *Log) Replay(img io.WriterAt, r io.ReaderAt, from, to int) error {
	if from < 0 || from > to || to > len(l.Entries) {
		return fmt.Errorf("no entries from %d to %d in a log of %d", from, to, len(l.Entries))
	}
	var buf []byte
	for i := from; i < to; i++ {
		off, n := l.Range(i)
		if want := int(min(n, maxChunk)); len(buf) < want {
			buf = make([]byte, want)
		}
		data := l.Entries[i].Data
		for done := int64(0); done < n; {
			chunk := buf[:min(n-done, maxChunk)]
			if data >= 0 {
				if err := readAt(r, chunk, data+done); err != nil {
					return fmt.Errorf("entry %d: reading its data: %w", i, err)
				}
			} else {
				clear(chunk)
			}
			if _, err := img.WriteAt(chunk, off+done); err != nil {
				return fmt.Errorf("entry %d: %w", i, err)
			}
			done += int64(len(chunk))
		}
	}
	return nil
}

// Points returns the indexes of the flushes, the FUA writes and the marks of l, in their order: the entries after
// which, with those before them, the device held what a loss of power could leave.
func (l *Log) Points() []int {
	var points []int
	for i, e := range l.Entries {
		if e.Flags&(FlagFlush|FlagFUA|FlagMark) != 0 {
			points = append(points, i)
		}
	}
	return points
}

var flagNames = []struct {
	flag uint64
	name string
}{{FlagFlush, "flush"}, {FlagFUA, "fua"}, {FlagMetadata, "metadata"}}

// String describes e: `mark "mkfs"`, "discard 0+16", "write 8+8" or "flush" (without data), and the flags that it has
// besides, as in "write 8+8, fua".
func (e Entry) String() string {
	var kind string
	switch {
	case e.Flags&FlagMark != 0:
		return fmt.Sprintf("mark %q", e.Mark)
	case e.Flags&FlagDiscard != 0:
		kind = fmt.Sprintf("discard %d+%d", e.Sector, e.Sectors)
	case e.Data < 0 && e.Flags&FlagFlush != 0:
		kind = "flush"
	default:
		kind = fmt.Sprintf("write %d+%d", e.Sector, e.Sectors)
	}
	parts := []string{kind}
	for _, f := range flagNames {
		if e.Flags&f.flag != 0 && f.name != kind {
			parts = append(parts, f.name)
		}
	}
	return strings.Join(parts, ", ")
}
