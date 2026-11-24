package logdb

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// Inspection is the store while Inspect runs, which no other process has open.
type Inspection struct {
	fsys vfs.FS
}

// Inspect waits, until ctx ends, for the other processes that have the store in fsys open to close it, and keeps those
// that open it from then on out, as Maintain does. Then it calls f, which checks the files with Check, and releases the
// locks; f must not use the Inspection after it returns. Unlike Maintain, it does not open the store: it opens no file
// for writing and changes none, and of o it only checks the values (Accept is not called).
func Inspect(ctx context.Context, fsys vfs.FS, o Options, f func(*Inspection) error) error {
	if err := o.fill(); err != nil {
		return err
	}
	if err := checkLayout(fsys); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	unlock, err := acquireMaintenance(ctx, fsys)
	if err != nil {
		return err
	}
	released := false
	defer func() {
		if !released {
			_ = unlock() // f has panicked
		}
	}()
	err = f(&Inspection{fsys: fsys})
	released = true
	if uerr := unlock(); uerr != nil {
		return errors.Join(err, uerr)
	}
	return err
}

// Check reports to found what is wrong in the files of a store that no process has open, until found returns false,
// and then returns nil: every change that Open would make to recover it (cut a torn tail, remove a torn new segment),
// what Open would ignore or not use (segments of an incomplete newer generation, a checkpoint that is damaged or of an
// older generation), a head that does not name the end of the log, and every problem that VerifyEach finds. A store
// that its writer closed, and a copy that Backup made, have none. It changes no file. A file of a newer version, and
// damage that stops the log from being read, are one problem, and nothing after it is read. It returns an error only
// for the end of ctx.
func (in *Inspection) Check(ctx context.Context, found func(Problem) bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fsys := in.fsys
	r, err := decide(fsys, nil, func(s segFile, off, end segOff) ([]byte, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return readRange(fsys, s.name, off, end, true)
	})
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	case err != nil:
		found(problemOf(err, ""))
		return nil
	}
	for _, p := range recoveryProblems(fsys.Name(), r) {
		if !found(p) {
			return nil
		}
	}
	if hp, stop := in.checkHead(r.st); hp != nil && !found(*hp) || stop {
		return nil
	}
	db := &DB{fsys: fsys, sem: make(chan struct{}, 1), handles: newHandles(fsys)}
	db.cur.Store(r.st)
	defer func() { _ = db.handles.close() }()
	return db.VerifyEach(ctx, found)
}

// recoveryProblems returns the changes that Open would make to recover r, which decide has found in the store in the
// directory name, and what it would not use.
func recoveryProblems(name string, r *recovery) []Problem {
	var ps []Problem
	if r.cut >= 0 {
		ps = append(ps, Problem{Path: path.Join(name, r.seg.name), Offset: int64(r.cut),
			Reason: "a torn tail, which Open would cut off"})
	}
	if r.remove != "" {
		ps = append(ps, Problem{Path: path.Join(name, r.remove), Offset: -1,
			Reason: "a new segment that has no valid header, which Open would remove"})
	}
	for _, gen := range r.info.IgnoredGenerations {
		ps = append(ps, Problem{Path: path.Join(name, "log"), Offset: -1, Reason: fmt.Sprintf(
			"generation %d is newer than that of the store and incomplete: Open ignores its segments, and CollectGarbage removes them", gen)})
	}
	ckp := Problem{Path: path.Join(name, "checkpoint"), Offset: -1}
	switch why, damaged := strings.CutPrefix(withoutKeys(r.info.Checkpoint), "damaged: "); {
	case damaged:
		ckp.Reason = fmt.Sprintf("damaged (%s): Open reads the log from the start of its generation", why)
		ps = append(ps, ckp)
	case r.info.Checkpoint == "stale":
		ckp.Reason = "of an older generation than that of the store: Open does not use it"
		ps = append(ps, ckp)
	}
	return ps
}

// checkHead reads head, which Open writes again if it is damaged, and returns the problem that it has, if any, and
// whether nothing more is to be read: a head of a newer version stops Open.
func (in *Inspection) checkHead(st *state) (*Problem, bool) {
	p := path.Join(in.fsys.Name(), "head")
	f, err := in.fsys.Open("head")
	var h headSlot
	if err == nil {
		h, err = readHead(f, p)
		_ = f.Close()
	}
	if err != nil {
		var ve *VersionError
		prob := problemOf(err, p)
		return &prob, errors.As(err, &ve)
	}
	if atHead(st, h) {
		return nil, false
	}
	return &Problem{Path: p, Offset: -1, Reason: headReason(h, st)}, false
}

// headReason says how the slot h of head differs from the last record of st.
func headReason(h headSlot, st *state) string {
	switch {
	case h.gen != st.gen:
		return fmt.Sprintf("head names generation %d, but the log ends in generation %d", h.gen, st.gen)
	case h.seq != st.seq:
		return fmt.Sprintf("head names sequence %d, but the log ends with sequence %d", h.seq, st.seq)
	}
	return fmt.Sprintf("head names the end %d of segment %d and CRC %d for sequence %d, but the log has %d, %d and %d",
		h.end, h.seg, h.crc, h.seq, st.end, st.seg, st.lastCRC)
}
