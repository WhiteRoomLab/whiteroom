package fspath

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
)

// MakeDirs makes the directory dir and its parents that are missing, as os.MkdirAll does, and then makes the name of
// each directory that it has made durable, the outermost first, with SyncParent.
func MakeDirs(dir string) error {
	return makeDirs(dir, SyncParent)
}

// makeDirs is MakeDirs with syncParent, which the tests replace.
func makeDirs(dir string, syncParent func(dir string) error) error {
	missing := missingDirs(dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for _, name := range slices.Backward(missing) {
		if err := syncParent(name); err != nil {
			return err
		}
	}
	return nil
}

// missingDirs returns dir and its parents, as far as they are missing, the innermost first. The paths are as dir is,
// relative or not: the absolute path of the working directory may be one that cannot be walked.
func missingDirs(dir string) []string {
	var missing []string
	for name := Clean(dir); ; name = Join(name, "..") {
		// A parent that cannot be looked at is not missing: os.MkdirAll tells why it cannot make dir. Nor is the ".."
		// of what is not a directory, as a symbolic link to nothing, which has no parent to go on with.
		if _, err := os.Lstat(name); !errors.Is(err, fs.ErrNotExist) || filepath.Base(name) == ".." {
			return missing
		}
		missing = append(missing, name)
	}
}

// SyncParent syncs the directory that holds the directory dir. The name of a directory that has just been made is
// durable only after it (3.5): MakeDirs does it for the directories that it makes, and a caller that makes the
// directory that it gives them does it for that one.
func SyncParent(dir string) error {
	d, err := os.Open(Join(dir, ".."))
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}
