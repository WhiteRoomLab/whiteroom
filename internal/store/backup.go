package store

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/dbclient"
	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/fspath"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// Backup copies the store that the daemon of d serves, whose writer may go on writing, to the data directory dstDir,
// which must be missing or an empty directory: the daemon's copy of its data directory at the last commit that is
// durable when its Backup begins, which has the originals of that commit. It returns the heads of the audit logs of the
// copy. Before it makes anything, it refuses a store of a table schema before 7, which has no rows of the heads, a data
// directory that has no store yet, with an error that wraps ErrNoStore, and a dstDir that is the data directory of the
// daemon or is in it, as the device and inode numbers that the daemon tells say (on Linux): the daemon sends the files of
// its data directory as it finds them, and would send what Backup writes there too. Then it calls prepare, if it is not
// nil, which may make dstDir. If Backup fails, dstDir must be emptied before it can try again.
func Backup(ctx context.Context, d dbclient.Dialer, dstDir string, prepare func() error) ([]audit.Head, error) {
	c, err := dbclient.Open(ctx, d, dbwire.RoleReader, dbclient.Options{})
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Close() }()
	return backupTo(ctx, c, dialerName(d), dstDir, nil, prepare)
}

// backupTo is Backup with the client c of a reader of the daemon that the errors call name, to dstDir on the OS, or to dst
// if it is not nil, which dstDir only names in errors.
func backupTo(ctx context.Context, c *dbclient.Client, name, dstDir string, dst vfs.FS, prepare func() error) ([]audit.Head, error) {
	if err := c.View(ctx, func(tx *dbclient.Tx) error {
		v, ok, err := tx.Get(tableMeta, schemaKey)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%s: %w", name, ErrNoStore)
		}
		if version, ok := decodeUint(v); !ok || version < exportSchema {
			return fmt.Errorf("the store has a table schema before %d, which has no rows of the heads; "+
				"open it as its writer once to upgrade it", exportSchema)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if dst == nil {
		if err := outsideDataDir(dstDir, c.DataDir()); err != nil {
			return nil, err
		}
	}
	if prepare != nil {
		if err := prepare(); err != nil {
			return nil, err
		}
	}
	if err := newDataDir(dstDir, dst); err != nil {
		return nil, err
	}
	root, done, err := dataRoot(dstDir, dst)
	if err != nil {
		return nil, err
	}
	defer done()
	b, err := c.Backup(ctx, root)
	if err != nil {
		return nil, err
	}
	heads := make([]audit.Head, 0, len(b.Heads))
	for i := range b.Heads {
		h, err := audit.ParseHead(b.Heads[i])
		if err != nil {
			return nil, fmt.Errorf("a head line of the backup does not parse: %w", err)
		}
		heads = append(heads, h)
	}
	return heads, nil
}

// outsideDataDir refuses dir if it is the data directory whose identity is id, or is in it. A zero id is not known, and
// refuses nothing.
func outsideDataDir(dir string, id dbwire.DirID) error {
	if id == (dbwire.DirID{}) {
		return nil
	}
	switch in, err := fspath.In(dir, id.Dev, id.Ino); {
	case err != nil:
		return err
	case !in:
		return nil
	}
	if dev, ino, _, err := fspath.FileID(dir); err == nil && dev == id.Dev && ino == id.Ino {
		return fmt.Errorf("%s: DEST is the data directory of the daemon", dir)
	}
	return fmt.Errorf("%s: DEST is in the data directory of the daemon", dir)
}

// newDataDir checks that dir, a new data directory, is empty: fsys if it is not nil, else the directory on the OS,
// which it makes if it is missing (fspath.MakeDirs).
func newDataDir(dir string, fsys vfs.FS) error {
	var entries []fs.DirEntry
	var err error
	if fsys != nil {
		entries, err = fsys.ReadDir(".")
	} else if err = fspath.MakeDirs(dir); err == nil {
		entries, err = os.ReadDir(dir)
	}
	if err != nil {
		return err
	}
	if len(entries) > 0 {
		return fmt.Errorf("%s: not empty; a new data directory needs a missing or empty directory", dir)
	}
	return nil
}

// dataRoot returns fsys, or if it is nil, the directory dir on the OS; done closes what dataRoot has opened.
func dataRoot(dir string, fsys vfs.FS) (root vfs.FS, done func(), err error) {
	if fsys != nil {
		return fsys, func() {}, nil
	}
	// The root joins the names of its files to its own as text, which drops a ".." after a symbolic link: such a root is
	// opened by its resolved path.
	if fspath.Clean(dir) != filepath.Clean(dir) {
		if dir, err = fspath.Resolve(dir); err != nil {
			return nil, nil, err
		}
	}
	if root, err = vfs.OpenRoot(dir); err != nil {
		return nil, nil, err
	}
	return root, func() { _ = root.Close() }, nil
}
