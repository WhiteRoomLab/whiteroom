package edit

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

// predictIDs returns the IDs that a new Editor of the tests makes for the kinds, in order, which are those that the
// Editor of newSample makes for its first operation.
func predictIDs(t *testing.T, kinds ...content.RefKind) []string {
	t.Helper()
	e := testEditor(t, nil)
	ids := make([]string, len(kinds))
	for i, kind := range kinds {
		ids[i] = mustID(t, e, kind)
	}
	return ids
}

// at returns the line and the column, from 1, of the first occurrence of needle in text.
func at(t *testing.T, text, needle string) (line, col int) {
	t.Helper()
	i := strings.Index(text, needle)
	if i < 0 {
		t.Fatalf("%q does not occur in %q", needle, text)
	}
	start := strings.LastIndex(text[:i], "\n") + 1
	return 1 + strings.Count(text[:i], "\n"), 1 + utf8.RuneCountInString(text[start:i])
}

// where returns the start of the line of a problem about the file name, `name:line:col: `, at the first needle in text.
func where(t *testing.T, name, text, needle string) string {
	t.Helper()
	line, col := at(t, text, needle)
	return fmt.Sprintf("%s:%d:%d: ", name, line, col)
}

// The JSON of documents without an id, as the admin screen sends them to Create, in the layout of the files of the sample
// site.
func termJSON(taxonomy, language, group, name, slug string) string {
	s := fmt.Sprintf("{\n  \"schemaVersion\": 1,\n  \"taxonomy\": %q,\n  \"language\": %q,\n", taxonomy, language)
	if group != "" {
		s += fmt.Sprintf("  \"translationGroup\": %q,\n", group)
	}
	return s + fmt.Sprintf("  \"name\": %q,\n  \"slug\": %q\n}", name, slug)
}

func redirectJSON(from, to string) string {
	return fmt.Sprintf("{\n  \"schemaVersion\": 1,\n  \"from\": %q,\n  \"to\": %q,\n  \"status\": 301\n}", from, to)
}

func authorJSON(name, slug string) string {
	return fmt.Sprintf("{\n  \"schemaVersion\": 1,\n  \"name\": %q,\n  \"slug\": %q,\n  \"links\": [\"https://example.com/%s\"]\n}",
		name, slug, slug)
}

func menuJSON(location, language, items string) string {
	return fmt.Sprintf("{\n  \"schemaVersion\": 1,\n  \"title\": \"メニュー\",\n  \"location\": %q,\n  \"language\": %q,\n  \"items\": %s\n}",
		location, language, items)
}

const formJSON = `{
  "schemaVersion": 1,
  "title": "資料請求",
  "fields": [
    { "name": "your-email", "type": "email", "label": { "en": "Email", "ja": "メール" }, "required": true }
  ],
  "submitLabel": { "en": "Send", "ja": "送信" },
  "message": { "en": "Thank you.", "ja": "ありがとうございます。" },
  "notify": ["info@example.com"]
}`

// The term news of the sample site.
const (
	newsID   = "trm_01JBA4C7E2G5J8K1M4P7R2T5W8"
	newsName = "terms/" + newsID + ".json"
)

