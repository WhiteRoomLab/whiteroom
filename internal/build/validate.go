package build

import (
	"crypto/sha256"
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
		if d := b.entries[id]; d.E.Status == content.StatusPublished {
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

// validateEntry checks d, a published entry, and then reads and checks its revision, which d gets if it is valid: with
// its excerpt, without its body, and with the name and the SHA-256 of its file but not its data.
func (b *builder) validateEntry(d *entryDoc, p *problems) error {
	if err := d.E.Validate(b.site); err != nil {
		p.addErr(d.File, err)
		return nil
	}
	f, err := b.src.Revision(d.E.PublishedRevision)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		p.add(d.File, "/publishedRevision", "revision %q does not exist", d.E.PublishedRevision)
		return nil
	case err != nil:
		return err
	}
	var rev content.Revision
	if !decodeFile(p, f, rev.UnmarshalJSON) {
		return nil
	}
	// Import and no Theme: saving accepted legacy blocks and missing alts, and theme mismatches only warn when rendering.
	opts := content.RevisionOptions{Site: b.site, Entry: &d.E, Publish: true, Import: true, Lookup: b.ref}
	if err := rev.Validate(opts); err != nil {
		p.addErr(f, err)
		return nil
	}
	d.excerpt = excerpt(&rev)
	// The files and their bodies are most of what a large site keeps in memory; renderEntry reads the file again.
	rev.Body = nil
	d.revSum, d.RevFile, d.Rev = sha256.Sum256(f.Data), content.SiteFile{Name: f.Name}, &rev
	b.revisions[f.Name] = d
	return nil
}

// validateDocs adds the errors of validate for each of docs to p.
func validateDocs[T any](p *problems, docs map[string]*doc[T], validate func(T) error) {
	for _, id := range slices.Sorted(maps.Keys(docs)) {
		p.addErr(docs[id].File, validate(docs[id].V))
	}
}
