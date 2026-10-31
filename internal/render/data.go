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
