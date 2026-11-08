package wpimport

import (
	"cmp"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/wprest"
	"github.com/WhiteRoomLab/whiteroom/internal/wxr"
)

// Permalinks is the permalink settings of the old site (6.10.4), guessed from its links: the structures of WordPress,
// and the URL rules of the site settings (6.6.8) that stand for them.
type Permalinks struct {
	// Front is the text of the structure of the posts before its first tag, such as "/archives/", or "/" if it is unknown.
	Front      string
	Post, Page Permalink
	Author     Permalink            // the author pages
	Types      map[string]Permalink // the custom types that the schema file defines
	Taxonomies map[string]Permalink // category, tag, and the custom taxonomies that the schema file defines
	// DefaultCategory is the slug, as written, that the links of the posts without a category have.
	DefaultCategory string
}

// Permalink is what the links of one kind of page tell.
type Permalink struct {
	Structure   string // in the tags of WordPress, such as "/archives/%post_id%"; "" if it was not guessed
	Rule        string // the URL rule
	Archive     string // the path of the list of a custom type, or ""
	Links       int    // the links that were looked at
	Matched     int    // the links that Structure fits
	RuleMatched int    // the links that the structure of Rule fits; 0 if Rule is the default, which no link supports

	tokens template // Structure, from which the old URLs of the redirects are made
}

// The limits of the guess. They bound the work, and the same links always give the same guess.
const (
	permSamples    = 16   // links that candidates are made from
	permCandidates = 256  // candidates made from one link
	permCounted    = 256  // links that the candidates are counted on first
	permKept       = 8    // candidates that are then counted on all links
	permCategories = 1000 // categories that a post without one can have in its link, when candidates are made
	permDepth      = 100  // levels of a page or a term with its ancestors
	permPathBytes  = 8192 // the longest path of a link that is looked at, and the longest value of a tag
)

// tag is a tag of the permalink settings of WordPress, in the order in which candidates try them.
type tag uint8

const (
	tagLit      tag = iota // not a tag: text
	tagSlug                // %postname%, or the slug of a term, a user, or an item of a custom type
	tagPath                // %pagename%, or a slug with those of the ancestors
	tagNumber              // %post_id%
	tagCategory            // %category%: the path of a category
	tagYear                // %year%
	tagMonth               // %monthnum%
	tagDay                 // %day%
	tagAuthor              // %author%
	tagHour                // %hour%
	tagMinute              // %minute%
	tagSecond              // %second%
	numTags
)

// tagPlaceholders are the placeholders of the URL rules (6.6.8) that stand for the tags, "" for those with none.
var tagPlaceholders = [numTags]string{
	tagSlug: "{slug}", tagPath: "{path}", tagNumber: "{number}", tagCategory: "{category}",
	tagYear: "{yyyy}", tagMonth: "{mm}", tagDay: "{dd}",
}

var (
	postTags  = []tag{tagSlug, tagNumber, tagCategory, tagYear, tagMonth, tagDay, tagAuthor, tagHour, tagMinute, tagSecond}
	postNames = [numTags]string{
		tagSlug: "%postname%", tagNumber: "%post_id%", tagCategory: "%category%", tagYear: "%year%", tagMonth: "%monthnum%",
		tagDay: "%day%", tagAuthor: "%author%", tagHour: "%hour%", tagMinute: "%minute%", tagSecond: "%second%",
	}
)

func tagNames(name string, tags ...tag) [numTags]string {
	var names [numTags]string
	for _, tg := range tags {
		names[tg] = name
	}
	return names
}

// token is a tag or text in a template.
type token struct {
	tag  tag
	text string // for tagLit
}

// template is a structure: the path of a link as tags and text. Text is in one token, so no two tokens in a row are text.
type template []token

func (t template) literalChars() int {
	n := 0
	for _, tk := range t {
		n += utf8.RuneCountInString(tk.text)
	}
	return n
}

// compareTemplates orders templates that match equally many links: less text, fewer tokens, then tags before text, tags
// in their order, and text as strings.
func compareTemplates(a, b template) int {
	if c := cmp.Or(cmp.Compare(a.literalChars(), b.literalChars()), cmp.Compare(len(a), len(b))); c != 0 {
		return c
	}
	for i := range a {
		x, y := a[i], b[i]
		if (x.tag == tagLit) != (y.tag == tagLit) {
			if x.tag != tagLit {
				return -1
			}
			return 1
		}
		if c := cmp.Or(cmp.Compare(x.tag, y.tag), strings.Compare(x.text, y.text)); c != 0 {
			return c
		}
	}
	return 0
}

func (t template) key() string {
	var b strings.Builder
	for _, tk := range t {
		if tk.tag == tagLit {
			b.WriteString("L" + strconv.Itoa(len(tk.text)) + ":" + tk.text)
		} else {
			b.WriteString("T" + strconv.Itoa(int(tk.tag)) + ":")
		}
	}
	return b.String()
}

func (t template) structure(names *[numTags]string) string {
	var b strings.Builder
	for _, tk := range t {
		if tk.tag == tagLit {
			b.WriteString(tk.text)
		} else {
			b.WriteString(names[tk.tag])
		}
	}
	return b.String()
}

