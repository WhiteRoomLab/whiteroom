package wpimport

import (
	"bytes"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/wprest"
	"github.com/WhiteRoomLab/whiteroom/internal/wxr"
)

const (
	// head10 is a WXR 1.0 up to the line after wp:wxr_version.
	head10 = `<rss version="2.0" xmlns:excerpt="http://wordpress.org/export/1.0/excerpt/" ` +
		`xmlns:content="http://purl.org/rss/1.0/modules/content/" xmlns:dc="http://purl.org/dc/elements/1.1/" ` +
		`xmlns:wp="http://wordpress.org/export/1.0/">` + "\n<channel>\n<wp:wxr_version>1.0</wp:wxr_version>\n"

	noSlugReview = "the slug %q is made from the login, as the REST API did not give it; the author page of WordPress may have had another URL"
	noPosts      = "the author has no posts; whiteroom makes author profiles only for the authors of posts"
	notAuthor    = "user %d of the REST API is not an author in the WXR; it is not imported"
)

// authorSite is the site of the tests of Authors: Japanese, and English as another language.
func authorSite() *content.Site {
	return &content.Site{Languages: []string{"ja", "en"}, DefaultLanguage: "ja"}
}

// wxrAuthor is a wp:author to write into a WXR. The first and the last name are in it too, and must not come out.
type wxrAuthor struct {
	id                    int // 0 for no wp:author_id
	login, display, email string
}

func (x wxrAuthor) xml() string {
	var b strings.Builder
	b.WriteString("<wp:author>")
	if x.id > 0 {
		fmt.Fprintf(&b, "<wp:author_id>%d</wp:author_id>", x.id)
	}
	fmt.Fprintf(&b, "<wp:author_login><![CDATA[%s]]></wp:author_login>", x.login)
	fmt.Fprintf(&b, "<wp:author_email><![CDATA[%s]]></wp:author_email>", x.email)
	fmt.Fprintf(&b, "<wp:author_display_name><![CDATA[%s]]></wp:author_display_name>", x.display)
	b.WriteString("<wp:author_first_name><![CDATA[Firstname]]></wp:author_first_name>")
	b.WriteString("<wp:author_last_name><![CDATA[Lastname]]></wp:author_last_name></wp:author>\n")
	return b.String()
}

type wxrPost struct {
	id                   int
	typ, status, creator string // "" for post and publish
}

func (x wxrPost) xml() string {
	typ, status := x.typ, x.status
	if typ == "" {
		typ = "post"
	}
	if status == "" {
		status = "publish"
	}
	return fmt.Sprintf("<item>\n<title><![CDATA[Item %d]]></title>\n<dc:creator><![CDATA[%s]]></dc:creator>\n"+
		"<wp:post_id>%d</wp:post_id>\n<wp:status><![CDATA[%s]]></wp:status>\n<wp:post_type><![CDATA[%s]]></wp:post_type>\n</item>\n",
		x.id, x.creator, x.id, status, typ)
}

func authorsXML(header string, authors []wxrAuthor, posts []wxrPost) string {
	var b strings.Builder
	b.WriteString(header + "<link>https://example.com</link>\n")
	for _, a := range authors {
		b.WriteString(a.xml())
	}
	for _, p := range posts {
		b.WriteString(p.xml())
	}
	b.WriteString(tail)
	return b.String()
}

// authorsWXR writes a WXR 1.2 of the site https://example.com with the authors and the items.
func authorsWXR(authors []wxrAuthor, posts []wxrPost) string {
	return authorsXML(head, authors, posts)
}

// postsBy writes a post by each of the logins, with the IDs from 1.
func postsBy(logins ...string) []wxrPost {
	var posts []wxrPost
	for i, login := range logins {
		posts = append(posts, wxrPost{id: i + 1, creator: login})
	}
	return posts
}

// makeAuthors reads the WXR in, and makes the author profiles of site and checks them. rest is the REST API, or nil.
func makeAuthors(t *testing.T, site *content.Site, in string, rest *wprest.Snapshot) (*Import, []content.AuthorProfile) {
	t.Helper()
	e, rep := readString(t, "site.xml", in)
	imp := New("site.xml", e, rep)
	if rest != nil {
		if rest.Index.Home == "" {
			rest.Index.Home = e.Channel.Link
		}
		imp.REST, imp.RESTFile = rest, "rest.json"
	}
	authors, err := imp.Authors(site)
	if err != nil {
		t.Fatalf("Authors = %v", err)
	}
	checkAuthors(t, imp, site, authors)
	return imp, authors
}

// checkAuthors checks what holds for the profiles of any WXR: each passes Validate with the records of the import, which
// finds it; they are in the order of their IDs, with slugs that differ; the email address, the first name and the last
// name of the authors are in neither the profiles nor the report; and no profile and no record of the report names an
// author or a user of the REST API by an email address.
func checkAuthors(t *testing.T, imp *Import, site *content.Site, authors []content.AuthorProfile) {
	t.Helper()
	var out strings.Builder
	slugs := make(map[string]string)
	for i, a := range authors {
		if err := a.Validate(site, imp.Ref); err != nil {
			t.Errorf("author %s (%.40q): %v", a.ID, a.Name, err)
		}
		if r, ok := imp.Ref(content.RefAuthor, a.ID); !ok || r != (content.Ref{}) {
			t.Errorf("Ref of author %s = %+v, %v", a.ID, r, ok)
		}
		if i > 0 && authors[i-1].ID >= a.ID {
			t.Errorf("authors %s and %s are not in the order of their IDs", authors[i-1].ID, a.ID)
		}
		if other, ok := slugs[a.Slug]; ok {
			t.Errorf("authors %s and %s have the slug %q", other, a.ID, a.Slug)
		}
		slugs[a.Slug] = a.ID
		b, err := a.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		out.Write(b)
	}
	var text, js bytes.Buffer
	if err := imp.Report.WriteText(&text); err != nil {
		t.Fatal(err)
	}
	if err := imp.Report.WriteJSON(&js); err != nil {
		t.Fatal(err)
	}
	out.Write(text.Bytes())
	out.Write(js.Bytes())
	for _, a := range imp.WXR.Authors {
		for _, secret := range []string{a.Email, a.FirstName, a.LastName} {
			if secret != "" && strings.Contains(out.String(), secret) {
				t.Errorf("%q of the author %q is in the profiles or the report", secret, a.Login)
			}
		}
	}
	isEmail := func(name string) bool {
		if hasEmailAddress(name) {
			return true
		}
		for _, a := range imp.WXR.Authors {
			if isAuthorEmail(name, a.Email) {
				return true
			}
		}
		return false
	}
	for _, a := range authors {
		if isEmail(a.Name) {
			t.Errorf("the name of the author %s is an email address: %q", a.ID, a.Name)
		}
	}
	for _, e := range imp.Report.Entries() {
		if src := e.Source; (src.Record == recordAuthor || src.Record == recordREST && src.Type == "user") && isEmail(src.Name) {
			t.Errorf("an entry names %q, an email address: %s", src.Name, e.Message)
		}
	}
}

func authorID(t *testing.T, imp *Import, login string) string {
	t.Helper()
	_, id, ok := imp.IDs.Lookup(UserObject(login))
	if !ok {
		t.Fatalf("user %q has no ID", login)
	}
	return id
}

func profileOf(t *testing.T, imp *Import, authors []content.AuthorProfile, login string) content.AuthorProfile {
	t.Helper()
	id := authorID(t, imp, login)
	i := slices.IndexFunc(authors, func(a content.AuthorProfile) bool { return a.ID == id })
	if i < 0 {
		t.Fatalf("no profile for the user %q", login)
	}
	return authors[i]
}

// authorWho is the start of a line of the report, up to the colon after the kind, for the user with the login: at the
// place of its wp:author, or of the first item that has it for dc:creator.
func authorWho(t *testing.T, e *wxr.Export, file, kind, login string) string {
	t.Helper()
	return authorWhoAs(t, e, file, kind, login, strconv.Quote(login))
}

// authorWhoAs is authorWho for an author that the report shows as shown: the quoted login, or the ID.
func authorWhoAs(t *testing.T, e *wxr.Export, file, kind, login, shown string) string {
	t.Helper()
	var pos wxr.Pos
	if a := e.Author(login); a != nil {
		pos = a.Pos
	} else if i := slices.IndexFunc(e.Items, func(it wxr.Item) bool { return it.Creator == login }); i >= 0 {
		pos = e.Items[i].Pos
	} else {
		t.Fatalf("no user %q in the WXR", login)
	}
	return fmt.Sprintf("%s:%d:%d: %s: author %s: ", file, pos.Line, pos.Column, kind, shown)
}

// authorTarget is the end of a line of the report for the profile with the ID id.
func authorTarget(id, pointer string) string {
	s := " -> authors/" + id + ".json"
	if pointer != "" {
		s += "#" + pointer
	}
	return s
}

// restSkipped is the line of the report for the user of the REST API that no author is.
func restSkipped(id int64, name string) string {
	return fmt.Sprintf("rest.json: skipped: rest %d (user %q): "+notAuthor, id, name, id)
}

