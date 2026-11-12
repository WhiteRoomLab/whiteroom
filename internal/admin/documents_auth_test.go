package admin

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/account"
	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/edit"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

// kinds are the kinds of the documents with the permission that each operation on them needs, and a document of the sample.
var kinds = []struct {
	path                 string
	kind                 content.RefKind
	id                   string
	create, save, remove account.Action // "" for no operation
}{
	{"terms", content.RefTerm, newsTerm, account.EditTerms, account.EditTerms, account.EditTerms},
	{"authors", content.RefAuthor, yamada, account.CreateAuthor, account.EditAuthor, account.DeleteAuthor},
	{"menus", content.RefMenu, headerMenu, account.EditMenus, account.EditMenus, account.EditMenus},
	{"forms", content.RefForm, sampleForm, account.EditForms, account.EditForms, account.EditForms},
	{"redirects", content.RefRedirect, sampleRule, account.EditRedirects, account.EditRedirects, account.EditRedirects},
	{"media", content.RefMedia, samplePDF, "", account.EditMedia, account.DeleteMedia},
}

// write is a request that changes a document, with the permission that it needs and the target that a refusal names.
type write struct {
	name, method, path, body string
	permission               account.Action
	target                   audit.Target
}

// writes returns a request of each operation that changes a document of the sample site: they need the permissions of kinds.
func (a *apiServer) writes(t *testing.T) []write {
	t.Helper()
	var out []write
	for _, k := range kinds {
		doc := a.stored(t, k.kind, k.id)
		item := "/api/" + k.path + "/" + k.id
		target := audit.Target{Kind: string(k.kind), ID: k.id}
		if k.create != "" {
			out = append(out, write{"create " + k.path, http.MethodPost, "/api/" + k.path, `{"document":{}}`, k.create, audit.Target{}})
		}
		out = append(out,
			write{"save " + k.path, http.MethodPut, item, saveBody(doc.Seq, string(doc.Data)), k.save, target},
			write{"delete " + k.path, http.MethodDelete, item + "?base=" + strconv.FormatInt(doc.Seq, 10), "", k.remove, target},
		)
	}
	settings := a.stored(t, store.SettingsKind, "")
	return append(out, write{"save the settings", http.MethodPut, "/api/settings", saveBody(settings.Seq, string(settings.Data)),
		account.EditSettings, audit.Target{Kind: audit.KindSettings}})
}

// wantForbidden sends the request as the account login and checks that it is refused with 403, that the refusal is recorded
// once as access.denied for the permission and the target, by the account, and that the store is as it was. The clock moves a
// minute first, so that the limit of the records of refusals does not count.
func (a *apiServer) wantForbidden(t *testing.T, login string, w write) {
	t.Helper()
	a.clock.advance(time.Minute)
	before, n := a.versions(t), len(a.records(t, audit.AccessDenied))
	checkProblem(t, fmt.Sprintf("%s as %s", w.name, login), a.as(login).send(w.method, w.path, w.body), "forbidden")
	records := a.records(t, audit.AccessDenied)
	if len(records) != n+1 {
		t.Errorf("%s as %s made %d records of access.denied, want 1", w.name, login, len(records)-n)
		return
	}
	if r := records[n]; r.Actor != a.people[login].account.ID || r.Action != audit.AccessDenied || r.Target != w.target ||
		len(r.Detail) != 1 || r.Detail["permission"] != string(w.permission) || r.Log != audit.ActivityLog {
		t.Errorf("%s as %s: the record is %+v, want the actor %s, the permission %s and the target %+v",
			w.name, login, r, a.people[login].account.ID, w.permission, w.target)
	}
	if got := a.versions(t); got != before {
		t.Errorf("%s as %s has changed the store:\n%s\nwant\n%s", w.name, login, got, before)
	}
}

