package build

import (
	"encoding/json/jsontext"
	"fmt"
	"html/template"
	"strconv"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/render"
)

// renderEntry renders the body of d, a published entry, and the values of its blocks and richText fields, also in
// groups and lists, with r, and returns their HTML by their JSON Pointers in the revision, such as "/body" and
// "/fields/spec/note", as entryData takes them, and the CSS that they need. The body and the blocks fields are one
// document (6.5.1): the body, then the blocks fields in the order of their definitions, depth first into groups and
// lists. The CSS has the rules of that document, then those of the richText fields in the same order. The warnings go
// to b.warnings at their places in the revision. The error is that of executing a block template, after the name of
// the file of the revision.
func (b *builder) renderEntry(d *entryDoc, r *render.Renderer) (map[string]template.HTML, render.CSS, error) {
	f := entryFields{blocks: []content.Blocks{d.rev.Body}, blockPaths: []string{"/body"}}
	f.fields(b.site.Type(d.e.Type).Fields, d.rev.Fields, "/fields")
	htmls, css, warnings, err := r.Document(f.blocks...)
	if err != nil {
		return nil, render.CSS{}, fmt.Errorf("%s: %w", d.revFile.Name, err)
	}
	html := make(map[string]template.HTML, len(htmls)+len(f.texts))
	for i, h := range htmls {
		html[f.blockPaths[i]] = h
	}
	for _, w := range warnings {
		b.warnings.add(d.revFile, f.blockPaths[w.List]+w.Path, "%s", w.Msg)
	}
	for i, rt := range f.texts {
		h, c, warnings := r.RichText(rt)
		html[f.textPaths[i]] = h
		css.Add(c)
		for _, w := range warnings {
			b.warnings.add(d.revFile, f.textPaths[i]+w.Path, "%s", w.Msg)
		}
	}
	return html, css, nil
}

// entryFields collects the values of the blocks and the richText fields of a revision with their JSON Pointers, in the
// order of the definitions, depth first into groups and lists.
type entryFields struct {
	blocks     []content.Blocks
	blockPaths []string
	texts      []content.RichText
	textPaths  []string
}

// fields collects the values of the fields defs at path, whose JSON values vs has by name.
func (x *entryFields) fields(defs []content.FieldDef, vs map[string]jsontext.Value, path string) {
	for i := range defs {
		f := &defs[i]
		x.value(f, vs[f.Name], path+"/"+f.Name)
	}
}

// value collects v, the JSON of a value of f at path (nil if it has none): a list of blocks, rich text, or those in a
// group or a list.
func (x *entryFields) value(f *content.FieldDef, v jsontext.Value, path string) {
	if v == nil {
		return
	}
	switch f.Type {
	case content.FieldBlocks:
		var bs content.Blocks
		if bs.UnmarshalJSON(v) == nil {
			x.blocks, x.blockPaths = append(x.blocks, bs), append(x.blockPaths, path)
		}
	case content.FieldRichText:
		var rt content.RichText
		if rt.UnmarshalJSON(v) == nil {
			x.texts, x.textPaths = append(x.texts, rt), append(x.textPaths, path)
		}
	case content.FieldGroup:
		vs, _ := decodeValue[map[string]jsontext.Value](v)
		x.fields(f.Fields, vs, path)
	case content.FieldList:
		items, _ := decodeValue[[]jsontext.Value](v)
		if f.Of != nil {
			for i, item := range items {
				x.value(f.Of, item, path+"/"+strconv.Itoa(i))
			}
		}
	case content.FieldText, content.FieldTextarea, content.FieldNumber, content.FieldBoolean, content.FieldDate,
		content.FieldDatetime, content.FieldSelect, content.FieldMedia, content.FieldLink, content.FieldEntry,
		content.FieldTerm:
	}
}