// frontOf returns the text of the structure t before its first tag: what WordPress calls the front.
func frontOf(t template) string {
	if len(t) > 1 && t[0].tag == tagLit && t[0].text != "" {
		return t[0].text
	}
	return "/"
}

// sample is a link with a path, and the values that its tags have.
type sample struct {
	id   int64
	path string
	vals [numTags]string // "" for none; not for tagCategory
	cats []string        // the paths of the categories of a post: the values of tagCategory
	// known is the categories of the WXR. A post with none has the slug of one of them in its link, the default category.
	known *categories
}

func (s *sample) value(tk token) (string, bool) {
	if tk.tag == tagLit {
		return tk.text, true
	}
	return s.vals[tk.tag], s.vals[tk.tag] != ""
}

// match reports whether the path of s is what t makes of the values of s. A path with the default category of a post that
// has none matches too, and match returns the slug of that category as it was written.
func (t template) match(s *sample) (ok bool, category string) {
	rest := s.path
	i := 0
	for ; i < len(t) && t[i].tag != tagCategory; i++ {
		v, ok := s.value(t[i])
		if !ok || !strings.HasPrefix(rest, v) {
			return false, ""
		}
		rest = rest[len(v):]
	}
	if i == len(t) {
		return rest == "", ""
	}
	for j := len(t) - 1; j > i; j-- {
		v, ok := s.value(t[j])
		if !ok || !strings.HasSuffix(rest, v) {
			return false, ""
		}
		rest = rest[:len(rest)-len(v)]
	}
	switch {
	case len(s.cats) > 0:
		return slices.Contains(s.cats, rest), ""
	case s.known != nil:
		if w, ok := s.known.written[rest]; ok {
			return true, w
		}
	}
	return false, ""
}

func countMatches(t template, samples []*sample) int {
	n := 0
	for _, s := range samples {
		if ok, _ := t.match(s); ok {
			n++
		}
	}
	return n
}

type option struct {
	tag tag
	n   int // the length of the value
}

// options lists, for each place in the path of s, the tags with a value that starts there. A value with a "/" in it is
// whole segments: it follows a "/", and a "/", a "." or the end follows it.
func (s *sample) options(tags []tag) [][]option {
	at := make([][]option, len(s.path))
	for i := 0; i < len(s.path); {
		for _, tg := range tags {
			if tg != tagCategory {
				at[i] = s.addOption(at[i], tg, s.vals[tg], i)
				continue
			}
			cats := s.cats
			if len(cats) == 0 && s.known != nil {
				cats = s.known.list
			}
			for _, c := range cats {
				at[i] = s.addOption(at[i], tg, c, i)
			}
		}
		_, size := utf8.DecodeRuneInString(s.path[i:])
		i += size
	}
	return at
}

func (s *sample) addOption(opts []option, tg tag, v string, i int) []option {
	if v == "" || !strings.HasPrefix(s.path[i:], v) {
		return opts
	}
	end := i + len(v)
	if strings.Contains(v, "/") {
		if i == 0 || s.path[i-1] != '/' || end < len(s.path) && s.path[end] != '/' && s.path[end] != '.' {
			return opts
		}
	}
	if slices.ContainsFunc(opts, func(o option) bool { return o.tag == tg && o.n == len(v) }) {
		return opts
	}
	return append(opts, option{tg, len(v)})
}

// generate returns the templates that make the path of s, at most limit of them. At each place it tries the tags with a
// value that starts there, in their order, and then goes on by one character of text, so that the first templates have the
// most tags.
func generate(s *sample, tags []tag, limit int) []template {
	if limit <= 0 {
		return nil
	}
	g := &generator{path: s.path, at: s.options(tags), limit: limit}
	g.walk(0, 0)
	return g.out
}

type generator struct {
	path  string
	at    [][]option
	limit int
	cur   []genToken // the tokens up to the place of walk
	out   []template
}

// genToken is a tag or text that makes path[from:to].
type genToken struct {
	tag      tag
	from, to int
}

func (g *generator) walk(i int, used uint16) {
	if i == len(g.path) {
		t := make(template, len(g.cur))
		for k, c := range g.cur {
			t[k] = token{tag: c.tag}
			if c.tag == tagLit {
				t[k].text = g.path[c.from:c.to]
			}
		}
		g.out = append(g.out, t)
		return
	}
	for _, o := range g.at[i] {
		if used&(1<<o.tag) != 0 {
			continue
		}
		g.cur = append(g.cur, genToken{tag: o.tag, from: i, to: i + o.n})
		g.walk(i+o.n, used|1<<o.tag)
		g.cur = g.cur[:len(g.cur)-1]
		if len(g.out) >= g.limit {
			return
		}
	}
	_, size := utf8.DecodeRuneInString(g.path[i:])
	if n := len(g.cur); n > 0 && g.cur[n-1].tag == tagLit {
		to := g.cur[n-1].to
		g.cur[n-1].to = i + size
		g.walk(i+size, used)
		g.cur[n-1].to = to
		return
	}
	g.cur = append(g.cur, genToken{tag: tagLit, from: i, to: i + size})
	g.walk(i+size, used)
	g.cur = g.cur[:len(g.cur)-1]
}

