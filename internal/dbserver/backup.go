package dbserver

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"sync"

	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

const headBehindText = "the engine's head is behind its last commit; take the backup again after the next commit"

// sentence is the error of a Backup that ends with a reply of the code CodeFailed and this text, which holds no value of a
// row or of a file.
func sentence(format string, args ...any) error {
	return &refusal{dbwire.CodeFailed, fmt.Sprintf(format, args...)}
}

// backupRequest answers a Backup: the position of the last commit that is durable when it begins, the lines of the table
// of the heads at that commit, the directories and the regular files of files/ but for the temporary files, and the files
// of the engine as logdb.Backup writes them, under logdb/. Written to an empty directory in that order, the entries are
// a data directory that opens to exactly that commit.
//
// Commits wait only while the reader of the engine opens, until the position is known, and not while the files are sent.
func (c *session) backupRequest() bool {
	if err := c.empty(); err != nil {
		c.bad(err)
		return false
	}
	s := c.s
	c.wr.Begin(dbwire.KindOK)
	e := &dbwire.Enc{W: c.wr}

	s.gate.Lock()
	var once sync.Once
	release := func() { once.Do(s.gate.Unlock) }
	_, err := logdb.Backup(c.ctx, s.logFS, &backupDst{c: c, e: e}, func(p logdb.Position) error {
		if err := s.hook("backup:gate"); err != nil {
			return err
		}
		seq, rows, err := s.headRows(c.ctx)
		switch {
		case err != nil:
			return err
		case seq != p.Seq:
			return sentence("%s", headBehindText)
		}
		release()
		if err := s.hook("backup:sending"); err != nil {
			return err
		}
		e.BackupEntry(dbwire.BackupEntry{Kind: dbwire.EntryPosition, Position: dbwire.BackupPosition{
			Generation: p.Generation, Segment: p.Segment, End: p.End, Seq: p.Seq, CRC: p.CRC}})
		for _, r := range rows {
			e.BackupEntry(dbwire.BackupEntry{Kind: dbwire.EntryHead, Line: r.value})
		}
		e.BackupEntry(dbwire.BackupEntry{Kind: dbwire.EntryDir, Path: "files"})
		if e.Err != nil {
			return e.Err
		}
		if err := c.sendFiles(e, "."); err != nil {
			return err
		}
		e.BackupEntry(dbwire.BackupEntry{Kind: dbwire.EntryDir, Path: "logdb"})
		return e.Err
	})
	release()
	if err == nil {
		e.BackupEntry(dbwire.BackupEntry{Kind: dbwire.EntryEnd})
		err = e.Err
	}
	var r *refusal
	switch {
	case err == nil:
		return c.wr.End() == nil
	case c.ctx.Err() != nil:
		return false
	case errors.As(err, &r):
		return c.fail(r.code, r.msg)
	}
	return c.fail(dbwire.CodeFailed, safeText(err))
}

// sendFiles writes the directory dir of files/ ("." is files/ itself), which the reply already has, with every directory
// and regular file in it but for the temporary files of PutOriginal, in the order of their names.
func (c *session) sendFiles(e *dbwire.Enc, dir string) error {
	entries, err := c.s.files.ReadDir(dir)
	if err != nil {
		return sentence("%s", readError(path.Join("files", dir), err))
	}
	for _, en := range entries {
		rel := path.Join(dir, en.Name())
		if err := c.ctx.Err(); err != nil {
			return err
		}
		switch {
		case strings.HasPrefix(en.Name(), tempPrefix):
		case en.IsDir():
			if err := sendEntry(e, dbwire.EntryDir, rel); err != nil {
				return err
			}
			if err := c.sendFiles(e, rel); err != nil {
				return err
			}
		case en.Type().IsRegular():
			if err := sendEntry(e, dbwire.EntryFile, rel); err != nil {
				return err
			}
			if err := c.sendFileContent(e, rel); err != nil {
				return err
			}
		}
	}
	return nil
}

// sendEntry writes the entry of kind for rel, a path in files/, or fails with a sentence if the path cannot be sent.
func sendEntry(e *dbwire.Enc, kind dbwire.EntryKind, rel string) error {
	e.BackupEntry(dbwire.BackupEntry{Kind: kind, Path: path.Join("files", rel)})
	if errors.Is(e.Err, dbwire.ErrProtocol) {
		return sentence("%q: the name cannot be sent in a backup", path.Join("files", rel))
	}
	return e.Err
}