// linesAt returns the lines of the report that are about pointer in a document.
func linesAt(t *testing.T, rep *Report, pointer string) []string {
	t.Helper()
	var lines []string
	for _, l := range reportLines(t, rep) {
		if strings.HasSuffix(l, "#"+pointer) {
			lines = append(lines, l)
		}
	}
	return lines
}

func TestAuthorsSample(t *testing.T) {
	e, rep := readSample(t, "wordpress-6.xml")
	imp := New("wordpress-6.xml", e, rep)
	site := authorSite()
	authors, err := imp.Authors(site)
	if err != nil {
		t.Fatal(err)
	}
	checkAuthors(t, imp, site, authors)

	// admin wrote the post 10 and others, and editor the posts 18 (a draft) and 22, and two pages.
	admin, editor := authorID(t, imp, "admin"), authorID(t, imp, "editor")
	for login, want := range map[string]content.AuthorProfile{
		"admin":  {ID: admin, Name: "admin", Slug: "admin"},
		"editor": {ID: editor, Name: "編集者", Slug: "editor"},
	} {
		if got := profileOf(t, imp, authors, login); !reflect.DeepEqual(got, want) {
			t.Errorf("the profile of %s = %+v, want %+v", login, got, want)
		}
	}
	if len(authors) != 2 {
		t.Errorf("%d profiles, want 2", len(authors))
	}

	file := "wordpress-6.xml"
	wantLines(t, rep, []string{
		commentMsg,
		authorWho(t, e, file, "review", "admin") + fmt.Sprintf(noSlugReview, "admin") + authorTarget(admin, "/slug"),
		authorWho(t, e, file, "review", "editor") + fmt.Sprintf(noSlugReview, "editor") + authorTarget(editor, "/slug"),
	})
	// The places are those of the sample.
	if line := authorWho(t, e, file, "review", "editor"); line != `wordpress-6.xml:23:2: review: author "editor": ` {
		t.Errorf("the start of the line for editor is %q", line)
	}
}

func TestAuthorsOldSamples(t *testing.T) {
	// WXR 1.0 has no wp:author: admin is known by the dc:creator of the first item.
	e, rep := readSample(t, "wordpress-3.0.xml")
	imp := New("wordpress-3.0.xml", e, rep)
	site := authorSite()
	authors, err := imp.Authors(site)
	if err != nil {
		t.Fatal(err)
	}
	checkAuthors(t, imp, site, authors)
	id := authorID(t, imp, "admin")
	if want := []content.AuthorProfile{{ID: id, Name: "admin", Slug: "admin"}}; !reflect.DeepEqual(authors, want) {
		t.Errorf("Authors of WXR 1.0 = %+v, want %+v", authors, want)
	}
	first := e.Items[0].Pos
	nameLine := fmt.Sprintf("wordpress-3.0.xml:%d:%d: changed: author \"admin\": the display name is empty; the name is \"admin\" here", first.Line, first.Column)
	slugLine := fmt.Sprintf("wordpress-3.0.xml:%d:%d: review: author \"admin\": ", first.Line, first.Column) + fmt.Sprintf(noSlugReview, "admin")
	var got []string
	for _, l := range reportLines(t, rep) {
		if strings.Contains(l, "author") {
			got = append(got, l)
		}
	}
	sameSet(t, "the lines", got, []string{nameLine + authorTarget(id, "/name"), slugLine + authorTarget(id, "/slug")})

	// WXR 1.1 has the display name.
	e, rep = readSample(t, "wordpress-3.3.xml")
	imp = New("wordpress-3.3.xml", e, rep)
	authors, err = imp.Authors(site)
	if err != nil {
		t.Fatal(err)
	}
	checkAuthors(t, imp, site, authors)
	if len(authors) != 1 || authors[0].Name != "管理者" || authors[0].Slug != "admin" {
		t.Errorf("Authors of WXR 1.1 = %+v, want the name 管理者 and the slug admin", authors)
	}
}

// restAuthors is a WXR and a REST API with an author that has all the REST API gives, one whose website is not a link,
// authors without posts, and a user of the REST API that no author is.
func restAuthors() (string, *wprest.Snapshot) {
	in := authorsWXR(
		[]wxrAuthor{
			{id: 1, login: "admin", display: "Admin", email: "admin@example.com"},
			{id: 2, login: "editor", display: "Tom &amp; Jerry", email: "editor@example.com"},
			{id: 3, login: "gone", display: "Gone", email: "gone@example.com"},
			{id: 4, login: "evil", display: "Evil", email: "evil@example.com"},
		},
		[]wxrPost{
			{id: 10, typ: "page", creator: "admin"},
			{id: 11, typ: "nav_menu_item", creator: "admin"},
			{id: 12, creator: "editor"},
			{id: 13, status: "trash", creator: "gone"},
			{id: 14, status: "auto-draft", creator: "gone"},
			{id: 15, status: "draft", creator: "evil"},
		},
	)
	rest := &wprest.Snapshot{Users: []wprest.User{
		{ID: 1, Name: "Admin", Slug: "admin"},
		{
			ID: 2, Name: "Tom &amp; Jerry", Slug: "%e5%b1%b1%e7%94%b0", Link: "https://example.com/author/%e5%b1%b1%e7%94%b0/",
			Description: "<p>Hello <b>world</b></p>\n<p>See <a href=\"https://example.org/\">my site</a></p>",
			URL:         "https://example.org/me/",
		},
		{ID: 4, Name: "Evil", Slug: "evil", URL: "javascript:alert(1)"},
		{ID: 9, Name: "Ghost", Slug: "ghost"},
	}}
	return in, rest
}

func TestAuthorsREST(t *testing.T) {
	in, rest := restAuthors()
	imp, authors := makeAuthors(t, authorSite(), in, rest)
	if len(authors) != 2 {
		t.Fatalf("%d profiles, want editor and evil", len(authors))
	}
	editor, evil := authorID(t, imp, "editor"), authorID(t, imp, "evil")
	want := map[string]content.AuthorProfile{
		"editor": {
			ID: editor, Name: "Tom & Jerry", Slug: "山田",
			Bio: content.Localized{"ja": "Hello world\n\nSee my site"}, Links: []string{"https://example.org/me/"},
		},
		"evil": {ID: evil, Name: "Evil", Slug: "evil"},
	}
	for login, w := range want {
		if got := profileOf(t, imp, authors, login); !reflect.DeepEqual(got, w) {
			t.Errorf("the profile of %s = %+v, want %+v", login, got, w)
		}
	}
	for _, login := range []string{"admin", "gone"} {
		if _, _, ok := imp.IDs.Lookup(UserObject(login)); ok {
			t.Errorf("%s has an ID", login)
		}
	}

	e, file := imp.WXR, "site.xml"
	wantLines(t, imp.Report, []string{
		authorWho(t, e, file, "skipped", "admin") + noPosts,
		authorWho(t, e, file, "skipped", "gone") + noPosts,
		authorWho(t, e, file, "changed", "editor") + `kept only the text of the HTML; lost <b>, a link to "https://example.org/"` + authorTarget(editor, "/bio/ja"),
		authorWho(t, e, file, "skipped", "evil") + `the website "javascript:alert(1)" is not imported: scheme "javascript" is not allowed` + authorTarget(evil, "/links"),
		restSkipped(9, "Ghost"),
	})
}

func TestAuthorsSameResult(t *testing.T) {
	in, rest := restAuthors()
	run := func() (profiles, report string) {
		imp, authors := makeAuthors(t, authorSite(), in, rest)
		var b strings.Builder
		for _, a := range authors {
			j, err := a.MarshalJSON()
			if err != nil {
				t.Fatal(err)
			}
			b.Write(j)
		}
		var js bytes.Buffer
		if err := imp.Report.WriteJSON(&js); err != nil {
			t.Fatal(err)
		}
		return b.String(), js.String()
	}
	p1, r1 := run()
	p2, r2 := run()
	if p1 != p2 {
		t.Errorf("the profiles of two imports of the same input differ:\n%s\n%s", p1, p2)
	}
	if r1 != r2 {
		t.Errorf("the reports of two imports of the same input differ:\n%s\n%s", r1, r2)
	}
	if p1 == "" || r1 == "" {
		t.Error("the profiles or the report are empty")
	}
}

