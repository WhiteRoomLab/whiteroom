package content

import (
	"encoding/json/v2"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// IDs of the menus in testdata.
const (
	testHeaderMenu = "mnu_01JBD2A3B4C5D6E7F8G9H0J1K2"
	testFooterMenu = "mnu_01JBD2M5N6P7Q8R9S0T1V2W3X4"
)

// The menus in testdata: the header, with a heading, terms without labels and an external link,
// and the footer, with a link to a page in another language.
var (
	headerMenu = Menu{ // header-menu-ja.json
		ID:       testHeaderMenu,
		Title:    "ヘッダー",
		Location: "header",
		Language: "ja",
		Items: []MenuItem{
			{Label: "会社概要", Entry: testAboutPage},
			{Label: "サービス", Children: []MenuItem{
				{Label: "サービス一覧", Entry: testPage},
				{Label: "制作実績", Href: "/works/"},
			}},
			{Term: testNewsCategory, Children: []MenuItem{{Term: testPressReleaseCategory}}},
			{Label: "採用情報", Href: "https://recruit.example.com/", NewTab: true},
		},
	}
	footerMenu = Menu{ // footer-menu-ja.json
		ID:       testFooterMenu,
		Title:    "フッター",
		Location: "footer",
		Language: "ja",
		Items: []MenuItem{
			{Label: "お問い合わせ", Entry: testContactPage},
			{Label: "プライバシーポリシー", Entry: testPrivacyPage},
			{Label: "English", Entry: testEnglishPage},
		},
	}
)

// cloneMenuItems returns a deep copy of items.
func cloneMenuItems(items []MenuItem) []MenuItem {
	items = slices.Clone(items)
	for i := range items {
		items[i].Children = cloneMenuItems(items[i].Children)
	}
	return items
}

// countMenuItems returns the number of items, including their descendants.
func countMenuItems(items []MenuItem) int {
	n := len(items)
	for _, x := range items {
		n += countMenuItems(x.Children)
	}
	return n
}

func TestMenuUnmarshalJSON(t *testing.T) {
	tests := []struct {
		file string
		want Menu
	}{
		{file: "header-menu-ja.json", want: headerMenu},
		{file: "footer-menu-ja.json", want: footerMenu},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			in := readTestdata(t, tt.file)
			var got Menu
			if err := json.Unmarshal([]byte(in), &got); err != nil {
				t.Fatalf("Unmarshal(%s) = %v", in, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Unmarshal(%s) = %#v, want %#v", in, got, tt.want)
			}
		})
	}
}

func TestMenuRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string // "" if in is canonical
	}{
		{name: "header", in: readTestdata(t, "header-menu-ja.json")},
		{name: "footer", in: readTestdata(t, "footer-menu-ja.json")},
		{name: "schemaVersion only", in: `{"schemaVersion":1}`},
		{
			name: "reversed key order",
			in: `{"items":[{"children":[{"href":"/works/","label":"制作実績"}],"newTab":true,"href":"/services/",` +
				`"term":"` + testCategory + `","entry":"` + testPage + `","label":"サービス"}],` +
				`"language":"ja","location":"header","title":"ヘッダー","id":"` + testHeaderMenu + `","schemaVersion":1}`,
			want: `{"schemaVersion":1,"id":"` + testHeaderMenu + `","title":"ヘッダー","location":"header","language":"ja",` +
				`"items":[{"label":"サービス","entry":"` + testPage + `","term":"` + testCategory + `","href":"/services/",` +
				`"newTab":true,"children":[{"label":"制作実績","href":"/works/"}]}]}`,
		},
		{
			name: "newTab false",
			in:   `{"schemaVersion":1,"items":[{"label":"制作実績","href":"/works/","newTab":false}]}`,
			want: `{"schemaVersion":1,"items":[{"label":"制作実績","href":"/works/"}]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := tt.want
			if want == "" {
				want = tt.in
			}
			assertRoundTrip[Menu](t, tt.in, want)
		})
	}
}

func TestMenuMarshalJSON(t *testing.T) {
	tests := []struct {
		name string
		in   Menu
		want string
	}{
		{name: "empty", in: Menu{}, want: `{"schemaVersion":1}`},
		{name: "empty items", in: Menu{Items: []MenuItem{}}, want: `{"schemaVersion":1}`},
		{
			name: "empty children",
			in:   Menu{Items: []MenuItem{{Label: "サービス", Children: []MenuItem{}}}},
			want: `{"schemaVersion":1,"items":[{"label":"サービス"}]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := json.Marshal(tt.in)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Errorf("Marshal(%#v) = %s, want %s", tt.in, got, tt.want)
			}
		})
	}
}

