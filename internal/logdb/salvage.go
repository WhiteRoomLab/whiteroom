package logdb

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"math"
	"path"
	"slices"
	"strings"

	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// maxMissingSegments bounds the segments that Salvage lists as missing, so that a name or a checkpoint that points far
// past the others does not make it list billions.
const maxMissingSegments = 4096

// StateSource says where the state that a repair keeps comes from.
type StateSource int

const (
	FromLog             StateSource = iota // the log, from the start of its generation
	FromCheckpoint                         // the checkpoint, whose record is in the log
	FromCheckpointAlone                    // the checkpoint, whose own record cannot be read
)

func (s StateSource) String() string {
	switch s {
	case FromLog:
		return "the log"
	case FromCheckpoint:
		return "the checkpoint"
	case FromCheckpointAlone:
		return "the checkpoint, whose record cannot be read"
	}
	return fmt.Sprintf("source %d", int(s))
}

// Span is a part of the log that cannot be read: Offset is where the bytes begin in Path, or -1 for a missing segment;
// Length is how many, or 0 for records that are missing without a byte of damage. The sequence and event numbers are
// those of the records that are lost with it, when the records around it tell them, else 0. Bytes that lose no record
// (a header read with its own salt, padding that is not zero) have none. The event numbers are those between the events
// that can be read: after a compaction they may include numbers that it had dropped.
type Span struct {
	Problem
	Length                int64
	FirstSeq, LastSeq     uint64
	FirstEvent, LastEvent uint64
}

// LostObject is an object of the kept state whose data cannot be read or does not have its hash.
type LostObject struct {
	Hash    [32]byte
	Problem Problem
}

// Truncation says what a repair drops although it can read it: the records after the first damage that comes after the
// start of the kept state. It is set only if there are such records.
type Truncation struct {
	Damage                Problem // the first damage after the start
	FirstSeq, LastSeq     uint64  // of the records that it drops
	FirstEvent, LastEvent uint64  // the events from the damage on, or 0
}

// RepairPlan is what a repair keeps and drops. Recovery is what Open would change or not use, as Inspect says it.
type RepairPlan struct {
	Generation, Seq uint64
	Source          StateSource
	Checkpoint      string // "used", "none", "stale", "damaged: " and why, or "not this log's"
	Spans           []Span
	Objects         []LostObject
	Truncation      *Truncation
	Recovery        []Problem
	Remove          []string // the segments of the other generations and the temporary files
}

// Empty reports whether the plan has no span, object or truncation: nothing is dropped because it cannot be read.
func (p RepairPlan) Empty() bool {
	return len(p.Spans) == 0 && len(p.Objects) == 0 && p.Truncation == nil
}

func (p RepairPlan) clone() RepairPlan {
	p.Spans = slices.Clone(p.Spans)
	p.Objects = slices.Clone(p.Objects)
	p.Recovery = slices.Clone(p.Recovery)
	p.Remove = slices.Clone(p.Remove)
	if p.Truncation != nil {
		t := *p.Truncation
		p.Truncation = &t
	}
	return p
}

// keptRec is a record of the kept generation that has events that a repair keeps.
type keptRec struct {
	seg segNum
	off segOff
	n   int
}

// segMeta is what Events needs to read the records of a segment again.
type segMeta struct {
	name string
	salt uint64
}

// Salvager is the store while Salvage runs.
type Salvager struct {
	fsys vfs.FS
	o    Options
	db   *DB // only for the files of the objects
	plan RepairPlan
	st   *state
	kept []keptRec
	meta map[segNum]segMeta
}

// Salvage waits, until ctx ends, for the other processes that have the store in fsys open to close it, and keeps those
// that open it from then on out, as Inspect does. Then it reads what can be read of the log, the checkpoint and the
// objects, calls f with what a repair would keep and drop, and releases the locks; f must not use the Salvager after it
// returns. It never opens the store as its writer and changes no file, and of o it only checks the values. Before f it
// returns the error of a file of a newer version, of a name in log/ that is not a segment's, and of a store whose state
// cannot be made without guessing, and of a head of a newer version. A missing or damaged head is no error: it is a
// problem in the plan, and a repair writes it again.
func Salvage(ctx context.Context, fsys vfs.FS, o Options, f func(*Salvager) error) error {
	if err := o.fill(); err != nil {
		return err
	}
	if err := checkSalvageLayout(fsys); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	unlock, err := acquireMaintenance(ctx, fsys)
	if err != nil {
		return err
	}
	db := &DB{fsys: fsys, handles: newHandles(fsys)}
	released := false
	release := func() error {
		released = true
		return errors.Join(db.handles.close(), unlock())
	}
	defer func() {
		if !released {
			_ = release() // f has panicked
		}
	}()
	s, err := analyze(ctx, fsys, o, db)
	if err == nil {
		err = f(s)
	}
	if rerr := release(); rerr != nil {
		return errors.Join(err, rerr)
	}
	return err
}

