package main

import (
	"context"

	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
	"github.com/WhiteRoomLab/whiteroom/internal/store/storetest"
)

// This file goes away in phase 14 of the plan of issue 109. Until the commands connect to whiteroom-db, they reach the
// data directory through a daemon inside their own process, which the first store of a directory starts and the last one
// ends.

func createStore(dir string) (*store.Store, error)     { return storetest.Create(dir) }
func openStore(dir string) (*store.Store, error)       { return storetest.Open(dir) }
func openStoreReader(dir string) (*store.Store, error) { return storetest.OpenReader(dir) }
func backupStore(ctx context.Context, dir, dest string) ([]audit.Head, error) {
	return storetest.Backup(ctx, dir, dest)
}
