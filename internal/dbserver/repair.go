package dbserver

import (
	"cmp"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/fspath"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// maxUnreadableListed bounds the numbers of events that a RepairReport lists as unreadable.
const maxUnreadableListed = 1000

// repairLogs are the audit logs, in the order in which a report lists their holes.
var repairLogs = [...]string{dbwire.LogLogin, dbwire.LogActivity}

// RepairOptions says what Repair does.
type RepairOptions struct {
	// Apply repairs the data directory. Without it Repair only looks, and changes nothing.
	Apply bool
	// Truncate lets a repair drop the commits that can be read but come after the first damage, which cannot be read. A
	// repair that needs it fails without it, and changes nothing.
	Truncate bool
	// DropUnreadable drops the events that are neither an event of the daemon nor a record that dbwire.ReadRecordID reads.
	// Without it they are kept, and listed.
	DropUnreadable bool
	// Keep is the directory that gets a copy of logdb/, as it is, before anything is changed: it must be missing (its parent
	// must exist) or empty, not a symbolic link itself, and not the data directory or in it. It is required with Apply, and not
	// allowed without it.
	Keep string
	// Now is the time of the rows of the holes and of the mark after their events; zero: Options.Now, or the time.
	Now time.Time
}

// RepairHole is the loss of the records First to Last of an audit log that the repair makes: the records are lost, and what
// remains of them is dropped.
type RepairHole struct {
	Log         string
	First, Last uint64
	Hash        [32]byte // of the stored bytes of the record Last
	// Row says that a row of the table chains and an event of the daemon record the hole. It is false for a hole in the
	// middle of a log whose record after it has no prev that can be read, which names the hash.
	Row bool
}

// RepairGap is a gap in an audit log that the repair does not make: records First to Last are missing, and the repair drops
// fewer events where they were than there are records, so that they were lost before it. Neither a hole of a repair nor a gap
// that whiteroom-db accept-break accepted records it.
type RepairGap struct {
	Log         string
	First, Last uint64
	// End says that the gap is at the end of the log: the head of the log names the record Last, which the log does not have.
	End bool
}

// RepairReport is what Repair found and did. Nothing in it is a key or a value of a row, or the data of an object.
type RepairReport struct {
	Plan logdb.RepairPlan
	// Holes are the holes that the repair makes in the audit logs, the login history first, each log in order of its records.
	Holes []RepairHole
	// Gaps are the gaps of the audit logs that the repair does not make: whiteroom doctor goes on reporting them.
	Gaps []RepairGap
	// Unreadable are the numbers of the events that are neither an event of the daemon nor a record, the first
	// UnreadableTotal of them up to a bound; Dropped says that the repair drops them.
	Unreadable      []uint64
	UnreadableTotal int
	Dropped         bool
	// SchemaFrom is the table schema that the repair raises to dbwire.TableVersion, for the rows of the holes; 0 if it
	// raises none.
	SchemaFrom uint64
	// Kept is the directory under Keep that has the copy of logdb/, once it is made.
	Kept string
	// Applied says that the repair is done, and Result is what the engine did, as far as it got.
	Applied bool
	Result  logdb.RepairResult
}

// Things returns how many things a repair does: what the plan drops, recovers and removes, and the events that it is told to
// drop as unreadable. The holes and the gaps are what follows from them.
func (r RepairReport) Things() int {
	n := len(r.Plan.Spans) + len(r.Plan.Objects) + len(r.Plan.Recovery) + len(r.Plan.Remove)
	if r.Plan.Truncation != nil {
		n++
	}
	if r.Dropped {
		n += r.UnreadableTotal
	}
	return n
}

// checkpointForeign is logdb's name for a checkpoint of the generation of the log that the log does not have.
const checkpointForeign = "not this log's"

// Repair repairs the data directory dir, which no daemon or other process has open, or, without r.Apply, looks at what a
// repair would do and changes nothing. It reads what can be read of the log, the checkpoint and the objects of the engine
// (logdb.Salvage), and what a repair keeps is the state of the last commit that comes before the first damage that cannot
// be read, or the checkpoint's, which covers the damage before it; it drops the events and the objects that cannot be read,
// and with r.Truncate also the commits that come after damage, and writes the rest as a new generation of the log. It records
// the loss of audit records as holes (RepairHole), with a row of the table chains and an event of the daemon each, in the
// same generation. Nothing of files/ is changed.
//
// Without r.Apply it is Inspect: the engine reads through a file system that refuses every write, a mark of a failed sync of
// this boot, and one that cannot be read, are refused before and after the locks, a mark of an earlier boot is left where it
// is, and the directory need not be writable. With it, it is Maintain, but that the first thing that it changes is the copy of
// logdb/ in r.Keep, which comes after every refusal: a plan that needs a truncation without r.Truncate, a store that has no
// commit that a repair can keep, and a table schema that cannot hold the holes (the schema 8 or 9 is raised to 10 with
// them; a schema below 8 is refused if there is a hole to record in it). A plan with nothing in it changes nothing, and copies nothing.
//
// The report is as far as Repair got, also with an error. It waits for the processes that have the directory open for up to
// two seconds, and then fails with an error that wraps ErrInUse.
func Repair(ctx context.Context, dir string, o Options, r RepairOptions) (RepairReport, error) {
	switch {
	case r.Apply && r.Keep == "":
		return RepairReport{}, errors.New("a repair that is applied needs the directory of the copy of logdb/")
	case !r.Apply && r.Keep != "":
		return RepairReport{}, errors.New("the directory of the copy is for a repair that is applied")
	}
	root, opened, err := dataRoot(dir)
	if err != nil {
		return RepairReport{}, err
	}
	defer func() { _ = root.Close() }()
	o.osDir = opened
	if r.Apply {
		if _, err := checkKeep(opened, r.Keep); err != nil {
			return RepairReport{}, err
		}
	}
	return repair(ctx, root, dir, o, r, osKeep(root, r.Keep))
}

// repair is Repair on the data directory root, which stays the caller's; dir is its name in errors. mk makes the directory of
// the copy, and is called once, if a repair is applied.
func repair(ctx context.Context, root vfs.FS, dir string, o Options, r RepairOptions, mk keepMaker) (RepairReport, error) {
	var rep RepairReport
	o.Log.Accept = acceptSchema(fspath.Join(dir, "logdb"))
	err := maintainWith(ctx, root, dir, o, !r.Apply, func(ctx context.Context, fsys vfs.FS, entered func(), f func(*logdb.Salvager) error) error {
		// Salvage reads the store before it calls f, which is as long as a check takes: the two seconds of waiting end when it
		// holds the last lock.
		return logdb.Salvage(ctx, &lockedFS{FS: fsys, locked: entered}, o.Log, func(s *logdb.Salvager) error {
			entered()
			return f(s)
		})
	}, func(s *logdb.Salvager) error {
		rr := &repairRun{ctx: ctx, s: s, root: root, dir: dir, o: o, r: r, mk: mk, rep: &rep}
		return rr.run()
	})
	return rep, err
}

// EngineProblem returns the problem p of the engine as Inspect reports it.
func EngineProblem(p logdb.Problem) Problem { return engineProblem(p) }

// lockedFS tells when the last lock of a maintenance is held: lock/writer, which the engine takes after lock/entry and
// lock/readers.
type lockedFS struct {
	vfs.FS
	locked func()
}

func (l *lockedFS) Lock(name string, mode vfs.LockMode) (func() error, error) {
	unlock, err := l.FS.Lock(name, mode)
	if err == nil && name == "lock/writer" {
		l.locked()
	}
	return unlock, err
}

// repairRun is a Repair that holds the locks.
type repairRun struct {
	ctx  context.Context
	s    *logdb.Salvager
	root vfs.FS
	dir  string
	o    Options
	r    RepairOptions
	mk   keepMaker
	rep  *RepairReport
}

func (rr *repairRun) run() error {
	s, rep := rr.s, rr.rep
	plan := s.Plan()
	if plan.Checkpoint == checkpointForeign {
		plan.Recovery = append(plan.Recovery, logdb.Problem{Path: path.Join(rr.root.Name(), "logdb", "checkpoint"), Offset: -1,
			Reason: "it is not a checkpoint of this log: Open does not use it, and reads the log from the start of its generation"})
	}
	rep.Plan = plan
	rep.Dropped = rr.r.DropUnreadable
	var schema uint64
	logs := map[string]*auditLog{}
	if err := s.View(rr.ctx, func(tx *logdb.Tx) error {
		if err := rr.o.Log.Accept(tx); err != nil {
			return err
		}
		if v, ok := tx.Get(dbwire.TableMeta, []byte(dbwire.MetaSchema)); ok {
			schema, _ = schemaVersion(v)
		}
		for _, name := range repairLogs {
			logs[name] = readAuditLog(tx, name)
		}
		return nil
	}); err != nil {
		return err
	}
	w := &auditWalk{logs: logs, lost: lostEvents(plan), next: plan.NextEvent, drop: rr.r.DropUnreadable, rep: rep}
	if err := s.Events(rr.ctx, w.event); err != nil {
		return err
	}
	if err := w.finish(); err != nil {
		return err
	}
	now := rr.r.Now
	if now.IsZero() {
		if rr.o.Now != nil {
			now = rr.o.Now()
		} else {
			now = time.Now()
		}
	}

	var puts []logdb.RepairPut
	var events [][]byte
	for _, h := range rep.Holes {
		if !h.Row {
			continue
		}
		hole := dbwire.ChainHole{Last: h.Last, Hash: h.Hash, At: now}
		puts = append(puts, logdb.RepairPut{Table: dbwire.TableChains, Key: dbwire.ChainHoleKey(h.Log, h.First),
			Value: dbwire.AppendChainHole(nil, hole)})
		events = append(events, dbwire.AppendHole(nil, dbwire.Hole{Log: h.Log, First: h.First, Last: h.Last, Hash: h.Hash}))
	}
	if len(events) > 0 {
		events = append(events, dbwire.AppendMark(nil, now))
		switch {
		case schema < 8:
			return fmt.Errorf("%s: the repair drops audit records, and the store has the table schema %d, which has no table chains "+
				"to record the holes in: a writer that opens the store (whiteroom-db serve) upgrades it to %d, or restore the store "+
				"from a copy", fspath.Join(rr.dir, "logdb"), schema, dbwire.TableVersion)
		case schema < dbwire.TableVersion:
			rep.SchemaFrom = schema
			puts = append(puts, logdb.RepairPut{Table: dbwire.TableMeta, Key: []byte(dbwire.MetaSchema),
				Value: binary.AppendUvarint(nil, dbwire.TableVersion)})
		}
	}
	if plan.Seq == 0 && !plan.Empty() {
		return fmt.Errorf("%s: no commit can be kept, so a repair would leave an empty store: restore the store from a copy",
			fspath.Join(rr.dir, "logdb"))
	}
	if !rr.r.Apply || rep.Things() == 0 {
		return nil
	}
	if t := plan.Truncation; t != nil && !rr.r.Truncate {
		return truncationRefusal(t)
	}
	if err := rr.keep(); err != nil {
		return err
	}
	var rewrite logdb.Rewrite
	if rr.r.DropUnreadable {
		rewrite.Event = func(e logdb.Event) ([]byte, bool) {
			if c := classify(e.Data); c.unreadable {
				return nil, false
			}
			return e.Data, true
		}
	}
	res, err := s.Apply(rr.ctx, logdb.RepairOptions{Truncate: rr.r.Truncate, Rewrite: rewrite, Puts: puts, Events: events})
	rep.Result = res
	rep.Applied = err == nil
	return err
}

func truncationRefusal(t *logdb.Truncation) error {
	commits := fmt.Sprintf("the commits %d to %d come", t.FirstSeq, t.LastSeq)
	if t.FirstSeq == t.LastSeq {
		commits = fmt.Sprintf("the commit %d comes", t.FirstSeq)
	}
	where := t.Damage.Path
	if t.Damage.Offset >= 0 {
		where += fmt.Sprintf(" offset %d", t.Damage.Offset)
	}
	return fmt.Errorf("%s after the damage at %s (%s): repair with -truncate to drop them, or restore the store from a copy",
		commits, where, t.Damage.Reason)
}

// keep makes the copy of logdb/ in the directory of Keep, and syncs it. A copy that fails is removed.
func (rr *repairRun) keep() error {
	k, err := rr.mk()
	if err != nil {
		return err
	}
	defer func() { _ = k.fs.Close() }()
	src, err := rr.root.Sub("logdb")
	if err != nil {
		k.discard()
		return err
	}
	defer func() { _ = src.Close() }()
	if err := k.copyLogdb(rr.ctx, src); err != nil {
		k.discard()
		return err
	}
	rr.rep.Kept = fspath.Join(k.name, "logdb")
	return nil
}

// keepDir is the directory of the copy of logdb/.
type keepDir struct {
	fs      vfs.FS
	name    string
	created []string // what the copy has made in fs, in the order in which it made them
	made    func()   // removes the directory, which the repair has made, if it is still that one and empty; nil if it was there
}

// discard removes what a copy that failed has made, and nothing else, as far as it can: a logdb/ that was there before, or
// that came from somewhere else while the copy ran, is not its to remove.
func (k *keepDir) discard() {
	for _, name := range slices.Backward(k.created) {
		_ = k.fs.Remove(name)
	}
	k.created = nil
	if k.made != nil {
		k.made()
	}
}

// keepMaker makes the directory of the copy: it checks that the directory is one that can take it, and makes it if it is
// missing.
type keepMaker func() (*keepDir, error)

// keepCheck is what checkKeep saw of the directory of the copy.
type keepCheck struct {
	info   fs.FileInfo // the directory, as lstat(2) tells it; nil if it is missing
	parent fs.FileInfo // the directory that holds the missing directory; nil if it is there
}

// checkKeep checks that the directory keep on the OS can take the copy of logdb/ of the data directory data: it is not in
// it, as the kernel resolves the names, and is missing, which a parent that exists makes, or empty. It is not a symbolic link
// itself: a name that another process can point elsewhere between this check and the use of it is not one to put a copy in.
// The directories that hold it may be links.
func checkKeep(data, keep string) (keepCheck, error) {
	keep = fspath.Clean(keep)
	if dev, ino, ok, err := fspath.FileID(data); err != nil {
		return keepCheck{}, err
	} else if ok {
		in, err := fspath.In(keep, dev, ino)
		if err != nil {
			return keepCheck{}, err
		}
		if in {
			if d, i, _, err := fspath.FileID(keep); err == nil && d == dev && i == ino {
				return keepCheck{}, keepIsData(keep)
			}
			return keepCheck{}, keepInData(keep)
		}
	}
	info, err := os.Lstat(keep)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// Not made with its parents: a parent that is missing is a mistake in -keep.
		parent, err := os.Stat(filepath.Dir(keep))
		if err != nil {
			return keepCheck{}, err
		}
		if !parent.IsDir() {
			return keepCheck{}, fmt.Errorf("%s: not a directory; -keep needs a missing or empty directory in a directory", filepath.Dir(keep))
		}
		return keepCheck{parent: parent}, nil
	case err != nil:
		return keepCheck{}, err
	case info.Mode()&fs.ModeSymlink != 0:
		return keepCheck{}, fmt.Errorf("%s: a symbolic link; -keep needs a missing or empty directory itself, not a link to one", keep)
	case !info.IsDir():
		return keepCheck{}, fmt.Errorf("%s: not a directory; -keep needs a missing or empty directory", keep)
	}
	entries, err := os.ReadDir(keep)
	if err != nil {
		return keepCheck{}, err
	}
	if len(entries) > 0 {
		return keepCheck{}, fmt.Errorf("%s: not empty; -keep needs a missing or empty directory", keep)
	}
	return keepCheck{info: info}, nil
}

