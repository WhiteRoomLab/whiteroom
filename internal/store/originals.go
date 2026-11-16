package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"strings"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/dbclient"
)

// Original is an original of a medium in the store.
type Original struct {
	Hash string // "sha256:" and the 64 lower case hex digits of the SHA-256 of the data, as Media.Hash has it
	Size int64  // in bytes
}

// PutOriginal saves what r reads, to its end, as an original, and returns it. The data must not be empty, and may have
// at most content.MaxMediaBytes. The daemon writes it to a temporary file in files/, which it syncs and then renames to
// the name of the original, so that the original is whole or missing, even after a crash. It replaces a file of that
// name, which has the same data unless it is damaged. It checks ctx before each read of r. Once a sync of the daemon has
// failed, every PutOriginal fails until the daemon is started again, after a restart of the machine. The error of the
// file system, as when files/ is gone, does not wrap fs.ErrNotExist, which is for a document or an original that is not
// there.
func (s *Store) PutOriginal(ctx context.Context, r io.Reader) (Original, error) {
	if s.readOnly {
		return Original{}, errors.New("the store is opened to read only")
	}
	// The daemon has a limit of its own, which may be higher: the limit of an original is that of the store.
	sum, size, err := s.c.PutOriginal(ctx, &limitedReader{r: r})
	if err != nil {
		return Original{}, err
	}
	return Original{Hash: hashText(sum[:]), Size: size}, nil
}

// limitedReader reads r, and fails with the error of an original that is too large once it has read more than
// content.MaxMediaBytes.
type limitedReader struct {
	r io.Reader
	n int64
}

func (l *limitedReader) Read(p []byte) (int, error) {
	n, err := l.r.Read(p)
	if l.n += int64(n); l.n > content.MaxMediaBytes {
		return n, fmt.Errorf("the original is larger than %d bytes", content.MaxMediaBytes)
	}
	return n, err
}

// OpenOriginal opens the original whose hash is hash. ctx bounds the open and every read of the file: when it ends, the
// reads fail with its error. If there is no original, the error wraps fs.ErrNotExist. The file checks
// the data against hash at its end: the Read that reaches the end returns an error instead of io.EOF if the data has
// another hash. A reader that stops at the size that Stat returns, as io.ReadFull does, does not reach the end, and
// must check the hash itself.
func (s *Store) OpenOriginal(ctx context.Context, hash string) (fs.File, error) {
	if err := checkHash(hash); err != nil {
		return nil, err
	}
	sum, err := digest(hash)
	if err != nil {
		return nil, err
	}
	digits := strings.TrimPrefix(hash, "sha256:")
	path := "files/" + digits[:2] + "/" + digits
	o, err := s.c.Original(ctx, sum)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, notFoundError(path)
	case err != nil:
		return nil, err
	case o.Size() > content.MaxMediaBytes:
		_ = o.Close()
		return nil, fmt.Errorf("%s: too large: %d bytes (max %d)", path, o.Size(), content.MaxMediaBytes)
	}
	return &originalFile{o: o, name: digits, path: path, want: hash, digest: sha256.New()}, nil
}

// originalFile is an original that OpenOriginal has opened. It has the methods of fs.File alone, so that every read
// goes through Read, which checks the data.
type originalFile struct {
	o      *dbclient.Original
	name   string    // of the file, the digits of the hash
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
	n, err := o.o.Read(p)
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

func (o *originalFile) Stat() (fs.FileInfo, error) {
	return originalInfo{name: o.name, size: o.o.Size()}, nil
}

func (o *originalFile) Close() error { return o.o.Close() }

// originalInfo is the FileInfo of an original: a regular file with the mode 0600, and nothing else known.
type originalInfo struct {
	name string
	size int64
}

func (i originalInfo) Name() string       { return i.name }
func (i originalInfo) Size() int64        { return i.size }
func (i originalInfo) Mode() fs.FileMode  { return 0o600 }
func (i originalInfo) ModTime() time.Time { return time.Time{} }
func (i originalInfo) IsDir() bool        { return false }
func (i originalInfo) Sys() any           { return nil }
