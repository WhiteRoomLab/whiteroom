package wpimport

import (
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/phpserialize"
	"github.com/WhiteRoomLab/whiteroom/internal/wxr"
)

const (
	seoYoast  = "yoast"
	seoAIOSEO = "aioseo"
)

// yoastPrimaryCategory is the postmeta of Yoast SEO that names the primary category of a post.
const yoastPrimaryCategory = "_yoast_wpseo_primary_category"

// seoPlugin is an SEO plugin that keeps the settings of each post in its postmeta (6.10.5). A key that is "" is a setting
// that the plugin does not keep there.
type seoPlugin struct {
	name        string // the value of seo.plugin
	label       string // the name in the report
	title       string
	description string
	canonical   string
	noindex     seoKey // the value of the key that says noindex
	imageID     string // the ID of the attachment of the image for sharing
	imageURL    string
	// primary names the primary category of a post, which the link of an item does not show. An item that has only this
	// tells that the site had the plugin, as the others do.
	primary string
	vars    seoVars
	// notImported are the postmeta that whiteroom has no setting for: the report counts the items that have each.
	notImported []seoKey
}

// seoKey is a postmeta key, and what its value must be to count: any value that is not empty, if is is nil.
type seoKey struct {
	key string
	is  valueTest
}

// valueTest tells whether the postmeta m, whose value is not empty and has no white space around it, says something. The
// error is for a value that cannot be read.
type valueTest func(m wxr.Meta) (bool, error)

func isValue(want string) valueTest {
	return func(m wxr.Meta) (bool, error) { return m.Value == want, nil }
}

// listHas tests for a value that is a list of words with commas between.
func listHas(words ...string) valueTest {
	return func(m wxr.Meta) (bool, error) {
		for item := range strings.SplitSeq(m.Value, ",") {
			if hasWord(words, item) {
				return true, nil
			}
		}
		return false, nil
	}
}

// phpListHas tests for a value that is a PHP array of words.
func phpListHas(words ...string) valueTest {
	return func(m wxr.Meta) (bool, error) {
		v, err := Unserialize(nil, Source{}, wxr.Meta{Key: m.Key, Value: m.Value})
		if err != nil {
			return false, err
		}
		if v.Kind != phpserialize.Array {
			return false, fmt.Errorf("the value of %q is not a list", m.Key)
		}
		for _, e := range v.Array {
			if hasWord(words, e.Value.Str) {
				return true, nil
			}
		}
		return false, nil
	}
}

func hasWord(words []string, item string) bool {
	item = strings.TrimSpace(item)
	return slices.ContainsFunc(words, func(w string) bool { return strings.EqualFold(item, w) })
}

// seoVars is how a plugin writes the variables in a title and a description, and what the ones that the import knows are
// called.
type seoVars struct {
	syntax varSyntax
	// fold is set if the plugin reads the names of variables without regard to case.
	fold bool
	// The names of the variables for the title of the entry, the name of the site, the separator, the page number, and the
	// excerpt.
	title, site, sep, page, excerpt []string
}

type varSyntax uint8

const (
	syntaxNone       varSyntax = iota // no variables
	syntaxDouble                      // %%name%%
	syntaxSingle                      // %name% and %name(argument)%
	syntaxUnderscore                  // %_name_%
	syntaxHash                        // #name
)

type varKind uint8

const (
	varOther varKind = iota
	varTitle
	varSite
	varSep
	varPage
	varExcerpt
)

func (v *seoVars) kind(name string) varKind {
	if v.fold {
		name = strings.ToLower(name)
	}
	for _, k := range []struct {
		names []string
		kind  varKind
	}{{v.title, varTitle}, {v.site, varSite}, {v.sep, varSep}, {v.page, varPage}, {v.excerpt, varExcerpt}} {
		if slices.Contains(k.names, name) {
			return k.kind
		}
	}
	return varOther
}

