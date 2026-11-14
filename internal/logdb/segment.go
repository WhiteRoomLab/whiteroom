package logdb

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"os"

	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// segment is a segment of the log that the writer has open.
type segment struct {
	f        vfs.File
	name     string // in the engine's directory
	gen      genNum
	index    segNum
	firstSeq seqNum
	salt     uint64
	end      segOff // after its last record
	records  int
}

// count returns i, the index of a segment that exists, as the number of the segments up to it.
func (i segNum) count() int {
	return int(i) //nolint:gosec // G115: at most the number of the files in log/, which fits
}

func segName(gen genNum, index segNum) string {
	return fmt.Sprintf("log/%016x-%016x.log", gen, index)
}

// parseSegName returns the generation and the index of the segment whose file in log/ has the name name, which must
// be as segName makes it.
func parseSegName(name string) (genNum, segNum, bool) {
	if len(name) != 37 || name[16] != '-' || name[33:] != ".log" {
		return 0, 0, false
	}
	gen, ok1 := parseHex(name[:16])
	index, ok2 := parseHex(name[17:33])
	return genNum(gen), segNum(index), ok1 && ok2 && gen > 0 && index > 0
}

// parseHex parses lower case hex digits.
func parseHex(s string) (uint64, bool) {
	var v uint64
	for i := range len(s) {
		switch c := s[i]; {
		case '0' <= c && c <= '9':
			v = v<<4 | uint64(c-'0')
		case 'a' <= c && c <= 'f':
			v = v<<4 | uint64(c-'a'+10)
		default:
			return 0, false
		}
	}
	return v, true
}

// createSegment makes the file of a new segment and writes its header, without syncing them. If it cannot write the
// header, it removes the file.
func createSegment(fsys vfs.FS, gen genNum, index segNum, firstSeq seqNum) (*segment, error) {
	var salt [8]byte
	if _, err := rand.Read(salt[:]); err != nil {
		return nil, err
	}
	s := &segment{name: segName(gen, index), gen: gen, index: index, firstSeq: firstSeq,
		salt: binary.LittleEndian.Uint64(salt[:]), end: segHeaderSize}
	f, err := fsys.OpenFile(s.name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	h := segHeader{gen: gen, index: index, firstSeq: firstSeq, salt: s.salt}
	if err := writeAt(f, appendSegHeader(nil, h), 0); err != nil {
		_ = f.Close()
		_ = fsys.Remove(s.name)
		return nil, err
	}
	s.f = f
	return s, nil
}

// append writes frame after the last record of s, with one WriteAt.
func (s *segment) append(frame []byte) error {
	return writeAt(s.f, frame, int64(s.end))
}

// truncate cuts s at end, durably.
func (s *segment) truncate(end segOff) error {
	if err := s.f.Truncate(int64(end)); err != nil {
		return err
	}
	return s.f.Sync()
}

func writeAt(f vfs.File, b []byte, off int64) error {
	n, err := f.WriteAt(b, off)
	if err == nil && n != len(b) {
		err = io.ErrShortWrite
	}
	return err
}
