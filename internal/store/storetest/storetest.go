// Package storetest opens stores by their data directories, for the tests of other packages: each function starts the
// DB daemon of the directory inside the process (internal/dbserver/dbtest), or joins the one that runs, and connects a
// store to it. The daemon ends when the last store of it is closed. It is for tests only.
package storetest

import (
	"context"

	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/dbclient"
	"github.com/WhiteRoomLab/whiteroom/internal/dbserver/dbtest"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

// Create makes the data directory dir, which must be missing or an empty directory, as whiteroom-db init does, and the
// store in it, as store.Create does.
func Create(dir string) (*store.Store, error) {
	if err := dbtest.Init(dir, dbtest.Options{}); err != nil {
		return nil, err
	}
	d, err := Dialer(dir)
	if err != nil {
		return nil, err
	}
	return store.Create(d)
}

// Open opens the store of the data directory dir as its writer, as store.Open does.
func Open(dir string) (*store.Store, error) {
	d, err := Dialer(dir)
	if err != nil {
		return nil, err
	}
	return store.Open(d)
}

// OpenReader opens the store of the data directory dir to read it, as store.OpenReader does.
func OpenReader(dir string) (*store.Store, error) {
	d, err := Dialer(dir)
	if err != nil {
		return nil, err
	}
	return store.OpenReader(d)
}

// Backup copies the store of the data directory srcDir to dstDir, as store.Backup does.
func Backup(ctx context.Context, srcDir, dstDir string) ([]audit.Head, error) {
	d, err := Dialer(srcDir)
	if err != nil {
		return nil, err
	}
	return store.Backup(ctx, d, dstDir)
}

// Dialer returns the Dialer of the daemon of the data directory dir, which it starts or joins. Closing it, which a
// store or a client that was opened with it does, gives up the place that it has in the daemon.
func Dialer(dir string) (dbclient.Dialer, error) {
	h, err := dbtest.Acquire(dir, dbtest.Options{})
	if err != nil {
		return nil, err
	}
	return h.Dialer(), nil
}
