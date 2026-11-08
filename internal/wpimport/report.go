package wpimport

import (
	"cmp"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/wprest"
	"github.com/WhiteRoomLab/whiteroom/internal/wxr"
)

// Kind is what happened to what an Entry is about.
type Kind string

const (
	Skipped Kind = "skipped" // not imported
	Changed Kind = "changed" // imported, but changed
	Review  Kind = "review"  // imported, but to be checked
)

// rank puts the kinds in the order of the report: what was lost first.
func (k Kind) rank() int {
	switch k {
	case Skipped:
		return 0
	case Changed:
		return 1
	case Review:
		return 2
	}
	return 3
}

// The records of a WXR, and rest for a value of the REST API.
const (
	recordChannel = "channel"
	recordAuthor  = "author"
	recordTerm    = "term"
	recordItem    = "item"
	recordREST    = "rest"
)

// Source is what in the WXR an Entry is about: a record, and the place in the file of the record or of a value of it.
type Source struct {
	File   string `json:"file,omitzero"`
	Line   int    `json:"line,omitzero"`
	Column int    `json:"column,omitzero"`
	Record string `json:"record,omitzero"` // channel, author, term or item, or rest for the REST API
	ID     int64  `json:"id,omitzero"`     // in WordPress, if the record has one
	Type   string `json:"type,omitzero"`   // the post type of an item, or the taxonomy of a term
	// Name is the login of an author, the name of a term, or the title of an item. An author whose login is an email
	// address, or is the email address of the author in the WXR, has no login here: the ID tells the author, or, if there
	// is none, the slug that the login makes. A user of the REST API whose name is an email address has no name either.
	Name string `json:"name,omitzero"`
	Meta string `json:"meta,omitzero"` // the key of the postmeta or termmeta that the Entry is about
}

// Target is where in the site directory the Entry's thing went.
type Target struct {
	File    string `json:"file,omitzero"`    // the path from the site directory, such as entries/<ID>.json
	Pointer string `json:"pointer,omitzero"` // the JSON Pointer in the document at File
}

type Entry struct {
	Kind    Kind   `json:"kind"`
	Source  Source `json:"source,omitzero"`
	Message string `json:"message"`
	Target  Target `json:"target,omitzero"`
}

// Info is what the report tells about the WXR as a whole.
type Info struct {
	File      string `json:"file,omitzero"`
	Version   string `json:"version,omitzero"` // of the WXR
	WordPress string `json:"wordpress,omitzero"`
	Site      string `json:"site,omitzero"` // the address of the home page
}

// Report is the import report: what was not imported or changed, and what needs a look. Add may be called by several
// goroutines at once.
type Report struct {
	info Info

	mu      sync.Mutex
	entries []Entry
}

func NewReport(info Info) *Report {
	return &Report{info: info}
}

func (r *Report) Add(e Entry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, e)
}

// Entries returns a copy of the entries in the order of the report: by the place in the WXR, then the record, the kind
// (skipped, changed, review), the message, and the target. The same entries make the same list.
func (r *Report) Entries() []Entry {
	r.mu.Lock()
	entries := slices.Clone(r.entries)
	r.mu.Unlock()
	slices.SortStableFunc(entries, func(a, b Entry) int {
		return cmp.Or(
			cmp.Compare(a.Source.File, b.Source.File),
			cmp.Compare(a.Source.Line, b.Source.Line),
			cmp.Compare(a.Source.Column, b.Source.Column),
			cmp.Compare(a.Source.Record, b.Source.Record),
			cmp.Compare(a.Source.ID, b.Source.ID),
			cmp.Compare(a.Kind.rank(), b.Kind.rank()),
			cmp.Compare(a.Message, b.Message),
			cmp.Compare(a.Target.File, b.Target.File),
			cmp.Compare(a.Target.Pointer, b.Target.Pointer),
			cmp.Compare(a.Source.Meta, b.Source.Meta),
			cmp.Compare(a.Source.Type, b.Source.Type),
			cmp.Compare(a.Source.Name, b.Source.Name),
			cmp.Compare(a.Kind, b.Kind),
		)
	})
	return entries
}