// seoPlugins are the SEO plugins whose postmeta are read, in the order that decides which is taken if as many items have the
// settings of two.
var seoPlugins = []seoPlugin{
	{
		name: seoYoast, label: "Yoast SEO",
		title: "_yoast_wpseo_title", description: "_yoast_wpseo_metadesc", canonical: "_yoast_wpseo_canonical",
		noindex: seoKey{"_yoast_wpseo_meta-robots-noindex", isValue("1")},
		imageID: "_yoast_wpseo_opengraph-image-id", imageURL: "_yoast_wpseo_opengraph-image",
		primary: yoastPrimaryCategory,
		vars: seoVars{
			syntax: syntaxDouble, title: []string{"title"}, site: []string{"sitename"}, sep: []string{"sep"},
			page: []string{"page", "pagenumber", "pagetotal"}, excerpt: []string{"excerpt", "excerpt_only"},
		},
		notImported: []seoKey{
			{"_yoast_wpseo_opengraph-title", nil}, {"_yoast_wpseo_opengraph-description", nil},
			{"_yoast_wpseo_twitter-title", nil}, {"_yoast_wpseo_twitter-description", nil}, {"_yoast_wpseo_twitter-image", nil},
			{"_yoast_wpseo_meta-robots-nofollow", isValue("1")}, {"_yoast_wpseo_meta-robots-adv", nil},
			{"_yoast_wpseo_bctitle", nil}, {"_yoast_wpseo_redirect", nil},
		},
	},
	{
		name: "rankmath", label: "Rank Math",
		title: "rank_math_title", description: "rank_math_description", canonical: "rank_math_canonical_url",
		noindex: seoKey{"rank_math_robots", phpListHas("noindex")},
		imageID: "rank_math_facebook_image_id", imageURL: "rank_math_facebook_image",
		vars: seoVars{
			syntax: syntaxSingle, title: []string{"title"}, site: []string{"sitename"}, sep: []string{"sep"},
			page: []string{"page", "pagenumber", "pagetotal"}, excerpt: []string{"excerpt", "excerpt_only", "seo_description"},
		},
		notImported: []seoKey{
			{"rank_math_facebook_title", nil}, {"rank_math_facebook_description", nil},
			{"rank_math_twitter_title", nil}, {"rank_math_twitter_description", nil}, {"rank_math_twitter_image", nil},
			{"rank_math_robots", phpListHas("nofollow", "noarchive", "noimageindex", "nosnippet")},
			{"rank_math_breadcrumb_title", nil},
		},
	},
	{
		name: "seopress", label: "SEOPress",
		title: "_seopress_titles_title", description: "_seopress_titles_desc", canonical: "_seopress_robots_canonical",
		noindex: seoKey{"_seopress_robots_index", isValue("yes")},
		imageID: "_seopress_social_fb_img_attachment_id", imageURL: "_seopress_social_fb_img",
		vars: seoVars{
			syntax: syntaxDouble, title: []string{"post_title", "title"}, site: []string{"sitetitle", "sitename"}, sep: []string{"sep"},
			page: []string{"current_pagination", "page"}, excerpt: []string{"post_excerpt", "excerpt"},
		},
		notImported: []seoKey{
			{"_seopress_social_fb_title", nil}, {"_seopress_social_fb_desc", nil},
			{"_seopress_social_twitter_title", nil}, {"_seopress_social_twitter_desc", nil}, {"_seopress_social_twitter_img", nil},
			{"_seopress_robots_follow", isValue("yes")}, {"_seopress_robots_snippet", isValue("yes")},
			{"_seopress_robots_imageindex", isValue("yes")},
		},
	},
	{
		name: "tsf", label: "The SEO Framework",
		title: "_genesis_title", description: "_genesis_description", canonical: "_genesis_canonical_uri",
		noindex: seoKey{"_genesis_noindex", isValue("1")},
		imageID: "_social_image_id", imageURL: "_social_image_url",
		notImported: []seoKey{
			{"_open_graph_title", nil}, {"_open_graph_description", nil}, {"_twitter_title", nil}, {"_twitter_description", nil},
			{"_genesis_nofollow", isValue("1")}, {"_genesis_noarchive", isValue("1")}, {"redirect", nil},
			{"exclude_local_search", isValue("1")}, {"exclude_from_archive", isValue("1")}, {"_tsf_title_no_blogname", isValue("1")},
		},
	},
	{
		name: "ssp", label: "SEO SIMPLE PACK",
		title: "ssp_meta_title", description: "ssp_meta_description", canonical: "ssp_meta_canonical",
		noindex:  seoKey{"ssp_meta_robots", listHas("noindex")},
		imageURL: "ssp_meta_image",
		vars: seoVars{
			syntax: syntaxUnderscore, title: []string{"page_title"}, site: []string{"site_title"}, sep: []string{"sep"},
			page: []string{"page"}, excerpt: []string{"page_contents"},
		},
		notImported: []seoKey{{"ssp_meta_robots", listHas("nofollow", "noarchive")}},
	},
	{
		name: seoAIOSEO, label: "All in One SEO",
		title: "_aioseo_title", description: "_aioseo_description",
		vars: seoVars{
			syntax: syntaxHash, fold: true, title: []string{"post_title"}, site: []string{"site_title"}, sep: []string{"separator_sa"},
			page: []string{"page_number"}, excerpt: []string{"post_excerpt", "post_excerpt_only", "post_content"},
		},
		notImported: []seoKey{
			{"_aioseo_og_title", nil}, {"_aioseo_og_description", nil}, {"_aioseo_twitter_title", nil},
			{"_aioseo_twitter_description", nil},
		},
	},
}

