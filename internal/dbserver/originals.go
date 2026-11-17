package dbserver

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"strings"

	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
	"github.com/WhiteRoomLab/whiteroom/internal/fspath"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// tempPrefix starts the name of a temporary file in files/, which has 16 lower case hex digits after it.
const tempPrefix = ".tmp-"

// originalSpaceStep is how much of an original is written between two looks at the free space.
const originalSpaceStep = 8 << 20

// errStopped is the error of a sync of an original after the server has stopped, whether that sync or another has failed:
// the page cache may then show names that never reach the disk, which later syncs do not write.
var errStopped = errors.New(stoppedText)

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

// readError returns err, from reading path in the data directory, with path in place of the operation and the path in
// files/ of an *fs.PathError.
func readError(path string, err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		err = pe.Err
	}
	return fmt.Errorf("%s: cannot read: %w", path, err)
}

// removeTemps removes the temporary files in files/ that a daemon has left by ending without removing them: the regular
// files named like those of createTemp, and nothing else. The server holds the writer lock, so no other temporary file
// is in use.
func (s *Server) removeTemps() error {
	entries, err := s.files.ReadDir(".")
	if err != nil {
		return readError(fspath.Join(s.name, "files"), err)
	}
	for _, e := range entries {
		if !isTempName(e.Name()) || !e.Type().IsRegular() {
			continue
		}
		if err := s.files.Remove(e.Name()); err != nil && !errors.Is(err, fs.ErrNotExist) {
			path := fspath.Join(s.name, "files/"+e.Name())
			var pe *fs.PathError
			if errors.As(err, &pe) {
				err = pe.Err
			}
			return fmt.Errorf("%s: cannot remove: %w", path, err)
		}
	}
	return nil
}

// createTemp creates a new temporary file in files/, .tmp-<16 random hex digits>, opened to write, and returns it with its
// name.
func (s *Server) createTemp() (vfs.File, string, error) {
	var err error
	for range 10 {
		var b [8]byte
		_, _ = rand.Read(b[:])
		tmp := tempPrefix + hex.EncodeToString(b[:])
		var f vfs.File
		if f, err = s.files.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600); !errors.Is(err, fs.ErrExist) {
			return f, tmp, err
		}
	}
	return nil, "", err
}

// syncOriginal runs sync, a sync of a PutOriginal, unless the server has stopped, and stops the server if it fails.
func (s *Server) syncOriginal(sync func() error) error {
	s.syncs.RLock()
	defer s.syncs.RUnlock()
	if s.stopped.Load() {
		return errStopped
	}
	if err := sync(); err != nil {
		s.stop(err)
		return errStopped
	}
	return nil
}

// mkdirOriginal makes dir, a directory of originals in files/, if it is missing, and then syncs files/, unless it has
// synced files/ after an earlier call made or found dir. A directory that is there may not have a durable name yet: a
// PutOriginal whose sync failed, or a daemon that was killed, may have made it.
func (s *Server) mkdirOriginal(dir string) error {
	switch err := s.files.Mkdir(dir, 0o700); {
	case errors.Is(err, fs.ErrExist):
		if _, ok := s.dirs.Load(dir); ok {
			return nil
		}
	case err != nil:
		return err
	}
	if err := s.syncOriginal(func() error { return s.files.SyncDir(".") }); err != nil {
		return err
	}
	s.dirs.Store(dir, true)
	return nil
}

// originalGate returns why the session may not put an original now, or 0. A refusal leaves the request unread.
func (c *session) originalGate() (dbwire.Code, string) {
	switch {
	case c.role != dbwire.RoleWriter:
		return dbwire.CodeDenied, "only the writer may put an original"
	case c.s.stopped.Load():
		return dbwire.CodeStopped, stoppedText
	}
	return c.space(1, true)
}

func (c *session) putOriginalRequest() bool {
	s := c.s
	bound := 5*s.limits.Object + requestSlack
	if code, msg := c.originalGate(); code != 0 {
		return c.refuseBody(bound, code, msg)
	}
	f, tmp, err := s.createTemp()
	if err != nil {
		return c.refuseBody(bound, dbwire.CodeFailed, safeText(err))
	}
	r, closes, readErr := c.storeOriginal(f, tmp)
	switch {
	case readErr != nil:
		c.bad(readErr)
		return false
	case r == nil:
		return c.ok(nil)
	case closes:
		return c.refuseAndClose(r.code, r.msg)
	}
	return c.fail(r.code, r.msg)
}

