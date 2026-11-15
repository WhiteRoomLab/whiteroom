package main

import (
	"github.com/WhiteRoomLab/whiteroom/internal/store"
	"github.com/WhiteRoomLab/whiteroom/internal/store/storetest"
)

// This file goes away in phase 14 of the plan of issue 109. Until whiteroom serve admin connects to whiteroom-db, it reaches
// its data directory through a daemon inside its own process, which the first store of a directory starts and the last one
// ends.

func openStore(dir string) (*store.Store, error) { return storetest.Open(dir) }
