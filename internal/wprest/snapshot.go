package wprest

import (
	"bytes"
	"cmp"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"
)

// ReadSnapshot reads a snapshot from r, as Snapshot.WriteJSON writes it. The objects of WordPress in it may have other
// keys, as the responses of the REST API do, and they are ignored; the top level may not. The slices of the result are
// in the order that Snapshot says, whatever the order in the file is.
func ReadSnapshot(r io.Reader, opts Options) (*Snapshot, error) {
	opts = opts.withDefaults()
	data, err := io.ReadAll(io.LimitReader(r, opts.MaxSnapshotBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading the snapshot: %w", err)
	}
	if int64(len(data)) > opts.MaxSnapshotBytes {
		return nil, &Error{Msg: fmt.Sprintf("too large: at least %d bytes (max %d)", len(data), opts.MaxSnapshotBytes)}
	}
	var s Snapshot
	hasIndex := false
	err = decodeBody(data, func(d *decoder) error {
		return d.object(func(key string) error {
			var err error
			switch key {
			case "api":
				s.API, err = d.str()
			case "index":
				var null bool
				if null, err = d.null(); err == nil && !null {
					s.Index, err = d.index(nil)
					hasIndex = err == nil
				}
			case "taxonomies":
				s.Taxonomies, err = readList(d, "taxonomies", opts.MaxTaxonomies, func(ptr string) (Taxonomy, error) { return d.taxonomy(ptr, "") })
			case "types":
				s.Types, err = readList(d, "types", opts.MaxTaxonomies, func(ptr string) (Type, error) { return d.postType(ptr, "") })
			case "terms":
				s.Terms, err = readList(d, "terms", opts.MaxTerms, func(ptr string) (Term, error) { return d.term(ptr, "") })
			case "users":
				s.Users, err = readList(d, "users", opts.MaxUsers, d.user)
			case "unavailable":
				s.Unavailable, err = readList(d, "unavailable", math.MaxInt, func(string) (Unavailable, error) { return d.unavailable() })
			default:
				err = d.errorf("unknown key")
			}
			return err
		})
	})
	if err == nil && !hasIndex {
		err = &Error{Msg: `missing key "index"`}
	}
	if err == nil {
		err = s.check(opts)
	}
	if err != nil {
		return nil, err
	}
	s.sort()
	return &s, nil
}

// WriteJSON writes the snapshot as JSON with the slices in their order, and a line break at the end. If it fails, part of
// the file may have been written.
func (s *Snapshot) WriteJSON(w io.Writer) error {
	c := *s
	c.Taxonomies, c.Types, c.Terms = slices.Clone(s.Taxonomies), slices.Clone(s.Types), slices.Clone(s.Terms)
	c.Users, c.Unavailable = slices.Clone(s.Users), slices.Clone(s.Unavailable)
	c.sort()
	if err := json.MarshalWrite(w, &c, jsontext.WithIndent("  ")); err != nil {
		return err
	}
	_, err := w.Write([]byte{'\n'})
	return err
}

func (s *Snapshot) sort() {
	slices.SortFunc(s.Taxonomies, func(a, b Taxonomy) int { return cmp.Compare(a.Slug, b.Slug) })
	slices.SortFunc(s.Types, func(a, b Type) int { return cmp.Compare(a.Slug, b.Slug) })
	slices.SortFunc(s.Terms, func(a, b Term) int {
		return cmp.Or(cmp.Compare(a.Taxonomy, b.Taxonomy), cmp.Compare(a.ID, b.ID))
	})
	slices.SortFunc(s.Users, func(a, b User) int { return cmp.Compare(a.ID, b.ID) })
	slices.SortFunc(s.Unavailable, func(a, b Unavailable) int {
		return cmp.Or(cmp.Compare(a.Route, b.Route), cmp.Compare(a.Taxonomy, b.Taxonomy),
			cmp.Compare(a.Status, b.Status), cmp.Compare(a.Code, b.Code))
	})
}

func tooMany(noun string, n, limit int) string {
	return fmt.Sprintf("too many %s: at least %d (max %d)", noun, n, limit)
}

// check finds what the decoding of one object cannot: the numbers over the limits, and the objects that are twice in
// the snapshot. It is called before the slices are sorted, so that the pointers are the places in the file.
func (s *Snapshot) check(o Options) error {
	over := func(noun string, n, limit int) error {
		if n <= limit {
			return nil
		}
		return &Error{Pointer: fmt.Sprintf("/%s/%d", noun, limit), Msg: tooMany(noun, limit+1, limit)}
	}
	for _, err := range []error{over("taxonomies", len(s.Taxonomies), o.MaxTaxonomies), over("types", len(s.Types), o.MaxTaxonomies),
		over("terms", len(s.Terms), o.MaxTerms), over("users", len(s.Users), o.MaxUsers)} {
		if err != nil {
			return err
		}
	}
	taxonomies := make(map[string]bool, len(s.Taxonomies))
	for i, t := range s.Taxonomies {
		if taxonomies[t.Slug] {
			return &Error{Pointer: fmt.Sprintf("/taxonomies/%d", i), Msg: fmt.Sprintf("duplicate taxonomy %q", t.Slug)}
		}
		taxonomies[t.Slug] = true
	}
	types := make(map[string]bool, len(s.Types))
	for i, t := range s.Types {
		if types[t.Slug] {
			return &Error{Pointer: fmt.Sprintf("/types/%d", i), Msg: fmt.Sprintf("duplicate type %q", t.Slug)}
		}
		types[t.Slug] = true
	}
	type termKey struct {
		taxonomy string
		id       int64
	}
	terms := make(map[termKey]bool, len(s.Terms))
	for i, t := range s.Terms {
		k := termKey{t.Taxonomy, t.ID}
		if terms[k] {
			return &Error{Pointer: fmt.Sprintf("/terms/%d", i), Msg: fmt.Sprintf("duplicate term %d of %s", t.ID, t.Taxonomy)}
		}
		terms[k] = true
	}
	users := make(map[int64]bool, len(s.Users))
	for i, u := range s.Users {
		if users[u.ID] {
			return &Error{Pointer: fmt.Sprintf("/users/%d", i), Msg: fmt.Sprintf("duplicate user %d", u.ID)}
		}
		users[u.ID] = true
	}
	return nil
}

type decoder struct {
	dec *jsontext.Decoder
}

func decodeBody(data []byte, read func(*decoder) error) error {
	// Not bytes.NewReader: jsontext then garbles member names in the pointers of errors.
	d := &decoder{dec: jsontext.NewDecoder(bytes.NewBuffer(data))}
	if err := read(d); err != nil {
		return err
	}
	// The decoder accepts a stream of values, so reject anything after the first.
	if _, err := d.dec.ReadToken(); !errors.Is(err, io.EOF) {
		return &Error{Msg: "unexpected data after value"}
	}
	return nil
}

func (d *decoder) pointer() string {
	return string(d.dec.StackPointer())
}

func (d *decoder) errorf(format string, args ...any) error {
	return &Error{Pointer: d.pointer(), Msg: fmt.Sprintf(format, args...)}
}

// fail converts an error from d.dec into an *Error.
func (d *decoder) fail(err error) error {
	// SyntacticError first: its JSONPointer is more precise than StackPointer, e.g. for truncated input.
	var se *jsontext.SyntacticError
	if errors.As(err, &se) {
		return &Error{Pointer: string(se.JSONPointer), Msg: syntaxMsg(err)}
	}
	return &Error{Pointer: d.pointer(), Msg: syntaxMsg(err)}
}

func syntaxMsg(err error) string {
	var se *jsontext.SyntacticError
	if errors.As(err, &se) {
		err = se.Err
	}
	switch {
	case errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF):
		return "unexpected end of JSON input"
	case errors.Is(err, jsontext.ErrDuplicateName):
		return "duplicate key"
	}
	return err.Error()
}