// TestCreate creates a document of each kind, and checks the document that the store has and its summary.
func TestCreate(t *testing.T) {
	for _, tt := range []struct {
		kind content.RefKind
		data string
	}{
		{content.RefTerm, termJSON("category", "ja", testID(content.RefTranslationGroup, 1), "ニュースレター", "newsletter")},
		{content.RefAuthor, authorJSON("佐藤 花子", "sato")},
		{content.RefMenu, menuJSON("sidebar", "ja", `[{ "label": "ホーム", "href": "/" }]`)},
		{content.RefForm, formJSON},
		{content.RefRedirect, redirectJSON("/old-contact/", "/contact/")},
	} {
		t.Run(string(tt.kind), func(t *testing.T) {
			ed, s := newSample(t)
			id := predictIDs(t, tt.kind)[0]
			res, err := ed.Create(t.Context(), Op{By: userID}, tt.kind, []byte(tt.data))
			if err != nil {
				t.Fatalf("Create() error = %v", err)
			}
			if res.Kind != tt.kind || res.ID != id || len(res.Warnings) != 0 {
				t.Errorf("Create() = %+v, want the %s %s without warnings", res, tt.kind, id)
			}
			doc := read(t, s, tt.kind, id)
			if doc.Version != res.Version || res.Version.SavedBy != userID || res.Version.Kind != tt.kind ||
				res.Version.ID != id || res.Version.Hash == "" {
				t.Errorf("Document() = %+v, Create() returned the version %+v", doc.Version, res.Version)
			}
			// The stored JSON is that of the document with its ID, as MarshalJSON writes it.
			d := docKinds[tt.kind]()
			if err := d.unmarshal(doc.Data); err != nil {
				t.Fatal(err)
			}
			if got := *d.id(); got != id {
				t.Errorf("the id in the stored document = %q, want %q", got, id)
			}
			if again, err := d.marshal(); err != nil || !bytes.Equal(again, doc.Data) {
				t.Errorf("the stored bytes are %s, MarshalJSON writes %s, %v", doc.Data, again, err)
			}
			err = s.View(t.Context(), func(tx *store.Tx) error {
				sum, err := tx.Summary(tt.kind, id)
				if err != nil || sum.ID != id || sum.Kind != tt.kind {
					t.Errorf("Summary() = %+v, %v; want the summary of %s", sum, err, id)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestCreateReferences checks that what a new document refers to is looked up in the store, in every status.
func TestCreateReferences(t *testing.T) {
	ed, _ := newSample(t)
	const draft, published = "ent_01JC0000000000000000000003", "ent_01JBG3N6S1V4X7Z0C3E6G9J2K5"
	items := fmt.Sprintf(`[{ "entry": %q, "label": "a" }, { "entry": %q, "label": "b" }, { "term": %q }]`, draft, published, newsID)
	must(t)(ed.Create(t.Context(), Op{By: userID}, content.RefMenu, []byte(menuJSON("sidebar", "ja", items))))
}

// TestCreateWarnings checks that a warning that a change brings is in the result, located in the bytes that were given,
// and that the warnings that the site had before are not: the sample site has a redirect to the same draft.
func TestCreateWarnings(t *testing.T) {
	ed, s := newSample(t)
	const draft = "ent_01JC0000000000000000000003"
	data := redirectJSON("/draft-page/", draft)
	name := docName(content.RefRedirect, predictIDs(t, content.RefRedirect)[0])
	res, err := ed.Create(t.Context(), Op{By: userID}, content.RefRedirect, []byte(data))
	if err != nil {
		t.Fatal(err)
	}
	want := where(t, name, data, `"to"`) + `/to: entry "` + draft + `" is not published; the redirect is left out`
	if len(res.Warnings) != 1 || res.Warnings[0].Error() != want {
		t.Errorf("Create() warnings = %v, want the one warning %q", res.Warnings, want)
	}
	if read(t, s, content.RefRedirect, res.ID).Seq != res.Version.Seq {
		t.Error("Create() with a warning has not saved the redirect")
	}
}

// TestCreateTermGroup checks the translation group of a term that is created without one, and of one that has its own.
func TestCreateTermGroup(t *testing.T) {
	ed, s := newSample(t)
	ids := predictIDs(t, content.RefTerm, content.RefTranslationGroup)
	res := must(t)(ed.Create(t.Context(), Op{By: userID}, content.RefTerm, []byte(termJSON("category", "ja", "", "ニュースレター", "newsletter"))))
	var term content.Term
	if err := term.UnmarshalJSON(read(t, s, content.RefTerm, res.ID).Data); err != nil {
		t.Fatal(err)
	}
	if res.ID != ids[0] || term.TranslationGroup != ids[1] || !strings.HasPrefix(term.TranslationGroup, "tg_") {
		t.Errorf("Create() = %q with the group %q, want %q with the group %q", res.ID, term.TranslationGroup, ids[0], ids[1])
	}

	// A group that the data name is kept, which here has a tag in ja already.
	const group = "tg_01JBA4C7M8P2S5V9Y2B6D9G3J6"
	data := termJSON("tag", "ja", group, "お祭り", "festival")
	_, err := ed.Create(t.Context(), Op{By: userID}, content.RefTerm, []byte(data))
	lines := problemLines(t, err)
	wantLine := `: /translationGroup: translation group "` + group + `" already has a term in the language "ja": terms/trm_01JBA4C7G4J7M1P4R7T1W4Y7B1.json`
	if len(lines) != 1 || !strings.HasSuffix(lines[0], wantLine) {
		t.Errorf("Create() into a group that has a term in ja = %q, want one problem that ends with %q", lines, wantLine)
	}
	// In a language that it has no term in, the group is kept.
	res = must(t)(ed.Create(t.Context(), Op{By: userID}, content.RefTerm, []byte(termJSON("tag", "en", testID(content.RefTranslationGroup, 2), "Festival", "festival"))))
	if err := term.UnmarshalJSON(read(t, s, content.RefTerm, res.ID).Data); err != nil || term.TranslationGroup != testID(content.RefTranslationGroup, 2) {
		t.Errorf("the stored group = %q, %v, want the group that was given", term.TranslationGroup, err)
	}
}

// TestCreateErrors checks what Create refuses, and that it saves nothing.
func TestCreateErrors(t *testing.T) {
	group := testID(content.RefTranslationGroup, 1)
	withID := replaceOnce(t, termJSON("category", "ja", group, "ニュースレター", "newsletter"), `  "taxonomy"`,
		fmt.Sprintf("  \"id\": %q,\n  \"taxonomy\"", newsID))
	badSlug := termJSON("category", "ja", group, "ニュースレター", "a b")
	badMenu := menuJSON("sidebar", "ja", fmt.Sprintf(`[{ "label": "ホーム", "entry": %q }]`, testID(content.RefEntry, 9)))
	valid := termJSON("category", "ja", group, "ニュースレター", "newsletter")
	user := Op{By: userID}

	// {name} is the name of the file of the new document.
	for _, tt := range []struct {
		name string
		op   Op
		kind content.RefKind
		data []byte
		want string
	}{
		{"an id", user, content.RefTerm, []byte(withID),
			where(t, "{name}", withID, `"id"`) + "/id: not allowed when creating; the store assigns the ID"},
		{"broken JSON", user, content.RefTerm, []byte(`{"schemaVersion": 1,`), "{name}:1:21: unexpected end of JSON input"},
		{"not JSON", user, content.RefTerm, []byte("{\n  \"schemaVersion\": 1,\n  oops"),
			"{name}:3:3: invalid character 'o' at start of value"},
		{"a validation problem", user, content.RefTerm, []byte(badSlug),
			where(t, "{name}", badSlug, `"slug"`) + `/slug: character ' ' is not allowed in slugs: ` +
				`use lower case letters, digits, "-" and "_" in ASCII`},
		{"an entry that does not exist", user, content.RefMenu, []byte(badMenu),
			where(t, "{name}", badMenu, `"entry"`) + fmt.Sprintf(`/items/0/entry: entry %q does not exist`, testID(content.RefEntry, 9))},
		{"a malformed user ID", Op{By: "usr_x"}, content.RefTerm, []byte(valid),
			`invalid user ID "usr_x": use "usr_" and a ULID (26 characters in upper case)`},
		{"no user ID", Op{}, content.RefTerm, []byte(valid),
			`invalid user ID "": use "usr_" and a ULID (26 characters in upper case)`},
		{"a malformed operation ID", Op{By: userID, ID: "op_x"}, content.RefTerm, []byte(valid),
			`invalid operation ID "op_x": use "op_" and a ULID (26 characters in upper case)`},
		{"64 MiB and one byte", user, content.RefTerm, bytes.Repeat([]byte(" "), content.MaxDocumentBytes+1),
			"{name}: too large: 67108865 bytes (max 67108864)"},
		{"a malformed user ID before the kind", Op{By: "usr_x"}, content.RefEntry, []byte(valid),
			`invalid user ID "usr_x": use "usr_" and a ULID (26 characters in upper case)`},
		{"an entry", user, content.RefEntry, []byte(valid),
			"entries change through CreateEntry, SaveRevision, CreateTranslation and Delete"},
		{"a medium", user, content.RefMedia, []byte(valid), "media are added with AddMedia"},
		{"the settings", user, store.SettingsKind, []byte(valid), "site/settings.json: it is saved with SaveSettings"},
		{"a kind that does not exist", user, "page", []byte(valid),
			`unsupported kind "page": Create handles terms, authors, menus, forms and redirects`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ed, s := newSample(t)
			before := snapshot(t, s)
			want := tt.want
			// Only the kinds that Create makes have IDs: content.FormatID panics for the others.
			if strings.Contains(want, "{name}") {
				want = strings.ReplaceAll(want, "{name}", docName(tt.kind, predictIDs(t, tt.kind)[0]))
			}
			res, err := ed.Create(t.Context(), tt.op, tt.kind, tt.data)
			if err == nil || err.Error() != want {
				t.Errorf("Create() = %+v, %v\nwant the error %q", res, err, want)
			}
			if got := snapshot(t, s); got != before {
				t.Errorf("Create() has changed the store:\n%s\nwant\n%s", got, before)
			}
		})
	}
}

// TestSave checks the versions that Save makes.
func TestSave(t *testing.T) {
	ed, s := newSample(t)
	op := Op{By: userID}
	orig := string(sampleFile(t, newsName))
	first := read(t, s, content.RefTerm, newsID)
	if string(first.Data) != orig {
		t.Fatal("the store has other bytes than the sample file")
	}

	// A change saves a version, with the bytes that were given.
	changed := replaceOnce(t, orig, "会社からのお知らせです。", "お知らせです。")
	res, err := ed.Save(t.Context(), op, content.RefTerm, newsID, first.Seq, []byte(changed))
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	cur := read(t, s, content.RefTerm, newsID)
	if res.Kind != content.RefTerm || res.ID != newsID || res.Version != cur.Version || cur.Seq <= first.Seq ||
		cur.SavedBy != userID || string(cur.Data) != changed || len(res.Warnings) != 0 {
		t.Errorf("Save() = %+v; the store has %+v with %q", res, cur.Version, cur.Data)
	}
	versions := func() int {
		var n int
		err := s.View(t.Context(), func(tx *store.Tx) error {
			h, err := tx.History(content.RefTerm, newsID)
			n = len(h)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := versions(); n != 2 {
		t.Errorf("the term has %d versions, want 2", n)
	}

	// The same bytes save nothing, and return the current version.
	before := snapshot(t, s)
	res, err = ed.Save(t.Context(), op, content.RefTerm, newsID, cur.Seq, []byte(changed))
	if err != nil || res.Version != cur.Version || len(res.Warnings) != 0 || snapshot(t, s) != before || versions() != 2 {
		t.Errorf("Save() of the same bytes = %+v, %v; want the current version %+v and no new version", res, err, cur.Version)
	}

	// A base that is not the current version fails with ErrConflict, and saves nothing.
	other := replaceOnce(t, changed, "お知らせです。", "ニュースです。")
	_, err = ed.Save(t.Context(), op, content.RefTerm, newsID, first.Seq, []byte(other))
	want := fmt.Sprintf("%s: changed since it was read: it is at version %d, not %d", newsName, cur.Seq, first.Seq)
	if !errors.Is(err, ErrConflict) || err.Error() != want || snapshot(t, s) != before {
		t.Errorf("Save() with an old base = %v, want the error %q that wraps ErrConflict, and no change", err, want)
	}

	// A document that does not exist.
	missing := testID(content.RefTerm, 9)
	_, err = ed.Save(t.Context(), op, content.RefTerm, missing, 1, []byte(changed))
	if !errors.Is(err, fs.ErrNotExist) || err.Error() != "terms/"+missing+".json: not found" {
		t.Errorf("Save() of a term that does not exist = %v, want terms/%s.json: not found", err, missing)
	}
}

// TestSaveKinds saves a document of each kind that Save handles, with a change that the document allows.
func TestSaveKinds(t *testing.T) {
	for _, tt := range []struct {
		kind     content.RefKind
		id       string
		old, new string
	}{
		{content.RefTerm, newsID, `"name": "お知らせ"`, `"name": "ニュース"`},
		{content.RefAuthor, "aut_01JBA4C7N9Q3T6W9Z3C6F9H2K5", `"name": "山田 太郎"`, `"name": "山田 次郎"`},
		{content.RefMenu, "mnu_01JBD2M5N6P7Q8R9S0T1V2W3X4", `"title": "フッター"`, `"title": "フッターのメニュー"`},
		{content.RefForm, "frm_01JB8Z3K5VP1D4G7J2M5Q8T3W6", `"title": "お問い合わせ"`, `"title": "お問い合わせフォーム"`},
		{content.RefRedirect, "rdr_01JC0000000000000000000001", `"status": 301`, `"status": 302`},
	} {
		t.Run(string(tt.kind), func(t *testing.T) {
			ed, s := newSample(t)
			doc := read(t, s, tt.kind, tt.id)
			data := replaceOnce(t, string(doc.Data), tt.old, tt.new)
			res, err := ed.Save(t.Context(), Op{By: userID}, tt.kind, tt.id, doc.Seq, []byte(data))
			if err != nil {
				t.Fatalf("Save() error = %v", err)
			}
			if got := read(t, s, tt.kind, tt.id); got.Seq != res.Version.Seq || got.Seq <= doc.Seq || string(got.Data) != data {
				t.Errorf("Save() = %+v; the store has %+v with %q, want the data that were given", res, got.Version, got.Data)
			}
		})
	}
}

// TestSaveNormalized checks Q14-B: the bytes that the store keeps are those given if normalizing changes nothing, and
// else those that MarshalJSON writes.
func TestSaveNormalized(t *testing.T) {
	ed, s := newSample(t)
	op := Op{By: userID}
	orig := string(sampleFile(t, newsName))
	seq := read(t, s, content.RefTerm, newsID).Seq

	// A slug in upper case is normalized, so what is stored is what MarshalJSON writes of the normalized term.
	upper := replaceOnce(t, orig, `"slug": "news"`, `"slug": "News"`)
	res := must(t)(ed.Save(t.Context(), op, content.RefTerm, newsID, seq, []byte(upper)))
	var term content.Term
	if err := term.UnmarshalJSON([]byte(upper)); err != nil {
		t.Fatal(err)
	}
	term.Normalize()
	want, err := term.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, s, content.RefTerm, newsID); string(got.Data) != string(want) || got.Seq != res.Version.Seq ||
		term.Slug != "news" {
		t.Errorf("Save() of a slug in upper case stored %q, want %q", got.Data, want)
	}

	// Bytes that normalizing does not change are stored as they are, whatever their layout.
	indented := strings.ReplaceAll(orig, "\n  ", "\n        ")
	if indented == orig {
		t.Fatal("the indentation did not change")
	}
	res = must(t)(ed.Save(t.Context(), op, content.RefTerm, newsID, res.Version.Seq, []byte(indented)))
	if got := read(t, s, content.RefTerm, newsID); string(got.Data) != indented || got.Seq != res.Version.Seq {
		t.Errorf("Save() of bytes that are normalized stored %q, want those given", got.Data)
	}
}

// TestSaveErrors checks what Save refuses, and that it saves nothing.
func TestSaveErrors(t *testing.T) {
	orig := string(sampleFile(t, newsName))
	badSlug := replaceOnce(t, orig, `"slug": "news"`, `"slug": "a b"`)
	noID := replaceOnce(t, orig, `  "id": "`+newsID+`",`+"\n", "")
	otherID := "trm_01JBA4C7F3H6K9N2Q5S8V1X4Z7"
	user := Op{By: userID}

	for _, tt := range []struct {
		name string
		call func(ed *Editor, seq int64) (Result, error)
		want string
	}{
		{"not JSON", func(ed *Editor, seq int64) (Result, error) {
			return ed.Save(t.Context(), user, content.RefTerm, newsID, seq, []byte("{\n  \"schemaVersion\": 1,\n  oops"))
		}, newsName + ":3:3: invalid character 'o' at start of value"},
		{"a validation problem", func(ed *Editor, seq int64) (Result, error) {
			return ed.Save(t.Context(), user, content.RefTerm, newsID, seq, []byte(badSlug))
		}, where(t, newsName, badSlug, `"slug"`) + `/slug: character ' ' is not allowed in slugs: ` +
			`use lower case letters, digits, "-" and "_" in ASCII`},
		{"a missing id", func(ed *Editor, seq int64) (Result, error) {
			return ed.Save(t.Context(), user, content.RefTerm, newsID, seq, []byte(noID))
		}, newsName + ":1:1: /id: missing id"},
		{"the id of another document", func(ed *Editor, seq int64) (Result, error) {
			return ed.Save(t.Context(), user, content.RefTerm, newsID, seq, []byte(strings.ReplaceAll(orig, newsID, otherID)))
		}, where(t, newsName, orig, `"id"`) + `/id: id "` + otherID + `" differs from the ID of the document, "` + newsID + `"`},
		{"64 MiB and one byte", func(ed *Editor, seq int64) (Result, error) {
			return ed.Save(t.Context(), user, content.RefTerm, newsID, seq, bytes.Repeat([]byte(" "), content.MaxDocumentBytes+1))
		}, newsName + ": too large: 67108865 bytes (max 67108864)"},
		{"a malformed ID", func(ed *Editor, seq int64) (Result, error) {
			return ed.Save(t.Context(), user, content.RefTerm, "trm_x", seq, []byte(orig))
		}, `invalid term ID "trm_x": use "trm_" and a ULID (26 characters in upper case)`},
		{"a malformed user ID", func(ed *Editor, seq int64) (Result, error) {
			return ed.Save(t.Context(), Op{By: "usr_x"}, content.RefTerm, newsID, seq, []byte(orig))
		}, `invalid user ID "usr_x": use "usr_" and a ULID (26 characters in upper case)`},
		{"an entry", func(ed *Editor, seq int64) (Result, error) {
			return ed.Save(t.Context(), user, content.RefEntry, "ent_01JB8W2N5KD3F6H9J1M4P7R2T5", 1, []byte(orig))
		}, "entries change through CreateEntry, SaveRevision, CreateTranslation and Delete"},
		{"the settings", func(ed *Editor, seq int64) (Result, error) {
			return ed.Save(t.Context(), user, store.SettingsKind, "", 1, []byte(orig))
		}, "site/settings.json: it is saved with SaveSettings"},
		{"a revision", func(ed *Editor, seq int64) (Result, error) {
			return ed.Save(t.Context(), user, content.RefRevision, aboutRev, 1, []byte(orig))
		}, `unsupported kind "revision": Save handles terms, media, authors, menus, forms and redirects`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ed, s := newSample(t)
			before := snapshot(t, s)
			res, err := tt.call(ed, read(t, s, content.RefTerm, newsID).Seq)
			if err == nil || err.Error() != tt.want {
				t.Errorf("Save() = %+v, %v\nwant the error %q", res, err, tt.want)
			}
			if got := snapshot(t, s); got != before {
				t.Errorf("Save() has changed the store:\n%s\nwant\n%s", got, before)
			}
		})
	}
}

// usedBy returns the files of the sample site that name id, apart from own, as Delete names the files that use a
// document: the documents, the published revisions (all of the sample's are) and the settings document.
func usedBy(t *testing.T, own, id string) []string {
	t.Helper()
	var files []string
	for _, dir := range []string{"authors", "entries", "forms", "media", "menus", "redirects", "revisions", "terms"} {
		list, err := os.ReadDir(filepath.Join(sampleDir, dir))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range list {
			name := dir + "/" + f.Name()
			if name != own && bytes.Contains(sampleFile(t, name), []byte(id)) {
				files = append(files, name)
			}
		}
	}
	if bytes.Contains(sampleFile(t, settingsName), []byte(id)) {
		files = append(files, settingsName)
	}
	slices.Sort(files)
	return files
}

// refusal returns the error of Delete of the document name that the files use.
func refusal(name string, files []string) string {
	list := strings.Join(files[:min(len(files), maxListed)], ", ")
	if len(files) > maxListed {
		list += fmt.Sprintf(" and %d more", len(files)-maxListed)
	}
	return name + ": cannot delete: used by " + list
}

// TestDelete deletes the documents of the sample site that nothing uses, and refuses those that published content
// uses, naming the files, as many as the error names.
func TestDelete(t *testing.T) {
	var named, left int
	for _, tt := range []struct {
		kind content.RefKind
		id   string
	}{
		{content.RefRedirect, "rdr_01JBD3A4B5C6D7E8F9G0H1J2K3"},
		{content.RefRedirect, "rdr_01JC0000000000000000000003"},
		{content.RefMenu, "mnu_01JBD2M5N6P7Q8R9S0T1V2W3X4"},
		{content.RefTerm, newsID},
		{content.RefTerm, "trm_01JBA4C7F3H6K9N2Q5S8V1X4Z7"},
		{content.RefTerm, "trm_01JBA4C7G4J7M1P4R7T1W4Y7B1"},
		{content.RefTerm, "trm_01JBA4C7H5K8N3Q6S9V3X6Z9C2"},
		{content.RefTerm, "trm_01JB9A00000000000000000001"},
		{content.RefAuthor, "aut_01JBA4C7N9Q3T6W9Z3C6F9H2K5"},
		{content.RefForm, "frm_01JB8Z3K5VP1D4G7J2M5Q8T3W6"},
	} {
		name := docName(tt.kind, tt.id)
		t.Run(name, func(t *testing.T) {
			ed, s := newSample(t)
			doc := read(t, s, tt.kind, tt.id)
			files := usedBy(t, name, tt.id)
			before := snapshot(t, s)
			res, err := ed.Delete(t.Context(), Op{By: userID}, tt.kind, tt.id, doc.Seq)
			if len(files) > 0 {
				if want := refusal(name, files); err == nil || err.Error() != want || snapshot(t, s) != before {
					t.Errorf("Delete() = %+v, %v\nwant the error %q and no change", res, err, want)
				}
				if len(files) > maxListed {
					left++
				} else {
					named++
				}
				return
			}
			if err != nil {
				t.Fatalf("Delete() error = %v", err)
			}
			if res.Kind != tt.kind || res.ID != tt.id || res.Version.Hash != "" || res.Version.Seq <= doc.Seq ||
				res.Version.SavedBy != userID || len(res.Warnings) != 0 {
				t.Errorf("Delete() = %+v, want the version that marks the deletion", res)
			}
			err = s.View(t.Context(), func(tx *store.Tx) error {
				if d, err := tx.Document(tt.kind, tt.id); !errors.Is(err, fs.ErrNotExist) {
					t.Errorf("Document() after Delete() = %+v, %v; want fs.ErrNotExist", d, err)
				}
				h, err := tx.History(tt.kind, tt.id)
				if err != nil || len(h) != 2 || h[1] != res.Version {
					t.Errorf("History() = %+v, %v; want the deletion as the last version", h, err)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
	if named == 0 || left == 0 {
		t.Errorf("%d refusals name all their files and %d leave some out, want some of each", named, left)
	}
}

// TestDeleteErrors checks the other errors of Delete.
func TestDeleteErrors(t *testing.T) {
	const id = "rdr_01JBD3A4B5C6D7E8F9G0H1J2K3"
	user := Op{By: userID}
	for _, tt := range []struct {
		name string
		call func(ed *Editor, seq int64) (Result, error)
		want func(seq int64) string
		is   error
	}{
		{"a stale base", func(ed *Editor, seq int64) (Result, error) {
			return ed.Delete(t.Context(), user, content.RefRedirect, id, seq+1)
		}, func(seq int64) string {
			return fmt.Sprintf("redirects/%s.json: changed since it was read: it is at version %d, not %d", id, seq, seq+1)
		}, ErrConflict},
		{"a redirect that does not exist", func(ed *Editor, seq int64) (Result, error) {
			return ed.Delete(t.Context(), user, content.RefRedirect, testID(content.RefRedirect, 9), 1)
		}, func(int64) string { return "redirects/" + testID(content.RefRedirect, 9) + ".json: not found" }, fs.ErrNotExist},
		{"the settings document", func(ed *Editor, seq int64) (Result, error) {
			return ed.Delete(t.Context(), user, store.SettingsKind, "", 1)
		}, func(int64) string { return "site/settings.json: cannot be deleted" }, nil},
		{"a malformed ID", func(ed *Editor, seq int64) (Result, error) {
			return ed.Delete(t.Context(), user, content.RefRedirect, "rdr_x", 1)
		}, func(int64) string {
			return `invalid redirect ID "rdr_x": use "rdr_" and a ULID (26 characters in upper case)`
		}, nil},
		{"a malformed user ID", func(ed *Editor, seq int64) (Result, error) {
			return ed.Delete(t.Context(), Op{By: "x"}, content.RefRedirect, id, seq)
		}, func(int64) string { return `invalid user ID "x": use "usr_" and a ULID (26 characters in upper case)` }, nil},
		{"a revision", func(ed *Editor, seq int64) (Result, error) {
			return ed.Delete(t.Context(), user, content.RefRevision, aboutRev, 1)
		}, func(int64) string {
			return `unsupported kind "revision": Delete handles entries, terms, media, authors, menus, forms and redirects`
		}, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ed, s := newSample(t)
			seq := read(t, s, content.RefRedirect, id).Seq
			before := snapshot(t, s)
			_, err := tt.call(ed, seq)
			if want := tt.want(seq); err == nil || err.Error() != want || tt.is != nil && !errors.Is(err, tt.is) {
				t.Errorf("Delete() error = %v, want %q wrapping %v", err, want, tt.is)
			}
			if got := snapshot(t, s); got != before {
				t.Errorf("Delete() has changed the store:\n%s\nwant\n%s", got, before)
			}
		})
	}
}

// TestDeleteDrafts deletes a document that only drafts use, with a warning for each, and refuses one that the published
// revision of an entry uses, which is not its last.
func TestDeleteDrafts(t *testing.T) {
	ed, s := newSample(t)
	op := Op{By: userID}
	const (
		draftEntry     = "ent_01JC0000000000000000000003" // a draft page without revisions
		publishedEntry = "ent_01JBD1Y2Z3A4B5C6D7E8F9G0H1" // the contact page
	)
	usingTag := func(tag string) []byte {
		return []byte(`{"schemaVersion": 1, "title": "Draft", "slug": "draft", "terms": {"tag": ["` + tag + `"]}}`)
	}
	newTag := func(slug string) string {
		return must(t)(ed.Create(t.Context(), op, content.RefTerm, []byte(termJSON("tag", "ja", "", slug, slug)))).ID
	}
	addRevision := func(entry, rev string, data []byte) {
		t.Helper()
		err := s.Update(t.Context(), func(tx *store.Tx) error {
			_, err := tx.AddRevision(entry, rev, data, userID)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	// The last revision of a draft, and a revision of a published entry that is newer than the one that it publishes.
	tag := newTag("one")
	draftRev, newerRev := testID(content.RefRevision, 1), testID(content.RefRevision, 2)
	addRevision(draftEntry, draftRev, usingTag(tag))
	addRevision(publishedEntry, newerRev, usingTag(tag))
	res, err := ed.Delete(t.Context(), op, content.RefTerm, tag, read(t, s, content.RefTerm, tag).Seq)
	if err != nil {
		t.Fatalf("Delete() of a term that only drafts use: %v", err)
	}
	name := docName(content.RefTerm, tag)
	want := []string{
		name + ": deleted although used by " + revisionName(draftRev) + ", the latest revision of " +
			docName(content.RefEntry, draftEntry) + ", which is not published",
		name + ": deleted although used by " + revisionName(newerRev) + ", the latest revision of " +
			docName(content.RefEntry, publishedEntry) + ", which is not published",
	}
	var got []string
	for _, w := range res.Warnings {
		got = append(got, w.Error())
	}
	if !slices.Equal(got, want) {
		t.Errorf("Delete() warnings:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	err = s.View(t.Context(), func(tx *store.Tx) error {
		revs, err := tx.Revisions(draftEntry)
		if err != nil || len(revs) != 1 || revs[0].ID != draftRev {
			t.Errorf("Revisions(%s) = %+v, %v; want %s, which stays", draftEntry, revs, err, draftRev)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// An entry that publishes a revision with the term, and has a later revision without it, is refused: the term is
	// used by the published revision.
	tag = newTag("two")
	const entry = "ent_01JBZ0000000000000000000E1"
	pubRev, laterRev := testID(content.RefRevision, 3), testID(content.RefRevision, 4)
	put(t, s, content.RefEntry, entry, `{"schemaVersion": 1, "id": "`+entry+`", "number": 99, "type": "post", "language": "ja", `+
		`"translationGroup": "`+testID(content.RefTranslationGroup, 3)+`", "status": "draft", `+
		`"createdAt": "2026-10-25T10:00:00+09:00", "createdBy": "`+userID+`"}`)
	addRevision(entry, pubRev, usingTag(tag))
	put(t, s, content.RefEntry, entry, `{"schemaVersion": 1, "id": "`+entry+`", "number": 99, "type": "post", "language": "ja", `+
		`"translationGroup": "`+testID(content.RefTranslationGroup, 3)+`", "status": "published", "publishedRevision": "`+pubRev+`", `+
		`"createdAt": "2026-10-25T10:00:00+09:00", "createdBy": "`+userID+`"}`)
	addRevision(entry, laterRev, []byte(`{"schemaVersion": 1, "title": "Later", "slug": "later"}`))
	_, err = ed.Delete(t.Context(), op, content.RefTerm, tag, read(t, s, content.RefTerm, tag).Seq)
	wantErr := docName(content.RefTerm, tag) + ": cannot delete: used by " + revisionName(pubRev)
	if err == nil || err.Error() != wantErr {
		t.Errorf("Delete() of a term that a published revision uses = %v, want the error %q", err, wantErr)
	}
}

// TestDeleteUsedBySettings checks that the settings document, which refers to documents by their IDs as any other
// document does, is named as what uses a document.
func TestDeleteUsedBySettings(t *testing.T) {
	ed, s := newSample(t)
	op := Op{By: userID}
	tag := must(t)(ed.Create(t.Context(), op, content.RefTerm, []byte(termJSON("tag", "ja", "", "お祭り", "festival")))).ID
	// A text is a reference when it is the ID of an entity, as the index counts them.
	settings := replaceOnce(t, string(sampleFile(t, settingsName)), "Webサイトの企画と制作", tag)
	must(t)(ed.SaveSettings(t.Context(), op, read(t, s, store.SettingsKind, "").Seq, []byte(settings)))
	_, err := ed.Delete(t.Context(), op, content.RefTerm, tag, read(t, s, content.RefTerm, tag).Seq)
	if want := docName(content.RefTerm, tag) + ": cannot delete: used by site/settings.json"; err == nil || err.Error() != want {
		t.Errorf("Delete() of a term that the settings name = %v, want the error %q", err, want)
	}
}

// TestSaveSettings checks the versions that SaveSettings makes.
func TestSaveSettings(t *testing.T) {
	ed, s := newSample(t)
	op := Op{By: userID}
	orig := string(sampleFile(t, settingsName))
	first := read(t, s, store.SettingsKind, "")
	if string(first.Data) != orig {
		t.Fatal("the store has other bytes than the sample file")
	}

	// A change of the tagline.
	changed := replaceOnce(t, orig, "Webサイトの企画と制作", "Webサイトの企画・制作・運用")
	res, err := ed.SaveSettings(t.Context(), op, first.Seq, []byte(changed))
	if err != nil {
		t.Fatalf("SaveSettings() error = %v", err)
	}
	cur := read(t, s, store.SettingsKind, "")
	if res.Kind != store.SettingsKind || res.ID != "" || res.Version != cur.Version || cur.Seq <= first.Seq ||
		cur.SavedBy != userID || string(cur.Data) != changed || len(res.Warnings) != 0 {
		t.Errorf("SaveSettings() = %+v; the store has %+v with %q", res, cur.Version, cur.Data)
	}

	// The same bytes save nothing.
	before := snapshot(t, s)
	res, err = ed.SaveSettings(t.Context(), op, cur.Seq, []byte(changed))
	if err != nil || res.Version != cur.Version || snapshot(t, s) != before {
		t.Errorf("SaveSettings() of the same bytes = %+v, %v; want the current version %+v and no change", res, err, cur.Version)
	}

	// A base that is not the current version, also 0 when there are settings.
	for _, base := range []int64{first.Seq, 0} {
		_, err = ed.SaveSettings(t.Context(), op, base, []byte(changed+"\n"))
		want := fmt.Sprintf("site/settings.json: changed since it was read: it is at version %d, not %d", cur.Seq, base)
		if !errors.Is(err, ErrConflict) || err.Error() != want || snapshot(t, s) != before {
			t.Errorf("SaveSettings() with the base %d = %v, want the error %q that wraps ErrConflict, and no change", base, err, want)
		}
	}

	// A URL is normalized, so what is stored is what MarshalSettings writes of the normalized settings (Q14-B).
	upper := replaceOnce(t, changed, `"https://example.com"`, `"HTTPS://Example.COM"`)
	res = must(t)(ed.SaveSettings(t.Context(), op, cur.Seq, []byte(upper)))
	var site content.Site
	if err := site.UnmarshalSettings([]byte(upper)); err != nil {
		t.Fatal(err)
	}
	site.NormalizeSettings()
	want, err := site.MarshalSettings()
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, s, store.SettingsKind, ""); string(got.Data) != string(want) || got.Seq != res.Version.Seq || site.URL != "https://example.com" {
		t.Errorf("SaveSettings() of a URL in upper case stored %q, want %q", got.Data, want)
	}
}

// TestSaveSettingsErrors checks what SaveSettings refuses, and that it saves nothing. A problem is located in the bytes
// that were given, also when normalizing has changed them.
func TestSaveSettingsErrors(t *testing.T) {
	orig := string(sampleFile(t, settingsName))
	user := Op{By: userID}
	frontPage := replaceOnce(t, orig, `"frontPage": { "ja": "ent_01JBG3N6S1V4X7Z0C3E6G9J2K5" }`,
		`"frontPage": { "en": "ent_01JC0000000000000000000003", "ja": "ent_01JBG3N6S1V4X7Z0C3E6G9J2K5" }`)
	noZone := replaceOnce(t, orig, `"Asia/Tokyo"`, `"Nowhere/City"`)
	noZoneUpper := replaceOnce(t, noZone, `"https://example.com"`, `"HTTPS://EXAMPLE.COM"`)
	noLogo := replaceOnce(t, orig, `"med_01JBG3N6V3X6Z9C2E5G8J1K4M7"`, `"med_01JBG3N6V3X6Z9C2E5G8J1K4M9"`)

	for _, tt := range []struct {
		name string
		op   Op
		data []byte
		want string
	}{
		{"a front page that is a draft", user, []byte(frontPage),
			where(t, settingsName, frontPage, `"en": "ent_01JC0000000000000000000003"`) +
				`/frontPage/en: entry "ent_01JC0000000000000000000003" is not published`},
		{"an unknown time zone", user, []byte(noZone),
			where(t, settingsName, noZone, `"timeZone"`) +
				`/timeZone: unknown time zone "Nowhere/City": use an IANA name such as "Asia/Tokyo" or "UTC"`},
		{"an unknown time zone, and a URL that is normalized", user, []byte(noZoneUpper),
			where(t, settingsName, noZoneUpper, `"timeZone"`) +
				`/timeZone: unknown time zone "Nowhere/City": use an IANA name such as "Asia/Tokyo" or "UTC"`},
		{"a logo that does not exist", user, []byte(noLogo),
			where(t, settingsName, noLogo, `"logo"`) + `/logo: media "med_01JBG3N6V3X6Z9C2E5G8J1K4M9" does not exist`},
		{"not JSON", user, []byte("{\n  oops"), "site/settings.json:2:3: invalid character 'o' at start of value"},
		{"64 MiB and one byte", user, bytes.Repeat([]byte(" "), content.MaxDocumentBytes+1),
			"site/settings.json: too large: 67108865 bytes (max 67108864)"},
		{"a malformed user ID", Op{By: "usr_x"}, []byte(orig),
			`invalid user ID "usr_x": use "usr_" and a ULID (26 characters in upper case)`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ed, s := newSample(t)
			before := snapshot(t, s)
			res, err := ed.SaveSettings(t.Context(), tt.op, read(t, s, store.SettingsKind, "").Seq, tt.data)
			if err == nil || err.Error() != tt.want {
				t.Errorf("SaveSettings() = %+v, %v\nwant the error %q", res, err, tt.want)
			}
			if got := snapshot(t, s); got != before {
				t.Errorf("SaveSettings() has changed the store:\n%s\nwant\n%s", got, before)
			}
		})
	}
}

// TestNoSettings checks an Editor on a store that has no settings document.
func TestNoSettings(t *testing.T) {
	s, err := store.Create(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ed := testEditor(t, s)
	op := Op{By: userID}

	// A document cannot be validated without them.
	_, err = ed.Create(t.Context(), op, content.RefRedirect, []byte(redirectJSON("/a/", "/b/")))
	want := "site/settings.json: not found; save the settings first"
	if !errors.Is(err, fs.ErrNotExist) || err == nil || err.Error() != want {
		t.Errorf("Create() on a store without settings = %v, want %q wrapping fs.ErrNotExist", err, want)
	}
	// The first settings have the base 0.
	settings := `{
  "schemaVersion": 1,
  "name": { "ja": "サイト" },
  "url": "https://example.com",
  "timeZone": "Asia/Tokyo",
  "languages": ["ja"],
  "defaultLanguage": "ja"
}`
	_, err = ed.SaveSettings(t.Context(), op, 5, []byte(settings))
	if want := "site/settings.json: changed since it was read: it is at version 0, not 5"; !errors.Is(err, ErrConflict) || err.Error() != want {
		t.Errorf("SaveSettings() of the first settings with the base 5 = %v, want %q", err, want)
	}
	res := must(t)(ed.SaveSettings(t.Context(), op, 0, []byte(settings)))
	if res.Version.Seq != 1 || res.Kind != store.SettingsKind || len(res.Warnings) != 0 {
		t.Errorf("SaveSettings() of the first settings = %+v", res)
	}
	// Then the documents can be made.
	must(t)(ed.Create(t.Context(), op, content.RefRedirect, []byte(redirectJSON("/a/", "/b/"))))
}

// TestExistingProblems checks that the problems that the site had before a change neither stop it nor are reported, and
// that a new one does.
func TestExistingProblems(t *testing.T) {
	ed, s := newSample(t)
	op := Op{By: userID}
	// Two terms with the slug news, which only the store's own Put lets in: the problem is on the second one.
	dup := testID(content.RefTerm, 1)
	put(t, s, content.RefTerm, dup, `{"schemaVersion": 1, "id": "`+dup+`", "taxonomy": "category", "language": "ja", "translationGroup": "`+
		testID(content.RefTranslationGroup, 1)+`", "name": "お知らせ2", "slug": "news"}`)
	if got := buildErrors(t, existingProblemsDir(t, dup)); len(got) != 1 ||
		!strings.HasSuffix(got[0], `/slug: slug "news" is also used by `+newsName) {
		t.Fatalf("the build of the site with the duplicate reports %q", got)
	}
	// And a redirect from the path of a page, which the build finds in a later stage, after those of the slugs: the
	// problems of every stage are the problems that the site had.
	rdr := testID(content.RefRedirect, 1)
	put(t, s, content.RefRedirect, rdr, `{"schemaVersion": 1, "id": "`+rdr+`", "from": "/about/", "to": "/", "status": 301}`)

	// A change of other documents.
	res := must(t)(ed.Create(t.Context(), op, content.RefRedirect, []byte(redirectJSON("/old-contact/", "/contact/"))))
	if len(res.Warnings) != 0 {
		t.Errorf("Create() reported %v", res.Warnings)
	}
	news := replaceOnce(t, string(sampleFile(t, newsName)), "会社からのお知らせです。", "お知らせです。")
	res = must(t)(ed.Save(t.Context(), op, content.RefTerm, newsID, read(t, s, content.RefTerm, newsID).Seq, []byte(news)))
	if len(res.Warnings) != 0 {
		t.Errorf("Save() reported %v", res.Warnings)
	}
	// A change of the document that has the problem, which keeps it.
	data := string(read(t, s, content.RefTerm, dup).Data)
	renamed := replaceOnce(t, data, "お知らせ2", "お知らせ3")
	res = must(t)(ed.Save(t.Context(), op, content.RefTerm, dup, read(t, s, content.RefTerm, dup).Seq, []byte(renamed)))
	if len(res.Warnings) != 0 {
		t.Errorf("Save() reported %v", res.Warnings)
	}

	// A third term with the slug is a new problem, which is on it alone.
	group := testID(content.RefTranslationGroup, 2)
	third := termJSON("category", "ja", group, "お知らせ4", "news")
	_, err := ed.Create(t.Context(), op, content.RefTerm, []byte(third))
	name := docName(content.RefTerm, predictIDs(t, content.RefRedirect, content.RefTerm)[1])
	want := where(t, name, third, `"slug"`) + `/slug: slug "news" is also used by ` + newsName
	if err == nil || err.Error() != want {
		t.Errorf("Create() of a third term with the slug = %v, want the error %q", err, want)
	}

	// Deleting the term that has the problem takes it away, and nothing is reported.
	res = must(t)(ed.Delete(t.Context(), op, content.RefTerm, dup, read(t, s, content.RefTerm, dup).Seq))
	if len(res.Warnings) != 0 {
		t.Errorf("Delete() reported %v", res.Warnings)
	}
}

// existingProblemsDir returns a copy of the sample site directory that has the term dup of TestExistingProblems.
func existingProblemsDir(t *testing.T, dup string) string {
	t.Helper()
	dir := copySite(t)
	data := `{"schemaVersion": 1, "id": "` + dup + `", "taxonomy": "category", "language": "ja", "translationGroup": "` +
		testID(content.RefTranslationGroup, 1) + `", "name": "お知らせ2", "slug": "news"}`
	if err := os.WriteFile(filepath.Join(dir, "terms", dup+".json"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestCreateLocated checks that a problem that the checks across the site find in the document that Create makes is
// located in the bytes that it was given.
func TestCreateLocated(t *testing.T) {
	ed, _ := newSample(t)
	data := authorJSON("山田 次郎", "yamada")
	name := docName(content.RefAuthor, predictIDs(t, content.RefAuthor)[0])
	_, err := ed.Create(t.Context(), Op{By: userID}, content.RefAuthor, []byte(data))
	want := where(t, name, data, `"slug"`) + `/slug: slug "yamada" is also used by authors/aut_01JBA4C7N9Q3T6W9Z3C6F9H2K5.json`
	if err == nil || err.Error() != want {
		t.Errorf("Create() = %v, want the error %q", err, want)
	}
}

// TestBrokenSchema checks that the problems of the settings and the schema file that the store has are the error of an
// operation on documents, located in their files, and that a change of the settings that it no longer has loaded counts
// every problem of the site as new.
func TestBrokenSchema(t *testing.T) {
	ed, s := newSample(t)
	op := Op{By: userID}
	// With this schema, which keeps the types and the taxonomies of the sample, the URL rule of the pages ends with ".html",
	// which the stored settings do not allow with a posts page.
	ed.schema.Data = []byte(replaceOnce(t, string(ed.schema.Data), "types:\n", "types:\n  page:\n    url: /{path}.html\n"))
	before := snapshot(t, s)
	_, err := ed.Create(t.Context(), op, content.RefRedirect, []byte(redirectJSON("/old-contact/", "/contact/")))
	orig := string(sampleFile(t, settingsName))
	want := where(t, settingsName, orig, `"ja": "ent_01JBG3N6T2W5Y8B1D4F7H0K3M6"`) +
		`/postsPage/ja: the URL rule of page "/{path}.html" must end with "/" for the posts page`
	if err == nil || err.Error() != want || snapshot(t, s) != before {
		t.Errorf("Create() = %v, want the error %q and no change", err, want)
	}

	// The settings are saved with the schema file too. They are valid without the posts page, and so is every document as
	// it validates under them, but the site has a problem that the settings before them cannot be compared with, so it is new.
	dup := testID(content.RefTerm, 1)
	put(t, s, content.RefTerm, dup, `{"schemaVersion": 1, "id": "`+dup+`", "taxonomy": "category", "language": "ja", "translationGroup": "`+
		testID(content.RefTranslationGroup, 1)+`", "name": "お知らせ2", "slug": "news"}`)
	settings := replaceOnce(t, orig, `  "postsPage": { "ja": "ent_01JBG3N6T2W5Y8B1D4F7H0K3M6" },`+"\n", "")
	_, err = ed.SaveSettings(t.Context(), op, read(t, s, store.SettingsKind, "").Seq, []byte(settings))
	want = "terms/" + dup + `.json:1:` // the problem is that of the term, whose bytes the store has
	if err == nil || !strings.HasPrefix(err.Error(), want) || !strings.Contains(err.Error(), `/slug: slug "news" is also used by `+newsName) {
		t.Errorf("SaveSettings() = %v, want the problem of the duplicate slug, which is new", err)
	}
}

// TestDanglingReferences runs the operations on a store whose documents refer to documents that are missing, and hold
// values that do not validate, which only the store's own API lets in: none panics, and none reports their problems.
func TestDanglingReferences(t *testing.T) {
	ed, s := newSample(t)
	op := Op{By: userID}
	user := userID
	var (
		orphan = testID(content.RefTerm, 2)
		broken = testID(content.RefTerm, 3)
		author = testID(content.RefAuthor, 1)
		menu   = testID(content.RefMenu, 1)
		form   = testID(content.RefForm, 1)
		rdr    = testID(content.RefRedirect, 1)
		entry  = testID(content.RefEntry, 1)
		rev    = testID(content.RefRevision, 1)
	)
	err := s.Update(t.Context(), func(tx *store.Tx) error {
		// The term news, which published revisions, a menu, a redirect and a term name.
		if _, err := tx.Delete(content.RefTerm, newsID, user); err != nil {
			return err
		}
		for _, d := range []struct {
			kind content.RefKind
			id   string
			data string
		}{
			// A term whose parent does not exist, and one with values that are none of the site's.
			{content.RefTerm, orphan, `{"schemaVersion": 1, "id": "` + orphan + `", "taxonomy": "category", "language": "ja", ` +
				`"translationGroup": "` + testID(content.RefTranslationGroup, 2) + `", "name": "孤児", "slug": "orphan", "parent": "` + testID(content.RefTerm, 99) + `"}`},
			{content.RefTerm, broken, `{"schemaVersion": 1, "id": "` + broken + `", "taxonomy": "nope", "language": "zz", ` +
				`"translationGroup": "x", "slug": "A B", "parent": "trm_x"}`},
			{content.RefAuthor, author, `{"schemaVersion": 1, "id": "` + author + `", "slug": "A B", "avatar": "med_x"}`},
			{content.RefMenu, menu, `{"schemaVersion": 1, "id": "` + menu + `", "language": "zz", "location": "nowhere", ` +
				`"items": [{"label": "x", "entry": "ent_x"}, {"term": "` + testID(content.RefTerm, 98) + `"}]}`},
			{content.RefForm, form, `{"schemaVersion": 1, "id": "` + form + `", "fields": [{"name": "1", "type": "nope"}], ` +
				`"thanksPage": {"ja": "` + testID(content.RefEntry, 98) + `"}}`},
			{content.RefRedirect, rdr, `{"schemaVersion": 1, "id": "` + rdr + `", "from": "x", "to": "` + testID(content.RefMedia, 98) + `", "status": 301}`},
			{content.RefEntry, entry, `{"schemaVersion": 1, "id": "` + entry + `", "number": 1, "type": "nope", "language": "zz", ` +
				`"translationGroup": "x", "status": "published", "publishedRevision": "` + rev + `", "createdAt": "x", "createdBy": "y"}`},
		} {
			if _, err := tx.Put(d.kind, d.id, []byte(d.data), user); err != nil {
				return fmt.Errorf("Put(%s): %w", d.id, err)
			}
		}
		_, err := tx.AddRevision(entry, rev, []byte(`{"schemaVersion": 1, "title": "x", "slug": "A B", "date": "x", "parent": "`+
			testID(content.RefEntry, 99)+`", "author": "`+testID(content.RefAuthor, 99)+`", "sticky": true, `+
			`"terms": {"nope": ["`+testID(content.RefTerm, 97)+`"], "category": ["`+newsID+`", "`+orphan+`"]}}`), user)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	// Documents that do not refer to the broken ones, made, changed and deleted. Each has the problems of the site after
	// it as well as before, so that the site before it is made too: in a second transaction.
	transactions := 0
	ed.wrap = func(r reader) reader {
		transactions++
		return r
	}
	footer := string(sampleFile(t, "menus/mnu_01JBD2M5N6P7Q8R9S0T1V2W3X4.json"))
	footerSeq := read(t, s, content.RefMenu, "mnu_01JBD2M5N6P7Q8R9S0T1V2W3X4").Seq
	for _, call := range []struct {
		name string
		run  func() (Result, error)
	}{
		{"Create a redirect", func() (Result, error) {
			return ed.Create(t.Context(), op, content.RefRedirect, []byte(redirectJSON("/old-contact/", "/contact/")))
		}},
		{"Create an author", func() (Result, error) {
			return ed.Create(t.Context(), op, content.RefAuthor, []byte(authorJSON("佐藤 花子", "sato")))
		}},
		{"Create a term", func() (Result, error) {
			return ed.Create(t.Context(), op, content.RefTerm, []byte(termJSON("tag", "ja", "", "お祭り", "festival")))
		}},
		{"Save a menu", func() (Result, error) {
			return ed.Save(t.Context(), op, content.RefMenu, "mnu_01JBD2M5N6P7Q8R9S0T1V2W3X4", footerSeq,
				[]byte(replaceOnce(t, footer, "お問い合わせ", "ご連絡")))
		}},
		{"Delete a redirect", func() (Result, error) {
			return ed.Delete(t.Context(), op, content.RefRedirect, "rdr_01JBD3A4B5C6D7E8F9G0H1J2K3",
				read(t, s, content.RefRedirect, "rdr_01JBD3A4B5C6D7E8F9G0H1J2K3").Seq)
		}},
		{"SaveSettings", func() (Result, error) {
			settings := replaceOnce(t, string(sampleFile(t, settingsName)), "Webサイトの企画と制作", "Webサイトの企画・制作")
			return ed.SaveSettings(t.Context(), op, read(t, s, store.SettingsKind, "").Seq, []byte(settings))
		}},
	} {
		t.Run(call.name, func(t *testing.T) {
			transactions = 0
			res, err := call.run()
			if err != nil || len(res.Warnings) != 0 {
				t.Errorf("%s = %+v, %v; want a success without warnings", call.name, res, err)
			}
			if transactions != 2 {
				t.Errorf("%s read the index in %d transactions, want 2: the Update and the View of the site before it", call.name, transactions)
			}
		})
	}
}
