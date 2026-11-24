//go:build !linux

package dbserver

import (
	"io/fs"

	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// dataLevel is not checked on a system other than Linux, where the daemon does not run and Repair refuses without a boot ID.
func dataLevel(vfs.FS, fs.FileInfo) (int, error) { return -1, nil }
