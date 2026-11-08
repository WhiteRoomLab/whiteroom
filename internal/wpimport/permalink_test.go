package wpimport

import (
	"cmp"
	"fmt"
	"maps"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/wprest"
	"github.com/WhiteRoomLab/whiteroom/internal/wxr"
)

// wpSite is a WordPress site that writes its links as WordPress does: the structure, the bases, the types and the terms
// are its settings, and export and snapshot give what its WXR and its REST API tell.
type wpSite struct {
	home      string
	structure string // permalink_structure; "" is Plain
	catBase   string
	tagBase   string
	frontPage int64 // page_on_front
	users     []wpUser
	terms     []wpTerm
	items     []wpItem
	types     map[string]wpType
	taxes     map[string]wpTax
}

type wpUser struct {
	id          int64
	login, slug string
}

type wpTerm struct {
	id     int64
	tax    string // as WordPress names it: category, post_tag, genre
	slug   string // as written
	parent int64
}

type wpItem struct {
	id     int64
	typ    string // post, page, or the name of a custom type
	status string // publish if empty
	slug   string // as written
	date   string // post_date
	parent int64
	author string // the login
	terms  []int64
	link   string // the link instead of the one that WordPress writes, if it is not empty
}

type wpType struct {
	slug         string // the slug of the rewrite; the name if empty
	noFront      bool   // with_front is false
	hierarchical bool
	archive      wprest.Archive // has_archive
}

type wpTax struct {
	hierarchical bool
	tree         bool // its links have the slugs of the ancestors
}

func (w *wpSite) indexed() bool { return strings.HasPrefix(w.structure, "/index.php") }

// front is the text of the structure before its first tag.
func (w *wpSite) front() string {
	if i := strings.IndexByte(w.structure, '%'); i >= 0 {
		return w.structure[:i]
	}
	return "/"
}

func (w *wpSite) root() string {
	if w.indexed() {
		return "/index.php/"
	}
	return "/"
}

// at returns the link to path, with the "/" at its end that the structure asks for.
func (w *wpSite) at(path string) string {
	path = strings.TrimRight(path, "/")
	if strings.HasSuffix(w.structure, "/") {
		path += "/"
	}
	return w.home + path
}

func (w *wpSite) item(id int64) *wpItem {
	for i := range w.items {
		if w.items[i].id == id {
			return &w.items[i]
		}
	}
	panic("no item " + strconv.FormatInt(id, 10))
}

func (w *wpSite) term(id int64) *wpTerm {
	for i := range w.terms {
		if w.terms[i].id == id {
			return &w.terms[i]
		}
	}
	panic("no term " + strconv.FormatInt(id, 10))
}

func (w *wpSite) user(login string) wpUser {
	for _, u := range w.users {
		if u.login == login {
			return u
		}
	}
	panic("no user " + login)
}

// uri is the path of a page or an item of a hierarchical type: the slugs of its ancestors, and its own.
func (w *wpSite) uri(it wpItem) string {
	parts := []string{it.slug}
	for p := it.parent; p != 0; p = w.item(p).parent {
		parts = append([]string{w.item(p).slug}, parts...)
	}
	return strings.Join(parts, "/")
}

func (w *wpSite) termPath(t wpTerm) string {
	parts := []string{t.slug}
	for p := t.parent; p != 0; p = w.term(p).parent {
		parts = append([]string{w.term(p).slug}, parts...)
	}
	return strings.Join(parts, "/")
}

// postCategory is the %category% of a post: the category with the lowest ID and its ancestors, or only the slug of the
// default category.
func (w *wpSite) postCategory(it wpItem) string {
	var best *wpTerm
	for _, id := range it.terms {
		if t := w.term(id); t.tax == "category" && (best == nil || t.id < best.id) {
			best = t
		}
	}
	if best == nil {
		return w.term(1).slug
	}
	return w.termPath(*best)
}

func plainStatus(s string) bool {
	return s == "draft" || s == "pending" || s == "auto-draft" || s == "future"
}

// link is the link of the item as get_permalink writes it.
func (w *wpSite) link(it wpItem) string {
	switch {
	case it.link != "":
		return it.link
	case it.typ == "page" && it.id == w.frontPage:
		return w.home + "/"
	case w.structure == "" || plainStatus(it.status):
		return fmt.Sprintf("%s/?p=%d", w.home, it.id)
	case it.typ == "post":
		d := it.date
		return w.home + strings.NewReplacer(
			"%year%", d[0:4], "%monthnum%", d[5:7], "%day%", d[8:10], "%hour%", d[11:13], "%minute%", d[14:16], "%second%", d[17:19],
			"%postname%", it.slug, "%post_id%", strconv.FormatInt(it.id, 10), "%category%", w.postCategory(it),
			"%author%", w.user(it.author).slug,
		).Replace(w.structure)
	case it.typ == "page":
		return w.at(w.root() + w.uri(it))
	}
	ty := w.types[it.typ]
	prefix := w.front()
	if ty.noFront {
		prefix = w.root()
	}
	name := it.slug
	if ty.hierarchical {
		name = w.uri(it)
	}
	return w.at(prefix + cmp.Or(ty.slug, it.typ) + "/" + name)
}

// base is the text before the slugs of the terms of a taxonomy with a base, which ends with "/".
func (w *wpSite) base(base, def string) string {
	switch {
	case base == "":
		return w.front() + def + "/"
	case w.indexed():
		return w.front() + base + "/"
	}
	return w.root() + base + "/"
}

func (w *wpSite) termLink(t wpTerm) string {
	if w.structure == "" {
		return fmt.Sprintf("%s/?term=%d", w.home, t.id)
	}
	switch t.tax {
	case "category":
		return w.at(w.base(w.catBase, "category") + w.termPath(t))
	case "post_tag":
		return w.at(w.base(w.tagBase, "tag") + t.slug)
	}
	name := t.slug
	if w.taxes[t.tax].tree {
		name = w.termPath(t)
	}
	return w.at(w.front() + t.tax + "/" + name)
}

func (w *wpSite) authorLink(u wpUser) string {
	if w.structure == "" {
		return fmt.Sprintf("%s/?author=%d", w.home, u.id)
	}
	return w.at(w.front() + "author/" + u.slug)
}

// export is the WXR of the site.
func (w *wpSite) export() *wxr.Export {
	e := &wxr.Export{Version: "1.2", Channel: wxr.Channel{Title: "Example", Link: w.home, BaseBlogURL: w.home}}
	for _, u := range w.users {
		e.Authors = append(e.Authors, wxr.Author{ID: u.id, Login: u.login, DisplayName: u.login})
	}
	for _, t := range w.terms {
		wt := wxr.Term{ID: t.id, Taxonomy: t.tax, Slug: t.slug, Name: t.slug}
		if t.parent != 0 {
			wt.Parent = w.term(t.parent).slug
		}
		e.Terms = append(e.Terms, wt)
	}
	for _, it := range w.items {
		wi := wxr.Item{
			ID: it.id, Type: it.typ, Status: cmp.Or(it.status, "publish"), Title: it.slug, Name: it.slug, Link: w.link(it),
			Date: it.date, Creator: it.author, Parent: it.parent,
		}
		for _, id := range it.terms {
			t := w.term(id)
			wi.Terms = append(wi.Terms, wxr.TermRef{Taxonomy: t.tax, Slug: t.slug, Name: t.slug})
		}
		e.Items = append(e.Items, wi)
	}
	return e
}

// snapshot is what the REST API of the site tells.
func (w *wpSite) snapshot() *wprest.Snapshot {
	s := &wprest.Snapshot{API: w.home + "/wp-json/", Index: wprest.Index{Name: "Example", Home: w.home}}
	s.Taxonomies = []wprest.Taxonomy{
		{Slug: "category", Hierarchical: true, RestBase: "categories"}, {Slug: "post_tag", RestBase: "tags"},
	}
	for _, name := range slices.Sorted(maps.Keys(w.taxes)) {
		s.Taxonomies = append(s.Taxonomies, wprest.Taxonomy{Slug: name, Hierarchical: w.taxes[name].hierarchical, RestBase: name})
	}
	for _, name := range slices.Sorted(maps.Keys(w.types)) {
		ty := w.types[name]
		s.Types = append(s.Types, wprest.Type{Slug: name, Hierarchical: ty.hierarchical, HasArchive: ty.archive, RestBase: name})
	}
	for _, t := range w.terms {
		s.Terms = append(s.Terms, wprest.Term{ID: t.id, Taxonomy: t.tax, Slug: t.slug, Name: t.slug, Parent: t.parent, Link: w.termLink(t)})
	}
	for _, u := range w.users {
		s.Users = append(s.Users, wprest.User{ID: u.id, Name: u.login, Slug: u.slug, Link: w.authorLink(u)})
	}
	return s
}

// baseSite is a site with six posts that are published and a draft, four pages (one is the front page), two items of the
// custom type works, three categories (one has the other as its parent), two tags and two authors.
func baseSite() *wpSite {
	return &wpSite{
		home:      "https://example.com",
		frontPage: 24,
		users:     []wpUser{{1, "admin", "admin"}, {2, "editor", "editor"}},
		terms: []wpTerm{
			{id: 1, tax: "category", slug: "uncategorized"},
			{id: 2, tax: "category", slug: "news"},
			{id: 3, tax: "category", slug: "press-release", parent: 2},
			{id: 4, tax: "post_tag", slug: "wp"},
			{id: 5, tax: "post_tag", slug: "go"},
		},
		items: []wpItem{
			{id: 11, typ: "post", slug: "hello-world", date: "2026-10-26 09:30:00", author: "admin", terms: []int64{1}},
			{id: 12, typ: "post", slug: "second-post", date: "2026-10-27 10:15:20", author: "admin", terms: []int64{2}},
			{id: 13, typ: "post", slug: "third-post", date: "2026-11-02 14:05:45", author: "editor", terms: []int64{3}},
			{id: 14, typ: "post", slug: "fourth-post", date: "2026-11-03 08:00:00", author: "editor", terms: []int64{2, 3}},
			{id: 15, typ: "post", slug: "fifth-post", date: "2026-11-04 23:59:59", author: "admin", terms: []int64{1}},
			{id: 16, typ: "post", slug: "sixth-post", date: "2026-12-01 00:00:01", author: "admin"},
			{id: 17, typ: "post", status: "draft", slug: "draft-post", date: "2026-12-02 12:00:00", author: "admin", terms: []int64{1}},
			{id: 21, typ: "page", slug: "about"},
			{id: 22, typ: "page", slug: "access", parent: 21},
			{id: 23, typ: "page", slug: "contact"},
			{id: 24, typ: "page", slug: "home"},
			{id: 31, typ: "works", slug: "foo"},
			{id: 32, typ: "works", slug: "bar"},
		},
		types: map[string]wpType{"works": {archive: wprest.Archive{Set: true}}},
		taxes: map[string]wpTax{},
	}
}

func permDefs() *content.Site {
	return &content.Site{Types: []content.TypeDef{{Name: "works", Label: "Works", Body: true}}}
}

// guess runs InferPermalinks on the site, with the REST API if rest, and returns the result with the report.
func (w *wpSite) guess(t *testing.T, defs *content.Site, rest bool) (*Permalinks, *Report) {
	t.Helper()
	var snap *wprest.Snapshot
	if rest {
		snap = w.snapshot()
	}
	return w.guessWith(t, defs, snap)
}

// guessWith runs InferPermalinks on the site with the REST API snap, which can be nil.
func (w *wpSite) guessWith(t *testing.T, defs *content.Site, snap *wprest.Snapshot) (*Permalinks, *Report) {
	t.Helper()
	rep := NewReport(Info{File: "site.xml"})
	imp := New("site.xml", w.export(), rep)
	if snap != nil {
		imp.REST, imp.RESTFile = snap, "rest.json"
	}
	pl, err := imp.InferPermalinks(defs)
	if err != nil {
		t.Fatalf("InferPermalinks = %v", err)
	}
	return pl, rep
}

// The places of the rules in the schema file.
const (
	ptPost     = "/types/post/url"
	ptPage     = "/types/page/url"
	ptWorks    = "/types/works/url"
	ptCategory = "/taxonomies/category/url"
	ptTag      = "/taxonomies/tag/url"
	ptAuthor   = "/authorUrl"
)

// permLine is the line of the report for the message msg about the rule or the path at pointer.
func permLine(pointer, msg string) string {
	return "site.xml: review: channel: " + msg + " -> site/schema.yaml#" + pointer
}

// permInferred is the line for a structure that all the links of p follow.
func permInferred(pointer, what string, p Permalink) string {
	return permLine(pointer, fmt.Sprintf("inferred the permalink structure %q of %s from %d of %d links; the URL rule is %q",
		p.Structure, what, p.Links, p.Links, p.Rule))
}

func permNoLink(pointer, what, rule string) string {
	return permLine(pointer, fmt.Sprintf("no link of %s has a path (the permalinks may be Plain); the URL rule is the default %q", what, rule))
}

func permMissingMsg(what, rule, front string) string {
	return fmt.Sprintf("the REST API did not give the links of %s; the URL rule is %q, the default of WordPress for the front %q", what, rule, front)
}

func permMissing(pointer, what, rule, front string) string {
	return permLine(pointer, permMissingMsg(what, rule, front))
}

// permAll is a Permalink with a structure that all the n links follow.
func permAll(structure, rule string, n int) Permalink {
	return Permalink{Structure: structure, Rule: rule, Links: n, Matched: n, RuleMatched: n}
}

// permWant is what a guess should tell.
type permWant struct {
	front                                    string
	post, page, author, works, category, tag Permalink
	defaultCategory                          string
	lines                                    []string
}

// permOthers is what the guess tells of the pages other than posts in the base site, when the structure ends with "/" if
// slash, and the pages have front before their slugs, as the author pages do, and root before the pages.
func permOthers(front, root string, slash bool) permWant {
	end := ""
	if slash {
		end = "/"
	}
	return permWant{
		front:    front,
		page:     permAll(root+"%pagename%"+end, root+"{path}/", 3),
		author:   permAll(front+"author/%author%"+end, front+"author/{slug}/", 2),
		works:    permAll(front+"works/%works%"+end, front+"works/{slug}/", 2),
		category: permAll(front+"category/%category%"+end, front+"category/{path}/", 3),
		tag:      permAll(front+"tag/%post_tag%"+end, front+"tag/{slug}/", 2),
	}
}

// othersLines are the lines for the pages that permOthers tells of.
func (w permWant) othersLines() []string {
	return []string{
		permInferred(ptPage, "pages", w.page),
		permInferred(ptAuthor, "author pages", w.author),
		permInferred(ptWorks, "the post type works", w.works),
		permInferred(ptCategory, "the taxonomy category", w.category),
		permInferred(ptTag, "the taxonomy tag", w.tag),
	}
}

// publicPermalink is p without what only the later phases read.
func publicPermalink(p Permalink) Permalink {
	p.tokens = nil
	return p
}

func samePermalink(a, b Permalink) bool {
	return reflect.DeepEqual(publicPermalink(a), publicPermalink(b))
}

