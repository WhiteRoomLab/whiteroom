// Package build builds the public site of a Source as static files: it reads the settings, the documents and the
// theme, checks what is published, and makes the pages and the other files.
package build

import (
	"io/fs"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// Source is what the build reads a site from. It returns the documents as files, which the build decodes, so that the
// problems in them have their lines and columns. Its methods may be called concurrently.
type Source interface {
	// Settings returns the settings document and the schema file.
	Settings() (settings, schema content.SiteFile, err error)
	// Documents returns the documents of kind (entries, terms, media, authors, menus, forms or redirects) by ID.
	Documents(kind content.RefKind) (map[string]content.SiteFile, error)
	// Revision returns the revision whose ID is id; if there is none, the error wraps fs.ErrNotExist.
	Revision(id string) (content.SiteFile, error)
	// OpenMedia opens the original of the medium whose Hash is hash; if there is none, the error wraps fs.ErrNotExist.
	OpenMedia(hash string) (fs.File, error)
	// Theme returns the files of the theme.
	Theme() (fs.FS, error)
}
