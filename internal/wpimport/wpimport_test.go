package wpimport

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/phpserialize"
	"github.com/WhiteRoomLab/whiteroom/internal/wprest"
	"github.com/WhiteRoomLab/whiteroom/internal/wxr"
)

const (
	nsDecl = `xmlns:excerpt="http://wordpress.org/export/1.2/excerpt/" xmlns:content="http://purl.org/rss/1.0/modules/content/" ` +
		`xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:wp="http://wordpress.org/export/1.2/"`
	// head is a WXR 1.2 up to the line after wp:wxr_version, which is the third.
	head = `<rss version="2.0" ` + nsDecl + ">\n<channel>\n<wp:wxr_version>1.2</wp:wxr_version>\n"
	tail = "</channel>\n</rss>\n"
)

func readString(t *testing.T, name, in string) (*wxr.Export, *Report) {
	t.Helper()
	e, rep, err := Read(context.Background(), name, strings.NewReader(in))
	if err != nil {
		t.Fatalf("Read(%s) = %v", name, err)
	}
	return e, rep
}

func readSample(t *testing.T, name string) (*wxr.Export, *Report) {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "wxr", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	e, rep, err := Read(context.Background(), name, f)
	if err != nil {
		t.Fatalf("Read(%s) = %v", name, err)
	}
	return e, rep
}

// place returns the line and the column of the nth occurrence of sub in s, counting from 1, and the columns in
// characters.
func place(t *testing.T, s, sub string, nth int) (line, column int) {
	t.Helper()
	i := -1
	for range nth {
		j := strings.Index(s[i+1:], sub)
		if j < 0 {
			t.Fatalf("%q does not occur %d times in %q", sub, nth, s)
		}
		i += 1 + j
	}
	line, column = 1, 1
	for _, r := range s[:i] {
		if r == '\n' {
			line, column = line+1, 1
		} else {
			column++
		}
	}
	return line, column
}

func TestRead(t *testing.T) {
	for _, tt := range []struct {
		name    string
		info    Info
		entries []Entry
	}{
		{
			name: "wordpress-6.xml",
			info: Info{File: "wordpress-6.xml", Version: "1.2", WordPress: "6.8.3", Site: "https://example.com"},
			entries: []Entry{{
				Kind:    Skipped,
				Source:  Source{File: "wordpress-6.xml", Line: 12, Column: 1, Record: "channel"},
				Message: "2 comments on 1 item are not imported; whiteroom has no comments",
			}},
		},
		{
			name: "wordpress-3.3.xml",
			info: Info{File: "wordpress-3.3.xml", Version: "1.1", WordPress: "3.3.1", Site: "http://blog.example.com"},
		},
		{
			name: "wordpress-3.0.xml",
			info: Info{File: "wordpress-3.0.xml", Version: "1.0", WordPress: "3.0", Site: "http://notes.example.com"},
			entries: []Entry{{
				Kind:    Skipped,
				Source:  Source{File: "wordpress-3.0.xml", Line: 12, Column: 1, Record: "channel"},
				Message: "1 comment on 1 item is not imported; whiteroom has no comments",
			}},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e, rep := readSample(t, tt.name)
			if e.Version != tt.info.Version || len(e.Items) == 0 {
				t.Errorf("Export has Version %q and %d items", e.Version, len(e.Items))
			}
			if rep.info != tt.info {
				t.Errorf("Info = %+v, want %+v", rep.info, tt.info)
			}
			got := rep.Entries()
			if len(got) != len(tt.entries) {
				t.Fatalf("Entries = %+v, want %+v", got, tt.entries)
			}
			for i := range got {
				if got[i] != tt.entries[i] {
					t.Errorf("Entries()[%d] = %+v, want %+v", i, got[i], tt.entries[i])
				}
			}
		})
	}
}