// TestDocumentsViewer shows that a viewer reads every document and changes none, and that each refusal is recorded.
func TestDocumentsViewer(t *testing.T) {
	a := newAPIServer(t)
	for _, k := range kinds {
		doc := a.stored(t, k.kind, k.id)
		path := "/api/" + k.path
		if list := a.listOf(t, "viewer", path); list.Total == 0 {
			t.Errorf("GET %s as a viewer has no documents", path)
		}
		if got := body(t, "GET "+path+"/"+k.id, a.as("viewer").get(path+"/"+k.id), http.StatusOK); got != wantFrame(t, doc) {
			t.Errorf("GET %s/%s as a viewer answers\n%s\nwant\n%s", path, k.id, got, wantFrame(t, doc))
		}
	}
	body(t, "GET /api/settings as a viewer", a.as("viewer").get("/api/settings"), http.StatusOK)
	if got := a.records(t, audit.AccessDenied); len(got) != 0 {
		t.Fatalf("reading made %d records of access.denied, want none", len(got))
	}

	writes := a.writes(t)
	if len(writes) != 5+6*2+1 {
		t.Fatalf("%d writes are listed, want a creation for 5 kinds, a save and a deletion for 6, and the settings", len(writes))
	}
	for _, w := range writes {
		a.wantForbidden(t, "viewer", w)
	}
	if got := len(a.records(t, audit.AccessDenied)); got != len(writes) {
		t.Errorf("%d records of access.denied, want %d", got, len(writes))
	}
	if logged := a.logs.String(); logged != "" {
		t.Errorf("the log is %q, want nothing", logged)
	}
}

