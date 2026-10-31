package render

import (
	"html/template"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// The data of the block templates, one type for each block type. Rich text and child blocks come as their HTML, the
// children joined with "\n"; the other fields hold the attributes of the block as written, "" or 0 if not set, unless
// their comments say otherwise.

// Common holds what every block that is not a part has.
type Common struct {
	ID     string // its block ID, such as "b_9x3v7k5m"
	Anchor string // for the id attribute: its anchor, or the automatic anchor of a heading; "" if none
	// For the class attribute: "b-<ID without b_>" if it has a style, "is-variant-<variant>" if the theme has its
	// variant, and its className, separated by spaces; "" if none.
	Class string
}

type Paragraph struct {
	Common
	TextAlign string // left, center or right
	Content   template.HTML
}

type Heading struct {
	Common
	Level     int    // 2 to 6
	TextAlign string // left, center or right
	Content   template.HTML
}

type List struct {
	Common
	Ordered  bool
	Start    int           // the number of the first item; 0 if not set or not ordered
	Children template.HTML // the items
}

// ListItem is the data of an item of a list, which is a part.
type ListItem struct {
	Children template.HTML // its paragraph, then its list if it has one
}

type Quote struct {
	Common
	Citation template.HTML // who or what is quoted
	Children template.HTML
}

type Image struct {
	Common
	Src         template.URL // the URL of the published file of its medium
	Alt         string       // its alt, else the alt of its medium in the language of the page; "" if decorative
	HasAlt      bool         // false if neither it nor its medium has an alt; then templates leave the attribute out
	Caption     template.HTML
	Link        *Link  // nil if it has none or its target cannot be linked
	Size        string // the name of an image size of the theme, such as "large"
	Width       string // a length, such as "300px" or "50%"
	AspectRatio string // a ratio, such as "16/9", or a number, such as "1.5"
	Align       string // left, center, right, wide or full
}

type Table struct {
	Common
	Caption          template.HTML
	Head, Body, Foot template.HTML // the rows of each section, in their order in the table; "" if none
}

// TableRow is the data of a row of a table, which is a part.
type TableRow struct {
	Section  string        // head, body or foot; body if not set
	Children template.HTML // the cells
}

// TableCell is the data of a cell of a table row, which is a part.
type TableCell struct {
	Header           bool   // a header cell, th
	Colspan, Rowspan int    // the number of columns and of rows that it spans
	TextAlign        string // left, center or right
	Content          template.HTML
}

type Buttons struct {
	Common
	Justify  string // left, center, right or spaceBetween
	Children template.HTML
}

type Button struct {
	Common
	Label string // the text of the button
	Link  *Link  // nil if it has none or its target cannot be linked
	Width string // 25%, 50%, 75% or 100%
}

type Separator struct {
	Common
}

type Spacer struct {
	Common
	Height content.Length // a preset or a length
}

type Code struct {
	Common
	Language string // the name of its language, such as "go"
	Code     string // the text
}

type Embed struct {
	Common
	Provider       string       // youtube, vimeo, x or googleMaps
	URL            template.URL // its url, encoded
	Src            template.URL // of the iframe; "" for x, which is rendered as a link to URL
	Title          string       // of the iframe: the text of its caption, else the name of the provider
	Width, Height  int          // of the iframe, in pixels; 0 for x
	Allow          string       // the allow attribute of the iframe; "" if none
	ReferrerPolicy string       // the referrerpolicy attribute of the iframe; "" if none
	Caption        template.HTML
}

type Columns struct {
	Common
	VerticalAlign string        // top, center or bottom
	StackOnMobile bool          // whether the columns stack on narrow screens; true if not set
	Children      template.HTML // the columns
}

type Column struct {
	Common
	Width         string // a length, such as "33.33%"
	VerticalAlign string // top, center, bottom or stretch
	Children      template.HTML
}

type Group struct {
	Common
	TagName  string         // section, article or aside, or else div
	Label    string         // the heading of the group
	Layout   content.Layout // how the children are placed; zero if not set
	Align    string         // wide or full
	Children template.HTML
}

type File struct {
	Common
	Href     template.URL // the URL of the published file of its medium
	Label    string       // its label, else the file name of its medium
	FileName string       // of its medium, such as "guide.pdf"
	MIMEType string       // of its medium, such as "application/pdf"
	Size     int          // of its medium, in bytes
}

type Form struct {
	Common
	Action      template.URL // the URL that it is posted to: <base>/_forms/<form ID>
	Language    string       // of the page, for the hidden field _language
	Fields      []FormField  // in their order, without those of unknown types
	SubmitLabel string       // the text of the submit button
}

type FormField struct {
	Name                 string
	Type                 string       // text, email, tel, textarea, select, radio, checkbox or consent
	Label                string       // in the language of the page; for consent, the text of the consent
	Required             bool         // as defined; true for consent, which is always required
	MinLength, MaxLength int          // in characters; 0 if not set
	Options              []FormOption // of select, radio and checkbox
	// Of consent: the privacy policy page in the language of the page; nil if it has none there, or if the page does
	// not exist or is not published.
	Policy *Policy
}

type FormOption struct {
	Value string
	Label string // in the language of the page
}

// Policy is the privacy policy page that a consent field links to.
type Policy struct {
	Href  template.URL
	Title string // of the page
}
