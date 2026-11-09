package wpimport

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/html"
	"github.com/WhiteRoomLab/whiteroom/internal/wpblock"
)

// blockCounts are the names that the bodies of the items use, and how many times each.
type blockCounts struct {
	presets    []map[string]int // by presetKinds
	variants   map[string]int
	imageSizes map[string]int
	marks      map[string]int
	customHTML int
}

func newBlockCounts() *blockCounts {
	cc := &blockCounts{variants: make(map[string]int), imageSizes: make(map[string]int), marks: make(map[string]int)}
	for range presetKinds {
		cc.presets = append(cc.presets, make(map[string]int))
	}
	return cc
}

// preset counts a use of the preset slug of the kind.
func (cc *blockCounts) preset(k presetKind, slug string) {
	if slug != "" {
		cc.presets[slices.Index(presetKinds, k)][slug]++
	}
}

// proposeBlocks counts the presets, the block styles, the image sizes and the classes of inline elements that the
// bodies of the items use, and puts them in s.Blocks for the person who imports to map them to those of the theme of
// the site (6.10.5): each name maps to itself, if whiteroom accepts it as a name, and to nothing otherwise; the classes
// of inline elements map to nothing, which leaves only their text. Each kind is recorded once for review. It reads the
// items that become entries and the reusable blocks. It counts the blocks of the core that are not dynamic, with the
// blocks in them, also those that the conversion makes legacy blocks of for another reason, such as a video and a pattern
// that is not published; the blocks of themes and plugins, and the dynamic ones, are left out with the blocks in them.
// Of the custom HTML blocks it counts those that are legacy blocks unless the site allows custom HTML.
func (imp *Import) proposeBlocks(s *Settings, defs *content.Site) {
	cc := newBlockCounts()
	for i := range imp.WXR.Items {
		it := &imp.WXR.Items[i]
		if it.Status == "trash" || it.Status == "auto-draft" || it.Type != "wp_block" && typeName(it.Type, defs) == "" {
			continue
		}
		cc.addBody(it.Content)
	}

	fill := func(m map[string]string, counts map[string]int, keep bool) {
		for name := range counts {
			if keep && content.CheckName(name) == nil {
				m[name] = name
			} else {
				m[name] = ""
			}
		}
	}
	p := &s.Blocks.Presets
	for i, k := range presetKinds {
		fill(p.of(k), cc.presets[i], true)
		if n := len(cc.presets[i]); n > 0 {
			imp.channelEntry(Review, Target{}, "the blocks use %s of WordPress: %s; blocks.presets.%s maps them to the presets of the theme of the site",
				plural(n, kindWords[k.key]+" preset", kindWords[k.key]+" presets"), usedNames(cc.presets[i]), k.key)
		}
	}
	fill(s.Blocks.Variants, cc.variants, true)
	if n := len(cc.variants); n > 0 {
		imp.channelEntry(Review, Target{}, "the blocks use %s of WordPress: %s; blocks.variants maps them to the variants of the theme of the site",
			plural(n, "block style", "block styles"), usedNames(cc.variants))
	}
	fill(s.Blocks.ImageSizes, cc.imageSizes, true)
	if n := len(cc.imageSizes); n > 0 {
		imp.channelEntry(Review, Target{}, "the images use %s of WordPress: %s; blocks.imageSizes maps them to the image sizes of the theme of the site",
			plural(n, "image size", "image sizes"), usedNames(cc.imageSizes))
	}
	fill(s.Blocks.Marks, cc.marks, false)
	if n := len(cc.marks); n > 0 {
		imp.channelEntry(Review, Target{}, "the text uses %s of inline elements: %s; blocks.marks maps them to the highlight variants of the theme of the site, and the others keep only their text",
			plural(n, "class", "classes"), usedNames(cc.marks))
	}
	switch n := cc.customHTML; {
	case n == 1:
		imp.channelEntry(Review, settingsTarget("/allowHtml"), "1 custom HTML block becomes a legacy block unless site.allowHtml is true")
	case n > 1:
		imp.channelEntry(Review, settingsTarget("/allowHtml"), "%d custom HTML blocks become legacy blocks unless site.allowHtml is true", n)
	}
}

// kindWords are the names of the kinds of presets in the report.
var kindWords = map[string]string{
	kindColor.key: "color", kindGradient.key: "gradient", kindFontSize.key: "font size", kindFontFamily.key: "font family",
	kindSpacing.key: "spacing",
}

// plural writes n and the noun in the singular or the plural.
func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// usedNames lists the names with their counts, the most used first, and then in the order of the names: the first
// maxLostShown, and the number of the others.
func usedNames(counts map[string]int) string {
	names := slices.Sorted(maps.Keys(counts))
	slices.SortStableFunc(names, func(a, b string) int { return cmp.Compare(counts[b], counts[a]) })
	shown := names[:min(len(names), maxLostShown)]
	parts := make([]string, len(shown))
	for i, name := range shown {
		times := "times"
		if counts[name] == 1 {
			times = "time"
		}
		parts[i] = fmt.Sprintf("%q (%d %s)", name, counts[name], times)
	}
	s := strings.Join(parts, ", ")
	if more := len(names) - len(shown); more > 0 {
		s += fmt.Sprintf(", and %d more", more)
	}
	return s
}

// addBody counts the names in the blocks of the content text of an item.
func (cc *blockCounts) addBody(text string) {
	if len(text) > maxBodyBytes || blank(text) {
		return
	}
	blocks, err := wpblock.Parse(text, wpblock.Options{MaxBlocks: maxWPBlocks})
	if err != nil {
		return
	}
	budget := maxHTMLBytes
	var stack []*wpblock.Block
	for i := range blocks {
		stack = append(stack, &blocks[i])
	}
	for len(stack) > 0 {
		b := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if !strings.HasPrefix(b.Name, "core/") || dynamicBlock(b.Name) {
			continue
		}
		cc.addBlock(b, &budget)
		for i := range b.Inner {
			stack = append(stack, &b.Inner[i])
		}
	}
}

