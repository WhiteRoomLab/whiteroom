package wxr

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata")

const (
	nsDecl = `xmlns:excerpt="http://wordpress.org/export/1.2/excerpt/" xmlns:content="http://purl.org/rss/1.0/modules/content/" ` +
		`xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:wp="http://wordpress.org/export/1.2/"`
	head = `<rss version="2.0" ` + nsDecl + ">\n<channel>\n<wp:wxr_version>1.2</wp:wxr_version>\n"
	tail = "</channel>\n</rss>\n"

	minimalItem = "<wp:post_id>1</wp:post_id>\n<wp:post_type>post</wp:post_type>\n"
)

// wxrDoc returns a WXR 1.2 whose channel has body after wp:wxr_version, which is on the third line.
func wxrDoc(body string) string {
	return head + body + tail
}

// itemDoc returns a WXR 1.2 with one item that has body.
func itemDoc(body string) string {
	return wxrDoc("<item>\n" + body + "</item>\n")
}

func read(t *testing.T, in string) *Export {
	t.Helper()
	ex, err := Read(context.Background(), strings.NewReader(in), Options{})
	if err != nil {
		t.Fatalf("Read() = %v", err)
	}
	return ex
}

func readSample(t *testing.T, name string) *Export {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	ex, err := Read(context.Background(), f, Options{})
	if err != nil {
		t.Fatalf("Read(%s) = %v", name, err)
	}
	return ex
}

// posAt returns the place of the byte at idx in s, counting lines by LF and columns by characters, without the byte
// order mark.
func posAt(s string, idx int) Pos {
	s = strings.TrimPrefix(s, "\xef\xbb\xbf")
	pos := Pos{1, 1}
	for _, r := range s[:min(idx, len(s))] {
		if r == '\n' {
			pos = Pos{pos.Line + 1, 1}
		} else {
			pos.Column++
		}
	}
	return pos
}

// place returns the place of the nth occurrence of sub in s, counting from 1.
func place(t testing.TB, s, sub string, nth int) Pos {
	t.Helper()
	s = strings.TrimPrefix(s, "\xef\xbb\xbf")
	idx := -1
	for i := 0; i < nth; i++ {
		j := strings.Index(s[idx+1:], sub)
		if j < 0 {
			t.Fatalf("%q does not occur %d times in %q", sub, nth, s)
		}
		idx += 1 + j
	}
	return posAt(s, idx)
}

// firstDiff describes where got and want differ.
func firstDiff(got, want []byte) string {
	g, w := strings.Split(string(got), "\n"), strings.Split(string(want), "\n")
	for i := 0; i < min(len(g), len(w)); i++ {
		if g[i] != w[i] {
			return fmt.Sprintf("line %d: got %s, want %s", i+1, g[i], w[i])
		}
	}
	return fmt.Sprintf("got %d lines, want %d", len(g), len(w))
}

