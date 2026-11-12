package admin

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/account"
	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

// entryWrites returns a request of each operation that makes an entry or changes the entry, with the permission that it needs
// and the target that a refusal names: the creation of an entry, the save of a revision, the creation of a translation and the
// deletion. The entry must be a post with a revision.
func (a *apiServer) entryWrites(t *testing.T, entry string) []write {
	t.Helper()
	doc := a.stored(t, content.RefEntry, entry)
	target := audit.Target{Kind: "entry", ID: entry}
	item := "/api/entries/" + entry
	return []write{
		{"create an entry", http.MethodPost, "/api/entries", `{"type":"post","language":"ja"}`, account.CreateEntry, audit.Target{}},
		{"save a revision", http.MethodPost, item + "/revisions", revisionBody(a.lastRevision(t, entry), postRevision("書き換え", "rewritten", "")),
			account.EditEntry, target},
		{"create a translation", http.MethodPost, item + "/translations", `{"language":"en"}`, account.CreateEntry, target},
		{"delete the entry", http.MethodDelete, item + "?base=" + strconv.FormatInt(doc.Seq, 10), "", account.DeleteEntry, target},
	}
}

// allowHTML saves the settings with allowHtml, as the administrator.
func (a *apiServer) allowHTML(t *testing.T) {
	t.Helper()
	doc := a.stored(t, store.SettingsKind, "")
	changed := strings.Replace(string(doc.Data), `"defaultLanguage": "ja",`, "\"defaultLanguage\": \"ja\",\n  \"allowHtml\": true,", 1)
	if changed == string(doc.Data) {
		t.Fatal("the settings of the sample site have no defaultLanguage to put allowHtml after")
	}
	result(t, "PUT /api/settings", a.as("admin").put("/api/settings", saveBody(doc.Seq, changed)), http.StatusOK)
}

// sampleEntry is a post of the sample site, made by the importer, who has no account.
const sampleEntry = "ent_01JD0000000000000000000001"

// TestEntriesViewer shows that a viewer reads every entry and changes none, and that each refusal is recorded.
func TestEntriesViewer(t *testing.T) {
	a := newAPIServer(t)
	as := a.as("viewer")
	for _, path := range []string{
		"/api/entries", "/api/entries/" + sampleEntry, "/api/entries/" + sampleEntry + "/revisions", "/api/revisions/rev_01JD0000000000000000000001",
	} {
		body(t, "GET "+path+" as a viewer", as.get(path), http.StatusOK)
	}
	if got := a.records(t, audit.AccessDenied); len(got) != 0 {
		t.Fatalf("reading made %d records of access.denied, want none", len(got))
	}

	revs := a.revisionIDs(t, sampleEntry)
	writes := a.entryWrites(t, sampleEntry)
	for _, w := range writes {
		a.wantForbidden(t, "viewer", w)
	}
	if got := len(a.records(t, audit.AccessDenied)); got != len(writes) {
		t.Errorf("%d records of access.denied, want %d", got, len(writes))
	}
	if got := a.revisionIDs(t, sampleEntry); !slices.Equal(got, revs) {
		t.Errorf("the revisions of the entry are %v, want those it had", got)
	}

	// The permission is asked before the body is received: a body that is too large is refused for the permission.
	big := strings.Repeat(" ", documentBodyLimit+1)
	checkProblem(t, "a body of more than a document and its wrap, from a viewer", as.post("/api/entries/"+sampleEntry+"/revisions", big), "forbidden")
	checkProblem(t, "a body of more than 64 KiB, from a viewer", as.post("/api/entries", big[:smallBodyLimit+1]), "forbidden")
	if logged := a.logs.String(); logged != "" {
		t.Errorf("the log is %q, want nothing", logged)
	}
}