func seoPluginNamed(name string) *seoPlugin {
	for i := range seoPlugins {
		if seoPlugins[i].name == name {
			return &seoPlugins[i]
		}
	}
	return nil
}

// keys lists the postmeta keys that the import reads or records for the plugin.
func (p *seoPlugin) keys() []string {
	var keys []string
	for _, k := range []string{p.title, p.description, p.canonical, p.noindex.key, p.imageID, p.imageURL, p.primary} {
		if k != "" && !slices.Contains(keys, k) {
			keys = append(keys, k)
		}
	}
	for _, k := range p.notImported {
		if !slices.Contains(keys, k.key) {
			keys = append(keys, k.key)
		}
	}
	return keys
}

// hasSettings reports whether the item has a value for a key of the plugin that tells that the site had the plugin: not
// for a key such as "redirect", which is a custom field on a site without it.
func (p *seoPlugin) hasSettings(it *wxr.Item) bool {
	has := func(key string) bool {
		if (keyRules{}).custom(key) {
			return false
		}
		_, v := seoMeta(it, key)
		return v != ""
	}
	for _, k := range [...]string{p.title, p.description, p.canonical, p.noindex.key, p.imageID, p.imageURL, p.primary} {
		if has(k) {
			return true
		}
	}
	for _, k := range p.notImported {
		if has(k.key) {
			return true
		}
	}
	return false
}

// seoMeta returns the first postmeta of the item with the key, with its value without the white space around it. The
// value is "" if there is none, and for the key "".
func seoMeta(it *wxr.Item, key string) (wxr.Meta, string) {
	if key == "" {
		return wxr.Meta{}, ""
	}
	m, ok := firstMeta(it.Meta, key)
	if !ok {
		return wxr.Meta{}, ""
	}
	m.Value = strings.TrimSpace(m.Value)
	return m, m.Value
}

// seoPlugin returns the plugin that the settings name, or nil if they name none.
func (c *entryConv) seoPlugin() *seoPlugin {
	if c.opts.Settings == nil {
		return nil
	}
	return seoPluginNamed(c.opts.Settings.SEO.Plugin)
}

// setSEO makes the seo of the revision of ei of the postmeta of the SEO plugin that the settings name, and counts the
// postmeta of it that are not imported, for reportSEO.
func (c *entryConv) setSEO(ei *entryItem) {
	p := c.seoPlugin()
	if p == nil {
		return
	}
	it, seo := ei.it, &ei.rev.SEO
	site := strings.TrimSpace(c.site.Name[ei.entry.Language])
	if m, v := seoMeta(it, p.title); v != "" {
		c.setSEOText(ei, m, "/seo/title", "title for search engines", &seo.Title, p.titleText(v, ei.rev.Title, site))
	}
	if m, v := seoMeta(it, p.description); v != "" {
		c.setSEOText(ei, m, "/seo/description", "description for search engines", &seo.Description, p.descriptionText(v, ei.rev.Title, site))
	}
	if _, v := seoMeta(it, p.canonical); v != "" {
		seo.Canonical = c.seoCanonical(ei, v)
	}
	if m, v := seoMeta(it, p.noindex.key); v != "" {
		noindex, err := p.noindex.is(m)
		if err != nil {
			c.metaNote(Skipped, ei, m, "/seo/noindex", "the noindex setting is not imported: %s", err)
		}
		seo.NoIndex = noindex
	}
	// The featured image is the default image for sharing (6.8).
	if id := c.seoImage(ei, p); id != ei.rev.FeaturedImage {
		seo.OGImage = id
	}
	for i, k := range p.notImported {
		m, v := seoMeta(it, k.key)
		if v == "" {
			continue
		}
		if k.is != nil {
			if ok, _ := k.is(m); !ok {
				continue
			}
		}
		if c.seoSkips == nil {
			c.seoSkips = make([]int, len(p.notImported))
		}
		c.seoSkips[i]++
	}
}