// checkPermalinks compares the guess with want, and checks that every rule passes the validation of the site settings.
func checkPermalinks(t *testing.T, got *Permalinks, rep *Report, defs *content.Site, want permWant) {
	t.Helper()
	if got.Front != want.front {
		t.Errorf("Front = %q, want %q", got.Front, want.front)
	}
	for _, c := range []struct {
		name      string
		got, want Permalink
	}{
		{"Post", got.Post, want.post}, {"Page", got.Page, want.page}, {"Author", got.Author, want.author},
		{"Types[works]", got.Types["works"], want.works},
		{"Taxonomies[category]", got.Taxonomies["category"], want.category},
		{"Taxonomies[tag]", got.Taxonomies["tag"], want.tag},
	} {
		if g := publicPermalink(c.got); !reflect.DeepEqual(g, c.want) {
			t.Errorf("%s = %+v\nwant %+v", c.name, g, c.want)
		}
	}
	if len(got.Types) != 1 || len(got.Taxonomies) != 2 {
		t.Errorf("%d types and %d taxonomies, want 1 and 2", len(got.Types), len(got.Taxonomies))
	}
	if got.DefaultCategory != want.defaultCategory {
		t.Errorf("DefaultCategory = %q, want %q", got.DefaultCategory, want.defaultCategory)
	}
	wantLines(t, rep, want.lines)
	checkPermalinkRules(t, got, defs)
}

// checkPermalinkRules checks that the site settings with the rules of pl, and the schema of defs, are valid.
func checkPermalinkRules(t *testing.T, pl *Permalinks, defs *content.Site) {
	t.Helper()
	s := &content.Site{
		Name: content.Localized{"en": "Example"}, URL: "https://example.com", TimeZone: "UTC",
		Languages: []string{"en"}, DefaultLanguage: "en",
		Types: slices.Clone(defs.Types), Taxonomies: slices.Clone(defs.Taxonomies), AuthorURL: pl.Author.Rule,
	}
	setType := func(name, rule, archive string) {
		i := slices.IndexFunc(s.Types, func(d content.TypeDef) bool { return d.Name == name })
		if i < 0 {
			s.Types = append(s.Types, content.TypeDef{Name: name})
			i = len(s.Types) - 1
		}
		s.Types[i].URL, s.Types[i].Archive = rule, archive
	}
	setType(content.TypePost, pl.Post.Rule, "")
	setType(content.TypePage, pl.Page.Rule, "")
	for name, p := range pl.Types {
		setType(name, p.Rule, p.Archive)
	}
	for name, p := range pl.Taxonomies {
		i := slices.IndexFunc(s.Taxonomies, func(d content.TaxonomyDef) bool { return d.Name == name })
		if i < 0 {
			s.Taxonomies = append(s.Taxonomies, content.TaxonomyDef{Name: name})
			i = len(s.Taxonomies) - 1
		}
		s.Taxonomies[i].URL = p.Rule
	}
	if err := s.Validate(nil); err != nil {
		t.Errorf("the rules do not pass the validation of the site settings: %v", err)
	}
	all := []Permalink{pl.Post, pl.Page, pl.Author}
	all = slices.AppendSeq(all, maps.Values(pl.Types))
	all = slices.AppendSeq(all, maps.Values(pl.Taxonomies))
	for _, p := range all {
		switch {
		case p.Matched > p.Links || p.RuleMatched > p.Links:
			t.Errorf("%+v matches more links than it looked at", publicPermalink(p))
		case p.Structure == "" && (p.Matched != 0 || p.RuleMatched != 0 || p.tokens != nil):
			t.Errorf("%+v has no structure but matches links", publicPermalink(p))
		}
	}
}

func TestPermalinkSettings(t *testing.T) {
	const (
		dayName   = "/%year%/%monthnum%/%day%/%postname%/"
		monthName = "/%year%/%monthnum%/%postname%/"
	)
	// The rows named 6.10.4 are the settings of the table of 6.10.4: Plain, day and name, month and name, numeric, post name,
	// and a custom structure. The others are the structures with something more to guess.
	tests := []struct {
		name string
		rest bool // the REST API is used
		make func() (*wpSite, permWant)
	}{
		{"6.10.4 plain", true, func() (*wpSite, permWant) {
			w := baseSite()
			want := permWant{
				front: "/", post: Permalink{Rule: "/{yyyy}/{mm}/{dd}/{slug}/"}, page: Permalink{Rule: "/{path}/"},
				author: Permalink{Rule: "/author/{slug}/"}, works: Permalink{Rule: "/works/{slug}/"},
				category: Permalink{Rule: "/category/{path}/"}, tag: Permalink{Rule: "/tag/{slug}/"},
			}
			want.lines = []string{
				permNoLink(ptPost, "posts", want.post.Rule), permNoLink(ptPage, "pages", want.page.Rule),
				permNoLink(ptAuthor, "author pages", want.author.Rule), permNoLink(ptWorks, "the post type works", want.works.Rule),
				permNoLink(ptCategory, "the taxonomy category", want.category.Rule), permNoLink(ptTag, "the taxonomy tag", want.tag.Rule),
			}
			return w, want
		}},
		{"6.10.4 day and name", true, func() (*wpSite, permWant) {
			w := baseSite()
			w.structure = dayName
			want := permOthers("/", "/", true)
			want.post = permAll(dayName, "/{yyyy}/{mm}/{dd}/{slug}/", 6)
			want.lines = append(want.othersLines(), permInferred(ptPost, "posts", want.post))
			return w, want
		}},
		{"6.10.4 month and name", true, func() (*wpSite, permWant) {
			w := baseSite()
			w.structure = monthName
			want := permOthers("/", "/", true)
			want.post = permAll(monthName, "/{yyyy}/{mm}/{slug}/", 6)
			want.lines = append(want.othersLines(), permInferred(ptPost, "posts", want.post))
			return w, want
		}},
		{"6.10.4 numeric", true, func() (*wpSite, permWant) {
			w := baseSite()
			w.structure = "/archives/%post_id%"
			w.types["works"] = wpType{archive: wprest.Archive{Set: true, On: true}}
			want := permOthers("/archives/", "/", false)
			want.post = permAll("/archives/%post_id%", "/archives/{number}/", 6)
			want.works.Archive = "/archives/works/"
			want.lines = append(want.othersLines(), permInferred(ptPost, "posts", want.post),
				permLine("/types/works/archive", `the post type works had an archive; its path is "/archives/works/"`))
			return w, want
		}},
		{"numeric without the REST API", false, func() (*wpSite, permWant) {
			w := baseSite()
			w.structure = "/archives/%post_id%"
			want := permOthers("/archives/", "/", false)
			want.post = permAll("/archives/%post_id%", "/archives/{number}/", 6)
			want.author = Permalink{Rule: "/archives/author/{slug}/"}
			want.category = Permalink{Rule: "/archives/category/{path}/"}
			want.tag = Permalink{Rule: "/archives/tag/{slug}/"}
			const missing = "the REST API did not give the links of %s; the URL rule is %q, the default of WordPress for the front \"/archives/\""
			want.lines = []string{
				permInferred(ptPost, "posts", want.post), permInferred(ptPage, "pages", want.page),
				permInferred(ptWorks, "the post type works", want.works),
				permLine("/types/works/archive", "the REST API did not tell whether the post type works had an archive; set its archive if it had one"),
				permLine(ptAuthor, fmt.Sprintf(missing, "author pages", want.author.Rule)),
				permLine(ptCategory, fmt.Sprintf(missing, "the taxonomy category", want.category.Rule)),
				permLine(ptTag, fmt.Sprintf(missing, "the taxonomy tag", want.tag.Rule)),
			}
			return w, want
		}},
		{"6.10.4 post name", true, func() (*wpSite, permWant) {
			w := baseSite()
			w.structure = "/%postname%/"
			want := permOthers("/", "/", true)
			want.post = permAll("/%postname%/", "/{slug}/", 6)
			want.lines = append(want.othersLines(), permInferred(ptPost, "posts", want.post))
			return w, want
		}},
		{"6.10.4 custom structure", true, func() (*wpSite, permWant) {
			w := baseSite()
			w.structure = "/%category%/%postname%.html"
			// A tag is not a category: the post without a category has a tag, and another post has a tag as well.
			w.item(11).terms = []int64{1, 5}
			w.item(16).terms = []int64{4}
			want := permOthers("/", "/", false)
			want.post = permAll("/%category%/%postname%.html", "/{category}/{slug}.html", 6)
			want.defaultCategory = "uncategorized"
			want.lines = append(want.othersLines(), permInferred(ptPost, "posts", want.post),
				permLine(ptPost, `posts without a category had "uncategorized" in their links; it is the default category`))
			return w, want
		}},
		{"bases", true, func() (*wpSite, permWant) {
			w := baseSite()
			w.structure, w.catBase, w.tagBase = "/blog/%postname%/", "topics", "keywords"
			want := permOthers("/blog/", "/", true)
			want.post = permAll("/blog/%postname%/", "/blog/{slug}/", 6)
			want.category = permAll("/topics/%category%/", "/topics/{path}/", 3)
			want.tag = permAll("/keywords/%post_tag%/", "/keywords/{slug}/", 2)
			want.lines = append(want.othersLines(), permInferred(ptPost, "posts", want.post))
			return w, want
		}},
		{"author and post name", true, func() (*wpSite, permWant) {
			w := baseSite()
			w.structure = "/%author%/%postname%/"
			want := permOthers("/", "/", true)
			// The four posts of admin follow /admin/{slug}/, which is the best rule that can be written.
			want.post = Permalink{Structure: "/%author%/%postname%/", Rule: "/admin/{slug}/", Links: 6, Matched: 6, RuleMatched: 4}
			want.lines = append(want.othersLines(), permLine(ptPost, `the permalink structure "/%author%/%postname%/" of posts, `+
				`which 6 of 6 links follow, cannot be a URL rule: %author% has no placeholder; the URL rule is "/admin/{slug}/", which 4 of them follow`))
			return w, want
		}},
		{"htm", true, func() (*wpSite, permWant) {
			w := baseSite()
			w.structure = "/%postname%.htm"
			want := permOthers("/", "/", false)
			want.post = Permalink{Structure: "/%postname%.htm", Rule: "/{yyyy}/{mm}/{dd}/{slug}/", Links: 6, Matched: 6}
			want.lines = append(want.othersLines(), permLine(ptPost, `the permalink structure "/%postname%.htm" of posts, which 6 of 6 links follow, `+
				`cannot be a URL rule: a URL rule must end with "/" or ".html", not ".htm"; the URL rule is the default "/{yyyy}/{mm}/{dd}/{slug}/"`))
			return w, want
		}},
		{"date and ID", true, func() (*wpSite, permWant) {
			w := baseSite()
			w.structure = "/%year%%monthnum%%day%-%post_id%/"
			want := permOthers("/", "/", true)
			want.post = permAll("/%year%%monthnum%%day%-%post_id%/", "/{yyyy}{mm}{dd}-{number}/", 6)
			want.lines = append(want.othersLines(), permInferred(ptPost, "posts", want.post))
			return w, want
		}},
		{"hour and minute", true, func() (*wpSite, permWant) {
			w := baseSite()
			w.structure = "/%year%/%monthnum%/%day%/%hour%%minute%/%postname%/"
			for i := range w.items {
				if w.items[i].typ == "post" {
					w.items[i].date = w.items[i].date[:11] + "09:30:00"
				}
			}
			want := permOthers("/", "/", true)
			want.post = Permalink{
				Structure: "/%year%/%monthnum%/%day%/%hour%%minute%/%postname%/", Rule: "/{yyyy}/{mm}/{dd}/0930/{slug}/",
				Links: 6, Matched: 6, RuleMatched: 6,
			}
			want.lines = append(want.othersLines(), permLine(ptPost, `the permalink structure "/%year%/%monthnum%/%day%/%hour%%minute%/%postname%/" `+
				`of posts, which 6 of 6 links follow, cannot be a URL rule: %hour% has no placeholder; `+
				`the URL rule is "/{yyyy}/{mm}/{dd}/0930/{slug}/", which 6 of them follow`))
			return w, want
		}},
		{"category and a dash", true, func() (*wpSite, permWant) {
			w := baseSite()
			w.structure = "/%category%-%postname%/"
			want := permOthers("/", "/", true)
			want.post = Permalink{Structure: "/%category%-%postname%/", Rule: "/uncategorized-{slug}/", Links: 6, Matched: 6, RuleMatched: 3}
			want.defaultCategory = "uncategorized"
			want.lines = append(want.othersLines(), permLine(ptPost, `the permalink structure "/%category%-%postname%/" of posts, `+
				`which 6 of 6 links follow, cannot be a URL rule: invalid URL rule "/{category}-{slug}/": {category} must be a whole segment; `+
				`the URL rule is "/uncategorized-{slug}/", which 3 of them follow`),
				permLine(ptPost, `posts without a category had "uncategorized" in their links; it is the default category`))
			return w, want
		}},
		{"subdirectory", true, func() (*wpSite, permWant) {
			w := baseSite()
			w.home, w.structure = "https://example.com/blog", dayName
			want := permOthers("/", "/", true)
			want.post = permAll(dayName, "/{yyyy}/{mm}/{dd}/{slug}/", 6)
			want.lines = append(want.othersLines(), permInferred(ptPost, "posts", want.post))
			return w, want
		}},
		{"Japanese slugs", true, func() (*wpSite, permWant) {
			w := baseSite()
			w.structure = dayName
			w.terms = append(w.terms, wpTerm{id: 6, tax: "category", slug: "%e6%97%a5%e6%9c%ac%e8%aa%9e"})
			w.items = append(w.items,
				wpItem{id: 18, typ: "post", slug: "%e3%81%82%e3%81%84", date: "2026-12-05 10:00:00", author: "admin", terms: []int64{6}},
				// An upper case hex number in the slug and the link, and one in the slug only.
				wpItem{id: 19, typ: "post", slug: "%E3%81%82%E3%81%84%E3%81%86", date: "2026-12-06 10:00:00", author: "admin", terms: []int64{6}},
				wpItem{id: 20, typ: "post", slug: "%E3%81%8B", date: "2026-12-07 10:00:00", author: "admin", terms: []int64{6},
					link: "https://example.com/2026/12/07/%e3%81%8b/"},
			)
			w.users = append(w.users, wpUser{3, "yamada", "%E5%B1%B1%E7%94%B0"})
			w.terms = append(w.terms, wpTerm{id: 7, tax: "post_tag", slug: "%e3%82%bf%e3%82%b0"})
			want := permOthers("/", "/", true)
			want.post = permAll(dayName, "/{yyyy}/{mm}/{dd}/{slug}/", 9)
			want.category = permAll("/category/%category%/", "/category/{path}/", 4)
			want.author = permAll("/author/%author%/", "/author/{slug}/", 3)
			want.tag = permAll("/tag/%post_tag%/", "/tag/{slug}/", 3)
			want.lines = append(want.othersLines(), permInferred(ptPost, "posts", want.post))
			return w, want
		}},
		{"a slug and an ID like dates", true, func() (*wpSite, permWant) {
			w := baseSite()
			w.structure = monthName
			w.items = append(w.items, wpItem{id: 10, typ: "post", slug: "2026", date: "2026-10-05 10:00:00", author: "admin", terms: []int64{1}})
			want := permOthers("/", "/", true)
			want.post = permAll(monthName, "/{yyyy}/{mm}/{slug}/", 7)
			want.lines = append(want.othersLines(), permInferred(ptPost, "posts", want.post))
			return w, want
		}},
		{"a link that does not follow", true, func() (*wpSite, permWant) {
			w := baseSite()
			w.structure = dayName
			w.item(15).link = "https://example.com/special-offer/"
			want := permOthers("/", "/", true)
			want.post = Permalink{Structure: dayName, Rule: "/{yyyy}/{mm}/{dd}/{slug}/", Links: 6, Matched: 5, RuleMatched: 5}
			want.lines = append(want.othersLines(), permLine(ptPost, `inferred the permalink structure "`+dayName+`" of posts from 5 of 6 links; `+
				`the URL rule is "/{yyyy}/{mm}/{dd}/{slug}/"; 1 link does not follow it, so the import will need a redirect for it`))
			return w, want
		}},
		{"a hierarchical type", true, func() (*wpSite, permWant) {
			w := baseSite()
			w.structure = dayName
			w.types["works"] = wpType{hierarchical: true, archive: wprest.Archive{Set: true}}
			w.item(32).parent = 31
			want := permOthers("/", "/", true)
			want.post = permAll(dayName, "/{yyyy}/{mm}/{dd}/{slug}/", 6)
			// Only the first of the two follows /works/{slug}/: the other has the slug of its parent in its link too.
			want.works = Permalink{Structure: "/works/%works%/", Rule: "/works/{slug}/", Links: 2, Matched: 2, RuleMatched: 1}
			want.lines = append(want.othersLines()[:2], want.othersLines()[3:]...)
			want.lines = append(want.lines, permInferred(ptPost, "posts", want.post), permLine(ptWorks, `the permalink structure "/works/%works%/" `+
				`of the post type works, which 2 of 2 links follow, cannot be a URL rule: invalid URL rule "/works/{path}/": `+
				`placeholder "{path}" is not allowed for works (use {slug}, {number}); the URL rule is "/works/{slug}/", which 1 of them follows`))
			return w, want
		}},
		{"index.php", true, func() (*wpSite, permWant) {
			w := baseSite()
			w.structure = "/index.php/%year%/%monthnum%/%postname%/"
			want := permOthers("/index.php/", "/index.php/", true)
			want.post = permAll(w.structure, "/index.php/{yyyy}/{mm}/{slug}/", 6)
			want.lines = append(want.othersLines(), permInferred(ptPost, "posts", want.post))
			return w, want
		}},
		{"no posts", true, func() (*wpSite, permWant) {
			w := baseSite()
			w.structure = "/%postname%/"
			w.items = slices.DeleteFunc(w.items, func(it wpItem) bool { return it.typ == "post" })
			want := permOthers("/", "/", true)
			want.post = Permalink{Rule: "/{yyyy}/{mm}/{dd}/{slug}/"}
			want.lines = append(want.othersLines(), permNoLink(ptPost, "posts", want.post.Rule))
			return w, want
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, want := tt.make()
			defs := permDefs()
			got, rep := w.guess(t, defs, tt.rest)
			checkPermalinks(t, got, rep, defs, want)
		})
	}
}