func TestAuthorsNames(t *testing.T) {
	in := authorsWXR(
		[]wxrAuthor{
			{id: 1, login: "amp", display: "Tom &amp; Jerry"},
			{id: 2, login: "lines", display: "A\nB\tC"},
			{id: 3, login: "empty", display: ""},
			{id: 4, login: "blank", display: " \n "},
			{id: 5, login: "tag", display: "&lt;b&gt;x&lt;/b&gt;"},
			{id: 6, login: "viarest", display: ""},
			{id: 7, login: "restlines", display: ""},
			{id: 8, login: "restblank", display: ""},
			{id: 9, login: "kept", display: "  Kept  "},
			{id: 10, login: "nfd", display: "e" + string(rune(0x301))},
		},
		postsBy("amp", "lines", "empty", "blank", "tag", "viarest", "restlines", "restblank", "kept", "nfd"),
	)
	rest := &wprest.Snapshot{Users: []wprest.User{
		{ID: 6, Name: "Real &amp; Name", Slug: "viarest"},
		{ID: 7, Name: "R\nS", Slug: "restlines"},
		{ID: 8, Name: " ", Slug: "restblank"},
		{ID: 9, Name: "Not used", Slug: "kept"},
	}}
	imp, authors := makeAuthors(t, authorSite(), in, rest)
	for login, name := range map[string]string{
		"amp": "Tom & Jerry", "lines": "A B C", "empty": "empty", "blank": "blank", "tag": "<b>x</b>",
		"viarest": "Real & Name", "restlines": "R S", "restblank": "restblank", "kept": "Kept", "nfd": string(rune(0xE9)),
	} {
		if got := profileOf(t, imp, authors, login).Name; got != name {
			t.Errorf("the name of %s = %q, want %q", login, got, name)
		}
	}

	e, file := imp.WXR, "site.xml"
	emptyLine := func(login, name string) string {
		return authorWho(t, e, file, "changed", login) + fmt.Sprintf("the display name is empty; the name is %q here", name) +
			authorTarget(authorID(t, imp, login), "/name")
	}
	linesLine := func(login string) string {
		return authorWho(t, e, file, "changed", login) + "line breaks and tabs in the name are spaces here" +
			authorTarget(authorID(t, imp, login), "/name")
	}
	sameSet(t, "the lines", linesAt(t, imp.Report, "/name"), []string{
		linesLine("lines"),
		emptyLine("empty", "empty"),
		emptyLine("blank", "blank"),
		emptyLine("viarest", "Real & Name"),
		emptyLine("restlines", "R S"),
		linesLine("restlines"),
		emptyLine("restblank", "restblank"),
	})
}

func TestAuthorsSlugs(t *testing.T) {
	long := strings.Repeat("a", 300)
	in := authorsWXR(
		[]wxrAuthor{
			{id: 1, login: "plain", display: "Plain"},
			{id: 2, login: "japanese", display: "Japanese"},
			{id: 3, login: "upper", display: "Upper"},
			{id: 4, login: "bad", display: "Bad"},
			{id: 5, login: "left", display: "Left"},
			{id: 6, login: "John.Smith", display: "John"},
			{id: 7, login: "noslug", display: "No slug"},
			{id: 8, login: "!!!", display: "Symbols"},
			{id: 9, login: "same1", display: "Same 1"},
			{id: 10, login: "same2", display: "Same 2"},
			{id: 11, login: "long", display: "Long"},
			{id: 12, login: "same3", display: "Same 3"},
			{id: 13, login: "Dup.Name", display: "Dup 1"},
			{id: 14, login: "dup-name", display: "Dup 2"},
		},
		postsBy("plain", "japanese", "upper", "bad", "left", "John.Smith", "noslug", "!!!", "same1", "same2", "long", "same3", "Dup.Name", "dup-name"),
	)
	rest := &wprest.Snapshot{Users: []wprest.User{
		{ID: 1, Slug: "plain"},
		{ID: 2, Slug: "%e5%b1%b1%e7%94%b0"},
		{ID: 3, Slug: "Upper-Case"},
		{ID: 4, Slug: "%zz"},
		{ID: 5, Slug: "%2e"},
		{ID: 7, Name: "No slug"},
		{ID: 9, Slug: "same"},
		{ID: 10, Slug: "same"},
		{ID: 11, Slug: long},
		{ID: 12, Slug: "same-2"},
	}}
	imp, authors := makeAuthors(t, authorSite(), in, rest)
	e, file := imp.WXR, "site.xml"
	id := func(login string) string { return authorID(t, imp, login) }
	line := func(login string, kind Kind, msg string) string {
		return authorWho(t, e, file, string(kind), login) + msg + authorTarget(id(login), "/slug")
	}
	// The slugs that the REST API gave come first: same3 has the slug same-2, which same2 would be given after same.
	for login, slug := range map[string]string{
		"plain": "plain", "japanese": "山田", "upper": "upper-case", "bad": "zz", "left": "author-5", "John.Smith": "john-smith",
		"noslug": "noslug", "!!!": "author-8", "same1": "same", "same2": "same-3", "long": long[:200], "same3": "same-2",
		"Dup.Name": "dup-name", "dup-name": "dup-name-2",
	} {
		if got := profileOf(t, imp, authors, login).Slug; got != slug {
			t.Errorf("the slug of %s = %.60q, want %.60q", login, got, slug)
		}
	}
	wantLines(t, imp.Report, []string{
		line("upper", Changed, `the slug "Upper-Case" is "upper-case" here: `+whyLower),
		line("bad", Changed, `the slug "%zz" is "zz" here: `+whyDecode+"invalid percent-encoding at byte 0; "+whyChars),
		line("left", Changed, `nothing is left of the slug "%2e"; it is "author-5" here`),
		line("John.Smith", Review, fmt.Sprintf(noSlugReview, "john-smith")),
		line("noslug", Review, fmt.Sprintf(noSlugReview, "noslug")),
		line("!!!", Changed, `nothing is left of the slug that the login makes; it is "author-8" here`),
		line("!!!", Review, fmt.Sprintf(noSlugReview, "author-8")),
		line("same2", Changed, `the slug "same" is also that of another author; it is "same-3" here`),
		line("long", Changed, fmt.Sprintf("the slug %q is %q here: %s", long, long[:200], whyCut)),
		line("Dup.Name", Review, fmt.Sprintf(noSlugReview, "dup-name")),
		line("dup-name", Review, fmt.Sprintf(noSlugReview, "dup-name")),
		line("dup-name", Changed, `the slug "dup-name" is also that of another author; it is "dup-name-2" here`),
	})
}

func TestAuthorsSlugWithoutID(t *testing.T) {
	// No wp:author_id and no REST API: the slug is "author" if nothing is left of the login.
	in := authorsWXR(
		[]wxrAuthor{{login: "!!"}, {login: "??"}, {login: "ok"}},
		postsBy("!!", "??", "ok"),
	)
	imp, authors := makeAuthors(t, authorSite(), in, nil)
	if len(authors) != 3 {
		t.Fatalf("%d profiles, want 3", len(authors))
	}
	got := map[string]string{}
	for _, login := range []string{"!!", "??", "ok"} {
		p := profileOf(t, imp, authors, login)
		got[login] = p.Slug + "/" + p.Name
	}
	want := map[string]string{"!!": "author/!!", "??": "author-2/??", "ok": "ok/ok"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("slugs and names = %q, want %q", got, want)
	}
}

func TestAuthorsBio(t *testing.T) {
	complex := strings.Repeat("<div>", 10000) + strings.Repeat("</p>", 10000)
	long := strings.Repeat("a", content.MaxTextBytes+1)
	tests := []struct {
		desc string
		bio  string // "" for none
		line string // the report line after the start, or ""
		kind Kind
	}{
		{desc: ""},
		{desc: "Plain text", bio: "Plain text"},
		{desc: "<p>One</p><p>Two</p>", bio: "One\n\nTwo"},
		{desc: "<b>Bold</b> text", bio: "Bold text", kind: Changed, line: "kept only the text of the HTML; lost <b>"},
		{desc: `<img src="/a.png">`, kind: Changed, line: `kept only the text of the HTML; lost an image "/a.png"`},
		{desc: " \n "},
		{desc: complex, kind: Skipped, line: "the bio is not imported: it is too complex to read as HTML"},
		{desc: long, kind: Skipped, line: fmt.Sprintf("the bio is not imported: it is too long: %d bytes (max %d)", len(long), content.MaxTextBytes)},
		{desc: "Line 1\r\nLine 2", bio: "Line 1\nLine 2"},
		{desc: "&lt;b&gt; &amp; x", bio: "<b> & x"},
		{desc: `<p>日本語の<a href="/x">リンク</a></p>`, bio: "日本語のリンク", kind: Changed, line: `kept only the text of the HTML; lost a link to "/x"`},
	}
	var authors []wxrAuthor
	var logins []string
	rest := &wprest.Snapshot{}
	for i, tt := range tests {
		login := "u" + strconv.Itoa(i+1)
		authors = append(authors, wxrAuthor{id: i + 1, login: login, display: login})
		logins = append(logins, login)
		rest.Users = append(rest.Users, wprest.User{ID: int64(i + 1), Name: login, Slug: login, Description: tt.desc})
	}
	for _, language := range []string{"ja", "en"} {
		t.Run(language, func(t *testing.T) {
			site := authorSite()
			site.DefaultLanguage = language
			imp, profiles := makeAuthors(t, site, authorsWXR(authors, postsBy(logins...)), rest)
			var want []string
			for i, tt := range tests {
				p := profileOf(t, imp, profiles, logins[i])
				var bio content.Localized
				if tt.bio != "" {
					bio = content.Localized{language: tt.bio}
				}
				if !reflect.DeepEqual(p.Bio, bio) {
					t.Errorf("the bio of %.40q = %.80q, want %.80q", tt.desc, p.Bio, bio)
				}
				if tt.line != "" {
					want = append(want, authorWho(t, imp.WXR, "site.xml", string(tt.kind), logins[i])+tt.line+authorTarget(p.ID, "/bio/"+language))
				}
			}
			sameSet(t, "the lines", linesAt(t, imp.Report, "/bio/"+language), want)
		})
	}
}