func TestReadComments(t *testing.T) {
	comment := "<wp:comment><wp:comment_id>1</wp:comment_id></wp:comment>"
	item := func(id int, comments int) string {
		return fmt.Sprintf("<item><wp:post_id>%d</wp:post_id><wp:post_type>post</wp:post_type>%s</item>\n", id, strings.Repeat(comment, comments))
	}
	for _, tt := range []struct {
		name  string
		items string
		want  string
	}{
		{"none", item(1, 0) + item(2, 0), ""},
		{"one", item(1, 0) + item(2, 1), "1 comment on 1 item is not imported; whiteroom has no comments"},
		{"two on one item", item(1, 2), "2 comments on 1 item are not imported; whiteroom has no comments"},
		{"two on two items", item(1, 1) + item(2, 1) + item(3, 0), "2 comments on 2 items are not imported; whiteroom has no comments"},
		{"five on three items", item(1, 3) + item(2, 1) + item(3, 0) + item(4, 1), "5 comments on 3 items are not imported; whiteroom has no comments"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			in := head + "<link>https://example.com</link>\n" + tt.items + tail
			_, rep := readString(t, "site.xml", in)
			entries := rep.Entries()
			if tt.want == "" {
				if len(entries) != 0 {
					t.Errorf("Entries = %+v, want none", entries)
				}
				return
			}
			line, column := place(t, in, "<channel>", 1)
			want := Entry{Kind: Skipped, Source: Source{File: "site.xml", Line: line, Column: column, Record: "channel"}, Message: tt.want}
			if len(entries) != 1 || entries[0] != want {
				t.Errorf("Entries = %+v, want [%+v]", entries, want)
			}
		})
	}
}

