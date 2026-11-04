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
	"slices"
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

// generation is a generation of the log with its segments, in the order of their indexes.
type generation struct {
	gen  uint64
	segs []segFile
}

// generations groups segs, which are in the order of generation and index, by generation.
func generations(segs []segFile) []generation {
	var gens []generation
	for _, s := range segs {
		if len(gens) == 0 || gens[len(gens)-1].gen != s.gen {
			gens = append(gens, generation{gen: s.gen})
		}
		g := &gens[len(gens)-1]
		g.segs = append(g.segs, s)
	}
	return gens
}

// consecutive checks that the segments of g have the indexes 1, 2, ... A compaction removes the segments of a newer
// generation from the last one, and syncs log/ after each, so that a crash leaves no gap in it either.
func (g generation) consecutive(fsys vfs.FS) error {
	for i, s := range g.segs {
		if s.index != uint64(i+1) {
			return corruptFile(path.Join(fsys.Name(), "log"), fmt.Sprintf("missing segment %d of generation %d", i+1, g.gen))
		}
	}
	return nil
}

// sequence follows the order of the records of a generation: in a generation after the first, its base records, with
// the parts 0, 1, ..., and its base-end, all with the sequence number S of its first segment; then the commits S+1,
// S+2, ... In generation 1, the commits 1, 2, ...
type sequence struct {
	gen  uint64
	seq  uint64 // of the last record; S in the base
	base bool   // in the base of a generation after the first
	part uint64 // of the next base record
}

func newSequence(gen uint64) sequence {
	return sequence{gen: gen, base: gen > 1}
}

// first checks the first sequence number in h, the header of the next segment, at path p: that of its first record.
// In the first segment of a generation after the first, it is S.
func (q *sequence) first(p string, h segHeader) error {
	want := q.seq + 1
	switch {
	case h.index == 1 && q.base:
		q.seq = h.firstSeq
		return nil
	case q.base:
		want = q.seq
	}
	if h.firstSeq != want {
		return corruptFile(p, fmt.Sprintf("the header names first sequence %d, want %d", h.firstSeq, want))
	}
	return nil
}

// check says why rec cannot be the next record, or returns "".
func (q *sequence) check(rec *record) string {
	if q.base {
		switch {
		case rec.typ == recCommit:
			return "a commit before the end of the base"
		case rec.seq != q.seq:
			return fmt.Sprintf("sequence %d, want %d", rec.seq, q.seq)
		case rec.typ == recBase && rec.part != q.part:
			return fmt.Sprintf("base part %d, want %d", rec.part, q.part)
		case rec.typ == recBaseEnd && rec.end.parts != q.part:
			return fmt.Sprintf("the base of generation %d does not match its end record", q.gen)
		}
		return ""
	}
	switch {
	case rec.seq != q.seq+1:
		return fmt.Sprintf("sequence %d, want %d", rec.seq, q.seq+1)
	case rec.typ != recCommit && q.gen == 1:
		return "a base record in generation 1"
	case rec.typ != recCommit:
		return "a base record after the end of the base"
	}
	return ""
}

// advance goes on after rec, which check has accepted.
func (q *sequence) advance(rec *record) {
	switch rec.typ {
	case recBase:
		q.part++
	case recBaseEnd:
		q.base = false
	default:
		q.seq = rec.seq
	}
}

// follows reports whether a record of the type typ with the sequence number seq, after an invalid frame, shows that
// the frame is not a torn tail: a commit after the last one, or, in the base, which is synced before its base-end is
// written, a record that is not a base record.
func (q *sequence) follows(typ byte, seq uint64) bool {
	if q.base {
		return typ != recBase
	}
	return seq > q.seq
}

// recovery is what Open has found, and what it is to change, which it decides before it changes any file.
type recovery struct {
	st     *state
	seg    *segment // the segment that the next record goes in, open to write
	cut    int64    // where seg has a torn tail, or -1
	remove string   // a torn last segment, or ""
	head   headWriter
	info   RecoveryInfo
	ckp    ckpMark // the checkpoint that pairs with the log, or zero
	ckpLog int64   // the size of the log up to the end of the record of that checkpoint
}

func (r *recovery) close() {
	if r.seg != nil && r.seg.f != nil {
		_ = r.seg.f.Close()
	}
	if r.head.f != nil {
		_ = r.head.f.Close()
	}
}

