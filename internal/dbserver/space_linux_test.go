package dbserver

import (
	"math"
	"syscall"
	"testing"
)

// inodesCounted reports whether the file system of dir counts its inodes, as statfs tells it.
func inodesCounted(t testing.TB, dir string) bool {
	t.Helper()
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		t.Fatal(err)
	}
	return st.Files != 0 && st.Files != math.MaxUint64 && st.Ffree != math.MaxUint64
}

// TestSpaceOf checks the free space that a statfs tells: the inodes are not known where the total is 0 (btrfs, a tmpfs
// without a limit of inodes) or where a count is -1.
func TestSpaceOf(t *testing.T) {
	for _, tt := range []struct {
		name         string
		files, ffree uint64
		want         Space
	}{
		{"counted", 1000, 10, Space{Bytes: 3 * 4096, Inodes: 10, HasInodes: true}},
		{"none free", 1000, 0, Space{Bytes: 3 * 4096, HasInodes: true}},
		{"no total", 0, 0, Space{Bytes: 3 * 4096}},
		{"a total of -1", math.MaxUint64, 10, Space{Bytes: 3 * 4096}},
		{"a free count of -1", 1000, math.MaxUint64, Space{Bytes: 3 * 4096}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			st := syscall.Statfs_t{Bsize: 4096, Blocks: 100, Bfree: 5, Bavail: 3, Files: tt.files, Ffree: tt.ffree}
			if got := spaceOf(&st); got != tt.want {
				t.Errorf("spaceOf() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
