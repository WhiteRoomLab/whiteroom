package wpimport

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/wxr"
)

// fieldsSchema is the schema file of the tests of the custom fields: the type works has a field of each type that the
// import converts, and others that it does not, gear has a required field, and the posts have a subtitle. genre is a
// taxonomy that works uses.
const fieldsSchema = `schemaVersion: 1
taxonomies:
  genre:
    label: Genre
types:
  post:
    fields:
      subtitle: {type: text}
  works:
    label: Works
    body: true
    taxonomies: [genre]
    fields:
      client: {type: text}
      memo: {type: textarea}
      year: {type: number}
      onsale: {type: boolean}
      since: {type: date}
      opens: {type: datetime}
      size: {type: select, options: [S, M, L]}
      colors: {type: select, options: [red, green, blue], multiple: true}
      cover: {type: media, accept: [image]}
      manual: {type: media, accept: [pdf]}
      photo: {type: media}
      maker: {type: entry, types: [post]}
      partner: {type: entry}
      related: {type: entry, multiple: true}
      topic: {type: term, taxonomy: category}
      keywords: {type: term, taxonomy: tag, multiple: true}
      style: {type: term, taxonomy: genre}
      website: {type: link}
      redirect: {type: text}
      address:
        type: group
        fields:
          city: {type: text}
          zip: {type: text}
          geo:
            type: group
            fields:
              lat: {type: number}
              lng: {type: number}
      gallery: {type: list, of: {type: media, accept: [image]}, maxItems: 5}
      urls: {type: list, of: {type: text}}
      flags: {type: list, of: {type: boolean}}
      steps:
        type: list
        of:
          type: group
          fields:
            name: {type: text}
      links: {type: list, of: {type: link}}
      items:
        type: list
        of:
          type: group
          fields:
            title: {type: text}
            image: {type: media, accept: [image]}
            amount: {type: number}
            sizes: {type: select, options: [S, M, L], multiple: true}
            parts:
              type: list
              of:
                type: group
                fields:
                  name: {type: text}
      matrix:
        type: list
        of:
          type: list
          of: {type: number}
      intro: {type: richText}
      sections: {type: blocks}
  gear:
    label: Gear
    body: true
    fields:
      model: {type: text, required: true}
  plain:
    label: Plain
    body: true
  nobody:
    label: No body
    fields:
      note: {type: text}
`

func fieldsDefs(t testing.TB) *content.Site {
	t.Helper()
	defs := new(content.Site)
	if err := defs.UnmarshalSchema([]byte(fieldsSchema)); err != nil {
		t.Fatalf("UnmarshalSchema = %v", err)
	}
	return defs
}

// fieldsSettings is entrySettings with the table fields of the settings.
func fieldsSettings(table map[string]map[string]string) *Settings {
	s := entrySettings()
	s.Fields = table
	return s
}

// fieldsSupport are the items that the fields refer to: the post 1, the page 3, the post 4 that is in the trash, the item 5
// of the type works, and the post 6 that has no date, which is not imported. The attachments of seoMedia are there too: the images 20 and 24, the PDF 21, and the image 22 that is in the
// trash.
func fieldsSupport() []wxrEntry {
	return append(seoMedia(),
		wxrEntry{id: 1, title: "One", name: "one", link: "https://example.com/post-1/"},
		wxrEntry{id: 3, typ: "page", title: "About", name: "about", link: "https://example.com/about/"},
		wxrEntry{id: 4, title: "Gone", name: "gone", status: "trash", link: "https://example.com/post-4/"},
		wxrEntry{id: 5, typ: "works", title: "Work", name: "work", link: "https://example.com/works/work/"},
		wxrEntry{id: 6, title: "Undated", name: "undated", date: zeroDate, dateGMT: zeroDate, modified: zeroDate, modifiedGMT: zeroDate},
	)
}

func fieldsChannel() string {
	return refsChannel(nil, []wxrTerm{
		{id: 2, taxonomy: "category", slug: "news", name: "News"},
		{id: 4, taxonomy: "post_tag", slug: "go", name: "Go"},
		{id: 5, taxonomy: "post_tag", slug: "rust", name: "Rust"},
		{id: 6, taxonomy: "genre", slug: "drama", name: "Drama"},
		{id: 7, taxonomy: "genre", slug: "comedy", name: "Comedy"},
	})
}

// fieldsRun makes the entries of the items and fieldsSupport with the settings s.
type fieldsRun struct {
	imp  *Import
	site *content.Site
	docs []*EntryDoc
	ids  *strings.Replacer
}

func runFields(t *testing.T, s *Settings, items ...wxrEntry) *fieldsRun {
	t.Helper()
	if s == nil {
		s = fieldsSettings(nil)
	}
	imp, site, docs := makeEntries(t, s, fieldsDefs(t), entriesWXR(fieldsChannel(), append(fieldsSupport(), items...)...))
	r := &fieldsRun{imp: imp, site: site, docs: docs}
	var pairs []string
	for _, post := range []int64{20, 21, 24} {
		pairs = append(pairs, fmt.Sprintf("{m%d}", post), postIDOf(t, imp, post))
	}
	for _, post := range []int64{1, 3, 5} {
		pairs = append(pairs, fmt.Sprintf("{e%d}", post), postIDOf(t, imp, post))
	}
	for _, c := range []struct{ name, taxonomy, slug string }{
		{"news", "category", "news"}, {"go", "post_tag", "go"}, {"rust", "post_tag", "rust"}, {"drama", "genre", "drama"}, {"comedy", "genre", "comedy"},
	} {
		pairs = append(pairs, "{t:"+c.name+"}", termIDOf(t, imp, c.taxonomy, c.slug))
	}
	r.ids = strings.NewReplacer(pairs...)
	return r
}

// fields returns the fields of the revision of the item post as JSON, or "" if it has none.
func (r *fieldsRun) fields(t *testing.T, post int64) string {
	t.Helper()
	d := docOf(r.docs, post)
	if d == nil {
		t.Fatalf("no entry was made of the item %d", post)
	}
	if d.Revision.Fields == nil {
		return ""
	}
	b, err := d.Revision.Fields.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// check compares the fields and the report about the item post with want and lines, in which the IDs of the media, the entries and
// the terms are written as {m20}, {e1} and {t:go}.
func (r *fieldsRun) check(t *testing.T, post int64, want string, lines ...string) {
	t.Helper()
	if got, want := r.fields(t, post), r.ids.Replace(want); got != want {
		t.Errorf("the fields of the item %d are\n%s\nwant\n%s", post, got, want)
	}
	expanded := make([]string, len(lines))
	for i, l := range lines {
		expanded[i] = r.ids.Replace(l)
	}
	wantItem(t, r.imp, r.docs, post, expanded...)
	checkFieldSources(t, r.imp, post)
}

// checkFieldSources checks that each record about the fields of the item post is about the postmeta that it names, at its
// place in the WXR, or else about the item.
func checkFieldSources(t *testing.T, imp *Import, post int64) {
	t.Helper()
	it := imp.WXR.Post(post)
	for _, e := range imp.Report.Entries() {
		if e.Source.Record != recordItem || e.Source.ID != post || !strings.HasPrefix(e.Target.Pointer, "/fields") {
			continue
		}
		s := e.Source
		switch {
		case s.Line == 0:
			t.Errorf("the record %q has no place", e.Message)
		case s.Meta == "":
			if s.Line != it.Line || s.Column != it.Column {
				t.Errorf("the record %q is at %d:%d, which is not the item at %d:%d", e.Message, s.Line, s.Column, it.Line, it.Column)
			}
		case !slices.ContainsFunc(it.Meta, func(m wxr.Meta) bool { return m.Key == s.Meta && m.Line == s.Line && m.Column == s.Column }):
			t.Errorf("the record %q is about the postmeta %q at %d:%d, which the item does not have", e.Message, s.Meta, s.Line, s.Column)
		}
	}
}

// fieldsLines lists the lines of the report about the custom fields that are not imported, which are about no item.
func fieldsLines(t *testing.T, imp *Import) []string {
	t.Helper()
	var out []string
	for _, l := range reportLines(t, imp.Report) {
		if strings.Contains(l, ": skipped: item (") && strings.Contains(l, "custom field") {
			out = append(out, l)
		}
	}
	return out
}

// fieldCase is an item of the type works with the postmeta meta, which has the fields want (as JSON) and the lines of the
// report that lines say. The report says that the custom fields with the keys unread are not imported, for all the cases.
type fieldCase struct {
	name   string
	meta   [][2]string
	want   string
	lines  []string
	unread []string
}

// run makes an item of each case, and checks them.
func runFieldCases(t *testing.T, s *Settings, cases []fieldCase) *fieldsRun {
	t.Helper()
	items := make([]wxrEntry, len(cases))
	for i, c := range cases {
		id := 100 + i
		items[i] = wxrEntry{id: id, typ: "works", title: "T", name: fmt.Sprintf("w%d", id), link: fmt.Sprintf("https://example.com/works/w%d/", id), meta: c.meta}
	}
	r := runFields(t, s, items...)
	counts := make(map[string]int)
	first := int64(0)
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) { r.check(t, int64(100+i), c.want, c.lines...) })
		for _, key := range c.unread {
			counts[key]++
			if first == 0 {
				first = int64(100 + i)
			}
		}
	}
	var want []string
	if len(counts) > 0 {
		keys := slices.Sorted(maps.Keys(counts))
		slices.SortStableFunc(keys, func(a, b string) int { return counts[b] - counts[a] })
		list := make([]string, len(keys))
		for i, key := range keys {
			list[i] = fmt.Sprintf("%q (%s)", key, count(counts[key], "item"))
		}
		want = append(want, groupLine(r.imp, first, "works", more("works", strings.Join(list, ", "))))
	}
	sameSet(t, "the lines about the custom fields that are not imported", fieldsLines(t, r.imp), want)
	return r
}

// phpList writes the PHP array of the serialized values vals, which have the keys 0, 1, ...
func phpList(vals ...string) string {
	var pairs []string
	for i, v := range vals {
		pairs = append(pairs, phpInt(i), v)
	}
	return phpArray(pairs...)
}

func phpFloat(f string) string { return "d:" + f + ";" }

func phpBool(b bool) string { return "b:" + string(rune('0'+btoi(b))) + ";" }

// phpLink is the PHP array that a link of ACF is.
func phpLink(url, title, target string) string {
	return phpArray(phpString("title"), phpString(title), phpString("url"), phpString(url), phpString("target"), phpString(target))
}

func phpStrings(vals ...string) string {
	out := make([]string, len(vals))
	for i, v := range vals {
		out[i] = phpString(v)
	}
	return phpList(out...)
}

