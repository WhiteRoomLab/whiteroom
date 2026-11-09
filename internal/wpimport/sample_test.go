package wpimport

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/wprest"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata/out and testdata/out-blocks")

// sampleOut holds the golden files of TestImportSample, which go test -update writes: settings.json and propose.txt, the
// settings that the proposal gives and its report; and what the import makes of them, which is site/settings.json,
// site/schema.yaml, terms/<ID>.json, authors/<ID>.json and import.txt, the report of the import.
const sampleOut = "testdata/out"

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// sampleDefs is the schema file of the developer of the sample site.
func sampleDefs(t *testing.T) *content.Site {
	t.Helper()
	defs := new(content.Site)
	if err := defs.UnmarshalSchema(readTestdata(t, "schema.yaml")); err != nil {
		t.Fatal(err)
	}
	return defs
}

func sampleREST(t *testing.T) *wprest.Snapshot {
	t.Helper()
	snap, err := wprest.ReadSnapshot(bytes.NewReader(readTestdata(t, "site-rest.json")), wprest.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

// sampleImport reads the WXR of the sample site into an import with a report of its own, with the REST API if rest.
func sampleImport(t *testing.T, rest bool) *Import {
	t.Helper()
	e, rep, err := Read(context.Background(), "site.xml", bytes.NewReader(readTestdata(t, "site.xml")))
	if err != nil {
		t.Fatal(err)
	}
	imp := New("site.xml", e, rep)
	if rest {
		imp.REST, imp.RESTFile = sampleREST(t), "site-rest.json"
	}
	return imp
}

// assignSample gives the front page, the page of the posts, the logo and the icon of the sample site the IDs that the
// conversion of the entries and of the media will give them, and records them as it will.
func assignSample(t *testing.T, imp *Import) {
	t.Helper()
	assign := func(n int64, kind content.RefKind, ref content.Ref) {
		it := imp.WXR.Post(n)
		if it == nil {
			t.Fatalf("the sample has no item %d", n)
		}
		id, err := imp.IDs.Assign(PostObject(it), kind)
		if err != nil {
			t.Fatal(err)
		}
		imp.AddRef(kind, id, ref)
	}
	for _, n := range []int64{30, 31} {
		assign(n, content.RefEntry, content.Ref{Type: content.TypePage, Language: "ja"})
	}
	for _, n := range []int64{50, 51} {
		assign(n, content.RefMedia, content.Ref{Type: content.MediaImage})
	}
}

func reportText(t *testing.T, rep *Report) string {
	t.Helper()
	var b bytes.Buffer
	if err := rep.WriteText(&b); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// TestImportSample proposes the settings of the sample site, imports what the settings make of the site and its terms and
// authors, and compares the files with the golden files in testdata/out.
func TestImportSample(t *testing.T) {
	golden := make(map[string]string)
	defs := sampleDefs(t)

	// The proposal, written as the file to correct and read back.
	imp := sampleImport(t, true)
	proposed, err := imp.ProposeSettings(defs)
	if err != nil {
		t.Fatal(err)
	}
	var file bytes.Buffer
	if err := proposed.WriteJSON(&file); err != nil {
		t.Fatal(err)
	}
	golden["settings.json"] = file.String()
	golden["propose.txt"] = reportText(t, imp.Report)
	settings, err := ReadSettings("settings.json", file.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(settings, proposed) {
		t.Errorf("ReadSettings gave %+v, want %+v", settings, proposed)
	}

	// The import, with another import and another report, as the two runs of the command will have them.
	imp = sampleImport(t, true)
	assignSample(t, imp)
	site, err := imp.Site(settings, defs)
	if err != nil {
		t.Fatal(err)
	}
	settingsDoc, err := site.MarshalSettings()
	if err != nil {
		t.Fatal(err)
	}
	schemaDoc, err := site.MarshalSchema()
	if err != nil {
		t.Fatal(err)
	}
	golden["site/settings.json"] = string(settingsDoc) + "\n"
	golden["site/schema.yaml"] = string(schemaDoc)
	terms, err := imp.Terms(site)
	if err != nil {
		t.Fatal(err)
	}
	authors, err := imp.Authors(site)
	if err != nil {
		t.Fatal(err)
	}
	for _, term := range terms {
		b, err := term.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		golden["terms/"+term.ID+".json"] = string(b) + "\n"
	}
	for _, a := range authors {
		b, err := a.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		golden["authors/"+a.ID+".json"] = string(b) + "\n"
	}
	golden["import.txt"] = reportText(t, imp.Report)

	// The documents are what the site directory will hold: they load, and the terms and the authors read back the same.
	loaded, err := content.LoadSite(content.SiteFile{Name: "site/settings.json", Data: settingsDoc},
		content.SiteFile{Name: "site/schema.yaml", Data: schemaDoc}, imp.Ref)
	if err != nil {
		t.Fatalf("LoadSite = %v", err)
	}
	checkTerms(t, imp, loaded, terms)
	checkAuthors(t, imp, loaded, authors)
	for _, term := range terms {
		var back content.Term
		if err := back.UnmarshalJSON([]byte(golden["terms/"+term.ID+".json"])); err != nil || !reflect.DeepEqual(back, term) {
			t.Errorf("term %s reads back as %+v, %v, want %+v", term.ID, back, err, term)
		}
	}
	for _, a := range authors {
		var back content.AuthorProfile
		if err := back.UnmarshalJSON([]byte(golden["authors/"+a.ID+".json"])); err != nil || !reflect.DeepEqual(back, a) {
			t.Errorf("author %s reads back as %+v, %v, want %+v", a.ID, back, err, a)
		}
	}
	for name, text := range golden {
		if strings.Contains(text, "@") {
			t.Errorf("%s has an @: an email address of the WXR got into the output", name)
		}
	}

	if *update {
		if err := os.RemoveAll(sampleOut); err != nil {
			t.Fatal(err)
		}
	}
	names := slices.Sorted(maps.Keys(golden))
	for _, name := range names {
		checkGolden(t, filepath.Join(sampleOut, filepath.FromSlash(name)), golden[name])
	}
	if got := goldenFiles(t, sampleOut); !slices.Equal(got, names) {
		t.Errorf("%s has %q, want %q", sampleOut, got, names)
	}
}

// goldenFiles lists the files under dir by their paths from it, with / for the separator.
func goldenFiles(t *testing.T, dir string) []string {
	t.Helper()
	var names []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, err := filepath.Rel(dir, path)
			if err != nil {
				return err
			}
			names = append(names, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	slices.Sort(names)
	return names
}

// checkGolden compares got with the golden file, which go test -update writes instead.
func checkGolden(t *testing.T, file, got string) {
	t.Helper()
	if *update {
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(file)
	switch {
	case err != nil:
		t.Errorf("%v; go test -update writes it", err)
	case got != string(want):
		t.Errorf("%s differs from what the import made, %s; go test -update rewrites it", file, firstDiff(got, string(want)))
	}
}

// firstDiff returns the first line in which got and want, two different texts, differ, with its number.
func firstDiff(got, want string) string {
	g, w := strings.SplitAfter(got, "\n"), strings.SplitAfter(want, "\n")
	i := 0
	for i < len(g)-1 && i < len(w)-1 && g[i] == w[i] {
		i++
	}
	return fmt.Sprintf("at line %d: %q, want %q", i+1, g[i], w[i])
}

// TestSampleRESTFile checks that the snapshot of the sample is the file that WriteJSON writes for it.
func TestSampleRESTFile(t *testing.T) {
	var b bytes.Buffer
	if err := sampleREST(t).WriteJSON(&b); err != nil {
		t.Fatal(err)
	}
	if want := string(readTestdata(t, "site-rest.json")); b.String() != want {
		t.Errorf("WriteJSON of the snapshot differs from testdata/site-rest.json, %s", firstDiff(b.String(), want))
	}
}

// TestImportWithoutREST imports the sample site without the REST API: what only it tells is left for the person to enter.
func TestImportWithoutREST(t *testing.T) {
	defs := sampleDefs(t)
	imp := sampleImport(t, false)
	proposed, err := imp.ProposeSettings(defs)
	if err != nil {
		t.Fatal(err)
	}
	want := Settings{
		Site: SiteSettings{
			Name: "見本サイト & ブログ", Tagline: "手で書いた見本のサイトです", URL: "https://example.com", Language: "ja",
			PostsPerPage: 10, FrontPage: 30,
		},
		URLs: URLSettings{
			Post: "/{yyyy}/{mm}/{dd}/{slug}/", Page: "/{path}/", Author: "/author/{slug}/",
			Taxonomies: map[string]string{"category": "/category/{path}/", "genre": "/genre/{slug}/", "tag": "/tag/{slug}/"},
			Types:      map[string]TypeURLs{"works": {URL: "/works/{slug}/"}},
		},
		Blocks: emptyBlockSettings(),
	}
	if !reflect.DeepEqual(*proposed, want) {
		t.Errorf("ProposeSettings = %+v, want %+v", *proposed, want)
	}
	var entries []string
	for _, e := range imp.Report.Entries() {
		if (e.Target.File == "site/settings.json" || e.Target.File == "site/schema.yaml") && e.Message != postsPerPageReview {
			entries = append(entries, fmt.Sprintf("%s: %s -> %s#%s", e.Kind, e.Message, e.Target.File, e.Target.Pointer))
		}
	}
	const missing = `review: the REST API did not give the links of %s; the URL rule is %q, the default of WordPress for the front "/" -> site/schema.yaml#%s`
	sameSet(t, "the entries about the settings and the rules", entries, []string{
		"review: the REST API did not give the time zone; enter site.timeZone -> site/settings.json#/timeZone",
		"review: the front page is page 30, whose link is the home page; set site.postsPage if the site had a page of posts -> site/settings.json#/frontPage",
		"review: set site.logo and site.icon to attachments of the WXR, if the site had them -> site/settings.json#/logo",
		fmt.Sprintf(missing, "the taxonomy category", "/category/{path}/", "/taxonomies/category/url"),
		fmt.Sprintf(missing, "the taxonomy tag", "/tag/{slug}/", "/taxonomies/tag/url"),
		fmt.Sprintf(missing, "the taxonomy genre", "/genre/{slug}/", "/taxonomies/genre/url"),
		fmt.Sprintf(missing, "author pages", "/author/{slug}/", "/authorUrl"),
		"review: the REST API did not tell whether the post type works had an archive; set its archive if it had one -> site/schema.yaml#/types/works/archive",
		`review: inferred the permalink structure "/%pagename%/" of pages from 3 of 3 links; the URL rule is "/{path}/" -> site/schema.yaml#/types/page/url`,
		`review: inferred the permalink structure "/%year%/%monthnum%/%day%/%postname%/" of posts from 3 of 3 links; the URL rule is "/{yyyy}/{mm}/{dd}/{slug}/" -> site/schema.yaml#/types/post/url`,
		`review: inferred the permalink structure "/works/%works%/" of the post type works from 1 of 1 link; the URL rule is "/works/{slug}/" -> site/schema.yaml#/types/works/url`,
	})

	// The time zone is left empty, so the settings are not complete until it is entered.
	imp = sampleImport(t, false)
	assignSample(t, imp)
	_, err = imp.Site(proposed, defs)
	var errs content.Errors
	if !errors.As(err, &errs) || len(errs) != 1 || errs[0].Path != "/site/timeZone" || errs[0].Line != 0 || errs[0].Column != 0 {
		t.Fatalf("Site = %v, want an error at /site/timeZone with no place", err)
	}
	proposed.Site.TimeZone = "Asia/Tokyo"
	site, err := imp.Site(proposed, defs)
	if err != nil {
		t.Fatalf("Site with the time zone = %v", err)
	}
	if got := site.FrontPage["ja"]; got == "" || site.PostsPage != nil || site.Logo != "" || site.Icon != "" {
		t.Errorf("the front page, the page of the posts, the logo and the icon are %v, %v, %q, %q", site.FrontPage, site.PostsPage, site.Logo, site.Icon)
	}

	// Without the REST API the authors are known by their logins only.
	authors, err := imp.Authors(site)
	if err != nil {
		t.Fatal(err)
	}
	checkAuthors(t, imp, site, authors)
	for _, login := range []string{"editor", "writer"} {
		if got := profileOf(t, imp, authors, login).Slug; got != login {
			t.Errorf("the slug of %s is %q, want the login", login, got)
		}
	}
}

// restServer serves the REST API of a WordPress as snap describes it, with the pages of two items, so that Fetch has to
// ask for more than one page. The requests are recorded in requests.
type restServer struct {
	*httptest.Server
	snap *wprest.Snapshot

	mu       sync.Mutex
	requests []string
}

const restPageSize = 2

func newRESTServer(t *testing.T, snap *wprest.Snapshot) *restServer {
	t.Helper()
	s := &restServer{snap: snap}
	s.Server = httptest.NewServer(s)
	t.Cleanup(s.Close)
	return s
}

func (s *restServer) log(r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := r.URL.Path
	if p := r.URL.Query().Get("page"); p != "" {
		entry += "?page=" + p
	}
	s.requests = append(s.requests, entry)
}

func (s *restServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.log(r)
	route, ok := strings.CutPrefix(r.URL.Path, "/wp-json")
	if r.Method != http.MethodGet || !ok {
		http.NotFound(w, r)
		return
	}
	switch route {
	case "/", "":
		writeJSONResponse(w, s.index())
	case "/wp/v2/taxonomies":
		writeJSONResponse(w, s.taxonomies())
	case "/wp/v2/types":
		writeJSONResponse(w, s.types())
	case "/wp/v2/users":
		var items []any
		for _, u := range s.snap.Users {
			items = append(items, map[string]any{
				"id": u.ID, "name": u.Name, "url": u.URL, "description": u.Description, "link": u.Link, "slug": u.Slug,
				"avatar_urls": map[string]any{"24": "https://secure.gravatar.com/avatar/x?s=24"}, "meta": []any{},
				"_links": map[string]any{"self": []any{map[string]any{"href": "https://example.com/wp-json/wp/v2/users/" + strconv.FormatInt(u.ID, 10)}}},
			})
		}
		s.page(w, r, items)
	default:
		for _, tx := range s.snap.Taxonomies {
			if route != "/wp/v2/"+tx.RestBase {
				continue
			}
			var items []any
			for _, term := range s.snap.Terms {
				if term.Taxonomy != tx.Slug {
					continue
				}
				items = append(items, map[string]any{
					"id": term.ID, "count": 1, "description": "", "link": term.Link, "name": term.Name, "slug": term.Slug,
					"taxonomy": term.Taxonomy, "parent": term.Parent, "meta": []any{}, "_links": map[string]any{},
				})
			}
			s.page(w, r, items)
			return
		}
		writeJSONResponse(w, map[string]any{"code": "rest_no_route", "message": "no route", "data": map[string]any{"status": 404}})
	}
}

func (s *restServer) index() map[string]any {
	ix := s.snap.Index
	offset, _ := strconv.ParseFloat(ix.GMTOffset, 64)
	return map[string]any{
		"name": ix.Name, "description": ix.Description, "url": ix.URL, "home": ix.Home, "gmt_offset": offset,
		"timezone_string": ix.TimezoneString, "namespaces": []any{"oembed/1.0", "wp/v2"}, "authentication": []any{},
		"show_on_front": ix.ShowOnFront, "page_on_front": ix.PageOnFront, "page_for_posts": ix.PageForPosts,
		"site_logo": ix.SiteLogo, "site_icon": ix.SiteIcon, "_links": map[string]any{},
	}
}

func (s *restServer) taxonomies() map[string]any {
	out := make(map[string]any)
	for _, tx := range s.snap.Taxonomies {
		out[tx.Slug] = map[string]any{
			"name": tx.Name, "slug": tx.Slug, "description": "", "hierarchical": tx.Hierarchical, "types": tx.Types,
			"rest_base": tx.RestBase, "rest_namespace": tx.RestNamespace,
		}
	}
	return out
}

func (s *restServer) types() map[string]any {
	out := make(map[string]any)
	for _, ty := range s.snap.Types {
		var archive any = ty.HasArchive.On
		if ty.HasArchive.Slug != "" {
			archive = ty.HasArchive.Slug
		}
		taxonomies := ty.Taxonomies
		if taxonomies == nil {
			taxonomies = []string{}
		}
		out[ty.Slug] = map[string]any{
			"name": ty.Name, "slug": ty.Slug, "description": "", "hierarchical": ty.Hierarchical, "has_archive": archive,
			"rest_base": ty.RestBase, "rest_namespace": ty.RestNamespace, "taxonomies": taxonomies,
		}
	}
	return out
}

// page writes the page of items that the request asks for, with the headers that give the number of pages. The site
// gives pages of restPageSize items, whatever per_page is.
func (s *restServer) page(w http.ResponseWriter, r *http.Request, items []any) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	page = max(page, 1)
	pages := (len(items) + restPageSize - 1) / restPageSize
	w.Header().Set("X-WP-Total", strconv.Itoa(len(items)))
	w.Header().Set("X-WP-TotalPages", strconv.Itoa(pages))
	if page > pages {
		w.WriteHeader(http.StatusBadRequest)
		writeJSONResponse(w, map[string]any{"code": "rest_post_invalid_page_number", "message": "no such page", "data": map[string]any{"status": 400}})
		return
	}
	writeJSONResponse(w, items[(page-1)*restPageSize:min(page*restPageSize, len(items))])
}

func writeJSONResponse(w http.ResponseWriter, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	_, _ = w.Write(b)
}

// TestImportFromServer reads the REST API from a server that answers as a WordPress does, and checks that the snapshot is
// that of testdata/site-rest.json, and that the settings are the same as those from the file.
func TestImportFromServer(t *testing.T) {
	want := sampleREST(t)
	srv := newRESTServer(t, want)
	got, err := wprest.Fetch(context.Background(), srv.URL, wprest.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got.API != srv.URL+"/wp-json/" {
		t.Errorf("API = %q, want %q", got.API, srv.URL+"/wp-json/")
	}
	got.API = want.API
	if !reflect.DeepEqual(got, want) {
		var a, b bytes.Buffer
		_ = got.WriteJSON(&a)
		_ = want.WriteJSON(&b)
		t.Errorf("Fetch gave a snapshot that differs from testdata/site-rest.json, %s", firstDiff(a.String(), b.String()))
	}

	srv.mu.Lock()
	requests := slices.Clone(srv.requests)
	srv.mu.Unlock()
	for _, r := range []string{
		"/wp-json/wp/v2/categories?page=1", "/wp-json/wp/v2/categories?page=2", "/wp-json/wp/v2/tags?page=2",
		"/wp-json/wp/v2/genres?page=1", "/wp-json/wp/v2/users?page=2",
	} {
		if !slices.Contains(requests, r) {
			t.Errorf("the server was not asked for %s; the requests were %q", r, requests)
		}
	}
	if slices.Contains(requests, "/wp-json/wp/v2/genres?page=2") {
		t.Errorf("the server was asked for a page of the genres after the last; the requests were %q", requests)
	}

	// The same settings come from the snapshot of the server as from the file.
	defs := sampleDefs(t)
	fromFile := sampleImport(t, true)
	wantSettings, err := fromFile.ProposeSettings(defs)
	if err != nil {
		t.Fatal(err)
	}
	fromServer := sampleImport(t, false)
	fromServer.REST, fromServer.RESTFile = got, got.API
	gotSettings, err := fromServer.ProposeSettings(defs)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotSettings, wantSettings) {
		t.Errorf("ProposeSettings from the server = %+v, want %+v", gotSettings, wantSettings)
	}
	if a, b := reportText(t, fromServer.Report), reportText(t, fromFile.Report); a != b {
		t.Errorf("the report from the server differs from that from the file, %s", firstDiff(a, b))
	}
}