// TestRead compares what Read reads from the sample files with the files that go test -update writes.
func TestRead(t *testing.T) {
	for _, name := range []string{"wordpress-6", "wordpress-3.3", "wordpress-3.0"} {
		t.Run(name, func(t *testing.T) {
			ex := readSample(t, name+".xml")
			got, err := json.Marshal(ex, jsontext.WithIndent("  "))
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, '\n')
			golden := filepath.Join("testdata", name+".json")
			if *update {
				if err := os.WriteFile(golden, got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("%v; go test -update writes it", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("Read() differs from %s; go test -update rewrites it: %s", golden, firstDiff(got, want))
			}
		})
	}
}

func taxonomies(ex *Export) []string {
	var tax []string
	for _, t := range ex.Terms {
		tax = append(tax, t.Taxonomy)
	}
	return tax
}

func TestReadWordPress6(t *testing.T) {
	ex := readSample(t, "wordpress-6.xml")
	if ex.Version != "1.2" || ex.WordPress != "6.8.3" {
		t.Errorf("Version, WordPress = %q, %q, want 1.2, 6.8.3", ex.Version, ex.WordPress)
	}
	// The title is that of the channel, not that of its image, and &amp; is &.
	ch := ex.Channel
	if ch.Title != "テストサイト & ブログ" || ch.Link != "https://example.com" || ch.Language != "ja" ||
		ch.Generator != "https://wordpress.org/?v=6.8.3" || ch.BaseSiteURL != "https://example.com" ||
		ch.BaseBlogURL != "https://example.com" || ch.PubDate != "Sun, 01 Nov 2026 00:00:00 +0000" ||
		ch.Description != "もうひとつの WordPress サイト" {
		t.Errorf("Channel = %+v", ch)
	}
	if len(ex.Authors) != 2 || len(ex.Terms) != 8 || len(ex.Items) != 13 || len(ex.Notes) != 0 {
		t.Fatalf("%d authors, %d terms, %d items, %d notes, want 2, 8, 13, 0",
			len(ex.Authors), len(ex.Terms), len(ex.Items), len(ex.Notes))
	}
	if a := ex.Author("editor"); a == nil || a.ID != 2 || a.DisplayName != "編集者" || a.FirstName != "太郎" || a.LastName != "山田" {
		t.Errorf("Author(editor) = %+v", a)
	}
	wantTax := []string{"category", "category", "category", "post_tag", "post_tag", "genre", "genre", "nav_menu"}
	if got := taxonomies(ex); !reflect.DeepEqual(got, wantTax) {
		t.Errorf("taxonomies = %q, want %q", got, wantTax)
	}
	news := ex.TermBySlug("category", "%e3%81%8a%e7%9f%a5%e3%82%89%e3%81%9b")
	if news == nil || news.ID != 3 || news.Name != "お知らせ" || news.Description != `<a href="https://example.com/">サイト</a>のお知らせ` {
		t.Errorf("the category of the news = %+v", news)
	}
	if tech := ex.Term("category", 4); tech == nil || tech.Parent != news.Slug {
		t.Errorf("Term(category, 4) = %+v, want the child of the news", tech)
	}
	if sf := ex.Term("genre", 8); sf == nil || sf.Parent != "fiction" || !reflect.DeepEqual(sf.Meta.All("sort_key"), []string{"10", "20"}) {
		t.Errorf("Term(genre, 8) = %+v", sf)
	}
	if v, ok := ex.Term("post_tag", 5).Meta.Get("accent_color"); !ok || v != "#ff0000" {
		t.Errorf("termmeta accent_color = %q, %v", v, ok)
	}
	if menu := ex.Term("nav_menu", 9); menu == nil || menu.Slug != "main-menu" || menu.Name != "メインメニュー" {
		t.Errorf("Term(nav_menu, 9) = %+v", menu)
	}

	hello := ex.Post(10)
	if hello == nil || !hello.Sticky || hello.Status != "publish" || hello.Type != "post" || hello.Creator != "admin" {
		t.Fatalf("Post(10) = %+v", hello)
	}
	if want := "<p>配列の添字 a[b[0]]> を書くと、CDATA が分かれます。</p>"; !strings.Contains(hello.Content, want) {
		t.Errorf("Content = %q, want it to have %q: the CDATA section that is split at ]]> is joined", hello.Content, want)
	}
	if hello.Name != "%e3%81%93%e3%82%93%e3%81%ab%e3%81%a1%e3%81%af" || hello.Excerpt != "最初の投稿の抜粋です。" {
		t.Errorf("Name, Excerpt = %q, %q", hello.Name, hello.Excerpt)
	}
	if hello.Comments != 2 || len(hello.Meta) != 5 {
		t.Errorf("Comments = %d, len(Meta) = %d, want 2, 5: the commentmeta in the comments is not the Meta of the item",
			hello.Comments, len(hello.Meta))
	}
	if got, want := hello.Meta.All("_wp_old_slug"), []string{"hello-world", "%e3%82%b9%e3%83%a9%e3%83%83%e3%82%b0"}; !reflect.DeepEqual(got, want) {
		t.Errorf("_wp_old_slug = %q, want %q", got, want)
	}
	var refs []string
	for _, r := range hello.Terms {
		refs = append(refs, r.Taxonomy+"/"+r.Slug)
	}
	wantRefs := []string{"category/%e3%81%8a%e7%9f%a5%e3%82%89%e3%81%9b", "category/uncategorized", "post_tag/wordpress", "post_format/post-format-aside"}
	if !reflect.DeepEqual(refs, wantRefs) {
		t.Errorf("Terms = %q, want %q", refs, wantRefs)
	}

	if work := ex.Post(13); work == nil || work.GUID != "https://example.com/?post_type=works&p=13" || work.Type != "works" ||
		len(work.Terms) != 1 || work.Terms[0].Taxonomy != "genre" {
		t.Errorf("Post(13) = %+v, want &#038; to be &", work)
	}
	photo := ex.Post(14)
	if photo == nil || photo.Status != "inherit" || photo.Parent != 10 || photo.AttachmentURL != "https://example.com/wp-content/uploads/2026/10/photo.jpg" {
		t.Fatalf("Post(14) = %+v", photo)
	}
	if v, _ := photo.Meta.Get("_wp_attachment_metadata"); !strings.HasPrefix(v, `a:7:{s:5:"width";i:2560;`) || !strings.HasSuffix(v, `s:9:"photo.jpg";}`) {
		t.Errorf("_wp_attachment_metadata = %q, want the serialized value as written", v)
	}
	if page := ex.Post(12); page == nil || page.Parent != 11 || page.MenuOrder != 1 || page.Type != "page" {
		t.Errorf("Post(12) = %+v", page)
	}
	if draft := ex.Post(18); draft == nil || draft.DateGMT != "0000-00-00 00:00:00" || draft.Name != "" || draft.Status != "draft" {
		t.Errorf("Post(18) = %+v", draft)
	}
	if locked := ex.Post(22); locked == nil || locked.Password != "himitsu" {
		t.Errorf("Post(22) = %+v", locked)
	}
}

func TestReadWordPress33(t *testing.T) {
	ex := readSample(t, "wordpress-3.3.xml")
	if ex.Version != "1.1" || ex.WordPress != "3.3.1" {
		t.Errorf("Version, WordPress = %q, %q, want 1.1, 3.3.1", ex.Version, ex.WordPress)
	}
	if len(ex.Authors) != 1 || ex.Authors[0].ID != 2 || len(ex.Terms) != 3 || len(ex.Items) != 3 {
		t.Fatalf("%d authors, %d terms, %d items, want 1, 3, 3", len(ex.Authors), len(ex.Terms), len(ex.Items))
	}
	// WXR 1.1 adds a space after a ] that ends a value, which is kept.
	if got := ex.Post(11).Title; got != "[告知] " {
		t.Errorf("Title = %q, want the space after ]", got)
	}
	if it := ex.Post(11); it.Modified != "" || it.ModifiedGMT != "" || !it.Sticky {
		t.Errorf("Post(11) = %+v, want no wp:post_modified and a sticky post", it)
	}
	if tag := ex.Term("post_tag", 3); tag == nil || tag.Slug != "memo" {
		t.Errorf("Term(post_tag, 3) = %+v", tag)
	}
	if len(ex.Term("category", 2).Meta) != 0 {
		t.Errorf("termmeta of a WXR 1.1 term = %v", ex.Term("category", 2).Meta)
	}
}

func TestReadWordPress30(t *testing.T) {
	ex := readSample(t, "wordpress-3.0.xml")
	// The comment says wordpress in lower case, and the generator element has the same version.
	if ex.Version != "1.0" || ex.WordPress != "3.0" {
		t.Errorf("Version, WordPress = %q, %q, want 1.0, 3.0", ex.Version, ex.WordPress)
	}
	if len(ex.Authors) != 0 || len(ex.Terms) != 4 || len(ex.Items) != 2 {
		t.Fatalf("%d authors, %d terms, %d items, want 0, 4, 2", len(ex.Authors), len(ex.Terms), len(ex.Items))
	}
	for _, term := range ex.Terms {
		if term.ID != 0 {
			t.Errorf("Terms of WXR 1.0 have no wp:term_id, but %s %s has %d", term.Taxonomy, term.Slug, term.ID)
		}
	}
	if got, want := taxonomies(ex), []string{"category", "category", "post_tag", "post_tag"}; !reflect.DeepEqual(got, want) {
		t.Errorf("taxonomies = %q, want %q", got, want)
	}
	// The category elements without attributes are skipped, and tag is post_tag.
	var refs []string
	for _, r := range ex.Post(5).Terms {
		refs = append(refs, r.Taxonomy+"/"+r.Slug+"/"+r.Name)
	}
	want := []string{"category/travel/Travel", "post_tag/rss/rss", "post_tag/feeds/feeds"}
	if !reflect.DeepEqual(refs, want) {
		t.Errorf("Terms = %q, want %q", refs, want)
	}
	if it := ex.Post(5); it.Comments != 1 || it.Creator != "admin" || it.Sticky {
		t.Errorf("Post(5) = %+v", it)
	}
	if v, ok := ex.Post(5).Meta.Get("_edit_lock"); !ok || v != "1277800000:1" {
		t.Errorf("_edit_lock = %q, %v", v, ok)
	}
}

// acceptCase is a file that Read reads, with a check of what it reads.
type acceptCase struct {
	name  string
	in    string
	check func(t *testing.T, ex *Export)
}

// acceptCases returns files with the variations that Read accepts, which the fuzz test also starts from.
func acceptCases() []acceptCase {
	version12 := func(t *testing.T, ex *Export) {
		t.Helper()
		if ex.Version != "1.2" {
			t.Errorf("Version = %q, want 1.2", ex.Version)
		}
	}
	prefixed := `<rss xmlns:w="http://wordpress.org/export/1.2/" xmlns:e="http://wordpress.org/export/1.2/excerpt/" ` +
		`xmlns:c="http://purl.org/rss/1.0/modules/content/" xmlns:d="http://purl.org/dc/elements/1.1/">` +
		`<channel><w:wxr_version>1.2</w:wxr_version><item><w:post_id>3</w:post_id><w:post_type>post</w:post_type>` +
		`<d:creator>x</d:creator><c:encoded>body</c:encoded><e:encoded>excerpt</e:encoded></item></channel></rss>`

	return []acceptCase{
		{"blank lines before the declaration", "\n \n\t" + decl + wxrDoc(""), version12},
		{"byte order mark", "\xef\xbb\xbf" + decl + wxrDoc(""), version12},
		{"no XML declaration", wxrDoc(""), version12},
		{"single quotes in the declaration", "<?xml version='1.0' encoding='UTF-8' standalone='yes'?>\n" + wxrDoc(""), version12},
		{"encoding utf8", `<?xml version="1.0" encoding="utf8"?>` + wxrDoc(""), version12},
		{"encoding UTF-8", `<?xml version="1.0" encoding="UTF-8"?>` + wxrDoc(""), version12},
		{"encoding utf-8", `<?xml version="1.0" encoding="utf-8"?>` + wxrDoc(""), version12},
		{"encoding us-ascii", `<?xml version="1.0" encoding="us-ascii"?>` + wxrDoc(""), version12},
		{"encoding US-ASCII", `<?xml version="1.0" encoding="US-ASCII"?>` + wxrDoc(""), version12},
		{"comments and processing instructions around the root", "<!-- a -->\n<?php echo 1; ?>\n" + wxrDoc("") + "<!-- b -->\n<?pi x?>\n", version12},
		{"the prefixes that the file chooses", prefixed, func(t *testing.T, ex *Export) {
			version12(t, ex)
			it := ex.Post(3)
			if it == nil || it.Creator != "x" || it.Content != "body" || it.Excerpt != "excerpt" {
				t.Errorf("Post(3) = %+v", it)
			}
		}},
		{"elements that are not known", head + `<unknown><a><b/></a></unknown>
<item>
` + minimalItem + `<x:y xmlns:x="urn:x">z</x:y><unknown2/></item>
<wp:author><wp:author_login>a</wp:author_login><extra>1</extra></wp:author>
<wp:category><wp:category_nicename>c</wp:category_nicename><extra/></wp:category>
<wp:term><wp:term_taxonomy>t</wp:term_taxonomy><wp:term_slug>s</wp:term_slug><extra><deep/></extra></wp:term>
</channel>
<other><deep/></other>
</rss>
`, func(t *testing.T, ex *Export) {
			if len(ex.Items) != 1 || len(ex.Authors) != 1 || len(ex.Terms) != 2 {
				t.Errorf("%d items, %d authors, %d terms, want 1, 1, 2", len(ex.Items), len(ex.Authors), len(ex.Terms))
			}
		}},
		{"elements of other namespaces", head + `<atom:link xmlns:atom="http://www.w3.org/2005/Atom" rel="self" href="https://example.com/feed/"/>
<sy:updatePeriod xmlns:sy="http://purl.org/rss/1.0/modules/syndication/">hourly</sy:updatePeriod>
<title>t</title>
<item xmlns:x="urn:x"><x:title>ignored</x:title><wp:post_id>4</wp:post_id><wp:post_type>post</wp:post_type></item>
</channel>
</rss>
`, func(t *testing.T, ex *Export) {
			if ex.Channel.Title != "t" || len(ex.Items) != 1 || ex.Items[0].Title != "" {
				t.Errorf("Title = %q, %d items, want t and one item without a title", ex.Channel.Title, len(ex.Items))
			}
		}},
		{"white space around the version and the integers", strings.Replace(itemDoc(
			"<wp:post_id> 5\n</wp:post_id><wp:post_type>post</wp:post_type><wp:post_parent>\t3 </wp:post_parent>"+
				"<wp:menu_order> -2 </wp:menu_order><wp:is_sticky> 1 </wp:is_sticky>"),
			"<wp:wxr_version>1.2</wp:wxr_version>", "<wp:wxr_version>\n  1.2\t</wp:wxr_version>", 1),
			func(t *testing.T, ex *Export) {
				version12(t, ex)
				if it := ex.Post(5); it == nil || it.Parent != 3 || it.MenuOrder != -2 || !it.Sticky {
					t.Errorf("Post(5) = %+v", it)
				}
			}},
		{"sticky", itemDoc(minimalItem + "<wp:is_sticky>2</wp:is_sticky>"), func(t *testing.T, ex *Export) {
			if !ex.Items[0].Sticky {
				t.Error("Sticky = false for 2")
			}
		}},
		{"not sticky", itemDoc(minimalItem + "<wp:is_sticky>0</wp:is_sticky>"), func(t *testing.T, ex *Export) {
			if ex.Items[0].Sticky {
				t.Error("Sticky = true for 0")
			}
		}},
		{"CRLF line breaks", strings.ReplaceAll(itemDoc(minimalItem+"<content:encoded><![CDATA[a\nb]]></content:encoded><title>c\nd</title>"), "\n", "\r\n"),
			func(t *testing.T, ex *Export) {
				if it := ex.Items[0]; it.Content != "a\nb" || it.Title != "c\nd" {
					t.Errorf("Content, Title = %q, %q, want LF line breaks", it.Content, it.Title)
				}
			}},
		{"comments and processing instructions in a value", itemDoc(minimalItem + "<title>a<!-- x -->b<?pi y?>c</title>"), func(t *testing.T, ex *Export) {
			if got := ex.Items[0].Title; got != "abc" {
				t.Errorf("Title = %q, want abc", got)
			}
		}},
		{"values as written", itemDoc(minimalItem + "<title>  x  </title><content:encoded>a<![CDATA[ b ]]>c</content:encoded><excerpt:encoded>\n</excerpt:encoded>"),
			func(t *testing.T, ex *Export) {
				if it := ex.Items[0]; it.Title != "  x  " || it.Content != "a b c" || it.Excerpt != "\n" {
					t.Errorf("Title, Content, Excerpt = %q, %q, %q", it.Title, it.Content, it.Excerpt)
				}
			}},
		{"the entities of XML", itemDoc(minimalItem + `<title>&lt;a&gt; &amp; &quot;b&quot; &apos;c&apos; &#12354;&#x3044;</title>`), func(t *testing.T, ex *Export) {
			if got := ex.Items[0].Title; got != `<a> & "b" 'c' あい` {
				t.Errorf("Title = %q", got)
			}
		}},
		{"the order of the elements", itemDoc("<wp:post_type>page</wp:post_type><title>t</title><wp:post_id>9</wp:post_id>"), func(t *testing.T, ex *Export) {
			if it := ex.Items[0]; it.ID != 9 || it.Type != "page" || it.Title != "t" {
				t.Errorf("Items[0] = %+v", it)
			}
		}},
		{"blank text between the elements", itemDoc(" \t\r\n<![CDATA[ ]]>" + minimalItem + "<!-- c -->"), version12},
		{"category elements that are not read", itemDoc(minimalItem + `<category><![CDATA[A]]></category><category domain="" nicename="x">B</category>` +
			`<category domain="category" nicename="">C</category><category domain="tag" nicename="t">T</category>` +
			`<category domain="category" nicename="c">C &amp; D</category>`), func(t *testing.T, ex *Export) {
			want := []TermRef{{Taxonomy: "post_tag", Slug: "t", Name: "T"}, {Taxonomy: "category", Slug: "c", Name: "C & D"}}
			got := ex.Items[0].Terms
			if len(got) != len(want) {
				t.Fatalf("Terms = %+v, want %+v", got, want)
			}
			for i := range want {
				got[i].Pos = Pos{}
				if got[i] != want[i] {
					t.Errorf("Terms[%d] = %+v, want %+v", i, got[i], want[i])
				}
			}
		}},
		{"comments are counted and not read", itemDoc(minimalItem + `<wp:comment><wp:comment_id>x</wp:comment_id><wp:postmeta><junk/></wp:postmeta></wp:comment><wp:comment/>`),
			func(t *testing.T, ex *Export) {
				if it := ex.Items[0]; it.Comments != 2 || len(it.Meta) != 0 {
					t.Errorf("Comments, len(Meta) = %d, %d, want 2, 0", it.Comments, len(it.Meta))
				}
			}},
		{"meta without a value", itemDoc(minimalItem + `<wp:postmeta><wp:meta_key>k</wp:meta_key></wp:postmeta>`), func(t *testing.T, ex *Export) {
			if v, ok := ex.Items[0].Meta.Get("k"); !ok || v != "" {
				t.Errorf("Get(k) = %q, %v", v, ok)
			}
		}},
		{"the same ID in different taxonomies", wxrDoc(`<wp:category><wp:term_id>5</wp:term_id><wp:category_nicename>a</wp:category_nicename></wp:category>
<wp:tag><wp:term_id>5</wp:term_id><wp:tag_slug>a</wp:tag_slug></wp:tag>
<wp:term><wp:term_id>5</wp:term_id><wp:term_taxonomy>genre</wp:term_taxonomy><wp:term_slug>a</wp:term_slug></wp:term>
`), func(t *testing.T, ex *Export) {
			if len(ex.Terms) != 3 {
				t.Fatalf("%d terms, want 3", len(ex.Terms))
			}
			for i, tax := range []string{"category", "post_tag", "genre"} {
				if got := ex.Term(tax, 5); got != &ex.Terms[i] {
					t.Errorf("Term(%s, 5) = %p, want Terms[%d]", tax, got, i)
				}
			}
		}},
		{"terms without IDs", wxrDoc(`<wp:category><wp:category_nicename>a</wp:category_nicename></wp:category>
<wp:category><wp:category_nicename>b</wp:category_nicename></wp:category>
`), func(t *testing.T, ex *Export) {
			if len(ex.Terms) != 2 || ex.Term("category", 0) != nil {
				t.Errorf("%d terms, Term(category, 0) = %v", len(ex.Terms), ex.Term("category", 0))
			}
		}},
		{"the generator element", wxrDoc("<generator>https://wordpress.org/?v=6.1.2</generator>\n"), func(t *testing.T, ex *Export) {
			if ex.WordPress != "6.1.2" || ex.Channel.Generator != "https://wordpress.org/?v=6.1.2" {
				t.Errorf("WordPress, Generator = %q, %q", ex.WordPress, ex.Channel.Generator)
			}
		}},
		{"the generator element with http", wxrDoc("<generator> http://wordpress.org/?v=2.9 </generator>\n"), func(t *testing.T, ex *Export) {
			if ex.WordPress != "2.9" || ex.Channel.Generator != " http://wordpress.org/?v=2.9 " {
				t.Errorf("WordPress, Generator = %q, %q", ex.WordPress, ex.Channel.Generator)
			}
		}},
		{"the generator comment comes first", `<!-- generator="WordPress/6.0" created="2022-05-24 00:00" -->` + "\n" +
			wxrDoc("<generator>https://wordpress.org/?v=6.1.2</generator>\n"), func(t *testing.T, ex *Export) {
			if ex.WordPress != "6.0" {
				t.Errorf("WordPress = %q, want 6.0", ex.WordPress)
			}
		}},
		{"a generator that is not WordPress", `<!-- generator="Other/1.0" -->` + wxrDoc("<generator>https://example.com/?v=1</generator>\n"), func(t *testing.T, ex *Export) {
			if ex.WordPress != "" {
				t.Errorf("WordPress = %q, want none", ex.WordPress)
			}
		}},
		{"a generator comment in the channel", wxrDoc(`<!-- generator="WordPress/9.9" -->` + "\n"), func(t *testing.T, ex *Export) {
			if ex.WordPress != "" {
				t.Errorf("WordPress = %q, want none", ex.WordPress)
			}
		}},
		{"the WordPress version without a closing quote", `<!-- generator="WordPress/6.0 -->` + wxrDoc(""), func(t *testing.T, ex *Export) {
			if ex.WordPress != "" {
				t.Errorf("WordPress = %q, want none", ex.WordPress)
			}
		}},
		{"an empty channel apart from the version", `<rss xmlns:wp="http://wordpress.org/export/1.0/"><channel><wp:wxr_version>1.0</wp:wxr_version></channel></rss>`,
			func(t *testing.T, ex *Export) {
				if ex.Version != "1.0" || len(ex.Items) != 0 {
					t.Errorf("Version = %q, %d items", ex.Version, len(ex.Items))
				}
			}},
		{"the namespaces of WXR 1.1 and 1.0 in one file", `<rss xmlns:wp="http://wordpress.org/export/1.1/" xmlns:wp0="http://wordpress.org/export/1.0/">` +
			`<channel><wp:wxr_version>1.1</wp:wxr_version><wp0:author><wp0:author_login>a</wp0:author_login></wp0:author></channel></rss>`,
			func(t *testing.T, ex *Export) {
				if ex.Version != "1.1" || len(ex.Authors) != 1 {
					t.Errorf("Version = %q, %d authors", ex.Version, len(ex.Authors))
				}
			}},
	}
}

func TestReadAccepts(t *testing.T) {
	for _, tt := range acceptCases() {
		t.Run(tt.name, func(t *testing.T) {
			tt.check(t, read(t, tt.in))
		})
	}
}

// TestRemovedCharacters puts the characters that XML does not allow where they are removed from a value, and where
// they are not, and checks the notes.
func TestRemovedCharacters(t *testing.T) {
	u := func(r rune) string { return string(r) }
	var (
		backspace, vtab, unitSep = u(0x08), u(0x0b), u(0x1f)
		fffe, ffff               = u(0xfffe), u(0xffff)
		// The characters that are removed from the values.
		loginChar, contentChar, metaChar, attrChar = u(0x01), u(0x02), u(0x03), u(0x04)
		// The characters that are not in a value, and not in a note.
		commentChar, unknownChar, xmlCommentChar, endTagChar = u(0x05), u(0x06), u(0x07), u(0x0c)
		betweenChar, wpCommentChar, unknownChar2             = u(0x0e), u(0x0f), u(0x10)
		many                                                 = u(0x11)
	)
	in := strings.Join([]string{
		`<rss version="2.0" ` + nsDecl + `>`,
		`<channel>`,
		`<title>あい` + backspace + `う</title>`,
		`<wp:wxr_version>1.2</wp:wxr_version>`,
		`<description>` + vtab + `x` + unitSep + `</description>`,
		`<wp:author><wp:author_login>a` + loginChar + `b</wp:author_login></wp:author>`,
		`<wp:category><wp:category_nicename>c</wp:category_nicename><wp:cat_name>x` + fffe + `y</wp:cat_name></wp:category>`,
		`<item>`,
		`<wp:post_id>1</wp:post_id><wp:post_type>post</wp:post_type>`,
		`<content:encoded><![CDATA[p` + contentChar + `q` + ffff + `r]]></content:encoded>`,
		`<wp:postmeta><wp:meta_key>k</wp:meta_key><wp:meta_value>v` + metaChar + `</wp:meta_value></wp:postmeta>`,
		`<category domain="post_tag` + attrChar + `" nicename="t">T</category>`,
		`<wp:comment><wp:comment_content>x` + commentChar + `y</wp:comment_content></wp:comment>`,
		`<unknown>` + unknownChar + `</unknown>`,
		`<!-- a` + xmlCommentChar + `b -->`,
		`<wp:post_name>n</wp:post_name` + endTagChar + `>`,
		betweenChar,
		`</item>`,
		`<item><wp:post_id>2</wp:post_id><wp:post_type>post</wp:post_type><wp:comment>` + wpCommentChar + `</wp:comment><unknown>` + unknownChar2 + `</unknown></item>`,
		`<item><wp:post_id>3</wp:post_id><wp:post_type>post</wp:post_type><title>` + strings.Repeat(many, 13) + `</title></item>`,
		`</channel>`,
		`</rss>`,
		``,
	}, "\n")
	ex := read(t, in)

	if ex.Channel.Title != "あいう" || ex.Channel.Description != "x" {
		t.Errorf("Title, Description = %q, %q, want あいう, x", ex.Channel.Title, ex.Channel.Description)
	}
	if ex.Authors[0].Login != "ab" || ex.Terms[0].Name != "xy" {
		t.Errorf("Login, Name = %q, %q, want ab, xy", ex.Authors[0].Login, ex.Terms[0].Name)
	}
	if it := ex.Items[0]; it.Content != "pqr" || it.Meta[0].Value != "v" || it.Terms[0].Taxonomy != "post_tag" || it.Name != "n" || it.Comments != 1 {
		t.Errorf("Items[0] = %+v", it)
	}
	if got := ex.Items[2].Title; got != "" {
		t.Errorf("Items[2].Title = %q, want none", got)
	}

	// listed returns the part of a note that lists the characters, at the places that the file has them.
	listed := func(chars ...string) string {
		var parts []string
		for _, c := range chars {
			p := place(t, in, c, 1)
			parts = append(parts, fmt.Sprintf("U+%04X at %d:%d", []rune(c)[0], p.Line, p.Column))
		}
		return strings.Join(parts, ", ")
	}
	const msg = "removed %d characters that XML does not allow: %s"
	manyPlaces := make([]string, 0, 10)
	for i := 1; i <= 10; i++ {
		p := place(t, in, many, i)
		manyPlaces = append(manyPlaces, fmt.Sprintf("U+0011 at %d:%d", p.Line, p.Column))
	}
	want := []Note{
		{Pos: place(t, in, backspace, 1), Record: RecordChannel, Index: 0, Msg: fmt.Sprintf(msg, 3, listed(backspace, vtab, unitSep))},
		{Pos: place(t, in, loginChar, 1), Record: RecordAuthor, Index: 0, Msg: "removed 1 character that XML does not allow: " + listed(loginChar)},
		{Pos: place(t, in, fffe, 1), Record: RecordTerm, Index: 0, Msg: "removed 1 character that XML does not allow: " + listed(fffe)},
		{Pos: place(t, in, contentChar, 1), Record: RecordItem, Index: 0, Msg: fmt.Sprintf(msg, 4, listed(contentChar, ffff, metaChar, attrChar))},
		{Pos: place(t, in, many, 1), Record: RecordItem, Index: 2, Msg: fmt.Sprintf(msg, 13, strings.Join(manyPlaces, ", ")+", and 3 more")},
	}
	if len(ex.Notes) != len(want) {
		for _, n := range ex.Notes {
			t.Logf("note: %+v", n)
		}
		t.Fatalf("%d notes, want %d", len(ex.Notes), len(want))
	}
	for i := range want {
		if ex.Notes[i] != want[i] {
			t.Errorf("Notes[%d] = %+v, want %+v", i, ex.Notes[i], want[i])
		}
	}
}

func TestRemovedCharactersInEmptyValues(t *testing.T) {
	c1, c2, c3 := string(rune(1)), string(rune(2)), string(rune(3))
	// A value that is only removed characters is empty, and they are noted. The character after <title/> is not in a
	// value, and neither is the one in the end tag.
	in := itemDoc(minimalItem + "<title/>" + c1 + "<link>" + c2 + "</link" + c3 + ">\n")
	ex := read(t, in)
	if got := ex.Items[0]; got.Title != "" || got.Link != "" {
		t.Errorf("Title, Link = %q, %q, want none", got.Title, got.Link)
	}
	p := place(t, in, c2, 1)
	want := Note{Pos: p, Record: RecordItem, Index: 0, Msg: fmt.Sprintf("removed 1 character that XML does not allow: U+0002 at %d:%d", p.Line, p.Column)}
	if len(ex.Notes) != 1 || ex.Notes[0] != want {
		t.Errorf("Notes = %+v, want [%+v]", ex.Notes, want)
	}
}

func TestRemovedCharactersBeforeTags(t *testing.T) {
	// A removed character before a start tag does not move the place of the record, and one in the start tag, between
	// the elements, or after the last one is not in the note.
	bs := string(rune(0x08))
	in := wxrDoc("<wp:author><wp:author_login>a</wp:author_login></wp:author>" + bs + "<wp:author" + bs + "><wp:author_login>b</wp:author_login></wp:author" + bs + ">" + bs + "\n")
	ex := read(t, in)
	if len(ex.Authors) != 2 || len(ex.Notes) != 0 {
		t.Fatalf("%d authors, %d notes, want 2, 0", len(ex.Authors), len(ex.Notes))
	}
	if want := place(t, in, "<wp:author"+bs+">", 1); ex.Authors[1].Pos != want {
		t.Errorf("Authors[1].Pos = %v, want %v", ex.Authors[1].Pos, want)
	}
}

func TestPositions(t *testing.T) {
	ideographicSpace := string(rune(0x3000))
	in := "\xef\xbb\xbf<?xml version=\"1.0\"?>\r\n" +
		"<rss version=\"2.0\" " + nsDecl + ">\r\n" +
		"<channel>\r\n" +
		"<wp:wxr_version>1.2</wp:wxr_version>\r\n" +
		"<!-- コメント -->\t<wp:author><wp:author_login>a</wp:author_login></wp:author>\r\n" +
		"<!--あいう-->\t\t" + "<wp:category><wp:category_nicename>c</wp:category_nicename>" +
		"<wp:termmeta><wp:meta_key>k</wp:meta_key></wp:termmeta></wp:category>\r\n" +
		"<wp:tag><wp:tag_slug>t</wp:tag_slug></wp:tag> <wp:term><wp:term_taxonomy>x</wp:term_taxonomy><wp:term_slug>s</wp:term_slug></wp:term>\n" +
		"<!--" + ideographicSpace + "--><item>\n" +
		"<wp:post_id>1</wp:post_id><wp:post_type>post</wp:post_type>\n" +
		"<category domain=\"c\" nicename=\"d\">N</category>\r\n" +
		"<wp:postmeta><wp:meta_key>k</wp:meta_key></wp:postmeta>\n" +
		"</item>\n" +
		"</channel>\r\n</rss>\r\n"
	ex := read(t, in)
	check := func(name string, got Pos, marker string) {
		t.Helper()
		if want := place(t, in, marker, 1); got != want {
			t.Errorf("%s = %v, want %v, the place of %s", name, got, want, marker)
		}
	}
	check("Channel.Pos", ex.Channel.Pos, "<channel>")
	check("Authors[0].Pos", ex.Authors[0].Pos, "<wp:author>")
	check("Terms[0].Pos", ex.Terms[0].Pos, "<wp:category>")
	check("Terms[0].Meta[0].Pos", ex.Terms[0].Meta[0].Pos, "<wp:termmeta>")
	check("Terms[1].Pos", ex.Terms[1].Pos, "<wp:tag>")
	check("Terms[2].Pos", ex.Terms[2].Pos, "<wp:term>")
	check("Items[0].Pos", ex.Items[0].Pos, "<item>")
	check("Items[0].Terms[0].Pos", ex.Items[0].Terms[0].Pos, "<category domain")
	check("Items[0].Meta[0].Pos", ex.Items[0].Meta[0].Pos, "<wp:postmeta>")

	// Some of them by hand: the byte order mark is not counted, the carriage return is, and a character is one column.
	if got, want := ex.Channel.Pos, (Pos{3, 1}); got != want {
		t.Errorf("Channel.Pos = %v, want %v", got, want)
	}
	// "<!-- コメント -->" is 13 characters, and a tab follows.
	if got, want := ex.Authors[0].Pos, (Pos{5, 15}); got != want {
		t.Errorf("Authors[0].Pos = %v, want %v", got, want)
	}
	// "<!--あいう-->" is 10 characters, and two tabs follow.
	if got, want := ex.Terms[0].Pos, (Pos{6, 13}); got != want {
		t.Errorf("Terms[0].Pos = %v, want %v", got, want)
	}
	// A comment with an ideographic space comes first, which is a character like the others.
	if got, want := ex.Items[0].Pos, (Pos{8, 9}); got != want {
		t.Errorf("Items[0].Pos = %v, want %v", got, want)
	}
}

func TestPositionsOnFirstLine(t *testing.T) {
	// The byte order mark is not a column. <rss ...> has 49 characters.
	in := "\xef\xbb\xbf" + `<rss xmlns:wp="http://wordpress.org/export/1.2/"><channel><wp:wxr_version>1.2</wp:wxr_version>` +
		`<wp:author><wp:author_login>a</wp:author_login></wp:author></channel></rss>`
	ex := read(t, in)
	if want := (Pos{1, 50}); ex.Channel.Pos != want {
		t.Errorf("Channel.Pos = %v, want %v", ex.Channel.Pos, want)
	}
	if want := place(t, in, "<wp:author>", 1); ex.Authors[0].Pos != want {
		t.Errorf("Authors[0].Pos = %v, want %v", ex.Authors[0].Pos, want)
	}
}

// TestReadVariants reads the samples in other forms that give the same Export: with a byte order mark, which is not
// counted, with CRLF line breaks, which are LF in values, and from readers that give a few bytes at a time.
func TestReadVariants(t *testing.T) {
	for _, name := range []string{"wordpress-6.xml", "wordpress-3.3.xml", "wordpress-3.0.xml"} {
		data, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		doc := string(data)
		want := read(t, doc)
		variants := []struct {
			name string
			r    io.Reader
		}{
			{"byte order mark", strings.NewReader("\xef\xbb\xbf" + doc)},
			{"CRLF", strings.NewReader(strings.ReplaceAll(doc, "\n", "\r\n"))},
			{"byte order mark and CRLF", strings.NewReader("\xef\xbb\xbf" + strings.ReplaceAll(doc, "\n", "\r\n"))},
			{"one byte at a time", iotest.OneByteReader(strings.NewReader(doc))},
			{"half of what is asked", iotest.HalfReader(strings.NewReader(doc))},
			{"the end with the last bytes", iotest.DataErrReader(strings.NewReader(doc))},
		}
		for _, v := range variants {
			got, err := Read(context.Background(), v.r, Options{})
			if err != nil {
				t.Errorf("%s, %s: Read() = %v", name, v.name, err)
			} else if !reflect.DeepEqual(got, want) {
				t.Errorf("%s, %s: Read() is not the Export of the file itself", name, v.name)
			}
		}
	}
}

func TestIndexes(t *testing.T) {
	ex := read(t, wxrDoc(`<wp:author><wp:author_login>a</wp:author_login></wp:author>
<wp:author><wp:author_login>b</wp:author_login></wp:author>
<wp:category><wp:term_id>5</wp:term_id><wp:category_nicename>x</wp:category_nicename></wp:category>
<wp:tag><wp:term_id>5</wp:term_id><wp:tag_slug>x</wp:tag_slug></wp:tag>
<wp:term><wp:term_id>6</wp:term_id><wp:term_taxonomy>genre</wp:term_taxonomy><wp:term_slug>y</wp:term_slug></wp:term>
<wp:term><wp:term_taxonomy>genre</wp:term_taxonomy><wp:term_slug>z</wp:term_slug></wp:term>
<item><wp:post_id>7</wp:post_id><wp:post_type>post</wp:post_type></item>
<item><wp:post_id>8</wp:post_id><wp:post_type>page</wp:post_type></item>
`))
	// A copy without the indexes looks things up in order, as an Export that a test makes does.
	plain := *ex
	plain.postIdx, plain.termIdx, plain.slugIdx, plain.authorIdx = nil, nil, nil, nil
	for _, e := range []*Export{ex, &plain} {
		name := "indexed"
		if e == &plain {
			name = "plain"
		}
		if got := e.Post(8); got != &e.Items[1] {
			t.Errorf("%s: Post(8) = %p, want Items[1]", name, got)
		}
		for _, id := range []int64{0, -1, 9} {
			if got := e.Post(id); got != nil {
				t.Errorf("%s: Post(%d) = %+v, want nil", name, id, got)
			}
		}
		if got := e.Term("category", 5); got != &e.Terms[0] {
			t.Errorf("%s: Term(category, 5) = %p, want Terms[0]", name, got)
		}
		if got := e.Term("post_tag", 5); got != &e.Terms[1] {
			t.Errorf("%s: Term(post_tag, 5) = %p, want Terms[1]", name, got)
		}
		if got := e.Term("genre", 6); got != &e.Terms[2] {
			t.Errorf("%s: Term(genre, 6) = %p, want Terms[2]", name, got)
		}
		for _, tc := range []struct {
			tax string
			id  int64
		}{{"genre", 5}, {"category", 6}, {"genre", 0}, {"genre", -1}, {"", 5}} {
			if got := e.Term(tc.tax, tc.id); got != nil {
				t.Errorf("%s: Term(%q, %d) = %+v, want nil", name, tc.tax, tc.id, got)
			}
		}
		if got := e.TermBySlug("category", "x"); got != &e.Terms[0] {
			t.Errorf("%s: TermBySlug(category, x) = %p, want Terms[0]", name, got)
		}
		if got := e.TermBySlug("post_tag", "x"); got != &e.Terms[1] {
			t.Errorf("%s: TermBySlug(post_tag, x) = %p, want Terms[1]", name, got)
		}
		if got := e.TermBySlug("genre", "z"); got != &e.Terms[3] {
			t.Errorf("%s: TermBySlug(genre, z) = %p, want Terms[3]", name, got)
		}
		if got := e.TermBySlug("genre", "x"); got != nil {
			t.Errorf("%s: TermBySlug(genre, x) = %+v, want nil", name, got)
		}
		if got := e.Author("b"); got != &e.Authors[1] {
			t.Errorf("%s: Author(b) = %p, want Authors[1]", name, got)
		}
		if got := e.Author("c"); got != nil {
			t.Errorf("%s: Author(c) = %+v, want nil", name, got)
		}
	}
	var empty Export
	if empty.Post(1) != nil || empty.Term("category", 1) != nil || empty.TermBySlug("category", "x") != nil || empty.Author("a") != nil {
		t.Error("an empty Export finds something")
	}
}

type cancelReader struct {
	r      io.Reader
	after  int
	cancel func()
}

func (c *cancelReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if c.after -= n; c.after <= 0 {
		c.cancel()
	}
	return n, err
}

func TestCanceled(t *testing.T) {
	var sb strings.Builder
	for i := 1; i <= 400; i++ {
		fmt.Fprintf(&sb, "<item><wp:post_id>%d</wp:post_id><wp:post_type>post</wp:post_type><title>t</title></item>\n", i)
	}
	in := wxrDoc(sb.String())

	t.Run("before", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		ex, err := Read(ctx, strings.NewReader(in), Options{})
		if !errors.Is(err, context.Canceled) || ex != nil {
			t.Errorf("Read() = %v, %v, want nil and %v", ex, err, context.Canceled)
		}
	})
	t.Run("while reading", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		// The tokens are checked every 1024, which 400 items have, so the check comes before the end.
		r := &cancelReader{r: strings.NewReader(in), after: 10000, cancel: cancel}
		ex, err := Read(ctx, r, Options{})
		if !errors.Is(err, context.Canceled) || ex != nil {
			t.Errorf("Read() = %v, %v, want nil and %v", ex, err, context.Canceled)
		}
		var e *Error
		if errors.As(err, &e) {
			t.Errorf("Read() = %v, an *Error, want the error of the context", err)
		}
	})
}

type failingReader struct {
	r   io.Reader
	err error
}

func (f *failingReader) Read(p []byte) (int, error) {
	n, err := f.r.Read(p)
	if err == io.EOF {
		err = f.err
	}
	return n, err
}

func TestReadError(t *testing.T) {
	boom := errors.New("boom")
	for _, tt := range []struct{ name, in string }{
		{"in the first bytes", "<r"},
		{"in an element", head + "<item>\n"},
		{"in text", head + "<item>\n<title>abc"},
		{"after the root", wxrDoc("")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ex, err := Read(context.Background(), &failingReader{r: strings.NewReader(tt.in), err: boom}, Options{})
			var e *Error
			switch {
			case ex != nil:
				t.Errorf("Read() = %+v, want nil", ex)
			case !errors.Is(err, boom):
				t.Errorf("Read() = %v, want the error of the reader", err)
			case errors.As(err, &e):
				t.Errorf("Read() = %v, an *Error, which a problem with the reader is not", err)
			case !strings.HasPrefix(err.Error(), "reading the WXR: "):
				t.Errorf("Read() = %q, want it to start with %q", err, "reading the WXR: ")
			}
		})
	}
}
