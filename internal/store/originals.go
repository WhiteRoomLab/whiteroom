package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/fspath"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// Original is an original of a medium in the store.
type Original struct {
	Hash string // "sha256:" and the 64 lower case hex digits of the SHA-256 of the data, as Media.Hash has it
	Size int64  // in bytes
}

// errOriginalsStopped is the error of PutOriginal after a sync of a PutOriginal of the store has failed: the page cache
// may then show names that never reach the disk, which later syncs do not write.
var errOriginalsStopped = errors.New("saving originals has stopped after a sync failed; " +
	"restart the machine before opening the store again")

// PutOriginal saves what r reads, to its end, as an original, and returns it. The data must not be empty, and may have
// at most content.MaxMediaBytes. PutOriginal writes it to a temporary file in files/, which it syncs and then renames
// to the name of the original, so that the original is whole or missing, even after a crash. It replaces a file of
// that name, which has the same data unless it is damaged. It checks ctx before each read of r. Once a sync of a
// PutOriginal has failed, every PutOriginal fails, also one that runs at the same time, until the store is opened
// again, after a restart of the machine. The error of the file system, as when files/ is gone, does not wrap
// fs.ErrNotExist, which is for a document or an original that is not there.
func (s *Store) PutOriginal(ctx context.Context, r io.Reader) (Original, error) {
	if s.readOnly {
		return Original{}, errors.New("the store is opened to read only")
	}
	if s.originalsStopped.Load() {
		return Original{}, errOriginalsStopped
	}
	f, tmp, err := s.createTemp(os.O_WRONLY)
	if err != nil {
		return Original{}, err
	}
	renamed := false
	defer func() {
		if !renamed {
			_ = s.files.Remove(tmp)
		}
	}()
	o, err := s.writeTemp(ctx, f, r)
	if err != nil {
		return Original{}, err
	}
	if err := s.call("original:synced"); err != nil {
		return Original{}, err
	}
	digits := strings.TrimPrefix(o.Hash, "sha256:")
	dir := digits[:2]
	if err := s.mkdir(dir); err != nil {
		return Original{}, err
	}
	if err := s.files.Rename(tmp, dir+"/"+digits); err != nil {
		return Original{}, notMissing(err)
	}
	renamed = true
	if err := s.syncOriginal(func() error { return s.files.SyncDir(dir) }); err != nil {
		return Original{}, err
	}
	if err := s.call("original:renamed"); err != nil {
		return Original{}, err
	}
	// Waits for the syncs that run: a failed one may have dropped names that the syncs above took as written.
	s.syncs.Lock()
	stopped := s.originalsStopped.Load()
	s.syncs.Unlock()
	if stopped {
		return Original{}, errOriginalsStopped
	}
	return o, nil
}

// syncOriginal runs sync, a sync of PutOriginal, unless one has failed, and stops saving originals if it fails.
func (s *Store) syncOriginal(sync func() error) error {
	s.syncs.RLock()
	defer s.syncs.RUnlock()
	if s.originalsStopped.Load() {
		return errOriginalsStopped
	}
	if err := sync(); err != nil {
		s.originalsStopped.Store(true)
		return fmt.Errorf("%w: %w", errOriginalsStopped, notMissing(err))
	}
	return nil
}

// notMissing returns err, an error of the file system under files/, as it is, but for one that wraps fs.ErrNotExist: that
// one keeps its text and loses its chain. A file that files/ lacks, or files/ itself, is a fault of the store, and not a
// document or an original that is not there, which is what fs.ErrNotExist tells the callers.
func notMissing(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return errors.New(err.Error())
	}
	return err
}

// createTemp creates a new temporary file in files/, .tmp-<16 random hex digits>, opened with access, os.O_WRONLY or
// os.O_RDWR, and returns it with its name.
func (s *Store) createTemp(access int) (vfs.File, string, error) {
	var err error
	for range 10 {
		var b [8]byte
		_, _ = rand.Read(b[:])
		tmp := tempPrefix + hex.EncodeToString(b[:])
		var f vfs.File
		if f, err = s.files.OpenFile(tmp, access|os.O_CREATE|os.O_EXCL, 0o600); !errors.Is(err, fs.ErrExist) {
			return f, tmp, notMissing(err)
		}
	}
	return nil, "", err
}

