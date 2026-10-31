package content

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

type FieldType string

const (
	FieldText     FieldType = "text"
	FieldTextarea FieldType = "textarea"
	FieldRichText FieldType = "richText"
	FieldBlocks   FieldType = "blocks"
	FieldNumber   FieldType = "number"
	FieldBoolean  FieldType = "boolean"
	FieldDate     FieldType = "date"
	FieldDatetime FieldType = "datetime"
	FieldSelect   FieldType = "select"
	FieldMedia    FieldType = "media"
	FieldLink     FieldType = "link"
	FieldEntry    FieldType = "entry"
	FieldTerm     FieldType = "term"
	FieldGroup    FieldType = "group"
	FieldList     FieldType = "list"
)

// FieldDef defines a custom field. The comments name the types that use each option.
type FieldDef struct {
	Name      string
	Type      FieldType
	Label     string
	Required  bool
	Synced    bool     // translatable: false; the value is the same in every translation
	MinLength int      // text, textarea: in characters (code points)
	MaxLength int      // text, textarea
	Min, Max  *float64 // number
	Options   []string // select
	Multiple  bool     // select, entry, term
	MinItems  int      // list, and select, entry and term with Multiple
	MaxItems  int
	Accept    []string   // media: MediaImage, MediaPDF; any kind if empty
	Types     []string   // entry: the types of the entries; any type if empty
	Taxonomy  string     // term
	Fields    []FieldDef // group
	Of        *FieldDef  // list: each item, without a name
}

// mediaKinds are the kinds of media that accept can name.
var mediaKinds = []string{MediaImage, MediaPDF}

// fieldOptions lists the options that each type of field can use, by their names in the definition.
var fieldOptions = map[FieldType][]string{
	FieldText:     {"minLength", "maxLength"},
	FieldTextarea: {"minLength", "maxLength"},
	FieldRichText: nil,
	FieldBlocks:   nil,
	FieldNumber:   {"min", "max"},
	FieldBoolean:  nil,
	FieldDate:     nil,
	FieldDatetime: nil,
	FieldSelect:   {"options", "multiple", "minItems", "maxItems"},
	FieldMedia:    {"accept"},
	FieldLink:     nil,
	FieldEntry:    {"types", "multiple", "minItems", "maxItems"},
	FieldTerm:     {"taxonomy", "multiple", "minItems", "maxItems"},
	FieldGroup:    {"fields"},
	FieldList:     {"of", "minItems", "maxItems"},
}

// options returns the names of the options set in f, in the order of the fields of FieldDef.
func (f *FieldDef) options() []string {
	var ks []string
	add := func(set bool, key string) {
		if set {
			ks = append(ks, key)
		}
	}
	add(f.MinLength != 0, "minLength")
	add(f.MaxLength != 0, "maxLength")
	add(f.Min != nil, "min")
	add(f.Max != nil, "max")
	add(f.Options != nil, "options")
	add(f.Multiple, "multiple")
	add(f.MinItems != 0, "minItems")
	add(f.MaxItems != 0, "maxItems")
	add(f.Accept != nil, "accept")
	add(f.Types != nil, "types")
	add(f.Taxonomy != "", "taxonomy")
	add(f.Fields != nil, "fields")
	add(f.Of != nil, "of")
	return ks
}

// syncable reports whether the values of f can be the same in every translation: they refer to no entity of a language.
func (f *FieldDef) syncable() bool {
	switch f.Type {
	case FieldText, FieldTextarea, FieldNumber, FieldBoolean, FieldDate, FieldDatetime, FieldSelect, FieldMedia:
		return true
	case FieldGroup:
		return !slices.ContainsFunc(f.Fields, func(g FieldDef) bool { return !g.syncable() })
	case FieldList:
		return f.Of != nil && f.Of.syncable()
	case FieldRichText, FieldBlocks, FieldLink, FieldEntry, FieldTerm:
	}
	return false
}

// countable reports whether f has a number of items, which minItems and maxItems limit.
func (f *FieldDef) countable() bool {
	return f.Type == FieldList || f.Multiple && (f.Type == FieldSelect || f.Type == FieldEntry || f.Type == FieldTerm)
}

