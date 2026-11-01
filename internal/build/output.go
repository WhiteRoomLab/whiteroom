package build

import (
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// The output directory holds only what the build makes: the lock file; the directory of the releases, each a
// directory named with 16 hex digits that has the public directory, server.json and nginx.conf; and the symbolic link
// to the release that is published. What builds that did not end leave has names that start with tmpPrefix in the
// directory of the releases, and with linkPrefix.
const (
	lockFile    = ".lock"
	releasesDir = "releases"
	currentLink = "current"
	publicDir   = "public"
	tmpPrefix   = ".tmp-"
	linkPrefix  = "current.tmp-"
)

// What the build requires of what it finds in the output directory.
const (
	outMust      = "the output directory must hold only .lock, releases and current"
	releasesMust = "releases must hold only the releases, directories named with 16 hex digits"
)

// layout is what an output directory holds (checkLayout).
type layout struct {
	current  string   // the release that current points to; "" if there is none
	releases []string // the releases, sorted
	stale    []string // what builds that did not end left, as paths in the output directory, sorted
}

// foreign is something in an output directory that the build has not made.
type foreign struct {
	path string // in the output directory, with "/"
	must string // what the build requires instead
}

// checkLayout returns what root, an output directory, holds: .lock; releases, with the releases; current, a symbolic
// link to releases/<name>; and what builds that did not end left, the directories releases/.tmp-<16 hex digits> and the
// symbolic links current.tmp-<16 hex digits>. Anything else is an error that names it, so that the build never
// removes what it has not made; the errors are sorted by path. A current that points to a release that does not exist
// counts as none.
func checkLayout(root *os.Root) (*layout, error) {
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return nil, outError(root, err)
	}
	l := &layout{}
	var bad []foreign
	target := ""
	for _, e := range entries {
		name := e.Name()
		switch {
		case name == lockFile:
			if !e.Type().IsRegular() {
				bad = append(bad, foreign{name, "it must be a regular file"})
			}
		case name == releasesDir:
			if !e.IsDir() {
				bad = append(bad, foreign{name, "it must be a directory"})
				continue
			}
			rs, err := fs.ReadDir(root.FS(), releasesDir)
			if err != nil {
				return nil, outError(root, err)
			}
			for _, r := range rs {
				switch name := r.Name(); {
				case r.IsDir() && isHex16(name):
					l.releases = append(l.releases, name)
				case r.IsDir() && isTemp(name, tmpPrefix):
					l.stale = append(l.stale, releasesDir+"/"+name)
				default:
					bad = append(bad, foreign{releasesDir + "/" + name, releasesMust})
				}
			}
		case name == currentLink:
			if e.Type() != fs.ModeSymlink {
				bad = append(bad, foreign{name, "it must be a symbolic link to a release"})
				continue
			}
			if target, err = root.Readlink(name); err != nil {
				return nil, outError(root, err)
			}
			if r, ok := strings.CutPrefix(target, releasesDir+"/"); !ok || !isHex16(r) {
				bad = append(bad, foreign{name, fmt.Sprintf("it points to %q, not to a release", target)})
			}
		case isTemp(name, linkPrefix) && e.Type() == fs.ModeSymlink:
			l.stale = append(l.stale, name)
		default:
			bad = append(bad, foreign{name, outMust})
		}
	}
	if len(bad) > 0 {
		slices.SortFunc(bad, func(a, b foreign) int { return cmp.Compare(a.path, b.path) })
		errs := make([]error, len(bad))
		for i, x := range bad {
			errs[i] = notMade(root, x.path, x.must)
		}
		return nil, errors.Join(errs...)
	}
	if r := strings.TrimPrefix(target, releasesDir+"/"); slices.Contains(l.releases, r) {
		l.current = r
	}
	return l, nil
}

// notMade returns the error about name, a path with "/" in root, an output directory, which the build has not made:
// it must be as must says.
func notMade(root *os.Root, name, must string) error {
	return fmt.Errorf("%s: not made by the build; %s", filepath.Join(root.Name(), filepath.FromSlash(name)), must)
}

// outError returns err, an error about a path in root, with the path of root before it; nil if err is nil.
func outError(root *os.Root, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", root.Name(), err)
}

// isHex16 reports whether s is 16 lower case hex digits, as the name of a release.
func isHex16(s string) bool {
	return len(s) == 16 && strings.Trim(s, "0123456789abcdef") == ""
}

// isTemp reports whether name is prefix and 16 lower case hex digits, as the names of temporary files.
func isTemp(name, prefix string) bool {
	s, ok := strings.CutPrefix(name, prefix)
	return ok && isHex16(s)
}

