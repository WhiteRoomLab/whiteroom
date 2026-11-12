package build

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
)

// Releases is what ListReleases finds in an output directory.
type Releases struct {
	// Current is the release that Out/current points to; "" if there is none, or if it points to a release that does not
	// exist.
	Current string
	// Names are the releases, in the order of their names; empty if there are none. Each is a whole release: Build
	// renames a release to releases/.tmp-* before it removes it, so what a removal that stopped half way left is not one.
	Names []string
}

// ListReleases returns the releases in out, an output directory of Build, and the one that out/current points to. It
// checks out as Build does: anything in it that Build has not made is an error, and so is a missing out, whose error
// wraps fs.ErrNotExist. It takes no lock and writes nothing, so while a build runs it may see out between two steps of
// the build.
func ListReleases(out string) (Releases, error) {
	if out == "" {
		return Releases{}, errors.New("no output directory")
	}
	root, err := os.OpenRoot(out)
	if err != nil {
		return Releases{}, err
	}
	defer func() { _ = root.Close() }()
	l, err := checkLayout(root)
	if err != nil {
		return Releases{}, err
	}
	return Releases{Current: l.current, Names: l.releases}, nil
}

// Rollback points out/current to the release releases/<name> of out, an output directory of Build, at once, by renaming
// a new relative symbolic link over it as Build does, under the lock of the builds. It reports whether current has
// changed: it has not if it pointed to the release already. It changes nothing else: it removes no release, and not
// what builds that did not end left, which the next build removes. The release that current pointed to before stays,
// and another Rollback can point to it, until a build removes it.
//
// The errors are: no output directory, if out is empty; invalid release name "<name>", if name is not 16 lower case hex
// digits; those of ListReleases, if out holds what Build has not made; <out>/releases/<name>: no such release, which wraps
// fs.ErrNotExist; another build is running in <out>, if a build or another Rollback holds the lock, which Rollback does
// not wait for; and those of the file system, which wrap their causes, such as fs.ErrNotExist for a missing out. If
// Rollback fails, current is as it was. Like Build, it needs flock(2).
func Rollback(out, name string) (changed bool, err error) {
	switch {
	case out == "":
		return false, errors.New("no output directory")
	case !isHex16(name):
		return false, fmt.Errorf("invalid release name %q", name)
	}
	root, err := os.OpenRoot(out)
	if err != nil {
		return false, err
	}
	defer func() { _ = root.Close() }()
	// Also before the lock, so that no .lock is made in a directory of other files, or without the release.
	if _, err := checkRelease(root, name); err != nil {
		return false, err
	}
	unlock, err := lockOut(root)
	if err != nil {
		return false, err
	}
	defer unlock()
	return rollbackLocked(root, name)
}

// rollbackLocked is Rollback in root, an output directory that the caller has locked. It checks root again, because a
// build may have removed the release since the caller checked it, and current must not point to a release that is
// gone.
func rollbackLocked(root *os.Root, name string) (changed bool, err error) {
	l, err := checkRelease(root, name)
	if err != nil {
		return false, err
	}
	if l.current == name {
		return false, nil
	}
	if err := pointCurrent(root, name); err != nil {
		return false, err
	}
	return true, nil
}

// noReleaseError is the error of a release that the output directory does not have, which wraps fs.ErrNotExist.
type noReleaseError struct{ path string }

func (e noReleaseError) Error() string { return e.path + ": no such release" }
func (e noReleaseError) Unwrap() error { return fs.ErrNotExist }

// checkRelease returns what root, an output directory, holds (checkLayout), if it holds the release name.
func checkRelease(root *os.Root, name string) (*layout, error) {
	l, err := checkLayout(root)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(l.releases, name) {
		return nil, noReleaseError{filepath.Join(root.Name(), releasesDir, name)}
	}
	return l, nil
}
