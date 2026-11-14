package wpimport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/wprest"
)

// siteItem is an item to write into a WXR, with the link that the settings are guessed from.
type siteItem struct {
	id          int
	typ, status string // "" for page and publish
	link        string
}

func (x siteItem) xml() string {
	typ, status := x.typ, x.status
	if typ == "" {
		typ = "page"
	}
	if status == "" {
		status = "publish"
	}
	return fmt.Sprintf("<item>\n<title><![CDATA[Item %d]]></title>\n<link>%s</link>\n<wp:post_id>%d</wp:post_id>\n"+
		"<wp:status><![CDATA[%s]]></wp:status>\n<wp:post_type><![CDATA[%s]]></wp:post_type>\n</item>\n", x.id, x.link, x.id, status, typ)
}

// siteWXR writes a WXR 1.2 of the site at link, with the language, which is left out if it is "", and the items.
func siteWXR(link, language string, items ...siteItem) string {
	var b strings.Builder
	b.WriteString(head)
	b.WriteString("<title>Site &amp; Co</title>\n<link>" + link + "</link>\n<description>A tagline</description>\n")
	if language != "" {
		b.WriteString("<language>" + language + "</language>\n")
	}
	for _, it := range items {
		b.WriteString(it.xml())
	}
	b.WriteString(tail)
	return b.String()
}

// proposeFrom reads the WXR in and proposes the settings with the REST API that has the index ix, if it is not nil. An
// index with no home is given the link of the channel, as the REST API of the site has.
func proposeFrom(t *testing.T, in string, ix *wprest.Index, defs *content.Site) (*Settings, *Import) {
	t.Helper()
	e, rep := readString(t, "site.xml", in)
	imp := New("site.xml", e, rep)
	if ix != nil {
		snap := &wprest.Snapshot{Index: *ix}
		if snap.Index.Home == "" {
			snap.Index.Home = e.Channel.Link
		}
		imp.REST, imp.RESTFile = snap, "rest.json"
	}
	s, err := imp.ProposeSettings(defs)
	if err != nil {
		t.Fatalf("ProposeSettings = %v", err)
	}
	return s, imp
}

const postsPerPageReview = "WordPress keeps the number of posts on a page and whether search engines may index the site where the " +
	"import cannot read them; check site.postsPerPage (10 by default) and site.noindex"

// settingsEntries lists the entries of the report that go to the settings document, as "kind: source: message ->
// pointer", in the order of the report. The review of the number of posts on a page, which every proposal has, is left
// out.
func settingsEntries(rep *Report) []string {
	var out []string
	for _, e := range rep.Entries() {
		if e.Target.File == "site/settings.json" && e.Message != postsPerPageReview {
			out = append(out, fmt.Sprintf("%s: %s: %s -> %s", e.Kind, e.Source.what(), e.Message, e.Target.Pointer))
		}
	}
	return out
}