// osKeep makes the keepMaker of the directory keep on the OS, for the data directory data, which is the directory of root.
func osKeep(data vfs.FS, keep string) keepMaker {
	return func() (*keepDir, error) {
		c, err := checkKeep(data.Name(), keep)
		if err != nil {
			return nil, err
		}
		dataInfo, err := data.Lstat(".")
		if err != nil {
			return nil, err
		}
		return openKeep(dataInfo, keep, c)
	}
}

func keepIsData(keep string) error {
	return fmt.Errorf("%s: -keep is the data directory: the copy of logdb/ must be kept apart from it", keep)
}

func keepInData(keep string) error {
	return fmt.Errorf("%s: -keep is in the data directory: the copy of logdb/ must be kept apart from it", keep)
}

// keepAbove is the refusal of a directory whose directories above it cannot be looked at.
func keepAbove(keep string, err error) error {
	return fmt.Errorf("%s: cannot tell whether -keep is in the data directory: %w", keep, err)
}

// keepChanged is the refusal of a directory that is not the one that checkKeep saw.
func keepChanged(keep string) error {
	return fmt.Errorf("%s: changed while the repair opened it: run the repair again", keep)
}

// openKeep opens the directory keep, which checkKeep has checked, and makes it if c says that it is missing. It checks the
// directory that it opened, through the open directory, and not by its name: it is the one that checkKeep saw (or that this
// run has made), it is not the data directory, whose directory is dataInfo, or in it, and it is empty. The names of what it
// checked can lead elsewhere by now. A directory that it fails to take is removed again, if this run has made it.
func openKeep(dataInfo fs.FileInfo, keep string, c keepCheck) (*keepDir, error) {
	keep = fspath.Clean(keep)
	var parent vfs.FS
	var made func()
	want := c.info
	if c.info == nil {
		var err error
		if parent, made, want, err = makeKeep(keep, c.parent, dataInfo); err != nil {
			return nil, err
		}
	}
	closeAll := func() {
		if made != nil {
			made()
		}
		if parent != nil {
			_ = parent.Close()
		}
	}
	var root vfs.FS
	var err error
	if parent != nil {
		root, err = parent.Sub(filepath.Base(keep))
	} else {
		root, _, err = dataRoot(keep)
	}
	if err != nil {
		closeAll()
		return nil, err
	}
	if err := checkOpenedKeep(root, keep, dataInfo, want); err != nil {
		_ = root.Close()
		closeAll()
		return nil, err
	}
	return &keepDir{fs: &keepFS{FS: root, parent: parent}, name: keep, made: made}, nil
}