func TestMenuItemUnmarshalJSON(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want MenuItem
	}{
		{
			name: "heading",
			in:   `{"label":"サービス","children":[{"label":"サービス一覧","entry":"` + testPage + `"},{"label":"制作実績","href":"/works/"}]}`,
			want: headerMenu.Items[1],
		},
		{
			name: "terms without labels",
			in:   `{"term":"` + testNewsCategory + `","children":[{"term":"` + testPressReleaseCategory + `"}]}`,
			want: headerMenu.Items[2],
		},
		{name: "new tab", in: `{"label":"採用情報","href":"https://recruit.example.com/","newTab":true}`, want: headerMenu.Items[3]},
		{name: "newTab false", in: `{"label":"制作実績","href":"/works/","newTab":false}`, want: MenuItem{Label: "制作実績", Href: "/works/"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got MenuItem
			if err := json.Unmarshal([]byte(tt.in), &got); err != nil {
				t.Fatalf("Unmarshal(%s) = %v", tt.in, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Unmarshal(%s) = %#v, want %#v", tt.in, got, tt.want)
			}
		})
	}
	// An item alone is read as strictly as in a menu.
	var item MenuItem
	assertError(t, json.Unmarshal([]byte(`{"label":"a","Label":"b"}`), &item), "/Label", "unknown key")
}

func TestMenuUnmarshalJSONError(t *testing.T) {
	tests := []struct {
		name string
		in   string
		path string
		msg  string
	}{
		{name: "empty", in: `{}`, path: "", msg: "empty object is not allowed"},
		{
			name: "missing schemaVersion",
			in:   `{"id":"x","title":"x","location":"x","language":"x","items":[{"label":"x","href":"/x/"}]}`,
			path: "",
			msg:  `missing key "schemaVersion"`,
		},
		{name: "schemaVersion 2", in: `{"schemaVersion":2}`, path: "/schemaVersion", msg: "unsupported schemaVersion 2 (use 1)"},
		{name: "unknown key", in: `{"schemaVersion":1,"name":"x"}`, path: "/name", msg: "unknown key"},
		{name: "translation group", in: `{"schemaVersion":1,"translationGroup":"` + testGroup + `"}`, path: "/translationGroup", msg: "unknown key"},
		{name: "key case", in: `{"schemaVersion":1,"Title":"x"}`, path: "/Title", msg: "unknown key"},
		{
			name: "unknown key in an item",
			in:   `{"schemaVersion":1,"items":[{"label":"x","media":"` + testPDF + `"}]}`,
			path: "/items/0/media",
			msg:  "unknown key",
		},
		{
			name: "unknown key in a child",
			in:   `{"schemaVersion":1,"items":[{"label":"x","children":[{"label":"y","href":"/y/","rel":["nofollow"]}]}]}`,
			path: "/items/0/children/0/rel",
			msg:  "unknown key",
		},
		{name: "key case in an item", in: `{"schemaVersion":1,"items":[{"Label":"x"}]}`, path: "/items/0/Label", msg: "unknown key"},
		{name: "empty items", in: `{"schemaVersion":1,"items":[]}`, path: "/items", msg: "empty array is not allowed"},
		{
			name: "empty children",
			in:   `{"schemaVersion":1,"items":[{"label":"x","children":[]}]}`,
			path: "/items/0/children",
			msg:  "empty array is not allowed",
		},
		{name: "empty item", in: `{"schemaVersion":1,"items":[{}]}`, path: "/items/0", msg: "empty object is not allowed"},
		{name: "null items", in: `{"schemaVersion":1,"items":null}`, path: "/items", msg: "expected array, got null"},
		{name: "null item", in: `{"schemaVersion":1,"items":[null]}`, path: "/items/0", msg: "expected object, got null"},
		{name: "null label", in: `{"schemaVersion":1,"items":[{"label":null}]}`, path: "/items/0/label", msg: "expected string, got null"},
		{name: "item for items", in: `{"schemaVersion":1,"items":{"label":"x","href":"/x/"}}`, path: "/items", msg: "expected array, got object"},
		{
			name: "link for href",
			in:   `{"schemaVersion":1,"items":[{"label":"x","href":{"entry":"` + testPage + `"}}]}`,
			path: "/items/0/href",
			msg:  "expected string, got object",
		},
		{
			name: "string for newTab",
			in:   `{"schemaVersion":1,"items":[{"label":"x","href":"/x/","newTab":"true"}]}`,
			path: "/items/0/newTab",
			msg:  "expected boolean, got string",
		},
		{name: "empty label", in: `{"schemaVersion":1,"items":[{"label":"","href":"/x/"}]}`, path: "/items/0/label", msg: "empty string is not allowed"},
		{name: "empty location", in: `{"schemaVersion":1,"location":""}`, path: "/location", msg: "empty string is not allowed"},
		{name: "duplicate key", in: `{"schemaVersion":1,"title":"a","title":"b"}`, path: "/title", msg: "duplicate key"},
		{
			name: "duplicate key in an item",
			in:   `{"schemaVersion":1,"items":[{"label":"a","label":"b"}]}`,
			path: "/items/0/label",
			msg:  "duplicate key",
		},
		{name: "trailing data", in: `{"schemaVersion":1} {}`, path: "", msg: "unexpected data after value"},
	}
	// old makes a new menu on each call, so that changes to its items in place show.
	old := func() Menu { return Menu{ID: "old", Items: []MenuItem{{Label: "old"}}} }
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := old()
			assertError(t, got.UnmarshalJSON([]byte(tt.in)), tt.path, tt.msg)
			if !reflect.DeepEqual(got, old()) {
				t.Errorf("UnmarshalJSON(%s) changed the menu to %#v", tt.in, got)
			}
		})
	}
}

