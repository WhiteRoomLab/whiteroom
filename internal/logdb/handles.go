package logdb

import (
	"errors"
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
	files  map[segID]vfs.File // nil once closed
	views  int                // that run
	closed bool               // by Close
}

type segID struct {
	gen   genNum
	index segNum
}

func newHandles(fsys vfs.FS) *handles {
	return &handles{fsys: fsys, files: map[segID]vfs.File{}}
}

// add makes f the file of the segment index of generation gen, which the handles then close.
func (h *handles) add(gen genNum, index segNum, f vfs.File) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.files[segID{gen, index}] = f
}

// readAt reads len(b) bytes at off of the segment index of generation gen.
func (h *handles) readAt(gen genNum, index segNum, b []byte, off segOff) error {
	f, err := h.file(gen, index)
	if err != nil {
		return err
	}
	return readAt(f, b, int64(off), path.Join(h.fsys.Name(), segName(gen, index)))
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
	if h.files == nil {
		return nil, errClosed
	}
	id := segID{gen, index}
	if f := h.files[id]; f != nil {
		return f, nil
	}
	name := segName(gen, index)
	f, err := h.fsys.Open(name)
	if err != nil {
		return nil, entryError(path.Join(h.fsys.Name(), name), err, false)
	}
	h.files[id] = f
	return f, nil
}

// drop closes the file of the segment index of generation gen, if the handles have it, and forgets it.
func (h *handles) drop(gen genNum, index segNum) {
	h.mu.Lock()
	defer h.mu.Unlock()
	id := segID{gen, index}
	if f := h.files[id]; f != nil {
		_ = f.Close()
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
	for _, f := range h.files {
		errs = append(errs, f.Close())
	}
	h.files = nil
	return errors.Join(errs...)
}
