// Package publish publishes the site of a store: it builds it from a snapshot of the store (Snapshot) with
// internal/build, one build at a time, after the operations that change what is published (4.3, 4.4).
package publish

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

// settingsName is the name of the settings document in errors, that of its file in a site directory (4.3).
const settingsName = "site/settings.json"

// documentKinds are the kinds of the documents of a Source, in the order in which the build reads them.
var documentKinds = []content.RefKind{
	content.RefEntry, content.RefTerm, content.RefMedia, content.RefAuthor, content.RefMenu, content.RefForm,
	content.RefRedirect,
}

// Source is the site of a store at the time of its Snapshot: a build.Source, and so an edit.Source. Its methods may be
// called concurrently.
type Source struct {
	settings    content.SiteFile
	settingsErr error // the store has no settings document: Settings returns it
	schema      content.SiteFile
	docs        map[content.RefKind]map[string]content.SiteFile
	store       *store.Store
	theme       fs.FS
}

// Snapshot reads the settings document and the current documents of every kind of s in one View, and returns them as
// a Source with schema, the schema file, and theme, the files of the theme, which the store does not keep. What is
// saved after Snapshot returns does not change what Settings and Documents return.
//
// The revisions and the originals are read when the build asks for them. The store never changes or removes a
// revision or an original, so the build sees the site of one point in time although it reads them later (4.2). The
// Source holds the bytes of every document, as the build does (4.3, on the memory of the build).
func Snapshot(ctx context.Context, s *store.Store, schema content.SiteFile, theme fs.FS) (*Source, error) {
	src := &Source{schema: schema, store: s, theme: theme}
	if err := s.View(ctx, src.read); err != nil {
		return nil, err
	}
	return src, nil
}

// read reads the documents in tx, which Snapshot gives to a View.
func (src *Source) read(tx *store.Tx) error {
	d, err := tx.Document(store.SettingsKind, "")
	switch {
	case errors.Is(err, fs.ErrNotExist):
		src.settingsErr = err
	case err != nil:
		return err
	default:
		src.settings = content.SiteFile{Name: settingsName, Data: d.Data}
	}
	src.docs = make(map[content.RefKind]map[string]content.SiteFile, len(documentKinds))
	for _, kind := range documentKinds {
		docs, err := tx.Documents(kind)
		if err != nil {
			return err
		}
		dir := content.DocumentDir(kind)
		files := make(map[string]content.SiteFile, len(docs))
		for _, d := range docs {
			files[d.ID] = content.SiteFile{Name: dir + "/" + d.ID + ".json", Data: d.Data}
		}
		src.docs[kind] = files
	}
	return nil
}

// Settings returns the settings document and the schema file. If the store has no settings document, the error is the
// store's, which wraps fs.ErrNotExist.
func (src *Source) Settings() (settings, schema content.SiteFile, err error) {
	if src.settingsErr != nil {
		return content.SiteFile{}, content.SiteFile{}, src.settingsErr
	}
	return src.settings, src.schema, nil
}

// Documents returns the documents of kind by ID, in a new map.
func (src *Source) Documents(kind content.RefKind) (map[string]content.SiteFile, error) {
	files, ok := src.docs[kind]
	if !ok {
		return nil, fmt.Errorf("unsupported kind %q", kind)
	}
	return maps.Clone(files), nil
}

// Revision reads the revision with id from the store when it is called, as a revision never changes. If there is none,
// the error wraps fs.ErrNotExist.
func (src *Source) Revision(id string) (content.SiteFile, error) {
	var rev store.Revision
	err := src.store.View(context.Background(), func(tx *store.Tx) (err error) {
		rev, err = tx.Revision(id)
		return err
	})
	if err != nil {
		return content.SiteFile{}, err
	}
	return content.SiteFile{Name: "revisions/" + id + ".json", Data: rev.Data}, nil
}

func (src *Source) OpenMedia(hash string) (fs.File, error) {
	return src.store.OpenOriginal(hash)
}

func (src *Source) Theme() (fs.FS, error) {
	return src.theme, nil
}
