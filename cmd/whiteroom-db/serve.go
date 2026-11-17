package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"os/user"
	"strconv"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/configfile"
	"github.com/WhiteRoomLab/whiteroom/internal/dbserver"
	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
)

const serveUsage = `Usage: whiteroom-db serve [flags]

Open the data directory, which "whiteroom-db init" has made and no other process
may open, and serve it on two Unix domain sockets until interrupted (SIGINT or
SIGTERM). It runs on Linux only.

The sockets writer.sock and reader.sock are made, with the mode 0660, in the
directory of -db, which must exist: serve does not make it. A socket file that an
earlier daemon left is replaced, and the files are removed when the daemon stops.
Who may connect is decided by the mode and the group of each socket file
(-writer-group, -reader-group) and by the permissions of the directory. Only the
Unix user of -writer-user, by default the user of this process, may write: another
user who connects to writer.sock is refused. Whoever connects to reader.sock reads.

It writes the heads of the chains of the audit logs to the standard error, a line
each: when it starts, then every minute those that have changed, and when it
stops, so that the journal keeps them outside the data directory. Its log lines go
there too, with the date and time, and tell no key or value of a row. A head that
cannot be written is tried again the next minute, and the daemon serves on, also
when the standard error is a pipe whose reader has ended.

The sizes of a frame, of a Commit and of an object are tied to the engine and to
the limits of the documents, and have no flag. The other limits are the flags
below; the default of each is the value that the daemon tells its clients.

With -config, the items data, db, writerUser, writerGroup, readerGroup,
maxReaders, commitRate, minFree, minFreeInodes, snapshotIdle, stall and maxStaged
of the YAML file give what the flags leave out; the file may hold the items of
whiteroom too, which are checked and not used. Without it, no config file is read.

Flags:
`

// serveFlags are the values of the flags of whiteroom-db serve.
type serveFlags struct {
	config, data, db                     string
	writerUser, writerGroup, readerGroup string
	maxReaders, commitRate               int
	minFree, minFreeInodes, maxStaged    int64
	snapshotIdle, stall                  time.Duration
}

func newServeFlags(stderr io.Writer) (*flag.FlagSet, *serveFlags) {
	flags := newFlagSet("whiteroom-db serve", serveUsage, stderr)
	def := dbwire.Limits{}.WithDefaults()
	var f serveFlags
	flags.StringVar(&f.config, "config", "", "read the settings from the YAML file `FILE`; the flags override them")
	flags.StringVar(&f.data, "data", "", "open the data directory `DIR` (required unless the config file has data)")
	flags.StringVar(&f.db, "db", "",
		"make the sockets in the directory `DIR`, which must exist (required unless the config file has db)")
	flags.StringVar(&f.writerUser, "writer-user", "",
		"let only the Unix user `USER`, a name or a number, write (default writerUser of the config file, else the user "+
			"of this process)")
	flags.StringVar(&f.writerGroup, "writer-group", "",
		"give writer.sock the group `GROUP`, a name or a number (default writerGroup of the config file, else the group "+
			"that the system gives)")
	flags.StringVar(&f.readerGroup, "reader-group", "",
		"give reader.sock the group `GROUP`, a name or a number (default readerGroup of the config file, else the group "+
			"that the system gives)")
	flags.IntVar(&f.maxReaders, "max-readers", def.Readers, "serve at most `N` readers at once, 1 or more")
	flags.IntVar(&f.commitRate, "commit-rate", def.CommitsPerSecond, "let the writer commit `N` times a second at most; 0 turns the limit off")
	flags.Int64Var(&f.minFree, "min-free", def.MinFree,
		"refuse a write that would leave fewer than `BYTES` free on the file system of the data directory; 0 turns the check off")
	flags.Int64Var(&f.minFreeInodes, "min-free-inodes", def.MinFreeInodes,
		"refuse a write that may make a file when the file system of the data directory has `N` free inodes or fewer, unless "+
			"it does not count them; 0 turns the check off")
	flags.DurationVar(&f.snapshotIdle, "snapshot-idle", def.SnapshotIdle,
		"release the snapshot of a connection that sends nothing for `DURATION`; 0 never releases")
	flags.DurationVar(&f.stall, "stall", def.Stall,
		"cut a connection that does not send or take bytes for `DURATION` in the middle of a request; 0 never cuts")
	flags.Int64Var(&f.maxStaged, "max-staged", def.Staged,
		"let a writer put at most `BYTES` of objects, in all, before it commits, 1 or more")
	return flags, &f
}