// WriteJSON writes the report as JSON, with the entries in the order of Entries.
func (r *Report) WriteJSON(w io.Writer) error {
	// The strings of a report come from a file that is UTF-8, but it should not fail for one that is not.
	b, err := json.Marshal(struct {
		WXR     Info    `json:"wxr"`
		Entries []Entry `json:"entries"`
	}{r.info, r.Entries()}, jsontext.WithIndent("  "), jsontext.AllowInvalidUTF8(true))
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

// WriteText writes a line for each entry, in the order of Entries, with the place first as the errors of the build
// have it, and the target, if there is one, at the end after " -> ":
//
//	wordpress-6.xml:120:3: changed: item 17 (wpcf7_contact_form "Contact") postmeta "_mail": the message
func (r *Report) WriteText(w io.Writer) error {
	var b strings.Builder
	for _, e := range r.Entries() {
		b.WriteString(e.line())
		b.WriteByte('\n')
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func (e Entry) line() string {
	var parts []string
	if loc := e.Source.location(); loc != "" {
		parts = append(parts, loc)
	}
	parts = append(parts, string(e.Kind))
	if what := e.Source.what(); what != "" {
		parts = append(parts, what)
	}
	if e.Message != "" {
		parts = append(parts, e.Message)
	}
	s := strings.Join(parts, ": ")
	if e.Target.File != "" || e.Target.Pointer != "" {
		s += " -> " + e.Target.File
		if e.Target.Pointer != "" {
			s += "#" + e.Target.Pointer
		}
	}
	return s
}

// location is the file and the place in it, as in the errors of the build: file:line:column.
func (s Source) location() string {
	var parts []string
	if s.File != "" {
		parts = append(parts, s.File)
	}
	if s.Line > 0 {
		parts = append(parts, strconv.Itoa(s.Line))
		if s.Column > 0 {
			parts = append(parts, strconv.Itoa(s.Column))
		}
	}
	return strings.Join(parts, ":")
}

// typeText is a post type or a taxonomy as it is, if it is made of the characters that WordPress allows in one (lower
// case letters, digits, _ and -), and quoted otherwise, so that the line stays one line.
func typeText(s string) string {
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '_', c == '-':
		default:
			return strconv.Quote(s)
		}
	}
	return s
}

// what is the record, with what tells it apart, and the meta if the Source has one.
func (s Source) what() string {
	var b strings.Builder
	b.WriteString(s.Record)
	// An author is told by the login, or by the ID if the login is not named (see Name), and a route of the REST API, which
	// has no ID or type, by its name alone.
	nameOnly := s.Record == recordAuthor || (s.Record == recordREST && s.ID == 0 && s.Type == "")
	if nameOnly {
		switch {
		case s.Name != "":
			fmt.Fprintf(&b, " %q", s.Name)
		case s.Record == recordAuthor && s.ID != 0:
			fmt.Fprintf(&b, " %d", s.ID)
		}
	} else {
		if s.ID != 0 {
			fmt.Fprintf(&b, " %d", s.ID)
		}
		switch {
		case s.Type != "" && s.Name != "":
			fmt.Fprintf(&b, " (%s %q)", typeText(s.Type), s.Name)
		case s.Type != "":
			fmt.Fprintf(&b, " (%s)", typeText(s.Type))
		case s.Name != "":
			fmt.Fprintf(&b, " (%q)", s.Name)
		}
	}
	if s.Meta != "" {
		meta := "postmeta"
		if s.Record == recordTerm {
			meta = "termmeta"
		}
		fmt.Fprintf(&b, " %s %q", meta, s.Meta)
	}
	return strings.TrimPrefix(b.String(), " ")
}

// RESTSource returns the source of an entry about the route of the REST API, such as /wp/v2/users, of the API that is
// called file in the report.
func RESTSource(file, route string) Source {
	return Source{File: file, Record: recordREST, Name: route}
}

// RESTUserSource returns the source of an entry about the user u of the REST API. It does not name a user whose name is an
// email address.
func RESTUserSource(file string, u *wprest.User) Source {
	name := u.Name
	if hasEmailAddress(decodeRefs(name)) {
		name = ""
	}
	return Source{File: file, Record: recordREST, ID: u.ID, Type: "user", Name: name}
}

// RESTTermSource returns the source of an entry about the term t of the REST API.
func RESTTermSource(file string, t *wprest.Term) Source {
	return Source{File: file, Record: recordREST, ID: t.ID, Type: t.Taxonomy, Name: t.Name}
}

// taxonomySource returns the source of an entry about the taxonomy of the term t as a whole, at the place of t.
func taxonomySource(file string, t *wxr.Term) Source {
	return Source{File: file, Line: t.Line, Column: t.Column, Record: recordTerm, Type: t.Taxonomy}
}

// documentTarget returns the place in the document of the entity with the ID id of kind, such as terms/<ID>.json.
func documentTarget(kind content.RefKind, id, pointer string) Target {
	return Target{File: content.DocumentDir(kind) + "/" + id + ".json", Pointer: pointer}
}

func settingsTarget(pointer string) Target {
	return Target{File: "site/settings.json", Pointer: pointer}
}

func schemaTarget(pointer string) Target {
	return Target{File: "site/schema.yaml", Pointer: pointer}
}
