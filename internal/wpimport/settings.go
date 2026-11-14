package wpimport

import (
	"bytes"
	"cmp"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/wprest"
	"github.com/WhiteRoomLab/whiteroom/internal/wxr"
)

// Settings is what the person who imports checks and corrects before the import (6.10.1, 6.10.4): ProposeSettings
// guesses it from the WXR and the REST API, WriteJSON writes the guess as a file, ReadSettings reads the corrected file,
// and Site makes the site settings of it. An empty text and 0 stand for no value.
type Settings struct {
	Site SiteSettings `json:"site"`
	URLs URLSettings  `json:"urls"`
	// DefaultCategory is the slug, decoded, of the category that %category% takes for a post without one: "" if the
	// permalink structure of the posts has no such tag.
	DefaultCategory string `json:"defaultCategory"`
	// PageTemplates tells how the page templates of the old site are named in the theme of the site (5.4.2): the keys are
	// the page templates of WordPress (the values of _wp_page_template, such as "templates/full-width.php") and the values
	// the names in the theme. A page whose template is not a key gets the name that templateName makes of it, and a value of
	// "" leaves the template out.
	PageTemplates map[string]string `json:"pageTemplates"`
	// SEO tells which SEO plugin's postmeta the SEO settings of the entries are read from (6.10.5).
	SEO SEOSettings `json:"seo"`
	// Fields tells which field of the schema file each custom field of WordPress (a key of the postmeta) is imported as: by
	// the name of the post type in whiteroom, and then the key. A key that is not here is imported as the field of the same
	// name, if the type has one; a value of "" leaves the key out (6.10.5). The keys are those of the fields of the type: a
	// field in a group, or in a row of a list, is read from the key of the group or the list with _<name>, or
	// _<number>_<name>, after it, and the table is not asked about that key.
	Fields map[string]map[string]string `json:"fields"`
	Blocks BlockSettings                `json:"blocks"`
}

// SEOSettings tells which SEO plugin the old site used.
type SEOSettings struct {
	// Plugin is yoast, rankmath, seopress, tsf, ssp or aioseo, or "" if the SEO settings of the entries are not imported.
	// Only the postmeta of this plugin are read: a site that changed its plugin keeps those of the old one, which are stale.
	Plugin string `json:"plugin"`
}

// SiteSettings are the general settings of the site, in its language.
type SiteSettings struct {
	Name         string `json:"name"`
	Tagline      string `json:"tagline"`
	URL          string `json:"url"`
	Language     string `json:"language"`
	TimeZone     string `json:"timeZone"`
	PostsPerPage int    `json:"postsPerPage"`
	NoIndex      bool   `json:"noindex"`
	// The pages and the attachments are given by their IDs in WordPress.
	FrontPage int64 `json:"frontPage"`
	PostsPage int64 `json:"postsPage"`
	Logo      int64 `json:"logo"`
	Icon      int64 `json:"icon"`
	// AllowHTML is the site setting allowHtml (6.6.8): if it is false, custom HTML becomes a legacy block.
	AllowHTML bool `json:"allowHtml"`
}

// URLSettings are the URL rules of the schema file (6.6.8) by the names in whiteroom. An empty rule is the default one.
type URLSettings struct {
	Post       string              `json:"post"`
	Page       string              `json:"page"`
	Author     string              `json:"author"`
	Taxonomies map[string]string   `json:"taxonomies"`
	Types      map[string]TypeURLs `json:"types"` // the custom types
}

type TypeURLs struct {
	URL     string `json:"url"`
	Archive string `json:"archive"`
}

// BlockSettings tells how the blocks of the old site are named in the theme of the site (6.10.5): the keys are the names
// in WordPress and the values the names in the theme. A name that is not a key is kept as it is, and a value of "" leaves
// it out: a preset or a block style is not imported, and an inline element keeps only its text.
type BlockSettings struct {
	Presets PresetSettings `json:"presets"`
	// Variants maps the block styles (the name after is-style-).
	Variants map[string]string `json:"variants"`
	// Marks maps the classes of the inline elements (mark and span) to the highlight variants.
	Marks map[string]string `json:"marks"`
	// ImageSizes maps the sizes of images.
	ImageSizes map[string]string `json:"imageSizes"`
}

// PresetSettings maps the slugs of the presets of the theme of WordPress, by kind.
type PresetSettings struct {
	Color      map[string]string `json:"color"`
	Gradient   map[string]string `json:"gradient"`
	FontSize   map[string]string `json:"fontSize"`
	FontFamily map[string]string `json:"fontFamily"`
	Spacing    map[string]string `json:"spacing"`
}

// emptyBlockSettings returns BlockSettings with empty maps, as ProposeSettings writes them and ReadSettings reads them.
func emptyBlockSettings() BlockSettings {
	return BlockSettings{
		Presets: PresetSettings{
			Color: map[string]string{}, Gradient: map[string]string{}, FontSize: map[string]string{},
			FontFamily: map[string]string{}, Spacing: map[string]string{},
		},
		Variants: map[string]string{}, Marks: map[string]string{}, ImageSizes: map[string]string{},
	}
}

