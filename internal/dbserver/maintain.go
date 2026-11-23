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
// of the machine (ErrNotRestarted) before the engine changes a file, and removes the mark of an earlier boot. If the
// maintenance fails with an error that wraps logdb.ErrFailed, it leaves the mark: one that f returns, before the engine lets
// go of its locks, so that no daemon opens the directory between the failure and the mark.
func Maintain(ctx context.Context, dir string, o Options, f func(*logdb.Maintenance) error) error {
	root, _, err := dataRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return maintain(ctx, root, dir, o, f)
}

// maintain is Maintain on the data directory root, which stays the caller's; dir is its name in errors.
func maintain(ctx context.Context, root vfs.FS, dir string, o Options, f func(*logdb.Maintenance) error) error {
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
	if o.Hook != nil {
		_ = o.Hook("maintain:checked")
	}
	logFS, err := root.Sub("logdb")
	if err != nil {
		return err
	}
	defer func() { _ = logFS.Close() }()
	o.Log.Accept = acceptSchema(fspath.Join(dir, "logdb"))
	now := o.Now
	if now == nil {
		now = time.Now
	}
	frozen := new(atomic.Bool)
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
	err = logdb.Maintain(waitCtx, engineFS, o.Log, func(m *logdb.Maintenance) error {
		entered = true
		timer.Stop()
		// A process that stopped while this one waited for the locks has left its mark since the first look: the engine must
		// not write what the page cache has and the disk may not when it closes.
		earlier, err := checkMark(root, dir, boot)
		if err != nil {
			frozen.Store(true)
			refused = err
			return err
		}
		if earlier != nil {
			removeMark(root, dir, earlier, o.Logf)
		}
		err = f(m)
		if errors.Is(err, logdb.ErrFailed) {
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
	case err != nil && !marked && errors.Is(err, logdb.ErrFailed):
		// The open of the engine failed, which has let go of its locks.
		return leave(err)
	}
	return err
}
