package wpimport

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/mediafile"
)

// maxMediaBytes is the most bytes of a file that is imported as a medium. It is a variable so that the tests can lower it.
var maxMediaBytes int64 = content.MaxMediaBytes

// skipError is a problem of one file, because of which its attachment is not imported; reason goes to the report. Every
// other error stops the import.
type skipError struct{ reason string }

func (e *skipError) Error() string { return e.reason }

func skipf(format string, args ...any) error {
	return &skipError{reason: fmt.Sprintf(format, args...)}
}

// errNotInCopy means that the copy of the uploads does not have the file.
var errNotInCopy = &skipError{reason: "not in the copy of the uploads"}

// readError means that the reader that a file was saved from failed, which a download retries.
type readError struct{ err error }

func (e *readError) Error() string { return e.err.Error() }
func (e *readError) Unwrap() error { return e.err }

// tempPrefix starts the names of the temporary files, which have 16 lower case hex digits after it. internal/sitedir
// ignores them, as they start with ".".
const tempPrefix = ".tmp-"

// originals writes the originals of the media, as files named by the SHA-256 of their data, in dir, which is the files/
// of the site directory (internal/sitedir). Its methods may be called concurrently.
type originals struct{ dir string }

// newOriginals makes dir if it is missing.
func newOriginals(dir string) (*originals, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating the directory of the originals: %w", err)
	}
	return &originals{dir: dir}, nil
}

// original is a file that save has written to a temporary file, until keep or discard.
type original struct {
	hash string // "sha256:" and the 64 lower case hex digits of the SHA-256 of the data, as Media.Hash has it
	size int64  // in bytes
	info mediafile.Info

	dir  string
	path string // the temporary file, and after keep the file of the original
	kept bool
}

// save writes what r reads, to its end, to a temporary file in the directory, and reads from it what it is, from its
// contents (6.6.2). Its data is not all in memory. The caller decides between keep and discard, which one of them must
// follow. An error leaves no file. A *skipError says that the file is not imported; a *readError, that r failed; the
// error of ctx, that ctx is done; the others, that the directory cannot be written or the file read back.
func (o *originals) save(ctx context.Context, r io.Reader) (*original, error) {
	f, tmp, err := o.createTemp()
	if err != nil {
		return nil, o.failed(err)
	}
	orig, err := o.fill(ctx, f, r)
	if cerr := f.Close(); err == nil {
		err = o.failed(cerr)
	}
	if err != nil {
		_ = os.Remove(tmp)
		return nil, err
	}
	orig.path = tmp
	return orig, nil
}

// fill writes what r reads to f, which it syncs, and probes it.
func (o *originals) fill(ctx context.Context, f *os.File, r io.Reader) (*original, error) {
	digest := sha256.New()
	w := &failedWriter{w: io.MultiWriter(f, digest)}
	// One byte more than the limit finds a file that is larger, without reading the rest of it.
	n, err := io.Copy(w, io.LimitReader(&ctxReader{ctx: ctx, r: r}, maxMediaBytes+1))
	switch {
	case err != nil && w.err != nil:
		return nil, o.failed(err)
	case err != nil:
		if cerr := ctx.Err(); cerr != nil {
			return nil, cerr
		}
		return nil, &readError{err: err}
	case n > maxMediaBytes:
		return nil, skipf("too large: more than %d bytes", maxMediaBytes)
	case n == 0:
		return nil, skipf("empty file")
	}
	if err := f.Sync(); err != nil {
		return nil, o.failed(err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, o.failed(err)
	}
	// Probe returns the error of its reader as it is, but its other errors say only that the file is not in a format.
	file := &failedReader{r: f}
	info, err := mediafile.Probe(file)
	switch {
	case file.err != nil:
		return nil, o.failed(file.err)
	case err != nil:
		return nil, skipf("cannot read the file: %v", err)
	}
	return &original{hash: "sha256:" + hex.EncodeToString(digest.Sum(nil)), size: n, info: info, dir: o.dir}, nil
}

// failed returns err, an error of the directory, with what it was doing, or nil if err is nil.
func (o *originals) failed(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("saving an original in %s: %w", o.dir, err)
}

// createTemp makes a new temporary file in the directory, to read and write, and returns it with its path.
func (o *originals) createTemp() (*os.File, string, error) {
	var err error
	for range 10 {
		var b [8]byte
		_, _ = rand.Read(b[:])
		path := filepath.Join(o.dir, tempPrefix+hex.EncodeToString(b[:]))
		var f *os.File
		if f, err = os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o644); !errors.Is(err, fs.ErrExist) {
			return f, path, err
		}
	}
	return nil, "", err
}

