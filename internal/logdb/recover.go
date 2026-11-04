package logdb

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path"
	"strings"

	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// layout is what the directory of a store has, besides the segments in log/.
var layout = []struct {
	name string
	dir  bool
}{
	{"lock", true}, {"lock/entry", false}, {"lock/readers", false}, {"lock/writer", false},
	{"log", true}, {"objects", true}, {"head", false},
}

func checkLayout(fsys vfs.FS) error {
	for _, e := range layout {
		if err := checkEntry(fsys, e.name, e.dir); err != nil {
			return err
		}
	}
	return nil
}

// checkEntry checks, with the messages of internal/store, that name is a directory if dir is true, and a regular file
// if not, without following a symbolic link.
func checkEntry(fsys vfs.FS, name string, dir bool) error {
	_, err := lstatEntry(fsys, name, dir)
	return err
}

// lstatEntry is checkEntry that also returns what Lstat says of name.
func lstatEntry(fsys vfs.FS, name string, dir bool) (fs.FileInfo, error) {
	p := path.Join(fsys.Name(), name)
	info, err := fsys.Lstat(name)
	if err != nil {
		return nil, entryError(p, err, dir)
	}
	return info, checkType(p, info.Mode(), dir)
}

// entryError returns err, of looking up the file at path p, or the directory if dir is true, with the messages of
// internal/store. A missing file of the engine is damage: the error does not wrap fs.ErrNotExist.
func entryError(p string, err error, dir bool) error {
	var pe *fs.PathError
	switch {
	case errors.Is(err, fs.ErrNotExist) && dir:
		return fmt.Errorf("%s: missing directory", p)
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("%s: missing file", p)
	case errors.As(err, &pe):
		return fmt.Errorf("%s: cannot read: %w", p, pe.Err)
	}
	return err
}

func checkType(p string, mode fs.FileMode, dir bool) error {
	switch {
	case mode&fs.ModeSymlink != 0:
		return fmt.Errorf("%s: symbolic links are not allowed", p)
	case dir && !mode.IsDir():
		return fmt.Errorf("%s: must be a directory", p)
	case !dir && !mode.IsRegular():
		return fmt.Errorf("%s: must be a regular file", p)
	}
	return nil
}

// segFile is a segment in log/.
type segFile struct {
	name       string
	gen, index uint64
}

// listSegments returns the segments in log/, in the order of generation and index.
func listSegments(fsys vfs.FS) ([]segFile, error) {
	entries, err := fsys.ReadDir("log")
	if err != nil {
		return nil, err
	}
	segs := make([]segFile, 0, len(entries))
	for _, e := range entries {
		name := "log/" + e.Name()
		p := path.Join(fsys.Name(), name)
		gen, index, ok := parseSegName(e.Name())
		if !ok {
			return nil, corruptFile(p, "not the name of a segment")
		}
		if err := checkType(p, e.Type(), false); err != nil {
			return nil, err
		}
		segs = append(segs, segFile{name: name, gen: gen, index: index})
	}
	if len(segs) == 0 {
		return nil, corruptFile(path.Join(fsys.Name(), "log"), "no segments")
	}
	return segs, nil
}

// recovery is what Open has found, and what it is to change, which it decides before it changes any file.
type recovery struct {
	st     *state
	seg    *segment // the segment that the next record goes in, open to write
	cut    int64    // where seg has a torn tail, or -1
	remove string   // a torn last segment, or ""
	head   headWriter
	info   RecoveryInfo
}

func (r *recovery) close() {
	if r.seg != nil && r.seg.f != nil {
		_ = r.seg.f.Close()
	}
	if r.head.f != nil {
		_ = r.head.f.Close()
	}
}

// recoverLog reads the log and the head of the store in fsys, whose locks the writer holds. It changes no file.
func recoverLog(fsys vfs.FS) (*recovery, error) {
	segs, err := listSegments(fsys)
	if err != nil {
		return nil, err
	}
	for i, s := range segs {
		if s.gen != 1 {
			return nil, corruptFile(path.Join(fsys.Name(), s.name), fmt.Sprintf("unsupported generation %d", s.gen))
		}
		if s.index != uint64(i+1) {
			return nil, corruptFile(path.Join(fsys.Name(), "log"), fmt.Sprintf("missing segment %d of generation %d", i+1, s.gen))
		}
	}
	r := &recovery{
		st:   &state{gen: 1, seg: 1, end: segHeaderSize, tables: tableMap{}, nextEvent: 1},
		cut:  -1,
		info: RecoveryInfo{Generation: 1, Checkpoint: "none", TruncatedAt: -1},
	}
	own := new(owner)
	for i, s := range segs {
		last := i == len(segs)-1
		p := path.Join(fsys.Name(), s.name)
		b, err := readFile(fsys, s.name)
		if err != nil {
			return nil, err
		}
		h, err := parseSegHeader(p, b, s.gen, s.index)
		var ce *CorruptError
		if last && i > 0 && errors.As(err, &ce) && allZero(b[min(len(b), segHeaderSize):]) {
			// A crash while the segment was begun: no record was in it.
			r.remove, r.info.RemovedSegment = s.name, p
			break
		}
		if err != nil {
			return nil, err
		}
		if want := r.st.seq + 1; h.firstSeq != want {
			return nil, corruptFile(p, fmt.Sprintf("the header names first sequence %d, want %d", h.firstSeq, want))
		}
		seg := &segment{name: s.name, gen: h.gen, index: h.index, firstSeq: h.firstSeq, salt: h.salt}
		if err := r.scan(p, b, seg, own, last); err != nil {
			return nil, err
		}
		if !last && seg.records == 0 {
			return nil, corruptFile(p, "no records")
		}
		r.st.logBytes += seg.end
		r.seg = seg
	}
	r.st.segments = int(r.seg.index)
	r.info.Seq = r.st.seq
	if err := r.openFiles(fsys); err != nil {
		r.close()
		return nil, err
	}
	return r, nil
}

