package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

const backupUsage = `Usage: whiteroom backup [flags] DEST

Copy the data directory to DEST, which must be missing or an empty directory, and
whose parent must exist. The copy is the data directory as it was at the last
commit that was durable when the command began: a whiteroom serve admin may go on
writing the data directory meanwhile, and the command writes nothing in it. The
copy is a data directory too, which whiteroom build -data and whiteroom serve
admin work on. To restore it, put it where the data directory is.

The copy holds all that the data directory holds, the hashes of the passwords and
the audit logs included: keep it as private as the data directory.

The command prints "backed up DIR to DEST", and then the heads of the chains of the
audit logs of the copy, a line each, which hold no personal information: keep them
apart from the copy.

With -config, the item data of the YAML file gives what -data leaves out; the
other items are checked and not used. Without it, no config file is read.

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
	var configFile, dataDir string
	flags.StringVar(&configFile, "config", "", "read the settings from the YAML file `FILE`; the flags override them")
	flags.StringVar(&dataDir, "data", "", "copy the data directory `DIR` (default data of the config file)")
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
	data := cmp.Or(dataDir, cfg.data)
	if data == "" {
		return usageError(flags, "no data directory: give -data DIR, or data in the config file")
	}
	dest := flags.Arg(0)
	p := errorPrinter{w: e.stderr, command: flags.Name()}

	if err := checkDataDir(data); err != nil {
		p.printError(ctx, err)
		return 1
	}
	missing, err := checkBackupDest(data, dest)
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
		if err := cleanupNewDataDir(nil, dest, missing); err != nil {
			printf(e.stderr, "%s: cleaning up %s: %v\n", flags.Name(), dest, err)
		}
		return 1
	}
	if err := store.Backup(ctx, data, dest); err != nil {
		return fail(err)
	}
	heads, err := copiedHeads(ctx, dest)
	if err != nil {
		return fail(err)
	}
	printf(e.stdout, "backed up %s to %s\n", data, dest)
	for _, h := range heads {
		printf(e.stdout, "%s\n", h)
	}
	return 0
}

// checkDataDir checks that dir is a data directory that Backup can copy: it has logdb/ and files/, which are not
// symbolic links. It looks in the directory that the kernel takes the path dir for.
func checkDataDir(dir string) error {
	if _, err := os.Stat(dir); err != nil {
		return err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	for _, name := range []string{"logdb", "files"} {
		// For the messages only: filepath.Join drops a ".." after a symbolic link.
		path := filepath.Join(dir, name)
		info, err := root.Lstat(name)
		var pe *fs.PathError
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return fmt.Errorf("%s: missing directory", path)
		case errors.As(err, &pe):
			return &fs.PathError{Op: "lstat", Path: path, Err: pe.Err}
		case err != nil:
			return err
		case info.Mode()&fs.ModeSymlink != 0:
			return fmt.Errorf("%s: symbolic links are not allowed", path)
		case !info.IsDir():
			return fmt.Errorf("%s: must be a directory", path)
		}
	}
	return nil
}

// checkBackupDest checks that dest can take the backup of the data directory data: it is not that directory, nor in it,
// and it is a missing directory, which has a parent, or an empty directory. It reports whether dest is missing.
func checkBackupDest(data, dest string) (missing bool, err error) {
	info, err := os.Stat(dest)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		missing = true
	case err != nil:
		return false, err
	case !info.IsDir():
		return false, fmt.Errorf("%s: not a directory; DEST must be missing or an empty directory", dest)
	}
	inside, err := insideDir(data, dest, missing)
	switch {
	case err != nil:
		return false, err
	case inside == ".":
		return false, fmt.Errorf("%s: DEST is the data directory", dest)
	case inside != "":
		return false, fmt.Errorf("%s: DEST is in the data directory %s", dest, data)
	case missing:
		return true, nil
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

// insideDir returns "." if dest is the directory dir, and its path from dir if it is in dir, and "" if it is neither, with
// the paths as the kernel resolves them. dest, if it is missing, has the parent that must exist.
func insideDir(dir, dest string, destMissing bool) (string, error) {
	dirPath, err := resolvedPath(dir)
	if err != nil {
		return "", err
	}
	var destPath string
	if destMissing {
		// Not filepath.Dir and Base: they clean the path, and take a trailing separator for an element.
		trimmed := dest
		for len(trimmed) > 1 && os.IsPathSeparator(trimmed[len(trimmed)-1]) {
			trimmed = trimmed[:len(trimmed)-1]
		}
		parentName, last := filepath.Split(trimmed)
		if parentName == "" {
			parentName = "."
		}
		var parent string
		if parent, err = resolvedPath(parentName); err == nil {
			destPath = filepath.Join(parent, last)
		}
	} else {
		destPath, err = resolvedPath(dest)
	}
	if err != nil {
		return "", err
	}
	// Paths without a relative path between them, as those of two volumes, are not one in the other.
	if rel, err := filepath.Rel(dirPath, destPath); err == nil && filepath.IsLocal(rel) {
		return rel, nil
	}
	return "", nil
}

// resolvedPath returns the absolute path of name, which exists, with its symbolic links resolved as the kernel does, also
// those that a ".." follows: it is not made absolute first, as filepath.Abs cleans the path and drops "link/..".
func resolvedPath(name string) (string, error) {
	resolved, err := filepath.EvalSymlinks(name)
	if err != nil || filepath.IsAbs(resolved) {
		return resolved, err
	}
	// A relative path that is left has no link in it. The working directory may be the path through a link.
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	if wd, err = filepath.EvalSymlinks(wd); err != nil {
		return "", err
	}
	return filepath.Join(wd, resolved), nil
}

// copiedHeads returns the heads of the audit logs of the data directory dir, which nothing has open, as a process that
// reads it sees them.
func copiedHeads(ctx context.Context, dir string) ([]audit.Head, error) {
	s, err := store.OpenReader(dir)
	if err != nil {
		return nil, err
	}
	heads, err := s.AuditHeads(ctx)
	return heads, errors.Join(err, s.Close())
}