// storeOriginal reads the chunks and the trailer of a PutOriginal into the temporary file tmp, which f has open, and makes
// it the original. The temporary file is gone when it returns, unless it has become the original. It returns the refusal to
// answer, if any, with whether the connection must close, as the request is not read to its end; or the error of reading
// the request.
func (c *session) storeOriginal(f vfs.File, tmp string) (r *refusal, closes bool, readErr error) {
	s, l := c.s, c.s.limits
	renamed, closed := false, false
	closeTemp := func() error {
		if closed {
			return nil
		}
		closed = true
		return f.Close()
	}
	defer func() {
		_ = closeTemp()
		if !renamed {
			_ = s.files.Remove(tmp)
		}
	}()
	failed := func(err error) (*refusal, bool, error) {
		if errors.Is(err, errStopped) {
			return &refusal{dbwire.CodeStopped, stoppedText}, false, nil
		}
		return &refusal{dbwire.CodeFailed, safeText(err)}, false, nil
	}

	d := &dbwire.Dec{R: c.rd}
	chunks := dbwire.NewChunkReader(d, math.MaxInt64)
	digest := sha256.New()
	buf := make([]byte, 32<<10)
	var size, checked int64
	for {
		// Not past the byte that makes the original too large: a peer that lies about a chunk gets no more of the bytes.
		n, err := chunks.Read(buf[:min(int64(len(buf)), l.Object-size+1)])
		if n > 0 {
			size += int64(n)
			if size > l.Object {
				return &refusal{dbwire.CodeLimit, fmt.Sprintf("the original is larger than %d bytes", l.Object)}, true, nil
			}
			if size-checked >= s.spaceStep {
				checked = size
				// The file of the original is made: the step needs bytes only.
				if code, msg := c.space(s.spaceStep, false); code != 0 {
					return &refusal{code, msg}, true, nil
				}
			}
			if _, err := f.Write(buf[:n]); err != nil {
				return &refusal{dbwire.CodeFailed, safeText(err)}, true, nil
			}
			digest.Write(buf[:n])
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, false, err
		}
	}
	hash, trailerSize, err := dbwire.ReadPutOriginalTrailer(d)
	if err != nil {
		return nil, false, err
	}
	sum := [32]byte(digest.Sum(nil))
	switch {
	case size == 0:
		return &refusal{dbwire.CodeRule, "the original is empty"}, false, nil
	case trailerSize != size || hash != sum:
		return &refusal{dbwire.CodeHash, "the data do not have the hash and the size that were given"}, false, nil
	}

	if err := s.syncOriginal(f.Sync); err != nil {
		return failed(err)
	}
	if err := closeTemp(); err != nil {
		return failed(err)
	}
	if err := s.hook("original:synced"); err != nil {
		return failed(err)
	}
	digits := hex.EncodeToString(sum[:])
	dir := digits[:2]
	if err := s.mkdirOriginal(dir); err != nil {
		return failed(err)
	}
	if err := s.files.Rename(tmp, dir+"/"+digits); err != nil {
		return failed(err)
	}
	renamed = true
	if err := s.syncOriginal(func() error { return s.files.SyncDir(dir) }); err != nil {
		return failed(err)
	}
	if err := s.hook("original:renamed"); err != nil {
		return failed(err)
	}
	// Waits for the syncs that run: a failed one may have dropped names that the syncs above took as written.
	s.syncs.Lock()
	stopped := s.stopped.Load()
	s.syncs.Unlock()
	if stopped {
		return failed(errStopped)
	}
	return nil, false, nil
}

func (c *session) originalRequest() bool {
	hash, err := dbwire.ReadOriginalRequest(&dbwire.Dec{R: c.rd})
	if err != nil {
		c.bad(err)
		return false
	}
	s, l := c.s, c.s.limits
	digits := hex.EncodeToString(hash[:])
	file := digits[:2] + "/" + digits
	name := "files/" + file
	// Lstat before opening: opening a named pipe would wait for a writer.
	info, err := s.files.Lstat(file)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return c.fail(dbwire.CodeNotFound, name+": not found")
	case err != nil:
		return c.fail(dbwire.CodeFailed, readError(name, err).Error())
	case info.Mode()&fs.ModeSymlink != 0:
		return c.fail(dbwire.CodeFailed, name+": symbolic links are not allowed")
	case !info.Mode().IsRegular():
		return c.fail(dbwire.CodeFailed, name+": must be a regular file")
	case info.Size() > l.Object:
		return c.fail(dbwire.CodeFailed, fmt.Sprintf("%s: too large: %d bytes (max %d)", name, info.Size(), l.Object))
	}
	f, err := s.files.Open(file)
	if err != nil {
		return c.fail(dbwire.CodeFailed, readError(name, err).Error())
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	switch {
	case err != nil:
		return c.fail(dbwire.CodeFailed, readError(name, err).Error())
	case !st.Mode().IsRegular():
		return c.fail(dbwire.CodeFailed, name+": must be a regular file")
	case st.Size() > l.Object:
		return c.fail(dbwire.CodeFailed, fmt.Sprintf("%s: too large: %d bytes (max %d)", name, st.Size(), l.Object))
	}

	size := st.Size()
	c.wr.Begin(dbwire.KindOK)
	e := &dbwire.Enc{W: c.wr}
	e.OriginalReply(size)
	if e.Err != nil {
		_ = c.wr.Fail(dbwire.CodeInternal, "the reply could not be written")
		return false
	}
	if text := c.sendFile(f, name, size); text != "" {
		return c.fail(dbwire.CodeFailed, text)
	}
	return c.wr.End() == nil
}

// sendFile writes the size bytes of f, which has the name in errors, to the reply. It returns the text of the error that
// ends the reply, or "" if all was sent; a write that fails ends the session, which has no more to say to its peer, so
// it returns "" then too and the End that follows fails.
func (c *session) sendFile(f vfs.File, name string, size int64) string {
	buf := make([]byte, 32<<10)
	for left := size; left > 0; {
		n, err := f.Read(buf[:min(int64(len(buf)), left)])
		if n > 0 {
			if _, werr := c.wr.Write(buf[:n]); werr != nil {
				return ""
			}
			left -= int64(n)
		}
		switch {
		case errors.Is(err, io.EOF) && left > 0:
			return name + ": the file is shorter than its size"
		case err != nil && !errors.Is(err, io.EOF):
			return readError(name, err).Error()
		}
	}
	var one [1]byte
	if n, _ := f.Read(one[:]); n > 0 {
		return name + ": the file is longer than its size"
	}
	return ""
}