// reportSEO records each postmeta of the plugin that setSEO counted, once, at the channel.
func (c *entryConv) reportSEO() {
	p := c.seoPlugin()
	for i, n := range c.seoSkips {
		if n > 0 {
			c.imp.channelEntry(Skipped, Target{}, "the postmeta %q of %s is not imported; whiteroom has no setting for it",
				p.notImported[i].key, count(n, "item"))
		}
	}
	c.seoSkips = nil
}

// setSEOText sets *to to the text r that the postmeta m was converted into, or says why it is not imported.
func (c *entryConv) setSEOText(ei *entryItem, m wxr.Meta, pointer, what string, to *string, r seoText) {
	switch {
	case len(r.unknown) > 0:
		c.metaNote(Review, ei, m, pointer, "the %s %q is not imported: the import does not replace %s",
			what, clip(m.Value, maxQuoted), varList(r.unknown))
	case r.long > 0:
		c.metaNote(Skipped, ei, m, pointer, "the %s is not imported: it is too long: %d bytes (max %d)", what, r.long, content.MaxTextBytes)
	default:
		*to = r.text
	}
}

// maxQuoted is the length of the value of a postmeta that a record of the report quotes, which would make a line that is too
// long otherwise; maxVarShown is that of the text of a variable.
const (
	maxQuoted   = 100
	maxVarShown = 40
)

// clip returns s, or its first n bytes (less, to end at a character) and "..." if it is longer.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "..."
}

// varList lists the variables, as written: the first maxLostShown, and the number of the others.
func varList(vars []string) string {
	shown := vars[:min(len(vars), maxLostShown)]
	parts := make([]string, len(shown))
	for i, v := range shown {
		parts[i] = varText(v)
	}
	s := strings.Join(parts, ", ")
	if more := len(vars) - len(shown); more > 0 {
		s += fmt.Sprintf(", and %d more", more)
	}
	return s
}

// varText writes a variable as it is, or quoted and cut if it has other than the characters of ASCII that a name has, so
// that the report stays on one line.
func varText(raw string) string {
	if len(raw) <= maxVarShown && !strings.ContainsFunc(raw, func(r rune) bool { return r <= ' ' || r >= 0x7f || r == '"' || r == '\\' }) {
		return raw
	}
	return strconv.Quote(clip(raw, maxVarShown))
}

// seoText is a title or a description of the plugin as it is imported.
type seoText struct {
	text    string   // the text; it is not empty, and one line that content.CheckLine accepts
	unknown []string // the variables that the import does not replace: there is no text then
	long    int64    // the length, if the text is longer than content.MaxTextBytes: there is no text then
}

// titleText converts value, a title that the plugin shows in the place of the title of the page: the variable of the title of
// the entry, which is title, is replaced, and what the format of the title adds, which is the name of the site, the separator
// and the number of the page, is taken off its ends, also where it is written by hand. site is the name of the site. There
// is no text if what is left is the title.
func (p *seoPlugin) titleText(value, title, site string) seoText {
	ps := p.vars.split(value)
	for i := range ps {
		if ps[i].name != "" && p.vars.kind(ps[i].name) == varTitle {
			ps[i] = seoPiece{text: title, keep: true}
		}
	}
	return p.vars.finish(p.vars.trimEnds(ps, site), title)
}

// descriptionText converts value, a description: the variables of the title of the entry, which is title, and of the name of
// the site are replaced. There is no text if it is only the excerpt, which the build shows if there is no description (6.8).
func (p *seoPlugin) descriptionText(value, title, site string) seoText {
	ps := p.vars.split(value)
	excerpt, other := false, false
	for _, pc := range ps {
		switch {
		case pc.name == "":
			other = other || strings.TrimSpace(pc.text) != ""
		case p.vars.kind(pc.name) == varExcerpt:
			excerpt = true
		default:
			other = true
		}
	}
	if excerpt && !other {
		return seoText{}
	}
	for i := range ps {
		if ps[i].name == "" {
			continue
		}
		switch p.vars.kind(ps[i].name) {
		case varTitle:
			ps[i] = seoPiece{text: title}
		case varSite:
			ps[i] = seoPiece{text: site}
		case varOther, varSep, varPage, varExcerpt:
		}
	}
	return p.vars.finish(ps, "")
}

