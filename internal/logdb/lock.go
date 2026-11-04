package logdb

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// The locks are taken in the order lock/entry, lock/readers, lock/writer, everywhere.

// lockPoll locks name in mode, and while another holds it, tries again after 10 ms, then less often up to every
// maxDelay, until ctx ends.
func lockPoll(ctx context.Context, fsys vfs.FS, name string, mode vfs.LockMode, maxDelay time.Duration) (func() error, error) {
	delay := min(10*time.Millisecond, maxDelay)
	for {
		unlock, err := fsys.Lock(name, mode)
		if !errors.Is(err, vfs.ErrLocked) {
			return unlock, err
		}
		t := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		case <-t.C:
		}
		delay = min(2*delay, maxDelay)
	}
}

// acquireWriter takes the locks of the writer: lock/entry shared while it takes the others, lock/readers shared, and
// lock/writer exclusive, which it waits for up to wait. It returns the function that releases them.
func acquireWriter(fsys vfs.FS, wait time.Duration) (func() error, error) {
	dir := fsys.Name()
	entry, err := fsys.Lock("lock/entry", vfs.Shared)
	if err != nil {
		return nil, lockErr(dir, err)
	}
	defer func() { _ = entry() }()
	readers, err := fsys.Lock("lock/readers", vfs.Shared)
	if err != nil {
		return nil, lockErr(dir, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	writer, err := lockPoll(ctx, fsys, "lock/writer", vfs.Exclusive, 10*time.Millisecond)
	if err != nil {
		_ = readers()
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("%s: another process holds the writer lock", dir)
		}
		return nil, err
	}
	return func() error { return errors.Join(writer(), readers()) }, nil
}

// lockErr is the error of a shared lock that an opener does not wait for: only maintenance holds it exclusively.
func lockErr(dir string, err error) error {
	if errors.Is(err, vfs.ErrLocked) {
		return fmt.Errorf("%s: maintenance is running", dir)
	}
	return err
}