// TestEntriesAuthor shows what an author may do: make entries and translate any entry, and save revisions of its own entries
// and delete them, and not those of others.
func TestEntriesAuthor(t *testing.T) {
	a := newAPIServer(t)
	author, editor := a.people["author"].account.ID, a.people["editor"].account.ID
	as := a.as("author")

	// The entries of others: a revision and a deletion are refused, in the Update, after the body was received.
	revs := a.revisionIDs(t, sampleEntry)
	writes := a.entryWrites(t, sampleEntry)
	a.wantForbidden(t, "author", writes[1])
	a.wantForbidden(t, "author", writes[3])
	byEditor := a.newEntry(t, "editor", "post", "ja")
	a.addRevision(t, "editor", byEditor.ID, postRevision("編集者の下書き", "editors", ""))
	revsOfEditor := a.revisionIDs(t, byEditor.ID)
	editorWrites := a.entryWrites(t, byEditor.ID)
	a.wantForbidden(t, "author", editorWrites[1])
	a.wantForbidden(t, "author", editorWrites[3])
	if !slices.Equal(a.revisionIDs(t, sampleEntry), revs) || !slices.Equal(a.revisionIDs(t, byEditor.ID), revsOfEditor) {
		t.Error("a revision was saved to an entry that the author did not make")
	}

	// Its own: made, saved, translated, saved by others, and deleted.
	mine := a.newEntry(t, "author", "post", "ja")
	if got := a.entryDoc(t, mine.ID); got.CreatedBy != author {
		t.Fatalf("the entry that the author made is %+v", got)
	}
	a.addRevision(t, "author", mine.ID, postRevision("著者の下書き", "authors", ""))
	a.addRevision(t, "author", mine.ID, postRevision("著者の下書き 2", "authors", ""))
	translation := result(t, "a translation of its own entry", as.post("/api/entries/"+mine.ID+"/translations", `{"language":"en"}`), http.StatusCreated)
	if got := a.entryDoc(t, translation.ID); got.CreatedBy != author || got.Language != "en" {
		t.Errorf("the translation is %+v, want an entry that the author made, in en", got)
	}
	// Any entry can be translated, also one that others made: the translation is the author's.
	others := result(t, "a translation of an entry of the importer", as.post("/api/entries/"+sampleEntry+"/translations", `{"language":"en"}`), http.StatusCreated)
	if got := a.entryDoc(t, others.ID); got.CreatedBy != author {
		t.Errorf("the translation of an entry of the importer is %+v, want an entry that the author made", got)
	}
	for _, login := range []string{"editor", "admin"} {
		saved := a.addRevision(t, login, mine.ID, postRevision("直した by "+login, "authors", ""))
		if got := a.storedRevision(t, saved.Revision).SavedBy; got != a.people[login].account.ID {
			t.Errorf("the revision that %s saved to the entry of the author is saved by %s", login, got)
		}
	}
	for _, id := range []string{translation.ID, others.ID, mine.ID} {
		doc := a.stored(t, content.RefEntry, id)
		deleted := result(t, "DELETE an entry of its own", as.del("/api/entries/"+id+"?base="+strconv.FormatInt(doc.Seq, 10)), http.StatusOK)
		if deleted.ID != id || !a.missing(t, content.RefEntry, id) {
			t.Errorf("the deletion of %s answers %+v, and the entry is still in the store: %v", id, deleted, !a.missing(t, content.RefEntry, id))
		}
	}
	// What the editor and the administrator did with the entry of the author is in the activity log, by them.
	if got := a.mine(t, audit.EntrySaveRevision, mine.ID); len(got) != 4 || got[2].Actor != editor || got[3].Actor != a.people["admin"].account.ID {
		t.Errorf("the records of entry.saveRevision for the entry are %+v, want 4: two by the author, one by the editor and one by the administrator", got)
	}
	if got := len(a.records(t, audit.AccessDenied)); got != 4 {
		t.Errorf("%d records of access.denied, want the 4 refusals", got)
	}
	if logged := a.logs.String(); logged != "" {
		t.Errorf("the log is %q, want nothing", logged)
	}
}