// checkFieldDefs checks fs, the fields of a type or a group at path, depth levels below the fields of a type.
func checkFieldDefs(c *checker, s *Site, path string, fs []FieldDef, depth int) {
	for i := range fs {
		f := &fs[i]
		p := path + "/" + escapePointer(f.Name)
		if err := checkName(f.Name); err != nil {
			c.add(p, err)
		} else if slices.ContainsFunc(fs[:i], func(g FieldDef) bool { return g.Name == f.Name }) {
			c.addf(p, "duplicate field %q", f.Name)
		}
		f.checkDef(c, s, p, depth, false)
	}
}

// checkDef checks f at path, depth levels below the fields of a type; item reports whether f is the items of a list.
func (f *FieldDef) checkDef(c *checker, s *Site, path string, depth int, item bool) {
	if item && f.Name != "" {
		c.addf(path+"/name", "not allowed for the items of a list")
	}
	allowed, ok := fieldOptions[f.Type]
	switch {
	case f.Type == "":
		c.addf(path+"/type", "missing type")
	case !ok:
		c.addf(path+"/type", "unknown field type %q", f.Type)
	}
	if f.Label != "" {
		c.add(path+"/label", checkLine(f.Label))
	}
	switch {
	case !f.Required:
	case item:
		c.addf(path+"/required", "not allowed for the items of a list")
	case f.Type == FieldBoolean:
		c.addf(path+"/required", "not allowed for boolean fields")
	}
	switch {
	case !f.Synced || !ok:
	case depth > 0 || item:
		c.addf(path+"/translatable", "only the fields of a type can be untranslatable")
	case !f.syncable():
		c.addf(path+"/translatable", "%s fields cannot be untranslatable", f.Type)
	}
	if !ok {
		return
	}
	for _, k := range f.options() {
		if !slices.Contains(allowed, k) {
			c.addf(path+"/"+k, "not allowed for %s fields", f.Type)
		}
	}
	switch f.Type {
	case FieldText, FieldTextarea:
		checkLimits(c, path, "minLength", f.MinLength, "maxLength", f.MaxLength)
	case FieldNumber:
		for _, b := range []struct {
			key string
			v   *float64
		}{{"min", f.Min}, {"max", f.Max}} {
			if b.v != nil && (math.IsNaN(*b.v) || math.IsInf(*b.v, 0)) {
				c.addf(path+"/"+b.key, "%s must be a finite number", b.key)
			}
		}
		if f.Min != nil && f.Max != nil && *f.Min > *f.Max {
			c.addf(path+"/min", "min %s is greater than max %s", formatNumber(*f.Min), formatNumber(*f.Max))
		}
	case FieldSelect:
		if len(f.Options) == 0 {
			c.addf(path+"/options", "missing options")
		}
		for i, o := range f.Options {
			p := path + "/options/" + strconv.Itoa(i)
			switch {
			case o == "":
				c.addf(p, "empty option")
			case slices.Contains(f.Options[:i], o):
				c.addf(p, "duplicate option %q", o)
			default:
				c.add(p, checkLine(o))
			}
		}
	case FieldMedia:
		for i, a := range f.Accept {
			p := path + "/accept/" + strconv.Itoa(i)
			if err := checkChoice("media kind", a, mediaKinds...); err != nil {
				c.add(p, err)
			} else if slices.Contains(f.Accept[:i], a) {
				c.addf(p, "duplicate media kind %q", a)
			}
		}
	case FieldEntry:
		for i, t := range f.Types {
			p := path + "/types/" + strconv.Itoa(i)
			if s.Type(t) == nil {
				c.addf(p, "unknown type %q", t)
			} else if slices.Contains(f.Types[:i], t) {
				c.addf(p, "duplicate type %q", t)
			}
		}
	case FieldTerm:
		if f.Taxonomy == "" {
			c.addf(path+"/taxonomy", "missing taxonomy")
		} else if !s.hasTaxonomy(f.Taxonomy) {
			c.addf(path+"/taxonomy", "unknown taxonomy %q", f.Taxonomy)
		}
	case FieldGroup:
		if len(f.Fields) == 0 {
			c.addf(path+"/fields", "missing fields")
		}
		checkFieldDefs(c, s, path+"/fields", f.Fields, depth+1)
	case FieldList:
		if f.Of == nil {
			c.addf(path+"/of", "missing of")
		} else {
			f.Of.checkDef(c, s, path+"/of", depth+1, true)
		}
	case FieldRichText, FieldBlocks, FieldBoolean, FieldDate, FieldDatetime, FieldLink:
	}
	if f.countable() {
		checkLimits(c, path, "minItems", f.MinItems, "maxItems", f.MaxItems)
	} else if slices.Contains(allowed, "multiple") {
		if f.MinItems != 0 {
			c.addf(path+"/minItems", "minItems needs multiple")
		}
		if f.MaxItems != 0 {
			c.addf(path+"/maxItems", "maxItems needs multiple")
		}
	}
}

