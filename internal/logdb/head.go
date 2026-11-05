package logdb

import (
	"errors"
	"fmt"
	"path"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// headWriter writes the file head for the readers: a slot for each update, in turn, with WriteAt and no Sync.
type headWriter struct {
	f       vfs.File
	counter uint64 // of the next slot
	bad     bool   // the file has no valid slot: both are written
}

func headOf(st *state, counter uint64) headSlot {
	return headSlot{counter: counter, gen: st.gen, seg: st.seg, end: st.end, seq: st.seq, crc: st.lastCRC}
}

func (h *headWriter) write(st *state) error {
	if h.bad {
		if err := h.f.Truncate(headSize); err != nil {
			return err
		}
		if err := writeAt(h.f, appendHeadSlot(appendHeadSlot(nil, headOf(st, 0)), headOf(st, 1)), 0); err != nil {
			return err
		}
		h.counter, h.bad = 2, false
		return nil
	}
	// After a failed write the same slot is written again, so that the other one stays valid.
	if err := writeAt(h.f, appendHeadSlot(nil, headOf(st, h.counter)), int64(h.counter%2)*headSlotSize); err != nil {
		return err
	}
	h.counter++
	return nil
}

// readHead returns the valid slot with the larger counter of the file head at path, open in f.
func readHead(f vfs.File, path string) (headSlot, error) {
	info, err := f.Stat()
	if err != nil {
		return headSlot{}, err
	}
	b := make([]byte, min(info.Size(), headSize))
	if err := readAt(f, b, 0, path); err != nil {
		return headSlot{}, err
	}
	s, err := parseHead(path, b)
	var ve *VersionError
	if info.Size() != int64(len(b)) && !errors.As(err, &ve) {
		return headSlot{}, corruptFile(path, fmt.Sprintf("has %d bytes, want %d", info.Size(), headSize))
	}
	return s, err
}

// readHeadAgain is readHead for a reader, while a writer may write f: if neither slot is valid, as when the writer
// writes the file again whole, it tries 3 times more, 1 ms apart.
func readHeadAgain(f vfs.File, p string) (headSlot, error) {
	for i := 0; ; i++ {
		s, err := readHead(f, p)
		var ce *CorruptError
		if i == 3 || !errors.As(err, &ce) {
			return s, err
		}
		time.Sleep(time.Millisecond)
	}
}

// atHead reports whether the last record of st is the one that the slot h of head names.
func atHead(st *state, h headSlot) bool {
	return st.gen == h.gen && st.seq == h.seq && st.seg == h.seg && st.end == h.end && st.lastCRC == h.crc
}

// headMismatch is the error of a reader that does not find in the log the record that head names.
func headMismatch(fsys vfs.FS) error {
	return corruptFile(path.Join(fsys.Name(), "head"), "the log does not match head")
}
