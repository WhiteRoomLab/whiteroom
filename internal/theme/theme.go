// Package theme reads themes, the directories in which developers write how a site looks: the settings
// (theme.yaml), the page templates, the components, the translations and the assets.
package theme

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

const MaxAssetBytes = 64 << 20 // bytes in each file in assets/

// Theme is a theme that Load has read and checked.
type Theme struct {
	Settings *content.ThemeSettings // of theme.yaml

	translations map[string]map[string]string // texts by key, by language
	keys         []string                     // keys of the default translation, in its order
	templates    map[string]string            // sources of the page templates by file name, such as "entry.html"
	components   map[string]string            // sources of the components by name, such as "entry/card"
	assets       map[string]asset             // files in assets/ by their paths relative to it, such as "css/style.css"
}

// Load reads the theme in fsys: theme.yaml, templates/ and, if there are any, components/, assets/ and
// translations/. It ignores hidden entries, whose names start with ".", everywhere, and the other entries at the root,
// such as README.md. Symbolic links and the other entries that are neither regular files nor directories are errors.
// The error is content.Errors, each with File set to the path of its file from the root, such as
// "templates/entry.html", sorted by file, with theme.yaml first, then translations/, components/, templates/ and
// assets/, and by position. The theme is nil with an error.
func Load(fsys fs.FS) (*Theme, error) {
	l := &loader{
		fsys: fsys,
		t: &Theme{
			translations: make(map[string]map[string]string),
			templates:    make(map[string]string),
			components:   make(map[string]string),
			assets:       make(map[string]asset),
		},
		translations: make(map[string]*translationFile),
	}
	l.readRoot()
	l.checkTemplates()
	l.checkTranslations()
	if len(l.errs) > 0 {
		slices.SortStableFunc(l.errs, compareErrors)
		return nil, l.errs
	}
	return l.t, nil
}

// loader keeps what Load has read, for the checks across files, and the problems it has found.
type loader struct {
	fsys fs.FS
	t    *Theme
	errs content.Errors

	settings *yamlFile // theme.yaml, if it has loaded

	templatesRead bool     // whether templates/ has been read
	templateFiles []string // names of the entries in templates/ except hidden ones, whatever they are

	translationsUnread bool                        // whether translations/ is there but has not been read
	languages          []string                    // languages of the files in translations/, loaded or not
	translations       map[string]*translationFile // the files that have loaded, by language
}

func (l *loader) addf(file, format string, args ...any) {
	l.errs = append(l.errs, &content.Error{File: file, Msg: fmt.Sprintf(format, args...)})
}

func (l *loader) add(file string, err error) {
	l.errs = append(l.errs, &content.Error{File: file, Msg: err.Error()})
}

// addErrors adds the errors of reading file, which content.Errors has with their files and positions.
func (l *loader) addErrors(file string, err error) {
	var es content.Errors
	if errors.As(err, &es) {
		l.errs = append(l.errs, es...)
		return
	}
	l.add(file, err)
}

// ioError adds err from reading file.
func (l *loader) ioError(file string, err error) {
	l.addf(file, "cannot read: %v", pathless(err))
}

// pathless returns err without the operation and the path of an *fs.PathError, which the File of an error tells.
func pathless(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

// fileGroups are the first segments of the paths of files, in the order of their errors.
var fileGroups = []string{"theme.yaml", "translations", "components", "templates", "assets"}

func compareErrors(a, b *content.Error) int {
	return cmp.Or(
		cmp.Compare(fileGroup(a.File), fileGroup(b.File)),
		strings.Compare(a.File, b.File),
		cmp.Compare(a.Line, b.Line),
		cmp.Compare(a.Column, b.Column),
	)
}

func fileGroup(file string) int {
	first, _, _ := strings.Cut(file, "/")
	if i := slices.Index(fileGroups, first); i >= 0 {
		return i
	}
	return len(fileGroups)
}