// serveSettings is what whiteroom-db serve works with.
type serveSettings struct {
	data, db             string
	writerUID            uint32
	writerGID, readerGID int // -1: as the system makes the socket
	limits               dbwire.Limits
}

// setting returns the value of a setting: the flag's if the command line gave it, else the config file's if it gives the
// item, else the flag's, which is the default. fromFile tells which, for the message of a problem with the value.
func setting[T any](flags *flag.FlagSet, name string, flagValue T, cfg configfile.File, item string, fileValue T) (v T, fromFile bool) {
	switch {
	case flagGiven(flags, name):
		return flagValue, false
	case cfg.Has(item):
		return fileValue, true
	}
	return flagValue, false
}

// resolve returns the settings from the flags, parsed into f, over the items of cfg, over the defaults. If one is wrong it
// prints it on stderr and returns false, which is a usage error.
func (f *serveFlags) resolve(flags *flag.FlagSet, cfg configfile.File, stderr io.Writer) (serveSettings, bool) {
	s := serveSettings{writerUID: uint32(os.Getuid()), writerGID: -1, readerGID: -1}
	s.data, _ = setting(flags, "data", f.data, cfg, "data", cfg.Data)
	s.db, _ = setting(flags, "db", f.db, cfg, "db", cfg.DB)
	switch {
	case s.data == "":
		usageError(flags, "no data directory: give -data DIR, or data in the config file")
		return s, false
	case s.db == "":
		usageError(flags, "no socket directory: give -db DIR, or db in the config file")
		return s, false
	}

	// A problem of a value, as a usage error for a flag, or as a problem of the item for the config file.
	bad := func(fromFile bool, name, item, msg string) bool {
		if fromFile {
			printConfigProblem(stderr, f.config, "/"+item, msg)
		} else {
			usageError(flags, "-%s: %s", name, msg)
		}
		return false
	}
	// id returns the number of the user or group that a setting names, or -1 if the setting is left out; if the name is wrong it
	// reports that and returns false.
	id := func(flagName, item, flagValue, fileValue string, find func(string) (string, error)) (int64, bool) {
		name, fromFile := setting(flags, flagName, flagValue, cfg, item, fileValue)
		if name == "" && !flagGiven(flags, flagName) {
			return -1, true
		}
		n, err := lookupID(name, find)
		if err != nil {
			return 0, bad(fromFile, flagName, item, err.Error())
		}
		return int64(n), true
	}
	uid, ok := id("writer-user", "writerUser", f.writerUser, cfg.WriterUser, lookupUser)
	if !ok {
		return s, false
	}
	if uid >= 0 {
		s.writerUID = uint32(uid)
	}
	wgid, ok := id("writer-group", "writerGroup", f.writerGroup, cfg.WriterGroup, lookupGroup)
	if !ok {
		return s, false
	}
	rgid, ok := id("reader-group", "readerGroup", f.readerGroup, cfg.ReaderGroup, lookupGroup)
	if !ok {
		return s, false
	}
	s.writerGID, s.readerGID = int(wgid), int(rgid)

	readers, fromFile := setting(flags, "max-readers", f.maxReaders, cfg, "maxReaders", cfg.MaxReaders)
	if readers < 1 {
		return s, bad(fromFile, "max-readers", "maxReaders", "must be an integer of 1 or more")
	}
	rate, fromFile := setting(flags, "commit-rate", f.commitRate, cfg, "commitRate", cfg.CommitRate)
	if rate < 0 {
		return s, bad(fromFile, "commit-rate", "commitRate", "must be an integer of 0 or more")
	}
	minFree, fromFile := setting(flags, "min-free", f.minFree, cfg, "minFree", cfg.MinFree)
	if minFree < 0 {
		return s, bad(fromFile, "min-free", "minFree", "must be an integer of 0 or more")
	}
	minFreeInodes, fromFile := setting(flags, "min-free-inodes", f.minFreeInodes, cfg, "minFreeInodes", cfg.MinFreeInodes)
	if minFreeInodes < 0 {
		return s, bad(fromFile, "min-free-inodes", "minFreeInodes", "must be an integer of 0 or more")
	}
	idle, fromFile := setting(flags, "snapshot-idle", f.snapshotIdle, cfg, "snapshotIdle", cfg.SnapshotIdle)
	if idle < 0 {
		return s, bad(fromFile, "snapshot-idle", "snapshotIdle", durationText)
	}
	stall, fromFile := setting(flags, "stall", f.stall, cfg, "stall", cfg.Stall)
	if stall < 0 {
		return s, bad(fromFile, "stall", "stall", durationText)
	}
	staged, fromFile := setting(flags, "max-staged", f.maxStaged, cfg, "maxStaged", cfg.MaxStaged)
	if staged < 1 {
		return s, bad(fromFile, "max-staged", "maxStaged", "must be an integer of 1 or more")
	}
	// A limit that 0 turns off is negative for the daemon, whose 0 is the default.
	s.limits = dbwire.Limits{Readers: readers, Staged: staged, CommitsPerSecond: offIfZero(rate), MinFree: offIfZero(minFree),
		MinFreeInodes: offIfZero(minFreeInodes), SnapshotIdle: offIfZero(idle), Stall: offIfZero(stall)}
	return s, true
}