// candidates returns the templates of the first links, without repeats.
func candidates(samples []*sample, tags []tag) []template {
	var out []template
	seen := make(map[string]bool)
	for _, s := range samples[:min(len(samples), permSamples)] {
		for _, t := range generate(s, tags, permCandidates) {
			if k := t.key(); !seen[k] {
				seen[k] = true
				out = append(out, t)
			}
		}
	}
	return out
}

type scored struct {
	t template
	n int // the links that t matches
}

func sortScored(list []scored) {
	slices.SortStableFunc(list, func(a, b scored) int {
		return cmp.Or(cmp.Compare(b.n, a.n), compareTemplates(a.t, b.t))
	})
}

// rank counts the candidates on up to permCounted links spread over the links, and then the best of them on all the
// links, and returns those, best first: the most links matched, then the order of compareTemplates.
func rank(cands []template, samples []*sample) []scored {
	counted := samples
	if len(samples) > permCounted {
		counted = make([]*sample, permCounted)
		for i := range counted {
			counted[i] = samples[i*len(samples)/permCounted]
		}
	}
	list := make([]scored, len(cands))
	for i, t := range cands {
		list[i] = scored{t, countMatches(t, counted)}
	}
	sortScored(list)
	list = list[:min(len(list), permKept)]
	if len(counted) != len(samples) {
		for i := range list {
			list[i].n = countMatches(list[i].t, samples)
		}
		sortScored(list)
	}
	return list
}

// linkPath returns the path of link in the site at home, in the canonical form of the from of a redirect, such as
// "/2026/10/hello/", or false if link has no path of its own in the site: it is on another host or port, has a user name,
// or has a query or a fragment, as the links of the Plain permalinks do. A path of more than permPathBytes is not one.
func linkPath(home *url.URL, link string) (string, bool) {
	link = strings.TrimSpace(link)
	if home == nil || strings.ContainsAny(link, "?#") {
		return "", false
	}
	u, err := url.Parse(link)
	if err != nil || u.User != nil || u.Opaque != "" || (u.Scheme != "http" && u.Scheme != "https") ||
		!strings.EqualFold(u.Hostname(), home.Hostname()) || !samePort(u, home) {
		return "", false
	}
	base := strings.TrimRight(content.CanonicalFrom(cmp.Or(home.RawPath, home.EscapedPath())), "/")
	path := content.CanonicalFrom(cmp.Or(u.RawPath, u.EscapedPath()))
	switch {
	case path == base:
		return "/", true
	case strings.HasPrefix(path, base+"/") && len(path)-len(base) <= permPathBytes:
		return path[len(base):], true
	}
	return "", false
}

// samePort reports whether a and b have the same port, or both the default port of their schemes.
func samePort(a, b *url.URL) bool {
	pa, da, oka := portOf(a)
	pb, db, okb := portOf(b)
	return oka && okb && (pa == pb || da && db)
}

func portOf(u *url.URL) (port int, isDefault, ok bool) {
	def := 443
	if u.Scheme == "http" {
		def = 80
	}
	if u.Port() == "" {
		return def, true, true
	}
	n, err := strconv.Atoi(u.Port())
	return n, n == def, err == nil
}

// slugValue returns slug, a slug as it is written in the WXR and the REST API, in the form that it has in a link path, or
// "" if a link cannot have it. Links are made of the slug as it is written, so it is not normalized as an imported slug is.
func slugValue(slug string) string {
	if slug == "" || strings.ContainsAny(slug, "?#") {
		return ""
	}
	if v := content.CanonicalFrom("/" + slug)[1:]; len(v) <= permPathBytes {
		return v
	}
	return ""
}

// dateParts returns the parts of a post_date of WordPress, "2006-01-02 15:04:05": the year, month, day, hour, minute, second.
func dateParts(s string) (parts [6]string, ok bool) {
	if len(s) != len("2006-01-02 15:04:05") || s[4] != '-' || s[7] != '-' || s[10] != ' ' || s[13] != ':' || s[16] != ':' {
		return parts, false
	}
	for i := range len(s) {
		switch i {
		case 4, 7, 10, 13, 16:
		default:
			if s[i] < '0' || s[i] > '9' {
				return parts, false
			}
		}
	}
	return [6]string{s[0:4], s[5:7], s[8:10], s[11:13], s[14:16], s[17:19]}, true
}

// tree is the pages or the terms of one kind, from which the paths with the slugs of the ancestors are made.
type tree struct {
	slug   map[int64]string // the slug of each, in the form of a link path
	parent map[int64]int64  // 0 for none
}

func newTree() *tree {
	return &tree{slug: make(map[int64]string), parent: make(map[int64]int64)}
}

func (tr *tree) add(id int64, slug string, parent int64) {
	tr.slug[id], tr.parent[id] = slug, parent
}

