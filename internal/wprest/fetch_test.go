package wprest

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// request is what a site got.
type request struct {
	Method string
	Path   string
	Query  url.Values
	Header http.Header
}

// site is a WordPress that serves a REST API: under /wp-json/ if pretty is true, as a site with pretty permalinks does,
// and under /?rest_route= always. It records the requests.
type site struct {
	t      *testing.T
	srv    *httptest.Server
	prefix string // where WordPress is installed, such as /blog

	mu       sync.Mutex
	pretty   bool
	routes   map[string]http.HandlerFunc // by the route, such as /wp/v2/users
	before   func(w http.ResponseWriter, r *http.Request) bool
	requests []request
}

func newSite(t *testing.T) *site {
	t.Helper()
	s := &site{t: t, pretty: true, routes: make(map[string]http.HandlerFunc)}
	s.srv = httptest.NewServer(s)
	t.Cleanup(s.srv.Close)
	return s
}

// URL is the home page of the site.
func (s *site) URL() string { return s.srv.URL + s.prefix }

func (s *site) handle(route string, h http.HandlerFunc) *site {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routes[route] = h
	return s
}

func (s *site) handleJSON(route, body string) *site {
	return s.handle(route, func(w http.ResponseWriter, _ *http.Request) { writeBody(w, http.StatusOK, body) })
}

func (s *site) setBefore(f func(w http.ResponseWriter, r *http.Request) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.before = f
}

func (s *site) setPretty(pretty bool) *site {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pretty = pretty
	return s
}

func (s *site) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.requests = append(s.requests, request{Method: r.Method, Path: r.URL.Path, Query: r.URL.Query(), Header: r.Header.Clone()})
	pretty, before := s.pretty, s.before
	s.mu.Unlock()
	if before != nil && before(w, r) {
		return
	}
	var route string
	path := strings.TrimPrefix(r.URL.Path, s.prefix)
	switch {
	case r.URL.Path != s.prefix+"/" && !strings.HasPrefix(r.URL.Path, s.prefix+"/wp-json"):
		notFound(w)
		return
	case strings.HasPrefix(path, "/wp-json") && pretty:
		route = "/" + strings.TrimPrefix(strings.TrimPrefix(path, "/wp-json"), "/")
	case path == "/" && r.URL.Query().Has("rest_route"):
		route = r.URL.Query().Get("rest_route")
	default:
		notFound(w)
		return
	}
	s.mu.Lock()
	h := s.routes[route]
	s.mu.Unlock()
	if h == nil {
		restError(w, http.StatusNotFound, "rest_no_route", "no route")
		return
	}
	h(w, r)
}

func (s *site) log() []request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]request(nil), s.requests...)
}

// requested lists the requests: the path, the rest_route if there is one, and the page of a request for a page.
func (s *site) requested() []string {
	var out []string
	for _, r := range s.log() {
		u := r.Path
		sep := "?"
		if r.Query.Has("rest_route") {
			u += sep + "rest_route=" + r.Query.Get("rest_route")
			sep = "&"
		}
		if p := r.Query.Get("page"); p != "" {
			u += sep + "page=" + p
		}
		out = append(out, u)
	}
	return out
}

func writeBody(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

func restError(w http.ResponseWriter, status int, code, message string) {
	writeBody(w, status, fmt.Sprintf(`{"code":%q,"message":%q,"data":{"status":%d}}`, code, message, status))
}

func notFound(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=UTF-8")
	w.WriteHeader(http.StatusNotFound)
	_, _ = io.WriteString(w, "<!DOCTYPE html><html><body><h1>Not Found</h1></body></html>")
}

func html200(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=UTF-8")
	_, _ = io.WriteString(w, "<!DOCTYPE html><html><body>Hello</body></html>")
}

func status(code int, errCode string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { restError(w, code, errCode, "message") }
}

// pager gives the items in pages, as the controllers of WordPress do.
type pager struct {
	items      []string
	perPageMax int  // the largest page that the site gives; 0 for no limit
	totals     bool // whether the headers X-WP-Total and X-WP-TotalPages are there
	ignorePage bool // gives the first page always
}

func (p pager) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	per, _ := strconv.Atoi(q.Get("per_page"))
	if per <= 0 {
		per = 10
	}
	if p.perPageMax > 0 {
		per = min(per, p.perPageMax)
	}
	page, _ := strconv.Atoi(q.Get("page"))
	if page <= 0 || p.ignorePage {
		page = 1
	}
	pages := (len(p.items) + per - 1) / per
	if p.totals {
		w.Header().Set("X-WP-Total", strconv.Itoa(len(p.items)))
		w.Header().Set("X-WP-TotalPages", strconv.Itoa(pages))
	}
	if page > pages && len(p.items) > 0 {
		restError(w, http.StatusBadRequest, "rest_post_invalid_page_number", "no such page")
		return
	}
	lo, hi := (page-1)*per, min(page*per, len(p.items))
	writeBody(w, http.StatusOK, "["+strings.Join(p.items[lo:hi], ",")+"]")
}

func seq(from, to int) []int {
	var ids []int
	for id := from; id <= to; id++ {
		ids = append(ids, id)
	}
	return ids
}

func termJSON(id int, taxonomy string) string {
	return fmt.Sprintf(`{"id":%d,"count":%d,"description":"","link":"https://example.com/%s/t%d/","name":"Term %d","slug":"t%d",`+
		`"taxonomy":%q,"parent":%d,"meta":[],"_links":{"self":[{"href":"https://example.com/wp-json/wp/v2/terms/%d"}]}}`,
		id, id%5, taxonomy, id, id, id, taxonomy, id/100, id)
}

func termOf(id int, taxonomy string) Term {
	return Term{
		ID: int64(id), Taxonomy: taxonomy, Slug: fmt.Sprintf("t%d", id), Name: fmt.Sprintf("Term %d", id),
		Parent: int64(id / 100), Link: fmt.Sprintf("https://example.com/%s/t%d/", taxonomy, id),
	}
}

func termItems(taxonomy string, ids ...int) []string {
	var items []string
	for _, id := range ids {
		items = append(items, termJSON(id, taxonomy))
	}
	return items
}

func termsOf(taxonomy string, ids ...int) []Term {
	var terms []Term
	for _, id := range ids {
		terms = append(terms, termOf(id, taxonomy))
	}
	return terms
}

func userJSON(id int) string {
	return fmt.Sprintf(`{"id":%d,"name":"User %d","url":"","description":"","link":"https://example.com/author/u%d/","slug":"u%d",`+
		`"avatar_urls":{"24":"https://secure.gravatar.com/avatar/x?s=24"},"meta":[],"_links":{"self":[{"href":"x"}]}}`, id, id, id, id)
}

const standardIndex = `{"name":"テストのサイト","description":"どこにでもあるサイト","url":%q,"home":%q,"gmt_offset":"9","timezone_string":"Asia/Tokyo",` +
	`"namespaces":["oembed/1.0","wp/v2","wp-site-health/v1"],"authentication":[],"show_on_front":"page","page_on_front":7,"page_for_posts":9,` +
	`"site_logo":11,"site_icon":12,"routes":{"/":{"namespace":"","methods":["GET"]}},"_links":{"help":[{"href":"https://developer.wordpress.org/rest-api/"}]}}`

const standardTaxonomies = `{
"category":{"name":"カテゴリー","slug":"category","description":"","hierarchical":true,"types":["post"],"rest_base":"categories","rest_namespace":"wp/v2","_links":{}},
"post_tag":{"name":"タグ","slug":"post_tag","description":"","hierarchical":false,"types":["post"],"rest_base":"tags","rest_namespace":"wp/v2"},
"genre":{"name":"ジャンル","slug":"genre","hierarchical":false,"types":["works"],"rest_base":"genres","rest_namespace":"wp/v2"},
"nav_menu":{"name":"ナビゲーションメニュー","slug":"nav_menu","hierarchical":false,"types":["wp_navigation"],"rest_base":"menus","rest_namespace":"wp/v2"}
}`

const standardTypes = `{
"post":{"slug":"post","name":"投稿","hierarchical":false,"has_archive":false,"rest_base":"posts","rest_namespace":"wp/v2","taxonomies":["category","post_tag"]},
"page":{"slug":"page","name":"固定ページ","hierarchical":true,"has_archive":false,"rest_base":"pages","rest_namespace":"wp/v2","taxonomies":[]},
"works":{"slug":"works","name":"作品","hierarchical":false,"has_archive":true,"rest_base":"works","rest_namespace":"wp/v2","taxonomies":["genre"]}
}`

// standard gives the site the content of a small site: 250 categories in 3 pages, 3 tags, 2 genres, and 2 users.
func (s *site) standard() *site {
	s.handle("/", func(w http.ResponseWriter, _ *http.Request) {
		writeBody(w, http.StatusOK, fmt.Sprintf(standardIndex, s.URL(), s.URL()))
	})
	// WordPress applies _fields to the keys of a response that is keyed by name, which are not fields: nothing is left.
	keyed := func(body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Has("_fields") {
				writeBody(w, http.StatusOK, "[]")
				return
			}
			writeBody(w, http.StatusOK, body)
		}
	}
	s.handle("/wp/v2/taxonomies", keyed(standardTaxonomies))
	s.handle("/wp/v2/types", keyed(standardTypes))
	s.handle("/wp/v2/categories", pager{items: termItems("category", seq(1, 250)...), totals: true}.ServeHTTP)
	s.handle("/wp/v2/tags", pager{items: termItems("post_tag", 301, 302, 303), totals: true}.ServeHTTP)
	s.handle("/wp/v2/genres", pager{items: termItems("genre", 401, 402), totals: true}.ServeHTTP)
	s.handle("/wp/v2/menus", func(w http.ResponseWriter, _ *http.Request) {
		s.t.Error("the terms of nav_menu were requested")
		restError(w, http.StatusUnauthorized, "rest_forbidden", "no")
	})
	s.handle("/wp/v2/users", pager{items: []string{
		`{"id":1,"name":"管理人","url":"https://example.net","description":"<p>こんにちは</p>","link":"https://example.com/author/admin/","slug":"admin",` +
			`"avatar_urls":{"24":"https://secure.gravatar.com/avatar/x?s=24"},"meta":[],"_links":{"self":[{"href":"x"}]}}`,
		`{"id":2,"name":"A &amp; B","url":"","description":"","link":"https://example.com/author/%e3%81%82/","slug":"%e3%81%82","_links":{}}`,
	}, totals: true}.ServeHTTP)
	return s
}

// standardSnapshot is what Fetch reads from the standard site, at the root.
func (s *site) standardSnapshot(root string) *Snapshot {
	terms := termsOf("category", seq(1, 250)...)
	terms = append(terms, termsOf("genre", 401, 402)...)
	terms = append(terms, termsOf("post_tag", 301, 302, 303)...)
	return &Snapshot{
		API: root,
		Index: Index{
			Name: "テストのサイト", Description: "どこにでもあるサイト", URL: s.URL(), Home: s.URL(), GMTOffset: "9", TimezoneString: "Asia/Tokyo",
			ShowOnFront: "page", PageOnFront: 7, PageForPosts: 9, SiteLogo: 11, SiteIcon: 12,
		},
		Taxonomies: []Taxonomy{
			{Slug: "category", Name: "カテゴリー", Hierarchical: true, RestBase: "categories", RestNamespace: "wp/v2", Types: []string{"post"}},
			{Slug: "genre", Name: "ジャンル", RestBase: "genres", RestNamespace: "wp/v2", Types: []string{"works"}},
			{Slug: "nav_menu", Name: "ナビゲーションメニュー", RestBase: "menus", RestNamespace: "wp/v2", Types: []string{"wp_navigation"}},
			{Slug: "post_tag", Name: "タグ", RestBase: "tags", RestNamespace: "wp/v2", Types: []string{"post"}},
		},
		Types: []Type{
			{Slug: "page", Name: "固定ページ", Hierarchical: true, HasArchive: Archive{Set: true}, RestBase: "pages", RestNamespace: "wp/v2"},
			{Slug: "post", Name: "投稿", HasArchive: Archive{Set: true}, RestBase: "posts", RestNamespace: "wp/v2", Taxonomies: []string{"category", "post_tag"}},
			{Slug: "works", Name: "作品", HasArchive: Archive{Set: true, On: true}, RestBase: "works", RestNamespace: "wp/v2", Taxonomies: []string{"genre"}},
		},
		Terms: terms,
		Users: []User{
			{ID: 1, Name: "管理人", Slug: "admin", Description: "<p>こんにちは</p>", URL: "https://example.net", Link: "https://example.com/author/admin/"},
			{ID: 2, Name: "A &amp; B", Slug: "%e3%81%82", Link: "https://example.com/author/%e3%81%82/"},
		},
	}
}

