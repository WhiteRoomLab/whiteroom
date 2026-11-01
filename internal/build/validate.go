package build

import (
	"errors"
	"io/fs"
	"maps"
	"slices"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// validate checks what is published, adding the errors to p: the published entries and their revisions, which it
// reads, and every term, medium, author, menu, form and redirect, which have no drafts. It neither checks the entries
// that are not published nor reads their revisions. The error is one of b.src other than a missing revision.
func (b *builder) validate(p *problems) error {
	for _, id := range slices.Sorted(maps.Keys(b.entries)) {
		if d := b.entries[id]; d.e.Status == content.StatusPublished {
			if err := b.validateEntry(d, p); err != nil {
				return err
			}
		}
	}
	validateDocs(p, b.terms, func(t content.Term) error { return t.Validate(b.site, b.ref) })
	validateDocs(p, b.media, func(m content.Media) error { return m.Validate(b.site) })
	validateDocs(p, b.authors, func(a content.AuthorProfile) error { return a.Validate(b.site, b.ref) })
	validateDocs(p, b.menus, func(m content.Menu) error { return m.Validate(b.site, b.ref) })
	validateDocs(p, b.forms, func(f content.Form) error { return f.Validate(b.site, b.ref) })
	validateDocs(p, b.redirects, func(r content.Redirect) error { return r.Validate(b.site, b.ref) })
	return nil
}

// validateEntry checks d, a published entry, and then reads and checks its revision, which d gets if it is valid.
func (b *builder) validateEntry(d *entryDoc, p *problems) error {
	if err := d.e.Validate(b.site); err != nil {
		p.addErr(d.file, err)
		return nil
	}
	f, err := b.src.Revision(d.e.PublishedRevision)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		p.add(d.file, "/publishedRevision", "revision %q does not exist", d.e.PublishedRevision)
		return nil
	case err != nil:
		return err
	}
	var rev content.Revision
	if !decodeFile(p, f, rev.UnmarshalJSON) {
		return nil
	}
	// Import and no Theme: saving accepted legacy blocks and missing alts, and theme mismatches only warn when rendering.
	opts := content.RevisionOptions{Site: b.site, Entry: &d.e, Publish: true, Import: true, Lookup: b.ref}
	if err := rev.Validate(opts); err != nil {
		p.addErr(f, err)
		return nil
	}
	d.revFile, d.rev = f, &rev
	return nil
}

// validateDocs adds the errors of validate for each of docs to p.
func validateDocs[T any](p *problems, docs map[string]*doc[T], validate func(T) error) {
	for _, id := range slices.Sorted(maps.Keys(docs)) {
		p.addErr(docs[id].file, validate(docs[id].v))
	}
}
