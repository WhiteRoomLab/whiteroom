package edit

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

// changedTerm returns the JSON of the term whose document is data, after change.
func changedTerm(t *testing.T, data []byte, change func(*content.Term)) string {
	t.Helper()
	var term content.Term
	if err := term.UnmarshalJSON(data); err != nil {
		t.Fatalf("UnmarshalJSON(%s) = %v", data, err)
	}
	change(&term)
	out, err := term.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// toEnglish is the change that gives a term the language en and a translation group of its own, which no other term of
// the sample site has the language in.
func toEnglish(x *content.Term) {
	x.Language, x.TranslationGroup = "en", testID(content.RefTranslationGroup, 1)
}

// newTag makes a tag in ja with ed.
func newTag(t *testing.T, ed *Editor) string {
	t.Helper()
	return must(t)(ed.Create(t.Context(), Op{By: userID}, content.RefTerm, []byte(termJSON("tag", "ja", "", "お祭り", "festival")))).ID
}

// TestRefChangeParity saves a term of the sample site with a taxonomy or a language that the documents that refer to it
// do not accept, which the validation of the term does not find: the Editor refuses it with the problems that the build
// finds in the other documents for a copy of the site that has the term as saved, whole lines, positions included.
func TestRefChangeParity(t *testing.T) {
	for _, tt := range []struct {
		name   string
		id     string
		change func(*content.Term)
		want   int // the number of problems
	}{
		{"a tag in another language", tagJA, toEnglish, 4},
		{"a category in another language", newsID, func(x *content.Term) { x.Language = "en" }, 9},
		{"a category in another taxonomy", newsID, func(x *content.Term) { x.Taxonomy = "tag" }, 9},
		// The group has a term in en already, which the checks across the site find; those checks do not run, as the build's
		// do not run after the problems of validation.
		{"a tag in another language, in a group that has a term in it", tagJA, func(x *content.Term) { x.Language = "en" }, 4},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := newParity(t)
			name := docName(content.RefTerm, tt.id)
			data := changedTerm(t, sampleFile(t, name), tt.change)
			p.write(name, data)
			_, err := p.ed.Save(t.Context(), Op{By: userID}, content.RefTerm, tt.id, p.seq(content.RefTerm, tt.id), []byte(data))
			got := problemLines(t, err)
			want := buildErrors(t, p.dir)
			if len(got) != tt.want || len(want) == 0 || !slices.Equal(got, want) {
				t.Errorf("the problems of the Editor:\n%s\nthe problems of the build:\n%s\nwant %d", strings.Join(got, "\n"), strings.Join(want, "\n"), tt.want)
			}
			if snap := snapshot(t, p.s); snap != p.base {
				t.Errorf("the change that failed has changed the store:\n%s\nwant\n%s", snap, p.base)
			}
			t.Logf("the problems:\n%s", strings.Join(got, "\n"))
		})
	}
}

// TestRefChangeDrafts saves a term with a language that only drafts do not accept: it is saved, with the problems that
// the drafts have as warnings, located in the bytes that the store has of them.
func TestRefChangeDrafts(t *testing.T) {
	op := Op{By: userID}
	// check saves the tag in en, which must succeed with the one warning that the revision rev has.
	check := func(t *testing.T, ed *Editor, s *store.Store, tag, rev string) {
		t.Helper()
		doc := read(t, s, content.RefTerm, tag)
		data := changedTerm(t, doc.Data, toEnglish)
		res, err := ed.Save(t.Context(), op, content.RefTerm, tag, doc.Seq, []byte(data))
		if err != nil {
			t.Fatalf("Save() = %v, want the term to be saved with a warning", err)
		}
		text := string(revisionData(t, s, rev))
		want := where(t, revisionName(rev), text, `"`+tag+`"`) + `/terms/tag/0: term "` + tag + `" is in the language "en", not "ja"`
		if got := lines(res.Warnings); !slices.Equal(got, []string{want}) {
			t.Errorf("Save() warnings:\n%s\nwant\n%s", strings.Join(got, "\n"), want)
		}
		if got := read(t, s, content.RefTerm, tag); got.Seq != res.Version.Seq || string(got.Data) != data {
			t.Errorf("the term is %+v with %s, want the data that were saved", got.Version, got.Data)
		}
	}

	t.Run("the latest revision of a draft", func(t *testing.T) {
		ed, s := newSample(t)
		tag := newTag(t, ed)
		entry := newDraft(t, ed, content.TypePost, "ja")
		rev := must(t)(ed.SaveRevision(t.Context(), op, entry, "", []byte(
			`{"schemaVersion":1,"title":"a","slug":"a","date":"2026-10-25T10:00:00+09:00","terms":{"tag":["`+tag+`"]}}`))).Revision
		check(t, ed, s, tag, rev)
	})

	t.Run("a revision that is newer than the published one", func(t *testing.T) {
		ed, s := newSample(t)
		tag := newTag(t, ed)
		latest := summaryOf(t, s, postJA).Latest
		data := replaceOnce(t, string(revisionData(t, s, latest)), tagJA, tag)
		rev := must(t)(ed.SaveRevision(t.Context(), op, postJA, latest, []byte(data))).Revision
		check(t, ed, s, tag, rev)
	})
}

// putPublished puts a published post in ja, with the number 600+n and the revision data, into s with the store's own API,
// which validates nothing against the site, and returns the entry and its revision.
func putPublished(t *testing.T, s *store.Store, n int, data string) (entry, rev string) {
	t.Helper()
	entry, rev = testID(content.RefEntry, n), testID(content.RefRevision, n)
	group := testID(content.RefTranslationGroup, n)
	put(t, s, content.RefEntry, entry, storedEntry(entry, 600+n, content.TypePost, "ja", group, content.StatusDraft, ""))
	addStoredRevision(t, s, entry, rev, data)
	put(t, s, content.RefEntry, entry, storedEntry(entry, 600+n, content.TypePost, "ja", group, content.StatusPublished, rev))
	return entry, rev
}