func fetch(t *testing.T, s *site, opts Options) (*Snapshot, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return Fetch(ctx, s.URL(), opts)
}

func mustFetch(t *testing.T, s *site, opts Options) *Snapshot {
	t.Helper()
	snap, err := fetch(t, s, opts)
	if err != nil {
		t.Fatalf("Fetch() = %v", err)
	}
	return snap
}

// firstDiff describes where two texts first differ.
func firstDiff(got, want string) string {
	g, w := strings.Split(got, "\n"), strings.Split(want, "\n")
	for i := range max(len(g), len(w)) {
		var gl, wl string
		if i < len(g) {
			gl = g[i]
		}
		if i < len(w) {
			wl = w[i]
		}
		if gl != wl {
			return fmt.Sprintf("line %d:\n got  %q\n want %q", i+1, gl, wl)
		}
	}
	return "no difference"
}

func checkSnapshot(t *testing.T, got, want *Snapshot) {
	t.Helper()
	if reflect.DeepEqual(got, want) {
		return
	}
	if g, w := writeString(t, got), writeString(t, want); g != w {
		t.Errorf("the snapshot differs at %s", firstDiff(g, w))
	} else {
		t.Errorf("the snapshot is\n%#v\nwant\n%#v", got, want)
	}
}

func checkRequested(t *testing.T, s *site, want ...string) {
	t.Helper()
	if got := s.requested(); !reflect.DeepEqual(got, want) {
		t.Errorf("the requests are\n%q\nwant\n%q", got, want)
	}
}

// checkHeaders checks what every request must be: a GET with the two headers and without the ones that identify the user.
func checkHeaders(t *testing.T, s *site) {
	t.Helper()
	for _, r := range s.log() {
		if r.Method != http.MethodGet {
			t.Errorf("%s %s: the method is not GET", r.Method, r.Path)
		}
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Errorf("GET %s: Accept = %q", r.Path, got)
		}
		if got := r.Header.Get("User-Agent"); got != UserAgent {
			t.Errorf("GET %s: User-Agent = %q", r.Path, got)
		}
		for _, h := range []string{"Cookie", "Authorization", "Referer", "Proxy-Authorization", "Origin"} {
			if _, ok := r.Header[h]; ok {
				t.Errorf("GET %s: sends %s: %q", r.Path, h, r.Header.Get(h))
			}
		}
		route := r.Query.Get("rest_route")
		if route == "" {
			route = strings.TrimPrefix(r.Path, "/wp-json")
		}
		if keyed := route == "/wp/v2/taxonomies" || route == "/wp/v2/types"; keyed == r.Query.Has("_fields") {
			t.Errorf("GET %s%v: _fields is there or not there where it must not be or be", r.Path, r.Query)
		}
		if r.Query.Has("page") {
			for k, v := range map[string]string{"per_page": "100", "orderby": "id", "order": "asc"} {
				if got := r.Query.Get(k); got != v {
					t.Errorf("GET %s%v: %s = %q, want %q", r.Path, r.Query, k, got, v)
				}
			}
		}
	}
}

func TestFetch(t *testing.T) {
	s := newSite(t).standard()
	snap := mustFetch(t, s, Options{})
	checkSnapshot(t, snap, s.standardSnapshot(s.URL()+"/wp-json/"))
	checkRequested(t, s,
		"/wp-json/", "/wp-json/wp/v2/taxonomies", "/wp-json/wp/v2/types",
		"/wp-json/wp/v2/categories?page=1", "/wp-json/wp/v2/categories?page=2", "/wp-json/wp/v2/categories?page=3",
		"/wp-json/wp/v2/genres?page=1", "/wp-json/wp/v2/tags?page=1", "/wp-json/wp/v2/users?page=1")
	checkHeaders(t, s)

	// The fields of the requests are the ones that are read.
	fields := map[string]string{
		"/wp-json/":                 "name,description,url,home,gmt_offset,timezone_string,show_on_front,page_on_front,page_for_posts,site_logo,site_icon,namespaces",
		"/wp-json/wp/v2/categories": "id,taxonomy,slug,name,parent,link",
		"/wp-json/wp/v2/users":      "id,name,slug,description,url,link",
	}
	for _, r := range s.log() {
		if want, ok := fields[r.Path]; ok && r.Query.Get("_fields") != want {
			t.Errorf("GET %s: _fields = %q, want %q", r.Path, r.Query.Get("_fields"), want)
		}
	}

	// The snapshot is the same when it is saved and read.
	again, err := ReadSnapshot(strings.NewReader(writeString(t, snap)), Options{})
	if err != nil {
		t.Fatal(err)
	}
	checkSnapshot(t, again, snap)
}

func TestFetchTaxonomyRoutes(t *testing.T) {
	// A taxonomy that is not in the list of those that are skipped is read, whatever its REST base is.
	s := newSite(t).standard()
	s.handleJSON("/wp/v2/taxonomies", `{
"book_genre":{"slug":"book_genre","name":"G","types":["book"]},
"custom":{"slug":"custom","name":"C","rest_base":"things","rest_namespace":"custom/v1"},
"post_format":{"slug":"post_format","rest_base":"formats"},
"language":{"slug":"language","rest_base":"language"},
"wp_theme":{"slug":"wp_theme","rest_base":"themes"}}`)
	s.handle("/wp/v2/book_genre", pager{items: termItems("book_genre", 1, 2), totals: true}.ServeHTTP)
	s.handle("/custom/v1/things", pager{items: termItems("custom", 7), totals: true}.ServeHTTP)
	for _, route := range []string{"/wp/v2/formats", "/wp/v2/language", "/wp/v2/themes"} {
		s.handle(route, func(w http.ResponseWriter, r *http.Request) {
			s.t.Errorf("the terms of a skipped taxonomy were requested: %s", r.URL)
			writeBody(w, http.StatusOK, "[]")
		})
	}
	snap := mustFetch(t, s, Options{})
	want := append(termsOf("book_genre", 1, 2), termsOf("custom", 7)...)
	checkSnapshot(t, &Snapshot{Terms: snap.Terms}, &Snapshot{Terms: want})
	var slugs []string
	for _, tx := range snap.Taxonomies {
		slugs = append(slugs, tx.Slug)
	}
	if !reflect.DeepEqual(slugs, []string{"book_genre", "custom", "language", "post_format", "wp_theme"}) {
		t.Errorf("the taxonomies are %q, want all of them in the order of the slugs", slugs)
	}
	checkRequested(t, s,
		"/wp-json/", "/wp-json/wp/v2/taxonomies", "/wp-json/wp/v2/types",
		"/wp-json/wp/v2/book_genre?page=1", "/wp-json/custom/v1/things?page=1", "/wp-json/wp/v2/users?page=1")
}

func TestFetchPlainPermalinks(t *testing.T) {
	s := newSite(t).standard().setPretty(false)
	snap := mustFetch(t, s, Options{})
	checkSnapshot(t, snap, s.standardSnapshot(s.URL()+"/?rest_route=/"))
	checkRequested(t, s,
		"/wp-json/", "/?rest_route=/", "/?rest_route=/wp/v2/taxonomies", "/?rest_route=/wp/v2/types",
		"/?rest_route=/wp/v2/categories&page=1", "/?rest_route=/wp/v2/categories&page=2", "/?rest_route=/wp/v2/categories&page=3",
		"/?rest_route=/wp/v2/genres&page=1", "/?rest_route=/wp/v2/tags&page=1", "/?rest_route=/wp/v2/users&page=1")
	checkHeaders(t, s)
}

func TestFetchGivenAPI(t *testing.T) {
	t.Run("the pretty one", func(t *testing.T) {
		s := newSite(t).standard()
		snap := mustFetch(t, s, Options{API: s.URL() + "/wp-json/"})
		checkSnapshot(t, snap, s.standardSnapshot(s.URL()+"/wp-json/"))
		if reqs := s.requested(); reqs[0] != "/wp-json/" || len(reqs) != 9 {
			t.Errorf("the requests are %q", reqs)
		}
	})
	t.Run("without the slash at the end", func(t *testing.T) {
		s := newSite(t).standard()
		snap := mustFetch(t, s, Options{API: s.URL() + "/wp-json"})
		checkSnapshot(t, snap, s.standardSnapshot(s.URL()+"/wp-json"))
		if reqs := s.requested(); reqs[0] != "/wp-json/" || reqs[1] != "/wp-json/wp/v2/taxonomies" {
			t.Errorf("the requests are %q", reqs)
		}
	})
	t.Run("rest_route on a site that has the pretty one too", func(t *testing.T) {
		s := newSite(t).standard()
		snap := mustFetch(t, s, Options{API: s.URL() + "/?rest_route=/"})
		checkSnapshot(t, snap, s.standardSnapshot(s.URL()+"/?rest_route=/"))
		for _, r := range s.requested() {
			if !strings.HasPrefix(r, "/?rest_route=/") {
				t.Errorf("the request %s is not through rest_route", r)
			}
		}
	})
	t.Run("rest_route without the slash at the end of the route", func(t *testing.T) {
		s := newSite(t).standard().setPretty(false)
		snap := mustFetch(t, s, Options{API: s.URL() + "/?rest_route="})
		if snap.Index.Name != "テストのサイト" || len(snap.Terms) != 255 {
			t.Errorf("the snapshot has the index %+v and %d terms", snap.Index, len(snap.Terms))
		}
	})
	t.Run("not an API", func(t *testing.T) {
		s := newSite(t).standard()
		_, err := fetch(t, s, Options{API: s.URL() + "/nothing/"})
		if err == nil || !strings.HasPrefix(err.Error(), "no REST API at "+s.URL()+"/nothing/: "+s.URL()+"/nothing/?_fields=") ||
			!strings.HasSuffix(err.Error(), ": 404 Not Found") {
			t.Errorf("Fetch() = %v, want that there is no REST API at the URL, because of a 404", err)
		}
		if len(s.requested()) != 1 {
			t.Errorf("the requests are %q, want only the one", s.requested())
		}
	})
}

func TestFetchInSubdirectory(t *testing.T) {
	s := newSite(t)
	s.prefix = "/blog"
	s.standard()
	snap := mustFetch(t, s, Options{})
	checkSnapshot(t, snap, s.standardSnapshot(s.URL()+"/wp-json/"))
	if reqs := s.requested(); len(reqs) != 9 || reqs[0] != "/blog/wp-json/" || reqs[1] != "/blog/wp-json/wp/v2/taxonomies" {
		t.Errorf("the requests are %q", reqs)
	}

	s = newSite(t).standard().setPretty(false)
	s.prefix = "/blog"
	snap = mustFetch(t, s, Options{})
	if want := s.URL() + "/?rest_route=/"; snap.API != want || len(snap.Terms) != 255 {
		t.Errorf("Fetch() read the API %q and %d terms, want %q and 255", snap.API, len(snap.Terms), want)
	}
	if reqs := s.requested(); reqs[0] != "/blog/wp-json/" || reqs[1] != "/blog/?rest_route=/" {
		t.Errorf("the requests are %q", reqs)
	}
}