// WriteJSON writes the settings as JSON with every key, also those with no value, so that the file shows what can be
// corrected. The keys of the maps are in order, and a line break ends the file.
func (s *Settings) WriteJSON(w io.Writer) error {
	b, err := json.Marshal(s, jsontext.WithIndent("  "), json.Deterministic(true))
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

// ReadSettings reads data, a file of settings that is called name in the errors. The keys that are not there have no
// value. The error is content.Errors, each at the file, the line and the column of the problem, with its JSON Pointer.
func ReadSettings(name string, data []byte) (*Settings, error) {
	nulls, syn := scanJSON(data)
	var s Settings
	var sem *json.SemanticError
	if syn == nil {
		err := json.Unmarshal(data, &s, json.RejectUnknownMembers(true))
		if err != nil && !errors.As(err, &sem) && !errors.As(err, &syn) {
			return nil, content.Errors{{Msg: err.Error(), File: name}}
		}
	}
	if syn != nil {
		// A file that is not JSON has nothing else to report yet.
		line, column := offsetPlace(data, syn.ByteOffset)
		return nil, content.Errors{{Path: string(syn.JSONPointer), Msg: syntaxText(syn.Err), File: name, Line: line, Column: column}}
	}
	var errs content.Errors
	for _, p := range nulls {
		errs = append(errs, &content.Error{Path: p, Msg: "null is not allowed; omit the key"})
	}
	if sem != nil {
		errs = append(errs, &content.Error{Path: string(sem.JSONPointer), Msg: semanticText(sem)})
	}
	if len(errs) == 0 {
		return &s, nil
	}
	located := content.LocateErrors(errs, content.SiteFile{Name: name, Data: data})
	slices.SortStableFunc(located, func(a, b *content.Error) int {
		return cmp.Or(cmp.Compare(a.Line, b.Line), cmp.Compare(a.Column, b.Column))
	})
	return nil, located
}

// scanJSON reads all of data as JSON. It returns the JSON Pointers of the nulls in it, or the first syntax error.
func scanJSON(data []byte) (nulls []string, syn *jsontext.SyntacticError) {
	// Not bytes.NewReader: jsontext then garbles member names in the pointers.
	dec := jsontext.NewDecoder(bytes.NewBuffer(data))
	syntax := func(err error) *jsontext.SyntacticError {
		var syn *jsontext.SyntacticError
		if errors.As(err, &syn) {
			return syn
		}
		return &jsontext.SyntacticError{ByteOffset: int64(len(data)), Err: err}
	}
	for {
		tok, err := dec.ReadToken()
		if err != nil {
			return nil, syntax(err)
		}
		if tok.Kind() == jsontext.KindNull {
			nulls = append(nulls, string(dec.StackPointer()))
		}
		if dec.StackDepth() == 0 {
			break
		}
	}
	// The decoder accepts a stream of values, so reject anything after the first.
	end := int(dec.InputOffset())
	if _, err := dec.ReadToken(); !errors.Is(err, io.EOF) {
		if err != nil {
			return nil, syntax(err)
		}
		next := len(data) - len(bytes.TrimLeft(data[end:], " \t\n\r"))
		return nil, &jsontext.SyntacticError{ByteOffset: int64(next), Err: errors.New("unexpected data after value")}
	}
	return nulls, nil
}

func syntaxText(err error) string {
	switch {
	case errors.Is(err, jsontext.ErrDuplicateName):
		return "duplicate key"
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return "unexpected end of JSON input"
	}
	return err.Error()
}

func semanticText(err *json.SemanticError) string {
	switch {
	case errors.Is(err.Err, json.ErrUnknownName):
		return "unknown key"
	case errors.Is(err.Err, strconv.ErrSyntax):
		return "must be an integer"
	case errors.Is(err.Err, strconv.ErrRange):
		return "out of range"
	}
	return fmt.Sprintf("expected %s, got %s", goKindName(err.GoType), jsonKindName(err.JSONKind))
}

// offsetPlace returns the line and the column, which count from 1 and in characters, of the byte at off in data.
func offsetPlace(data []byte, off int64) (line, column int) {
	off = min(max(off, 0), int64(len(data)))
	line, column = 1, 1
	for _, r := range string(data[:off]) {
		if r == '\n' {
			line, column = line+1, 1
		} else {
			column++
		}
	}
	return line, column
}

func goKindName(t reflect.Type) string {
	if t == nil {
		return "a value"
	}
	switch k := t.Kind(); {
	case k == reflect.String:
		return "string"
	case k == reflect.Bool:
		return "boolean"
	case k >= reflect.Int && k <= reflect.Int64:
		return "number"
	case k == reflect.Struct || k == reflect.Map:
		return "object"
	}
	return "a value"
}

func jsonKindName(k jsontext.Kind) string {
	switch k {
	case jsontext.KindNull:
		return "null"
	case jsontext.KindFalse, jsontext.KindTrue:
		return "boolean"
	case jsontext.KindString:
		return "string"
	case jsontext.KindNumber:
		return "number"
	case jsontext.KindBeginObject:
		return "object"
	case jsontext.KindBeginArray:
		return "array"
	case jsontext.KindInvalid, jsontext.KindEndObject, jsontext.KindEndArray:
	}
	return "a value"
}

// entryAt adds an entry about src that goes to target.
func (imp *Import) entryAt(kind Kind, src Source, target Target, format string, args ...any) {
	imp.Report.Add(Entry{Kind: kind, Source: src, Message: fmt.Sprintf(format, args...), Target: target})
}

// channelEntry adds an entry about the whole site, at the channel of the WXR.
func (imp *Import) channelEntry(kind Kind, target Target, format string, args ...any) {
	imp.entryAt(kind, ChannelSource(imp.File, imp.WXR), target, format, args...)
}

// postSource returns the source of an entry about the post with the ID id in WordPress: the item of the WXR, or the
// channel if there is none.
func (imp *Import) postSource(id int64) Source {
	if it := imp.WXR.Post(id); it != nil {
		return ItemSource(imp.File, it)
	}
	return ChannelSource(imp.File, imp.WXR)
}

// ProposeSettings guesses the settings of the site (6.10.1, 6.10.4) from the channel of the WXR, from the REST API in
// imp.REST if there is one, and from the links of the old site, and records the grounds of the guess and the values to
// check or to enter in the report. The names that the bodies use (presets, block styles, image sizes, and the classes of
// inline elements) are listed in Blocks, and the page templates that the pages use in PageTemplates, for the person who
// imports to map them (6.10.5). The SEO plugin that most items have postmeta of is SEO.Plugin, and the custom fields that the
// items of each post type have are in Fields, for the person to map to the fields of the schema file. defs is the schema file of
// the developer, as UnmarshalSchema reads it, or nil. The URL rules in it are kept; the others are guessed. It is an error,
// and nothing is recorded, if the REST API is not that of the site of the WXR, or does not say whose it is.
func (imp *Import) ProposeSettings(defs *content.Site) (*Settings, error) {
	if imp.WXR == nil {
		return nil, errors.New("the WXR is needed to propose the settings")
	}
	if err := imp.checkREST(); err != nil {
		return nil, err
	}
	if defs == nil {
		defs = &content.Site{}
	}
	ch := &imp.WXR.Channel
	s := &Settings{
		URLs:          URLSettings{Taxonomies: map[string]string{}, Types: map[string]TypeURLs{}},
		PageTemplates: map[string]string{},
		Fields:        map[string]map[string]string{},
		Blocks:        emptyBlockSettings(),
	}
	st := &s.Site
	st.Name, st.Tagline = ch.Title, ch.Description
	if st.Name == "" {
		imp.channelEntry(Review, settingsTarget("/name"), "the site has no title; enter site.name")
	}
	st.URL = imp.proposeURL()
	st.Language = imp.proposeLanguage()
	st.TimeZone = imp.proposeTimeZone()
	st.PostsPerPage = content.DefaultPostsPerPage
	imp.channelEntry(Review, settingsTarget("/postsPerPage"),
		"WordPress keeps the number of posts on a page and whether search engines may index the site where the import cannot "+
			"read them; check site.postsPerPage (%d by default) and site.noindex", content.DefaultPostsPerPage)
	st.FrontPage, st.PostsPage = imp.proposeFrontPage()
	st.Logo, st.Icon = imp.proposeMedia()
	if err := imp.proposeURLs(s, defs); err != nil {
		return nil, err
	}
	imp.reportSkippedTypes(defs)
	imp.proposeBlocks(s, defs)
	imp.proposePageTemplates(s)
	imp.proposeSEO(s, defs)
	imp.proposeFields(s, defs)
	imp.reportSkippedTaxonomies(defs)
	return s, nil
}

// proposePageTemplates counts the page templates that the pages use, and puts them in s.PageTemplates for the person who
// imports to map them to those of the theme of the site (6.10.5): each maps to the name that templateName makes of it, or
// to nothing. They are recorded once for review. It reads the pages that are imported: those that are not in the trash nor
// an automatic draft.
func (imp *Import) proposePageTemplates(s *Settings) {
	counts := make(map[string]int)
	for i := range imp.WXR.Items {
		it := &imp.WXR.Items[i]
		if it.Type != "page" || discardedStatus(it.Status) != "" {
			continue
		}
		if file := pageTemplateOf(it); file != "" {
			counts[file]++
		}
	}
	for file := range counts {
		s.PageTemplates[file] = templateName(file)
	}
	if n := len(counts); n > 0 {
		imp.channelEntry(Review, Target{}, "the pages use %s of WordPress: %s; pageTemplates maps them to the page templates of the theme of the site",
			plural(n, "page template", "page templates"), usedNames(counts))
	}
}

// proposeSEO finds the SEO plugin that the items that are imported have the settings of in their postmeta, and puts the one
// that most of them have in s.SEO.Plugin, for the person who imports to check (6.10.5). Of two that as many have, it takes the
// first of seoPlugins. There is nothing to put, and nothing to record, if no item has any. The items that are imported are
// those of a type that the site has that are not in the trash nor an automatic draft.
func (imp *Import) proposeSEO(s *Settings, defs *content.Site) {
	counts := make([]int, len(seoPlugins))
	for i := range imp.WXR.Items {
		it := &imp.WXR.Items[i]
		if typeName(it.Type, defs) == "" || discardedStatus(it.Status) != "" {
			continue
		}
		for j := range seoPlugins {
			if seoPlugins[j].hasSettings(it) {
				counts[j]++
			}
		}
	}
	var found []int
	for j, n := range counts {
		if n > 0 {
			found = append(found, j)
		}
	}
	if len(found) == 0 {
		return
	}
	slices.SortStableFunc(found, func(a, b int) int { return cmp.Compare(counts[b], counts[a]) })
	chosen := &seoPlugins[found[0]]
	s.SEO.Plugin = chosen.name
	parts := make([]string, len(found))
	for i, j := range found {
		parts[i] = fmt.Sprintf("%s (%s)", seoPlugins[j].label, count(counts[j], "item"))
	}
	list := strings.Join(parts, ", ")
	if n := len(parts); n > 1 {
		list = strings.Join(parts[:n-1], ", ") + " and " + parts[n-1]
	}
	imp.channelEntry(Review, Target{}, "the items have the SEO settings of %s; seo.plugin is %q: its titles, descriptions, canonical URLs, "+
		"noindex and images for sharing are imported; the defaults of the plugin for the site and for each post type are options of "+
		"WordPress, which the WXR does not have", list, chosen.name)
	if chosen.name == seoAIOSEO {
		imp.channelEntry(Review, Target{}, "All in One SEO keeps the canonical URL, noindex and the image for sharing in its own table, "+
			"which the WXR does not have; only its titles and descriptions are imported")
	}
}

// checkSEOPlugin fails if name is neither empty nor the name of an SEO plugin.
func checkSEOPlugin(name string, fail func(pointer, format string, args ...any)) {
	if name == "" || seoPluginNamed(name) != nil {
		return
	}
	names := make([]string, len(seoPlugins))
	for i := range seoPlugins {
		names[i] = seoPlugins[i].name
	}
	fail("/seo/plugin", "unknown SEO plugin %q: use %s or %s, or leave it empty", name, strings.Join(names[:len(names)-1], ", "), names[len(names)-1])
}

// proposeFields counts the custom fields of WordPress that the items of each post type have, and puts them in s.Fields for
// the person who imports to map them to the fields of the schema file (6.10.5): each key maps to the field of the type that is
// called the same, and to nothing if there is none. The post types that no item has a custom field of have no entry. The keys
// are counted by the items that have a value for them. Not counted are the keys that WordPress and the plugins keep for
// themselves, those of the SEO plugin of s.SEO (proposeSEO has put it there), the keys of the rows of a list, and the keys
// that the groups and the lists of the type are read from, which the table does not map. It reads the items that are
// imported: those of a type that the site has that are not in the trash nor an automatic draft.
func (imp *Import) proposeFields(s *Settings, defs *content.Site) {
	rules := keyRules{plugin: pluginKeys(s.SEO.Plugin)}
	counts := make(map[string]map[string]int)
	nested := make(map[string]*nestedKeys)
	var order []string // the post types, by their first item
	for i := range imp.WXR.Items {
		it := &imp.WXR.Items[i]
		typ := typeName(it.Type, defs)
		if typ == "" || discardedStatus(it.Status) != "" {
			continue
		}
		if nested[typ] == nil {
			nested[typ] = nestedKeysOf(defs.Type(typ))
		}
		keys := slices.DeleteFunc(rules.candidates(it, nil, &metaIndex{metas: it.Meta}), nested[typ].has)
		if len(keys) == 0 {
			continue
		}
		if counts[typ] == nil {
			counts[typ] = make(map[string]int)
			order = append(order, typ)
		}
		for _, key := range keys {
			counts[typ][key]++
		}
	}
	for _, typ := range order {
		def := defs.Type(typ)
		table := make(map[string]string, len(counts[typ]))
		for key := range counts[typ] {
			table[key] = ""
			if def != nil && slices.ContainsFunc(def.Fields, func(f content.FieldDef) bool { return f.Name == key }) {
				table[key] = key
			}
		}
		s.Fields[typ] = table
		imp.channelEntry(Review, Target{}, "the items of the post type %s have %s: %s; fields.%s maps them to the fields of %s in the schema file",
			typeText(typ), plural(len(table), "custom field", "custom fields"), usedNamesOf(counts[typ], "item", "items"), typeText(typ), typeText(typ))
	}
}

// checkFieldTable fails for each post type of table that site does not have, each key whose field its type does not have,
// and each key after the first that names a field that another key names.
func checkFieldTable(site *content.Site, table map[string]map[string]string, fail func(pointer, format string, args ...any)) {
	for _, typ := range slices.Sorted(maps.Keys(table)) {
		pointer := "/fields/" + pointerEscaper.Replace(typ)
		def := site.Type(typ)
		if def == nil {
			fail(pointer, "type %q is not in the schema file", typ)
			continue
		}
		keyOf := make(map[string]string)
		for _, key := range slices.Sorted(maps.Keys(table[typ])) {
			name := table[typ][key]
			prev, taken := keyOf[name]
			switch {
			case name == "":
			case !slices.ContainsFunc(def.Fields, func(f content.FieldDef) bool { return f.Name == name }):
				fail(pointer+"/"+pointerEscaper.Replace(key), "the type %s has no field %q", typeText(typ), name)
			case taken:
				fail(pointer+"/"+pointerEscaper.Replace(key), "the field %q is also that of %q", name, prev)
			default:
				keyOf[name] = key
			}
		}
	}
}

// pageTemplateOf returns the page template of the item it in WordPress: the first _wp_page_template without the white space
// around it, or "" if there is none, as WordPress takes an empty value and "default" for no template.
func pageTemplateOf(it *wxr.Item) string {
	file, _ := it.Meta.Get("_wp_page_template")
	if file = strings.TrimSpace(file); file == "default" {
		return ""
	}
	return file
}

// templateName returns the name that the theme of the site gives the page template file of WordPress, if it is the name
// of the file: what follows the last "/", without ".php". It returns "" if that is not a name that whiteroom accepts.
func templateName(file string) string {
	name := strings.TrimSuffix(file[strings.LastIndexByte(file, '/')+1:], ".php")
	if content.CheckName(name) != nil {
		return ""
	}
	return name
}

// checkREST checks that imp.REST, if there is one, is the REST API of the site of the WXR: its home has the same host
// name and path as the link of the channel (sameAddress). A snapshot that gives no home cannot be checked. The methods
// that read imp.REST call it first, as the snapshot of another site would give the wrong people their bios and websites.
func (imp *Import) checkREST() error {
	if imp.REST == nil {
		return nil
	}
	home, link := imp.REST.Index.Home, imp.WXR.Channel.Link
	switch {
	case strings.TrimSpace(home) == "":
		return errors.New("the REST API does not give its home, so it cannot be checked to be that of the WXR")
	case !sameAddress(home, link):
		return fmt.Errorf("the REST API is that of %q, but the WXR is of %q", home, link)
	}
	return nil
}

// sameAddress reports whether the addresses a and b of a site have the same host name, without regard to case, and the
// same path in the canonical form without a trailing "/". The scheme and the port do not count: a site may have moved
// to https.
func sameAddress(a, b string) bool {
	key := func(s string) (string, bool) {
		u, err := url.Parse(strings.TrimSpace(s))
		if err != nil || u.Hostname() == "" {
			return "", false
		}
		path := strings.TrimRight(content.CanonicalFrom(cmp.Or(u.RawPath, u.EscapedPath())), "/")
		return strings.ToLower(u.Hostname()) + "\x00" + path, true
	}
	ka, oka := key(a)
	kb, okb := key(b)
	if oka && okb {
		return ka == kb
	}
	return strings.EqualFold(strings.TrimRight(strings.TrimSpace(a), "/"), strings.TrimRight(strings.TrimSpace(b), "/"))
}

// settingProblem returns the message of the error of Validate at path in site, or "" if there is none.
func settingProblem(site *content.Site, path string) string {
	var errs content.Errors
	if errors.As(site.Validate(nil), &errs) {
		for _, e := range errs {
			if e.Path == path {
				return e.Msg
			}
		}
	}
	return ""
}

// proposeURL returns the address of the home page of the WXR as the URL of the site (6.6.8): cleaned up, with the scheme
// and the host in lower case, and without a trailing "/" and the default port. One that the rules refuse is kept, with
// the reason in the report.
func (imp *Import) proposeURL() string {
	site := &content.Site{URL: imp.WXR.Channel.Link}
	site.NormalizeSettings()
	u := tidySiteURL(site.URL)
	target := settingsTarget("/url")
	if u == "" {
		imp.channelEntry(Review, target, "the WXR has no site address; enter site.url")
	} else if reason := settingProblem(&content.Site{URL: u}, "/url"); reason != "" {
		imp.channelEntry(Review, target, "the URL %q cannot be that of the site: %s; correct site.url", u, reason)
	}
	return u
}

// tidySiteURL removes the "/" at the end of u, and ":80" from the authority of an http URL and ":443" from that of an
// https URL, as the site settings require.
func tidySiteURL(u string) string {
	scheme, rest, ok := strings.Cut(u, "://")
	if !ok {
		return strings.TrimRight(u, "/")
	}
	rest = strings.TrimRight(rest, "/")
	end := strings.IndexAny(rest, "/?#")
	if end < 0 {
		end = len(rest)
	}
	authority := rest[:end]
	switch scheme {
	case "http":
		authority = strings.TrimSuffix(authority, ":80")
	case "https":
		authority = strings.TrimSuffix(authority, ":443")
	}
	return scheme + "://" + authority + rest[end:]
}

// proposeLanguage returns the language of the WXR as a language code (6.4.5), or "" if it is not one.
func (imp *Import) proposeLanguage() string {
	written := strings.TrimSpace(imp.WXR.Channel.Language)
	target := settingsTarget("/defaultLanguage")
	if written == "" {
		imp.channelEntry(Review, target, "the WXR does not give the language; enter site.language")
		return ""
	}
	code, dropped, ok := siteLanguage(written)
	switch {
	case !ok:
		imp.channelEntry(Review, target, "the language %q is not a language code; enter site.language", written)
	case dropped:
		imp.channelEntry(Review, target, "the language %q is %q here; whiteroom has no variants of languages", written, code)
	}
	return code
}

// siteLanguage turns s, the language of WordPress such as "ja", "en_US" or "de_DE_formal", into a language code (6.4.5):
// the language, the script if there is one, and the region if there is one. What follows them is dropped, as the
// variants of WordPress have no counterpart; dropped tells whether there was any. ok is false if s is not a language code.
func siteLanguage(s string) (code string, dropped, ok bool) {
	parts := strings.Split(strings.ReplaceAll(s, "_", "-"), "-")
	for _, p := range parts {
		if !isAlnum(p) {
			return "", false, false
		}
	}
	if lang := parts[0]; len(lang) < 2 || len(lang) > 3 || !isLetters(lang) {
		return "", false, false
	}
	code = strings.ToLower(parts[0])
	rest := parts[1:]
	if len(rest) > 0 && len(rest[0]) == 4 && isLetters(rest[0]) {
		code += "-" + strings.ToUpper(rest[0][:1]) + strings.ToLower(rest[0][1:])
		rest = rest[1:]
	}
	if len(rest) > 0 {
		switch p := rest[0]; {
		case len(p) == 2 && isLetters(p):
			code += "-" + strings.ToUpper(p)
			rest = rest[1:]
		case len(p) == 3 && isDigits(p):
			code += "-" + p
			rest = rest[1:]
		}
	}
	if content.CheckLanguage(code) != nil {
		return "", false, false
	}
	return code, len(rest) > 0, true
}

func asciiLetter(r rune) bool { return 'a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' }

func asciiDigit(r rune) bool { return '0' <= r && r <= '9' }

// onlyOf reports whether s is not empty and made of the runes that ok accepts.
func onlyOf(s string, ok func(rune) bool) bool {
	for _, r := range s {
		if !ok(r) {
			return false
		}
	}
	return s != ""
}

func isLetters(s string) bool { return onlyOf(s, asciiLetter) }

func isDigits(s string) bool { return onlyOf(s, asciiDigit) }

func isAlnum(s string) bool {
	return onlyOf(s, func(r rune) bool { return asciiLetter(r) || asciiDigit(r) })
}

// proposeTimeZone returns the time zone of the REST API (6.10.1): its name, or if the site gave an offset in hours, the
// Etc/GMT name of it (the sign is reversed). It returns "" if there is none, and records why.
func (imp *Import) proposeTimeZone() string {
	target := settingsTarget("/timeZone")
	var ix wprest.Index
	if imp.REST != nil {
		ix = imp.REST.Index
	}
	name, offset := strings.TrimSpace(ix.TimezoneString), strings.TrimSpace(ix.GMTOffset)
	switch {
	case name != "":
	case offset != "":
		hours, err := strconv.ParseFloat(offset, 64)
		switch {
		case err != nil || math.IsNaN(hours) || math.IsInf(hours, 0) || hours != math.Trunc(hours):
			imp.channelEntry(Review, target, "the time zone of WordPress is the offset %q, which is not a whole number of hours; "+
				"enter site.timeZone", offset)
			return ""
		case hours < -12 || hours > 14:
			imp.channelEntry(Review, target, "the time zone of WordPress is the offset %q, which is out of range (-12 to 14); "+
				"enter site.timeZone", offset)
			return ""
		case hours == 0:
			name = "UTC"
		case hours > 0:
			name = "Etc/GMT-" + strconv.Itoa(int(hours))
		default:
			name = "Etc/GMT+" + strconv.Itoa(int(-hours))
		}
	default:
		imp.channelEntry(Review, target, "the REST API did not give the time zone; enter site.timeZone")
		return ""
	}
	if reason := settingProblem(&content.Site{TimeZone: name}, "/timeZone"); reason != "" {
		imp.channelEntry(Review, target, "the time zone of WordPress cannot be that of the site: %s; enter site.timeZone", reason)
		return ""
	}
	return name
}

// proposeFrontPage returns the IDs in WordPress of the front page and of the page of the posts, as the REST API says;
// before WordPress 6.8, or without the REST API, the front page is the page whose link is the home page. 0 is none.
func (imp *Import) proposeFrontPage() (front, posts int64) {
	var ix wprest.Index
	if imp.REST != nil {
		ix = imp.REST.Index
	}
	switch ix.ShowOnFront {
	case "posts":
		return 0, 0
	case "page":
	default:
		return imp.frontPageByLink(), 0
	}

	front, posts = ix.PageOnFront, ix.PageForPosts
	isPage := func(id int64) bool {
		it := imp.WXR.Post(id)
		return it != nil && it.Type == "page"
	}
	if front != 0 && !isPage(front) {
		imp.entryAt(Skipped, imp.postSource(front), settingsTarget("/frontPage"), "the front page, page %d, is not in the WXR", front)
		front = 0
	}
	if posts != 0 && !isPage(posts) {
		imp.entryAt(Skipped, imp.postSource(posts), settingsTarget("/postsPage"), "the posts page, page %d, is not in the WXR", posts)
		posts = 0
	}
	switch {
	case front == 0 && posts != 0:
		imp.channelEntry(Review, settingsTarget("/postsPage"),
			"page %d was the page of the latest posts without a front page; it is imported as an ordinary page", posts)
		posts = 0
	case front != 0 && front == posts:
		imp.channelEntry(Review, settingsTarget("/frontPage"),
			"page %d is both the front page and the page of the latest posts; it is the front page here", front)
		posts = 0
	}
	return front, posts
}

// frontPageByLink finds the front page among the published pages of the WXR: WordPress gives it the home page for its
// link. It returns 0 if there is not exactly one, and records it.
func (imp *Import) frontPageByLink() int64 {
	home := parseHome(imp.WXR.Channel.Link)
	if home == nil {
		home = parseHome(imp.WXR.Channel.BaseBlogURL)
	}
	var found []int64
	for i := range imp.WXR.Items {
		it := &imp.WXR.Items[i]
		if it.Type != "page" || it.Status != "publish" {
			continue
		}
		if path, ok := linkPath(home, it.Link); ok && path == "/" {
			found = append(found, it.ID)
		}
	}
	target := settingsTarget("/frontPage")
	switch len(found) {
	case 1:
		imp.channelEntry(Review, target,
			"the front page is page %d, whose link is the home page; set site.postsPage if the site had a page of posts", found[0])
		return found[0]
	case 0:
		imp.channelEntry(Review, target, "no page has the home page as its link, so the front page cannot be told; "+
			"set site.frontPage and site.postsPage if the site had pages for them")
	default:
		imp.channelEntry(Review, target, "%d pages have the home page as their link (%s), so the front page cannot be told; "+
			"set site.frontPage and site.postsPage if the site had pages for them", len(found), idsText(found))
	}
	return 0
}

// idsText lists the first IDs of ids.
func idsText(ids []int64) string {
	const shown = 5
	parts := make([]string, 0, shown)
	for _, id := range ids[:min(len(ids), shown)] {
		parts = append(parts, strconv.FormatInt(id, 10))
	}
	list := strings.Join(parts, ", ")
	if len(ids) > shown {
		list += fmt.Sprintf(", and %d more", len(ids)-shown)
	}
	return list
}

// proposeMedia returns the IDs in WordPress of the attachments of the logo and the icon of the site, which the REST API
// gives. Those that the WXR does not have are left out, and recorded.
func (imp *Import) proposeMedia() (logo, icon int64) {
	var ix wprest.Index
	if imp.REST != nil {
		ix = imp.REST.Index
	}
	// The index has 0 for a site without the setting, and for one that does not tell: only both being 0 is in doubt.
	if ix.SiteLogo == 0 && ix.SiteIcon == 0 {
		imp.channelEntry(Review, settingsTarget("/logo"), "set site.logo and site.icon to attachments of the WXR, if the site had them")
		return 0, 0
	}
	attachment := func(id int64, what, pointer string) int64 {
		if id < 1 {
			return 0
		}
		if it := imp.WXR.Post(id); it == nil || it.Type != "attachment" {
			imp.entryAt(Skipped, imp.postSource(id), settingsTarget(pointer), "the %s, attachment %d, is not in the WXR", what, id)
			return 0
		}
		return id
	}
	return attachment(ix.SiteLogo, "logo", "/logo"), attachment(ix.SiteIcon, "icon", "/icon")
}

// proposeURLs guesses the URL rules (6.10.4). The rules that the schema file has are kept; if they differ from the
// guess, that is recorded.
func (imp *Import) proposeURLs(s *Settings, defs *content.Site) error {
	perm, err := imp.InferPermalinks(defs)
	if err != nil {
		return err
	}
	rule := func(written, guessed, pointer, what string) string {
		if written == "" {
			return guessed
		}
		if written != guessed {
			imp.channelEntry(Review, schemaTarget(pointer), "the schema file has the URL rule %q for %s, which differs from %q "+
				"inferred from the old site; old URLs will need redirects", written, what, guessed)
		}
		return written
	}
	typeRule := func(name, what string, p Permalink) string {
		var written string
		if t := defs.Type(name); t != nil {
			written = t.URL
		}
		return rule(written, p.Rule, "/types/"+pointerEscaper.Replace(name)+"/url", what)
	}

	s.URLs.Post = typeRule(content.TypePost, "posts", perm.Post)
	s.URLs.Page = typeRule(content.TypePage, "pages", perm.Page)
	s.URLs.Author = rule(defs.AuthorURL, perm.Author.Rule, "/authorUrl", "author pages")
	for _, name := range slices.Sorted(maps.Keys(perm.Types)) {
		p := perm.Types[name]
		what := "the post type " + typeText(name)
		urls := TypeURLs{URL: typeRule(name, what, p), Archive: p.Archive}
		if t := defs.Type(name); t != nil && t.Archive != "" {
			if p.Archive != "" && t.Archive != p.Archive {
				imp.channelEntry(Review, schemaTarget("/types/"+pointerEscaper.Replace(name)+"/archive"),
					"the schema file has the archive %q for %s, which differs from %q inferred from the old site; "+
						"old URLs will need redirects", t.Archive, what, p.Archive)
			}
			urls.Archive = t.Archive
		}
		s.URLs.Types[name] = urls
	}
	for _, name := range slices.Sorted(maps.Keys(perm.Taxonomies)) {
		var written string
		if t := defs.Taxonomy(name); t != nil {
			written = t.URL
		}
		s.URLs.Taxonomies[name] = rule(written, perm.Taxonomies[name].Rule, "/taxonomies/"+pointerEscaper.Replace(name)+"/url",
			"the taxonomy "+typeText(name))
	}
	if perm.DefaultCategory != "" {
		s.DefaultCategory = perm.DefaultCategory
		if slug, err := wxr.DecodeSlug(perm.DefaultCategory); err == nil {
			s.DefaultCategory = slug
		}
	}
	return nil
}

// reportSkippedTypes records, for each post type of the WXR whose items are not imported because the schema file does not
// define it, that they are not, once at the first item. The types that other conversions handle are left out.
func (imp *Import) reportSkippedTypes(defs *content.Site) {
	type skipped struct {
		first *wxr.Item
		n     int
	}
	var order []string
	skips := make(map[string]*skipped)
	for i := range imp.WXR.Items {
		it := &imp.WXR.Items[i]
		if ownedTypes[it.Type] || typeName(it.Type, defs) != "" {
			continue
		}
		sk := skips[it.Type]
		if sk == nil {
			sk = &skipped{first: it}
			skips[it.Type] = sk
			order = append(order, it.Type)
		}
		sk.n++
	}
	for _, wp := range order {
		sk := skips[wp]
		verb, pronoun := "are", "them"
		if sk.n == 1 {
			verb, pronoun = "is", "it"
		}
		imp.Report.Add(Entry{
			Kind:   Skipped,
			Source: Source{File: imp.File, Line: sk.first.Line, Column: sk.first.Column, Record: recordItem, Type: wp},
			Message: fmt.Sprintf("%s of the post type %s %s not imported; define %s in the schema file to import %s",
				count(sk.n, "item"), typeText(wp), verb, typeText(wp), pronoun),
		})
	}
}

// Site makes the site settings (6.6.8) of s, the settings that the person corrected, and defs, the schema file of the
// developer as UnmarshalSchema reads it, or nil: the settings document and the items of the schema file that the URL
// rules of s fill. The front page, the page of the posts, the logo and the icon are given as IDs in WordPress, which Site
// finds in imp.IDs and checks with imp.Ref, so it is called after the plan has given the entries and the media their IDs
// and recorded them with AddRef (6.10.5), and before Terms, Authors and the conversion of the entries, which need the
// site. Called before the plan, it leaves out all of them that s names, as not imported. Those that were not imported
// are left out, and recorded each time Site is called. The error is content.Errors that name the places in s by their
// JSON Pointers, and in the schema file by theirs if the problem is in what the developer wrote.
func (imp *Import) Site(s *Settings, defs *content.Site) (*content.Site, error) {
	if s == nil {
		return nil, errors.New("the settings are needed to make the site settings")
	}
	var errs content.Errors
	fail := func(pointer, format string, args ...any) {
		errs = append(errs, &content.Error{Path: pointer, Msg: fmt.Sprintf(format, args...)})
	}

	st := &s.Site
	site := &content.Site{
		URL:             st.URL,
		TimeZone:        st.TimeZone,
		Languages:       []string{st.Language},
		DefaultLanguage: st.Language,
		PostsPerPage:    st.PostsPerPage,
		SEO:             content.SiteSEO{NoIndex: st.NoIndex},
		AllowHTML:       st.AllowHTML,
	}
	if st.Name != "" {
		site.Name = content.Localized{st.Language: st.Name}
	}
	if st.Tagline != "" {
		site.Tagline = content.Localized{st.Language: st.Tagline}
	}
	// found returns the ID of the entity of kind that the post n of WordPress became. If there is none, it records that
	// n was not imported, as the thing that what names at pointer in the settings and in the settings document.
	found := func(n int64, kind content.RefKind, pointer, what, noun string) string {
		switch {
		case n == 0:
			return ""
		case n < 0:
			fail("/site/"+pointer, "invalid ID %d: use 0 for none, or the ID of the %s in WordPress", n, noun)
			return ""
		}
		if k, id, ok := imp.IDs.Lookup(Object{Kind: ObjectPost, ID: n}); ok && k == kind {
			return id
		}
		imp.entryAt(Skipped, imp.postSource(n), settingsTarget("/"+pointer), "the %s, %s %d, is not imported", what, noun, n)
		return ""
	}
	if id := found(st.FrontPage, content.RefEntry, "frontPage", "front page", "page"); id != "" {
		site.FrontPage = content.Localized{st.Language: id}
	}
	if id := found(st.PostsPage, content.RefEntry, "postsPage", "posts page", "page"); id != "" {
		site.PostsPage = content.Localized{st.Language: id}
	}
	site.Logo = found(st.Logo, content.RefMedia, "logo", "logo", "attachment")
	site.Icon = found(st.Icon, content.RefMedia, "icon", "icon", "attachment")

	if defs != nil {
		site.Types = slices.Clone(defs.Types)
		site.Taxonomies = slices.Clone(defs.Taxonomies)
	}
	site.AuthorURL = s.URLs.Author
	setSchemaURLs(site, s, fail)
	checkBlockNames(&s.Blocks, fail)
	checkPageTemplates(s.PageTemplates, fail)
	checkSEOPlugin(s.SEO.Plugin, fail)
	checkFieldTable(site, s.Fields, fail)

	site.NormalizeSettings()
	if err := site.Validate(imp.Ref); err != nil {
		var invalid content.Errors
		if !errors.As(err, &invalid) {
			invalid = content.Errors{{Msg: err.Error()}}
		}
		for _, e := range invalid {
			errs = append(errs, &content.Error{Path: importPointer(e.Path, s), Msg: e.Msg})
		}
	}
	if len(errs) > 0 {
		return nil, dedupe(errs)
	}
	return site, nil
}

// setSchemaURLs puts the URL rules of s into the types and the taxonomies of site, which have the definitions of the
// schema file. post, page, category and tag are added to the front if they are not defined, and left out if they would
// have nothing but the name.
func setSchemaURLs(site *content.Site, s *Settings, fail func(pointer, format string, args ...any)) {
	var addedTypes []content.TypeDef
	for _, b := range []struct{ name, rule string }{{content.TypePost, s.URLs.Post}, {content.TypePage, s.URLs.Page}} {
		if i := slices.IndexFunc(site.Types, func(t content.TypeDef) bool { return t.Name == b.name }); i >= 0 {
			site.Types[i].URL = b.rule
		} else if b.rule != "" {
			addedTypes = append(addedTypes, content.TypeDef{Name: b.name, URL: b.rule})
		}
	}
	site.Types = slices.DeleteFunc(append(addedTypes, site.Types...), func(t content.TypeDef) bool {
		return (t.Name == content.TypePost || t.Name == content.TypePage) &&
			t.Label == "" && t.URL == "" && len(t.Taxonomies) == 0 && len(t.Fields) == 0
	})
	for _, name := range slices.Sorted(maps.Keys(s.URLs.Types)) {
		pointer := "/urls/types/" + pointerEscaper.Replace(name)
		i := slices.IndexFunc(site.Types, func(t content.TypeDef) bool { return t.Name == name })
		switch {
		case name == content.TypePost || name == content.TypePage:
			fail(pointer, "type %q is not a custom type; use urls.%s", name, name)
		case i < 0:
			fail(pointer, "type %q is not in the schema file", name)
		default:
			site.Types[i].URL, site.Types[i].Archive = s.URLs.Types[name].URL, s.URLs.Types[name].Archive
		}
	}

	var addedTaxonomies []content.TaxonomyDef
	for _, name := range []string{content.TaxonomyCategory, content.TaxonomyTag} {
		rule, ok := s.URLs.Taxonomies[name]
		if !ok {
			continue
		}
		if i := slices.IndexFunc(site.Taxonomies, func(t content.TaxonomyDef) bool { return t.Name == name }); i >= 0 {
			site.Taxonomies[i].URL = rule
		} else if rule != "" {
			addedTaxonomies = append(addedTaxonomies, content.TaxonomyDef{Name: name, URL: rule})
		}
	}
	site.Taxonomies = slices.DeleteFunc(append(addedTaxonomies, site.Taxonomies...), func(t content.TaxonomyDef) bool {
		return (t.Name == content.TaxonomyCategory || t.Name == content.TaxonomyTag) && t.Label == "" && t.URL == ""
	})
	for _, name := range slices.Sorted(maps.Keys(s.URLs.Taxonomies)) {
		if name == content.TaxonomyCategory || name == content.TaxonomyTag {
			continue
		}
		if i := slices.IndexFunc(site.Taxonomies, func(t content.TaxonomyDef) bool { return t.Name == name }); i < 0 {
			fail("/urls/taxonomies/"+pointerEscaper.Replace(name), "taxonomy %q is not in the schema file", name)
		} else {
			site.Taxonomies[i].URL = s.URLs.Taxonomies[name]
		}
	}
}

// checkBlockNames fails for each value of the maps that is neither empty nor a name that whiteroom accepts.
func checkBlockNames(b *BlockSettings, fail func(pointer, format string, args ...any)) {
	for _, m := range []struct {
		pointer string
		names   map[string]string
	}{
		{"/blocks/presets/color/", b.Presets.Color},
		{"/blocks/presets/gradient/", b.Presets.Gradient},
		{"/blocks/presets/fontSize/", b.Presets.FontSize},
		{"/blocks/presets/fontFamily/", b.Presets.FontFamily},
		{"/blocks/presets/spacing/", b.Presets.Spacing},
		{"/blocks/variants/", b.Variants},
		{"/blocks/marks/", b.Marks},
		{"/blocks/imageSizes/", b.ImageSizes},
	} {
		for _, key := range slices.Sorted(maps.Keys(m.names)) {
			if name := m.names[key]; name != "" {
				if err := content.CheckName(name); err != nil {
					fail(m.pointer+pointerEscaper.Replace(key), "%s", err.Error())
				}
			}
		}
	}
}

// checkPageTemplates fails for each value of names that is neither empty nor a name that whiteroom accepts.
func checkPageTemplates(names map[string]string, fail func(pointer, format string, args ...any)) {
	for _, key := range slices.Sorted(maps.Keys(names)) {
		if name := names[key]; name != "" {
			if err := content.CheckName(name); err != nil {
				fail("/pageTemplates/"+pointerEscaper.Replace(key), "%s", err.Error())
			}
		}
	}
}

var pointerUnescaper = strings.NewReplacer("~1", "/", "~0", "~")

// importPointer returns the JSON Pointer in s for p, the pointer of an error of the site settings. What is in the schema
// file and not in s keeps its pointer.
func importPointer(p string, s *Settings) string {
	key, rest, _ := strings.Cut(strings.TrimPrefix(p, "/"), "/")
	switch key {
	case "name", "tagline", "url", "timeZone", "postsPerPage", "frontPage", "postsPage", "logo", "icon":
		return "/site/" + key
	case "languages", "defaultLanguage":
		return "/site/language"
	case "authorUrl":
		return "/urls/author"
	case "types":
		name, field, _ := strings.Cut(rest, "/")
		_, custom := s.URLs.Types[pointerUnescaper.Replace(name)]
		switch {
		case field == "url" && name == content.TypePost:
			return "/urls/post"
		case field == "url" && name == content.TypePage:
			return "/urls/page"
		case (field == "url" || field == "archive") && custom:
			return "/urls/types/" + name + "/" + field
		}
	case "taxonomies":
		name, field, _ := strings.Cut(rest, "/")
		if _, ok := s.URLs.Taxonomies[pointerUnescaper.Replace(name)]; ok && field == "url" {
			return "/urls/taxonomies/" + name
		}
	}
	return p
}

// dedupe removes the errors that repeat the path and the message of an earlier one.
func dedupe(errs content.Errors) content.Errors {
	type key struct{ path, msg string }
	seen := make(map[key]bool, len(errs))
	out := errs[:0]
	for _, e := range errs {
		if k := (key{e.Path, e.Msg}); !seen[k] {
			seen[k] = true
			out = append(out, e)
		}
	}
	return out
}