// path returns the slugs of id and its ancestors from the top, joined with "/", as WordPress makes them: it stops at a
// parent that does not exist, and leaves out those without a name. It returns "" if id has no slug, more than permDepth
// levels (which a loop has), or more than permPathBytes.
func (tr *tree) path(id int64) string {
	self := tr.slug[id]
	if self == "" {
		return ""
	}
	slugs := []string{self}
	size := len(self)
	for cur, depth := id, 1; ; depth++ {
		p := tr.parent[cur]
		if p == 0 {
			break
		}
		s, ok := tr.slug[p]
		if !ok {
			break
		}
		if depth >= permDepth {
			return ""
		}
		if s != "" {
			if size += len(s) + 1; size > permPathBytes {
				return ""
			}
			slugs = append(slugs, s)
		}
		cur = p
	}
	slices.Reverse(slugs)
	return strings.Join(slugs, "/")
}

type categories struct {
	bySlug  map[string]*wxr.Term // by the slug as written
	written map[string]string    // the slug in the form of a link path -> the slug as written
	order   map[string]int       // the slug as written -> the place of the category in the WXR
	list    []string             // the slugs in the form of a link path, at most permCategories of them
	paths   map[string]string
}

func newCategories(e *wxr.Export) *categories {
	c := &categories{
		bySlug: make(map[string]*wxr.Term), written: make(map[string]string), order: make(map[string]int),
		paths: make(map[string]string),
	}
	for i := range e.Terms {
		t := &e.Terms[i]
		if t.Taxonomy != "category" {
			continue
		}
		if _, ok := c.bySlug[t.Slug]; ok {
			continue
		}
		c.bySlug[t.Slug], c.order[t.Slug] = t, i
		if v := slugValue(t.Slug); v != "" {
			if _, ok := c.written[v]; !ok {
				c.written[v] = t.Slug
				if len(c.list) < permCategories {
					c.list = append(c.list, v)
				}
			}
		}
	}
	return c
}

// path returns the path of the category with the slug as written, as %category% has it: the slugs of its ancestors, and its
// own, joined with "/". It returns "" if there is none, as tree.path does.
func (c *categories) path(slug string) string {
	if p, ok := c.paths[slug]; ok {
		return p
	}
	p := ""
	if t := c.bySlug[slug]; t != nil {
		p = c.walk(t)
	}
	c.paths[slug] = p
	return p
}

func (c *categories) walk(t *wxr.Term) string {
	self := slugValue(t.Slug)
	if self == "" {
		return ""
	}
	slugs := []string{self}
	size := len(self)
	for depth := 1; t.Parent != ""; depth++ {
		p := c.bySlug[t.Parent]
		if p == nil {
			break
		}
		if depth >= permDepth {
			return ""
		}
		if s := slugValue(p.Slug); s != "" {
			if size += len(s) + 1; size > permPathBytes {
				return ""
			}
			slugs = append(slugs, s)
		}
		t = p
	}
	slices.Reverse(slugs)
	return strings.Join(slugs, "/")
}

// permTarget is one kind of page whose URL rule is guessed: posts, pages, a custom type, a taxonomy, or author pages.
type permTarget struct {
	pointer string                      // of its rule in the schema file
	what    string                      // in the messages
	tags    []tag                       // the tags it can have, in their order
	names   [numTags]string             // the tags as WordPress writes them
	html    bool                        // the rule can end with ".html"
	set     func(*content.Site, string) // puts a rule, or a path, into a site
	site    *content.Site               // where rules are checked
	def     string                      // the rule when no link tells
	missing bool                        // the REST API did not give its links
}

// check returns why rule cannot be the rule of tg in the site settings, or nil.
func (tg *permTarget) check(rule string) error {
	tg.set(tg.site, rule)
	var errs content.Errors
	if errors.As(tg.site.Validate(nil), &errs) {
		for _, e := range errs {
			if e.Path == tg.pointer {
				return errors.New(e.Msg)
			}
		}
	}
	return nil
}

// rule returns the URL rule that stands for t, or why there is none.
func (tg *permTarget) rule(t template) (string, error) {
	var b strings.Builder
	for _, tk := range t {
		if tk.tag == tagLit {
			b.WriteString(tk.text)
			continue
		}
		ph := tagPlaceholders[tk.tag]
		if ph == "" {
			return "", fmt.Errorf("%s has no placeholder", tg.names[tk.tag])
		}
		b.WriteString(ph)
	}
	rule := b.String()
	if !strings.HasSuffix(rule, "/") && (!tg.html || !strings.HasSuffix(rule, ".html")) {
		last := rule[strings.LastIndexByte(rule, '/')+1:]
		if i := strings.LastIndexByte(last, '.'); i >= 0 {
			ends := `"/"`
			if tg.html {
				ends = `"/" or ".html"`
			}
			return "", fmt.Errorf("a URL rule must end with %s, not %q", ends, last[i:])
		}
		rule += "/"
	}
	if err := tg.check(rule); err != nil {
		return "", err
	}
	return rule, nil
}