// checkLimits checks a pair of limits such as minLength and maxLength, where 0 is no limit.
func checkLimits(c *checker, path, minKey string, minV int, maxKey string, maxV int) {
	if minV < 0 {
		c.addf(path+"/"+minKey, "%s %d is negative", minKey, minV)
	}
	if maxV < 0 {
		c.addf(path+"/"+maxKey, "%s %d is negative", maxKey, maxV)
	}
	if maxV > 0 && minV > maxV {
		c.addf(path+"/"+minKey, "%s %d is greater than %s %d", minKey, minV, maxKey, maxV)
	}
}

// field returns the definition of the field name in fs, or nil if there is none.
func field(fs []FieldDef, name string) *FieldDef {
	if i := slices.IndexFunc(fs, func(f FieldDef) bool { return f.Name == name }); i >= 0 {
		return &fs[i]
	}
	return nil
}

// isEmptyValue reports whether v is an empty string, object or array, which is written by omitting the value.
func isEmptyValue(v jsontext.Value) bool {
	switch string(v) {
	case `""`, "{}", "[]":
		return true
	}
	return false
}

// member is a member of a JSON object, or an element of an array with an empty name.
type member struct {
	name  string
	value jsontext.Value
	path  string
}

// members reads v, a non-empty object at path, as written.
func members(v jsontext.Value, path string) ([]member, error) {
	var ms []member
	err := unmarshal(v, path, func(d *decoder) error {
		return d.object(func(name string) error {
			raw, p, err := d.raw()
			ms = append(ms, member{name, raw, p})
			return err
		})
	})
	return ms, err
}

// items reads v, a non-empty array at path, as written.
func items(v jsontext.Value, path string) ([]member, error) {
	var ms []member
	err := unmarshal(v, path, func(d *decoder) error {
		return d.array(func(int) error {
			raw, p, err := d.raw()
			ms = append(ms, member{"", raw, p})
			return err
		})
	})
	return ms, err
}

// decode reads v at path with read and adds the error, if any. It reports whether v was read.
func (c *checker) decode(v jsontext.Value, path string, read func(*decoder) error) bool {
	err := unmarshal(v, path, read)
	c.addError(err)
	return err == nil
}