func TestMenuUnmarshalJSONLimits(t *testing.T) {
	const (
		tooDeep = "menu items are nested too deeply (max 8)"
		tooMany = "too many menu items (max 1000)"
	)
	// nested is n items, each in the children of the one before, with a link in the last.
	nested := func(n int) string {
		return strings.Repeat(`{"label":"x","children":[`, n-1) + `{"label":"x","href":"/x/"}` + strings.Repeat(`]}`, n-1)
	}
	// links is n items with links, separated by commas.
	links := func(n int) string { return strings.TrimSuffix(strings.Repeat(`{"label":"x","href":"/x/"},`, n), ",") }
	heading := func(n int) string { return `{"label":"x","children":[` + links(n) + `]}` }
	menu := func(items string) string { return `{"schemaVersion":1,"items":[` + items + `]}` }
	tests := []struct {
		name  string
		item  bool // in is an item for MenuItem.UnmarshalJSON, not a menu
		in    string
		items int    // items read if path is ""
		path  string // path of the error
		msg   string
	}{
		{name: "deepest", in: menu(nested(MaxMenuDepth)), items: MaxMenuDepth},
		{
			name: "too deep",
			in:   menu(nested(MaxMenuDepth + 1)),
			path: "/items/0" + strings.Repeat("/children/0", MaxMenuDepth-1) + "/children",
			msg:  tooDeep,
		},
		{name: "most items", in: menu(links(MaxMenuItems)), items: MaxMenuItems},
		{name: "too many items", in: menu(links(MaxMenuItems + 1)), path: "/items/1000", msg: tooMany},
		{name: "too many items in the children", in: menu(heading(MaxMenuItems)), path: "/items/0/children/999", msg: tooMany},
		{name: "item after a heading with 999 children", in: menu(heading(MaxMenuItems-1) + "," + links(1)), path: "/items/1", msg: tooMany},
		{name: "deepest in an item", item: true, in: nested(MaxMenuDepth), items: MaxMenuDepth},
		{
			name: "too deep in an item",
			item: true,
			in:   nested(MaxMenuDepth + 1),
			path: strings.Repeat("/children/0", MaxMenuDepth-1) + "/children",
			msg:  tooDeep,
		},
		{name: "most items in an item", item: true, in: heading(MaxMenuItems - 1), items: MaxMenuItems},
		{name: "too many items in an item", item: true, in: heading(MaxMenuItems), path: "/children/999", msg: tooMany},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var items []MenuItem
			var err error
			if tt.item {
				var x MenuItem
				err = x.UnmarshalJSON([]byte(tt.in))
				items = []MenuItem{x}
			} else {
				var x Menu
				err = x.UnmarshalJSON([]byte(tt.in))
				items = x.Items
			}
			if tt.path != "" {
				assertError(t, err, tt.path, tt.msg)
				return
			}
			if err != nil {
				t.Fatalf("UnmarshalJSON(%.100s...) = %v", tt.in, err)
			}
			if n := countMenuItems(items); n != tt.items {
				t.Errorf("UnmarshalJSON(%.100s...) read %d items, want %d", tt.in, n, tt.items)
			}
		})
	}
}