func TestAuthorsLinks(t *testing.T) {
	tests := []struct {
		url    string
		link   string // "" for none
		shown  string // the website in the report, if it says why there is no link; "" if the report does not show it
		reason string
	}{
		{url: "https://example.org/", link: "https://example.org/"},
		{url: "http://example.org", link: "http://example.org"},
		{url: "  https://example.org/a\n", link: "https://example.org/a"},
		{url: "HTTPS://Example.org/A", link: "HTTPS://Example.org/A"},
		{url: ""},
		{url: " \t "},
		{url: "javascript:alert(1)", shown: "javascript:alert(1)", reason: `scheme "javascript" is not allowed`},
		{url: "java\tscript:alert(1)", shown: "javascript:alert(1)", reason: `scheme "javascript" is not allowed`},
		{url: "/about", shown: "/about", reason: "must be an http or https URL"},
		{url: "mailto:someone@example.org", reason: "must be an http or https URL"}, // not shown: it has an email address
		{url: "ftp://example.org/@someone", shown: "ftp://example.org/@someone", reason: `scheme "ftp" is not allowed`},
		{url: "tel:+81312345678", shown: "tel:+81312345678", reason: "must be an http or https URL"},
		{url: "ftp://example.org/", shown: "ftp://example.org/", reason: `scheme "ftp" is not allowed`},
		{url: "http://", shown: "http://", reason: "missing host"},
		{url: "https://user:pw@example.org/", reason: "credentials in URLs are not allowed"},
		{url: "example.org", shown: "example.org", reason: `must be an absolute URL or a path starting with "/"`},
		{url: "//example.org/", shown: "//example.org/", reason: "protocol-relative URLs are not allowed"},
		{url: `http://example.org\evil`, shown: `http://example.org\evil`, reason: "backslashes are not allowed"},
	}
	var authors []wxrAuthor
	var logins []string
	rest := &wprest.Snapshot{}
	for i, tt := range tests {
		login := "u" + strconv.Itoa(i+1)
		authors = append(authors, wxrAuthor{id: i + 1, login: login, display: login})
		logins = append(logins, login)
		rest.Users = append(rest.Users, wprest.User{ID: int64(i + 1), Name: login, Slug: login, URL: tt.url})
	}
	imp, profiles := makeAuthors(t, authorSite(), authorsWXR(authors, postsBy(logins...)), rest)
	var want []string
	for i, tt := range tests {
		p := profileOf(t, imp, profiles, logins[i])
		var links []string
		if tt.link != "" {
			links = []string{tt.link}
		}
		if !reflect.DeepEqual(p.Links, links) {
			t.Errorf("the links for %q = %q, want %q", tt.url, p.Links, links)
		}
		if tt.reason != "" {
			msg := fmt.Sprintf("the website %q is not imported: %s", tt.shown, tt.reason)
			if tt.shown == "" {
				msg = "the website is not imported: " + tt.reason
			}
			want = append(want, authorWho(t, imp.WXR, "site.xml", "skipped", logins[i])+msg+authorTarget(p.ID, "/links"))
		}
	}
	wantLines(t, imp.Report, want)
}

func TestAuthorsOldWXR(t *testing.T) {
	// A WXR 1.0 has no wp:author: the users are the dc:creator of the items, and have no IDs.
	in := authorsXML(head10, nil, []wxrPost{
		{id: 5, creator: "alice"},
		{id: 6, creator: "bob"},
		{id: 7, creator: "山田"},
		{id: 8, typ: "page", creator: "carol"},
		{id: 9, creator: ""},
		{id: 10, creator: "alice"},
	})
	rest := &wprest.Snapshot{Users: []wprest.User{
		{ID: 7, Name: "Alice A.", Slug: "alice", Description: "About <em>Alice</em>", URL: "https://alice.example.org/"},
		{ID: 8, Name: "山田 太郎", Slug: "%e5%b1%b1%e7%94%b0"},
		{ID: 20, Name: "Dave", Slug: "dave"},
	}}
	imp, authors := makeAuthors(t, authorSite(), in, rest)
	if len(authors) != 3 {
		t.Fatalf("%d profiles, want alice, bob and the one in Japanese", len(authors))
	}
	alice, bob, yamada := authorID(t, imp, "alice"), authorID(t, imp, "bob"), authorID(t, imp, "山田")
	want := map[string]content.AuthorProfile{
		"alice": {ID: alice, Name: "Alice A.", Slug: "alice", Bio: content.Localized{"ja": "About Alice"}, Links: []string{"https://alice.example.org/"}},
		"bob":   {ID: bob, Name: "bob", Slug: "bob"},
		"山田":    {ID: yamada, Name: "山田 太郎", Slug: "山田"},
	}
	for login, w := range want {
		if got := profileOf(t, imp, authors, login); !reflect.DeepEqual(got, w) {
			t.Errorf("the profile of %s = %+v, want %+v", login, got, w)
		}
	}
	if _, _, ok := imp.IDs.Lookup(UserObject("carol")); ok {
		t.Error("carol, who wrote a page, has an ID")
	}
	// A user that only dc:creator names is at the place of the first item that names it, also if it is not a post.
	at := func(kind string, item int, login string) string {
		line, column := place(t, in, "<item>", item)
		return fmt.Sprintf("site.xml:%d:%d: %s: author %q: ", line, column, kind, login)
	}
	wantLines(t, imp.Report, []string{
		at("changed", 1, "alice") + `the display name is empty; the name is "Alice A." here` + authorTarget(alice, "/name"),
		at("changed", 1, "alice") + "kept only the text of the HTML; lost <em>" + authorTarget(alice, "/bio/ja"),
		at("changed", 2, "bob") + `the display name is empty; the name is "bob" here` + authorTarget(bob, "/name"),
		at("review", 2, "bob") + fmt.Sprintf(noSlugReview, "bob") + authorTarget(bob, "/slug"),
		at("changed", 3, "山田") + `the display name is empty; the name is "山田 太郎" here` + authorTarget(yamada, "/name"),
		at("skipped", 4, "carol") + noPosts,
		restSkipped(20, "Dave"),
	})
}

func TestAuthorsMatching(t *testing.T) {
	user := func(id int64, name, slug string) wprest.User { return wprest.User{ID: id, Name: name, Slug: slug} }
	tests := []struct {
		name    string
		authors []wxrAuthor
		users   []wprest.User
		names   map[string]string // the names that the profiles get, by login
		others  []wprest.User     // the users of the REST API that no author is
	}{
		{
			name:    "the ID decides, not the slug",
			authors: []wxrAuthor{{id: 5, login: "carl", display: ""}},
			users:   []wprest.User{user(5, "Karl", "karl"), user(6, "Carl", "carl")},
			names:   map[string]string{"carl": "Karl"},
			others:  []wprest.User{user(6, "Carl", "carl")},
		},
		{
			name:    "an author with an ID that the REST API lacks is not found by the slug",
			authors: []wxrAuthor{{id: 9, login: "bob", display: ""}},
			users:   []wprest.User{user(10, "Robert", "bob")},
			names:   map[string]string{"bob": "bob"},
			others:  []wprest.User{user(10, "Robert", "bob")},
		},
		{
			name:    "an author with no ID is found by the slug",
			authors: []wxrAuthor{{login: "Bob.Smith"}, {login: "eve"}},
			users:   []wprest.User{user(3, "Robert Smith", "bob-smith"), user(4, "Eve E.", "eve")},
			names:   map[string]string{"Bob.Smith": "Robert Smith", "eve": "Eve E."},
		},
		{
			name:    "a user that an ID found is not found again by the slug",
			authors: []wxrAuthor{{id: 5, login: "x", display: ""}, {login: "y"}},
			users:   []wprest.User{user(5, "Ex", "y")},
			names:   map[string]string{"x": "Ex", "y": "y"},
		},
		{
			name:    "two users with the slug are none",
			authors: []wxrAuthor{{login: "bob"}},
			users:   []wprest.User{user(1, "One", "bob"), user(2, "Two", "bob")},
			names:   map[string]string{"bob": "bob"},
			others:  []wprest.User{user(1, "One", "bob"), user(2, "Two", "bob")},
		},
		{
			name:    "two logins make the slug, and the first gets the user",
			authors: []wxrAuthor{{login: "Bob"}, {login: "bob."}},
			users:   []wprest.User{user(1, "The Bob", "bob")},
			names:   map[string]string{"Bob": "The Bob", "bob.": "bob."},
		},
		{
			name:    "the slug of the user is read as percent-encoded",
			authors: []wxrAuthor{{login: "山田"}},
			users:   []wprest.User{user(1, "Yamada", "%e5%b1%b1%e7%94%b0"), user(2, "Other", "%zz")},
			names:   map[string]string{"山田": "Yamada"},
			others:  []wprest.User{user(2, "Other", "%zz")},
		},
		{
			name:    "a user with no slug is found by the ID only",
			authors: []wxrAuthor{{id: 1, login: "a", display: ""}, {login: "b"}},
			users:   []wprest.User{{ID: 1, Name: "A"}, {ID: 2, Name: "B"}},
			names:   map[string]string{"a": "A", "b": "b"},
			others:  []wprest.User{{ID: 2, Name: "B"}},
		},
		{
			name:    "a login that makes no slug is not found",
			authors: []wxrAuthor{{login: "!!!"}},
			users:   []wprest.User{{ID: 1, Name: "Empty"}},
			names:   map[string]string{"!!!": "!!!"},
			others:  []wprest.User{{ID: 1, Name: "Empty"}},
		},
		{
			name:    "an ID that two authors have is the first author's",
			authors: []wxrAuthor{{id: 5, login: "p", display: ""}, {id: 5, login: "q", display: ""}},
			users:   []wprest.User{user(5, "Pee", "p")},
			names:   map[string]string{"p": "Pee", "q": "q"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logins []string
			for _, a := range tt.authors {
				logins = append(logins, a.login)
			}
			imp, profiles := makeAuthors(t, authorSite(), authorsWXR(tt.authors, postsBy(logins...)), &wprest.Snapshot{Users: tt.users})
			for login, name := range tt.names {
				if got := profileOf(t, imp, profiles, login).Name; got != name {
					t.Errorf("the name of %s = %q, want %q", login, got, name)
				}
			}
			var got, want []string
			for _, l := range reportLines(t, imp.Report) {
				if strings.Contains(l, "of the REST API is not an author") {
					got = append(got, l)
				}
			}
			for _, u := range tt.others {
				want = append(want, restSkipped(u.ID, u.Name))
			}
			sameSet(t, "the lines", got, want)
		})
	}
}

