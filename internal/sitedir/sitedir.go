// Package sitedir reads a site from a directory, which keeps it until there is a database:
//
//	site/settings.json    the settings document
//	site/schema.yaml      the schema file
//	entries/<ID>.json     the entries by their IDs, and likewise the terms, the media, the authors, the menus, the
//	                      forms and the redirects in terms/, media/, authors/, menus/, forms/ and redirects/
//	revisions/<ID>.json   the revisions of the entries
//	files/<hex>           the originals of the media, by the 64 lower case hex digits of their SHA-256
//	theme/                the theme
//
// It reads nothing outside the directory, and does not follow symbolic links, which are errors. It ignores hidden
// entries, whose names start with ".", and the other entries at the root, such as README.md.
package sitedir

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"sync"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

const MaxDocumentBytes = 64 << 20 // bytes in a document or a file of the site settings

// Dir is a site directory that Open has opened. Its methods may be called concurrently. The problems of its files
// are content.Errors, each with File set to the path of its file from the directory, such as "site/schema.yaml".
type Dir struct {
	root *os.Root

	mu    sync.Mutex
	theme *os.Root // theme/, once Theme has opened it
}

func Open(dir string) (*Dir, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	return &Dir{root: root}, nil
}

// Close closes the directory, and the FS that Theme has returned.
func (d *Dir) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	var err error
	if d.theme != nil {
		err = d.theme.Close()
		d.theme = nil
	}
	return errors.Join(err, d.root.Close())
}

// Settings reads the files of the site settings. The error has the problems of both.
func (d *Dir) Settings() (settings, schema content.SiteFile, err error) {
	if _, e := d.stat("site", true); e != nil {
		return content.SiteFile{}, content.SiteFile{}, content.Errors{e}
	}
	var errs content.Errors
	read := func(name string) content.SiteFile {
		data, e := d.readFile(name)
		if e != nil {
			errs = append(errs, e)
		}
		return content.SiteFile{Name: name, Data: data}
	}
	// In the order of the names, which is that of the errors.
	schema = read("site/schema.yaml")
	settings = read("site/settings.json")
	if len(errs) > 0 {
		return content.SiteFile{}, content.SiteFile{}, errs
	}
	return settings, schema, nil
}

// kindDirs are the directories of the kinds of documents that Documents reads.
var kindDirs = map[content.RefKind]string{
	content.RefEntry:    "entries",
	content.RefTerm:     "terms",
	content.RefMedia:    "media",
	content.RefAuthor:   "authors",
	content.RefMenu:     "menus",
	content.RefForm:     "forms",
	content.RefRedirect: "redirects",
}

// Documents reads the documents of kind from its directory, such as entries/, and returns them by their IDs; none if
// the directory does not exist. Each entry of the directory, apart from hidden ones, must be a regular file named
// <ID>.json with an ID of kind; the error has the problems of every entry, sorted.
func (d *Dir) Documents(kind content.RefKind) (map[string]content.SiteFile, error) {
	dir, ok := kindDirs[kind]
	if !ok {
		return nil, fmt.Errorf("unsupported kind %q", kind)
	}
	info, e := d.stat(dir, true)
	if e != nil {
		return nil, content.Errors{e}
	}
	docs := make(map[string]content.SiteFile)
	if info == nil {
		return docs, nil
	}
	entries, err := fs.ReadDir(d.root.FS(), dir)
	if err != nil {
		return nil, content.Errors{readError(dir, err)}
	}
	var errs content.Errors
	for _, de := range entries {
		if strings.HasPrefix(de.Name(), ".") {
			continue
		}
		id, f, e := d.readDocument(kind, dir, de)
		if e != nil {
			errs = append(errs, e)
			continue
		}
		docs[id] = f
	}
	if len(errs) > 0 {
		return nil, errs
	}
	return docs, nil
}

// readDocument reads the document of kind that de, an entry of dir, is.
func (d *Dir) readDocument(kind content.RefKind, dir string, de fs.DirEntry) (string, content.SiteFile, *content.Error) {
	name := dir + "/" + de.Name()
	if e := checkType(name, de.Type(), false); e != nil {
		return "", content.SiteFile{}, e
	}
	id, ok := strings.CutSuffix(de.Name(), ".json")
	if !ok {
		return "", content.SiteFile{}, errorf(name, "invalid name: use <ID>.json")
	}
	if err := content.CheckID(kind, id); err != nil {
		return "", content.SiteFile{}, errorf(name, "invalid name: use <ID>.json; %v", err)
	}
	data, e := d.readFile(name)
	if e != nil {
		return "", content.SiteFile{}, e
	}
	return id, content.SiteFile{Name: name, Data: data}, nil
}

