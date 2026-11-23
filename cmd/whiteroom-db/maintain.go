package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/WhiteRoomLab/whiteroom/internal/dbserver"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
)

const initUsage = `Usage: whiteroom-db init [flags]

Make the data directory, which must be missing or empty, with the files of a store
that has no data yet; the parents that are missing are made too. "whiteroom-db
serve" serves it, and the first commit of its writer makes the first row.

With -config, the item data of the YAML file gives what -data leaves out; the file
may hold other items, which are checked and not used. Without it, no config file
is read.

Flags:
`

const compactUsage = `Usage: whiteroom-db compact [flags]

Write the state of the data directory as a new generation of its log, and remove
the old one. Nothing is dropped: the tables, the objects and the events, with their
numbers, are those that the directory had.

No daemon may have the data directory open: stop "whiteroom-db serve" on it first.
If one has, the command waits for about two seconds, and then fails without
changing anything.

After a sync has failed on the data directory (in the daemon, or in this
command), the command refuses the directory until the machine has restarted, and
says so on the standard error: what the sync was to write may be in the page
cache only. The refusal changes nothing. After the restart the command removes
the mark <DATA>/stopped, which the failure left, and says so on the standard
error.

With -config, the item data of the YAML file gives what -data leaves out; the file
may hold other items, which are checked and not used. Without it, no config file
is read.

Flags:
`

const gcUsage = `Usage: whiteroom-db gc [flags]

Remove the files of the data directory that its state does not need: the segments
of the log of other generations, the files of objects that no commit names, and
the temporary files that a failure or a crash left.

No daemon may have the data directory open: stop "whiteroom-db serve" on it first.
If one has, the command waits for about two seconds, and then fails without
changing anything.

After a sync has failed on the data directory (in the daemon, or in this
command), the command refuses the directory until the machine has restarted, and
says so on the standard error: what the sync was to write may be in the page
cache only. The refusal changes nothing. After the restart the command removes
the mark <DATA>/stopped, which the failure left, and says so on the standard
error.

With -config, the item data of the YAML file gives what -data leaves out; the file
may hold other items, which are checked and not used. Without it, no config file
is read.

Flags:
`

// storeOptions are the options of the engine of a store, which serve uses too.
func storeOptions() dbserver.Options {
	return dbserver.Options{Log: logdb.Options{Placement: logdb.Hybrid}}
}

// maintainOptions are storeOptions with the lines that the maintenance logs, on stderr after the name of command.
func maintainOptions(e env, command string) dbserver.Options {
	o := storeOptions()
	o.Logf = func(format string, args ...any) { printf(e.stderr, "%s: %s\n", command, fmt.Sprintf(format, args...)) }
	return o
}

// setup parses the arguments of init, compact and gc, reads the config file, and finds the data directory. If the command
// is to end, it returns false and the exit code.
func setup(name, usage string, args []string, e env) (data string, code int, ok bool) {
	flags := newFlagSet(name, usage, e.stderr)
	var config, flagData string
	flags.StringVar(&config, "config", "", "read the settings from the YAML file `FILE`; the flags override them")
	flags.StringVar(&flagData, "data", "", "use the data directory `DIR` (required unless the config file has data)")
	if code, ok := parse(flags, args); !ok {
		return "", code, false
	}
	cfg, ok := loadConfig(e.stderr, flags.Name(), config)
	if !ok {
		return "", 2, false
	}
	data, _ = setting(flags, "data", flagData, cfg, "data", cfg.Data)
	if data == "" {
		return "", usageError(flags, "no data directory: give -data DIR, or data in the config file"), false
	}
	return data, 0, true
}

// failed prints the error err of command, "interrupted" for that of the context when it is done, and with the way out if
// the data directory is in use, and returns the exit code 1.
func failed(ctx context.Context, e env, command string, err error) int {
	switch {
	case ctx.Err() != nil && errors.Is(err, ctx.Err()):
		printf(e.stderr, "%s: interrupted\n", command)
	case errors.Is(err, dbserver.ErrInUse):
		printf(e.stderr, "%s: %v; stop whiteroom-db serve on it first\n", command, err)
	default:
		printf(e.stderr, "%s: %v\n", command, err)
	}
	return 1
}

// runInit runs whiteroom-db init with args, those after init.
func runInit(ctx context.Context, args []string, e env) int {
	data, code, ok := setup("whiteroom-db init", initUsage, args, e)
	if !ok {
		return code
	}
	if err := dbserver.CreateDir(data, storeOptions()); err != nil {
		return failed(ctx, e, "whiteroom-db init", err)
	}
	printf(e.stdout, "created the data directory %s\n", data)
	return 0
}

// runCompact runs whiteroom-db compact with args, those after compact.
func runCompact(ctx context.Context, args []string, e env) int {
	data, code, ok := setup("whiteroom-db compact", compactUsage, args, e)
	if !ok {
		return code
	}
	err := dbserver.Maintain(ctx, data, maintainOptions(e, "whiteroom-db compact"), func(m *logdb.Maintenance) error {
		return m.Compact(ctx, logdb.Rewrite{})
	})
	if err != nil {
		return failed(ctx, e, "whiteroom-db compact", err)
	}
	printf(e.stdout, "compacted %s\n", data)
	return 0
}

// runGC runs whiteroom-db gc with args, those after gc.
func runGC(ctx context.Context, args []string, e env) int {
	data, code, ok := setup("whiteroom-db gc", gcUsage, args, e)
	if !ok {
		return code
	}
	var res logdb.GCResult
	err := dbserver.Maintain(ctx, data, maintainOptions(e, "whiteroom-db gc"), func(m *logdb.Maintenance) (err error) {
		res, err = m.CollectGarbage(ctx)
		return err
	})
	if err != nil {
		return failed(ctx, e, "whiteroom-db gc", err)
	}
	printf(e.stdout, "collected garbage in %s: %d segments, %d object files, %d temporary files\n", data, res.Segments,
		res.Objects, res.Temps)
	return 0
}