func TestAuthorsUsersUnavailable(t *testing.T) {
	in := authorsWXR(
		[]wxrAuthor{{id: 1, login: "admin", display: "Admin", email: "admin@example.com"}, {id: 2, login: "kim", display: "Kim"}},
		postsBy("admin", "kim"),
	)
	// The users that the snapshot has are not used, whatever it says about why it did not give them.
	users := []wprest.User{
		{ID: 1, Name: "Not used", Slug: "other", Description: "Not used", URL: "https://example.org/"},
		{ID: 7, Name: "Not an author"},
	}
	const route = "/wp/v2/users"
	tests := []struct {
		name string
		un   wprest.Unavailable
		file string
		why  string
	}{
		{"refused", wprest.Unavailable{Route: route, Status: 401, Code: "rest_user_cannot_view"}, "rest.json", " (401 rest_user_cannot_view)"},
		{"not JSON", wprest.Unavailable{Route: route, Status: 200, Code: "not_json"}, "rest.json", " (200 not_json)"},
		{"no code", wprest.Unavailable{Route: route, Status: 403}, "rest.json", " (403)"},
		{"no status", wprest.Unavailable{Route: route, Code: "gone"}, "rest.json", " (gone)"},
		{"nothing", wprest.Unavailable{Route: route}, "rest.json", ""},
		{"code with characters of WordPress", wprest.Unavailable{Route: route, Status: 401, Code: "My.Code-1_x"}, "rest.json", " (401 My.Code-1_x)"},
		{"code with other characters", wprest.Unavailable{Route: route, Status: 401, Code: "a b\nc"}, "rest.json", ` (401 "a b\nc")`},
		{"no name for the API", wprest.Unavailable{Route: route, Status: 401, Code: "rest_user_cannot_view"}, "", " (401 rest_user_cannot_view)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, rep := readString(t, "site.xml", in)
			imp := New("site.xml", e, rep)
			imp.REST = &wprest.Snapshot{
				Index: wprest.Index{Home: "https://example.com"}, Users: users,
				Unavailable: []wprest.Unavailable{{Route: "/wp/v2/tags", Status: 404}, tt.un},
			}
			imp.RESTFile = tt.file
			profiles, err := imp.Authors(authorSite())
			if err != nil {
				t.Fatal(err)
			}
			checkAuthors(t, imp, authorSite(), profiles)
			admin, kim := authorID(t, imp, "admin"), authorID(t, imp, "kim")
			want := []content.AuthorProfile{{ID: admin, Name: "Admin", Slug: "admin"}, {ID: kim, Name: "Kim", Slug: "kim"}}
			slices.SortFunc(want, func(a, b content.AuthorProfile) int { return strings.Compare(a.ID, b.ID) })
			if !reflect.DeepEqual(profiles, want) {
				t.Errorf("Authors = %+v, want %+v", profiles, want)
			}
			start := ""
			if tt.file != "" {
				start = tt.file + ": "
			}
			wantLines(t, rep, []string{
				start + `review: rest "/wp/v2/users": the REST API did not give the users` + tt.why +
					"; the slugs of the authors are made from their logins, and they have no bios or websites",
				authorWho(t, e, "site.xml", "review", "admin") + fmt.Sprintf(noSlugReview, "admin") + authorTarget(admin, "/slug"),
				authorWho(t, e, "site.xml", "review", "kim") + fmt.Sprintf(noSlugReview, "kim") + authorTarget(kim, "/slug"),
			})
		})
	}

	t.Run("a snapshot read from a file", func(t *testing.T) {
		snap, err := wprest.ReadSnapshot(strings.NewReader(
			`{"index": {"home": "https://example.com"}, "users": [{"id": 1, "slug": "other"}], `+
				`"unavailable": [{"route": "/wp/v2/users", "status": 401, "code": "rest_user_cannot_view"}]}`), wprest.Options{})
		if err != nil {
			t.Fatal(err)
		}
		imp, profiles := makeAuthors(t, authorSite(), in, snap)
		if len(profiles) != 2 {
			t.Errorf("%d profiles, want 2", len(profiles))
		}
		admin, kim := authorID(t, imp, "admin"), authorID(t, imp, "kim")
		wantLines(t, imp.Report, []string{
			`rest.json: review: rest "/wp/v2/users": the REST API did not give the users (401 rest_user_cannot_view); ` +
				"the slugs of the authors are made from their logins, and they have no bios or websites",
			authorWho(t, imp.WXR, "site.xml", "review", "admin") + fmt.Sprintf(noSlugReview, "admin") + authorTarget(admin, "/slug"),
			authorWho(t, imp.WXR, "site.xml", "review", "kim") + fmt.Sprintf(noSlugReview, "kim") + authorTarget(kim, "/slug"),
		})
	})
}

func TestAuthorsOtherRoutesUnavailable(t *testing.T) {
	// A route of terms that the site did not give does not stop the users.
	in := authorsWXR([]wxrAuthor{{id: 1, login: "kim", display: "Kim"}}, postsBy("kim"))
	rest := &wprest.Snapshot{
		Users:       []wprest.User{{ID: 1, Name: "Kim", Slug: "kim-k"}},
		Unavailable: []wprest.Unavailable{{Route: "/wp/v2/categories", Taxonomy: "category", Status: 401, Code: "rest_cannot_view"}, {Route: "/wp/v2/types", Status: 404}},
	}
	imp, profiles := makeAuthors(t, authorSite(), in, rest)
	if len(profiles) != 1 || profiles[0].Slug != "kim-k" {
		t.Errorf("Authors = %+v, want the slug of the REST API", profiles)
	}
	wantLines(t, imp.Report, nil)
}

func TestAuthorsEmptyCreator(t *testing.T) {
	// A post with no author, as WordPress writes it for a user that was deleted, makes no profile.
	in := authorsWXR(nil, []wxrPost{{id: 1, creator: ""}, {id: 2, creator: ""}})
	imp, profiles := makeAuthors(t, authorSite(), in, nil)
	if len(profiles) != 0 {
		t.Errorf("Authors = %+v, want none", profiles)
	}
	wantLines(t, imp.Report, nil)

	// A wp:author with no post is skipped, also in a WXR with no item at all.
	in = authorsWXR([]wxrAuthor{{id: 1, login: "idle", display: "Idle"}}, nil)
	imp, profiles = makeAuthors(t, authorSite(), in, nil)
	if len(profiles) != 0 {
		t.Errorf("Authors = %+v, want none", profiles)
	}
	wantLines(t, imp.Report, []string{authorWho(t, imp.WXR, "site.xml", "skipped", "idle") + noPosts})
}