func TestReadNotes(t *testing.T) {
	u := func(r rune) string { return string(r) }
	vt, soh, stx, etx := u(0x0b), u(0x01), u(0x02), u(0x03)
	in := head +
		"<title>Site" + vt + "</title>\n" +
		"<link>https://example.com</link>\n" +
		"<wp:author><wp:author_id>3</wp:author_id><wp:author_login>ed" + soh + "itor</wp:author_login></wp:author>\n" +
		"<wp:category><wp:term_id>5</wp:term_id><wp:category_nicename>news</wp:category_nicename><wp:cat_name>Ne" + stx + "ws</wp:cat_name></wp:category>\n" +
		"<item>\n<title>He" + etx + "llo" + etx + "</title>\n<wp:post_id>45</wp:post_id>\n<wp:post_type>post</wp:post_type>\n</item>\n" +
		tail
	_, rep := readString(t, "site.xml", in)

	// at returns the source with the place of the nth occurrence of char, and that place as the line and column.
	at := func(src Source, char string, nth int) (Source, string) {
		line, column := place(t, in, char, nth)
		src.File, src.Line, src.Column = "site.xml", line, column
		return src, fmt.Sprintf("U+%04X at %d:%d", []rune(char)[0], line, column)
	}
	channel, chMsg := at(Source{Record: "channel"}, vt, 1)
	author, auMsg := at(Source{Record: "author", ID: 3, Name: "editor"}, soh, 1)
	term, teMsg := at(Source{Record: "term", ID: 5, Type: "category", Name: "News"}, stx, 1)
	item, it1 := at(Source{Record: "item", ID: 45, Type: "post", Name: "Hello"}, etx, 1)
	_, it2 := at(Source{}, etx, 2)
	want := []Entry{
		{Kind: Changed, Source: channel, Message: "removed 1 character that XML does not allow: " + chMsg},
		{Kind: Changed, Source: author, Message: "removed 1 character that XML does not allow: " + auMsg},
		{Kind: Changed, Source: term, Message: "removed 1 character that XML does not allow: " + teMsg},
		{Kind: Changed, Source: item, Message: "removed 2 characters that XML does not allow: " + it1 + ", " + it2},
	}
	got := rep.Entries()
	if len(got) != len(want) {
		t.Fatalf("Entries = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Entries()[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}

	// The login of an author who is an email address is not named: the note shows the author by the ID.
	in = head +
		"<link>https://example.com</link>\n" +
		"<wp:author><wp:author_id>3</wp:author_id><wp:author_login>mary@example.org</wp:author_login>" +
		"<wp:author_email>mary@example.org</wp:author_email><wp:author_display_name>Ma" + soh + "ry</wp:author_display_name></wp:author>\n" +
		tail
	_, rep = readString(t, "site.xml", in)
	line, column := place(t, in, soh, 1)
	wantEntry := Entry{
		Kind:    Changed,
		Source:  Source{File: "site.xml", Line: line, Column: column, Record: "author", ID: 3},
		Message: fmt.Sprintf("removed 1 character that XML does not allow: U+0001 at %d:%d", line, column),
	}
	if got := rep.Entries(); len(got) != 1 || got[0] != wantEntry {
		t.Errorf("Entries = %+v, want [%+v]", got, wantEntry)
	}
	var text, js strings.Builder
	if err := rep.WriteText(&text); err != nil {
		t.Fatal(err)
	}
	if err := rep.WriteJSON(&js); err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("site.xml:%d:%d: changed: author 3: %s\n", line, column, wantEntry.Message); text.String() != want {
		t.Errorf("WriteText = %q, want %q", text.String(), want)
	}
	if strings.Contains(text.String()+js.String(), "mary@example.org") {
		t.Errorf("the email address is in the report:\n%s\n%s", text.String(), js.String())
	}
}

func TestReadErrors(t *testing.T) {
	t.Run("a file with a problem", func(t *testing.T) {
		for _, in := range []string{
			"",
			"hello",
			head + "<item>\n<wp:post_id>1</wp:post_id>\n<wp:post_type>post</wp:post_type>\n" + tail,
			head + "<item>\n<wp:post_id>x</wp:post_id>\n<wp:post_type>post</wp:post_type>\n</item>\n" + tail,
		} {
			e, rep, err := Read(context.Background(), "site.xml", strings.NewReader(in))
			_, direct := wxr.Read(context.Background(), strings.NewReader(in), wxr.Options{})
			var we *wxr.Error
			if !errors.As(direct, &we) {
				t.Fatalf("wxr.Read(%q) = %v, want a *wxr.Error", in, direct)
			}
			var ce *content.Error
			if !errors.As(err, &ce) {
				t.Fatalf("Read(%q) = %v, want a *content.Error", in, err)
			}
			if want := (content.Error{File: "site.xml", Line: we.Line, Column: we.Column, Msg: we.Msg}); *ce != want {
				t.Errorf("Read(%q) = %+v, want %+v", in, *ce, want)
			}
			if want := fmt.Sprintf("site.xml:%d:%d: %s", we.Line, we.Column, we.Msg); err.Error() != want {
				t.Errorf("Read(%q).Error() = %q, want %q", in, err.Error(), want)
			}
			if e != nil || rep != nil {
				t.Errorf("Read(%q) = %v, %v with an error, want nil", in, e, rep)
			}
		}
	})
	t.Run("the error as it is written", func(t *testing.T) {
		_, _, err := Read(context.Background(), "empty.xml", strings.NewReader(""))
		if want := "empty.xml:1:1: not a WXR file: missing <rss>"; err == nil || err.Error() != want {
			t.Errorf("Read(empty) = %v, want %s", err, want)
		}
	})
	t.Run("a reader that fails", func(t *testing.T) {
		boom := errors.New("boom")
		e, rep, err := Read(context.Background(), "site.xml", iotest.ErrReader(boom))
		var ce *content.Error
		if !errors.Is(err, boom) || errors.As(err, &ce) || e != nil || rep != nil {
			t.Errorf("Read = %v, %v, %v; want the error of the reader, not a *content.Error", e, rep, err)
		}
	})
	t.Run("a canceled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		e, rep, err := Read(ctx, "site.xml", strings.NewReader(head+tail))
		var ce *content.Error
		if !errors.Is(err, context.Canceled) || errors.As(err, &ce) || e != nil || rep != nil {
			t.Errorf("Read = %v, %v, %v; want context.Canceled", e, rep, err)
		}
	})
}

func TestSources(t *testing.T) {
	e := &wxr.Export{Channel: wxr.Channel{Pos: wxr.Pos{Line: 2, Column: 1}}}
	a := &wxr.Author{Pos: wxr.Pos{Line: 5, Column: 2}, ID: 3, Login: "editor"}
	term := &wxr.Term{Pos: wxr.Pos{Line: 8, Column: 3}, ID: 7, Taxonomy: "genre", Name: "SF"}
	it := &wxr.Item{Pos: wxr.Pos{Line: 11, Column: 4}, ID: 14, Type: "attachment", Title: "photo"}
	m := wxr.Meta{Pos: wxr.Pos{Line: 20, Column: 5}, Key: "_mail", Value: "x"}
	item := ItemSource("a.xml", it)
	for _, tt := range []struct {
		name string
		got  Source
		want Source
	}{
		{"channel", ChannelSource("a.xml", e), Source{File: "a.xml", Line: 2, Column: 1, Record: "channel"}},
		{"author", AuthorSource("a.xml", a), Source{File: "a.xml", Line: 5, Column: 2, Record: "author", ID: 3, Name: "editor"}},
		{"term", TermSource("a.xml", term), Source{File: "a.xml", Line: 8, Column: 3, Record: "term", ID: 7, Type: "genre", Name: "SF"}},
		{"item", item, Source{File: "a.xml", Line: 11, Column: 4, Record: "item", ID: 14, Type: "attachment", Name: "photo"}},
		{"meta of an item", MetaSource(item, m), Source{File: "a.xml", Line: 20, Column: 5, Record: "item", ID: 14, Type: "attachment", Name: "photo", Meta: "_mail"}},
		{"meta of a term", MetaSource(TermSource("a.xml", term), m), Source{File: "a.xml", Line: 20, Column: 5, Record: "term", ID: 7, Type: "genre", Name: "SF", Meta: "_mail"}},
		{"taxonomy of a term", taxonomySource("a.xml", term), Source{File: "a.xml", Line: 8, Column: 3, Record: "term", Type: "genre"}},
		{"route of the REST API", RESTSource("rest.json", "/wp/v2/users"), Source{File: "rest.json", Record: "rest", Name: "/wp/v2/users"}},
		{"user of the REST API", RESTUserSource("rest.json", &wprest.User{ID: 5, Name: "admin", Slug: "ignored"}), Source{File: "rest.json", Record: "rest", ID: 5, Type: "user", Name: "admin"}},
		{"term of the REST API", RESTTermSource("rest.json", &wprest.Term{ID: 3, Taxonomy: "category", Name: "News", Slug: "ignored"}), Source{File: "rest.json", Record: "rest", ID: 3, Type: "category", Name: "News"}},
	} {
		if tt.got != tt.want {
			t.Errorf("%s: Source = %+v, want %+v", tt.name, tt.got, tt.want)
		}
	}
	for _, tt := range []struct {
		src  Source
		want string
	}{
		{taxonomySource("a.xml", term), "term (genre)"},
		{RESTSource("rest.json", "/wp/v2/users"), `rest "/wp/v2/users"`},
		{RESTUserSource("rest.json", &wprest.User{ID: 5, Name: "admin"}), `rest 5 (user "admin")`},
		{RESTTermSource("rest.json", &wprest.Term{ID: 3, Taxonomy: "category", Name: "News"}), `rest 3 (category "News")`},
	} {
		if got := tt.src.what(); got != tt.want {
			t.Errorf("what() of %+v = %q, want %q", tt.src, got, tt.want)
		}
	}
	if item.Meta != "" || item.Line != 11 {
		t.Errorf("MetaSource changed its argument: %+v", item)
	}
}

// metaOf returns the item whose ID is id, and its first postmeta that has key.
func metaOf(t *testing.T, e *wxr.Export, id int64, key string) (*wxr.Item, wxr.Meta) {
	t.Helper()
	it := e.Post(id)
	if it == nil {
		t.Fatalf("no item %d", id)
	}
	for _, m := range it.Meta {
		if m.Key == key {
			return it, m
		}
	}
	t.Fatalf("item %d has no postmeta %q", id, key)
	return nil, wxr.Meta{}
}

func TestUnserialize(t *testing.T) {
	e, _ := readSample(t, "wordpress-6.xml")
	rep := NewReport(Info{})
	read := func(id int64, key string) phpserialize.Value {
		t.Helper()
		it, m := metaOf(t, e, id, key)
		v, err := Unserialize(rep, ItemSource("wordpress-6.xml", it), m)
		if err != nil {
			t.Fatalf("Unserialize(%s of %d) = %v", key, id, err)
		}
		return v
	}

	v := read(14, "_wp_attachment_metadata")
	if got, ok := v.Lookup("original_image"); v.Kind != phpserialize.Array || !ok || got.Str != "photo.jpg" {
		t.Errorf("_wp_attachment_metadata = %+v; want an array with original_image photo.jpg", v)
	}
	if sizes, _ := v.Lookup("sizes"); len(sizes.Array) != 2 {
		t.Errorf("sizes = %+v, want 2 sizes", sizes)
	}

	v = read(17, "_mail")
	if got, ok := v.Lookup("recipient"); v.Kind != phpserialize.Array || !ok || got.Str != "[_site_admin_email]" {
		t.Errorf("_mail = %+v; want an array with recipient [_site_admin_email]", v)
	}
	if body, _ := v.Lookup("body"); !strings.Contains(body.Str, "\n[your-message]\n") {
		t.Errorf("body = %q, want the message with its line breaks", body.Str)
	}

	v = read(15, "_menu_item_classes")
	if v.Kind != phpserialize.Array || len(v.Array) != 1 || v.Array[0].Key.Int != 0 || v.Array[0].Value.Kind != phpserialize.String || v.Array[0].Value.Str != "" {
		t.Errorf("_menu_item_classes = %+v, want a list of one empty string", v)
	}

	for _, tt := range []struct {
		id   int64
		key  string
		want string
	}{
		{14, "_wp_attached_file", "2026/10/photo-scaled.jpg"},
		{10, "_thumbnail_id", "14"},
		{10, "_edit_last", "2"},
		{17, "_additional_settings", ""},
		{17, "_hash", "d3b2c4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1"},
		{15, "_menu_item_menu_item_parent", "0"},
	} {
		got := read(tt.id, tt.key)
		if got.Kind != phpserialize.String || got.Str != tt.want {
			t.Errorf("%s of %d = %+v, want the string %q", tt.key, tt.id, got, tt.want)
		}
	}
	form := read(17, "_form")
	if form.Kind != phpserialize.String || !strings.Contains(form.Str, "[text* your-name]") {
		t.Errorf("_form = %+v, want the text as written", form)
	}
	if got := rep.Entries(); len(got) != 0 {
		t.Errorf("the report has %+v after reading values that are strict, want nothing", got)
	}
}

func TestUnserializeSpaceAndNesting(t *testing.T) {
	src := Source{File: "a.xml", Record: "item", ID: 1}
	rep := NewReport(Info{})
	str := func(s string) phpserialize.Value { return phpserialize.Value{Kind: phpserialize.String, Str: s} }
	tests := []struct {
		name  string
		value string
		want  phpserialize.Value
	}{
		{"not serialized", "  hello\n", str("  hello\n")},
		{"nothing", "", str("")},
		{"white space only", " \n", str(" \n")},
		{"an integer as text", "14", str("14")},
		{"null with space around", " \t\r\n\v\x00N;\x00\n", phpserialize.Value{Kind: phpserialize.Null}},
		{"an array with space around", "\n\t a:1:{i:0;s:1:\"x\";}\r\n", phpserialize.Value{Kind: phpserialize.Array, Array: []phpserialize.Entry{
			{Key: phpserialize.Value{Kind: phpserialize.Int}, Value: str("x")},
		}}},
		{"a serialized value in a string", `s:12:"a:1:{i:0;N;}";`, str("a:1:{i:0;N;}")},
		{"a serialized value in a serialized string", `s:20:"s:12:"a:1:{i:0;N;}";";`, str(`s:12:"a:1:{i:0;N;}";`)},
		{"form feed is not space", "\fN;", str("\fN;")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Unserialize(rep, src, wxr.Meta{Key: "k", Value: tt.value})
			if err != nil {
				t.Fatal(err)
			}
			if fmt.Sprintf("%+v", got) != fmt.Sprintf("%+v", tt.want) {
				t.Errorf("Unserialize(%q) = %+v, want %+v", tt.value, got, tt.want)
			}
		})
	}
	if got := rep.Entries(); len(got) != 0 {
		t.Errorf("the report has %+v, want nothing", got)
	}
}