// checkSalvageLayout is checkLayout without head, which a repair writes again: it must be a regular file if it is there.
func checkSalvageLayout(fsys vfs.FS) error {
	for _, e := range layout {
		if e.name == "head" {
			continue
		}
		if err := checkEntry(fsys, e.name, e.dir); err != nil {
			return err
		}
	}
	p := path.Join(fsys.Name(), "head")
	info, err := fsys.Lstat("head")
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return entryError(p, err, false)
	}
	return checkType(p, info.Mode(), false)
}

// headProblem reads head, which Open writes again if it is damaged and does not open the store without. A head of a
// newer version is an error.
func headProblem(fsys vfs.FS) (*Problem, error) {
	p := path.Join(fsys.Name(), "head")
	f, err := fsys.Open("head")
	if errors.Is(err, fs.ErrNotExist) {
		return &Problem{Path: p, Offset: -1, Reason: "missing file"}, nil
	}
	if err != nil {
		return nil, entryError(p, err, false)
	}
	_, err = readHead(f, p)
	_ = f.Close()
	var ve *VersionError
	switch {
	case errors.As(err, &ve):
		return nil, err
	case err != nil:
		pr := problemOf(err, p)
		return &pr, nil
	}
	return nil, nil
}

// Plan returns what a repair keeps and drops.
func (s *Salvager) Plan() RepairPlan {
	return s.plan.clone()
}

// Events calls fn with each event that a repair keeps, in order, until it returns false: those of every record of the
// kept generation that can be read, up to the last commit of the kept state. It reads them from the log again.
func (s *Salvager) Events(ctx context.Context, fn func(Event) bool) error {
	var b []byte
	cur := segNum(0)
	for _, k := range s.kept {
		if err := ctx.Err(); err != nil {
			return err
		}
		m := s.meta[k.seg]
		if k.seg != cur {
			var err error
			if b, err = readRange(s.fsys, m.name, 0, -1, false); err != nil {
				return err
			}
			cur = k.seg
		}
		p := path.Join(s.fsys.Name(), m.name)
		if k.off >= segOff(len(b)) {
			return &CorruptError{Path: p, Offset: int64(k.off), Reason: "truncated record"}
		}
		rec, _, _, _, reason := readFrame(b[k.off:], m.salt)
		if reason != "" {
			return &CorruptError{Path: p, Offset: int64(k.off), Reason: reason}
		}
		for i := range rec.ops {
			if o := &rec.ops[i]; o.code == opEvent && !fn(Event{Number: o.number, Seq: uint64(o.seq), Data: clone(o.value)}) {
				return nil
			}
		}
	}
	return nil
}

// View calls f with a transaction that reads the kept state, and fails to write.
func (s *Salvager) View(ctx context.Context, f func(*Tx) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return (&Tx{db: s.db, st: s.st}).run(f)
}