// newRuleSite returns a site that has the definitions of defs, in which a rule is checked as the site settings check it.
// It has one language, so the path of a language does not overlap with a rule.
func newRuleSite(defs *content.Site) *content.Site {
	s := &content.Site{
		Languages: []string{"en"}, DefaultLanguage: "en",
		Types: slices.Clone(defs.Types), Taxonomies: slices.Clone(defs.Taxonomies),
	}
	for i := range s.Types {
		s.Types[i].Fields = nil
	}
	return s
}

func typeIndex(s *content.Site, name string) int {
	i := slices.IndexFunc(s.Types, func(t content.TypeDef) bool { return t.Name == name })
	if i < 0 {
		s.Types = append(s.Types, content.TypeDef{Name: name})
		i = len(s.Types) - 1
	}
	return i
}

func typeURL(name string) func(*content.Site, string) {
	return func(s *content.Site, v string) { s.Types[typeIndex(s, name)].URL = v }
}

func typeArchive(name string) func(*content.Site, string) {
	return func(s *content.Site, v string) { s.Types[typeIndex(s, name)].Archive = v }
}

func taxonomyURL(name string) func(*content.Site, string) {
	return func(s *content.Site, v string) {
		i := slices.IndexFunc(s.Taxonomies, func(t content.TaxonomyDef) bool { return t.Name == name })
		if i < 0 {
			s.Taxonomies = append(s.Taxonomies, content.TaxonomyDef{Name: name})
			i = len(s.Taxonomies) - 1
		}
		s.Taxonomies[i].URL = v
	}
}

func setAuthorURL(s *content.Site, v string) { s.AuthorURL = v }

var pointerEscaper = strings.NewReplacer("~", "~0", "/", "~1")

// permInference is the state of one InferPermalinks.
type permInference struct {
	imp      *Import
	defs     *content.Site
	home     *url.URL // of the links in the WXR
	restHome *url.URL // of the links in the REST API
	known    *categories
	authors  map[string]string // the slug that the login of each author has in links
	front    string
}

func parseHome(s string) *url.URL {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Opaque != "" {
		return nil
	}
	return u
}

func newPermInference(imp *Import, defs *content.Site) *permInference {
	e := imp.WXR
	in := &permInference{imp: imp, defs: defs, known: newCategories(e), front: "/"}
	in.home = parseHome(e.Channel.Link)
	if in.home == nil {
		in.home = parseHome(e.Channel.BaseBlogURL)
	}
	in.restHome = in.home
	if r := imp.REST; r != nil {
		if h := parseHome(r.Index.Home); h != nil {
			in.restHome = h
		}
	}
	// The slug of an author in links is the one of the REST API, as in the author profiles.
	cands := imp.authorCandidates()
	imp.matchUsers(cands)
	in.authors = make(map[string]string, len(cands))
	for _, c := range cands {
		raw := nicename(c.login)
		if c.user != nil && c.user.Slug != "" {
			raw = c.user.Slug
		}
		in.authors[c.login] = slugValue(raw)
	}
	return in
}

// InferPermalinks guesses the permalink settings of the old site (6.10.4) from the links of the items of the WXR, and
// of the terms and the users of the REST API, and returns the URL rules for the site settings with the structures of
// WordPress that they come from. defs is the schema file of the site, as UnmarshalSchema reads it: the custom types and
// taxonomies that it defines are guessed too, and nil defines none. Each guess is recorded in the report as an entry to
// review, each time it is called. The error is for what should not happen, and for a REST API that is not that of the
// site of the WXR, or does not say whose it is; nothing is recorded then.
func (imp *Import) InferPermalinks(defs *content.Site) (*Permalinks, error) {
	if imp.WXR == nil {
		return nil, errors.New("the WXR is needed to guess the permalinks")
	}
	if err := imp.checkREST(); err != nil {
		return nil, err
	}
	if defs == nil {
		defs = &content.Site{}
	}
	in := newPermInference(imp, defs)
	pl := &Permalinks{Types: make(map[string]Permalink), Taxonomies: make(map[string]Permalink)}

	// The posts come first: the front of their structure is in the default rules of the other pages.
	posts := in.postSamples()
	tg := in.target("/types/post/url", "posts", postTags, postNames, typeURL(content.TypePost))
	tg.def = content.DefaultPostURL
	pl.Post = in.infer(tg, posts)
	pl.Front = frontOf(pl.Post.tokens)
	in.front = pl.Front
	in.defaultCategory(pl, posts)

	tg = in.target("/types/page/url", "pages", []tag{tagPath}, tagNames("%pagename%", tagPath), typeURL(content.TypePage))
	tg.def = in.fallback(tg, in.root()+"{path}/", content.DefaultPageURL)
	pl.Page = in.infer(tg, in.itemSamples(content.TypePage, true, true))

	for _, def := range defs.Types {
		name := def.Name
		if _, done := pl.Types[name]; done || name == content.TypePost || name == content.TypePage || typeName(name, defs) != name {
			continue
		}
		pl.Types[name] = in.customType(name)
	}
	for _, name := range in.taxonomyNames() {
		pl.Taxonomies[name] = in.taxonomy(name)
	}

	tg = in.target("/authorUrl", "author pages", []tag{tagSlug}, tagNames("%author%", tagSlug), setAuthorURL)
	tg.html = false
	tg.missing = !in.usersAvailable()
	tg.def = in.fallback(tg, in.front+"author/{slug}/", content.DefaultAuthorURL)
	pl.Author = in.infer(tg, in.userSamples())
	return pl, nil
}