// phpString writes s as PHP does, with the length of its bytes.
func phpString(s string) string {
	return fmt.Sprintf("s:%d:\"%s\";", len(s), s)
}

// crlfMail is a mail as PHP serializes it when the body, and the footer if it has one, have CRLF, as a text box in a
// browser gives. PHP counts the CR in the lengths, and an XML parser removes it from the CDATA.
func crlfMail(withFooter bool) string {
	entries := phpString("recipient") + phpString("[_site_admin_email]") + phpString("body") + phpString("line 1\r\nline 2\r\nline 3")
	n := 2
	if withFooter {
		entries += phpString("footer") + phpString("a\r\nb")
		n++
	}
	return fmt.Sprintf("a:%d:{%s%sb:1;}", n+1, entries, phpString("active"))
}

func TestUnserializeCRLF(t *testing.T) {
	for _, tt := range []struct {
		name       string
		withFooter bool
		count      string // the number of strings in the message
		footer     string
	}{
		{"one string", false, "1 string", ""},
		{"two strings", true, "2 strings", "a\nb"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			in := head + "<link>https://example.com</link>\n" +
				"<item>\n<title>Contact</title>\n<wp:post_id>17</wp:post_id>\n<wp:post_type>wpcf7_contact_form</wp:post_type>\n" +
				"<wp:postmeta>\n<wp:meta_key>_mail</wp:meta_key>\n<wp:meta_value><![CDATA[" + crlfMail(tt.withFooter) + "]]></wp:meta_value>\n</wp:postmeta>\n</item>\n" + tail
			if !strings.Contains(in, "\r\n") {
				t.Fatal("the WXR has no CRLF")
			}
			e, _ := readString(t, "site.xml", in)
			it, m := metaOf(t, e, 17, "_mail")
			if strings.Contains(m.Value, "\r") {
				t.Fatalf("the value has a CR: %q", m.Value)
			}
			if _, err := phpserialize.Unserialize(m.Value); err == nil {
				t.Fatalf("Unserialize(%q) succeeded, want the lengths to be wrong", m.Value)
			}

			rep := NewReport(Info{})
			v, err := Unserialize(rep, ItemSource("site.xml", it), m)
			if err != nil {
				t.Fatalf("Unserialize = %v", err)
			}
			if got, _ := v.Lookup("recipient"); got.Str != "[_site_admin_email]" {
				t.Errorf("recipient = %q", got.Str)
			}
			if got, _ := v.Lookup("body"); got.Str != "line 1\nline 2\nline 3" {
				t.Errorf("body = %q, want the lines with LF", got.Str)
			}
			if got, _ := v.Lookup("footer"); got.Str != tt.footer {
				t.Errorf("footer = %q, want %q", got.Str, tt.footer)
			}
			line, column := place(t, in, "<wp:postmeta>", 1)
			want := Entry{
				Kind:   Changed,
				Source: Source{File: "site.xml", Line: line, Column: column, Record: "item", ID: 17, Type: "wpcf7_contact_form", Name: "Contact", Meta: "_mail"},
				Message: "read the serialized value by counting the LF line breaks in " + tt.count +
					" as CRLF (XML turns CRLF into LF); the line breaks are kept as LF",
			}
			if got := rep.Entries(); len(got) != 1 || got[0] != want {
				t.Errorf("Entries = %+v, want [%+v]", got, want)
			}

			// A nil report only means that nothing is recorded.
			v, err = Unserialize(nil, ItemSource("site.xml", it), m)
			if err != nil {
				t.Fatalf("Unserialize(nil) = %v", err)
			}
			if got, _ := v.Lookup("body"); got.Str != "line 1\nline 2\nline 3" {
				t.Errorf("body without a report = %q", got.Str)
			}
		})
	}
}