// TestEntriesEditorAndAdmin shows that an editor and an administrator change any entry.
func TestEntriesEditorAndAdmin(t *testing.T) {
	a := newAPIServer(t)
	for _, login := range []string{"editor", "admin"} {
		t.Run(login, func(t *testing.T) {
			as := a.as(login)
			made := a.newEntry(t, "author", "post", "ja")
			a.addRevision(t, "author", made.ID, postRevision("著者の下書き", "by-author-"+login, ""))
			a.addRevision(t, login, made.ID, postRevision("直した", "by-author-"+login, ""))
			translated := result(t, "a translation", as.post("/api/entries/"+made.ID+"/translations", `{"language":"en"}`), http.StatusCreated)
			for _, id := range []string{translated.ID, made.ID} {
				doc := a.stored(t, content.RefEntry, id)
				result(t, "DELETE an entry that the author made", as.del("/api/entries/"+id+"?base="+strconv.FormatInt(doc.Seq, 10)), http.StatusOK)
			}
		})
	}
	if got := a.records(t, audit.AccessDenied); len(got) != 0 {
		t.Errorf("%d records of access.denied, want none", len(got))
	}
}

// TestEntriesHTML shows that an html block is for an administrator to add or change: the validation of the save says so, and
// an editor keeps the html block that is there, changes the rest of the entry, and removes it.
func TestEntriesHTML(t *testing.T) {
	a := newAPIServer(t)
	e := a.newEntry(t, "editor", "post", "ja")
	revisions := "/api/entries/" + e.ID + "/revisions"
	kept := htmlBlock("b_html0001", "html")
	para := textBlock("b_para0001", "本文")
	withHTML := postRevisionWith("html", "html", "", para, kept)

	// The site does not allow html blocks yet, to anyone.
	if p := firstProblem(t, "an html block where it is not allowed", a.as("admin").post(revisions, revisionLines("", withHTML))); !strings.Contains(p.Detail, "not enabled") {
		t.Errorf("the error of an html block where it is not allowed is %+v, want that html blocks are not enabled", p)
	}
	a.allowHTML(t)

	// An editor cannot add one: the problem is at the block, in the bytes that it sent.
	editorRevs := a.revisionIDs(t, e.ID)
	problem := firstProblem(t, "a new html block from an editor", a.as("editor").post(revisions, revisionLines("", withHTML)))
	line, col := locate(t, withHTML, `{"type":"html"`)
	if !revisionFile.MatchString(problem.File) || problem.Pointer != "/body/1" || problem.Detail != "only an administrator can add or change html blocks" ||
		problem.Line != line || problem.Column != col {
		t.Errorf("the error of a new html block is %+v, want a revision's file, /body/1, that only an administrator can, and the place %d:%d in the revision", problem, line, col)
	}
	if got := a.revisionIDs(t, e.ID); !slices.Equal(got, editorRevs) {
		t.Errorf("the revisions of the entry are %v, want none saved", got)
	}

	// An administrator can.
	added := result(t, "a new html block from an administrator", a.as("admin").post(revisions, revisionBody("", withHTML)), http.StatusCreated)
	if got := a.storedRevision(t, added.Revision); string(got.Data) != withHTML || got.SavedBy != a.people["admin"].account.ID {
		t.Errorf("the stored revision is %s saved by %s, want the one with the html block that the administrator sent", got.Data, got.SavedBy)
	}

	// An editor keeps it, and changes the rest.
	keeps := postRevisionWith("html 2", "html", "", textBlock("b_para0001", "本文を変えた"), kept, textBlock("b_para0002", "足した"))
	kept2 := result(t, "a save that keeps the html block", a.as("editor").post(revisions, revisionBody(added.Revision, keeps)), http.StatusCreated)
	// And cannot change it, nor add a block like it.
	changed := postRevisionWith("html 3", "html", "", textBlock("b_para0001", "本文"), htmlBlock("b_html0001", "changed"))
	problem = firstProblem(t, "a changed html block", a.as("editor").post(revisions, revisionLines(kept2.Revision, changed)))
	if problem.Pointer != "/body/1" || problem.Detail != "only an administrator can add or change html blocks" {
		t.Errorf("the error of a changed html block is %+v, want /body/1 and that only an administrator can", problem)
	}
	copied := postRevisionWith("html 4", "html", "", para, kept, htmlBlock("b_html0002", "html"))
	problem = firstProblem(t, "a copy of an html block", a.as("editor").post(revisions, revisionLines(kept2.Revision, copied)))
	if problem.Pointer != "/body/2" || problem.Detail != "only an administrator can add or change html blocks" {
		t.Errorf("the error of a copy of an html block is %+v, want /body/2 and that only an administrator can", problem)
	}
	// It can be removed.
	removed := result(t, "a save that removes the html block", a.as("editor").post(revisions, revisionBody(kept2.Revision, postRevision("html 5", "html", ""))), http.StatusCreated)
	if got := a.revisionIDs(t, e.ID); len(got) != 3 || got[2] != removed.Revision {
		t.Errorf("the revisions of the entry are %v, want the three that were saved", got)
	}

	// A translation copies the html block, which no one adds: an author makes it from the revision of an administrator.
	a.addRevision(t, "admin", e.ID, withHTML)
	translated := result(t, "a translation with an html block", a.as("author").post("/api/entries/"+e.ID+"/translations", `{"language":"en"}`), http.StatusCreated)
	if got := a.storedRevision(t, translated.Revision); !strings.Contains(string(got.Data), `"b_html0001"`) || got.SavedBy != a.people["author"].account.ID {
		t.Errorf("the revision of the translation is %s saved by %s, want a copy with the html block, saved by the author", got.Data, got.SavedBy)
	}
	// An author keeps the html block in an entry of its own too, and cannot change it.
	own := a.newEntry(t, "author", "post", "ja")
	a.addRevision(t, "admin", own.ID, withHTML)
	ownPath := "/api/entries/" + own.ID + "/revisions"
	result(t, "a save of an author that keeps the html block", a.as("author").post(ownPath, revisionBody(a.lastRevision(t, own.ID), keeps)), http.StatusCreated)
	firstProblem(t, "a changed html block from an author", a.as("author").post(ownPath, revisionBody(a.lastRevision(t, own.ID), changed)))
	if logged := a.logs.String(); logged != "" {
		t.Errorf("the log is %q, want nothing", logged)
	}
}