// seoPiece is a part of a title or a description: text, or a variable, which has a name.
type seoPiece struct {
	text string // the text; a variable as it is written, with what is around its name
	name string // the name of a variable
	keep bool   // text that is not trimmed: the title of the entry
}

// split cuts s into text and variables, in the order they are written.
func (v *seoVars) split(s string) []seoPiece {
	if v.syntax == syntaxNone {
		if s == "" {
			return nil
		}
		return []seoPiece{{text: s}}
	}
	var pieces []seoPiece
	start := 0
	// A variable that fails to match leaves its opener as text, and the search goes on after the first byte of it: each byte
	// is read at most twice, as the name of a variable does not hold its opener.
	for i := 0; i < len(s); {
		j := strings.IndexByte(s[i:], v.syntax.opener())
		if j < 0 {
			break
		}
		i += j
		end, name := v.syntax.match(s, i)
		if end == 0 {
			i++
			continue
		}
		if i > start {
			pieces = append(pieces, seoPiece{text: s[start:i]})
		}
		pieces = append(pieces, seoPiece{text: s[i:end], name: name})
		i, start = end, end
	}
	if start < len(s) {
		pieces = append(pieces, seoPiece{text: s[start:]})
	}
	return pieces
}

// opener returns the byte that a variable starts with, or 0 if there are none.
func (syn varSyntax) opener() byte {
	switch syn {
	case syntaxDouble, syntaxSingle, syntaxUnderscore:
		return '%'
	case syntaxHash:
		return '#'
	case syntaxNone:
	}
	return 0
}

// match reads the variable that starts at s[i], which is the opener, and returns where it ends and its name, or 0 if there
// is none.
func (syn varSyntax) match(s string, i int) (end int, name string) {
	switch syn {
	case syntaxDouble:
		if !strings.HasPrefix(s[i:], "%%") {
			return 0, ""
		}
		rest := s[i+2:]
		n := strings.IndexByte(rest, '%')
		if n < 1 || !strings.HasPrefix(rest[n:], "%%") {
			return 0, ""
		}
		return i + 2 + n + 2, rest[:n]
	case syntaxSingle:
		j := i + 1
		for j < len(s) && (isNameByte(s[j]) || s[j] == '-') {
			j++
		}
		if j > i+1 && j < len(s) && s[j] == '(' {
			k := j + 1
			for k < len(s) && s[k] != ')' && s[k] != '%' {
				k++
			}
			if k+1 < len(s) && s[k] == ')' && s[k+1] == '%' {
				return k + 2, s[i+1 : k+1]
			}
		}
		j = i + 1
		for j < len(s) && s[j] != '%' && !isASCIISpace(s[j]) {
			j++
		}
		if j > i+1 && j < len(s) && s[j] == '%' {
			return j + 1, s[i+1 : j]
		}
	case syntaxUnderscore:
		if !strings.HasPrefix(s[i:], "%_") {
			return 0, ""
		}
		j := i + 2
		for j < len(s) && isNameByte(s[j]) {
			j++
		}
		// The name is what is between "%_" and the last "_" of the run of the characters of names.
		if j < len(s) && s[j] == '%' && j-(i+2) >= 2 && s[j-1] == '_' {
			return j + 1, s[i+2 : j-1]
		}
	case syntaxHash:
		j := i + 1
		if j >= len(s) || !asciiLetter(rune(s[j])) {
			return 0, ""
		}
		for j < len(s) && isNameByte(s[j]) {
			j++
		}
		// #custom_field-<name> and #tax_name-<name> have a name of their own after "-".
		if base := strings.ToLower(s[i+1 : j]); (base == "custom_field" || base == "tax_name") && j+1 < len(s) && s[j] == '-' && isSuffixByte(s[j+1]) {
			for j++; j < len(s) && isSuffixByte(s[j]); j++ {
			}
		}
		return j, s[i+1 : j]
	case syntaxNone:
	}
	return 0, ""
}

func isNameByte(c byte) bool { return c == '_' || asciiLetter(rune(c)) || asciiDigit(rune(c)) }

