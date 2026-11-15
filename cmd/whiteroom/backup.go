package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"

	"github.com/WhiteRoomLab/whiteroom/internal/fspath"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

const backupUsage = `Usage: whiteroom backup [flags] DEST

Copy the data directory of the daemon whiteroom-db whose sockets are in the
directory of -db to DEST, which must be missing or an empty directory, and whose
parent must exist. The copy is the data directory as it was at the last commit
that was durable when the command began: a whiteroom serve admin may go on writing
meanwhile, and the command writes nothing in the store. The daemon copies it, and
the command writes DEST.

The copy is a data directory, which a whiteroom-db serve can run on: to check it,
run one on it with another directory of sockets, and give that to whiteroom
doctor. To restore it, stop the daemon, put the copy where its data directory is,
and start the daemon.

The copy holds all that the data directory holds, the hashes of the passwords and
the audit logs included: keep it as private as the data directory.

The command prints "backed up the store of DIR to DEST", and then the heads of the
chains of the audit logs of the copy, a line each, which hold no personal
information: keep them apart from the copy.

With -config, the item db of the YAML file gives what -db leaves out; the other
items are checked and not used: one file may be shared with whiteroom-db. Without
it, no config file is read.

Flags:
`

// runBackup runs whiteroom backup with args, those after backup.
func runBackup(ctx context.Context, args []string, e env) int {
	flags := flag.NewFlagSet("whiteroom backup", flag.ContinueOnError)
	flags.SetOutput(e.stderr)
	flags.Usage = func() {
		printf(flags.Output(), "%s", backupUsage)
		flags.PrintDefaults()
	}
	var configFile, dbDir string
	flags.StringVar(&configFile, "config", "", "read the settings from the YAML file `FILE`; the flags override them")
	flags.StringVar(&dbDir, "db", "", "connect to the daemon whose sockets are in the directory `DIR` (default db of the config file)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	switch flags.NArg() {
	case 0:
		return usageError(flags, "no destination: give DEST, after the flags")
	case 1:
	default:
		return usageError(flags, "too many arguments: give one DEST, after the flags")
	}
	cfg, err := loadConfig(configFile)
	if err != nil {
		printConfigError(e.stderr, flags.Name(), err)
		return 2
	}
	db := cmp.Or(dbDir, cfg.db)
	if db == "" {
		return usageError(flags, errNoDB)
	}
	dest := flags.Arg(0)
	p := errorPrinter{w: e.stderr, command: flags.Name()}

	missing, err := checkBackupDest(dest)
	if err != nil {
		p.printError(ctx, err)
		return 1
	}
	if missing {
		// Not MkdirAll: a parent that is missing is a mistake in DEST, not something to make.
		if err := os.Mkdir(dest, 0o700); err != nil {
			p.printError(ctx, err)
			return 1
		}
	}

	// From here on, what the command has made in DEST is removed when it fails: DEST was missing or empty.
	fail := func(err error) int {
		p.printError(ctx, err)
		if err := cleanupNewDataDir(dest, missing); err != nil {
			printf(e.stderr, "%s: cleaning up %s: %v\n", flags.Name(), dest, err)
		}
		return 1
	}
	if missing {
		if err := fspath.SyncParent(dest); err != nil {
			return fail(err)
		}
	}
	heads, err := store.Backup(ctx, dbDialer(db), dest)
	if err != nil {
		return fail(err)
	}
	printf(e.stdout, "backed up the store of %s to %s\n", db, dest)
	for _, h := range heads {
		printf(e.stdout, "%s\n", h)
	}
	return 0
}

// checkBackupDest checks that dest can take the backup: it is a missing directory, which has a parent, or an empty
// directory. It reports whether dest is missing.
func checkBackupDest(dest string) (missing bool, err error) {
	info, err := os.Stat(dest)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return true, nil
	case err != nil:
		return false, err
	case !info.IsDir():
		return false, fmt.Errorf("%s: not a directory; DEST must be missing or an empty directory", dest)
	}
	entries, err := os.ReadDir(dest)
	switch {
	case err != nil:
		return false, err
	case len(entries) > 0:
		return false, fmt.Errorf("%s: not empty; DEST must be missing or an empty directory", dest)
	}
	return false, nil
}

// cleanupNewDataDir removes what whiteroom backup has made in the directory dir, the directory that the kernel takes the
// path dir for: logdb/ and files/, and dir itself if it was missing. It leaves the parents that making dir has made.
func cleanupNewDataDir(dir string, missing bool) error {
	var errs []error
	// Not filepath.Join(dir, ...): it drops a ".." after a symbolic link, and names another directory than the one that was made.
	root, err := os.OpenRoot(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		errs = append(errs, err)
	default:
		errs = append(errs, root.RemoveAll("logdb"), root.RemoveAll("files"), root.Close())
	}
	if missing {
		if err := os.Remove(dir); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
