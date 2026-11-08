package content

import (
	"bytes"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/WhiteRoomLab/whiteroom/internal/yaml"
)

// settingsKeys are the keys that decodeSettings reads apart from schemaVersion, in the order that MarshalSettings writes
// them: in the schema file, they belong in the settings document.
var settingsKeys = []string{
	"name", "tagline", "url", "timeZone", "logo", "icon", "languages", "defaultLanguage", "prefixDefaultLanguage",
	"frontPage", "postsPage", "postsPerPage", "seo", "allowHtml",
}

// UnmarshalSchema reads data, a schema file such as site/schema.yaml in the subset of YAML that internal/yaml reads,
// into AuthorURL, Types and Taxonomies, which developers write; the other items come from the settings document and
// stay as they are. The mappings of types, taxonomies and fields become lists in the written order, named by their
// keys. The items that data omits become zero, and s changes only if all of data is read without error. An error is
// an *Error at the line and the column of the problem. The values stay as written, without normalization: Validate
// reports those not normalized and the other problems, such as invalid names and options that the type of a field
// does not use.
func (s *Site) UnmarshalSchema(data []byte) error {
	_, err := s.unmarshalSchema(data)
	return err
}

// unmarshalSchema is UnmarshalSchema, and also returns the positions of the values in data by JSON Pointer, as
// yaml.ToJSON does, to locate the errors of Validate.
func (s *Site) unmarshalSchema(data []byte) (map[string]yaml.Position, error) {
	out, positions, err := yaml.ToJSON(data)
	if err != nil {
		var e *yaml.Error
		if !errors.As(err, &e) {
			return nil, &Error{Msg: err.Error()}
		}
		return nil, &Error{Msg: e.Msg, Line: e.Line, Column: e.Column}
	}
	var v Site
	if err := unmarshal(out, "", v.decodeSchema); err != nil {
		return nil, locate(err, "", positions)[0]
	}
	s.AuthorURL, s.Types, s.Taxonomies = v.AuthorURL, v.Types, v.Taxonomies
	return positions, nil
}

