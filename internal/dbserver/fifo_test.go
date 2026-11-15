//go:build linux || darwin

package dbserver

import (
	"bytes"
	"crypto/sha256"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
)

// mkfifo makes a named pipe, or skips the test if the file system cannot.
func mkfifo(t testing.TB, path string) {
	t.Helper()
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("cannot make named pipes: %v", err)
	}
}

// TestOriginalNamedPipe checks that Original reports a named pipe as the file of an original without opening it, which
// would wait for a writer, and that PutOriginal replaces it.
func TestOriginalNamedPipe(t *testing.T) {
	f := newDirFixture(t, nil)
	w := f.connect(dbwire.RoleWriter)
	data := []byte("GIF89a")
	h := sha256.Sum256(data)
	path := diskPath(f.dir, h)
	mkdir(t, filepath.Dir(path))
	mkfifo(t, path)
	_, err := w.original(h)
	re := wantCode(t, err, dbwire.CodeFailed)
	if want := originalName(h) + ": must be a regular file"; re.Msg != want {
		t.Errorf("the text is %q, want %q", re.Msg, want)
	}
	mustPut(t, w, data)
	if got, err := w.original(h); err != nil || !bytes.Equal(got, data) {
		t.Errorf("Original after PutOriginal reads %q, %v; want %q", got, err, data)
	}
}