// unique calls create with names of prefix and 16 random hex digits until it has one that does not exist, and returns
// it with the error of create.
func unique(prefix string, create func(name string) error) (string, error) {
	var err error
	for range 10 {
		var b [8]byte
		_, _ = rand.Read(b[:])
		name := prefix + hex.EncodeToString(b[:])
		if err = create(name); !errors.Is(err, fs.ErrExist) {
			return name, err
		}
	}
	return "", err
}

// newRelease makes a temporary directory for a release in root, an output directory: releases/.tmp-<16 random hex
// digits>, with an empty public directory. It returns its path in root and its root.
func newRelease(root *os.Root) (tmp string, rel *os.Root, err error) {
	if err := root.MkdirAll(releasesDir, 0o755); err != nil {
		return "", nil, outError(root, err)
	}
	tmp, err = unique(releasesDir+"/"+tmpPrefix, func(name string) error { return root.Mkdir(name, 0o755) })
	if err != nil {
		return "", nil, outError(root, err)
	}
	if rel, err = root.OpenRoot(tmp); err != nil {
		err = outError(root, err)
	} else if err = rel.Mkdir(publicDir, 0o755); err != nil {
		err = outError(rel, err)
		_ = rel.Close()
	} else {
		return tmp, rel, nil
	}
	return "", nil, errors.Join(err, outError(root, root.RemoveAll(tmp)))
}

// releaseName returns the name of the release in rel, made of its content: the first 16 hex digits of the SHA-256 of
// the list of its files, a line "<path>\t<the hex digits of the SHA-256 of the file>\n" for each, with the path in rel,
// in the order of the bytes of the paths. It reads the files in at most workers goroutines. It also returns the number
// of the files in the public directory. The error is one of reading, or ctx.Err().
func releaseName(ctx context.Context, rel *os.Root, workers int) (name string, files int, err error) {
	var paths []string
	err = fs.WalkDir(rel.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir():
			return nil
		case !d.Type().IsRegular():
			return fmt.Errorf("%s: not a regular file", p)
		}
		paths = append(paths, p)
		return nil
	})
	if err != nil {
		return "", 0, outError(rel, err)
	}
	slices.Sort(paths)
	sums := make([][sha256.Size]byte, len(paths))
	err = forEach(ctx, len(paths), workers, func(i int) error {
		f, err := rel.Open(paths[i])
		if err != nil {
			return err
		}
		h := sha256.New()
		_, err = io.Copy(h, f)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		h.Sum(sums[i][:0])
		return err
	})
	switch {
	case ctx.Err() != nil:
		return "", 0, ctx.Err()
	case err != nil:
		return "", 0, outError(rel, err)
	}
	var list strings.Builder
	for i, p := range paths {
		fmt.Fprintf(&list, "%s\t%x\n", p, sums[i][:])
		if strings.HasPrefix(p, publicDir+"/") {
			files++
		}
	}
	sum := sha256.Sum256([]byte(list.String()))
	return hex.EncodeToString(sum[:8]), files, nil
}

// publish publishes the release in tmp, a temporary directory in the releases of root, an output directory, under its
// name: it renames tmp to releases/<name>, or removes tmp if releases/<name> exists, which then has the same content.
// Then, unless previous, the release that current points to, is name, it points current to releases/<name> by renaming
// a new relative symbolic link over it, which replaces it at once. It reports whether current has changed. If it
// fails, root is as it was, apart from tmp.
func publish(root *os.Root, tmp, name, previous string) (changed bool, err error) {
	dir := releasesDir + "/" + name
	fi, err := root.Lstat(dir)
	made := errors.Is(err, fs.ErrNotExist)
	switch {
	case made:
		err = root.Rename(tmp, dir)
	case err == nil && !fi.IsDir():
		return false, notMade(root, dir, releasesMust)
	case err == nil:
		err = root.RemoveAll(tmp)
	}
	if err != nil || name == previous {
		return false, outError(root, err)
	}
	link, err := unique(linkPrefix, func(link string) error { return root.Symlink(dir, link) })
	if err == nil {
		if err = root.Rename(link, currentLink); err != nil {
			err = errors.Join(outError(root, err), outError(root, root.Remove(link)))
		}
	} else {
		err = outError(root, err)
	}
	if err != nil {
		if made {
			err = errors.Join(err, outError(root, root.RemoveAll(dir)))
		}
		return false, err
	}
	return true, nil
}

// removeOld removes from root, an output directory that held l, the releases of l other than keep, and what builds that
// did not end left. It removes nothing else.
func removeOld(root *os.Root, l *layout, keep []string) error {
	var errs []error
	for _, name := range l.releases {
		if !slices.Contains(keep, name) {
			errs = append(errs, outError(root, root.RemoveAll(releasesDir+"/"+name)))
		}
	}
	for _, name := range l.stale {
		errs = append(errs, outError(root, root.RemoveAll(name)))
	}
	return errors.Join(errs...)
}