func TestPermalinkArchive(t *testing.T) {
	const pointer = "/types/works/archive"
	tests := []struct {
		name      string
		structure string
		typ       wpType
		rest      bool
		wantRule  string
		want      string // the path of the list
		line      string // the message
	}{
		{"a list at the slug of the type", "/%postname%/", wpType{archive: wprest.Archive{Set: true, On: true}}, true,
			"/works/{slug}/", "/works/", `the post type works had an archive; its path is "/works/"`},
		{"no list", "/%postname%/", wpType{archive: wprest.Archive{Set: true}}, true, "/works/{slug}/", "", ""},
		{"a list at a string", "/%postname%/", wpType{archive: wprest.Archive{Set: true, On: true, Slug: "portfolio"}}, true,
			"/works/{slug}/", "/portfolio/", `the post type works had an archive; its path is "/portfolio/"`},
		{"a string after the front", "/archives/%post_id%", wpType{archive: wprest.Archive{Set: true, On: true, Slug: "portfolio"}}, true,
			"/archives/works/{slug}/", "/archives/portfolio/", `the post type works had an archive; its path is "/archives/portfolio/"`},
		{"a string without the front", "/archives/%post_id%", wpType{noFront: true, archive: wprest.Archive{Set: true, On: true, Slug: "portfolio"}}, true,
			"/works/{slug}/", "/portfolio/", `the post type works had an archive; its path is "/portfolio/"`},
		{"another slug of the type", "/%postname%/", wpType{slug: "portfolio", archive: wprest.Archive{Set: true, On: true}}, true,
			"/portfolio/{slug}/", "/portfolio/", `the post type works had an archive; its path is "/portfolio/"`},
		{"the type is not in the REST API", "/%postname%/", wpType{}, true, "/works/{slug}/", "",
			"the REST API did not tell whether the post type works had an archive; set its archive if it had one"},
		{"has_archive is not in the REST API", "/%postname%/", wpType{}, true, "/works/{slug}/", "",
			"the REST API did not tell whether the post type works had an archive; set its archive if it had one"},
		{"no REST API", "/%postname%/", wpType{archive: wprest.Archive{Set: true, On: true}}, false, "/works/{slug}/", "",
			"the REST API did not tell whether the post type works had an archive; set its archive if it had one"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := baseSite()
			w.structure = tt.structure
			w.types["works"] = tt.typ
			if tt.name == "the type is not in the REST API" {
				delete(w.types, "works")
				w.types["other"] = wpType{}
			}
			defs := permDefs()
			got, rep := w.guess(t, defs, tt.rest)
			works := got.Types["works"]
			if works.Rule != tt.wantRule || works.Archive != tt.want {
				t.Errorf("works = %q with the list %q, want %q with %q", works.Rule, works.Archive, tt.wantRule, tt.want)
			}
			var lines []string
			for _, l := range reportLines(t, rep) {
				if strings.HasSuffix(l, "#"+pointer) {
					lines = append(lines, l)
				}
			}
			var want []string
			if tt.line != "" {
				want = []string{permLine(pointer, tt.line)}
			}
			if !slices.Equal(lines, want) {
				t.Errorf("the lines about the list are\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
			}
			checkPermalinkRules(t, got, defs)
		})
	}
}

func TestPermalinkArchiveCannotBeTold(t *testing.T) {
	// The rule has no directory, so the list has no path to be taken from.
	w := baseSite()
	w.structure = "/%postname%/"
	w.types["works"] = wpType{archive: wprest.Archive{Set: true, On: true}}
	for i := range w.items {
		if w.items[i].typ == "works" {
			w.items[i].link = "https://example.com/works-" + w.items[i].slug + "/"
		}
	}
	got, rep := w.guess(t, permDefs(), true)
	if works := got.Types["works"]; works.Rule != "/works-{slug}/" || works.Archive != "" {
		t.Errorf("works = %q with the list %q", works.Rule, works.Archive)
	}
	wantLines := linesAt(t, rep, "/types/works/archive")
	want := permLine("/types/works/archive", `the post type works had an archive, but its path cannot be told: `+
		`the URL rule has no directory to take the path from; set its archive if it should have one`)
	if !slices.Equal(wantLines, []string{want}) {
		t.Errorf("the lines are %q, want %q", wantLines, want)
	}

	// A string that is not a path.
	w = baseSite()
	w.structure = "/%postname%/"
	w.types["works"] = wpType{archive: wprest.Archive{Set: true, On: true, Slug: "a b"}}
	got, rep = w.guess(t, permDefs(), true)
	if works := got.Types["works"]; works.Archive != "" {
		t.Errorf("the list is %q", works.Archive)
	}
	lines := linesAt(t, rep, "/types/works/archive")
	if len(lines) != 1 || !strings.Contains(lines[0], `had an archive, but its path cannot be told: invalid archive path "/a b/"`) {
		t.Errorf("the lines are %q", lines)
	}
}

// tpl returns a template of the strings and the tags in parts.
func tpl(parts ...any) template {
	var t template
	for _, p := range parts {
		switch p := p.(type) {
		case string:
			t = append(t, token{text: p})
		case tag:
			t = append(t, token{tag: p})
		default:
			panic(fmt.Sprintf("tpl: %T", p))
		}
	}
	return t
}

func TestPermalinkLinkPath(t *testing.T) {
	tests := []struct {
		name, home, link string
		want             string // "" if the link has no path of its own
	}{
		{"a path", "https://example.com", "https://example.com/2026/10/hello/", "/2026/10/hello/"},
		{"the home page", "https://example.com", "https://example.com/", "/"},
		{"the home page without a slash", "https://example.com", "https://example.com", "/"},
		{"another scheme", "http://example.com", "https://example.com/a/", "/a/"},
		{"the scheme in upper case", "https://example.com", "HTTPS://example.com/a/", "/a/"},
		{"the host in upper case", "https://example.com", "https://EXAMPLE.com/a/", "/a/"},
		{"another host", "https://example.com", "https://www.example.com/a/", ""},
		{"a host with a dot at its end", "https://example.com", "https://example.com./a/", ""},
		{"the default port written", "https://example.com", "https://example.com:443/a/", "/a/"},
		{"another port", "https://example.com", "https://example.com:8080/a/", ""},
		{"the same port", "https://example.com:8080", "https://example.com:8080/a/", "/a/"},
		{"a port only at the home", "https://example.com:8080", "https://example.com/a/", ""},
		{"the default ports of two schemes", "http://example.com", "https://example.com:443/a/", "/a/"},
		{"the port of another scheme", "https://example.com:80", "https://example.com/a/", ""},
		{"a query", "https://example.com", "https://example.com/?p=123", ""},
		{"a query after a path", "https://example.com", "https://example.com/a/?x=1", ""},
		{"an empty query", "https://example.com", "https://example.com/a/?", ""},
		{"a fragment", "https://example.com", "https://example.com/a/#top", ""},
		{"an empty fragment", "https://example.com", "https://example.com/a/#", ""},
		{"a user name", "https://example.com", "https://user@example.com/a/", ""},
		{"a relative link", "https://example.com", "/a/", ""},
		{"another kind of URL", "https://example.com", "ftp://example.com/a/", ""},
		{"an address", "https://example.com", "mailto:a@example.com", ""},
		{"an opaque URL", "https://example.com", "http:example.com/a/", ""},
		{"an invalid URL", "https://example.com", "https://exa mple.com/a/", ""},
		{"white space around the link", "https://example.com", " https://example.com/a/\n", "/a/"},
		{"an empty link", "https://example.com", "", ""},
		{"a subdirectory", "https://example.com/blog", "https://example.com/blog/a/", "/a/"},
		{"the home page of a subdirectory", "https://example.com/blog", "https://example.com/blog/", "/"},
		{"the home page of a subdirectory without a slash", "https://example.com/blog", "https://example.com/blog", "/"},
		{"a slash at the end of the home", "https://example.com/blog/", "https://example.com/blog/a/", "/a/"},
		{"outside the subdirectory", "https://example.com/blog", "https://example.com/a/", ""},
		{"a name that starts as the subdirectory", "https://example.com/blog", "https://example.com/blogger/a/", ""},
		{"Japanese in a percent-encoded form", "https://example.com", "https://example.com/%e6%97%a5%e6%9c%ac/", "/日本/"},
		{"upper case hex digits", "https://example.com", "https://example.com/%E6%97%A5%E6%9C%AC/", "/日本/"},
		{"Japanese as it is", "https://example.com", "https://example.com/日本/", "/日本/"},
		{"an encoded slash", "https://example.com", "https://example.com/a%2Fb/", "/a%2Fb/"},
		{"an encoded slash in lower case", "https://example.com", "https://example.com/a%2fb/", "/a%2Fb/"},
		{"an unreserved character", "https://example.com", "https://example.com/%7Efoo/", "/~foo/"},
		{"the path of the home in another form", "https://example.com/%E3%83%96", "https://example.com/ブ/a/", "/a/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home, err := url.Parse(tt.home)
			if err != nil {
				t.Fatal(err)
			}
			got, ok := linkPath(home, tt.link)
			if got != tt.want || ok != (tt.want != "") {
				t.Errorf("linkPath(%q, %q) = %q, %v, want %q", tt.home, tt.link, got, ok, tt.want)
			}
		})
	}
	if got, ok := linkPath(nil, "https://example.com/a/"); ok || got != "" {
		t.Errorf("linkPath(nil, ...) = %q, %v", got, ok)
	}
}

func TestPermalinkSlugValue(t *testing.T) {
	for _, tt := range []struct{ slug, want string }{
		{"hello-world", "hello-world"},
		{"2026", "2026"},
		{"", ""},
		{"%e6%97%a5%e6%9c%ac", "日本"},
		{"%E6%97%A5%E6%9C%AC", "日本"},
		{"日本", "日本"},
		{"a%2fb", "a%2Fb"},
		{"%41bc", "Abc"},
		{"a b", "a%20b"},
		{"a?b", ""},
		{"a#b", ""},
		{"50%", "50%"},
	} {
		if got := slugValue(tt.slug); got != tt.want {
			t.Errorf("slugValue(%q) = %q, want %q", tt.slug, got, tt.want)
		}
	}
}

func TestPermalinkDateParts(t *testing.T) {
	if got, ok := dateParts("2026-10-26 09:30:05"); !ok || got != [6]string{"2026", "10", "26", "09", "30", "05"} {
		t.Errorf("dateParts = %v, %v", got, ok)
	}
	if got, ok := dateParts("0000-00-00 00:00:00"); !ok || got[0] != "0000" {
		t.Errorf("dateParts of a date that WordPress writes for a draft = %v, %v", got, ok)
	}
	for _, s := range []string{
		"", "2026-10-26", "2026-10-26T09:30:05", "2026-10-26 09:30", "2026-10-26 09:30:0a", " 2026-10-26 09:30:05",
		"2026-10-26 09:30:055", "2026/10/26 09:30:05", "2026-10-26 09-30-05", "+026-10-26 09:30:05",
	} {
		if got, ok := dateParts(s); ok {
			t.Errorf("dateParts(%q) = %v, true", s, got)
		}
	}
}

func TestPermalinkTree(t *testing.T) {
	tr := newTree()
	tr.add(1, "about", 0)
	tr.add(2, "access", 1)
	tr.add(3, "map", 2)
	tr.add(4, "orphan", 99) // the parent is not there: WordPress stops there
	tr.add(5, "", 1)        // without a name: it is left out of the paths of its children
	tr.add(6, "child", 5)
	tr.add(7, "", 0)
	tr.add(8, "loop-a", 9)
	tr.add(9, "loop-b", 8)
	tr.add(10, "self", 10)
	for _, tt := range []struct {
		id   int64
		want string
	}{
		{1, "about"}, {2, "about/access"}, {3, "about/access/map"}, {4, "orphan"}, {5, ""}, {6, "about/child"}, {7, ""},
		{8, ""}, {9, ""}, {10, ""}, {99, ""},
	} {
		if got := tr.path(tt.id); got != tt.want {
			t.Errorf("path(%d) = %q, want %q", tt.id, got, tt.want)
		}
	}

	// 100 levels have a path, and the 101st has not.
	deep := newTree()
	for i := int64(1); i <= 101; i++ {
		deep.add(i, "s"+strconv.FormatInt(i, 10), i-1)
	}
	if got := deep.path(100); strings.Count(got, "/") != 99 || !strings.HasPrefix(got, "s1/s2/") || !strings.HasSuffix(got, "/s100") {
		t.Errorf("path(100) = %q", got)
	}
	if got := deep.path(101); got != "" {
		t.Errorf("path(101) = %q, want none", got)
	}
}