func TestEntriesFieldValues(t *testing.T) {
	const r = "{R}#/fields/"
	kana := string(rune(0x304B)) + string(rune(0x3099))
	long := strings.Repeat("a", content.MaxTextBytes+1)
	notImported := func(field, key, why string) string {
		return fmt.Sprintf("skipped: the custom field %q is not imported: %s -> {R}#/fields/%s", key, why, field)
	}
	cases := []fieldCase{
		// text
		{name: "text", meta: metas("client", "Acme"), want: `{"client":"Acme"}`},
		{name: "text with line breaks and tabs", meta: metas("client", "Line one\nLine two\tend"), want: `{"client":"Line one Line two end"}`,
			lines: []string{`changed: line breaks and tabs in the custom field "client" are spaces here -> ` + r + "client"}},
		{name: "text with spaces at its ends", meta: metas("client", "  Acme "), want: `{"client":"Acme"}`},
		{name: "text of white space", meta: metas("client", " \t\n ")},
		{name: "text that is too long", meta: metas("client", long),
			lines: []string{notImported("client", "client", fmt.Sprintf("it is too long: %d bytes (max %d)", len(long), content.MaxTextBytes))}},
		{name: "text that is not normalized", meta: metas("client", kana), want: `{"client":"` + string(rune(0x304C)) + `"}`},
		{name: "text with the characters that JSON escapes", meta: metas("client", `<b>&"\ /`), want: `{"client":"<b>&\"\\ /"}`},
		{name: "text with a character outside the first plane", meta: metas("client", "a"+string(rune(0x1F600))+"b"), want: `{"client":"a` + string(rune(0x1F600)) + `b"}`},
		{name: "text that is serialized", meta: metas("client", phpString("Acme")), want: `{"client":"Acme"}`},
		{name: "text that is a serialized number", meta: metas("client", phpInt(42)), want: `{"client":"42"}`},
		{name: "text that is a serialized true", meta: metas("client", phpBool(true)), want: `{"client":"1"}`},
		{name: "text that is a serialized float of many digits", meta: metas("client", phpFloat("1.0E+25")), want: `{"client":"10000000000000000000000000"}`},
		{name: "text that is serialized with the line breaks counted as CRLF", meta: metas("client", "s:4:\"a\nb\";"), want: `{"client":"a b"}`,
			lines: []string{
				"changed: read the serialized value by counting the LF line breaks in 1 string as CRLF (XML turns CRLF into LF); the line breaks are kept as LF",
				`changed: line breaks and tabs in the custom field "client" are spaces here -> ` + r + "client",
			}},
		{name: "text that is too long and has a line break", meta: metas("client", strings.Repeat("a", content.MaxTextBytes)+"\nb"),
			lines: []string{notImported("client", "client", fmt.Sprintf("it is too long: %d bytes (max %d)", content.MaxTextBytes+2, content.MaxTextBytes))}},
		{name: "text that is a serialized float", meta: metas("client", phpFloat("2.5")), want: `{"client":"2.5"}`},
		{name: "text that is a serialized null", meta: metas("client", "N;")},
		{name: "text that is a string that looks like infinity", meta: metas("client", "d:INF;"), want: `{"client":"d:INF;"}`},
		{name: "text that is a list", meta: metas("client", phpStrings("a")),
			lines: []string{notImported("client", "client", "it is a list, not one value")}},
		{name: "text that is an empty list", meta: metas("client", "a:0:{}")},
		{name: "text that is serialized and cannot be read", meta: metas("client", `a:1:{i:0;s:3:"ab";}`),
			lines: []string{notImported("client", "client", `the value of "client": byte 17: expected '"', found ';'`)}},
		{name: "text of three postmeta", meta: metas("client", "first", "client", "second", "client", "third"), want: `{"client":"first"}`,
			lines: []string{`skipped: the custom field "client" has 3 values; the first is imported -> ` + r + "client"}},
		{name: "text of a postmeta that is empty and one that is not", meta: metas("client", " ", "client", "second"), want: `{"client":"second"}`},

		// textarea
		{name: "textarea", meta: metas("memo", "one\ntwo\n\nthree"), want: `{"memo":"one\ntwo\n\nthree"}`},
		{name: "textarea with a tab", meta: metas("memo", "a\tb"), want: `{"memo":"a\tb"}`},
		{name: "textarea with the characters that JSON escapes", meta: metas("memo", "<i>&\"\\\n"+string(rune(0x2028))+"end"),
			want: `{"memo":"<i>&\"\\\n` + string(rune(0x2028)) + `end"}`},

		// number
		{name: "number 12", meta: metas("year", "12"), want: `{"year":12}`},
		{name: "number -3.5", meta: metas("year", "-3.5"), want: `{"year":-3.5}`},
		{name: "number 1e3", meta: metas("year", "1e3"), want: `{"year":1000}`},
		{name: "number 2.50E-1", meta: metas("year", "2.50E-1"), want: `{"year":0.25}`},
		{name: "number with a plus sign", meta: metas("year", "+5"), want: `{"year":5}`},
		{name: "number with no digit before the point", meta: metas("year", ".5"), want: `{"year":0.5}`},
		{name: "number with no digit after the point", meta: metas("year", "5."), want: `{"year":5}`},
		{name: "number with spaces around it", meta: metas("year", " 7\n"), want: `{"year":7}`},
		{name: "number minus zero", meta: metas("year", "-0"), want: `{"year":0}`},
		{name: "number 1,000", meta: metas("year", "1,000"), lines: []string{notImported("year", "year", "it is not a number")}},
		{name: "number abc", meta: metas("year", "abc"), lines: []string{notImported("year", "year", "it is not a number")}},
		{name: "number in hexadecimal", meta: metas("year", "0x10"), lines: []string{notImported("year", "year", "it is not a number")}},
		{name: "number with an underscore", meta: metas("year", "1_000"), lines: []string{notImported("year", "year", "it is not a number")}},
		{name: "number inf", meta: metas("year", "inf"), lines: []string{notImported("year", "year", "it is not a number")}},
		{name: "number nan", meta: metas("year", "NaN"), lines: []string{notImported("year", "year", "it is not a number")}},
		{name: "number with a lone sign", meta: metas("year", "-"), lines: []string{notImported("year", "year", "it is not a number")}},
		{name: "number with a lone exponent", meta: metas("year", "1e"), lines: []string{notImported("year", "year", "it is not a number")}},
		{name: "number that is out of range", meta: metas("year", "1e999"), lines: []string{notImported("year", "year", "the number is out of range")}},
		{name: "number that is large", meta: metas("year", "1e21"), want: `{"year":1000000000000000000000}`},
		{name: "number that is an integer of thirty digits", meta: metas("year", "123456789012345678901234567890"), want: `{"year":123456789012345680000000000000}`},
		{name: "number that is small", meta: metas("year", "1e-7"), want: `{"year":0.0000001}`},
		{name: "number that is the smallest", meta: metas("year", "5e-324"), want: `{"year":0.` + strings.Repeat("0", 323) + `5}`},
		{name: "number that is the largest", meta: metas("year", "1.7976931348623157e308"), want: `{"year":17976931348623157` + strings.Repeat("0", 292) + `}`},
		{name: "number that is empty", meta: metas("year", "")},
		{name: "number that is a serialized integer", meta: metas("year", phpInt(2026)), want: `{"year":2026}`},
		{name: "number that is a serialized float", meta: metas("year", phpFloat("2.5")), want: `{"year":2.5}`},
		{name: "number that is a serialized string", meta: metas("year", phpString("40")), want: `{"year":40}`},

		// boolean
		{name: "boolean 1", meta: metas("onsale", "1"), want: `{"onsale":true}`},
		{name: "boolean true", meta: metas("onsale", "true"), want: `{"onsale":true}`},
		{name: "boolean yes", meta: metas("onsale", "yes"), want: `{"onsale":true}`},
		{name: "boolean on in capitals", meta: metas("onsale", " ON "), want: `{"onsale":true}`},
		{name: "boolean 0", meta: metas("onsale", "0")},
		{name: "boolean false", meta: metas("onsale", "False")},
		{name: "boolean no", meta: metas("onsale", "no")},
		{name: "boolean off", meta: metas("onsale", "off")},
		{name: "boolean that is serialized true", meta: metas("onsale", phpBool(true)), want: `{"onsale":true}`},
		{name: "boolean that is serialized false", meta: metas("onsale", phpBool(false))},
		{name: "boolean that is text", meta: metas("onsale", "maybe"), lines: []string{notImported("onsale", "onsale", "it is not a boolean")}},
		{name: "boolean 2", meta: metas("onsale", "2"), lines: []string{notImported("onsale", "onsale", "it is not a boolean")}},

		// date
		{name: "date as Ymd", meta: metas("since", "20261114"), want: `{"since":"2026-11-14"}`},
		{name: "date as Y-m-d", meta: metas("since", "2026-11-14"), want: `{"since":"2026-11-14"}`},
		{name: "date with spaces around it", meta: metas("since", " 20261114\n"), want: `{"since":"2026-11-14"}`},
		{name: "date as Y-m-d with spaces around it", meta: metas("since", " 2026-11-14 "), want: `{"since":"2026-11-14"}`},
		{name: "date with slashes", meta: metas("since", "2026/11/14"), lines: []string{notImported("since", "since", "it is not a date")}},
		{name: "date that is not a day", meta: metas("since", "2026-02-30"), lines: []string{notImported("since", "since", "it is not a date")}},
		{name: "date as a Ymd that is not a day", meta: metas("since", "20261340"), lines: []string{notImported("since", "since", "it is not a date")}},
		{name: "date as Ymd of seven digits", meta: metas("since", "2026114"), lines: []string{notImported("since", "since", "it is not a date")}},
		{name: "date with a time", meta: metas("since", "2026-11-14 10:00:00"), lines: []string{notImported("since", "since", "it is not a date")}},
		{name: "date that is serialized", meta: metas("since", phpInt(20261114)), want: `{"since":"2026-11-14"}`},

		// datetime
		{name: "datetime of the site", meta: metas("opens", "2026-11-14 10:00:00"), want: `{"opens":"2026-11-14T10:00:00+09:00"}`},
		{name: "datetime with spaces around it", meta: metas("opens", " 2026-11-14 10:00:00\n"), want: `{"opens":"2026-11-14T10:00:00+09:00"}`},
		{name: "datetime with an offset and spaces around it", meta: metas("opens", " 2026-11-14T10:00:00+09:00 "), want: `{"opens":"2026-11-14T10:00:00+09:00"}`},
		{name: "datetime with an offset", meta: metas("opens", "2026-11-14T10:00:00+09:00"), want: `{"opens":"2026-11-14T10:00:00+09:00"}`},
		{name: "datetime with another offset", meta: metas("opens", "2026-11-14T10:00:00-05:30"), want: `{"opens":"2026-11-14T10:00:00-05:30"}`},
		{name: "datetime in UTC", meta: metas("opens", "2026-11-14T01:00:00Z"), want: `{"opens":"2026-11-14T01:00:00Z"}`},
		{name: "datetime with a fraction", meta: metas("opens", "2026-11-14T10:00:00.5+09:00"), lines: []string{notImported("opens", "opens", "it is not a date")}},
		{name: "datetime with an offset that is not one", meta: metas("opens", "2026-11-14T10:00:00+24:00"), lines: []string{notImported("opens", "opens", "it is not a date")}},
		{name: "datetime with no offset", meta: metas("opens", "2026-11-14T10:00:00"), lines: []string{notImported("opens", "opens", "it is not a date")}},
		{name: "datetime with no seconds", meta: metas("opens", "2026-11-14 10:00"), lines: []string{notImported("opens", "opens", "it is not a date")}},
		{name: "datetime that is a date", meta: metas("opens", "20261114"), lines: []string{notImported("opens", "opens", "it is not a date")}},
		{name: "datetime that is not a day", meta: metas("opens", "2026-02-30 10:00:00"), lines: []string{notImported("opens", "opens", "it is not a date")}},
		{name: "datetime that is not a day, with an offset", meta: metas("opens", "2026-02-30T10:00:00+09:00"), lines: []string{notImported("opens", "opens", "it is not a date")}},
		{name: "datetime with a lower case t", meta: metas("opens", "2026-11-14t10:00:00z"), lines: []string{notImported("opens", "opens", "it is not a date")}},
		{name: "datetime that is text", meta: metas("opens", "tomorrow"), lines: []string{notImported("opens", "opens", "it is not a date")}},

		// select
		{name: "select", meta: metas("size", "M"), want: `{"size":"M"}`},
		{name: "select with spaces around it", meta: metas("size", " L "), want: `{"size":"L"}`},
		{name: "select that is not an option", meta: metas("size", "XL"),
			lines: []string{`skipped: the custom field "size" is not imported: "XL" is not an option -> ` + r + "size"}},
		{name: "select of many values that is one", meta: metas("colors", "green"), want: `{"colors":["green"]}`},
		{name: "select of many values that is a list", meta: metas("colors", phpStrings("red", "blue")), want: `{"colors":["red","blue"]}`},
		{name: "select of many values that has one twice", meta: metas("colors", phpStrings("red", "red", "green")), want: `{"colors":["red","green"]}`},
		{name: "select of many values of two postmeta", meta: metas("colors", "red", "colors", "green"), want: `{"colors":["red","green"]}`},
		{name: "select of many values that has an empty one", meta: metas("colors", phpStrings("red", "", "blue")), want: `{"colors":["red","blue"]}`},
		{name: "select of many values that is an empty list", meta: metas("colors", "a:0:{}")},
		// What is not an option takes the whole field with it: the validation takes out the field, not the value.
		{name: "select of many values with one that is not an option", meta: metas("colors", phpStrings("red", "cyan")),
			lines: []string{`skipped: the custom field "colors" is not imported: "cyan" is not an option -> ` + r + "colors"}},
		{name: "select of many values that is a list of lists", meta: metas("colors", phpList(phpStrings("red"))),
			lines: []string{notImported("colors", "colors", "it is a list, not one value")}},

		// media
		{name: "media", meta: metas("cover", "20"), want: `{"cover":"{m20}"}`},
		{name: "media that is a serialized integer", meta: metas("cover", phpInt(24)), want: `{"cover":"{m24}"}`},
		{name: "media with spaces around it", meta: metas("cover", " 20 "), want: `{"cover":"{m20}"}`},
		{name: "media of any kind that is an image", meta: metas("photo", "20"), want: `{"photo":"{m20}"}`},
		{name: "media of any kind that is a PDF", meta: metas("photo", "21"), want: `{"photo":"{m21}"}`},
		{name: "media of a PDF", meta: metas("manual", "21"), want: `{"manual":"{m21}"}`},
		{name: "media that is a PDF where images are accepted", meta: metas("cover", "21"),
			lines: []string{notImported("cover", "cover", "attachment 21 is pdf, which the field does not accept (accept: image)")}},
		{name: "media that is an image where PDFs are accepted", meta: metas("manual", "20"),
			lines: []string{notImported("manual", "manual", "attachment 20 is image, which the field does not accept (accept: pdf)")}},
		{name: "media that is in the trash", meta: metas("cover", "22"), lines: []string{notImported("cover", "cover", "attachment 22 is not imported")}},
		{name: "media that is not in the WXR", meta: metas("cover", "99"), lines: []string{notImported("cover", "cover", "attachment 99 is not imported")}},
		{name: "media that is a post", meta: metas("cover", "1"), lines: []string{notImported("cover", "cover", "post 1 is not an attachment")}},
		{name: "media that is text", meta: metas("cover", "abc"), lines: []string{notImported("cover", "cover", "it is not the ID of an attachment")}},
		{name: "media 0", meta: metas("cover", "0"), lines: []string{notImported("cover", "cover", "it is not the ID of an attachment")}},
		{name: "media -5", meta: metas("cover", "-5"), lines: []string{notImported("cover", "cover", "it is not the ID of an attachment")}},
		{name: "media that is too large", meta: metas("cover", "99999999999999999999"), lines: []string{notImported("cover", "cover", "it is not the ID of an attachment")}},

		// entry
		{name: "entry that is a post", meta: metas("maker", "1"), want: `{"maker":"{e1}"}`},
		{name: "entry of any type that is a page", meta: metas("partner", "3"), want: `{"partner":"{e3}"}`},
		{name: "entry of any type that is of the type works", meta: metas("partner", "5"), want: `{"partner":"{e5}"}`},
		{name: "entry that is a page where posts are taken", meta: metas("maker", "3"),
			lines: []string{notImported("maker", "maker", "item 3 is of the type page, which the field does not take (use post)")}},
		{name: "entry that is in the trash", meta: metas("maker", "4"), lines: []string{notImported("maker", "maker", "item 4 is not imported")}},
		{name: "entry that is not in the WXR", meta: metas("maker", "99"), lines: []string{notImported("maker", "maker", "item 99 is not imported")}},
		{name: "entry that has no date", meta: metas("maker", "6"), lines: []string{notImported("maker", "maker", "item 6 is not imported")}},
		{name: "entry that is an attachment", meta: metas("maker", "20"), lines: []string{notImported("maker", "maker", "post 20 is not an item")}},
		{name: "entry that is text", meta: metas("maker", "one"), lines: []string{notImported("maker", "maker", "it is not the ID of an item")}},
		{name: "entry that is a list", meta: metas("maker", phpList(phpInt(1))), lines: []string{notImported("maker", "maker", "it is a list, not one value")}},
		{name: "entries of many values in a list", meta: metas("related", phpList(phpInt(1), phpInt(3))), want: `{"related":["{e1}","{e3}"]}`},
		{name: "entries of many values with strings and integers", meta: metas("related", phpList(phpString("1"), phpInt(3), "N;")), want: `{"related":["{e1}","{e3}"]}`},
		{name: "entries of many values that has one twice", meta: metas("related", phpList(phpInt(1), phpInt(1), phpInt(3))), want: `{"related":["{e1}","{e3}"]}`},
		{name: "entries of many values of one postmeta that is not a list", meta: metas("related", "5"), want: `{"related":["{e5}"]}`},
		{name: "entries of many values of three postmeta", meta: metas("related", "1", "related", "3", "related", "5"), want: `{"related":["{e1}","{e3}","{e5}"]}`},
		{name: "entries of many values that has one that is in the trash", meta: metas("related", phpList(phpInt(1), phpInt(3), phpInt(4))), want: `{"related":["{e1}","{e3}"]}`,
			lines: []string{`skipped: 1 of the 3 values of the custom field "related" is not imported: value 3: item 4 is not imported -> ` + r + "related"}},
		{name: "entries of many values of which none are imported", meta: metas("related", phpList(phpInt(4), phpString("x"))),
			lines: []string{`skipped: 2 of the 2 values of the custom field "related" are not imported: value 1: item 4 is not imported; value 2: it is not the ID of an item -> ` + r + "related"}},
		{name: "entries of many values of one that is not imported", meta: metas("related", "4"),
			lines: []string{notImported("related", "related", "item 4 is not imported")}},

		// term
		{name: "term of a category", meta: metas("topic", "2"), want: `{"topic":"{t:news}"}`},
		{name: "term of a genre", meta: metas("style", "7"), want: `{"style":"{t:comedy}"}`},
		{name: "term that is a tag where categories are taken", meta: metas("topic", "4"), lines: []string{notImported("topic", "topic", "term 4 is not in the WXR")}},
		{name: "term that is not in the WXR", meta: metas("topic", "99"), lines: []string{notImported("topic", "topic", "term 99 is not in the WXR")}},
		{name: "term that is text", meta: metas("topic", "news"), lines: []string{notImported("topic", "topic", "it is not the ID of a term")}},
		{name: "terms of tags", meta: metas("keywords", phpList(phpInt(5), phpInt(4))), want: `{"keywords":["{t:rust}","{t:go}"]}`},
		{name: "terms of a tag that is one", meta: metas("keywords", "4"), want: `{"keywords":["{t:go}"]}`},
		{name: "terms of tags that has one of another taxonomy", meta: metas("keywords", phpList(phpInt(4), phpInt(2))), want: `{"keywords":["{t:go}"]}`,
			lines: []string{`skipped: 1 of the 2 values of the custom field "keywords" is not imported: value 2: term 2 is not in the WXR -> ` + r + "keywords"}},

		// link
		{name: "link of a list", meta: metas("website", phpLink("https://example.org/a", "", "")), want: `{"website":{"href":"https://example.org/a"}}`},
		{name: "link of a list with a target", meta: metas("website", phpLink("https://example.org/a", "", "_blank")),
			want: `{"website":{"href":"https://example.org/a","newTab":true}}`},
		{name: "link of a list with a title", meta: metas("website", phpLink("https://example.org/a", "Docs", "_self")), want: `{"website":{"href":"https://example.org/a"}}`,
			lines: []string{`skipped: the title "Docs" of the link of the custom field "website" is not imported -> ` + r + "website"}},
		{name: "link of a list with a title that is long", meta: metas("website", phpLink("https://example.org/a", strings.Repeat("t", 300), "")), want: `{"website":{"href":"https://example.org/a"}}`,
			lines: []string{`skipped: the title "` + strings.Repeat("t", maxQuoted) + `..." of the link of the custom field "website" is not imported -> ` + r + "website"}},
		{name: "link of a list with a target of capital letters", meta: metas("website", phpLink("https://example.org/a", "", "_BLANK")),
			want: `{"website":{"href":"https://example.org/a","newTab":true}}`},
		{name: "link of a list with a target and spaces around it", meta: metas("website", phpLink("https://example.org/a", "", " _blank ")),
			want: `{"website":{"href":"https://example.org/a","newTab":true}}`},
		{name: "link of a list with a title of white space", meta: metas("website", phpLink("https://example.org/a", " \t", "")), want: `{"website":{"href":"https://example.org/a"}}`},
		{name: "link of an address", meta: metas("website", "https://example.org/b"), want: `{"website":{"href":"https://example.org/b"}}`},
		{name: "link to a post of the old site", meta: metas("website", "https://example.com/post-1/"), want: `{"website":{"entry":"{e1}"}}`},
		{name: "link to a post of the old site with an anchor", meta: metas("website", "https://example.com/post-1/#part"), want: `{"website":{"entry":"{e1}","anchor":"part"}}`},
		{name: "link to a page that is a path from the root", meta: metas("website", "/about/"), want: `{"website":{"entry":"{e3}"}}`},
		{name: "link to a file of the old site", meta: metas("website", "https://example.com/wp-content/uploads/2026/10/b.pdf"), want: `{"website":{"media":"{m21}"}}`},
		{name: "link to a page of the old site that is not imported", meta: metas("website", "https://example.com/post-4/"), want: `{"website":{"href":"/post-4/"}}`},
		{name: "link to a page of the old site with a query and a fragment", meta: metas("website", "https://example.com/nowhere/?x=1#top"),
			want: `{"website":{"href":"/nowhere/?x=1#top"}}`},
		{name: "link to a post of the old site with a fragment that is not a name", meta: metas("website", "https://example.com/post-1/#sec.1"), want: `{"website":{"entry":"{e1}"}}`},
		{name: "link of an address that is long and not accepted", meta: metas("website", "javascript:"+strings.Repeat("a", 200)),
			lines: []string{notImported("website", "website", `the link to "javascript:`+strings.Repeat("a", 89)+`..." is not accepted: scheme "javascript" is not allowed`)}},
		{name: "link to an anchor", meta: metas("website", "#part"), want: `{"website":{"anchor":"part"}}`},
		{name: "link to an anchor that is not a name", meta: metas("website", "#a b"),
			lines: []string{notImported("website", "website", `the link to "#a b" is not accepted: the anchor is not a name that whiteroom accepts`)}},
		{name: "link of an email address", meta: metas("website", "mailto:a@example.org"), want: `{"website":{"href":"mailto:a@example.org"}}`},
		{name: "link of a script", meta: metas("website", "javascript:alert(1)"),
			lines: []string{notImported("website", "website", `the link to "javascript:alert(1)" is not accepted: scheme "javascript" is not allowed`)}},
		{name: "link of a script with a tab in it", meta: metas("website", "java\tscript:alert(1)"),
			lines: []string{notImported("website", "website", `the link to "javascript:alert(1)" is not accepted: scheme "javascript" is not allowed`)}},
		{name: "link of a path that is not from the root", meta: metas("website", "foo/bar"),
			lines: []string{notImported("website", "website", `the link to "foo/bar" is not accepted: must be an absolute URL or a path starting with "/"`)}},
		{name: "link of a list with no address", meta: metas("website", phpLink("", "Docs", ""))},
		{name: "link that is a list of lists", meta: metas("website", phpList(phpLink("https://example.org/a", "", ""))),
			lines: []string{notImported("website", "website", `it is not a link: the list has no "url"`)}},
	}
	runFieldCases(t, nil, cases)
}