// tempPrefix starts the name of a temporary file in files/, which has 16 lower case hex digits after it.
const tempPrefix = ".tmp-"

// isTempName reports whether name is that of a temporary file that createTemp makes.
func isTempName(name string) bool {
	digits, ok := strings.CutPrefix(name, tempPrefix)
	if !ok || len(digits) != 16 {
		return false
	}
	for i := range len(digits) {
		if strings.IndexByte("0123456789abcdef", digits[i]) < 0 {
			return false
		}
	}
	return true
}

// TempFile is a temporary file of the writer of the store, in files/ beside the originals: a file that the process uses
// for what does not fit in memory and does not need to keep. It is not synced. If the process ends without closing it,
// the file stays until the process that opens the store as its writer next removes it (Open). A TempFile is not for
// concurrent use.
type TempFile struct {
	f      vfs.File
	files  vfs.FS
	name   string
	closed atomic.Bool
}

// CreateTemp makes a new empty temporary file in files/, to read and write: it is named like the temporary file of
// PutOriginal, .tmp-<16 random hex digits>, and has the mode 0600. A store that is opened to read only makes none. The
// error of the file system, as when files/ is gone, does not wrap fs.ErrNotExist, which is for a document or an original
// that is not there.
func (s *Store) CreateTemp() (*TempFile, error) {
	if s.readOnly {
		return nil, errReadOnly
	}
	f, name, err := s.createTemp(os.O_RDWR)
	if err != nil {
		return nil, err
	}
	return &TempFile{f: f, files: s.files, name: name}, nil
}

func (t *TempFile) Read(p []byte) (int, error)  { return t.f.Read(p) }
func (t *TempFile) Write(p []byte) (int, error) { return t.f.Write(p) }

func (t *TempFile) Seek(offset int64, whence int) (int64, error) { return t.f.Seek(offset, whence) }

// Close closes the file and removes it. Only the first call does: the others do nothing.
func (t *TempFile) Close() error {
	if t.closed.Swap(true) {
		return nil
	}
	err := t.f.Close()
	if rerr := t.files.Remove(t.name); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
		err = errors.Join(err, rerr)
	}
	return err
}

// removeTemps removes the temporary files in files/ that a process has left by ending without removing them: the
// regular files named like those of createTemp, and nothing else. It is for the process that opens the store as its
// writer, which holds the lock: no other temporary file is in use. dir is the data directory, which errors name.
func (s *Store) removeTemps(dir string) error {
	entries, err := s.files.ReadDir(".")
	if err != nil {
		return readError(fspath.Join(dir, "files"), err)
	}
	for _, e := range entries {
		if !isTempName(e.Name()) || !e.Type().IsRegular() {
			continue
		}
		if err := s.files.Remove(e.Name()); err != nil && !errors.Is(err, fs.ErrNotExist) {
			path := fspath.Join(dir, filepath.Join("files", e.Name()))
			var pe *fs.PathError
			if errors.As(err, &pe) {
				err = pe.Err
			}
			return fmt.Errorf("%s: cannot remove: %w", path, err)
		}
	}
	return nil
}

// writeTemp writes what r reads, to its end, to f, a new temporary file, which it syncs and closes. It returns the
// original that the data is, and checks ctx before each read.
func (s *Store) writeTemp(ctx context.Context, f vfs.File, r io.Reader) (o Original, err error) {
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}()
	digest := sha256.New()
	w := io.MultiWriter(f, digest)
	buf := make([]byte, 32<<10)
	for {
		if err := ctx.Err(); err != nil {
			return Original{}, err
		}
		n, err := r.Read(buf)
		if o.Size += int64(n); o.Size > content.MaxMediaBytes {
			return Original{}, fmt.Errorf("the original is larger than %d bytes", content.MaxMediaBytes)
		}
		if _, werr := w.Write(buf[:n]); werr != nil {
			return Original{}, werr
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Original{}, err
		}
	}
	if o.Size == 0 {
		return Original{}, errors.New("the original is empty")
	}
	if err := s.syncOriginal(f.Sync); err != nil {
		return Original{}, err
	}
	o.Hash = "sha256:" + hex.EncodeToString(digest.Sum(nil))
	return o, nil
}

