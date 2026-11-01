package content

import (
	"fmt"
	"strings"
)

// RefKind is the kind of an entity that content refers to by ID.
type RefKind string

const (
	RefEntry            RefKind = "entry"
	RefMedia            RefKind = "media"
	RefForm             RefKind = "form"
	RefTerm             RefKind = "term"
	RefAuthor           RefKind = "author"
	RefMenu             RefKind = "menu"
	RefRedirect         RefKind = "redirect"
	RefRevision         RefKind = "revision"
	RefTranslationGroup RefKind = "translationGroup"
	RefUser             RefKind = "user"
)

var idPrefixes = map[RefKind]string{
	RefEntry:            "ent_",
	RefMedia:            "med_",
	RefForm:             "frm_",
	RefTerm:             "trm_",
	RefAuthor:           "aut_",
	RefMenu:             "mnu_",
	RefRedirect:         "rdr_",
	RefRevision:         "rev_",
	RefTranslationGroup: "tg_",
	RefUser:             "usr_",
}

// Ref is what ValidateOptions.Lookup tells about an entity.
type Ref struct {
	Type     string // the type of an entry, the taxonomy of a term, or the kind of a medium (MediaImage or MediaPDF)
	Language string // the language of an entry or a term
}

const (
	MediaImage = "image"
	MediaPDF   = "pdf"
)

// crockford is the alphabet of ULIDs.
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// CheckID checks that id is the ID of an entity of kind: its prefix, such as "ent_", and a ULID in upper case.
func CheckID(kind RefKind, id string) error {
	return checkID(kind, id)
}

// checkID checks that id is the ID of an entity of kind: the prefix of kind and a ULID in upper case.
func checkID(kind RefKind, id string) error {
	prefix := idPrefixes[kind]
	rest, ok := strings.CutPrefix(id, prefix)
	// A ULID has 128 bits in 26 characters of 5 bits each, so the first is at most 7.
	if !ok || len(rest) != 26 || rest[0] > '7' || strings.Trim(rest, crockford) != "" {
		return fmt.Errorf("invalid %s ID %q: use %q and a ULID (26 characters in upper case)", kind, id, prefix)
	}
	return nil
}
