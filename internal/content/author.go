package content

import (
	"encoding/json/v2"
	"errors"
	"net/url"
	"slices"
	"strconv"
)

// AuthorProfile is the public profile of an author. Login accounts link to it on their side, so it holds nothing about them.
type AuthorProfile struct {
	ID     string
	Name   string
	Slug   string
	Bio    Localized
	Avatar string   // ID of an image
	Links  []string // http and https URLs, such as profiles on social media
}

func (x *AuthorProfile) UnmarshalJSON(data []byte) error { return decodeJSON(x, data) }

func (x *AuthorProfile) decode(d *decoder) error {
	var v AuthorProfile
	version := false
	err := d.object(func(key string) error {
		var err error
		switch key {
		case "schemaVersion":
			err = readSchemaVersion(d)
			version = true
		case "id":
			v.ID, err = d.string()
		case "name":
			v.Name, err = d.string()
		case "slug":
			v.Slug, err = d.string()
		case "bio":
			err = v.Bio.decode(d)
		case "avatar":
			v.Avatar, err = d.string()
		case "links":
			err = d.array(func(int) error {
				l, err := d.string()
				v.Links = append(v.Links, l)
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

func (x AuthorProfile) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		SchemaVersion int       `json:"schemaVersion"`
		ID            string    `json:"id,omitempty"`
		Name          string    `json:"name,omitempty"`
		Slug          string    `json:"slug,omitempty"`
		Bio           Localized `json:"bio,omitempty"`
		Avatar        string    `json:"avatar,omitempty"`
		Links         []string  `json:"links,omitempty"`
	}{
		SchemaVersion: SchemaVersion,
		ID:            x.ID,
		Name:          x.Name,
		Slug:          x.Slug,
		Bio:           x.Bio,
		Avatar:        x.Avatar,
		Links:         x.Links,
	})
}

// Normalize cleans up the texts and URLs in place as Revision.Normalize does, maps the slug to lower case
// and removes values of which nothing is left.
func (x *AuthorProfile) Normalize() {
	x.Name = normalizeText(x.Name, false)
	x.Slug = normalizeSlug(x.Slug)
	x.Bio.normalize(true)
	for i, l := range x.Links {
		x.Links[i] = normalizeHref(l)
	}
	x.Links = slices.DeleteFunc(x.Links, func(l string) bool { return l == "" })
	// Empty is nil, as UnmarshalJSON makes it.
	if len(x.Links) == 0 {
		x.Links = nil
	}
}

// Validate checks x in site. ID, Name and Slug are required.
// lookup tells about the avatar as ValidateOptions.Lookup does; nil skips checking that the avatar exists and is an image.
func (x AuthorProfile) Validate(site *Site, lookup func(kind RefKind, id string) (Ref, bool)) error {
	c := checker{doc: &docState{opts: ValidateOptions{Lookup: lookup}}}
	x.check(&c, site)
	return c.result()
}

func (x AuthorProfile) check(c *checker, site *Site) {
	c.required("id", x.ID, func(s string) error { return checkID(RefAuthor, s) })
	c.required("name", x.Name, checkLine)
	c.required("slug", x.Slug, checkSlug)
	x.Bio.check(c, "/bio", site, true)
	if x.Avatar != "" {
		c.media("/avatar", x.Avatar, MediaImage)
	}
	for i, l := range x.Links {
		p := "/links/" + strconv.Itoa(i)
		if slices.Contains(x.Links[:i], l) {
			c.addf(p, "duplicate link %q", l)
		} else {
			c.add(p, checkWebURL(l))
		}
	}
}

// checkWebURL accepts http and https URLs as checkHref does.
func checkWebURL(s string) error {
	if err := checkHref(s); err != nil {
		return err
	}
	if u, _ := url.Parse(s); u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("must be an http or https URL")
	}
	return nil
}