// makeKeep makes the directory keep, which checkKeep found missing, in its parent, which is opened first and compared with
// the parent that checkKeep saw, and checked not to be the data directory, whose directory is data, or in it: the directory is
// made where the check looked, whatever the names lead to now, and it is made durable there. It returns the open parent, what
// removes the directory again, and the directory that it made.
func makeKeep(keep string, checked, data fs.FileInfo) (parent vfs.FS, remove func(), made fs.FileInfo, err error) {
	parent, _, err = dataRoot(filepath.Dir(keep))
	if err != nil {
		return nil, nil, nil, err
	}
	fail := func(err error) (vfs.FS, func(), fs.FileInfo, error) {
		_ = parent.Close()
		return nil, nil, nil, err
	}
	if info, err := parent.Lstat("."); err != nil {
		return fail(err)
	} else if !os.SameFile(info, checked) {
		return fail(keepChanged(filepath.Dir(keep)))
	}
	if level, err := dataLevel(parent, data); err != nil {
		return fail(keepAbove(keep, err))
	} else if level >= 0 {
		return fail(keepInData(keep))
	}
	if remove, made, err = mkdirDurable(parent, filepath.Base(keep)); err != nil {
		return fail(err)
	}
	return parent, remove, made, nil
}

// mkdirDurable makes the directory base in parent, and syncs parent. It returns what removes the directory again, if it is still
// the one that it made and is empty, and the directory that it made. It is not MkdirAll: a parent that is missing is a
// mistake in -keep, not something to make.
func mkdirDurable(parent vfs.FS, base string) (remove func(), made fs.FileInfo, err error) {
	if err := parent.Mkdir(base, 0o700); err != nil {
		return nil, nil, err
	}
	if made, err = parent.Lstat(base); err != nil {
		return nil, nil, err
	}
	remove = func() {
		if now, err := parent.Lstat(base); err == nil && os.SameFile(now, made) {
			_ = parent.Remove(base)
		}
	}
	if err := parent.SyncDir("."); err != nil {
		remove()
		return nil, nil, err
	}
	return remove, made, nil
}

