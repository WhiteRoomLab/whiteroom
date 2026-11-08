package edit

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/build"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/sitedir"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

// More documents of the sample site that the tests of the import change.
const (
	pressTerm    = "trm_01JBA4C7F3H6K9N2Q5S8V1X4Z7" // a category in ja, a child of newsID
	pressName    = "terms/" + pressTerm + ".json"
	aboutGroupID = "tg_01JB8X4M7NB5D8F1H4J7K2M6P9"  // the translation group of aboutJA and aboutEN
	tagGroupID   = "tg_01JBA4C7M8P2S5V9Y2B6D9G3J6"  // that of tagJA and tagEN
	servicesRev  = "rev_01JB8Y6T2RG7M4XK9P2D5W8NQ3" // the published revision of servicesJA
	contactRev   = "rev_01JBD1Y2Z3A4B5C6D7E8F9G0H1" // that of contactJA
	aboutENRev   = "rev_01JB8Y6T2VS4W7Z2B5D8F1H6J9" // that of aboutEN
)

// writeSite puts data into the file name of the site directory dir, which it makes if it is missing.
func writeSite(t *testing.T, dir, name, data string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

// changeSite replaces the one occurrence of old in the file name of the site directory dir with new.
func changeSite(t *testing.T, dir, name, old, new string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	writeSite(t, dir, name, replaceOnce(t, string(data), old, new))
}

func removeSite(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.Remove(filepath.Join(dir, filepath.FromSlash(name))); err != nil {
		t.Fatal(err)
	}
}

// addDoc puts the document of kind with id, whose JSON data has no id, into the site directory dir, with the id that the
// files of a site directory have.
func addDoc(t *testing.T, dir string, kind content.RefKind, id, data string) {
	t.Helper()
	writeSite(t, dir, docName(kind, id), replaceOnce(t, data, "\"schemaVersion\": 1,\n", "\"schemaVersion\": 1,\n  \"id\": \""+id+"\",\n"))
}

// addPage puts a published page in ja with the slug slug, which is also its title, into the site directory dir: its entry
// and its revision have the IDs testID gives for n.
func addPage(t *testing.T, dir string, n int, slug string) {
	t.Helper()
	entry, rev := testID(content.RefEntry, n), testID(content.RefRevision, n)
	writeSite(t, dir, docName(content.RefEntry, entry),
		storedEntry(entry, 300+n, content.TypePage, "ja", testID(content.RefTranslationGroup, n), content.StatusPublished, rev))
	writeSite(t, dir, revisionName(rev), `{"schemaVersion":1,"title":"`+slug+`","slug":"`+slug+`"}`)
}

// buildErrorsWith builds the site directory dir with the theme th into temporary directories and returns the lines of its
// error, as buildErrors does with the example theme.
func buildErrorsWith(t *testing.T, dir string, th fs.FS) []string {
	t.Helper()
	d, err := sitedir.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	_, err = build.Build(t.Context(), build.Options{
		Source: source{Dir: d, theme: th}, Out: t.TempDir(), CacheDir: t.TempDir(), Workers: 2,
	})
	if err == nil {
		return nil
	}
	return problemLines(t, err)
}

// importMatchesBuild imports the site directory dir, which the build refuses, into a new store, and checks that the Import
// fails with the lines of the build's error, positions included, one of which has msg, and stores no document. It
// returns the lines.
func importMatchesBuild(t *testing.T, dir, msg string) []string {
	t.Helper()
	return matchBuild(t, dir, os.DirFS(themeDir), msg)
}

// matchBuild is importMatchesBuild for the site directory dir with the theme th.
func matchBuild(t *testing.T, dir string, th fs.FS, msg string) []string {
	t.Helper()
	want := buildErrorsWith(t, dir, th)
	if len(want) == 0 || !strings.Contains(strings.Join(want, "\n"), msg) {
		t.Fatalf("the problems of the build:\n%s\nwant a problem with %q", strings.Join(want, "\n"), msg)
	}
	s := newStore(t)
	warnings, err := Import(t.Context(), s, openSiteWith(t, dir, th), userID)
	got := problemLines(t, err)
	if !slices.Equal(got, want) || warnings != nil {
		t.Errorf("the problems of Import() (with warnings %v):\n%s\nthe problems of the build:\n%s", warnings, strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	checkNoImport(t, s)
	return got
}

// checkNoImport checks that nothing of an Import that failed is in s: no document and no revision. What it has left,
// originals that nothing uses, is allowed (TestImportRetry).
func checkNoImport(t *testing.T, s *store.Store) {
	t.Helper()
	if snap := snapshot(t, s); snap != "" {
		t.Errorf("Import() has stored documents:\n%s", snap)
	}
	if err := s.View(t.Context(), checkEmpty); err != nil {
		t.Errorf("the store is not empty after a failed Import(): %v", err)
	}
}

// TestImportParity imports copies of the sample site that each have a problem that the build refuses: one for each rule of
// the checks across the documents (6.7 1 to 11), which the Import finds in its Update on the model of the index, and some
// of validation, which it finds on the documents that it has read. It fails with the lines of the build's error, the files
// and the positions in them included, and stores no document.
func TestImportParity(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name   string
		msg    string // in a line of the error, which says that the problem is the one that the variant means
		change func(t *testing.T, dir string)
	}{
		// 6.7 1: the front page and the posts page are published.
		{"a front page that is a draft", `/frontPage/en: entry "` + draftEN + `" is not published`, func(t *testing.T, dir string) {
			changeSite(t, dir, settingsName, `"frontPage": { "ja": "ent_01JBG3N6S1V4X7Z0C3E6G9J2K5" }`,
				`"frontPage": { "en": "`+draftEN+`", "ja": "ent_01JBG3N6S1V4X7Z0C3E6G9J2K5" }`)
		}},
		// 6.7 2: the slugs of entries.
		{"a page slug that another page has", `/slug: slug "about" is also used by revisions/` + aboutRev + `.json`, func(t *testing.T, dir string) {
			changeSite(t, dir, revisionName(contactRev), `"slug": "contact"`, `"slug": "about"`)
		}},
		// 6.7 3: the slugs of terms.
		{"a term slug that is taken", `/slug: slug "news" is also used by terms/` + newsID + `.json`, func(t *testing.T, dir string) {
			addDoc(t, dir, content.RefTerm, testID(content.RefTerm, 1),
				termJSON("category", "ja", testID(content.RefTranslationGroup, 1), "お知らせ2", "news"))
		}},
		// 6.7 4: the slugs of authors.
		{"an author slug that is taken", `/slug: slug "yamada" is also used by authors/`, func(t *testing.T, dir string) {
			addDoc(t, dir, content.RefAuthor, testID(content.RefAuthor, 1), authorJSON("山田 次郎", "yamada"))
		}},
		// 6.7 5: the translation groups.
		{"two entries of a translation group in one language", `already has an entry in the language "ja": entries/` + aboutJA + `.json`, func(t *testing.T, dir string) {
			writeSite(t, dir, docName(content.RefEntry, testID(content.RefEntry, 1)),
				storedEntry(testID(content.RefEntry, 1), 200, content.TypePage, "ja", aboutGroupID, content.StatusDraft, ""))
		}},
		{"two terms of a translation group in one language", `already has a term in the language "ja": terms/` + tagJA + `.json`, func(t *testing.T, dir string) {
			addDoc(t, dir, content.RefTerm, testID(content.RefTerm, 1), termJSON("tag", "ja", tagGroupID, "お祭り", "festival"))
		}},
		// 6.7 6: the menus.
		{"two menus at a location", `/location: location "header" already has a menu in the language "ja": menus/`, func(t *testing.T, dir string) {
			addDoc(t, dir, content.RefMenu, testID(content.RefMenu, 1), menuJSON("header", "ja", `[{ "label": "ホーム", "href": "/" }]`))
		}},
		// 6.7 7: the parents of pages are published, which validation does not need.
		{"a published page whose parent is a draft", `/parent: entry "` + draftEN + `" is not published`, func(t *testing.T, dir string) {
			changeSite(t, dir, revisionName(aboutENRev), `"slug": "about",`, `"slug": "about",`+"\n  "+`"parent": "`+draftEN+`",`)
		}},
		// 6.7 8: the cycles of parents.
		{"a cycle of the parents of pages", `/parent: the parents make a cycle: `, func(t *testing.T, dir string) {
			changeSite(t, dir, revisionName(servicesRev), `"slug": "services",`, `"slug": "services",`+"\n  "+`"parent": "`+contactJA+`",`)
			changeSite(t, dir, revisionName(contactRev), `"slug": "contact",`, `"slug": "contact",`+"\n  "+`"parent": "`+servicesJA+`",`)
		}},
		{"a cycle of the parents of terms", `/parent: the parents make a cycle: `, func(t *testing.T, dir string) {
			changeSite(t, dir, docName(content.RefTerm, newsID), `"slug": "news",`, `"slug": "news",`+"\n  "+`"parent": "`+pressTerm+`",`)
		}},
		// 6.7 9: the redirects with one from.
		{"two redirects with one from", `/from: from "/company" matches the same requests as that of redirects/`, func(t *testing.T, dir string) {
			addDoc(t, dir, content.RefRedirect, testID(content.RefRedirect, 1), redirectJSON("/company", "/"))
		}},
		// 6.7 10: the loops of redirects.
		{"a redirect loop", `/to: redirect loop: `, func(t *testing.T, dir string) {
			addDoc(t, dir, content.RefRedirect, testID(content.RefRedirect, 1), redirectJSON("/loop-a/", "/loop-b/"))
			addDoc(t, dir, content.RefRedirect, testID(content.RefRedirect, 2), redirectJSON("/loop-b/", "/loop-a/"))
		}},
		// 6.7 11: the output paths, as the build finds them before it writes the files.
		{"a redirect from the path of a page", `/from: from "/about/" is the output path "/about/" of revisions/` + aboutRev + `.json`, func(t *testing.T, dir string) {
			addDoc(t, dir, content.RefRedirect, testID(content.RefRedirect, 1), redirectJSON("/about/", "/"))
		}},
		{"a post at the path of a page", `/slug: output path "/contact/" is also that of revisions/` + contactRev + `.json`, func(t *testing.T, dir string) {
			changeSite(t, dir, "site/schema.yaml", "url: /{yyyy}/{mm}/{dd}/{slug}/", "url: /{slug}/")
			post, rev := testID(content.RefEntry, 1), testID(content.RefRevision, 1)
			writeSite(t, dir, docName(content.RefEntry, post),
				storedEntry(post, 137, content.TypePost, "ja", testID(content.RefTranslationGroup, 1), content.StatusPublished, rev))
			writeSite(t, dir, revisionName(rev), `{
  "schemaVersion": 1,
  "title": "お問い合わせの受付時間",
  "slug": "contact",
  "date": "2026-10-31T10:00:00+09:00",
  "author": "aut_01JBA4C7N9Q3T6W9Z3C6F9H2K5",
  "terms": { "category": ["`+newsID+`"] },
  "body": [
    {
      "type": "paragraph",
      "id": "b_post0024",
      "content": [{ "type": "text", "text": "お問い合わせは、平日の10時から17時まで受け付けています。" }]
    }
  ]
}
`)
		}},
		{"a page at the path of the feed", `/slug: output path "/feed/" is also that of the feed in ja`, func(t *testing.T, dir string) {
			addPage(t, dir, 1, "feed")
		}},
		// A scheduled post without a date has the scheduled time as its date (TestImportScheduled); a published one has none.
		{"a published post without a date", `/date: missing date`, func(t *testing.T, dir string) {
			post, rev := testID(content.RefEntry, 1), testID(content.RefRevision, 1)
			writeSite(t, dir, docName(content.RefEntry, post),
				storedEntry(post, 137, content.TypePost, "ja", testID(content.RefTranslationGroup, 1), content.StatusPublished, rev))
			writeSite(t, dir, revisionName(rev), `{
  "schemaVersion": 1,
  "title": "Undated",
  "slug": "undated"
}`)
		}},
		// Validation, which the build runs before the checks across the documents: of a document that another changes.
		{"a term in another language than the revisions that use it", `is in the language "en", not "ja"`, func(t *testing.T, dir string) {
			changeSite(t, dir, docName(content.RefTerm, tagJA), `"language": "ja",`, `"language": "en",`)
		}},
		// And of references, which the lookup of the documents answers before there is a store.
		{"a published revision that refers to a medium that does not exist", `does not exist`, func(t *testing.T, dir string) {
			changeSite(t, dir, revisionName(aboutRev), jpegMedium, "med_01JB8Z3K5QH2V6T9C4R7Y1E8B6")
		}},
		{"a menu that refers to an entry that does not exist", `does not exist`, func(t *testing.T, dir string) {
			changeSite(t, dir, docName(content.RefMenu, "mnu_01JBD2M5N6P7Q8R9S0T1V2W3X4"), aboutEN, "ent_01JB8Y6T2VS4W7Z2B5D8F1H6JA")
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := copySite(t)
			tt.change(t, dir)
			t.Logf("the problems:\n%s", strings.Join(importMatchesBuild(t, dir, tt.msg), "\n"))
		})
	}
}

// TestImportedSaveParity makes changes that the build refuses to the sample site as TestSaveParity and TestRefChangeParity
// do, with an Editor of a store that Import has made: the problems are the same lines as the build's for a copy of the
// directory, positions included, as the store has the bytes of the files.
func TestImportedSaveParity(t *testing.T) {
	t.Parallel()
	op := Op{By: userID}
	for _, tt := range []struct {
		name   string
		change func(p *parity) error
	}{
		{"a term slug that is taken", func(p *parity) error {
			data := replaceOnce(t, string(sampleFile(t, pressName)), `"slug": "press-release"`, `"slug": "news"`)
			p.write(pressName, data)
			_, err := p.ed.Save(t.Context(), op, content.RefTerm, pressTerm, p.seq(content.RefTerm, pressTerm), []byte(data))
			return err
		}},
		{"a front page that is a draft", func(p *parity) error {
			data := replaceOnce(t, string(sampleFile(t, settingsName)), `"frontPage": { "ja": "ent_01JBG3N6S1V4X7Z0C3E6G9J2K5" }`,
				`"frontPage": { "en": "`+draftEN+`", "ja": "ent_01JBG3N6S1V4X7Z0C3E6G9J2K5" }`)
			p.write(settingsName, data)
			_, err := p.ed.SaveSettings(t.Context(), op, p.seq(store.SettingsKind, ""), []byte(data))
			return err
		}},
		// The term is valid in itself, and the revisions that use it are not valid with it (Q16-A).
		{"a tag in another language", func(p *parity) error {
			name := docName(content.RefTerm, tagJA)
			data := changedTerm(t, sampleFile(t, name), toEnglish)
			p.write(name, data)
			_, err := p.ed.Save(t.Context(), op, content.RefTerm, tagJA, p.seq(content.RefTerm, tagJA), []byte(data))
			return err
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ed, s := newImported(t)
			p := &parity{t: t, ed: ed, s: s, dir: copySite(t), base: snapshot(t, s)}
			got := problemLines(t, tt.change(p))
			want := buildErrors(t, p.dir)
			if len(want) == 0 || !slices.Equal(got, want) {
				t.Errorf("the problems of the Editor:\n%s\nthe problems of the build:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
			if snap := snapshot(t, s); snap != p.base {
				t.Errorf("the change that failed has changed the store:\n%s\nwant\n%s", snap, p.base)
			}
			t.Logf("the problems:\n%s", strings.Join(got, "\n"))
		})
	}
}
