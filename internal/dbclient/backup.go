package dbclient

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"strings"

	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// Backup is the position of the commit that a backup has, as the daemon sent it, and the lines of the heads that the
// store had at that commit.
type Backup struct {
	Generation, Segment uint64
	End                 int64
	Seq                 uint64
	CRC                 uint32
	Heads               []string
}

// Backup makes a copy of the store of the daemon in dst, which must be empty: the daemon sends the directories and the
// files of the data directory, and Backup makes them with the modes 0700 and 0600. It refuses an entry that is not
// files, logdb or under one of them, or whose path is absolute or has a ".." element, before it makes anything of it. It
// syncs every file, then the directories that it made, the deepest first, and dst last, so that the copy is whole after
// a crash. A copy that opens is the store at the commit of the position that Backup returns. If Backup fails, what it
// has written stays, for the caller to remove.
func (c *Client) Backup(ctx context.Context, dst vfs.FS) (Backup, error) {
	if c.isClosed() {
		return Backup{}, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return Backup{}, err
	}
	entries, err := dst.ReadDir(".")
	switch {
	case err != nil:
		return Backup{}, fmt.Errorf("%s: cannot read: %w", dst.Name(), err)
	case len(entries) > 0:
		return Backup{}, fmt.Errorf("%s: not empty", dst.Name())
	}
	// A new connection: a Backup that fails after it has written entries cannot be tried again on another.
	cn, err := c.connect(ctx, dbwire.RoleReader)
	if err != nil {
		return Backup{}, err
	}
	var b Backup
	err = cn.call(ctx, dbwire.KindBackup, nil, func(d *dbwire.Dec) (err error) {
		b, err = restore(d, dst)
		return err
	})
	if err != nil {
		c.putReader(cn) // closes it, unless the error is the daemon's answer, which ends the reply
		return Backup{}, err
	}
	c.putReader(cn)
	return b, nil
}

// restore makes the entries of the reply in dst.
func restore(d *dbwire.Dec, dst vfs.FS) (Backup, error) {
	var (
		b      Backup
		dirs   []string
		hasPos bool
		name   = dst.Name()
	)
	for {
		en, ok := dbwire.ReadBackupEntry(d)
		if !ok {
			break
		}
		switch en.Kind {
		case dbwire.EntryEnd: // ReadBackupEntry gives no ok for it
		case dbwire.EntryPosition:
			if hasPos {
				return Backup{}, fmt.Errorf("%w: the backup has two positions", dbwire.ErrProtocol)
			}
			p := en.Position
			b.Generation, b.Segment, b.End, b.Seq, b.CRC = p.Generation, p.Segment, p.End, p.Seq, p.CRC
			hasPos = true
		case dbwire.EntryHead:
			b.Heads = append(b.Heads, en.Line)
		case dbwire.EntryDir:
			if err := checkBackupPath(name, en.Path); err != nil {
				return Backup{}, &localError{err}
			}
			if err := dst.Mkdir(en.Path, 0o700); err != nil {
				return Backup{}, &localError{err}
			}
			dirs = append(dirs, en.Path)
		case dbwire.EntryFile:
			if err := checkBackupPath(name, en.Path); err != nil {
				return Backup{}, &localError{err}
			}
			if err := restoreFile(d, dst, en.Path); err != nil {
				return Backup{}, err
			}
		}
	}
	if d.Err != nil {
		return Backup{}, d.Err
	}
	if !hasPos {
		return Backup{}, fmt.Errorf("%w: the backup has no position", dbwire.ErrProtocol)
	}
	// A directory is made after its parent, so the reverse order has each one before its parent.
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := dst.SyncDir(dirs[i]); err != nil {
			return Backup{}, &localError{err}
		}
	}
	if err := dst.SyncDir("."); err != nil {
		return Backup{}, &localError{err}
	}
	return b, nil
}

// restoreFile makes the file p in dst of the chunks that come next, and syncs it.
func restoreFile(d *dbwire.Dec, dst vfs.FS, p string) error {
	f, err := dst.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return &localError{err}
	}
	_, err = io.Copy(localWriter{f}, dbwire.NewChunkReader(d, math.MaxInt64))
	if err == nil {
		if serr := f.Sync(); serr != nil {
			err = &localError{serr}
		}
	}
	if cerr := f.Close(); err == nil && cerr != nil {
		err = &localError{cerr}
	}
	return err
}

// localWriter marks the errors of w as those of the caller's side.
type localWriter struct{ w io.Writer }

func (l localWriter) Write(p []byte) (int, error) {
	n, err := l.w.Write(p)
	if err != nil {
		err = &localError{err}
	}
	return n, err
}

// checkBackupPath refuses the path p of an entry of the backup, which has to be files or logdb, or a path under them, in
// the form of io/fs: relative, with slashes, and with no empty, "." or ".." element. name is the destination in errors.
func checkBackupPath(name, p string) error {
	top, _, _ := strings.Cut(p, "/")
	if (top == "files" || top == "logdb") && fs.ValidPath(p) && !strings.ContainsAny(p, "\\:\x00") {
		return nil
	}
	return fmt.Errorf("%s: the backup has an entry for %q, which is not a path in files or logdb", name, p)
}