// scan applies the records of seg, whose content at path p is b, to the state. It refuses a record that is not valid,
// except for a torn tail of the last segment, which it marks to be cut.
func (r *recovery) scan(p string, b []byte, seg *segment, own *owner, last bool) error {
	st := r.st
	off := int64(segHeaderSize)
	for off < int64(len(b)) {
		body, n, reason := parseFrame(b[off:], seg.salt)
		if reason != "" {
			if !last || !mayBeTorn(reason) {
				return &CorruptError{Path: p, Offset: off, Reason: reason}
			}
			if at := findRecord(b, off, seg.salt, st.seq+1); at >= 0 {
				return &CorruptError{Path: p, Offset: off, Reason: fmt.Sprintf("a valid record follows at offset %d", at)}
			}
			r.cut, r.info.TruncatedAt = off, off
			break
		}
		rec, reason := nextRecord(body, st.seq+1)
		if reason == "" {
			reason = st.apply(own, rec.ops, rec.seq, seg.index, off)
		}
		if reason != "" {
			return &CorruptError{Path: p, Offset: off, Reason: reason}
		}
		st.lastCRC = binary.LittleEndian.Uint32(b[off+4:])
		off += int64(n)
		st.seq++
		st.seg, st.end = seg.index, off
		seg.records++
		r.info.Replayed++
	}
	seg.end = off
	return nil
}

// nextRecord decodes body, the body of a frame that must hold the commit want, or says why it cannot.
func nextRecord(body []byte, want uint64) (record, string) {
	rec, reason := parseBody(body)
	switch {
	case reason != "":
	case rec.seq != want:
		reason = fmt.Sprintf("sequence %d, want %d", rec.seq, want)
	case rec.typ != recCommit:
		reason = "a base record in generation 1"
	}
	return rec, reason
}

// mayBeTorn reports whether reason, of parseFrame, is one that a crash while the frame was written can leave. The
// others come after the CRC of the frame has verified: the record was written completely, and cutting it could lose
// a commit.
func mayBeTorn(reason string) bool {
	return reason == "truncated record" || reason == "bad record CRC" || strings.HasPrefix(reason, "bad record length ")
}

// findRecord returns the first offset after off in b, a segment with salt, where a frame whose CRC verifies holds a
// record with the sequence number next or a larger one, or -1.
func findRecord(b []byte, off int64, salt, next uint64) int64 {
	for at := off + 8; at+8 <= int64(len(b)); at += 8 {
		if _, _, reason := parseFrame(b[at:], salt); reason != "" && mayBeTorn(reason) {
			continue
		}
		if binary.LittleEndian.Uint64(b[at+9:]) >= next { // the frame is complete: its body has type and seq
			return at
		}
	}
	return -1
}

// openFiles opens the segment that the next record goes in, and the file head, whose counter it reads.
func (r *recovery) openFiles(fsys vfs.FS) error {
	f, err := fsys.OpenFile(r.seg.name, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	r.seg.f = f
	if r.head.f, err = fsys.OpenFile("head", os.O_RDWR, 0); err != nil {
		return err
	}
	s, err := readHead(r.head.f, path.Join(fsys.Name(), "head"))
	var ce *CorruptError
	switch {
	case errors.As(err, &ce):
		r.head.bad = true // head is only for the readers: it is written again
	case err != nil:
		return err
	default:
		r.head.counter = s.counter + 1
	}
	return nil
}

// apply makes the changes that recovery has decided, and makes durable what Open goes on from: a record that a
// killed writer left in the page cache, and the name of a segment that it made.
func (r *recovery) apply(fsys vfs.FS) error {
	if r.remove != "" {
		if err := fsys.Remove(r.remove); err != nil {
			return err
		}
	}
	if r.cut >= 0 {
		if err := r.seg.f.Truncate(r.cut); err != nil {
			return err
		}
	}
	if err := r.seg.f.Sync(); err != nil {
		return err
	}
	return fsys.SyncDir("log")
}

func readFile(fsys vfs.FS, name string) ([]byte, error) {
	return readPrefix(fsys, name, -1)
}

// readPrefix reads the file name to its end, or its first limit bytes if it has more and limit is not negative.
func readPrefix(fsys vfs.FS, name string, limit int64) ([]byte, error) {
	f, err := fsys.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	p := path.Join(fsys.Name(), name)
	size := info.Size()
	if limit >= 0 {
		size = min(size, limit)
	}
	if size > math.MaxInt {
		return nil, fmt.Errorf("%s: too large to read", p)
	}
	b := make([]byte, size)
	if err := readAt(f, b, 0, p); err != nil {
		return nil, err
	}
	return b, nil
}

// readAt reads len(b) bytes at off of f, the file at path.
func readAt(f vfs.File, b []byte, off int64, path string) error {
	n, err := f.ReadAt(b, off)
	switch {
	case n == len(b):
		return nil
	case err == nil || errors.Is(err, io.EOF):
		return fmt.Errorf("%s: %w", path, io.ErrUnexpectedEOF)
	}
	return err
}