// sendFileContent writes the content of the file rel of files/ as the chunks that follow its entry.
func (c *session) sendFileContent(e *dbwire.Enc, rel string) error {
	name := path.Join("files", rel)
	f, err := c.s.files.Open(rel)
	if err != nil {
		return sentence("%s", readError(name, err))
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return sentence("%s", readError(name, err))
	}
	chunks := e.Chunks()
	buf := make([]byte, 32<<10)
	for left := info.Size(); left > 0; {
		n, err := f.Read(buf[:min(int64(len(buf)), left)])
		if n > 0 {
			if _, werr := chunks.Write(buf[:n]); werr != nil {
				return werr
			}
			left -= int64(n)
		}
		switch {
		case errors.Is(err, io.EOF) && left > 0:
			return sentence("%s: the file is shorter than its size", name)
		case err != nil && !errors.Is(err, io.EOF):
			return sentence("%s", readError(name, err))
		}
	}
	return chunks.Close()
}

// backupDst is the directory that logdb.Backup writes: a vfs.FS that only writes, and turns what the engine does into the
// entries of the reply, under logdb/. The receiver makes the files durable, so Sync and SyncDir do nothing.
type backupDst struct {
	c    *session
	e    *dbwire.Enc
	file *backupFile // the file that is open, if any
}

var errBackupDst = errors.New("the backup of the daemon does not do that")

func (d *backupDst) Name() string                                    { return "backup" }
func (d *backupDst) Open(string) (vfs.File, error)                   { return nil, errBackupDst }
func (d *backupDst) Remove(string) error                             { return errBackupDst }
func (d *backupDst) Rename(string, string) error                     { return errBackupDst }
func (d *backupDst) Lstat(string) (fs.FileInfo, error)               { return nil, errBackupDst }
func (d *backupDst) Sub(string) (vfs.FS, error)                      { return nil, errBackupDst }
func (d *backupDst) Lock(string, vfs.LockMode) (func() error, error) { return nil, errBackupDst }
func (d *backupDst) SyncDir(string) error                            { return nil }
func (d *backupDst) Close() error                                    { return nil }

func (d *backupDst) ReadDir(name string) ([]fs.DirEntry, error) {
	if name != "." {
		return nil, errBackupDst
	}
	return nil, nil
}

func (d *backupDst) Mkdir(name string, _ fs.FileMode) error {
	d.e.BackupEntry(dbwire.BackupEntry{Kind: dbwire.EntryDir, Path: "logdb/" + name})
	return d.e.Err
}

func (d *backupDst) OpenFile(name string, flag int, _ fs.FileMode) (vfs.File, error) {
	if flag != os.O_WRONLY|os.O_CREATE|os.O_EXCL || d.file != nil {
		return nil, errBackupDst
	}
	d.e.BackupEntry(dbwire.BackupEntry{Kind: dbwire.EntryFile, Path: "logdb/" + name})
	if d.e.Err != nil {
		return nil, d.e.Err
	}
	d.file = &backupFile{d: d, chunks: d.e.Chunks()}
	return d.file, nil
}

// backupFile is a file of the reply that is being written: its bytes are chunks, in order.
type backupFile struct {
	d       *backupDst
	chunks  *dbwire.ChunkWriter
	written int64
	closed  bool
}

func (f *backupFile) Write(p []byte) (int, error) {
	if f.closed {
		return 0, errBackupDst
	}
	if err := f.d.c.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := f.chunks.Write(p)
	f.written += int64(n)
	return n, err
}

// WriteAt is Write at the end of what is written: the engine writes the head of a store in one piece at 0.
func (f *backupFile) WriteAt(p []byte, off int64) (int, error) {
	if off != f.written {
		return 0, errBackupDst
	}
	return f.Write(p)
}

func (f *backupFile) Close() error {
	if f.closed {
		return nil
	}
	f.closed = true
	f.d.file = nil
	return f.chunks.Close()
}

func (f *backupFile) Sync() error                        { return nil }
func (f *backupFile) Read([]byte) (int, error)           { return 0, errBackupDst }
func (f *backupFile) ReadAt([]byte, int64) (int, error)  { return 0, errBackupDst }
func (f *backupFile) Seek(int64, int) (int64, error)     { return 0, errBackupDst }
func (f *backupFile) Truncate(int64) error               { return errBackupDst }
func (f *backupFile) Stat() (fs.FileInfo, error)         { return nil, errBackupDst }
func (f *backupFile) ReadDir(int) ([]fs.DirEntry, error) { return nil, errBackupDst }