// analyze reads the store under the locks, as Salvage says, and makes what f is given.
func analyze(ctx context.Context, fsys vfs.FS, o Options, db *DB) (*Salvager, error) {
	ck, why, err := readCheckpoint(fsys)
	if err != nil {
		return nil, err
	}
	segs, err := listSegments(fsys)
	if err != nil {
		return nil, err
	}
	gens := generations(segs)
	if ck != nil && !slices.ContainsFunc(gens, func(g generation) bool { return g.gen == ck.h.gen }) {
		// A checkpoint proves that its generation was complete, also when none of its segments is left.
		gens = append(gens, generation{gen: ck.h.gen})
		slices.SortFunc(gens, func(a, b generation) int { return cmp.Compare(a.gen, b.gen) })
	}
	var ignored []uint64
	var res *genResult
	for i := len(gens) - 1; i >= 0 && res == nil; i-- {
		r, err := scanGeneration(ctx, fsys, gens[i], ck)
		switch {
		case err != nil:
			return nil, err
		case r.complete:
			res = r
		default:
			ignored = append(ignored, uint64(gens[i].gen))
		}
	}
	if res == nil {
		return nil, corruptFile(path.Join(fsys.Name(), "log"), "no complete generation")
	}
	if res.sc.refuse != nil {
		return nil, res.sc.refuse
	}
	slices.Reverse(ignored)
	sc := res.sc
	s := &Salvager{fsys: fsys, o: o, db: db, st: res.st, kept: sc.kept, meta: sc.segs}
	s.plan = RepairPlan{Generation: uint64(res.st.gen), Seq: uint64(res.st.seq), Spans: sc.spans, Truncation: sc.trunc}
	switch {
	case !res.useCk:
		s.plan.Source = FromLog
	case res.paired:
		s.plan.Source = FromCheckpoint
	default:
		s.plan.Source = FromCheckpointAlone
	}
	switch {
	case ck == nil:
		s.plan.Checkpoint = why
	case ck.h.gen < res.st.gen:
		s.plan.Checkpoint = "stale"
	case res.useCk:
		s.plan.Checkpoint = "used"
	default:
		s.plan.Checkpoint = "not this log's"
	}
	r := &recovery{cut: -1, info: RecoveryInfo{Checkpoint: s.plan.Checkpoint, IgnoredGenerations: ignored}}
	if sc.torn != nil {
		r.cut, r.seg = sc.torn.off, &segment{name: sc.torn.name}
	}
	r.remove = sc.tornSeg
	s.plan.Recovery = recoveryProblems(fsys.Name(), r)
	hp, err := headProblem(fsys)
	if err != nil {
		return nil, err
	}
	if hp != nil {
		s.plan.Recovery = append(s.plan.Recovery, *hp)
	}
	if err := s.verifyObjects(ctx); err != nil {
		return nil, err
	}
	if s.plan.Remove, err = removals(ctx, fsys, segs, res.st.gen); err != nil {
		return nil, err
	}
	return s, nil
}

// verifyObjects finds the objects of the kept state whose data cannot be read or does not have its hash. An object of a
// newer version, and a file that this process may not read, are no damage: they stop it.
func (s *Salvager) verifyObjects(ctx context.Context) error {
	var err error
	st := s.st
	st.objects.Ascend("", func(k string, loc objLoc) bool {
		if err = ctx.Err(); err != nil {
			return false
		}
		hash := [32]byte([]byte(k))
		data, rerr := s.db.readObject(st.gen, hash, loc)
		var ve *VersionError
		if errors.As(rerr, &ve) || errors.Is(rerr, fs.ErrPermission) || errors.Is(rerr, context.Canceled) ||
			errors.Is(rerr, context.DeadlineExceeded) {
			err = rerr
			return false
		}
		p, off := s.db.objectPlace(st.gen, hash, loc)
		if rerr != nil {
			s.plan.Objects = append(s.plan.Objects, LostObject{Hash: hash, Problem: problemOf(rerr, p)})
		} else if got := sha256.Sum256(data); got != hash {
			reason := fmt.Sprintf("object %x: the data has the hash %x", hash[:], got[:])
			s.plan.Objects = append(s.plan.Objects, LostObject{Hash: hash, Problem: Problem{Path: p, Offset: off, Reason: reason}})
		}
		return true
	})
	return err
}

// removals lists what a repair removes besides the kept generation's own segments: the segments of the other
// generations, then the temporary files of checkpoints and of objects, as CollectGarbage finds them.
func removals(ctx context.Context, fsys vfs.FS, segs []segFile, gen genNum) ([]string, error) {
	var out []string
	for _, s := range segs {
		if s.gen != gen {
			out = append(out, path.Join(fsys.Name(), s.name))
		}
	}
	entries, err := fsys.ReadDir(".")
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.Type().IsRegular() && strings.HasPrefix(e.Name(), ".tmp-checkpoint-") {
			out = append(out, path.Join(fsys.Name(), e.Name()))
		}
	}
	dirs, err := fsys.ReadDir("objects")
	if err != nil {
		return nil, err
	}
	for _, d := range dirs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !d.IsDir() || !isHex(d.Name(), 2) {
			continue
		}
		entries, err := fsys.ReadDir("objects/" + d.Name())
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if e.Type().IsRegular() && strings.HasPrefix(e.Name(), ".tmp-") {
				out = append(out, path.Join(fsys.Name(), "objects", d.Name(), e.Name()))
			}
		}
	}
	return out, nil
}

// genResult is what the scan of one generation has found.
type genResult struct {
	sc       *logScan
	st       *state
	complete bool // its base, if it has one, is complete: its base-end or a commit can be read, or a checkpoint names it
	useCk    bool
	paired   bool // the record of the checkpoint is in the log
}