func TestPermalinkCategories(t *testing.T) {
	e := &wxr.Export{Terms: []wxr.Term{
		{ID: 1, Taxonomy: "category", Slug: "news"},
		{ID: 2, Taxonomy: "category", Slug: "press", Parent: "news"},
		{ID: 3, Taxonomy: "category", Slug: "%e6%97%a5%e6%9c%ac", Parent: "press"},
		{ID: 4, Taxonomy: "category", Slug: "orphan", Parent: "missing"},
		{ID: 5, Taxonomy: "category", Slug: "self", Parent: "self"},
		{ID: 6, Taxonomy: "category", Slug: "news"}, // the same slug twice: the first is the category
		{ID: 7, Taxonomy: "post_tag", Slug: "tag"},
		{ID: 8, Taxonomy: "category", Slug: "%E6%97%A5%E6%9C%AC"}, // the same slug in the form of a link path
	}}
	c := newCategories(e)
	for _, tt := range []struct{ slug, want string }{
		{"news", "news"}, {"press", "news/press"}, {"%e6%97%a5%e6%9c%ac", "news/press/日本"}, {"orphan", "orphan"},
		{"self", ""}, {"tag", ""}, {"unknown", ""}, {"%E6%97%A5%E6%9C%AC", "日本"},
	} {
		if got := c.path(tt.slug); got != tt.want {
			t.Errorf("path(%q) = %q, want %q", tt.slug, got, tt.want)
		}
	}
	if want := []string{"news", "press", "日本", "orphan", "self"}; !slices.Equal(c.list, want) {
		t.Errorf("list = %q, want %q", c.list, want)
	}
	if c.written["日本"] != "%e6%97%a5%e6%9c%ac" || c.order["press"] != 1 || c.order["news"] != 0 {
		t.Errorf("written = %q, order = %v", c.written, c.order)
	}

	// 100 levels of categories have a path, and the 101st has not.
	deep := &wxr.Export{}
	for i := 1; i <= 101; i++ {
		term := wxr.Term{ID: int64(i), Taxonomy: "category", Slug: "s" + strconv.Itoa(i)}
		if i > 1 {
			term.Parent = "s" + strconv.Itoa(i-1)
		}
		deep.Terms = append(deep.Terms, term)
	}
	c = newCategories(deep)
	if got := c.path("s100"); strings.Count(got, "/") != 99 || !strings.HasPrefix(got, "s1/s2/") || !strings.HasSuffix(got, "/s100") {
		t.Errorf("path(s100) = %q", got)
	}
	if got := c.path("s101"); got != "" {
		t.Errorf("path(s101) = %q, want none", got)
	}

	// Many categories: all can be the default category of a post, but the candidates are made from some.
	many := &wxr.Export{}
	for i := range permCategories + 100 {
		many.Terms = append(many.Terms, wxr.Term{ID: int64(i + 1), Taxonomy: "category", Slug: "c" + strconv.Itoa(i)})
	}
	c = newCategories(many)
	if len(c.list) != permCategories || len(c.written) != permCategories+100 {
		t.Errorf("%d in the list and %d written, want %d and %d", len(c.list), len(c.written), permCategories, permCategories+100)
	}
}

func TestPermalinkGenerate(t *testing.T) {
	s := &sample{path: "/2026/10/hello/"}
	s.vals[tagSlug], s.vals[tagYear], s.vals[tagMonth], s.vals[tagNumber] = "hello", "2026", "10", "10"
	got := generate(s, postTags, 256)

	// The ID is 10 as the month is, so there are two tags for that place, and the text.
	keys := make(map[string]bool)
	for _, tmpl := range got {
		if ok, _ := tmpl.match(s); !ok {
			t.Errorf("%+v does not make the path", tmpl)
		}
		if keys[tmpl.key()] {
			t.Errorf("%+v is there twice", tmpl)
		}
		keys[tmpl.key()] = true
	}
	for _, tmpl := range []template{
		tpl("/", tagYear, "/", tagMonth, "/", tagSlug, "/"),
		tpl("/", tagYear, "/", tagNumber, "/", tagSlug, "/"),
		tpl("/", tagYear, "/10/", tagSlug, "/"),
		tpl("/", tagYear, "/10/hello/"),
		tpl("/2026/", tagMonth, "/", tagSlug, "/"),
		tpl("/2026/10/", tagSlug, "/"),
		tpl("/2026/10/hello/"),
	} {
		if !keys[tmpl.key()] {
			t.Errorf("%+v is not generated", tmpl)
		}
	}
	if len(got) != 12 {
		t.Errorf("%d templates, want 12", len(got))
	}
	// The tags come before text, in their order: the first has the most tags, and the number before the month.
	if first := tpl("/", tagYear, "/", tagNumber, "/", tagSlug, "/"); len(got) == 0 || got[0].key() != first.key() {
		t.Errorf("the first is %+v, want %+v", got, first)
	}
	// The limit keeps the first ones.
	for _, limit := range []int{1, 3, 11, 12, 13} {
		if few := generate(s, postTags, limit); !slices.EqualFunc(few, got[:min(limit, len(got))], func(a, b template) bool { return a.key() == b.key() }) {
			t.Errorf("limit %d gives %d templates that are not the first ones", limit, len(few))
		}
	}
	// Only the tags that are given are used.
	for _, tmpl := range generate(s, []tag{tagSlug}, 256) {
		if slices.ContainsFunc(tmpl, func(tk token) bool { return tk.tag != tagLit && tk.tag != tagSlug }) {
			t.Errorf("%+v has another tag than the slug", tmpl)
		}
	}
}

func TestPermalinkGenerateSegments(t *testing.T) {
	// A value with a "/" is whole segments: it follows a "/", and a "/", a "." or the end follows it.
	for _, tt := range []struct {
		path string
		cats []string
		want bool // a template has the category
	}{
		{"/news/press/hello/", []string{"news/press"}, true},
		{"/news/press.html", []string{"news/press"}, true},
		{"/blog/news/press", []string{"news/press"}, true},
		{"/news/press-hello/", []string{"news/press"}, false},
		{"/xnews/press/hello/", []string{"news/press"}, false},
		{"/news/pressx/hello/", []string{"news/press"}, false},
		{"/news-hello/", []string{"news"}, true}, // a value without "/" can be followed by anything
		{"/xnews/", []string{"news"}, true},
	} {
		s := &sample{path: tt.path, cats: tt.cats}
		found := slices.ContainsFunc(generate(s, []tag{tagCategory}, 256), func(tmpl template) bool {
			return slices.ContainsFunc(tmpl, func(tk token) bool { return tk.tag == tagCategory })
		})
		if found != tt.want {
			t.Errorf("a template for %q with the category %q: %v, want %v", tt.path, tt.cats, found, tt.want)
		}
	}

	// A post without a category can have the slug of any category of the WXR, in the candidates too.
	known := newCategories(&wxr.Export{Terms: []wxr.Term{
		{ID: 1, Taxonomy: "category", Slug: "uncategorized"}, {ID: 2, Taxonomy: "category", Slug: "news"},
	}})
	s := &sample{path: "/uncategorized/hello.html", known: known}
	s.vals[tagSlug] = "hello"
	want := tpl("/", tagCategory, "/", tagSlug, ".html")
	if !slices.ContainsFunc(generate(s, postTags, 256), func(tmpl template) bool { return tmpl.key() == want.key() }) {
		t.Error("no template with the default category of a post without a category")
	}
}

func TestPermalinkMatch(t *testing.T) {
	known := newCategories(&wxr.Export{Terms: []wxr.Term{
		{ID: 1, Taxonomy: "category", Slug: "uncategorized"}, {ID: 2, Taxonomy: "category", Slug: "news"},
		{ID: 3, Taxonomy: "category", Slug: "%e6%97%a5%e6%9c%ac"}, {ID: 4, Taxonomy: "category", Slug: "press", Parent: "news"},
	}})
	post := func(path string, cats ...string) *sample {
		s := &sample{path: path, cats: cats, known: known}
		s.vals[tagSlug], s.vals[tagYear], s.vals[tagMonth], s.vals[tagNumber] = "hello", "2026", "10", "12"
		return s
	}
	html := tpl("/", tagCategory, "/", tagSlug, ".html")
	slugSample := func(path, slug string, cats ...string) *sample {
		s := &sample{path: path, cats: cats}
		s.vals[tagSlug] = slug
		return s
	}
	tests := []struct {
		name     string
		t        template
		s        *sample
		ok       bool
		category string
	}{
		{"tags and text", tpl("/", tagYear, "/", tagMonth, "/", tagSlug, "/"), post("/2026/10/hello/"), true, ""},
		{"text only", tpl("/2026/10/hello/"), post("/2026/10/hello/"), true, ""},
		{"a tag at the start and the end", tpl(tagSlug), post("hello"), true, ""},
		{"a path that goes on", tpl("/", tagYear, "/"), post("/2026/10/"), false, ""},
		{"a path that ends", tpl("/", tagYear, "/", tagMonth, "/", tagSlug, "/"), post("/2026/10/"), false, ""},
		{"another value", tpl("/", tagYear, "/", tagMonth, "/", tagSlug, "/"), post("/2026/11/hello/"), false, ""},
		{"another text", tpl("/blog/", tagSlug, "/"), post("/news/hello/"), false, ""},
		{"a tag without a value", tpl("/", tagDay, "/", tagSlug, "/"), post("/26/hello/"), false, ""},
		{"the number", tpl("/archives/", tagNumber), post("/archives/12"), true, ""},
		{"a category of the post", html, post("/news/hello.html", "news"), true, ""},
		{"a category with its ancestors", html, post("/news/press/hello.html", "news/press"), true, ""},
		{"one of the categories", html, post("/news/hello.html", "uncategorized", "news"), true, ""},
		{"another category", html, post("/uncategorized/hello.html", "news"), false, ""},
		{"only the leaf of the path", html, post("/press/hello.html", "news/press"), false, ""},
		{"no category in the path", html, post("//hello.html", "news"), false, ""},
		{"text around the category", tpl("/blog/", tagCategory, "/", tagSlug, "/"), post("/blog/news/hello/", "news"), true, ""},
		{"a category and text that follows", tpl("/", tagCategory, "-", tagSlug, "/"), post("/news/press-hello/", "news/press"), true, ""},
		{"the default category", html, post("/uncategorized/hello.html"), true, "uncategorized"},
		{"the default category as written", html, post("/日本/hello.html"), true, "%e6%97%a5%e6%9c%ac"},
		{"a slug that is not a category", html, post("/other/hello.html"), false, ""},
		{"a path of categories is not the default category", html, post("/news/press/hello.html"), false, ""},
		{"no category in the path of a post without one", html, post("//hello.html"), false, ""},
		{"a category and the text overlap", tpl("/a", tagCategory, "a"), post("/a"), false, ""},
		{"text that is not at the start", tpl("a", tagSlug), slugSample("ba", "a"), false, ""},
		{"text that is not at the end", tpl("/", tagCategory, "a", tagSlug), slugSample("/newsab", "a", "news"), false, ""},
		{"text at the start and the end", tpl("/", tagCategory, "a", tagSlug), slugSample("/newsaa", "a", "news"), true, ""},
		{"a post without a category in a sample that knows none", html, &sample{path: "/news/hello.html"}, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok, category := tt.t.match(tt.s)
			if ok != tt.ok || category != tt.category {
				t.Errorf("match = %v, %q, want %v, %q", ok, category, tt.ok, tt.category)
			}
		})
	}
}

func TestPermalinkCompare(t *testing.T) {
	// Each of the templates is before the one after it. The first has the fewest characters of text; for the same number,
	// the fewest tokens; then the tokens are compared, a tag before text, tags in their order, and text as strings.
	order := []template{
		tpl("/", tagSlug, "/"),
		tpl("/ab/", tagSlug),
		tpl("/", tagYear, "ab/"),
		tpl("/", tagYear, "/", tagMonth, "/", tagSlug, "/"),
		tpl("/blog/", tagSlug, "/"),
	}
	same := [][]template{
		{tpl("/", tagYear, "/", tagHour, "/"), tpl("/", tagMonth, "/", tagYear, "/"), tpl("/", tagHour, "/", tagYear, "/")},
		{tpl("/", tagYear, "/", tagMonth, "/10/"), tpl("/", tagYear, "/", tagMonth, "/11/")},
		{tpl(tagSlug, "ab"), tpl("ab", tagSlug)},
		{tpl("/", tagSlug, "/", tagNumber), tpl("/", tagNumber, "/", tagSlug)},
	}
	for _, list := range append([][]template{order}, same...) {
		for i := range list {
			for j := range list {
				if got, want := compareTemplates(list[i], list[j]), cmp.Compare(i, j); got != want {
					t.Errorf("compareTemplates(%+v, %+v) = %d, want %d", list[i], list[j], got, want)
				}
			}
		}
	}
}

func TestPermalinkRank(t *testing.T) {
	var samples []*sample
	for i := range 1000 {
		s := &sample{id: int64(i + 1), path: "/2026/05/post-" + strconv.Itoa(i) + "/"}
		if i%4 == 3 {
			s.path = "/x/post-" + strconv.Itoa(i) + "/"
		}
		s.vals[tagYear], s.vals[tagMonth], s.vals[tagSlug] = "2026", "05", "post-"+strconv.Itoa(i)
		samples = append(samples, s)
	}
	ranked := rank(candidates(samples, postTags), samples)
	if len(ranked) != 8 {
		t.Fatalf("%d candidates are kept, want 8", len(ranked))
	}
	for i, r := range ranked {
		if want := countMatches(r.t, samples); r.n != want {
			t.Errorf("%d: %+v is counted %d, but matches %d of all", i, r.t, r.n, want)
		}
		if i > 0 && ranked[i-1].n < r.n {
			t.Errorf("%d: %d links, after %d", i, r.n, ranked[i-1].n)
		}
	}
	// The year and the month are tags or text: four templates match 750 links, and the one with the tags is the best.
	if want := tpl("/", tagYear, "/", tagMonth, "/", tagSlug, "/"); ranked[0].t.key() != want.key() {
		t.Errorf("the best is %+v, want %+v", ranked[0].t, want)
	}
	for i := range 4 {
		if ranked[i].n != 750 {
			t.Errorf("%d: %d links, want 750", i, ranked[i].n)
		}
	}
	if want := tpl("/x/", tagSlug, "/"); ranked[4].n != 250 || ranked[4].t.key() != want.key() {
		t.Errorf("the fifth is %+v with %d, want %+v with 250", ranked[4].t, ranked[4].n, want)
	}

	// Candidates that match as many links are in the order of compareTemplates, not the order in which they were made.
	var two []*sample
	for i, path := range []string{"/bb/x/", "/a/y/"} {
		s := &sample{id: int64(i + 1), path: path}
		s.vals[tagSlug] = string(path[len(path)-2])
		two = append(two, s)
	}
	ranked = rank(candidates(two, []tag{tagSlug}), two)
	if want := tpl("/a/", tagSlug, "/"); ranked[0].n != 1 || ranked[0].t.key() != want.key() {
		t.Errorf("the best of two is %+v with %d, want %+v with 1", ranked[0].t, ranked[0].n, want)
	}
}

