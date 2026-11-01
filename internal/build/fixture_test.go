package build

import (
	"crypto/sha256"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// IDs in the fixture: those of the examples in 6.11 of the concept (docs/06.11-examples.md) and of the tests of
// internal/content, and new ones.
const (
	homeID      = "ent_01JBG3N6S1V4X7Z0C3E6G9J2K5" // ja page, the front page
	newsID      = "ent_01JBG3N6T2W5Y8B1D4F7H0K3M6" // ja page, the posts page
	aboutID     = "ent_01JB8W2N5KD3F6H9J1M4P7R2T5" // ja page of 6.11.1
	servicesID  = "ent_01JB8Y6T2RG7M4XK9P2D5W8NQ3" // ja page
	contactID   = "ent_01JBD1Y2Z3A4B5C6D7E8F9G0H1" // ja page with the form
	privacyID   = "ent_01JBD1A2B3C4D5E6F7G8H9J0K1" // ja page
	enAboutID   = "ent_01JB8Y6T2VS4W7Z2B5D8F1H6J9" // en page, a translation of aboutID
	enPrivacyID = "ent_01JBD1M2N3P4Q5R6S7T8V9W0X1" // en page, a translation of privacyID
	postID      = "ent_01JB8Y6T2WT6X9C3E6G9J2K5M8" // ja post of 6.11.9
	enPostID    = "ent_01JC0000000000000000000001" // en post, a translation of postID
	worksID     = "ent_01JC0000000000000000000002" // ja works
	draftID     = "ent_01JC0000000000000000000003" // ja page, a draft

	newsCategoryID  = "trm_01JBA4C7E2G5J8K1M4P7R2T5W8" // ja category of 6.11.3
	pressReleaseID  = "trm_01JBA4C7F3H6K9N2Q5S8V1X4Z7" // ja category, a child of newsCategoryID
	eventTagID      = "trm_01JBA4C7G4J7M1P4R7T1W4Y7B1" // ja tag
	enEventTagID    = "trm_01JBA4C7H5K8N3Q6S9V3X6Z9C2" // en tag, a translation of eventTagID
	worksCategoryID = "trm_01JB9A00000000000000000001" // ja works_category

	imageID   = "med_01JB8Z3K5QH2V6T9C4R7Y1E8B5" // the image of 6.11.5
	pdfID     = "med_01JB8Z3K5TM8A1C5F9V3J6K2N7" // the PDF of 6.11.5
	avatarID  = "med_01JB8Z3K5RJ4W7Z1D5S8X2F6C9"
	logoID    = "med_01JBG3N6V3X6Z9C2E5G8J1K4M7"
	iconID    = "med_01JBG3N6W4Y7B0D3F6H9K2M5P8"
	ogImageID = "med_01JBG3N6X5Z8C1E4G7J0K3N6Q9"

	authorID   = "aut_01JBA4C7N9Q3T6W9Z3C6F9H2K5" // 6.11.4
	headerID   = "mnu_01JBD2A3B4C5D6E7F8G9H0J1K2" // 6.11.6
	footerID   = "mnu_01JBD2M5N6P7Q8R9S0T1V2W3X4" // 6.11.6
	formID     = "frm_01JB8Z3K5VP1D4G7J2M5Q8T3W6" // 6.11.8
	redirectID = "rdr_01JBD3A4B5C6D7E8F9G0H1J2K3" // 6.11.7
	userID     = "usr_01JB8T7H4MA2C5E8G1J4K7N3P6"

	aboutGroup = "tg_01JB8X4M7NB5D8F1H4J7K2M6P9" // 6.11.1
	postGroup  = "tg_01JBG3N6Q9S2V5X8Z1B4D7F0H3" // 6.11.9
)

// revisionID returns the ID of the published revision of the entry id in the fixture.
func revisionID(id string) string { return "rev_" + strings.TrimPrefix(id, "ent_") }

// groupOf returns the ID of a translation group of the entry id alone.
func groupOf(id string) string { return "tg_" + strings.TrimPrefix(id, "ent_") }

// testSite returns the settings of the fixture, those of 6.11.9.
func testSite() *content.Site {
	return &content.Site{
		Name:            content.Localized{"ja": "株式会社〇〇", "en": "Example Inc."},
		Tagline:         content.Localized{"ja": "Webサイトの企画と制作"},
		URL:             "https://example.com",
		TimeZone:        "Asia/Tokyo",
		Logo:            logoID,
		Icon:            iconID,
		Languages:       []string{"ja", "en"},
		DefaultLanguage: "ja",
		FrontPage:       content.Localized{"ja": homeID},
		PostsPage:       content.Localized{"ja": newsID},
		SEO: content.SiteSEO{
			TitleFormat: content.Localized{"ja": "{title}｜{siteName}", "en": "{title} | {siteName}"},
			OGImage:     content.Localized{"ja": ogImageID},
		},
	}
}

// testSchema is the schema file of the fixture, that of 6.11.9: the URL rule of post, and works and works_category of
// 6.4.4.
const testSchema = `schemaVersion: 1
types:
  post:
    url: /{yyyy}/{mm}/{dd}/{slug}/
  works:
    label: 制作実績
    url: /works/{slug}/
    archive: /works/
    taxonomies: [works_category]
    body: true
    fields:
      client:
        type: text
        label: クライアント名
        required: true
        maxLength: 100
      year:
        type: number
        label: 制作年
        min: 1990
        max: 2100
        translatable: false
      images:
        type: list
        label: 画像
        of: { type: media, accept: [image] }
        maxItems: 20
taxonomies:
  works_category:
    label: 実績の種類
    hierarchical: false
`

// testTheme returns the theme of the fixture: the locations header and footer, and the page templates entry.html,
// list.html and 404.html.
func testTheme() fstest.MapFS {
	return fstest.MapFS{
		"theme.yaml":           {Data: []byte("schemaVersion: 1\nlocations:\n  header: ヘッダー\n  footer: フッター\n")},
		"templates/entry.html": {Data: []byte("<p>entry</p>\n")},
		"templates/list.html":  {Data: []byte("<p>list</p>\n")},
		"templates/404.html":   {Data: []byte("<p>not found</p>\n")},
	}
}

// kindDirs are the directories of the documents by kind in a site directory (internal/sitedir), whose names the files
// of memSource have.
var kindDirs = map[content.RefKind]string{
	content.RefEntry:    "entries",
	content.RefTerm:     "terms",
	content.RefMedia:    "media",
	content.RefAuthor:   "authors",
	content.RefMenu:     "menus",
	content.RefForm:     "forms",
	content.RefRedirect: "redirects",
}

// memSource is a Source in memory, with the names of the files in a site directory. Tests change it with its fields
// and the functions below before they load it.
type memSource struct {
	mu               sync.Mutex
	settings, schema content.SiteFile
	docs             map[content.RefKind]map[string]content.SiteFile
	revisions        map[string]content.SiteFile
	files            fstest.MapFS // the originals by the hex digits of their hashes
	theme            fs.FS
	fail             map[string]error // errors to return by the name of the method, such as "Revision"
	read             []string         // the IDs of the revisions read, in order
	opened           map[string]int   // the calls of OpenMedia, failing ones too, by hash
	posts            int              // posts that addPosts has added
}

var _ Source = (*memSource)(nil)

func (s *memSource) Settings() (settings, schema content.SiteFile, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.fail["Settings"]; err != nil {
		return content.SiteFile{}, content.SiteFile{}, err
	}
	return s.settings, s.schema, nil
}

func (s *memSource) Documents(kind content.RefKind) (map[string]content.SiteFile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.fail["Documents"]; err != nil {
		return nil, err
	}
	if kindDirs[kind] == "" {
		return nil, fmt.Errorf("unsupported kind %q", kind)
	}
	docs := make(map[string]content.SiteFile)
	maps.Copy(docs, s.docs[kind])
	return docs, nil
}

func (s *memSource) Revision(id string) (content.SiteFile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.read = append(s.read, id)
	if err := s.fail["Revision"]; err != nil {
		return content.SiteFile{}, err
	}
	f, ok := s.revisions[id]
	if !ok {
		return content.SiteFile{}, &fs.PathError{Op: "open", Path: "revisions/" + id + ".json", Err: fs.ErrNotExist}
	}
	return f, nil
}

func (s *memSource) OpenMedia(hash string) (fs.File, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.opened[hash]++
	if err := s.fail["OpenMedia"]; err != nil {
		return nil, err
	}
	digits, ok := strings.CutPrefix(hash, "sha256:")
	if !ok {
		return nil, fmt.Errorf("invalid hash %q", hash)
	}
	return s.files.Open(digits)
}

func (s *memSource) Theme() (fs.FS, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.fail["Theme"]; err != nil {
		return nil, err
	}
	return s.theme, nil
}

// indentJSON returns data, a JSON value, indented as in the examples of the concept, with a line break at the end.
func indentJSON(t testing.TB, data []byte) string {
	t.Helper()
	v := jsontext.Value(data)
	if err := v.Indent(jsontext.WithIndent("  ")); err != nil {
		t.Fatalf("Indent(%s) = %v", data, err)
	}
	return string(v) + "\n"
}

// marshalDoc returns v, such as a content.Entry, as its MarshalJSON writes it, indented.
func marshalDoc(t testing.TB, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("Marshal(%#v) = %v", v, err)
	}
	return indentJSON(t, data)
}