// TestDocumentsAuthor shows what an author may change: its own author profile and the media that it uploaded, and nothing else.
func TestDocumentsAuthor(t *testing.T) {
	a := newAPIServer(t)
	author, editor := a.people["author"].account.ID, a.people["editor"].account.ID
	as := a.as("author")

	// Not the terms, the menus, the forms, the redirects, the settings, nor to make or delete an author profile.
	for _, w := range a.writes(t) {
		if w.permission == account.EditAuthor || w.permission == account.EditMedia || w.permission == account.DeleteMedia {
			continue
		}
		a.wantForbidden(t, "author", w)
	}

	// The author profile that the account is linked to is its own, and no other.
	other := result(t, "POST /api/authors", a.as("editor").post("/api/authors", createBody(authorDoc("田中 一郎", "tanaka"))), http.StatusCreated)
	profile := a.stored(t, content.RefAuthor, yamada)
	save := func(id string) write {
		doc := a.stored(t, content.RefAuthor, id)
		return write{"save an author profile", http.MethodPut, "/api/authors/" + id, saveBody(doc.Seq, string(doc.Data)), account.EditAuthor,
			audit.Target{Kind: "author", ID: id}}
	}
	a.wantForbidden(t, "author", save(yamada)) // not linked yet
	if _, err := a.accounts.Update(t.Context(), account.System, author, account.Change{Author: ptr(yamada)}); err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(string(profile.Data), "山田 太郎", "山田 太郎です", 1)
	saved := result(t, "PUT its own author profile", as.put("/api/authors/"+yamada, saveBody(profile.Seq, changed)), http.StatusOK)
	if saved.ID != yamada || saved.Version.SavedBy != author || string(a.stored(t, content.RefAuthor, yamada).Data) != strings.TrimSpace(changed) {
		t.Errorf("the save of its own author profile answers %+v, and the store has\n%s", saved, a.stored(t, content.RefAuthor, yamada).Data)
	}
	a.wantForbidden(t, "author", save(other.ID))
	a.wantForbidden(t, "author", write{"delete its own author profile", http.MethodDelete,
		"/api/authors/" + yamada + "?base=" + strconv.FormatInt(saved.Version.Seq, 10), "", account.DeleteAuthor,
		audit.Target{Kind: "author", ID: yamada}})
	if got := a.stored(t, content.RefAuthor, yamada); got.Seq != saved.Version.Seq {
		t.Error("the author profile has changed again")
	}

	// The media that it uploaded are its own: it saves their metadata and deletes them; not those of others.
	mine, theirs := a.addMedium(t, "author", "mine.pdf"), a.addMedium(t, "editor", "theirs.pdf")
	for _, m := range []struct {
		name string
		id   string
	}{{"mine", mine.ID}, {"theirs", theirs.ID}} {
		doc := a.stored(t, content.RefMedia, m.id)
		item := "/api/media/" + m.id
		target := audit.Target{Kind: "media", ID: m.id}
		edited := strings.Replace(string(doc.Data), m.name+".pdf", m.name+"-2.pdf", 1)
		if m.id == theirs.ID {
			a.wantForbidden(t, "author", write{"save the medium of an editor", http.MethodPut, item, saveBody(doc.Seq, edited), account.EditMedia, target})
			a.wantForbidden(t, "author", write{"delete the medium of an editor", http.MethodDelete, item + "?base=" + strconv.FormatInt(doc.Seq, 10), "",
				account.DeleteMedia, target})
			continue
		}
		res := result(t, "PUT "+item, as.put(item, saveBody(doc.Seq, edited)), http.StatusOK)
		if res.Version.SavedBy != author || string(a.stored(t, content.RefMedia, m.id).Data) != edited {
			t.Errorf("the save of its own medium answers %+v", res)
		}
		del := result(t, "DELETE "+item, as.del(item+"?base="+strconv.FormatInt(res.Version.Seq, 10)), http.StatusOK)
		if del.ID != m.id || !a.missing(t, content.RefMedia, m.id) {
			t.Errorf("the deletion of its own medium answers %+v", del)
		}
	}

	// An editor changes the media of others.
	doc := a.stored(t, content.RefMedia, theirs.ID)
	res := result(t, "PUT the medium of an editor as the editor", a.as("editor").put("/api/media/"+theirs.ID,
		saveBody(doc.Seq, strings.Replace(string(doc.Data), "theirs.pdf", "ours.pdf", 1))), http.StatusOK)
	if res.Version.SavedBy != editor {
		t.Errorf("the save of the editor answers %+v", res)
	}
	// A medium of the importer, who has no account, is nobody's own.
	imported := a.stored(t, content.RefMedia, sampleLogo)
	a.wantForbidden(t, "author", write{"save a medium of the importer", http.MethodPut, "/api/media/" + sampleLogo,
		saveBody(imported.Seq, string(imported.Data)), account.EditMedia, audit.Target{Kind: "media", ID: sampleLogo}})
	if logged := a.logs.String(); logged != "" {
		t.Errorf("the log is %q, want nothing", logged)
	}
}

func ptr[T any](v T) *T { return &v }

// TestDocumentsSettingsRoles shows that only an administrator saves the settings.
func TestDocumentsSettingsRoles(t *testing.T) {
	a := newAPIServer(t)
	doc := a.stored(t, store.SettingsKind, "")
	changed := strings.Replace(string(doc.Data), "Webサイトの企画と制作", "Webサイトの企画", 1)
	w := write{"save the settings", http.MethodPut, "/api/settings", saveBody(doc.Seq, changed), account.EditSettings, audit.Target{Kind: audit.KindSettings}}
	for _, login := range []string{"editor", "author", "viewer"} {
		a.wantForbidden(t, login, w)
	}
	res := result(t, "PUT /api/settings as an admin", a.as("admin").put("/api/settings", w.body), http.StatusOK)
	if res.Version.SavedBy != a.people["admin"].account.ID || !res.Publishes {
		t.Errorf("the save of the settings answers %+v", res)
	}
	records := a.records(t, audit.SettingsSave)
	if len(records) != 1 || records[0].Actor != a.people["admin"].account.ID {
		t.Errorf("the records of settings.save are %+v, want one by the admin", records)
	}
}

// victim makes an account of the role, who is logged in, for a test that changes it while its request runs.
func (a *apiServer) victim(t *testing.T, login string, role account.Role) string {
	t.Helper()
	acct := a.addAccount(t, login, role)
	a.people[login] = &person{account: acct, token: a.logIn(t, login)}
	return acct.ID
}