// scanGeneration reads the segments of g, which has no segment if only the checkpoint ck names it, and the state that a
// repair would keep of it. A refusal that depends on g being the one kept is in the scan, not returned.
func scanGeneration(ctx context.Context, fsys vfs.FS, g generation, ck *checkpoint) (*genResult, error) {
	named := ck != nil && ck.h.gen == g.gen
	res := &genResult{}
	var n int
	if named {
		var err error
		if res.useCk, n, err = checkpointFit(ctx, fsys, g, ck); err != nil {
			return nil, err
		}
		res.paired = res.useCk && n > 0
	}
	st := &state{gen: g.gen, seg: 1, end: segHeaderSize, tables: tableMap{}, nextEvent: 1}
	sc := &logScan{gen: g.gen, dir: path.Join(fsys.Name(), "log"), q: salvageSeq{gen: g.gen, base: g.gen > 1},
		useCk: res.useCk, segs: map[segNum]segMeta{}}
	if res.useCk {
		*st = *ck.st
		st.end = ck.h.off + segOff(n)
		sc.ckSeq, sc.ckNext = ck.h.seq, ck.st.nextEvent
	}
	count := 0
	sc.apply = func(rec *record, index segNum, off segOff, n int, crc uint32) string {
		if count++; count%1024 == 0 {
			if sc.stop = ctx.Err(); sc.stop != nil {
				return ""
			}
		}
		next := *st
		next.tables = maps.Clone(st.tables) // a record that fails must leave st as it was
		if reason := next.applyRecord(new(owner), rec, index, off); reason != "" {
			return reason
		}
		next.seq, next.seg, next.off, next.end, next.lastCRC = rec.seq, index, off, off+segOff(n), crc
		*st = next
		return ""
	}
	res.sc, res.st = sc, st
	maxIdx := segNum(0)
	if len(g.segs) > 0 {
		maxIdx = g.segs[len(g.segs)-1].index
	}
	if res.useCk {
		maxIdx = max(maxIdx, ck.h.seg)
	}
	missing := uint64(0)
	next, i := segNum(1), 0
	for next <= maxIdx {
		if i < len(g.segs) && g.segs[i].index == next {
			if err := sc.readSegment(ctx, fsys, g.segs[i], next == maxIdx); err != nil {
				return nil, err
			}
			i++
			next++
			continue
		}
		end := maxIdx
		if i < len(g.segs) {
			end = g.segs[i].index - 1
		}
		if missing += uint64(end-next) + 1; missing > maxMissingSegments {
			return nil, corruptFile(sc.dir, "too many missing segments")
		}
		for ; next <= end; next++ {
			sc.gap(path.Join(fsys.Name(), segName(g.gen, next)), -1, 0, "missing segment")
		}
	}
	sc.finish()
	res.complete = g.gen == 1 || named || sc.sawEnd
	return res, nil
}

// readSegment reads the segment s, the last one of its generation if last, and scans it.
func (sc *logScan) readSegment(ctx context.Context, fsys vfs.FS, s segFile, last bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b, err := readRange(fsys, s.name, 0, -1, false)
	if err != nil {
		return err
	}
	p := path.Join(fsys.Name(), s.name)
	h, rescued, err := decodeHeader(p, b, sc.gen, s.index)
	var ve *VersionError
	var ce *CorruptError
	switch {
	case errors.As(err, &ve):
		return err
	case errors.As(err, &ce):
		// A crash while the segment was begun: no record was in it.
		if last && allZero(b[min(len(b), segHeaderSize):]) && (s.index > 1 || sc.q.base) {
			sc.tornSeg = s.name
			return nil
		}
		sc.gap(p, 0, int64(len(b)), ce.Reason)
		return nil
	case err != nil:
		return err
	case rescued:
		sc.addSpan(p, 0, segHeaderSize, "bad header CRC", false)
	case s.index == 1 && sc.q.base && sc.q.seq == 0:
		sc.q.seq = h.firstSeq
	}
	sc.segs[s.index] = segMeta{name: s.name, salt: h.salt}
	sc.scanBytes(s.index, p, b, h, last)
	return sc.stop
}

// decodeHeader is parseSegHeader that also reads a header whose CRC fails with the salt that it has, if the first frame
// of the segment verifies with it: rescued is then true, and only the salt is known.
func decodeHeader(p string, b []byte, gen genNum, index segNum) (h segHeader, rescued bool, err error) {
	h, err = parseSegHeader(p, b, gen, index)
	var ce *CorruptError
	if err == nil || !errors.As(err, &ce) || ce.Reason != "bad header CRC" {
		return h, false, err
	}
	salt := binary.LittleEndian.Uint64(b[40:])
	if _, _, _, _, reason := readFrame(b[segHeaderSize:], salt); reason == "" {
		return segHeader{gen: gen, index: index, salt: salt}, true, nil
	}
	return segHeader{}, false, err
}

