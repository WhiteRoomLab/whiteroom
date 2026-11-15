package store

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/fspath"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// The workload of the recording with dm-log-writes (dmlog_test.go): record.sh runs TestDMLogWorkload in the file system
// that it records.

const (
	envDMLogWorkload = "WHITEROOM_DMLOG_WORKLOAD" // the directory of the workload: the root of the recorded file system
	envDMLogPhase    = "WHITEROOM_DMLOG_PHASE"    // the number of the phase of a process of the workload
	envDMLogLoose    = "WHITEROOM_DMLOG_LOOSE"    // the syncs that the workload drops: files or dirs
)

// The directories of the workload in the recorded file system, with slashes: the data directory and the copy of Backup,
// in a directory that the workload makes too.
const (
	workloadTop    = "site"
	workloadData   = workloadTop + "/data"
	workloadBackup = workloadTop + "/backup"
)

// dmlogUpdates is the number of Updates of the workload of the recording.
const dmlogUpdates = 200

// workloadOptions are those of the store of the workload: the media documents in files and the terms inline, and
// segments and checkpoints small enough that the recording has rollovers and checkpoints.
func workloadOptions() options {
	return options{
		log: &logdb.Options{Placement: logdb.Hybrid, Threshold: 100, SegmentBytes: 16 << 10, CheckpointBytes: 32 << 10,
			SyncCheckpoints: true},
		now: func() time.Time { return testTime },
	}
}

// looseFS is an FS that drops the syncs of its files, or of its directories: a store on it is not durable, and a
// recording of the workload with it (envDMLogLoose) must not pass TestDMLogCheck.
type looseFS struct {
	vfs.FS
	files bool // else the directories
}

// looseRoots are the file systems that looseRoot has returned, by their directories: the daemon of a data directory is
// found by its file system, so that one directory has one.
var looseRoots sync.Map

// looseRoot returns the directory dir of the OS without the syncs that envDMLogLoose names, or nil if it names none.
func looseRoot(dir string) (vfs.FS, error) {
	loose := os.Getenv(envDMLogLoose)
	switch loose {
	case "":
		return nil, nil
	case "files", "dirs":
	default:
		return nil, fmt.Errorf("%s=%s: want files or dirs", envDMLogLoose, loose)
	}
	if fsys, ok := looseRoots.Load(dir); ok {
		return fsys.(vfs.FS), nil
	}
	root, err := vfs.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	fsys := looseFS{FS: root, files: loose == "files"}
	looseRoots.Store(dir, fsys)
	return fsys, nil
}

func (f looseFS) Open(name string) (vfs.File, error) {
	return f.OpenFile(name, os.O_RDONLY, 0)
}

func (f looseFS) OpenFile(name string, flag int, perm fs.FileMode) (vfs.File, error) {
	file, err := f.FS.OpenFile(name, flag, perm)
	if err != nil || !f.files {
		return file, err
	}
	return looseFile{file}, nil
}

func (f looseFS) SyncDir(name string) error {
	if f.files {
		return f.FS.SyncDir(name)
	}
	return nil
}

func (f looseFS) Sub(name string) (vfs.FS, error) {
	sub, err := f.FS.Sub(name)
	if err != nil {
		return nil, err
	}
	return looseFS{FS: sub, files: f.files}, nil
}

type looseFile struct{ vfs.File }

func (looseFile) Sync() error { return nil }

// workloadIn returns workloadOptions for the data directory dir, which is there: on the OS, or without the syncs that
// envDMLogLoose names.
func workloadIn(dir string) (options, error) {
	o := workloadOptions()
	fsys, err := looseRoot(dir)
	if fsys != nil {
		o.fs = fsys
	}
	return o, err
}

// workloadOriginal is the original that the workload puts before the Update n, a multiple of 10, which puts the media
// document workloadMedia(n) that names it.
func workloadOriginal(n int) []byte {
	return bytes.Repeat(fmt.Appendf(nil, "the original of the Update %d of the workload ", n), 50)
}

func workloadMedia(n int) string {
	return fmt.Sprintf("med_01JD%022d", n)
}

// workloadUpdate makes the Update n of the workload in s: the pair n and the record store.write of the pair n, and for
// a multiple of 10, the media document of workloadOriginal(n), which it puts before.
func workloadUpdate(ctx context.Context, s *Store, n int) error {
	var original []byte
	if n%10 == 0 {
		original = workloadOriginal(n)
		want := Original{Hash: sum(original), Size: int64(len(original))}
		switch o, err := s.PutOriginal(ctx, bytes.NewReader(original)); {
		case err != nil:
			return fmt.Errorf("PutOriginal() before the Update %d: %w", n, err)
		case o != want:
			return fmt.Errorf("PutOriginal() before the Update %d = %+v, want %+v", n, o, want)
		}
	}
	if err := s.Update(ctx, func(tx *Tx) error {
		if err := putPair(tx, n); err != nil {
			return err
		}
		if original != nil {
			id := workloadMedia(n)
			if _, err := tx.Put(content.RefMedia, id, mediaJSON(id, original), userID); err != nil {
				return err
			}
		}
		return tx.Record(workloadRecord(n))
	}); err != nil {
		return fmt.Errorf("Update() %d: %w", n, err)
	}
	return nil
}