// TestEntriesHTMLInUpdate shows that the right to add an html block is checked again in the Update of the operations that take
// it: an administrator who has been made an editor since the request came is refused, and nothing is saved.
func TestEntriesHTMLInUpdate(t *testing.T) {
	a := newAPIServer(t)
	a.allowHTML(t)
	for i, name := range []string{"a save with an html block", "a translation"} {
		t.Run(name, func(t *testing.T) {
			login := "second" + strconv.Itoa(i)
			victim := a.victim(t, login, account.RoleAdmin) // a second administrator, so that the role of the first is not the last one's
			e := a.newEntry(t, login, "post", "ja")
			path := "/api/entries/" + e.ID + "/revisions"
			body := revisionBody("", postRevisionWith("html", "html", "", textBlock("b_para0001", "本文"), htmlBlock("b_html0001", "html")))
			if i == 1 {
				a.addRevision(t, login, e.ID, postRevision("訳す", "translate", ""))
				path, body = "/api/entries/"+e.ID+"/translations", `{"language":"en"}`
			}
			revs := a.revisionIDs(t, e.ID)
			before := a.versions(t)

			var fired atomic.Int32
			a.hook = func(point string) {
				if point == "checked" && fired.Add(1) == 1 {
					down := account.RoleEditor
					if _, err := a.accounts.Update(t.Context(), account.System, victim, account.Change{Role: &down}); err != nil {
						t.Errorf("Update() error = %v", err)
					}
				}
			}
			a.clock.advance(time.Minute)
			n := len(a.records(t, audit.AccessDenied))
			checkProblem(t, name+" by an administrator who is an editor by then", a.as(login).post(path, body), "forbidden")
			a.hook = nil
			if fired.Load() != 1 {
				t.Fatalf("the hook ran %d times, want 1", fired.Load())
			}
			if got := a.revisionIDs(t, e.ID); !slices.Equal(got, revs) {
				t.Errorf("the revisions of the entry are %v, want %v", got, revs)
			}
			if got := a.versions(t); got != before {
				t.Errorf("the request has changed the store:\n%s\nwant\n%s", got, before)
			}
			records := a.records(t, audit.AccessDenied)
			if len(records) != n+1 {
				t.Fatalf("the refusal made %d records of access.denied, want 1", len(records)-n)
			}
			if r := records[n]; r.Actor != victim || r.Target != (audit.Target{Kind: "entry", ID: e.ID}) || r.Detail["permission"] != string(account.EditHTML) {
				t.Errorf("the record is %+v, want the actor %s, the permission html.edit and the entry", r, victim)
			}
			if logged := a.logs.String(); logged != "" {
				t.Errorf("the log is %q, want nothing", logged)
			}
		})
	}
}

