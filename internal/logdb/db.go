package logdb

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"sync/atomic"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// Options are the options of Create and Open. The zero value of a field is its default.
type Options struct {
	Placement    Placement     // of the data of new objects
	Threshold    int           // Hybrid: the size from which objects go in files; 0: 64 KiB
	SegmentBytes int64         // a record that would take a segment past it goes in a new one, unless it is the first; 0: 64 MiB
	WriterWait   time.Duration // how long Open waits for lock/writer, which readers hold briefly; 0: 2 s
	// Hook is called at points of the engine such as "update:written", for the tests. Its error makes the operation
	// fail at the points before a commit point; at the others it can only block.
	Hook func(point string) error
	// Accept is called by Open with a transaction that reads the recovered state, before Open changes a file. Its error
	// is that of Open.
	Accept func(tx *Tx) error
}

// fill checks o and sets the defaults of the fields that are zero.
func (o *Options) fill() error {
	switch o.Placement {
	case Inline, Files, Hybrid:
	default:
		return fmt.Errorf("unknown placement %d", o.Placement)
	}
	if o.Threshold < 0 {
		return fmt.Errorf("negative threshold %d", o.Threshold)
	}
	if o.Threshold == 0 {
		o.Threshold = 64 << 10
	}
	if o.SegmentBytes == 0 {
		o.SegmentBytes = 64 << 20
	}
	if o.WriterWait == 0 {
		o.WriterWait = 2 * time.Second
	}
	return nil
}

// RecoveryInfo is what Open found in the log.
type RecoveryInfo struct {
	Generation, Seq    uint64
	Checkpoint         string // "used", "none", "stale" or "damaged: " and why
	Replayed           int    // the records applied after the checkpoint, or from the start
	TruncatedAt        int64  // the offset where Open cut a torn tail off the last segment, or -1
	RemovedSegment     string // the path of a torn last segment that Open removed, or ""
	IgnoredGenerations []uint64
}

// DB is a store of the engine, opened by its writer. Its methods may be called concurrently.
type DB struct {
	fsys       vfs.FS
	o          Options
	cur        atomic.Pointer[state] // published: what Views see
	sem        chan struct{}         // held by the Update that runs, and by Close
	closed     atomic.Bool
	info       RecoveryInfo
	headErrors atomic.Int64
	handles    *handles

	// Guarded by sem.
	seg      *segment // the last segment, which the next record goes in
	head     headWriter
	logBytes int64
	failed   error // after a sync failed: the error of every Update
	unlock   func() error
	objDirs  map[string]bool // the directories of objects/ whose names are known to be durable
}

// Create makes a new store in fsys, which must be an empty directory, and opens it. If it fails after making
// something, fsys must be emptied before Create can try again.
func Create(fsys vfs.FS, o Options) (*DB, error) {
	if err := o.fill(); err != nil {
		return nil, err
	}
	entries, err := fsys.ReadDir(".")
	if err != nil {
		return nil, err
	}
	if len(entries) > 0 {
		return nil, fmt.Errorf("%s: not empty", fsys.Name())
	}
	if err := makeLayout(fsys); err != nil {
		return nil, err
	}
	return Open(fsys, o)
}

func makeLayout(fsys vfs.FS) error {
	for _, dir := range []string{"lock", "log", "objects"} {
		if err := fsys.Mkdir(dir, 0o700); err != nil {
			return err
		}
	}
	for _, name := range []string{"lock/entry", "lock/readers", "lock/writer"} {
		if err := createFile(fsys, name, nil); err != nil {
			return err
		}
	}
	seg, err := createSegment(fsys, 1, 1, 1)
	if err != nil {
		return err
	}
	if err := errors.Join(seg.f.Sync(), seg.f.Close()); err != nil {
		return err
	}
	st := &state{gen: 1, seg: 1, end: segHeaderSize}
	if err := createFile(fsys, "head", appendHeadSlot(appendHeadSlot(nil, headOf(st, 0)), headOf(st, 1))); err != nil {
		return err
	}
	for _, dir := range []string{"lock", "log", "objects", "."} {
		if err := fsys.SyncDir(dir); err != nil {
			return err
		}
	}
	return nil
}

// createFile makes the file name with data, synced unless it is empty.
func createFile(fsys vfs.FS, name string, data []byte) error {
	f, err := fsys.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if len(data) > 0 {
		if err := writeAt(f, data, 0); err != nil {
			_ = f.Close()
			return err
		}
		if err := f.Sync(); err != nil {
			_ = f.Close()
			return err
		}
	}
	return f.Close()
}

// Open opens the store in fsys as its writer: it takes the locks, recovers the state from the log, and makes durable
// what it goes on from.
func Open(fsys vfs.FS, o Options) (*DB, error) {
	if err := o.fill(); err != nil {
		return nil, err
	}
	if err := checkLayout(fsys); err != nil {
		return nil, err
	}
	unlock, err := acquireWriter(fsys, o.WriterWait)
	if err != nil {
		return nil, err
	}
	var r *recovery
	var h *handles
	opened := false
	defer func() {
		if opened {
			return
		}
		switch {
		case h != nil:
			_ = h.close() // it has the file of r.seg
			_ = r.head.f.Close()
		case r != nil:
			r.close()
		}
		_ = unlock()
	}()
	r, err = recoverLog(fsys)
	if err != nil {
		return nil, err
	}
	h = newHandles(fsys)
	h.add(r.seg.gen, r.seg.index, r.seg.f)
	db := &DB{fsys: fsys, o: o, sem: make(chan struct{}, 1), info: r.info, handles: h, seg: r.seg, head: r.head,
		logBytes: r.st.logBytes, unlock: unlock, objDirs: map[string]bool{}}
	if o.Accept != nil {
		if err := (&Tx{db: db, st: r.st}).run(o.Accept); err != nil {
			return nil, err
		}
	}
	if err := r.apply(fsys); err != nil {
		return nil, err
	}
	db.cur.Store(r.st)
	db.writeHead()
	opened = true
	return db, nil
}

// Close closes the store: unless it has failed, it writes and syncs head first. Then View and Update fail; the Views
// that run go on until they end.
func (db *DB) Close() error {
	db.sem <- struct{}{}
	defer func() { <-db.sem }()
	if db.closed.Load() {
		return nil
	}
	db.closed.Store(true)
	var err error
	if db.failed == nil {
		err = db.head.write(db.cur.Load())
		if err == nil {
			err = db.head.f.Sync()
		}
	}
	return errors.Join(err, db.handles.close(), db.head.f.Close(), db.unlock())
}

// Recovery returns what Open found in the log.
func (db *DB) Recovery() RecoveryInfo {
	info := db.info
	info.IgnoredGenerations = slices.Clone(info.IgnoredGenerations)
	return info
}