func TestEntriesFieldLists(t *testing.T) {
	const r = "{R}#/fields/"
	notImported := func(field, key, why string) string {
		return fmt.Sprintf("skipped: the custom field %q is not imported: %s -> {R}#/fields/%s", key, why, field)
	}
	var bad, badLines []string
	for i := range 12 {
		bad = append(bad, phpInt(90+i))
		badLines = append(badLines, fmt.Sprintf("value %d: attachment %d is not imported", i+1, 90+i))
	}
	cases := []fieldCase{
		// A list of media is the gallery of ACF: a PHP array of IDs.
		{name: "gallery of a PHP array", meta: metas("gallery", phpList(phpInt(20), phpInt(24))), want: `{"gallery":["{m20}","{m24}"]}`},
		{name: "gallery of IDs that are strings", meta: metas("gallery", phpStrings("24", "20")), want: `{"gallery":["{m24}","{m20}"]}`},
		{name: "gallery that has an image twice", meta: metas("gallery", phpList(phpInt(20), phpInt(20))), want: `{"gallery":["{m20}","{m20}"]}`},
		{name: "gallery of three postmeta", meta: metas("gallery", "20", "gallery", "24", "gallery", "20"), want: `{"gallery":["{m20}","{m24}","{m20}"]}`},
		{name: "gallery of one ID", meta: metas("gallery", "24"), want: `{"gallery":["{m24}"]}`},
		{name: "gallery that is an empty list", meta: metas("gallery", "a:0:{}")},
		{name: "gallery that has values of white space", meta: metas("gallery", phpList(phpInt(20), phpString(" "), phpString(""), "N;", "b:0;")), want: `{"gallery":["{m20}"]}`},
		{name: "gallery that has a medium that is not imported", meta: metas("gallery", phpList(phpInt(20), phpInt(22), phpInt(24))),
			want:  `{"gallery":["{m20}","{m24}"]}`,
			lines: []string{`skipped: 1 of the 3 values of the custom field "gallery" is not imported: value 2: attachment 22 is not imported -> ` + r + "gallery"}},
		{name: "gallery that has a PDF", meta: metas("gallery", phpList(phpInt(21), phpInt(20))), want: `{"gallery":["{m20}"]}`,
			lines: []string{`skipped: 1 of the 2 values of the custom field "gallery" is not imported: value 1: attachment 21 is pdf, which the field does not accept (accept: image) -> ` + r + "gallery"}},
		{name: "gallery of which twelve are not imported", meta: metas("gallery", phpList(bad...)),
			lines: []string{`skipped: 12 of the 12 values of the custom field "gallery" are not imported: ` + strings.Join(badLines[:10], "; ") + `; and 2 more -> ` + r + "gallery"}},
		{name: "gallery of one that is not imported", meta: metas("gallery", phpList(phpInt(99))),
			lines: []string{notImported("gallery", "gallery", "attachment 99 is not imported")}},
		{name: "gallery that has too many", meta: metas("gallery", phpList(phpInt(20), phpInt(20), phpInt(20), phpInt(20), phpInt(20), phpInt(20))),
			lines: []string{notImported("gallery", "gallery", "too many items: 6 (max 5)")}},
		{name: "gallery that cannot be read", meta: metas("gallery", `a:2:{i:0;i:20;}`),
			lines: []string{notImported("gallery", "gallery", `the value of "gallery": byte 14: array has fewer elements than declared`)}},
		{name: "gallery of postmeta of which one cannot be read", meta: metas("gallery", "20", "gallery", `a:2:{i:0;i:20;}`, "gallery", "24"),
			want:  `{"gallery":["{m20}","{m24}"]}`,
			lines: []string{notImported("gallery", "gallery", `the value of "gallery": byte 14: array has fewer elements than declared`)}},

		// A list of text is what a custom field with the same key more than once is.
		{name: "list of three postmeta", meta: metas("urls", "a", "urls", "b", "urls", "c"), want: `{"urls":["a","b","c"]}`},
		{name: "list of a PHP array", meta: metas("urls", phpStrings("a", "b")), want: `{"urls":["a","b"]}`},
		{name: "list of one value", meta: metas("urls", "a"), want: `{"urls":["a"]}`},
		{name: "list that has an empty value", meta: metas("urls", phpStrings("a", " ", "b")), want: `{"urls":["a","b"]}`},
		{name: "list that has a value twice", meta: metas("urls", phpStrings("a", "a")), want: `{"urls":["a","a"]}`},
		{name: "list that has a list", meta: metas("urls", phpList(phpStrings("a"))),
			lines: []string{notImported("urls", "urls", "it is a list, not one value")}},
		{name: "list of two postmeta of which one is a list", meta: metas("urls", "a", "urls", phpStrings("b")), want: `{"urls":["a"]}`,
			lines: []string{`skipped: 1 of the 2 values of the custom field "urls" is not imported: value 2: it is a list, not one value -> ` + r + "urls"}},
		{name: "list of text that changes a line break", meta: metas("urls", phpStrings("a\nb", "c")), want: `{"urls":["a b","c"]}`,
			lines: []string{`changed: line breaks and tabs in the custom field "urls" are spaces here -> ` + r + "urls"}},
		{name: "list of text that changes line breaks in many values, which the report says once", meta: metas("urls", "a\nb", "urls", "c\td", "urls", "e\nf"),
			want:  `{"urls":["a b","c d","e f"]}`,
			lines: []string{`changed: line breaks and tabs in the custom field "urls" are spaces here -> ` + r + "urls"}},

		{name: "list of booleans that has no", meta: metas("flags", phpStrings("yes", "no", "off", "1")), want: `{"flags":[true,true]}`},
		{name: "list of booleans", meta: metas("flags", phpList(phpBool(true), phpBool(false), phpString("yes"), phpString("maybe"))), want: `{"flags":[true,true]}`,
			lines: []string{`skipped: 1 of the 3 values of the custom field "flags" is not imported: value 3: it is not a boolean -> ` + r + "flags"}},
		{name: "two fields of many values", meta: metas("gallery", phpList(phpInt(20), phpInt(22), phpInt(24)), "related", phpList(phpInt(1), phpInt(4))),
			want: `{"gallery":["{m20}","{m24}"],"related":["{e1}"]}`,
			lines: []string{
				`skipped: 1 of the 3 values of the custom field "gallery" is not imported: value 2: attachment 22 is not imported -> ` + r + "gallery",
				`skipped: 1 of the 2 values of the custom field "related" is not imported: value 2: item 4 is not imported -> ` + r + "related",
			}},
		{name: "list with empty values that are not counted", meta: metas("gallery", phpList(phpString(""), phpInt(22), "N;", phpInt(20))), want: `{"gallery":["{m20}"]}`,
			lines: []string{`skipped: 1 of the 2 values of the custom field "gallery" is not imported: value 1: attachment 22 is not imported -> ` + r + "gallery"}},

		// A list of links is a list of lists, as a link is.
		{name: "list of links", meta: metas("links", phpList(phpLink("https://example.org/a", "", ""), phpLink("https://example.org/b", "", "_blank"))),
			want: `{"links":[{"href":"https://example.org/a"},{"href":"https://example.org/b","newTab":true}]}`},
		{name: "list of one link that is a list of the link", meta: metas("links", phpLink("https://example.org/a", "Docs", "_blank")),
			want:  `{"links":[{"href":"https://example.org/a","newTab":true}]}`,
			lines: []string{`skipped: the title "Docs" of the link of the custom field "links" is not imported -> ` + r + "links"}},
		{name: "list of links and one that is not", meta: metas("links", phpList(phpLink("https://example.org/a", "", ""), phpString("javascript:x"))),
			want:  `{"links":[{"href":"https://example.org/a"}]}`,
			lines: []string{`skipped: 1 of the 2 values of the custom field "links" is not imported: value 2: the link to "javascript:x" is not accepted: scheme "javascript" is not allowed -> ` + r + "links"}},
		{name: "list of links that has what is not one", meta: metas("links", phpList(phpLink("https://example.org/a", "", ""), "d:INF;")),
			want:  `{"links":[{"href":"https://example.org/a"}]}`,
			lines: []string{`skipped: 1 of the 2 values of the custom field "links" is not imported: value 2: it is not a link -> ` + r + "links"}},
		{name: "list of links of addresses", meta: metas("links", "https://example.org/a", "links", "https://example.com/post-1/"),
			want: `{"links":[{"href":"https://example.org/a"},{"entry":"{e1}"}]}`},

		// A list of lists.
		{name: "list of lists of numbers", meta: metas("matrix", phpList(phpList(phpInt(1), phpInt(2)), phpList(phpInt(3)))), want: `{"matrix":[[1,2],[3]]}`},
		{name: "list of lists that has a value that is not a number", meta: metas("matrix", phpList(phpStrings("x", "2"))), want: `{"matrix":[[2]]}`,
			lines: []string{`skipped: 1 of the 2 values of the custom field "matrix" is not imported: value 1: it is not a number -> ` + r + "matrix"}},
		{name: "list of texts that has infinity", meta: metas("urls", phpList("d:INF;", phpString("a"))), want: `{"urls":["a"]}`,
			lines: []string{`skipped: 1 of the 2 values of the custom field "urls" is not imported: value 1: it is not a finite number -> ` + r + "urls"}},
		{name: "list of numbers that has what is not a number", meta: metas("matrix", phpList(phpList("d:NAN;", phpInt(1)))), want: `{"matrix":[[1]]}`,
			lines: []string{`skipped: 1 of the 2 values of the custom field "matrix" is not imported: value 1: it is not a finite number -> ` + r + "matrix"}},
		{name: "list of lists of which one is empty", meta: metas("matrix", phpList(phpList(phpInt(1)), "a:0:{}")), want: `{"matrix":[[1]]}`},
	}
	runFieldCases(t, nil, cases)
}

