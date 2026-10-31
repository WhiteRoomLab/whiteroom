package content

import (
	"encoding/json/v2"
	"fmt"
	"slices"
	"strconv"
)

// Form is the definition of a contact form, which is not split by language.
type Form struct {
	ID          string
	Title       string // the name in the admin screens
	Fields      []FormField
	SubmitLabel Localized // the text of the submit button, one line in each language
	Message     Localized // shown after sending, with line breaks, in the languages without ThanksPage
	ThanksPage  Localized // ID of the entry to show after sending, in each language without Message
	Notify      []string  // email addresses to notify of submissions
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

// formFieldTypes are the types of form fields, in the order that messages list them.
var formFieldTypes = []string{
	string(FormText), string(FormEmail), string(FormTel), string(FormTextarea),
	string(FormSelect), string(FormRadio), string(FormCheckbox), string(FormConsent),
}

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
		case "submitLabel":
			err = v.SubmitLabel.decode(d)
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
		SubmitLabel   Localized   `json:"submitLabel,omitempty"`
		Message       Localized   `json:"message,omitempty"`
		ThanksPage    Localized   `json:"thanksPage,omitempty"`
		Notify        []string    `json:"notify,omitempty"`
	}{
		SchemaVersion: SchemaVersion,
		ID:            x.ID,
		Title:         x.Title,
		Fields:        x.Fields,
		SubmitLabel:   x.SubmitLabel,
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
	x.SubmitLabel.normalize(false)
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

// Validate checks x in site. ID, Title and Fields are required, and so are SubmitLabel and the labels of the fields and
// the options in every language of site, and exactly one of Message and ThanksPage in each language.
// lookup tells about the entries of ThanksPage and the policies as ValidateOptions.Lookup does;
// nil skips checking that they exist and are in the languages of their keys.
func (x Form) Validate(site *Site, lookup func(kind RefKind, id string) (Ref, bool)) error {
	c := checker{doc: &docState{opts: ValidateOptions{Lookup: lookup}}}
	x.check(&c, site)
	return c.result()
}

func (x Form) check(c *checker, site *Site) {
	c.required("id", x.ID, func(s string) error { return checkID(RefForm, s) })
	c.required("title", x.Title, checkLine)
	switch n := len(x.Fields); {
	case n == 0:
		c.addf("/fields", "missing fields")
	case n > MaxFormFields:
		c.addf("/fields", "too many fields (%d; max %d)", n, MaxFormFields)
	}
	names := make(map[string]bool)
	for i := range x.Fields {
		f := &x.Fields[i]
		p := "/fields/" + strconv.Itoa(i)
		if f.Name == "" {
			c.addf(p+"/name", "missing name")
		} else if err := checkFormFieldName(f.Name); err != nil {
			c.add(p+"/name", err)
		} else if names[f.Name] {
			c.addf(p+"/name", "duplicate field name %q", f.Name)
		}
		names[f.Name] = true
		f.check(c, p, site)
	}
	x.SubmitLabel.checkRequired(c, "/submitLabel", "submitLabel", site, false)
	x.checkThanks(c, site)
	if n := len(x.Notify); n > MaxNotifyAddresses {
		c.addf("/notify", "too many addresses (%d; max %d)", n, MaxNotifyAddresses)
	}
	addresses := make(map[string]bool)
	for i, a := range x.Notify {
		p := "/notify/" + strconv.Itoa(i)
		if err := checkEmail(a); err != nil {
			c.add(p, err)
		} else if addresses[a] {
			c.addf(p, "duplicate address %q", a)
		}
		addresses[a] = true
	}
}

// checkThanks checks Message and ThanksPage, what is shown after sending, of which each language of site needs exactly one.
func (x Form) checkThanks(c *checker, site *Site) {
	x.Message.check(c, "/message", site, true)
	if len(x.Message) == 0 && len(x.ThanksPage) == 0 {
		c.addf("/message", "missing message or thanksPage")
	} else {
		for _, language := range site.Languages {
			if x.Message[language] == "" && x.ThanksPage[language] == "" {
				c.addf("/message/"+escapePointer(language), "missing message or thanksPage in %s", language)
			}
		}
	}
	x.ThanksPage.checkEntries(c, "/thanksPage", site)
	for _, language := range site.Languages {
		if x.Message[language] != "" && x.ThanksPage[language] != "" {
			c.addf("/thanksPage/"+escapePointer(language), "cannot be combined with message in %s", language)
		}
	}
}

// check checks x, a field at path, apart from its name, which Form.check compares with those of the other fields.
func (x *FormField) check(c *checker, path string, site *Site) {
	known := slices.Contains(formFieldTypes, string(x.Type))
	switch {
	case x.Type == "":
		c.addf(path+"/type", "missing type")
	case !known:
		c.add(path+"/type", checkChoice("field type", string(x.Type), formFieldTypes...))
	}
	x.Label.checkRequired(c, path+"/label", "label", site, false)
	if !known {
		// The keys that an unknown type can use are unknown.
		return
	}
	notAllowed := func(set bool, key string) {
		if set {
			c.addf(path+"/"+key, "not allowed for %s fields", x.Type)
		}
	}
	if x.Required && x.Type == FormConsent {
		c.addf(path+"/required", "not allowed for consent, which is always required")
	}
	if x.Type == FormText || x.Type == FormTextarea {
		checkLimits(c, path, "minLength", x.MinLength, "maxLength", x.MaxLength)
	} else {
		notAllowed(x.MinLength != 0, "minLength")
		notAllowed(x.MaxLength != 0, "maxLength")
	}
	if x.Type == FormSelect || x.Type == FormRadio || x.Type == FormCheckbox {
		x.checkOptions(c, path+"/options", site)
	} else {
		notAllowed(len(x.Options) > 0, "options")
	}
	if x.Type == FormConsent {
		x.Policy.checkEntries(c, path+"/policy", site)
	} else {
		notAllowed(len(x.Policy) > 0, "policy")
	}
}

// checkOptions checks the options of x, a field of a type with options, at path.
func (x *FormField) checkOptions(c *checker, path string, site *Site) {
	switch n := len(x.Options); {
	case n == 0:
		c.addf(path, "missing options")
	case n > MaxFormOptions:
		c.addf(path, "too many options (%d; max %d)", n, MaxFormOptions)
	}
	values := make(map[string]bool)
	for i := range x.Options {
		o := &x.Options[i]
		p := path + "/" + strconv.Itoa(i)
		if o.Value == "" {
			c.addf(p+"/value", "missing value")
		} else if err := checkLine(o.Value); err != nil {
			c.add(p+"/value", err)
		} else if values[o.Value] {
			c.addf(p+"/value", "duplicate option %q", o.Value)
		}
		values[o.Value] = true
		o.Label.checkRequired(c, p+"/label", "label", site, false)
	}
}

// checkEntries checks x at path, the ID of an entry in each language, as check does texts.
// Each entry must be in the language of its key.
func (x Localized) checkEntries(c *checker, path string, site *Site) {
	x.checkValues(c, path, site, false, func(p, language, id string) {
		if r, ok := c.ref(p, RefEntry, id); ok && r.Language != language {
			c.addf(p, "entry %q is in the language %q, not %q", id, r.Language, language)
		}
	})
}

// checkFormFieldName accepts a name as checkName does that starts with an ASCII letter.
// Names starting with "_" are reserved for the CMS, such as for spam protection.
func checkFormFieldName(s string) error {
	if err := checkName(s); err != nil {
		return err
	}
	if c := s[0]; (c < 'a' || 'z' < c) && (c < 'A' || 'Z' < c) {
		return fmt.Errorf(`invalid field name %q: start with an ASCII letter (names starting with "_" are reserved)`, s)
	}
	return nil
}