func TestUnserializeErrors(t *testing.T) {
	nested := func(n int) string { return strings.Repeat("a:1:{i:0;", n) + "N;" + strings.Repeat("}", n) }
	long := phpString(strings.Repeat("a", phpserialize.MaxBytes))
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{"33 levels", nested(33), `a.xml:3:5: the value of "k": byte 288: arrays are nested too deeply: more than 32 levels`},
		{"33 levels with space before", "  \n" + nested(33), `a.xml:3:5: the value of "k": byte 291: arrays are nested too deeply: more than 32 levels`},
		{"33 levels with space after", nested(33) + " \n", `a.xml:3:5: the value of "k": byte 288: arrays are nested too deeply: more than 32 levels`},
		{"a length that is too long", `s:9:"abc";`, `a.xml:3:5: the value of "k": byte 2: string is longer than the rest of the input`},
		{"a length that is too short", `s:2:"abc";`, `a.xml:3:5: the value of "k": byte 7: expected '"', found 'c'`},
		{"a missing value", `a:1:{s:1:"x";}`, `a.xml:3:5: the value of "k": byte 13: unexpected '}'`},
		{"an object", `O:8:"stdClass":0:{}`, `a.xml:3:5: the value of "k": byte 0: objects are not supported`},
		{"a value that is too long", long, fmt.Sprintf(`a.xml:3:5: the value of "k": byte 1048576: too long: %d bytes (max 1048576)`, len(long))},
		// The length 6 is that of the bytes a, CR, LF, b, LF, c, and the CR is gone, so that no length ends the string.
		{"CRLF and LF in a string", "a:1:{s:1:\"k\";s:6:\"a\nb\nc\";}", `a.xml:3:5: the value of "k": byte 24: expected '"', found ';'`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rep := NewReport(Info{})
			m := wxr.Meta{Pos: wxr.Pos{Line: 3, Column: 5}, Key: "k", Value: tt.value}
			v, err := Unserialize(rep, Source{File: "a.xml", Record: "item", ID: 1}, m)
			var ce *content.Error
			if !errors.As(err, &ce) {
				t.Fatalf("Unserialize = %+v, %v; want a *content.Error", v, err)
			}
			if err.Error() != tt.want {
				t.Errorf("Unserialize error = %q, want %q", err.Error(), tt.want)
			}
			if ce.File != "a.xml" || ce.Line != 3 || ce.Column != 5 || ce.Path != "" {
				t.Errorf("Unserialize error = %+v, want a.xml 3:5 with no path", *ce)
			}
			if v.Kind != phpserialize.Null || len(rep.Entries()) != 0 {
				t.Errorf("Unserialize = %+v with an error and %d entries, want the zero Value and none", v, len(rep.Entries()))
			}
		})
	}
}