func TestPermalinkRule(t *testing.T) {
	target := func(pointer string, html bool, tags []tag, names [numTags]string, set func(*content.Site, string)) *permTarget {
		in := &permInference{defs: &content.Site{Types: []content.TypeDef{{Name: "works"}}, Taxonomies: []content.TaxonomyDef{{Name: "genre"}}}}
		tg := in.target(pointer, "x", tags, names, set)
		tg.html = html
		return tg
	}
	post := target(ptPost, true, postTags, postNames, typeURL("post"))
	page := target(ptPage, true, []tag{tagPath}, tagNames("%pagename%", tagPath), typeURL("page"))
	works := target(ptWorks, true, []tag{tagSlug, tagPath}, tagNames("%works%", tagSlug, tagPath), typeURL("works"))
	category := target(ptCategory, false, []tag{tagPath}, tagNames("%category%", tagPath), taxonomyURL("category"))
	genre := target("/taxonomies/genre/url", false, []tag{tagSlug}, tagNames("%genre%", tagSlug), taxonomyURL("genre"))
	author := target(ptAuthor, false, []tag{tagSlug}, tagNames("%author%", tagSlug), setAuthorURL)
	tests := []struct {
		name string
		tg   *permTarget
		t    template
		want string // the rule, or the reason that there is none after "error: "
	}{
		{"a post", post, tpl("/", tagYear, "/", tagMonth, "/", tagDay, "/", tagSlug, "/"), "/{yyyy}/{mm}/{dd}/{slug}/"},
		{"the number", post, tpl("/archives/", tagNumber), "/archives/{number}/"},
		{"html", post, tpl("/", tagSlug, ".html"), "/{slug}.html"},
		{"a category and html", post, tpl("/", tagCategory, "/", tagSlug, ".html"), "/{category}/{slug}.html"},
		{"several tags in a segment", post, tpl("/", tagYear, tagMonth, tagDay, "-", tagNumber, "/"), "/{yyyy}{mm}{dd}-{number}/"},
		{"text with a dot before the last segment", post, tpl("/v1.0/", tagSlug), "/v1.0/{slug}/"},
		{"index.php", post, tpl("/index.php/", tagSlug, "/"), "/index.php/{slug}/"},
		{"a slash is added", post, tpl("/", tagSlug), "/{slug}/"},
		{".htm", post, tpl("/", tagSlug, ".htm"), `error: a URL rule must end with "/" or ".html", not ".htm"`},
		{".php", post, tpl("/", tagNumber, ".php"), `error: a URL rule must end with "/" or ".html", not ".php"`},
		{".HTML", post, tpl("/", tagSlug, ".HTML"), `error: a URL rule must end with "/" or ".html", not ".HTML"`},
		{"the author", post, tpl("/", tagAuthor, "/", tagSlug, "/"), "error: %author% has no placeholder"},
		{"the hour", post, tpl("/", tagYear, "/", tagHour, "/", tagSlug, "/"), "error: %hour% has no placeholder"},
		{"the minute", post, tpl("/", tagMinute, "/", tagSlug, "/"), "error: %minute% has no placeholder"},
		{"the second", post, tpl("/", tagSecond, "/", tagSlug, "/"), "error: %second% has no placeholder"},
		{"a category that is not a segment", post, tpl("/", tagCategory, "-", tagSlug, "/"),
			`error: invalid URL rule "/{category}-{slug}/": {category} must be a whole segment`},
		{"text only", post, tpl("/hello/"), `error: invalid URL rule "/hello/": missing {slug} or {number}`},
		{"only the date", post, tpl("/", tagYear, "/", tagMonth, "/"), `error: invalid URL rule "/{yyyy}/{mm}/": missing {slug} or {number}`},
		{"a character that a rule cannot have", post, tpl("/a%20b/", tagSlug, "/"), `error: invalid URL rule "/a%20b/{slug}/": character '%' (U+0025) is not allowed`},
		{"a segment that starts with a dot", post, tpl("/.news/", tagSlug, "/"), `error: invalid URL rule "/.news/{slug}/": segments cannot start with "."`},
		{"a page", page, tpl("/", tagPath), "/{path}/"},
		{"a page and index.php", page, tpl("/index.php/", tagPath, "/"), "/index.php/{path}/"},
		{"the path for a custom type", works, tpl("/works/", tagPath, "/"),
			`error: invalid URL rule "/works/{path}/": placeholder "{path}" is not allowed for works (use {slug}, {number})`},
		{"the slug for a custom type", works, tpl("/works/", tagSlug), "/works/{slug}/"},
		{"a taxonomy with a hierarchy", category, tpl("/category/", tagPath), "/category/{path}/"},
		{"a taxonomy without html", genre, tpl("/genre/", tagSlug, ".html"), `error: a URL rule must end with "/", not ".html"`},
		{"the path for a taxonomy without a hierarchy", genre, tpl("/genre/", tagPath, "/"),
			`error: invalid URL rule "/genre/{path}/": placeholder "{path}" is not allowed for genre (use {slug})`},
		{"an author", author, tpl("/author/", tagSlug), "/author/{slug}/"},
		{"an author with the slug only", author, tpl("/", tagSlug, "/"), "/{slug}/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.tg.rule(tt.t)
			if reason, isError := strings.CutPrefix(tt.want, "error: "); isError {
				if err == nil || err.Error() != reason || got != "" {
					t.Errorf("rule = %q, %v, want the error %q", got, err, reason)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Errorf("rule = %q, %v, want %q", got, err, tt.want)
			}
		})
	}
}

func TestPermalinkRest(t *testing.T) {
	const dayName = "/%year%/%monthnum%/%day%/%postname%/"
	defs := permDefs()
	run := func(t *testing.T, change func(s *wprest.Snapshot)) (*Permalinks, []string) {
		t.Helper()
		w := baseSite()
		w.structure = dayName
		snap := w.snapshot()
		change(snap)
		pl, rep := w.guessWith(t, defs, snap)
		checkPermalinkRules(t, pl, defs)
		return pl, reportLines(t, rep)
	}
	has := func(t *testing.T, lines []string, want string) {
		t.Helper()
		if !slices.Contains(lines, want) {
			t.Errorf("no line\n%s\nin\n%s", want, strings.Join(lines, "\n"))
		}
	}
	authorMissing := permMissing(ptAuthor, "author pages", "/author/{slug}/", "/")
	categoryMissing := permMissing(ptCategory, "the taxonomy category", "/category/{path}/", "/")

	t.Run("the users are refused", func(t *testing.T) {
		pl, lines := run(t, func(s *wprest.Snapshot) {
			s.Unavailable = []wprest.Unavailable{{Route: "/wp/v2/users", Status: 401, Code: "rest_user_cannot_view"}}
		})
		if got := publicPermalink(pl.Author); !samePermalink(got, (Permalink{Rule: "/author/{slug}/"})) {
			t.Errorf("Author = %+v", got)
		}
		has(t, lines, authorMissing)
		if got := pl.Taxonomies["category"]; got.Links != 3 {
			t.Errorf("the terms are read: category = %+v", publicPermalink(got))
		}
	})
	t.Run("the categories are refused", func(t *testing.T) {
		pl, lines := run(t, func(s *wprest.Snapshot) {
			s.Terms = slices.DeleteFunc(s.Terms, func(tm wprest.Term) bool { return tm.Taxonomy == "category" })
			s.Unavailable = []wprest.Unavailable{{Route: "/wp/v2/categories", Taxonomy: "category", Status: 404, Code: "rest_no_route"}}
		})
		if got := publicPermalink(pl.Taxonomies["category"]); !samePermalink(got, (Permalink{Rule: "/category/{path}/"})) {
			t.Errorf("category = %+v", got)
		}
		has(t, lines, categoryMissing)
		if got := pl.Taxonomies["tag"]; got.Links != 2 || got.Structure != "/tag/%post_tag%/" {
			t.Errorf("tag = %+v", publicPermalink(got))
		}
	})
	t.Run("the list of the taxonomies is refused", func(t *testing.T) {
		pl, lines := run(t, func(s *wprest.Snapshot) {
			s.Taxonomies = nil
			s.Unavailable = []wprest.Unavailable{{Route: "/wp/v2/taxonomies", Status: 401, Code: "rest_cannot_view"}}
		})
		// The categories and the tags are read all the same.
		if pl.Taxonomies["category"].Links != 3 || pl.Taxonomies["tag"].Links != 2 {
			t.Errorf("category = %+v, tag = %+v", publicPermalink(pl.Taxonomies["category"]), publicPermalink(pl.Taxonomies["tag"]))
		}
		for _, l := range lines {
			if strings.Contains(l, "REST API") {
				t.Errorf("line %q", l)
			}
		}
	})
	t.Run("nothing in the REST API", func(t *testing.T) {
		pl, lines := run(t, func(s *wprest.Snapshot) { s.Terms, s.Users, s.Types, s.Taxonomies = nil, nil, nil, nil })
		for _, p := range []Permalink{pl.Author, pl.Taxonomies["category"], pl.Taxonomies["tag"]} {
			if p.Links != 0 || p.Structure != "" {
				t.Errorf("%+v", publicPermalink(p))
			}
		}
		// The API was not refused, so there are no links to look at: that is not the same as not having the API.
		has(t, lines, permNoLink(ptAuthor, "author pages", "/author/{slug}/"))
		has(t, lines, permNoLink(ptCategory, "the taxonomy category", "/category/{path}/"))
		has(t, lines, permNoLink(ptTag, "the taxonomy tag", "/tag/{slug}/"))
	})
	t.Run("the links of the REST API are on another host", func(t *testing.T) {
		pl, lines := run(t, func(s *wprest.Snapshot) {
			for i := range s.Terms {
				s.Terms[i].Link = strings.Replace(s.Terms[i].Link, "example.com", "other.example", 1)
			}
			for i := range s.Users {
				s.Users[i].Link = strings.Replace(s.Users[i].Link, "example.com", "other.example", 1)
			}
		})
		for _, p := range []Permalink{pl.Author, pl.Taxonomies["category"], pl.Taxonomies["tag"]} {
			if p.Links != 0 {
				t.Errorf("%+v", publicPermalink(p))
			}
		}
		has(t, lines, permNoLink(ptAuthor, "author pages", "/author/{slug}/"))
	})
	t.Run("no address of the home in the REST API", func(t *testing.T) {
		// The snapshot cannot be checked to be that of the site of the WXR, so it is not used.
		w := baseSite()
		snap := w.snapshot()
		snap.Index.Home = ""
		rep := NewReport(Info{File: "site.xml"})
		imp := New("site.xml", w.export(), rep)
		imp.REST, imp.RESTFile = snap, "rest.json"
		pl, err := imp.InferPermalinks(permDefs())
		if want := "the REST API does not give its home, so it cannot be checked to be that of the WXR"; err == nil || err.Error() != want || pl != nil {
			t.Errorf("InferPermalinks = %v, %v, want the error %q", pl, err, want)
		}
		if lines := reportLines(t, rep); len(lines) != 0 {
			t.Errorf("the report has %q, want nothing", lines)
		}
	})
	t.Run("the links of the REST API are http", func(t *testing.T) {
		pl, _ := run(t, func(s *wprest.Snapshot) {
			for i := range s.Terms {
				s.Terms[i].Link = strings.Replace(s.Terms[i].Link, "https:", "http:", 1)
			}
			for i := range s.Users {
				s.Users[i].Link = strings.Replace(s.Users[i].Link, "https:", "http:", 1)
			}
		})
		if pl.Author.Links != 2 || pl.Taxonomies["category"].Links != 3 || pl.Taxonomies["tag"].Links != 2 {
			t.Errorf("author = %+v, category = %+v", publicPermalink(pl.Author), publicPermalink(pl.Taxonomies["category"]))
		}
	})
}

func TestPermalinkTaxonomies(t *testing.T) {
	genreSite := func(hierarchical, tree bool) (*wpSite, *content.Site) {
		w := baseSite()
		w.structure = "/%postname%/"
		parent := int64(0)
		if hierarchical {
			parent = 6
		}
		w.terms = append(w.terms,
			wpTerm{id: 6, tax: "genre", slug: "rock"}, wpTerm{id: 7, tax: "genre", slug: "jazz"},
			wpTerm{id: 8, tax: "genre", slug: "hardrock", parent: parent})
		w.taxes["genre"] = wpTax{hierarchical: hierarchical, tree: tree}
		defs := permDefs()
		defs.Taxonomies = []content.TaxonomyDef{{Name: "genre", Hierarchical: hierarchical}}
		return w, defs
	}
	const pointer = "/taxonomies/genre/url"
	tests := []struct {
		name  string
		setup func() (*wpSite, *content.Site, func(*wprest.Snapshot))
		want  Permalink
		line  string
	}{
		{"flat", func() (*wpSite, *content.Site, func(*wprest.Snapshot)) {
			w, defs := genreSite(false, false)
			return w, defs, func(*wprest.Snapshot) {}
		}, permAll("/genre/%genre%/", "/genre/{slug}/", 3), ""},
		{"a hierarchy in the links", func() (*wpSite, *content.Site, func(*wprest.Snapshot)) {
			w, defs := genreSite(true, true)
			return w, defs, func(*wprest.Snapshot) {}
		}, permAll("/genre/%genre%/", "/genre/{path}/", 3), ""},
		{"a hierarchy that is not in the links", func() (*wpSite, *content.Site, func(*wprest.Snapshot)) {
			w, defs := genreSite(true, false)
			return w, defs, func(*wprest.Snapshot) {}
		}, permAll("/genre/%genre%/", "/genre/{slug}/", 3), ""},
		{"a hierarchy that the schema does not have", func() (*wpSite, *content.Site, func(*wprest.Snapshot)) {
			w, defs := genreSite(true, true)
			defs.Taxonomies[0].Hierarchical = false
			return w, defs, func(*wprest.Snapshot) {}
		}, Permalink{Structure: "/genre/%genre%/", Rule: "/genre/{slug}/", Links: 3, Matched: 3, RuleMatched: 2},
			`the permalink structure "/genre/%genre%/" of the taxonomy genre, which 3 of 3 links follow, cannot be a URL rule: ` +
				`invalid URL rule "/genre/{path}/": placeholder "{path}" is not allowed for genre (use {slug}); ` +
				`the URL rule is "/genre/{slug}/", which 2 of them follow`},
		{"not in the REST API", func() (*wpSite, *content.Site, func(*wprest.Snapshot)) {
			w, defs := genreSite(false, false)
			return w, defs, func(s *wprest.Snapshot) {
				s.Taxonomies = slices.DeleteFunc(s.Taxonomies, func(x wprest.Taxonomy) bool { return x.Slug == "genre" })
				s.Terms = slices.DeleteFunc(s.Terms, func(x wprest.Term) bool { return x.Taxonomy == "genre" })
			}
		}, Permalink{Rule: "/genre/{slug}/"}, permMissingMsg("the taxonomy genre", "/genre/{slug}/", "/")},
		{"refused", func() (*wpSite, *content.Site, func(*wprest.Snapshot)) {
			w, defs := genreSite(false, false)
			return w, defs, func(s *wprest.Snapshot) {
				s.Terms = slices.DeleteFunc(s.Terms, func(x wprest.Term) bool { return x.Taxonomy == "genre" })
				s.Unavailable = []wprest.Unavailable{{Route: "/wp/v2/genre", Taxonomy: "genre", Status: 403, Code: "rest_forbidden"}}
			}
		}, Permalink{Rule: "/genre/{slug}/"}, permMissingMsg("the taxonomy genre", "/genre/{slug}/", "/")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, defs, change := tt.setup()
			snap := w.snapshot()
			change(snap)
			pl, rep := w.guessWith(t, defs, snap)
			if got := publicPermalink(pl.Taxonomies["genre"]); !samePermalink(got, tt.want) {
				t.Errorf("genre = %+v, want %+v", got, tt.want)
			}
			if len(pl.Taxonomies) != 3 {
				t.Errorf("%d taxonomies, want category, tag and genre", len(pl.Taxonomies))
			}
			lines := linesAt(t, rep, pointer)
			want := []string{permLine(pointer, tt.line)}
			if tt.line == "" {
				want = []string{permInferred(pointer, "the taxonomy genre", tt.want)}
			}
			if !slices.Equal(lines, want) {
				t.Errorf("the lines are\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
			}
			checkPermalinkRules(t, pl, defs)
		})
	}

	// The taxonomies that are guessed: the built in ones once, and the custom ones that the schema defines and WordPress has.
	w := baseSite()
	w.structure = "/%postname%/"
	defs := permDefs()
	defs.Taxonomies = []content.TaxonomyDef{
		{Name: "category", Label: "Topics"}, {Name: "genre"}, {Name: "post_tag"}, {Name: "nav_menu"}, {Name: "post_format"},
		{Name: "genre"}, {Name: "series"},
	}
	pl, rep := w.guess(t, defs, true)
	var names []string
	for name := range pl.Taxonomies {
		names = append(names, name)
	}
	slices.Sort(names)
	if want := []string{"category", "genre", "series", "tag"}; !slices.Equal(names, want) {
		t.Errorf("taxonomies %q, want %q", names, want)
	}
	for _, name := range names {
		if lines := linesAt(t, rep, "/taxonomies/"+name+"/url"); len(lines) != 1 {
			t.Errorf("%s: %d lines, want 1: %q", name, len(lines), lines)
		}
	}
	if got := publicPermalink(pl.Taxonomies["series"]); !samePermalink(got, Permalink{Rule: "/series/{slug}/"}) {
		t.Errorf("series = %+v", got)
	}

	// A taxonomy without links has the default rule of WordPress after the front of the posts.
	w.structure = "/archives/%post_id%"
	pl, _ = w.guess(t, defs, false)
	if got := pl.Taxonomies["genre"].Rule; got != "/archives/genre/{slug}/" {
		t.Errorf("genre: rule %q", got)
	}
}

