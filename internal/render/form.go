package render

import (
	"fmt"
	"html/template"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// form returns the data of b, a form block at path; ok is false if its form does not exist, which it warns about before
// anything else. It leaves out the fields of unknown types and leaves empty the texts that the form lacks in the
// language of the page, which it warns about at the attribute form.
func (w *walker) form(b *content.Block, path string) (Form, bool) {
	id := b.Attrs.Form
	p := path + "/attrs/form"
	f, ok := w.r.res.Form(id)
	if !ok || f == nil {
		w.warnf(p, "form %q does not exist; not rendered", id)
		return Form{}, false
	}
	language := w.r.language
	d := Form{Common: w.common(b, path), Action: template.URL(w.r.sitePath("/_forms/" + id)), Language: language}
	// text returns l in the language of the page; what names l in the warning if it is missing there.
	text := func(l content.Localized, what string) string {
		s := l[language]
		if s == "" {
			w.warnf(p, "form %q has no %s in %s; rendered empty", id, what, language)
		}
		return s
	}
	for i := range f.Fields {
		x := &f.Fields[i]
		switch x.Type {
		case content.FormText, content.FormEmail, content.FormTel, content.FormTextarea, content.FormSelect,
			content.FormRadio, content.FormCheckbox, content.FormConsent:
		default:
			w.warnf(p, "form %q: field %q has an unknown type %q; left out", id, x.Name, x.Type)
			continue
		}
		field := FormField{
			Name:      x.Name,
			Type:      string(x.Type),
			Required:  x.Required || x.Type == content.FormConsent,
			MinLength: x.MinLength,
			MaxLength: x.MaxLength,
		}
		field.Label = text(x.Label, fmt.Sprintf("label of field %q", x.Name))
		for _, o := range x.Options {
			label := text(o.Label, fmt.Sprintf("label of option %q of field %q", o.Value, x.Name))
			field.Options = append(field.Options, FormOption{Value: o.Value, Label: label})
		}
		if x.Type == content.FormConsent {
			field.Policy = w.policy(x, p)
		}
		d.Fields = append(d.Fields, field)
	}
	d.SubmitLabel = text(f.SubmitLabel, "submitLabel")
	return d, true
}

// policy returns the privacy policy page of x, a consent field, in the language of the page; nil if x has none there,
// or if the page does not exist or is not published, which it warns about at path.
func (w *walker) policy(x *content.FormField, path string) *Policy {
	id := x.Policy[w.r.language]
	if id == "" {
		return nil
	}
	title, p, ok := w.r.res.Entry(id)
	switch {
	case !ok:
		w.warnf(path, "policy %q of field %q does not exist; rendered without the link", id, x.Name)
		return nil
	case p == "":
		w.warnf(path, "policy %q of field %q is not published; rendered without the link", id, x.Name)
		return nil
	}
	return &Policy{Href: template.URL(w.r.sitePath(p)), Title: title}
}