// target returns the target of a rule at pointer, which can end with ".html" like the rules of entries.
func (in *permInference) target(pointer, what string, tags []tag, names [numTags]string, set func(*content.Site, string)) *permTarget {
	return &permTarget{pointer: pointer, what: what, tags: tags, names: names, html: true, set: set, site: newRuleSite(in.defs)}
}

// fallback returns withFront, the default rule of WordPress for the front of the posts, or plain, the default rule of the
// site settings, if withFront cannot be a rule.
func (in *permInference) fallback(tg *permTarget, withFront, plain string) string {
	if tg.check(withFront) == nil {
		return withFront
	}
	return plain
}

// root returns the text that WordPress puts before the pages: "/index.php/" if the permalinks go through index.php.
func (in *permInference) root() string {
	if strings.HasPrefix(in.front, "/index.php/") {
		return "/index.php/"
	}
	return "/"
}

func (in *permInference) entry(pointer, format string, args ...any) {
	in.imp.Report.Add(Entry{
		Kind:    Review,
		Source:  ChannelSource(in.imp.File, in.imp.WXR),
		Message: fmt.Sprintf(format, args...),
		Target:  schemaTarget(pointer),
	})
}

func notFollowing(n int) string {
	if n == 1 {
		return "1 link does not follow it, so the import will need a redirect for it"
	}
	return fmt.Sprintf("%d links do not follow it, so the import will need redirects for them", n)
}

func follow(n int) string {
	if n == 1 {
		return "follows"
	}
	return "follow"
}

// infer guesses the structure of the links in samples, and the rule that stands for it, and records the guess.
func (in *permInference) infer(tg *permTarget, samples []*sample) Permalink {
	p := Permalink{Rule: tg.def, Links: len(samples)}
	if len(samples) == 0 {
		if tg.missing {
			in.entry(tg.pointer, "the REST API did not give the links of %s; the URL rule is %q, the default of WordPress for the front %q",
				tg.what, tg.def, in.front)
		} else {
			in.entry(tg.pointer, "no link of %s has a path (the permalinks may be Plain); the URL rule is the default %q", tg.what, tg.def)
		}
		return p
	}

	ranked := rank(candidates(samples, tg.tags), samples)
	best := ranked[0]
	p.Structure, p.Matched, p.tokens = best.t.structure(&tg.names), best.n, best.t
	rule, err := tg.rule(best.t)
	if err == nil {
		p.Rule, p.RuleMatched = rule, best.n
		msg := fmt.Sprintf("inferred the permalink structure %q of %s from %d of %s; the URL rule is %q",
			p.Structure, tg.what, p.Matched, count(p.Links, "link"), p.Rule)
		if p.Matched < p.Links {
			msg += "; " + notFollowing(p.Links-p.Matched)
		}
		in.entry(tg.pointer, "%s", msg)
		return p
	}

	// The best structure cannot be a rule: the rule is that of the next best that can be. Each fits at least the link that
	// it was made from.
	for _, alt := range ranked[1:] {
		if r, e := tg.rule(alt.t); e == nil {
			p.Rule, p.RuleMatched = r, alt.n
			break
		}
	}
	end := fmt.Sprintf("the URL rule is the default %q", p.Rule)
	if p.RuleMatched > 0 {
		end = fmt.Sprintf("the URL rule is %q, which %d of them %s", p.Rule, p.RuleMatched, follow(p.RuleMatched))
	}
	in.entry(tg.pointer, "the permalink structure %q of %s, which %d of %s %s, cannot be a URL rule: %v; %s",
		p.Structure, tg.what, p.Matched, count(p.Links, "link"), follow(p.Matched), err, end)
	return p
}

// defaultCategory finds the default category of WordPress, which %category% takes for a post without a category, from the
// links of the posts that have none, if the structure of the posts has the tag.
func (in *permInference) defaultCategory(pl *Permalinks, posts []*sample) {
	const pointer = "/types/post/url"
	if !slices.ContainsFunc(pl.Post.tokens, func(tk token) bool { return tk.tag == tagCategory }) {
		return
	}
	tally := make(map[string]int)
	for _, s := range posts {
		if ok, c := pl.Post.tokens.match(s); ok && c != "" {
			tally[c]++
		}
	}
	best, n := "", 0
	for c, k := range tally {
		if k > n || k == n && in.known.order[c] < in.known.order[best] {
			best, n = c, k
		}
	}
	if best != "" {
		pl.DefaultCategory = best
		in.entry(pointer, "posts without a category had %q in their links; it is the default category", best)
		return
	}

	// No link tells it: it is the category with the ID 1, which WordPress has by default, if the WXR has it.
	without := 0
	for i := range in.imp.WXR.Items {
		it := &in.imp.WXR.Items[i]
		if it.Type == "post" && it.Status != "trash" && it.Status != "auto-draft" && !hasCategory(it) {
			without++
		}
	}
	if without == 0 {
		return
	}
	subject := fmt.Sprintf("%d posts have", without)
	if without == 1 {
		subject = "1 post has"
	}
	if t := in.imp.WXR.Term("category", 1); t != nil && t.Slug != "" {
		pl.DefaultCategory = t.Slug
		in.entry(pointer, "%s no category, which {category} needs; the default category is %q, which WordPress has by default (term 1)",
			subject, t.Slug)
		return
	}
	in.entry(pointer, "%s no category, which {category} needs; set the default category", subject)
}

