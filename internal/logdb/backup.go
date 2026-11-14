package logdb

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path"
	"slices"

	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// Position is a commit in the log of a store: the end of its record, End in the segment Segment of the generation
// Generation, with its sequence number and the CRC of its frame.
type Position struct {
	Generation, Segment uint64
	End                 int64
	Seq                 uint64
	CRC                 uint32
}

// Backup copies the store in src to dst, an empty directory, as it is at the last commit that is durable when it
// begins, while its writer may go on writing. It opens src as a reader, which keeps Maintain waiting until it returns,
// and calls between with the position of that commit; then it copies the log up to it, the checkpoint if it pairs with
// that log, and the files of the objects of that state, and syncs them. Open of dst restores the store. If Backup fails,
// dst must be emptied before it can try again.
func Backup(ctx context.Context, src, dst vfs.FS, between func(Position) error) (Position, error) {
	if err := ctx.Err(); err != nil {
		return Position{}, err
	}
	db, err := OpenReader(src, Options{})
	if err != nil {
		return Position{}, err
	}
	st := db.cur.Load()
	p := Position{Generation: uint64(st.gen), Segment: uint64(st.seg), End: int64(st.end), Seq: uint64(st.seq), CRC: st.lastCRC}
	if between != nil {
		err = between(p)
	}
	if err == nil {
		err = db.copyTo(ctx, dst, st)
	}
	if cerr := db.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return Position{}, err
	}
	return p, nil
}

// copyTo makes in dst the layout of a store with the log of st, the checkpoint if it pairs with it, and the files of
// the objects of st, and syncs every file and directory.
func (db *DB) copyTo(ctx context.Context, dst vfs.FS, st *state) error {
	entries, err := dst.ReadDir(".")
	if err != nil {
		return err
	}
	if len(entries) > 0 {
		return fmt.Errorf("%s: not empty", dst.Name())
	}
	for _, dir := range []string{"lock", "log", "objects"} {
		if err := dst.Mkdir(dir, 0o700); err != nil {
			return err
		}
	}
	for _, name := range []string{"lock/entry", "lock/readers", "lock/writer"} {
		if err := createFile(dst, name, nil); err != nil {
			return err
		}
	}
	for index := segNum(1); index <= st.seg; index++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		end := segOff(-1)
		if index == st.seg {
			end = st.end // the writer may have written more after it
		}
		if err := db.copySegment(dst, st.gen, index, end); err != nil {
			return err
		}
	}
	if err := db.copyCheckpoint(dst, st); err != nil {
		return err
	}
	dirs := map[string]bool{}
	st.objects.Ascend("", func(k string, loc objLoc) bool {
		if loc.placement != placeFile {
			return true
		}
		if err = ctx.Err(); err != nil {
			return false
		}
		name := objName([32]byte([]byte(k)))
		if dir := path.Dir(name); !dirs[dir] {
			if err = dst.Mkdir(dir, 0o700); err != nil {
				return false
			}
			dirs[dir] = true
		}
		err = db.copyObject(dst, name, loc.size)
		return err == nil
	})
	if err != nil {
		return err
	}
	if err := createFile(dst, "head", appendHeadSlot(appendHeadSlot(nil, headOf(st, 0)), headOf(st, 1))); err != nil {
		return err
	}
	for _, dir := range append(slices.Sorted(maps.Keys(dirs)), "objects", "lock", "log", ".") {
		if err := dst.SyncDir(dir); err != nil {
			return err
		}
	}
	return nil
}

// copySegment copies the segment index of generation gen, up to end, or to its end if end is negative, to dst.
func (db *DB) copySegment(dst vfs.FS, gen genNum, index segNum, end segOff) error {
	f, err := db.handles.file(gen, index)
	if err != nil {
		return err
	}
	if end < 0 {
		info, err := f.Stat()
		if err != nil {
			return err
		}
		end = segOff(info.Size())
	}
	return copyFile(dst, segName(gen, index), io.NewSectionReader(f, 0, int64(end)), int64(end))
}

// copyCheckpoint copies the checkpoint of the store to dst if it pairs with the log up to st, which dst has: if it is
// of the generation of st, and its record is in that log. The writer may have written a later one.
func (db *DB) copyCheckpoint(dst vfs.FS, st *state) error {
	p := path.Join(db.fsys.Name(), "checkpoint")
	b, err := readFile(db.fsys, "checkpoint")
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return err
	}
	h, _, err := parseCheckpoint(p, b)
	var ce *CorruptError
	switch {
	case errors.As(err, &ce):
		return nil // Open of the copy replays the log
	case err != nil:
		return err
	case h.gen != st.gen || h.seq > st.seq || h.seg > st.seg:
		return nil
	}
	end := segOff(-1)
	if h.seg == st.seg {
		end = st.end
	}
	sb, err := db.handles.readPrefix(h.gen, h.seg, end)
	if err != nil {
		return err
	}
	ck := &checkpoint{path: p, h: h}
	if _, n, err := ck.pairs(path.Join(db.fsys.Name(), segName(h.gen, h.seg)), sb); n == 0 || err != nil {
		return err
	}
	return createFile(dst, "checkpoint", b)
}

// copyObject copies the file name of an object of size bytes to dst.
func (db *DB) copyObject(dst vfs.FS, name string, size int64) error {
	p := path.Join(db.fsys.Name(), name)
	info, err := lstatEntry(db.fsys, name, false)
	if err != nil {
		return err
	}
	if want := fromInt64(size) + objHeaderSize; fromInt64(info.Size()) != want {
		return corruptFile(p, fmt.Sprintf("has %d bytes, want %d", info.Size(), want))
	}
	f, err := db.fsys.Open(name)
	if err != nil {
		return entryError(p, err, false)
	}
	defer func() { _ = f.Close() }()
	return copyFile(dst, name, io.NewSectionReader(f, 0, info.Size()), info.Size())
}

// copyFile makes the file name in dst with the n bytes of r, and syncs it.
func copyFile(dst vfs.FS, name string, r io.Reader, n int64) error {
	f, err := dst.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	copied, err := io.Copy(f, r)
	if err == nil && copied != n {
		err = fmt.Errorf("%s: %w", path.Join(dst.Name(), name), io.ErrUnexpectedEOF)
	}
	if err == nil {
		err = f.Sync()
	}
	return errors.Join(err, f.Close())
}
