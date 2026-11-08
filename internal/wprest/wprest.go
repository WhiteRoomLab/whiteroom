// Package wprest reads from the public REST API of a WordPress site what an import needs: the settings in the index
// (/wp-json/), the taxonomies and the post types, the terms, and the authors. The values are as the API returns them;
// what they mean, such as undoing the percent-encoding of a slug, is for the caller to decide. Fetch reads them from
// the site, Snapshot.WriteJSON saves them, and ReadSnapshot reads them back, so that the import can be repeated from the
// file when the site is gone.
package wprest

import (
	"encoding/json/jsontext"
	"fmt"
	"net/http"
	"time"
)

// The limits of Fetch and ReadSnapshot, which Options can change.
const (
	PerPage                = 100
	MaxResponseBytes int64 = 16 << 20  // one response of the site
	MaxSnapshotBytes int64 = 1 << 30   // the file of a snapshot
	MaxTerms               = 1_000_000 // in all the taxonomies together, as wxr.MaxTerms
	MaxUsers               = 100_000   // as wxr.MaxAuthors
	MaxTaxonomies          = 1000      // of the taxonomies, and of the post types
	MaxRedirects           = 5
	Timeout                = 60 * time.Second // one request
	UserAgent              = "whiteroom-wpimport"
)

// Options sets the limits of Fetch and ReadSnapshot. A field that is not positive has the value of the constant of its
// name. MaxSnapshotBytes is also the limit of what Fetch keeps of the responses, and of the snapshot that it returns when
// WriteJSON writes it, so that ReadSnapshot with the same Options reads back what Fetch returned.
type Options struct {
	// API is the root of the REST API, such as https://example.com/wp-json/ or https://example.com/?rest_route=/. If it
	// is empty, Fetch looks for it.
	API string

	// Transport sends the requests. If it is nil, Fetch uses one with limits on the time of its steps.
	Transport http.RoundTripper

	Timeout                            time.Duration
	MaxResponseBytes, MaxSnapshotBytes int64
	MaxTerms, MaxUsers, MaxTaxonomies  int
}

func (o Options) withDefaults() Options {
	if o.Timeout <= 0 {
		o.Timeout = Timeout
	}
	if o.MaxResponseBytes <= 0 {
		o.MaxResponseBytes = MaxResponseBytes
	}
	if o.MaxSnapshotBytes <= 0 {
		o.MaxSnapshotBytes = MaxSnapshotBytes
	}
	if o.MaxTerms <= 0 {
		o.MaxTerms = MaxTerms
	}
	if o.MaxUsers <= 0 {
		o.MaxUsers = MaxUsers
	}
	if o.MaxTaxonomies <= 0 {
		o.MaxTaxonomies = MaxTaxonomies
	}
	return o
}

// Snapshot is what Fetch has read from a site. The JSON keys of the objects of WordPress are its own names. The slices
// are in the order that the comments say, and the file of a snapshot has them in that order.
type Snapshot struct {
	API         string        `json:"api,omitzero"` // the root of the REST API that was read
	Index       Index         `json:"index"`
	Taxonomies  []Taxonomy    `json:"taxonomies,omitempty"`  // by Slug
	Types       []Type        `json:"types,omitempty"`       // by Slug
	Terms       []Term        `json:"terms,omitempty"`       // by Taxonomy, then ID
	Users       []User        `json:"users,omitempty"`       // by ID
	Unavailable []Unavailable `json:"unavailable,omitempty"` // by Route, then Taxonomy
}

// Index is what the index of the REST API tells about the site. WordPress 6.8 added show_on_front, page_on_front and
// page_for_posts, and site_logo (5.8) and site_icon (5.9) are in the response only if the request names them in _fields.
type Index struct {
	Name           string `json:"name,omitzero"`
	Description    string `json:"description,omitzero"`
	URL            string `json:"url,omitzero"` // the site address in the settings of WordPress, which can be different from Home
	Home           string `json:"home,omitzero"`
	GMTOffset      string `json:"gmt_offset,omitzero"` // as written in the response, whether it is a number or a string
	TimezoneString string `json:"timezone_string,omitzero"`
	ShowOnFront    string `json:"show_on_front,omitzero"`
	PageOnFront    int64  `json:"page_on_front,omitzero"`
	PageForPosts   int64  `json:"page_for_posts,omitzero"`
	SiteLogo       int64  `json:"site_logo,omitzero"` // the ID of an attachment
	SiteIcon       int64  `json:"site_icon,omitzero"` // the ID of an attachment
}

type Taxonomy struct {
	Slug          string   `json:"slug,omitzero"`
	Name          string   `json:"name,omitzero"`
	Hierarchical  bool     `json:"hierarchical,omitzero"`
	RestBase      string   `json:"rest_base,omitzero"`
	RestNamespace string   `json:"rest_namespace,omitzero"` // since WordPress 5.9
	Types         []string `json:"types,omitempty"`         // the post types that use the taxonomy
}

type Type struct {
	Slug          string   `json:"slug,omitzero"`
	Name          string   `json:"name,omitzero"`
	Hierarchical  bool     `json:"hierarchical,omitzero"`
	HasArchive    Archive  `json:"has_archive,omitzero"`
	RestBase      string   `json:"rest_base,omitzero"`
	RestNamespace string   `json:"rest_namespace,omitzero"`
	Taxonomies    []string `json:"taxonomies,omitempty"`
}

// Archive is has_archive of a post type, which is false, true, or the slug of the archive.
type Archive struct {
	Set  bool   // has_archive was in the response (it is not for null)
	On   bool   // it was true, or a string that is not empty
	Slug string // the string, if it was one
}

func (a Archive) IsZero() bool { return !a.Set }

func (a Archive) MarshalJSONTo(enc *jsontext.Encoder) error {
	if a.On && a.Slug != "" {
		return enc.WriteToken(jsontext.String(a.Slug))
	}
	return enc.WriteToken(jsontext.Bool(a.On))
}

type Term struct {
	ID       int64  `json:"id,omitzero"`
	Taxonomy string `json:"taxonomy,omitzero"`
	Slug     string `json:"slug,omitzero"` // as written, so percent-encoded if it is not ASCII
	Name     string `json:"name,omitzero"` // with the character references of HTML
	Parent   int64  `json:"parent,omitzero"`
	Link     string `json:"link,omitzero"`
}

type User struct {
	ID          int64  `json:"id,omitzero"`
	Name        string `json:"name,omitzero"`        // the display name, with the character references of HTML
	Slug        string `json:"slug,omitzero"`        // user_nicename, as written
	Description string `json:"description,omitzero"` // the profile, as HTML
	URL         string `json:"url,omitzero"`         // the web site of the author
	Link        string `json:"link,omitzero"`        // the page of the author
}

// Unavailable is a route that the site did not give, such as the users of a site that does not show them without a login.
type Unavailable struct {
	Route    string `json:"route,omitzero"`    // such as /wp/v2/users, or "" if no route was made
	Taxonomy string `json:"taxonomy,omitzero"` // of the route of the terms
	Status   int    `json:"status,omitzero"`   // of the HTTP response, or 0 if there was none
	Code     string `json:"code,omitzero"`     // of the error of WordPress, such as rest_user_cannot_view; invalid_route; not_json
}

// Error is a problem with a snapshot or with a response of the site, at Pointer, an RFC 6901 JSON Pointer in the JSON
// ("" for the root, or if it is not about a place in it).
type Error struct {
	Pointer string
	Msg     string
}

func (e *Error) Error() string {
	if e.Pointer == "" {
		return e.Msg
	}
	return fmt.Sprintf("%s: %s", e.Pointer, e.Msg)
}