func TestAuthorsStatuses(t *testing.T) {
	// A post that is in the trash or an automatic draft does not make its author one; any other status does.
	statuses := []string{"publish", "future", "draft", "pending", "private", "inherit", "custom", "trash", "auto-draft"}
	var authors []wxrAuthor
	var posts []wxrPost
	want := map[string]bool{}
	for i, status := range statuses {
		login := "u-" + status
		authors = append(authors, wxrAuthor{id: i + 1, login: login, display: login})
		posts = append(posts, wxrPost{id: i + 1, status: status, creator: login})
		want[login] = status != "trash" && status != "auto-draft"
	}
	// Posts of other types, and in the trash, do not count; a published post does, wherever it is in the file.
	authors = append(authors,
		wxrAuthor{id: 20, login: "mixed", display: "mixed"},
		wxrAuthor{id: 21, login: "others", display: "others"},
	)
	posts = append(posts,
		wxrPost{id: 20, status: "trash", creator: "mixed"},
		wxrPost{id: 21, typ: "page", creator: "mixed"},
		wxrPost{id: 22, typ: "Post", creator: "mixed"},
		wxrPost{id: 23, status: "publish", creator: "mixed"},
		wxrPost{id: 24, status: "trash", creator: "others"},
		wxrPost{id: 25, typ: "page", creator: "others"},
		wxrPost{id: 26, typ: "Post", creator: "others"},
		wxrPost{id: 27, typ: "works", creator: "others"},
		wxrPost{id: 28, typ: "attachment", status: "inherit", creator: "others"},
	)
	want["mixed"], want["others"] = true, false
	imp, profiles := makeAuthors(t, authorSite(), authorsWXR(authors, posts), nil)
	for login, made := range want {
		_, _, ok := imp.IDs.Lookup(UserObject(login))
		if ok != made {
			t.Errorf("the profile of %s is made: %v, want %v", login, ok, made)
		}
	}
	wantCount := 0
	for _, made := range want {
		if made {
			wantCount++
		}
	}
	if len(profiles) != wantCount {
		t.Errorf("%d profiles, want %d", len(profiles), wantCount)
	}
}

func TestAuthorsErrors(t *testing.T) {
	e, rep := readSample(t, "wordpress-6.xml")
	imp := New("wordpress-6.xml", e, rep)
	before := len(rep.Entries())

	authors, err := imp.Authors(nil)
	if err == nil || authors != nil || !strings.Contains(err.Error(), "settings of the site") {
		t.Errorf("Authors(nil) = %v, %v, want the error about the settings", authors, err)
	}
	for _, language := range []string{"", "Japanese!", "ja_JP"} {
		site := authorSite()
		site.DefaultLanguage = language
		authors, err = imp.Authors(site)
		if err == nil || authors != nil || !strings.HasPrefix(err.Error(), "the default language of the site: ") {
			t.Errorf("Authors(default language %q) = %v, %v, want an error", language, authors, err)
		}
	}
	if after := len(rep.Entries()); after != before {
		t.Errorf("the report has %d entries after the errors, want the %d it had", after, before)
	}

	// An ID that the user has as something else is an error of the import.
	e, rep = readSample(t, "wordpress-6.xml")
	imp = New("wordpress-6.xml", e, rep)
	if _, err := imp.IDs.Assign(UserObject("admin"), content.RefEntry); err != nil {
		t.Fatal(err)
	}
	if authors, err = imp.Authors(authorSite()); err == nil || authors != nil || !strings.Contains(err.Error(), "already imported") {
		t.Errorf("Authors with an ID taken as another kind = %v, %v, want an error", authors, err)
	}
}

// TestAuthorsValidationGuard makes profiles that the check of the document leaves out.
func TestAuthorsValidationGuard(t *testing.T) {
	t.Run("a bio in a language that the site lacks", func(t *testing.T) {
		site := &content.Site{Languages: []string{"en"}, DefaultLanguage: "ja"}
		in := authorsWXR([]wxrAuthor{{id: 1, login: "a", display: "A"}, {id: 2, login: "b", display: "B"}}, postsBy("a", "b"))
		rest := &wprest.Snapshot{Users: []wprest.User{
			{ID: 1, Name: "A", Slug: "a", Description: "About A"},
			{ID: 2, Name: "B", Slug: "b"},
		}}
		imp, profiles := makeAuthors(t, site, in, rest)
		if len(profiles) != 1 || profiles[0].Name != "B" {
			t.Fatalf("Authors = %+v, want only b", profiles)
		}
		id := imp.IDs.NewID(content.RefAuthor, UserObject("a").key())
		if _, _, ok := imp.IDs.Lookup(UserObject("a")); ok {
			t.Error("the author that failed the check has an ID")
		}
		if _, ok := imp.Ref(content.RefAuthor, id); ok {
			t.Error("the author that failed the check is recorded")
		}
		wantLines(t, imp.Report, []string{
			authorWho(t, imp.WXR, "site.xml", "skipped", "a") + `the author is not imported: /bio/ja: language "ja" is not in the site settings (use en)`,
		})
	})

	t.Run("a login that is only a line break", func(t *testing.T) {
		// Read does not let such a login through, but the name must come out right if one is given: no name is left, and
		// nothing is left of the slug either, so the name is the slug author.
		login := string(rune(0x2028))
		e := &wxr.Export{
			Channel: wxr.Channel{Link: "https://example.com"},
			Items:   []wxr.Item{{ID: 1, Type: "post", Status: "publish", Creator: login}},
		}
		imp := New("hand.xml", e, NewReport(Info{}))
		profiles, err := imp.Authors(authorSite())
		if err != nil {
			t.Fatal(err)
		}
		if len(profiles) != 1 || profiles[0].Name != "author" || profiles[0].Slug != "author" {
			t.Fatalf("Authors = %+v, want one profile with the name and the slug author", profiles)
		}
		checkAuthors(t, imp, authorSite(), profiles)
		at := func(kind, msg, pointer string) string {
			return fmt.Sprintf("hand.xml: %s: author %q: %s", kind, login, msg) + authorTarget(profiles[0].ID, pointer)
		}
		sameSet(t, "the lines", reportLines(t, imp.Report), []string{
			at("review", `the names of the author are empty or email addresses; the name is the slug "author" here; correct it`, "/name"),
			at("changed", fmt.Sprintf("nothing is left of the slug %q; it is %q here", login, "author"), "/slug"),
			at("review", fmt.Sprintf(noSlugReview, "author"), "/slug"),
		})
	})

	t.Run("a name that is too long", func(t *testing.T) {
		name := strings.Repeat("a", content.MaxTextBytes+1)
		in := authorsWXR([]wxrAuthor{{id: 1, login: "long", display: name}, {id: 2, login: "short", display: "Short"}}, postsBy("long", "short"))
		rest := &wprest.Snapshot{Users: []wprest.User{{ID: 1, Slug: "long"}, {ID: 2, Slug: "short"}}}
		imp, profiles := makeAuthors(t, authorSite(), in, rest)
		if len(profiles) != 1 || profiles[0].Name != "Short" {
			t.Fatalf("Authors = %d profiles, want only short", len(profiles))
		}
		if _, _, ok := imp.IDs.Lookup(UserObject("long")); ok {
			t.Error("the author that failed the check has an ID")
		}
		wantLines(t, imp.Report, []string{
			authorWho(t, imp.WXR, "site.xml", "skipped", "long") +
				fmt.Sprintf("the author is not imported: /name: too long: %d bytes (max %d)", len(name), content.MaxTextBytes),
		})
	})
}

func TestAuthorsMany(t *testing.T) {
	const n, m = 10000, 5000
	e := &wxr.Export{Channel: wxr.Channel{Link: "https://example.com"}}
	rest := &wprest.Snapshot{Index: wprest.Index{Home: "https://example.com"}}
	// Users that only dc:creator names, each found by its slug.
	for i := 1; i <= n; i++ {
		login := "user" + strconv.Itoa(i)
		e.Items = append(e.Items, wxr.Item{ID: int64(i), Type: "post", Status: "publish", Creator: login})
		rest.Users = append(rest.Users, wprest.User{ID: int64(i), Name: "User " + strconv.Itoa(i), Slug: login})
	}
	// Logins that make one slug, and as many users that have it: none is found.
	const symbols = "!#$%&'()*+"
	for i := range m {
		login := "x" + string([]byte{symbols[i/1000%10], symbols[i/100%10], symbols[i/10%10], symbols[i%10]})
		e.Items = append(e.Items, wxr.Item{ID: int64(n + 1 + i), Type: "post", Status: "publish", Creator: login})
		rest.Users = append(rest.Users, wprest.User{ID: int64(n + 1 + i), Name: "X " + strconv.Itoa(i), Slug: "x"})
	}
	imp := New("many.xml", e, NewReport(Info{}))
	imp.REST, imp.RESTFile = rest, "rest.json"
	site := authorSite()
	authors, err := imp.Authors(site)
	if err != nil {
		t.Fatal(err)
	}
	checkAuthors(t, imp, site, authors)
	if len(authors) != n+m {
		t.Fatalf("%d profiles, want %d", len(authors), n+m)
	}
	for _, i := range []int{1, n / 2, n} {
		p := profileOf(t, imp, authors, "user"+strconv.Itoa(i))
		if p.Name != "User "+strconv.Itoa(i) || p.Slug != "user"+strconv.Itoa(i) {
			t.Errorf("the profile of user%d = %q, %q", i, p.Name, p.Slug)
		}
	}
	for _, i := range []int{0, 1, m - 1} {
		login := "x" + string([]byte{symbols[i/1000%10], symbols[i/100%10], symbols[i/10%10], symbols[i%10]})
		p := profileOf(t, imp, authors, login)
		want := "x"
		if i > 0 {
			want = "x-" + strconv.Itoa(i+1)
		}
		if p.Name != login || p.Slug != want {
			t.Errorf("the profile of %q = %q, %q, want the slug %q", login, p.Name, p.Slug, want)
		}
	}
	// A name entry for each user that only dc:creator names; for the logins that make "x", the review of the slug, the other
	// slug for all but the first, and the user of the REST API that was not found.
	if got, want := len(imp.Report.Entries()), n+4*m-1; got != want {
		t.Errorf("the report has %d entries, want %d", got, want)
	}
}