// workloadRecord is the record of the Update n of the workload.
func workloadRecord(n int) audit.Record {
	return audit.Record{Log: audit.ActivityLog, Actor: userID, Action: audit.StoreWrite,
		Detail: map[string]string{"pair": strconv.Itoa(n)}}
}

// orphanUpdate makes an Update that no process of the workload ends: a media document, with its original, that no
// other Update puts. The process dies before the record of the Update, and the file of the document stays, for the GC.
func orphanUpdate(ctx context.Context, s *Store) error {
	original := []byte("an original that no document of the workload names")
	if _, err := s.PutOriginal(ctx, bytes.NewReader(original)); err != nil {
		return err
	}
	return s.Update(ctx, func(tx *Tx) error {
		id := workloadMedia(0)
		if _, err := tx.Put(content.RefMedia, id, mediaJSON(id, original), userID); err != nil {
			return err
		}
		return tx.Record(workloadRecord(0))
	})
}

// pairOf returns the number of the pair of s, after checkPairView.
func pairOf(ctx context.Context, s *Store) (int, error) {
	n := 0
	err := s.View(ctx, func(tx *Tx) error {
		var err error
		n, err = checkPairView(tx)
		return err
	})
	return n, err
}

// runWorkload makes the Updates from+1 to to of the workload in the store of the data directory dir, which it creates
// if from is 0, and closes it. It calls mark with "ack 0" after create, "ack <n>" after the Update n, and "closed"
// after Close.
func runWorkload(ctx context.Context, dir string, from, to int, mark func(text string) error) error {
	var s *Store
	var err error
	if from == 0 {
		if s, err = create(dir, workloadOptions()); err == nil {
			err = mark("ack 0")
		}
	} else {
		s, err = open(dir, workloadOptions())
	}
	if s == nil {
		return err
	}
	for n := from + 1; err == nil && n <= to; n++ {
		if err = workloadUpdate(ctx, s, n); err == nil {
			err = mark(fmt.Sprintf("ack %d", n))
		}
	}
	if err := errors.Join(err, s.Close()); err != nil {
		return err
	}
	return mark("closed")
}

// workloadPhase is what a process of the workload of the recording does.
type workloadPhase struct {
	kind   string // write: Updates of the writer; read: a View of a reader; maintain: a GC, a compaction and a GC
	create bool   // write: the process creates the store
	to     int    // write: the last Update before the process closes the store, or before it arms the point of kill
	kill   string // the point of the hook at which the process kills itself, or "": it ends by itself
	backup int    // write: the Update after which the process makes the copy of Backup, or 0
}

// armedUpdates is how many Updates a write phase makes at most with its point of kill armed.
const armedUpdates = 50

// workloadPhases are the processes of the workload of the recording, one after the other. A process with kill dies
// there of SIGKILL: what it has written stays in the page cache, as after any process that is killed, and the next
// process finds it.
var workloadPhases = []workloadPhase{
	// The record of the Update 41 is written, and not synced.
	{kind: "write", create: true, to: 40, kill: "logdb:update:written"},
	// A reader syncs the end of the log before it shows it.
	{kind: "read"},
	// The files of the objects of an Update are written, and not its record (orphanUpdate).
	{kind: "write", to: 69, kill: "logdb:update:objects"},
	// A checkpoint has its name, and its directory is not synced.
	{kind: "write", to: 100, kill: "logdb:checkpoint:renamed"},
	// The original of the Update 130 is in its temporary file.
	{kind: "write", to: 129, kill: "original:synced"},
	{kind: "write", to: 180, backup: 160},
	// The base of the generation 2 is written, and not its end: the generation is incomplete.
	{kind: "maintain", kill: "compact:base"},
	// The generation 2 is complete, and the checkpoint is that of the generation 1, whose segments are there.
	{kind: "maintain", kill: "compact:base-end"},
	{kind: "maintain"},
	{kind: "write", to: dmlogUpdates},
}

