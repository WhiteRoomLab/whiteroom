package build

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"html/template"
	"strconv"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/render"
)

// fieldData makes the values of the custom fields of d, a published entry, for a page that r renders. html has the HTML
// of the richText and blocks fields by their JSON Pointers in the revision, such as "/fields/spec/note"; without it,
// they are "".
type fieldData struct {
	b    *builder
	d    *entryDoc
	r    *render.Renderer
	html map[string]template.HTML
}

// fields returns the values of the fields defs at path, whose JSON values vs has by name: every field of defs by name.
func (x fieldData) fields(defs []content.FieldDef, vs map[string]jsontext.Value, path string) map[string]any {
	m := make(map[string]any, len(defs))
	for i := range defs {
		f := &defs[i]
		m[f.Name] = x.value(f, vs[f.Name], path+"/"+f.Name)
	}
	return m
}

// value returns the value of f at path, whose JSON is v (nil if it has none), as the templates get it:
//
//   - text, textarea and select: string, "" without a value; with multiple, []string
//   - richText and blocks: template.HTML, that of html at path
//   - number: float64, or nil
//   - boolean: bool
//   - date: *Date of the date alone, or nil; datetime: *Date in the time zone of the site, or nil
//   - media: *Media, or nil
//   - link: *render.Link, or nil, also if it cannot be linked, which it warns about at path
//   - entry: *EntryLink, or nil; with multiple, []EntryLink
//   - term: *TermLink, or nil; with multiple, []TermLink
//   - group: map[string]any with every field of the group, also without a value
//   - list: []any, the values of the items
func (x fieldData) value(f *content.FieldDef, v jsontext.Value, path string) any {
	switch f.Type {
	case content.FieldText, content.FieldTextarea:
		s, _ := decodeValue[string](v)
		return s
	case content.FieldSelect:
		if f.Multiple {
			ss, _ := decodeValue[[]string](v)
			return ss
		}
		s, _ := decodeValue[string](v)
		return s
	case content.FieldRichText, content.FieldBlocks:
		return x.html[path]
	case content.FieldNumber:
		if n, ok := decodeValue[float64](v); ok {
			return n
		}
	case content.FieldBoolean:
		on, _ := decodeValue[bool](v)
		return on
	case content.FieldDate:
		s, _ := decodeValue[string](v)
		if d := newDay(s); d != nil {
			return d
		}
	case content.FieldDatetime:
		s, _ := decodeValue[string](v)
		if d := newDate(s, x.b.loc); d != nil {
			return d
		}
	case content.FieldMedia:
		if id, ok := decodeValue[string](v); ok {
			if m := x.b.mediaData(id, x.r); m != nil {
				return m
			}
		}
	case content.FieldLink:
		if l := x.link(v, path); l != nil {
			return l
		}
	case content.FieldEntry:
		return refLinks(f, v, func(id string) EntryLink { return x.b.entryLink(id, x.r) })
	case content.FieldTerm:
		return refLinks(f, v, func(id string) TermLink { return x.b.termLink(id, x.r) })
	case content.FieldGroup:
		vs, _ := decodeValue[map[string]jsontext.Value](v)
		return x.fields(f.Fields, vs, path)
	case content.FieldList:
		items, _ := decodeValue[[]jsontext.Value](v)
		var values []any
		if f.Of != nil {
			for i, item := range items {
				values = append(values, x.value(f.Of, item, path+"/"+strconv.Itoa(i)))
			}
		}
		return values
	}
	return nil
}

// link returns the link of v, the JSON of a link at path, or nil if it has none or it cannot be linked, which it warns
// about at path in the revision.
func (x fieldData) link(v jsontext.Value, path string) *render.Link {
	var l content.Link
	if v == nil || l.UnmarshalJSON(v) != nil {
		return nil
	}
	link, warnings := x.r.Link(&l)
	for _, w := range warnings {
		x.b.warnings.add(x.d.revFile, path+w.Path, "%s", w.Msg)
	}
	return link
}

// refLinks returns the link of the ID in v, the JSON of a value of f, an entry or a term field: *T, or nil if it has
// none; with multiple, []T of the IDs.
func refLinks[T any](f *content.FieldDef, v jsontext.Value, link func(id string) T) any {
	if f.Multiple {
		ids, _ := decodeValue[[]string](v)
		var ls []T
		for _, id := range ids {
			ls = append(ls, link(id))
		}
		return ls
	}
	if id, ok := decodeValue[string](v); ok {
		l := link(id)
		return &l
	}
	return nil
}

// decodeValue decodes v, the JSON of a value, as a T; ok is false if v is nil or not a T.
func decodeValue[T any](v jsontext.Value) (x T, ok bool) {
	if v == nil || json.Unmarshal(v, &x) != nil {
		var zero T
		return zero, false
	}
	return x, true
}

// mediaData returns the data of the medium with the ID on a page that r renders, or nil if the resolver does not find
// it, as when it does not exist or the original of an image cannot be read.
func (b *builder) mediaData(id string, r *render.Renderer) *Media {
	m, path, ok := b.res.Media(id)
	if !ok || m == nil {
		return nil
	}
	x := &Media{URL: r.URL(path), Kind: m.Kind(), FileName: m.FileName, MIMEType: m.MIMEType, Size: m.Size}
	if x.Kind == content.MediaImage {
		x.Image = r.PageImage(id)
	}
	return x
}

// newDate returns the date of s, a date and a time in RFC 3339, in loc, or nil if s is not one.
func newDate(s string, loc *time.Location) *Date {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil
	}
	t = t.In(loc)
	return &Date{
		Datetime: t.Format(time.RFC3339),
		Year:     t.Year(),
		Month:    int(t.Month()),
		Day:      t.Day(),
		Hour:     t.Hour(),
		Minute:   t.Minute(),
	}
}

// newDay returns the date of s, a date alone such as "2026-10-25", or nil if s is not one.
func newDay(s string) *Date {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return nil
	}
	return &Date{Datetime: t.Format(time.DateOnly), Year: t.Year(), Month: int(t.Month()), Day: t.Day()}
}