// checkFields checks vs, the values of the fields fs at path. Unknown names are reported in sorted order after the others.
func checkFields(c *checker, path string, fs []FieldDef, vs map[string]jsontext.Value) {
	for i := range fs {
		f := &fs[i]
		p := path + "/" + escapePointer(f.Name)
		if v, ok := vs[f.Name]; ok {
			f.check(c, p, v)
		} else if f.Required && c.doc.publish {
			c.addf(p, "missing %s", f.Name)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(vs)) {
		if field(fs, name) == nil {
			c.addf(path+"/"+escapePointer(name), "unknown field %q", name)
		}
	}
}

// check checks v, a value of f at path.
func (f *FieldDef) check(c *checker, path string, v jsontext.Value) {
	if isEmptyValue(v) {
		c.addf(path, "empty value; omit it")
		return
	}
	if f.countable() {
		f.checkItems(c, path, v)
		return
	}
	var s string
	readString := func(d *decoder) error {
		var err error
		s, err = d.string()
		return err
	}
	switch f.Type {
	case FieldText, FieldTextarea:
		if !c.decode(v, path, readString) {
			return
		}
		if f.Type == FieldText {
			c.add(path, checkLine(s))
		} else {
			c.add(path, checkText(s, true))
		}
		n := utf8.RuneCountInString(s)
		if f.MaxLength > 0 && n > f.MaxLength {
			c.addf(path, "too long: %d characters (max %d)", n, f.MaxLength)
		}
		if c.doc.publish && n < f.MinLength {
			c.addf(path, "too short: %d characters (min %d)", n, f.MinLength)
		}
	case FieldRichText:
		var rt RichText
		if c.decode(v, path, rt.decode) {
			rt.check(c, path)
		}
	case FieldBlocks:
		var bs Blocks
		read := func(d *decoder) error {
			var err error
			bs, err = (&blockReader{}).list(d, 1, true)
			return err
		}
		if c.decode(v, path, read) {
			bs.check(c, path)
		}
	case FieldNumber:
		var num string
		read := func(d *decoder) error {
			var err error
			num, err = d.number()
			return err
		}
		if !c.decode(v, path, read) {
			return
		}
		n, err := strconv.ParseFloat(num, 64)
		switch {
		case err != nil:
			c.addf(path, "number %s is out of range", num)
		case f.Min != nil && n < *f.Min:
			c.addf(path, "%s is less than the minimum %s", num, formatNumber(*f.Min))
		case f.Max != nil && n > *f.Max:
			c.addf(path, "%s is greater than the maximum %s", num, formatNumber(*f.Max))
		}
	case FieldBoolean:
		c.decode(v, path, func(d *decoder) error {
			_, err := d.bool()
			return err
		})
	case FieldDate:
		if c.decode(v, path, readString) {
			c.add(path, checkDate(s))
		}
	case FieldDatetime:
		if c.decode(v, path, readString) {
			c.add(path, checkDatetime(s))
		}
	case FieldSelect:
		if c.decode(v, path, readString) && !slices.Contains(f.Options, s) {
			c.addf(path, "%q is not an option", s)
		}
	case FieldMedia:
		if c.decode(v, path, readString) {
			c.media(path, s, f.Accept...)
		}
	case FieldLink:
		var l Link
		if c.decode(v, path, l.decode) {
			l.check(c, path)
		}
	case FieldEntry:
		if !c.decode(v, path, readString) {
			return
		}
		if r, ok := c.ref(path, RefEntry, s); ok && len(f.Types) > 0 && !slices.Contains(f.Types, r.Type) {
			c.addf(path, "entry %q is of the type %q (use %s)", s, r.Type, strings.Join(f.Types, ", "))
		}
	case FieldTerm:
		if c.decode(v, path, readString) {
			c.term(path, s, f.Taxonomy)
		}
	case FieldGroup:
		ms, err := members(v, path)
		if err != nil {
			c.addError(err)
			return
		}
		vs := make(map[string]jsontext.Value, len(ms))
		for _, m := range ms {
			vs[m.name] = m.value
		}
		checkFields(c, path, f.Fields, vs)
	case FieldList:
		// Countable, as checked above.
	}
}

// checkItems checks v, the items of f at path, where f is countable.
func (f *FieldDef) checkItems(c *checker, path string, v jsontext.Value) {
	ms, err := items(v, path)
	if err != nil {
		c.addError(err)
		return
	}
	n := len(ms)
	if f.MaxItems > 0 && n > f.MaxItems {
		c.addf(path, "too many items: %d (max %d)", n, f.MaxItems)
	}
	if c.doc.publish && n < f.MinItems {
		c.addf(path, "too few items: %d (min %d)", n, f.MinItems)
	}
	item := f.item()
	if item == nil {
		return
	}
	seen := make(map[string]bool)
	for _, m := range ms {
		item.check(c, m.path, m.value)
		if f.Type == FieldList {
			continue
		}
		// The items of select, entry and term are strings, which must differ.
		var s string
		if unmarshal(m.value, "", func(d *decoder) (err error) { s, err = d.string(); return err }) == nil {
			if seen[s] {
				c.addf(m.path, "duplicate item %q", s)
			}
			seen[s] = true
		}
	}
}

// item returns the definition of each item of f, where f is countable, or nil if a list has no Of.
func (f *FieldDef) item() *FieldDef {
	if f.Type == FieldList {
		return f.Of
	}
	one := *f
	one.Multiple = false
	return &one
}

// term checks id, which refers to a term of the taxonomy in the language of the entry.
func (c *checker) term(path, id, taxonomy string) {
	r, ok := c.ref(path, RefTerm, id)
	if !ok {
		return
	}
	if r.Type != taxonomy {
		c.addf(path, "term %q is in the taxonomy %q, not %q", id, r.Type, taxonomy)
	}
	if r.Language != c.doc.language {
		c.addf(path, "term %q is in the language %q, not %q", id, r.Language, c.doc.language)
	}
}

// normalize returns v, a value of f, cleaned up as Revision.Normalize does, or nil if nothing is left of it.
// It returns v as it is if v is not a value of f.
func (f *FieldDef) normalize(v jsontext.Value) jsontext.Value {
	if f.countable() {
		ms, err := items(v, "")
		item := f.item()
		if err != nil || item == nil {
			return v
		}
		var out []jsontext.Value
		for _, m := range ms {
			if n := item.normalize(m.value); n != nil {
				out = append(out, n)
			}
		}
		if len(out) == 0 {
			return nil
		}
		return marshalValue(out)
	}
	var s string
	readString := func(d *decoder) error {
		var err error
		s, err = d.string()
		return err
	}
	switch f.Type {
	case FieldText, FieldSelect:
		if unmarshal(v, "", readString) != nil {
			return v
		}
		return marshalString(normalizeText(s, false))
	case FieldTextarea:
		if unmarshal(v, "", readString) != nil {
			return v
		}
		return marshalString(normalizeText(s, true))
	case FieldRichText:
		var rt RichText
		if unmarshal(v, "", rt.decode) != nil {
			return v
		}
		rt.Normalize()
		if len(rt) == 0 {
			return nil
		}
		return marshalValue(rt)
	case FieldBlocks:
		var bs Blocks
		if unmarshal(v, "", bs.decode) != nil {
			return v
		}
		if len(bs) == 0 {
			return nil
		}
		bs.Normalize()
		return marshalValue(bs)
	case FieldBoolean:
		// false is the same as no value.
		if string(v) == "false" {
			return nil
		}
	case FieldLink:
		var l Link
		if unmarshal(v, "", l.decode) != nil {
			return v
		}
		if l.Href != "" {
			l.Href = normalizeHref(l.Href)
		}
		return marshalValue(l)
	case FieldGroup:
		ms, err := members(v, "")
		if err != nil {
			return v
		}
		vs := make(Fields, len(ms))
		for _, m := range ms {
			vs[m.name] = m.value
		}
		vs.normalize(f.Fields)
		if len(vs) == 0 {
			return nil
		}
		return marshalValue(vs)
	case FieldNumber, FieldDate, FieldDatetime, FieldMedia, FieldEntry, FieldTerm, FieldList:
	}
	return v
}

// blocks returns the lists of blocks in v, a value of f, in the order they are written.
func (f *FieldDef) blocks(v jsontext.Value) []Blocks {
	switch {
	case f.Type == FieldBlocks:
		var bs Blocks
		if unmarshal(v, "", bs.decode) == nil {
			return []Blocks{bs}
		}
	case f.Type == FieldGroup:
		ms, err := members(v, "")
		if err != nil {
			return nil
		}
		var out []Blocks
		for _, m := range ms {
			if g := field(f.Fields, m.name); g != nil {
				out = append(out, g.blocks(m.value)...)
			}
		}
		return out
	case f.Type == FieldList && f.Of != nil:
		ms, err := items(v, "")
		if err != nil {
			return nil
		}
		var out []Blocks
		for _, m := range ms {
			out = append(out, f.Of.blocks(m.value)...)
		}
		return out
	}
	return nil
}

// richTexts returns the rich texts in v, a value of f, in the order they are written.
func (f *FieldDef) richTexts(v jsontext.Value) []RichText {
	switch {
	case f.Type == FieldRichText:
		var rt RichText
		if unmarshal(v, "", rt.decode) == nil {
			return []RichText{rt}
		}
	case f.Type == FieldGroup:
		ms, err := members(v, "")
		if err != nil {
			return nil
		}
		var out []RichText
		for _, m := range ms {
			if g := field(f.Fields, m.name); g != nil {
				out = append(out, g.richTexts(m.value)...)
			}
		}
		return out
	case f.Type == FieldList && f.Of != nil:
		ms, err := items(v, "")
		if err != nil {
			return nil
		}
		var out []RichText
		for _, m := range ms {
			out = append(out, f.Of.richTexts(m.value)...)
		}
		return out
	}
	return nil
}

// formatNumber writes v without an exponent, such as 1000000 rather than 1e+06.
func formatNumber(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

func marshalString(s string) jsontext.Value {
	if s == "" {
		return nil
	}
	return marshalValue(s)
}

// marshalValue returns v as JSON, with the keys of maps in sorted order.
func marshalValue(v any) jsontext.Value {
	data, err := json.Marshal(v, json.Deterministic(true))
	if err != nil {
		// The values here are of this package's types, which always marshal.
		panic(err)
	}
	return data
}