func sameStrings(t *testing.T, what string, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("%s is\n%s\nwant\n%s", what, strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// sameSet compares the lines in any order.
func sameSet(t *testing.T, what string, got, want []string) {
	t.Helper()
	sameStrings(t, what, slices.Sorted(slices.Values(got)), slices.Sorted(slices.Values(want)))
}

// errorLines lists the errors in err, which is content.Errors, as "path: message".
func errorLines(t *testing.T, err error) []string {
	t.Helper()
	var errs content.Errors
	if !errors.As(err, &errs) {
		t.Fatalf("the error is %v, not content.Errors", err)
	}
	var out []string
	for _, e := range errs {
		out = append(out, e.Path+": "+e.Msg)
	}
	return out
}

func TestSiteLanguage(t *testing.T) {
	for _, tt := range []struct {
		in, code    string
		dropped, ok bool
	}{
		{"ja", "ja", false, true},
		{"en-US", "en-US", false, true},
		{"en_US", "en-US", false, true},
		{"zh-Hant-TW", "zh-Hant-TW", false, true},
		{"zh_hant_tw", "zh-Hant-TW", false, true},
		{"es-419", "es-419", false, true},
		{"sr-Latn", "sr-Latn", false, true},
		{"fil", "fil", false, true},
		{"EN-us", "en-US", false, true},
		{"de-DE-formal", "de-DE", true, true},
		{"de_DE_formal", "de-DE", true, true},
		{"pt-PT-ao90", "pt-PT", true, true},
		{"art-xemoji", "art", true, true},
		{"de-1996", "de", true, true},
		{"ja-J", "ja", true, true},
		{"", "", false, false},
		{"x", "", false, false},
		{"j1", "", false, false},
		{"japanese", "", false, false},
		{"ja-", "", false, false},
		{"-ja", "", false, false},
		{"ja--JP", "", false, false},
		{"ja@x", "", false, false},
		{"ja JP", "", false, false},
		{"日本語", "", false, false},
	} {
		code, dropped, ok := siteLanguage(tt.in)
		if code != tt.code || dropped != tt.dropped || ok != tt.ok {
			t.Errorf("siteLanguage(%q) = %q, %v, %v, want %q, %v, %v", tt.in, code, dropped, ok, tt.code, tt.dropped, tt.ok)
		}
		if ok {
			if err := content.CheckLanguage(code); err != nil {
				t.Errorf("siteLanguage(%q) = %q, which is not a language code: %v", tt.in, code, err)
			}
		}
	}
}

func TestProposeName(t *testing.T) {
	s, imp := proposeFrom(t, siteWXR("https://example.com", "ja"), nil, nil)
	if s.Site.Name != "Site & Co" || s.Site.Tagline != "A tagline" {
		t.Errorf("the name and the tagline are %q and %q, as the channel has them", s.Site.Name, s.Site.Tagline)
	}
	for _, e := range settingsEntries(imp.Report) {
		if strings.Contains(e, "/name") {
			t.Errorf("the title gives the entry %q", e)
		}
	}

	in := strings.Replace(siteWXR("https://example.com", "ja"), "<title>Site &amp; Co</title>", "<title></title>", 1)
	in = strings.Replace(in, "<description>A tagline</description>", "<description></description>", 1)
	s, imp = proposeFrom(t, in, nil, nil)
	if s.Site.Name != "" || s.Site.Tagline != "" {
		t.Errorf("the name and the tagline are %q and %q, want none", s.Site.Name, s.Site.Tagline)
	}
	want := "review: channel: the site has no title; enter site.name -> /name"
	if got := settingsEntries(imp.Report); !slices.Contains(got, want) {
		t.Errorf("the entries are\n%s\nwant %q among them", strings.Join(got, "\n"), want)
	}
}

func TestProposePostsPerPage(t *testing.T) {
	s, imp := proposeFrom(t, siteWXR("https://example.com", "ja"), nil, nil)
	if s.Site.PostsPerPage != 10 || s.Site.NoIndex {
		t.Errorf("postsPerPage and noindex are %d and %v, want 10 and false", s.Site.PostsPerPage, s.Site.NoIndex)
	}
	var got []string
	for _, e := range imp.Report.Entries() {
		if e.Message == postsPerPageReview {
			got = append(got, fmt.Sprintf("%s: %s -> %s#%s", e.Kind, e.Source.what(), e.Target.File, e.Target.Pointer))
		}
	}
	sameStrings(t, "the entries about the number of posts", got, []string{"review: channel -> site/settings.json#/postsPerPage"})
}

func TestProposeURL(t *testing.T) {
	const unicodeHost = "https://" + "例え" + ".jp"
	for _, tt := range []struct {
		link, url string
		entry     string // the entry about the URL, or ""
	}{
		{"https://example.com", "https://example.com", ""},
		{"https://example.com/", "https://example.com", ""},
		{"https://example.com//", "https://example.com", ""},
		{"https://example.com:443", "https://example.com", ""},
		{"https://example.com:443/", "https://example.com", ""},
		{"http://example.com:80/", "http://example.com", ""},
		{"https://example.com:8443/blog/", "https://example.com:8443/blog", ""},
		{"http://example.com:443", "http://example.com:443", ""},
		{"HTTPS://Example.COM/Blog/", "https://example.com/Blog", ""},
		{"  https://example.com/blog \n", "https://example.com/blog", ""},
		{"https://example.com/%e3%81%82/", "https://example.com/%e3%81%82", ""},
		{
			unicodeHost, unicodeHost,
			"review: channel: the URL " + `"` + unicodeHost + `"` + " cannot be that of the site: the host must be ASCII; write a domain name in the xn-- form; " +
				"correct site.url -> /url",
		},
		{
			"https://example.com/?a=1", "https://example.com/?a=1",
			`review: channel: the URL "https://example.com/?a=1" cannot be that of the site: queries and fragments are not allowed; correct site.url -> /url`,
		},
		{
			"example.com", "example.com",
			`review: channel: the URL "example.com" cannot be that of the site: must be an absolute URL or a path starting with "/"; correct site.url -> /url`,
		},
		{
			"ftp://example.com", "ftp://example.com",
			`review: channel: the URL "ftp://example.com" cannot be that of the site: scheme "ftp" is not allowed; correct site.url -> /url`,
		},
		{
			"https://", "https://",
			`review: channel: the URL "https://" cannot be that of the site: missing host; correct site.url -> /url`,
		},
		{"", "", "review: channel: the WXR has no site address; enter site.url -> /url"},
	} {
		s, imp := proposeFrom(t, siteWXR(tt.link, "ja"), nil, nil)
		if s.Site.URL != tt.url {
			t.Errorf("link %q gives the URL %q, want %q", tt.link, s.Site.URL, tt.url)
		}
		var got string
		for _, e := range settingsEntries(imp.Report) {
			if strings.HasSuffix(e, " -> /url") {
				got = e
			}
		}
		if got != tt.entry {
			t.Errorf("link %q gives the entry %q, want %q", tt.link, got, tt.entry)
		}
		// What is proposed is what the site settings accept, or else it has an entry.
		if reason := settingProblem(&content.Site{URL: s.Site.URL}, "/url"); tt.entry == "" && reason != "" {
			t.Errorf("link %q gives the URL %q, which the site settings refuse: %s", tt.link, s.Site.URL, reason)
		}
	}
}

func TestTidySiteURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://example.com":             "https://example.com",
		"https://example.com/":            "https://example.com",
		"https://example.com//":           "https://example.com",
		"https://example.com/blog/":       "https://example.com/blog",
		"https://example.com:443":         "https://example.com",
		"https://example.com:443/blog":    "https://example.com/blog",
		"http://example.com:80":           "http://example.com",
		"http://example.com:80/a:80":      "http://example.com/a:80",
		"https://example.com:80":          "https://example.com:80",
		"http://example.com:443":          "http://example.com:443",
		"https://example.com:8443":        "https://example.com:8443",
		"https://example.com:1443":        "https://example.com:1443",
		"https://[::1]:443/":              "https://[::1]",
		"https://user@example.com:443":    "https://user@example.com",
		"https://example.com/path?x=:443": "https://example.com/path?x=:443",
		"https://":                        "https://",
		"https:///":                       "https://",
		"no scheme:443":                   "no scheme:443",
		"no scheme/":                      "no scheme",
		"":                                "",
	} {
		if got := tidySiteURL(in); got != want {
			t.Errorf("tidySiteURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestProposeLanguage(t *testing.T) {
	for _, tt := range []struct {
		language, code string
		entry          string
	}{
		{"ja", "ja", ""},
		{"en_US", "en-US", ""},
		{" EN-us ", "en-US", ""},
		{"zh-Hant-TW", "zh-Hant-TW", ""},
		{"de-DE-formal", "de-DE", `review: channel: the language "de-DE-formal" is "de-DE" here; whiteroom has no variants of languages -> /defaultLanguage`},
		{"pt_PT_ao90", "pt-PT", `review: channel: the language "pt_PT_ao90" is "pt-PT" here; whiteroom has no variants of languages -> /defaultLanguage`},
		{"x", "", `review: channel: the language "x" is not a language code; enter site.language -> /defaultLanguage`},
		{"", "", "review: channel: the WXR does not give the language; enter site.language -> /defaultLanguage"},
	} {
		s, imp := proposeFrom(t, siteWXR("https://example.com", tt.language), nil, nil)
		if s.Site.Language != tt.code {
			t.Errorf("language %q gives %q, want %q", tt.language, s.Site.Language, tt.code)
		}
		var want []string
		if tt.entry != "" {
			want = append(want, tt.entry)
		}
		var got []string
		for _, e := range settingsEntries(imp.Report) {
			if strings.HasSuffix(e, " -> /defaultLanguage") {
				got = append(got, e)
			}
		}
		sameStrings(t, fmt.Sprintf("the entries for the language %q", tt.language), got, want)
	}
}

func TestProposeTimeZone(t *testing.T) {
	const notWhole = "review: channel: the time zone of WordPress is the offset %q, which is not a whole number of hours; enter site.timeZone -> /timeZone"
	const outOfRange = "review: channel: the time zone of WordPress is the offset %q, which is out of range (-12 to 14); enter site.timeZone -> /timeZone"
	const forged = "9\nsite.xml:1:1: review: channel: FORGED LINE"
	const rejected = "review: channel: the time zone of WordPress cannot be that of the site: unknown time zone %q: " +
		`use an IANA name such as "Asia/Tokyo" or "UTC"; enter site.timeZone -> /timeZone`
	const none = "review: channel: the REST API did not give the time zone; enter site.timeZone -> /timeZone"
	for _, tt := range []struct {
		ix       *wprest.Index
		zone     string
		entry    string
		testName string
	}{
		{&wprest.Index{TimezoneString: "Asia/Tokyo", GMTOffset: "9"}, "Asia/Tokyo", "", "name"},
		{&wprest.Index{TimezoneString: " America/New_York "}, "America/New_York", "", "name with spaces"},
		{&wprest.Index{GMTOffset: "9"}, "Etc/GMT-9", "", "offset 9"},
		{&wprest.Index{GMTOffset: "9.0"}, "Etc/GMT-9", "", "offset 9.0"},
		{&wprest.Index{GMTOffset: "+9"}, "Etc/GMT-9", "", "offset +9"},
		{&wprest.Index{GMTOffset: "0"}, "UTC", "", "offset 0"},
		{&wprest.Index{GMTOffset: "-0"}, "UTC", "", "offset -0"},
		{&wprest.Index{GMTOffset: "-5"}, "Etc/GMT+5", "", "offset -5"},
		{&wprest.Index{GMTOffset: "14"}, "Etc/GMT-14", "", "offset 14"},
		{&wprest.Index{GMTOffset: "-12"}, "Etc/GMT+12", "", "offset -12"},
		{&wprest.Index{GMTOffset: "5.5"}, "", fmt.Sprintf(notWhole, "5.5"), "offset 5.5"},
		{&wprest.Index{GMTOffset: "-3.5"}, "", fmt.Sprintf(notWhole, "-3.5"), "offset -3.5"},
		{&wprest.Index{GMTOffset: "abc"}, "", fmt.Sprintf(notWhole, "abc"), "offset abc"},
		{&wprest.Index{GMTOffset: "NaN"}, "", fmt.Sprintf(notWhole, "NaN"), "offset NaN"},
		{&wprest.Index{GMTOffset: forged}, "", fmt.Sprintf(notWhole, forged), "offset with a line break"},
		{&wprest.Index{GMTOffset: "15\x1b[31m"}, "", fmt.Sprintf(notWhole, "15\x1b[31m"), "offset with an escape"},
		{&wprest.Index{GMTOffset: "15"}, "", fmt.Sprintf(outOfRange, "15"), "offset 15"},
		{&wprest.Index{GMTOffset: "-13"}, "", fmt.Sprintf(outOfRange, "-13"), "offset -13"},
		{&wprest.Index{TimezoneString: "Local"}, "", fmt.Sprintf(rejected, "Local"), "Local"},
		{&wprest.Index{TimezoneString: "Not/AZone", GMTOffset: "9"}, "", fmt.Sprintf(rejected, "Not/AZone"), "unknown name with an offset"},
		{&wprest.Index{}, "", none, "nothing"},
		{nil, "", none, "no REST API"},
	} {
		s, imp := proposeFrom(t, siteWXR("https://example.com", "ja"), tt.ix, nil)
		if s.Site.TimeZone != tt.zone {
			t.Errorf("%s: the time zone is %q, want %q", tt.testName, s.Site.TimeZone, tt.zone)
		}
		if lines, entries := strings.Count(reportText(t, imp.Report), "\n"), len(imp.Report.Entries()); lines != entries {
			t.Errorf("%s: %d lines for %d entries", tt.testName, lines, entries)
		}
		var got []string
		for _, e := range settingsEntries(imp.Report) {
			if strings.HasSuffix(e, " -> /timeZone") {
				got = append(got, e)
			}
		}
		var want []string
		if tt.entry != "" {
			want = append(want, tt.entry)
		}
		sameStrings(t, tt.testName+": the entries about the time zone", got, want)
		if reason := settingProblem(&content.Site{TimeZone: tt.zone}, "/timeZone"); tt.zone != "" && reason != "" {
			t.Errorf("%s: the time zone %q is refused: %s", tt.testName, tt.zone, reason)
		}
	}
}

func TestProposeFrontPage(t *testing.T) {
	pages := func(extra ...siteItem) []siteItem {
		return append([]siteItem{
			{id: 30, link: "https://example.com/"},
			{id: 31, link: "https://example.com/news/"},
			{id: 40, typ: "post", link: "https://example.com/2026/10/05/a/"},
		}, extra...)
	}
	const (
		skippedFront = "skipped: channel: the front page, page %d, is not in the WXR -> /frontPage"
		skippedPosts = "skipped: channel: the posts page, page %d, is not in the WXR -> /postsPage"
		noFront      = "review: channel: page %d was the page of the latest posts without a front page; it is imported as an ordinary page -> /postsPage"
		same         = "review: channel: page %d is both the front page and the page of the latest posts; it is the front page here -> /frontPage"
		byLink       = "review: channel: the front page is page %d, whose link is the home page; set site.postsPage if the site had a page of posts -> /frontPage"
		cannotTell   = "so the front page cannot be told; set site.frontPage and site.postsPage if the site had pages for them -> /frontPage"
	)
	frontPage := func(entries []string) []string {
		return slices.DeleteFunc(slices.Clone(entries), func(e string) bool { return !strings.Contains(e, "/frontPage") && !strings.Contains(e, "/postsPage") })
	}
	for _, tt := range []struct {
		name         string
		items        []siteItem
		ix           *wprest.Index
		front, posts int64
		entries      []string
	}{
		{"both", pages(), &wprest.Index{ShowOnFront: "page", PageOnFront: 30, PageForPosts: 31}, 30, 31, nil},
		{"only the front page", pages(), &wprest.Index{ShowOnFront: "page", PageOnFront: 30}, 30, 0, nil},
		{"none", pages(), &wprest.Index{ShowOnFront: "page"}, 0, 0, nil},
		{"posts page without a front page", pages(), &wprest.Index{ShowOnFront: "page", PageForPosts: 31}, 0, 0,
			[]string{fmt.Sprintf(noFront, 31)}},
		{"the same page", pages(), &wprest.Index{ShowOnFront: "page", PageOnFront: 30, PageForPosts: 30}, 30, 0,
			[]string{fmt.Sprintf(same, 30)}},
		{"the front page is not in the WXR", pages(), &wprest.Index{ShowOnFront: "page", PageOnFront: 99, PageForPosts: 31}, 0, 0,
			[]string{fmt.Sprintf(skippedFront, 99), fmt.Sprintf(noFront, 31)}},
		{"the posts page is not in the WXR", pages(), &wprest.Index{ShowOnFront: "page", PageOnFront: 30, PageForPosts: 98}, 30, 0,
			[]string{fmt.Sprintf(skippedPosts, 98)}},
		{"the front page is a post", pages(), &wprest.Index{ShowOnFront: "page", PageOnFront: 40, PageForPosts: 31}, 0, 0,
			[]string{fmt.Sprintf("skipped: item 40 (post %q): the front page, page 40, is not in the WXR -> /frontPage", "Item 40"), fmt.Sprintf(noFront, 31)}},
		{"latest posts", pages(), &wprest.Index{ShowOnFront: "posts", PageOnFront: 30, PageForPosts: 31}, 0, 0, nil},
		{"older WordPress", pages(), &wprest.Index{}, 30, 0, []string{fmt.Sprintf(byLink, 30)}},
		{"no REST API", pages(), nil, 30, 0, []string{fmt.Sprintf(byLink, 30)}},
		{"another show_on_front", pages(), &wprest.Index{ShowOnFront: "other", PageOnFront: 31}, 30, 0, []string{fmt.Sprintf(byLink, 30)}},
		{"no page with the home page as its link", []siteItem{{id: 31, link: "https://example.com/news/"}}, nil, 0, 0,
			[]string{"review: channel: no page has the home page as its link, " + cannotTell}},
		{"a draft is not the front page", []siteItem{{id: 30, status: "draft", link: "https://example.com/?page_id=30"}}, nil, 0, 0,
			[]string{"review: channel: no page has the home page as its link, " + cannotTell}},
		{"a post with the home page as its link", []siteItem{{id: 30, typ: "post", link: "https://example.com/"}}, nil, 0, 0,
			[]string{"review: channel: no page has the home page as its link, " + cannotTell}},
		{"a draft with the home page as its link", []siteItem{{id: 30, status: "draft", link: "https://example.com/"}}, nil, 0, 0,
			[]string{"review: channel: no page has the home page as its link, " + cannotTell}},
		{"another host", []siteItem{{id: 30, link: "https://example.net/"}}, nil, 0, 0,
			[]string{"review: channel: no page has the home page as its link, " + cannotTell}},
		{"two pages", pages(siteItem{id: 35, link: "https://example.com"}), nil, 0, 0,
			[]string{"review: channel: 2 pages have the home page as their link (30, 35), " + cannotTell}},
		{"many pages", []siteItem{
			{id: 1, link: "https://example.com/"}, {id: 2, link: "https://example.com/"}, {id: 3, link: "https://example.com/"},
			{id: 4, link: "https://example.com/"}, {id: 5, link: "https://example.com/"}, {id: 6, link: "https://example.com/"},
			{id: 7, link: "https://example.com/"},
		}, nil, 0, 0,
			[]string{"review: channel: 7 pages have the home page as their link (1, 2, 3, 4, 5, and 2 more), " + cannotTell}},
	} {
		s, imp := proposeFrom(t, siteWXR("https://example.com", "ja", tt.items...), tt.ix, nil)
		if s.Site.FrontPage != tt.front || s.Site.PostsPage != tt.posts {
			t.Errorf("%s: the front page and the page of the posts are %d and %d, want %d and %d",
				tt.name, s.Site.FrontPage, s.Site.PostsPage, tt.front, tt.posts)
		}
		sameSet(t, tt.name+": the entries", frontPage(settingsEntries(imp.Report)), tt.entries)
	}
}

func TestProposeMedia(t *testing.T) {
	items := []siteItem{
		{id: 50, typ: "attachment", status: "inherit", link: "https://example.com/logo/"},
		{id: 51, typ: "attachment", status: "inherit", link: "https://example.com/icon/"},
		{id: 30, link: "https://example.com/"},
	}
	const unknown = "review: channel: set site.logo and site.icon to attachments of the WXR, if the site had them -> /logo"
	for _, tt := range []struct {
		name       string
		ix         *wprest.Index
		logo, icon int64
		entries    []string
	}{
		{"both", &wprest.Index{SiteLogo: 50, SiteIcon: 51}, 50, 51, nil},
		{"only the logo", &wprest.Index{SiteLogo: 50}, 50, 0, nil},
		{"only the icon", &wprest.Index{SiteIcon: 51}, 0, 51, nil},
		{"the icon is not in the WXR", &wprest.Index{SiteLogo: 50, SiteIcon: 99}, 50, 0,
			[]string{"skipped: channel: the icon, attachment 99, is not in the WXR -> /icon"}},
		{"neither is in the WXR", &wprest.Index{SiteLogo: 98, SiteIcon: 99}, 0, 0,
			[]string{"skipped: channel: the logo, attachment 98, is not in the WXR -> /logo", "skipped: channel: the icon, attachment 99, is not in the WXR -> /icon"}},
		{"the logo is a page", &wprest.Index{SiteLogo: 30, SiteIcon: 51}, 0, 51,
			[]string{fmt.Sprintf("skipped: item 30 (page %q): the logo, attachment 30, is not in the WXR -> /logo", "Item 30")}},
		{"the REST API gives neither", &wprest.Index{Home: "https://example.com"}, 0, 0, []string{unknown}},
		{"no REST API", nil, 0, 0, []string{unknown}},
	} {
		s, imp := proposeFrom(t, siteWXR("https://example.com", "ja", items...), tt.ix, nil)
		if s.Site.Logo != tt.logo || s.Site.Icon != tt.icon {
			t.Errorf("%s: the logo and the icon are %d and %d, want %d and %d", tt.name, s.Site.Logo, s.Site.Icon, tt.logo, tt.icon)
		}
		var got []string
		for _, e := range settingsEntries(imp.Report) {
			if strings.HasSuffix(e, " -> /logo") || strings.HasSuffix(e, " -> /icon") {
				got = append(got, e)
			}
		}
		sameSet(t, tt.name+": the entries", got, tt.entries)
	}
}

func TestProposeRESTOfAnotherSite(t *testing.T) {
	const noHome = "the REST API does not give its home, so it cannot be checked to be that of the WXR"
	other := func(home, link string) string {
		return fmt.Sprintf("the REST API is that of %q, but the WXR is of %q", home, link)
	}
	// The methods that read the REST API check first that it is that of the site of the WXR.
	methods := []struct {
		name string
		call func(imp *Import) error
	}{
		{"ProposeSettings", func(imp *Import) error { _, err := imp.ProposeSettings(nil); return err }},
		{"InferPermalinks", func(imp *Import) error { _, err := imp.InferPermalinks(nil); return err }},
		{"Authors", func(imp *Import) error { _, err := imp.Authors(authorSite()); return err }},
	}
	for _, tt := range []struct {
		wxr, home string
		err       string // "" if the REST API is that of the site
	}{
		{"https://example.com", "https://example.com", ""},
		{"https://example.com", "https://example.com/", ""},
		{"https://example.com/", "https://example.com", ""},
		{"https://example.com", "http://example.com", ""},
		{"http://example.com", "https://example.com", ""},
		{"https://example.com", "https://EXAMPLE.com", ""},
		{"https://example.com", "https://example.com:8080", ""},
		{"https://example.com/blog", "https://example.com/blog/", ""},
		{"https://example.com/%e3%81%82", "https://example.com/" + "あ", ""},
		{"https://example.com", "", noHome},
		{"https://example.com", " \t ", noHome},
		{"https://example.com", "https://www.example.com", other("https://www.example.com", "https://example.com")},
		{"https://example.com", "https://other.example.net", other("https://other.example.net", "https://example.com")},
		{"https://example.com", "https://example.com/blog", other("https://example.com/blog", "https://example.com")},
		{"https://example.com/blog", "https://example.com", other("https://example.com", "https://example.com/blog")},
		{"https://example.com/blog", "https://example.com/news", other("https://example.com/news", "https://example.com/blog")},
		{"https://example.com/blog", "https://example.com/blog2", other("https://example.com/blog2", "https://example.com/blog")},
		{"", "https://example.com", other("https://example.com", "")},
		// The addresses are quoted, so that a line break in one is not one in the message.
		{"https://example.com", "https://example.net\nsite.xml:1:1: review: channel: FORGED", other("https://example.net\nsite.xml:1:1: review: channel: FORGED", "https://example.com")},
		{"https://example.com", "https://example.net\x1b[31m", other("https://example.net\x1b[31m", "https://example.com")},
	} {
		for _, m := range methods {
			e, rep := readString(t, "site.xml", siteWXR(tt.wxr, "ja"))
			imp := New("site.xml", e, rep)
			imp.REST = &wprest.Snapshot{Index: wprest.Index{Home: tt.home}}
			before := len(reportLines(t, imp.Report))
			err := m.call(imp)
			switch {
			case tt.err == "" && err != nil:
				t.Errorf("%s: REST API of %q for the WXR of %q: %v", m.name, tt.home, tt.wxr, err)
			case tt.err != "" && (err == nil || err.Error() != tt.err):
				t.Errorf("%s: REST API of %q for the WXR of %q: error %v, want %q", m.name, tt.home, tt.wxr, err, tt.err)
			case tt.err != "":
				if strings.ContainsAny(err.Error(), "\n\r") {
					t.Errorf("%s: the error for the home %q has a line break: %q", m.name, tt.home, err)
				}
				if got := len(reportLines(t, imp.Report)); got != before {
					t.Errorf("%s: REST API of %q for the WXR of %q: the report grew from %d to %d lines", m.name, tt.home, tt.wxr, before, got)
				}
			}
		}
	}

	// Nothing is proposed without the WXR.
	if _, err := (&Import{}).ProposeSettings(nil); err == nil {
		t.Error("ProposeSettings of an import without the WXR gave no error")
	}
}

// TestProposeOneLinePerEntry checks that whatever a text of the REST API holds, line breaks and the escape that sets a
// colour on a terminal among them, the report has a line for each entry.
func TestProposeOneLinePerEntry(t *testing.T) {
	odd := "a\nb\rc\x1b[31md" + string(rune(0x2028)) + "e"
	mutations := []struct {
		name string
		set  func(s *wprest.Snapshot)
	}{
		{"name of the site", func(s *wprest.Snapshot) { s.Index.Name = odd }},
		{"description", func(s *wprest.Snapshot) { s.Index.Description = odd }},
		{"url", func(s *wprest.Snapshot) { s.Index.URL = odd }},
		{"gmt_offset", func(s *wprest.Snapshot) { s.Index.GMTOffset, s.Index.TimezoneString = odd, "" }},
		{"timezone_string", func(s *wprest.Snapshot) { s.Index.TimezoneString = odd }},
		{"show_on_front", func(s *wprest.Snapshot) { s.Index.ShowOnFront = odd }},
		{"slug of a taxonomy", func(s *wprest.Snapshot) { s.Taxonomies[1].Slug = odd }},
		{"name of a taxonomy", func(s *wprest.Snapshot) { s.Taxonomies[1].Name = odd }},
		{"rest_base of a taxonomy", func(s *wprest.Snapshot) { s.Taxonomies[1].RestBase = odd }},
		{"slug of a type", func(s *wprest.Snapshot) { s.Types[2].Slug = odd }},
		{"name of a type", func(s *wprest.Snapshot) { s.Types[2].Name = odd }},
		{"has_archive", func(s *wprest.Snapshot) { s.Types[2].HasArchive = wprest.Archive{Set: true, On: true, Slug: odd} }},
		{"slug of a term", func(s *wprest.Snapshot) { s.Terms[1].Slug = odd }},
		{"name of a term", func(s *wprest.Snapshot) { s.Terms[1].Name = odd }},
		{"link of a term", func(s *wprest.Snapshot) { s.Terms[1].Link = odd }},
		{"name of a user", func(s *wprest.Snapshot) {
			for i := range s.Users {
				s.Users[i].Name = odd
			}
		}},
		{"slug of a user", func(s *wprest.Snapshot) {
			for i := range s.Users {
				s.Users[i].Slug = odd
			}
		}},
		{"link of a user", func(s *wprest.Snapshot) {
			for i := range s.Users {
				s.Users[i].Link, s.Users[i].URL, s.Users[i].Description = odd, odd, odd
			}
		}},
		{"a route that is not given", func(s *wprest.Snapshot) {
			s.Unavailable = append(s.Unavailable, wprest.Unavailable{Route: odd, Taxonomy: odd, Status: 401, Code: odd})
		}},
		{"the users that are not given", func(s *wprest.Snapshot) {
			s.Unavailable = append(s.Unavailable, wprest.Unavailable{Route: "/wp/v2/users", Status: 401, Code: odd})
		}},
		{"the terms that are not given", func(s *wprest.Snapshot) {
			s.Unavailable = append(s.Unavailable, wprest.Unavailable{Route: "/wp/v2/categories", Taxonomy: "category", Status: 401, Code: odd})
		}},
	}
	base := slices.Clone(mutations)
	mutations = append(mutations, struct {
		name string
		set  func(s *wprest.Snapshot)
	}{"all of them", func(s *wprest.Snapshot) {
		for _, m := range base {
			m.set(s)
		}
	}})

	for _, m := range mutations {
		t.Run(m.name, func(t *testing.T) {
			check := func(what string, imp *Import) {
				t.Helper()
				text := reportText(t, imp.Report)
				if lines, entries := strings.Count(text, "\n"), len(imp.Report.Entries()); lines != entries {
					t.Errorf("%s: %d lines for %d entries:\n%s", what, lines, entries, text)
				}
			}
			imp := sampleImport(t, false)
			imp.REST, imp.RESTFile = sampleREST(t), "site-rest.json"
			m.set(imp.REST)
			if _, err := imp.ProposeSettings(sampleDefs(t)); err != nil {
				t.Fatalf("ProposeSettings = %v", err)
			}
			check("ProposeSettings", imp)

			imp = sampleImport(t, false)
			imp.REST, imp.RESTFile = sampleREST(t), "site-rest.json"
			m.set(imp.REST)
			if _, err := imp.Authors(authorSite()); err != nil {
				t.Fatalf("Authors = %v", err)
			}
			check("Authors", imp)
		})
	}
}

// TestSiteBeforeThePlan checks what Site does when it is called before the plan has recorded the pages and the media: it
// leaves out the front page, the page of the posts, the logo and the icon, and says that it did.
func TestSiteBeforeThePlan(t *testing.T) {
	settings, err := ReadSettings("settings.json", readTestdata(t, "out/settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	imp := sampleImport(t, false)
	site, err := imp.Site(settings, sampleDefs(t))
	if err != nil {
		t.Fatalf("Site = %v", err)
	}
	if site.FrontPage != nil || site.PostsPage != nil || site.Logo != "" || site.Icon != "" {
		t.Errorf("the front page, the page of the posts, the logo and the icon are %v, %v, %q, %q, want none",
			site.FrontPage, site.PostsPage, site.Logo, site.Icon)
	}
	var got []string
	for _, e := range imp.Report.Entries() {
		if e.Target.File == "site/settings.json" {
			got = append(got, fmt.Sprintf("%s: %s -> %s", e.Kind, e.Message, e.Target.Pointer))
		}
	}
	sameSet(t, "the entries", got, []string{
		"skipped: the front page, page 30, is not imported -> /frontPage",
		"skipped: the posts page, page 31, is not imported -> /postsPage",
		"skipped: the logo, attachment 50, is not imported -> /logo",
		"skipped: the icon, attachment 51, is not imported -> /icon",
	})
}

// proposeSchema is the schema file of a developer who keeps some of the rules of the sample site, and writes others.
const proposeSchema = `schemaVersion: 1
authorUrl: /writers/{slug}/
taxonomies:
  category:
    url: /topics/{path}/
  genre:
    label: ジャンル
    hierarchical: true
    url: /genre/{slug}/
types:
  post:
    url: /{yyyy}/{mm}/{dd}/{slug}/
  works:
    label: 制作実績
    url: /portfolio/{slug}/
    archive: /works/
    taxonomies: [genre]
    body: true
`

func TestProposeKeepsTheRulesOfTheSchemaFile(t *testing.T) {
	defs := new(content.Site)
	if err := defs.UnmarshalSchema([]byte(proposeSchema)); err != nil {
		t.Fatal(err)
	}
	imp := sampleImport(t, true)
	s, err := imp.ProposeSettings(defs)
	if err != nil {
		t.Fatal(err)
	}
	want := URLSettings{
		Post: "/{yyyy}/{mm}/{dd}/{slug}/", Page: "/{path}/", Author: "/writers/{slug}/",
		Taxonomies: map[string]string{"category": "/topics/{path}/", "genre": "/genre/{slug}/", "tag": "/tag/{slug}/"},
		Types:      map[string]TypeURLs{"works": {URL: "/portfolio/{slug}/", Archive: "/works/"}},
	}
	if !reflect.DeepEqual(s.URLs, want) {
		t.Errorf("the URL rules are %+v, want %+v", s.URLs, want)
	}
	var got []string
	for _, e := range imp.Report.Entries() {
		if e.Target.File == "site/schema.yaml" && strings.HasPrefix(e.Message, "the schema file has") {
			got = append(got, fmt.Sprintf("%s: %s -> %s", e.Kind, e.Message, e.Target.Pointer))
		}
	}
	sameSet(t, "the entries about the rules of the schema file", got, []string{
		`review: the schema file has the URL rule "/writers/{slug}/" for author pages, which differs from "/author/{slug}/" inferred from the old site; old URLs will need redirects -> /authorUrl`,
		`review: the schema file has the URL rule "/topics/{path}/" for the taxonomy category, which differs from "/category/{path}/" inferred from the old site; old URLs will need redirects -> /taxonomies/category/url`,
		`review: the schema file has the URL rule "/portfolio/{slug}/" for the post type works, which differs from "/works/{slug}/" inferred from the old site; old URLs will need redirects -> /types/works/url`,
	})
	// The rules that it has and that agree with the guess give no entry, and neither does the archive that agrees.
	for _, e := range imp.Report.Entries() {
		if strings.Contains(e.Message, "/{yyyy}/{mm}/{dd}/{slug}/") && strings.HasPrefix(e.Message, "the schema file has") {
			t.Errorf("the rule of the posts, which agrees, gives the entry %q", e.Message)
		}
	}

	// An archive that differs has an entry.
	defs.Types[1].Archive = "/portfolio/"
	imp = sampleImport(t, true)
	if _, err := imp.ProposeSettings(defs); err != nil {
		t.Fatal(err)
	}
	want1 := `the schema file has the archive "/portfolio/" for the post type works, which differs from "/works/" inferred from the old site; old URLs will need redirects`
	found := false
	for _, e := range imp.Report.Entries() {
		if e.Message == want1 && e.Target == schemaTarget("/types/works/archive") && e.Kind == Review {
			found = true
		}
	}
	if !found {
		t.Errorf("no entry %q; the report is\n%s", want1, reportText(t, imp.Report))
	}

	// An archive that the guess cannot tell is kept without an entry.
	defs.Types[1].Archive = "/anywhere/"
	imp = sampleImport(t, false)
	s, err = imp.ProposeSettings(defs)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.URLs.Types["works"].Archive; got != "/anywhere/" {
		t.Errorf("the archive is %q, want the one of the schema file", got)
	}
	for _, e := range imp.Report.Entries() {
		if strings.Contains(e.Message, "the schema file has the archive") {
			t.Errorf("an archive that the guess did not tell gives the entry %q", e.Message)
		}
	}
}

func TestProposeSkippedTypes(t *testing.T) {
	items := []siteItem{
		{id: 1, typ: "post", link: "https://example.com/a/"},
		{id: 2, typ: "product", link: "https://example.com/product/a/"},
		{id: 3, typ: "attachment", status: "inherit", link: "https://example.com/a/"},
		{id: 4, typ: "event", link: "https://example.com/event/a/"},
		{id: 5, typ: "product", link: "https://example.com/product/b/"},
		{id: 6, typ: "nav_menu_item", link: "https://example.com/?p=6"},
		{id: 7, typ: "wpcf7_contact_form", link: "https://example.com/?p=7"},
		{id: 8, typ: "My Type", link: "https://example.com/?p=8"},
		{id: 9, typ: "revision", status: "inherit", link: "https://example.com/?p=9"},
		{id: 10, typ: "page", link: "https://example.com/b/"},
		{id: 11, typ: "product", status: "trash", link: "https://example.com/?p=11"},
		{id: 12, typ: "book", link: "https://example.com/book/a/"},
		// ACF keeps its definitions in these, which are no content and are not reported.
		{id: 13, typ: "acf-field-group", link: "https://example.com/?p=13"},
		{id: 14, typ: "acf-field", link: "https://example.com/?p=14"},
		{id: 15, typ: "acf-post-type", link: "https://example.com/?p=15"},
		{id: 16, typ: "acf-taxonomy", link: "https://example.com/?p=16"},
	}
	in := siteWXR("https://example.com", "ja", items...)
	defs := new(content.Site)
	if err := defs.UnmarshalSchema([]byte("schemaVersion: 1\ntypes:\n  event:\n    label: Event\n")); err != nil {
		t.Fatal(err)
	}
	_, imp := proposeFrom(t, in, nil, defs)
	e := imp.WXR
	line := func(id int64, rest string) string {
		it := e.Post(id)
		return fmt.Sprintf("site.xml:%d:%d: skipped: item (%s): %s", it.Line, it.Column, it.Type, rest)
	}
	var got []string
	for _, l := range reportLines(t, imp.Report) {
		if strings.Contains(l, ": skipped: item (") {
			got = append(got, l)
		}
	}
	sameSet(t, "the entries about the types", got, []string{
		line(2, "3 items of the post type product are not imported; define product in the schema file to import them"),
		fmt.Sprintf("site.xml:%d:%d: skipped: item (%q): 1 item of the post type %q is not imported; define %q in the schema file to import it",
			e.Post(8).Line, e.Post(8).Column, "My Type", "My Type", "My Type"),
		line(12, "1 item of the post type book is not imported; define book in the schema file to import it"),
	})

	// Without a definition of the type, the events are not imported either.
	_, imp = proposeFrom(t, in, nil, nil)
	got = got[:0]
	for _, l := range reportLines(t, imp.Report) {
		if strings.Contains(l, ": skipped: item (") {
			got = append(got, l)
		}
	}
	if len(got) != 4 || !strings.Contains(strings.Join(got, "\n"), "1 item of the post type event is not imported") {
		t.Errorf("the entries about the types without a definition are\n%s\nwant 4, with the event among them", strings.Join(got, "\n"))
	}
}

func TestSettingsWriteJSON(t *testing.T) {
	var b bytes.Buffer
	if err := new(Settings).WriteJSON(&b); err != nil {
		t.Fatal(err)
	}
	const empty = `{
  "site": {
    "name": "",
    "tagline": "",
    "url": "",
    "language": "",
    "timeZone": "",
    "postsPerPage": 0,
    "noindex": false,
    "frontPage": 0,
    "postsPage": 0,
    "logo": 0,
    "icon": 0,
    "allowHtml": false
  },
  "urls": {
    "post": "",
    "page": "",
    "author": "",
    "taxonomies": {},
    "types": {}
  },
  "defaultCategory": "",
  "pageTemplates": {},
  "seo": {
    "plugin": ""
  },
  "fields": {},
  "blocks": {
    "presets": {
      "color": {},
      "gradient": {},
      "fontSize": {},
      "fontFamily": {},
      "spacing": {}
    },
    "variants": {},
    "marks": {},
    "imageSizes": {}
  }
}
`
	if b.String() != empty {
		t.Errorf("the empty settings are written as\n%s\nwant\n%s", b.String(), empty)
	}

	full := &Settings{
		Site: SiteSettings{
			Name: "A <b> & \"C\"", Tagline: "手で書いた", URL: "https://example.com/blog", Language: "ja", TimeZone: "Asia/Tokyo",
			PostsPerPage: 12, NoIndex: true, FrontPage: 30, PostsPage: 31, Logo: 50, Icon: 51, AllowHTML: true,
		},
		URLs: URLSettings{
			Post: "/{slug}/", Page: "/{path}/", Author: "/a/{slug}/",
			Taxonomies: map[string]string{"tag": "/t/{slug}/", "category": "/c/{path}/", "a/b~c": "/x/{slug}/"},
			Types:      map[string]TypeURLs{"works": {URL: "/w/{slug}/", Archive: "/w/"}, "book": {}},
		},
		DefaultCategory: "お知らせ",
		PageTemplates:   map[string]string{"templates/full-width.php": "full-width", "a~b/c.php": "", "page-lp.php": "lp"},
		SEO:             SEOSettings{Plugin: "yoast"},
		Fields: map[string]map[string]string{
			"works": {"client": "client", "old~key": "", "a/b": "memo"}, "post": {"subtitle": "subtitle"}, "a~b": {},
		},
		Blocks: BlockSettings{
			Presets: PresetSettings{
				Color:      map[string]string{"vivid-red": "accent", "pale-pink": ""},
				Gradient:   map[string]string{"blush-bordeaux": "warm"},
				FontSize:   map[string]string{"x-large": "huge", "a/b": "ab"},
				FontFamily: map[string]string{"system-font": "sans"},
				Spacing:    map[string]string{"50": "small"},
			},
			Variants:   map[string]string{"rounded": "round", "fill": ""},
			Marks:      map[string]string{"marker": "marker", "note~1": "note"},
			ImageSizes: map[string]string{"large": "wide", "medium_large": ""},
		},
	}
	b.Reset()
	if err := full.WriteJSON(&b); err != nil {
		t.Fatal(err)
	}
	const want = `{
  "site": {
    "name": "A <b> & \"C\"",
    "tagline": "手で書いた",
    "url": "https://example.com/blog",
    "language": "ja",
    "timeZone": "Asia/Tokyo",
    "postsPerPage": 12,
    "noindex": true,
    "frontPage": 30,
    "postsPage": 31,
    "logo": 50,
    "icon": 51,
    "allowHtml": true
  },
  "urls": {
    "post": "/{slug}/",
    "page": "/{path}/",
    "author": "/a/{slug}/",
    "taxonomies": {
      "a/b~c": "/x/{slug}/",
      "category": "/c/{path}/",
      "tag": "/t/{slug}/"
    },
    "types": {
      "book": {
        "url": "",
        "archive": ""
      },
      "works": {
        "url": "/w/{slug}/",
        "archive": "/w/"
      }
    }
  },
  "defaultCategory": "お知らせ",
  "pageTemplates": {
    "a~b/c.php": "",
    "page-lp.php": "lp",
    "templates/full-width.php": "full-width"
  },
  "seo": {
    "plugin": "yoast"
  },
  "fields": {
    "a~b": {},
    "post": {
      "subtitle": "subtitle"
    },
    "works": {
      "a/b": "memo",
      "client": "client",
      "old~key": ""
    }
  },
  "blocks": {
    "presets": {
      "color": {
        "pale-pink": "",
        "vivid-red": "accent"
      },
      "gradient": {
        "blush-bordeaux": "warm"
      },
      "fontSize": {
        "a/b": "ab",
        "x-large": "huge"
      },
      "fontFamily": {
        "system-font": "sans"
      },
      "spacing": {
        "50": "small"
      }
    },
    "variants": {
      "fill": "",
      "rounded": "round"
    },
    "marks": {
      "marker": "marker",
      "note~1": "note"
    },
    "imageSizes": {
      "large": "wide",
      "medium_large": ""
    }
  }
}
`
	if b.String() != want {
		t.Errorf("the settings are written as\n%s\nwant\n%s", b.String(), want)
	}
	got, err := ReadSettings("settings.json", b.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, full) {
		t.Errorf("the settings read back as %+v, want %+v", got, full)
	}

	// The same settings make the same file.
	var again bytes.Buffer
	for range 10 {
		again.Reset()
		if err := full.WriteJSON(&again); err != nil || again.String() != want {
			t.Fatalf("WriteJSON again = %v, %q", err, again.String())
		}
	}

	// Text that is not UTF-8 is not written.
	bad := &Settings{Site: SiteSettings{Name: "\xff"}}
	b.Reset()
	if err := bad.WriteJSON(&b); err == nil || b.Len() != 0 {
		t.Errorf("WriteJSON of a name that is not UTF-8 = %v, with %q written", err, b.String())
	}
	// A writer that fails fails the writing.
	werr := errors.New("disk full")
	if err := full.WriteJSON(failingWriter{werr}); !errors.Is(err, werr) {
		t.Errorf("WriteJSON to a writer that fails = %v, want %v", err, werr)
	}
}

func TestReadSettings(t *testing.T) {
	// A file with some of the keys is read, and the others have no value.
	s, err := ReadSettings("settings.json", []byte(`{"site": {"name": "A", "frontPage": 30}, "urls": {"types": {"works": {"url": "/w/{slug}/"}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	want := &Settings{
		Site: SiteSettings{Name: "A", FrontPage: 30},
		URLs: URLSettings{Types: map[string]TypeURLs{"works": {URL: "/w/{slug}/"}}},
	}
	if !reflect.DeepEqual(s, want) {
		t.Errorf("ReadSettings = %+v, want %+v", s, want)
	}
	for _, in := range []string{"{}", " {} \n", `{"site": {}}`, `{"urls": {"taxonomies": {}, "types": {}}}`} {
		if s, err := ReadSettings("settings.json", []byte(in)); err != nil {
			t.Errorf("ReadSettings(%q) = %v", in, err)
		} else if s.Site != (SiteSettings{}) {
			t.Errorf("ReadSettings(%q) has the site settings %+v", in, s.Site)
		}
	}
}

func TestReadSettingsBlocks(t *testing.T) {
	// A file with some of the names has only those.
	s, err := ReadSettings("settings.json", []byte(`{"site": {"allowHtml": true}, "blocks": {"presets": {"color": {"red": "accent", "pink": ""}}, "marks": {}}}`))
	if err != nil {
		t.Fatal(err)
	}
	want := BlockSettings{
		Presets: PresetSettings{Color: map[string]string{"red": "accent", "pink": ""}},
		Marks:   map[string]string{},
	}
	if !reflect.DeepEqual(s.Blocks, want) || !s.Site.AllowHTML {
		t.Errorf("ReadSettings has the blocks %+v and allowHtml %v, want %+v and true", s.Blocks, s.Site.AllowHTML, want)
	}
}

func TestReadSettingsPageTemplates(t *testing.T) {
	s, err := ReadSettings("settings.json", []byte(`{"pageTemplates": {"templates/full-width.php": "full-width", "page-lp.php": ""}}`))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"templates/full-width.php": "full-width", "page-lp.php": ""}
	if !reflect.DeepEqual(s.PageTemplates, want) {
		t.Errorf("ReadSettings has the page templates %#v, want %#v", s.PageTemplates, want)
	}
	// A file without the key has none, and one with no names has an empty map.
	for in, wantNil := range map[string]bool{`{}`: true, `{"site": {}}`: true, `{"pageTemplates": {}}`: false} {
		s, err := ReadSettings("settings.json", []byte(in))
		if err != nil {
			t.Fatal(err)
		}
		if (s.PageTemplates == nil) != wantNil || len(s.PageTemplates) != 0 {
			t.Errorf("ReadSettings(%s) has the page templates %#v, want nil: %v", in, s.PageTemplates, wantNil)
		}
	}
}

func TestTemplateName(t *testing.T) {
	long := strings.Repeat("a", content.MaxTextBytes+1)
	for _, tt := range []struct{ file, want string }{
		{"templates/full-width.php", "full-width"},
		{"page-lp.php", "page-lp"},
		{"a/b/c/Landing_2.php", "Landing_2"},
		{"full-width", "full-width"},
		{"template.PHP", ""},
		{"x.php.php", ""},
		{".php", ""},
		{"", ""},
		{"templates/", ""},
		{"templates/.php", ""},
		{"my page.php", ""},
		{"page-ünï.php", ""},
		{"templates/full width.php", ""},
		{"a.b.php", ""},
		{"1.php", "1"},
		{"-.php", "-"},
		{"templates/" + long + ".php", ""},
		{`dir\file.php`, ""},
	} {
		if got := templateName(tt.file); got != tt.want {
			t.Errorf("templateName(%.40q) = %.40q, want %.40q", tt.file, got, tt.want)
		}
	}
}

// pageTemplateEntries lists the entries of the report about the page templates, as "kind: message", with " -> pointer" if
// there is a place in the settings.
func pageTemplateEntries(rep *Report) []string {
	var out []string
	for _, e := range rep.Entries() {
		if strings.HasPrefix(e.Message, "the pages use") {
			line := string(e.Kind) + ": " + e.Message
			if e.Target != (Target{}) {
				line += " -> " + e.Target.File + "#" + e.Target.Pointer
			}
			out = append(out, line)
		}
	}
	return out
}

func TestProposePageTemplates(t *testing.T) {
	const tail = "; pageTemplates maps them to the page templates of the theme of the site"
	page := func(id int, status, typ string, templates ...string) bodyXML {
		x := bodyXML{id: id, typ: typ, status: status}
		for _, v := range templates {
			x.meta = append(x.meta, [2]string{"_wp_page_template", v})
		}
		return x
	}
	t.Run("the templates of the pages that are imported", func(t *testing.T) {
		in := bodiesWXR(
			page(1, "publish", "page", "templates/full-width.php"),
			page(2, "draft", "page", "templates/full-width.php"),
			page(3, "private", "page", " \ttemplates/full-width.php\n"),
			page(4, "publish", "page", "page-lp.php"),
			page(5, "publish", "page", "default"),
			page(6, "publish", "page", ""),
			page(7, "publish", "page", "  \n"),
			page(8, "trash", "page", "templates/trashed.php"),
			page(9, "auto-draft", "page", "templates/auto.php"),
			page(10, "publish", "post", "templates/post.php"),
			page(11, "publish", "page", "Landing Page.php"),
			page(12, "pending", "page", "page-lp.php", "templates/second.php"),
			page(13, "publish", "page", " default "),
			page(14, "publish", "page", "Default"),
			page(15, "publish", "page"),
			page(16, "publish", "works", "templates/works.php"),
			page(17, "future", "page", "templates/ü.php"),
		)
		s, imp := proposeFrom(t, in, nil, nil)
		want := map[string]string{
			"templates/full-width.php": "full-width", "page-lp.php": "page-lp", "Landing Page.php": "", "Default": "Default",
			"templates/ü.php": "",
		}
		if !reflect.DeepEqual(s.PageTemplates, want) {
			t.Errorf("PageTemplates = %#v, want %#v", s.PageTemplates, want)
		}
		sameStrings(t, "the entries", pageTemplateEntries(imp.Report), []string{
			`review: the pages use 5 page templates of WordPress: "templates/full-width.php" (3 times), "page-lp.php" (2 times), "Default" (1 time), ` +
				`"Landing Page.php" (1 time), "templates/ü.php" (1 time)` + tail,
		})
		for _, e := range imp.Report.Entries() {
			if strings.HasPrefix(e.Message, "the pages use") && (e.Source.Record != recordChannel || e.Target != (Target{})) {
				t.Errorf("the entry %+v is not about the channel, with no place in the settings", e)
			}
		}
	})
	t.Run("one template", func(t *testing.T) {
		s, imp := proposeFrom(t, bodiesWXR(page(1, "publish", "page", "tpl-a.php")), nil, nil)
		if want := map[string]string{"tpl-a.php": "tpl-a"}; !reflect.DeepEqual(s.PageTemplates, want) {
			t.Errorf("PageTemplates = %#v, want %#v", s.PageTemplates, want)
		}
		sameStrings(t, "the entries", pageTemplateEntries(imp.Report), []string{
			`review: the pages use 1 page template of WordPress: "tpl-a.php" (1 time)` + tail,
		})
	})
	t.Run("the names that are counted most are first, and then by name", func(t *testing.T) {
		in := bodiesWXR(
			page(1, "publish", "page", "b.php"), page(2, "publish", "page", "a.php"),
			page(3, "publish", "page", "c.php"), page(4, "publish", "page", "c.php"),
		)
		_, imp := proposeFrom(t, in, nil, nil)
		sameStrings(t, "the entries", pageTemplateEntries(imp.Report), []string{
			`review: the pages use 3 page templates of WordPress: "c.php" (2 times), "a.php" (1 time), "b.php" (1 time)` + tail,
		})
	})
	t.Run("no template", func(t *testing.T) {
		in := bodiesWXR(page(1, "publish", "page"), page(2, "publish", "page", "default"), page(3, "trash", "page", "x.php"),
			page(4, "publish", "post", "y.php"))
		s, imp := proposeFrom(t, in, nil, nil)
		if s.PageTemplates == nil || len(s.PageTemplates) != 0 {
			t.Errorf("PageTemplates = %#v, want an empty map that is not nil", s.PageTemplates)
		}
		if got := pageTemplateEntries(imp.Report); len(got) != 0 {
			t.Errorf("the entries are %q, want none", got)
		}
		_, imp = proposeFrom(t, siteWXR("https://example.com", "ja"), nil, nil)
		if got := pageTemplateEntries(imp.Report); len(got) != 0 {
			t.Errorf("the entries of a site without pages are %q, want none", got)
		}
	})
	t.Run("the proposal is written and read back", func(t *testing.T) {
		s, _ := proposeFrom(t, bodiesWXR(page(1, "publish", "page", "templates/full-width.php"), page(2, "publish", "page", "A B.php")), nil, nil)
		var b bytes.Buffer
		if err := s.WriteJSON(&b); err != nil {
			t.Fatal(err)
		}
		if want := `  "pageTemplates": {
    "A B.php": "",
    "templates/full-width.php": "full-width"
  },
`; !strings.Contains(b.String(), want) {
			t.Errorf("the file is\n%s\nwant the page templates\n%s", b.String(), want)
		}
		back, err := ReadSettings("settings.json", b.Bytes())
		if err != nil || !reflect.DeepEqual(back, s) {
			t.Errorf("ReadSettings = %+v, %v, want %+v", back, err, s)
		}
	})
}

func TestSitePageTemplates(t *testing.T) {
	imp, defs := siteImport(t)
	// Names that the theme can have, and the empty name, which leaves the template out, are all right, with any key.
	s := sampleSettings()
	s.PageTemplates = map[string]string{"templates/full-width.php": "full-width", "a b.php": "", "": "x_1", "Y-2": "Y-2"}
	if _, err := imp.Site(s, defs); err != nil {
		t.Fatalf("Site with names that are right = %v", err)
	}

	// The keys are in order, and the pointer has the key escaped.
	s = sampleSettings()
	s.PageTemplates = map[string]string{"templates/b.php": "a b", "a~c.php": "ü", "ok.php": "fine", "z.php": "1+1"}
	site, err := imp.Site(s, defs)
	if site != nil {
		t.Errorf("Site = %+v, want none", site)
	}
	const invalid = `invalid name %q: use ASCII letters, digits, "-" and "_"`
	sameStrings(t, "the errors", errorLines(t, err), []string{
		"/pageTemplates/a~0c.php: " + fmt.Sprintf(invalid, "ü"),
		"/pageTemplates/templates~1b.php: " + fmt.Sprintf(invalid, "a b"),
		"/pageTemplates/z.php: " + fmt.Sprintf(invalid, "1+1"),
	})
	var errs content.Errors
	_ = errors.As(err, &errs)
	for _, e := range errs {
		if e.File != "" || e.Line != 0 || e.Column != 0 {
			t.Errorf("the error %+v has a place", *e)
		}
	}

	// A name that is too long is an error too, and the others are not hidden by it.
	s = sampleSettings()
	s.Site.URL = ""
	s.PageTemplates = map[string]string{"long.php": strings.Repeat("a", content.MaxTextBytes+1)}
	_, err = imp.Site(s, defs)
	got := errorLines(t, err)
	if len(got) != 2 || got[0] != fmt.Sprintf("/pageTemplates/long.php: too long: %d bytes (max %d)", content.MaxTextBytes+1, content.MaxTextBytes) ||
		!strings.HasPrefix(got[1], "/site/url: ") {
		t.Errorf("the errors are %.300q, want one at /pageTemplates/long.php and one at /site/url", got)
	}

	// Settings without the table are all right.
	s = sampleSettings()
	if s.PageTemplates != nil {
		t.Fatal("the settings of the test have page templates")
	}
	if _, err := imp.Site(s, defs); err != nil {
		t.Errorf("Site without page templates = %v", err)
	}
}

// fieldProposals lists the entries of the report about the custom fields of the items, as "kind: message", which are at the
// channel and have no place in the settings.
func fieldProposals(t *testing.T, rep *Report) []string {
	t.Helper()
	var out []string
	for _, e := range rep.Entries() {
		if strings.HasPrefix(e.Message, "the items of the post type") {
			if e.Source.Record != recordChannel || e.Target != (Target{}) {
				t.Errorf("the entry %+v is not about the channel, with no place in the settings", e)
			}
			out = append(out, string(e.Kind)+": "+e.Message)
		}
	}
	return out
}

func TestProposeFields(t *testing.T) {
	defs := fieldsDefs(t)
	item := func(id int, typ, status string, kv ...string) bodyXML {
		return bodyXML{id: id, typ: typ, status: status, meta: metas(kv...)}
	}
	proposal := func(typ string, n int, list string) string {
		return fmt.Sprintf("review: the items of the post type %[1]s have %[2]s: %[3]s; fields.%[1]s maps them to the fields of %[1]s in the schema file",
			typ, plural(n, "custom field", "custom fields"), list)
	}
	t.Run("the keys of the items of each type", func(t *testing.T) {
		in := bodiesWXR(
			item(1, "works", "", "client", "A", "memo", "B", "year", "1", "note", "n"),
			item(2, "works", "draft", "client", "A2", "note", " \n", "extra", "e"),
			item(3, "works", "private", "client", "A3", "_edit_lock", "x", "rank_math_title", "x", "ssp_meta_x", "x", "blank", "", "rank_mathx", "x", "ssp_metax", "x"),
			item(4, "post", "", "subtitle", "s", "extra2", "x"),
			item(5, "page", "", "pg", "x"),
			item(6, "product", "", "prod", "x"),
			item(7, "works", "trash", "trashed", "x"),
			item(8, "works", "auto-draft", "auto", "x"),
			item(9, "post", "", "extra2", "y", "extra2", "z"),
		)
		s, imp := proposeFrom(t, in, nil, defs)
		want := map[string]map[string]string{
			"works": {"client": "client", "memo": "memo", "year": "year", "note": "", "extra": "", "rank_mathx": "", "ssp_metax": ""},
			"post":  {"subtitle": "subtitle", "extra2": ""},
			"page":  {"pg": ""},
		}
		if !reflect.DeepEqual(s.Fields, want) {
			t.Errorf("Fields = %v, want %v", s.Fields, want)
		}
		sameSet(t, "the entries", fieldProposals(t, imp.Report), []string{
			proposal("works", 7, `"client" (3 items), "extra" (1 item), "memo" (1 item), "note" (1 item), "rank_mathx" (1 item), "ssp_metax" (1 item), "year" (1 item)`),
			proposal("post", 2, `"extra2" (2 items), "subtitle" (1 item)`),
			proposal("page", 1, `"pg" (1 item)`),
		})
	})
	t.Run("the keys of the rows of a list are not counted", func(t *testing.T) {
		in := bodiesWXR(item(1, "works", "",
			"items", "2", "items_0_title", "A", "items_1_title", "B", "items_00_x", "c", "orphan_0_x", "d", "a_0_b", "e", "a_0_b_1_c", "f",
			"blankp", " ", "blankp_0_x", "v", "x_0", "y", "items_0", "z", "n_1_", "w"))
		s, _ := proposeFrom(t, in, nil, defs)
		want := map[string]string{"items": "items", "items_00_x": "", "orphan_0_x": "", "a_0_b": "", "x_0": "", "items_0": "", "n_1_": ""}
		if !reflect.DeepEqual(s.Fields["works"], want) {
			t.Errorf("Fields = %v, want %v", s.Fields["works"], want)
		}
	})
	t.Run("the keys that a group or a list of the type is read from are not counted", func(t *testing.T) {
		in := bodiesWXR(
			item(1, "works", "",
				"address", " ", "address_city", "Tokyo", "address_geo_lat", "35.6", "address_country", "JP", "address_geo", "g", "addressx_city", "x",
				"items_0_title", "A", "items_0_parts", "1", "items_0_parts_0_name", "x", "items_0_unknown", "u", "itemsx_0_title", "B", "items_00_title", "C"),
			// The type post has no such group and no such list.
			item(2, "post", "", "address_city", "Osaka", "items_0_title", "D"),
		)
		s, imp := proposeFrom(t, in, nil, defs)
		want := map[string]map[string]string{
			"works": {"address_country": "", "address_geo": "", "addressx_city": "", "itemsx_0_title": "", "items_00_title": ""},
			"post":  {"address_city": "", "items_0_title": ""},
		}
		if !reflect.DeepEqual(s.Fields, want) {
			t.Errorf("Fields = %v, want %v", s.Fields, want)
		}
		sameSet(t, "the entries", fieldProposals(t, imp.Report), []string{
			proposal("works", 5, `"address_country" (1 item), "address_geo" (1 item), "addressx_city" (1 item), "items_00_title" (1 item), "itemsx_0_title" (1 item)`),
			proposal("post", 2, `"address_city" (1 item), "items_0_title" (1 item)`),
		})
		// An item that has only such keys has no custom field to map.
		s, imp = proposeFrom(t, bodiesWXR(item(1, "works", "", "address_city", "Tokyo", "items_0_title", "A")), nil, defs)
		if len(s.Fields) != 0 || len(fieldProposals(t, imp.Report)) != 0 {
			t.Errorf("Fields = %v with the entries %q, want none", s.Fields, fieldProposals(t, imp.Report))
		}
	})
	t.Run("the keys of the SEO plugin are not counted", func(t *testing.T) {
		// Yoast SEO is the plugin of most items, so redirect, which The SEO Framework keeps, is a custom field.
		in := bodiesWXR(
			item(1, "works", "", "_yoast_wpseo_title", "a"), item(2, "works", "", "_yoast_wpseo_title", "b"), item(3, "works", "", "_yoast_wpseo_title", "c"),
			item(4, "works", "", "redirect", "x", "exclude_local_search", "1"),
		)
		s, _ := proposeFrom(t, in, nil, defs)
		if want := map[string]string{"redirect": "redirect", "exclude_local_search": ""}; s.SEO.Plugin != "yoast" || !reflect.DeepEqual(s.Fields["works"], want) {
			t.Errorf("the plugin is %q and Fields %v, want yoast and %v", s.SEO.Plugin, s.Fields["works"], want)
		}
		// The SEO Framework is the plugin of the items, so its keys are not.
		in = bodiesWXR(
			item(1, "works", "", "_genesis_title", "a", "redirect", "x", "exclude_local_search", "1", "exclude_from_archive", "1", "foo", "y"),
			item(2, "works", "", "_genesis_title", "b", "_open_graph_title", "c"),
		)
		s, imp := proposeFrom(t, in, nil, defs)
		if want := map[string]string{"foo": ""}; s.SEO.Plugin != "tsf" || !reflect.DeepEqual(s.Fields["works"], want) {
			t.Errorf("the plugin is %q and Fields %v, want tsf and %v", s.SEO.Plugin, s.Fields["works"], want)
		}
		sameStrings(t, "the entries", fieldProposals(t, imp.Report), []string{
			`review: the items of the post type works have 1 custom field: "foo" (1 item); fields.works maps them to the fields of works in the schema file`,
		})
	})
	t.Run("no custom fields", func(t *testing.T) {
		in := bodiesWXR(item(1, "works", "", "_edit_lock", "x", "blank", " "), item(2, "post", ""), item(3, "works", "trash", "k", "v"))
		s, imp := proposeFrom(t, in, nil, defs)
		if s.Fields == nil || len(s.Fields) != 0 {
			t.Errorf("Fields = %#v, want an empty map that is not nil", s.Fields)
		}
		if got := fieldProposals(t, imp.Report); len(got) != 0 {
			t.Errorf("the entries are %q, want none", got)
		}
		_, imp = proposeFrom(t, siteWXR("https://example.com", "ja"), nil, defs)
		if got := fieldProposals(t, imp.Report); len(got) != 0 {
			t.Errorf("the entries of a site without items are %q, want none", got)
		}
	})
	t.Run("more than ten keys", func(t *testing.T) {
		var kv []string
		for i := 1; i <= 13; i++ {
			kv = append(kv, fmt.Sprintf("k%02d", i), "x")
		}
		in := bodiesWXR(item(1, "works", "", kv...), item(2, "works", "", "k13", "x", "k12", "x", "k12", "y"))
		_, imp := proposeFrom(t, in, nil, defs)
		sameStrings(t, "the entries", fieldProposals(t, imp.Report), []string{proposal("works", 13,
			`"k12" (2 items), "k13" (2 items), "k01" (1 item), "k02" (1 item), "k03" (1 item), "k04" (1 item), "k05" (1 item), "k06" (1 item), "k07" (1 item), "k08" (1 item), and 3 more`)})
	})
	t.Run("a type that the schema file does not define", func(t *testing.T) {
		// The fields of post and page are those of the schema file if it has them, and none otherwise.
		in := bodiesWXR(item(1, "post", "", "subtitle", "s", "other", "o"), item(2, "works", "", "client", "c"))
		s, _ := proposeFrom(t, in, nil, nil)
		if want := map[string]map[string]string{"post": {"subtitle": "", "other": ""}}; !reflect.DeepEqual(s.Fields, want) {
			t.Errorf("Fields = %v, want %v", s.Fields, want)
		}
	})
	t.Run("the proposal is written and read back", func(t *testing.T) {
		s, _ := proposeFrom(t, bodiesWXR(item(1, "works", "", "client", "A", "a/b", "x", "a~b", "y")), nil, defs)
		var b bytes.Buffer
		if err := s.WriteJSON(&b); err != nil {
			t.Fatal(err)
		}
		if want := "  \"fields\": {\n    \"works\": {\n      \"a/b\": \"\",\n      \"a~b\": \"\",\n      \"client\": \"client\"\n    }\n  },\n"; !strings.Contains(b.String(), want) {
			t.Errorf("the file is\n%s\nwant the fields\n%s", b.String(), want)
		}
		back, err := ReadSettings("settings.json", b.Bytes())
		if err != nil || !reflect.DeepEqual(back, s) {
			t.Errorf("ReadSettings = %+v, %v, want %+v", back, err, s)
		}
	})
}

func TestReadSettingsFields(t *testing.T) {
	s, err := ReadSettings("settings.json", []byte(`{"fields": {"works": {"client_name": "client", "old": ""}, "post": {}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]map[string]string{"works": {"client_name": "client", "old": ""}, "post": {}}; !reflect.DeepEqual(s.Fields, want) {
		t.Errorf("ReadSettings has the fields %#v, want %#v", s.Fields, want)
	}
	for in, wantNil := range map[string]bool{`{}`: true, `{"site": {}}`: true, `{"fields": {}}`: false} {
		s, err := ReadSettings("settings.json", []byte(in))
		if err != nil {
			t.Fatal(err)
		}
		if (s.Fields == nil) != wantNil || len(s.Fields) != 0 {
			t.Errorf("ReadSettings(%s) has the fields %#v, want nil: %v", in, s.Fields, wantNil)
		}
	}
	for _, tt := range []struct{ in, want string }{
		{`{"fields": {"works": {"a": 5}}}`, "/fields/works/a: expected string, got number"},
		{`{"fields": {"works": {"a": null}}}`, "/fields/works/a: null is not allowed; omit the key"},
		{`{"fields": {"works": null}}`, "/fields/works: null is not allowed; omit the key"},
		{`{"fields": {"works": []}}`, "/fields/works: expected object, got array"},
		{`{"fields": []}`, "/fields: expected object, got array"},
		{`{"fields": {"works": {"a": "b", "a": "c"}}}`, "/fields/works/a: duplicate key"},
	} {
		_, err := ReadSettings("settings.json", []byte(tt.in))
		if got := errorLines(t, err); len(got) != 1 || got[0] != tt.want {
			t.Errorf("ReadSettings(%s) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestSiteFields(t *testing.T) {
	imp, _ := siteImport(t)
	defs := fieldsDefs(t)
	settings := func(table map[string]map[string]string) *Settings {
		s := sampleSettings()
		s.Fields = table
		return s
	}
	// Fields of the schema file, for any key, in the types of the schema file, and post and page, with or without a definition of them.
	site, err := imp.Site(settings(map[string]map[string]string{
		"works": {"client_name": "client", "a/b": "memo", "old": "", "again": ""}, "post": {"sub": "subtitle", "x": ""}, "gear": {}, "page": {"y": ""}, "plain": {"z": ""},
	}), defs)
	if err != nil || site == nil {
		t.Fatalf("Site with fields that are right = %v", err)
	}
	// The errors are in the order of the types and of the keys, and each has the pointer with the segments escaped.
	_, err = imp.Site(settings(map[string]map[string]string{
		"works":  {"a": "client", "b": "client", "c": "nosuch", "d": "memo", "e": "memo", "f": "client", "g~/": "nosuch"},
		"nosuch": {"x": "client"}, "a/b": {}, "post": {"s": "nothing", "t": "subtitle", "u": "subtitle"},
	}), defs)
	sameStrings(t, "the errors", errorLines(t, err), []string{
		`/fields/a~1b: type "a/b" is not in the schema file`,
		`/fields/nosuch: type "nosuch" is not in the schema file`,
		`/fields/post/s: the type post has no field "nothing"`,
		`/fields/post/u: the field "subtitle" is also that of "t"`,
		`/fields/works/b: the field "client" is also that of "a"`,
		`/fields/works/c: the type works has no field "nosuch"`,
		`/fields/works/e: the field "memo" is also that of "d"`,
		`/fields/works/f: the field "client" is also that of "a"`,
		`/fields/works/g~0~1: the type works has no field "nosuch"`,
	})
	var errs content.Errors
	_ = errors.As(err, &errs)
	for _, e := range errs {
		if e.File != "" || e.Line != 0 || e.Column != 0 {
			t.Errorf("the error %+v has a place", *e)
		}
	}

	// A type that the schema file does not define is one that post and page are not.
	_, err = imp.Site(settings(map[string]map[string]string{"works": {"a": "client"}, "post": {"a": "subtitle"}, "page": {"a": "subtitle"}}), nil)
	var ofFields []string
	for _, l := range errorLines(t, err) {
		if strings.HasPrefix(l, "/fields/") {
			ofFields = append(ofFields, l)
		}
	}
	sameStrings(t, "the errors without a schema file", ofFields, []string{
		`/fields/page/a: the type page has no field "subtitle"`,
		`/fields/post/a: the type post has no field "subtitle"`,
		`/fields/works: type "works" is not in the schema file`,
	})

	// The others are not hidden by these.
	s := settings(map[string]map[string]string{"works": {"a": "nosuch"}})
	s.Site.URL = ""
	_, err = imp.Site(s, defs)
	got := errorLines(t, err)
	if len(got) != 2 || got[0] != `/fields/works/a: the type works has no field "nosuch"` || !strings.HasPrefix(got[1], "/site/url: ") {
		t.Errorf("the errors are %q, want one at /fields/works/a and one at /site/url", got)
	}

	// Settings without the table are all right.
	if _, err := imp.Site(settings(nil), defs); err != nil {
		t.Errorf("Site without fields = %v", err)
	}
}

// seoEntries lists the entries of the report about the SEO plugin, as "kind: message", with " -> file#pointer" if there is a
// place.
func seoEntries(rep *Report) []string {
	var out []string
	for _, e := range rep.Entries() {
		if strings.HasPrefix(e.Message, "the items have the SEO settings") || strings.HasPrefix(e.Message, "All in One SEO keeps") {
			line := string(e.Kind) + ": " + e.Message
			if e.Target != (Target{}) {
				line += " -> " + e.Target.File + "#" + e.Target.Pointer
			}
			if e.Source.Record != recordChannel {
				line += " (not at the channel)"
			}
			out = append(out, line)
		}
	}
	return out
}

func TestProposeSEO(t *testing.T) {
	const tail = ": its titles, descriptions, canonical URLs, noindex and images for sharing are imported; the defaults of the plugin " +
		"for the site and for each post type are options of WordPress, which the WXR does not have"
	const aioseoTable = "review: All in One SEO keeps the canonical URL, noindex and the image for sharing in its own table, " +
		"which the WXR does not have; only its titles and descriptions are imported"
	item := func(id int, status, typ string, meta ...string) bodyXML {
		x := bodyXML{id: id, typ: typ, status: status}
		for i := 0; i+1 < len(meta); i += 2 {
			x.meta = append(x.meta, [2]string{meta[i], meta[i+1]})
		}
		return x
	}
	yoast := func(id int) bodyXML { return item(id, "publish", "post", "_yoast_wpseo_title", "A title") }
	rankmath := func(id int) bodyXML { return item(id, "publish", "post", "rank_math_description", "A description") }
	seopress := func(id int) bodyXML { return item(id, "publish", "page", "_seopress_titles_title", "A title") }
	tsf := func(id int) bodyXML { return item(id, "draft", "post", "_genesis_description", "A description") }
	ssp := func(id int) bodyXML { return item(id, "publish", "post", "ssp_meta_title", "A title") }
	aioseo := func(id int) bodyXML { return item(id, "publish", "post", "_aioseo_title", "A title") }
	build := func(groups ...[]bodyXML) string {
		var all []bodyXML
		for _, g := range groups {
			all = append(all, g...)
		}
		return bodiesWXR(all...)
	}
	many := func(n, first int, f func(int) bodyXML) []bodyXML {
		var out []bodyXML
		for i := range n {
			out = append(out, f(first+i))
		}
		return out
	}

	for _, tt := range []struct {
		name   string
		in     string
		plugin string
		want   []string
	}{
		{
			name: "the plugin that most items have, and the others that are there", in: build(many(3, 1, yoast), many(1, 10, rankmath)), plugin: "yoast",
			want: []string{`review: the items have the SEO settings of Yoast SEO (3 items) and Rank Math (1 item); seo.plugin is "yoast"` + tail},
		},
		{
			name: "the plugin with more items is taken, wherever it is in the WXR", in: build(many(1, 1, yoast), many(2, 10, rankmath)), plugin: "rankmath",
			want: []string{`review: the items have the SEO settings of Rank Math (2 items) and Yoast SEO (1 item); seo.plugin is "rankmath"` + tail},
		},
		{
			name: "one plugin", in: build(many(1, 1, seopress)), plugin: "seopress",
			want: []string{`review: the items have the SEO settings of SEOPress (1 item); seo.plugin is "seopress"` + tail},
		},
		{
			name: "as many items: the first in the table, which lists them in its order", in: build(many(2, 1, tsf), many(2, 10, seopress), many(2, 20, yoast)),
			plugin: "yoast",
			want:   []string{`review: the items have the SEO settings of Yoast SEO (2 items), SEOPress (2 items) and The SEO Framework (2 items); seo.plugin is "yoast"` + tail},
		},
		{
			name: "three plugins by their numbers", in: build(many(2, 1, ssp), many(3, 10, rankmath), many(2, 20, tsf), many(1, 30, aioseo)), plugin: "rankmath",
			want: []string{`review: the items have the SEO settings of Rank Math (3 items), The SEO Framework (2 items), SEO SIMPLE PACK (2 items) and All in One SEO (1 item); seo.plugin is "rankmath"` + tail},
		},
		{
			name: "four plugins at last", in: build(many(1, 1, yoast), many(1, 10, rankmath), many(1, 20, seopress), many(1, 30, tsf), many(1, 40, ssp), many(1, 50, aioseo)),
			plugin: "yoast",
			want: []string{`review: the items have the SEO settings of Yoast SEO (1 item), Rank Math (1 item), SEOPress (1 item), The SEO Framework (1 item), ` +
				`SEO SIMPLE PACK (1 item) and All in One SEO (1 item); seo.plugin is "yoast"` + tail},
		},
		{
			name: "All in One SEO has a second entry", in: build(many(2, 1, aioseo), many(1, 10, yoast)), plugin: "aioseo",
			want: []string{
				`review: the items have the SEO settings of All in One SEO (2 items) and Yoast SEO (1 item); seo.plugin is "aioseo"` + tail,
				aioseoTable,
			},
		},
		{
			name: "All in One SEO that is not the one taken has none", in: build(many(1, 1, aioseo), many(2, 10, yoast)), plugin: "yoast",
			want: []string{`review: the items have the SEO settings of Yoast SEO (2 items) and All in One SEO (1 item); seo.plugin is "yoast"` + tail},
		},
		{
			name: "the items that are not imported are not counted",
			in: build(
				[]bodyXML{
					item(1, "publish", "post", "_yoast_wpseo_title", "A title"),
					item(2, "trash", "post", "rank_math_title", "In the trash"),
					item(3, "trash", "page", "rank_math_title", "In the trash"),
					item(4, "auto-draft", "post", "rank_math_title", "Automatic"),
					item(5, "publish", "product", "rank_math_title", "A type that the site does not have"),
					item(6, "inherit", "attachment", "rank_math_title", "An attachment"),
					item(7, "publish", "wp_block", "rank_math_title", "A reusable block"),
				}),
			plugin: "yoast",
			want:   []string{`review: the items have the SEO settings of Yoast SEO (1 item); seo.plugin is "yoast"` + tail},
		},
		{
			name: "an item that has the settings of two plugins counts for both",
			in: build([]bodyXML{
				item(1, "publish", "post", "_yoast_wpseo_title", "A", "rank_math_title", "B"),
				item(2, "publish", "post", "_yoast_wpseo_title", "A", "rank_math_title", "B", "ssp_meta_title", "C"),
				item(3, "publish", "post", "rank_math_title", "B"),
			}),
			plugin: "rankmath",
			want:   []string{`review: the items have the SEO settings of Rank Math (3 items), Yoast SEO (2 items) and SEO SIMPLE PACK (1 item); seo.plugin is "rankmath"` + tail},
		},
		{
			name: "a draft, a private and a scheduled item are counted",
			in: build([]bodyXML{
				item(1, "draft", "post", "ssp_meta_title", "A"), item(2, "private", "post", "ssp_meta_title", "B"),
				item(3, "future", "page", "ssp_meta_title", "C"), item(4, "pending", "post", "ssp_meta_title", "D"),
				item(5, "publish", "post", "_yoast_wpseo_title", "A title"),
			}),
			plugin: "ssp",
			want:   []string{`review: the items have the SEO settings of SEO SIMPLE PACK (4 items) and Yoast SEO (1 item); seo.plugin is "ssp"` + tail},
		},
		{
			name: "a custom type that the schema file has",
			in: build([]bodyXML{item(1, "publish", "works", "_aioseo_description", "A description"), item(2, "publish", "post", "rank_math_title", "A title")},
				[]bodyXML{item(3, "publish", "works", "rank_math_title", "Another")}),
			plugin: "rankmath",
			want:   []string{`review: the items have the SEO settings of Rank Math (2 items) and All in One SEO (1 item); seo.plugin is "rankmath"` + tail},
		},
		{
			name: "a postmeta that is only recorded counts, and one that is read counts for each of its keys once",
			in: build([]bodyXML{
				item(1, "publish", "post", "_yoast_wpseo_opengraph-title", "Shared"),
				item(2, "publish", "post", "_yoast_wpseo_title", "A", "_yoast_wpseo_metadesc", "B", "_yoast_wpseo_canonical", "https://example.org/"),
				item(3, "publish", "post", "rank_math_breadcrumb_title", "Crumb"),
			}),
			plugin: "yoast",
			want:   []string{`review: the items have the SEO settings of Yoast SEO (2 items) and Rank Math (1 item); seo.plugin is "yoast"` + tail},
		},
		{
			// Without it, a site that wrote no SEO settings by hand would lose the primary categories from its addresses.
			name: "the primary category of Yoast SEO alone tells that the site had it",
			in: build([]bodyXML{
				item(1, "publish", "post", "_yoast_wpseo_primary_category", "4"), item(2, "publish", "post"),
				item(3, "publish", "post", "_yoast_wpseo_primary_category", ""),
			}),
			plugin: "yoast",
			want:   []string{`review: the items have the SEO settings of Yoast SEO (1 item); seo.plugin is "yoast"` + tail},
		},
		{
			name: "values that are empty, white space or only in a later postmeta do not count",
			in: build([]bodyXML{
				item(1, "publish", "post", "_yoast_wpseo_title", ""),
				item(2, "publish", "post", "_yoast_wpseo_title", " \t\n "),
				item(3, "publish", "post", "_yoast_wpseo_title", "", "_yoast_wpseo_title", "A later one"),
				item(4, "publish", "post", "_yoast_wpseo_focuskw", "keyword", "_yoast_wpseo_linkdex", "30"),
				item(5, "publish", "post", "rank_math_advanced_robots", "a:1:{s:6:\"max-snippet\";i:-1;}", "rank_math_focus_keyword", "x"),
				item(6, "publish", "post", "Yoast_wpseo_title", "wrong case"),
			}),
			plugin: "", want: nil,
		},
		{name: "no postmeta of a plugin", in: build([]bodyXML{item(1, "publish", "post")}), plugin: "", want: nil},
		{name: "no items", in: siteWXR("https://example.com", "ja"), plugin: "", want: nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			defs := &content.Site{Types: []content.TypeDef{{Name: "works", Label: "Works", Body: true}}}
			s, imp := proposeFrom(t, tt.in, nil, defs)
			if s.SEO.Plugin != tt.plugin {
				t.Errorf("SEO.Plugin = %q, want %q", s.SEO.Plugin, tt.plugin)
			}
			sameSet(t, "the entries", seoEntries(imp.Report), tt.want)
		})
	}

	t.Run("every key of a plugin counts, one item for each", func(t *testing.T) {
		for _, p := range seoPlugins {
			var items []bodyXML
			for i, k := range p.keys() {
				items = append(items, item(i+1, "publish", "post", k, "x"))
			}
			s, imp := proposeFrom(t, bodiesWXR(items...), nil, nil)
			if s.SEO.Plugin != p.name {
				t.Errorf("the plugin of the items with the keys of %s is %q", p.name, s.SEO.Plugin)
			}
			want := fmt.Sprintf(`review: the items have the SEO settings of %s (%d items); seo.plugin is %q`, p.label, len(items), p.name)
			if got := seoEntries(imp.Report); !slices.ContainsFunc(got, func(l string) bool { return strings.HasPrefix(l, want) }) {
				t.Errorf("the entries about %s are %q, want one that starts with %q", p.name, got, want)
			}
		}
	})
	t.Run("the proposal is written and read back", func(t *testing.T) {
		s, _ := proposeFrom(t, build(many(1, 1, rankmath)), nil, nil)
		var b bytes.Buffer
		if err := s.WriteJSON(&b); err != nil {
			t.Fatal(err)
		}
		if want := "  \"seo\": {\n    \"plugin\": \"rankmath\"\n  },\n"; !strings.Contains(b.String(), want) {
			t.Errorf("the file is\n%s\nwant the SEO settings\n%s", b.String(), want)
		}
		back, err := ReadSettings("settings.json", b.Bytes())
		if err != nil || !reflect.DeepEqual(back, s) {
			t.Errorf("ReadSettings = %+v, %v, want %+v", back, err, s)
		}
	})
	t.Run("the proposal is the same each time", func(t *testing.T) {
		in := build(many(2, 1, ssp), many(2, 10, yoast))
		first, imp := proposeFrom(t, in, nil, nil)
		want := seoEntries(imp.Report)
		for range 20 {
			again, imp := proposeFrom(t, in, nil, nil)
			if again.SEO != first.SEO {
				t.Fatalf("SEO = %+v, want %+v", again.SEO, first.SEO)
			}
			sameStrings(t, "the entries", seoEntries(imp.Report), want)
		}
	})
}

func TestReadSettingsSEO(t *testing.T) {
	s, err := ReadSettings("settings.json", []byte(`{"seo": {"plugin": "tsf"}}`))
	if err != nil || s.SEO.Plugin != "tsf" {
		t.Errorf("ReadSettings = %+v, %v, want the plugin tsf", s, err)
	}
	// A file without the key, and one with no plugin, have none.
	for _, in := range []string{`{}`, `{"seo": {}}`, `{"seo": {"plugin": ""}}`} {
		if s, err := ReadSettings("settings.json", []byte(in)); err != nil || s.SEO.Plugin != "" {
			t.Errorf("ReadSettings(%s) = %+v, %v, want no plugin", in, s, err)
		}
	}
	// The name is not checked when the file is read: Site checks it.
	if s, err := ReadSettings("settings.json", []byte(`{"seo": {"plugin": "nosuch"}}`)); err != nil || s.SEO.Plugin != "nosuch" {
		t.Errorf("ReadSettings of an unknown plugin = %+v, %v", s, err)
	}
	for _, tt := range []struct{ in, want string }{
		{`{"seo": {"plugin": 5}}`, "/seo/plugin: expected string, got number"},
		{`{"seo": {"plugin": null}}`, "/seo/plugin: null is not allowed; omit the key"},
		{`{"seo": {"plugin": "a", "other": 1}}`, "/seo/other: unknown key"},
		{`{"seo": "yoast"}`, "/seo: expected object, got string"},
	} {
		_, err := ReadSettings("settings.json", []byte(tt.in))
		if err == nil {
			t.Errorf("ReadSettings(%s) = nil", tt.in)
			continue
		}
		if got := errorLines(t, err); len(got) != 1 || got[0] != tt.want {
			t.Errorf("ReadSettings(%s) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestSiteSEOPlugin(t *testing.T) {
	imp, defs := siteImport(t)
	for _, name := range []string{"", "yoast", "rankmath", "seopress", "tsf", "ssp", "aioseo"} {
		s := sampleSettings()
		s.SEO.Plugin = name
		if _, err := imp.Site(s, defs); err != nil {
			t.Errorf("Site with the plugin %q = %v", name, err)
		}
	}
	for _, name := range []string{"Yoast", "yoast ", "wordpress-seo", "all-in-one-seo", "x"} {
		s := sampleSettings()
		s.SEO.Plugin = name
		site, err := imp.Site(s, defs)
		if site != nil {
			t.Errorf("Site with the plugin %q = %+v, want none", name, site)
		}
		want := fmt.Sprintf(`/seo/plugin: unknown SEO plugin %q: use yoast, rankmath, seopress, tsf, ssp or aioseo, or leave it empty`, name)
		sameStrings(t, "the errors", errorLines(t, err), []string{want})
		var errs content.Errors
		_ = errors.As(err, &errs)
		for _, e := range errs {
			if e.File != "" || e.Line != 0 || e.Column != 0 {
				t.Errorf("the error %+v has a place", *e)
			}
		}
	}
	// The others are not hidden by it.
	s := sampleSettings()
	s.SEO.Plugin = "x"
	s.Site.URL = ""
	_, err := imp.Site(s, defs)
	got := errorLines(t, err)
	if len(got) != 2 || !strings.HasPrefix(got[0], "/seo/plugin: ") || !strings.HasPrefix(got[1], "/site/url: ") {
		t.Errorf("the errors are %q, want one at /seo/plugin and one at /site/url", got)
	}
}

func TestProposeEmptyBlockNames(t *testing.T) {
	// The proposal has every map of the names, empty, and allowHtml false, so that the file shows what can be corrected.
	s, _ := proposeFrom(t, siteWXR("https://example.com", "ja"), nil, nil)
	if !reflect.DeepEqual(s.Blocks, emptyBlockSettings()) {
		t.Errorf("the proposal has the blocks %+v, want empty maps", s.Blocks)
	}
	for name, m := range map[string]map[string]string{
		"color": s.Blocks.Presets.Color, "gradient": s.Blocks.Presets.Gradient, "fontSize": s.Blocks.Presets.FontSize,
		"fontFamily": s.Blocks.Presets.FontFamily, "spacing": s.Blocks.Presets.Spacing, "variants": s.Blocks.Variants,
		"marks": s.Blocks.Marks, "imageSizes": s.Blocks.ImageSizes,
	} {
		if m == nil || len(m) != 0 {
			t.Errorf("the names of %s are %#v, want an empty map that is not nil", name, m)
		}
	}
	if s.Site.AllowHTML {
		t.Error("the proposal has allowHtml true")
	}
}

// settingsProblem is an error that ReadSettings is to give: at the nth appearance of a text in the file (the end if at is
// "<end>"), for the pointer, with the message.
type settingsProblem struct {
	at, pointer, msg string
	nth              int // 0 for the first
}

func TestReadSettingsErrors(t *testing.T) {
	const file = "settings.json"
	for _, tt := range []struct {
		name, in string
		want     []settingsProblem
	}{
		{"unknown key", "{\n  \"site\": {\n    \"name\": \"x\",\n    \"foo\": 1\n  }\n}\n",
			[]settingsProblem{{at: `"foo"`, pointer: "/site/foo", msg: "unknown key"}}},
		{"unknown key in a type", "{\n  \"urls\": {\n    \"types\": {\n      \"works\": {\n        \"url\": \"/w/{slug}/\",\n        \"bar\": 1\n      }\n    }\n  }\n}\n",
			[]settingsProblem{{at: `"bar"`, pointer: "/urls/types/works/bar", msg: "unknown key"}}},
		{"unknown key at the top", `{"site": {}, "schemaVersion": 1}`,
			[]settingsProblem{{at: `"schemaVersion"`, pointer: "/schemaVersion", msg: "unknown key"}}},
		{"a key in the wrong case", `{"Site": {}}`, []settingsProblem{{at: `"Site"`, pointer: "/Site", msg: "unknown key"}}},
		{"duplicate key", "{\n  \"site\": {\n    \"name\": \"x\",\n    \"name\": \"y\"\n  }\n}\n",
			[]settingsProblem{{at: `"name"`, nth: 2, pointer: "/site/name", msg: "duplicate key"}}},
		{"duplicate type", `{"urls": {"types": {"works": {}, "works": {}}}}`,
			[]settingsProblem{{at: `"works"`, nth: 2, pointer: "/urls/types/works", msg: "duplicate key"}}},
		{"null", "{\n  \"site\": {\n    \"tagline\": null\n  }\n}\n",
			[]settingsProblem{{at: `"tagline"`, pointer: "/site/tagline", msg: "null is not allowed; omit the key"}}},
		{"null object", `{"site": null}`, []settingsProblem{{at: `"site"`, pointer: "/site", msg: "null is not allowed; omit the key"}}},
		{"null in a map", `{"urls": {"taxonomies": {"a/b": null}, "types": null}}`,
			[]settingsProblem{
				{at: `"a/b"`, pointer: "/urls/taxonomies/a~1b", msg: "null is not allowed; omit the key"},
				{at: `"types"`, pointer: "/urls/types", msg: "null is not allowed; omit the key"},
			}},
		{"null document", `null`, []settingsProblem{{at: "null", msg: "null is not allowed; omit the key"}}},
		{"a string where a number is", `{"site": {"postsPerPage": "ten"}}`,
			[]settingsProblem{{at: `"postsPerPage"`, pointer: "/site/postsPerPage", msg: "expected number, got string"}}},
		{"a number where a string is", `{"site": {"name": 5}}`,
			[]settingsProblem{{at: `"name"`, pointer: "/site/name", msg: "expected string, got number"}}},
		{"a string where a boolean is", `{"site": {"noindex": "true"}}`,
			[]settingsProblem{{at: `"noindex"`, pointer: "/site/noindex", msg: "expected boolean, got string"}}},
		{"a number where a boolean is", `{"site": {"noindex": 1}}`,
			[]settingsProblem{{at: `"noindex"`, pointer: "/site/noindex", msg: "expected boolean, got number"}}},
		{"an array where an object is", `{"urls": []}`, []settingsProblem{{at: `"urls"`, pointer: "/urls", msg: "expected object, got array"}}},
		{"an array where a map is", `{"urls": {"taxonomies": []}}`,
			[]settingsProblem{{at: `"taxonomies"`, pointer: "/urls/taxonomies", msg: "expected object, got array"}}},
		{"an object where a string is", `{"urls": {"post": {}}}`,
			[]settingsProblem{{at: `"post"`, pointer: "/urls/post", msg: "expected string, got object"}}},
		{"a boolean where a string is", `{"urls": {"types": {"works": {"url": true}}}}`,
			[]settingsProblem{{at: `"url"`, pointer: "/urls/types/works/url", msg: "expected string, got boolean"}}},
		{"a fraction", `{"site": {"postsPerPage": 1.5}}`,
			[]settingsProblem{{at: `"postsPerPage"`, pointer: "/site/postsPerPage", msg: "must be an integer"}}},
		{"a number that is too large", `{"site": {"frontPage": 99999999999999999999}}`,
			[]settingsProblem{{at: `"frontPage"`, pointer: "/site/frontPage", msg: "out of range"}}},
		{"the root is an array", `[]`, []settingsProblem{{at: "[", msg: "expected object, got array"}}},
		{"the root is a string", `"x"`, []settingsProblem{{at: `"x"`, msg: "expected object, got string"}}},
		{"in order of place", "{\n  \"site\": {\n    \"name\": 5,\n    \"tagline\": null,\n    \"url\": null\n  }\n}\n",
			[]settingsProblem{
				{at: `"name"`, pointer: "/site/name", msg: "expected string, got number"},
				{at: `"tagline"`, pointer: "/site/tagline", msg: "null is not allowed; omit the key"},
				{at: `"url"`, pointer: "/site/url", msg: "null is not allowed; omit the key"},
			}},
		{"a null and an unknown key", "{\n  \"site\": {\n    \"foo\": 1,\n    \"tagline\": null\n  }\n}\n",
			[]settingsProblem{
				{at: `"foo"`, pointer: "/site/foo", msg: "unknown key"},
				{at: `"tagline"`, pointer: "/site/tagline", msg: "null is not allowed; omit the key"},
			}},
		{"characters are counted", `{"site": {"name": "見本", "foo": 1}}`,
			[]settingsProblem{{at: `"foo"`, pointer: "/site/foo", msg: "unknown key"}}},
		{"empty", ``, []settingsProblem{{at: "<end>", msg: "unexpected end of JSON input"}}},
		{"white space", " \n ", []settingsProblem{{at: "<end>", msg: "unexpected end of JSON input"}}},
		{"a second value", `{"site": {}} {}`, []settingsProblem{{at: "{}", nth: 2, msg: "unexpected data after value"}}},
		{"a second value after white space", "{}\n\n  []", []settingsProblem{{at: "[]", msg: "unexpected data after value"}}},
		{"truncated", `{"site":`, []settingsProblem{{at: "<end>", pointer: "/site", msg: "unexpected end of JSON input"}}},
		{"unknown key in the blocks", `{"blocks": {"foo": {}}}`, []settingsProblem{{at: `"foo"`, pointer: "/blocks/foo", msg: "unknown key"}}},
		{"unknown kind of preset", `{"blocks": {"presets": {"shadow": {}}}}`,
			[]settingsProblem{{at: `"shadow"`, pointer: "/blocks/presets/shadow", msg: "unknown key"}}},
		{"a preset in the blocks", `{"blocks": {"color": {}}}`, []settingsProblem{{at: `"color"`, pointer: "/blocks/color", msg: "unknown key"}}},
		{"a number as a name", `{"blocks": {"variants": {"rounded": 5}}}`,
			[]settingsProblem{{at: `"rounded"`, pointer: "/blocks/variants/rounded", msg: "expected string, got number"}}},
		{"a boolean as a name", `{"blocks": {"presets": {"spacing": {"50": true}}}}`,
			[]settingsProblem{{at: `"50"`, pointer: "/blocks/presets/spacing/50", msg: "expected string, got boolean"}}},
		{"null as a name", `{"blocks": {"marks": {"marker": null}}}`,
			[]settingsProblem{{at: `"marker"`, pointer: "/blocks/marks/marker", msg: "null is not allowed; omit the key"}}},
		{"null as the names of a kind", `{"blocks": {"presets": {"color": null}}}`,
			[]settingsProblem{{at: `"color"`, pointer: "/blocks/presets/color", msg: "null is not allowed; omit the key"}}},
		{"an array where the names are", `{"blocks": {"imageSizes": []}}`,
			[]settingsProblem{{at: `"imageSizes"`, pointer: "/blocks/imageSizes", msg: "expected object, got array"}}},
		{"a string where the presets are", `{"blocks": {"presets": "red"}}`,
			[]settingsProblem{{at: `"presets"`, pointer: "/blocks/presets", msg: "expected object, got string"}}},
		{"a key that has a slash", `{"blocks": {"variants": {"a/b": 1}}}`,
			[]settingsProblem{{at: `"a/b"`, pointer: "/blocks/variants/a~1b", msg: "expected string, got number"}}},
		{"the same name twice", `{"blocks": {"variants": {"a": "b", "a": "c"}}}`,
			[]settingsProblem{{at: `"a"`, nth: 2, pointer: "/blocks/variants/a", msg: "duplicate key"}}},
		{"allowHtml as a string", `{"site": {"allowHtml": "yes"}}`,
			[]settingsProblem{{at: `"allowHtml"`, pointer: "/site/allowHtml", msg: "expected boolean, got string"}}},
		{"allowHtml as null", `{"site": {"allowHtml": null}}`,
			[]settingsProblem{{at: `"allowHtml"`, pointer: "/site/allowHtml", msg: "null is not allowed; omit the key"}}},
		{"a number as a page template", `{"pageTemplates": {"a.php": 5}}`,
			[]settingsProblem{{at: `"a.php"`, pointer: "/pageTemplates/a.php", msg: "expected string, got number"}}},
		{"null as a page template", `{"pageTemplates": {"templates/a.php": null}}`,
			[]settingsProblem{{at: `"templates/a.php"`, pointer: "/pageTemplates/templates~1a.php", msg: "null is not allowed; omit the key"}}},
		{"null as the page templates", `{"pageTemplates": null}`,
			[]settingsProblem{{at: `"pageTemplates"`, pointer: "/pageTemplates", msg: "null is not allowed; omit the key"}}},
		{"an array where the page templates are", `{"pageTemplates": []}`,
			[]settingsProblem{{at: `"pageTemplates"`, pointer: "/pageTemplates", msg: "expected object, got array"}}},
		{"the same page template twice", `{"pageTemplates": {"a.php": "a", "a.php": "b"}}`,
			[]settingsProblem{{at: `"a.php"`, nth: 2, pointer: "/pageTemplates/a.php", msg: "duplicate key"}}},
	} {
		s, err := ReadSettings(file, []byte(tt.in))
		if err == nil || s != nil {
			t.Errorf("%s: ReadSettings = %v, %v, want errors", tt.name, s, err)
			continue
		}
		var errs content.Errors
		if !errors.As(err, &errs) {
			t.Errorf("%s: the error is %T, not content.Errors", tt.name, err)
			continue
		}
		var got, want []string
		for _, e := range errs {
			if e.File != file {
				t.Errorf("%s: the file of %v is %q", tt.name, e, e.File)
			}
			got = append(got, fmt.Sprintf("%d:%d: %s: %s", e.Line, e.Column, e.Path, e.Msg))
		}
		for _, p := range tt.want {
			in, at, nth := tt.in, p.at, max(p.nth, 1)
			if at == "<end>" {
				in, at = tt.in+"\x00", "\x00"
			}
			line, column := place(t, in, at, nth)
			want = append(want, fmt.Sprintf("%d:%d: %s: %s", line, column, p.pointer, p.msg))
		}
		sameStrings(t, tt.name+": the errors", got, want)
		if len(errs) == 1 && tt.want[0].pointer != "" {
			if text := file + ":" + want[0]; errs[0].Error() != text {
				t.Errorf("%s: the error reads %q, want %q", tt.name, errs[0].Error(), text)
			}
		}
	}

	// A file that is not JSON gives only the syntax error, also if it has a null before it.
	_, err := ReadSettings(file, []byte("{\"site\": {\"tagline\": null}, \"x\""))
	if got := errorLines(t, err); len(got) != 1 || !strings.HasSuffix(got[0], ": unexpected end of JSON input") {
		t.Errorf("the errors of a file with a null and a syntax error are %q, want the syntax error alone", got)
	}

	// The messages of the syntax errors are those of the library, so only their places are fixed.
	for _, tt := range []struct {
		name, in, at string
		pointer      string
	}{
		{"text after the value", `{} x`, "x", ""},
		{"a missing comma", `{"site": {"name": "x" "url": "y"}}`, `"url"`, "/site"},
		{"invalid UTF-8", "{\"site\": {\"name\": \"a\xffb\"}}", "\xff", "/site/name"},
		{"a missing value", `{"site": }`, "}", "/site"},
	} {
		_, err := ReadSettings(file, []byte(tt.in))
		var errs content.Errors
		if !errors.As(err, &errs) || len(errs) != 1 {
			t.Errorf("%s: ReadSettings = %v, want one error", tt.name, err)
			continue
		}
		line, column := place(t, tt.in, tt.at, 1)
		e := errs[0]
		if e.Path != tt.pointer || e.Line != line || e.Column != column || e.Msg == "" || e.File != file {
			t.Errorf("%s: the error is %+v, want the pointer %q at %d:%d", tt.name, *e, tt.pointer, line, column)
		}
	}
}

func TestOffsetPlace(t *testing.T) {
	data := []byte("ab\n見本\nc")
	for off, want := range map[int64][2]int{-1: {1, 1}, 0: {1, 1}, 2: {1, 3}, 3: {2, 1}, 4: {2, 2}, 6: {2, 2}, 9: {2, 3}, 10: {3, 1}, 11: {3, 2}, 99: {3, 2}} {
		if line, column := offsetPlace(data, off); line != want[0] || column != want[1] {
			t.Errorf("offsetPlace(%d) = %d:%d, want %d:%d", off, line, column, want[0], want[1])
		}
	}
}

// siteImport returns the import of the sample WXR with the front page, the page of the posts, the logo and the icon of
// the sample converted, and the schema file of the sample.
func siteImport(t *testing.T) (*Import, *content.Site) {
	t.Helper()
	imp := sampleImport(t, false)
	assignSample(t, imp)
	return imp, sampleDefs(t)
}

// sampleSettings is a corrected set of the settings of the sample site.
func sampleSettings() *Settings {
	return &Settings{
		Site: SiteSettings{
			Name: "見本サイト", Tagline: "手で書いた", URL: "https://example.com", Language: "ja", TimeZone: "Asia/Tokyo",
			PostsPerPage: 20, NoIndex: true, FrontPage: 30, PostsPage: 31, Logo: 50, Icon: 51,
		},
		URLs: URLSettings{
			Post: "/{yyyy}/{mm}/{dd}/{slug}/", Page: "/{path}/", Author: "/author/{slug}/",
			Taxonomies: map[string]string{"category": "/category/{path}/", "tag": "/tag/{slug}/", "genre": "/genre/{slug}/"},
			Types:      map[string]TypeURLs{"works": {URL: "/works/{slug}/", Archive: "/works/"}},
		},
	}
}

func lookupID(t *testing.T, imp *Import, post int64) string {
	t.Helper()
	_, id, ok := imp.IDs.Lookup(Object{Kind: ObjectPost, ID: post})
	if !ok {
		t.Fatalf("post %d has no ID", post)
	}
	return id
}

func TestSite(t *testing.T) {
	imp, defs := siteImport(t)
	site, err := imp.Site(sampleSettings(), defs)
	if err != nil {
		t.Fatal(err)
	}
	want := &content.Site{
		Name: content.Localized{"ja": "見本サイト"}, Tagline: content.Localized{"ja": "手で書いた"},
		URL: "https://example.com", TimeZone: "Asia/Tokyo", Logo: lookupID(t, imp, 50), Icon: lookupID(t, imp, 51),
		Languages: []string{"ja"}, DefaultLanguage: "ja",
		FrontPage: content.Localized{"ja": lookupID(t, imp, 30)}, PostsPage: content.Localized{"ja": lookupID(t, imp, 31)},
		PostsPerPage: 20, AuthorURL: "/author/{slug}/", SEO: content.SiteSEO{NoIndex: true},
		Types: []content.TypeDef{
			{Name: "post", URL: "/{yyyy}/{mm}/{dd}/{slug}/"},
			{Name: "page", URL: "/{path}/"},
			{Name: "works", Label: "制作実績", URL: "/works/{slug}/", Archive: "/works/", Taxonomies: []string{"genre"}, Body: true},
		},
		Taxonomies: []content.TaxonomyDef{
			{Name: "category", URL: "/category/{path}/"},
			{Name: "tag", URL: "/tag/{slug}/"},
			{Name: "genre", Label: "ジャンル", URL: "/genre/{slug}/", Hierarchical: true},
		},
	}
	if !reflect.DeepEqual(site, want) {
		t.Errorf("Site = %+v\nwant %+v", site, want)
	}
	if err := site.Validate(imp.Ref); err != nil {
		t.Errorf("Validate = %v", err)
	}
	// Nothing is left out: the report has only what reading the WXR recorded, the comments.
	if got := reportLines(t, imp.Report); len(got) != 1 || !strings.Contains(got[0], "2 comments on 1 item") {
		t.Errorf("the report is %q, want only the line about the comments", got)
	}
	// What it makes can be written as the two files of the site, and they load as the same site.
	settings, err := site.MarshalSettings()
	if err != nil {
		t.Fatal(err)
	}
	schema, err := site.MarshalSchema()
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := content.LoadSite(content.SiteFile{Name: "site/settings.json", Data: settings},
		content.SiteFile{Name: "site/schema.yaml", Data: schema}, imp.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded, site) {
		t.Errorf("the site loads as %+v, want %+v", loaded, site)
	}
}

func TestSiteDoesNotChangeTheSchemaFile(t *testing.T) {
	imp, defs := siteImport(t)
	before := sampleDefs(t)
	if _, err := imp.Site(sampleSettings(), defs); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(defs, before) {
		t.Errorf("Site changed the schema file: %+v, was %+v", defs, before)
	}
	s := sampleSettings()
	copied := sampleSettings()
	if _, err := imp.Site(s, defs); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s, copied) {
		t.Errorf("Site changed the settings: %+v, was %+v", s, copied)
	}
}

func TestSiteWithoutTheSchemaFile(t *testing.T) {
	imp, _ := siteImport(t)
	s := sampleSettings()
	s.URLs.Taxonomies = map[string]string{"category": "/c/{path}/"}
	s.URLs.Types = nil
	site, err := imp.Site(s, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := []content.TypeDef{{Name: "post", URL: "/{yyyy}/{mm}/{dd}/{slug}/"}, {Name: "page", URL: "/{path}/"}}; !reflect.DeepEqual(site.Types, want) {
		t.Errorf("Types = %+v, want %+v", site.Types, want)
	}
	if want := []content.TaxonomyDef{{Name: "category", URL: "/c/{path}/"}}; !reflect.DeepEqual(site.Taxonomies, want) {
		t.Errorf("Taxonomies = %+v, want %+v", site.Taxonomies, want)
	}
	if _, err := site.MarshalSchema(); err != nil {
		t.Errorf("MarshalSchema = %v", err)
	}
}

func TestSiteDefaultRules(t *testing.T) {
	// A rule that is empty is the default one, so a definition that would have nothing but its name is left out.
	imp, _ := siteImport(t)
	s := sampleSettings()
	s.URLs = URLSettings{Taxonomies: map[string]string{"category": "", "tag": ""}}
	site, err := imp.Site(s, nil)
	if err != nil {
		t.Fatal(err)
	}
	if site.Types != nil || site.Taxonomies != nil || site.AuthorURL != "" {
		t.Errorf("the definitions are %+v, %+v and %q, want none", site.Types, site.Taxonomies, site.AuthorURL)
	}

	// A definition that has more than the rule is kept without it, and one that has only the rule is left out.
	defs := new(content.Site)
	yaml := "schemaVersion: 1\ntaxonomies:\n  tag:\n    label: タグ\n    url: /t/{slug}/\n  category:\n    url: /c/{path}/\ntypes:\n  page:\n    url: /p/{path}/\n  post:\n    label: 記事\n    url: /{slug}/\n"
	if err := defs.UnmarshalSchema([]byte(yaml)); err != nil {
		t.Fatal(err)
	}
	site, err = imp.Site(s, defs)
	if err != nil {
		t.Fatal(err)
	}
	if want := []content.TypeDef{{Name: "post", Label: "記事"}}; !reflect.DeepEqual(site.Types, want) {
		t.Errorf("Types = %+v, want %+v", site.Types, want)
	}
	if want := []content.TaxonomyDef{{Name: "tag", Label: "タグ"}}; !reflect.DeepEqual(site.Taxonomies, want) {
		t.Errorf("Taxonomies = %+v, want %+v", site.Taxonomies, want)
	}
	if _, err := site.MarshalSchema(); err != nil {
		t.Errorf("MarshalSchema = %v", err)
	}
}

func TestSiteKeepsTheOrderOfTheDefinitions(t *testing.T) {
	imp, _ := siteImport(t)
	defs := new(content.Site)
	yaml := "schemaVersion: 1\ntaxonomies:\n  genre:\n    label: ジャンル\n  tag:\n    label: タグ\ntypes:\n  works:\n    label: 制作実績\n  page:\n    label: 固定\n"
	if err := defs.UnmarshalSchema([]byte(yaml)); err != nil {
		t.Fatal(err)
	}
	s := sampleSettings()
	s.URLs.Types = map[string]TypeURLs{"works": {URL: "/w/{slug}/"}}
	s.URLs.Taxonomies = map[string]string{"category": "/c/{path}/", "tag": "/t/{slug}/", "genre": "/g/{slug}/"}
	site, err := imp.Site(s, defs)
	if err != nil {
		t.Fatal(err)
	}
	var types, taxonomies []string
	for _, ty := range site.Types {
		types = append(types, ty.Name)
	}
	for _, tx := range site.Taxonomies {
		taxonomies = append(taxonomies, tx.Name)
	}
	sameStrings(t, "the types", types, []string{"post", "works", "page"})
	sameStrings(t, "the taxonomies", taxonomies, []string{"category", "genre", "tag"})
	if got := site.Types[2]; got.Label != "固定" || got.URL != "/{path}/" {
		t.Errorf("the type page is %+v, want the label of the schema file and the rule of the settings", got)
	}
	if got := site.Taxonomies[2]; got.Label != "タグ" || got.URL != "/t/{slug}/" {
		t.Errorf("the taxonomy tag is %+v, want the label of the schema file and the rule of the settings", got)
	}
}

func TestSiteLeavesOutWhatWasNotImported(t *testing.T) {
	imp, defs := siteImport(t)
	other, err := imp.IDs.Assign(Object{Kind: ObjectPost, ID: 32}, content.RefEntry)
	if err != nil {
		t.Fatal(err)
	}
	imp.AddRef(content.RefEntry, other, content.Ref{Type: content.TypePage, Language: "ja"})
	imp.IDs.Drop(Object{Kind: ObjectPost, ID: 51}) // the icon, which the conversion of the media could not import

	s := sampleSettings()
	s.Site.FrontPage = 99 // not in the WXR
	s.Site.PostsPage = 32 // converted
	s.Site.Logo = 31      // an entry, not a medium
	site, err := imp.Site(s, defs)
	// The page of the posts needs a front page.
	if got := errorLines(t, err); len(got) != 1 || !strings.HasPrefix(got[0], "/site/postsPage: ") || site != nil {
		t.Fatalf("Site = %v, %v, want an error at /site/postsPage", site, err)
	}
	var found []string
	for _, e := range imp.Report.Entries() {
		if e.Target.File == "site/settings.json" {
			found = append(found, fmt.Sprintf("%s: %s: %s -> %s", e.Kind, e.Source.what(), e.Message, e.Target.Pointer))
		}
	}
	sameSet(t, "the entries", found, []string{
		"skipped: channel: the front page, page 99, is not imported -> /frontPage",
		fmt.Sprintf("skipped: item 31 (page %q): the logo, attachment 31, is not imported -> /logo", "ニュース"),
		fmt.Sprintf("skipped: item 51 (attachment %q): the icon, attachment 51, is not imported -> /icon", "icon"),
	})

	// Without the page of the posts, the site is made without what was left out.
	s.Site.PostsPage = 0
	site, err = imp.Site(s, defs)
	if err != nil {
		t.Fatal(err)
	}
	if site.FrontPage != nil || site.PostsPage != nil || site.Logo != "" || site.Icon != "" {
		t.Errorf("the front page, the page of the posts, the logo and the icon are %v, %v, %q, %q, want none",
			site.FrontPage, site.PostsPage, site.Logo, site.Icon)
	}
	if err := site.Validate(imp.Ref); err != nil {
		t.Errorf("Validate = %v", err)
	}

	// An ID of the wrong kind of entity is not imported as the thing: a medium is not a page.
	s = sampleSettings()
	s.Site.FrontPage, s.Site.PostsPage = 50, 0
	site, err = imp.Site(s, defs)
	if err != nil || site.FrontPage != nil {
		t.Errorf("Site with a medium for the front page = %+v, %v, want a site without a front page", site, err)
	}
}

func TestSiteInvalidIDs(t *testing.T) {
	imp, defs := siteImport(t)
	s := sampleSettings()
	s.Site.FrontPage, s.Site.PostsPage, s.Site.Logo, s.Site.Icon = -1, -2, -3, -4
	_, err := imp.Site(s, defs)
	sameStrings(t, "the errors", errorLines(t, err), []string{
		"/site/frontPage: invalid ID -1: use 0 for none, or the ID of the page in WordPress",
		"/site/postsPage: invalid ID -2: use 0 for none, or the ID of the page in WordPress",
		"/site/logo: invalid ID -3: use 0 for none, or the ID of the attachment in WordPress",
		"/site/icon: invalid ID -4: use 0 for none, or the ID of the attachment in WordPress",
	})
}

func TestSiteErrors(t *testing.T) {
	type change func(s *Settings)
	for _, tt := range []struct {
		name   string
		change change
		path   string
		msg    string // a part of the message of the site settings
	}{
		{"no name", func(s *Settings) { s.Site.Name = "" }, "/site/name", "missing name"},
		{"a name with a line break", func(s *Settings) { s.Site.Name = "a\nb" }, "/site/name", ""},
		{"a tagline with a line break", func(s *Settings) { s.Site.Tagline = "a\nb" }, "/site/tagline", ""},
		{"no URL", func(s *Settings) { s.Site.URL = "" }, "/site/url", "missing url"},
		{"a URL with a slash at the end", func(s *Settings) { s.Site.URL = "https://example.com/" }, "/site/url", `must not end with "/"`},
		{"a URL with a host in upper case", func(s *Settings) { s.Site.URL = "https://Example.com" }, "", ""},
		{"a URL that is not http", func(s *Settings) { s.Site.URL = "ftp://example.com" }, "/site/url", ""},
		{"no time zone", func(s *Settings) { s.Site.TimeZone = "" }, "/site/timeZone", "missing timeZone"},
		{"an unknown time zone", func(s *Settings) { s.Site.TimeZone = "Mars/Base" }, "/site/timeZone", "unknown time zone"},
		{"a language that is not a code", func(s *Settings) { s.Site.Language = "x"; s.Site.FrontPage, s.Site.PostsPage = 0, 0 }, "/site/language", "invalid language code"},
		{"too many posts on a page", func(s *Settings) { s.Site.PostsPerPage = 101 }, "/site/postsPerPage", "out of range"},
		{"a negative number of posts on a page", func(s *Settings) { s.Site.PostsPerPage = -1 }, "/site/postsPerPage", "out of range"},
		{"a wrong rule of the posts", func(s *Settings) { s.URLs.Post = "/{slug}" }, "/urls/post", ""},
		{"a rule of the posts that needs a placeholder", func(s *Settings) { s.URLs.Post = "/posts/" }, "/urls/post", "{slug}"},
		{"a wrong rule of the pages", func(s *Settings) { s.URLs.Page = "/{yyyy}/{slug}/" }, "/urls/page", "{yyyy}"},
		{"a wrong rule of the authors", func(s *Settings) { s.URLs.Author = "/author/" }, "/urls/author", "{slug}"},
		{"a wrong rule of the category", func(s *Settings) { s.URLs.Taxonomies["category"] = "/c/" }, "/urls/taxonomies/category", ""},
		{"a wrong rule of the tag", func(s *Settings) { s.URLs.Taxonomies["tag"] = "/t/{path}/" }, "/urls/taxonomies/tag", "{path}"},
		{"a wrong rule of a taxonomy", func(s *Settings) { s.URLs.Taxonomies["genre"] = "/g/" }, "/urls/taxonomies/genre", ""},
		{"a wrong rule of a type", func(s *Settings) { s.URLs.Types["works"] = TypeURLs{URL: "/w/", Archive: "/w/"} }, "/urls/types/works/url", ""},
		{"a wrong archive", func(s *Settings) { s.URLs.Types["works"] = TypeURLs{URL: "/w/{slug}/", Archive: "/w"} }, "/urls/types/works/archive", ""},
		{"an archive with a placeholder", func(s *Settings) { s.URLs.Types["works"] = TypeURLs{URL: "/w/{slug}/", Archive: "/{slug}/"} }, "/urls/types/works/archive", ""},
	} {
		imp, defs := siteImport(t)
		s := sampleSettings()
		tt.change(s)
		site, err := imp.Site(s, defs)
		if tt.path == "" {
			// The ones that normalization repairs are not errors.
			if err != nil {
				t.Errorf("%s: Site = %v", tt.name, err)
			} else if tt.name == "a URL with a host in upper case" && site.URL != "https://example.com" {
				t.Errorf("%s: the URL is %q", tt.name, site.URL)
			}
			continue
		}
		if err == nil || site != nil {
			t.Errorf("%s: Site = %v, %v, want an error at %s", tt.name, site, err, tt.path)
			continue
		}
		lines := errorLines(t, err)
		if len(lines) != 1 || !strings.HasPrefix(lines[0], tt.path+": ") || !strings.Contains(lines[0], tt.msg) {
			t.Errorf("%s: the errors are %q, want one at %s with %q", tt.name, lines, tt.path, tt.msg)
		}
		var errs content.Errors
		_ = errors.As(err, &errs)
		if e := errs[0]; e.File != "" || e.Line != 0 || e.Column != 0 {
			t.Errorf("%s: the error has a place: %+v", tt.name, *e)
		}
	}
}

func TestSiteLanguageErrors(t *testing.T) {
	imp, defs := siteImport(t)
	s := sampleSettings()
	s.Site.Language, s.Site.FrontPage, s.Site.PostsPage = "", 0, 0
	_, err := imp.Site(s, defs)
	got := errorLines(t, err)
	if len(got) != 2 {
		t.Fatalf("Site without a language = %v", err)
	}
	for _, line := range got {
		if !strings.HasPrefix(line, "/site/language: ") {
			t.Errorf("the error %q is not at /site/language", line)
		}
	}
}

func TestSiteAllowHTML(t *testing.T) {
	imp, defs := siteImport(t)
	for _, allow := range []bool{false, true} {
		s := sampleSettings()
		s.Site.AllowHTML = allow
		site, err := imp.Site(s, defs)
		if err != nil {
			t.Fatal(err)
		}
		if site.AllowHTML != allow {
			t.Errorf("allowHtml %v in the settings, %v in the site", allow, site.AllowHTML)
		}
		settings, err := site.MarshalSettings()
		if err != nil {
			t.Fatal(err)
		}
		if has := strings.Contains(string(settings), `"allowHtml":true`); has != allow {
			t.Errorf("the settings document %s has allowHtml true: %v, want %v", settings, has, allow)
		}
	}
}

func TestSiteBlockNames(t *testing.T) {
	imp, defs := siteImport(t)
	s := sampleSettings()
	// Names that the theme can have, and the empty name, which leaves the thing out, are all right.
	s.Blocks = BlockSettings{
		Presets: PresetSettings{
			Color: map[string]string{"vivid-red": "accent_1", "pale-pink": ""}, Gradient: map[string]string{"x": "Y-2"},
			FontSize: map[string]string{"x-large": "huge"}, FontFamily: map[string]string{"serif": "serif"},
			Spacing: map[string]string{"50": "s"},
		},
		Variants: map[string]string{"rounded": "round"}, Marks: map[string]string{"marker": ""}, ImageSizes: map[string]string{"large": "wide"},
	}
	if _, err := imp.Site(s, defs); err != nil {
		t.Fatalf("Site with names that are right = %v", err)
	}

	// Each kind is checked, in the order of the file, and the keys of a kind in order; the pointer has the key escaped.
	bad := func(key string) map[string]string { return map[string]string{key: "a b", "ok": "fine"} }
	s = sampleSettings()
	s.Blocks = BlockSettings{
		Presets: PresetSettings{
			Color: bad("c2"), Gradient: bad("g"), FontSize: bad("f/s"), FontFamily: bad("ff"), Spacing: bad("sp~"),
		},
		Variants: map[string]string{"v2": "-x y", "v1": "ü", "v0": "ok"}, Marks: bad("m"), ImageSizes: bad("i"),
	}
	s.Blocks.Presets.Color["c1"] = "1+1"
	_, err := imp.Site(s, defs)
	const invalid = `invalid name %q: use ASCII letters, digits, "-" and "_"`
	sameStrings(t, "the errors", errorLines(t, err), []string{
		"/blocks/presets/color/c1: " + fmt.Sprintf(invalid, "1+1"),
		"/blocks/presets/color/c2: " + fmt.Sprintf(invalid, "a b"),
		"/blocks/presets/gradient/g: " + fmt.Sprintf(invalid, "a b"),
		"/blocks/presets/fontSize/f~1s: " + fmt.Sprintf(invalid, "a b"),
		"/blocks/presets/fontFamily/ff: " + fmt.Sprintf(invalid, "a b"),
		"/blocks/presets/spacing/sp~0: " + fmt.Sprintf(invalid, "a b"),
		"/blocks/variants/v1: " + fmt.Sprintf(invalid, "ü"),
		"/blocks/variants/v2: " + fmt.Sprintf(invalid, "-x y"),
		"/blocks/marks/m: " + fmt.Sprintf(invalid, "a b"),
		"/blocks/imageSizes/i: " + fmt.Sprintf(invalid, "a b"),
	})
	var errs content.Errors
	_ = errors.As(err, &errs)
	for _, e := range errs {
		if e.File != "" || e.Line != 0 || e.Column != 0 {
			t.Errorf("the error %+v has a place", *e)
		}
	}

	// A name that is too long is an error too, and the others are not hidden by it.
	s = sampleSettings()
	s.Site.URL = ""
	s.Blocks.Variants = map[string]string{"long": strings.Repeat("a", content.MaxTextBytes+1)}
	_, err = imp.Site(s, defs)
	got := errorLines(t, err)
	if len(got) != 2 || got[0] != fmt.Sprintf("/blocks/variants/long: too long: %d bytes (max %d)", content.MaxTextBytes+1, content.MaxTextBytes) ||
		!strings.HasPrefix(got[1], "/site/url: ") {
		t.Errorf("the errors are %.300q, want one at /blocks/variants/long and one at /site/url", got)
	}
}

func TestSiteUnknownDefinitions(t *testing.T) {
	imp, defs := siteImport(t)
	s := sampleSettings()
	s.URLs.Types = map[string]TypeURLs{"works": {URL: "/works/{slug}/"}, "product": {URL: "/p/{slug}/"}, "post": {}, "a/b": {}}
	s.URLs.Taxonomies = map[string]string{"category": "/c/{path}/", "series": "/s/{slug}/", "x~y": ""}
	site, err := imp.Site(s, defs)
	if site != nil {
		t.Errorf("Site = %+v, want none", site)
	}
	sameSet(t, "the errors", errorLines(t, err), []string{
		`/urls/types/a~1b: type "a/b" is not in the schema file`,
		`/urls/types/post: type "post" is not a custom type; use urls.post`,
		`/urls/types/product: type "product" is not in the schema file`,
		`/urls/taxonomies/series: taxonomy "series" is not in the schema file`,
		`/urls/taxonomies/x~0y: taxonomy "x~y" is not in the schema file`,
	})
}

func TestSiteErrorsOfTheSchemaFile(t *testing.T) {
	// What is wrong in what the developer wrote keeps the pointer in the schema file, also if the rule is the settings'.
	imp, _ := siteImport(t)
	defs := new(content.Site)
	yaml := "schemaVersion: 1\ntaxonomies:\n  genre:\n    label: ジャンル\n    url: /g/\ntypes:\n  works:\n    label: 制作実績\n    url: /w/\n    taxonomies: [nosuch]\n  other:\n    url: /o/{slug}\n"
	if err := defs.UnmarshalSchema([]byte(yaml)); err != nil {
		t.Fatal(err)
	}
	s := sampleSettings()
	s.URLs.Types = map[string]TypeURLs{"other": {URL: "/o/{slug}/"}}
	s.URLs.Taxonomies = map[string]string{}
	_, err := imp.Site(s, defs)
	got := errorLines(t, err)
	var paths []string
	for _, line := range got {
		path, _, _ := strings.Cut(line, ": ")
		paths = append(paths, path)
	}
	// works and genre are not in the settings, so their rules are the schema file's; other is, and its rule is right.
	sameStrings(t, "the paths", paths, []string{"/taxonomies/genre/url", "/types/works/url", "/types/works/taxonomies/0"})
}

func TestImportPointer(t *testing.T) {
	s := &Settings{URLs: URLSettings{
		Taxonomies: map[string]string{"category": "", "genre": "", "x~y": ""},
		Types:      map[string]TypeURLs{"works": {}, "a/b": {}},
	}}
	for in, want := range map[string]string{
		"":                         "",
		"/name":                    "/site/name",
		"/name/ja":                 "/site/name",
		"/tagline/ja":              "/site/tagline",
		"/url":                     "/site/url",
		"/timeZone":                "/site/timeZone",
		"/languages":               "/site/language",
		"/languages/0":             "/site/language",
		"/defaultLanguage":         "/site/language",
		"/postsPerPage":            "/site/postsPerPage",
		"/frontPage/ja":            "/site/frontPage",
		"/postsPage/ja":            "/site/postsPage",
		"/logo":                    "/site/logo",
		"/icon":                    "/site/icon",
		"/authorUrl":               "/urls/author",
		"/types/post/url":          "/urls/post",
		"/types/page/url":          "/urls/page",
		"/types/works/url":         "/urls/types/works/url",
		"/types/works/archive":     "/urls/types/works/archive",
		"/types/a~1b/url":          "/urls/types/a~1b/url",
		"/types/other/url":         "/types/other/url",
		"/types/works/label":       "/types/works/label",
		"/types/works/fields/x":    "/types/works/fields/x",
		"/types/works":             "/types/works",
		"/types/post/label":        "/types/post/label",
		"/taxonomies/category/url": "/urls/taxonomies/category",
		"/taxonomies/genre/url":    "/urls/taxonomies/genre",
		"/taxonomies/x~0y/url":     "/urls/taxonomies/x~0y",
		"/taxonomies/tag/url":      "/taxonomies/tag/url",
		"/taxonomies/genre/label":  "/taxonomies/genre/label",
		"/taxonomies/genre":        "/taxonomies/genre",
		"/seo/noindex":             "/seo/noindex",
		"/allowHtml":               "/allowHtml",
	} {
		if got := importPointer(in, s); got != want {
			t.Errorf("importPointer(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDedupe(t *testing.T) {
	errs := content.Errors{
		{Path: "/a", Msg: "x"}, {Path: "/a", Msg: "y"}, {Path: "/a", Msg: "x"}, {Path: "/b", Msg: "x"}, {Path: "/a", Msg: "y"},
	}
	var got []string
	for _, e := range dedupe(errs) {
		got = append(got, e.Path+e.Msg)
	}
	sameStrings(t, "dedupe", got, []string{"/ax", "/ay", "/bx"})
}

func TestIDsText(t *testing.T) {
	for _, tt := range []struct {
		ids  []int64
		want string
	}{
		{[]int64{1}, "1"},
		{[]int64{1, 2}, "1, 2"},
		{[]int64{1, 2, 3, 4, 5}, "1, 2, 3, 4, 5"},
		{[]int64{1, 2, 3, 4, 5, 6}, "1, 2, 3, 4, 5, and 1 more"},
		{[]int64{1, 2, 3, 4, 5, 6, 7, 8}, "1, 2, 3, 4, 5, and 3 more"},
	} {
		if got := idsText(tt.ids); got != tt.want {
			t.Errorf("idsText(%v) = %q, want %q", tt.ids, got, tt.want)
		}
	}
}

func TestSameAddress(t *testing.T) {
	for _, tt := range []struct {
		a, b string
		want bool
	}{
		{"https://example.com", "https://example.com", true},
		{"https://example.com", "https://example.com/", true},
		{"https://Example.com", "http://example.COM", true},
		{" https://example.com ", "https://example.com", true},
		{"https://example.com/blog", "https://example.com/blog/", true},
		{"https://example.com/a%20b", "https://example.com/a b", true},
		{"https://example.com/blog", "https://example.com", false},
		{"https://example.com/blog", "https://example.com/Blog", false},
		{"https://example.com", "https://example.net", false},
		{"https://example.com", "https://www.example.com", false},
		{"https://user@example.com", "https://example.com", true},
		{"example.com", "example.com/", true},
		{"example.com", "https://example.com", false},
		{"", "", true},
		{"", "https://example.com", false},
		{"%zz", "%zz", true},
	} {
		if got := sameAddress(tt.a, tt.b); got != tt.want {
			t.Errorf("sameAddress(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
		if got := sameAddress(tt.b, tt.a); got != tt.want {
			t.Errorf("sameAddress(%q, %q) = %v, want %v", tt.b, tt.a, got, tt.want)
		}
	}
}

// proposeSite proposes the settings of the site w, with its REST API, for the schema file defs.
func proposeSite(t *testing.T, w *wpSite, defs *content.Site) (*Settings, *Import) {
	t.Helper()
	imp := New("site.xml", w.export(), NewReport(Info{File: "site.xml"}))
	imp.REST, imp.RESTFile = w.snapshot(), "rest.json"
	s, err := imp.ProposeSettings(defs)
	if err != nil {
		t.Fatalf("ProposeSettings = %v", err)
	}
	return s, imp
}

func TestProposeDefaultCategory(t *testing.T) {
	site := func(structure string) *wpSite {
		w := baseSite()
		w.structure = structure
		return w
	}
	japanese := func() *wpSite {
		w := site("/%category%/%postname%/")
		w.terms[0].slug = "%e6%97%a5%e6%9c%ac" // the default category, which has the post without a category
		return w
	}
	invalid := func() *wpSite {
		w := site("/%category%/%postname%/")
		w.terms[0].slug = "%c3%28"
		return w
	}
	for _, tt := range []struct {
		name string
		w    *wpSite
		want string
	}{
		{"the slug in the links", site("/%category%/%postname%/"), "uncategorized"},
		{"decoded", japanese(), "日本"},
		{"as written if it cannot be decoded", invalid(), "%c3%28"},
		{"no category in the structure", site("/%year%/%postname%/"), ""},
		{"plain permalinks", site(""), ""},
	} {
		s, _ := proposeSite(t, tt.w, permDefs())
		if s.DefaultCategory != tt.want {
			t.Errorf("%s: DefaultCategory = %q, want %q", tt.name, s.DefaultCategory, tt.want)
		}
	}
}

// TestProposedRulesMakeASite checks that the rules that a site of each kind of permalinks gives are those that the
// guess of the permalinks found, and that the site settings accept them together, with the definitions of the developer.
func TestProposedRulesMakeASite(t *testing.T) {
	for _, structure := range []string{
		"", "/%year%/%monthnum%/%day%/%postname%/", "/%year%/%monthnum%/%postname%/", "/archives/%post_id%", "/%postname%/",
		"/%category%/%postname%.html", "/index.php/%year%/%postname%/", "/blog/%year%/%post_id%/",
	} {
		w := baseSite()
		w.structure = structure
		w.catBase, w.tagBase = "topics", "labels"
		defs := permDefs()
		s, imp := proposeSite(t, w, defs)

		pl, _ := w.guess(t, defs, true)
		want := URLSettings{
			Post: pl.Post.Rule, Page: pl.Page.Rule, Author: pl.Author.Rule,
			Taxonomies: map[string]string{"category": pl.Taxonomies["category"].Rule, "tag": pl.Taxonomies["tag"].Rule},
			Types:      map[string]TypeURLs{"works": {URL: pl.Types["works"].Rule, Archive: pl.Types["works"].Archive}},
		}
		if !reflect.DeepEqual(s.URLs, want) {
			t.Errorf("structure %q: the URL rules are %+v, want %+v", structure, s.URLs, want)
		}

		// The settings that the person enters complete them, and the site settings accept everything.
		s.Site.Language, s.Site.FrontPage = "en", 0
		s.Site.TimeZone, s.Site.URL = "UTC", "https://example.com"
		site, err := imp.Site(s, defs)
		if err != nil {
			t.Errorf("structure %q: Site = %v", structure, err)
			continue
		}
		schema, err := site.MarshalSchema()
		if err != nil {
			t.Errorf("structure %q: MarshalSchema = %v", structure, err)
			continue
		}
		read := new(content.Site)
		if err := read.UnmarshalSchema(schema); err != nil {
			t.Errorf("structure %q: the schema file %q is not read: %v", structure, schema, err)
			continue
		}
		if !reflect.DeepEqual(read.Types, site.Types) {
			t.Errorf("structure %q: the schema file reads as the types %+v, want %+v", structure, read.Types, site.Types)
		}
	}
}

func FuzzReadSettings(f *testing.F) {
	golden, err := os.ReadFile(filepath.Join("testdata", "out", "settings.json"))
	if err != nil {
		f.Fatal(err)
	}
	f.Add(golden)
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"site":{"name":"A","language":"ja","url":"https://example.com","timeZone":"UTC","frontPage":30,"logo":-1}}`))
	f.Add([]byte(`{"urls":{"post":"/{slug}/","types":{"works":{"url":"/w/{slug}/","archive":"/w/"},"post":{}},"taxonomies":{"genre":"/g/{slug}/","x":""}}}`))
	f.Add([]byte("{\"site\":{\"name\":null,\"name\":\"\xff\"},\"foo\":[1,2]}"))
	f.Add([]byte(`{"site":{"allowHtml":true},"blocks":{"presets":{"color":{"red":"accent","pink":""},"gradient":{"a b":"c d"},"fontSize":{},"fontFamily":{"x":"y"},"spacing":{"50":"s"}},"variants":{"rounded":"round"},"marks":{"a/b":"m"},"imageSizes":{"large":"wide"}}}`))
	f.Add([]byte(`{"blocks":{"presets":{"shadow":{}},"variants":{"a":1,"a":null},"marks":[],"imageSizes":null}}`))

	// Each input gets an import of the sample site, in which the front page, the page of the posts, the logo and the icon
	// are converted.
	base, err := os.ReadFile(filepath.Join("testdata", "site.xml"))
	if err != nil {
		f.Fatal(err)
	}
	defs := new(content.Site)
	schema, err := os.ReadFile(filepath.Join("testdata", "schema.yaml"))
	if err != nil {
		f.Fatal(err)
	}
	if err := defs.UnmarshalSchema(schema); err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		s, err := ReadSettings("f.json", data)
		if err != nil {
			var errs content.Errors
			if !errors.As(err, &errs) || len(errs) == 0 {
				t.Fatalf("ReadSettings = %v, want content.Errors", err)
			}
			lines := bytes.Count(data, []byte("\n")) + 1
			for _, e := range errs {
				if e.File != "f.json" || e.Msg == "" || e.Line < 1 || e.Line > lines || e.Column < 1 {
					t.Errorf("the error %+v is not in the file f.json at a place in it", *e)
				}
			}
			return
		}

		// What is read is written and read again to the same file.
		var first, second bytes.Buffer
		if err := s.WriteJSON(&first); err != nil {
			t.Fatalf("WriteJSON = %v", err)
		}
		again, err := ReadSettings("f.json", first.Bytes())
		if err != nil {
			t.Fatalf("ReadSettings of what WriteJSON wrote = %v", err)
		}
		if err := again.WriteJSON(&second); err != nil || second.String() != first.String() {
			t.Fatalf("the settings are written as\n%s\nand after reading them as\n%s (%v)", first.String(), second.String(), err)
		}

		// The site settings are made, or else the errors are about the settings.
		e, rep, err := Read(context.Background(), "site.xml", bytes.NewReader(base))
		if err != nil {
			t.Fatal(err)
		}
		imp := New("site.xml", e, rep)
		for _, n := range []int64{30, 31} {
			id, err := imp.IDs.Assign(Object{Kind: ObjectPost, ID: n}, content.RefEntry)
			if err != nil {
				t.Fatal(err)
			}
			imp.AddRef(content.RefEntry, id, content.Ref{Type: content.TypePage, Language: s.Site.Language})
		}
		for _, n := range []int64{50, 51} {
			id, err := imp.IDs.Assign(Object{Kind: ObjectPost, ID: n}, content.RefMedia)
			if err != nil {
				t.Fatal(err)
			}
			imp.AddRef(content.RefMedia, id, content.Ref{Type: content.MediaImage})
		}
		site, err := imp.Site(s, defs)
		if err != nil {
			var errs content.Errors
			if !errors.As(err, &errs) || len(errs) == 0 || site != nil {
				t.Fatalf("Site = %v, %v, want content.Errors", site, err)
			}
			for _, e := range errs {
				if e.Path == "" || e.Msg == "" || e.File != "" || e.Line != 0 {
					t.Errorf("the error %+v is not about a place in the settings or in the schema file", *e)
				}
			}
			return
		}
		if err := site.Validate(imp.Ref); err != nil {
			t.Errorf("the site that Site made does not pass Validate: %v", err)
		}
		if _, err := site.MarshalSettings(); err != nil {
			t.Errorf("MarshalSettings = %v", err)
		}
		if schema, err := site.MarshalSchema(); err != nil {
			t.Errorf("MarshalSchema = %v", err)
		} else if read := new(content.Site); read.UnmarshalSchema(schema) != nil {
			t.Errorf("the schema file %q is not read", schema)
		}
	})
}