// putRaw adds or replaces the document of kind with the ID id, whose content is data.
func (s *memSource) putRaw(kind content.RefKind, id, data string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.docs[kind] == nil {
		s.docs[kind] = make(map[string]content.SiteFile)
	}
	s.docs[kind][id] = content.SiteFile{Name: kindDirs[kind] + "/" + id + ".json", Data: []byte(data)}
}

// put adds or replaces the document of kind with the ID id, with v written by marshalDoc.
func (s *memSource) put(t testing.TB, kind content.RefKind, id string, v any) {
	t.Helper()
	s.putRaw(kind, id, marshalDoc(t, v))
}

// putRevisionRaw adds or replaces the revision with the ID id, whose content is data.
func (s *memSource) putRevisionRaw(id, data string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revisions[id] = content.SiteFile{Name: "revisions/" + id + ".json", Data: []byte(data)}
}

func (s *memSource) putRevision(t testing.TB, id string, r content.Revision) {
	t.Helper()
	s.putRevisionRaw(id, marshalDoc(t, r))
}

// putEntry adds or replaces the entry e and, if it has a published revision, sets that revision to r.
func (s *memSource) putEntry(t testing.TB, e content.Entry, r content.Revision) {
	t.Helper()
	s.put(t, content.RefEntry, e.ID, e)
	if e.PublishedRevision != "" {
		s.putRevision(t, e.PublishedRevision, r)
	}
}