func kindName(k jsontext.Kind) string {
	switch k {
	case jsontext.KindNull:
		return "null"
	case jsontext.KindFalse, jsontext.KindTrue:
		return "boolean"
	case jsontext.KindString:
		return "string"
	case jsontext.KindNumber:
		return "number"
	case jsontext.KindBeginObject:
		return "object"
	case jsontext.KindBeginArray:
		return "array"
	case jsontext.KindInvalid, jsontext.KindEndObject, jsontext.KindEndArray:
	}
	return k.String()
}

func (d *decoder) next(want string) (jsontext.Token, error) {
	tok, err := d.dec.ReadToken()
	if err != nil {
		return jsontext.Token{}, d.fail(err)
	}
	if got := kindName(tok.Kind()); got != want {
		return jsontext.Token{}, d.errorf("expected %s, got %s", want, got)
	}
	return tok, nil
}

// null reads the next value if it is null. WordPress writes null where it has no value, so null is the same as a key
// that is not there.
func (d *decoder) null() (bool, error) {
	if d.dec.PeekKind() != jsontext.KindNull {
		return false, nil
	}
	if _, err := d.dec.ReadToken(); err != nil {
		return false, d.fail(err)
	}
	return true, nil
}

// object reads an object, which may be empty. member is called with each key, after it is read, and must read its value.
func (d *decoder) object(member func(key string) error) error {
	if _, err := d.next("object"); err != nil {
		return err
	}
	for d.dec.PeekKind() != jsontext.KindEndObject {
		name, err := d.dec.ReadToken()
		if err != nil {
			return d.fail(err)
		}
		if err := member(name.String()); err != nil {
			return err
		}
	}
	if _, err := d.dec.ReadToken(); err != nil {
		return d.fail(err)
	}
	return nil
}

