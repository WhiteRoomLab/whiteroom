//go:build linux || darwin

package store

import (
	"bytes"
	"path/filepath"
	"syscall"
	"testing"
)

// TestOriginalNamedPipe checks that OpenOriginal reports a named pipe as the file of an original without opening it,
// which would wait for a writer, and that PutOriginal replaces it.
func TestOriginalNamedPipe(t *testing.T) {
	forBackends(t, twoBackends, func(t *testing.T, tb testBackend) {
		s, dir := newStore(t, tb)
		data := []byte("GIF89a")
		hash := sum(data)
		path := originalPath(dir, hash)
		mkdir(t, filepath.Dir(path))
		if err := syscall.Mkfifo(path, 0o600); err != nil {
			t.Skipf("cannot make named pipes: %v", err)
		}
		f, err := s.OpenOriginal(hash)
		if f != nil {
			_ = f.Close()
		}
		if want := originalName(hash) + ": must be a regular file"; f != nil || err == nil || err.Error() != want {
			t.Errorf("OpenOriginal() of a named pipe = %v, %v; want an error %q", f, err, want)
		}
		putOriginal(t, s, data)
		if got, err := readOriginal(t, s, hash); err != nil || !bytes.Equal(got, data) {
			t.Errorf("OpenOriginal() after PutOriginal() reads %q, %v; want %q", got, err, data)
		}
	})
}
