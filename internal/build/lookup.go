package build

import "github.com/WhiteRoomLab/whiteroom/internal/content"

// ref tells about the entity of kind with the ID id as content.ValidateOptions.Lookup does (siteplan.Site.Ref).
func (b *builder) ref(kind content.RefKind, id string) (content.Ref, bool) {
	return b.sp.Ref(kind, id)
}

// pathRef tells about the page or the term with the ID id as the lookup of content.Site.EntryPath does
// (siteplan.Site.PathRef).
func (b *builder) pathRef(kind content.RefKind, id string) (content.PathRef, bool) {
	return b.sp.PathRef(kind, id)
}

// published returns the entry id if it is published with a valid revision, or nil.
func (b *builder) published(id string) *entryDoc {
	if b.sp.Published(id) == nil {
		return nil
	}
	return b.entries[id]
}
