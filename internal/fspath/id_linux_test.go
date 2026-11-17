package fspath

import (
	"os"
	"syscall"
	"testing"
)

func TestFileID(t *testing.T) {
	dir := t.TempDir()
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	st := info.Sys().(*syscall.Stat_t)
	dev, ino, ok, err := FileID(dir)
	if err != nil || !ok || dev != uint64(st.Dev) || ino != st.Ino { //nolint:unconvert // Dev is a uint32 on mips
		t.Errorf("FileID() = %d, %d, %v, %v, want the device %d and the inode %d of stat", dev, ino, ok, err, st.Dev, st.Ino)
	}
}
