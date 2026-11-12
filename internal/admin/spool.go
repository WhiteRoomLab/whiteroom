package admin

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

// spoolMemory is how much a spool keeps in memory: what is written beyond it goes to a temporary file of the store.
const spoolMemory = 256 << 10

// spool holds a body that is received, or an answer that is made, outside the memory of the heavy work: up to
// spoolMemory bytes in memory, and the rest in a temporary file of the store (files/.tmp-*), which Close removes. It is
// written once, from the start, and then read from the start as often as needed; it is not for concurrent use.
type spool struct {
	st   *store.Store
	mem  bytes.Buffer
	file *store.TempFile // nil until the data is more than spoolMemory
	size int64
}

func newSpool(st *store.Store) *spool { return &spool{st: st} }

func (s *spool) Write(p []byte) (int, error) {
	if s.file == nil && int64(s.mem.Len())+int64(len(p)) > spoolMemory {
		f, err := s.st.CreateTemp()
		if err != nil {
			return 0, err
		}
		if _, err := f.Write(s.mem.Bytes()); err != nil {
			_ = f.Close()
			return 0, err
		}
		s.file = f
		s.mem = bytes.Buffer{}
	}
	if s.file != nil {
		n, err := s.file.Write(p)
		s.size += int64(n)
		return n, err
	}
	n, err := s.mem.Write(p)
	s.size += int64(n)
	return n, err
}

// Len returns the number of bytes written.
func (s *spool) Len() int64 { return s.size }

// spilled reports whether the data is in a temporary file.
func (s *spool) spilled() bool { return s.file != nil }

// Reader returns a reader of the data from its start, which replaces the one that an earlier call returned.
func (s *spool) Reader() (io.Reader, error) {
	if s.file == nil {
		return bytes.NewReader(s.mem.Bytes()), nil
	}
	if _, err := s.file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return s.file, nil
}

// bytes returns the data as one slice, which is the memory of the spool itself unless the data is in a file.
func (s *spool) bytes() ([]byte, error) {
	if s.file == nil {
		return s.mem.Bytes(), nil
	}
	r, err := s.Reader()
	if err != nil {
		return nil, err
	}
	data := make([]byte, s.size)
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, err
	}
	return data, nil
}

// Close drops the memory and removes the temporary file. Only the first call does.
func (s *spool) Close() error {
	s.mem = bytes.Buffer{}
	if s.file == nil {
		return nil
	}
	return s.file.Close()
}

// readFailure remembers the first error of the Reads that is not io.EOF, to tell a failed read from a failed write when io.Copy
// returns the error of either.
type readFailure struct {
	r   io.Reader
	err error
}

func (f *readFailure) Read(p []byte) (int, error) {
	n, err := f.r.Read(p)
	if err != nil && !errors.Is(err, io.EOF) && f.err == nil {
		f.err = err
	}
	return n, err
}

// sendSpool sends out as the body of the answer with status, and header besides the Content-Type (application/json unless
// header has one) and the Content-Length. It gives the write a new time limit first: the 60 seconds of the server, and 10
// minutes (slowTransfer) for what is in a file, since the request may have taken minutes to receive. If it cannot read
// out after the answer has begun it logs that and aborts the connection, which tells the client that the body is not whole.
func (s *Server) sendSpool(w http.ResponseWriter, r *http.Request, status int, header http.Header, out *spool) {
	if status == 0 {
		status = http.StatusOK
	}
	rd, err := out.Reader()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	limit := writeTimeout
	if out.spilled() {
		limit = slowTransfer
	}
	_ = extendWrite(w, limit)
	h := w.Header()
	for name, values := range header {
		h.Del(name)
		for _, v := range values {
			h.Add(name, v)
		}
	}
	if h.Get("Content-Type") == "" {
		h.Set("Content-Type", "application/json")
	}
	noBody := status == http.StatusNoContent || status == http.StatusNotModified || status < http.StatusOK
	if !noBody {
		h.Set("Content-Length", strconv.FormatInt(out.Len(), 10))
	}
	w.WriteHeader(status)
	if noBody || r.Method == http.MethodHead {
		return
	}
	failure := &readFailure{r: rd}
	if _, err := io.Copy(w, failure); err != nil && failure.err != nil {
		s.log.Printf("%s %q: %v", r.Method, r.RequestURI, fmt.Errorf("cannot read the answer: %w", failure.err))
		panic(http.ErrAbortHandler)
	}
}