func TestHasEmailAddress(t *testing.T) {
	wide := func(r rune) string { return string(r) }
	for _, tt := range []struct {
		in   string
		want bool
	}{
		{"a@b.c", true},
		{"mary@example.org", true},
		{"John <john@example.com>", true},
		{"Write to john@example.com or call", true},
		{"メール@例.jp", true},
		{"a@b@c.d", true},
		{"a@b..c", true},
		{"mailto:someone@example.org", true},
		{"https://user:pw@example.org/", true},
		{wide(0xFF41) + "@" + wide(0xFF42) + "." + wide(0xFF43), true}, // full-width letters with an @ and a . of ASCII
		{"@john.doe", false},
		{"john@localhost", false},
		{"a@.b", false},
		{"a@b.", false},
		{"a @b.c", false},
		{"a@ b.c", false},
		{"a@b .c", false},
		{"@@a.b", false},
		{"@", false},
		{"a@", false},
		{"x.y", false},
		{"Tom & Jerry", false},
		{"a" + wide(0xFF20) + "b.c", false},  // a full-width @ is not one
		{"a" + wide(0x3000) + "@b.c", false}, // an ideographic space before the @
		{"", false},
	} {
		if got := hasEmailAddress(tt.in); got != tt.want {
			t.Errorf("hasEmailAddress(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestIsAuthorEmail(t *testing.T) {
	for _, tt := range []struct {
		s, email string
		want     bool
	}{
		{"Secret@Example.com ", "secret@example.com", true},
		{" secret@example.com", " SECRET@example.com\n", true},
		{"JDOE@localhost", "jdoe@localhost", true},
		{"jdoe", "jdoe@localhost", false},
		{"secret@example.com", "other@example.com", false},
		{"x", "", false},
		{"", "", false},
		{"  ", "  ", false},
	} {
		if got := isAuthorEmail(tt.s, tt.email); got != tt.want {
			t.Errorf("isAuthorEmail(%q, %q) = %v, want %v", tt.s, tt.email, got, tt.want)
		}
	}
}

// TestAuthorsEmailNames checks that an email address is not the name of a profile, and that the report names no login that
// is an email address, or the email address of the author: it shows such an author by the ID, or by the slug that the
// login makes if there is no ID.
func TestAuthorsEmailNames(t *testing.T) {
	const noName = "the names of the author are empty or email addresses; the name is the slug %q here; correct it"
	for _, tt := range []struct {
		name         string
		in           string
		rest         *wprest.Snapshot
		login, shown string // the author to look at, and how the report shows it: the quoted login, or the ID
		nameAndSlug  [2]string
		// lines are the lines of the report: who starts the ones about the author, and at ends them at a pointer.
		lines func(who func(kind string) string, at func(pointer string) string) []string
	}{
		{
			name:  "the login, the display name and the email address are the same",
			in:    authorsWXR([]wxrAuthor{{id: 3, login: "mary@example.org", display: "mary@example.org", email: "mary@example.org"}}, postsBy("mary@example.org")),
			login: "mary@example.org", shown: "3", nameAndSlug: [2]string{"maryexample-org", "maryexample-org"},
			lines: func(who func(string) string, at func(string) string) []string {
				return []string{
					who("review") + `the names of the author are empty or email addresses; the name is the slug "maryexample-org" here; correct it` + at("/name"),
					who("review") + fmt.Sprintf(noSlugReview, "maryexample-org") + at("/slug"),
				}
			},
		},
		{
			name:  "a login that is an email address, and no display name",
			in:    authorsWXR([]wxrAuthor{{id: 4, login: "john@example.com", email: "john@example.com"}}, postsBy("john@example.com")),
			login: "john@example.com", shown: "4", nameAndSlug: [2]string{"johnexample-com", "johnexample-com"},
			lines: func(who func(string) string, at func(string) string) []string {
				return []string{
					who("review") + fmt.Sprintf(noName, "johnexample-com") + at("/name"),
					who("review") + fmt.Sprintf(noSlugReview, "johnexample-com") + at("/slug"),
				}
			},
		},
		{
			name:  "a display name that is the email address, but does not look like one",
			in:    authorsWXR([]wxrAuthor{{id: 5, login: "jdoe", display: "JDOE@localhost", email: "jdoe@localhost"}}, postsBy("jdoe")),
			login: "jdoe", shown: `"jdoe"`, nameAndSlug: [2]string{"jdoe", "jdoe"},
			lines: func(who func(string) string, at func(string) string) []string {
				return []string{
					who("changed") + `the display name is an email address; the name is "jdoe" here` + at("/name"),
					who("review") + fmt.Sprintf(noSlugReview, "jdoe") + at("/slug"),
				}
			},
		},
		{
			name:  "a display name with an email address in it, and the name of the REST API",
			in:    authorsWXR([]wxrAuthor{{id: 6, login: "bob", display: "Bob <bob@example.net>"}}, postsBy("bob")),
			rest:  &wprest.Snapshot{Users: []wprest.User{{ID: 6, Name: "Bob B."}}},
			login: "bob", shown: `"bob"`, nameAndSlug: [2]string{"Bob B.", "bob"},
			lines: func(who func(string) string, at func(string) string) []string {
				return []string{
					who("changed") + `the display name is an email address; the name is "Bob B." here` + at("/name"),
					who("review") + fmt.Sprintf(noSlugReview, "bob") + at("/slug"),
				}
			},
		},
		{
			name:  "a display name that is a character reference of an email address",
			in:    authorsWXR([]wxrAuthor{{id: 2, login: "ann", display: "ann&#64;example.org", email: "ann@example.org"}}, postsBy("ann")),
			login: "ann", shown: `"ann"`, nameAndSlug: [2]string{"ann", "ann"},
			lines: func(who func(string) string, at func(string) string) []string {
				return []string{
					who("changed") + `the display name is an email address; the name is "ann" here` + at("/name"),
					who("review") + fmt.Sprintf(noSlugReview, "ann") + at("/slug"),
				}
			},
		},
		{
			name:  "no display name, and the name of the REST API is an email address",
			in:    authorsWXR([]wxrAuthor{{id: 7, login: "carol"}}, postsBy("carol")),
			rest:  &wprest.Snapshot{Users: []wprest.User{{ID: 7, Name: "carol@example.net"}}},
			login: "carol", shown: `"carol"`, nameAndSlug: [2]string{"carol", "carol"},
			lines: func(who func(string) string, at func(string) string) []string {
				return []string{
					who("changed") + `the display name is empty; the name is "carol" here` + at("/name"),
					who("review") + fmt.Sprintf(noSlugReview, "carol") + at("/slug"),
				}
			},
		},
		{
			name:  "a user that only dc:creator names, whose login is an email address",
			in:    authorsXML(head10, nil, []wxrPost{{id: 1, creator: "dave@example.com"}}),
			login: "dave@example.com", shown: `"daveexample-com"`, nameAndSlug: [2]string{"daveexample-com", "daveexample-com"},
			lines: func(who func(string) string, at func(string) string) []string {
				return []string{
					who("review") + fmt.Sprintf(noName, "daveexample-com") + at("/name"),
					who("review") + fmt.Sprintf(noSlugReview, "daveexample-com") + at("/slug"),
				}
			},
		},
		{
			name:  "a login that is the email address, but does not look like one",
			in:    authorsWXR([]wxrAuthor{{id: 8, login: "erin@localhost", display: "Erin", email: "erin@localhost"}}, postsBy("erin@localhost")),
			login: "erin@localhost", shown: "8", nameAndSlug: [2]string{"Erin", "erinlocalhost"},
			lines: func(who func(string) string, at func(string) string) []string {
				return []string{who("review") + fmt.Sprintf(noSlugReview, "erinlocalhost") + at("/slug")}
			},
		},
		{
			name: "users of the REST API whose names are email addresses, and that no author is",
			in:   authorsWXR([]wxrAuthor{{id: 1, login: "kim", display: "Kim"}}, postsBy("kim")),
			rest: &wprest.Snapshot{Users: []wprest.User{
				{ID: 9, Name: "frank@example.com", Slug: "frank"}, {ID: 10, Name: "gina&#64;example.com", Slug: "gina"},
			}},
			login: "kim", shown: `"kim"`, nameAndSlug: [2]string{"Kim", "kim"},
			lines: func(who func(string) string, at func(string) string) []string {
				return []string{
					who("review") + fmt.Sprintf(noSlugReview, "kim") + at("/slug"),
					"rest.json: skipped: rest 9 (user): " + fmt.Sprintf(notAuthor, 9),
					"rest.json: skipped: rest 10 (user): " + fmt.Sprintf(notAuthor, 10),
				}
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			imp, profiles := makeAuthors(t, authorSite(), tt.in, tt.rest)
			if len(profiles) != 1 {
				t.Fatalf("%d profiles, want 1", len(profiles))
			}
			p := profileOf(t, imp, profiles, tt.login)
			if p.Name != tt.nameAndSlug[0] || p.Slug != tt.nameAndSlug[1] {
				t.Errorf("the profile has the name %q and the slug %q, want %q and %q", p.Name, p.Slug, tt.nameAndSlug[0], tt.nameAndSlug[1])
			}
			who := func(kind string) string { return authorWhoAs(t, imp.WXR, "site.xml", kind, tt.login, tt.shown) }
			at := func(pointer string) string { return authorTarget(p.ID, pointer) }
			wantLines(t, imp.Report, tt.lines(who, at))

			// Neither the profiles nor the report, in the text and in JSON, have an @.
			var out strings.Builder
			for _, p := range profiles {
				j, err := p.MarshalJSON()
				if err != nil {
					t.Fatal(err)
				}
				out.Write(j)
			}
			var text, js bytes.Buffer
			if err := imp.Report.WriteText(&text); err != nil {
				t.Fatal(err)
			}
			if err := imp.Report.WriteJSON(&js); err != nil {
				t.Fatal(err)
			}
			out.Write(text.Bytes())
			out.Write(js.Bytes())
			if strings.Contains(out.String(), "@") {
				t.Errorf("an @ is in the profiles or the report:\n%s", out.String())
			}
		})
	}
}

func TestAuthorsSlugFromRESTFirst(t *testing.T) {
	// WordPress gave john-doe to the user with posts, and john-doe-2 to the one with a draft only, who is not in the REST
	// API. The login of the latter makes john-doe too, and the WXR has it first: the slug of the REST API is kept.
	in := authorsWXR(
		[]wxrAuthor{{id: 2, login: "john.doe", display: "John D."}, {id: 1, login: "john-doe", display: "John Doe"}},
		[]wxrPost{{id: 1, status: "draft", creator: "john.doe"}, {id: 2, creator: "john-doe"}},
	)
	rest := &wprest.Snapshot{Users: []wprest.User{{ID: 1, Name: "John Doe", Slug: "john-doe"}}}
	imp, profiles := makeAuthors(t, authorSite(), in, rest)
	if got := profileOf(t, imp, profiles, "john-doe").Slug; got != "john-doe" {
		t.Errorf("the slug of john-doe = %q, want john-doe", got)
	}
	second := profileOf(t, imp, profiles, "john.doe")
	if second.Slug != "john-doe-2" {
		t.Errorf("the slug of john.doe = %q, want john-doe-2", second.Slug)
	}
	wantLines(t, imp.Report, []string{
		authorWho(t, imp.WXR, "site.xml", "review", "john.doe") + fmt.Sprintf(noSlugReview, "john-doe") + authorTarget(second.ID, "/slug"),
		authorWho(t, imp.WXR, "site.xml", "changed", "john.doe") +
			`the slug "john-doe" is also that of another author; it is "john-doe-2" here` + authorTarget(second.ID, "/slug"),
	})
}

func TestAuthorsSlugOfTheRESTBeforeTheLogins(t *testing.T) {
	// Two users of the REST API have the slug x, which WordPress does not do, and the WXR has an author between them whose
	// login makes x-2. The one that could not take x is the first to be given a number.
	in := authorsWXR(
		[]wxrAuthor{{id: 1, login: "a1", display: "A1"}, {id: 2, login: "x.2", display: "X2"}, {id: 3, login: "a3", display: "A3"}},
		postsBy("a1", "x.2", "a3"),
	)
	rest := &wprest.Snapshot{Users: []wprest.User{{ID: 1, Name: "A1", Slug: "x"}, {ID: 3, Name: "A3", Slug: "x"}}}
	imp, profiles := makeAuthors(t, authorSite(), in, rest)
	for login, want := range map[string]string{"a1": "x", "x.2": "x-2-2", "a3": "x-2"} {
		if got := profileOf(t, imp, profiles, login).Slug; got != want {
			t.Errorf("the slug of %s = %q, want %q", login, got, want)
		}
	}
	line := func(kind Kind, login, msg string) string {
		return authorWho(t, imp.WXR, "site.xml", string(kind), login) + msg + authorTarget(authorID(t, imp, login), "/slug")
	}
	wantLines(t, imp.Report, []string{
		line(Review, "x.2", fmt.Sprintf(noSlugReview, "x-2")),
		line(Changed, "x.2", `the slug "x-2" is also that of another author; it is "x-2-2" here`),
		line(Changed, "a3", `the slug "x" is also that of another author; it is "x-2" here`),
	})
}

func FuzzAuthors(f *testing.F) {
	f.Add("alice", "Alice", "alice", "<p>Hi <b>there</b></p>", "https://example.org/", false, "")
	f.Add("山田", "Tom &amp; Jerry", "%e5%b1%b1%e7%94%b0", "&lt;b&gt;", "javascript:alert(1)", false, "")
	f.Add("!!!", "", "%zz", strings.Repeat("<div>", 3000), "mailto:a@b.c", false, "")
	f.Add("x y", "A\nB\tC", "", "line 1\r\nline 2", "  http://example.org  ", true, "My Code")
	f.Add("bob", " ", "Upper Case", "<img src=\"/a.png\"><a href=\"/x\">y</a>", "//example.org", true, "a\nb")
	f.Add("mary@example.org", "mary@example.org", "mary", "", "", false, "")
	f.Add("secret", " Secret@Example.com ", "secret", "", "https://user:pw@example.org/", false, "")
	f.Add("a@b.c", "A", "a", "", "mailto:secret@example.com", false, "")
	f.Fuzz(func(t *testing.T, login, display, slug, desc, link string, blocked bool, code string) {
		const email = "secret@example.com"
		for _, s := range []string{login, display, slug, desc, link, code} {
			if !utf8.ValidString(s) {
				t.Skip()
			}
		}
		// The REST API is public, so what it says comes out as it is, also if it is the email address of the WXR.
		for _, s := range []string{slug, desc, code} {
			if strings.Contains(strings.ToLower(s), email) {
				t.Skip()
			}
		}
		if login == "" {
			t.Skip()
		}
		e := &wxr.Export{
			Channel: wxr.Channel{Link: "https://example.com"},
			Authors: []wxr.Author{{ID: 1, Login: login, DisplayName: display, Email: email}},
			Items:   []wxr.Item{{ID: 1, Type: "post", Status: "publish", Creator: login}},
		}
		rest := &wprest.Snapshot{
			Index: wprest.Index{Home: "https://example.com"},
			Users: []wprest.User{
				{ID: 1, Name: display, Slug: slug, Description: desc, URL: link},
				{ID: 2, Name: login, Slug: login},
			},
		}
		if blocked {
			rest.Unavailable = []wprest.Unavailable{{Route: "/wp/v2/users", Status: 401, Code: code}}
		}
		imp := New("f.xml", e, NewReport(Info{}))
		imp.REST, imp.RESTFile = rest, "rest.json"
		site := authorSite()
		authors, err := imp.Authors(site)
		if err != nil {
			t.Fatalf("Authors = %v", err)
		}
		if len(authors) > 1 {
			t.Fatalf("%d profiles for one author", len(authors))
		}
		for _, a := range authors {
			if err := a.Validate(site, imp.Ref); err != nil {
				t.Errorf("the profile does not pass Validate: %v", err)
			}
			if _, ok := imp.Ref(content.RefAuthor, a.ID); !ok {
				t.Error("the profile is not recorded")
			}
			if hasEmailAddress(a.Name) || isAuthorEmail(a.Name, email) {
				t.Errorf("the name %q is an email address", a.Name)
			}
		}
		var text, js bytes.Buffer
		if err := imp.Report.WriteText(&text); err != nil {
			t.Fatal(err)
		}
		if err := imp.Report.WriteJSON(&js); err != nil {
			t.Fatal(err)
		}
		if lines, entries := strings.Count(text.String(), "\n"), len(imp.Report.Entries()); lines != entries {
			t.Errorf("%d lines for %d entries:\n%s", lines, entries, text.String())
		}
		for _, out := range []string{text.String(), js.String()} {
			if strings.Contains(strings.ToLower(out), email) {
				t.Errorf("the email address is in the report:\n%s", out)
			}
		}
		for _, en := range imp.Report.Entries() {
			src := en.Source
			if (src.Record == recordAuthor || src.Record == recordREST && src.Type == "user") && (hasEmailAddress(src.Name) || isAuthorEmail(src.Name, email)) {
				t.Errorf("an entry names %q, an email address: %s", src.Name, en.Message)
			}
			if src.Record == recordAuthor && hidesLogin(login, email) && (src.ID != 1 || src.Name != "") {
				t.Errorf("an entry names the author %d %q, whose login %q is not to be named", src.ID, src.Name, login)
			}
			if en.Target.Pointer == "/links" && hasEmailAddress(content.NormalizeHref(link)) && strings.Contains(en.Message, "@") {
				t.Errorf("the entry about the website has the email address in it: %s", en.Message)
			}
		}
	})
}
