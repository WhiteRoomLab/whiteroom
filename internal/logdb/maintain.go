package logdb

import (
	"context"
	"errors"

	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// Maintenance is the store while Maintain runs, which no other process has open.
type Maintenance struct {
	db *DB
}

// Maintain waits, until ctx ends, for the other processes that have the store in fsys open to close it, and keeps
// those that open it from then on out: Open and OpenReader fail at once while it runs. Then it opens the store as its
// writer, with o, calls f, and closes it; f must not use the Maintenance after it returns.
func Maintain(ctx context.Context, fsys vfs.FS, o Options, f func(*Maintenance) error) error {
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
	db, err := open(fsys, o, unlock)
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			_ = db.Close() // f has panicked
		}
	}()
	err = f(&Maintenance{db: db})
	closed = true
	if cerr := db.Close(); cerr != nil {
		return errors.Join(err, cerr)
	}
	return err
}

// View is the View of the store.
func (m *Maintenance) View(ctx context.Context, f func(*Tx) error) error {
	return m.db.View(ctx, f)
}

// Compact writes the state, as r changes it, as the base of a new generation, goes on in it, and removes the old one.
// A crash leaves the old generation or the new one, never a mix. It waits for a CollectGarbage that runs until ctx
// ends. An error before the new generation is complete leaves the store in the old one, and usable unless a sync has
// failed.
func (m *Maintenance) Compact(ctx context.Context, r Rewrite) error {
	db := m.db
	if err := db.turn(ctx); err != nil {
		return err
	}
	defer func() { <-db.sem }()
	return db.compact(ctx, r)
}

// turn takes sem for a maintenance operation, waiting for the one that runs until ctx ends.
func (db *DB) turn(ctx context.Context) error {
	if db.closed.Load() {
		return errClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case db.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
