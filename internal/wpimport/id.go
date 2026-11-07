package wpimport

import (
	"crypto/sha256"
	"fmt"
	"strconv"
	"strings"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/wxr"
)

// ObjectKind is the kind of an object of WordPress that gets an ID of its own.
type ObjectKind string

const (
	// ObjectPost is what has a wp:post_id: posts, pages, custom post types, attachments, menu items, and the forms of
	// Contact Form 7.
	ObjectPost ObjectKind = "post"
	// ObjectTerm is a term, a menu (nav_menu) too.
	ObjectTerm ObjectKind = "term"
	// ObjectUser is an author.
	ObjectUser ObjectKind = "user"
)

// Object is an object of WordPress. Objects are compared by value.
type Object struct {
	Kind     ObjectKind
	ID       int64  // of a post or of a term
	Taxonomy string // of a term
	Slug     string // of a term that has no ID, as written
	Login    string // of a user
}

func PostObject(it *wxr.Item) Object {
	return Object{Kind: ObjectPost, ID: it.ID}
}

// TermObject returns the term by its ID, or, if it has none as in a WXR 1.0, by its slug as written.
func TermObject(t *wxr.Term) Object {
	if t.ID >= 1 {
		return Object{Kind: ObjectTerm, Taxonomy: t.Taxonomy, ID: t.ID}
	}
	return Object{Kind: ObjectTerm, Taxonomy: t.Taxonomy, Slug: t.Slug}
}

func UserObject(login string) Object {
	return Object{Kind: ObjectUser, Login: login}
}

func (o Object) String() string {
	switch o.Kind {
	case ObjectPost:
		return fmt.Sprintf("post %d", o.ID)
	case ObjectTerm:
		if o.ID >= 1 {
			return fmt.Sprintf("term %d (%s)", o.ID, o.Taxonomy)
		}
		return fmt.Sprintf("term %q (%s)", o.Slug, o.Taxonomy)
	case ObjectUser:
		return fmt.Sprintf("user %q", o.Login)
	}
	type plain Object
	return fmt.Sprintf("%+v", plain(o))
}

// check returns an error unless o has the fields of its kind and no others.
func (o Object) check() error {
	problem := ""
	switch o.Kind {
	case ObjectPost:
		switch {
		case o.ID < 1:
			problem = "a post needs an ID of 1 or more"
		case o.Taxonomy != "" || o.Slug != "" || o.Login != "":
			problem = "a post has only an ID"
		}
	case ObjectTerm:
		switch {
		case o.Taxonomy == "":
			problem = "a term needs a taxonomy"
		case o.ID < 0 || (o.ID == 0 && o.Slug == ""):
			problem = "a term needs an ID of 1 or more, or else a slug"
		case o.ID >= 1 && o.Slug != "":
			problem = "a term has an ID or a slug, not both"
		case o.Login != "":
			problem = "a term has no login"
		}
	case ObjectUser:
		switch {
		case o.Login == "":
			problem = "a user needs a login"
		case o.ID != 0 || o.Taxonomy != "" || o.Slug != "":
			problem = "a user has only a login"
		}
	default:
		problem = "unknown kind"
	}
	if problem != "" {
		return fmt.Errorf("invalid object %s: %s", o, problem)
	}
	return nil
}

// key is the string that the ID of the object comes from.
func (o Object) key() string {
	switch o.Kind {
	case ObjectPost:
		return "post\x00" + strconv.FormatInt(o.ID, 10)
	case ObjectTerm:
		if o.ID >= 1 {
			return "term\x00" + o.Taxonomy + "\x00id\x00" + strconv.FormatInt(o.ID, 10)
		}
		return "term\x00" + o.Taxonomy + "\x00slug\x00" + o.Slug
	case ObjectUser:
		return "user\x00" + o.Login
	}
	return ""
}

// Lookup finds the kind and the ID of the entity that an object of WordPress became, and is false for an object that is
// not imported, so that what refers to it can leave the reference out. The Lookup method of a Table is one.
type Lookup func(o Object) (kind content.RefKind, id string, ok bool)

// Seed returns what the IDs of an import of e come from: the address of the home page, so that the IDs are the same
// for every WXR of the site.
func Seed(e *wxr.Export) string {
	if s := strings.TrimSpace(e.Channel.BaseBlogURL); s != "" {
		return s
	}
	return strings.TrimSpace(e.Channel.Link)
}

// idVersion is hashed into every ID. Changing how IDs are made changes the IDs of an import that is made again, so
// change the number at its end then.
const idVersion = "whiteroom wpimport id 1"

// newID returns an ID of kind that is the same for the same seed and key: the ULID has a time of 0, and 80 bits of a
// hash in place of random bits.
func newID(kind content.RefKind, seed, key string) string {
	var in []byte
	for _, s := range []string{idVersion, seed, string(kind), key} {
		in = append(in, s...)
		in = append(in, 0)
	}
	sum := sha256.Sum256(in)
	var u [16]byte
	copy(u[6:], sum[:10])
	return content.FormatID(kind, u)
}

type assignment struct {
	kind    content.RefKind
	id      string
	dropped bool
}

// Table gives an ID to each object of WordPress that is imported, the same one every time for the same seed, kind and
// object. Assign and Drop must not be called together or with Lookup, but Lookup may be called by several goroutines.
type Table struct {
	seed     string
	assigned map[Object]*assignment
	owners   map[string]Object
}

func NewTable(seed string) *Table {
	return &Table{seed: seed, assigned: make(map[Object]*assignment), owners: make(map[string]Object)}
}

// Assign gives o the ID of an entity of kind. An object that has an ID gets the same one again, also after Drop, but
// only for the same kind: another kind is an error.
func (t *Table) Assign(o Object, kind content.RefKind) (string, error) {
	if err := o.check(); err != nil {
		return "", err
	}
	if a, ok := t.assigned[o]; ok {
		if a.kind != kind {
			return "", fmt.Errorf("%s is already imported as %s", o, a.kind)
		}
		a.dropped = false
		return a.id, nil
	}
	id := t.NewID(kind, o.key())
	if other, ok := t.owners[id]; ok {
		return "", fmt.Errorf("%s and %s get the same ID %s", o, other, id)
	}
	t.assigned[o] = &assignment{kind: kind, id: id}
	t.owners[id] = o
	return id, nil
}

// Drop takes o out of the import, which Lookup then does not find. An Assign of it brings it back with the same ID.
func (t *Table) Drop(o Object) {
	if a, ok := t.assigned[o]; ok {
		a.dropped = true
	}
}

func (t *Table) Lookup(o Object) (content.RefKind, string, bool) {
	a, ok := t.assigned[o]
	if !ok || a.dropped {
		return "", "", false
	}
	return a.kind, a.id, true
}

// NewID returns an ID of kind for something that does not come from one object of WordPress, such as a revision, a
// translation group, or a redirect. The key tells it apart from the others of its kind, so it is up to the caller to
// make a key of its own. NewID may be called while others call Assign, Drop and Lookup.
func (t *Table) NewID(kind content.RefKind, key string) string {
	return newID(kind, t.seed, key)
}
