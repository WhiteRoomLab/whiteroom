// Package wxr reads a WordPress export (WXR 1.0 to 1.2) into Go values, as they are written. What the values mean, such
// as dates, statuses and the normalization of slugs, is for the caller to decide.
package wxr

import "fmt"

// The limits of Read, which Options can change.
const (
	MaxBytes    int64 = 1 << 30 // the size of the file
	MaxItems          = 1_000_000
	MaxTerms          = 1_000_000 // wp:category, wp:tag and wp:term together
	MaxAuthors        = 100_000
	MaxDepth          = 64       // the nesting of elements, with <rss> at 1
	MaxTagBytes       = 64 << 10 // a start tag or an end tag, from < to >
)

// Options sets the limits of Read. A field that is not positive has the value of the constant of its name.
type Options struct {
	MaxBytes                                              int64
	MaxItems, MaxTerms, MaxAuthors, MaxDepth, MaxTagBytes int
}

func (o Options) withDefaults() Options {
	if o.MaxBytes <= 0 {
		o.MaxBytes = MaxBytes
	}
	if o.MaxItems <= 0 {
		o.MaxItems = MaxItems
	}
	if o.MaxTerms <= 0 {
		o.MaxTerms = MaxTerms
	}
	if o.MaxAuthors <= 0 {
		o.MaxAuthors = MaxAuthors
	}
	if o.MaxDepth <= 0 {
		o.MaxDepth = MaxDepth
	}
	if o.MaxTagBytes <= 0 {
		o.MaxTagBytes = MaxTagBytes
	}
	return o
}

// Pos is a place in the file. Line and Column start at 1, and Column counts characters (code points), so a tab is one.
// The characters that Read removes count, and a byte order mark at the start does not.
type Pos struct{ Line, Column int }

// Error is a problem with the file at Pos. It has no file name, which the caller adds.
type Error struct {
	Pos
	Msg string
}

func (e *Error) Error() string {
	return fmt.Sprintf("%d:%d: %s", e.Line, e.Column, e.Msg)
}

// Export is what Read has read, in the order of the file.
type Export struct {
	Version   string // wp:wxr_version, without white space: 1.0, 1.1 or 1.2
	WordPress string // the version of the WordPress that wrote the file, or "" if the file does not tell
	Channel   Channel
	Authors   []Author
	Terms     []Term
	Items     []Item
	Notes     []Note // in the order of the places in the file

	postIdx   map[int64]int
	termIdx   map[termKey]int
	slugIdx   map[termKey]int
	authorIdx map[string]int
}

// termKey is a taxonomy with the ID of a term, or with its slug.
type termKey struct {
	taxonomy string
	id       int64
	slug     string
}

type Channel struct {
	Pos
	Title, Link, Description, PubDate, Language, Generator, BaseSiteURL, BaseBlogURL string
}

type Author struct {
	Pos
	ID                                             int64 // wp:author_id, or 0 if there is none
	Login, Email, DisplayName, FirstName, LastName string
}

// Term is a wp:category, a wp:tag or a wp:term.
type Term struct {
	Pos
	ID          int64  // wp:term_id, or 0 if there is none, as in WXR 1.0
	Taxonomy    string // category for a wp:category, and post_tag for a wp:tag
	Slug        string // as written, so percent-encoded if it is not ASCII
	Parent      string // the slug of the parent as written, or ""
	Name        string
	Description string
	Meta        MetaList // wp:termmeta
}

type Item struct {
	Pos
	Title, Link, PubDate string
	Creator              string // dc:creator, the login of the author
	GUID, Description    string
	Content              string // content:encoded
	Excerpt              string // excerpt:encoded
	ID                   int64  // wp:post_id

	Date, DateGMT, Modified, ModifiedGMT string
	CommentStatus, PingStatus            string
	Name                                 string // wp:post_name, as written, so percent-encoded if it is not ASCII
	Status                               string
	Type                                 string // wp:post_type
	Password, AttachmentURL              string
	Parent, MenuOrder                    int64
	Sticky                               bool
	Terms                                []TermRef // the category elements
	Meta                                 MetaList  // wp:postmeta
	Comments                             int       // the number of wp:comment elements, whose content is not read
}

// TermRef is a category element of an item.
type TermRef struct {
	Pos
	Taxonomy string // domain, with post_tag for tag
	Slug     string // nicename, as written
	Name     string
}

// Meta is a wp:postmeta or a wp:termmeta.
type Meta struct {
	Pos
	Key, Value string
}

type MetaList []Meta

// Get returns the value of the first Meta whose Key is key.
func (l MetaList) Get(key string) (string, bool) {
	for _, m := range l {
		if m.Key == key {
			return m.Value, true
		}
	}
	return "", false
}

// All returns the values of the Meta whose Key is key, in the order they are written.
func (l MetaList) All(key string) []string {
	var values []string
	for _, m := range l {
		if m.Key == key {
			values = append(values, m.Value)
		}
	}
	return values
}

type RecordKind uint8

const (
	RecordChannel RecordKind = iota
	RecordAuthor
	RecordTerm
	RecordItem
)

// Note is something that Read changed in the values of a record: it removed the characters that XML does not allow.
type Note struct {
	Pos    // of the first character removed
	Record RecordKind
	Index  int // in Authors, Terms or Items, and 0 for the channel
	Msg    string
}

// Post returns the item whose wp:post_id is id, or nil.
func (e *Export) Post(id int64) *Item {
	if id < 1 {
		return nil
	}
	if e.postIdx != nil {
		if i, ok := e.postIdx[id]; ok {
			return &e.Items[i]
		}
		return nil
	}
	for i := range e.Items {
		if e.Items[i].ID == id {
			return &e.Items[i]
		}
	}
	return nil
}

// Term returns the term of taxonomy whose wp:term_id is id, or nil. WordPress before 4.2 could give terms of different
// taxonomies the same ID, so the taxonomy is part of the key.
func (e *Export) Term(taxonomy string, id int64) *Term {
	if id < 1 {
		return nil
	}
	if e.termIdx != nil {
		if i, ok := e.termIdx[termKey{taxonomy: taxonomy, id: id}]; ok {
			return &e.Terms[i]
		}
		return nil
	}
	for i := range e.Terms {
		if t := &e.Terms[i]; t.Taxonomy == taxonomy && t.ID == id {
			return t
		}
	}
	return nil
}

// TermBySlug returns the term of taxonomy whose slug is slug, as written, or nil.
func (e *Export) TermBySlug(taxonomy, slug string) *Term {
	if e.slugIdx != nil {
		if i, ok := e.slugIdx[termKey{taxonomy: taxonomy, slug: slug}]; ok {
			return &e.Terms[i]
		}
		return nil
	}
	for i := range e.Terms {
		if t := &e.Terms[i]; t.Taxonomy == taxonomy && t.Slug == slug {
			return t
		}
	}
	return nil
}

// Author returns the author whose login is login, or nil.
func (e *Export) Author(login string) *Author {
	if e.authorIdx != nil {
		if i, ok := e.authorIdx[login]; ok {
			return &e.Authors[i]
		}
		return nil
	}
	for i := range e.Authors {
		if e.Authors[i].Login == login {
			return &e.Authors[i]
		}
	}
	return nil
}