// changes are the requests of the operations that check the permission in their Update, one of each route that changes a
// document, which the account login sends: they are the same for the tests that change the account while a request runs.
func (a *apiServer) changes(t *testing.T, login string, deletable string) []write {
	t.Helper()
	news := a.stored(t, content.RefTerm, newsTerm)
	settings := a.stored(t, store.SettingsKind, "")
	gone := a.stored(t, content.RefTerm, deletable)
	return []write{
		{"create", http.MethodPost, "/api/terms", createBody(termDoc("category", "ja", "作る", "make-"+login)), account.EditTerms, audit.Target{}},
		{"save", http.MethodPut, "/api/terms/" + newsTerm, saveBody(news.Seq, strings.Replace(string(news.Data), "お知らせ", "お知らせ"+login, 1)),
			account.EditTerms, audit.Target{Kind: "term", ID: newsTerm}},
		{"delete", http.MethodDelete, "/api/terms/" + deletable + "?base=" + strconv.FormatInt(gone.Seq, 10), "", account.EditTerms,
			audit.Target{Kind: "term", ID: deletable}},
		{"save the settings", http.MethodPut, "/api/settings", saveBody(settings.Seq, strings.Replace(string(settings.Data), "Webサイト", "Web"+login, 1)),
			account.EditSettings, audit.Target{Kind: audit.KindSettings}},
	}
}

