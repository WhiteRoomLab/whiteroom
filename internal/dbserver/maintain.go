package dbserver

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/fspath"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// ErrInUse is wrapped by the error of Maintain for a data directory that a process has open.
var ErrInUse = errors.New("the data directory is in use")

// lockWait is how long Maintain waits for the processes that have the data directory open to close it.
const lockWait = 2 * time.Second

// Maintain calls f with the engine of the data directory dir, which no daemon or other process has open: it is
// logdb.Maintain, with the checks of OpenDir, and with the rule of Open for the table schema. It waits for the other
// processes for up to two seconds, and then fails with an error that wraps ErrInUse and changes nothing. f runs without
// that limit, and has to use the context of its caller for the maintenance that it asks for.
//
// It keeps the rule of Open for the mark of a failed sync (stopmark.go): it refuses a directory whose mark is of the boot
// of the machine (ErrNotRestarted), and one that this process cannot write in, before the engine changes a file, and
// removes the mark of an earlier boot. If the maintenance fails with an error that wraps logdb.ErrFailed, it leaves the
// mark: one that f returns, before the engine lets go of its locks, so that no daemon opens the directory between the
// failure and the mark.
func Maintain(ctx context.Context, dir string, o Options, f func(*logdb.Maintenance) error) error {
	root, opened, err := dataRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	o.osDir = opened
	return maintain(ctx, root, dir, o, f)
}

// maintain is Maintain on the data directory root, which stays the caller's; dir is its name in errors.
func maintain(ctx context.Context, root vfs.FS, dir string, o Options, f func(*logdb.Maintenance) error) error {
	o.Log.Accept = acceptSchema(fspath.Join(dir, "logdb"))
	return maintainWith(ctx, root, dir, o, false, func(ctx context.Context, fsys vfs.FS, entered func(), g func(*logdb.Maintenance) error) error {
		return logdb.Maintain(ctx, fsys, o.Log, func(m *logdb.Maintenance) error {
			entered()
			return g(m)
		})
	}, f)
}

// maintainWith is what the maintenances of the engine have in common: the checks of the data directory and of the mark of a
// failed sync, the wait of two seconds for the other processes, and the mark that a failure with the sentence of the engine
// leaves. open runs the maintenance of the engine in fsys, and calls entered once it holds the locks; f runs inside it.
//
// With readOnly it only looks: the engine's file system refuses every write, no mark is removed or left, and the directory
// need not be writable.
func maintainWith[E any](ctx context.Context, root vfs.FS, dir string, o Options, readOnly bool,
	open func(ctx context.Context, fsys vfs.FS, entered func(), f func(E) error) error, f func(E) error) error {
	boot, err := bootOf(o)
	if err != nil {
		return err
	}
	for _, name := range []string{"logdb", "files"} {
		if err := checkDir(root, dir, name); err != nil {
			return err
		}
	}
	if _, err := checkMark(root, dir, boot); err != nil {
		return err
	}
	if !readOnly {
		if err := checkWritable(dir, o.osDir); err != nil {
			return err
		}
		if o.Hook != nil {
			_ = o.Hook("maintain:checked")
		}
	}
	logFS, err := root.Sub("logdb")
	if err != nil {
		return err
	}
	defer func() { _ = logFS.Close() }()
	now := o.Now
	if now == nil {
		now = time.Now
	}
	frozen := new(atomic.Bool)
	frozen.Store(readOnly)
	engineFS := vfs.FS(&writeFS{FS: logFS, to: new(atomic.Pointer[atomic.Uint64]), frozen: frozen})
	// leave writes the mark for the failure err, which a failed write of it is added to.
	leave := func(err error) error {
		if werr := writeMark(root, boot, now(), err); werr != nil {
			return fmt.Errorf("%w; %s", err, leaveText(dir, werr))
		}
		return err
	}

	waitCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	timer := time.AfterFunc(lockWait, cancel)
	defer timer.Stop()
	entered, marked := false, false
	var refused error
	err = open(waitCtx, engineFS, func() {
		entered = true
		timer.Stop()
	}, func(e E) error {
		// A process that stopped while this one waited for the locks has left its mark since the first look: the engine must
		// not write what the page cache has and the disk may not when it closes.
		earlier, err := checkMark(root, dir, boot)
		if err != nil {
			frozen.Store(true)
			refused = err
			return err
		}
		if earlier != nil && !readOnly {
			removeMark(root, dir, earlier, o.Logf)
		}
		err = f(e)
		if errors.Is(err, logdb.ErrFailed) && !readOnly {
			// Before the engine lets go of its locks: no daemon may open the directory between the failure and the mark.
			marked = true
			err = leave(err)
			if o.Hook != nil {
				_ = o.Hook("maintain:marked")
			}
		}
		return err
	})
	switch {
	case refused != nil:
		return refused
	case err != nil && !entered && ctx.Err() == nil && errors.Is(err, context.Canceled):
		return fmt.Errorf("%s: %w", dir, ErrInUse)
	case err != nil && !marked && !readOnly && errors.Is(err, logdb.ErrFailed):
		// The open of the engine failed, which has let go of its locks.
		return leave(err)
	}
	return err
}
