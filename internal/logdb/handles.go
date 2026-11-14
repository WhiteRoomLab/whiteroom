package logdb

import (
	"errors"
	"fmt"
	"path"
	"sync"

	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// handles are the open files of the segments, which the objects in records are read from: the writer's file of each
// segment that it has written to, and the others, opened on demand. There is no cache of data, so reads see what is
// on disk. Views that run when Close is called keep the files open until the last one ends.
type handles struct {
	fsys   vfs.FS
	mu     sync.Mutex
	files  map[segID]*handle // nil once closed
	views  int               // that run
	closed bool              // by Close
}

// handle is the open file of a segment, with a size that the file has had, or 0.
type handle struct {
	f    vfs.File
	size int64
}

// has reports whether the file is known to have had size bytes at off.
func (e *handle) has(off segOff, size int64) bool {
	return size <= e.size-int64(off)
}

// check returns an error unless the file, which is at path p, has had size bytes at off. It asks the size of the file
// when the one that it knows is too small: the last segment grows. The caller holds the mu of the handles.
func (e *handle) check(p string, off segOff, size int64) error {
	if e.has(off, size) {
		return nil
	}
	info, err := e.f.Stat()
	if err != nil {
		return err
	}
	if e.size = info.Size(); !e.has(off, size) {
		reason := fmt.Sprintf("has %d bytes, too few for an object of %d bytes at offset %d", e.size, size, off)
		return corruptFile(p, reason)
	}
	return nil
}

type segID struct {
	gen   genNum
	index segNum
}

func newHandles(fsys vfs.FS) *handles {
	return &handles{fsys: fsys, files: map[segID]*handle{}}
}

// add makes f the file of the segment index of generation gen, which the handles then close.
func (h *handles) add(gen genNum, index segNum, f vfs.File) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.files[segID{gen, index}] = &handle{f: f}
}

// read returns the size bytes at off of the segment index of generation gen. It allocates them only if the file has
// had them, as a checkpoint may name a size that no record has.
func (h *handles) read(gen genNum, index segNum, off segOff, size int64) ([]byte, error) {
	p := path.Join(h.fsys.Name(), segName(gen, index))
	h.mu.Lock()
	e, err := h.open(gen, index)
	if err == nil {
		err = e.check(p, off, size)
	}
	h.mu.Unlock()
	if err != nil {
		return nil, err
	}
	b := make([]byte, size)
	if err := readAt(e.f, b, int64(off), p); err != nil {
		return nil, err
	}
	return b, nil
}

// readPrefix reads the segment index of generation gen as readPrefix reads a file.
func (h *handles) readPrefix(gen genNum, index segNum, limit segOff) ([]byte, error) {
	f, err := h.file(gen, index)
	if err != nil {
		return nil, err
	}
	return readOpen(f, path.Join(h.fsys.Name(), segName(gen, index)), int64(limit))
}

func (h *handles) file(gen genNum, index segNum) (vfs.File, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	e, err := h.open(gen, index)
	if err != nil {
		return nil, err
	}
	return e.f, nil
}

// open returns the handle of the segment index of generation gen, which it opens if the handles do not have it. The
// caller holds mu.
func (h *handles) open(gen genNum, index segNum) (*handle, error) {
	if h.files == nil {
		return nil, errClosed
	}
	id := segID{gen, index}
	if e := h.files[id]; e != nil {
		return e, nil
	}
	name := segName(gen, index)
	f, err := h.fsys.Open(name)
	if err != nil {
		return nil, entryError(path.Join(h.fsys.Name(), name), err, false)
	}
	e := &handle{f: f}
	h.files[id] = e
	return e, nil
}

// drop closes the file of the segment index of generation gen, if the handles have it, and forgets it.
func (h *handles) drop(gen genNum, index segNum) {
	h.mu.Lock()
	defer h.mu.Unlock()
	id := segID{gen, index}
	if e := h.files[id]; e != nil {
		_ = e.f.Close()
		delete(h.files, id)
	}
}

// enter counts a View that begins, unless Close has been called.
func (h *handles) enter() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return false
	}
	h.views++
	return true
}

func (h *handles) leave() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.views--
	if h.closed && h.views == 0 {
		_ = h.closeFiles()
	}
}

// close closes the files, or leaves them to the last View that runs.
func (h *handles) close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	if h.views > 0 {
		return nil
	}
	return h.closeFiles()
}

func (h *handles) closeFiles() error {
	var errs []error
	for _, e := range h.files {
		errs = append(errs, e.f.Close())
	}
	h.files = nil
	return errors.Join(errs...)
}