func hasCategory(it *wxr.Item) bool {
	return slices.ContainsFunc(it.Terms, func(r wxr.TermRef) bool { return r.Taxonomy == "category" })
}

// customType guesses the permalink of the custom type name that the schema file defines.
func (in *permInference) customType(name string) Permalink {
	hier := false
	if rt := in.restType(name); rt != nil {
		hier = rt.Hierarchical
	}
	for i := range in.imp.WXR.Items {
		if it := &in.imp.WXR.Items[i]; it.Type == name && it.Parent != 0 {
			hier = true
		}
	}
	tags := []tag{tagSlug}
	if hier {
		tags = append(tags, tagPath)
	}
	tg := in.target("/types/"+pointerEscaper.Replace(name)+"/url", "the post type "+typeText(name), tags,
		tagNames("%"+name+"%", tagSlug, tagPath), typeURL(name))
	tg.def = in.fallback(tg, in.front+name+"/{slug}/", "/"+name+"/{slug}/")
	p := in.infer(tg, in.itemSamples(name, hier, false))
	in.archive(name, &p)
	return p
}

func (in *permInference) restType(name string) *wprest.Type {
	if r := in.imp.REST; r != nil {
		for i := range r.Types {
			if r.Types[i].Slug == name {
				return &r.Types[i]
			}
		}
	}
	return nil
}

// archive finds the path of the list of the custom type name, which the REST API says whether it has, and puts it in p.
func (in *permInference) archive(name string, p *Permalink) {
	pointer := "/types/" + pointerEscaper.Replace(name) + "/archive"
	what := "the post type " + typeText(name)
	rt := in.restType(name)
	if rt == nil || !rt.HasArchive.Set {
		in.entry(pointer, "the REST API did not tell whether %s had an archive; set its archive if it had one", what)
		return
	}
	a := rt.HasArchive
	if !a.On {
		return
	}
	var path string
	switch {
	case a.Slug == "":
		// The list has the slug of the type that the rule has.
		path = p.Rule
		if i := strings.IndexByte(path, '{'); i >= 0 {
			path = path[:i]
		}
		if path = path[:strings.LastIndexByte(path, '/')+1]; path == "/" {
			path = ""
		}
	case in.front != "/" && strings.HasPrefix(p.Rule, in.front):
		path = in.front + a.Slug + "/"
	default:
		path = "/" + a.Slug + "/"
	}
	tg := &permTarget{pointer: pointer, set: typeArchive(name), site: newRuleSite(in.defs)}
	err := errors.New("the URL rule has no directory to take the path from")
	if path != "" {
		err = tg.check(path)
	}
	if err != nil {
		in.entry(pointer, "%s had an archive, but its path cannot be told: %v; set its archive if it should have one", what, err)
		return
	}
	p.Archive = path
	in.entry(pointer, "%s had an archive; its path is %q", what, path)
}

// taxonomyNames returns the taxonomies to guess: category, tag, and the custom taxonomies that the schema file defines.
func (in *permInference) taxonomyNames() []string {
	names := []string{content.TaxonomyCategory, content.TaxonomyTag}
	for _, def := range in.defs.Taxonomies {
		if taxonomyName(def.Name, in.defs) == def.Name && !slices.Contains(names, def.Name) {
			names = append(names, def.Name)
		}
	}
	return names
}

// taxonomy guesses the permalink of the taxonomy name from the links of its terms.
func (in *permInference) taxonomy(name string) Permalink {
	wp := name
	if name == content.TaxonomyTag {
		wp = "post_tag"
	}
	var tags []tag
	switch name {
	case content.TaxonomyCategory:
		tags = []tag{tagPath}
	case content.TaxonomyTag:
		tags = []tag{tagSlug}
	default:
		tags = []tag{tagSlug}
		if r := in.imp.REST; r != nil && slices.ContainsFunc(r.Taxonomies, func(t wprest.Taxonomy) bool { return t.Slug == wp && t.Hierarchical }) {
			tags = append(tags, tagPath)
		}
	}
	tg := in.target("/taxonomies/"+pointerEscaper.Replace(name)+"/url", "the taxonomy "+typeText(name), tags,
		tagNames("%"+wp+"%", tagSlug, tagPath), taxonomyURL(name))
	tg.html = false
	tg.missing = !in.termsAvailable(wp)
	switch name {
	case content.TaxonomyCategory:
		tg.def = in.fallback(tg, in.front+"category/{path}/", content.DefaultCategoryURL)
	case content.TaxonomyTag:
		tg.def = in.fallback(tg, in.front+"tag/{slug}/", content.DefaultTagURL)
	default:
		tg.def = in.fallback(tg, in.front+name+"/{slug}/", "/"+name+"/{slug}/")
	}
	return in.infer(tg, in.termSamples(wp, slices.Contains(tags, tagPath)))
}