// keyed reads what PHP makes of an array that is keyed by name: an object, or [] if there is no member.
func (d *decoder) keyed(member func(key string) error) error {
	if d.dec.PeekKind() != jsontext.KindBeginArray {
		return d.object(member)
	}
	if _, err := d.dec.ReadToken(); err != nil {
		return d.fail(err)
	}
	if d.dec.PeekKind() != jsontext.KindEndArray {
		return d.errorf("expected object, got array")
	}
	if _, err := d.dec.ReadToken(); err != nil {
		return d.fail(err)
	}
	return nil
}

// items reads an array, which may be empty. elem is called with the pointer of each element, and must read it.
func (d *decoder) items(elem func(ptr string) error) error {
	if _, err := d.next("array"); err != nil {
		return err
	}
	array := d.pointer()
	for i := 0; d.dec.PeekKind() != jsontext.KindEndArray; i++ {
		if err := elem(array + "/" + strconv.Itoa(i)); err != nil {
			return err
		}
	}
	if _, err := d.dec.ReadToken(); err != nil {
		return d.fail(err)
	}
	return nil
}

// readList reads null or an array of at most limit elements, which elem reads.
func readList[T any](d *decoder, noun string, limit int, elem func(ptr string) (T, error)) ([]T, error) {
	if null, err := d.null(); null || err != nil {
		return nil, err
	}
	var list []T
	err := d.items(func(ptr string) error {
		if len(list) >= limit {
			return &Error{Pointer: ptr, Msg: tooMany(noun, limit+1, limit)}
		}
		v, err := elem(ptr)
		if err != nil {
			return err
		}
		list = append(list, v)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return list, nil
}

func (d *decoder) skip() error {
	if err := d.dec.SkipValue(); err != nil {
		return d.fail(err)
	}
	return nil
}

func (d *decoder) str() (string, error) {
	if null, err := d.null(); null || err != nil {
		return "", err
	}
	tok, err := d.next("string")
	if err != nil {
		return "", err
	}
	return tok.String(), nil
}

func (d *decoder) boolean() (bool, error) {
	if null, err := d.null(); null || err != nil {
		return false, err
	}
	tok, err := d.next("boolean")
	if err != nil {
		return false, err
	}
	return tok.Bool(), nil
}

// integer reads an integer, which is a number written without a fraction or an exponent; null is 0.
func (d *decoder) integer() (int64, error) {
	if null, err := d.null(); null || err != nil {
		return 0, err
	}
	tok, err := d.next("number")
	if err != nil {
		return 0, err
	}
	v, err := strconv.ParseInt(tok.String(), 10, 64)
	if err != nil {
		return 0, d.errorf("must be an integer")
	}
	return v, nil
}

// intFrom reads an integer of the key name that is at least min; null is 0, which the caller sees as a key that is not
// there.
func (d *decoder) intFrom(name string, min int64) (int64, error) {
	if null, err := d.null(); null || err != nil {
		return 0, err
	}
	v, err := d.integer()
	if err == nil && v < min {
		err = d.errorf("invalid %s %d: use an integer from %d", name, v, min)
	}
	return v, err
}

// optionID reads an ID that WordPress takes from an option or a theme mod of the site, which can be a string of digits
// or false instead of an integer.
func (d *decoder) optionID(name string) (int64, error) {
	kind := d.dec.PeekKind()
	if kind == jsontext.KindFalse {
		_, err := d.next("boolean")
		return 0, err
	}
	if kind != jsontext.KindString {
		return d.intFrom(name, 0)
	}
	tok, err := d.next("string")
	if err != nil {
		return 0, err
	}
	s := tok.String()
	if s == "" {
		return 0, nil
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil || v < 0 || strings.Trim(s, "0123456789") != "" {
		return 0, d.errorf("invalid %s %q: use an integer from 0", name, s)
	}
	return v, nil
}

// numberOrString reads a number as it is written, or a string; null is "".
func (d *decoder) numberOrString() (string, error) {
	if null, err := d.null(); null || err != nil {
		return "", err
	}
	tok, err := d.dec.ReadToken()
	if err != nil {
		return "", d.fail(err)
	}
	if k := tok.Kind(); k != jsontext.KindString && k != jsontext.KindNumber {
		return "", d.errorf("expected number or string, got %s", kindName(k))
	}
	return tok.String(), nil
}

// stringList reads an array of strings; null is none.
func (d *decoder) stringList() ([]string, error) {
	return readList(d, "", math.MaxInt, func(string) (string, error) {
		tok, err := d.next("string")
		return tok.String(), err
	})
}

func (d *decoder) archive() (Archive, error) {
	if null, err := d.null(); null || err != nil {
		return Archive{}, err
	}
	tok, err := d.dec.ReadToken()
	if err != nil {
		return Archive{}, d.fail(err)
	}
	switch k := tok.Kind(); k {
	case jsontext.KindString:
		return Archive{Set: true, On: tok.String() != "", Slug: tok.String()}, nil
	case jsontext.KindTrue, jsontext.KindFalse:
		return Archive{Set: true, On: tok.Bool()}, nil
	case jsontext.KindInvalid, jsontext.KindNull, jsontext.KindNumber, jsontext.KindBeginObject, jsontext.KindEndObject,
		jsontext.KindBeginArray, jsontext.KindEndArray:
	}
	return Archive{}, d.errorf("expected boolean or string, got %s", kindName(tok.Kind()))
}

// keyOrMissing returns what a slug or a taxonomy of an object at ptr is: the value, or key if the object does not have
// one, as in the responses of the REST API that are keyed by the slug. It is an error if neither is there, or if both
// are and differ.
func keyOrMissing(ptr, name, value, key string) (string, error) {
	switch {
	case value == "" && key == "":
		return "", &Error{Pointer: ptr, Msg: fmt.Sprintf("missing key %q", name)}
	case value == "":
		return key, nil
	case key != "" && value != key:
		return "", &Error{Pointer: ptr + "/" + name, Msg: fmt.Sprintf("%s %q does not match %q", name, value, key)}
	}
	return value, nil
}

// index reads the index of the REST API. If namespaces is not nil, it is set to whether the object has the array
// namespaces, which the index of a REST API has.
func (d *decoder) index(namespaces *bool) (Index, error) {
	var ix Index
	err := d.object(func(key string) error {
		var err error
		switch key {
		case "name":
			ix.Name, err = d.str()
		case "description":
			ix.Description, err = d.str()
		case "url":
			ix.URL, err = d.str()
		case "home":
			ix.Home, err = d.str()
		case "gmt_offset":
			ix.GMTOffset, err = d.numberOrString()
		case "timezone_string":
			ix.TimezoneString, err = d.str()
		case "show_on_front":
			ix.ShowOnFront, err = d.str()
		case "page_on_front":
			ix.PageOnFront, err = d.optionID(key)
		case "page_for_posts":
			ix.PageForPosts, err = d.optionID(key)
		case "site_logo":
			ix.SiteLogo, err = d.optionID(key)
		case "site_icon":
			ix.SiteIcon, err = d.optionID(key)
		case "namespaces":
			if namespaces != nil {
				*namespaces = d.dec.PeekKind() == jsontext.KindBeginArray
			}
			err = d.skip()
		default:
			err = d.skip()
		}
		return err
	})
	return ix, err
}

// taxonomy reads the taxonomy at ptr. If key is not empty, the slug is optional, and it must be key if it is there.
func (d *decoder) taxonomy(ptr, key string) (Taxonomy, error) {
	var t Taxonomy
	err := d.object(func(k string) error {
		var err error
		switch k {
		case "slug":
			t.Slug, err = d.str()
		case "name":
			t.Name, err = d.str()
		case "hierarchical":
			t.Hierarchical, err = d.boolean()
		case "rest_base":
			t.RestBase, err = d.str()
		case "rest_namespace":
			t.RestNamespace, err = d.str()
		case "types":
			t.Types, err = d.stringList()
		default:
			err = d.skip()
		}
		return err
	})
	if err != nil {
		return Taxonomy{}, err
	}
	t.Slug, err = keyOrMissing(ptr, "slug", t.Slug, key)
	return t, err
}

// postType reads the post type at ptr, with the slug as taxonomy does.
func (d *decoder) postType(ptr, key string) (Type, error) {
	var t Type
	err := d.object(func(k string) error {
		var err error
		switch k {
		case "slug":
			t.Slug, err = d.str()
		case "name":
			t.Name, err = d.str()
		case "hierarchical":
			t.Hierarchical, err = d.boolean()
		case "has_archive":
			t.HasArchive, err = d.archive()
		case "rest_base":
			t.RestBase, err = d.str()
		case "rest_namespace":
			t.RestNamespace, err = d.str()
		case "taxonomies":
			t.Taxonomies, err = d.stringList()
		default:
			err = d.skip()
		}
		return err
	})
	if err != nil {
		return Type{}, err
	}
	t.Slug, err = keyOrMissing(ptr, "slug", t.Slug, key)
	return t, err
}

// term reads the term at ptr. If taxonomy is not empty, the term does not need to have one, and it must be that one if
// it does, as the terms that are read from the route of a taxonomy.
func (d *decoder) term(ptr, taxonomy string) (Term, error) {
	var t Term
	err := d.object(func(key string) error {
		var err error
		switch key {
		case "id":
			t.ID, err = d.intFrom(key, 1)
		case "taxonomy":
			t.Taxonomy, err = d.str()
		case "slug":
			t.Slug, err = d.str()
		case "name":
			t.Name, err = d.str()
		case "parent":
			t.Parent, err = d.intFrom(key, 0)
		case "link":
			t.Link, err = d.str()
		default:
			err = d.skip()
		}
		return err
	})
	if err != nil {
		return Term{}, err
	}
	if t.ID == 0 {
		return Term{}, &Error{Pointer: ptr, Msg: `missing key "id"`}
	}
	t.Taxonomy, err = keyOrMissing(ptr, "taxonomy", t.Taxonomy, taxonomy)
	return t, err
}

func (d *decoder) user(ptr string) (User, error) {
	var u User
	err := d.object(func(key string) error {
		var err error
		switch key {
		case "id":
			u.ID, err = d.intFrom(key, 1)
		case "name":
			u.Name, err = d.str()
		case "slug":
			u.Slug, err = d.str()
		case "description":
			u.Description, err = d.str()
		case "url":
			u.URL, err = d.str()
		case "link":
			u.Link, err = d.str()
		default:
			err = d.skip()
		}
		return err
	})
	if err != nil {
		return User{}, err
	}
	if u.ID == 0 {
		return User{}, &Error{Pointer: ptr, Msg: `missing key "id"`}
	}
	return u, nil
}

func (d *decoder) unavailable() (Unavailable, error) {
	var u Unavailable
	err := d.object(func(key string) error {
		var err error
		switch key {
		case "route":
			if u.Route, err = d.str(); err == nil && u.Route != "" && u.Route[0] != '/' {
				err = d.errorf("invalid route %q: use a path that starts with \"/\"", u.Route)
			}
		case "taxonomy":
			u.Taxonomy, err = d.str()
		case "status":
			var v int64
			if v, err = d.integer(); err == nil && v != 0 && (v < 100 || v > 599) {
				err = d.errorf("invalid status %d: use 0 or an integer from 100 to 599", v)
			}
			u.Status = int(v)
		case "code":
			u.Code, err = d.str()
		default:
			err = d.skip()
		}
		return err
	})
	return u, err
}
