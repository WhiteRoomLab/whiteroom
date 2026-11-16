package dbserver

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/WhiteRoomLab/whiteroom/internal/fspath"
	"github.com/WhiteRoomLab/whiteroom/internal/logdb/vfs"
)

// makeDirs makes the directories of CreateDir; a variable for the tests, which look at the call.
var makeDirs = fspath.MakeDirs

// CreateDir makes a data directory at dir, which must be missing or an empty directory: it makes the parents that are
// missing, and syncs the names that it has made (fspath.MakeDirs), and then what Create makes in it. If it fails after
// making something in dir, CreateDir needs dir emptied before it can try again.
func CreateDir(dir string, o Options) error {
	if err := makeDirs(dir); err != nil {
		return err
	}
	if entries, err := os.ReadDir(dir); err != nil {
		return err
	} else if len(entries) > 0 {
		return fmt.Errorf("%s: not empty; a new data directory needs a missing or empty directory", dir)
	}
	root, _, err := dataRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return Create(root, dir, o)
}

// OpenDir opens the data directory dir as the writer of its engine (Open), by the path that the kernel resolves. Unless
// o.FreeSpace is given, the free space is that of the file system of dir (not checked on a system other than Linux).
func OpenDir(dir string, o Options) (*Server, error) {
	root, resolved, err := dataRoot(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	if o.FreeSpace == nil && spaceChecked {
		o.FreeSpace = func() (uint64, error) { return freeSpace(resolved) }
	}
	return Open(root, dir, o)
}

// dataRoot opens the directory dir on the OS, and returns the path that it has opened.
func dataRoot(dir string) (root vfs.FS, opened string, err error) {
	// logdb joins the names of its files to the name of the root as text, which drops a ".." after a symbolic link: such a
	// root is opened by its resolved path.
	if fspath.Clean(dir) != filepath.Clean(dir) {
		if dir, err = fspath.Resolve(dir); err != nil {
			return nil, "", err
		}
	}
	if root, err = vfs.OpenRoot(dir); err != nil {
		return nil, "", err
	}
	return root, dir, nil
}