// checkpointFit says whether the checkpoint ck, of the generation g, is used, and if its record is in the log, the
// length of its frame. It is used even if its record cannot be read, or its segment is gone: its file is valid, and it
// proves that the record was written. It is not used if the log has another record there, which is not that log's.
func checkpointFit(ctx context.Context, fsys vfs.FS, g generation, ck *checkpoint) (bool, int, error) {
	h := ck.h
	i := slices.IndexFunc(g.segs, func(s segFile) bool { return s.index == h.seg })
	if i < 0 {
		return true, 0, nil
	}
	if err := ctx.Err(); err != nil {
		return false, 0, err
	}
	b, err := readRange(fsys, g.segs[i].name, 0, -1, false)
	if err != nil {
		return false, 0, err
	}
	sh, rescued, err := decodeHeader(path.Join(fsys.Name(), g.segs[i].name), b, g.gen, h.seg)
	var ve *VersionError
	switch {
	case errors.As(err, &ve):
		return false, 0, err
	case err != nil:
		return true, 0, nil
	case !rescued && (sh.firstSeq > h.seq || h.off == segHeaderSize && sh.firstSeq != h.seq):
		return false, 0, nil
	case h.off >= segOff(len(b)):
		return true, 0, nil
	}
	rec, n, _, frame, reason := readFrame(b[h.off:], sh.salt)
	switch {
	case !frame:
		return true, 0, nil
	case binary.LittleEndian.Uint32(b[h.off+4:]) != h.recCRC:
		return false, 0, nil
	case reason != "":
		return true, 0, nil // its frame is that of the checkpoint, but its body cannot be read
	case rec.seq != h.seq || rec.typ != recCommit && (h.gen == 1 || rec.typ != recBaseEnd):
		return false, 0, nil
	}
	return true, n, nil
}

// salvageSeq follows the order of the records of a generation as sequence does, but lets records be missing before the
// next one that can be read.
type salvageSeq struct {
	gen  genNum
	seq  seqNum // of the last record, S in the base; 0 if S is not known yet
	base bool   // in the base of a generation after the first
	part uint64 // of the next base record
}

// acceptable is the part of follow that the type and the sequence number tell, for the frames that findNext is to check
// the CRC of.
func (q *salvageSeq) acceptable(typ recType, seq seqNum) bool {
	switch typ {
	case recCommit:
		return seq > q.seq
	case recBase, recBaseEnd:
		return q.base && (q.seq == 0 || seq == q.seq)
	}
	return false
}

// follow says why rec cannot come after the last record, or, if it can, whether records are missing before it.
func (q *salvageSeq) follow(rec *record) (lost bool, reason string) {
	switch rec.typ {
	case recBase, recBaseEnd:
		switch {
		case !q.base && q.gen == 1:
			return false, "a base record in generation 1"
		case !q.base:
			return false, "a base record after the end of the base"
		case q.seq != 0 && rec.seq != q.seq:
			return false, fmt.Sprintf("sequence %d, want %d", rec.seq, q.seq)
		case rec.typ == recBase && rec.part < q.part:
			return false, fmt.Sprintf("base part %d, want %d", rec.part, q.part)
		case rec.typ == recBaseEnd && rec.end.parts < q.part:
			return false, fmt.Sprintf("the base of generation %d does not match its end record", q.gen)
		case rec.typ == recBase:
			return rec.part > q.part, ""
		}
		return rec.end.parts > q.part, ""
	case recCommit:
		if rec.seq <= q.seq {
			return false, fmt.Sprintf("sequence %d, want %d", rec.seq, q.seq+1)
		}
		return q.base || rec.seq > q.seq+1, ""
	}
	return false, fmt.Sprintf("unknown record type %d", rec.typ)
}

// lostRange returns the sequence numbers of the records that are missing before rec, which follow has said: in the
// base, S is the number of them all. first is 0 if it is not known.
func (q *salvageSeq) lostRange(rec *record) (first, last seqNum) {
	first = q.seq + 1
	if q.base {
		first = q.seq
	}
	last = rec.seq
	if rec.typ == recCommit {
		last = rec.seq - 1
	}
	return first, last
}