func TestFetchBlocked(t *testing.T) {
	text := func(code int, body string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(code)
			_, _ = io.WriteString(w, body)
		}
	}
	users := "/wp/v2/users"
	tests := []struct {
		name    string
		handler http.HandlerFunc
		want    Unavailable
	}{
		{"401 with the code of WordPress", status(http.StatusUnauthorized, "rest_user_cannot_view"), Unavailable{Route: users, Status: 401, Code: "rest_user_cannot_view"}},
		{"403", status(http.StatusForbidden, "rest_forbidden"), Unavailable{Route: users, Status: 403, Code: "rest_forbidden"}},
		{"400", status(http.StatusBadRequest, "rest_invalid_param"), Unavailable{Route: users, Status: 400, Code: "rest_invalid_param"}},
		{"418", status(http.StatusTeapot, "teapot"), Unavailable{Route: users, Status: 418, Code: "teapot"}},
		{"404 page", func(w http.ResponseWriter, _ *http.Request) { notFound(w) }, Unavailable{Route: users, Status: 404}},
		{"200 page", html200, Unavailable{Route: users, Status: 200, Code: "not_json"}},
		{"200 text", text(http.StatusOK, "[]"), Unavailable{Route: users, Status: 200, Code: "not_json"}},
		{"200 without a type", func(w http.ResponseWriter, _ *http.Request) {
			w.Header()["Content-Type"] = nil
			_, _ = io.WriteString(w, "[]")
		}, Unavailable{Route: users, Status: 200, Code: "not_json"}},
		{"200 with another JSON type", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/hal+json")
			_, _ = io.WriteString(w, "[]")
		}, Unavailable{Route: users, Status: 200, Code: "not_json"}},
		{"204", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }, Unavailable{Route: users, Status: 204, Code: "not_json"}},
		{"a code with a space", status(http.StatusForbidden, "not allowed"), Unavailable{Route: users, Status: 403}},
		{"a code with a quote", func(w http.ResponseWriter, _ *http.Request) {
			writeBody(w, http.StatusForbidden, `{"code":"a\"b"}`)
		}, Unavailable{Route: users, Status: 403}},
		{"a code that is too long", status(http.StatusForbidden, strings.Repeat("a", 101)), Unavailable{Route: users, Status: 403}},
		{"a code of 100 bytes", status(http.StatusForbidden, strings.Repeat("a", 100)), Unavailable{Route: users, Status: 403, Code: strings.Repeat("a", 100)}},
		{"a code of all the characters", status(http.StatusForbidden, "a.B_c-9"), Unavailable{Route: users, Status: 403, Code: "a.B_c-9"}},
		{"an empty code", status(http.StatusForbidden, ""), Unavailable{Route: users, Status: 403}},
		{"a code that is a number", func(w http.ResponseWriter, _ *http.Request) {
			writeBody(w, http.StatusForbidden, `{"code":5}`)
		}, Unavailable{Route: users, Status: 403}},
		{"an error that is an array", func(w http.ResponseWriter, _ *http.Request) {
			writeBody(w, http.StatusForbidden, `["rest_forbidden"]`)
		}, Unavailable{Route: users, Status: 403}},
		{"an error that is not JSON", text(http.StatusForbidden, `{"code":"rest_forbidden"}`), Unavailable{Route: users, Status: 403}},
		{"an error of 64 KiB", func(w http.ResponseWriter, _ *http.Request) {
			body := `{"code":"rest_forbidden","message":"`
			writeBody(w, http.StatusForbidden, body+strings.Repeat("x", maxErrorBody-len(body)-2)+`"}`)
		}, Unavailable{Route: users, Status: 403, Code: "rest_forbidden"}},
		{"an error that is larger", func(w http.ResponseWriter, _ *http.Request) {
			body := `{"code":"rest_forbidden","message":"`
			writeBody(w, http.StatusForbidden, body+strings.Repeat("x", maxErrorBody-len(body)-1)+`"}`)
		}, Unavailable{Route: users, Status: 403}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newSite(t).standard()
			s.handle(users, tt.handler)
			snap := mustFetch(t, s, Options{})
			want := s.standardSnapshot(s.URL() + "/wp-json/")
			want.Users, want.Unavailable = nil, []Unavailable{tt.want}
			checkSnapshot(t, snap, want)
			if reqs := s.requested(); reqs[len(reqs)-1] != "/wp-json/wp/v2/users?page=1" || len(reqs) != 9 {
				t.Errorf("the requests are %q, want the users to be asked for once at the end", reqs)
			}
		})
	}
}

func TestFetchRoutesBlocked(t *testing.T) {
	base := func(s *site) *Snapshot { return s.standardSnapshot(s.URL() + "/wp-json/") }

	t.Run("taxonomies", func(t *testing.T) {
		s := newSite(t).standard()
		s.handle("/wp/v2/taxonomies", status(http.StatusForbidden, "rest_forbidden"))
		want := base(s)
		want.Taxonomies = nil
		want.Terms = append(termsOf("category", seq(1, 250)...), termsOf("post_tag", 301, 302, 303)...)
		want.Unavailable = []Unavailable{{Route: "/wp/v2/taxonomies", Status: 403, Code: "rest_forbidden"}}
		checkSnapshot(t, mustFetch(t, s, Options{}), want)
		checkRequested(t, s,
			"/wp-json/", "/wp-json/wp/v2/taxonomies", "/wp-json/wp/v2/types",
			"/wp-json/wp/v2/categories?page=1", "/wp-json/wp/v2/categories?page=2", "/wp-json/wp/v2/categories?page=3",
			"/wp-json/wp/v2/tags?page=1", "/wp-json/wp/v2/users?page=1")
	})
	t.Run("taxonomies and categories", func(t *testing.T) {
		s := newSite(t).standard()
		s.handle("/wp/v2/taxonomies", func(w http.ResponseWriter, _ *http.Request) { notFound(w) })
		s.handle("/wp/v2/categories", status(http.StatusForbidden, "rest_forbidden"))
		want := base(s)
		want.Taxonomies, want.Terms = nil, termsOf("post_tag", 301, 302, 303)
		want.Unavailable = []Unavailable{
			{Route: "/wp/v2/categories", Taxonomy: "category", Status: 403, Code: "rest_forbidden"},
			{Route: "/wp/v2/taxonomies", Status: 404},
		}
		checkSnapshot(t, mustFetch(t, s, Options{}), want)
	})
	t.Run("taxonomies that are not any", func(t *testing.T) {
		// The route is there, and gives PHP's empty array: no taxonomies, so no terms, and nothing is unavailable.
		s := newSite(t).standard()
		s.handleJSON("/wp/v2/taxonomies", `[]`)
		want := base(s)
		want.Taxonomies, want.Terms = nil, nil
		checkSnapshot(t, mustFetch(t, s, Options{}), want)
		checkRequested(t, s, "/wp-json/", "/wp-json/wp/v2/taxonomies", "/wp-json/wp/v2/types", "/wp-json/wp/v2/users?page=1")
	})
	t.Run("types", func(t *testing.T) {
		s := newSite(t).standard()
		s.handle("/wp/v2/types", func(w http.ResponseWriter, _ *http.Request) { notFound(w) })
		want := base(s)
		want.Types = nil
		want.Unavailable = []Unavailable{{Route: "/wp/v2/types", Status: 404}}
		checkSnapshot(t, mustFetch(t, s, Options{}), want)
	})
	t.Run("types that are not any", func(t *testing.T) {
		s := newSite(t).standard()
		s.handleJSON("/wp/v2/types", `[]`)
		want := base(s)
		want.Types = nil
		checkSnapshot(t, mustFetch(t, s, Options{}), want)
	})
	t.Run("the terms of a taxonomy", func(t *testing.T) {
		s := newSite(t).standard()
		s.handle("/wp/v2/genres", status(http.StatusUnauthorized, "rest_forbidden"))
		want := base(s)
		want.Terms = append(termsOf("category", seq(1, 250)...), termsOf("post_tag", 301, 302, 303)...)
		want.Unavailable = []Unavailable{{Route: "/wp/v2/genres", Taxonomy: "genre", Status: 401, Code: "rest_forbidden"}}
		checkSnapshot(t, mustFetch(t, s, Options{}), want)
	})
	t.Run("everything but the index", func(t *testing.T) {
		s := newSite(t)
		s.handle("/", func(w http.ResponseWriter, _ *http.Request) {
			writeBody(w, http.StatusOK, `{"name":"N","namespaces":["wp/v2"]}`)
		})
		for _, route := range []string{"/wp/v2/taxonomies", "/wp/v2/types", "/wp/v2/categories", "/wp/v2/tags", "/wp/v2/users"} {
			s.handle(route, status(http.StatusUnauthorized, "rest_forbidden"))
		}
		want := &Snapshot{API: s.URL() + "/wp-json/", Index: Index{Name: "N"}, Unavailable: []Unavailable{
			{Route: "/wp/v2/categories", Taxonomy: "category", Status: 401, Code: "rest_forbidden"},
			{Route: "/wp/v2/tags", Taxonomy: "post_tag", Status: 401, Code: "rest_forbidden"},
			{Route: "/wp/v2/taxonomies", Status: 401, Code: "rest_forbidden"},
			{Route: "/wp/v2/types", Status: 401, Code: "rest_forbidden"},
			{Route: "/wp/v2/users", Status: 401, Code: "rest_forbidden"},
		}}
		checkSnapshot(t, mustFetch(t, s, Options{}), want)
	})
	t.Run("routes that cannot be made", func(t *testing.T) {
		s := newSite(t).standard()
		s.handleJSON("/wp/v2/taxonomies", `{
"a":{"slug":"a","rest_base":"../x"},
"b":{"slug":"b","rest_base":"a b"},
"c":{"slug":"c","rest_namespace":"x?y=1"},
"d":{"slug":"d","rest_namespace":"//"},
"e":{"slug":"e","rest_base":"."},
"f":{"slug":"f","rest_namespace":"wp/../v2"},
"g":{"slug":"g","rest_base":"é"},
"h":{"slug":"h","rest_base":"a%2Fb"},
"i":{"slug":"i","rest_base":"/x"},
"j":{"slug":"j","rest_base":"x/"},
"k":{"slug":"k","rest_namespace":"wp\\v2"},
"l":{"slug":"l","rest_base":"a#b"},
"m b":{"slug":"m b"},
"ok":{"slug":"ok","rest_base":"fine.v2_x-y/Z9"}}`)
		s.handle("/wp/v2/fine.v2_x-y/Z9", pager{items: termItems("ok", 1)}.ServeHTTP)
		snap := mustFetch(t, s, Options{})
		var want []Unavailable
		for _, slug := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l", "m b"} {
			want = append(want, Unavailable{Taxonomy: slug, Code: "invalid_route"})
		}
		if !reflect.DeepEqual(snap.Unavailable, want) {
			t.Errorf("Unavailable = %+v, want %+v", snap.Unavailable, want)
		}
		if !reflect.DeepEqual(snap.Terms, termsOf("ok", 1)) {
			t.Errorf("Terms = %+v, want the one term of ok", snap.Terms)
		}
		checkRequested(t, s,
			"/wp-json/", "/wp-json/wp/v2/taxonomies", "/wp-json/wp/v2/types", "/wp-json/wp/v2/fine.v2_x-y/Z9?page=1", "/wp-json/wp/v2/users?page=1")
	})
}

func TestFetchTermTaxonomy(t *testing.T) {
	s := newSite(t).standard()
	s.handleJSON("/wp/v2/taxonomies", `{"category":{"slug":"category","rest_base":"categories"}}`)
	s.handleJSON("/wp/v2/categories", `[{"id":1,"slug":"a"},{"id":2,"taxonomy":"","slug":"b"},{"id":3,"taxonomy":null},{"id":4,"taxonomy":"category"}]`)
	snap := mustFetch(t, s, Options{})
	want := []Term{
		{ID: 1, Taxonomy: "category", Slug: "a"}, {ID: 2, Taxonomy: "category", Slug: "b"},
		{ID: 3, Taxonomy: "category"}, {ID: 4, Taxonomy: "category"},
	}
	if !reflect.DeepEqual(snap.Terms, want) {
		t.Errorf("Terms = %+v, want %+v", snap.Terms, want)
	}

	s.handleJSON("/wp/v2/categories", `[{"id":1,"taxonomy":"category"},{"id":2,"taxonomy":"post_tag"}]`)
	_, err := fetch(t, s, Options{})
	checkError(t, err, "/1/taxonomy", `taxonomy "post_tag" does not match "category"`)
	if !strings.HasPrefix(err.Error(), "GET "+s.URL()+"/wp-json/wp/v2/categories?") {
		t.Errorf("Fetch() = %v, want the URL of the request in the error", err)
	}
}

