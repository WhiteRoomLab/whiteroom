package edit

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

// parity is a change that a test makes to the sample site twice: with an Editor of a store that has it, and to a copy
// of its directory, which the build reads.
type parity struct {
	t    *testing.T
	ed   *Editor
	s    *store.Store
	dir  string
	base string // the snapshot of the store that the change that fails must leave
}

func newParity(t *testing.T) *parity {
	ed, s := newSample(t)
	return &parity{t: t, ed: ed, s: s, dir: copySite(t), base: snapshot(t, s)}
}

func (p *parity) write(name, data string) {
	p.t.Helper()
	if err := os.WriteFile(filepath.Join(p.dir, filepath.FromSlash(name)), []byte(data), 0o644); err != nil {
		p.t.Fatal(err)
	}
}

// writeNew puts the document that Create makes of data, the JSON without an id, into the copy: the document with the ID
// that the Editor will make, which makes the same file; its bytes differ from the store's, so its lines differ.
func (p *parity) writeNew(kind content.RefKind, id, data string) string {
	p.t.Helper()
	name := docName(kind, id)
	p.write(name, replaceOnce(p.t, data, "\"schemaVersion\": 1,\n", "\"schemaVersion\": 1,\n  \"id\": \""+id+"\",\n"))
	return name
}

// copyStored puts the document of kind with id, as the store has it, into the copy.
func (p *parity) copyStored(kind content.RefKind, id string) {
	p.t.Helper()
	p.write(docName(kind, id), string(read(p.t, p.s, kind, id).Data))
}

// seq returns the current version of the document of kind with id.
func (p *parity) seq(kind content.RefKind, id string) int64 {
	p.t.Helper()
	return read(p.t, p.s, kind, id).Seq
}

func predictIDsAt(t *testing.T, now time.Time, kinds ...content.RefKind) []string {
	t.Helper()
	e := testEditor(t, nil)
	e.now = func() time.Time { return now }
	ids := make([]string, len(kinds))
	for i, kind := range kinds {
		ids[i] = mustID(t, e, kind)
	}
	return ids
}

