package logdb

import (
	"errors"
	"fmt"

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