func TestEntriesFieldGroups(t *testing.T) {
	const r = "{R}#/fields/"
	notImported := func(field, key, why string) string {
		return fmt.Sprintf("skipped: the custom field %q is not imported: %s -> {R}#/fields/%s", key, why, field)
	}
	cases := []fieldCase{
		// A group has the keys <key>_<name> of its fields, also in a group.
		{name: "group", meta: metas("address_city", "Tokyo", "address_zip", "100-0001", "address_geo_lat", "35.68", "address_geo_lng", "139.76"),
			want: `{"address":{"city":"Tokyo","geo":{"lat":35.68,"lng":139.76},"zip":"100-0001"}}`},
		{name: "group of one field", meta: metas("address_city", "Tokyo"), want: `{"address":{"city":"Tokyo"}}`},
		{name: "group of a field in a group", meta: metas("address_geo_lng", "139.76"), want: `{"address":{"geo":{"lng":139.76}}}`},
		{name: "group of no value", meta: metas("address_city", "  ", "address_zip", "")},
		{name: "group that has a field that is wrong", meta: metas("address_geo_lat", "north", "address_geo_lng", "139.76", "address_city", "Tokyo"),
			want:  `{"address":{"city":"Tokyo","geo":{"lng":139.76}}}`,
			lines: []string{notImported("address", "address_geo_lat", "it is not a number")}},
		{name: "group that has no field that is right", meta: metas("address_geo_lat", "north"),
			lines: []string{notImported("address", "address_geo_lat", "it is not a number")}},
		{name: "group of fields that have two values", meta: metas("address_city", "Tokyo", "address_city", "Osaka"), want: `{"address":{"city":"Tokyo"}}`,
			lines: []string{`skipped: the custom field "address_city" has 2 values; the first is imported -> ` + r + "address"}},

		// The rows of a list of groups, which ACF calls a repeater, have the keys <key>_<number>_<name>.
		{name: "rows", meta: metas("items", "2", "items_0_title", "A", "items_0_image", "20", "items_1_title", "B", "items_1_amount", "3",
			"items_1_sizes", phpStrings("S", "M")),
			want: `{"items":[{"image":"{m20}","title":"A"},{"amount":3,"sizes":["S","M"],"title":"B"}]}`},
		{name: "rows of numbers that are not next to each other", meta: metas("items_3_title", "D", "items_0_title", "A", "items", "1000000"),
			want: `{"items":[{"title":"A"},{"title":"D"}]}`},
		{name: "rows in the order of the numbers", meta: metas("items", "11", "items_10_title", "ten", "items_9_title", "nine", "items_2_title", "two"),
			want: `{"items":[{"title":"two"},{"title":"nine"},{"title":"ten"}]}`},
		{name: "rows with a number of the parent that is wrong", meta: metas("items", "1", "items_0_title", "A", "items_1_title", "B", "items_2_title", "C"),
			want: `{"items":[{"title":"A"},{"title":"B"},{"title":"C"}]}`},
		{name: "rows with no number of the parent", meta: metas("items_0_title", "A"), want: `{"items":[{"title":"A"}]}`},
		{name: "rows of which one has no value", meta: metas("items", "3", "items_0_title", "A", "items_1_title", "  ", "items_2_title", "C"),
			want: `{"items":[{"title":"A"},{"title":"C"}]}`},
		{name: "rows of which one has only a value that is wrong", meta: metas("items", "3", "items_0_title", "A", "items_2_amount", "oops"),
			want:  `{"items":[{"title":"A"}]}`,
			lines: []string{notImported("items", "items_2_amount", "it is not a number")}},
		{name: "rows with no value", meta: metas("items", "2", "items_0_title", "", "items_1_image", " ")},
		{name: "rows with a number of the parent only", meta: metas("items", "5")},
		{name: "rows with fields that the schema file does not have", meta: metas("items", "2", "items_0_title", "A", "items_0_note", "n", "items_1_note", "m", "items_1_other", "o", "items_1_blank", " "),
			want:  `{"items":[{"title":"A"}]}`,
			lines: []string{`skipped: the sub fields "note", "other" of the custom field "items" are not imported; the field has no such fields in the schema file -> ` + r + "items"}},
		{name: "rows with twelve fields that the schema file does not have, of which ten are named", meta: metas(
			"items_0_u01", "x", "items_0_u02", "x", "items_0_u03", "x", "items_0_u04", "x", "items_0_u05", "x", "items_0_u06", "x",
			"items_0_u07", "x", "items_0_u08", "x", "items_0_u09", "x", "items_0_u10", "x", "items_0_u11", "x", "items_0_u12", "x"),
			lines: []string{`skipped: the sub fields "u01", "u02", "u03", "u04", "u05", "u06", "u07", "u08", "u09", "u10", and 2 more of the custom field "items" are not imported; the field has no such fields in the schema file -> ` + r + "items"}},
		{name: "rows of two lists with a field that the schema file does not have each", meta: metas("items_0_title", "A", "items_0_note", "n", "steps_0_name", "S", "steps_0_note", "m"),
			want: `{"items":[{"title":"A"}],"steps":[{"name":"S"}]}`,
			lines: []string{
				`skipped: the sub field "note" of the custom field "items" is not imported; the field has no such field in the schema file -> ` + r + "items",
				`skipped: the sub field "note" of the custom field "steps" is not imported; the field has no such field in the schema file -> ` + r + "steps",
			}},
		{name: "rows with a field that the schema file does not have", meta: metas("items", "1", "items_0_title", "A", "items_0_note", "n"),
			want:  `{"items":[{"title":"A"}]}`,
			lines: []string{`skipped: the sub field "note" of the custom field "items" is not imported; the field has no such field in the schema file -> ` + r + "items"}},
		{name: "rows with only a field that the schema file does not have", meta: metas("items", "1", "items_0_note", "n"),
			lines: []string{`skipped: the sub field "note" of the custom field "items" is not imported; the field has no such field in the schema file -> ` + r + "items"}},
		{name: "rows that change line breaks, which the report says once", meta: metas("items", "2", "items_0_title", "a\nb", "items_1_title", "c\nd", "address_city", "e\nf"),
			want: `{"address":{"city":"e f"},"items":[{"title":"a b"},{"title":"c d"}]}`,
			lines: []string{
				`changed: line breaks and tabs in the custom field "items" are spaces here -> ` + r + "items",
				`changed: line breaks and tabs in the custom field "address" are spaces here -> ` + r + "address",
			}},
		{name: "rows in rows", meta: metas("items", "1", "items_0_title", "A", "items_0_parts", "2", "items_0_parts_0_name", "x", "items_0_parts_1_name", "y"),
			want: `{"items":[{"parts":[{"name":"x"},{"name":"y"}],"title":"A"}]}`},
		{name: "rows in rows that are in the order of their numbers", meta: metas("items_1_parts_1_name", "d", "items_1_parts_0_name", "c", "items_0_parts_1_name", "b", "items_0_parts_0_name", "a"),
			want: `{"items":[{"parts":[{"name":"a"},{"name":"b"}]},{"parts":[{"name":"c"},{"name":"d"}]}]}`},
		{name: "rows in rows with a field that the schema file does not have", meta: metas("items_0_parts_0_name", "x", "items_0_parts_0_size", "L"),
			want:  `{"items":[{"parts":[{"name":"x"}]}]}`,
			lines: []string{`skipped: the sub field "size" of the custom field "items" is not imported; the field has no such field in the schema file -> ` + r + "items"}},
		{name: "rows in rows with a field that the schema file does not have, in each row", meta: metas("items_0_parts_0_name", "x", "items_0_parts_0_size", "L", "items_1_parts_0_name", "y", "items_1_parts_0_size", "M"),
			want:  `{"items":[{"parts":[{"name":"x"}]},{"parts":[{"name":"y"}]}]}`,
			lines: []string{`skipped: the sub field "size" of the custom field "items" is not imported; the field has no such field in the schema file -> ` + r + "items"}},
		{name: "rows with a number that is not one that rows have", meta: metas("items_00_title", "x", "items_0_title", "A", "items_x_title", "y", "items_-1_title", "z"),
			want: `{"items":[{"title":"A"}]}`, unread: []string{"items_00_title", "items_x_title", "items_-1_title"}},
		{name: "rows of which a field is not an option", meta: metas("items", "1", "items_0_title", "A", "items_0_sizes", phpStrings("S", "XL")),
			lines: []string{`skipped: the custom field "items" is not imported: "XL" is not an option -> ` + r + "items"}},
		{name: "rows of which a field is a list that is wrong", meta: metas("items", "1", "items_0_title", "A", "items_0_sizes", `a:2:{i:0;s:1:"S";}`),
			want:  `{"items":[{"title":"A"}]}`,
			lines: []string{notImported("items", "items_0_sizes", `the value of "items_0_sizes": byte 17: array has fewer elements than declared`)}},
	}
	runFieldCases(t, nil, cases)
}