func (s *Site) decodeSchema(d *decoder) error {
	version := false
	err := d.object(func(key string) error {
		var err error
		switch key {
		case "schemaVersion":
			err = readSchemaVersion(d)
			version = true
		case "authorUrl":
			s.AuthorURL, err = d.string()
		case "taxonomies":
			err = d.object(func(name string) error {
				t := TaxonomyDef{Name: name}
				err := t.decode(d)
				s.Taxonomies = append(s.Taxonomies, t)
				return err
			})
		case "types":
			err = d.object(func(name string) error {
				t := TypeDef{Name: name}
				err := t.decode(d)
				s.Types = append(s.Types, t)
				return err
			})
		default:
			if slices.Contains(settingsKeys, key) {
				return d.errorf("unknown key; it belongs in the settings document")
			}
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
	return nil
}

func (t *TaxonomyDef) decode(d *decoder) error {
	return d.object(func(key string) error {
		var err error
		switch key {
		case "label":
			t.Label, err = d.string()
		case "url":
			t.URL, err = d.string()
		case "hierarchical":
			// Even false, which would read as omitted: the hierarchy of category and tag is fixed.
			if slices.ContainsFunc(builtinTaxonomies, func(b TaxonomyDef) bool { return b.Name == t.Name }) {
				return d.errorf("not allowed for built-in taxonomies")
			}
			t.Hierarchical, err = d.bool()
		default:
			return d.errorf("unknown key")
		}
		return err
	})
}

func (t *TypeDef) decode(d *decoder) error {
	return d.object(func(key string) error {
		var err error
		switch key {
		case "label":
			t.Label, err = d.string()
		case "url":
			t.URL, err = d.string()
		case "archive":
			t.Archive, err = d.string()
		case "taxonomies":
			t.Taxonomies, err = readStrings(d)
		case "body":
			// Even false, which would read as omitted: post and page always have a body.
			if t.Name == TypePost || t.Name == TypePage {
				return d.errorf("not allowed for %s", t.Name)
			}
			t.Body, err = d.bool()
		case "fields":
			t.Fields, err = readFieldDefs(d)
		default:
			return d.errorf("unknown key")
		}
		return err
	})
}

// readFieldDefs reads a mapping from the names of fields to their definitions.
func readFieldDefs(d *decoder) ([]FieldDef, error) {
	var fs []FieldDef
	err := d.object(func(name string) error {
		f := FieldDef{Name: name}
		err := f.decode(d)
		fs = append(fs, f)
		return err
	})
	return fs, err
}

// decode reads the definition of a field, whose name is its key in the mapping of fields: "name" is an unknown key.
func (f *FieldDef) decode(d *decoder) error {
	return d.object(func(key string) error {
		var err error
		switch key {
		case "type":
			var t string
			t, err = d.string()
			f.Type = FieldType(t)
		case "label":
			f.Label, err = d.string()
		case "required":
			f.Required, err = d.bool()
		case "translatable":
			var translatable bool
			translatable, err = d.bool()
			f.Synced = !translatable
		case "minLength":
			f.MinLength, err = d.int()
		case "maxLength":
			f.MaxLength, err = d.int()
		case "min":
			f.Min, err = readFloat(d)
		case "max":
			f.Max, err = readFloat(d)
		case "options":
			f.Options, err = readStrings(d)
		case "multiple":
			f.Multiple, err = d.bool()
		case "minItems":
			f.MinItems, err = d.int()
		case "maxItems":
			f.MaxItems, err = d.int()
		case "accept":
			f.Accept, err = readStrings(d)
		case "types":
			f.Types, err = readStrings(d)
		case "taxonomy":
			f.Taxonomy, err = d.string()
		case "fields":
			f.Fields, err = readFieldDefs(d)
		case "of":
			f.Of = new(FieldDef)
			err = f.Of.decode(d)
		default:
			return d.errorf("unknown key")
		}
		return err
	})
}

// MarshalSchema returns AuthorURL, Types and Taxonomies as the contents of a schema file, without comments.
// UnmarshalSchema reads them back as they are, except that an empty list reads as nil, if s passes Validate and has
// no character that YAML cannot have, such as U+FEFF. A definition with nothing to write, which UnmarshalSchema
// rejects, is an error.
func (s *Site) MarshalSchema() ([]byte, error) {
	var buf bytes.Buffer
	w := &schemaWriter{enc: jsontext.NewEncoder(&buf)}
	w.token(jsontext.BeginObject)
	w.name("schemaVersion")
	w.token(jsontext.Int(SchemaVersion))
	w.str("authorUrl", s.AuthorURL)
	if len(s.Taxonomies) > 0 {
		w.name("taxonomies")
		w.token(jsontext.BeginObject)
		for i := range s.Taxonomies {
			t := &s.Taxonomies[i]
			w.name(t.Name)
			w.object("the taxonomy "+strconv.Quote(t.Name), func() {
				w.str("label", t.Label)
				w.str("url", t.URL)
				w.flag("hierarchical", t.Hierarchical)
			})
		}
		w.token(jsontext.EndObject)
	}
	if len(s.Types) > 0 {
		w.name("types")
		w.token(jsontext.BeginObject)
		for i := range s.Types {
			t := &s.Types[i]
			what := "the type " + strconv.Quote(t.Name)
			w.name(t.Name)
			w.object(what, func() {
				w.str("label", t.Label)
				w.str("url", t.URL)
				w.str("archive", t.Archive)
				w.strs("taxonomies", t.Taxonomies)
				// UnmarshalSchema rejects body for post and page, which always have it.
				w.flag("body", t.Body && t.Name != TypePost && t.Name != TypePage)
				if len(t.Fields) > 0 {
					w.name("fields")
					w.fields(t.Fields, what)
				}
			})
		}
		w.token(jsontext.EndObject)
	}
	w.token(jsontext.EndObject)
	if w.err != nil {
		return nil, w.err
	}
	return yaml.FromJSON(buf.Bytes())
}

// schemaWriter writes the JSON of a schema file, and stops at the first error. Its methods for members write nothing
// for an empty string, false, 0, nil and an empty list, which are the same as omitting them.
type schemaWriter struct {
	enc *jsontext.Encoder
	err error
	n   int // the names written
}

func (w *schemaWriter) token(t jsontext.Token) {
	if w.err == nil {
		w.err = w.enc.WriteToken(t)
	}
}

func (w *schemaWriter) name(s string) {
	w.n++
	w.token(jsontext.String(s))
}

func (w *schemaWriter) str(name, v string) {
	if v != "" {
		w.name(name)
		w.token(jsontext.String(v))
	}
}

func (w *schemaWriter) flag(name string, v bool) {
	if v {
		w.name(name)
		w.token(jsontext.True)
	}
}

func (w *schemaWriter) count(name string, v int) {
	if v != 0 {
		w.name(name)
		w.token(jsontext.Int(int64(v)))
	}
}

func (w *schemaWriter) number(name string, v *float64) {
	if v != nil {
		w.name(name)
		if w.err == nil {
			w.err = w.enc.WriteValue(jsontext.Value(formatNumber(*v)))
		}
	}
}

func (w *schemaWriter) strs(name string, vs []string) {
	if len(vs) > 0 {
		w.name(name)
		w.token(jsontext.BeginArray)
		for _, v := range vs {
			w.token(jsontext.String(v))
		}
		w.token(jsontext.EndArray)
	}
}

// object writes the definition what as an object with the members that write adds, and fails if there are none.
func (w *schemaWriter) object(what string, write func()) {
	w.token(jsontext.BeginObject)
	n := w.n
	write()
	if w.n == n && w.err == nil {
		w.err = fmt.Errorf("%s has nothing to write; give it at least one item", what)
	}
	w.token(jsontext.EndObject)
}

// fields writes the definitions of fs, the fields of parent, which is described like what of object.
func (w *schemaWriter) fields(fs []FieldDef, parent string) {
	w.token(jsontext.BeginObject)
	for i := range fs {
		w.name(fs[i].Name)
		w.field(&fs[i], "the field "+strconv.Quote(fs[i].Name)+" of "+parent)
	}
	w.token(jsontext.EndObject)
}

// field writes the definition of f, which is described as what.
func (w *schemaWriter) field(f *FieldDef, what string) {
	w.object(what, func() {
		w.str("type", string(f.Type))
		w.str("label", f.Label)
		w.flag("required", f.Required)
		if f.Synced {
			w.name("translatable")
			w.token(jsontext.False)
		}
		w.count("minLength", f.MinLength)
		w.count("maxLength", f.MaxLength)
		w.number("min", f.Min)
		w.number("max", f.Max)
		w.strs("options", f.Options)
		w.flag("multiple", f.Multiple)
		w.count("minItems", f.MinItems)
		w.count("maxItems", f.MaxItems)
		w.strs("accept", f.Accept)
		w.strs("types", f.Types)
		w.str("taxonomy", f.Taxonomy)
		if len(f.Fields) > 0 {
			w.name("fields")
			w.fields(f.Fields, what)
		}
		if f.Of != nil {
			w.name("of")
			w.field(f.Of, `the "of" of `+what)
		}
	})
}

// readFloat reads a number as the values of number fields are read.
func readFloat(d *decoder) (*float64, error) {
	s, err := d.number()
	if err != nil {
		return nil, err
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil, d.errorf("number %s is out of range", s)
	}
	return &v, nil
}

func readStrings(d *decoder) ([]string, error) {
	var ss []string
	err := d.array(func(int) error {
		s, err := d.string()
		ss = append(ss, s)
		return err
	})
	return ss, err
}