func TestFetchKeyedSlugs(t *testing.T) {
	s := newSite(t).standard()
	s.handleJSON("/wp/v2/taxonomies", `{"category":{"name":"C"},"post_tag":{"slug":"post_tag","name":"T"}}`)
	s.handleJSON("/wp/v2/types", `{"post":{"name":"P","has_archive":"posts"}}`)
	s.handle("/wp/v2/categories", pager{}.ServeHTTP)
	s.handle("/wp/v2/tags", pager{}.ServeHTTP)
	snap := mustFetch(t, s, Options{})
	if want := []Taxonomy{{Slug: "category", Name: "C"}, {Slug: "post_tag", Name: "T"}}; !reflect.DeepEqual(snap.Taxonomies, want) {
		t.Errorf("Taxonomies = %+v, want %+v", snap.Taxonomies, want)
	}
	if want := []Type{{Slug: "post", Name: "P", HasArchive: Archive{Set: true, On: true, Slug: "posts"}}}; !reflect.DeepEqual(snap.Types, want) {
		t.Errorf("Types = %+v, want %+v", snap.Types, want)
	}

	for _, tt := range []struct{ route, body, pointer, msg string }{
		{"/wp/v2/taxonomies", `{"category":{"slug":"post_tag"}}`, "/category/slug", `slug "post_tag" does not match "category"`},
		{"/wp/v2/taxonomies", `{"category":{"slug":"category"},"x":{"slug":"category"}}`, "/x/slug", `slug "category" does not match "x"`},
		{"/wp/v2/taxonomies", `{"":{}}`, "/", `missing key "slug"`},
		{"/wp/v2/taxonomies", `{"a":[]}`, "/a", "expected object, got array"},
		{"/wp/v2/taxonomies", `{"a":null}`, "/a", "expected object, got null"},
		{"/wp/v2/taxonomies", `[{"slug":"a"}]`, "", "expected object, got array"},
		{"/wp/v2/taxonomies", `"a"`, "", "expected object, got string"},
		{"/wp/v2/taxonomies", `{"a/b":{"slug":"a/b","hierarchical":1}}`, "/a~1b/hierarchical", "expected boolean, got number"},
		{"/wp/v2/types", `{"post":{"slug":"page"}}`, "/post/slug", `slug "page" does not match "post"`},
		{"/wp/v2/types", `[{"slug":"post"}]`, "", "expected object, got array"},
		{"/wp/v2/types", `{"post":{"has_archive":0}}`, "/post/has_archive", "expected boolean or string, got number"},
	} {
		s := newSite(t).standard()
		s.handleJSON(tt.route, tt.body)
		_, err := fetch(t, s, Options{})
		checkError(t, err, tt.pointer, tt.msg)
		if err != nil && !strings.HasPrefix(err.Error(), "GET "+s.URL()+"/wp-json"+tt.route+": ") {
			t.Errorf("Fetch() = %v, want the URL of the request in the error", err)
		}
	}

	// More taxonomies than the limit.
	s = newSite(t).standard()
	_, err := fetch(t, s, Options{MaxTaxonomies: 3})
	checkError(t, err, "/nav_menu", "too many taxonomies: at least 4 (max 3)")
	s = newSite(t).standard()
	s.handleJSON("/wp/v2/taxonomies", `[]`)
	s.handleJSON("/wp/v2/types", `{"a":{},"b":{},"c":{},"d":{}}`)
	_, err = fetch(t, s, Options{MaxTaxonomies: 3})
	checkError(t, err, "/d", "too many types: at least 4 (max 3)")
	s.handleJSON("/wp/v2/types", `{"a":{},"b":{},"c":{}}`)
	if _, err := fetch(t, s, Options{MaxTaxonomies: 3}); err != nil {
		t.Errorf("Fetch() with as many types as the limit = %v", err)
	}
}

func TestFetchPaging(t *testing.T) {
	// categoriesOnly gives the site the taxonomy category and what the route of it does.
	categoriesOnly := func(t *testing.T, h http.HandlerFunc) *site {
		t.Helper()
		s := newSite(t).standard()
		s.handleJSON("/wp/v2/taxonomies", `{"category":{"slug":"category","rest_base":"categories"}}`)
		s.handle("/wp/v2/categories", h)
		return s
	}
	categoryRequests := func(s *site) (n int) {
		for _, r := range s.requested() {
			if strings.HasPrefix(r, "/wp-json/wp/v2/categories") {
				n++
			}
		}
		return n
	}
	items := func(ids ...int) string { return "[" + strings.Join(termItems("category", ids...), ",") + "]" }

	t.Run("pages that are short, with the total", func(t *testing.T) {
		s := categoriesOnly(t, pager{items: termItems("category", seq(1, 25)...), perPageMax: 10, totals: true}.ServeHTTP)
		snap := mustFetch(t, s, Options{})
		if !reflect.DeepEqual(snap.Terms, termsOf("category", seq(1, 25)...)) || categoryRequests(s) != 3 {
			t.Errorf("Fetch() read %d terms with %d requests, want 25 with 3", len(snap.Terms), categoryRequests(s))
		}
	})
	t.Run("pages that are short, without the total", func(t *testing.T) {
		// A site that gives fewer than the 100 asked for, and does not say how many pages it has, is read as having one page.
		s := categoriesOnly(t, pager{items: termItems("category", seq(1, 25)...), perPageMax: 10}.ServeHTTP)
		snap := mustFetch(t, s, Options{})
		if !reflect.DeepEqual(snap.Terms, termsOf("category", seq(1, 10)...)) || categoryRequests(s) != 1 {
			t.Errorf("Fetch() read %d terms with %d requests, want 10 with 1", len(snap.Terms), categoryRequests(s))
		}
	})
	t.Run("pages that are full, without the total", func(t *testing.T) {
		s := categoriesOnly(t, pager{items: termItems("category", seq(1, 250)...)}.ServeHTTP)
		snap := mustFetch(t, s, Options{})
		if len(snap.Terms) != 250 || categoryRequests(s) != 3 {
			t.Errorf("Fetch() read %d terms with %d requests, want 250 with 3", len(snap.Terms), categoryRequests(s))
		}
	})
	t.Run("a last page that is full, without the total", func(t *testing.T) {
		// The page after it is the end: a 400 from WordPress.
		s := categoriesOnly(t, pager{items: termItems("category", seq(1, 200)...)}.ServeHTTP)
		snap := mustFetch(t, s, Options{})
		if len(snap.Terms) != 200 || categoryRequests(s) != 3 {
			t.Errorf("Fetch() read %d terms with %d requests, want 200 with 3", len(snap.Terms), categoryRequests(s))
		}
	})
	t.Run("a last page that is full, with the total", func(t *testing.T) {
		s := categoriesOnly(t, pager{items: termItems("category", seq(1, 200)...), totals: true}.ServeHTTP)
		snap := mustFetch(t, s, Options{})
		if len(snap.Terms) != 200 || categoryRequests(s) != 2 {
			t.Errorf("Fetch() read %d terms with %d requests, want 200 with 2", len(snap.Terms), categoryRequests(s))
		}
	})
	t.Run("no terms", func(t *testing.T) {
		s := categoriesOnly(t, pager{totals: true}.ServeHTTP)
		snap := mustFetch(t, s, Options{})
		if snap.Terms != nil || categoryRequests(s) != 1 {
			t.Errorf("Fetch() read %d terms with %d requests, want none with 1", len(snap.Terms), categoryRequests(s))
		}
	})
	t.Run("a total that is huge, and an empty page", func(t *testing.T) {
		s := categoriesOnly(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-WP-TotalPages", "1000000000")
			if r.URL.Query().Get("page") == "1" {
				writeBody(w, http.StatusOK, items(1, 2, 3, 4, 5))
				return
			}
			writeBody(w, http.StatusOK, "[]")
		})
		snap := mustFetch(t, s, Options{})
		if !reflect.DeepEqual(snap.Terms, termsOf("category", seq(1, 5)...)) || categoryRequests(s) != 2 {
			t.Errorf("Fetch() read %d terms with %d requests, want 5 with 2", len(snap.Terms), categoryRequests(s))
		}
	})
	t.Run("a total that is huge, and a 400", func(t *testing.T) {
		s := categoriesOnly(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-WP-TotalPages", "1000000000")
			if r.URL.Query().Get("page") == "1" {
				writeBody(w, http.StatusOK, items(1, 2, 3))
				return
			}
			restError(w, http.StatusBadRequest, "rest_post_invalid_page_number", "x")
		})
		snap := mustFetch(t, s, Options{})
		if len(snap.Terms) != 3 || categoryRequests(s) != 2 {
			t.Errorf("Fetch() read %d terms with %d requests, want 3 with 2", len(snap.Terms), categoryRequests(s))
		}
	})
	t.Run("a total of 0 or 1", func(t *testing.T) {
		for _, total := range []string{"0", "1", " 1 "} {
			s := categoriesOnly(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("X-WP-TotalPages", total)
				writeBody(w, http.StatusOK, items(1, 2, 3))
			})
			snap := mustFetch(t, s, Options{})
			if len(snap.Terms) != 3 || categoryRequests(s) != 1 {
				t.Errorf("X-WP-TotalPages %q: Fetch() read %d terms with %d requests, want 3 with 1", total, len(snap.Terms), categoryRequests(s))
			}
		}
	})
	t.Run("a total that is not a number", func(t *testing.T) {
		for _, total := range []string{"many", "-1", "1.5", "", "99999999999999999999"} {
			s := categoriesOnly(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-WP-TotalPages", total)
				if r.URL.Query().Get("page") == "1" {
					writeBody(w, http.StatusOK, items(1, 2, 3))
					return
				}
				t.Errorf("X-WP-TotalPages %q: the page %s was requested", total, r.URL.Query().Get("page"))
				writeBody(w, http.StatusOK, "[]")
			})
			snap := mustFetch(t, s, Options{})
			if len(snap.Terms) != 3 {
				t.Errorf("X-WP-TotalPages %q: Fetch() read %d terms, want 3", total, len(snap.Terms))
			}
		}
	})
	t.Run("a total that is only in the first page", func(t *testing.T) {
		s := categoriesOnly(t, func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Query().Get("page") {
			case "1":
				w.Header().Set("X-WP-TotalPages", "3")
				writeBody(w, http.StatusOK, items(seq(1, 5)...))
			case "2":
				w.Header().Set("X-WP-TotalPages", "1")
				writeBody(w, http.StatusOK, items(seq(6, 7)...))
			default:
				w.Header().Set("X-WP-TotalPages", "100")
				writeBody(w, http.StatusOK, items(8))
			}
		})
		snap := mustFetch(t, s, Options{})
		if len(snap.Terms) != 8 || categoryRequests(s) != 3 {
			t.Errorf("Fetch() read %d terms with %d requests, want 8 with 3", len(snap.Terms), categoryRequests(s))
		}
	})
	t.Run("a site that ignores the page", func(t *testing.T) {
		s := categoriesOnly(t, pager{items: termItems("category", seq(1, 250)...), ignorePage: true}.ServeHTTP)
		_, err := fetch(t, s, Options{})
		if err == nil || !strings.HasPrefix(err.Error(), "GET "+s.URL()+"/wp-json/wp/v2/categories?") ||
			!strings.HasSuffix(err.Error(), ": page 2 repeats the items of the pages before it; the site may ignore the page parameter") {
			t.Errorf("Fetch() = %v, want the error that page 2 repeats the pages before it", err)
		}
		if categoryRequests(s) != 2 {
			t.Errorf("%d requests, want it to stop at the second", categoryRequests(s))
		}
	})
	t.Run("a page that repeats some items", func(t *testing.T) {
		s := categoriesOnly(t, func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Query().Get("page") {
			case "1":
				writeBody(w, http.StatusOK, items(seq(1, 100)...))
			case "2":
				writeBody(w, http.StatusOK, items(append([]int{100, 1}, seq(101, 150)...)...))
			default:
				writeBody(w, http.StatusOK, items(seq(150, 151)...))
			}
		})
		snap := mustFetch(t, s, Options{})
		if !reflect.DeepEqual(snap.Terms, termsOf("category", seq(1, 150)...)) || categoryRequests(s) != 2 {
			t.Errorf("Fetch() read %d terms with %d requests, want 150 with 2", len(snap.Terms), categoryRequests(s))
		}
	})
	t.Run("a page with the same item twice", func(t *testing.T) {
		s := categoriesOnly(t, func(w http.ResponseWriter, _ *http.Request) {
			writeBody(w, http.StatusOK, items(1, 2, 1, 2, 3))
		})
		snap := mustFetch(t, s, Options{})
		if !reflect.DeepEqual(snap.Terms, termsOf("category", 1, 2, 3)) {
			t.Errorf("Terms = %+v, want the first of each", snap.Terms)
		}
	})
	t.Run("a page that repeats the items of a page before the last", func(t *testing.T) {
		s := categoriesOnly(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-WP-TotalPages", "9")
			switch r.URL.Query().Get("page") {
			case "1":
				writeBody(w, http.StatusOK, items(seq(1, 3)...))
			case "2":
				writeBody(w, http.StatusOK, items(seq(4, 6)...))
			default:
				writeBody(w, http.StatusOK, items(seq(1, 3)...))
			}
		})
		_, err := fetch(t, s, Options{})
		if err == nil || !strings.HasSuffix(err.Error(), ": page 3 repeats the items of the pages before it; the site may ignore the page parameter") {
			t.Errorf("Fetch() = %v, want the error that page 3 repeats the pages before it", err)
		}
	})
	t.Run("pages after the first that are not ours", func(t *testing.T) {
		for _, tt := range []struct {
			name    string
			handler http.HandlerFunc
			want    string // the end of the error
		}{
			{"401", status(http.StatusUnauthorized, "rest_forbidden"), ": 401 Unauthorized (rest_forbidden)"},
			{"403", status(http.StatusForbidden, ""), ": 403 Forbidden"},
			{"404 page", func(w http.ResponseWriter, _ *http.Request) { notFound(w) }, ": 404 Not Found"},
			{"429", status(http.StatusTooManyRequests, "too_many"), ": 429 Too Many Requests (too_many)"},
			{"500", status(http.StatusInternalServerError, "oops"), ": 500 Internal Server Error"},
			{"200 page", html200, ": not JSON (text/html)"},
			{"200 object", func(w http.ResponseWriter, _ *http.Request) { writeBody(w, http.StatusOK, `{}`) }, ": expected array, got object"},
		} {
			s := categoriesOnly(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-WP-TotalPages", "5")
				if r.URL.Query().Get("page") == "1" {
					writeBody(w, http.StatusOK, items(seq(1, 100)...))
					return
				}
				tt.handler(w, r)
			})
			_, err := fetch(t, s, Options{})
			if err == nil || !strings.HasSuffix(err.Error(), tt.want) || !strings.Contains(err.Error(), "page=2") {
				t.Errorf("%s: Fetch() = %v, want an error for page 2 that ends %q", tt.name, err, tt.want)
			}
		}
	})
	t.Run("first pages that are not ours", func(t *testing.T) {
		for _, tt := range []struct {
			name    string
			handler http.HandlerFunc
			want    string
		}{
			{"408", status(http.StatusRequestTimeout, ""), ": 408 Request Timeout"},
			{"429", status(http.StatusTooManyRequests, "too_many"), ": 429 Too Many Requests (too_many)"},
			{"500", status(http.StatusInternalServerError, ""), ": 500 Internal Server Error"},
			{"502", status(http.StatusBadGateway, ""), ": 502 Bad Gateway"},
			{"503", status(http.StatusServiceUnavailable, "maintenance"), ": 503 Service Unavailable"},
			{"304", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotModified) }, ": 304 Not Modified"},
			{"599", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(599) }, ": 599"},
		} {
			s := categoriesOnly(t, tt.handler)
			_, err := fetch(t, s, Options{})
			if err == nil || !strings.HasSuffix(err.Error(), tt.want) || !strings.HasPrefix(err.Error(), "GET "+s.URL()+"/wp-json/wp/v2/categories?") {
				t.Errorf("%s: Fetch() = %v, want an error that ends %q", tt.name, err, tt.want)
			}
			if categoryRequests(s) != 1 {
				t.Errorf("%s: %d requests, want the one", tt.name, categoryRequests(s))
			}
		}
	})
}