func (in *permInference) termsAvailable(wp string) bool {
	r := in.imp.REST
	if r == nil || slices.ContainsFunc(r.Unavailable, func(u wprest.Unavailable) bool { return u.Taxonomy == wp }) {
		return false
	}
	return wp == "category" || wp == "post_tag" || slices.ContainsFunc(r.Taxonomies, func(t wprest.Taxonomy) bool { return t.Slug == wp })
}

func (in *permInference) usersAvailable() bool {
	r := in.imp.REST
	return r != nil && !slices.ContainsFunc(r.Unavailable, func(u wprest.Unavailable) bool { return u.Route == restUsersRoute })
}

func sortSamples(samples []*sample) {
	slices.SortStableFunc(samples, func(a, b *sample) int { return cmp.Compare(a.id, b.id) })
}

// postSamples returns the posts of the WXR with a link that has a path, in the order of their IDs.
func (in *permInference) postSamples() []*sample {
	var out []*sample
	for i := range in.imp.WXR.Items {
		it := &in.imp.WXR.Items[i]
		if it.Type != "post" {
			continue
		}
		path, ok := linkPath(in.home, it.Link)
		if !ok {
			continue
		}
		s := &sample{id: it.ID, path: path, known: in.known}
		s.vals[tagSlug] = slugValue(it.Name)
		s.vals[tagNumber] = strconv.FormatInt(it.ID, 10)
		s.vals[tagAuthor] = in.authors[it.Creator]
		if d, ok := dateParts(it.Date); ok {
			s.vals[tagYear], s.vals[tagMonth], s.vals[tagDay] = d[0], d[1], d[2]
			s.vals[tagHour], s.vals[tagMinute], s.vals[tagSecond] = d[3], d[4], d[5]
		}
		for _, r := range it.Terms {
			if r.Taxonomy != "category" {
				continue
			}
			c := slugValue(r.Slug)
			if in.known.bySlug[r.Slug] != nil {
				c = in.known.path(r.Slug)
			}
			if c != "" {
				s.cats = append(s.cats, c)
			}
		}
		out = append(out, s)
	}
	sortSamples(out)
	return out
}

// itemSamples returns the items of the type typ with a link that has a path, in the order of their IDs: with the path of
// the ancestors if withPath. The front page, which has the path "/", is left out if skipFront.
func (in *permInference) itemSamples(typ string, withPath, skipFront bool) []*sample {
	var tr *tree
	if withPath {
		tr = newTree()
		for i := range in.imp.WXR.Items {
			if it := &in.imp.WXR.Items[i]; it.Type == typ {
				tr.add(it.ID, slugValue(it.Name), it.Parent)
			}
		}
	}
	var out []*sample
	for i := range in.imp.WXR.Items {
		it := &in.imp.WXR.Items[i]
		if it.Type != typ {
			continue
		}
		path, ok := linkPath(in.home, it.Link)
		if !ok || skipFront && path == "/" {
			continue
		}
		s := &sample{id: it.ID, path: path}
		s.vals[tagSlug] = slugValue(it.Name)
		if tr != nil {
			s.vals[tagPath] = tr.path(it.ID)
		}
		out = append(out, s)
	}
	sortSamples(out)
	return out
}

// termSamples returns the terms of the taxonomy wp of WordPress, from the REST API, with a link that has a path, in the
// order of their IDs: with the path of the ancestors if withPath.
func (in *permInference) termSamples(wp string, withPath bool) []*sample {
	r := in.imp.REST
	if r == nil {
		return nil
	}
	var tr *tree
	if withPath {
		tr = newTree()
		for i := range r.Terms {
			if t := &r.Terms[i]; t.Taxonomy == wp {
				tr.add(t.ID, slugValue(t.Slug), t.Parent)
			}
		}
	}
	var out []*sample
	for i := range r.Terms {
		t := &r.Terms[i]
		if t.Taxonomy != wp {
			continue
		}
		path, ok := linkPath(in.restHome, t.Link)
		if !ok {
			continue
		}
		s := &sample{id: t.ID, path: path}
		s.vals[tagSlug] = slugValue(t.Slug)
		if tr != nil {
			s.vals[tagPath] = tr.path(t.ID)
		}
		out = append(out, s)
	}
	sortSamples(out)
	return out
}

func (in *permInference) userSamples() []*sample {
	r := in.imp.REST
	if r == nil || !in.usersAvailable() {
		return nil
	}
	var out []*sample
	for i := range r.Users {
		u := &r.Users[i]
		path, ok := linkPath(in.restHome, u.Link)
		if !ok {
			continue
		}
		s := &sample{id: u.ID, path: path}
		s.vals[tagSlug] = slugValue(u.Slug)
		out = append(out, s)
	}
	sortSamples(out)
	return out
}