// putSettings replaces the settings document with site, as MarshalSettings writes it, indented.
func (s *memSource) putSettings(t testing.TB, site *content.Site) {
	t.Helper()
	data, err := site.MarshalSettings()
	if err != nil {
		t.Fatalf("MarshalSettings() = %v", err)
	}
	s.settings = content.SiteFile{Name: "site/settings.json", Data: []byte(indentJSON(t, data))}
}

// edit changes the document of kind with the ID id with change, and writes it again with marshalDoc.
func edit[T any, P interface {
	*T
	UnmarshalJSON(data []byte) error
}](t testing.TB, s *memSource, kind content.RefKind, id string, change func(P)) {
	t.Helper()
	var v T
	if err := P(&v).UnmarshalJSON(s.docs[kind][id].Data); err != nil {
		t.Fatalf("decoding the %s %s: %v", kind, id, err)
	}
	change(&v)
	s.put(t, kind, id, v)
}

// editRevision changes the published revision of the entry id with change, and writes it again with marshalDoc.
func (s *memSource) editRevision(t testing.TB, id string, change func(*content.Revision)) {
	t.Helper()
	var e content.Entry
	if err := e.UnmarshalJSON(s.docs[content.RefEntry][id].Data); err != nil {
		t.Fatalf("decoding the entry %s: %v", id, err)
	}
	var r content.Revision
	if err := r.UnmarshalJSON(s.revisions[e.PublishedRevision].Data); err != nil {
		t.Fatalf("decoding the revision of %s: %v", id, err)
	}
	change(&r)
	s.putRevision(t, e.PublishedRevision, r)
}