func TestFetchLimits(t *testing.T) {
	suffix := func(err error, want string) {
		t.Helper()
		if err == nil || !strings.HasSuffix(err.Error(), ": "+want) || !strings.HasPrefix(err.Error(), "GET http") {
			t.Errorf("Fetch() = %v, want an error for a request that ends %q", err, want)
		}
	}
	t.Run("terms", func(t *testing.T) {
		s := newSite(t).standard()
		// The second page has the term over the limit, so the error is for it.
		_, err := fetch(t, s, Options{MaxTerms: 150})
		suffix(err, "too many terms: at least 151 (max 150)")
		if err != nil && !strings.Contains(err.Error(), "page=2") {
			t.Errorf("Fetch() = %v, want the error for page 2", err)
		}
		if s.requested()[len(s.requested())-1] != "/wp-json/wp/v2/categories?page=2" {
			t.Errorf("the requests are %q, want the last to be the second page of the categories", s.requested())
		}
	})
	t.Run("terms of all the taxonomies", func(t *testing.T) {
		// 250 categories, 2 genres and 3 tags, in the order of the taxonomies.
		s := newSite(t).standard()
		if _, err := fetch(t, s, Options{MaxTerms: 255}); err != nil {
			t.Errorf("Fetch() with the limit of the number of terms = %v", err)
		}
		s = newSite(t).standard()
		_, err := fetch(t, s, Options{MaxTerms: 254})
		suffix(err, "too many terms: at least 255 (max 254)")
		if err != nil && !strings.Contains(err.Error(), "/wp/v2/tags") {
			t.Errorf("Fetch() = %v, want the error for the tags", err)
		}
	})
	t.Run("users", func(t *testing.T) {
		s := newSite(t).standard()
		if _, err := fetch(t, s, Options{MaxUsers: 2}); err != nil {
			t.Errorf("Fetch() with the limit of the number of users = %v", err)
		}
		s = newSite(t).standard()
		_, err := fetch(t, s, Options{MaxUsers: 1})
		suffix(err, "too many users: at least 2 (max 1)")
	})
	t.Run("pages", func(t *testing.T) {
		// A site that says it has 1000000000 pages, and has a new item on each of them, is read for the number of pages
		// that the limit allows and one more, and then it is an error.
		s := newSite(t).standard()
		s.handleJSON("/wp/v2/taxonomies", `{"category":{"slug":"category","rest_base":"categories"}}`)
		s.handle("/wp/v2/categories", func(w http.ResponseWriter, r *http.Request) {
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			w.Header().Set("X-WP-TotalPages", "1000000000")
			writeBody(w, http.StatusOK, "["+termJSON(page, "category")+"]")
		})
		_, err := fetch(t, s, Options{MaxTerms: 100})
		suffix(err, "more than 2 pages of terms")
		if n := len(s.requested()); n != 5 {
			t.Errorf("%d requests: %q, want the index, taxonomies, types and 2 pages", n, s.requested())
		}
		s = newSite(t).standard()
		s.handleJSON("/wp/v2/taxonomies", `{"category":{"slug":"category","rest_base":"categories"}}`)
		s.handle("/wp/v2/categories", func(w http.ResponseWriter, r *http.Request) {
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			w.Header().Set("X-WP-TotalPages", "1000000000")
			writeBody(w, http.StatusOK, "["+termJSON(page, "category")+"]")
		})
		_, err = fetch(t, s, Options{MaxTerms: 101})
		suffix(err, "more than 3 pages of terms")
	})
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestFetchInvalidURLs(t *testing.T) {
	var called atomic.Int32
	opts := Options{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		called.Add(1)
		return nil, errors.New("no requests")
	})}
	for _, home := range []string{
		"", "example.com", "//example.com", "ftp://example.com", "file:///etc/passwd", "https://", "http://:80", "https:///x",
		"https://user@example.com", "https://user:pass@example.com", "https://example.com?", "https://example.com/?a=1",
		"https://example.com#", "https://example.com/#top", "https://exa mple.com", "https://example.com/%zz", "http:example.com",
		" https://example.com", "https://example.com/\n", "https://exa\x7fmple.com",
	} {
		_, err := Fetch(context.Background(), home, opts)
		want := fmt.Sprintf("invalid home URL %q: use an http or https URL without a user, a query or a fragment", home)
		if err == nil || err.Error() != want {
			t.Errorf("Fetch(%q) = %v, want %s", home, err, want)
		}
	}
	for _, api := range []string{
		"ftp://example.com/wp-json/", "https://example.com/wp-json/?a=1", "https://example.com/?rest_route=/&a=1", "https://example.com/?",
		"https://example.com/?a=1&rest_route=/", "https://u@example.com/wp-json/", "https://example.com/wp-json/#a", "wp-json", "https://example.com/?%zz",
	} {
		_, err := Fetch(context.Background(), "https://example.com", Options{API: api, Transport: opts.Transport})
		want := fmt.Sprintf("invalid REST API URL %q: use an http or https URL without a user or a fragment, and with no query but rest_route", api)
		if err == nil || err.Error() != want {
			t.Errorf("Fetch() with the API %q = %v, want %s", api, err, want)
		}
	}
	if called.Load() != 0 {
		t.Errorf("%d requests were sent, want none", called.Load())
	}

	// Some URLs are valid.
	for _, home := range []string{"http://example.com", "https://example.com/", "https://example.com//", "HTTPS://Example.COM/blog/", "http://[::1]:8080", "https://例え.jp"} {
		called.Store(0)
		_, err := Fetch(context.Background(), home, opts)
		if err == nil || !strings.Contains(err.Error(), "no requests") || called.Load() != 1 {
			t.Errorf("Fetch(%q) = %v after %d requests, want the error of the transport for the first request", home, err, called.Load())
		}
	}
}

func TestFetchIndexNotFound(t *testing.T) {
	s := newSite(t).setPretty(false)
	_, err := fetch(t, s, Options{})
	fields := url.QueryEscape(indexFields)
	want := fmt.Sprintf("no REST API at %s: %s/wp-json/?_fields=%s: 404 Not Found; %s/?_fields=%s&rest_route=%%2F: 404 Not Found (rest_no_route)",
		s.URL(), s.URL(), fields, s.URL(), fields)
	if err == nil || err.Error() != want {
		t.Errorf("Fetch() = %v\nwant %s", err, want)
	}
	checkRequested(t, s, "/wp-json/", "/?rest_route=/")

	// A home with a slash at the end is the same.
	_, err = Fetch(context.Background(), s.URL()+"/", Options{})
	if err == nil || err.Error() != want {
		t.Errorf("Fetch() with a slash at the end = %v\nwant %s", err, want)
	}
}

func TestFetchIndex(t *testing.T) {
	type handler = http.HandlerFunc
	text := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, `{"namespaces":[]}`)
	}
	jsonOf := func(body string) handler {
		return func(w http.ResponseWriter, _ *http.Request) { writeBody(w, http.StatusOK, body) }
	}

	// What is not the index of a REST API, which makes Fetch try the next URL.
	for _, tt := range []struct {
		name    string
		handler handler
		reason  string
	}{
		{"a page", html200, "not JSON (text/html)"},
		{"text", text, "not JSON (text/plain)"},
		{"without a type", func(w http.ResponseWriter, _ *http.Request) {
			w.Header()["Content-Type"] = nil
			_, _ = io.WriteString(w, `{"namespaces":[]}`)
		}, "not JSON (no Content-Type)"},
		{"another JSON type", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/hal+json")
			_, _ = io.WriteString(w, `{"namespaces":[]}`)
		}, "not JSON (application/hal+json)"},
		{"an invalid type", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "json")
			_, _ = io.WriteString(w, `{"namespaces":[]}`)
		}, "not JSON (json)"},
		{"an array", jsonOf(`[]`), "not the index of a REST API: expected object, got array"},
		{"a string", jsonOf(`"wp/v2"`), "not the index of a REST API: expected object, got string"},
		{"without namespaces", jsonOf(`{"name":"x"}`), "not the index of a REST API"},
		{"namespaces that is a string", jsonOf(`{"namespaces":"wp/v2"}`), "not the index of a REST API"},
		{"namespaces that is null", jsonOf(`{"namespaces":null}`), "not the index of a REST API"},
		{"an object that is not closed", jsonOf(`{"namespaces":[]`), "not the index of a REST API: unexpected end of JSON input"},
		{"nothing", jsonOf(``), "not the index of a REST API: unexpected end of JSON input"},
		{"a name that is a number", jsonOf(`{"namespaces":[],"name":5}`), "not the index of a REST API: /name: expected string, got number"},
		{"data after the object", jsonOf(`{"namespaces":[]} <br />`), "not the index of a REST API: unexpected data after value"},
		{"a PHP notice before the object", jsonOf("<br />\n<b>Warning</b>: x\n" + `{"namespaces":[]}`), "not the index of a REST API: invalid character '<' at start of value"},
		{"404 page", func(w http.ResponseWriter, _ *http.Request) { notFound(w) }, "404 Not Found"},
		{"404", status(http.StatusNotFound, "rest_no_route"), "404 Not Found (rest_no_route)"},
		{"401", status(http.StatusUnauthorized, "rest_login_required"), "401 Unauthorized (rest_login_required)"},
		{"403", status(http.StatusForbidden, "rest_forbidden"), "403 Forbidden (rest_forbidden)"},
		{"406", status(http.StatusNotAcceptable, "not_acceptable"), "406 Not Acceptable (not_acceptable)"},
		{"410", status(http.StatusGone, "gone"), "410 Gone (gone)"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := newSite(t).standard()
			s.handle("/", tt.handler)
			_, err := fetch(t, s, Options{})
			if err == nil || !strings.HasPrefix(err.Error(), "no REST API at "+s.URL()+": "+s.URL()+"/wp-json/?_fields=") ||
				strings.Count(err.Error(), ": "+tt.reason) != 2 || !strings.HasSuffix(err.Error(), ": "+tt.reason) {
				t.Errorf("Fetch() = %v, want that there is no REST API because of: %s", err, tt.reason)
			}
			checkRequested(t, s, "/wp-json/", "/?rest_route=/")
		})
	}

	// What is not an answer, which ends it at once.
	for _, tt := range []struct {
		name    string
		handler handler
		reason  string
	}{
		{"400", status(http.StatusBadRequest, "rest_invalid"), "400 Bad Request (rest_invalid)"},
		{"405", status(http.StatusMethodNotAllowed, ""), "405 Method Not Allowed"},
		{"408", status(http.StatusRequestTimeout, ""), "408 Request Timeout"},
		{"429", status(http.StatusTooManyRequests, "too_many"), "429 Too Many Requests (too_many)"},
		{"500", status(http.StatusInternalServerError, "internal_server_error"), "500 Internal Server Error"},
		{"503", status(http.StatusServiceUnavailable, "maintenance"), "503 Service Unavailable"},
		{"304", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotModified) }, "304 Not Modified"},
		{"an unknown status", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(599) }, "599"},
		{"201", func(w http.ResponseWriter, _ *http.Request) {
			writeBody(w, http.StatusCreated, `{"namespaces":[]}`)
		}, "201 Created"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := newSite(t).standard()
			s.handle("/", tt.handler)
			_, err := fetch(t, s, Options{})
			if err == nil || !strings.HasPrefix(err.Error(), "GET "+s.URL()+"/wp-json/?_fields=") || !strings.HasSuffix(err.Error(), ": "+tt.reason) ||
				strings.HasPrefix(err.Error(), "no REST API") {
				t.Errorf("Fetch() = %v, want an error that ends %q", err, tt.reason)
			}
			checkRequested(t, s, "/wp-json/")
		})
	}

	t.Run("the second URL", func(t *testing.T) {
		// The first is not the index, and the second is.
		s := newSite(t).standard().setPretty(false)
		snap := mustFetch(t, s, Options{})
		if snap.API != s.URL()+"/?rest_route=/" {
			t.Errorf("API = %q", snap.API)
		}
	})
	t.Run("the media type", func(t *testing.T) {
		for _, ct := range []string{"application/json", "application/json; charset=UTF-8", "Application/JSON", "application/json;charset=utf-8;x=y", "application/json; charset"} {
			s := newSite(t).standard()
			s.handle("/", func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", ct)
				_, _ = io.WriteString(w, fmt.Sprintf(standardIndex, s.URL(), s.URL()))
			})
			snap, err := fetch(t, s, Options{})
			if err != nil || snap.Index.Name != "テストのサイト" {
				t.Errorf("Content-Type %q: Fetch() = (a snapshot: %v), %v, want the index to be read", ct, snap != nil, err)
			}
		}
	})
	t.Run("the values of WordPress", func(t *testing.T) {
		// WordPress takes some from options, so they can be strings or false, and it writes the offset as a number or a string.
		for _, tt := range []struct {
			body string
			want Index
		}{
			{`{"namespaces":[],"gmt_offset":9,"site_logo":false,"site_icon":"12","page_on_front":"0","page_for_posts":0}`, Index{GMTOffset: "9", SiteIcon: 12}},
			{`{"namespaces":[],"gmt_offset":-3.5,"site_logo":"","site_icon":null,"timezone_string":""}`, Index{GMTOffset: "-3.5"}},
			{`{"namespaces":[],"gmt_offset":"","description":"","show_on_front":"posts"}`, Index{ShowOnFront: "posts"}},
		} {
			s := newSite(t).standard()
			s.handleJSON("/", tt.body)
			if snap := mustFetch(t, s, Options{}); snap.Index != tt.want {
				t.Errorf("%s: Index = %+v, want %+v", tt.body, snap.Index, tt.want)
			}
		}
	})
	t.Run("a larger index than WordPress gives", func(t *testing.T) {
		// A site that does not know _fields gives all of it, and the routes are ignored.
		s := newSite(t).standard()
		routes := make([]string, 2000)
		for i := range routes {
			routes[i] = fmt.Sprintf(`"/wp/v2/route%d":{"namespace":"wp/v2","methods":["GET","POST"],"endpoints":[{"methods":["GET"],"args":{"context":{"type":"string"}}}]}`, i)
		}
		s.handleJSON("/", `{"name":"N","namespaces":["wp/v2"],"routes":{`+strings.Join(routes, ",")+`},"authentication":{}}`)
		if snap := mustFetch(t, s, Options{}); snap.Index.Name != "N" {
			t.Errorf("Index = %+v", snap.Index)
		}
	})
}

