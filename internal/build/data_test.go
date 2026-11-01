package build

import (
	"encoding/json/jsontext"
	"html/template"
	"maps"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/imaging"
	"github.com/WhiteRoomLab/whiteroom/internal/mediafile"
	"github.com/WhiteRoomLab/whiteroom/internal/render"
	"github.com/WhiteRoomLab/whiteroom/internal/theme"
)

// productType is the definition of a type with a field of every type, for the schema file.
const productType = `  product:
    label: 製品
    url: /products/{slug}/
    taxonomies: [works_category]
    body: true
    fields:
      name: {type: text}
      notes: {type: textarea}
      intro: {type: richText}
      details: {type: blocks}
      price: {type: number}
      onSale: {type: boolean}
      released: {type: date}
      updated: {type: datetime}
      color: {type: select, options: [red, blue]}
      sizes: {type: select, options: [s, m, l], multiple: true}
      photo: {type: media}
      manual: {type: media, accept: [pdf]}
      link: {type: link}
      related: {type: entry}
      seeAlso: {type: entry, multiple: true}
      category: {type: term, taxonomy: works_category}
      tags: {type: term, taxonomy: works_category, multiple: true}
      spec:
        type: group
        fields:
          weight: {type: number}
          note: {type: richText}
          site: {type: link}
      variants:
        type: list
        of:
          type: group
          fields:
            name: {type: text}
            image: {type: media, accept: [image]}
            description: {type: richText}
            link: {type: link}
`

// The products of productSource.
var (
	productID      = newID("ent", 1) // with a value in every field
	emptyProductID = newID("ent", 2) // without values
)

// Percent-encoded names of the files of media.
const (
	officeName = "%E4%BC%9A%E7%A4%BE%E5%A4%96%E8%A6%B3" // 会社外観
	guideName  = "%E4%BC%9A%E7%A4%BE%E6%A1%88%E5%86%85" // 会社案内
)

// productSource returns the fixture with the type product (productType) and its entries productID and emptyProductID
// in ja, and with originals: a JPEG of 1200x800 pixels for imageID, whose alt in ja is 会社外観; a PDF for pdfID; and a
// PNG of 256x256 pixels for avatarID.
func productSource(t *testing.T) *memSource {
	t.Helper()
	src := testSource(t)
	src.schema.Data = []byte(strings.Replace(testSchema, "\ntaxonomies:\n", "\n"+productType+"taxonomies:\n", 1))
	src.putOriginal(t, imageID, "会社外観.jpg", testJPEG(t, 1200, 800))
	edit(t, src, content.RefMedia, imageID, func(m *content.Media) { m.Alt = content.Localized{"ja": "会社外観"} })
	src.putOriginal(t, pdfID, "会社案内.pdf", []byte(testPDF))
	src.putOriginal(t, avatarID, "yamada.png", testPNG(t, 256, 256))
	richText := func(s string) jsontext.Value { return jsontext.Value(`[{"type":"text","text":"` + s + `"}]`) }
	src.putEntry(t, newEntry(productID, 31, "product", "ja", groupOf(productID)), content.Revision{
		Title:         "製品A",
		Slug:          "product-a",
		FeaturedImage: imageID,
		Terms:         content.Terms{"works_category": {worksCategoryID}},
		Fields: content.Fields{
			"name":     jsontext.Value(`"ウィジェット"`),
			"notes":    jsontext.Value(`"1行目\n2行目"`),
			"intro":    richText("はじめに"),
			"details":  jsontext.Value(`[{"type":"paragraph","id":"b_prod0002","content":` + string(richText("詳細")) + `}]`),
			"price":    jsontext.Value(`1980.5`),
			"onSale":   jsontext.Value(`true`),
			"released": jsontext.Value(`"2026-10-01"`),
			"updated":  jsontext.Value(`"2026-10-26T00:30:00+09:00"`),
			"color":    jsontext.Value(`"blue"`),
			"sizes":    jsontext.Value(`["m","l"]`),
			"photo":    jsontext.Value(`"` + imageID + `"`),
			"manual":   jsontext.Value(`"` + pdfID + `"`),
			"link":     jsontext.Value(`{"entry":"` + draftID + `"}`),
			"related":  jsontext.Value(`"` + aboutID + `"`),
			"seeAlso":  jsontext.Value(`["` + servicesID + `","` + draftID + `"]`),
			"category": jsontext.Value(`"` + worksCategoryID + `"`),
			"tags":     jsontext.Value(`["` + worksCategoryID + `"]`),
			"spec": jsontext.Value(`{"weight":2.5,"note":` + string(richText("注記")) +
				`,"site":{"href":"/about/","newTab":true}}`),
			"variants": jsontext.Value(`[{"name":"小","link":{"entry":"` + draftID + `"}},` +
				`{"name":"大","image":"` + imageID + `","description":` + string(richText("大きいサイズ")) + `}]`),
		},
		Body: content.Blocks{paragraph("b_prod0001", "製品Aの説明です。")},
	})
	src.putEntry(t, newEntry(emptyProductID, 32, "product", "ja", groupOf(emptyProductID)), content.Revision{
		Title:   "製品B",
		Slug:    "product-b",
		Excerpt: "手書きの\n抜粋です。",
	})
	return src
}

