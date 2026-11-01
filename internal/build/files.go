package build

import (
	"os"
	"path"
)

// writeFile writes data as the file name, a path with "/" in root, creating the directories that it is in. It fails if
// the file exists, so that no file of a release is written twice.
func writeFile(root *os.Root, name string, data []byte) error {
	if err := root.MkdirAll(path.Dir(name), 0o755); err != nil {
		return err
	}
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}
