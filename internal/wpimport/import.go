package wpimport

import (
	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/wprest"
	"github.com/WhiteRoomLab/whiteroom/internal/wxr"
)

// Import is the import of one WordPress site. A conversion records the documents that it makes with AddRef, and Ref
// gives them back to the validation of the other documents. The methods must not be called at the same time.
type Import struct {
	File string // the name of the WXR, as given to Read
	WXR  *wxr.Export
	// Report is where the conversions record what they did not import or changed.
	Report *Report
	// IDs gives each object of WordPress its ID.
	IDs *Table
	// REST is what the REST API of the site gave, or nil if it is not used.
	REST *wprest.Snapshot
	// RESTFile is the name of the REST API in the report: the name of the file of the snapshot, or Snapshot.API if the
	// snapshot was read from the site.
	RESTFile string

	refs map[string]importRef
	urls *urlIndex // made when the links of a body are first converted
	// patternBodies are the contents of the synced patterns, read when a body first copies the pattern, by its post ID.
	patternBodies map[int64]*patternBody
	// forms are the forms of Contact Form 7 in the WXR, collected when a body first looks for one; formsRead tells that.
	forms     []*wxr.Item
	formsRead bool
}

type importRef struct {
	kind content.RefKind
	ref  content.Ref
}

// New returns the import of the site of e, which Read read from the file called file, with the report rep. The caller
// sets REST and RESTFile if the REST API is used.
func New(file string, e *wxr.Export, rep *Report) *Import {
	return &Import{File: file, WXR: e, Report: rep, IDs: NewTable(Seed(e)), refs: make(map[string]importRef)}
}

// AddRef records that the entity with the ID id of kind has been made, with r as Lookup tells of it. It replaces an
// earlier record of the same ID.
func (imp *Import) AddRef(kind content.RefKind, id string, r content.Ref) {
	if imp.refs == nil {
		imp.refs = make(map[string]importRef)
	}
	imp.refs[id] = importRef{kind: kind, ref: r}
}

// DropRef forgets the entity with the ID id.
func (imp *Import) DropRef(id string) {
	delete(imp.refs, id)
}

// Ref tells about the entity with the ID id, if it was recorded as one of kind. It is the Lookup of
// content.ValidateOptions.
func (imp *Import) Ref(kind content.RefKind, id string) (content.Ref, bool) {
	r, ok := imp.refs[id]
	if !ok || r.kind != kind {
		return content.Ref{}, false
	}
	return r.ref, true
}