// entryChanges returns the writes of entryWrites for an entry that the account login may change, with a deletion of another entry.
func (a *apiServer) entryChanges(t *testing.T, login string) []write {
	t.Helper()
	target := a.newEntry(t, "editor", "post", "ja")
	a.addRevision(t, "editor", target.ID, postRevision("対象 "+login, "target-"+login, ""))
	return a.entryWrites(t, target.ID)
}

var entryOperations = []string{"create an entry", "save a revision", "create a translation", "delete the entry"}

// TestEntriesRoleChangesInUpdate shows that the permission is checked again in the Update of each operation, with the role that
// the store has then: a request that the role allowed when it was received is refused if the role has changed since, and
// nothing is saved.
func TestEntriesRoleChangesInUpdate(t *testing.T) {
	a := newAPIServer(t)
	for i, name := range entryOperations {
		t.Run(name, func(t *testing.T) {
			login := "victim" + strconv.Itoa(i)
			victim := a.victim(t, login, account.RoleEditor)
			w := a.entryChanges(t, login)[i]
			var revs []string
			if w.target.ID != "" {
				revs = a.revisionIDs(t, w.target.ID)
			}

			var fired atomic.Int32
			a.hook = func(point string) {
				if point == "checked" && fired.Add(1) == 1 {
					down := account.RoleViewer
					if _, err := a.accounts.Update(t.Context(), account.System, victim, account.Change{Role: &down}); err != nil {
						t.Errorf("Update() error = %v", err)
					}
				}
			}
			a.clock.advance(time.Minute)
			before, n := a.versions(t), len(a.records(t, audit.AccessDenied))
			checkProblem(t, w.name+" by an account whose role changes", a.as(login).send(w.method, w.path, w.body), "forbidden")
			if fired.Load() != 1 {
				t.Fatalf("the hook ran %d times, want 1", fired.Load())
			}
			a.hook = nil

			if got := a.versions(t); got != before {
				t.Errorf("the request has changed the store:\n%s\nwant\n%s", got, before)
			}
			if w.target.ID != "" {
				if got := a.revisionIDs(t, w.target.ID); !slices.Equal(got, revs) {
					t.Errorf("the revisions of the entry are %v, want %v", got, revs)
				}
			}
			records := a.records(t, audit.AccessDenied)
			if len(records) != n+1 {
				t.Fatalf("the refusal made %d records of access.denied, want 1", len(records)-n)
			}
			if r := records[n]; r.Actor != victim || r.Target != w.target || r.Detail["permission"] != string(w.permission) {
				t.Errorf("the record is %+v, want the actor %s, the permission %s and the target %+v", r, victim, w.permission, w.target)
			}
			if logged := a.logs.String(); logged != "" {
				t.Errorf("the log is %q, want nothing", logged)
			}
		})
	}
}

