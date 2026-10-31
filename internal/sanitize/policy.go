package sanitize

// elements is the HTML elements that both kinds keep; h1 elements are kept as h2.
var elements = set(
	"a", "abbr", "address", "article", "aside", "b", "bdi", "bdo", "blockquote", "br", "caption", "cite", "code", "col",
	"colgroup", "data", "dd", "del", "details", "dfn", "div", "dl", "dt", "em", "figcaption", "figure", "footer", "h2",
	"h3", "h4", "h5", "h6", "header", "hgroup", "hr", "i", "img", "ins", "kbd", "li", "mark", "menu", "nav", "ol", "p",
	"pre", "q", "rp", "rt", "ruby", "s", "samp", "search", "section", "small", "span", "strong", "sub", "summary", "sup",
	"table", "tbody", "td", "tfoot", "th", "thead", "time", "tr", "u", "ul", "var", "wbr",
)

// htmlElements is the elements that the HTML kind keeps too, and that Legacy removes with their content.
var htmlElements = set("iframe", "video", "audio", "source", "track")

// globalAttrs is the attributes that every kept element keeps.
var globalAttrs = set("dir", "lang", "title", "id", "class")

// elementAttrs is the attributes that an element keeps besides globalAttrs.
var elementAttrs = map[string][]string{
	"a":          {"href", "hreflang", "type", "target", "rel"},
	"blockquote": {"cite"},
	"del":        {"cite", "datetime"},
	"ins":        {"cite", "datetime"},
	"ol":         {"reversed", "start", "type"},
	"li":         {"value"},
	"data":       {"value"},
	"time":       {"datetime"},
	"colgroup":   {"span"},
	"col":        {"span"},
	"td":         {"colspan", "headers", "rowspan"},
	"th":         {"abbr", "colspan", "headers", "rowspan", "scope"},
	"img":        {"src", "alt", "width", "height", "loading"},
	"details":    {"open"},
	"iframe":     {"src", "width", "height", "loading", "allow", "allowfullscreen", "referrerpolicy", "sandbox"},
	"video":      {"src", "poster", "controls", "width", "height", "autoplay", "muted", "loop", "playsinline", "preload"},
	"audio":      {"src", "controls", "autoplay", "muted", "loop", "preload"},
	"source":     {"src", "type"},
	"track":      {"src", "kind", "srclang", "label", "default"},
}

// urlKind is a kind of URL, which allows the schemes of schemes.
type urlKind uint8

const (
	noURL urlKind = iota
	linkURL
	imageURL
	mediaURL
	frameURL
)

// urlAttrs is the kind of URL of each attribute that holds one, by element and attribute name.
var urlAttrs = map[[2]string]urlKind{
	{"a", "href"}:          linkURL,
	{"blockquote", "cite"}: linkURL,
	{"del", "cite"}:        linkURL,
	{"ins", "cite"}:        linkURL,
	{"img", "src"}:         imageURL,
	{"video", "src"}:       mediaURL,
	{"audio", "src"}:       mediaURL,
	{"source", "src"}:      mediaURL,
	{"track", "src"}:       mediaURL,
	{"video", "poster"}:    mediaURL,
	{"iframe", "src"}:      frameURL,
}

// schemes is the schemes of URLs that content.CheckHref accepts and that each kind of URL allows, "" for paths
// starting with "/". Links also allow fragments ("#top").
var schemes = map[urlKind][]string{
	linkURL:  {"http", "https", "mailto", "tel", ""},
	imageURL: {"http", "https", ""},
	mediaURL: {"https", ""},
	frameURL: {"https"},
}

// needSrc is the kept elements that are removed without a valid src.
var needSrc = set("img", "iframe", "source", "track")

// relValues is the values of rel that are kept.
var relValues = []string{"nofollow", "sponsored", "ugc", "noopener", "noreferrer"}

// dropped is the HTML elements removed with their content, which is not page text. The other elements that are not
// kept are unwrapped: they are removed, and their children kept in their place.
var dropped = set(
	"script", "style", "template", "noscript", "noembed", "noframes", "object", "embed", "applet", "frameset", "frame",
	"select", "datalist", "optgroup", "option", "textarea", "title", "head", "canvas", "param", "base", "link", "meta",
	"input", "keygen",
)

// void is the HTML elements without end tags, which have no content; for the messages.
var void = set(
	"area", "base", "basefont", "bgsound", "br", "col", "embed", "frame", "hr", "img", "input", "keygen", "link", "meta",
	"param", "source", "track", "wbr",
)

func set(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, s := range names {
		m[s] = true
	}
	return m
}
