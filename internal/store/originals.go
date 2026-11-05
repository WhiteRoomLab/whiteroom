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
	"strings"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// Original is an original of a medium in the store.
type Original struct {
	Hash string // "sha256:" and the 64 lower case hex digits of the SHA-256 of the data, as Media.Hash has it
	Size int64  // in bytes
}

// PutOriginal saves what r reads, to its end, as an original, and returns it. The data must not be empty, and may have
// at most content.MaxMediaBytes. PutOriginal writes it to a temporary file in files/, which it syncs and then renames
// to the name of the original, so that the original is whole or missing, even after a crash. It replaces a file of
// that name, which has the same data unless it is damaged. It checks ctx before each read of r.
func (s *Store) PutOriginal(ctx context.Context, r io.Reader) (Original, error) {
	f, tmp, err := s.createTemp()
	if err != nil {
		return Original{}, err
	}
	renamed := false
	defer func() {
		if !renamed {
			_ = s.files.Remove(tmp)
		}
	}()
	o, err := writeTemp(ctx, f, r)
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
		return Original{}, err
	}
	renamed = true
	if err := s.files.SyncDir(dir); err != nil {
		return Original{}, err
	}
	if err := s.call("original:renamed"); err != nil {
		return Original{}, err
	}
	return o, nil
}

// createTemp creates a new temporary file in files/, .tmp-<16 random hex digits>, and returns it with its name.
func (s *Store) createTemp() (vfs.File, string, error) {
	var err error
	for range 10 {
		var b [8]byte
		_, _ = rand.Read(b[:])
		tmp := ".tmp-" + hex.EncodeToString(b[:])
		var f vfs.File
		if f, err = s.files.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600); !errors.Is(err, fs.ErrExist) {
			return f, tmp, err
		}
	}
	return nil, "", err
}

// writeTemp writes what r reads, to its end, to f, a new temporary file, which it syncs and closes. It returns the
// original that the data is, and checks ctx before each read.
func writeTemp(ctx context.Context, f vfs.File, r io.Reader) (o Original, err error) {
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
	if err := f.Sync(); err != nil {
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
		return err
	}
	if err := s.files.SyncDir("."); err != nil {
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
