package content

import (
	"encoding/json/v2"
	"slices"
)

// Form is the definition of a contact form, which is not split by language.
type Form struct {
	ID         string
	Title      string // the name in the admin screens
	Fields     []FormField
	Message    Localized // shown after sending, with line breaks, in the languages without ThanksPage
	ThanksPage Localized // ID of the entry to show after sending, in each language without Message
	Notify     []string  // email addresses to notify of submissions
}

// FormFieldType is the type of a form field. Select and radio fields take one of their options,
// and checkbox fields any number of them.
type FormFieldType string

const (
	FormText     FormFieldType = "text"
	FormEmail    FormFieldType = "email"
	FormTel      FormFieldType = "tel"
	FormTextarea FormFieldType = "textarea"
	FormSelect   FormFieldType = "select"
	FormRadio    FormFieldType = "radio"
	FormCheckbox FormFieldType = "checkbox"
	FormConsent  FormFieldType = "consent"
)

type FormField struct {
	Name      string        `json:"name,omitempty"`
	Type      FormFieldType `json:"type,omitempty"`
	Label     Localized     `json:"label,omitempty"`
	Required  bool          `json:"required,omitzero"`  // not for consent, which is always required
	MinLength int           `json:"minLength,omitzero"` // text and textarea: in characters (code points)
	MaxLength int           `json:"maxLength,omitzero"` // text and textarea
	Options   []FormOption  `json:"options,omitempty"`  // select, radio and checkbox
	Policy    Localized     `json:"policy,omitempty"`   // consent: ID of the privacy policy page in each language
}

type FormOption struct {
	Value string    `json:"value,omitempty"` // stored and notified; the same in every language
	Label Localized `json:"label,omitempty"`
}

func (x *Form) UnmarshalJSON(data []byte) error       { return decodeJSON(x, data) }
func (x *FormField) UnmarshalJSON(data []byte) error  { return decodeJSON(x, data) }
func (x *FormOption) UnmarshalJSON(data []byte) error { return decodeJSON(x, data) }

func (x *Form) decode(d *decoder) error {
	var v Form
	version := false
	err := d.object(func(key string) error {
		var err error
		switch key {
		case "schemaVersion":
			err = readSchemaVersion(d)
			version = true
		case "id":
			v.ID, err = d.string()
		case "title":
			v.Title, err = d.string()
		case "fields":
			err = d.array(func(int) error {
				var f FormField
				if err := f.decode(d); err != nil {
					return err
				}
				v.Fields = append(v.Fields, f)
				return nil
			})
		case "message":
			err = v.Message.decode(d)
		case "thanksPage":
			err = v.ThanksPage.decode(d)
		case "notify":
			err = d.array(func(int) error {
				s, err := d.string()
				v.Notify = append(v.Notify, s)
				return err
			})
		default:
			return d.errorf("unknown key")
		}
		return err
	})
	if err != nil {
		return err
	}
	if !version {
		return d.errorf(`missing key "schemaVersion"`)
	}
	*x = v
	return nil
}

// decode accepts keys that the type does not use, such as options in a text field, for validation to report.
func (x *FormField) decode(d *decoder) error {
	var v FormField
	err := d.object(func(key string) error {
		var err error
		switch key {
		case "name":
			v.Name, err = d.string()
		case "type":
			var s string
			s, err = d.string()
			v.Type = FormFieldType(s)
		case "label":
			err = v.Label.decode(d)
		case "required":
			v.Required, err = d.bool()
		case "minLength":
			v.MinLength, err = readInt(d, func(n int) error { return checkPositive("minLength", n) })
		case "maxLength":
			v.MaxLength, err = readInt(d, func(n int) error { return checkPositive("maxLength", n) })
		case "options":
			err = d.array(func(int) error {
				var o FormOption
				if err := o.decode(d); err != nil {
					return err
				}
				v.Options = append(v.Options, o)
				return nil
			})
		case "policy":
			err = v.Policy.decode(d)
		default:
			return d.errorf("unknown key")
		}
		return err
	})
	if err != nil {
		return err
	}
	*x = v
	return nil
}

func (x *FormOption) decode(d *decoder) error {
	var v FormOption
	err := d.object(func(key string) error {
		var err error
		switch key {
		case "value":
			v.Value, err = d.string()
		case "label":
			err = v.Label.decode(d)
		default:
			return d.errorf("unknown key")
		}
		return err
	})
	if err != nil {
		return err
	}
	*x = v
	return nil
}

func (x Form) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		SchemaVersion int         `json:"schemaVersion"`
		ID            string      `json:"id,omitempty"`
		Title         string      `json:"title,omitempty"`
		Fields        []FormField `json:"fields,omitempty"`
		Message       Localized   `json:"message,omitempty"`
		ThanksPage    Localized   `json:"thanksPage,omitempty"`
		Notify        []string    `json:"notify,omitempty"`
	}{
		SchemaVersion: SchemaVersion,
		ID:            x.ID,
		Title:         x.Title,
		Fields:        x.Fields,
		Message:       x.Message,
		ThanksPage:    x.ThanksPage,
		Notify:        x.Notify,
	})
}

// Normalize cleans up the texts and the addresses in place as Revision.Normalize does texts and URLs, maps the domains
// of the addresses to lower case, and removes the languages and the addresses of which nothing is left.
func (x *Form) Normalize() {
	x.Title = normalizeText(x.Title, false)
	for i := range x.Fields {
		x.Fields[i].normalize()
	}
	x.Message.normalize(true)
	for i, a := range x.Notify {
		x.Notify[i] = normalizeEmail(a)
	}
	x.Notify = slices.DeleteFunc(x.Notify, func(a string) bool { return a == "" })
	// Empty is nil, as UnmarshalJSON makes it.
	if len(x.Notify) == 0 {
		x.Notify = nil
	}
}

func (x *FormField) normalize() {
	x.Label.normalize(false)
	for i := range x.Options {
		o := &x.Options[i]
		o.Value = normalizeText(o.Value, false)
		o.Label.normalize(false)
	}
}