func TestFetchResponseErrors(t *testing.T) {
	tests := []struct {
		name    string
		route   string
		body    string
		pointer string
		msg     string
	}{
		{"types: an object that is not closed", "/wp/v2/types", `{"post":{"slug":"post"}`, "", "unexpected end of JSON input"},
		{"users: an array that is not closed", "/wp/v2/users", `[{"id":1}`, "", "unexpected end of JSON input"},
		{"users: an object", "/wp/v2/users", `{"id":1}`, "", "expected array, got object"},
		{"users: null", "/wp/v2/users", `null`, "", "expected array, got null"},
		{"users: an empty response", "/wp/v2/users", ``, "", "unexpected end of JSON input"},
		{"users: data after the array", "/wp/v2/users", `[] []`, "", "unexpected data after value"},
		{"users: a notice before the array", "/wp/v2/users", "Notice: x\n[]", "", "invalid character 'N' at start of value"},
		{"users: a key twice", "/wp/v2/users", `[{"id":1,"id":2}]`, "/0/id", "duplicate key"},
		{"users: a key twice in the part that is ignored", "/wp/v2/users", `[{"id":1,"_links":{"a":1,"a":2}}]`, "/0/_links/a", "duplicate key"},
		{"users: an ID of 0", "/wp/v2/users", `[{"id":1},{"id":0}]`, "/1/id", "invalid id 0: use an integer from 1"},
		{"users: no ID", "/wp/v2/users", `[{"id":1},{"name":"x"}]`, "/1", `missing key "id"`},
		{"users: an ID that is a string", "/wp/v2/users", `[{"id":"1"}]`, "/0/id", "expected number, got string"},
		{"users: a name that is a number", "/wp/v2/users", `[{"id":1,"name":2}]`, "/0/name", "expected string, got number"},
		{"users: an item that is a number", "/wp/v2/users", `[1]`, "/0", "expected object, got number"},
		{"users: invalid UTF-8", "/wp/v2/users", "[{\"id\":1,\"name\":\"\xff\"}]", "/0/name", "invalid UTF-8"},
		{"categories: an ID of -1", "/wp/v2/categories", `[{"id":-1}]`, "/0/id", "invalid id -1: use an integer from 1"},
		{"categories: a parent of -1", "/wp/v2/categories", `[{"id":1,"parent":-1}]`, "/0/parent", "invalid parent -1: use an integer from 0"},
		{"categories: an object", "/wp/v2/categories", `{"code":"x"}`, "", "expected array, got object"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newSite(t).standard()
			s.handleJSON(tt.route, tt.body)
			_, err := fetch(t, s, Options{})
			var e *Error
			if !errors.As(err, &e) || e.Pointer != tt.pointer || !strings.Contains(e.Msg, tt.msg) {
				t.Fatalf("Fetch() = %v, want an *Error at %q with %q", err, tt.pointer, tt.msg)
			}
			prefix := "GET " + s.URL() + "/wp-json" + tt.route + "?"
			if tt.route == "/wp/v2/types" {
				prefix = "GET " + s.URL() + "/wp-json" + tt.route + ": "
			}
			if !strings.HasPrefix(err.Error(), prefix) {
				t.Errorf("Fetch() = %v, want it to start with the request %s", err, prefix)
			}
		})
	}

}

func TestFetchServerErrors(t *testing.T) {
	// The errors that are not about what the site answers.
	for _, route := range []string{"/wp/v2/taxonomies", "/wp/v2/types", "/wp/v2/categories", "/wp/v2/users"} {
		for _, code := range []int{http.StatusInternalServerError, http.StatusBadGateway, http.StatusTooManyRequests, http.StatusRequestTimeout} {
			s := newSite(t).standard()
			s.handle(route, status(code, ""))
			_, err := fetch(t, s, Options{})
			want := fmt.Sprintf("%d %s", code, http.StatusText(code))
			prefix := "GET " + s.URL() + "/wp-json" + route + "?"
			if route == "/wp/v2/taxonomies" || route == "/wp/v2/types" {
				prefix = "GET " + s.URL() + "/wp-json" + route + ": "
			}
			if err == nil || !strings.HasPrefix(err.Error(), prefix) || !strings.HasSuffix(err.Error(), ": "+want) {
				t.Errorf("%s %d: Fetch() = %v, want an error for the request that ends %q", route, code, err, want)
			}
		}
	}
}

func TestFetchResponseTooLarge(t *testing.T) {
	s := newSite(t).standard()
	// The index is 700 bytes or so.
	_, err := fetch(t, s, Options{MaxResponseBytes: 100})
	if err == nil || err.Error() != "GET "+s.URL()+"/wp-json/?_fields="+url.QueryEscape(indexFields)+": the response is larger than 100 bytes" {
		t.Errorf("Fetch() = %v, want that the response is larger than 100 bytes", err)
	}

	// small is a site with little in it, so that only the users make a response of any size.
	small := func() *site {
		s := newSite(t)
		s.handleJSON("/", `{"namespaces":[]}`)
		s.handleJSON("/wp/v2/taxonomies", `[]`)
		s.handleJSON("/wp/v2/types", `[]`)
		return s
	}
	s = small()
	body := userJSON(1)
	s.handleJSON("/wp/v2/users", "["+body+"]")
	if _, err := fetch(t, s, Options{MaxResponseBytes: int64(len(body)) + 2}); err != nil {
		t.Errorf("Fetch() with the size of the response as the limit = %v", err)
	}
	_, err = fetch(t, s, Options{MaxResponseBytes: int64(len(body)) + 1})
	if err == nil || !strings.HasSuffix(err.Error(), fmt.Sprintf(": the response is larger than %d bytes", len(body)+1)) {
		t.Errorf("Fetch() with one byte less = %v, want that the response is too large", err)
	}

	// A page that is not JSON is not read, so it can be larger than the limit.
	s = small()
	s.handle("/wp/v2/users", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, strings.Repeat("x", 1<<20))
	})
	snap, err := fetch(t, s, Options{MaxResponseBytes: 100})
	if err != nil {
		t.Fatalf("Fetch() = %v", err)
	}
	if want := []Unavailable{{Route: "/wp/v2/users", Status: 200, Code: "not_json"}}; !reflect.DeepEqual(snap.Unavailable, want) {
		t.Errorf("Unavailable = %+v, want %+v", snap.Unavailable, want)
	}

	// The body of an error response is not read beyond what is needed for the code, whatever MaxResponseBytes is.
	s = small()
	s.handle("/wp/v2/users", func(w http.ResponseWriter, _ *http.Request) {
		writeBody(w, http.StatusForbidden, `{"code":"rest_forbidden","message":"`+strings.Repeat("x", 1000)+`"}`)
	})
	snap, err = fetch(t, s, Options{MaxResponseBytes: 100})
	if err != nil {
		t.Fatalf("Fetch() = %v", err)
	}
	if want := []Unavailable{{Route: "/wp/v2/users", Status: 403, Code: "rest_forbidden"}}; !reflect.DeepEqual(snap.Unavailable, want) {
		t.Errorf("Unavailable = %+v, want %+v", snap.Unavailable, want)
	}
}

// termsSite is a site that has only categories in its REST API, the items, served 100 to a page. It also returns the size of
// the bodies of all its responses together, for a Fetch that asks for every page.
func termsSite(t *testing.T, items []string) (*site, int64) {
	t.Helper()
	const index, taxonomies, types, users = `{"namespaces":[]}`, `{"category":{"slug":"category","rest_base":"categories"}}`, `[]`, `[]`
	s := newSite(t)
	s.handleJSON("/", index)
	s.handleJSON("/wp/v2/taxonomies", taxonomies)
	s.handleJSON("/wp/v2/types", types)
	s.handle("/wp/v2/categories", pager{items: items, totals: true}.ServeHTTP)
	s.handleJSON("/wp/v2/users", users)
	total := int64(len(index) + len(taxonomies) + len(types) + len(users))
	for lo := 0; lo < len(items); lo += PerPage {
		total += int64(len("[" + strings.Join(items[lo:min(lo+PerPage, len(items))], ",") + "]"))
	}
	return s, total
}

// TestFetchResponsesLimit checks that the responses that Fetch keeps are limited together by MaxSnapshotBytes: each is
// smaller than MaxResponseBytes, but a site with many pages would make Fetch hold them all.
func TestFetchResponsesLimit(t *testing.T) {
	// 10 pages of 100 categories, a page of about 400 KB.
	var items []string
	for id := 1; id <= 1000; id++ {
		items = append(items, fmt.Sprintf(`{"id":%d,"taxonomy":"category","slug":"t%d","name":%q,"parent":0,"link":""}`, id, id, strings.Repeat("x", 4000)))
	}
	const limit = 1 << 20
	s, total := termsSite(t, items)
	if total < 3*limit {
		t.Fatalf("the responses are %d bytes together, want the site to be much larger than the limit", total)
	}
	_, err := fetch(t, s, Options{MaxSnapshotBytes: limit})
	// The first two pages and the small responses are about 830 KB, so the third page is the one that is over.
	want := fmt.Sprintf(": the responses are larger than %d bytes together", limit)
	if err == nil || !strings.HasPrefix(err.Error(), "GET http") || !strings.HasSuffix(err.Error(), want) || !strings.Contains(err.Error(), "page=3") {
		t.Errorf("Fetch() = %v, want an error for the third page that ends %q", err, want)
	}
	if got := s.requested()[len(s.requested())-1]; got != "/wp-json/wp/v2/categories?page=3" {
		t.Errorf("the last request is %q, want the third page of the categories (%q)", got, s.requested())
	}

	// With a limit that is larger than all the responses together, every page is read.
	s, total = termsSite(t, items)
	snap, err := fetch(t, s, Options{MaxSnapshotBytes: total + 1<<20})
	if err != nil {
		t.Fatalf("Fetch() with a limit larger than the responses = %v", err)
	}
	if len(snap.Terms) != len(items) {
		t.Errorf("Fetch() with a limit larger than the responses = %d terms, want %d", len(snap.Terms), len(items))
	}
}