func (q *salvageSeq) advance(rec *record) {
	switch rec.typ {
	case recBase:
		q.seq, q.part = rec.seq, rec.part+1
		if q.part == 0 {
			q.part = math.MaxUint64
		}
	case recBaseEnd, recCommit:
		q.seq, q.base = rec.seq, false
	}
}

// readFrame decodes the frame at the start of b, a segment with salt: its record, the length of the frame, how many
// bytes of padding are not zero in a frame whose CRC is right (the record is then right, and the padding is damaged),
// whether the frame is valid, and, if there is no record, why. A reason with a valid frame is that of its body.
func readFrame(b []byte, salt uint64) (rec record, n, pad int, frame bool, reason string) {
	body, n, reason := parseFrame(b, salt)
	if reason == "nonzero padding" {
		l := int(binary.LittleEndian.Uint32(b))
		body, pad, reason = b[8:8+l], n-8-l, ""
	}
	if reason != "" {
		return record{}, n, 0, false, reason
	}
	rec, reason = parseBody(body)
	return rec, n, pad, true, reason
}

// checkEvents checks the events of rec, a record that the checkpoint covers and that no state applies: they must
// follow the number last, as applying them would check, but with gaps.
func checkEvents(rec *record, last uint64) string {
	for i := range rec.ops {
		p := &rec.ops[i]
		if p.code != opEvent {
			continue
		}
		switch {
		case p.number <= last || p.number == math.MaxUint64:
			return fmt.Sprintf("event number %d, want %d or more", p.number, last+1)
		case rec.typ == recCommit && p.seq != rec.seq:
			return fmt.Sprintf("event sequence %d, want %d", p.seq, rec.seq)
		case rec.typ == recBase && (p.seq == 0 || p.seq > rec.seq):
			return fmt.Sprintf("event sequence %d, want 1 to %d", p.seq, rec.seq)
		}
		last = p.number
	}
	return ""
}

// eventBounds returns the first and the last event number in rec, or 0, 0.
func eventBounds(rec *record) (lo, hi uint64) {
	for i := range rec.ops {
		if p := &rec.ops[i]; p.code == opEvent {
			if lo == 0 {
				lo = p.number
			}
			hi = p.number
		}
	}
	return lo, hi
}

// spanMeta is what the scan keeps of a span until it knows the records around it.
type spanMeta struct {
	evFirst uint64 // the number after the last event that could be read before it
	covered bool   // every record that it loses is covered by the checkpoint
}

// tornTail is where Open would cut the last segment.
type tornTail struct {
	name string
	off  segOff
}

// logScan holds what the scan of one generation has decided so far, in the order of its segments. The start of the kept
// state is the record of the checkpoint if it is used: what its sequence number covers is not applied, and its events
// are only kept.
type logScan struct {
	gen    genNum
	dir    string
	q      salvageSeq
	useCk  bool
	ckSeq  seqNum
	ckNext uint64
	apply  func(rec *record, index segNum, off segOff, n int, crc uint32) string // "" if applied
	stop   error                                                                 // of the caller: scanBytes ends

	spans  []Span
	meta   []spanMeta
	pend   []int // the spans whose records are not known yet
	pendEv []int // those whose events are not
	lastEv uint64
	kept   []keptRec
	segs   map[segNum]segMeta

	cut     bool
	cutProb Problem
	cutEv   uint64
	trunc   *Truncation
	refuse  error
	sawEnd  bool
	torn    *tornTail
	tornSeg string
	endPath string
	endOff  segOff
}

func (sc *logScan) addSpan(p string, off, length int64, reason string, gap bool) int {
	i := len(sc.spans)
	sc.spans = append(sc.spans, Span{Problem: Problem{Path: p, Offset: off, Reason: reason}, Length: length})
	sc.meta = append(sc.meta, spanMeta{evFirst: sc.lastEv + 1})
	if gap {
		sc.pend = append(sc.pend, i)
	}
	return i
}

// gap adds bytes that cannot be read, or a missing segment, which lose the records that the next one that can be read
// does not follow.
func (sc *logScan) gap(p string, off, length int64, reason string) {
	sc.addSpan(p, off, length, reason, true)
}

// ownLost reports whether the record of the checkpoint, which was synced, has not been read: what cannot be read from
// here on has it, and is no torn tail.
func (sc *logScan) ownLost() bool {
	return sc.useCk && sc.q.seq < sc.ckSeq
}