// TestEntriesCredentialsChangeInUpdate shows that a request whose account has other credentials when it is saved than when it
// came, such as after a reset, is answered as one without a session: 401, the cookie cleared, and nothing saved or recorded.
func TestEntriesCredentialsChangeInUpdate(t *testing.T) {
	a := newAPIServer(t)
	for i, name := range entryOperations {
		t.Run(name, func(t *testing.T) {
			login := "reset" + strconv.Itoa(i)
			victim := a.victim(t, login, account.RoleEditor)
			w := a.entryChanges(t, login)[i]

			var fired atomic.Int32
			a.hook = func(point string) {
				if point == "checked" && fired.Add(1) == 1 {
					a.clock.advance(time.Minute) // the time of the credentials is that of the clock
					token, err := a.accounts.Reset(t.Context(), account.System, victim)
					if err != nil {
						t.Errorf("Reset() error = %v", err)
					}
					a.seen.token(token)
				}
			}
			before, n := a.versions(t), len(a.records(t, audit.AccessDenied))
			res := a.as(login).send(w.method, w.path, w.body)
			checkProblem(t, w.name+" by an account whose credentials change", res, "unauthenticated")
			if got := res.Header.Values("Set-Cookie"); !equalValues(got, a.clearLine()) {
				t.Errorf("the answer sets the cookies %q, want the one that clears the session", got)
			}
			a.hook = nil
			if fired.Load() != 1 {
				t.Fatalf("the hook ran %d times, want 1", fired.Load())
			}
			if got := a.versions(t); got != before {
				t.Errorf("the request has changed the store:\n%s\nwant\n%s", got, before)
			}
			if got := len(a.records(t, audit.AccessDenied)); got != n {
				t.Errorf("the request made %d records of access.denied, want none: it is not a refusal of a permission", got-n)
			}
		})
	}
}