// recoverLog reads the log, the checkpoint and the head of the store in fsys, whose locks the writer holds. It changes
// no file. The state is that of the newest complete generation: the first, or one whose base-end is valid. Newer ones
// are left for a compaction to remove; older ones are not read.
func recoverLog(fsys vfs.FS) (*recovery, error) {
	segs, err := listSegments(fsys)
	if err != nil {
		return nil, err
	}
	ck, why, err := readCheckpoint(fsys)
	if err != nil {
		return nil, err
	}
	r := &recovery{cut: -1, info: RecoveryInfo{Checkpoint: why, TruncatedAt: -1}}
	gens := generations(segs)
	found := false
	for i := len(gens) - 1; i >= 0 && !found; i-- {
		g := gens[i]
		if ck != nil && ck.h.gen > g.gen {
			return nil, ck.incomplete()
		}
		if err := g.consecutive(fsys); err != nil {
			return nil, err
		}
		if found, err = r.recover(fsys, g, ck); err != nil {
			return nil, err
		}
		if !found {
			r.info.IgnoredGenerations = append(r.info.IgnoredGenerations, g.gen)
		}
	}
	switch {
	case !found && ck != nil:
		return nil, ck.incomplete()
	case !found:
		return nil, corruptFile(path.Join(fsys.Name(), "log"), "no complete generation")
	case ck != nil && ck.h.gen < r.st.gen:
		r.info.Checkpoint = "stale"
	case ck != nil:
		r.info.Checkpoint = "used"
	}
	slices.Reverse(r.info.IgnoredGenerations)
	r.st.segments = int(r.seg.index)
	r.info.Generation, r.info.Seq = r.st.gen, r.st.seq
	if err := r.openFiles(fsys); err != nil {
		r.close()
		return nil, err
	}
	return r, nil
}

// recover replays the generation g, from the checkpoint ck if it is of g and pairs with the log, and reports whether
// g is complete; then r has its state. A checkpoint of a complete generation that does not pair with it is corruption.
func (r *recovery) recover(fsys vfs.FS, g generation, ck *checkpoint) (bool, error) {
	ours := ck != nil && ck.h.gen == g.gen
	if ours {
		if paired, err := r.fromCheckpoint(fsys, g, ck); paired || err != nil {
			return paired, err
		}
		if g.gen == 1 {
			return false, ck.notInLog(fsys) // the first generation is complete
		}
	}
	st := &state{gen: g.gen, seg: 1, end: segHeaderSize, tables: tableMap{}, nextEvent: 1}
	q := newSequence(g.gen)
	out, err := replay(fsys, g, 0, nil, segHeader{}, segHeaderSize, st, &q, new(owner))
	switch {
	case err != nil || !out.complete:
		return false, err
	case ours:
		return false, ck.notInLog(fsys)
	}
	r.use(fsys, out)
	return true, nil
}

// fromCheckpoint replays g after the record of the checkpoint ck, if the log has it there: a valid frame of a commit,
// or of a base-end, with the sequence number and the CRC that ck names. The segments before that of the record are not
// read.
func (r *recovery) fromCheckpoint(fsys vfs.FS, g generation, ck *checkpoint) (bool, error) {
	h := ck.h
	if h.seg > uint64(len(g.segs)) {
		return false, nil
	}
	s := g.segs[h.seg-1]
	b, err := readFile(fsys, s.name)
	if err != nil {
		return false, err
	}
	sh, err := parseSegHeader(path.Join(fsys.Name(), s.name), b, g.gen, s.index)
	var ve *VersionError
	switch {
	case errors.As(err, &ve):
		return false, err
	case err != nil, sh.firstSeq > h.seq, h.off == segHeaderSize && sh.firstSeq != h.seq, h.off >= int64(len(b)):
		return false, nil
	}
	body, n, reason := parseFrame(b[h.off:], sh.salt)
	if reason != "" || binary.LittleEndian.Uint32(b[h.off+4:]) != h.recCRC {
		return false, nil
	}
	rec, reason := parseBody(body)
	if reason != "" || rec.seq != h.seq || rec.typ != recCommit && (g.gen == 1 || rec.typ != recBaseEnd) {
		return false, nil
	}
	st := ck.st
	st.end = h.off + int64(n)
	for _, prev := range g.segs[:h.seg-1] {
		info, err := lstatEntry(fsys, prev.name, false)
		if err != nil {
			return false, err
		}
		st.logBytes += info.Size()
	}
	r.ckp, r.ckpLog = ckpMark{gen: h.gen, seq: h.seq}, st.logBytes+st.end
	q := sequence{gen: g.gen, seq: h.seq}
	out, err := replay(fsys, g, int(h.seg-1), b, sh, st.end, st, &q, ck.own)
	if err != nil {
		return false, err
	}
	r.use(fsys, out)
	return true, nil
}