func TestMenuNormalize(t *testing.T) {
	const ctrl = string(rune(0x0001))
	items := func(xs ...MenuItem) Menu { return Menu{Items: xs} }
	tests := []struct {
		name string
		in   Menu
		want Menu
	}{
		{
			name: "control characters",
			in:   Menu{Title: "a" + ctrl + "b", Items: []MenuItem{{Label: "c" + ctrl + "d", Href: "/e" + ctrl + "f/"}}},
			want: Menu{Title: "ab", Items: []MenuItem{{Label: "cd", Href: "/ef/"}}},
		},
		{
			name: "NFC",
			in:   Menu{Title: "cafe" + acute, Items: []MenuItem{{Label: "cafe" + acute, Href: "/cafe" + acute + "/"}}},
			want: Menu{Title: "caf" + eAcute, Items: []MenuItem{{Label: "caf" + eAcute, Href: "/cafe" + acute + "/"}}},
		},
		{
			name: "line breaks and tabs left in the title and the labels",
			in:   Menu{Title: "a\r\nb\tc", Items: []MenuItem{{Label: "d\r\ne"}}},
			want: Menu{Title: "a\r\nb\tc", Items: []MenuItem{{Label: "d\r\ne"}}},
		},
		{
			name: "spaces and control characters in the href",
			in:   items(MenuItem{Label: "a", Href: " https://recruit.example.com/jo\tbs/\n"}),
			want: items(MenuItem{Label: "a", Href: "https://recruit.example.com/jobs/"}),
		},
		{name: "href of which nothing is left", in: items(MenuItem{Label: "a", Href: " " + ctrl}), want: items(MenuItem{Label: "a"})},
		{
			name: "children",
			in: items(MenuItem{Label: "a" + ctrl, Children: []MenuItem{
				{Label: "cafe" + acute, Children: []MenuItem{{Label: "b" + ctrl, Href: " /b/ "}}},
			}}),
			want: items(MenuItem{Label: "a", Children: []MenuItem{
				{Label: "caf" + eAcute, Children: []MenuItem{{Label: "b", Href: "/b/"}}},
			}}),
		},
		{
			name: "other items left as they are",
			in:   Menu{ID: "x" + ctrl, Location: "Header" + ctrl, Language: "JA", Items: []MenuItem{{Entry: "e" + ctrl, Term: "t" + ctrl, NewTab: true}}},
			want: Menu{ID: "x" + ctrl, Location: "Header" + ctrl, Language: "JA", Items: []MenuItem{{Entry: "e" + ctrl, Term: "t" + ctrl, NewTab: true}}},
		},
		{name: "header", in: headerMenu, want: headerMenu},
		{name: "footer", in: footerMenu, want: footerMenu},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.in
			// Normalize changes the items in place.
			got.Items = cloneMenuItems(tt.in.Items)
			got.Normalize()
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Normalize() of %#v = %#v, want %#v", tt.in, got, tt.want)
			}
		})
	}
}