// testRenderer returns the renderer of the pages of b in language, which asks b.res about what content refers to.
func testRenderer(t testing.TB, b *builder, language string) *render.Renderer {
	t.Helper()
	tr, err := b.theme.Renderer(language, b.base)
	if err != nil {
		t.Fatal(err)
	}
	return render.New(tr, b.res)
}

// warningTexts returns the Error() of each warning of b.
func warningTexts(b *builder) []string {
	var texts []string
	for _, e := range b.warnings.sorted() {
		texts = append(texts, e.Error())
	}
	return texts
}

// checkFields checks the values of the fields of e, by name.
func checkFields(t *testing.T, e *Entry, want map[string]any) {
	t.Helper()
	for _, name := range slices.Sorted(maps.Keys(want)) {
		if got, ok := e.Fields[name]; !ok || !reflect.DeepEqual(got, want[name]) {
			t.Errorf("%s: Fields[%q] = %#v, want %#v", e.ID, name, got, want[name])
		}
	}
	for name := range e.Fields {
		if _, ok := want[name]; !ok {
			t.Errorf("%s: Fields[%q] = %#v, want no such field", e.ID, name, e.Fields[name])
		}
	}
}

// TestEntryData checks the data of entries with a value in every type of field and without values, on their pages and
// in lists.
func TestEntryData(t *testing.T) {
	src := productSource(t)
	b := mustLoad(t, src)
	newTestResolver(t, b)
	r := testRenderer(t, b, "ja")

	photo := b.media[imageID].v
	jpegFile := func(w, h int) imaging.Variant { return imaging.Variant{Width: w, Height: h, Format: mediafile.JPEG} }
	office := func(suffix string, v imaging.Variant) string {
		return "/media/" + imageID + "/" + officeName + suffix + ".jpg?v=" + imaging.Version(photo.Hash, v)
	}
	officeSrc := office("", jpegFile(1200, 800))
	photoImage := &render.PageImage{
		Src: template.URL(officeSrc),
		Srcset: template.Srcset(office("-640x427", jpegFile(640, 427)) + " 640w, " +
			office("-960x640", jpegFile(960, 640)) + " 960w, " + officeSrc + " 1200w"),
		Sizes: "(max-width: 1200px) 100vw, 1200px",
		Width: 1200, Height: 800, Alt: "会社外観", HasAlt: true,
	}
	photoMedia := &Media{
		URL: template.URL(officeSrc), Kind: content.MediaImage, FileName: "会社外観.jpg", MIMEType: "image/jpeg",
		Size: photo.Size, Image: photoImage,
	}
	guide := b.media[pdfID].v
	corporate := TermLink{Name: "コーポレートサイト", URL: "/works_category/corporate-site/"}

	html := map[string]template.HTML{
		"/body":                          "<p>本文</p>",
		"/fields/intro":                  "はじめに",
		"/fields/details":                "<p>詳細</p>",
		"/fields/spec/note":              "注記",
		"/fields/variants/1/description": "大きいサイズ",
	}
	got := b.entryData(b.published(productID), r, html)
	fields := map[string]any{
		"name":     "ウィジェット",
		"notes":    "1行目\n2行目",
		"intro":    template.HTML("はじめに"),
		"details":  template.HTML("<p>詳細</p>"),
		"price":    1980.5,
		"onSale":   true,
		"released": &Date{Datetime: "2026-10-01", Year: 2026, Month: 10, Day: 1},
		"updated":  &Date{Datetime: "2026-10-26T00:30:00+09:00", Year: 2026, Month: 10, Day: 26, Minute: 30},
		"color":    "blue",
		"sizes":    []string{"m", "l"},
		"photo":    photoMedia,
		"manual": &Media{
			URL:  template.URL("/media/" + pdfID + "/" + guideName + ".pdf?v=" + imaging.PDFVersion(guide.Hash)),
			Kind: content.MediaPDF, FileName: "会社案内.pdf", MIMEType: "application/pdf", Size: len(testPDF),
		},
		"link":     nil,
		"related":  &EntryLink{Title: "会社概要", URL: "/about/"},
		"seeAlso":  []EntryLink{{Title: "サービス", URL: "/services/"}, {}},
		"category": &corporate,
		"tags":     []TermLink{corporate},
		"spec": map[string]any{
			"weight": 2.5,
			"note":   template.HTML("注記"),
			"site":   &render.Link{Href: "/about/", Target: "_blank", Rel: "noopener"},
		},
		"variants": []any{
			map[string]any{"name": "小", "image": nil, "description": template.HTML(""), "link": nil},
			map[string]any{"name": "大", "image": photoMedia, "description": template.HTML("大きいサイズ"), "link": nil},
		},
	}
	checkFields(t, got, fields)
	want := &Entry{
		ID:            productID,
		Type:          "product",
		Title:         "製品A",
		URL:           "/products/product-a/",
		Excerpt:       "製品Aの説明です。",
		Terms:         map[string][]TermLink{"works_category": {corporate}},
		FeaturedImage: photoImage,
		Body:          "<p>本文</p>",
		Fields:        fields,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("entryData(%s) = %+v, want %+v", productID, got, want)
	}
	unpublished := `entry "` + draftID + `" is not published; rendered without the link`
	wantWarnings := []string{
		revisionFile(productID) + ":32:5: /fields/link: " + unpublished,
		revisionFile(productID) + ":71:9: /fields/variants/0/link: " + unpublished,
	}
	if got := warningTexts(b); !slices.Equal(got, wantWarnings) {
		t.Errorf("warnings:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(wantWarnings, "\n"))
	}

	// In lists: without the HTML.
	summary := b.entryData(b.published(productID), r, nil)
	maps.Copy(fields, map[string]any{"intro": template.HTML(""), "details": template.HTML("")})
	fields["spec"].(map[string]any)["note"] = template.HTML("")
	fields["variants"].([]any)[1].(map[string]any)["description"] = template.HTML("")
	want.Body = ""
	if !reflect.DeepEqual(summary, want) {
		t.Errorf("entryData(%s) without HTML = %+v, want %+v", productID, summary, want)
	}

	empty := b.entryData(b.published(emptyProductID), r, nil)
	wantEmpty := &Entry{
		ID:      emptyProductID,
		Type:    "product",
		Title:   "製品B",
		URL:     "/products/product-b/",
		Excerpt: "手書きの\n抜粋です。",
		Terms:   map[string][]TermLink{"works_category": nil},
		Fields: map[string]any{
			"name":     "",
			"notes":    "",
			"intro":    template.HTML(""),
			"details":  template.HTML(""),
			"price":    nil,
			"onSale":   false,
			"released": nil,
			"updated":  nil,
			"color":    "",
			"sizes":    []string(nil),
			"photo":    nil,
			"manual":   nil,
			"link":     nil,
			"related":  nil,
			"seeAlso":  []EntryLink(nil),
			"category": nil,
			"tags":     []TermLink(nil),
			"spec":     map[string]any{"weight": nil, "note": template.HTML(""), "site": nil},
			"variants": []any(nil),
		},
	}
	checkFields(t, empty, wantEmpty.Fields)
	if !reflect.DeepEqual(empty, wantEmpty) {
		t.Errorf("entryData(%s) = %+v, want %+v", emptyProductID, empty, wantEmpty)
	}

	// Other types: works with a list of media, and a page without terms and fields.
	works := b.entryData(b.published(worksID), r, nil)
	avatar := b.media[avatarID].v
	avatarSrc := template.URL("/media/" + avatarID + "/yamada.png?v=" +
		imaging.Version(avatar.Hash, imaging.Variant{Width: 256, Height: 256, Format: mediafile.PNG}))
	checkFields(t, works, map[string]any{
		"client": "〇〇株式会社",
		"year":   2024.0,
		"images": []any{
			photoMedia,
			&Media{
				URL: avatarSrc, Kind: content.MediaImage, FileName: "yamada.png", MIMEType: "image/png", Size: avatar.Size,
				Image: &render.PageImage{Src: avatarSrc, Width: 256, Height: 256},
			},
		},
	})
	if !reflect.DeepEqual(works.Terms, map[string][]TermLink{"works_category": {corporate}}) || works.Date != nil ||
		works.Author != nil || works.Excerpt != "コーポレートサイトを全面的に作り直しました。" {
		t.Errorf("entryData(%s) = %+v", worksID, works)
	}
	about := b.entryData(b.published(aboutID), r, nil)
	if about.Terms == nil || len(about.Terms) != 0 || about.Fields == nil || len(about.Fields) != 0 ||
		about.Excerpt != "私たちについて 当社の事業内容をご紹介します。 所在地 東京都千代田区〇〇1-2-3" {
		t.Errorf("entryData(%s) = %+v", aboutID, about)
	}

	// Media that the resolver does not find: an image without its original, which the build reports later, and a
	// medium that does not exist.
	x := fieldData{b: b, d: b.published(productID), r: r}
	media := &content.FieldDef{Name: "photo", Type: content.FieldMedia}
	for _, id := range []string{logoID, missingMediaID} {
		if got := x.value(media, jsontext.Value(`"`+id+`"`), "/fields/photo"); got != nil {
			t.Errorf("value() of the medium %s = %#v, want nil", id, got)
		}
	}
}

func TestDecodeValue(t *testing.T) {
	if s, ok := decodeValue[string](jsontext.Value(`"a\nb"`)); s != "a\nb" || !ok {
		t.Errorf(`decodeValue[string]("a\nb") = %q, %v`, s, ok)
	}
	for _, v := range []jsontext.Value{nil, jsontext.Value(`1`), jsontext.Value(`["a"]`)} {
		if s, ok := decodeValue[string](v); s != "" || ok {
			t.Errorf("decodeValue[string](%s) = %q, %v, want \"\", false", v, s, ok)
		}
	}
	if ss, ok := decodeValue[[]string](jsontext.Value(`["a","b"]`)); !slices.Equal(ss, []string{"a", "b"}) || !ok {
		t.Errorf(`decodeValue[[]string](["a","b"]) = %q, %v`, ss, ok)
	}
	if ss, ok := decodeValue[[]string](jsontext.Value(`["a",1]`)); ss != nil || ok {
		t.Errorf(`decodeValue[[]string](["a",1]) = %q, %v, want nil, false`, ss, ok)
	}
	if n, ok := decodeValue[float64](jsontext.Value(`1e400`)); n != 0 || ok {
		t.Errorf("decodeValue[float64](1e400) = %v, %v, want 0, false", n, ok)
	}
}

// TestEntryDataInTemplates checks how html/template writes the data of entries for the page templates: the URLs as
// they are, the fields without values skipped by with and range, and an error for a field that the type lacks.
func TestEntryDataInTemplates(t *testing.T) {
	const text = `<a href="{{.URL}}">{{.Title}}</a>
{{with .FeaturedImage}}<img src="{{.Src}}" srcset="{{.Srcset}}" width="{{.Width}}"{{if .HasAlt}} alt="{{.Alt}}"{{end}}>` +
		`{{else}}no image{{end}}
{{range $taxonomy, $terms := .Terms}}{{$taxonomy}}:{{range $terms}} <a href="{{.URL}}">{{.Name}}</a>{{end}}{{end}}
{{$f := .Fields}}[{{$f.name}}] [{{$f.price}}] {{$f.onSale}} {{len $f.sizes}} ` +
		`{{with $f.released}}{{.Year}}/{{.Month}}/{{.Day}}{{else}}no date{{end}}
{{with $f.manual}}<a href="{{.URL}}">{{.FileName}}</a>{{else}}no manual{{end}}
{{with $f.link}}<a href="{{.Href}}">link</a>{{else}}no link{{end}}
{{range $f.seeAlso}}[{{.Title}}|{{.URL}}]{{else}}no entries{{end}}
{{range $f.variants}}{{.name}}{{with .image}} <img src="{{.Image.Src}}">{{end}};{{else}}no variants{{end}}
{{with $f.spec.weight}}{{.}}{{else}}no weight{{end}} ` +
		`{{with $f.spec.site}}<a href="{{.Href}}" target="{{.Target}}" rel="{{.Rel}}">site</a>{{else}}no site{{end}}`
	tmpl := template.Must(template.New("entry").Option("missingkey=error").Parse(text))
	src := productSource(t)
	b := mustLoad(t, src)
	newTestResolver(t, b)
	r := testRenderer(t, b, "ja")
	photo := b.media[imageID].v
	office := func(suffix string, w, h int) string {
		return "/media/" + imageID + "/" + officeName + suffix + ".jpg?v=" +
			imaging.Version(photo.Hash, imaging.Variant{Width: w, Height: h, Format: mediafile.JPEG})
	}
	officeSrc := office("", 1200, 800)
	pdf := "/media/" + pdfID + "/" + guideName + ".pdf?v=" + imaging.PDFVersion(b.media[pdfID].v.Hash)
	for _, tt := range []struct {
		id   string
		want string
	}{
		{
			productID,
			`<a href="/products/product-a/">製品A</a>
<img src="` + officeSrc + `" srcset="` + office("-640x427", 640, 427) + ` 640w, ` + office("-960x640", 960, 640) +
				` 960w, ` + officeSrc + ` 1200w" width="1200" alt="会社外観">
works_category: <a href="/works_category/corporate-site/">コーポレートサイト</a>
[ウィジェット] [1980.5] true 2 2026/10/1
<a href="` + pdf + `">会社案内.pdf</a>
no link
[サービス|/services/][|]
小;大 <img src="` + officeSrc + `">;
2.5 <a href="/about/" target="_blank" rel="noopener">site</a>`,
		},
		{
			emptyProductID,
			`<a href="/products/product-b/">製品B</a>
no image
works_category:
[] [] false 0 no date
no manual
no link
no entries
no variants
no weight no site`,
		},
	} {
		var out strings.Builder
		if err := tmpl.Execute(&out, b.entryData(b.published(tt.id), r, nil)); err != nil {
			t.Fatalf("Execute(%s) error: %v", tt.id, err)
		}
		if out.String() != tt.want {
			t.Errorf("Execute(%s):\n%s\nwant:\n%s", tt.id, out.String(), tt.want)
		}
	}
	missing := template.Must(template.New("missing").Option("missingkey=error").Parse("{{.Fields.year}}"))
	err := missing.Execute(&strings.Builder{}, b.entryData(b.published(productID), r, nil))
	if err == nil || !strings.Contains(err.Error(), `map has no entry for key "year"`) {
		t.Errorf("Execute() of a field that the type lacks: error %v, want one for the key", err)
	}
}

// TestEntryDataConcurrent checks that entryData can make the data of entries at once, as pages are rendered, with the
// results of making them one at a time.
func TestEntryDataConcurrent(t *testing.T) {
	src := productSource(t)
	src.addPosts(t, "ja", 11)
	b := mustLoad(t, src)
	newTestResolver(t, b)
	r := testRenderer(t, b, "ja")
	var ds []*entryDoc
	for _, id := range slices.Sorted(maps.Keys(b.entries)) {
		if d := b.published(id); d != nil && d.e.Language == "ja" {
			ds = append(ds, d)
		}
	}
	got := make([][]*Entry, 4)
	var wg sync.WaitGroup
	for i := range got {
		wg.Go(func() {
			for _, d := range ds {
				got[i] = append(got[i], b.entryData(d, r, map[string]template.HTML{"/body": "B"}))
			}
		})
	}
	wg.Wait()
	for i, d := range ds {
		want := b.entryData(d, r, map[string]template.HTML{"/body": "B"})
		for _, es := range got {
			if !reflect.DeepEqual(es[i], want) {
				t.Errorf("entryData(%s) = %+v, want %+v", d.e.ID, es[i], want)
			}
		}
	}
}

func TestKindName(t *testing.T) {
	for k, want := range map[theme.Kind]string{
		theme.KindEntry:    "entry",
		theme.KindPosts:    "posts",
		theme.KindTerm:     "term",
		theme.KindArchive:  "archive",
		theme.KindAuthor:   "author",
		theme.KindNotFound: "notFound",
		theme.Kind(-1):     "",
	} {
		if got := kindName(k); got != want {
			t.Errorf("kindName(%d) = %q, want %q", k, got, want)
		}
	}
}

// TestPostData checks the data of posts, their authors and their terms in two languages, with the time zone and the
// path of the site.
func TestPostData(t *testing.T) {
	const (
		jaPost = "/2026/10/26/%E6%96%B0%E3%82%B5%E3%83%BC%E3%83%93%E3%82%B9/" // /2026/10/26/新サービス/
		event  = "/tag/%E3%82%A4%E3%83%99%E3%83%B3%E3%83%88/"                 // /tag/イベント/
		jaBio  = "編集長。Webデザインとアクセシビリティについて書いています。\n著書に『はじめてのWebアクセシビリティ』があります。"
	)
	links := []template.URL{"https://x.com/example", "https://example.com/", "https://example.com/%E8%91%97%E8%80%85/"}
	tests := []struct {
		name     string
		timeZone string
		base     string // the path of the site URL
		ja, en   *Date  // of the posts in ja and en
		updated  *Date  // of the datetime field of productID
	}{
		{
			name:     "Asia/Tokyo",
			timeZone: "Asia/Tokyo",
			ja:       &Date{Datetime: "2026-10-26T00:30:00+09:00", Year: 2026, Month: 10, Day: 26, Minute: 30},
			en:       &Date{Datetime: "2026-10-26T00:30:00+09:00", Year: 2026, Month: 10, Day: 26, Minute: 30},
			updated:  &Date{Datetime: "2026-10-26T00:30:00+09:00", Year: 2026, Month: 10, Day: 26, Minute: 30},
		},
		{
			name:     "UTC, under a path",
			timeZone: "UTC",
			base:     "/blog",
			ja:       &Date{Datetime: "2026-10-25T15:30:00Z", Year: 2026, Month: 10, Day: 25, Hour: 15, Minute: 30},
			en:       &Date{Datetime: "2026-10-25T15:30:00Z", Year: 2026, Month: 10, Day: 25, Hour: 15, Minute: 30},
			updated:  &Date{Datetime: "2026-10-25T15:30:00Z", Year: 2026, Month: 10, Day: 25, Hour: 15, Minute: 30},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := productSource(t)
			site := testSite()
			site.TimeZone = tt.timeZone
			site.URL = "https://example.com" + tt.base
			src.putSettings(t, site)
			edit(t, src, content.RefAuthor, authorID, func(a *content.AuthorProfile) {
				a.Links = append(a.Links, "https://example.com/著者/")
			})
			src.editRevision(t, postID, func(r *content.Revision) { r.Sticky = true })
			b := mustLoad(t, src)
			newTestResolver(t, b)
			avatar := b.media[avatarID].v
			avatarImage := &render.PageImage{
				Src: template.URL(tt.base + "/media/" + avatarID + "/yamada.png?v=" +
					imaging.Version(avatar.Hash, imaging.Variant{Width: 256, Height: 256, Format: mediafile.PNG})),
				Width:  256,
				Height: 256,
			}
			base := template.URL(tt.base)

			ja := b.entryData(b.published(postID), testRenderer(t, b, "ja"), map[string]template.HTML{"/body": "B"})
			want := &Entry{
				ID:      postID,
				Type:    content.TypePost,
				Title:   "新サービスを始めました",
				URL:     base + jaPost,
				Excerpt: "Webサイトの更新と運用をお手伝いするサービスです。",
				Date:    tt.ja,
				Author: &Author{
					Name: "山田 太郎", URL: base + "/author/yamada/", Bio: jaBio, Avatar: avatarImage, Links: links,
				},
				Terms: map[string][]TermLink{
					content.TaxonomyCategory: {{Name: "プレスリリース", URL: base + "/category/news/press-release/"}},
					content.TaxonomyTag:      {{Name: "イベント", URL: base + event}},
				},
				Sticky: true,
				Body:   "B",
				Fields: map[string]any{},
			}
			if !reflect.DeepEqual(ja, want) {
				t.Errorf("entryData(%s) = %+v, want %+v", postID, ja, want)
			}

			en := b.entryData(b.published(enPostID), testRenderer(t, b, "en"), nil)
			want = &Entry{
				ID:      enPostID,
				Type:    content.TypePost,
				Title:   "We started a new service",
				URL:     base + "/en/2026/10/26/new-service/",
				Excerpt: "A service to update and run your website.",
				Date:    tt.en,
				Author: &Author{
					Name: "山田 太郎", URL: base + "/en/author/yamada/",
					Bio:    "Editor in chief. Writes about web design and accessibility.",
					Avatar: avatarImage, Links: links,
				},
				Terms: map[string][]TermLink{
					content.TaxonomyCategory: nil,
					content.TaxonomyTag:      {{Name: "Events", URL: base + "/en/tag/events/"}},
				},
				Fields: map[string]any{},
			}
			if !reflect.DeepEqual(en, want) {
				t.Errorf("entryData(%s) = %+v, want %+v", enPostID, en, want)
			}

			product := b.entryData(b.published(productID), testRenderer(t, b, "ja"), nil)
			for name, want := range map[string]any{
				"updated": tt.updated,
				"related": &EntryLink{Title: "会社概要", URL: base + "/about/"},
				"seeAlso": []EntryLink{{Title: "サービス", URL: base + "/services/"}, {}},
			} {
				if got := product.Fields[name]; !reflect.DeepEqual(got, want) {
					t.Errorf("Fields[%q] = %+v, want %+v", name, got, want)
				}
			}
		})
	}
}