// TestFetchSnapshotLimit checks that a snapshot that Fetch returns can be written and read back with the same Options: the
// file that WriteJSON writes is larger than the responses, because of the indentation and the key taxonomy.
func TestFetchSnapshotLimit(t *testing.T) {
	var items []string
	for id := 1; id <= 1000; id++ {
		items = append(items, fmt.Sprintf(`{"id":%d,"slug":"a","name":"b"}`, id))
	}
	s, total := termsSite(t, items)
	snap := mustFetch(t, s, Options{})
	file := writeString(t, snap)
	n := int64(len(file))
	if total >= n-1 {
		t.Fatalf("the responses are %d bytes together and the snapshot is %d, want the snapshot to be larger", total, n)
	}

	// The limit is the size of the file: the snapshot is returned, and reads back as it is.
	got := mustFetch(t, s, Options{MaxSnapshotBytes: n})
	checkSnapshot(t, got, snap)
	back, err := ReadSnapshot(strings.NewReader(writeString(t, got)), Options{MaxSnapshotBytes: n})
	if err != nil {
		t.Fatalf("ReadSnapshot() with the Options of Fetch = %v", err)
	}
	checkSnapshot(t, back, snap)

	// One byte less: the responses are within the limit, and the snapshot is not.
	_, err = fetch(t, s, Options{MaxSnapshotBytes: n - 1})
	if want := fmt.Sprintf("the snapshot is too large to save: at least %d bytes (max %d)", n, n-1); err == nil || err.Error() != want {
		t.Errorf("Fetch() with one byte less = %v, want %q", err, want)
	}
	if _, err := ReadSnapshot(strings.NewReader(file), Options{MaxSnapshotBytes: n - 1}); err == nil {
		t.Error("ReadSnapshot() with one byte less read the file")
	}
}

func TestFetchCompressed(t *testing.T) {
	// The transport decompresses a response that is gzipped, and the limit is for what it is after that.
	gzipped := func(body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
				t.Errorf("Accept-Encoding = %q, want gzip", r.Header.Get("Accept-Encoding"))
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Encoding", "gzip")
			zw := gzip.NewWriter(w)
			_, _ = io.WriteString(zw, body)
			_ = zw.Close()
		}
	}
	small := func() *site {
		s := newSite(t)
		s.handleJSON("/", `{"namespaces":[]}`)
		s.handleJSON("/wp/v2/taxonomies", `[]`)
		s.handleJSON("/wp/v2/types", `[]`)
		return s
	}
	s := small()
	s.handle("/wp/v2/users", gzipped("["+userJSON(1)+"]"))
	snap := mustFetch(t, s, Options{})
	if want := []User{{ID: 1, Name: "User 1", Slug: "u1", Link: "https://example.com/author/u1/"}}; !reflect.DeepEqual(snap.Users, want) {
		t.Errorf("Users = %+v, want %+v", snap.Users, want)
	}

	s = small()
	s.handle("/wp/v2/users", gzipped("["+strings.Repeat(" ", 1<<20)+"]"))
	_, err := fetch(t, s, Options{MaxResponseBytes: 1000})
	if err == nil || !strings.HasSuffix(err.Error(), ": the response is larger than 1000 bytes") {
		t.Errorf("Fetch() = %v, want that the response is larger than 1000 bytes, though it is a few bytes on the wire", err)
	}
}

func TestFetchTLS(t *testing.T) {
	s := newSite(t).standard()
	s.srv.Close()
	s.srv = httptest.NewUnstartedServer(s)
	s.srv.Config.ErrorLog = log.New(io.Discard, "", 0) // the handshake with a client that does not trust the certificate
	s.srv.StartTLS()
	t.Cleanup(s.srv.Close)
	if !strings.HasPrefix(s.URL(), "https://") {
		t.Fatalf("URL() = %q", s.URL())
	}
	snap := mustFetch(t, s, Options{Transport: s.srv.Client().Transport})
	checkSnapshot(t, snap, s.standardSnapshot(s.URL()+"/wp-json/"))

	// The default transport does not trust the certificate of the test.
	_, err := fetch(t, s, Options{})
	if err == nil || !strings.HasPrefix(err.Error(), "GET "+s.URL()+"/wp-json/?_fields=") || !strings.Contains(err.Error(), "certificate") {
		t.Errorf("Fetch() = %v, want that the certificate is not trusted", err)
	}
}

func TestFetchTimeout(t *testing.T) {
	s := newSite(t).standard()
	s.handle("/wp/v2/users", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	})
	start := time.Now()
	_, err := fetch(t, s, Options{Timeout: 500 * time.Millisecond})
	if err == nil || !strings.HasPrefix(err.Error(), "GET "+s.URL()+"/wp-json/wp/v2/users?") {
		t.Fatalf("Fetch() = %v, want an error for the request for the users", err)
	}
	var ne net.Error
	if !errors.As(err, &ne) || !ne.Timeout() {
		t.Errorf("Fetch() = %v (%T), want an error that is a timeout", err, err)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("Fetch() took %v", time.Since(start))
	}

	// A body that stops is a timeout too.
	s = newSite(t).standard()
	s.handle("/wp/v2/users", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", "1000")
		_, _ = io.WriteString(w, "[")
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	})
	_, err = fetch(t, s, Options{Timeout: 500 * time.Millisecond})
	if err == nil || !strings.HasPrefix(err.Error(), "GET "+s.URL()+"/wp-json/wp/v2/users?") || !errors.As(err, &ne) || !ne.Timeout() {
		t.Errorf("Fetch() with a body that stops = %v, want a timeout for the users", err)
	}
}

func TestFetchContext(t *testing.T) {
	t.Run("done before", func(t *testing.T) {
		s := newSite(t).standard()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		snap, err := Fetch(ctx, s.URL(), Options{})
		if !errors.Is(err, context.Canceled) || err.Error() != "context canceled" || snap != nil {
			t.Errorf("Fetch() = (a snapshot: %v), %v, want no snapshot and context.Canceled", snap != nil, err)
		}
		if reqs := s.requested(); len(reqs) != 0 {
			t.Errorf("the requests are %q, want none", reqs)
		}
	})
	t.Run("done in a request", func(t *testing.T) {
		s := newSite(t).standard()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		s.handle("/wp/v2/types", func(w http.ResponseWriter, _ *http.Request) {
			cancel()
			writeBody(w, http.StatusOK, standardTypes)
		})
		snap, err := Fetch(ctx, s.URL(), Options{})
		if !errors.Is(err, context.Canceled) || err.Error() != "context canceled" || snap != nil {
			t.Errorf("Fetch() = (a snapshot: %v), %v, want no snapshot and context.Canceled", snap != nil, err)
		}
		if reqs := s.requested(); len(reqs) != 3 {
			t.Errorf("the requests are %q, want to stop after the one that cancelled", reqs)
		}
	})
	t.Run("done while the body of the last response is read", func(t *testing.T) {
		// The body of an error is read only for its code, and a failure to read it is not an error; the end is.
		s := newSite(t).standard()
		s.handle("/wp/v2/users", status(http.StatusForbidden, "rest_forbidden"))
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		inner := s.srv.Client().Transport
		tr := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			resp, err := inner.RoundTrip(r)
			if err == nil && r.URL.Path == "/wp-json/wp/v2/users" {
				resp.Body = &cancelOnRead{ReadCloser: resp.Body, cancel: cancel}
			}
			return resp, err
		})
		snap, err := Fetch(ctx, s.URL(), Options{Transport: tr})
		if !errors.Is(err, context.Canceled) || snap != nil {
			t.Errorf("Fetch() = (a snapshot: %v), %v, want no snapshot and context.Canceled", snap != nil, err)
		}
	})
	t.Run("deadline", func(t *testing.T) {
		s := newSite(t).standard()
		s.handle("/wp/v2/taxonomies", func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
			case <-time.After(10 * time.Second):
			}
		})
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		_, err := Fetch(ctx, s.URL(), Options{})
		if !errors.Is(err, context.DeadlineExceeded) || err.Error() != "context deadline exceeded" {
			t.Errorf("Fetch() = %v, want context.DeadlineExceeded", err)
		}
	})
}

// cancelOnRead cancels the context when the body is read, and fails the read.
type cancelOnRead struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelOnRead) Read([]byte) (int, error) {
	c.cancel()
	return 0, context.Canceled
}

func TestNewTransport(t *testing.T) {
	tr := newTransport()
	if tr.Proxy == nil || tr.DialContext == nil || tr.TLSClientConfig == nil || tr.TLSClientConfig.MinVersion != 0x0303 ||
		tr.TLSHandshakeTimeout != 10*time.Second || tr.ResponseHeaderTimeout != 30*time.Second || tr.MaxResponseHeaderBytes != 64<<10 || !tr.ForceAttemptHTTP2 {
		t.Errorf("newTransport() = %+v", tr)
	}
}

func TestFetchRedirects(t *testing.T) {
	// A redirect to the same site is followed, with nothing that tells where the request came from.
	t.Run("same site", func(t *testing.T) {
		s := newSite(t).standard()
		s.setBefore(func(w http.ResponseWriter, r *http.Request) bool {
			if r.URL.Path == "/wp-json/wp/v2/users" && r.URL.Query().Get("moved") == "" {
				q := r.URL.Query()
				q.Set("moved", "1")
				http.Redirect(w, r, "/wp-json/wp/v2/users?"+q.Encode(), http.StatusMovedPermanently)
				return true
			}
			if r.URL.Path == "/wp-json/" && r.URL.Query().Get("moved") == "" {
				http.Redirect(w, r, "/wp-json/?moved=1&"+r.URL.RawQuery, http.StatusFound)
				return true
			}
			return false
		})
		snap := mustFetch(t, s, Options{})
		if len(snap.Users) != 2 || snap.Index.Name != "テストのサイト" {
			t.Errorf("Fetch() has the index %+v and %d users, want the index and the users after redirects", snap.Index, len(snap.Users))
		}
		checkHeaders(t, s)
		var hops int
		for _, r := range s.log() {
			if r.Query.Get("moved") != "" {
				hops++
			}
		}
		if hops != 2 {
			t.Errorf("%d requests after a redirect, want 2", hops)
		}
	})
	t.Run("the limit", func(t *testing.T) {
		for hops := 0; hops <= MaxRedirects+1; hops++ {
			s := newSite(t).standard()
			s.setBefore(func(w http.ResponseWriter, r *http.Request) bool {
				if r.URL.Path != "/wp-json/" {
					return false
				}
				n, _ := strconv.Atoi(r.URL.Query().Get("hop"))
				if n >= hops {
					return false
				}
				http.Redirect(w, r, fmt.Sprintf("/wp-json/?hop=%d", n+1), http.StatusFound)
				return true
			})
			_, err := fetch(t, s, Options{})
			if hops <= MaxRedirects {
				if err != nil {
					t.Errorf("%d redirects: Fetch() = %v", hops, err)
				}
				continue
			}
			if err == nil || !strings.HasPrefix(err.Error(), "GET "+s.URL()+"/wp-json/?_fields=") ||
				!strings.HasSuffix(err.Error(), ": stopped after 5 redirects") {
				t.Errorf("%d redirects: Fetch() = %v, want that it stopped after 5 redirects", hops, err)
			}
			if got := len(s.requested()); got != MaxRedirects+1 {
				t.Errorf("%d requests, want 6: the first and the redirects that were followed", got)
			}
		}
	})
	t.Run("another site", func(t *testing.T) {
		var hits atomic.Int32
		other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hits.Add(1)
			writeBody(w, http.StatusOK, `{"namespaces":[]}`)
		}))
		defer other.Close()
		for _, to := range []string{other.URL + "/wp-json/", other.URL, "http://localhost:1/", "//" + strings.TrimPrefix(other.URL, "http://") + "/x"} {
			s := newSite(t).standard()
			s.setBefore(func(w http.ResponseWriter, r *http.Request) bool {
				if r.URL.Path == "/wp-json/" {
					http.Redirect(w, r, to, http.StatusFound)
					return true
				}
				return false
			})
			_, err := fetch(t, s, Options{})
			target := to
			if strings.HasPrefix(to, "//") {
				target = "http:" + to
			}
			if err == nil || !strings.Contains(err.Error(), "redirected to "+target+", which is not on http://"+strings.TrimPrefix(s.srv.URL, "http://")+"; read the REST API there") {
				t.Errorf("redirect to %s: Fetch() = %v, want the error that the redirect leaves the site", to, err)
			}
			if err != nil && !strings.HasPrefix(err.Error(), "GET "+s.URL()+"/wp-json/?_fields=") {
				t.Errorf("redirect to %s: Fetch() = %v, want the request in the error", to, err)
			}
		}
		if hits.Load() != 0 {
			t.Errorf("the other site got %d requests, want none", hits.Load())
		}
	})
	t.Run("a site with the same name", func(t *testing.T) {
		// 127.0.0.1 and localhost are the same machine, and different names: the site is the name.
		s := newSite(t).standard()
		port := strings.TrimPrefix(s.srv.URL, "http://127.0.0.1")
		s.setBefore(func(w http.ResponseWriter, r *http.Request) bool {
			if r.URL.Path == "/wp-json/" {
				http.Redirect(w, r, "http://localhost"+port+"/wp-json/", http.StatusFound)
				return true
			}
			return false
		})
		_, err := fetch(t, s, Options{})
		if err == nil || !strings.Contains(err.Error(), "redirected to http://localhost"+port+"/wp-json/, which is not on "+s.srv.URL+"; read") {
			t.Errorf("Fetch() = %v, want that a redirect to another name is not followed", err)
		}
	})
	t.Run("credentials in the URL", func(t *testing.T) {
		s := newSite(t).standard()
		s.setBefore(func(w http.ResponseWriter, r *http.Request) bool {
			if r.URL.Path == "/wp-json/" && r.URL.User == nil {
				http.Redirect(w, r, "http://user:secret@"+r.Host+"/wp-json/?x=1", http.StatusFound)
				return true
			}
			return false
		})
		_, err := fetch(t, s, Options{})
		if err == nil || !strings.Contains(err.Error(), "redirected to http://user:xxxxx@"+strings.TrimPrefix(s.srv.URL, "http://")+"/wp-json/?x=1, which has credentials in it") ||
			strings.Contains(err.Error(), "secret") {
			t.Errorf("Fetch() = %v, want that the redirect with credentials is not followed, and the password is not in the error", err)
		}
	})
}