// TestSaveParity makes changes that the build refuses to the sample site, with an Editor and in a copy of its directory:
// the problems are the same lines, with the positions in the files. The store has the bytes of the files, so the lines of
// the problems in documents that the Editor has not been given are those of the build. For a document that it makes, the
// bytes that the Editor was given lack the id that the file has, so its lines are compared without their positions.
func TestSaveParity(t *testing.T) {
	op := Op{By: userID}
	const (
		pressID = "trm_01JBA4C7F3H6K9N2Q5S8V1X4Z7"
		eventID = "trm_01JBA4C7G4J7M1P4R7T1W4Y7B1"
		draftID = "ent_01JC0000000000000000000003"
	)
	for _, tt := range []struct {
		name string
		// change makes the change that the build refuses, with the Editor, which must fail, and in the copy of the site
		// directory. It returns the name of the file of the document that Create has made, or "".
		change func(p *parity) (loose string, err error)
	}{
		{"a term slug that is taken, by a term with a later ID", func(p *parity) (string, error) {
			data := termJSON("category", "ja", testID(content.RefTranslationGroup, 1), "お知らせ2", "news")
			name := p.writeNew(content.RefTerm, predictIDs(t, content.RefTerm)[0], data)
			_, err := p.ed.Create(t.Context(), op, content.RefTerm, []byte(data))
			return name, err
		}},
		{"a term slug that is taken, by a term with an earlier ID", func(p *parity) (string, error) {
			// The problem goes to the term that the sample has, which the Editor has not been given.
			past := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
			p.ed.now = func() time.Time { return past }
			data := termJSON("category", "ja", testID(content.RefTranslationGroup, 1), "お知らせ2", "news")
			name := p.writeNew(content.RefTerm, predictIDsAt(t, past, content.RefTerm)[0], data)
			_, err := p.ed.Create(t.Context(), op, content.RefTerm, []byte(data))
			return name, err
		}},
		{"a cycle of the parents of terms", func(p *parity) (string, error) {
			data := replaceOnce(t, string(sampleFile(t, newsName)), `"slug": "news",`, `"slug": "news",`+"\n"+`  "parent": "`+pressID+`",`)
			p.write(newsName, data)
			_, err := p.ed.Save(t.Context(), op, content.RefTerm, newsID, p.seq(content.RefTerm, newsID), []byte(data))
			return "", err
		}},
		{"a term in a translation group that has a term in its language", func(p *parity) (string, error) {
			data := termJSON("tag", "ja", "tg_01JBA4C7M8P2S5V9Y2B6D9G3J6", "お祭り", "festival")
			name := p.writeNew(content.RefTerm, predictIDs(t, content.RefTerm)[0], data)
			_, err := p.ed.Create(t.Context(), op, content.RefTerm, []byte(data))
			return name, err
		}},
		{"an author slug that is taken", func(p *parity) (string, error) {
			data := authorJSON("山田 次郎", "yamada")
			name := p.writeNew(content.RefAuthor, predictIDs(t, content.RefAuthor)[0], data)
			_, err := p.ed.Create(t.Context(), op, content.RefAuthor, []byte(data))
			return name, err
		}},
		{"a second menu at a location", func(p *parity) (string, error) {
			data := menuJSON("header", "ja", `[{ "label": "ホーム", "href": "/" }]`)
			name := p.writeNew(content.RefMenu, predictIDs(t, content.RefMenu)[0], data)
			_, err := p.ed.Create(t.Context(), op, content.RefMenu, []byte(data))
			return name, err
		}},
		{"two redirects with one from", func(p *parity) (string, error) {
			data := redirectJSON("/company", "/")
			name := p.writeNew(content.RefRedirect, predictIDs(t, content.RefRedirect)[0], data)
			_, err := p.ed.Create(t.Context(), op, content.RefRedirect, []byte(data))
			return name, err
		}},
		{"a redirect loop", func(p *parity) (string, error) {
			ids := predictIDs(t, content.RefRedirect, content.RefRedirect)
			first := must(t)(p.ed.Create(t.Context(), op, content.RefRedirect, []byte(redirectJSON("/loop-a/", "/loop-b/"))))
			if first.ID != ids[0] {
				t.Fatalf("Create() made the ID %s, want %s", first.ID, ids[0])
			}
			p.copyStored(content.RefRedirect, first.ID)
			p.base = snapshot(t, p.s)
			data := redirectJSON("/loop-b/", "/loop-a/")
			name := p.writeNew(content.RefRedirect, ids[1], data)
			_, err := p.ed.Create(t.Context(), op, content.RefRedirect, []byte(data))
			return name, err
		}},
		{"a redirect from the path of a page", func(p *parity) (string, error) {
			data := redirectJSON("/about/", "/")
			name := p.writeNew(content.RefRedirect, predictIDs(t, content.RefRedirect)[0], data)
			_, err := p.ed.Create(t.Context(), op, content.RefRedirect, []byte(data))
			return name, err
		}},
		{"a redirect from the path of a feed", func(p *parity) (string, error) {
			data := redirectJSON("/feed/", "/")
			name := p.writeNew(content.RefRedirect, predictIDs(t, content.RefRedirect)[0], data)
			_, err := p.ed.Create(t.Context(), op, content.RefRedirect, []byte(data))
			return name, err
		}},
		{"a front page that is a draft", func(p *parity) (string, error) {
			data := replaceOnce(t, string(sampleFile(t, settingsName)), `"frontPage": { "ja": "ent_01JBG3N6S1V4X7Z0C3E6G9J2K5" }`,
				`"frontPage": { "en": "`+draftID+`", "ja": "ent_01JBG3N6S1V4X7Z0C3E6G9J2K5" }`)
			p.write(settingsName, data)
			_, err := p.ed.SaveSettings(t.Context(), op, p.seq(store.SettingsKind, ""), []byte(data))
			return "", err
		}},
		{"a term with a slug that is not valid", func(p *parity) (string, error) {
			data := replaceOnce(t, string(sampleFile(t, "terms/"+eventID+".json")), `"slug": "イベント"`, `"slug": "a b"`)
			p.write("terms/"+eventID+".json", data)
			_, err := p.ed.Save(t.Context(), op, content.RefTerm, eventID, p.seq(content.RefTerm, eventID), []byte(data))
			return "", err
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := newParity(t)
			loose, err := tt.change(p)
			got := problemLines(t, err)
			want := buildErrors(t, p.dir)
			if loose != "" {
				got, want = looseLines(t, got, loose), looseLines(t, want, loose)
			}
			if len(want) == 0 || !slices.Equal(got, want) {
				t.Errorf("the problems of the Editor:\n%s\nthe problems of the build:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
			if snap := snapshot(t, p.s); snap != p.base {
				t.Errorf("the change that failed has changed the store:\n%s\nwant\n%s", snap, p.base)
			}
			t.Logf("the problems:\n%s", strings.Join(got, "\n"))
		})
	}
}

// looseLines returns lines with the positions of those about the file name taken off; there must be at most one of those,
// so that the lines stay in their order.
func looseLines(t *testing.T, lines []string, name string) []string {
	t.Helper()
	out := slices.Clone(lines)
	n := 0
	for i, l := range out {
		if strings.HasPrefix(l, name+":") {
			out[i] = stripPositions([]string{l})[0]
			n++
		}
	}
	if n > 1 {
		t.Fatalf("%d lines about %s, want at most one: %q", n, name, lines)
	}
	return out
}

// failing wraps a reader so that the reads that have an error fail with it.
type failing struct {
	reader
	summary, summaries, referrers error
}

func (f failing) Summary(kind content.RefKind, id string) (store.Summary, error) {
	if f.summary != nil {
		return store.Summary{}, f.summary
	}
	return f.reader.Summary(kind, id)
}

func (f failing) Summaries(kind content.RefKind) ([]store.Summary, error) {
	if f.summaries != nil {
		return nil, f.summaries
	}
	return f.reader.Summaries(kind)
}

func (f failing) Referrers(kind content.RefKind, id string) ([]store.Referrer, error) {
	if f.referrers != nil {
		return nil, f.referrers
	}
	return f.reader.Referrers(kind, id)
}

// failReads returns the wrap of an Editor that makes the reads of f fail in the transaction numbered only, counted from 1
// in the order in which an operation opens them: its Update, and then the View of the site before the change; in every
// transaction if only is 0.
func failReads(only int, f failing) func(reader) reader {
	transactions := 0
	return func(r reader) reader {
		transactions++
		if only != 0 && transactions != only {
			return r
		}
		f.reader = r
		return f
	}
}

// TestLookupFailure checks that a failure to read the index fails the operation with that error, not with problems of
// validation, and saves nothing.
func TestLookupFailure(t *testing.T) {
	errRead := errors.New("the index cannot be read")
	op := Op{By: userID}
	news := replaceOnce(t, string(sampleFile(t, newsName)), "会社からのお知らせです。", "お知らせです。")
	settings := replaceOnce(t, string(sampleFile(t, settingsName)), "Webサイトの企画と制作", "Webサイトの企画・制作")
	const rdr = "rdr_01JBD3A4B5C6D7E8F9G0H1J2K3"

	calls := map[string]func(ed *Editor, s *store.Store) (Result, error){
		"Save": func(ed *Editor, s *store.Store) (Result, error) {
			return ed.Save(t.Context(), op, content.RefTerm, newsID, read(t, s, content.RefTerm, newsID).Seq, []byte(news))
		},
		"Create": func(ed *Editor, s *store.Store) (Result, error) {
			return ed.Create(t.Context(), op, content.RefRedirect, []byte(redirectJSON("/old-contact/", "/contact/")))
		},
		"Delete": func(ed *Editor, s *store.Store) (Result, error) {
			return ed.Delete(t.Context(), op, content.RefRedirect, rdr, read(t, s, content.RefRedirect, rdr).Seq)
		},
		"SaveSettings": func(ed *Editor, s *store.Store) (Result, error) {
			return ed.SaveSettings(t.Context(), op, read(t, s, store.SettingsKind, "").Seq, []byte(settings))
		},
		// Two redirects with one from, which needs the site before the change to tell that the problem is new.
		"Create of a problem": func(ed *Editor, s *store.Store) (Result, error) {
			return ed.Create(t.Context(), op, content.RefRedirect, []byte(redirectJSON("/company", "/")))
		},
	}
	every := []string{"Save", "Create", "Delete", "SaveSettings", "Create of a problem"}
	for _, tt := range []struct {
		name  string
		wrap  func() func(reader) reader
		fails []string // the calls that fail with the error; the others succeed, but Create of a problem has its problem
	}{
		{"the summary of a document", func() func(reader) reader { return failReads(0, failing{summary: errRead}) }, every},
		{"the summaries of the documents", func() func(reader) reader { return failReads(0, failing{summaries: errRead}) }, every},
		{"the referrers of a document", func() func(reader) reader { return failReads(0, failing{referrers: errRead}) }, []string{"Delete"}},
		// The sample site has a redirect to an entry that is not published, whose warning is old, so every operation
		// makes the site before the change to tell that its warnings are not new.
		{"the summaries for the site before the change", func() func(reader) reader {
			return failReads(2, failing{summaries: errRead})
		}, every},
	} {
		for name, call := range calls {
			t.Run(tt.name+"/"+name, func(t *testing.T) {
				ed, s := newSample(t)
				ed.wrap = tt.wrap()
				before := snapshot(t, s)
				res, err := call(ed, s)
				var problems content.Errors
				switch {
				case slices.Contains(tt.fails, name):
					if !errors.Is(err, errRead) || errors.As(err, &problems) || snapshot(t, s) != before {
						t.Errorf("%s() = %+v, %v; want the error of the read, not problems, and no change", name, res, err)
					}
				case name == "Create of a problem":
					if !errors.As(err, &problems) {
						t.Errorf("%s() = %+v, %v; want its problem", name, res, err)
					}
				case err != nil:
					t.Errorf("%s() error = %v, want none", name, err)
				}
			})
		}
	}
}

func testOpID(n int) string {
	return opPrefix + strings.TrimPrefix(testID(content.RefEntry, n), "ent_")
}

// TestOperationID checks the operation IDs: a retry returns the first result and saves nothing; the same ID with another
// request fails; one that failed has recorded nothing.
func TestOperationID(t *testing.T) {
	ed, s := newSample(t)
	user := Op{By: userID, ID: testOpID(1)}
	data := redirectJSON("/old-contact/", "/contact/")

	first, err := ed.Create(t.Context(), user, content.RefRedirect, []byte(data))
	if err != nil {
		t.Fatal(err)
	}
	after := snapshot(t, s)
	again, err := ed.Create(t.Context(), user, content.RefRedirect, []byte(data))
	if err != nil || !reflect.DeepEqual(first, again) || snapshot(t, s) != after {
		t.Errorf("a retry of Create() = %+v, %v; want the first result %+v and no change", again, err, first)
	}

	wantErr := fmt.Sprintf("operation ID %q was used for another request", user.ID)
	for _, tt := range []struct {
		name string
		call func() (Result, error)
	}{
		{"other data", func() (Result, error) {
			return ed.Create(t.Context(), user, content.RefRedirect, []byte(redirectJSON("/old-contact/", "/about/")))
		}},
		{"another kind", func() (Result, error) {
			return ed.Create(t.Context(), user, content.RefAuthor, []byte(data))
		}},
		{"another user", func() (Result, error) {
			return ed.Create(t.Context(), Op{By: testID(content.RefUser, 1), ID: user.ID}, content.RefRedirect, []byte(data))
		}},
		{"another operation", func() (Result, error) {
			return ed.Delete(t.Context(), user, content.RefRedirect, first.ID, first.Version.Seq)
		}},
	} {
		res, err := tt.call()
		if err == nil || err.Error() != wantErr || snapshot(t, s) != after {
			t.Errorf("%s: the same operation ID = %+v, %v; want the error %q and no change", tt.name, res, err, wantErr)
		}
	}

	// An operation that has failed has recorded nothing: its retry runs, and the ID is free.
	failed := Op{By: userID, ID: testOpID(2)}
	bad := replaceOnce(t, redirectJSON("/old-contact/", "/contact/"), `"/old-contact/"`, `"old-contact"`)
	var problems content.Errors
	for range 2 {
		if _, err := ed.Create(t.Context(), failed, content.RefRedirect, []byte(bad)); !errors.As(err, &problems) {
			t.Errorf("Create() of a redirect that does not validate = %v, want its problems", err)
		}
	}
	if snapshot(t, s) != after {
		t.Error("an operation that failed has changed the store")
	}
	ok, err := ed.Create(t.Context(), failed, content.RefRedirect, []byte(redirectJSON("/other/", "/contact/")))
	if err != nil || ok.ID == first.ID {
		t.Errorf("Create() with the ID of an operation that failed = %+v, %v; want a new redirect", ok, err)
	}
}

// TestOperationIDResult checks that the result that a retry returns is the whole first one, with its warnings, also
// when what it did cannot be done again.
func TestOperationIDResult(t *testing.T) {
	ed, s := newSample(t)
	op := Op{By: userID, ID: testOpID(3)}
	tag := must(t)(ed.Create(t.Context(), Op{By: userID}, content.RefTerm, []byte(termJSON("tag", "ja", "", "お祭り", "festival")))).ID
	const draftEntry = "ent_01JC0000000000000000000003"
	err := s.Update(t.Context(), func(tx *store.Tx) error {
		_, err := tx.AddRevision(draftEntry, testID(content.RefRevision, 1),
			[]byte(`{"schemaVersion": 1, "title": "Draft", "slug": "draft", "terms": {"tag": ["`+tag+`"]}}`), userID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	seq := read(t, s, content.RefTerm, tag).Seq
	first, err := ed.Delete(t.Context(), op, content.RefTerm, tag, seq)
	if err != nil || len(first.Warnings) != 1 {
		t.Fatalf("Delete() = %+v, %v; want a result with a warning", first, err)
	}
	after := snapshot(t, s)
	again, err := ed.Delete(t.Context(), op, content.RefTerm, tag, seq)
	if err != nil || !reflect.DeepEqual(first, again) || snapshot(t, s) != after {
		t.Errorf("a retry of Delete() = %+v, %v; want the first result %+v and no change", again, err, first)
	}
	// What the store keeps is the hash of the request and the result.
	err = s.View(t.Context(), func(tx *store.Tx) error {
		data, ok, err := tx.Operation(op.ID)
		if err != nil || !ok || !strings.HasPrefix(string(data), `{"request":"sha256:`) ||
			!strings.Contains(string(data), `"warnings":[{"file":"`) {
			t.Errorf("Operation() = %s, %t, %v; want the record of the request and its result", data, ok, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRequestHash(t *testing.T) {
	base := request{name: "save", params: []string{"term", "trm_a", "1"}, data: []byte("data")}
	same := request{name: "save", params: []string{"term", "trm_a", "1"}, data: []byte("data")}
	if base.hash(userID) != same.hash(userID) || !strings.HasPrefix(base.hash(userID), "sha256:") {
		t.Errorf("the hash of a request = %q, and of its copy %q", base.hash(userID), same.hash(userID))
	}
	hashes := map[string]string{base.hash(userID): "the request"}
	for name, q := range map[string]request{
		"another name":          {name: "delete", params: base.params, data: base.data},
		"a parameter":           {name: "save", params: []string{"term", "trm_b", "1"}, data: base.data},
		"fewer parameters":      {name: "save", params: base.params[:2], data: base.data},
		"the data":              {name: "save", params: base.params, data: []byte("datum")},
		"no data":               {name: "save", params: base.params},
		"a parameter joined":    {name: "save", params: []string{"termtrm_a", "1"}, data: base.data},
		"a parameter as data":   {name: "save", params: base.params[:2], data: []byte("1data")},
		"the name as parameter": {name: "", params: append([]string{"save"}, base.params...), data: base.data},
	} {
		h := q.hash(userID)
		if other, ok := hashes[h]; ok {
			t.Errorf("%s has the hash of %s", name, other)
		}
		hashes[h] = name
	}
	if h := base.hash(testID(content.RefUser, 1)); hashes[h] != "" {
		t.Errorf("another user has the hash of %s", hashes[h])
	}
}

// TestConcurrentCreate checks that an Editor can be used from several goroutines: every operation succeeds and makes a
// document with an ID of its own.
func TestConcurrentCreate(t *testing.T) {
	ed, s := newSample(t)
	const goroutines, each = 8, 5
	var mu sync.Mutex
	ids := make(map[string]bool)
	var wg sync.WaitGroup
	for g := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range each {
				from := fmt.Sprintf("/c-%d-%d/", g, i)
				res, err := ed.Create(t.Context(), Op{By: userID}, content.RefRedirect, []byte(redirectJSON(from, "/contact/")))
				if err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				ids[res.ID] = true
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	var n int
	err := s.View(t.Context(), func(tx *store.Tx) error {
		sums, err := tx.Summaries(content.RefRedirect)
		n = len(sums)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != goroutines*each || n != 7+goroutines*each {
		t.Errorf("%d IDs and %d redirects in the store, want %d and %d", len(ids), n, goroutines*each, 7+goroutines*each)
	}
}