// TestTermData checks the data of terms on their lists.
func TestTermData(t *testing.T) {
	src := testSource(t)
	b := mustLoad(t, src)
	newTestResolver(t, b)
	for _, tt := range []struct {
		id, language string
		want         *Term
	}{
		{
			newsCategoryID, "ja",
			&Term{
				Name: "お知らせ", Description: "会社からのお知らせです。\n新しいサービスやイベントの情報を掲載します。",
				URL: "/category/news/", Taxonomy: content.TaxonomyCategory,
			},
		},
		{
			pressReleaseID, "ja",
			&Term{
				Name: "プレスリリース", URL: "/category/news/press-release/", Taxonomy: content.TaxonomyCategory,
				Parent: &TermLink{Name: "お知らせ", URL: "/category/news/"},
			},
		},
		{enEventTagID, "en", &Term{Name: "Events", URL: "/en/tag/events/", Taxonomy: content.TaxonomyTag}},
	} {
		if got := b.termData(tt.id, testRenderer(t, b, tt.language)); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("termData(%s) = %+v, want %+v", tt.id, got, tt.want)
		}
	}
}

func TestNewDate(t *testing.T) {
	tokyo, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		s    string
		loc  *time.Location
		want *Date
	}{
		{"2026-10-26T00:30:00+09:00", tokyo, &Date{"2026-10-26T00:30:00+09:00", 2026, 10, 26, 0, 30}},
		{"2026-10-26T00:30:00+09:00", time.UTC, &Date{"2026-10-25T15:30:00Z", 2026, 10, 25, 15, 30}},
		{"2026-10-25T15:30:45Z", tokyo, &Date{"2026-10-26T00:30:45+09:00", 2026, 10, 26, 0, 30}},
		{"2026-12-31T23:59:59-10:00", tokyo, &Date{"2027-01-01T18:59:59+09:00", 2027, 1, 1, 18, 59}},
		// Before and after the start of the daylight saving time.
		{"2026-03-08T06:59:00Z", newYork, &Date{"2026-03-08T01:59:00-05:00", 2026, 3, 8, 1, 59}},
		{"2026-03-08T07:00:00Z", newYork, &Date{"2026-03-08T03:00:00-04:00", 2026, 3, 8, 3, 0}},
		{"2026-10-26", tokyo, nil},
		{"", tokyo, nil},
	}
	for _, tt := range tests {
		if got := newDate(tt.s, tt.loc); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("newDate(%q, %s) = %+v, want %+v", tt.s, tt.loc, got, tt.want)
		}
	}
	for _, tt := range []struct {
		s    string
		want *Date
	}{
		{"2026-10-25", &Date{Datetime: "2026-10-25", Year: 2026, Month: 10, Day: 25}},
		{"2028-02-29", &Date{Datetime: "2028-02-29", Year: 2028, Month: 2, Day: 29}},
		{"2026-02-29", nil},
		{"2026-10-25T00:00:00Z", nil},
		{"", nil},
	} {
		if got := newDay(tt.s); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("newDay(%q) = %+v, want %+v", tt.s, got, tt.want)
		}
	}
}