func TestPermalinkTypes(t *testing.T) {
	t.Run("another slug, and no front", func(t *testing.T) {
		w := baseSite()
		w.structure = "/archives/%post_id%"
		w.types["works"] = wpType{slug: "portfolio", noFront: true}
		w.types["events"] = wpType{}
		w.items = append(w.items, wpItem{id: 41, typ: "events", slug: "e1"}, wpItem{id: 42, typ: "events", slug: "e2"})
		defs := permDefs()
		defs.Types = append(defs.Types, content.TypeDef{Name: "events"})
		pl, _ := w.guess(t, defs, true)
		if got, want := publicPermalink(pl.Types["works"]), permAll("/portfolio/%works%", "/portfolio/{slug}/", 2); !samePermalink(got, want) {
			t.Errorf("works = %+v, want %+v", got, want)
		}
		if got, want := publicPermalink(pl.Types["events"]), permAll("/archives/events/%events%", "/archives/events/{slug}/", 2); !samePermalink(got, want) {
			t.Errorf("events = %+v, want %+v", got, want)
		}
		checkPermalinkRules(t, pl, defs)
	})
	t.Run("the types that the schema defines", func(t *testing.T) {
		w := baseSite()
		w.structure = "/archives/%post_id%"
		defs := permDefs()
		defs.Types = []content.TypeDef{
			{Name: "post", URL: "/x/{slug}/"}, {Name: "page"}, {Name: "works"}, {Name: "attachment"}, {Name: "recipes"}, {Name: "works"},
			{Name: "nav_menu_item"},
		}
		pl, rep := w.guess(t, defs, true)
		var names []string
		for name := range pl.Types {
			names = append(names, name)
		}
		slices.Sort(names)
		if want := []string{"recipes", "works"}; !slices.Equal(names, want) {
			t.Errorf("types %q, want %q", names, want)
		}
		// A type without links has the default rule of WordPress, after the front of the posts.
		if got, want := publicPermalink(pl.Types["recipes"]), (Permalink{Rule: "/archives/recipes/{slug}/"}); !samePermalink(got, want) {
			t.Errorf("recipes = %+v, want %+v", got, want)
		}
		if lines := linesAt(t, rep, "/types/recipes/url"); !slices.Equal(lines, []string{
			permNoLink("/types/recipes/url", "the post type recipes", "/archives/recipes/{slug}/"),
		}) {
			t.Errorf("lines %q", lines)
		}
		if pl.Post.Rule != "/archives/{number}/" {
			t.Errorf("the rule of the posts in the schema is not used: %q", pl.Post.Rule)
		}
		checkPermalinkRules(t, pl, &content.Site{Types: []content.TypeDef{{Name: "works"}, {Name: "recipes"}}})
	})
	t.Run("a hierarchy that is only in the items", func(t *testing.T) {
		w := baseSite()
		w.structure = "/%postname%/"
		w.item(32).parent = 31 // the type is not hierarchical, so the link of the child has only its own slug
		pl, _ := w.guess(t, permDefs(), true)
		if got, want := publicPermalink(pl.Types["works"]), permAll("/works/%works%/", "/works/{slug}/", 2); !samePermalink(got, want) {
			t.Errorf("works = %+v, want %+v", got, want)
		}
	})
	t.Run("a hierarchy of the type without a child", func(t *testing.T) {
		w := baseSite()
		w.structure = "/%postname%/"
		w.types["works"] = wpType{hierarchical: true}
		pl, _ := w.guess(t, permDefs(), true)
		// The path and the slug are the same for both: the slug is the first.
		if got, want := publicPermalink(pl.Types["works"]), permAll("/works/%works%/", "/works/{slug}/", 2); !samePermalink(got, want) {
			t.Errorf("works = %+v, want %+v", got, want)
		}
	})
}

func TestPermalinkAuthors(t *testing.T) {
	const structure = "/%author%/%postname%/"
	w := baseSite()
	w.structure = structure
	w.users[0].slug = "yamada" // the user_nicename of admin differs from the login
	defs := permDefs()

	pl, _ := w.guess(t, defs, true)
	if got, want := publicPermalink(pl.Post), (Permalink{Structure: structure, Rule: "/yamada/{slug}/", Links: 6, Matched: 6, RuleMatched: 4}); !samePermalink(got, want) {
		t.Errorf("with the REST API: Post = %+v, want %+v", got, want)
	}
	if pl.Author.Structure != "/author/%author%/" || pl.Author.Links != 2 {
		t.Errorf("Author = %+v", publicPermalink(pl.Author))
	}

	// Without the REST API the slug is the one that the login makes, which the links of admin do not have.
	pl, rep := w.guess(t, defs, false)
	want := Permalink{Structure: "/yamada/%postname%/", Rule: "/yamada/{slug}/", Links: 6, Matched: 4, RuleMatched: 4}
	if got := publicPermalink(pl.Post); !samePermalink(got, want) {
		t.Errorf("without the REST API: Post = %+v, want %+v", got, want)
	}
	if lines := linesAt(t, rep, ptPost); len(lines) != 1 || !strings.HasSuffix(strings.SplitN(lines[0], " -> ", 2)[0],
		`from 4 of 6 links; the URL rule is "/yamada/{slug}/"; 2 links do not follow it, so the import will need redirects for them`) {
		t.Errorf("lines %q", lines)
	}
	if pl.Front != "/yamada/" {
		t.Errorf("Front = %q", pl.Front)
	}
}

func TestPermalinkDefaultCategory(t *testing.T) {
	const structure = "/%category%/%postname%/"
	site := func() *wpSite {
		w := baseSite()
		w.structure = structure
		w.item(16).terms = []int64{1}
		return w
	}
	post := func(id int64, slug, link string) wpItem {
		return wpItem{id: id, typ: "post", slug: slug, date: "2026-12-05 10:00:00", author: "admin", link: link}
	}
	line := func(msg string) string { return permLine(ptPost, msg) }
	defaultLines := func(t *testing.T, rep *Report) []string {
		t.Helper()
		var lines []string
		for _, l := range linesAt(t, rep, ptPost) {
			if !strings.Contains(l, "inferred the permalink structure") {
				lines = append(lines, l)
			}
		}
		return lines
	}

	t.Run("the most common slug in the links of the posts without a category", func(t *testing.T) {
		w := site()
		w.items = append(w.items, post(18, "x-post", "https://example.com/news/x-post/"), post(19, "y-post", "https://example.com/news/y-post/"),
			post(20, "z-post", "https://example.com/uncategorized/z-post/"))
		pl, rep := w.guess(t, permDefs(), true)
		if pl.DefaultCategory != "news" {
			t.Errorf("DefaultCategory = %q", pl.DefaultCategory)
		}
		if got, want := defaultLines(t, rep), []string{line(`posts without a category had "news" in their links; it is the default category`)}; !slices.Equal(got, want) {
			t.Errorf("lines %q, want %q", got, want)
		}
	})
	t.Run("the category that is first in the WXR if they are as common", func(t *testing.T) {
		w := site()
		w.items = append(w.items, post(18, "x-post", "https://example.com/news/x-post/"), post(20, "z-post", "https://example.com/uncategorized/z-post/"))
		pl, _ := w.guess(t, permDefs(), true)
		if pl.DefaultCategory != "uncategorized" {
			t.Errorf("DefaultCategory = %q", pl.DefaultCategory)
		}
		slices.Reverse(w.terms)
		pl, _ = w.guess(t, permDefs(), true)
		if pl.DefaultCategory != "news" {
			t.Errorf("with the categories the other way round: DefaultCategory = %q", pl.DefaultCategory)
		}
	})
	t.Run("the slug as it is written", func(t *testing.T) {
		w := site()
		w.terms = append(w.terms, wpTerm{id: 6, tax: "category", slug: "%e6%97%a5%e6%9c%ac"})
		w.items = append(w.items, post(18, "x-post", "https://example.com/%E6%97%A5%E6%9C%AC/x-post/"))
		pl, _ := w.guess(t, permDefs(), true)
		if pl.DefaultCategory != "%e6%97%a5%e6%9c%ac" {
			t.Errorf("DefaultCategory = %q", pl.DefaultCategory)
		}
	})
	t.Run("a post without a category that has a plain link", func(t *testing.T) {
		w := site()
		w.item(17).terms = []int64{4} // a tag is not a category
		pl, rep := w.guess(t, permDefs(), true)
		if pl.DefaultCategory != "uncategorized" {
			t.Errorf("DefaultCategory = %q", pl.DefaultCategory)
		}
		want := []string{line(`1 post has no category, which {category} needs; the default category is "uncategorized", which WordPress has by default (term 1)`)}
		if got := defaultLines(t, rep); !slices.Equal(got, want) {
			t.Errorf("lines %q, want %q", got, want)
		}
		w.items = append(w.items, wpItem{id: 18, typ: "post", status: "pending", slug: "p", author: "admin"})
		_, rep = w.guess(t, permDefs(), true)
		want = []string{line(`2 posts have no category, which {category} needs; the default category is "uncategorized", which WordPress has by default (term 1)`)}
		if got := defaultLines(t, rep); !slices.Equal(got, want) {
			t.Errorf("lines %q, want %q", got, want)
		}
	})
	t.Run("posts in the trash and automatic drafts do not need a category", func(t *testing.T) {
		w := site()
		w.item(17).terms = nil
		w.item(17).status, w.item(17).link = "trash", "https://example.com/?p=17"
		w.items = append(w.items, wpItem{id: 18, typ: "post", status: "auto-draft", slug: "a", author: "admin"})
		pl, rep := w.guess(t, permDefs(), true)
		if pl.DefaultCategory != "" || len(defaultLines(t, rep)) != 0 {
			t.Errorf("DefaultCategory = %q, lines %q", pl.DefaultCategory, defaultLines(t, rep))
		}
	})
	t.Run("no category with the ID 1", func(t *testing.T) {
		w := site()
		w.terms = w.terms[1:]
		for i := range w.items {
			w.items[i].terms = slices.DeleteFunc(w.items[i].terms, func(id int64) bool { return id == 1 })
			if w.items[i].typ == "post" && w.items[i].status == "" && len(w.items[i].terms) == 0 {
				w.items[i].terms = []int64{2}
			}
		}
		w.item(17).terms = nil
		pl, rep := w.guess(t, permDefs(), true)
		if pl.DefaultCategory != "" {
			t.Errorf("DefaultCategory = %q", pl.DefaultCategory)
		}
		want := []string{line("1 post has no category, which {category} needs; set the default category")}
		if got := defaultLines(t, rep); !slices.Equal(got, want) {
			t.Errorf("lines %q, want %q", got, want)
		}
	})
	t.Run("no tag for the category in the structure", func(t *testing.T) {
		w := baseSite()
		w.structure = "/%postname%/"
		w.item(17).terms = nil
		pl, rep := w.guess(t, permDefs(), true)
		if pl.DefaultCategory != "" || len(defaultLines(t, rep)) != 0 {
			t.Errorf("DefaultCategory = %q, lines %q", pl.DefaultCategory, defaultLines(t, rep))
		}
	})
}