// mkdir makes dir, a directory of originals in files/, if it is missing, and then syncs files/, unless it has synced
// files/ after an earlier call made or found dir. A directory that is there may not have a durable name yet: a
// PutOriginal whose sync failed, or a process that was killed, may have made it.
func (s *Store) mkdir(dir string) error {
	switch err := s.files.Mkdir(dir, 0o700); {
	case errors.Is(err, fs.ErrExist):
		if _, ok := s.dirs.Load(dir); ok {
			return nil
		}
	case err != nil:
		return notMissing(err)
	}
	if err := s.syncOriginal(func() error { return s.files.SyncDir(".") }); err != nil {
		return err
	}
	s.dirs.Store(dir, true)
	return nil
}

// OpenOriginal opens the original whose hash is hash. If there is none, the error wraps fs.ErrNotExist. The file checks
// the data against hash at its end: the Read that reaches the end returns an error instead of io.EOF if the data has
// another hash. A reader that stops at the size that Stat returns, as io.ReadFull does, does not reach the end, and
// must check the hash itself.
func (s *Store) OpenOriginal(hash string) (fs.File, error) {
	if err := checkHash(hash); err != nil {
		return nil, err
	}
	digits := strings.TrimPrefix(hash, "sha256:")
	file := digits[:2] + "/" + digits
	path := "files/" + file
	// Lstat before opening: opening a named pipe would wait for a writer.
	info, err := s.files.Lstat(file)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, notFoundError(path)
	case err != nil:
		return nil, readError(path, err)
	case info.Mode()&fs.ModeSymlink != 0:
		return nil, fmt.Errorf("%s: symbolic links are not allowed", path)
	case !info.Mode().IsRegular():
		return nil, fmt.Errorf("%s: must be a regular file", path)
	case info.Size() > content.MaxMediaBytes:
		return nil, fmt.Errorf("%s: too large: %d bytes (max %d)", path, info.Size(), content.MaxMediaBytes)
	}
	f, err := s.files.Open(file)
	if err != nil {
		return nil, readError(path, err)
	}
	return &originalFile{f: f, path: path, want: hash, digest: sha256.New()}, nil
}

// readError returns err, from reading path in the data directory, with path in place of the operation and the path in
// files/ of an *fs.PathError.
func readError(path string, err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		err = pe.Err
	}
	return fmt.Errorf("%s: cannot read: %w", path, err)
}

// originalFile is an original that OpenOriginal has opened. It has the methods of fs.File alone, so that every read
// goes through Read, which checks the data.
type originalFile struct {
	f      vfs.File
	path   string    // in errors
	want   string    // the hash of the data
	digest hash.Hash // of the data read so far
	size   int64     // of the data read so far
}

func (o *originalFile) Read(p []byte) (int, error) {
	// A byte more than an original may have, to find a file that has grown since OpenOriginal.
	if rest := content.MaxMediaBytes + 1 - o.size; int64(len(p)) > rest {
		p = p[:rest]
	}
	n, err := o.f.Read(p)
	o.digest.Write(p[:n])
	o.size += int64(n)
	switch {
	case o.size > content.MaxMediaBytes:
		return n, fmt.Errorf("%s: too large: at least %d bytes (max %d)", o.path, o.size, content.MaxMediaBytes)
	case errors.Is(err, io.EOF):
		if got := "sha256:" + hex.EncodeToString(o.digest.Sum(nil)); got != o.want {
			return n, fmt.Errorf("original %s: the data has the hash %s", o.want, got)
		}
	}
	return n, err
}

func (o *originalFile) Stat() (fs.FileInfo, error) { return o.f.Stat() }

func (o *originalFile) Close() error { return o.f.Close() }