// keep renames the temporary file to the name of the original, in place of a file that is there, which has the same data
// unless it is damaged. After the first call, it does nothing.
func (f *original) keep() error {
	if f.kept {
		return nil
	}
	path := filepath.Join(f.dir, strings.TrimPrefix(f.hash, "sha256:"))
	if err := os.Rename(f.path, path); err != nil {
		return fmt.Errorf("saving an original in %s: %w", f.dir, err)
	}
	f.path, f.kept = path, true
	return nil
}

// discard removes the temporary file. After keep, it does nothing.
func (f *original) discard() {
	if !f.kept {
		_ = os.Remove(f.path)
	}
}

// read returns the data of the file, which is the temporary file before keep and the original after.
func (f *original) read() ([]byte, error) {
	return os.ReadFile(f.path)
}

// openUpload opens the file rel, a path in the copy of the uploads fsys, which has at most maxMediaBytes. The error is
// errNotInCopy if there is no such file, a *skipError if it is a symbolic link, is not a regular file or is too large, and
// otherwise an error that stops the import. A file system that cannot Lstat (fs.ReadLinkFS) gets no look before the open.
func openUpload(fsys fs.FS, rel string) (fs.File, error) {
	if !fs.ValidPath(rel) {
		return nil, errNotInCopy
	}
	// Before the open, which waits on a named pipe and follows a link out of the copy.
	if l, ok := fsys.(fs.ReadLinkFS); ok {
		info, err := l.Lstat(rel)
		if err != nil {
			return nil, uploadError(err)
		}
		if err := checkUploadMode(info.Mode()); err != nil {
			return nil, err
		}
	}
	f, err := fsys.Open(rel)
	if err != nil {
		return nil, uploadError(err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("reading the copy of the uploads: %w", err)
	}
	if err := checkUploadMode(info.Mode()); err != nil {
		_ = f.Close()
		return nil, err
	}
	if info.Size() > maxMediaBytes {
		_ = f.Close()
		return nil, skipf("too large: %d bytes (max %d)", info.Size(), maxMediaBytes)
	}
	return f, nil
}

// uploadError returns the error of looking at or opening a file of the copy of the uploads: errNotInCopy if there is no
// such file, and otherwise one that stops the import.
func uploadError(err error) error {
	// A directory in the path that is a file, or a name that the file system cannot have, does not make the copy unreadable.
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) || errors.Is(err, syscall.ENAMETOOLONG) {
		return errNotInCopy
	}
	return fmt.Errorf("reading the copy of the uploads: %w", err)
}

// checkUploadMode returns a *skipError unless mode is that of a regular file.
func checkUploadMode(mode fs.FileMode) error {
	switch {
	case mode&fs.ModeSymlink != 0:
		return skipf("a symbolic link in the copy of the uploads")
	case !mode.IsRegular():
		return skipf("not a regular file in the copy of the uploads")
	}
	return nil
}

// ctxReader reads from r unless ctx is done, whose error it then returns.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// failedWriter keeps the first error of w, to tell it from the error of the reader that io.Copy returns.
type failedWriter struct {
	w   io.Writer
	err error
}

func (f *failedWriter) Write(p []byte) (int, error) {
	n, err := f.w.Write(p)
	if err != nil && f.err == nil {
		f.err = err
	}
	return n, err
}

// failedReader passes on the reads of r and keeps the first error other than io.EOF, as the decoders may wrap it.
type failedReader struct {
	r   io.Reader
	err error
}

func (f *failedReader) Read(p []byte) (int, error) {
	n, err := f.r.Read(p)
	if err != nil && !errors.Is(err, io.EOF) && f.err == nil {
		f.err = err
	}
	return n, err
}
