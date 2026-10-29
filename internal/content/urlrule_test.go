package content

import (
	"cmp"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestParseURLRule(t *testing.T) {
	text := func(s string) rulePart { return rulePart{text: s} }
	placeholder := func(name string) rulePart { return rulePart{text: name, placeholder: true} }
	tests := []struct {
		name string
		in   string
		want []rulePart
		msg  string
	}{
		{name: "numeric", in: "/archives/{number}/", want: []rulePart{text("/archives/"), placeholder("number"), text("/")}},
		{
			name: "placeholders in a row",
			in:   "/{yyyy}{mm}{dd}/{number}.html",
			want: []rulePart{
				text("/"), placeholder("yyyy"), placeholder("mm"), placeholder("dd"), text("/"), placeholder("number"), text(".html"),
			},
		},
		{name: "text only", in: "/works/", want: []rulePart{text("/works/")}},
		{name: "placeholder only", in: "{slug}", want: []rulePart{placeholder("slug")}},
		{name: "empty", in: ""},
		{name: "empty placeholder", in: "/{}/", want: []rulePart{text("/"), placeholder(""), text("/")}},
		{name: "brace in a placeholder", in: "/{{slug}/", want: []rulePart{text("/"), placeholder("{slug"), text("/")}},
		{name: "slash in a placeholder", in: "/{a/b}/", want: []rulePart{text("/"), placeholder("a/b"), text("/")}},
		{name: "unclosed brace", in: "/{slug/", msg: `unclosed "{"`},
		{name: "closing brace", in: "/slug}/", msg: `unexpected "}"`},
		{name: "closing brace after a placeholder", in: "/{{slug}}/", msg: `unexpected "}"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseURLRule(tt.in)
			assertErrorMsg(t, err, tt.msg)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseURLRule(%q) = %#v, want %#v", tt.in, got, tt.want)
			}
		})
	}
}

func TestCheckURLRule(t *testing.T) {
	const (
		ideographicSpace = string(rune(0x3000))
		zeroWidthSpace   = string(rune(0x200B))

		usePost  = " (use {slug}, {number}, {yyyy}, {mm}, {dd}, {category})"
		usePage  = " (use {slug}, {path}, {number})"
		useWorks = " (use {slug}, {number})"
		ending   = `must end with "/" or ".html"`
		slash    = `must end with "/"`
		dots     = `dot segments ("." and "..") are not allowed`
		htmlName = `missing a name before ".html"`
		noSlug   = "missing {slug} or {number}"
		en       = `cannot start with "/en/", the path of the language en`
	)
	s := testSite()
	prefixed := testSite()
	prefixed.PrefixDefaultLanguage = true
	chinese := testSite()
	chinese.Languages = []string{"ja", "zh-Hant"}
	var (
		post     = entryRuleKind(s.Type(TypePost))
		page     = entryRuleKind(s.Type(TypePage))
		works    = entryRuleKind(s.Type("works"))
		news     = entryRuleKind(&TypeDef{Name: "news", Taxonomies: []string{TaxonomyCategory}}) // a custom type with category
		category = taxonomyRuleKind(s.Taxonomy(TaxonomyCategory))
		tag      = taxonomyRuleKind(s.Taxonomy(TaxonomyTag))
		series   = taxonomyRuleKind(s.Taxonomy("series")) // hierarchical
		author   = authorRuleKind
		archive  = archiveRuleKind
	)
	tests := []struct {
		name   string
		site   *Site // testSite() if nil
		kind   ruleKind
		in     string
		reason string // the error is `invalid URL rule "<in>": <reason>`, or `invalid archive path "<in>": <reason>` for archives
		msg    string // the whole error, if it does not quote the rule
	}{
		{name: "default rule of post", kind: post, in: DefaultPostURL},
		{name: "month and name", kind: post, in: "/{yyyy}/{mm}/{slug}/"},
		{name: "post name", kind: post, in: "/{slug}/"},
		{name: "numeric", kind: post, in: "/archives/{number}/"},
		{name: "category and name", kind: post, in: "/{category}/{slug}/"},
		{name: "date in a segment and .html", kind: post, in: "/{yyyy}{mm}{dd}/{number}.html"},
		{name: "name and .html", kind: post, in: "/{slug}.html"},
		{name: "text and a placeholder in a segment", kind: post, in: "/news-{number}/"},
		{name: "Japanese", kind: post, in: "/お知らせ/{slug}/"},
		{name: "punctuation other than ASCII", kind: post, in: "/【お知らせ】・一覧/{slug}/"},
		{name: "upper case", kind: post, in: "/Blog/{slug}/"},
		{name: "ASCII punctuation", kind: post, in: "/a.b_c-d/{slug}/"},
		{name: "default language without a path", kind: post, in: "/ja/{slug}/"},
		{name: "segment starting with a language code", kind: post, in: "/english/{slug}/"},
		{name: "language code and a placeholder in a segment", kind: post, in: "/en-{slug}/"},
		{name: "language code in another case", site: chinese, kind: post, in: "/zh-Hant/{slug}/"},
		{name: "default rule of page", kind: page, in: DefaultPageURL},
		{name: "path and .html", kind: page, in: "/{path}.html"},
		{name: "path after text", kind: page, in: "/pages/{path}/"},
		{name: "number of a page", kind: page, in: "/{number}/"},
		{name: "slug of a page", kind: page, in: "/pages/{slug}/"},
		{name: "default rule of a custom type", kind: works, in: "/works/{slug}/"},
		{name: "category of a custom type", kind: news, in: "/news/{category}/{slug}/"},
		{name: "default rule of category", kind: category, in: DefaultCategoryURL},
		{name: "category without its parents", kind: category, in: "/category/{slug}/"},
		{name: "default rule of tag", kind: tag, in: DefaultTagURL},
		{name: "hierarchical custom taxonomy", kind: series, in: "/series/{path}/"},
		{name: "default rule of author pages", kind: author, in: DefaultAuthorURL},
		{name: "author pages", kind: author, in: "/writers/{slug}/"},
		{name: "archive", kind: archive, in: "/works/"},
		{name: "Japanese archive", kind: archive, in: "/実績/"},
		{name: "archive of two segments", kind: archive, in: "/works/all/"},

		{name: "empty", kind: post, in: "", reason: `must start with "/"`},
		{name: "relative", kind: post, in: "{slug}/", reason: `must start with "/"`},
		{name: "URL", kind: post, in: "https://example.com/{slug}/", reason: `must start with "/"`},
		{name: "without an ending", kind: post, in: "/{slug}", reason: ending},
		{name: ".htm", kind: post, in: "/{slug}.htm", reason: ending},
		{name: ".HTML", kind: post, in: "/{slug}.HTML", reason: ending},
		{name: ".html for a taxonomy", kind: tag, in: "/tag/{slug}.html", reason: slash},
		{name: ".html for author pages", kind: author, in: "/author/{slug}.html", reason: slash},
		{name: "archive without an ending", kind: archive, in: "/works", reason: slash},
		{name: ".html for an archive", kind: archive, in: "/works.html", reason: slash},
		{name: "leading empty segment", kind: post, in: "//{slug}/", reason: "empty segment"},
		{name: "empty segment", kind: post, in: "/a//{slug}/", reason: "empty segment"},
		{name: "trailing empty segment", kind: post, in: "/{slug}//", reason: "empty segment"},
		{name: "empty segment of an archive", kind: archive, in: "//", reason: "empty segment"},
		{name: "dot segment", kind: post, in: "/./{slug}/", reason: dots},
		{name: "dot-dot segment", kind: post, in: "/{slug}/../", reason: dots},
		{name: "hidden segment", kind: post, in: "/.news/{slug}/", reason: `segments cannot start with "."`},
		{name: "hidden file", kind: post, in: "/{yyyy}/.{slug}.html", reason: `segments cannot start with "."`},
		{name: "hidden archive", kind: archive, in: "/.works/", reason: `segments cannot start with "."`},
		{name: "dots in a segment", kind: post, in: "/news..{slug}/"},
		{name: ".html only", kind: post, in: "/.html", reason: htmlName},
		{name: ".html after a slash", kind: post, in: "/{slug}/.html", reason: htmlName},
		{name: "unclosed brace", kind: post, in: "/{slug/", reason: `unclosed "{"`},
		{name: "closing brace", kind: post, in: "/slug}/", reason: `unexpected "}"`},
		{name: "closing brace after a placeholder", kind: post, in: "/{slug}}/", reason: `unexpected "}"`},
		// The braces are checked before the names.
		{name: "brace in a placeholder", kind: post, in: "/{{slug}}/", reason: `unexpected "}"`},
		{name: "unknown placeholder", kind: post, in: "/{year}/{slug}/", reason: `unknown placeholder "{year}"` + usePost},
		{name: "placeholder in another case", kind: post, in: "/{Slug}/", reason: `unknown placeholder "{Slug}"` + usePost},
		{name: "spaces in a placeholder", kind: post, in: "/{ slug }/", reason: `unknown placeholder "{ slug }"` + usePost},
		{name: "empty placeholder", kind: post, in: "/{}/{slug}/", reason: `unknown placeholder "{}"` + usePost},
		{name: "slash in a placeholder", kind: post, in: "/{a/b}/{slug}/", reason: `unknown placeholder "{a/b}"` + usePost},
		{name: "unknown placeholder of a taxonomy", kind: tag, in: "/tag/{name}/", reason: `unknown placeholder "{name}" (use {slug})`},
		{name: "path for post", kind: post, in: "/{path}/", reason: `placeholder "{path}" is not allowed for post` + usePost},
		{name: "date for page", kind: page, in: "/{yyyy}/{slug}/", reason: `placeholder "{yyyy}" is not allowed for page` + usePage},
		{name: "category for page", kind: page, in: "/{category}/{slug}/", reason: `placeholder "{category}" is not allowed for page` + usePage},
		{name: "date for a custom type", kind: works, in: "/works/{yyyy}/{slug}/", reason: `placeholder "{yyyy}" is not allowed for works` + useWorks},
		{
			name:   "category for a custom type without it",
			kind:   works,
			in:     "/works/{category}/{slug}/",
			reason: `placeholder "{category}" is not allowed for works` + useWorks,
		},
		{
			name:   "path for a custom type with category",
			kind:   news,
			in:     "/news/{path}/",
			reason: `placeholder "{path}" is not allowed for news (use {slug}, {number}, {category})`,
		},
		{name: "number for category", kind: category, in: "/category/{number}/", reason: `placeholder "{number}" is not allowed for category (use {slug}, {path})`},
		{name: "path for tag", kind: tag, in: "/tag/{path}/", reason: `placeholder "{path}" is not allowed for tag (use {slug})`},
		{name: "number for author pages", kind: author, in: "/author/{number}/", reason: `placeholder "{number}" is not allowed for author pages (use {slug})`},
		{name: "placeholder in an archive", kind: archive, in: "/works/{slug}/", reason: "placeholders are not allowed"},
		{name: "unknown placeholder in an archive", kind: archive, in: "/works/{x}/", reason: "placeholders are not allowed"},
		{name: "name twice", kind: post, in: "/{slug}/{slug}/", reason: "{slug} can be used only once"},
		{name: "year twice", kind: post, in: "/{yyyy}/{yyyy}/{slug}/", reason: "{yyyy} can be used only once"},
		{name: "text before path", kind: page, in: "/x{path}/", reason: "{path} must be a whole segment"},
		{name: "text after path", kind: page, in: "/{path}-x/", reason: "{path} must be a whole segment"},
		{name: "text after path before .html", kind: page, in: "/{path}-x.html", reason: "{path} must be a whole segment"},
		{name: "path and a placeholder in a segment", kind: page, in: "/{path}{number}/", reason: "{path} must be a whole segment"},
		{name: "path before .html in a directory", kind: page, in: "/{path}.html/{number}/", reason: "{path} must be a whole segment"},
		{name: "text before category", kind: post, in: "/x-{category}/{slug}/", reason: "{category} must be a whole segment"},
		{name: "category and .html", kind: post, in: "/{number}/{category}.html", reason: "{category} must be a whole segment"},
		{name: "without a name or a number", kind: post, in: "/{yyyy}/{mm}/", reason: noSlug},
		{name: "front page for post", kind: post, in: "/", reason: noSlug},
		{name: "category without a name", kind: news, in: "/news/{category}/", reason: noSlug},
		{name: "page without a name, a path or a number", kind: page, in: "/pages/", reason: "missing {slug}, {path} or {number}"},
		{name: "taxonomy without a name", kind: tag, in: "/tag/", reason: "missing {slug}"},
		{name: "hierarchical taxonomy without a name or a path", kind: series, in: "/series/", reason: "missing {slug} or {path}"},
		{name: "author pages without a name", kind: author, in: "/author/", reason: "missing {slug}"},
		{name: "name and path", kind: page, in: "/{path}/{slug}/", reason: "{slug} and {path} cannot be used together"},
		{name: "name and path of a taxonomy", kind: series, in: "/series/{path}/{slug}/", reason: "{slug} and {path} cannot be used together"},
		{name: "space", kind: post, in: "/a b/{slug}/", reason: `character ' ' (U+0020) is not allowed`},
		{name: "tab", kind: post, in: "/a\tb/{slug}/", reason: `character '\t' (U+0009) is not allowed`},
		{name: "question mark", kind: post, in: "/a?b/{slug}/", reason: `character '?' (U+003F) is not allowed`},
		{name: "hash", kind: post, in: "/a#b/{slug}/", reason: `character '#' (U+0023) is not allowed`},
		{name: "percent-encoding", kind: post, in: "/%E3%81%82/{slug}/", reason: `character '%' (U+0025) is not allowed`},
		{name: "tags of WordPress", kind: post, in: "/%year%/%postname%/", reason: `character '%' (U+0025) is not allowed`},
		{name: "backslash", kind: post, in: `/a\b/{slug}/`, reason: `character '\\' (U+005C) is not allowed`},
		{name: "tilde", kind: post, in: "/~a/{slug}/", reason: `character '~' (U+007E) is not allowed`},
		{
			name:   "ideographic space",
			kind:   post,
			in:     "/お知らせ" + ideographicSpace + "一覧/{slug}/",
			reason: "character " + strconv.QuoteRune(0x3000) + " (U+3000) is not allowed",
		},
		{
			name:   "zero width space",
			kind:   post,
			in:     "/a" + zeroWidthSpace + "b/{slug}/",
			reason: "character " + strconv.QuoteRune(0x200B) + " (U+200B) is not allowed",
		},
		{name: "after a placeholder", kind: post, in: "/{slug} /", reason: `character ' ' (U+0020) is not allowed`},
		{name: "not NFC", kind: post, in: "/cafe" + acute + "/{slug}/", reason: "not in Unicode NFC; normalize it first"},
		{name: "path of a language", kind: post, in: "/en/{slug}/", reason: en},
		{name: "path of a language for a taxonomy", kind: tag, in: "/en/{slug}/", reason: en},
		{
			name:   "path of the default language with a prefix",
			site:   prefixed,
			kind:   post,
			in:     "/ja/{slug}/",
			reason: `cannot start with "/ja/", the path of the language ja`,
		},
		{
			name:   "path of a language with a script",
			site:   chinese,
			kind:   post,
			in:     "/zh-hant/{slug}/",
			reason: `cannot start with "/zh-hant/", the path of the language zh-Hant`,
		},
		{name: "archive at the path of a language", kind: archive, in: "/en/", reason: en},
		{name: "archive at the front page", kind: archive, in: "/", reason: `cannot be "/", the path of the front page`},
		{name: "too long", kind: post, in: "/" + strings.Repeat("a", MaxTextBytes) + "/", msg: "too long: 1048578 bytes (max 1048576)"},
		{name: "invalid UTF-8", kind: post, in: "/a\xff/{slug}/", msg: "invalid UTF-8"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := tt.msg
			if tt.reason != "" {
				what := "URL rule"
				if tt.kind.archive {
					what = "archive path"
				}
				want = "invalid " + what + " " + strconv.Quote(tt.in) + ": " + tt.reason
			}
			err := checkURLRule(tt.in, tt.kind, cmp.Or(tt.site, s))
			switch {
			case want == "" && err != nil:
				t.Errorf("error = %q, want nil", err)
			case want != "" && (err == nil || err.Error() != want):
				t.Errorf("error = %v, want %q", err, want)
			}
		})
	}
}