// TestUnserializeBytePosition checks that the byte of the error counts from the start of the value as it is written,
// also after the space that is trimmed from its start, by finding the byte where the strict reader stops.
func TestUnserializeBytePosition(t *testing.T) {
	for _, lead := range []string{"", " ", "\n\n", " \t\r\n\v\x00"} {
		in := lead + `a:2:{s:1:"a";i:1;s:1:"b";X}` + "\n"
		_, strict := phpserialize.Unserialize(strings.Trim(in, phpSpace))
		var pe *phpserialize.Error
		if !errors.As(strict, &pe) {
			t.Fatal(strict)
		}
		_, err := Unserialize(nil, Source{File: "a.xml"}, wxr.Meta{Pos: wxr.Pos{Line: 1, Column: 1}, Key: "k", Value: in})
		want := fmt.Sprintf(`a.xml:1:1: the value of "k": byte %d: %s`, len(lead)+pe.Offset, pe.Msg)
		if err == nil || err.Error() != want {
			t.Errorf("Unserialize(%q) = %v, want %s", in, err, want)
		}
		if in[len(lead)+pe.Offset] != 'X' {
			t.Errorf("the byte %d of %q is %q, want X", len(lead)+pe.Offset, in, in[len(lead)+pe.Offset])
		}
	}
}
