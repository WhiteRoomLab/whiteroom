// Package fspath makes paths of the local file system that name what the kernel takes the given paths for. The lexical
// functions of path/filepath drop a ".." with the element before it, which the kernel resolves first: if link is a
// symbolic link, filepath.Join("link/..", "out") is "out", but the kernel takes "link/../out" for the out next to what
// link points to.
package fspath

import (
	"cmp"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// Join returns the path of name in the directory dir. It is filepath.Join(dir, name) but for a ".." after a symbolic
// link, which it keeps, with the link: Join("link/..", "out") is "link/../out", and Join("dir/..", "out") is "out". It
// looks at the file system for each ".." after an element that is not "." or "..".
func Join(dir, name string) string {
	p := name
	if dir != "" {
		p = dir + string(filepath.Separator) + name
	}
	if _, afterName := dotDots(p); afterName {
		return clean(p)
	}
	return filepath.Join(dir, name)
}

// Clean returns filepath.Clean(name), but for a ".." after a symbolic link, which it keeps, with the link, as Join does.
func Clean(name string) string {
	if _, afterName := dotDots(name); afterName {
		return clean(name)
	}
	return filepath.Clean(name)
}

// Abs returns an absolute path of name. It is filepath.Abs(name) if name has no "..", and Resolve(name) if it has one:
// filepath.Abs drops a ".." with the element before it, also with the last element of the working directory.
func Abs(name string) (string, error) {
	if dotDot, _ := dotDots(name); dotDot {
		return Resolve(name)
	}
	return filepath.Abs(name) //nolint:forbidigo // name has no "..", the only element that the kernel resolves otherwise
}

// Resolve returns the absolute path of name with its symbolic links resolved as the kernel resolves them. The part at the
// end of name that cannot be resolved, as it is missing or cannot be read, is joined to the resolved path of the rest.
func Resolve(name string) (string, error) {
	prefix, rest := name, ""
	for {
		resolved, err := filepath.EvalSymlinks(prefix)
		if err == nil {
			if !filepath.IsAbs(resolved) {
				// Not os.Getwd alone: it may give $PWD, a path through a symbolic link that a ".." of resolved would cut.
				wd, err := os.Getwd()
				if err != nil {
					return "", err
				}
				if wd, err = filepath.EvalSymlinks(wd); err != nil {
					return "", err
				}
				resolved = filepath.Join(wd, resolved)
			}
			return filepath.Join(resolved, rest), nil
		}
		dir, last := filepath.Split(trimSeparators(prefix))
		if dir == "" {
			dir = "."
		}
		if last == "" || dir == prefix {
			return "", err
		}
		// The rest is missing, and has no symbolic link, or cannot be read by the kernel either.
		rest = filepath.Join(last, rest)
		prefix = dir
	}
}

// clean returns filepath.Clean(name), but that it keeps a ".." after an element that is not a directory, such as a
// symbolic link, or that it cannot look at, with the element: the kernel resolves the element first. It drops a ".."
// after a missing element, which a directory that is made there will not change.
func clean(name string) string {
	vol := filepath.VolumeName(name)
	rooted := len(name) > len(vol) && os.IsPathSeparator(name[len(vol)])
	var elems []string
	path := func() string {
		p := strings.Join(elems, string(filepath.Separator))
		if rooted {
			p = string(filepath.Separator) + p
		}
		return vol + cmp.Or(p, ".")
	}
	for elem := range strings.FieldsFuncSeq(name[len(vol):], isSeparator) {
		switch {
		case elem == ".":
		case elem != "..":
			elems = append(elems, elem)
		case len(elems) == 0:
			if !rooted {
				elems = append(elems, elem)
			}
		case elems[len(elems)-1] == "..", keepsDotDot(path()):
			elems = append(elems, elem)
		default:
			elems = elems[:len(elems)-1]
		}
	}
	return path()
}

// keepsDotDot reports whether a ".." after name must be kept: name is not a directory, or cannot be looked at.
func keepsDotDot(name string) bool {
	info, err := os.Lstat(name)
	if err != nil {
		return !errors.Is(err, fs.ErrNotExist)
	}
	return !info.IsDir()
}

func isSeparator(r rune) bool { return r < utf8.RuneSelf && os.IsPathSeparator(uint8(r)) }

// dotDots reports whether name has a ".." element, and whether one follows an element that may be a symbolic link,
// which is any other than "." and "..".
func dotDots(name string) (dotDot, afterName bool) {
	name = name[len(filepath.VolumeName(name)):]
	named := false
	for elem := range strings.FieldsFuncSeq(name, isSeparator) {
		switch elem {
		case ".":
		case "..":
			if named {
				return true, true
			}
			dotDot = true
		default:
			named = true
		}
	}
	return dotDot, false
}

// trimSeparators returns name without the separators at its end, but for a separator that is all the path after the
// volume name.
func trimSeparators(name string) string {
	end := len(name)
	for end > len(filepath.VolumeName(name))+1 && os.IsPathSeparator(name[end-1]) {
		end--
	}
	return name[:end]
}
