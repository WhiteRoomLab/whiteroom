package content

import (
	"encoding/json/v2"
	"fmt"
	"strings"
)

// UnmarshalSettings reads data, a settings document, into the items that the admin screen changes, which are all but
// AuthorURL, Types and Taxonomies: those come from the schema file and stay as they are. The items that data omits
// become zero. s changes only if all of data is read without error.
func (s *Site) UnmarshalSettings(data []byte) error {
	var v Site
	if err := unmarshal(data, "", v.decodeSettings); err != nil {
		return err
	}
	v.AuthorURL, v.Types, v.Taxonomies = s.AuthorURL, s.Types, s.Taxonomies
	*s = v
	return nil
}

func (s *Site) decodeSettings(d *decoder) error {
	version := false
	err := d.object(func(key string) error {
		var err error
		switch key {
		case "schemaVersion":
			err = readSchemaVersion(d)
			version = true
		case "name":
			err = s.Name.decode(d)
		case "tagline":
			err = s.Tagline.decode(d)
		case "url":
			s.URL, err = d.string()
		case "timeZone":
			s.TimeZone, err = d.string()
		case "logo":
			s.Logo, err = d.string()
		case "icon":
			s.Icon, err = d.string()
		case "languages":
			err = d.array(func(int) error {
				l, err := d.string()
				s.Languages = append(s.Languages, l)
				return err
			})
		case "defaultLanguage":
			s.DefaultLanguage, err = d.string()
		case "prefixDefaultLanguage":
			s.PrefixDefaultLanguage, err = d.bool()
		case "frontPage":
			err = s.FrontPage.decode(d)
		case "postsPage":
			err = s.PostsPage.decode(d)
		case "postsPerPage":
			s.PostsPerPage, err = readInt(d, checkPostsPerPage)
		case "seo":
			err = s.SEO.decode(d)
		case "allowHtml":
			s.AllowHTML, err = d.bool()
		case "types", "taxonomies", "authorUrl":
			return d.errorf("unknown key; it belongs in the schema file")
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
	return nil
}

func (x *SiteSEO) decode(d *decoder) error {
	var v SiteSEO
	err := d.object(func(key string) error {
		var err error
		switch key {
		case "titleFormat":
			err = v.TitleFormat.decode(d)
		case "ogImage":
			err = v.OGImage.decode(d)
		case "noindex":
			v.NoIndex, err = d.bool()
		default:
			return d.errorf("unknown key")
		}
		return err
	})
	if err != nil {
		return err
	}
	*x = v
	return nil
}

func checkPostsPerPage(n int) error {
	if n < 1 || n > MaxPostsPerPage {
		return fmt.Errorf("postsPerPage %d is out of range (1 to %d)", n, MaxPostsPerPage)
	}
	return nil
}

// MarshalSettings writes the items of s that UnmarshalSettings reads, as a settings document: AuthorURL, Types and
// Taxonomies, which belong in the schema file, are not written.
func (s *Site) MarshalSettings() ([]byte, error) {
	type seo struct {
		TitleFormat Localized `json:"titleFormat,omitempty"`
		OGImage     Localized `json:"ogImage,omitempty"`
		NoIndex     bool      `json:"noindex,omitzero"`
	}
	return json.Marshal(struct {
		SchemaVersion         int       `json:"schemaVersion"`
		Name                  Localized `json:"name,omitempty"`
		Tagline               Localized `json:"tagline,omitempty"`
		URL                   string    `json:"url,omitempty"`
		TimeZone              string    `json:"timeZone,omitempty"`
		Logo                  string    `json:"logo,omitempty"`
		Icon                  string    `json:"icon,omitempty"`
		Languages             []string  `json:"languages,omitempty"`
		DefaultLanguage       string    `json:"defaultLanguage,omitempty"`
		PrefixDefaultLanguage bool      `json:"prefixDefaultLanguage,omitzero"`
		FrontPage             Localized `json:"frontPage,omitempty"`
		PostsPage             Localized `json:"postsPage,omitempty"`
		PostsPerPage          int       `json:"postsPerPage,omitzero"`
		SEO                   seo       `json:"seo,omitempty"` // not omitzero: empty maps make it {}, which cannot be read
		AllowHTML             bool      `json:"allowHtml,omitzero"`
	}{
		SchemaVersion:         SchemaVersion,
		Name:                  s.Name,
		Tagline:               s.Tagline,
		URL:                   s.URL,
		TimeZone:              s.TimeZone,
		Logo:                  s.Logo,
		Icon:                  s.Icon,
		Languages:             s.Languages,
		DefaultLanguage:       s.DefaultLanguage,
		PrefixDefaultLanguage: s.PrefixDefaultLanguage,
		FrontPage:             s.FrontPage,
		PostsPage:             s.PostsPage,
		PostsPerPage:          s.PostsPerPage,
		SEO:                   seo(s.SEO),
		AllowHTML:             s.AllowHTML,
	})
}

// NormalizeSettings cleans up Name, Tagline, SEO.TitleFormat and URL in place as Revision.Normalize does texts of one
// line and URLs, removing the languages of which nothing is left, and maps the ASCII letters of the scheme and the host
// of URL to lower case. The admin screen is to call it before saving the settings document. It leaves the other items
// as they are, including AuthorURL, Types and Taxonomies, which developers write in the schema file; Validate reports
// what is not normalized.
func (s *Site) NormalizeSettings() {
	s.Name.normalize(false)
	s.Tagline.normalize(false)
	s.SEO.TitleFormat.normalize(false)
	s.URL = normalizeSiteURL(s.URL)
}

// normalizeSiteURL cleans up s as normalizeHref does and maps the ASCII letters of the scheme and the authority, up to
// the first "/", "?" or "#" after "://", to lower case. Only a "://" before any other "/", "?" or "#" ends a scheme.
func normalizeSiteURL(s string) string {
	s = normalizeHref(s)
	i := strings.IndexAny(s, "/?#")
	if i < 1 || s[i-1] != ':' || !strings.HasPrefix(s[i:], "//") {
		return s
	}
	end := len(s)
	if j := strings.IndexAny(s[i+2:], "/?#"); j >= 0 {
		end = i + 2 + j
	}
	// Not strings.ToLower: it maps U+212A KELVIN SIGN to "k", turning a host that checkSiteURL rejects into another.
	return strings.Map(func(r rune) rune {
		if 'A' <= r && r <= 'Z' {
			return r + 'a' - 'A'
		}
		return r
	}, s[:end]) + s[end:]
}
