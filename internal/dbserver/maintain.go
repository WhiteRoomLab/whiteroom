package dbserver

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/fspath"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
)

// ErrInUse is wrapped by the error of Maintain for a data directory that a process has open.
var ErrInUse = errors.New("the data directory is in use")

// lockWait is how long Maintain waits for the processes that have the data directory open to close it.
const lockWait = 2 * time.Second

// Maintain calls f with the engine of the data directory dir, which no daemon or other process has open: it is
// logdb.Maintain, with the checks of OpenDir, and with the rule of Open for the table schema. It waits for the other
// processes for up to two seconds, and then fails with an error that wraps ErrInUse and changes nothing. f runs without
// that limit, and has to use the context of its caller for the maintenance that it asks for.
func Maintain(ctx context.Context, dir string, o Options, f func(*logdb.Maintenance) error) error {
	root, _, err := dataRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	for _, name := range []string{"logdb", "files"} {
		if err := checkDir(root, dir, name); err != nil {
			return err
		}
	}
	logFS, err := root.Sub("logdb")
	if err != nil {
		return err
	}
	defer func() { _ = logFS.Close() }()
	o.Log.Accept = acceptSchema(fspath.Join(dir, "logdb"))

	waitCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	timer := time.AfterFunc(lockWait, cancel)
	defer timer.Stop()
	entered := false
	err = logdb.Maintain(waitCtx, logFS, o.Log, func(m *logdb.Maintenance) error {
		entered = true
		timer.Stop()
		return f(m)
	})
	if err != nil && !entered && ctx.Err() == nil && errors.Is(err, context.Canceled) {
		return fmt.Errorf("%s: %w", dir, ErrInUse)
	}
	return err
}