// accepts says whether rec can be the next record, and whether records are missing before it.
func (sc *logScan) accepts(rec *record) (lost bool, reason string) {
	lost, reason = sc.q.follow(rec)
	if reason == "" && sc.useCk && rec.seq <= sc.ckSeq {
		reason = checkEvents(rec, sc.lastEv)
	}
	return lost, reason
}

// findNext returns the first offset after off in b, a segment with salt, of a frame that is a record that accepts. The
// type and the sequence number are checked before the CRC, so that garbage does not cost a CRC of every length.
func (sc *logScan) findNext(b []byte, off segOff, salt uint64) segOff {
	for at := off + 8; at+8+minBody <= segOff(len(b)); at += 8 {
		if !sc.q.acceptable(recType(b[at+8]), seqNum(binary.LittleEndian.Uint64(b[at+9:]))) {
			continue
		}
		rec, _, _, _, reason := readFrame(b[at:], salt)
		if reason != "" {
			continue
		}
		if _, reason := sc.accepts(&rec); reason == "" {
			return at
		}
	}
	return -1
}

// scanBytes decides what b, the bytes of the segment index at path p whose header is h, holds: which frames are
// records, where the bytes that cannot be read begin and end (the spans), and where the first damage after the start of
// the kept state is, from which a repair keeps no commit. last is true for the last segment of the generation, which may
// end with a torn tail.
//
// It does no I/O, and needs none of the state of the store: the records are applied by sc.apply, the caller's, and a
// record that it refuses is the first damage; the records after it, which are already classified, are only counted. It
// is the part of reading the log that the core takes over (3.6 「核の範囲」, 3.1).
func (sc *logScan) scanBytes(index segNum, p string, b []byte, h segHeader, last bool) {
	sc.endPath, sc.endOff = p, segOff(len(b))
	off := segOff(segHeaderSize)
	for off < segOff(len(b)) && sc.stop == nil {
		rec, n, pad, _, reason := readFrame(b[off:], h.salt)
		lost := false
		if reason == "" {
			lost, reason = sc.accepts(&rec)
		}
		if reason == "" {
			sc.record(index, p, off, n, pad, &rec, binary.LittleEndian.Uint32(b[off+4:]), lost)
			off += segOff(n)
			continue
		}
		switch at := sc.findNext(b, off, h.salt); {
		case at >= 0:
			sc.gap(p, int64(off), int64(at-off), reason)
			off = at
		case last && mayBeTorn(reason) && !sc.ownLost():
			// What Open cuts. A record that the checkpoint names is no torn tail: it was synced.
			if !sc.cut {
				sc.torn = &tornTail{name: path.Join("log", path.Base(p)), off: off}
			}
			return
		default:
			sc.gap(p, int64(off), int64(segOff(len(b))-off), reason)
			return
		}
	}
}

// record takes rec, the record at off in the segment index at path p, in a frame of n bytes with pad bytes of padding
// that are not zero, as the next one, after the records that are lost if lost.
func (sc *logScan) record(index segNum, p string, off segOff, n, pad int, rec *record, crc uint32, lost bool) {
	wasBase := sc.q.base
	var first, last seqNum
	if lost {
		first, last = sc.q.lostRange(rec)
	}
	head := sc.resolve(p, off, lost, first, last)
	if lost && (!sc.useCk || last > sc.ckSeq) {
		sc.startCut(sc.spans[head].Problem, wasBase)
	}
	sc.q.advance(rec)
	lo, hi := eventBounds(rec)
	switch {
	case sc.useCk && rec.seq <= sc.ckSeq:
		sc.keep(index, off, n, hi)
	case sc.cut:
		sc.drop(rec.seq, hi)
	default:
		if reason := sc.apply(rec, index, off, n, crc); reason != "" {
			i := sc.addSpan(p, int64(off), int64(n), reason, false)
			sp := &sc.spans[i]
			sp.FirstSeq, sp.LastSeq = uint64(rec.seq), uint64(rec.seq)
			sp.FirstEvent, sp.LastEvent = lo, hi
			sc.startCut(sp.Problem, wasBase)
		} else {
			sc.keep(index, off, n, hi)
		}
	}
	sc.seeEvents(lo, hi)
	if pad > 0 {
		sc.addSpan(p, int64(off)+int64(n-pad), int64(pad), "nonzero padding", false)
	}
	if rec.typ != recBase {
		sc.sawEnd = true
	}
}