// TestRefChangeOldProblems checks that a problem that a document had before the change is neither an error nor a warning:
// the published revisions here have a title with a tab, which only the store's own API lets in.
func TestRefChangeOldProblems(t *testing.T) {
	op := Op{By: userID}
	const head = `{"schemaVersion":1,"title":"a\tb","slug":"old","date":"2026-10-25T10:00:00+09:00",`
	const oldProblem = "/title: contains a line break or tab"

	// A revision that names the tag only in a text is a reference for the index, by the ID that the text is, and
	// validation does not look the text up: it has the old problem alone, which the change leaves as it is.
	t.Run("a reference by a text", func(t *testing.T) {
		ed, s := newSample(t)
		tag := newTag(t, ed)
		entry, rev := putPublished(t, s, 1, head+`"body":[{"type":"paragraph","id":"b_00000001","content":[{"type":"text","text":"`+tag+`"}]}]}`)
		err := s.View(t.Context(), func(tx *store.Tx) error {
			refs, err := tx.Referrers(content.RefTerm, tag)
			if want := []store.Referrer{{Kind: content.RefEntry, ID: entry, Role: store.RoleLatest}, {Kind: content.RefEntry, ID: entry, Role: store.RolePublished}}; err != nil || !slices.Equal(refs, want) {
				t.Errorf("Referrers() = %v, %v; want the revision %s, which the index counts as a reference", refs, err, rev)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		doc := read(t, s, content.RefTerm, tag)
		data := changedTerm(t, doc.Data, toEnglish)
		res, err := ed.Save(t.Context(), op, content.RefTerm, tag, doc.Seq, []byte(data))
		if err != nil || len(res.Warnings) != 0 {
			t.Errorf("Save() = %+v, %v; want the term saved without warnings, as the old problem of the revision is not new", res, err)
		}
		if got := read(t, s, content.RefTerm, tag); got.Seq != res.Version.Seq {
			t.Errorf("the term is at %+v, want the version that Save() returned", got.Version)
		}
	})

	// A revision that uses the tag has the new problem as well: the error has it and not the old one.
	t.Run("a reference", func(t *testing.T) {
		ed, s := newSample(t)
		tag := newTag(t, ed)
		_, rev := putPublished(t, s, 1, head+`"terms":{"tag":["`+tag+`"]}}`)
		before := snapshot(t, s)
		doc := read(t, s, content.RefTerm, tag)
		_, err := ed.Save(t.Context(), op, content.RefTerm, tag, doc.Seq, []byte(changedTerm(t, doc.Data, toEnglish)))
		text := string(revisionData(t, s, rev))
		want := where(t, revisionName(rev), text, `"`+tag+`"`) + `/terms/tag/0: term "` + tag + `" is in the language "en", not "ja"`
		if got := problemLines(t, err); !slices.Equal(got, []string{want}) {
			t.Errorf("Save() problems:\n%s\nwant\n%s, and not %s", strings.Join(got, "\n"), want, oldProblem)
		}
		if got := snapshot(t, s); got != before {
			t.Errorf("Save() has changed the store:\n%s\nwant\n%s", got, before)
		}
	})
}

// The settings of the small site of TestSettingsChangeParity, in ja and en, and in ja alone.
const (
	smallSettings = `{
  "schemaVersion": 1,
  "name": { "en": "Example", "ja": "サンプル" },
  "url": "https://example.com",
  "timeZone": "Asia/Tokyo",
  "languages": ["ja", "en"],
  "defaultLanguage": "ja"%s
}`
	smallJA = `{
  "schemaVersion": 1,
  "name": { "ja": "サンプル" },
  "url": "https://example.com",
  "timeZone": "Asia/Tokyo",
  "languages": ["ja"],
  "defaultLanguage": "ja"
}`
)

// smallSite is a site directory of a published page in ja and a draft page in en, with its settings, and a store that has
// it: the latest revision of the draft, which no entry publishes, only the store has, as the build reads only the
// revisions that entries publish.
type smallSite struct {
	ed              *Editor
	s               *store.Store
	dir             string
	page, pageRev   string
	draft, draftRev string
}

// newSmallSite makes the small site with the settings, and the other files of the directory by their names, which replace
// the files that it has.
func newSmallSite(t *testing.T, settings string, files map[string]string) *smallSite {
	t.Helper()
	x := &smallSite{dir: t.TempDir()}
	x.page, x.pageRev = testID(content.RefEntry, 1), testID(content.RefRevision, 1)
	x.draft, x.draftRev = testID(content.RefEntry, 2), testID(content.RefRevision, 2)
	all := map[string]string{
		settingsName:       settings,
		"site/schema.yaml": string(sampleFile(t, "site/schema.yaml")),
		docName(content.RefEntry, x.page): storedEntry(x.page, 701, content.TypePage, "ja", testID(content.RefTranslationGroup, 1),
			content.StatusPublished, x.pageRev),
		revisionName(x.pageRev): `{"schemaVersion":1,"title":"ホーム","slug":"home"}`,
		docName(content.RefEntry, x.draft): storedEntry(x.draft, 702, content.TypePage, "en", testID(content.RefTranslationGroup, 2),
			content.StatusDraft, ""),
	}
	maps.Copy(all, files)
	for name, data := range all {
		writeSite(t, x.dir, name, data)
	}
	s, err := store.Create(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	putSite(t, s, x.dir)
	draft, ok := files[revisionName(x.draftRev)]
	if !ok {
		draft = `{"schemaVersion":1,"title":"Draft","slug":"draft"}`
	}
	addStoredRevision(t, s, x.draft, x.draftRev, draft)
	x.s, x.ed = s, testEditor(t, s)
	return x
}

// refuse saves settings, which must fail, and returns the lines of its error and the lines of the build's error for the
// site directory with the settings; the store must not have changed.
func (x *smallSite) refuse(t *testing.T, settings string) (got, want []string) {
	t.Helper()
	before := snapshot(t, x.s)
	_, err := x.ed.SaveSettings(t.Context(), Op{By: userID}, read(t, x.s, store.SettingsKind, "").Seq, []byte(settings))
	got = problemLines(t, err)
	if snap := snapshot(t, x.s); snap != before {
		t.Errorf("SaveSettings() has changed the store:\n%s\nwant\n%s", snap, before)
	}
	writeSite(t, x.dir, settingsName, settings)
	return got, buildErrors(t, x.dir)
}

// sameProblem checks that got, the lines of the problems of the Editor, are the one problem msg in the file name, and
// those of the build.
func sameProblem(t *testing.T, got, want []string, name, msg string) {
	t.Helper()
	if len(got) != 1 || !strings.HasPrefix(got[0], name+":") || !strings.HasSuffix(got[0], msg) || !slices.Equal(got, want) {
		t.Errorf("the problems of the Editor:\n%s\nthe problems of the build:\n%s\nwant one in %s: %s",
			strings.Join(got, "\n"), strings.Join(want, "\n"), name, msg)
	}
}

// TestSettingsChangeParity saves settings that the documents of the site do not accept, which the validation of the settings
// does not find: the Editor refuses them with the problems that the build finds in the documents for the site directory with
// the settings as saved, whole lines, positions included; and what the build does not validate, the drafts, it warns of.
func TestSettingsChangeParity(t *testing.T) {
	op := Op{By: userID}
	settings := func(extra string) string { return strings.Replace(smallSettings, "%s", extra, 1) }
	menu := testID(content.RefMenu, 1)
	enMenu := `{"schemaVersion":1,"id":"` + menu + `","title":"Header","location":"header","language":"en","items":[{"label":"Home","href":"/"}]}`
	noEnglish := `/language: language "en" is not in the site settings (use ja)`

	t.Run("a draft in a language that the settings lose", func(t *testing.T) {
		x := newSmallSite(t, settings(""), nil)
		res, err := x.ed.SaveSettings(t.Context(), op, read(t, x.s, store.SettingsKind, "").Seq, []byte(smallJA))
		if err != nil {
			t.Fatalf("SaveSettings() = %v, want the settings saved with a warning", err)
		}
		// The state of the draft is not valid, which is the only warning: its latest revision is validated only for a valid state.
		doc := string(read(t, x.s, content.RefEntry, x.draft).Data)
		want := where(t, docName(content.RefEntry, x.draft), doc, `"language"`) + noEnglish
		if got := lines(res.Warnings); !slices.Equal(got, []string{want}) {
			t.Errorf("SaveSettings() warnings:\n%s\nwant\n%s", strings.Join(got, "\n"), want)
		}
		if got := read(t, x.s, store.SettingsKind, ""); got.Seq != res.Version.Seq || string(got.Data) != smallJA {
			t.Errorf("the settings are %+v with %s, want the settings that were saved", got.Version, got.Data)
		}
		// The build does not validate the draft: it succeeds for the same site.
		writeSite(t, x.dir, settingsName, smallJA)
		if got := buildErrors(t, x.dir); len(got) != 0 {
			t.Errorf("the build of the site with the settings fails: %q", got)
		}
	})

	t.Run("a menu in a language that the settings lose", func(t *testing.T) {
		x := newSmallSite(t, settings(""), map[string]string{docName(content.RefMenu, menu): enMenu})
		got, want := x.refuse(t, smallJA)
		sameProblem(t, got, want, docName(content.RefMenu, menu), noEnglish)
	})

	t.Run("a published page in a language that the settings lose", func(t *testing.T) {
		en, enRev := testID(content.RefEntry, 3), testID(content.RefRevision, 3)
		x := newSmallSite(t, settings(""), map[string]string{
			docName(content.RefEntry, en): storedEntry(en, 703, content.TypePage, "en", testID(content.RefTranslationGroup, 3),
				content.StatusPublished, enRev),
			revisionName(enRev): `{"schemaVersion":1,"title":"Home","slug":"home"}`,
		})
		got, want := x.refuse(t, smallJA)
		sameProblem(t, got, want, docName(content.RefEntry, en), noEnglish)
	})

	// The problems are in the order of their files, not of the documents that the Editor validates.
	t.Run("problems in several documents", func(t *testing.T) {
		en, enRev := testID(content.RefEntry, 3), testID(content.RefRevision, 3)
		x := newSmallSite(t, settings(""), map[string]string{
			docName(content.RefMenu, menu): enMenu,
			docName(content.RefEntry, en): storedEntry(en, 703, content.TypePage, "en", testID(content.RefTranslationGroup, 3),
				content.StatusPublished, enRev),
			revisionName(enRev): `{"schemaVersion":1,"title":"Home","slug":"home"}`,
		})
		got, want := x.refuse(t, smallJA)
		if len(got) != 2 || !strings.HasPrefix(got[0], "entries/") || !strings.HasPrefix(got[1], "menus/") || !slices.Equal(got, want) {
			t.Errorf("the problems of the Editor:\n%s\nthe problems of the build:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
	})

	const html = `{"type":"html","id":"b_0000html","attrs":{"html":"<p>x</p>"}}`
	notEnabled := "/body/0: html blocks are not enabled"

	t.Run("a published revision with a block that the settings stop allowing", func(t *testing.T) {
		x := newSmallSite(t, settings(",\n  \"allowHtml\": true"), map[string]string{
			revisionName(testID(content.RefRevision, 1)): `{"schemaVersion":1,"title":"ホーム","slug":"home","body":[` + html + `]}`,
		})
		got, want := x.refuse(t, settings(""))
		sameProblem(t, got, want, revisionName(testID(content.RefRevision, 1)), notEnabled)
	})

	// The latest revision of the draft is not validated by the build, which the revision that no entry publishes shows.
	t.Run("a draft revision with a block that the settings stop allowing", func(t *testing.T) {
		draft := `{"schemaVersion":1,"title":"Draft","slug":"draft","body":[` + html + `]}`
		x := newSmallSite(t, settings(",\n  \"allowHtml\": true"), map[string]string{revisionName(testID(content.RefRevision, 2)): draft})
		res, err := x.ed.SaveSettings(t.Context(), op, read(t, x.s, store.SettingsKind, "").Seq, []byte(settings("")))
		if err != nil {
			t.Fatalf("SaveSettings() = %v, want the settings saved with a warning", err)
		}
		want := where(t, revisionName(x.draftRev), draft, `{"type":"html"`) + notEnabled
		if got := lines(res.Warnings); !slices.Equal(got, []string{want}) {
			t.Errorf("SaveSettings() warnings:\n%s\nwant\n%s", strings.Join(got, "\n"), want)
		}
		writeSite(t, x.dir, settingsName, settings(""))
		if got := buildErrors(t, x.dir); len(got) != 0 {
			t.Errorf("the build of the site with the settings fails: %q", got)
		}
	})

	// The problems of a site before the change are not reported, as the build reports them: a revision with a title that has a
	// tab, which only the store's own API lets in, is in the build's error and not in the Editor's.
	t.Run("a problem that the site had", func(t *testing.T) {
		x := newSmallSite(t, settings(""), map[string]string{
			docName(content.RefMenu, menu):               enMenu,
			revisionName(testID(content.RefRevision, 1)): `{"schemaVersion":1,"title":"ホ\tーム","slug":"home"}`,
		})
		got, want := x.refuse(t, smallJA)
		if len(want) != 2 || !strings.HasSuffix(want[1], "/title: contains a line break or tab") {
			t.Fatalf("the problems of the build:\n%s\nwant the problem of the menu and the problem that the site had", strings.Join(want, "\n"))
		}
		sameProblem(t, got, want[:1], docName(content.RefMenu, menu), noEnglish)
	})
}

// counting is a reader that records its calls.
type counting struct {
	reader
	calls *[]string
}

func (c counting) Summary(kind content.RefKind, id string) (store.Summary, error) {
	*c.calls = append(*c.calls, "Summary "+string(kind)+" "+id)
	return c.reader.Summary(kind, id)
}

func (c counting) Summaries(kind content.RefKind) ([]store.Summary, error) {
	*c.calls = append(*c.calls, "Summaries "+string(kind))
	return c.reader.Summaries(kind)
}

func (c counting) Referrers(kind content.RefKind, id string) ([]store.Referrer, error) {
	*c.calls = append(*c.calls, "Referrers "+string(kind)+" "+id)
	return c.reader.Referrers(kind, id)
}

// callsTo returns the recorded calls of the method, such as "Summary term trm_..." for "Summary".
func callsTo(calls []string, method string) []string {
	return slices.DeleteFunc(slices.Clone(calls), func(c string) bool { return !strings.HasPrefix(c, method+" ") })
}

// TestRefChangeCost checks what a save reads of the index when it changes nothing that other documents read: no
// referrer, no other document, and no model beyond those of the checks across the site. A save of the same bytes, which
// saves nothing, reads none of that either.
func TestRefChangeCost(t *testing.T) {
	op := Op{By: userID}
	settings := string(sampleFile(t, settingsName))
	// Each model of the site reads the summaries of every kind once; the sample has a warning of its own, a redirect to a
	// draft, so that the site before the change is made too.
	const kinds = 7
	models := 2 * kinds

	run := func(t *testing.T, call func(ed *Editor, s *store.Store) (Result, error)) (calls []string, res Result, err error) {
		t.Helper()
		ed, s := newSample(t)
		ed.wrap = func(r reader) reader { return counting{r, &calls} }
		res, err = call(ed, s)
		return calls, res, err
	}

	for _, tt := range []struct {
		kind     content.RefKind
		id       string
		old, new string
		own      int // the reads of the summary of the document: before the save and after it, if it can change its Ref
	}{
		{content.RefTerm, newsID, `"name": "お知らせ"`, `"name": "ニュース"`, 2},
		{content.RefAuthor, "aut_01JBA4C7N9Q3T6W9Z3C6F9H2K5", `"name": "山田 太郎"`, `"name": "山田 次郎"`, 0},
		{content.RefMenu, "mnu_01JBD2M5N6P7Q8R9S0T1V2W3X4", `"title": "フッター"`, `"title": "フッターのメニュー"`, 0},
		{content.RefForm, "frm_01JB8Z3K5VP1D4G7J2M5Q8T3W6", `"title": "お問い合わせ"`, `"title": "お問い合わせフォーム"`, 0},
		{content.RefRedirect, "rdr_01JC0000000000000000000001", `"status": 301`, `"status": 302`, 0},
	} {
		t.Run(string(tt.kind), func(t *testing.T) {
			doc := string(sampleFile(t, docName(tt.kind, tt.id)))
			changed := replaceOnce(t, doc, tt.old, tt.new)
			save := func(data string) func(ed *Editor, s *store.Store) (Result, error) {
				return func(ed *Editor, s *store.Store) (Result, error) {
					return ed.Save(t.Context(), op, tt.kind, tt.id, read(t, s, tt.kind, tt.id).Seq, []byte(data))
				}
			}

			calls, res, err := run(t, save(changed))
			if err != nil || len(res.Warnings) != 0 {
				t.Fatalf("Save() = %+v, %v", res, err)
			}
			if got := callsTo(calls, "Referrers"); len(got) != 0 {
				t.Errorf("Save() read the referrers: %q", got)
			}
			if got := len(callsTo(calls, "Summaries")); got != models {
				t.Errorf("Save() read the summaries of a kind %d times, want %d: the model of the site after the change and before it", got, models)
			}
			own := " " + string(tt.kind) + " " + tt.id
			if got := callsTo(calls, "Summary"); count(got, "Summary"+own) != tt.own {
				t.Errorf("Save() read the summary of the document %d times, want %d", count(got, "Summary"+own), tt.own)
			} else {
				// The others are the lookups of what the settings and the document name.
				for _, c := range got {
					if id := c[strings.LastIndexByte(c, ' ')+1:]; !strings.HasSuffix(c, own) && !strings.Contains(settings+doc+changed, id) {
						t.Errorf("Save() read %q, which neither the settings nor the document name", c)
					}
				}
			}

			// The same bytes save nothing, and read nothing of it.
			calls, res, err = run(t, save(doc))
			if err != nil || len(res.Warnings) != 0 {
				t.Fatalf("Save() of the bytes that the store has = %+v, %v", res, err)
			}
			for _, method := range []string{"Referrers", "Summaries"} {
				if got := callsTo(calls, method); len(got) != 0 {
					t.Errorf("Save() of the bytes that the store has read %q", got)
				}
			}
			if got := callsTo(calls, "Summary"); count(got, "Summary"+own) != 0 {
				t.Errorf("Save() of the bytes that the store has read the summary of the document")
			}
		})
	}

	// The referrers are read once, and each entry among them once: its summary, for its status and its latest revision.
	t.Run("a term that changes its language", func(t *testing.T) {
		var entries []string
		calls, _, err := run(t, func(ed *Editor, s *store.Store) (Result, error) {
			err := s.View(t.Context(), func(tx *store.Tx) error {
				refs, err := tx.Referrers(content.RefTerm, tagJA)
				for _, ref := range refs {
					if ref.Kind == content.RefEntry && !slices.Contains(entries, ref.ID) {
						entries = append(entries, ref.ID)
					}
				}
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			doc := read(t, s, content.RefTerm, tagJA)
			return ed.Save(t.Context(), op, content.RefTerm, tagJA, doc.Seq, []byte(changedTerm(t, doc.Data, toEnglish)))
		})
		if got := callsTo(calls, "Referrers"); !slices.Equal(got, []string{"Referrers term " + tagJA}) {
			t.Errorf("Save() read the referrers %q, want those of the term once (it fails with %v)", got, err)
		}
		if len(entries) < 2 {
			t.Fatalf("the term has the referrers %v, want entries", entries)
		}
		for _, id := range entries {
			if got := count(callsTo(calls, "Summary"), "Summary entry "+id); got != 1 {
				t.Errorf("Save() read the summary of %s %d times, want once", id, got)
			}
		}
	})

	t.Run("settings", func(t *testing.T) {
		changed := replaceOnce(t, settings, "Webサイトの企画と制作", "Webサイトの企画・制作")
		save := func(data string) func(ed *Editor, s *store.Store) (Result, error) {
			return func(ed *Editor, s *store.Store) (Result, error) {
				return ed.SaveSettings(t.Context(), op, read(t, s, store.SettingsKind, "").Seq, []byte(data))
			}
		}
		// Every document is validated again, which reads no referrer, and the summaries of the entries are read once more, to
		// tell which of them are published.
		calls, res, err := run(t, save(changed))
		if err != nil || len(res.Warnings) != 0 {
			t.Fatalf("SaveSettings() = %+v, %v; want every document of the sample to validate as before", res, err)
		}
		if got := callsTo(calls, "Referrers"); len(got) != 0 {
			t.Errorf("SaveSettings() read the referrers: %q", got)
		}
		if got := len(callsTo(calls, "Summaries")); got != models+1 {
			t.Errorf("SaveSettings() read the summaries of a kind %d times, want %d: the models, and the entries to validate them", got, models+1)
		}

		// The same bytes save nothing, and read only what the settings name.
		calls, res, err = run(t, save(settings))
		if err != nil || len(res.Warnings) != 0 {
			t.Fatalf("SaveSettings() of the bytes that the store has = %+v, %v", res, err)
		}
		for _, method := range []string{"Referrers", "Summaries"} {
			if got := callsTo(calls, method); len(got) != 0 {
				t.Errorf("SaveSettings() of the bytes that the store has read %q", got)
			}
		}
		for _, c := range callsTo(calls, "Summary") {
			if id := c[strings.LastIndexByte(c, ' ')+1:]; !strings.Contains(settings, id) {
				t.Errorf("SaveSettings() of the bytes that the store has read %q, which the settings do not name", c)
			}
		}
	})
}

// count returns the number of the calls that are c.
func count(calls []string, c string) int {
	return len(slices.DeleteFunc(slices.Clone(calls), func(x string) bool { return x != c }))
}

// TestRefChangeLookupFailure checks that a failure to read what the validation of another document needs fails the
// operation with that error, not with problems, and saves nothing.
func TestRefChangeLookupFailure(t *testing.T) {
	errRead := errors.New("the index cannot be read")
	op := Op{By: userID}
	// The author of published revisions that name the tag, which neither the settings nor the tag name.
	const author = "aut_01JBA4C7N9Q3T6W9Z3C6F9H2K5"
	saveTag := func(ed *Editor, s *store.Store) (Result, error) {
		doc := read(t, s, content.RefTerm, tagJA)
		return ed.Save(t.Context(), op, content.RefTerm, tagJA, doc.Seq, []byte(changedTerm(t, doc.Data, toEnglish)))
	}
	saveSettings := func(ed *Editor, s *store.Store) (Result, error) {
		settings := replaceOnce(t, string(sampleFile(t, settingsName)), "Webサイトの企画と制作", "Webサイトの企画・制作")
		return ed.SaveSettings(t.Context(), op, read(t, s, store.SettingsKind, "").Seq, []byte(settings))
	}
	// The settings that it replaces do not load with this schema file, so that no state before the change is known.
	saveOverBrokenSettings := func(ed *Editor, s *store.Store) (Result, error) {
		ed.schema.Data = []byte(replaceOnce(t, string(ed.schema.Data), "types:\n", "types:\n  page:\n    url: /{path}.html\n"))
		settings := replaceOnce(t, string(sampleFile(t, settingsName)), `  "postsPage": { "ja": "ent_01JBG3N6T2W5Y8B1D4F7H0K3M6" },`+"\n", "")
		return ed.SaveSettings(t.Context(), op, read(t, s, store.SettingsKind, "").Seq, []byte(settings))
	}
	for _, tt := range []struct {
		name string
		wrap func(reader) reader
		call func(ed *Editor, s *store.Store) (Result, error)
	}{
		{"the lookup of a reference of a revision, for a term", func(r reader) reader { return failSummaryOf{r, author, errRead} }, saveTag},
		{"the lookup of a reference of a revision, for the settings", func(r reader) reader { return failSummaryOf{r, author, errRead} }, saveSettings},
		{"the lookup of a reference of a revision, for settings over some that do not load",
			func(r reader) reader { return failSummaryOf{r, author, errRead} }, saveOverBrokenSettings},
		{"the summary of an entry that refers to the term", func(r reader) reader { return failSummaryOf{r, postJA, errRead} }, saveTag},
		{"the referrers of the term", failReads(0, failing{referrers: errRead}), saveTag},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ed, s := newSample(t)
			ed.wrap = tt.wrap
			before := snapshot(t, s)
			res, err := tt.call(ed, s)
			var problems content.Errors
			if !errors.Is(err, errRead) || errors.As(err, &problems) || snapshot(t, s) != before {
				t.Errorf("%s = %+v, %v; want the error of the read, not problems, and no change", tt.name, res, err)
			}
		})
	}

	// A published page in en whose revision names the page in ja as its parent: the lookup of the parent is needed only
	// while the state of the page is valid, which it is before the settings lose en and is not after, so that only the state
	// before the change meets the failure.
	t.Run("a lookup that only the state before the change needs", func(t *testing.T) {
		en, enRev := testID(content.RefEntry, 3), testID(content.RefRevision, 3)
		x := newSmallSite(t, strings.Replace(smallSettings, "%s", "", 1), map[string]string{
			docName(content.RefEntry, en): storedEntry(en, 703, content.TypePage, "en", testID(content.RefTranslationGroup, 3),
				content.StatusPublished, enRev),
			revisionName(enRev): `{"schemaVersion":1,"title":"Home","slug":"home","parent":"` + testID(content.RefEntry, 1) + `"}`,
		})
		x.ed.wrap = func(r reader) reader { return failSummaryOf{r, x.page, errRead} }
		before := snapshot(t, x.s)
		res, err := x.ed.SaveSettings(t.Context(), op, read(t, x.s, store.SettingsKind, "").Seq, []byte(smallJA))
		var problems content.Errors
		if !errors.Is(err, errRead) || errors.As(err, &problems) || snapshot(t, x.s) != before {
			t.Errorf("SaveSettings() = %+v, %v; want the error of the read, not problems, and no change", res, err)
		}
	})
}

// failsOnce is a reader whose first Summary of the document of kind with id fails with err, as the first read of the summary
// of a document does that a save makes before it saves. The later ones are the store's.
type failsOnce struct {
	reader
	kind content.RefKind
	id   string
	err  error
	done *bool
}

func (f failsOnce) Summary(kind content.RefKind, id string) (store.Summary, error) {
	if kind == f.kind && id == f.id && !*f.done {
		*f.done = true
		return store.Summary{}, f.err
	}
	return f.reader.Summary(kind, id)
}

// TestRefChangeNoSummary saves a term whose current version has no summary, as the store's has that this program cannot
// decode, which a save replaces to make one: it is not an error. Lookup did not find the term before, so what refers to it
// is validated again, with the problems that it had then as the old ones: those that it gets from the term as saved are
// new, and those that it has either way are not.
func TestRefChangeNoSummary(t *testing.T) {
	op := Op{By: userID}
	// hook makes the first Summary of the tag, which Save reads before it saves, fail with err, and returns whether it has.
	hook := func(ed *Editor, err error) *bool {
		done := new(bool)
		ed.wrap = func(r reader) reader { return failsOnce{r, content.RefTerm, tagJA, err, done} }
		return done
	}
	// notFound is the error of a document that has no summary: it wraps fs.ErrNotExist.
	notFound := fmt.Errorf("%s: %w", docName(content.RefTerm, tagJA), fs.ErrNotExist)

	// A published revision that names the tag and a term that does not exist has the problem of the term that does not exist
	// whether or not lookup finds the tag.
	t.Run("a save that adds no problem", func(t *testing.T) {
		ed, s := newSample(t)
		putPublished(t, s, 1, `{"schemaVersion":1,"title":"a","slug":"old","date":"2026-10-25T10:00:00+09:00","terms":{"tag":["`+tagJA+
			`","`+testID(content.RefTerm, 9)+`"]}}`)
		done := hook(ed, notFound)
		doc := read(t, s, content.RefTerm, tagJA)
		data := changedTerm(t, doc.Data, func(x *content.Term) { x.Name = "イベント情報" })
		res, err := ed.Save(t.Context(), op, content.RefTerm, tagJA, doc.Seq, []byte(data))
		if !*done {
			t.Fatal("Save() did not read the summary of the term before it saved it")
		}
		if err != nil || len(res.Warnings) != 0 {
			t.Fatalf("Save() = %+v, %v; want the term saved without warnings", res, err)
		}
		if got := read(t, s, content.RefTerm, tagJA); got.Seq != res.Version.Seq || got.Seq <= doc.Seq || string(got.Data) != data {
			t.Errorf("the term is %+v with %s, want the data that were saved", got.Version, got.Data)
		}
	})

	t.Run("a save that adds a problem", func(t *testing.T) {
		p := newParity(t)
		done := hook(p.ed, notFound)
		name := docName(content.RefTerm, tagJA)
		data := changedTerm(t, sampleFile(t, name), toEnglish)
		p.write(name, data)
		_, err := p.ed.Save(t.Context(), op, content.RefTerm, tagJA, p.seq(content.RefTerm, tagJA), []byte(data))
		if !*done {
			t.Fatal("Save() did not read the summary of the term before it saved it")
		}
		got := problemLines(t, err)
		want := buildErrors(t, p.dir)
		if len(got) != 4 || !slices.Equal(got, want) {
			t.Errorf("the problems of the Editor:\n%s\nthe problems of the build:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
		if snap := snapshot(t, p.s); snap != p.base {
			t.Errorf("the change that failed has changed the store:\n%s\nwant\n%s", snap, p.base)
		}
	})

	// Any other error of the store fails the save, also if the later reads of the summary succeed.
	t.Run("another error of the store", func(t *testing.T) {
		ed, s := newSample(t)
		errRead := errors.New("the index cannot be read")
		done := hook(ed, errRead)
		before := snapshot(t, s)
		doc := read(t, s, content.RefTerm, tagJA)
		_, err := ed.Save(t.Context(), op, content.RefTerm, tagJA, doc.Seq, []byte(changedTerm(t, doc.Data, func(x *content.Term) { x.Name = "イベント情報" })))
		var problems content.Errors
		if !*done || !errors.Is(err, errRead) || errors.As(err, &problems) || snapshot(t, s) != before {
			t.Errorf("Save() = %v, want the error of the read, not problems, and no change", err)
		}
	})
}

// unitProblems runs the unit that mk makes in a View of s, in the site of the sample with the lookup of the index, and
// returns its problems.
func unitProblems(t *testing.T, ed *Editor, s *store.Store, mk func(r *run) unit) content.Errors {
	t.Helper()
	var es content.Errors
	err := s.View(t.Context(), func(tx *store.Tx) error {
		r := newRun(ed, t.Context(), tx, "")
		site, _, err := r.site()
		if err != nil {
			return err
		}
		es, err = mk(r).check(checkEnv{site: site, lookup: r.lookup})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return es
}

// TestRefChangeUnits checks the validations of single documents and revisions that a change runs again.
func TestRefChangeUnits(t *testing.T) {
	// Bytes that do not decode are not the change's problem: the unit finds none.
	t.Run("bytes that do not decode", func(t *testing.T) {
		ed, s := newSample(t)
		bad := []byte("{")
		for name, mk := range map[string]func(r *run) unit{
			"a document":                     func(*run) unit { return documentUnit(content.RefTerm, newsID, bad) },
			"a published entry":              func(r *run) unit { return r.publishedUnit(aboutJA, bad) },
			"an entry that is not published": func(*run) unit { return stateUnit(draftEN, bad) },
			"the latest revision":            func(r *run) unit { return r.latestUnit(aboutJA, bad, aboutRev) },
		} {
			if got := unitProblems(t, ed, s, mk); len(got) != 0 {
				t.Errorf("the unit of %s has the problems %v, want none", name, got)
			}
		}
	})

	t.Run("a document that the build does not validate as such", func(t *testing.T) {
		ed, s := newSample(t)
		err := s.View(t.Context(), func(tx *store.Tx) error {
			r := newRun(ed, t.Context(), tx, "")
			site, _, err := r.site()
			if err != nil {
				return err
			}
			_, err = documentUnit(content.RefEntry, aboutJA, sampleFile(t, docName(content.RefEntry, aboutJA))).check(checkEnv{site: site, lookup: r.lookup})
			if want := `unsupported kind "entry"`; err == nil || err.Error() != want {
				t.Errorf("the unit of an entry as a document = %v, want the error %q", err, want)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})

	// The build reports a published revision that the store does not have at the entry.
	t.Run("a published revision that the store does not have", func(t *testing.T) {
		x := newSmallSite(t, strings.Replace(smallSettings, "%s", "", 1), nil)
		entry, rev := testID(content.RefEntry, 4), testID(content.RefRevision, 4)
		doc := storedEntry(entry, 704, content.TypePage, "ja", testID(content.RefTranslationGroup, 4), content.StatusPublished, rev)
		writeSite(t, x.dir, docName(content.RefEntry, entry), doc)
		put(t, x.s, content.RefEntry, entry, doc)
		got := lines(unitProblems(t, x.ed, x.s, func(r *run) unit { return r.publishedUnit(entry, []byte(doc)) }))
		want := buildErrors(t, x.dir)
		if len(want) != 1 || !slices.Equal(got, want) || !strings.HasSuffix(got[0], `/publishedRevision: revision "`+rev+`" does not exist`) {
			t.Errorf("the problems of the unit:\n%s\nthe problems of the build:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
	})

	// SaveRevision validated the latest revision with the revision before it, which keeps what that revision had.
	t.Run("the revision before the latest revision", func(t *testing.T) {
		ed, s := newSample(t)
		entry := newDraft(t, ed, content.TypePage, "ja")
		const legacy = `{"type":"legacy","id":"b_00000001","attrs":{"html":"<p>old</p>","reason":"imported"}}`
		page := func(title, block string) string {
			return `{"schemaVersion":1,"title":"` + title + `","slug":"a","body":[` + block + `]}`
		}
		first, kept, changed := testID(content.RefRevision, 1), testID(content.RefRevision, 2), testID(content.RefRevision, 3)
		addStoredRevision(t, s, entry, first, page("a", legacy))
		addStoredRevision(t, s, entry, kept, page("b", legacy))
		newLegacy := strings.Replace(legacy, "old", "new", 1)
		addStoredRevision(t, s, entry, changed, page("c", newLegacy))
		doc := read(t, s, content.RefEntry, entry).Data
		latest := func(rev string) func(r *run) unit {
			return func(r *run) unit { return r.latestUnit(entry, doc, rev) }
		}
		const problem = "/body/0: legacy blocks can only be created by import"
		if got := unitProblems(t, ed, s, latest(kept)); len(got) != 0 {
			t.Errorf("the revision that keeps the legacy block of the one before it has the problems %v, want none", got)
		}
		for rev, text := range map[string]string{first: page("a", legacy), changed: page("c", newLegacy)} {
			want := where(t, revisionName(rev), text, `{"type":"legacy"`) + problem
			if got := lines(unitProblems(t, ed, s, latest(rev))); !slices.Equal(got, []string{want}) {
				t.Errorf("the problems of %s:\n%s\nwant\n%s", rev, strings.Join(got, "\n"), want)
			}
		}
	})

	// The build validates a published revision as an import and without the theme, so that what saving accepted stays valid.
	t.Run("a published revision that saving accepted", func(t *testing.T) {
		revision := `{"schemaVersion":1,"title":"ホーム","slug":"home","body":[` +
			`{"type":"legacy","id":"b_00000001","attrs":{"html":"<p>old</p>","reason":"imported"}},` +
			`{"type":"paragraph","id":"b_00000003","style":{"textColor":{"value":"#ff0000"}},"content":[{"type":"text","text":"x"}]}]}`
		x := newSmallSite(t, strings.Replace(smallSettings, "%s", "", 1), map[string]string{revisionName(testID(content.RefRevision, 1)): revision})
		doc := read(t, x.s, content.RefEntry, x.page).Data
		if got := unitProblems(t, x.ed, x.s, func(r *run) unit { return r.publishedUnit(x.page, doc) }); len(got) != 0 {
			t.Errorf("the unit of the published revision has the problems %v, want none", got)
		}
		if got := buildErrors(t, x.dir); len(got) != 0 {
			t.Errorf("the build of the site fails: %q", got)
		}
	})

	// SaveRevision validated the latest revision as a draft: without what publishing needs, and with the theme.
	t.Run("a draft as SaveRevision validates it", func(t *testing.T) {
		ed, s := newSample(t)
		entry := newDraft(t, ed, content.TypePage, "ja")
		bare, red := testID(content.RefRevision, 6), testID(content.RefRevision, 7)
		const styled = `{"schemaVersion":1,"body":[{"type":"paragraph","id":"b_00000003","style":{"textColor":{"value":"#ff0000"}},"content":[{"type":"text","text":"x"}]}]}`
		addStoredRevision(t, s, entry, bare, `{"schemaVersion":1}`)
		addStoredRevision(t, s, entry, red, styled)
		doc := read(t, s, content.RefEntry, entry).Data
		latest := func(rev string) func(r *run) unit {
			return func(r *run) unit { return r.latestUnit(entry, doc, rev) }
		}
		if got := unitProblems(t, ed, s, latest(bare)); len(got) != 0 {
			t.Errorf("the draft without what publishing needs has the problems %v, want none", got)
		}
		want := where(t, revisionName(red), styled, `"value"`) + "/body/0/style/textColor/value: the theme allows only presets for color"
		if got := lines(unitProblems(t, ed, s, latest(red))); !slices.Equal(got, []string{want}) {
			t.Errorf("the problems of the draft with a style that the theme does not allow:\n%s\nwant\n%s", strings.Join(got, "\n"), want)
		}
	})

	// The build does not read the revision of an entry whose state is not valid, nor does the unit, and the latest revision of
	// such an entry is not the problem of the unit of the revision.
	t.Run("an entry whose state is not valid", func(t *testing.T) {
		x := newSmallSite(t, strings.Replace(smallSettings, "%s", "", 1), nil)
		entry, rev := testID(content.RefEntry, 5), testID(content.RefRevision, 5)
		doc := storedEntry(entry, 705, "nope", "ja", testID(content.RefTranslationGroup, 5), content.StatusPublished, rev)
		const revision = `{"schemaVersion":1,"title":"a\tb","slug":"a"}`
		writeSite(t, x.dir, docName(content.RefEntry, entry), doc)
		writeSite(t, x.dir, revisionName(rev), revision)
		put(t, x.s, content.RefEntry, entry, doc)
		addStoredRevision(t, x.s, entry, rev, revision)

		want := buildErrors(t, x.dir)
		wantLine := where(t, docName(content.RefEntry, entry), doc, `"type"`) + `/type: unknown type "nope"`
		for name, mk := range map[string]func(r *run) unit{
			"published":     func(r *run) unit { return r.publishedUnit(entry, []byte(doc)) },
			"not published": func(*run) unit { return stateUnit(entry, []byte(doc)) },
		} {
			if got := lines(unitProblems(t, x.ed, x.s, mk)); !slices.Equal(got, []string{wantLine}) || !slices.Equal(got, want) {
				t.Errorf("the problems of the unit of the entry as %s:\n%s\nwant\n%s\nand the build's:\n%s", name, strings.Join(got, "\n"), wantLine, strings.Join(want, "\n"))
			}
		}
		if got := unitProblems(t, x.ed, x.s, func(r *run) unit { return r.latestUnit(entry, []byte(doc), rev) }); len(got) != 0 {
			t.Errorf("the latest revision of an entry whose state is not valid has the problems %v, want none", got)
		}
	})
}

// changeRef changes the document of kind with id by change with the store's own API, and then does what Save does for it:
// it checks what its Ref has changed in an Update that ends with the error of the check, which keeps the change out.
func changeRef(t *testing.T, ed *Editor, s *store.Store, kind content.RefKind, id string, change func(data []byte) string) (content.Errors, error) {
	t.Helper()
	var warnings content.Errors
	err := s.Update(t.Context(), func(tx *store.Tx) error {
		r := newRun(ed, t.Context(), tx, userID)
		site, _, err := r.site()
		if err != nil {
			return err
		}
		old, err := r.oldSummary(kind, id)
		if err != nil {
			return err
		}
		doc, err := tx.Document(kind, id)
		if err != nil {
			return err
		}
		if _, err = tx.Put(kind, id, []byte(change(doc.Data)), userID); err != nil {
			return err
		}
		warnings, err = r.checkRefChange(site, kind, id, old)
		return err
	})
	return warnings, err
}

// TestRefChangeKinds checks the validation again of what refers to a medium and to an entry, whose Ref no operation
// changes: a medium that becomes a PDF file is not an image any more, and an entry in another language is not the front
// page of the language. As for a term, the problems are the build's for a copy of the site with the change.
func TestRefChangeKinds(t *testing.T) {
	asPDF := func(data []byte) string {
		var m content.Media
		if err := m.UnmarshalJSON(data); err != nil {
			t.Fatal(err)
		}
		m.MIMEType, m.Width, m.Height, m.Alt, m.Caption = "application/pdf", 0, 0, nil, nil
		out, err := m.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}
	inEnglish := func(data []byte) string {
		var e content.Entry
		if err := e.UnmarshalJSON(data); err != nil {
			t.Fatal(err)
		}
		e.Language = "en"
		out, err := e.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}
	for _, tt := range []struct {
		name   string
		kind   content.RefKind
		id     string
		change func(data []byte) string
		file   string // the file of the first problem
	}{
		{"a medium that images use", content.RefMedia, "med_01JB8Z3K5QH2V6T9C4R7Y1E8B5", asPDF, "revisions/"},
		{"the logo of the site", content.RefMedia, "med_01JBG3N6V3X6Z9C2E5G8J1K4M7", asPDF, settingsName},
		{"the front page of the site", content.RefEntry, homeJA, inEnglish, settingsName},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := newParity(t)
			_, err := changeRef(t, p.ed, p.s, tt.kind, tt.id, func(data []byte) string {
				changed := tt.change(data)
				p.write(docName(tt.kind, tt.id), changed)
				return changed
			})
			got := problemLines(t, err)
			want := buildErrors(t, p.dir)
			if !strings.HasPrefix(got[0], tt.file) || !slices.Equal(got, want) {
				t.Errorf("the problems of the check:\n%s\nthe problems of the build:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
			t.Logf("the problems:\n%s", strings.Join(got, "\n"))
		})
	}
}

// TestRefChangeGrid saves each term of the sample site with each language and taxonomy that the site has, and compares the
// outcome with the build's for a copy of the site with the term as saved: the problems that the Editor refuses it with are
// some of those that the build finds, and a term that it saves is one that the build builds. They are not all of them when
// the term is not valid itself, as the other documents are not validated then.
func TestRefChangeGrid(t *testing.T) {
	terms := []string{"trm_01JB9A00000000000000000001", newsID, "trm_01JBA4C7F3H6K9N2Q5S8V1X4Z7", tagJA, tagEN}
	for _, id := range terms {
		var term content.Term
		if err := term.UnmarshalJSON(sampleFile(t, docName(content.RefTerm, id))); err != nil {
			t.Fatal(err)
		}
		for _, language := range []string{"ja", "en"} {
			for _, taxonomy := range []string{"category", "tag", "works_category"} {
				if language == term.Language && taxonomy == term.Taxonomy {
					continue
				}
				t.Run(term.Slug+"/"+language+"/"+taxonomy, func(t *testing.T) {
					t.Parallel()
					p := newParity(t)
					name := docName(content.RefTerm, id)
					data := changedTerm(t, sampleFile(t, name), func(x *content.Term) { x.Language, x.Taxonomy = language, taxonomy })
					p.write(name, data)
					_, err := p.ed.Save(t.Context(), Op{By: userID}, content.RefTerm, id, p.seq(content.RefTerm, id), []byte(data))
					want := buildErrors(t, p.dir)
					var problems content.Errors
					switch {
					case err == nil:
						if len(want) > 0 {
							t.Errorf("the term was saved, and the build refuses the site:\n%s", strings.Join(want, "\n"))
						}
					case !errors.As(err, &problems):
						t.Fatalf("Save() = %v", err)
					default:
						for _, l := range lines(problems) {
							if !slices.Contains(want, l) {
								t.Errorf("the Editor refuses the term with %q, which the build's problems do not have:\n%s", l, strings.Join(want, "\n"))
							}
						}
					}
				})
			}
		}
	}
}