func TestPermalinkMessages(t *testing.T) {
	posts := func(structure string, n int) *wpSite {
		w := baseSite()
		w.structure = structure
		w.items = w.items[:0]
		for i := range n {
			id := strconv.Itoa(i + 1)
			w.items = append(w.items, wpItem{id: int64(i + 1), typ: "post", slug: "p" + id, date: "2026-10-0" + id + " 10:00:00", author: "admin", terms: []int64{1}})
		}
		return w
	}
	postLines := func(t *testing.T, w *wpSite) []string {
		t.Helper()
		_, rep := w.guess(t, permDefs(), true)
		return linesAt(t, rep, ptPost)
	}
	tests := []struct {
		name string
		site func() *wpSite
		want string
	}{
		{"one link", func() *wpSite { return posts("/%postname%/", 1) },
			`inferred the permalink structure "/%postname%/" of posts from 1 of 1 link; the URL rule is "/{slug}/"`},
		{"all links", func() *wpSite { return posts("/%postname%/", 3) },
			`inferred the permalink structure "/%postname%/" of posts from 3 of 3 links; the URL rule is "/{slug}/"`},
		{"one link that does not follow", func() *wpSite {
			w := posts("/%postname%/", 3)
			w.items[2].link = "https://example.com/other/path/"
			return w
		}, `inferred the permalink structure "/%postname%/" of posts from 2 of 3 links; the URL rule is "/{slug}/"; ` +
			`1 link does not follow it, so the import will need a redirect for it`},
		{"links that do not follow", func() *wpSite {
			w := posts("/%postname%/", 4)
			w.items[1].link = "https://example.com/other/path/"
			w.items[2].link = "https://example.com/another/path/"
			w.items[3].link = "https://example.com/third/path/"
			return w
		}, `inferred the permalink structure "/%postname%/" of posts from 1 of 4 links; the URL rule is "/{slug}/"; ` +
			`3 links do not follow it, so the import will need redirects for them`},
		{"one link of a structure that is not a rule", func() *wpSite { return posts("/%author%/%postname%/", 1) },
			`the permalink structure "/%author%/%postname%/" of posts, which 1 of 1 link follows, cannot be a URL rule: ` +
				`%author% has no placeholder; the URL rule is "/admin/{slug}/", which 1 of them follows`},
		{"links of a structure that is not a rule", func() *wpSite { return posts("/%author%/%postname%/", 3) },
			`the permalink structure "/%author%/%postname%/" of posts, which 3 of 3 links follow, cannot be a URL rule: ` +
				`%author% has no placeholder; the URL rule is "/admin/{slug}/", which 3 of them follow`},
		{"no rule for the structure", func() *wpSite { return posts("/%postname%.htm", 2) },
			`the permalink structure "/%postname%.htm" of posts, which 2 of 2 links follow, cannot be a URL rule: ` +
				`a URL rule must end with "/" or ".html", not ".htm"; the URL rule is the default "/{yyyy}/{mm}/{dd}/{slug}/"`},
		{"no links", func() *wpSite { return posts("/%postname%/", 0) },
			`no link of posts has a path (the permalinks may be Plain); the URL rule is the default "/{yyyy}/{mm}/{dd}/{slug}/"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := postLines(t, tt.site())
			if want := []string{permLine(ptPost, tt.want)}; !slices.Equal(got, want) {
				t.Errorf("lines\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
		})
	}

	// Every entry is to be reviewed, is about the channel, is in the schema file, and is one line.
	w := baseSite()
	w.structure = "/%category%/%postname%.html"
	_, rep := w.guess(t, permDefs(), true)
	for _, e := range rep.Entries() {
		if e.Kind != Review || e.Source != ChannelSource("site.xml", w.export()) || e.Target.File != "site/schema.yaml" ||
			!strings.HasPrefix(e.Target.Pointer, "/") || strings.Contains(e.Message, "\n") {
			t.Errorf("entry %+v", e)
		}
	}
}

func TestPermalinkPages(t *testing.T) {
	w := baseSite()
	w.structure = "/%postname%/"
	w.items = append(w.items,
		wpItem{id: 25, typ: "page", slug: "map", parent: 22},
		wpItem{id: 26, typ: "page", slug: "detail", parent: 25},
		// WordPress stops at a parent that is not there.
		wpItem{id: 27, typ: "page", slug: "lonely", parent: 99, link: "https://example.com/lonely/"},
		wpItem{id: 28, typ: "page", status: "draft", slug: "draft-page"},
		// The page of the posts is a page like the others.
		wpItem{id: 29, typ: "page", slug: "blog"},
	)
	pl, rep := w.guess(t, permDefs(), true)
	if got, want := publicPermalink(pl.Page), permAll("/%pagename%/", "/{path}/", 7); !samePermalink(got, want) {
		t.Errorf("Page = %+v, want %+v", got, want)
	}
	if lines := linesAt(t, rep, ptPage); !slices.Equal(lines, []string{permInferred(ptPage, "pages", permAll("/%pagename%/", "/{path}/", 7))}) {
		t.Errorf("lines %q", lines)
	}

	// With index.php, and a slash at the end of none.
	w.structure = "/index.php/%postname%"
	pl, _ = w.guess(t, permDefs(), true)
	// (The link of the page that is not under its parent has no index.php.)
	if got, want := publicPermalink(pl.Page), (Permalink{Structure: "/index.php/%pagename%", Rule: "/index.php/{path}/", Links: 7, Matched: 6, RuleMatched: 6}); !samePermalink(got, want) {
		t.Errorf("with index.php: Page = %+v, want %+v", got, want)
	}

	// The page of a path in its parent's, with the wrong slug in the WXR: it does not follow.
	w.structure = "/%postname%/"
	w.item(26).link = "https://example.com/about/access/other/"
	pl, _ = w.guess(t, permDefs(), true)
	if pl.Page.Links != 7 || pl.Page.Matched != 6 {
		t.Errorf("Page = %+v", publicPermalink(pl.Page))
	}
}

func TestPermalinkHome(t *testing.T) {
	w := baseSite()
	w.structure = "/%postname%/"
	defs := permDefs()
	guess := func(change func(e *wxr.Export)) *Permalinks {
		t.Helper()
		e := w.export()
		change(e)
		imp := New("site.xml", e, NewReport(Info{File: "site.xml"}))
		// The REST API is that of the site of the link; a snapshot cannot be checked without a link, so there is none.
		if snap := w.snapshot(); strings.TrimSpace(e.Channel.Link) != "" {
			snap.Index.Home = e.Channel.Link
			imp.REST = snap
		}
		pl, err := imp.InferPermalinks(defs)
		if err != nil {
			t.Fatal(err)
		}
		checkPermalinkRules(t, pl, defs)
		return pl
	}
	if pl := guess(func(*wxr.Export) {}); pl.Post.Links != 6 {
		t.Errorf("Links = %d", pl.Post.Links)
	}
	// The address of the blog is used if the channel has no link.
	if pl := guess(func(e *wxr.Export) { e.Channel.Link = "" }); pl.Post.Links != 6 || pl.Page.Links != 3 {
		t.Errorf("without the link of the channel: Links = %d, %d", pl.Post.Links, pl.Page.Links)
	}
	if pl := guess(func(e *wxr.Export) { e.Channel.Link, e.Channel.BaseBlogURL = "https://example.com/", "" }); pl.Post.Links != 6 {
		t.Errorf("with a slash at the end: Links = %d", pl.Post.Links)
	}
	// No address, or one that is not an address of a site: no link has a path of its own.
	for _, home := range []string{"", "example.com", "ftp://example.com", "https://", "mailto:a@example.com", "https://user@example.com"} {
		pl := guess(func(e *wxr.Export) { e.Channel.Link, e.Channel.BaseBlogURL = home, "" })
		if pl.Post.Links != 0 || pl.Page.Links != 0 || pl.Front != "/" {
			t.Errorf("home %q: Links = %d, %d, Front = %q", home, pl.Post.Links, pl.Page.Links, pl.Front)
		}
	}
	// A link without a host is not on the host of a home without one.
	pl := guess(func(e *wxr.Export) {
		e.Channel.Link, e.Channel.BaseBlogURL = "https://", ""
		for i := range e.Items {
			e.Items[i].Link = "https:///" + e.Items[i].Name + "/"
		}
	})
	if pl.Post.Links != 0 || pl.Page.Links != 0 {
		t.Errorf("home without a host: Links = %d, %d", pl.Post.Links, pl.Page.Links)
	}
	// The links on another host are left out.
	pl = guess(func(e *wxr.Export) {
		for i := range e.Items {
			if e.Items[i].ID%2 == 0 {
				e.Items[i].Link = strings.Replace(e.Items[i].Link, "example.com", "example.net", 1)
			}
		}
	})
	if pl.Post.Links != 3 || pl.Post.Matched != 3 || pl.Page.Links != 2 {
		t.Errorf("Post = %+v, Page = %+v", publicPermalink(pl.Post), publicPermalink(pl.Page))
	}
}

func TestPermalinkNothing(t *testing.T) {
	if pl, err := (&Import{}).InferPermalinks(nil); err == nil || pl != nil {
		t.Errorf("InferPermalinks without a WXR = %v, %v", pl, err)
	}

	// No schema file: only the built in ones.
	w := baseSite()
	w.structure = "/%postname%/"
	pl, rep := w.guess(t, nil, true)
	if len(pl.Types) != 0 || len(pl.Taxonomies) != 2 {
		t.Errorf("%d types and %d taxonomies, want 0 and 2", len(pl.Types), len(pl.Taxonomies))
	}
	if lines := linesAt(t, rep, ptWorks); len(lines) != 0 {
		t.Errorf("lines about works: %q", lines)
	}
	checkPermalinkRules(t, pl, &content.Site{})

	// An empty WXR.
	imp := New("site.xml", &wxr.Export{}, NewReport(Info{File: "site.xml"}))
	pl, err := imp.InferPermalinks(&content.Site{})
	if err != nil {
		t.Fatal(err)
	}
	if pl.Front != "/" || pl.Post.Rule != content.DefaultPostURL || pl.Page.Rule != content.DefaultPageURL ||
		pl.Author.Rule != content.DefaultAuthorURL || pl.Taxonomies["category"].Rule != content.DefaultCategoryURL ||
		pl.Taxonomies["tag"].Rule != content.DefaultTagURL {
		t.Errorf("%+v", pl)
	}
	checkPermalinkRules(t, pl, &content.Site{})
}

func TestPermalinkRepeat(t *testing.T) {
	for _, structure := range []string{
		"", "/%year%/%monthnum%/%day%/%postname%/", "/archives/%post_id%", "/%category%/%postname%.html", "/%author%/%postname%/",
		"/%year%/%monthnum%/%day%/%hour%%minute%/%postname%/", "/%category%-%postname%/",
	} {
		w := baseSite()
		w.structure = structure
		first, firstRep := w.guess(t, permDefs(), true)
		second, secondRep := w.guess(t, permDefs(), true)
		if !reflect.DeepEqual(first, second) || !slices.Equal(reportLines(t, firstRep), reportLines(t, secondRep)) {
			t.Errorf("%q: two guesses differ", structure)
		}

		// The order of the items does not matter: the links are looked at in the order of their IDs.
		slices.Reverse(w.items)
		slices.Reverse(w.users)
		third, thirdRep := w.guess(t, permDefs(), true)
		if !reflect.DeepEqual(first, third) || !slices.Equal(reportLines(t, firstRep), reportLines(t, thirdRep)) {
			t.Errorf("%q: the guess with the items the other way round differs:\n%+v\n%+v", structure, first.Post, third.Post)
		}
	}
}

func TestPermalinkLarge(t *testing.T) {
	const n = 20000
	w := baseSite()
	w.structure = "/%year%/%monthnum%/%day%/%postname%/"
	w.items = slices.DeleteFunc(w.items, func(it wpItem) bool { return it.typ == "post" })
	for i := range n {
		date := fmt.Sprintf("%d-%02d-%02d %02d:%02d:00", 2000+i%26, i%12+1, i%28+1, i%24, i%60)
		w.items = append(w.items, wpItem{
			id: int64(1000 + i), typ: "post", slug: "post-" + strconv.Itoa(i), date: date,
			author: []string{"admin", "editor"}[i%2], terms: []int64{int64(1 + i%3)},
		})
	}
	e, snap := w.export(), w.snapshot()
	rep := NewReport(Info{File: "site.xml"})
	imp := New("site.xml", e, rep)
	imp.REST = snap
	start := time.Now()
	pl, err := imp.InferPermalinks(permDefs())
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("the guess for %d posts took %v", n, elapsed)
	if got, want := publicPermalink(pl.Post), permAll("/%year%/%monthnum%/%day%/%postname%/", "/{yyyy}/{mm}/{dd}/{slug}/", n); !samePermalink(got, want) {
		t.Errorf("Post = %+v, want %+v", got, want)
	}
	if elapsed > 30*time.Second {
		t.Errorf("the guess took %v", elapsed)
	}

	// Links that follow no structure: the work is bounded all the same.
	for i := range w.items {
		if w.items[i].typ == "post" {
			w.items[i].link = fmt.Sprintf("https://example.com/a%d/b%d/c%d/post-%d/", i%7, i%13, i%31, i)
		}
	}
	e = w.export()
	imp = New("site.xml", e, NewReport(Info{File: "site.xml"}))
	start = time.Now()
	pl, err = imp.InferPermalinks(permDefs())
	elapsed = time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("the guess for %d posts that follow no structure took %v", n, elapsed)
	if pl.Post.Links != n || pl.Post.Matched < 1 || elapsed > 30*time.Second {
		t.Errorf("Post = %+v after %v", publicPermalink(pl.Post), elapsed)
	}
}

func TestPermalinkDefaultRules(t *testing.T) {
	defs := permDefs()
	t.Run("index.php", func(t *testing.T) {
		// Without links to look at, the pages have the default rules of WordPress for the front of the posts.
		w := baseSite()
		w.structure = "/index.php/%postname%/"
		w.items = slices.DeleteFunc(w.items, func(it wpItem) bool { return it.typ == "page" || it.typ == "works" })
		pl, _ := w.guess(t, defs, false)
		if pl.Front != "/index.php/" {
			t.Errorf("Front = %q", pl.Front)
		}
		for name, c := range map[string]struct{ got, want string }{
			"page":     {pl.Page.Rule, "/index.php/{path}/"},
			"works":    {pl.Types["works"].Rule, "/index.php/works/{slug}/"},
			"author":   {pl.Author.Rule, "/index.php/author/{slug}/"},
			"category": {pl.Taxonomies["category"].Rule, "/index.php/category/{path}/"},
			"tag":      {pl.Taxonomies["tag"].Rule, "/index.php/tag/{slug}/"},
		} {
			if c.got != c.want {
				t.Errorf("%s: rule %q, want %q", name, c.got, c.want)
			}
		}
		checkPermalinkRules(t, pl, defs)
	})
	t.Run("text before the first tag", func(t *testing.T) {
		w := baseSite()
		w.structure = "/blog-%postname%/"
		w.items = slices.DeleteFunc(w.items, func(it wpItem) bool { return it.typ == "page" || it.typ == "works" })
		pl, _ := w.guess(t, defs, false)
		if pl.Front != "/blog-" || pl.Post.Structure != "/blog-%postname%/" || pl.Post.Rule != "/blog-{slug}/" {
			t.Errorf("Front = %q, Post = %+v", pl.Front, publicPermalink(pl.Post))
		}
		// The text is not a directory, as WordPress puts the names after it: /blog-author/yamada/.
		for name, c := range map[string]struct{ got, want string }{
			"page":     {pl.Page.Rule, "/{path}/"},
			"works":    {pl.Types["works"].Rule, "/blog-works/{slug}/"},
			"author":   {pl.Author.Rule, "/blog-author/{slug}/"},
			"category": {pl.Taxonomies["category"].Rule, "/blog-category/{path}/"},
			"tag":      {pl.Taxonomies["tag"].Rule, "/blog-tag/{slug}/"},
		} {
			if c.got != c.want {
				t.Errorf("%s: rule %q, want %q", name, c.got, c.want)
			}
		}
		checkPermalinkRules(t, pl, defs)
	})
	t.Run("a front that cannot be in a rule", func(t *testing.T) {
		// The text of the links is kept as it is, so a space is in it as %20, which a rule cannot have.
		w := baseSite()
		w.structure = "/%postname%/"
		w.items = slices.DeleteFunc(w.items, func(it wpItem) bool { return it.typ == "page" || it.typ == "works" })
		for i := range w.items {
			w.items[i].link = "https://example.com/a%20b/" + w.items[i].slug + "/"
		}
		pl, _ := w.guess(t, defs, false)
		if pl.Front != "/a%20b/" || pl.Post.Structure != "/a%20b/%postname%/" || pl.Post.Rule != content.DefaultPostURL {
			t.Errorf("Front = %q, Post = %+v", pl.Front, publicPermalink(pl.Post))
		}
		for name, c := range map[string]struct{ got, want string }{
			"works":    {pl.Types["works"].Rule, "/works/{slug}/"},
			"author":   {pl.Author.Rule, content.DefaultAuthorURL},
			"category": {pl.Taxonomies["category"].Rule, content.DefaultCategoryURL},
			"tag":      {pl.Taxonomies["tag"].Rule, content.DefaultTagURL},
		} {
			if c.got != c.want {
				t.Errorf("%s: rule %q, want %q", name, c.got, c.want)
			}
		}
		checkPermalinkRules(t, pl, defs)
	})
	t.Run("links without a tag", func(t *testing.T) {
		// The links have nothing of the post in them, so no tag is in the structure, and the front is not known.
		w := baseSite()
		w.structure = "/%postname%/"
		w.items = w.items[:1]
		w.items[0].link = "https://example.com/special-offer/"
		pl, rep := w.guess(t, defs, true)
		want := Permalink{Structure: "/special-offer/", Rule: content.DefaultPostURL, Links: 1, Matched: 1}
		if !samePermalink(pl.Post, want) || pl.Front != "/" {
			t.Errorf("Post = %+v, Front = %q", publicPermalink(pl.Post), pl.Front)
		}
		wantLine := permLine(ptPost, `the permalink structure "/special-offer/" of posts, which 1 of 1 link follows, cannot be a URL rule: `+
			`invalid URL rule "/special-offer/": missing {slug} or {number}; the URL rule is the default "/{yyyy}/{mm}/{dd}/{slug}/"`)
		if lines := linesAt(t, rep, ptPost); !slices.Equal(lines, []string{wantLine}) {
			t.Errorf("lines %q, want %q", lines, wantLine)
		}
	})
}

func TestPermalinkNoHierarchy(t *testing.T) {
	// Without a child, the path of a category is its slug, and the rule of WordPress for categories is the path, as the
	// hierarchy is in the links as soon as there is one. The path of a page is the rule for pages too.
	w := baseSite()
	w.structure = "/%postname%/"
	w.terms = slices.DeleteFunc(w.terms, func(tm wpTerm) bool { return tm.id == 3 })
	w.item(13).terms, w.item(14).terms = []int64{2}, []int64{2}
	w.item(22).parent = 0
	pl, _ := w.guess(t, permDefs(), true)
	if got, want := publicPermalink(pl.Taxonomies["category"]), permAll("/category/%category%/", "/category/{path}/", 2); !samePermalink(got, want) {
		t.Errorf("category = %+v, want %+v", got, want)
	}
	if got, want := publicPermalink(pl.Page), permAll("/%pagename%/", "/{path}/", 3); !samePermalink(got, want) {
		t.Errorf("Page = %+v, want %+v", got, want)
	}
	if got, want := publicPermalink(pl.Taxonomies["tag"]), permAll("/tag/%post_tag%/", "/tag/{slug}/", 2); !samePermalink(got, want) {
		t.Errorf("tag = %+v, want %+v", got, want)
	}
}

func TestPermalinkHierarchyOfItems(t *testing.T) {
	// The REST API is not there, so the items tell that the type has a hierarchy: a child has the slug of its parent in
	// its link, and the path is what the structure has.
	w := baseSite()
	w.structure = "/%postname%/"
	w.types["works"] = wpType{hierarchical: true}
	w.item(32).parent = 31
	pl, rep := w.guess(t, permDefs(), false)
	want := Permalink{Structure: "/works/%works%/", Rule: "/works/{slug}/", Links: 2, Matched: 2, RuleMatched: 1}
	if got := publicPermalink(pl.Types["works"]); !samePermalink(got, want) {
		t.Errorf("works = %+v, want %+v", got, want)
	}
	if lines := linesAt(t, rep, ptWorks); len(lines) != 1 || !strings.Contains(lines[0], `placeholder "{path}" is not allowed for works`) {
		t.Errorf("lines %q", lines)
	}
}

func TestPermalinkLoginSlug(t *testing.T) {
	// Without the REST API, an author has the slug that WordPress makes of the login.
	w := baseSite()
	w.structure = "/%author%/%postname%/"
	w.users = append(w.users, wpUser{3, "Taro.Yamada Jr", "taro-yamada-jr"})
	w.items = append(w.items,
		wpItem{id: 18, typ: "post", slug: "t1", date: "2026-12-05 10:00:00", author: "Taro.Yamada Jr", terms: []int64{1}},
		wpItem{id: 19, typ: "post", slug: "t2", date: "2026-12-06 10:00:00", author: "Taro.Yamada Jr", terms: []int64{1}},
	)
	for _, rest := range []bool{false, true} {
		pl, _ := w.guess(t, permDefs(), rest)
		if pl.Post.Structure != "/%author%/%postname%/" || pl.Post.Links != 8 || pl.Post.Matched != 8 {
			t.Errorf("with the REST API %v: Post = %+v", rest, publicPermalink(pl.Post))
		}
	}
}

func TestPermalinkSamples(t *testing.T) {
	w := baseSite()
	w.structure = "/%postname%/"
	slices.Reverse(w.items)
	slices.Reverse(w.terms)
	slices.Reverse(w.users)
	snap := w.snapshot()
	slices.Reverse(snap.Terms)
	slices.Reverse(snap.Users)
	imp := New("site.xml", w.export(), NewReport(Info{File: "site.xml"}))
	imp.REST = snap
	in := newPermInference(imp, permDefs())
	ids := func(samples []*sample) []int64 {
		var out []int64
		for _, s := range samples {
			out = append(out, s.id)
		}
		return out
	}
	for _, c := range []struct {
		name    string
		samples []*sample
		want    []int64
	}{
		{"posts", in.postSamples(), []int64{11, 12, 13, 14, 15, 16}},
		{"pages", in.itemSamples("page", true, true), []int64{21, 22, 23}},
		{"works", in.itemSamples("works", false, false), []int64{31, 32}},
		{"categories", in.termSamples("category", true), []int64{1, 2, 3}},
		{"tags", in.termSamples("post_tag", false), []int64{4, 5}},
		{"users", in.userSamples(), []int64{1, 2}},
	} {
		if got := ids(c.samples); !slices.Equal(got, c.want) {
			t.Errorf("%s: IDs %v, want %v", c.name, got, c.want)
		}
	}
	// The values of a sample.
	var hello *sample
	for _, s := range in.postSamples() {
		if s.id == 13 {
			hello = s
		}
	}
	want := [numTags]string{
		tagSlug: "third-post", tagNumber: "13", tagYear: "2026", tagMonth: "11", tagDay: "02", tagAuthor: "editor",
		tagHour: "14", tagMinute: "05", tagSecond: "45",
	}
	if hello.path != "/third-post/" || hello.vals != want || !slices.Equal(hello.cats, []string{"news/press-release"}) {
		t.Errorf("sample %+v", hello)
	}
}

func TestPermalinkGenerateTags(t *testing.T) {
	// A tag is used once.
	s := &sample{path: "/2026/2026/"}
	s.vals[tagYear] = "2026"
	got := generate(s, []tag{tagYear}, 256)
	want := []template{tpl("/", tagYear, "/2026/"), tpl("/2026/", tagYear, "/"), tpl("/2026/2026/")}
	if !slices.EqualFunc(got, want, func(a, b template) bool { return a.key() == b.key() }) {
		t.Errorf("templates %+v, want %+v", got, want)
	}

	// None, if there is no room.
	if got := generate(s, []tag{tagYear}, 0); len(got) != 0 {
		t.Errorf("%d templates with the limit 0", len(got))
	}

	// Values that are the same in many places make more templates than the limit.
	many := &sample{path: "/10/10/10/10/10/10/"}
	many.vals[tagNumber], many.vals[tagYear], many.vals[tagMonth], many.vals[tagDay] = "10", "10", "10", "10"
	many.vals[tagHour], many.vals[tagMinute], many.vals[tagSecond] = "10", "10", "10"
	if got := generate(many, postTags, 256); len(got) != 256 {
		t.Errorf("%d templates, want 256", len(got))
	}
	if got := generate(many, postTags, 1000000); len(got) <= 256 {
		t.Errorf("%d templates with a large limit", len(got))
	}
}

func TestPermalinkLimits(t *testing.T) {
	// What is looked at: the first 16 links make candidates, 256 of the links spread over all count them, and the best 8 are
	// counted on all.
	if permSamples != 16 || permCandidates != 256 || permCounted != 256 || permKept != 8 {
		t.Errorf("limits %d, %d, %d, %d", permSamples, permCandidates, permCounted, permKept)
	}
	// A page or a term with its ancestors has up to 100 levels, and a path up to 8192 bytes; a post without a category can
	// have the slug of one of 1000 categories in the candidates.
	if permDepth != 100 || permPathBytes != 8192 || permCategories != 1000 {
		t.Errorf("limits %d, %d, %d", permDepth, permPathBytes, permCategories)
	}
	var samples []*sample
	for i := range 17 {
		s := &sample{id: int64(i + 1), path: "/d" + strconv.Itoa(i) + "/p" + strconv.Itoa(i) + "/"}
		s.vals[tagSlug] = "p" + strconv.Itoa(i)
		samples = append(samples, s)
	}
	cands := candidates(samples, []tag{tagSlug})
	has := func(i int) bool {
		want := tpl("/d"+strconv.Itoa(i)+"/", tagSlug, "/")
		return slices.ContainsFunc(cands, func(c template) bool { return c.key() == want.key() })
	}
	if !has(0) || !has(15) || has(16) {
		t.Errorf("candidates of the links 0, 15 and 16: %v, %v, %v, want true, true, false", has(0), has(15), has(16))
	}

	// Two templates are two candidates, also if their text and their tags make the same string.
	s := &sample{id: 1, path: "/xT1:Ly"}
	s.vals[tagSlug] = "T1:L"
	if cands := candidates([]*sample{s}, []tag{tagSlug}); len(cands) != 2 {
		t.Errorf("%d candidates, want 2: %+v", len(cands), cands)
	}
}

func TestPermalinkPathLength(t *testing.T) {
	home, err := url.Parse("https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	path := "/" + strings.Repeat("a", permPathBytes-1)
	if got, ok := linkPath(home, "https://example.com"+path); !ok || got != path {
		t.Errorf("linkPath of %d bytes = %d bytes, %v", len(path), len(got), ok)
	}
	if got, ok := linkPath(home, "https://example.com"+path+"a"); ok || got != "" {
		t.Errorf("linkPath of %d bytes = %d bytes, %v", len(path)+1, len(got), ok)
	}
	if v := slugValue(strings.Repeat("a", permPathBytes)); len(v) != permPathBytes {
		t.Errorf("slugValue of %d bytes = %d bytes", permPathBytes, len(v))
	}
	if v := slugValue(strings.Repeat("a", permPathBytes+1)); v != "" {
		t.Errorf("slugValue of %d bytes = %d bytes", permPathBytes+1, len(v))
	}

	// The slugs of the ancestors count too.
	long := strings.Repeat("b", 4000)
	tr := newTree()
	tr.add(1, long, 0)
	tr.add(2, long, 1)
	tr.add(3, long, 2)
	c := newCategories(&wxr.Export{Terms: []wxr.Term{
		{ID: 1, Taxonomy: "category", Slug: long + "1"},
		{ID: 2, Taxonomy: "category", Slug: long + "2", Parent: long + "1"},
		{ID: 3, Taxonomy: "category", Slug: long + "3", Parent: long + "2"},
	}})
	if got := tr.path(2); len(got) != 2*4000+1 {
		t.Errorf("tree path of two = %d bytes", len(got))
	}
	if got := tr.path(3); got != "" {
		t.Errorf("tree path of three = %d bytes", len(got))
	}
	if got := c.path(long + "2"); len(got) != 2*4001+1 {
		t.Errorf("category path of two = %d bytes", len(got))
	}
	if got := c.path(long + "3"); got != "" {
		t.Errorf("category path of three = %d bytes", len(got))
	}
}

func TestPermalinkRankSpread(t *testing.T) {
	// The first 15 links have a directory each, then comes one of the long directory, which all the links after the first
	// 256 have. Counted on the first 256 links only, the long directory would be one of many with one link.
	const long = "/a-long-name-of-a-directory/"
	var samples []*sample
	for i := range 1000 {
		path := "/d" + strconv.Itoa(i) + "/post-" + strconv.Itoa(i) + "/"
		if i == 15 || i >= 256 {
			path = long + "post-" + strconv.Itoa(i) + "/"
		}
		s := &sample{id: int64(i + 1), path: path}
		s.vals[tagSlug] = "post-" + strconv.Itoa(i)
		samples = append(samples, s)
	}
	ranked := rank(candidates(samples, []tag{tagSlug}), samples)
	if want := tpl(long, tagSlug, "/"); ranked[0].t.key() != want.key() || ranked[0].n != 745 {
		t.Errorf("the best is %+v with %d links, want %+v with 745", ranked[0].t, ranked[0].n, want)
	}
}

func TestPermalinkTokens(t *testing.T) {
	w := baseSite()
	w.structure = "/%year%/%monthnum%/%postname%/"
	w.catBase = "topics"
	pl, _ := w.guess(t, permDefs(), true)
	for _, c := range []struct {
		name string
		got  template
		want template
	}{
		{"Post", pl.Post.tokens, tpl("/", tagYear, "/", tagMonth, "/", tagSlug, "/")},
		{"Page", pl.Page.tokens, tpl("/", tagPath, "/")},
		{"Author", pl.Author.tokens, tpl("/author/", tagSlug, "/")},
		{"works", pl.Types["works"].tokens, tpl("/works/", tagSlug, "/")},
		{"category", pl.Taxonomies["category"].tokens, tpl("/topics/", tagPath, "/")},
		{"tag", pl.Taxonomies["tag"].tokens, tpl("/tag/", tagSlug, "/")},
	} {
		if c.got.key() != c.want.key() {
			t.Errorf("%s: tokens %+v, want %+v", c.name, c.got, c.want)
		}
	}
	w.structure = ""
	pl, _ = w.guess(t, permDefs(), true)
	if pl.Post.tokens != nil || pl.Page.tokens != nil || pl.Taxonomies["category"].tokens != nil {
		t.Errorf("tokens of Plain links: %+v, %+v", pl.Post.tokens, pl.Page.tokens)
	}
}

func FuzzInferPermalinks(f *testing.F) {
	f.Add("https://example.com", "https://example.com/2026/10/hello/", "hello", "2026-10-26 09:30:00", "https://example.com/2026/10/world/", "world", "news")
	f.Add("https://example.com/blog", "https://example.com/blog/archives/1", "a", "2026-10-26 09:30:00", "https://example.com/blog/archives/2", "b", "")
	f.Add("https://example.com", "https://example.com/news/hello.html", "hello", "", "https://example.com/uncategorized/world.html", "world", "news")
	f.Add("https://example.com", "https://example.com/%e6%97%a5/", "%e6%97%a5", "x", "https://example.com/?p=2", "", "%e6%97%a5")
	f.Add("", "https://example.com/a/b/c/", "a/b", "2026-10-26 09:30:00", "\n", "{slug}", "{")
	f.Fuzz(func(t *testing.T, home, link1, slug1, date, link2, slug2, cat string) {
		build := func() *Import {
			e := &wxr.Export{
				Channel: wxr.Channel{Link: home},
				Terms:   []wxr.Term{{ID: 1, Taxonomy: "category", Slug: cat}, {ID: 2, Taxonomy: "category", Slug: slug2, Parent: cat}},
				Items: []wxr.Item{
					{ID: 1, Type: "post", Status: "publish", Name: slug1, Link: link1, Date: date, Creator: "a",
						Terms: []wxr.TermRef{{Taxonomy: "category", Slug: cat}}},
					{ID: 2, Type: "post", Status: "publish", Name: slug2, Link: link2, Date: date, Creator: "b"},
					{ID: 3, Type: "page", Status: "publish", Name: slug1, Link: link2, Parent: 2},
					{ID: 4, Type: "page", Status: "publish", Name: slug2, Link: link1, Parent: 3},
					{ID: 5, Type: "works", Status: "publish", Name: slug1, Link: link1, Parent: 5},
				},
			}
			imp := New("f.xml", e, NewReport(Info{File: "f.xml"}))
			// A REST API that does not give its home is refused.
			if strings.TrimSpace(home) != "" {
				imp.REST = &wprest.Snapshot{
					Index:      wprest.Index{Home: home},
					Terms:      []wprest.Term{{ID: 1, Taxonomy: "category", Slug: slug1, Link: link1}, {ID: 2, Taxonomy: "post_tag", Slug: slug2, Link: link2}},
					Users:      []wprest.User{{ID: 1, Slug: slug2, Link: link2}},
					Types:      []wprest.Type{{Slug: "works", HasArchive: wprest.Archive{Set: true, On: true, Slug: slug1}}},
					Taxonomies: []wprest.Taxonomy{{Slug: "category", Hierarchical: true}},
				}
			}
			return imp
		}
		defs := &content.Site{Types: []content.TypeDef{{Name: "works"}}}
		imp := build()
		pl, err := imp.InferPermalinks(defs)
		if err != nil {
			t.Fatal(err)
		}
		checkPermalinkRules(t, pl, defs)
		var text strings.Builder
		if err := imp.Report.WriteText(&text); err != nil {
			t.Fatal(err)
		}
		if strings.Count(text.String(), "\n") != len(imp.Report.Entries()) {
			t.Errorf("an entry of the report is not one line:\n%s", text.String())
		}
		again := build()
		pl2, err := again.InferPermalinks(defs)
		if err != nil {
			t.Fatal(err)
		}
		var text2 strings.Builder
		if err := again.Report.WriteText(&text2); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(pl, pl2) || text.String() != text2.String() {
			t.Errorf("two guesses differ")
		}
	})
}