// newEntry returns an entry of typ in language, published with the revision revisionID(id).
func newEntry(id string, number int, typ, language, group string) content.Entry {
	return content.Entry{
		ID:                id,
		Number:            number,
		Type:              typ,
		Language:          language,
		TranslationGroup:  group,
		Status:            content.StatusPublished,
		PublishedRevision: revisionID(id),
		CreatedAt:         "2026-10-25T10:00:00+09:00",
		CreatedBy:         userID,
	}
}

func richText(s string) content.RichText {
	return content.RichText{{Type: content.InlineText, Text: s}}
}

func paragraph(id, s string) content.Block {
	return content.Block{Type: content.BlockParagraph, ID: id, Text: richText(s)}
}

// addPosts adds n posts published in language by the author of 6.11.4, one a day from 2026-09-01, numbered from
// 1001 in the order of all the posts added, in the category news (ja) or with the tag Events (en), and returns
// their IDs in order.
func (s *memSource) addPosts(t testing.TB, language string, n int) []string {
	t.Helper()
	terms := content.Terms{content.TaxonomyCategory: {newsCategoryID}}
	if language == "en" {
		terms = content.Terms{content.TaxonomyTag: {enEventTagID}}
	}
	ids := make([]string, n)
	for i := range ids {
		s.posts++
		number, ulid := 1000+s.posts, fmt.Sprintf("01JD%022d", s.posts)
		ids[i] = "ent_" + ulid
		date := time.Date(2026, 9, 1+i, 9, 0, 0, 0, time.FixedZone("", 9*60*60))
		s.putEntry(t, newEntry(ids[i], number, content.TypePost, language, "tg_"+ulid), content.Revision{
			Title:  fmt.Sprintf("Post %d", number),
			Slug:   fmt.Sprintf("post-%d", number),
			Date:   date.Format(time.RFC3339),
			Author: authorID,
			Terms:  terms,
			Body:   content.Blocks{paragraph("b_post0001", fmt.Sprintf("Post %d.", number))},
		})
	}
	return ids
}