func offIfZero[T ~int | ~int64](v T) T {
	if v == 0 {
		return -1
	}
	return v
}

const durationText = "must be a duration of 0 or more, such as 30s or 5m"

// lookupID returns the number that name stands for, a number in decimal or a name that find knows.
func lookupID(name string, find func(string) (string, error)) (uint32, error) {
	if name == "" {
		return 0, errors.New("must be a name or a number")
	}
	id, err := strconv.ParseUint(name, 10, 32)
	switch {
	case err == nil:
		return uint32(id), nil
	case errors.Is(err, strconv.ErrRange):
		return 0, errors.New("the number is over 4294967295")
	}
	text, err := find(name)
	if err != nil {
		return 0, err
	}
	if id, err = strconv.ParseUint(text, 10, 32); err != nil {
		return 0, fmt.Errorf("%q has no number that this program can use", name)
	}
	return uint32(id), nil
}

func lookupUser(name string) (string, error) {
	u, err := user.Lookup(name)
	if err != nil {
		var unknown user.UnknownUserError
		if errors.As(err, &unknown) {
			return "", fmt.Errorf("unknown user %q", name)
		}
		return "", fmt.Errorf("cannot look up the user %q: %w", name, err)
	}
	return u.Uid, nil
}

func lookupGroup(name string) (string, error) {
	g, err := user.LookupGroup(name)
	if err != nil {
		var unknown user.UnknownGroupError
		if errors.As(err, &unknown) {
			return "", fmt.Errorf("unknown group %q", name)
		}
		return "", fmt.Errorf("cannot look up the group %q: %w", name, err)
	}
	return g.Gid, nil
}

// runServe runs whiteroom-db serve with args, those after serve, until ctx is done.
func runServe(ctx context.Context, args []string, e env) int {
	if e.catchSIGPIPE != nil {
		e.catchSIGPIPE()
	}
	flags, f := newServeFlags(e.stderr)
	if code, ok := parse(flags, args); !ok {
		return code
	}
	cfg, ok := loadConfig(e.stderr, flags.Name(), f.config)
	if !ok {
		return 2
	}
	s, ok := f.resolve(flags, cfg, e.stderr)
	if !ok {
		return 2
	}
	// Run opens the data directory before it makes the sockets.
	if info, err := os.Stat(s.db); errors.Is(err, fs.ErrNotExist) {
		return failed(ctx, e, flags.Name(), fmt.Errorf("%s: the directory of the sockets does not exist; make it first, which serve does not", s.db))
	} else if err != nil {
		return failed(ctx, e, flags.Name(), err)
	} else if !info.IsDir() {
		return failed(ctx, e, flags.Name(), fmt.Errorf("%s: not a directory; the sockets are made in a directory that exists", s.db))
	}

	logger := log.New(e.stderr, "", log.LstdFlags)
	err := dbserver.Run(ctx, dbserver.Config{
		Data: s.data, Sockets: s.db, WriterUID: s.writerUID, WriterGID: s.writerGID, ReaderGID: s.readerGID,
		Options: dbserver.Options{Log: logdb.Options{Placement: logdb.Hybrid}, Limits: s.limits, Logf: logger.Printf},
		Heads:   e.stderr, Ticks: e.ticks,
		Ready: func() {
			printf(e.stdout, "serving the data directory %s on the sockets in %s (writer: user %d)\n", s.data, s.db, s.writerUID)
		},
	})
	if err != nil {
		return failed(ctx, e, flags.Name(), err)
	}
	return 0
}