// Revision reads the revision whose ID is id. If it does not exist, the error wraps fs.ErrNotExist.
func (d *Dir) Revision(id string) (content.SiteFile, error) {
	// Not only for the message: an id with "/" in it would name another file, such as "rev_/../../site/settings".
	if err := content.CheckID(content.RefRevision, id); err != nil {
		return content.SiteFile{}, err
	}
	name := "revisions/" + id + ".json"
	info, e := d.statFile(name)
	if e != nil {
		return content.SiteFile{}, content.Errors{e}
	}
	if info == nil {
		return content.SiteFile{}, notExist(name)
	}
	data, e := d.read(name, info)
	if e != nil {
		return content.SiteFile{}, content.Errors{e}
	}
	return content.SiteFile{Name: name, Data: data}, nil
}

// OpenMedia opens the original whose Media.Hash is hash, without checking it against the hash. If it does not exist,
// the error wraps fs.ErrNotExist.
func (d *Dir) OpenMedia(hash string) (fs.File, error) {
	digits, ok := strings.CutPrefix(hash, "sha256:")
	if !ok || len(digits) != 64 || strings.Trim(digits, "0123456789abcdef") != "" {
		return nil, fmt.Errorf(`invalid hash %q: use "sha256:" and 64 lower case hex digits`, hash)
	}
	name := "files/" + digits
	info, e := d.statFile(name)
	if e != nil {
		return nil, content.Errors{e}
	}
	if info == nil {
		return nil, notExist(name)
	}
	f, err := d.root.Open(name)
	if err != nil {
		return nil, content.Errors{readError(name, err)}
	}
	return f, nil
}

// Theme returns the directory of the theme, theme/, without checking what is in it. Its ReadDir reports symbolic links
// as such, and reading through them cannot leave theme/.
func (d *Dir) Theme() (fs.FS, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.theme == nil {
		info, e := d.stat("theme", true)
		if e == nil && info == nil {
			e = errorf("theme", "missing directory")
		}
		if e != nil {
			return nil, content.Errors{e}
		}
		// Not fs.Sub: a root of its own keeps the symbolic links in theme/ from reaching the rest of the site.
		root, err := d.root.OpenRoot("theme")
		if err != nil {
			return nil, content.Errors{readError("theme", err)}
		}
		d.theme = root
	}
	return d.theme.FS(), nil
}

// stat returns the FileInfo of name, without following a symbolic link, or nil if name does not exist, and the
// problem if it is not a directory, if dir is true, or a regular file, if not.
func (d *Dir) stat(name string, dir bool) (fs.FileInfo, *content.Error) {
	info, err := d.root.Lstat(name)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil
	case err != nil:
		return nil, readError(name, err)
	}
	if e := checkType(name, info.Mode(), dir); e != nil {
		return nil, e
	}
	return info, nil
}

// statFile returns what stat returns for name, a regular file, after checking the directory it is in.
func (d *Dir) statFile(name string) (fs.FileInfo, *content.Error) {
	dir, _, _ := strings.Cut(name, "/")
	if info, e := d.stat(dir, true); e != nil || info == nil {
		return nil, e
	}
	return d.stat(name, false)
}

// readFile reads name, a regular file in a directory that stat has checked.
func (d *Dir) readFile(name string) ([]byte, *content.Error) {
	info, e := d.stat(name, false)
	switch {
	case e != nil:
		return nil, e
	case info == nil:
		return nil, errorf(name, "missing file")
	}
	return d.read(name, info)
}

// read reads name, a regular file whose FileInfo is info, if it has at most MaxDocumentBytes.
func (d *Dir) read(name string, info fs.FileInfo) ([]byte, *content.Error) {
	if info.Size() > MaxDocumentBytes {
		return nil, errorf(name, "too large: %d bytes (max %d)", info.Size(), MaxDocumentBytes)
	}
	f, err := d.root.Open(name)
	if err != nil {
		return nil, readError(name, err)
	}
	defer func() { _ = f.Close() }()
	var b bytes.Buffer
	b.Grow(int(info.Size()) + bytes.MinRead)
	// The file may have grown since its size was read.
	if _, err := b.ReadFrom(io.LimitReader(f, MaxDocumentBytes+1)); err != nil {
		return nil, readError(name, err)
	}
	if b.Len() > MaxDocumentBytes {
		return nil, errorf(name, "too large: at least %d bytes (max %d)", b.Len(), MaxDocumentBytes)
	}
	return b.Bytes(), nil
}

// checkType returns the problem if mode, the type of name, is not that of a directory, if dir is true, or of a
// regular file, if not.
func checkType(name string, mode fs.FileMode, dir bool) *content.Error {
	switch {
	case mode&fs.ModeSymlink != 0:
		return errorf(name, "symbolic links are not allowed")
	case dir && !mode.IsDir():
		return errorf(name, "must be a directory")
	case !dir && !mode.IsRegular():
		return errorf(name, "must be a regular file")
	}
	return nil
}

func errorf(name, format string, args ...any) *content.Error {
	return &content.Error{File: name, Msg: fmt.Sprintf(format, args...)}
}

// readError returns err from reading name, without the operation and the path of an *fs.PathError, which the File of
// the problem tells.
func readError(name string, err error) *content.Error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		err = pe.Err
	}
	return errorf(name, "cannot read: %v", err)
}

func notExist(name string) error {
	return &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}