func TestMenuValidate(t *testing.T) {
	const (
		ctrl    = string(rune(0x0001))
		tooDeep = "menu items are nested too deeply (max 8)"
		tooMany = "too many menu items (max 1000)"
	)
	// menu returns x changed by f, which gets a copy of the items of x.
	menu := func(x Menu, f func(x *Menu)) Menu {
		x.Items = cloneMenuItems(x.Items)
		f(&x)
		return x
	}
	// items returns a menu in Japanese with xs.
	items := func(xs ...MenuItem) Menu {
		return Menu{ID: testHeaderMenu, Title: "ヘッダー", Location: "header", Language: "ja", Items: xs}
	}
	link := MenuItem{Label: "制作実績", Href: "/works/"}
	links := func(n int) []MenuItem { return slices.Repeat([]MenuItem{link}, n) }
	// nested is n headings, each in the children of the one before, with inner in the last.
	nested := func(n int, inner MenuItem) MenuItem {
		x := inner
		for range n {
			x = MenuItem{Label: "x", Children: []MenuItem{x}}
		}
		return x
	}
	tests := []struct {
		name     string
		in       Menu
		noLookup bool // validate without lookup
		paths    []string
		msg      string // part of the error text, a "path: message" line for each problem; not checked if ""
	}{
		{name: "header", in: headerMenu},
		{name: "footer", in: footerMenu},
		{name: "header without lookup", in: headerMenu, noLookup: true},
		{name: "footer without lookup", in: footerMenu, noLookup: true},
		{name: "required items only", in: Menu{ID: testHeaderMenu, Title: "ヘッダー", Language: "ja"}},
		{
			name:  "missing items",
			in:    Menu{},
			paths: []string{"/id", "/title", "/language"},
			msg:   "/id: missing id\n/title: missing title\n/language: missing language",
		},
		{
			// The errors are in document order.
			name: "invalid items",
			in: Menu{
				ID:       testAboutPage,
				Title:    "a\tb",
				Location: "ヘッダー",
				Language: "fr",
				Items: []MenuItem{
					{Label: "a\nb", Entry: testAboutPage, Href: " /about/"},
					{NewTab: true, Children: []MenuItem{{Label: "c", Entry: testCategory}}},
				},
			},
			paths: []string{
				"/id", "/title", "/location", "/language", "/items/0", "/items/0/label", "/items/0/href",
				"/items/1/label", "/items/1/newTab", "/items/1/children/0/entry",
			},
			msg: `/id: invalid menu ID "` + testAboutPage + `": use "mnu_" and a ULID (26 characters in upper case)` + "\n" +
				"/title: contains a line break or tab\n" +
				`/location: invalid name "ヘッダー": use ASCII letters, digits, "-" and "_"` + "\n" +
				`/language: language "fr" is not in the site settings (use ja, en)` + "\n" +
				"/items/0: set at most one of entry, term and href\n" +
				"/items/0/label: contains a line break or tab\n" +
				"/items/0/href: contains control characters or surrounding spaces; normalize it first\n" +
				"/items/1/label: missing label\n" +
				"/items/1/newTab: not allowed for an item without a link\n" +
				`/items/1/children/0/entry: invalid entry ID "` + testCategory + `": use "ent_" and a ULID (26 characters in upper case)`,
		},
		{
			name:  "ID of another kind",
			in:    menu(headerMenu, func(x *Menu) { x.ID = testAboutPage }),
			paths: []string{"/id"},
			msg:   `/id: invalid menu ID "` + testAboutPage + `": use "mnu_" and a ULID (26 characters in upper case)`,
		},
		{
			name:  "line break in the title",
			in:    menu(headerMenu, func(x *Menu) { x.Title = "ヘッダー\nメニュー" }),
			paths: []string{"/title"},
			msg:   "/title: contains a line break or tab",
		},
		{
			name:  "title not in NFC",
			in:    menu(headerMenu, func(x *Menu) { x.Title = "cafe" + acute }),
			paths: []string{"/title"},
			msg:   "/title: not in Unicode NFC; normalize it first",
		},
		{name: "without a location", in: menu(headerMenu, func(x *Menu) { x.Location = "" })},
		{name: "location of a theme", in: menu(headerMenu, func(x *Menu) { x.Location = "mobile_drawer-2" })},
		{
			name:  "location with a space",
			in:    menu(headerMenu, func(x *Menu) { x.Location = "header menu" }),
			paths: []string{"/location"},
			msg:   `/location: invalid name "header menu": use ASCII letters, digits, "-" and "_"`,
		},
		{
			name:  "Japanese location",
			in:    menu(headerMenu, func(x *Menu) { x.Location = "ヘッダー" }),
			paths: []string{"/location"},
			msg:   `/location: invalid name "ヘッダー"`,
		},
		{
			name:  "language not in the site settings",
			in:    menu(headerMenu, func(x *Menu) { x.Language = "fr" }),
			paths: []string{"/language"},
			msg:   `/language: language "fr" is not in the site settings (use ja, en)`,
		},
		{name: "menu in English with pages in Japanese", in: menu(footerMenu, func(x *Menu) { x.Language = "en" })},

		{name: "entries of any type", in: items(MenuItem{Label: "製品", Entry: testProduct}, MenuItem{Label: "お知らせ", Entry: testPost})},
		{name: "terms of any taxonomy", in: items(MenuItem{Label: "イベント", Term: testTag}, MenuItem{Label: "Webサイト", Term: testWorksTerm})},
		{name: "entry in another language", in: items(MenuItem{Label: "English", Entry: testEnglishPage})},
		{name: "term in another language", in: items(MenuItem{Label: "News", Term: testEnglishCategory})},
		{name: "entry without a label", in: items(MenuItem{Entry: testAboutPage})},
		{name: "term without a label", in: items(MenuItem{Term: testCategory})},
		{
			name:  "href without a label",
			in:    items(MenuItem{Href: "/works/"}),
			paths: []string{"/items/0/label"},
			msg:   "/items/0/label: missing label",
		},
		{name: "heading", in: items(MenuItem{Label: "サービス", Children: []MenuItem{link}})},
		{
			name:  "heading without a label",
			in:    items(MenuItem{Children: []MenuItem{link}}),
			paths: []string{"/items/0/label"},
			msg:   "/items/0/label: missing label",
		},
		{name: "link with children", in: items(MenuItem{Label: "サービス", Entry: testPage, Children: []MenuItem{link}})},
		{
			name:  "item without a link or children",
			in:    items(MenuItem{Label: "サービス"}),
			paths: []string{"/items/0"},
			msg:   "/items/0: menu item needs entry, term, href or children",
		},
		{
			name:  "empty item",
			in:    items(MenuItem{}),
			paths: []string{"/items/0", "/items/0/label"},
			msg:   "/items/0: menu item needs entry, term, href or children\n/items/0/label: missing label",
		},
		{
			name:  "entry and href",
			in:    items(MenuItem{Label: "会社概要", Entry: testAboutPage, Href: "/about/"}),
			paths: []string{"/items/0"},
			msg:   "/items/0: set at most one of entry, term and href",
		},
		{
			name:  "entry and term",
			in:    items(MenuItem{Entry: testAboutPage, Term: testCategory}),
			paths: []string{"/items/0"},
			msg:   "/items/0: set at most one of entry, term and href",
		},
		{
			name:  "entry, term and href",
			in:    items(MenuItem{Entry: testAboutPage, Term: testCategory, Href: "/about/", Children: []MenuItem{link}}),
			paths: []string{"/items/0"},
			msg:   "/items/0: set at most one of entry, term and href",
		},
		{
			name:  "line break in a label",
			in:    items(MenuItem{Label: "会社\n概要", Entry: testAboutPage}),
			paths: []string{"/items/0/label"},
			msg:   "/items/0/label: contains a line break or tab",
		},
		{
			name:  "control character in a label",
			in:    items(MenuItem{Label: "会社概要" + ctrl, Entry: testAboutPage}),
			paths: []string{"/items/0/label"},
			msg:   "/items/0/label: contains control characters; normalize it first",
		},
		{
			name:  "label not in NFC",
			in:    items(MenuItem{Label: "cafe" + acute, Entry: testAboutPage}),
			paths: []string{"/items/0/label"},
			msg:   "/items/0/label: not in Unicode NFC; normalize it first",
		},
		{name: "external href", in: items(MenuItem{Label: "採用情報", Href: "https://recruit.example.com/"})},
		{name: "href with a fragment", in: items(MenuItem{Label: "アクセス", Href: "/about/#access"})},
		{name: "mailto href", in: items(MenuItem{Label: "メール", Href: "mailto:info@example.com"})},
		{name: "href not in NFC", in: items(MenuItem{Label: "caf" + eAcute, Href: "/cafe" + acute + "/"})},
		{
			name:  "javascript href",
			in:    items(MenuItem{Label: "a", Href: "javascript:alert(1)"}),
			paths: []string{"/items/0/href"},
			msg:   `/items/0/href: scheme "javascript" is not allowed`,
		},
		{
			name:  "href with surrounding spaces",
			in:    items(MenuItem{Label: "a", Href: " /works/"}),
			paths: []string{"/items/0/href"},
			msg:   "/items/0/href: contains control characters or surrounding spaces; normalize it first",
		},
		{
			name:  "protocol-relative href",
			in:    items(MenuItem{Label: "a", Href: "//example.com/"}),
			paths: []string{"/items/0/href"},
			msg:   "/items/0/href: protocol-relative URLs are not allowed",
		},
		{
			name:  "relative href",
			in:    items(MenuItem{Label: "a", Href: "works/"}),
			paths: []string{"/items/0/href"},
			msg:   `/items/0/href: must be an absolute URL or a path starting with "/"`,
		},
		{
			name:  "entry that does not exist",
			in:    items(MenuItem{Label: "a", Entry: "ent_" + testMissing}),
			paths: []string{"/items/0/entry"},
			msg:   `/items/0/entry: entry "ent_` + testMissing + `" does not exist`,
		},
		{name: "entry that does not exist without lookup", in: items(MenuItem{Label: "a", Entry: "ent_" + testMissing}), noLookup: true},
		{
			name:  "term that does not exist",
			in:    items(MenuItem{Term: "trm_" + testMissing}),
			paths: []string{"/items/0/term"},
			msg:   `/items/0/term: term "trm_` + testMissing + `" does not exist`,
		},
		{name: "term that does not exist without lookup", in: items(MenuItem{Term: "trm_" + testMissing}), noLookup: true},
		{
			name:  "entry of another kind",
			in:    items(MenuItem{Label: "a", Entry: testCategory}),
			paths: []string{"/items/0/entry"},
			msg:   `/items/0/entry: invalid entry ID "` + testCategory + `"`,
		},
		{
			name:     "entry of another kind without lookup",
			in:       items(MenuItem{Label: "a", Entry: testCategory}),
			noLookup: true,
			paths:    []string{"/items/0/entry"},
			msg:      `/items/0/entry: invalid entry ID "` + testCategory + `"`,
		},
		{
			name:  "term of another kind",
			in:    items(MenuItem{Term: testAboutPage}),
			paths: []string{"/items/0/term"},
			msg:   `/items/0/term: invalid term ID "` + testAboutPage + `"`,
		},
		{name: "new tab of an entry", in: items(MenuItem{Label: "a", Entry: testAboutPage, NewTab: true})},
		{name: "new tab of a term", in: items(MenuItem{Term: testCategory, NewTab: true})},
		{
			name:  "new tab of a heading",
			in:    items(MenuItem{Label: "サービス", NewTab: true, Children: []MenuItem{link}}),
			paths: []string{"/items/0/newTab"},
			msg:   "/items/0/newTab: not allowed for an item without a link",
		},
		{
			name: "errors in the children",
			in: menu(headerMenu, func(x *Menu) {
				x.Items[1].Children[1].Href = "javascript:alert(1)"
				x.Items[2].Children[0].Term = "trm_" + testMissing
			}),
			paths: []string{"/items/1/children/1/href", "/items/2/children/0/term"},
			msg: `/items/1/children/1/href: scheme "javascript" is not allowed` + "\n" +
				`/items/2/children/0/term: term "trm_` + testMissing + `" does not exist`,
		},

		{name: "deepest", in: items(nested(MaxMenuDepth-1, link))},
		{
			// The item at depth 9 is not checked.
			name:  "too deep",
			in:    items(nested(MaxMenuDepth, MenuItem{})),
			paths: []string{"/items/0" + strings.Repeat("/children/0", MaxMenuDepth-1) + "/children"},
			msg:   tooDeep,
		},
		{name: "most items", in: items(links(MaxMenuItems)...)},
		{
			name:  "too many items",
			in:    items(links(MaxMenuItems + 1)...),
			paths: []string{"/items/1000"},
			msg:   "/items/1000: " + tooMany,
		},
		{
			name:  "more items",
			in:    items(links(MaxMenuItems + 2)...),
			paths: []string{"/items/1000"},
			msg:   "/items/1000: " + tooMany,
		},
		{
			name:  "too many items in the children",
			in:    items(MenuItem{Label: "サービス", Children: links(MaxMenuItems)}),
			paths: []string{"/items/0/children/999"},
			msg:   "/items/0/children/999: " + tooMany,
		},
		{
			name:  "invalid item counted",
			in:    items(append(links(MaxMenuItems), MenuItem{})...),
			paths: []string{"/items/1000", "/items/1000", "/items/1000/label"},
			msg:   "/items/1000: " + tooMany + "\n/items/1000: menu item needs entry, term, href or children\n/items/1000/label: missing label",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lookup := testLookup
			if tt.noLookup {
				lookup = nil
			}
			err := tt.in.Validate(testSite(), lookup)
			if tt.paths == nil && err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
			if got := errorPaths(t, err); !reflect.DeepEqual(got, tt.paths) {
				t.Errorf("Validate() paths = %q, want %q", got, tt.paths)
			}
			if tt.msg != "" {
				assertErrorMsg(t, err, tt.msg)
			}
		})
	}
}