// fieldsItem is a published item of the type typ ("" for post) with the postmeta kv.
func fieldsItem(id int, typ string, kv ...string) wxrEntry {
	return wxrEntry{id: id, typ: typ, title: "T", name: fmt.Sprintf("i%d", id), link: fmt.Sprintf("https://example.com/i%d/", id), meta: metas(kv...)}
}

// more is the message about the custom fields of the type typ that are not imported, whose list is list.
func more(typ, list string) string {
	return fmt.Sprintf("the custom fields %[2]s of the post type %[1]s are not imported; fields.%[1]s of the settings maps them to the fields of the schema file", typ, list)
}

func notImportedMessage(typ string, items string) string {
	return fmt.Sprintf("the custom fields %[1]s of the post type %[2]s are not imported; fields.%[2]s of the settings maps them to the fields of the schema file", items, typ)
}

func TestEntriesFieldTable(t *testing.T) {
	for _, tt := range []struct {
		name   string
		table  map[string]map[string]string
		plugin string
		typ    string // "works" if empty
		meta   [][2]string
		want   string
		unread string // the keys that the report says are not imported, as the list in its message
	}{
		{name: "no table", meta: metas("client", "A", "memo", "B"), want: `{"client":"A","memo":"B"}`},
		{name: "a key for a field", table: map[string]map[string]string{"works": {"client_name": "client"}},
			meta: metas("client_name", "A"), want: `{"client":"A"}`},
		{name: "a key for a field that is read of the name too", table: map[string]map[string]string{"works": {"client_name": "client"}},
			meta: metas("client", "A", "client_name", "B"), want: `{"client":"B"}`, unread: `"client" (1 item)`},
		{name: "the key of the name that the table has for none", table: map[string]map[string]string{"works": {"client": ""}},
			meta: metas("client", "A", "memo", "B"), want: `{"memo":"B"}`, unread: `"client" (1 item)`},
		{name: "the key of the name that the table has for another field", table: map[string]map[string]string{"works": {"client": "memo"}},
			meta: metas("client", "A"), want: `{"memo":"A"}`},
		{name: "the key of the name that the table has for another field, and the key of the other", table: map[string]map[string]string{"works": {"client": "memo"}},
			meta: metas("client", "A", "memo", "B"), want: `{"memo":"A"}`, unread: `"memo" (1 item)`},
		{name: "a key that starts with an underscore", table: map[string]map[string]string{"works": {"_old_client": "client"}},
			meta: metas("_old_client", "A"), want: `{"client":"A"}`},
		{name: "a key that starts with an underscore that is left out", table: map[string]map[string]string{"works": {"_old_client": ""}},
			meta: metas("_old_client", "A"), unread: `"_old_client" (1 item)`},
		{name: "a key of Rank Math", table: map[string]map[string]string{"works": {"rank_math_note": "memo"}},
			meta: metas("rank_math_note", "A"), want: `{"memo":"A"}`},
		{name: "a key of the plugin that the table gives", table: map[string]map[string]string{"works": {"redirect": "redirect"}}, plugin: "tsf",
			meta: metas("redirect", "https://example.org/"), want: `{"redirect":"https://example.org/"}`},
		{name: "a key of the plugin that the table does not give", plugin: "tsf", meta: metas("redirect", "https://example.org/")},
		{name: "a key of another plugin", plugin: "yoast", meta: metas("redirect", "https://example.org/"), want: `{"redirect":"https://example.org/"}`},
		{name: "a key of no plugin", meta: metas("redirect", "https://example.org/"), want: `{"redirect":"https://example.org/"}`},
		{name: "a key of a plugin whose name is that of a field", plugin: "tsf", meta: metas("exclude_local_search", "1", "client", "A"), want: `{"client":"A"}`},
		{name: "a key of a group", table: map[string]map[string]string{"works": {"addr": "address"}},
			meta: metas("addr_city", "Tokyo", "addr_geo_lat", "35.6"), want: `{"address":{"city":"Tokyo","geo":{"lat":35.6}}}`},
		{name: "a key of a group and the name of the group", table: map[string]map[string]string{"works": {"addr": "address"}},
			meta: metas("address_city", "Osaka", "addr_city", "Tokyo"), want: `{"address":{"city":"Tokyo"}}`, unread: `"address_city" (1 item)`},
		{name: "a key of rows", table: map[string]map[string]string{"works": {"rows": "items"}},
			meta: metas("rows", "2", "rows_0_title", "A", "rows_1_title", "B"), want: `{"items":[{"title":"A"},{"title":"B"}]}`},
		{name: "a key of rows and the name of the rows", table: map[string]map[string]string{"works": {"rows": "items"}},
			meta: metas("items_0_title", "X", "rows_0_title", "A"), want: `{"items":[{"title":"A"}]}`, unread: `"items_0_title" (1 item)`},
		{name: "the table of another type", table: map[string]map[string]string{"post": {"client_name": "subtitle"}},
			meta: metas("client_name", "A", "client", "B"), want: `{"client":"B"}`, unread: `"client_name" (1 item)`},
		{name: "the table of the type", typ: "post", table: map[string]map[string]string{"post": {"sub": "subtitle"}},
			meta: metas("sub", "A", "subtitle", "B"), want: `{"subtitle":"A"}`, unread: `"subtitle" (1 item)`},
		{name: "a type with no fields", typ: "plain", meta: metas("client", "A"), unread: `"client" (1 item)`},
		{name: "a table of no keys", table: map[string]map[string]string{"works": {}}, meta: metas("client", "A"), want: `{"client":"A"}`},
		{name: "a table for the fields of a type that has no postmeta", table: map[string]map[string]string{"works": {"client_name": "client"}}, meta: metas("_edit_last", "1")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := fieldsSettings(tt.table)
			s.SEO.Plugin = tt.plugin
			typ := tt.typ
			if typ == "" {
				typ = "works"
			}
			wp := typ
			if typ == "post" {
				wp = ""
			}
			r := runFields(t, s, wxrEntry{id: 100, typ: wp, title: "T", name: "i100", link: "https://example.com/i100/", meta: tt.meta})
			r.check(t, 100, tt.want)
			var want []string
			if tt.unread != "" {
				msg := notImportedMessage(typ, tt.unread)
				if !strings.Contains(tt.unread, ",") {
					msg = fmt.Sprintf("the custom field %[1]s of the post type %[2]s is not imported; fields.%[2]s of the settings maps it to a field of the schema file", tt.unread, typ)
				}
				want = append(want, groupLine(r.imp, 100, typ, msg))
			}
			sameSet(t, "the lines about the custom fields that are not imported", fieldsLines(t, r.imp), want)
		})
	}
}