// TestEntriesRetry shows that the header Idempotency-Key makes the operations on entries happen once: a retry is answered as
// the first request was, also if the entry has changed since, and the activity log has one record.
func TestEntriesRetry(t *testing.T) {
	a := newAPIServer(t)
	as := a.as("editor")
	editor := a.people["editor"].account.ID
	const opID = "op_01JD0000000000000000000001"
	count := func(action audit.Action, entry string) int { return len(a.mine(t, action, entry)) }

	// A creation: the same entry, the same number, the same version, and one entry.
	first := as.post("/api/entries", `{"type":"post","language":"ja"}`, key(1)...)
	location := first.Header.Get("Location")
	created := result(t, "the creation", first, http.StatusCreated)
	entries := len(a.idsWhere(t, content.RefEntry, everyDocument))
	retry := as.post("/api/entries", `{"type":"post","language":"ja"}`, key(1)...)
	if retry.Header.Get("Location") != location {
		t.Errorf("the retry has the Location %q, want %q", retry.Header.Get("Location"), location)
	}
	if again := result(t, "the retry of the creation", retry, http.StatusCreated); again.ID != created.ID || again.Number != created.Number || again.Version != created.Version {
		t.Errorf("the retry answers %+v, want %+v", again, created)
	}
	if got := len(a.idsWhere(t, content.RefEntry, everyDocument)); got != entries || count(audit.EntryCreate, created.ID) != 1 {
		t.Errorf("the retry made an entry, or recorded again: %d entries, want %d, and %d records of entry.create", got, entries, count(audit.EntryCreate, created.ID))
	}
	if r := a.mine(t, audit.EntryCreate, created.ID)[0]; r.Operation != opID || r.Actor != editor {
		t.Errorf("the record is %+v, want the operation ID of the key and the editor", r)
	}
	// The key is for this request only: another body, another route, and another account are other requests.
	checkProblem(t, "the key of a creation with another type", as.post("/api/entries", `{"type":"page","language":"ja"}`, key(1)...), "operation-reused")
	checkProblem(t, "the key of a creation by another account", a.as("admin").post("/api/entries", `{"type":"post","language":"ja"}`, key(1)...), "operation-reused")
	revisions := location + "/revisions"
	checkProblem(t, "the key of a creation on a save", as.post(revisions, revisionBody("", postRevision("x", "x", "")), key(1)...), "operation-reused")

	// A save of a revision: the retry returns the revision that the first made, not a conflict, though the entry has changed.
	rev := postRevision("再送", "retry", "")
	saveRes := as.post(revisions, revisionBody("", rev), key(2)...)
	savedLocation := saveRes.Header.Get("Location")
	saved := result(t, "the save", saveRes, http.StatusCreated)
	a.addRevision(t, "editor", created.ID, postRevision("再送のあと", "after-retry", ""))
	revs := a.revisionIDs(t, created.ID)
	retry = as.post(revisions, revisionBody("", rev), key(2)...)
	if retry.Header.Get("Location") != savedLocation {
		t.Errorf("the retry of the save has the Location %q, want %q", retry.Header.Get("Location"), savedLocation)
	}
	if again := result(t, "the retry of the save", retry, http.StatusCreated); again.Revision != saved.Revision || again.Version != saved.Version {
		t.Errorf("the retry of the save answers %+v, want the revision %s", again, saved.Revision)
	}
	if got := a.revisionIDs(t, created.ID); !slices.Equal(got, revs) || count(audit.EntrySaveRevision, created.ID) != 2 {
		t.Errorf("the retry of the save has changed the revisions %v, or recorded again: %d records of entry.saveRevision, want 2",
			got, count(audit.EntrySaveRevision, created.ID))
	}
	checkProblem(t, "a save without the key on the old base", as.post(revisions, revisionBody("", rev)), "conflict")
	checkProblem(t, "the key of a save with another revision", as.post(revisions, revisionBody("", postRevision("別", "other", "")), key(2)...), "operation-reused")

	// A translation: the same new entry, once.
	translations := location + "/translations"
	trRes := as.post(translations, `{"language":"en"}`, key(3)...)
	trLocation := trRes.Header.Get("Location")
	translated := result(t, "the translation", trRes, http.StatusCreated)
	entries = len(a.idsWhere(t, content.RefEntry, everyDocument))
	retry = as.post(translations, `{"language":"en"}`, key(3)...)
	if retry.Header.Get("Location") != trLocation {
		t.Errorf("the retry of the translation has the Location %q, want %q", retry.Header.Get("Location"), trLocation)
	}
	if again := result(t, "the retry of the translation", retry, http.StatusCreated); again.ID != translated.ID || again.Revision != translated.Revision || again.Number != translated.Number {
		t.Errorf("the retry of the translation answers %+v, want %+v", again, translated)
	}
	if got := len(a.idsWhere(t, content.RefEntry, everyDocument)); got != entries || count(audit.EntryCreateTranslation, translated.ID) != 1 {
		t.Errorf("the retry of the translation made an entry, or recorded again: %d entries, want %d, and %d records", got, entries, count(audit.EntryCreateTranslation, translated.ID))
	}
	checkProblem(t, "the key of a translation into another language", as.post(translations, `{"language":"ja"}`, key(3)...), "operation-reused")

	// A deletion: the retry returns the same answer when the entry is gone.
	doc := a.stored(t, content.RefEntry, translated.ID)
	deletion := trLocation + "?base=" + strconv.FormatInt(doc.Seq, 10)
	deleted := result(t, "the deletion", as.del(deletion, key(4)...), http.StatusOK)
	if again := result(t, "the retry of the deletion", as.del(deletion, key(4)...), http.StatusOK); again.Version != deleted.Version || again.ID != translated.ID {
		t.Errorf("the retry of the deletion answers %+v, want %+v", again, deleted)
	}
	checkProblem(t, "a deletion without the key", as.del(deletion), "not-found")
	if got := count(audit.EntryDelete, translated.ID); got != 1 {
		t.Errorf("%d records of entry.delete, want 1", got)
	}

	// The shape of the key is checked before anything is done, as the permission is before the key.
	checkProblem(t, "a key without quotes", as.post("/api/entries", `{"type":"post","language":"ja"}`, "Idempotency-Key", "op_01JD0000000000000000000009"), "bad-header")
	checkProblem(t, "a key of a viewer", a.as("viewer").post("/api/entries", `{"type":"post","language":"ja"}`, key(9)...), "forbidden")
	if logged := a.logs.String(); logged != "" {
		t.Errorf("the log is %q, want nothing", logged)
	}
}