// TestDocumentsRoleChangesInUpdate shows that the permission is checked again in the Update of the operation, with the role
// that the store has then: a request that the role allowed when it was received is refused if the role has changed since, and
// nothing is saved.
func TestDocumentsRoleChangesInUpdate(t *testing.T) {
	a := newAPIServer(t)
	for i, name := range []string{"create", "save", "delete", "save the settings"} {
		t.Run(name, func(t *testing.T) {
			login := "victim" + strconv.Itoa(i)
			role := account.RoleEditor
			if name == "save the settings" {
				role = account.RoleAdmin // a second administrator, so that the role of the first is not the last one's
			}
			victim := a.victim(t, login, role)
			deletable := result(t, "POST /api/terms", a.as("editor").post("/api/terms", createBody(termDoc("category", "ja", "消す"+login, "del-"+login))),
				http.StatusCreated).ID
			w := a.changes(t, login, deletable)[i]

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

// TestDocumentsCredentialsChangeInUpdate shows that a request whose account has other credentials when it is saved than when it
// came, such as after a reset, is answered as one without a session: 401, the cookie cleared, and nothing saved or recorded.
func TestDocumentsCredentialsChangeInUpdate(t *testing.T) {
	a := newAPIServer(t)
	for i, name := range []string{"create", "save", "delete", "save the settings"} {
		t.Run(name, func(t *testing.T) {
			login := "reset" + strconv.Itoa(i)
			role := account.RoleEditor
			if name == "save the settings" {
				role = account.RoleAdmin
			}
			victim := a.victim(t, login, role)
			deletable := result(t, "POST /api/terms", a.as("editor").post("/api/terms", createBody(termDoc("category", "ja", "消す"+login, "del-"+login))),
				http.StatusCreated).ID
			w := a.changes(t, login, deletable)[i]

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
			checkProblem(t, "a request after the reset", a.as(login).get("/api/terms"), "unauthenticated")
		})
	}
}

// TestDocumentsOwnerChangesInUpdate shows that who owns a medium is read in the Update of the operation: an author who changes
// a medium that was its own when the request came is refused if another account has uploaded it since.
func TestDocumentsOwnerChangesInUpdate(t *testing.T) {
	a := newAPIServer(t)
	data, err := os.ReadFile(filepath.FromSlash(pdfOfSite))
	if err != nil {
		t.Fatal(err)
	}
	editor := a.people["editor"].account.ID

	for _, tt := range []struct {
		name       string
		permission account.Action
		request    func(id string, doc store.Document) (method, path, body string)
	}{
		{"save", account.EditMedia, func(id string, doc store.Document) (string, string, string) {
			return http.MethodPut, "/api/media/" + id, saveBody(doc.Seq, strings.Replace(string(doc.Data), "mine.pdf", "mine-2.pdf", 1))
		}},
		{"delete", account.DeleteMedia, func(id string, doc store.Document) (string, string, string) {
			return http.MethodDelete, "/api/media/" + id + "?base=" + strconv.FormatInt(doc.Seq, 10), ""
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mine := a.addMedium(t, "author", "mine.pdf")
			doc := a.stored(t, content.RefMedia, mine.ID)
			method, path, body := tt.request(mine.ID, doc)

			var fired atomic.Int32
			a.hook = func(point string) {
				if point == "checked" && fired.Add(1) == 1 {
					// The editor uploads another file for the medium, which makes the editor the account that uploaded it.
					cur := a.stored(t, content.RefMedia, mine.ID)
					op := edit.Op{By: editor}
					if _, err := a.pub.Editor().ReplaceMedia(t.Context(), op, mine.ID, cur.Seq, bytes.NewReader(data), "application/pdf"); err != nil {
						t.Errorf("ReplaceMedia() error = %v", err)
					}
				}
			}
			a.clock.advance(time.Minute)
			n := len(a.records(t, audit.AccessDenied))
			checkProblem(t, tt.name+" of a medium that another account has uploaded since", a.as("author").send(method, path, body), "forbidden")
			a.hook = nil
			if fired.Load() != 1 {
				t.Fatalf("the hook ran %d times, want 1", fired.Load())
			}
			records := a.records(t, audit.AccessDenied)
			if len(records) != n+1 || records[n].Detail["permission"] != string(tt.permission) || records[n].Target != (audit.Target{Kind: "media", ID: mine.ID}) {
				t.Errorf("the records are %+v, want one more for %s of the medium", records, tt.permission)
			}
			if got := a.stored(t, content.RefMedia, mine.ID); !strings.Contains(string(got.Data), editor) || !strings.Contains(string(got.Data), "mine.pdf") {
				t.Errorf("the medium is %s, want that the editor uploaded it and its name is as it was", got.Data)
			}
		})
	}
}

// TestDocumentsRetry shows that the header Idempotency-Key makes the operations that change a document happen once: a retry
// is answered as the first request was, also if the document has changed since, and the activity log has one record.
func TestDocumentsRetry(t *testing.T) {
	a := newAPIServer(t)
	as := a.as("editor")
	editor := a.people["editor"].account.ID
	doc := termDoc("category", "ja", "再送", "retry")
	count := func(action audit.Action) int { return len(a.records(t, action)) }

	// A creation: the same ID, the same version, and one document.
	first := as.post("/api/terms", createBody(doc), key(1)...)
	location := first.Header.Get("Location")
	created := result(t, "the creation", first, http.StatusCreated)
	versions := a.versions(t)
	retry := as.post("/api/terms", createBody(doc), key(1)...)
	if retry.Header.Get("Location") != location {
		t.Errorf("the retry has the Location %q, want %q", retry.Header.Get("Location"), location)
	}
	if again := result(t, "the retry of the creation", retry, http.StatusCreated); again.ID != created.ID || again.Version != created.Version || again.Publishes != created.Publishes {
		t.Errorf("the retry answers %+v, want %+v", again, created)
	}
	if got := a.versions(t); got != versions || count(audit.TermCreate) != 1 {
		t.Errorf("the retry has changed the store, or recorded again: %d records of term.create", count(audit.TermCreate))
	}
	if r := a.records(t, audit.TermCreate)[0]; r.Operation != "op_01JD0000000000000000000001" || r.Actor != editor {
		t.Errorf("the record is %+v, want the operation ID of the key and the editor", r)
	}

	// The key is for this request only: another body, another route, and another account are other requests.
	checkProblem(t, "the key of a creation with another document", as.post("/api/terms", createBody(termDoc("category", "ja", "別", "other")), key(1)...), "operation-reused")
	loc := "/api/terms/" + created.ID
	stored := a.stored(t, content.RefTerm, created.ID)
	checkProblem(t, "the key of a creation on a save", as.put(loc, saveBody(stored.Seq, string(stored.Data)), key(1)...), "operation-reused")
	checkProblem(t, "the key of a creation by another account", a.as("admin").post("/api/terms", createBody(doc), key(1)...), "operation-reused")

	// A save: the retry returns what the first did, not a conflict, even though the document has changed since.
	changed := strings.Replace(string(stored.Data), "再送", "再送2", 1)
	saved := result(t, "the save", as.put(loc, saveBody(stored.Seq, changed), key(2)...), http.StatusOK)
	next := strings.Replace(changed, "再送2", "再送3", 1)
	result(t, "another save", as.put(loc, saveBody(saved.Version.Seq, next)), http.StatusOK)
	versions = a.versions(t)
	if again := result(t, "the retry of the save", as.put(loc, saveBody(stored.Seq, changed), key(2)...), http.StatusOK); again.Version != saved.Version {
		t.Errorf("the retry of the save answers the version %+v, want %+v", again.Version, saved.Version)
	}
	if got := a.versions(t); got != versions {
		t.Errorf("the retry of the save has changed the store:\n%s\nwant\n%s", got, versions)
	}
	if got := count(audit.TermSave); got != 2 {
		t.Errorf("%d records of term.save, want 2: the save with the key once, and the other", got)
	}
	checkProblem(t, "a save without the key at the old version", as.put(loc, saveBody(stored.Seq, changed)), "conflict")

	// A deletion: the retry returns the same answer when the document is gone.
	now := a.stored(t, content.RefTerm, created.ID)
	deleted := result(t, "the deletion", as.del(loc+"?base="+strconv.FormatInt(now.Seq, 10), key(3)...), http.StatusOK)
	if again := result(t, "the retry of the deletion", as.del(loc+"?base="+strconv.FormatInt(now.Seq, 10), key(3)...), http.StatusOK); again.Version != deleted.Version {
		t.Errorf("the retry of the deletion answers %+v, want %+v", again.Version, deleted.Version)
	}
	checkProblem(t, "a deletion without the key", as.del(loc+"?base="+strconv.FormatInt(now.Seq, 10)), "not-found")
	if got := count(audit.TermDelete); got != 1 {
		t.Errorf("%d records of term.delete, want 1", got)
	}

	// The settings.
	settings := a.stored(t, store.SettingsKind, "")
	body := saveBody(settings.Seq, strings.Replace(string(settings.Data), "Webサイト", "Web", 1))
	saved = result(t, "the save of the settings", a.as("admin").put("/api/settings", body, key(4)...), http.StatusOK)
	if again := result(t, "the retry of the save of the settings", a.as("admin").put("/api/settings", body, key(4)...), http.StatusOK); again.Version != saved.Version {
		t.Errorf("the retry of the save of the settings answers %+v, want %+v", again.Version, saved.Version)
	}
	if got := count(audit.SettingsSave); got != 1 {
		t.Errorf("%d records of settings.save, want 1", got)
	}
	if r := a.records(t, audit.SettingsSave)[0]; r.Actor != a.people["admin"].account.ID || r.Operation != "op_01JD0000000000000000000004" {
		t.Errorf("the record of the settings is %+v", r)
	}

	// The shape of the key is checked before anything is done, as the permission is before the key.
	checkProblem(t, "a key without quotes", as.post("/api/terms", createBody(doc), "Idempotency-Key", "op_01JD0000000000000000000009"), "bad-header")
	checkProblem(t, "a key of a viewer", a.as("viewer").post("/api/terms", createBody(doc), key(9)...), "forbidden")
	if logged := a.logs.String(); logged != "" {
		t.Errorf("the log is %q, want nothing", logged)
	}
}