// Each record is about the postmeta that it names: the first of those that it is about.
func TestEntriesFieldsRecordSources(t *testing.T) {
	r := runFields(t, nil, fieldsItem(100, "works",
		"client", "first\nline", "client", "second",
		"gallery", "20", "gallery", "22", "gallery", "99",
		"urls", "a", "urls", "b\nc",
		"website", phpLink("https://example.org/", "Title", ""),
		"items_0_title", "t", "items_0_note", "n", "items_1_note", "m", "items_1_other", "o",
	))
	it := r.imp.WXR.Post(100)
	// metaAt returns the postmeta with the key, the nth of them.
	metaAt := func(key string, n int) wxr.Meta {
		for _, m := range it.Meta {
			if m.Key == key {
				if n == 0 {
					return m
				}
				n--
			}
		}
		t.Fatalf("no postmeta %d of %q", n, key)
		return wxr.Meta{}
	}
	want := map[string]wxr.Meta{
		`the custom field "client" has 2 values; the first is imported`:                                                                                    metaAt("client", 0),
		`line breaks and tabs in the custom field "client" are spaces here`:                                                                                metaAt("client", 0),
		`line breaks and tabs in the custom field "urls" are spaces here`:                                                                                  metaAt("urls", 1),
		`2 of the 3 values of the custom field "gallery" are not imported: value 2: attachment 22 is not imported; value 3: attachment 99 is not imported`: metaAt("gallery", 1),
		`the title "Title" of the link of the custom field "website" is not imported`:                                                                      metaAt("website", 0),
		`the sub fields "note", "other" of the custom field "items" are not imported; the field has no such fields in the schema file`:                     metaAt("items_0_note", 0),
	}
	found := make(map[string]bool)
	for _, e := range r.imp.Report.Entries() {
		if e.Source.ID != 100 || !strings.HasPrefix(e.Target.Pointer, "/fields") {
			continue
		}
		m, ok := want[e.Message]
		if !ok {
			continue
		}
		found[e.Message] = true
		if e.Source.Meta != m.Key || e.Source.Line != m.Line || e.Source.Column != m.Column {
			t.Errorf("the record %q is about the postmeta %q at %d:%d, want %q at %d:%d", e.Message, e.Source.Meta, e.Source.Line, e.Source.Column, m.Key, m.Line, m.Column)
		}
	}
	for message := range want {
		if !found[message] {
			t.Errorf("no record %q; the report about the item is\n%s", message, strings.Join(itemLines(r.imp.Report, 100), "\n"))
		}
	}
}

// The records name the key of the postmeta that a field is made of, and are about the field, which has another name.
func TestEntriesFieldsOfAnotherName(t *testing.T) {
	table := map[string]map[string]string{"works": {"client_name": "client", "addr": "address", "pics": "gallery", "rows": "items"}}
	r := runFields(t, fieldsSettings(table), fieldsItem(100, "works",
		"client_name", "a\nb", "addr_city", "c\nd", "pics", phpList(phpInt(20), phpInt(22)), "rows_0_title", "e", "rows_0_note", "n"))
	r.check(t, 100, `{"address":{"city":"c d"},"client":"a b","gallery":["{m20}"],"items":[{"title":"e"}]}`,
		`changed: line breaks and tabs in the custom field "client_name" are spaces here -> {R}#/fields/client`,
		`changed: line breaks and tabs in the custom field "addr" are spaces here -> {R}#/fields/address`,
		`skipped: 1 of the 2 values of the custom field "pics" is not imported: value 2: attachment 22 is not imported -> {R}#/fields/gallery`,
		`skipped: the sub field "note" of the custom field "rows" is not imported; the field has no such field in the schema file -> {R}#/fields/items`,
	)
}

func TestEntriesFieldsSettingsNil(t *testing.T) {
	in := entriesWXR(fieldsChannel(), append(fieldsSupport(), fieldsItem(100, "works", "client", "A", "client_name", "B", "redirect", "x"))...)
	imp, site := planEntries(t, fieldsSettings(map[string]map[string]string{"works": {"client_name": "client"}}), fieldsDefs(t), in)
	docs, err := collectEntries(context.Background(), imp, nil, site)
	if err != nil {
		t.Fatal(err)
	}
	checkEntries(t, imp, site, docs)
	if got, want := (&fieldsRun{docs: docs}).fields(t, 100), `{"client":"A","redirect":"x"}`; got != want {
		t.Errorf("the fields are %s, want %s", got, want)
	}
	want := []string{groupLine(imp, 100, "works", `the custom field "client_name" (1 item) of the post type works is not imported; fields.works of the settings maps it to a field of the schema file`)}
	sameSet(t, "the lines about the custom fields that are not imported", fieldsLines(t, imp), want)
}

func TestEntriesFieldsNotImported(t *testing.T) {
	run := func(plugin string, items ...wxrEntry) *fieldsRun {
		t.Helper()
		s := fieldsSettings(nil)
		s.SEO.Plugin = plugin
		return runFields(t, s, items...)
	}
	t.Run("once for each type, at its first item", func(t *testing.T) {
		r := run("",
			fieldsItem(100, "works", "client", "A", "old", "x", "note", "y", "_edit_lock", "z", "rank_math_title", "q", "ssp_meta_x", "q", "blank", " "),
			fieldsItem(101, "works", "old", "x", "client", "B"),
			fieldsItem(102, "works", "old", "x", "old", "y"),
			fieldsItem(103, "", "extra", "x", "subtitle", "S"),
			fieldsItem(104, "plain", "a", "1", "b", "2", "b", "3"),
			fieldsItem(105, "gear", "model", "M"),
			wxrEntry{id: 106, typ: "works", status: "trash", title: "Gone", name: "gone", meta: metas("trashed", "x")},
			wxrEntry{id: 107, typ: "works", status: "auto-draft", name: "auto", meta: metas("drafted", "x")},
		)
		sameSet(t, "the lines", fieldsLines(t, r.imp), []string{
			groupLine(r.imp, 100, "works", more("works", `"old" (3 items), "note" (1 item)`)),
			groupLine(r.imp, 103, "post", "the custom field \"extra\" (1 item) of the post type post is not imported; fields.post of the settings maps it to a field of the schema file"),
			groupLine(r.imp, 104, "plain", more("plain", `"a" (1 item), "b" (1 item)`)),
		})
		// They come after the records of the items, in the order of the types' first items.
		var got []string
		for _, l := range fieldsLines(t, r.imp) {
			got = append(got, l[:strings.Index(l, ": skipped:")])
		}
		var want []string
		for _, post := range []int64{100, 103, 104} {
			it := r.imp.WXR.Post(post)
			want = append(want, fmt.Sprintf("site.xml:%d:%d", it.Line, it.Column))
		}
		if !slices.Equal(got, want) {
			t.Errorf("the lines are at %q, want %q", got, want)
		}
	})
	t.Run("the keys that are read are not counted, nor are those of the rows of a list", func(t *testing.T) {
		r := run("", fieldsItem(100, "works",
			"address_city", "Tokyo", "address_extra", "e",
			"items", "1", "items_0_title", "A", "items_0_note", "n", "items_01_title", "x",
			"rep", "2", "rep_0_a", "x", "rep_1_a", "y",
			"sol_0_a", "z",
			"rep2_0_b", "1", "rep2_0_b_1_c", "x",
			"empty_0_a", " ", "empty", " ",
		))
		sameSet(t, "the lines", fieldsLines(t, r.imp), []string{
			groupLine(r.imp, 100, "works", more("works",
				`"address_extra" (1 item), "items_01_title" (1 item), "rep" (1 item), "rep2_0_b" (1 item), "sol_0_a" (1 item)`)),
		})
		r.check(t, 100, `{"address":{"city":"Tokyo"},"items":[{"title":"A"}]}`,
			`skipped: the sub field "note" of the custom field "items" is not imported; the field has no such field in the schema file -> {R}#/fields/items`)
	})
	t.Run("the first ten, and the number of the others", func(t *testing.T) {
		var kv []string
		list := []string{`"k13" (2 items)`}
		for i := 1; i <= 13; i++ {
			kv = append(kv, fmt.Sprintf("k%02d", i), "x")
			if i <= 9 {
				list = append(list, fmt.Sprintf(`"k%02d" (1 item)`, i))
			}
		}
		r := run("", fieldsItem(100, "works", kv...), fieldsItem(101, "works", "k13", "x"))
		sameSet(t, "the lines", fieldsLines(t, r.imp), []string{
			groupLine(r.imp, 100, "works", more("works", strings.Join(list, ", ")+", and 3 more")),
		})
	})
	t.Run("a key of the SEO plugin is not one of a custom field", func(t *testing.T) {
		kv := []string{"redirect", "x", "exclude_local_search", "1", "exclude_from_archive", "1", "_genesis_title", "T", "foo", "y"}
		r := run("tsf", fieldsItem(100, "works", kv...))
		sameSet(t, "the lines with the plugin", fieldsLines(t, r.imp), []string{groupLine(r.imp, 100, "works", `the custom field "foo" (1 item) of the post type works is not imported; fields.works of the settings maps it to a field of the schema file`)})
		r.check(t, 100, ``)

		r = run("", fieldsItem(100, "works", kv...))
		sameSet(t, "the lines without it", fieldsLines(t, r.imp), []string{
			groupLine(r.imp, 100, "works", more("works", `"exclude_from_archive" (1 item), "exclude_local_search" (1 item), "foo" (1 item)`)),
		})
		r.check(t, 100, `{"redirect":"x"}`)
	})
	t.Run("the same input gives the same report", func(t *testing.T) {
		items := []wxrEntry{
			fieldsItem(100, "works", "client", "A", "old", "x", "note", "y", "items_1_title", "t", "items_0_x", "u", "gallery", "20"),
			fieldsItem(101, "works", "a", "1", "b", "2", "c", "3"),
			fieldsItem(102, "", "extra", "x"),
		}
		var texts []string
		for range 3 {
			r := run("", items...)
			texts = append(texts, entriesText(t, r.imp, r.docs))
		}
		if texts[0] != texts[1] || texts[1] != texts[2] {
			t.Errorf("three conversions of the same WXR differ:\n%s\n%s", texts[0], texts[1])
		}
	})
}

func TestEntriesFieldsRequired(t *testing.T) {
	r := runFields(t, nil,
		fieldsItem(100, "gear", "model", "X"),
		fieldsItem(101, "gear"),
		fieldsItem(102, "gear", "model", " "),
		fieldsItem(103, "gear", "model", strings.Repeat("a", content.MaxTextBytes+1)),
	)
	for post, want := range map[int64]content.Status{100: content.StatusPublished, 101: content.StatusDraft, 102: content.StatusDraft, 103: content.StatusDraft} {
		if got := docOf(r.docs, post).Entry.Status; got != want {
			t.Errorf("the status of the item %d is %s, want %s", post, got, want)
		}
	}
	const draft = "review: the entry cannot be published: /fields/model: missing model; it is a draft here -> {E}#/status"
	r.check(t, 100, `{"model":"X"}`)
	r.check(t, 101, "", draft)
	r.check(t, 102, "", draft)
	r.check(t, 103, "", draft, fmt.Sprintf("skipped: the custom field \"model\" is not imported: it is too long: %d bytes (max %d) -> {R}#/fields/model", content.MaxTextBytes+1, content.MaxTextBytes))
}

// What is wrong with a field takes out the field, and nothing else of the entry: not the body, which the guard of emitAll takes
// back if the revision does not pass.
func TestEntriesFieldsAreTidied(t *testing.T) {
	item := fieldsItem(100, "works", "size", "XL", "client", "Acme", "colors", phpStrings("red", "cyan"))
	item.content = "<p>Hello</p>"
	r := runFields(t, nil, item)
	d := docOf(r.docs, 100)
	if len(d.Revision.Body) != 1 || d.Entry.Status != content.StatusPublished {
		t.Errorf("the entry has %d blocks and the status %s", len(d.Revision.Body), d.Entry.Status)
	}
	r.check(t, 100, `{"client":"Acme"}`,
		`skipped: the custom field "size" is not imported: "XL" is not an option -> {R}#/fields/size`,
		`skipped: the custom field "colors" is not imported: "cyan" is not an option -> {R}#/fields/colors`)
}

