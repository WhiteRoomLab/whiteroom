package build

import (
	"encoding/json/jsontext"
	"encoding/json/v2"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/render"
	"github.com/WhiteRoomLab/whiteroom/internal/theme"
)

// The structured data of pages: JSON-LD of schema.org, written with the members in the order of the fields.

const schemaOrg = "https://schema.org"

// webSite is the structured data of a front page.
type webSite struct {
	Context    string `json:"@context"`
	Type       string `json:"@type"`
	Name       string `json:"name"`
	URL        string `json:"url"`
	InLanguage string `json:"inLanguage"`
}

// blogPosting is the structured data of the page of a post.
type blogPosting struct {
	Context       string       `json:"@context"`
	Type          string       `json:"@type"`
	Headline      string       `json:"headline"`
	URL           string       `json:"url"`
	DatePublished string       `json:"datePublished,omitempty"`
	Author        *person      `json:"author,omitzero"`
	Image         string       `json:"image,omitempty"`
	Description   string       `json:"description,omitempty"`
	InLanguage    string       `json:"inLanguage"`
	Publisher     organization `json:"publisher"`
}

// webPage is the structured data of the page of an entry other than a post (WebPage), or of a list (CollectionPage).
type webPage struct {
	Context     string `json:"@context"`
	Type        string `json:"@type"`
	Name        string `json:"name"`
	URL         string `json:"url"`
	Description string `json:"description,omitempty"`
	InLanguage  string `json:"inLanguage"`
}

// profilePage is the structured data of an author page.
type profilePage struct {
	Context    string  `json:"@context"`
	Type       string  `json:"@type"`
	MainEntity *person `json:"mainEntity"`
}

type person struct {
	Type   string   `json:"@type"`
	Name   string   `json:"name"`
	URL    string   `json:"url"`
	SameAs []string `json:"sameAs,omitempty"`
	Image  string   `json:"image,omitempty"`
}

type organization struct {
	Type string `json:"@type"`
	Name string `json:"name"`
	Logo string `json:"logo,omitempty"`
}

// structuredData returns the structured data of p, whose canonical URL is url, whose description is description ("" if
// none) and whose image for Open Graph is image (nil if none): WebSite on the front pages; BlogPosting on the pages of
// posts; WebPage on the pages of other entries; CollectionPage on the pages of the lists of the posts pages, the terms
// and the types; ProfilePage on the author pages; nil on the 404 pages.
func (b *builder) structuredData(p *page, url, description string, image *headImage) any {
	lang := p.language
	if p.front {
		return &webSite{
			Context: schemaOrg, Type: "WebSite", Name: b.site.Name[lang], URL: b.absURL(b.site.LanguagePath(lang)),
			InLanguage: lang,
		}
	}
	switch p.kind {
	case theme.KindEntry:
		if p.entry.e.Type == content.TypePost {
			return b.blogPosting(p, url, description, image)
		}
		return &webPage{
			Context: schemaOrg, Type: "WebPage", Name: b.heading(p), URL: url, Description: description,
			InLanguage: lang,
		}
	case theme.KindPosts, theme.KindTerm, theme.KindArchive:
		return &webPage{
			Context: schemaOrg, Type: "CollectionPage", Name: b.heading(p), URL: url, Description: description,
			InLanguage: lang,
		}
	case theme.KindAuthor:
		x := b.person(p.author.v.ID, lang)
		if id := p.author.v.Avatar; id != "" {
			if avatar := b.headImage(id, lang); avatar != nil {
				x.Image = avatar.url
			}
		}
		return &profilePage{Context: schemaOrg, Type: "ProfilePage", MainEntity: x}
	case theme.KindNotFound:
	}
	return nil
}

// blogPosting returns the structured data of p, the page of a post, as structuredData does.
func (b *builder) blogPosting(p *page, url, description string, image *headImage) *blogPosting {
	rev, lang := p.entry.rev, p.language
	x := &blogPosting{
		Context:     schemaOrg,
		Type:        "BlogPosting",
		Headline:    rev.Title,
		URL:         url,
		Description: description,
		InLanguage:  lang,
		Publisher:   organization{Type: "Organization", Name: b.site.Name[lang]},
	}
	if d := newDate(rev.Date, b.loc); d != nil {
		x.DatePublished = d.Datetime
	}
	if rev.Author != "" {
		x.Author = b.person(rev.Author, lang)
	}
	if image != nil {
		x.Image = image.url
	}
	if b.site.Logo != "" {
		if logo := b.headImage(b.site.Logo, lang); logo != nil {
			x.Publisher.Logo = logo.url
		}
	}
	return x
}

// person returns the author with the ID as a Person on a page in language, with the URL of their author page in the
// language.
func (b *builder) person(id, language string) *person {
	a := &b.authors[id].v
	x := &person{Type: "Person", Name: a.Name, URL: b.absURL(b.authorPaths[id][language])}
	for _, l := range a.Links {
		x.SameAs = append(x.SameAs, render.EncodeURL(l))
	}
	return x
}

// jsonLD returns the script of data, structured data, as JSON-LD on one line, in which <, > and & are escaped, so that
// no text in it can end the script.
func jsonLD(data any) string {
	// Marshal cannot fail on these types once invalid UTF-8 is allowed (and replaced).
	js, _ := json.Marshal(data, jsontext.EscapeForHTML(true), jsontext.AllowInvalidUTF8(true))
	return `<script type="application/ld+json">` + string(js) + "</script>"
}