func TestSameSite(t *testing.T) {
	for _, tt := range []struct {
		from, to string
		want     bool
	}{
		{"http://example.com/wp-json/", "http://example.com/wp-json", true},
		{"http://example.com/", "http://EXAMPLE.com/x", true},
		{"https://example.com/", "https://example.com/x?y=1", true},
		{"http://example.com/", "http://example.com:80/", true},
		{"https://example.com:443/", "https://example.com/", true},
		{"http://example.com:8080/", "http://example.com:8080/x", true},
		{"http://example.com/", "https://example.com/", true},
		{"http://example.com/", "https://example.com:443/", true},
		{"http://example.com:80/", "https://example.com/", true},
		{"http://[::1]:8080/", "http://[::1]:8080/x", true},
		{"https://example.com/", "http://example.com/", false},
		{"http://example.com/", "https://example.com:8443/", false},
		{"http://example.com:8080/", "https://example.com/", true},
		{"http://example.com:8080/", "http://example.com/", false},
		{"http://example.com/", "http://example.com:8080/", false},
		{"https://example.com/", "https://example.com:8443/", false},
		{"http://example.com/", "http://www.example.com/", false},
		{"http://www.example.com/", "http://example.com/", false},
		{"http://example.com/", "http://example.org/", false},
		{"http://example.com/", "http://example.com.evil.test/", false},
		{"http://example.com/", "http://127.0.0.1/", false},
		{"http://example.com/", "http://localhost/", false},
		{"http://[::1]:8080/", "http://[::2]:8080/", false},
		{"http://example.com/", "ftp://example.com/", false},
	} {
		from, err1 := url.Parse(tt.from)
		to, err2 := url.Parse(tt.to)
		if err1 != nil || err2 != nil {
			t.Fatal(err1, err2)
		}
		if got := sameSite(from, to); got != tt.want {
			t.Errorf("sameSite(%s, %s) = %v, want %v", tt.from, tt.to, got, tt.want)
		}
	}
}

func TestRouteURL(t *testing.T) {
	q := url.Values{"page": {"2"}, "per_page": {"100"}, "_fields": {"id,name"}}
	for _, tt := range []struct{ root, route, want string }{
		{"https://example.com/wp-json/", "/", "https://example.com/wp-json/?_fields=id%2Cname&page=2&per_page=100"},
		{"https://example.com/wp-json", "/", "https://example.com/wp-json/?_fields=id%2Cname&page=2&per_page=100"},
		{"https://example.com/wp-json/", "/wp/v2/users", "https://example.com/wp-json/wp/v2/users?_fields=id%2Cname&page=2&per_page=100"},
		{"https://example.com/wp-json", "/wp/v2/users", "https://example.com/wp-json/wp/v2/users?_fields=id%2Cname&page=2&per_page=100"},
		{"https://example.com/blog/wp-json/", "/wp/v2/users", "https://example.com/blog/wp-json/wp/v2/users?_fields=id%2Cname&page=2&per_page=100"},
		{"https://example.com:8443/wp-json/", "/wp/v2/users", "https://example.com:8443/wp-json/wp/v2/users?_fields=id%2Cname&page=2&per_page=100"},
		{"https://example.com/%E3%83%96/wp-json/", "/wp/v2/users", "https://example.com/%E3%83%96/wp-json/wp/v2/users?_fields=id%2Cname&page=2&per_page=100"},
		{"https://example.com/a%2Fb/wp-json/", "/wp/v2/users", "https://example.com/a%2Fb/wp-json/wp/v2/users?_fields=id%2Cname&page=2&per_page=100"},
		{"https://example.com/?rest_route=/", "/", "https://example.com/?_fields=id%2Cname&page=2&per_page=100&rest_route=%2F"},
		{"https://example.com/?rest_route=/", "/wp/v2/users", "https://example.com/?_fields=id%2Cname&page=2&per_page=100&rest_route=%2Fwp%2Fv2%2Fusers"},
		{"https://example.com/?rest_route=", "/wp/v2/users", "https://example.com/?_fields=id%2Cname&page=2&per_page=100&rest_route=%2Fwp%2Fv2%2Fusers"},
		{"https://example.com/blog/?rest_route=/", "/wp/v2/users", "https://example.com/blog/?_fields=id%2Cname&page=2&per_page=100&rest_route=%2Fwp%2Fv2%2Fusers"},
		{"https://example.com/index.php?rest_route=/", "/wp/v2/users", "https://example.com/index.php?_fields=id%2Cname&page=2&per_page=100&rest_route=%2Fwp%2Fv2%2Fusers"},
	} {
		root, err := url.Parse(tt.root)
		if err != nil {
			t.Fatal(err)
		}
		f := &fetcher{root: root}
		if got := f.routeURL(tt.route, q); got != tt.want {
			t.Errorf("routeURL(%s, %s) =\n%s\nwant\n%s", tt.root, tt.route, got, tt.want)
		}
	}
	if len(q) != 3 {
		t.Errorf("routeURL changed the query: %v", q)
	}
}

func TestRestRoute(t *testing.T) {
	for _, tt := range []struct {
		slug, namespace, base string
		want                  string
	}{
		{"category", "wp/v2", "categories", "/wp/v2/categories"},
		{"category", "", "categories", "/wp/v2/categories"},
		{"genre", "", "", "/wp/v2/genre"},
		{"genre", "my-plugin/v1", "genres", "/my-plugin/v1/genres"},
		{"genre", "a.b/c_d", "e-f/g", "/a.b/c_d/e-f/g"},
		{"genre", "", "..", ""},
		{"genre", "", "../x", ""},
		{"genre", "", "x/..", ""},
		{"genre", "", "x//y", ""},
		{"genre", "", "/x", ""},
		{"genre", "", "x/", ""},
		{"genre", "", ".", ""},
		{"genre", "", "x y", ""},
		{"genre", "", "x%2Fy", ""},
		{"genre", "", "x?y", ""},
		{"genre", "", "x#y", ""},
		{"genre", "", "x:y", ""},
		{"genre", "", "x@y", ""},
		{"genre", "", "x\\y", ""},
		{"genre", "", "x\ny", ""},
		{"genre", "", "ジャンル", ""},
		{"ジャンル", "", "", ""},
		{"genre", "..", "x", ""},
		{"genre", "wp/v2/..", "x", ""},
		{"genre", "http://evil.test", "x", ""},
		{"genre", "//evil.test", "x", ""},
	} {
		got, ok := restRoute(tt.slug, tt.namespace, tt.base)
		if got != tt.want || ok != (tt.want != "") {
			t.Errorf("restRoute(%q, %q, %q) = %q, %v, want %q", tt.slug, tt.namespace, tt.base, got, ok, tt.want)
		}
	}
}

func TestParseBase(t *testing.T) {
	for _, tt := range []struct {
		raw  string
		api  bool
		want bool
	}{
		{"https://example.com", false, true},
		{"http://example.com/blog/", false, true},
		{"https://example.com/wp-json/", false, true},
		{"https://example.com/wp-json/", true, true},
		{"https://example.com/?rest_route=/", true, true},
		{"https://example.com/?rest_route=/", false, false},
		{"https://example.com/?rest_route=/&x=1", true, false},
		{"https://example.com/?x=1", true, false},
		{"https://example.com/?", true, false},
		{"https://example.com/#x", true, false},
		{"https://user@example.com/", true, false},
		{"ftp://example.com/", true, false},
		{"/wp-json/", true, false},
	} {
		if _, err := parseBase(tt.raw, tt.api); (err == nil) != tt.want {
			t.Errorf("parseBase(%q, %v) = %v, want ok: %v", tt.raw, tt.api, err, tt.want)
		}
	}
}

func TestErrorCode(t *testing.T) {
	for _, tt := range []struct{ body, want string }{
		{`{"code":"rest_forbidden","message":"Sorry","data":{"status":403}}`, "rest_forbidden"},
		{`{"message":"Sorry","code":"rest_forbidden"}`, "rest_forbidden"},
		{`{"code":"a.b-c_D9"}`, "a.b-c_D9"},
		{`{"code":""}`, ""},
		{`{"code":null}`, ""},
		{`{"code":403}`, ""},
		{`{"code":["a"]}`, ""},
		{`{"Code":"x"}`, ""},
		{`{"code":"a b"}`, ""},
		{`{"code":"a/b"}`, ""},
		{`{"code":"<script>"}`, ""},
		{`{"code":"é"}`, ""},
		{`{"code":"a","code":"b"}`, ""},
		{`{"code":"` + strings.Repeat("a", 100) + `"}`, strings.Repeat("a", 100)},
		{`{"code":"` + strings.Repeat("a", 101) + `"}`, ""},
		{`["rest_forbidden"]`, ""},
		{`"rest_forbidden"`, ""},
		{`{"code":"x"`, ""},
		{``, ""},
		{`not JSON`, ""},
	} {
		if got := errorCode([]byte(tt.body)); got != tt.want {
			t.Errorf("errorCode(%s) = %q, want %q", tt.body, got, tt.want)
		}
	}
}

func TestResponseBlocked(t *testing.T) {
	for _, tt := range []struct {
		status    int
		mediaType string
		want      bool
	}{
		{200, "application/json", false},
		{200, "text/html", true},
		{200, "", true},
		{204, "", true},
		{201, "application/json", false},
		{301, "", false},
		{304, "", false},
		{400, "application/json", true},
		{401, "", true},
		{403, "text/html", true},
		{404, "text/html", true},
		{407, "", true},
		{408, "application/json", false},
		{410, "", true},
		{429, "application/json", false},
		{451, "", true},
		{499, "", true},
		{500, "application/json", false},
		{503, "text/html", false},
		{100, "", false},
	} {
		r := &response{status: tt.status, mediaType: tt.mediaType}
		if got := r.blocked(); got != tt.want {
			t.Errorf("blocked() of %d %q = %v, want %v", tt.status, tt.mediaType, got, tt.want)
		}
	}
}

func TestFetchConcurrentSafe(t *testing.T) {
	// Fetch uses nothing that is shared, so it can be called at the same time for different sites.
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			s := newSite(t).standard()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if _, err := Fetch(ctx, s.URL(), Options{}); err != nil {
				t.Errorf("Fetch() = %v", err)
			}
		})
	}
	wg.Wait()
}