func TestEntriesFieldsSize(t *testing.T) {
	long := strings.Repeat("x", 2000)
	build := func(typ, key string) wxrEntry {
		item := fieldsItem(100, typ, key, long)
		item.content = "<p>Hello</p>"
		return item
	}
	for _, tt := range []struct{ typ, key string }{{"works", "client"}, {"nobody", "note"}} {
		t.Run(tt.typ, func(t *testing.T) {
			first := runFields(t, nil, build(tt.typ, tt.key))
			rev := docOf(first.docs, 100).Revision
			if len(rev.Fields) != 1 {
				t.Fatalf("the fields are %v", rev.Fields)
			}
			rest, _ := revisionBound(&content.Revision{Title: rev.Title, Slug: rev.Slug, Date: rev.Date, Fields: rev.Fields})
			const note = "skipped: the custom fields are not imported: the revision would be larger than %d bytes as JSON -> {R}#/fields"

			// The revision is not larger than the limit by more than the body: the fields stay, and the body does not fit.
			t.Run("at the limit", func(t *testing.T) {
				lowerLimit(t, &maxRevisionBytes, rest)
				r := runFields(t, nil, build(tt.typ, tt.key))
				if len(docOf(r.docs, 100).Revision.Fields) != 1 {
					t.Errorf("the fields are %v, want them left", docOf(r.docs, 100).Revision.Fields)
				}
				for _, l := range itemLines(r.imp.Report, 100) {
					if strings.Contains(l, "custom fields are not imported") {
						t.Errorf("the report has %q", l)
					}
				}
			})
			t.Run("over the limit by a byte", func(t *testing.T) {
				lowerLimit(t, &maxRevisionBytes, rest-1)
				r := runFields(t, nil, build(tt.typ, tt.key))
				d := docOf(r.docs, 100)
				if d.Revision.Fields != nil {
					t.Errorf("the fields are %v, want none", d.Revision.Fields)
				}
				want := []string{fmt.Sprintf(note, rest-1)}
				if tt.typ == "works" {
					if len(d.Revision.Body) != 1 {
						t.Errorf("the body has %d blocks, want it left", len(d.Revision.Body))
					}
				} else {
					want = append(want, "skipped: the content is not imported; the type nobody has no body -> {R}")
				}
				r.check(t, 100, "", want...)
			})
			t.Run("over the limit without the fields too", func(t *testing.T) {
				lowerLimit(t, &maxRevisionBytes, 50)
				r := runFields(t, nil, build(tt.typ, tt.key))
				d := docOf(r.docs, 100)
				if d.Revision.Fields != nil || d.Revision.Body != nil {
					t.Errorf("the revision has the fields %v and the body %v, want neither", d.Revision.Fields, d.Revision.Body)
				}
			})
		})
	}
}

// An item can have tens of thousands of postmeta, such as the rows of a repeater: the conversion takes a time that is linear in
// their number, and does not loop to the number that the parent of the rows says.
func TestEntriesFieldsLarge(t *testing.T) {
	rows, limit := 12_500, time.Second
	if raceEnabled {
		rows, limit = 1_250, 20*time.Second
	}
	// run makes the entries of a WXR with one item of the type works, whose postmeta are meta, and returns how long Entries took.
	run := func(t *testing.T, meta wxr.MetaList) (*Import, []*EntryDoc, time.Duration) {
		t.Helper()
		s := fieldsSettings(nil)
		imp, site := planEntries(t, s, fieldsDefs(t), entriesWXR(fieldsChannel(), append(fieldsSupport(), fieldsItem(100, "works"))...))
		imp.WXR.Post(100).Meta = meta
		start := time.Now()
		docs, err := collectEntries(context.Background(), imp, s, site)
		took := time.Since(start)
		if err != nil {
			t.Fatalf("Entries = %v", err)
		}
		checkEntries(t, imp, site, docs)
		return imp, docs, took
	}
	pair := func(key, value string) wxr.Meta { return wxr.Meta{Key: key, Value: value} }

	t.Run("rows and keys that are not imported", func(t *testing.T) {
		// The rows in the order of the numbers that is the reverse of the order of the numbers, with a field that the schema file has
		// and one that it does not; and as many keys of fields that are not there.
		meta := wxr.MetaList{pair("items", "1000000000")}
		for i := rows - 1; i >= 0; i-- {
			meta = append(meta, pair(fmt.Sprintf("items_%d_title", i), fmt.Sprintf("t%d", i)), pair(fmt.Sprintf("items_%d_amount", i), fmt.Sprint(i)),
				pair(fmt.Sprintf("junk_%d", i), "x"), pair(fmt.Sprintf("junk_%d_%d_x", i, i), "x"))
		}
		meta = append(meta, pair("client", "Acme"))
		imp, docs, took := run(t, meta)
		if took > limit {
			t.Errorf("Entries of an item of %d postmeta took %v, want less than %v", len(meta), took, limit)
		}
		var items []map[string]any
		if err := json.Unmarshal(docOf(docs, 100).Revision.Fields["items"], &items); err != nil {
			t.Fatal(err)
		}
		if len(items) != rows {
			t.Fatalf("%d rows, want %d", len(items), rows)
		}
		for i, row := range items {
			if row["title"] != fmt.Sprintf("t%d", i) || row["amount"] != float64(i) {
				t.Fatalf("row %d is %v", i, row)
			}
		}
		// One record for the keys that are not imported, and none for each row or key. The rows have the keys junk_<number>_<number>_x
		// that are not imported too, but they are those of the rows of junk_<number>, which is a key.
		var names []string
		for i := range rows {
			names = append(names, fmt.Sprintf("junk_%d", i))
		}
		slices.Sort(names)
		var list []string
		for _, name := range names[:10] {
			list = append(list, fmt.Sprintf("%q (1 item)", name))
		}
		want := groupLine(imp, 100, "works", more("works", strings.Join(list, ", ")+fmt.Sprintf(", and %d more", rows-10)))
		sameSet(t, "the lines about the custom fields that are not imported", fieldsLines(t, imp), []string{want})
		if n := len(imp.Report.Entries()); n > 5 {
			t.Errorf("the report has %d entries, want a few", n)
		}
	})
	t.Run("a number of the parent that is large", func(t *testing.T) {
		_, docs, took := run(t, wxr.MetaList{pair("items", "999999999999"), pair("items_5_title", "a"), pair("items_999999999_title", "b")})
		if got := string(docOf(docs, 100).Revision.Fields["items"]); got != `[{"title":"a"},{"title":"b"}]` {
			t.Errorf("the rows are %s", got)
		}
		if took > limit {
			t.Errorf("Entries took %v, want less than %v", took, limit)
		}
	})
	t.Run("keys that look like rows", func(t *testing.T) {
		// A key with many numbers between underscores is read once for each, not for each pair of them.
		key := "a" + strings.Repeat("_1", 20_000) + "_x"
		_, _, took := run(t, wxr.MetaList{pair("items", "1"), pair(key, "x"), pair("items_0_title", "t")})
		if took > limit {
			t.Errorf("Entries took %v, want less than %v", took, limit)
		}
	})
}

// A reference to something that the table or the references do not have any more is not imported either.
func TestEntriesFieldsReferencesDropped(t *testing.T) {
	s := fieldsSettings(nil)
	imp, site := planEntries(t, s, fieldsDefs(t), entriesWXR(fieldsChannel(),
		append(fieldsSupport(), fieldsItem(100, "works", "cover", "20", "topic", "2", "keywords", phpList(phpInt(4), phpInt(5)),
			"website", "https://example.com/wp-content/uploads/2026/10/a.png"))...))
	for _, id := range []string{postIDOf(t, imp, 20), termIDOf(t, imp, "category", "news"), termIDOf(t, imp, "post_tag", "go")} {
		imp.DropRef(id)
	}
	docs, err := collectEntries(context.Background(), imp, s, site)
	if err != nil {
		t.Fatal(err)
	}
	r := &fieldsRun{imp: imp, site: site, docs: docs, ids: strings.NewReplacer("{t:rust}", termIDOf(t, imp, "post_tag", "rust"))}
	r.check(t, 100, `{"keywords":["{t:rust}"],"website":{"href":"/wp-content/uploads/2026/10/a.png"}}`,
		`skipped: the custom field "cover" is not imported: attachment 20 is not imported -> {R}#/fields/cover`,
		`skipped: the custom field "topic" is not imported: term 2 is not imported -> {R}#/fields/topic`,
		`skipped: 1 of the 2 values of the custom field "keywords" is not imported: value 1: term 4 is not imported -> {R}#/fields/keywords`,
	)
}

// The values that are empty are no value, whatever the type of the field: the conversion of a value does not need them left out.
func TestScalarValuesThatAreEmpty(t *testing.T) {
	for name, convert := range map[string]func(string) (jsontext.Value, string){
		"number": numberValue, "boolean": boolValue,
	} {
		for _, in := range []string{"", " ", "\n\t"} {
			if got, why := convert(in); got != nil || why != "" {
				t.Errorf("the %s of %q is %q, %q, want none", name, in, got, why)
			}
		}
	}
}

func TestStringValue(t *testing.T) {
	for _, tt := range []struct {
		in, want, why string
	}{
		{"", "", ""},
		{" \n\t", "", ""},
		{"a", `"a"`, ""},
		{" a ", `" a "`, ""},
		{strings.Repeat("a", content.MaxTextBytes), `"` + strings.Repeat("a", content.MaxTextBytes) + `"`, ""},
		{strings.Repeat("a", content.MaxTextBytes+1), "", fmt.Sprintf("it is too long: %d bytes (max %d)", content.MaxTextBytes+1, content.MaxTextBytes)},
		{"a\xffb", "", "it is not UTF-8 text"},
		{"\xc3", "", "it is not UTF-8 text"},
	} {
		got, why := stringValue(tt.in)
		if string(got) != tt.want || why != tt.why {
			t.Errorf("stringValue(%.20q) = %.30q, %q, want %.30q, %q", tt.in, got, why, tt.want, tt.why)
		}
	}
}

func TestRowSplits(t *testing.T) {
	for _, tt := range []struct {
		key  string
		want []rowSplit
	}{
		{"a_1_b", []rowSplit{{1, 4, 1}}},
		{"a_0_b", []rowSplit{{1, 4, 0}}},
		{"items_12_title", []rowSplit{{5, 9, 12}}},
		{"a_1_2_b", []rowSplit{{1, 4, 1}, {3, 6, 2}}},
		{"a_1_b_22_c", []rowSplit{{1, 4, 1}, {5, 9, 22}}},
		{"a_1_", []rowSplit{{1, 4, 1}}},
		{"a_1__b", []rowSplit{{1, 4, 1}}},
		{"a__1_b", []rowSplit{{2, 5, 1}}},
		{"a_123456789_b", []rowSplit{{1, 12, 123456789}}},
		{"a_1234567890_b", nil},
		{"a_01_b", nil},
		{"a_00_b", nil},
		{"a_1", nil},
		{"a_x_b", nil},
		{"a_-1_b", nil},
		{"a_1a_b", nil},
		{"_1_a", nil},
		{"1_1_a", []rowSplit{{1, 4, 1}}},
		{"abc", nil},
		{"", nil},
	} {
		var got []rowSplit
		for sp := range rowSplits(tt.key) {
			got = append(got, sp)
		}
		if !slices.Equal(got, tt.want) {
			t.Errorf("rowSplits(%q) = %v, want %v", tt.key, got, tt.want)
		}
	}
	// The loop of a caller that stops ends it.
	n := 0
	for range rowSplits("a_1_2_3_b") {
		if n++; n == 2 {
			break
		}
	}
	if n != 2 {
		t.Errorf("a loop that breaks at the second place read %d", n)
	}
}

