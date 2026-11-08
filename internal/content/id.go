package content

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
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

// FormatID returns the ID of an entity of kind whose ULID has the bits u, written as ULIDs are. It panics if kind is
// unknown.
func FormatID(kind RefKind, u [16]byte) string {
	prefix, ok := idPrefixes[kind]
	if !ok {
		panic(fmt.Sprintf("content: unknown kind %q", kind))
	}
	hi, lo := binary.BigEndian.Uint64(u[:8]), binary.BigEndian.Uint64(u[8:])
	var b [26]byte
	for i := len(b) - 1; i >= 0; i-- {
		b[i] = crockford[lo&31]
		lo = lo>>5 | hi<<59
		hi >>= 5
	}
	return prefix + string(b[:])
}

// IDSource makes the ULIDs of the IDs that the operations make (6.7): 48 bits of Unix milliseconds and 80 random bits
// from its reader. They grow within a source, also within a millisecond and when the clock goes back: then the random
// bits of the last one count up. It is safe for concurrent use.
type IDSource struct {
	mu   sync.Mutex
	rand io.Reader
	last [16]byte
	used bool
}

// NewIDSource returns an IDSource that reads its random bits from r, such as crypto/rand.Reader.
func NewIDSource(r io.Reader) *IDSource {
	return &IDSource{rand: r}
}

// Next returns a ULID for the time now that is larger than the last one that the source made.
func (g *IDSource) Next(now time.Time) ([16]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	ms := now.UnixMilli()
	if ms < 0 || ms >= 1<<48 {
		return [16]byte{}, fmt.Errorf("the time %s is out of range for an ID (1970 to the year 10889)", now.Format(time.RFC3339))
	}
	var id [16]byte
	if lastMs := binary.BigEndian.Uint64(g.last[:8]) >> 16; g.used && uint64(ms) <= lastMs {
		id = g.last
		if countUp(id[6:]) {
			return [16]byte{}, errors.New("cannot make an ID: the 80 random bits of the last one are all ones")
		}
	} else {
		binary.BigEndian.PutUint64(id[:8], uint64(ms)<<16)
		if _, err := io.ReadFull(g.rand, id[6:]); err != nil {
			return [16]byte{}, err
		}
	}
	g.last, g.used = id, true
	return id, nil
}

// NewID returns a new ID for an entity of kind, whose ULID is that of Next.
func (g *IDSource) NewID(kind RefKind, now time.Time) (string, error) {
	ulid, err := g.Next(now)
	if err != nil {
		return "", err
	}
	return FormatID(kind, ulid), nil
}

// countUp adds 1 to b, a big-endian number, and reports whether it overflowed.
func countUp(b []byte) bool {
	for i := len(b) - 1; i >= 0; i-- {
		b[i]++
		if b[i] != 0 {
			return false
		}
	}
	return true
}