func isSuffixByte(c byte) bool { return c == '-' || isNameByte(c) }

func isASCIISpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\v' || c == '\f' || c == '\r'
}

// seoSeparators are the characters, besides white space, that set the name of the site off from the title.
const seoSeparators = "-–—|｜·•:：/／~«»<>*"

func isSeparator(r rune) bool { return unicode.IsSpace(r) || strings.ContainsRune(seoSeparators, r) }

// trimEnds takes off the ends of ps what the format of a title adds around the title: white space, the variables of the
// separator, of the name of the site and of the page number, and the name of the site, if it is written by hand. The
// separators next to what was taken off go with it. The title of the entry stays.
func (v *seoVars) trimEnds(ps []seoPiece, site string) []seoPiece {
	for {
		var back, front bool
		ps, back = v.trimEnd(ps, site, false)
		ps, front = v.trimEnd(ps, site, true)
		if !back && !front {
			return ps
		}
	}
}

// trimEnd takes one thing off the end of ps, or the front, and reports whether it did.
func (v *seoVars) trimEnd(ps []seoPiece, site string, front bool) ([]seoPiece, bool) {
	if len(ps) == 0 {
		return ps, false
	}
	at, next := len(ps)-1, len(ps)-2
	if front {
		at, next = 0, 1
	}
	p := ps[at]
	drop := func() []seoPiece {
		if front {
			return ps[1:]
		}
		return ps[:len(ps)-1]
	}
	if p.name != "" {
		if k := v.kind(p.name); k != varSite && k != varSep && k != varPage {
			return ps, false
		}
		if next >= 0 && next < len(ps) && ps[next].name == "" && !ps[next].keep {
			ps[next].text = trimSide(ps[next].text, front, isSeparator)
		}
		return drop(), true
	}
	if p.keep {
		return ps, false
	}
	text := trimSide(p.text, front, unicode.IsSpace)
	if site != "" {
		if cut, ok := cutSite(text, site, front); ok {
			text = cut
		}
	}
	if text == "" {
		return drop(), true
	}
	if text == p.text {
		return ps, false
	}
	ps[at].text = text
	return ps, true
}

func trimSide(s string, front bool, f func(rune) bool) string {
	if front {
		return strings.TrimLeftFunc(s, f)
	}
	return strings.TrimRightFunc(s, f)
}

// cutSite takes the name of the site off the end of text, or the front, with the separators next to it. It does so only if
// the name is all of the text or a separator sets it off from the rest, so that a title that starts with the name of the site,
// such as "Tokyo Tower" for a site that is called "Tokyo", is left as it is.
func cutSite(text, site string, front bool) (string, bool) {
	var rest string
	var ok bool
	if front {
		rest, ok = strings.CutPrefix(text, site)
	} else {
		rest, ok = strings.CutSuffix(text, site)
	}
	if !ok {
		return text, false
	}
	if edge := trimSide(rest, front, unicode.IsSpace); edge != "" {
		var r rune
		if front {
			r, _ = utf8.DecodeRuneInString(edge)
		} else {
			r, _ = utf8.DecodeLastRuneInString(edge)
		}
		if !strings.ContainsRune(seoSeparators, r) {
			return text, false
		}
	}
	return trimSide(rest, front, isSeparator), true
}

// finish makes the text of ps, which is the pieces of a title or a description after the variables that are replaced were. same
// is the text that is no news ("" for none): the entry has it already. The text is one line and normalized. Variables that are
// left, and the text that is too long, leave no text.
func (v *seoVars) finish(ps []seoPiece, same string) seoText {
	if unknown := variables(ps); len(unknown) > 0 {
		return seoText{unknown: unknown}
	}
	var n int64
	for _, p := range ps {
		n += int64(len(p.text))
	}
	if n > content.MaxTextBytes {
		return seoText{long: n}
	}
	var b strings.Builder
	for _, p := range ps {
		b.WriteString(p.text)
	}
	text, _ := oneLine(b.String())
	text = content.NormalizeLine(text)
	switch {
	case len(text) > content.MaxTextBytes:
		return seoText{long: int64(len(text))}
	case text == same:
		// The entry has it already. An empty text with another same is no text, as it is returned below.
		return seoText{}
	}
	// What was on both sides of a variable that was replaced can make one.
	if unknown := variables(v.split(text)); len(unknown) > 0 {
		return seoText{unknown: unknown}
	}
	return seoText{text: text}
}