func TestIsRowKey(t *testing.T) {
	idx := &metaIndex{metas: wxr.MetaList{{Key: "a"}, {Key: "a_0_b"}, {Key: "c_1"}, {Key: "a_0_b_1_c"}, {Key: "d_2_e"}}}
	for key, want := range map[string]bool{
		"a_0_x": true, "a_0_b_1_c": true, "a_7_": true, "a_0_b_9_z": true, "c_1": false, "c_1_x": false,
		"d_2_e": false, "e_2_d": false, "a_01_x": false, "b_0_x": false, "a": false, "": false, "a_0": false,
	} {
		if got := isRowKey(key, idx); got != want {
			t.Errorf("isRowKey(%q) = %v, want %v", key, got, want)
		}
	}
}

func TestRowsOf(t *testing.T) {
	idx := &metaIndex{metas: wxr.MetaList{
		{Key: "items_1_b"}, {Key: "items"}, {Key: "items_0_a"}, {Key: "items_1_a"}, {Key: "items_x_a"}, {Key: "itemsx_0_a"},
		{Key: "items_0_sub_1_c"}, {Key: "item_0_a"}, {Key: "items_1_a"}, {Key: "items_10_a"}, {Key: "items_2_"}, {Key: "items_01_a"},
		{Key: "items_"}, {Key: "items_3"},
	}}
	// row is the number of a row, the index of the postmeta, and what its key has after the number.
	type row struct {
		n, meta int
		rest    string
	}
	for _, tt := range []struct {
		prefix string
		want   []row
	}{
		{"items_", []row{{0, 2, "a"}, {0, 6, "sub_1_c"}, {1, 0, "b"}, {1, 3, "a"}, {1, 8, "a"}, {2, 10, ""}, {10, 9, "a"}}},
		{"items_0_sub_", []row{{1, 6, "c"}}},
		{"item_", []row{{0, 7, "a"}}},
		{"itemsx_", []row{{0, 5, "a"}}},
		{"items_1_", nil},
		{"none_", nil},
		{"z_", nil},
		{"a_", nil},
	} {
		var got []row
		for _, k := range idx.rowsOf(tt.prefix) {
			got = append(got, row{k.n, int(k.meta), idx.metas[k.meta].Key[k.rest:]})
		}
		if !slices.Equal(got, tt.want) {
			t.Errorf("rowsOf(%q) = %v, want %v", tt.prefix, got, tt.want)
		}
	}
	if got := (&metaIndex{}).rowsOf("items_"); got != nil {
		t.Errorf("rowsOf of no postmeta = %v", got)
	}
}

func TestNestedKeys(t *testing.T) {
	n := nestedKeysOf(fieldsDefs(t).Type("works"))
	for key, want := range map[string]bool{
		"address_city": true, "address_zip": true, "address_geo_lat": true, "address_geo_lng": true,
		"items_0_title": true, "items_12_parts_3_name": true, "items_0_nosuch": true, "items_0_": true, "steps_1_name": true,
		"address": false, "address_geo": false, "address_country": false, "addressx_city": false, "client": false, "items": false,
		"items_title": false, "items_00_title": false, "items_0": false, "itemsx_0_title": false, "gallery_0_x": false,
		"matrix_0_1": false, "x_items_0_title": false, "": false,
	} {
		if got := n.has(key); got != want {
			t.Errorf("has(%q) = %v, want %v", key, got, want)
		}
	}
	// A list of groups in a group has its number of rows in a key of the group.
	defs := new(content.Site)
	if err := defs.UnmarshalSchema([]byte(`schemaVersion: 1
types:
  post:
    fields:
      box:
        type: group
        fields:
          rows: {type: list, of: {type: group, fields: {name: {type: text}}}}
          tags: {type: list, of: {type: text}}
`)); err != nil {
		t.Fatal(err)
	}
	n = nestedKeysOf(defs.Type("post"))
	for key, want := range map[string]bool{"box_rows": true, "box_rows_0_name": true, "box_tags": true, "box_tags_0_x": false, "box": false, "rows_0_name": false} {
		if got := n.has(key); got != want {
			t.Errorf("has(%q) of a list in a group = %v, want %v", key, got, want)
		}
	}
	if nestedKeysOf(nil).has("a_0_b") {
		t.Error("a type that is not defined has nested keys")
	}
}

func TestIsDecimal(t *testing.T) {
	for s, want := range map[string]bool{
		"0": true, "12": true, "-3.5": true, "+5": true, ".5": true, "5.": true, "1e3": true, "1E+3": true, "1.5e-3": true, "007": true,
		"": false, "-": false, "+": false, ".": false, "e3": false, "1e": false, "1e+": false, "1.2.3": false, "1,000": false, "0x1": false,
		"inf": false, "nan": false, "1_0": false, " 1": false, "1 ": false, "--1": false, "1e3.5": false, "١٢": false,
	} {
		if got := isDecimal(s); got != want {
			t.Errorf("isDecimal(%q) = %v, want %v", s, got, want)
		}
	}
}

// The settings that are given to Entries are not those that Site checked: it takes what they say as they are.
func TestEntriesFieldKeys(t *testing.T) {
	s := fieldsSettings(map[string]map[string]string{
		"works":  {"b": "client", "a": "client", "c": "nosuch", "d": ""},
		"nosuch": {"x": "client"},
	})
	imp, site := planEntries(t, fieldsSettings(nil), fieldsDefs(t), entriesWXR(fieldsChannel(),
		append(fieldsSupport(), fieldsItem(100, "works", "a", "from a", "b", "from b", "c", "from c", "d", "from d", "memo", "kept"))...))
	docs, err := collectEntries(context.Background(), imp, s, site)
	if err != nil {
		t.Fatal(err)
	}
	checkEntries(t, imp, site, docs)
	// Of two keys of a field, the first in order is the key of it.
	if got, want := (&fieldsRun{docs: docs}).fields(t, 100), `{"client":"from a","memo":"kept"}`; got != want {
		t.Errorf("the fields are %s, want %s", got, want)
	}
	sameSet(t, "the lines about the custom fields that are not imported", fieldsLines(t, imp), []string{
		groupLine(imp, 100, "works", more("works", `"b" (1 item), "c" (1 item), "d" (1 item)`)),
	})
}

// fuzzFields is what FuzzFieldValue converts the postmeta with.
type fuzzFields struct {
	c  *entryConv
	ei *entryItem
}

func newFuzzFields(t *testing.T) *fuzzFields {
	t.Helper()
	s := fieldsSettings(nil)
	imp, site := planEntries(t, s, fieldsDefs(t), entriesWXR(fieldsChannel(), append(fieldsSupport(), fieldsItem(100, "works"))...))
	c := &entryConv{imp: imp, site: site, opts: EntryOptions{Site: site, Settings: s, By: mediaTestBy}, zone: mustLocation(t, "Asia/Tokyo")}
	items, _, err := c.collect()
	if err != nil {
		t.Fatal(err)
	}
	for _, ei := range items {
		if ei.it.ID == 100 && c.begin(ei) {
			return &fuzzFields{c: c, ei: ei}
		}
	}
	t.Fatal("the item 100 is not imported")
	return nil
}

// FuzzFieldValue checks that the conversion of the value of a postmeta, of each type of field that has no limits, takes no
// time to speak of, and that what it makes is a value of the field that passes the validation as a draft.
func FuzzFieldValue(f *testing.F) {
	for _, seed := range []string{
		"", " ", "Acme", "line\nbreak\ttab", "12", "-3.5", "1e3", "1,000", "abc", "1e999", "0x10", "inf", "+.5e-2", "-0", "1",
		"0", "true", "yes", "on", "No", "OFF", "20261114", "2026-11-14", "2026/11/14", "2026-02-30", "2026-11-14 10:00:00",
		"2026-11-14T10:00:00+09:00", "2026-11-14T10:00:00Z", "2026-11-14T10:00:00.5+09:00", "2026-11-14T10:00:00+24:00",
		"https://example.org/a", "https://example.com/post-1/", "/about/", "#part", "#a b", "mailto:a@example.org", "javascript:alert(1)",
		"java\tscript:x", "foo/bar", "//example.org/", `s:4:"Acme";`, "i:42;", "d:2.5;", "b:1;", "N;", "a:0:{}",
		phpStrings("a", "b"), phpLink("https://example.org/a", "Docs", "_blank"), phpLink("", "x", ""), phpList(phpLink("https://example.org/a", "", "")),
		`a:1:{i:0;s:3:"ab";}`, "a:2:{i:0;i:1;}", "a:1:{s:3:\"url\";a:0:{}}", string(rune(0x304B)) + string(rune(0x3099)), "\xff\xfe",
		strings.Repeat("9", 400), "-" + strings.Repeat("0", 100) + "1",
	} {
		f.Add(seed)
	}
	var fz *fuzzFields
	f.Fuzz(func(t *testing.T, value string) {
		if fz == nil {
			fz = newFuzzFields(t)
		}
		c, ei := fz.c, fz.ei
		c.imp.Report = NewReport(Info{})
		c.fields = fieldState{}
		keys := []string{"client", "memo", "year", "onsale", "since", "opens", "website"}
		var meta [][2]string
		for _, k := range keys {
			meta = append(meta, [2]string{k, value})
		}
		ei.it.Meta = nil
		for _, m := range meta {
			ei.it.Meta = append(ei.it.Meta, wxr.Meta{Key: m[0], Value: m[1]})
		}
		ei.rev = content.Revision{}
		c.setFields(ei)
		ei.rev.Normalize(c.site.Type(ei.entry.Type))
		if err := ei.rev.Validate(c.revisionOptions(&ei.entry, false)); err != nil {
			t.Errorf("the fields %v of the value %q do not pass the validation: %v", ei.rev.Fields, value, err)
		}
	})
}

// The fields that make the revision too large are left out before the status is decided: the entry that needs one is a draft,
// and the body, which the check of the entry has to be able to take back alone, stays.
func TestEntriesFieldsSizeOfRequired(t *testing.T) {
	build := func() wxrEntry {
		item := fieldsItem(100, "gear", "model", strings.Repeat("x", 2000))
		item.content = "<p>Hello</p>"
		return item
	}
	first := runFields(t, nil, build())
	if d := docOf(first.docs, 100); d.Entry.Status != content.StatusPublished || len(d.Revision.Fields) != 1 || len(d.Revision.Body) != 1 {
		t.Fatalf("the entry is %+v with the revision %+v, want a published entry with a field and a block", d.Entry, d.Revision)
	}
	rev := docOf(first.docs, 100).Revision
	rest, _ := revisionBound(&content.Revision{Title: rev.Title, Slug: rev.Slug, Fields: rev.Fields})
	lowerLimit(t, &maxRevisionBytes, rest-1)
	r := runFields(t, nil, build())
	d := docOf(r.docs, 100)
	if d.Entry.Status != content.StatusDraft || d.Revision.Fields != nil || len(d.Revision.Body) != 1 {
		t.Errorf("the entry is %+v with the revision %+v, want a draft with no field and a block", d.Entry, d.Revision)
	}
	r.check(t, 100, "",
		fmt.Sprintf("skipped: the custom fields are not imported: the revision would be larger than %d bytes as JSON -> {R}#/fields", rest-1),
		"review: the entry cannot be published: /fields/model: missing model; it is a draft here -> {E}#/status")
}

// richText and blocks fields are not made of the HTML of a custom field.
func TestEntriesFieldRichText(t *testing.T) {
	const why = "the import does not convert HTML into richText and blocks fields"
	notImported := func(field, key string) string {
		return fmt.Sprintf("skipped: the custom field %q is not imported: %s -> {R}#/fields/%s", key, why, field)
	}
	runFieldCases(t, nil, []fieldCase{
		{name: "richText", meta: metas("intro", "<p>Hello</p>"), lines: []string{notImported("intro", "intro")}},
		{name: "blocks", meta: metas("sections", "<p>Hello</p>"), lines: []string{notImported("sections", "sections")}},
		{name: "richText that is empty", meta: metas("intro", " ")},
		{name: "richText and another field", meta: metas("intro", "<p>Hello</p>", "client", "Acme"), want: `{"client":"Acme"}`, lines: []string{notImported("intro", "intro")}},
	})
}
