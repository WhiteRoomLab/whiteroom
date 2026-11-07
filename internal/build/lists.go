package build

import "github.com/WhiteRoomLab/whiteroom/internal/siteplan"

// docOf returns the entry of the build that e, an entry of siteplan, is, or nil for nil.
func (b *builder) docOf(e *siteplan.Entry) *entryDoc {
	if e == nil {
		return nil
	}
	return b.entries[e.E.ID]
}

// docsOf returns the entries of the build that es, entries of siteplan, are, in their order, or nil for nil.
func (b *builder) docsOf(es []*siteplan.Entry) []*entryDoc {
	if es == nil {
		return nil
	}
	ds := make([]*entryDoc, len(es))
	for i, e := range es {
		ds[i] = b.entries[e.E.ID]
	}
	return ds
}