func (cc *blockCounts) addBlock(b *wpblock.Block, budget *int) {
	if a := b.Attrs; a != nil {
		for _, s := range []struct {
			key  string
			kind presetKind
		}{
			{"textColor", kindColor}, {"backgroundColor", kindColor}, {"borderColor", kindColor}, {"overlayColor", kindColor},
			{"gradient", kindGradient}, {"fontSize", kindFontSize}, {"fontFamily", kindFontFamily},
		} {
			if slug, ok := a.Get(s.key).Str(); ok {
				cc.preset(s.kind, slug)
			}
		}
		cc.addRefs(a)
		className, _ := a.Get("className").Str()
		seen := make(map[string]bool)
		for _, class := range strings.FieldsFunc(className, isHTMLSpace) {
			if name, ok := strings.CutPrefix(class, "is-style-"); ok && name != "" && name != "default" && !seen[class] {
				seen[class] = true
				cc.variants[name]++
			}
		}
		sizeKey := ""
		switch b.Name {
		case "core/image", "core/gallery":
			sizeKey = "sizeSlug"
		case "core/media-text":
			sizeKey = "mediaSizeSlug"
		}
		if size, ok := a.Get(sizeKey).Str(); ok && sizeKey != "" && size != "" && size != "full" {
			cc.imageSizes[size]++
		}
	}
	switch b.Name {
	case "core/pullquote":
		cc.variants["pullquote"]++
	case "core/html":
		if legacyUnlessAllowed(b.HTML(), budget) {
			cc.customHTML++
		}
	}
	if !plainHTML[b.Name] {
		cc.addInline(b.HTML(), budget)
	}
}

// legacyUnlessAllowed reports whether the custom HTML text is a legacy block if the site does not allow custom HTML: it is
// not nothing, and not one iframe that a provider embeds, which is an embed block whatever the site allows. It reads at
// most *budget bytes of HTML in all, and no text that is longer than the HTML of one block may be; text that it does not
// read is counted.
func legacyUnlessAllowed(text string, budget *int) bool {
	if blank(strings.Map(dropControl, text)) {
		return false
	}
	if !hasTag(text, "iframe") || len(text) > min(*budget, maxBlockHTMLBytes) {
		return true
	}
	*budget -= len(text)
	nodes, err := parseIn("div", text)
	if err != nil {
		return true
	}
	provider, _ := embedOf(nodes)
	return provider == ""
}

// plainHTML are the blocks whose HTML is not text with marks: code, HTML and shortcodes are taken as they are, and the
// spans of a cover are its background.
var plainHTML = map[string]bool{
	"core/html": true, "core/code": true, "core/preformatted": true, "core/shortcode": true, "core/cover": true,
}

// addRefs counts the references to presets in the strings of the attributes a, at any depth.
func (cc *blockCounts) addRefs(a *wpblock.Value) {
	stack := []*wpblock.Value{a}
	for len(stack) > 0 {
		v := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		switch v.Kind {
		case wpblock.String:
			if !strings.Contains(v.String, "preset") {
				continue
			}
			for _, m := range presetRefPattern.FindAllStringSubmatch(v.String, -1) {
				kind, slug := m[1], m[2]
				if kind == "" {
					kind, slug = m[3], m[4]
				}
				if k, ok := presetKindOf(kind); ok {
					cc.preset(k, slug)
				}
			}
		case wpblock.Array:
			for i := range v.Array {
				stack = append(stack, &v.Array[i])
			}
		case wpblock.Object:
			for i := range v.Object {
				stack = append(stack, &v.Object[i].Value)
			}
		case wpblock.Null, wpblock.Bool, wpblock.Number:
		}
	}
}

// addInline counts the classes of the mark and span elements in markup, the HTML of a block: the colors of the presets,
// and the other classes that are not WordPress's own, which a theme or a plugin gives. It reads at most budget bytes of
// HTML in all, and no markup that is longer than the HTML of one block may be.
func (cc *blockCounts) addInline(markup string, budget *int) {
	if !hasTag(markup, "span", "mark") || len(markup) > min(*budget, maxBlockHTMLBytes) {
		return
	}
	*budget -= len(markup)
	nodes, err := parseIn("div", markup)
	if err != nil {
		return
	}
	root := &html.Node{Type: html.DocumentFragmentNode}
	for _, n := range nodes {
		root.AppendChild(n)
	}
	for d := root.FirstChild; d != nil; d = after(root, d, true) {
		if d.Type != html.ElementNode || d.Namespace != html.HTML || d.Name != "mark" && d.Name != "span" {
			continue
		}
		for _, class := range classList(d) {
			switch slug, _, ok := colorClass(class); {
			case ok:
				cc.preset(kindColor, slug)
			case class == "has-inline-color", strings.HasPrefix(class, "wp-"), strings.HasPrefix(class, "has-"):
			default:
				cc.marks[class]++
			}
		}
	}
}

// hasTag reports whether s has the start of an element called one of names, in any case.
func hasTag(s string, names ...string) bool {
	for i := strings.IndexByte(s, '<'); i >= 0 && i+1 < len(s); {
		for _, name := range names {
			if len(s)-i-1 >= len(name) && strings.EqualFold(s[i+1:i+1+len(name)], name) {
				return true
			}
		}
		next := strings.IndexByte(s[i+1:], '<')
		if next < 0 {
			break
		}
		i += 1 + next
	}
	return false
}