// checkOpenedKeep checks the open directory root of the copy.
func checkOpenedKeep(root vfs.FS, keep string, dataInfo, want fs.FileInfo) error {
	info, err := root.Lstat(".")
	if err != nil {
		return err
	}
	switch {
	case os.SameFile(info, dataInfo):
		return keepIsData(keep)
	case !os.SameFile(info, want):
		return keepChanged(keep)
	}
	if level, err := dataLevel(root, dataInfo); err != nil {
		return keepAbove(keep, err)
	} else if level >= 0 {
		return keepInData(keep)
	}
	entries, err := root.ReadDir(".")
	if err != nil {
		return err
	}
	if len(entries) > 0 {
		return fmt.Errorf("%s: not empty; -keep needs a missing or empty directory", keep)
	}
	return nil
}

// keepFS is the open directory of the copy, with the directory that holds it if the repair made it there.
type keepFS struct {
	vfs.FS
	parent vfs.FS
}

func (k *keepFS) Close() error {
	err := k.FS.Close()
	if k.parent != nil {
		err = errors.Join(err, k.parent.Close())
	}
	return err
}

// copyLogdb copies every file of src, the directory logdb/ of the data directory, and the directories, to logdb/ of k.fs,
// which must have none, and makes them durable: the files, then the directories. It records what it makes in k.created. The
// files are copied as they are, not read as the engine reads them. A failed sync is no failure of the data directory, which
// the copy does not write in.
func (k *keepDir) copyLogdb(ctx context.Context, src vfs.FS) error {
	if err := k.fs.Mkdir("logdb", 0o700); err != nil {
		return err
	}
	k.created = append(k.created, "logdb")
	dirs := []string{"logdb"}
	if err := k.copyDir(ctx, src, ".", "logdb", &dirs); err != nil {
		return err
	}
	for _, d := range slices.Backward(dirs) {
		if err := k.fs.SyncDir(d); err != nil {
			return err
		}
	}
	return k.fs.SyncDir(".")
}