// use makes out, the replay of the current generation, what r has found.
func (r *recovery) use(fsys vfs.FS, out replayed) {
	r.st, r.seg, r.cut, r.info.TruncatedAt, r.info.Replayed = out.st, out.seg, out.cut, out.cut, out.records
	if out.remove != "" {
		r.remove, r.info.RemovedSegment = out.remove, path.Join(fsys.Name(), out.remove)
	}
}

// replayed is what replay has found in a generation.
type replayed struct {
	complete bool     // its base, if it has one, is complete
	st       *state   // with every record of the log applied
	seg      *segment // the last segment that stays
	cut      int64    // where seg has a torn tail, or -1
	remove   string   // a torn last segment, or ""
	records  int      // applied
}

// replay applies the records of the segments of g from g.segs[from], the first at off, to st, in the order of q, and
// reports whether the base of g is complete. If b is not nil, it is the content of g.segs[from], whose header is h and
// which has records before off. It refuses a record that is not valid, except for a torn tail of the last segment,
// which it marks to be cut or removed; in the base, which a compaction writes before anything depends on it, such a
// tail leaves the generation incomplete.
func replay(fsys vfs.FS, g generation, from int, b []byte, h segHeader, off int64, st *state, q *sequence,
	own *owner) (replayed, error) {
	out := replayed{st: st, cut: -1}
	for i := from; i < len(g.segs); i++ {
		s := g.segs[i]
		last := i == len(g.segs)-1
		p := path.Join(fsys.Name(), s.name)
		records := 0
		if i > from || b == nil {
			var err error
			if b, err = readFile(fsys, s.name); err != nil {
				return out, err
			}
			h, err = parseSegHeader(p, b, g.gen, s.index)
			var ce *CorruptError
			if last && errors.As(err, &ce) && allZero(b[min(len(b), segHeaderSize):]) && (s.index > 1 || q.base) {
				// A crash while the segment was begun: no record was in it.
				out.remove = s.name
				break
			}
			if err != nil {
				return out, err
			}
			if err := q.first(p, h); err != nil {
				return out, err
			}
			off = segHeaderSize
		} else {
			records = 1
		}
		seg := &segment{name: s.name, gen: g.gen, index: s.index, firstSeq: h.firstSeq, salt: h.salt, records: records}
		for off < int64(len(b)) {
			body, n, reason := parseFrame(b[off:], h.salt)
			if reason != "" {
				if !last || !mayBeTorn(reason) {
					return out, &CorruptError{Path: p, Offset: off, Reason: reason}
				}
				if at := findRecord(b, off, h.salt, q.follows); at >= 0 {
					return out, &CorruptError{Path: p, Offset: off, Reason: fmt.Sprintf("a valid record follows at offset %d", at)}
				}
				out.cut = off
				break
			}
			rec, reason := parseBody(body)
			if reason == "" {
				reason = q.check(&rec)
			}
			if reason == "" {
				reason = st.applyRecord(own, &rec, s.index, off)
			}
			if reason != "" {
				return out, &CorruptError{Path: p, Offset: off, Reason: reason}
			}
			q.advance(&rec)
			st.seq, st.seg, st.off, st.lastCRC = rec.seq, s.index, off, binary.LittleEndian.Uint32(b[off+4:])
			off += int64(n)
			st.end = off
			seg.records++
			out.records++
		}
		seg.end = off
		if !last && seg.records == 0 {
			return out, corruptFile(p, "no records")
		}
		st.logBytes += seg.end
		out.seg = seg
	}
	out.complete = !q.base
	return out, nil
}

// mayBeTorn reports whether reason, of parseFrame, is one that a crash while the frame was written can leave. The
// others come after the CRC of the frame has verified: the record was written completely, and cutting it could lose
// a commit.
func mayBeTorn(reason string) bool {
	return reason == "truncated record" || reason == "bad record CRC" || strings.HasPrefix(reason, "bad record length ")
}

// findRecord returns the first offset after off in b, a segment with salt, of a frame whose CRC verifies and whose
// record follows accepts by its type and sequence number, or -1.
func findRecord(b []byte, off int64, salt uint64, follows func(typ byte, seq uint64) bool) int64 {
	for at := off + 8; at+8 <= int64(len(b)); at += 8 {
		if _, _, reason := parseFrame(b[at:], salt); reason != "" && mayBeTorn(reason) {
			continue
		}
		if follows(b[at+8], binary.LittleEndian.Uint64(b[at+9:])) { // the frame is complete: its body has type and seq
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
	return readOpen(f, path.Join(fsys.Name(), name), limit)
}

// readOpen is readPrefix of f, the file at path p, which is open.
func readOpen(f vfs.File, p string, limit int64) ([]byte, error) {
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
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
