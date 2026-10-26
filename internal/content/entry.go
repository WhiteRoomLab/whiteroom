package content

import (
	"encoding/json/v2"
	"fmt"
	"slices"
	"strings"
)

// SchemaVersion is the version of the JSON of entries and revisions that this package reads and writes.
const SchemaVersion = 1

type Status string

const (
	StatusDraft     Status = "draft"
	StatusPublished Status = "published"
	StatusScheduled Status = "scheduled"
	StatusArchived  Status = "archived"
)

// Entry is the state of an entry, which changes without a new revision.
type Entry struct {
	ID                string
	Number            int // sequential in the site; the ID of the post in WordPress for imported entries
	Type              string
	Language          string
	TranslationGroup  string
	Status            Status
	PublishedRevision string // the revision on the public site; only when Status is StatusPublished
	CreatedAt         string // RFC 3339, as checkDatetime accepts
	CreatedBy         string // ID of the user
}

func (x *Entry) UnmarshalJSON(data []byte) error { return decodeJSON(x, data) }

func (x *Entry) decode(d *decoder) error {
	var v Entry
	version := false
	err := d.object(func(key string) error {
		var err error
		switch key {
		case "schemaVersion":
			err = readSchemaVersion(d)
			version = true
		case "id":
			v.ID, err = d.string()
		case "number":
			v.Number, err = readInt(d, checkNumber)
		case "type":
			v.Type, err = d.string()
		case "language":
			v.Language, err = d.string()
		case "translationGroup":
			v.TranslationGroup, err = d.string()
		case "status":
			var s string
			s, err = d.string()
			v.Status = Status(s)
		case "publishedRevision":
			v.PublishedRevision, err = d.string()
		case "createdAt":
			v.CreatedAt, err = d.string()
		case "createdBy":
			v.CreatedBy, err = d.string()
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

// readSchemaVersion reads the schemaVersion of a document, which must be SchemaVersion.
func readSchemaVersion(d *decoder) error {
	v, err := d.int()
	if err != nil {
		return err
	}
	if v != SchemaVersion {
		return d.errorf("unsupported schemaVersion %d (use %d)", v, SchemaVersion)
	}
	return nil
}

func (x Entry) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		SchemaVersion     int    `json:"schemaVersion"`
		ID                string `json:"id,omitempty"`
		Number            int    `json:"number,omitzero"`
		Type              string `json:"type,omitempty"`
		Language          string `json:"language,omitempty"`
		TranslationGroup  string `json:"translationGroup,omitempty"`
		Status            Status `json:"status,omitempty"`
		PublishedRevision string `json:"publishedRevision,omitempty"`
		CreatedAt         string `json:"createdAt,omitempty"`
		CreatedBy         string `json:"createdBy,omitempty"`
	}{
		SchemaVersion:     SchemaVersion,
		ID:                x.ID,
		Number:            x.Number,
		Type:              x.Type,
		Language:          x.Language,
		TranslationGroup:  x.TranslationGroup,
		Status:            x.Status,
		PublishedRevision: x.PublishedRevision,
		CreatedAt:         x.CreatedAt,
		CreatedBy:         x.CreatedBy,
	})
}

// Validate checks x in site. Every item of an entry is required, apart from PublishedRevision.
func (x Entry) Validate(site *Site) error {
	var c checker
	x.check(&c, site)
	return c.result()
}

func (x Entry) check(c *checker, site *Site) {
	required := func(key, v string, check func(string) error) {
		if v == "" {
			c.addf("/"+key, "missing %s", key)
		} else {
			c.add("/"+key, check(v))
		}
	}
	required("id", x.ID, func(s string) error { return checkID(RefEntry, s) })
	if x.Number == 0 {
		c.addf("/number", "missing number")
	} else {
		c.add("/number", checkNumber(x.Number))
	}
	required("type", x.Type, func(s string) error {
		if site.Type(s) == nil {
			return fmt.Errorf("unknown type %q", s)
		}
		return nil
	})
	required("language", x.Language, func(s string) error {
		if !slices.Contains(site.Languages, s) {
			return fmt.Errorf("language %q is not in the site settings (use %s)", s, strings.Join(site.Languages, ", "))
		}
		return nil
	})
	required("translationGroup", x.TranslationGroup, func(s string) error { return checkID(RefTranslationGroup, s) })
	required("status", string(x.Status), func(s string) error {
		return checkChoice("status", s, string(StatusDraft), string(StatusPublished), string(StatusScheduled), string(StatusArchived))
	})
	switch {
	case x.PublishedRevision != "":
		if x.Status != StatusPublished {
			c.addf("/publishedRevision", "not allowed unless the status is published")
		}
		c.add("/publishedRevision", checkID(RefRevision, x.PublishedRevision))
	case x.Status == StatusPublished:
		c.addf("/publishedRevision", "missing publishedRevision of a published entry")
	}
	required("createdAt", x.CreatedAt, checkDatetime)
	required("createdBy", x.CreatedBy, func(s string) error { return checkID(RefUser, s) })
}

func checkNumber(v int) error {
	if v < 1 {
		return fmt.Errorf("number %d is out of range (1 or more)", v)
	}
	return nil
}