// variables lists the variables in ps, as written, each once.
func variables(ps []seoPiece) []string {
	var vars []string
	var seen map[string]bool
	for _, p := range ps {
		if p.name == "" {
			continue
		}
		if seen == nil {
			seen = make(map[string]bool)
		}
		if !seen[p.text] {
			seen[p.text] = true
			vars = append(vars, p.text)
		}
	}
	return vars
}

// seoCanonical returns the canonical URL that the postmeta says for the entry ei, v, as the revision has it: a path of the
// site if it is an address of the old site, as in the links of a body. It is "" if the address is that of the entry itself,
// which is what the build has if there is none. An address that whiteroom does not accept is returned as it is, for tidy to
// take out.
func (c *entryConv) seoCanonical(ei *entryItem, v string) string {
	u, outside, ok := c.parseHref(content.NormalizeHref(v))
	if !ok {
		return outside
	}
	idx := c.imp.urlIndex()
	id, sitePath, same := idx.lookup(u)
	if !same {
		return outside
	}
	query := u.RawQuery != ""
	// The address of the post that p= names has a query, and is the same as the address of the post.
	plain := query && (sitePath == "/" || sitePath == "/index.php")
	switch {
	case id == ei.it.ID && (!query || plain):
		return ""
	case query:
		return sitePath + "?" + u.RawQuery
	}
	if want, ok := linkPath(idx.home, ei.it.Link); ok && collapseSlashes(want) == sitePath {
		return ""
	}
	return sitePath
}

// parseHref parses href, an address in a postmeta. A path from the root of the host is one of the host of the old site, so
// it is resolved against its home. outside is the address to keep if it is not one of the site: href, or the resolved one.
func (c *entryConv) parseHref(href string) (u *url.URL, outside string, ok bool) {
	u, err := url.Parse(href)
	if err != nil {
		return nil, href, false
	}
	outside = href
	if home := c.imp.urlIndex().home; home != nil && u.Host == "" && strings.HasPrefix(href, "/") {
		u = home.ResolveReference(u)
		outside = u.String()
	}
	return u, outside, true
}

// seoImage returns the ID of the medium of the image for sharing that the postmeta of the plugin say for ei: the attachment
// that the key of the ID names, or else the one that the address of the image is a file or the page of. It is "" if there is
// none, and records the image that is not imported.
func (c *entryConv) seoImage(ei *entryItem, p *seoPlugin) string {
	const pointer = "/seo/ogImage"
	if m, v := seoMeta(ei.it, p.imageID); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); isDigits(v) && err == nil && n >= 1 {
			id, why := c.imageOf(n)
			if why != "" {
				c.metaNote(Skipped, ei, m, pointer, "the image for sharing, attachment %d, %s", n, why)
			}
			return id
		}
	}
	m, v := seoMeta(ei.it, p.imageURL)
	if v == "" {
		return ""
	}
	post := int64(0)
	if u, _, ok := c.parseHref(content.NormalizeHref(v)); ok {
		post, _, _ = c.imp.urlIndex().lookup(u)
	}
	if it := c.imp.WXR.Post(post); it == nil || it.Type != "attachment" {
		c.metaNote(Skipped, ei, m, pointer, "the image for sharing %q is not a file of an attachment of the WXR", clip(v, maxQuoted))
		return ""
	}
	id, why := c.imageOf(post)
	if why != "" {
		c.metaNote(Skipped, ei, m, pointer, "the image for sharing, attachment %d, %s", post, why)
	}
	return id
}

// primaryCategory returns the category of cats that Yoast SEO has as the primary one of the item, if the settings say that
// the site had Yoast SEO, and the item is in it.
func (c *entryConv) primaryCategory(ei *entryItem, cats []termPick) (termPick, bool) {
	p := c.seoPlugin()
	if p == nil || p.primary == "" {
		return termPick{}, false
	}
	_, v := seoMeta(ei.it, p.primary)
	n, err := strconv.ParseInt(v, 10, 64)
	if !isDigits(v) || err != nil || n < 1 {
		return termPick{}, false
	}
	i := slices.IndexFunc(cats, func(p termPick) bool { return p.t.ID == n })
	if i < 0 {
		return termPick{}, false
	}
	return cats[i], true
}
