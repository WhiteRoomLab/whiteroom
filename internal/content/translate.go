package content

import (
	"encoding/json/jsontext"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// Translate changes x, a revision of an entry of the type t, into its translation into language (6.4.5). The entries that
// links go to, in the body and in the fields that hold links, the parent and the terms, also in the fields of type term,
// are replaced by their translations, which entry and term find by ID. A link that has none stays; a term or a parent
// that has none is left out. Every one of them is a warning, at its JSON Pointer in x as it was; the warnings are sorted
// by pointer. t is nil if the site has no such type, and then the fields stay as they are. Entry and media fields stay:
// they may refer to other languages.
func (x *Revision) Translate(t *TypeDef, language string, entry, term func(id string) (string, bool)) Errors {
	tr := translator{language: language, entry: entry, term: term}
	if x.Parent != "" {
		if id, ok := moved(entry, x.Parent); ok {
			x.Parent = id
		} else {
			tr.warnf("/parent", "entry %q has no translation in %s; the parent is left out", x.Parent, language)
			x.Parent = ""
		}
	}
	for _, taxonomy := range slices.Sorted(maps.Keys(x.Terms)) {
		var ids []string
		for i, id := range x.Terms[taxonomy] {
			if to, ok := moved(term, id); ok {
				ids = append(ids, to)
			} else {
				tr.warnf("/terms/"+escapePointer(taxonomy)+"/"+strconv.Itoa(i), "term %q has no translation in %s; it is left out", id, language)
			}
		}
		if len(ids) > 0 {
			x.Terms[taxonomy] = ids
		} else {
			delete(x.Terms, taxonomy)
		}
	}
	if len(x.Terms) == 0 {
		x.Terms = nil
	}
	if t != nil {
		tr.fields(&x.Fields, t.Fields)
	}
	tr.blocks(x.Body, "/body")
	slices.SortStableFunc(tr.errs, func(a, b *Error) int { return strings.Compare(a.Path, b.Path) })
	return tr.errs
}

type translator struct {
	language    string
	entry, term func(id string) (string, bool)
	errs        Errors
}

// moved returns the translation of id that find gives, if there is one.
func moved(find func(id string) (string, bool), id string) (string, bool) {
	if find == nil {
		return "", false
	}
	to, ok := find(id)
	return to, ok && to != ""
}

func (tr *translator) warnf(path, format string, args ...any) {
	tr.errs = append(tr.errs, &Error{Path: path, Msg: fmt.Sprintf(format, args...)})
}

// blocks moves the links in bs at path, in place.
func (tr *translator) blocks(bs Blocks, path string) {
	for i := range bs {
		b := &bs[i]
		p := path + "/" + strconv.Itoa(i)
		tr.richText(b.Text, p+"/content")
		tr.blocks(b.Children, p+"/content")
		tr.richText(b.Attrs.Citation, p+"/attrs/citation")
		tr.richText(b.Attrs.Caption, p+"/attrs/caption")
		tr.link(b.Attrs.Link, p+"/attrs/link")
	}
}

// richText moves the links in rt at path, in place.
func (tr *translator) richText(rt RichText, path string) {
	for i, n := range rt {
		for j, m := range n.Marks {
			if m.Type == MarkLink {
				tr.link(m.Link, path+"/"+strconv.Itoa(i)+"/marks/"+strconv.Itoa(j)+"/attrs")
			}
		}
	}
}

func (tr *translator) link(l *Link, path string) {
	if l == nil || l.Entry == "" {
		return
	}
	if id, ok := moved(tr.entry, l.Entry); ok {
		l.Entry = id
		return
	}
	tr.warnf(path+"/entry", "entry %q has no translation in %s; the link still goes to it", l.Entry, tr.language)
}

// fields moves the links and the terms in the values of the fields, of which fs are the definitions, at /fields.
func (tr *translator) fields(vs *Fields, fs []FieldDef) {
	for _, name := range slices.Sorted(maps.Keys(*vs)) {
		f := field(fs, name)
		if f == nil {
			continue
		}
		if v := tr.field(f, (*vs)[name], "/fields/"+escapePointer(name)); v != nil {
			(*vs)[name] = v
		} else {
			delete(*vs, name)
		}
	}
	if len(*vs) == 0 {
		*vs = nil
	}
}

// moves reports whether the values of f can hold what a translation moves: links and terms.
func (f *FieldDef) moves() bool {
	switch f.Type {
	case FieldRichText, FieldBlocks, FieldLink, FieldTerm:
		return true
	case FieldGroup:
		return slices.ContainsFunc(f.Fields, func(g FieldDef) bool { return g.moves() })
	case FieldList:
		return f.Of != nil && f.Of.moves()
	case FieldText, FieldTextarea, FieldNumber, FieldBoolean, FieldDate, FieldDatetime, FieldSelect, FieldMedia, FieldEntry:
	}
	return false
}

// field returns v, a value of f at path, with its links and terms moved, written as FieldDef.normalize writes it; nil if
// nothing is left of it. It returns v as it is if f holds nothing to move or v is not a value of f. Entry and media
// fields keep their values, which may refer to other languages (6.4.5).
func (tr *translator) field(f *FieldDef, v jsontext.Value, path string) jsontext.Value {
	if !f.moves() {
		return v
	}
	if f.countable() {
		ms, err := items(v, path)
		item := f.item()
		if err != nil || item == nil {
			return v
		}
		var out []jsontext.Value
		for _, m := range ms {
			if n := tr.field(item, m.value, m.path); n != nil {
				out = append(out, n)
			}
		}
		if len(out) == 0 {
			return nil
		}
		return marshalValue(out)
	}
	switch f.Type {
	case FieldRichText:
		var rt RichText
		if unmarshal(v, path, rt.decode) != nil || len(rt) == 0 {
			return v
		}
		tr.richText(rt, path)
		return marshalValue(rt)
	case FieldBlocks:
		var bs Blocks
		if unmarshal(v, path, bs.decode) != nil || len(bs) == 0 {
			return v
		}
		tr.blocks(bs, path)
		return marshalValue(bs)
	case FieldLink:
		var l Link
		if unmarshal(v, path, l.decode) != nil {
			return v
		}
		tr.link(&l, path)
		return marshalValue(l)
	case FieldTerm:
		var id string
		read := func(d *decoder) (err error) {
			id, err = d.string()
			return err
		}
		if unmarshal(v, path, read) != nil {
			return v
		}
		if to, ok := moved(tr.term, id); ok {
			return marshalString(to)
		}
		tr.warnf(path, "term %q has no translation in %s; it is left out", id, tr.language)
		return nil
	case FieldGroup:
		ms, err := members(v, path)
		if err != nil {
			return v
		}
		vs := make(Fields, len(ms))
		for _, m := range ms {
			w := m.value
			if g := field(f.Fields, m.name); g != nil {
				w = tr.field(g, m.value, m.path)
			}
			if w != nil {
				vs[m.name] = w
			}
		}
		if len(vs) == 0 {
			return nil
		}
		return marshalValue(vs)
	case FieldText, FieldTextarea, FieldNumber, FieldBoolean, FieldDate, FieldDatetime, FieldSelect, FieldMedia, FieldEntry,
		FieldList:
	}
	return v
}