// testSource returns a site in Japanese and English made of the examples in 6.11 of the concept: the settings of
// 6.11.9, in which ja has the front page home and the posts page news, and en the latest posts at its front; the
// schema with works (testSchema); the pages home, news, about (6.11.1), services, contact (with the form of 6.11.8)
// and privacy in ja, and about and privacy in en as their translations; the post of 6.11.9 and its translation in en
// by the author of 6.11.4, with the terms of 6.11.3; a works entry; a draft page; the media of 6.11.5 and those of the
// settings and the author, without originals; the menus of 6.11.6; the redirect of 6.11.7; and testTheme.
func testSource(t testing.TB) *memSource {
	t.Helper()
	s := &memSource{
		docs:      make(map[content.RefKind]map[string]content.SiteFile),
		revisions: make(map[string]content.SiteFile),
		files:     fstest.MapFS{},
		theme:     testTheme(),
		opened:    make(map[string]int),
	}
	s.putSettings(t, testSite())
	s.schema = content.SiteFile{Name: "site/schema.yaml", Data: []byte(testSchema)}

	s.putEntry(t, newEntry(homeID, 1, content.TypePage, "ja", groupOf(homeID)), content.Revision{
		Title: "ホーム",
		Slug:  "home",
		Body:  content.Blocks{paragraph("b_home0001", "Webサイトの企画と制作の会社です。")},
	})
	s.putEntry(t, newEntry(aboutID, 2, content.TypePage, "ja", aboutGroup), content.Revision{
		Title:    "会社概要",
		Slug:     "about",
		Template: "standard",
		SEO:      content.SEO{Description: "株式会社〇〇の会社概要です。"},
		Body: content.Blocks{
			{Type: content.BlockHeading, ID: "b_4n8c1w2e", Attrs: content.Attrs{Level: 2}, Text: richText("私たちについて")},
			paragraph("b_9x3v7k5m", "当社の事業内容をご紹介します。"),
			{Type: content.BlockImage, ID: "b_2p6t8r1z", Attrs: content.Attrs{Media: imageID, Alt: "会社外観"}},
			{Type: content.BlockTable, ID: "b_5h1j3g7d", Children: content.Blocks{{
				Type: content.BlockTableRow,
				Children: content.Blocks{
					{Type: content.BlockTableCell, Attrs: content.Attrs{Header: true}, Text: richText("所在地")},
					{Type: content.BlockTableCell, Text: richText("東京都千代田区〇〇1-2-3")},
				},
			}}},
		},
	})
	s.putEntry(t, newEntry(servicesID, 3, content.TypePage, "ja", groupOf(servicesID)), content.Revision{
		Title: "サービス",
		Slug:  "services",
		Body:  content.Blocks{paragraph("b_serv0001", "Webサイトの企画、制作と運用をお手伝いします。")},
	})
	s.putEntry(t, newEntry(contactID, 4, content.TypePage, "ja", groupOf(contactID)), content.Revision{
		Title: "お問い合わせ",
		Slug:  "contact",
		Body: content.Blocks{
			paragraph("b_cont0001", "こちらのフォームからお問い合わせください。"),
			{Type: content.BlockForm, ID: "b_cont0002", Attrs: content.Attrs{Form: formID}},
		},
	})
	s.putEntry(t, newEntry(privacyID, 5, content.TypePage, "ja", groupOf(privacyID)), content.Revision{
		Title: "プライバシーポリシー",
		Slug:  "privacy",
		Body:  content.Blocks{paragraph("b_priv0001", "個人情報の取り扱いについて定めます。")},
	})
	s.putEntry(t, newEntry(newsID, 6, content.TypePage, "ja", groupOf(newsID)), content.Revision{
		Title: "お知らせ",
		Slug:  "news",
		Body:  content.Blocks{paragraph("b_news0001", "会社からのお知らせです。")},
	})
	s.putEntry(t, newEntry(enAboutID, 7, content.TypePage, "en", aboutGroup), content.Revision{
		Title: "About us",
		Slug:  "about",
		Body:  content.Blocks{paragraph("b_enab0001", "We plan and make websites.")},
	})
	s.putEntry(t, newEntry(enPrivacyID, 8, content.TypePage, "en", groupOf(privacyID)), content.Revision{
		Title: "Privacy policy",
		Slug:  "privacy",
		Body:  content.Blocks{paragraph("b_enpr0001", "How we handle personal information.")},
	})
	works := newEntry(worksID, 9, "works", "ja", groupOf(worksID))
	works.CreatedAt = "2026-10-20T10:00:00+09:00"
	s.putEntry(t, works, content.Revision{
		Title: "〇〇株式会社のコーポレートサイト",
		Slug:  "example-corporate-site",
		Terms: content.Terms{"works_category": {worksCategoryID}},
		Fields: content.Fields{
			"client": jsontext.Value(`"〇〇株式会社"`),
			"images": jsontext.Value(`["` + imageID + `","` + avatarID + `"]`),
			"year":   jsontext.Value(`2024`),
		},
		Body: content.Blocks{paragraph("b_7k2m9x4p", "コーポレートサイトを全面的に作り直しました。")},
	})
	draft := newEntry(draftID, 10, content.TypePage, "ja", groupOf(draftID))
	draft.Status, draft.PublishedRevision = content.StatusDraft, ""
	s.putEntry(t, draft, content.Revision{})
	post := newEntry(postID, 123, content.TypePost, "ja", postGroup)
	post.CreatedAt = "2026-10-25T18:00:00+09:00"
	s.putEntry(t, post, content.Revision{
		Title:  "新サービスを始めました",
		Slug:   "新サービス",
		Date:   "2026-10-26T00:30:00+09:00",
		Author: authorID,
		Terms:  content.Terms{content.TaxonomyCategory: {pressReleaseID}, content.TaxonomyTag: {eventTagID}},
		Body:   content.Blocks{paragraph("b_3r8w1n6t", "Webサイトの更新と運用をお手伝いするサービスです。")},
	})
	enPost := newEntry(enPostID, 124, content.TypePost, "en", postGroup)
	enPost.CreatedAt = post.CreatedAt
	s.putEntry(t, enPost, content.Revision{
		Title:  "We started a new service",
		Slug:   "new-service",
		Date:   "2026-10-26T00:30:00+09:00",
		Author: authorID,
		Terms:  content.Terms{content.TaxonomyTag: {enEventTagID}},
		Body:   content.Blocks{paragraph("b_3r8w1n6t", "A service to update and run your website.")},
	})

	for _, x := range []content.Term{
		{
			ID:               newsCategoryID,
			Taxonomy:         content.TaxonomyCategory,
			Language:         "ja",
			TranslationGroup: "tg_01JBA4C7J6M9Q2S5V8Y1B4D7F0",
			Name:             "お知らせ",
			Slug:             "news",
			Description:      "会社からのお知らせです。\n新しいサービスやイベントの情報を掲載します。",
		},
		{
			ID:               pressReleaseID,
			Taxonomy:         content.TaxonomyCategory,
			Language:         "ja",
			TranslationGroup: "tg_01JBA4C7K7N1R4T7W1Z4C7E1G4",
			Name:             "プレスリリース",
			Slug:             "press-release",
			Parent:           newsCategoryID,
		},
		{
			ID:               eventTagID,
			Taxonomy:         content.TaxonomyTag,
			Language:         "ja",
			TranslationGroup: "tg_01JBA4C7M8P2S5V9Y2B6D9G3J6",
			Name:             "イベント",
			Slug:             "イベント",
		},
		{
			ID:               enEventTagID,
			Taxonomy:         content.TaxonomyTag,
			Language:         "en",
			TranslationGroup: "tg_01JBA4C7M8P2S5V9Y2B6D9G3J6",
			Name:             "Events",
			Slug:             "events",
		},
		{
			ID:               worksCategoryID,
			Taxonomy:         "works_category",
			Language:         "ja",
			TranslationGroup: "tg_01JB9A00000000000000000001",
			Name:             "コーポレートサイト",
			Slug:             "corporate-site",
		},
	} {
		s.put(t, content.RefTerm, x.ID, x)
	}

	imageDoc := func(id, name, mimeType string, width, height int) content.Media {
		return content.Media{
			ID:         id,
			Hash:       fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(name))),
			FileName:   name,
			MIMEType:   mimeType,
			Size:       width * height / 4,
			Width:      width,
			Height:     height,
			UploadedAt: "2026-10-25T10:00:00+09:00",
			UploadedBy: userID,
		}
	}
	for _, m := range []content.Media{
		{
			ID:         imageID,
			Hash:       "sha256:fa6b1a13e38782a24e72fe6ec39feb7a16bd15ce3e3a9efe8750ff6c8b0c0f01",
			FileName:   "会社外観.jpg",
			MIMEType:   "image/jpeg",
			Size:       482133,
			Width:      1600,
			Height:     1067,
			Alt:        content.Localized{"en": "Our office building", "ja": "会社外観"},
			Caption:    content.Localized{"ja": "本社の外観（2024年撮影）"},
			UploadedAt: "2026-10-25T10:00:00+09:00",
			UploadedBy: userID,
		},
		{
			ID:         pdfID,
			Hash:       "sha256:7ba5e322e1c7f0ed36cbdcca013f17f25ef7f3877393833c2b4372731112dccd",
			FileName:   "会社案内.pdf",
			MIMEType:   "application/pdf",
			Size:       2345678,
			UploadedAt: "2026-10-25T10:05:00+09:00",
			UploadedBy: userID,
		},
		imageDoc(avatarID, "yamada.png", "image/png", 256, 256),
		imageDoc(logoID, "logo.png", "image/png", 400, 100),
		imageDoc(iconID, "icon.png", "image/png", 512, 512),
		imageDoc(ogImageID, "ogp.jpg", "image/jpeg", 1200, 630),
	} {
		s.put(t, content.RefMedia, m.ID, m)
	}

	s.put(t, content.RefAuthor, authorID, content.AuthorProfile{
		ID:   authorID,
		Name: "山田 太郎",
		Slug: "yamada",
		Bio: content.Localized{
			"en": "Editor in chief. Writes about web design and accessibility.",
			"ja": "編集長。Webデザインとアクセシビリティについて書いています。\n著書に『はじめてのWebアクセシビリティ』があります。",
		},
		Avatar: avatarID,
		Links:  []string{"https://x.com/example", "https://example.com/"},
	})

	s.put(t, content.RefMenu, headerID, content.Menu{
		ID:       headerID,
		Title:    "ヘッダー",
		Location: "header",
		Language: "ja",
		Items: []content.MenuItem{
			{Label: "会社概要", Entry: aboutID},
			{Label: "サービス", Children: []content.MenuItem{
				{Label: "サービス一覧", Entry: servicesID},
				{Label: "制作実績", Href: "/works/"},
			}},
			{Term: newsCategoryID, Children: []content.MenuItem{{Term: pressReleaseID}}},
			{Label: "採用情報", Href: "https://recruit.example.com/", NewTab: true},
		},
	})
	s.put(t, content.RefMenu, footerID, content.Menu{
		ID:       footerID,
		Title:    "フッター",
		Location: "footer",
		Language: "ja",
		Items: []content.MenuItem{
			{Label: "お問い合わせ", Entry: contactID},
			{Label: "プライバシーポリシー", Entry: privacyID},
			{Label: "English", Entry: enAboutID},
		},
	})

	label := func(en, ja string) content.Localized { return content.Localized{"en": en, "ja": ja} }
	s.put(t, content.RefForm, formID, content.Form{
		ID:    formID,
		Title: "お問い合わせ",
		Fields: []content.FormField{
			{Name: "your-name", Type: content.FormText, Label: label("Name", "お名前"), Required: true, MaxLength: 100},
			{Name: "your-email", Type: content.FormEmail, Label: label("Email", "メールアドレス"), Required: true},
			{Name: "your-tel", Type: content.FormTel, Label: label("Phone", "電話番号")},
			{
				Name:     "topic",
				Type:     content.FormRadio,
				Label:    label("Topic", "ご用件"),
				Required: true,
				Options: []content.FormOption{
					{Value: "quote", Label: label("Quote", "お見積もり")},
					{Value: "recruit", Label: label("Careers", "採用について")},
					{Value: "other", Label: label("Other", "その他")},
				},
			},
			{Name: "your-message", Type: content.FormTextarea, Label: label("Message", "お問い合わせ内容"), Required: true, MaxLength: 2000},
			{
				Name:   "privacy",
				Type:   content.FormConsent,
				Label:  label("I agree to the handling of my personal information", "個人情報の取り扱いに同意する"),
				Policy: content.Localized{"en": enPrivacyID, "ja": privacyID},
			},
		},
		SubmitLabel: label("Send", "送信"),
		Message: label(
			"Thank you for contacting us.\nWe will reply within two business days.",
			"お問い合わせありがとうございます。\n2営業日以内にご返信します。",
		),
		Notify: []string{"info@example.com"},
	})

	s.put(t, content.RefRedirect, redirectID, content.Redirect{ID: redirectID, From: "/?p=123", To: postID, Status: 301})
	return s
}

// writeSiteDir writes the files of s into a new directory in the layout of internal/sitedir, and returns it.
func writeSiteDir(t *testing.T, s *memSource) string {
	t.Helper()
	dir := t.TempDir()
	write := func(name string, data []byte) {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(s.settings.Name, s.settings.Data)
	write(s.schema.Name, s.schema.Data)
	for _, docs := range s.docs {
		for _, f := range docs {
			write(f.Name, f.Data)
		}
	}
	for _, f := range s.revisions {
		write(f.Name, f.Data)
	}
	for digits, f := range s.files {
		write("files/"+digits, f.Data)
	}
	err := fs.WalkDir(s.theme, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(s.theme, name)
		if err == nil {
			write("theme/"+name, data)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return dir
}