func (k *keepDir) copyDir(ctx context.Context, src vfs.FS, from, to string, dirs *[]string) error {
	entries, err := src.ReadDir(from)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		in, out := path.Join(from, e.Name()), path.Join(to, e.Name())
		switch {
		case e.IsDir():
			if err := k.fs.Mkdir(out, 0o700); err != nil {
				return err
			}
			k.created = append(k.created, out)
			*dirs = append(*dirs, out)
			if err := k.copyDir(ctx, src, in, out, dirs); err != nil {
				return err
			}
		case e.Type().IsRegular():
			if err := k.copyFile(ctx, src, in, out); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%s: not a regular file or a directory, which a copy cannot hold", path.Join(src.Name(), in))
		}
	}
	return nil
}

func (k *keepDir) copyFile(ctx context.Context, src vfs.FS, from, to string) error {
	in, err := src.Open(from)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := k.fs.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	k.created = append(k.created, to)
	if _, err := io.CopyBuffer(out, ctxReader{ctx, in}, make([]byte, 128<<10)); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// An auditLog is what the repair reads of one audit log in the state that it keeps, and its walk over the kept events.
type auditLog struct {
	name  string
	start *dbwire.ChainStart
	holes map[uint64]dbwire.ChainHole
	gaps  map[uint64]dbwire.ChainHole // the accepted gaps
	bad   string                      // why a row of the table chains cannot be read, "" if every row can
	head  *auditHead

	last, lastEvent uint64 // the last record that was read, and its event; 0: none
	dropped         uint64 // the unreadable events that the repair drops, met after the last record
}

type auditHead struct {
	n    uint64
	hash [32]byte
}

func readAuditLog(tx *logdb.Tx, name string) *auditLog {
	l := &auditLog{name: name}
	rows := readChainRows(tx, name)
	l.start, l.holes, l.gaps, l.bad = rows.start, rows.holes, rows.gaps, rows.bad
	l.head = readHead(tx, name)
	return l
}

// readHead returns the head of the audit log named name that the table heads has, nil if it has none that can be read.
func readHead(tx *logdb.Tx, name string) *auditHead {
	if v, ok := tx.Get(dbwire.TableHeads, []byte(name)); ok {
		if log, n, hash, valid := dbwire.ParseHeadLine(v); valid && log == name {
			return &auditHead{n, hash}
		}
	}
	return nil
}

// lost is a set of event numbers that the plan loses: the ranges, ordered and apart.
type lost struct{ lo, hi []uint64 }

// lostEvents returns the event numbers that the plan loses. The ranges of the plan are bounds: they may hold numbers that an
// earlier compaction had dropped, so nothing is a hole for them alone.
func lostEvents(plan logdb.RepairPlan) lost {
	type span struct{ lo, hi uint64 }
	var all []span
	add := func(first, last uint64) {
		if first != 0 && last >= first {
			all = append(all, span{first, last})
		}
	}
	for _, s := range plan.Spans {
		add(s.FirstEvent, s.LastEvent)
	}
	if t := plan.Truncation; t != nil {
		add(t.FirstEvent, t.LastEvent)
	}
	slices.SortFunc(all, func(a, b span) int { return cmp.Or(cmp.Compare(a.lo, b.lo), cmp.Compare(a.hi, b.hi)) })
	var l lost
	for _, s := range all {
		if n := len(l.lo); n > 0 && s.lo <= l.hi[n-1]+1 {
			l.hi[n-1] = max(l.hi[n-1], s.hi)
			continue
		}
		l.lo, l.hi = append(l.lo, s.lo), append(l.hi, s.hi)
	}
	return l
}

// between returns how many numbers n with a < n < b are lost.
func (l lost) between(a, b uint64) uint64 {
	var n uint64
	for i := sort.Search(len(l.hi), func(i int) bool { return l.hi[i] > a }); i < len(l.hi) && l.lo[i] < b; i++ {
		if lo, hi := max(l.lo[i], a+1), min(l.hi[i], b-1); lo <= hi {
			n += hi - lo + 1
		}
	}
	return n
}

// classified is what an event of the log is to the repair.
type classified struct {
	record     bool // a record of an audit log: log, n and prev
	log        string
	n          uint64
	prev       string
	unreadable bool // neither a valid event of the daemon nor a record
}

// classify is the one decision of what an event is, which the walk that finds the holes and the rewrite that drops the
// unreadable events both use.
func classify(data []byte) classified {
	if dbwire.IsDaemonEvent(data) {
		return classified{unreadable: !dbwire.ValidEvent(data)}
	}
	log, n, prev, ok := dbwire.ReadRecordID(data)
	return classified{record: ok, log: log, n: n, prev: prev, unreadable: !ok}
}

// auditWalk is the walk of the repair over the events that it keeps, which finds the records that it loses.
type auditWalk struct {
	logs map[string]*auditLog
	lost lost
	next uint64 // the number after the last event of the kept state
	drop bool
	rep  *RepairReport
	err  error
}

func (w *auditWalk) event(e logdb.Event) bool {
	c := classify(e.Data)
	switch {
	case c.unreadable:
		w.rep.UnreadableTotal++
		if len(w.rep.Unreadable) < maxUnreadableListed {
			w.rep.Unreadable = append(w.rep.Unreadable, e.Number)
		}
		if w.drop {
			for _, l := range w.logs {
				l.dropped++
			}
		}
	case c.record:
		w.record(w.logs[c.log], c, e.Number)
	}
	return w.err == nil
}

func (w *auditWalk) record(l *auditLog, c classified, number uint64) {
	expected := dbwire.ChainExpected(l.start, l.last)
	if c.n > expected {
		w.skip(l, c, number, expected)
	}
	l.last, l.lastEvent, l.dropped = c.n, number, 0
}

// skip finds what the record c, which has the event number number, skips: the log expected the record expected. The records
// that it skips are a hole of the repair only if the repair drops at least as many events between the two records as there
// are records missing: one audit record is one event, so with fewer, a gap that was there before the repair is part of them.
func (w *auditWalk) skip(l *auditLog, c classified, number, expected uint64) {
	if l.bad != "" {
		w.err = fmt.Errorf("audit log %q: %s: the repair cannot tell which of its holes are recorded", l.name, l.bad)
		return
	}
	switch _, _, recorded := dbwire.ChainPass(l.holes, l.gaps, l.start, l.last, c.n, c.prev); {
	case recorded:
	case l.dropped+w.lost.between(l.lastEvent, number) >= c.n-expected:
		h := RepairHole{Log: l.name, First: expected, Last: c.n - 1}
		h.Hash, h.Row = parseHash(c.prev)
		w.rep.Holes = append(w.rep.Holes, h)
	default:
		w.rep.Gaps = append(w.rep.Gaps, RepairGap{Log: l.name, First: expected, Last: c.n - 1})
	}
}

// finish finds the records that the head of each log names after its last record, which the repair loses, and puts the holes
// and the gaps in order.
func (w *auditWalk) finish() error {
	if w.err != nil {
		return w.err
	}
	for _, name := range repairLogs {
		l := w.logs[name]
		expected := dbwire.ChainExpected(l.start, l.last)
		if l.head == nil || l.head.n < expected {
			continue
		}
		if _, _, ok := dbwire.ChainEndPass(l.holes, l.gaps, l.start, l.last, l.head.n, l.head.hash); ok {
			continue
		}
		if l.bad != "" {
			return fmt.Errorf("audit log %q: %s: the repair cannot tell which of its holes are recorded", l.name, l.bad)
		}
		// The records that the head names after the last one that was read, against the events dropped after it: the records were
		// written in the commits of the kept state, so the events of the commits after it are none of them.
		if l.dropped+w.lost.between(l.lastEvent, w.next) >= l.head.n-expected+1 {
			w.rep.Holes = append(w.rep.Holes, RepairHole{Log: name, First: expected, Last: l.head.n, Hash: l.head.hash, Row: true})
		} else {
			w.rep.Gaps = append(w.rep.Gaps, RepairGap{Log: name, First: expected, Last: l.head.n, End: true})
		}
	}
	order := func(a, b string) int {
		return cmp.Compare(slices.Index(repairLogs[:], a), slices.Index(repairLogs[:], b))
	}
	slices.SortStableFunc(w.rep.Holes, func(a, b RepairHole) int {
		if c := order(a.Log, b.Log); c != 0 {
			return c
		}
		return cmp.Compare(a.First, b.First)
	})
	slices.SortStableFunc(w.rep.Gaps, func(a, b RepairGap) int {
		if c := order(a.Log, b.Log); c != 0 {
			return c
		}
		return cmp.Compare(a.First, b.First)
	})
	return nil
}

// parseHash returns the hash that the prev of a record names, which is "sha256:" and 64 lower case hexadecimal digits.
func parseHash(prev string) (hash [32]byte, ok bool) {
	digits, found := strings.CutPrefix(prev, "sha256:")
	if !found || len(digits) != 2*len(hash) || strings.Trim(digits, "0123456789abcdef") != "" {
		return hash, false
	}
	if _, err := hex.Decode(hash[:], []byte(digits)); err != nil {
		return [32]byte{}, false
	}
	return hash, true
}