// TestDMLogWorkload is the workload of the recording, which record.sh runs with envDMLogWorkload set to the root of
// the file system that it records: the phases of workloadPhases, each in a process of its own, which is this test
// with envDMLogPhase. For each mark of runPhase, the process prints "dmlog: <text>", and waits for the reply "go" on
// stdin, which record.sh writes after it has set the mark of the words of the text joined by "-" (ack-<n>, closed):
// each mark is in the log before the workload goes on. On SIGTERM, the workload ends, with the process of its phase.
func TestDMLogWorkload(t *testing.T) {
	root := os.Getenv(envDMLogWorkload)
	if root == "" {
		t.Skip("the workload of internal/logdb/dmlog/record.sh, which sets " + envDMLogWorkload)
	}
	if v := os.Getenv(envDMLogPhase); v != "" {
		i, err := strconv.Atoi(v)
		if err != nil || i < 0 || i >= len(workloadPhases) {
			t.Fatalf("%s=%s: want the number of a phase", envDMLogPhase, v)
		}
		replies := bufio.NewReader(os.Stdin)
		if err := runPhase(t, root, workloadPhases[i], func(text string) error {
			fmt.Println("dmlog: " + text)
			switch reply, err := replies.ReadString('\n'); {
			case err != nil:
				return fmt.Errorf("the reply of record.sh to %q: %w", text, err)
			case reply != "go\n":
				return fmt.Errorf("record.sh has replied %q to %q", reply, text)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return
	}
	// record.sh ends the workload with SIGTERM when it stops, and then unmounts the file system: the process of the
	// phase, which has the store open, must have ended by then.
	ctx, stop := signal.NotifyContext(t.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	for i, ph := range workloadPhases {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDMLogWorkload$", "-test.count=1")
		cmd.Env = append(os.Environ(), envDMLogPhase+"="+strconv.Itoa(i))
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		err := cmd.Run()
		if ctx.Err() != nil {
			t.Fatalf("interrupted in the phase %d (%+v)", i, ph)
		}
		switch killed := killedProcess(err); {
		case ph.kill != "" && !killed:
			t.Fatalf("the phase %d (%+v) has ended with %v, want that it kills itself", i, ph, err)
		case ph.kill == "" && err != nil:
			t.Fatalf("the phase %d (%+v): %v", i, ph, err)
		}
		fmt.Printf("workload: the phase %d (%+v) has ended\n", i, ph)
	}
}

// killedProcess reports whether err is that of a process that SIGKILL has ended.
func killedProcess(err error) bool {
	ee, ok := errors.AsType[*exec.ExitError](err)
	if !ok {
		return false
	}
	ws, ok := ee.Sys().(syscall.WaitStatus)
	return ok && ws.Signaled() && ws.Signal() == syscall.SIGKILL
}

// killSelf ends the process as SIGKILL does: without closing or syncing anything.
func killSelf() {
	p, err := os.FindProcess(os.Getpid())
	if err == nil {
		err = p.Kill()
	}
	if err != nil {
		fmt.Println("workload: cannot kill the process:", err)
		os.Exit(3)
	}
	select {}
}

// runPhase runs the phase ph of the workload in root, the root of the recorded file system. It calls mark with what
// the checks of a recording go by (dmlogPoint.see): "ack <n>" after the Update n, and "ack 0" after create; open
// before a writer opens a store that is there, and "seen <n>" once a writer or a reader that has opened it sees the
// pair n; "backup <n>" after Backup of the store with the pair n; closed after the writer has closed the store;
// maintain before the maintenance, and after it, "generation <g>", the generation of the store, and closed.
func runPhase(t *testing.T, root string, ph workloadPhase, mark func(text string) error) error {
	t.Helper()
	dir := filepath.Join(root, filepath.FromSlash(workloadData))
	switch ph.kind {
	case "write":
		return writePhase(t, root, ph, mark)
	case "read":
		_, err := readPhase(t, dir, mark)
		return err
	case "maintain":
		return maintainPhase(t, dir, ph, mark)
	}
	return fmt.Errorf("a phase of the unknown kind %q", ph.kind)
}

// readPhase opens the store of dir as a reader, calls mark with "seen <n>" for its pair n, and closes it. It returns
// the generation of the store.
func readPhase(t *testing.T, dir string, mark func(text string) error) (uint64, error) {
	t.Helper()
	o, err := workloadIn(dir)
	if err != nil {
		return 0, err
	}
	s, err := openLogReader(dir, o)
	if err != nil {
		return 0, err
	}
	n, err := pairOf(t.Context(), s)
	if err == nil {
		err = mark(fmt.Sprintf("seen %d", n))
	}
	return logDB(t, s).Recovery().Generation, errors.Join(err, s.Close())
}

// openPhase creates the store of the workload in root, or opens it as its writer, and returns it with its pair.
func openPhase(t *testing.T, root string, ph workloadPhase, mark func(text string) error) (*Store, int, error) {
	t.Helper()
	dir := filepath.Join(root, filepath.FromSlash(workloadData))
	loose := os.Getenv(envDMLogLoose) != ""
	if ph.create && loose {
		// As create does on the OS, for the file system of workloadIn.
		if err := fspath.MakeDirs(dir); err != nil {
			return nil, 0, err
		}
	}
	o := workloadOptions()
	if !ph.create || loose {
		var err error
		if o, err = workloadIn(dir); err != nil {
			return nil, 0, err
		}
	}
	if ph.create {
		s, err := create(dir, o)
		if err != nil {
			return nil, 0, err
		}
		return s, 0, mark("ack 0")
	}
	if err := mark("open"); err != nil {
		return nil, 0, err
	}
	s, err := open(dir, o)
	if err != nil {
		return nil, 0, err
	}
	n, err := pairOf(t.Context(), s)
	if err == nil {
		err = mark(fmt.Sprintf("seen %d", n))
	}
	if err != nil {
		return nil, 0, errors.Join(err, s.Close())
	}
	return s, n, nil
}

func writePhase(t *testing.T, root string, ph workloadPhase, mark func(text string) error) error {
	t.Helper()
	ctx := t.Context()
	s, n, err := openPhase(t, root, ph, mark)
	if err != nil {
		return err
	}
	var armed atomic.Bool
	s.hook = func(point string) error {
		if armed.Load() && point == ph.kill {
			killSelf()
		}
		return nil
	}
	next := func() error {
		n++
		if err := workloadUpdate(ctx, s, n); err != nil {
			return err
		}
		return mark(fmt.Sprintf("ack %d", n))
	}
	for err == nil && n < ph.to {
		if err = next(); err == nil && n == ph.backup {
			if err = backupWorkload(ctx, root); err == nil {
				err = mark(fmt.Sprintf("backup %d", n))
			}
		}
	}
	if err != nil || ph.kill == "" {
		if err := errors.Join(err, s.Close()); err != nil {
			return err
		}
		return mark("closed")
	}
	armed.Store(true)
	switch {
	case ph.kill == "logdb:update:objects":
		err = orphanUpdate(ctx, s)
	case strings.HasPrefix(ph.kill, "logdb:checkpoint:"):
		// The checkpoint of the last Update, unless the Update has written it: then that of a later one.
		err = logDB(t, s).Checkpoint()
	}
	for i := 0; err == nil && i < armedUpdates; i++ {
		err = next()
	}
	return errors.Join(err, s.Close(), fmt.Errorf("the process has not reached %s", ph.kill))
}

// backupWorkload makes the copy of Backup of the store of the workload in root.
func backupWorkload(ctx context.Context, root string) error {
	src := filepath.Join(root, filepath.FromSlash(workloadData))
	dst := filepath.Join(root, filepath.FromSlash(workloadBackup))
	if os.Getenv(envDMLogLoose) == "" {
		_, err := backup(ctx, src, nil, dst, nil)
		return err
	}
	if err := fspath.MakeDirs(dst); err != nil {
		return err
	}
	srcFS, err := looseRoot(src)
	if err != nil {
		return err
	}
	dstFS, err := looseRoot(dst)
	if err != nil {
		return err
	}
	_, err = backup(ctx, src, srcFS, dst, dstFS)
	return err
}

// maintainPhase collects the garbage of the engine of the store of dir, which no process has open, compacts it and
// collects the garbage again.
func maintainPhase(t *testing.T, dir string, ph workloadPhase, mark func(text string) error) error {
	t.Helper()
	ctx := t.Context()
	if err := mark("maintain"); err != nil {
		return err
	}
	name := filepath.Join(dir, "logdb")
	fsys, err := looseRoot(name)
	if err == nil && fsys == nil {
		fsys, err = vfs.OpenRoot(name)
	}
	if err != nil {
		return err
	}
	defer func() { _ = fsys.Close() }()
	o := *workloadOptions().log
	o.Hook = func(point string) error {
		if point == ph.kill {
			killSelf()
		}
		return nil
	}
	if err := logdb.Maintain(ctx, fsys, o, func(m *logdb.Maintenance) error {
		for _, when := range []string{"before", "after"} {
			removed, err := m.CollectGarbage(ctx)
			if err != nil {
				return err
			}
			fmt.Printf("workload: the GC %s the compaction has removed %+v\n", when, removed)
			if when == "before" {
				if err := m.Compact(ctx, logdb.Rewrite{}); err != nil {
					return err
				}
			}
		}
		return nil
	}); err != nil {
		return err
	}
	if ph.kill != "" {
		return fmt.Errorf("the process has not reached %s", ph.kill)
	}
	gen, err := readPhase(t, dir, func(string) error { return nil })
	if err != nil {
		return err
	}
	if err := mark(fmt.Sprintf("generation %d", gen)); err != nil {
		return err
	}
	return mark("closed")
}