// resolve gives the spans that wait for the next record the sequence numbers of the records that are lost, and returns
// the first of them. If records are lost without a byte of damage, it makes a span of no bytes.
func (sc *logScan) resolve(p string, off segOff, lost bool, first, last seqNum) int {
	head := -1
	if len(sc.pend) > 0 {
		head = sc.pend[0]
	}
	if lost && head < 0 {
		head = sc.addSpan(p, int64(off), 0, "missing records", true)
	}
	covered := sc.useCk && last <= sc.ckSeq
	for _, i := range sc.pend {
		if !lost {
			continue
		}
		if first > 0 && first <= last {
			sc.spans[i].FirstSeq, sc.spans[i].LastSeq = uint64(first), uint64(last)
		}
		sc.meta[i].covered = covered
		if sc.useCk && first > sc.ckSeq {
			sc.meta[i].evFirst = max(sc.meta[i].evFirst, sc.ckNext) // what the checkpoint covers has the events before it
		}
		sc.pendEv = append(sc.pendEv, i)
	}
	sc.pend = sc.pend[:0]
	return head
}

// startCut ends the kept state before the damage, the first after the start. In a base that no checkpoint covers it is
// no state at all: the store is refused.
func (sc *logScan) startCut(damage Problem, wasBase bool) {
	if sc.cut {
		return
	}
	sc.cut, sc.cutProb, sc.cutEv = true, damage, sc.lastEv+1
	if sc.useCk {
		sc.cutEv = max(sc.cutEv, sc.ckNext)
	}
	if wasBase && !sc.useCk {
		sc.refuseBase(damage)
	}
}

// refuseBase makes the scan end in the refusal of the store, for the first damage that it is given.
func (sc *logScan) refuseBase(damage Problem) {
	if sc.refuse == nil {
		sc.refuse = &CorruptError{Path: damage.Path, Offset: damage.Offset, Reason: fmt.Sprintf(
			"%s; the base of generation %d cannot be read, and no checkpoint has its state: restore the store from a copy",
			damage.Reason, sc.gen)}
	}
}

func (sc *logScan) keep(index segNum, off segOff, n int, hi uint64) {
	if hi > 0 {
		sc.kept = append(sc.kept, keptRec{seg: index, off: off, n: n})
	}
}

// drop counts a record that can be read and that a repair drops because it comes after the damage.
func (sc *logScan) drop(seq seqNum, hi uint64) {
	if sc.trunc == nil {
		sc.trunc = &Truncation{Damage: sc.cutProb, FirstSeq: uint64(seq), FirstEvent: sc.cutEv}
	}
	sc.trunc.LastSeq = uint64(seq)
	sc.trunc.LastEvent = max(sc.trunc.LastEvent, hi)
}

// seeEvents notes the events lo to hi of a record that can be read: they end the span that waits for them.
func (sc *logScan) seeEvents(lo, hi uint64) {
	if hi == 0 {
		return
	}
	for _, i := range sc.pendEv {
		last := lo - 1
		if sc.meta[i].covered {
			last = min(last, sc.ckNext-1)
		}
		if sc.meta[i].evFirst <= last {
			sc.spans[i].FirstEvent, sc.spans[i].LastEvent = sc.meta[i].evFirst, last
		}
	}
	sc.pendEv = sc.pendEv[:0]
	sc.lastEv = hi
}

// finish ends the scan of the generation: the spans after its last record that can be read, and the refusal of a base
// that is not complete.
func (sc *logScan) finish() {
	if sc.ownLost() {
		// The record of the checkpoint is lost: it is the last record that the spans lose that the checkpoint covers.
		if len(sc.pend) == 0 {
			sc.addSpan(sc.endPath, int64(sc.endOff), 0, "missing records", true)
		}
		first := sc.q.seq + 1
		if sc.q.base {
			first = sc.q.seq
		}
		for _, i := range sc.pend {
			if first > 0 {
				sc.spans[i].FirstSeq, sc.spans[i].LastSeq = uint64(first), uint64(sc.ckSeq)
			}
			sc.meta[i].covered = true
			sc.pendEv = append(sc.pendEv, i)
		}
	}
	sc.pend = nil
	for _, i := range sc.pendEv {
		if m := sc.meta[i]; m.covered && m.evFirst < sc.ckNext {
			sc.spans[i].FirstEvent, sc.spans[i].LastEvent = m.evFirst, sc.ckNext-1
		}
	}
	sc.pendEv = nil
	if t := sc.trunc; t != nil && t.LastEvent < t.FirstEvent {
		t.FirstEvent, t.LastEvent = 0, 0
	}
	if sc.gen > 1 && !sc.useCk && sc.q.base {
		sc.refuseBase(Problem{Path: sc.dir, Offset: -1, Reason: "the end of the base cannot be read"})
	}
}
