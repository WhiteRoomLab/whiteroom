package admin

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/account"
	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// publishWrites returns a request of each route that sets the state of the entry, with the permission that it needs and the
// target that a refusal names: the publishing, the unpublishing, the archiving and the scheduling. The entry must have a revision.
func (a *apiServer) publishWrites(t *testing.T, entry string) []write {
	t.Helper()
	item := "/api/entries/" + entry
	target := audit.Target{Kind: "entry", ID: entry}
	return []write{
		{"publish", http.MethodPost, item + "/publish", a.publishBody(t, entry), account.PublishEntry, target},
		{"unpublish", http.MethodPost, item + "/unpublish", a.baseBody(t, entry), account.PublishEntry, target},
		{"archive", http.MethodPost, item + "/archive", a.baseBody(t, entry), account.PublishEntry, target},
		{"schedule", http.MethodPost, item + "/schedule", a.scheduleBody(t, entry, time.Hour), account.PublishEntry, target},
	}
}

var publishOperations = []string{"publish", "unpublish", "archive", "schedule"}

// noPublishRecords checks that no operation of the publishing has been recorded for the entry.
func (a *apiServer) noPublishRecords(t *testing.T, entry string) {
	t.Helper()
	for _, action := range []audit.Action{audit.EntryPublish, audit.EntryUnpublish, audit.EntryArchive, audit.EntrySchedule} {
		if got := a.mine(t, action, entry); len(got) != 0 {
			t.Errorf("%d records of %s for the entry: %+v, want none", len(got), action, got)
		}
	}
}

// TestPublishViewer shows that a viewer sets the state of no entry: each refusal is recorded with the permission and the entry,
// it comes before the body is received, and nothing is published.
func TestPublishViewer(t *testing.T) {
	a := newAPIServer(t)
	e, _ := a.draftPage(t, "editor", "en", "Viewer", "viewer")
	a.waitBuilds(t)
	builds := a.buildCount(t)
	writes := a.publishWrites(t, e)
	for _, w := range writes {
		a.wantForbidden(t, "viewer", w)
	}
	if got := len(a.records(t, audit.AccessDenied)); got != len(writes) {
		t.Errorf("%d records of access.denied, want %d", got, len(writes))
	}
	// The permission is asked before the body, and the key and the query are not looked at yet.
	for _, w := range writes {
		a.clock.advance(time.Minute)
		checkProblem(t, w.name+" with a body that is too large, from a viewer", a.as("viewer").post(w.path, strings.Repeat(" ", smallBodyLimit+1)), "forbidden")
		a.clock.advance(time.Minute)
		checkProblem(t, w.name+" with a key and a query, from a viewer", a.as("viewer").post(w.path+"?x=1", w.body, "Idempotency-Key", "x"), "forbidden")
	}
	a.noPublishRecords(t, e)
	a.waitBuilds(t)
	if got := a.buildCount(t); got != builds {
		t.Errorf("%d builds ended, want none", got-builds)
	}
	if got := a.entryDoc(t, e); got.Status != content.StatusDraft {
		t.Errorf("the entry is %+v, want a draft", got)
	}
	if _, ok := a.page("en/viewer/"); ok {
		t.Error("the page is published")
	}
	if logged := a.logs.String(); logged != "" {
		t.Errorf("the log is %q, want nothing", logged)
	}
}

// TestPublishAuthor shows that an author sets the state of the entries that it made, and of no others: the refusal for the
// entry of another is made in the Update, after the body was received, and recorded with the entry.
func TestPublishAuthor(t *testing.T) {
	a := newAPIServer(t)
	author := a.people["author"].account.ID
	as := a.as("author")

	// The entries of others: of the importer, who has no account, and of an editor.
	byEditor, _ := a.draftPage(t, "editor", "en", "Editor's", "editors")
	a.waitBuilds(t)
	for _, entry := range []string{sampleEntry, byEditor} {
		for _, w := range a.publishWrites(t, entry) {
			a.wantForbidden(t, "author", w)
		}
		if got := a.entryDoc(t, entry); entry == byEditor && got.Status != content.StatusDraft {
			t.Errorf("the entry of the editor is %+v, want a draft", got)
		}
	}
	if _, ok := a.page("en/editors/"); ok {
		t.Error("the page of the editor is published")
	}

	// Its own: published, taken back, archived and scheduled.
	mine, _ := a.draftPage(t, "author", "en", "Mine", "mine")
	item := "/api/entries/" + mine
	steps := []struct {
		route string
		body  func() string
		want  content.Status
	}{
		{"/publish", func() string { return a.publishBody(t, mine) }, content.StatusPublished},
		{"/unpublish", func() string { return a.baseBody(t, mine) }, content.StatusDraft},
		{"/archive", func() string { return a.baseBody(t, mine) }, content.StatusArchived},
		{"/schedule", func() string { return a.scheduleBody(t, mine, time.Hour) }, content.StatusScheduled},
	}
	for _, s := range steps {
		res := result(t, "POST "+item+s.route, as.post(item+s.route, s.body()), http.StatusOK)
		if res.Status != s.want || res.Version.SavedBy != author {
			t.Errorf("POST %s answers %+v, want the status %s saved by the author", s.route, res, s.want)
		}
		a.waitBuilds(t)
	}

	// An editor and an administrator set the state of the entry of the author.
	for _, who := range []struct {
		login, route string
		body         func() string
		want         content.Status
	}{
		{"editor", "/publish", func() string { return a.publishBody(t, mine) }, content.StatusPublished},
		{"admin", "/archive", func() string { return a.baseBody(t, mine) }, content.StatusArchived},
	} {
		res := result(t, who.login+" POST "+item+who.route, a.as(who.login).post(item+who.route, who.body()), http.StatusOK)
		if res.Status != who.want || res.Version.SavedBy != a.people[who.login].account.ID {
			t.Errorf("POST %s as %s answers %+v, want the status %s saved by them", who.route, who.login, res, who.want)
		}
	}
	a.waitBuilds(t)
	records := a.mine(t, audit.EntryPublish, mine)
	if len(records) != 2 || records[0].Actor != author || records[1].Actor != a.people["editor"].account.ID {
		t.Errorf("the records of entry.publish for the entry of the author are %+v, want one by the author and one by the editor", records)
	}
	if got := len(a.records(t, audit.AccessDenied)); got != 2*4 {
		t.Errorf("%d records of access.denied, want the 8 refusals", got)
	}
	if logged := a.logs.String(); logged != "" {
		t.Errorf("the log is %q, want nothing", logged)
	}
}

// TestPublishRoleChangesInUpdate shows that the permission is checked again in the Update of each operation, with the role that
// the store has then: a request that the role allowed when it was received is refused if the role has changed since, and nothing
// is saved or asked for.
func TestPublishRoleChangesInUpdate(t *testing.T) {
	a := newAPIServer(t)
	for i, name := range publishOperations {
		t.Run(name, func(t *testing.T) {
			login := "victim" + strconv.Itoa(i)
			victim := a.victim(t, login, account.RoleEditor)
			e, _ := a.draftPage(t, login, "en", "Victim "+name, "victim-"+name)
			w := a.publishWrites(t, e)[i]
			a.waitBuilds(t)
			before, builds := a.versions(t), a.buildCount(t)

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
			n := len(a.records(t, audit.AccessDenied))
			checkProblem(t, w.name+" by an account whose role changes", a.as(login).send(w.method, w.path, w.body), "forbidden")
			a.hook = nil
			if fired.Load() != 1 {
				t.Fatalf("the hook ran %d times, want 1", fired.Load())
			}
			if got := a.versions(t); got != before {
				t.Errorf("the request has changed the store:\n%s\nwant\n%s", got, before)
			}
			records := a.records(t, audit.AccessDenied)
			if len(records) != n+1 {
				t.Fatalf("the refusal made %d records of access.denied, want 1", len(records)-n)
			}
			if r := records[n]; r.Actor != victim || r.Target != w.target || r.Detail["permission"] != string(account.PublishEntry) {
				t.Errorf("the record is %+v, want the actor %s, the permission entry.publish and the target %+v", r, victim, w.target)
			}
			a.noPublishRecords(t, e)
			a.waitBuilds(t)
			if st := readBuilds(t, a.as("admin")); !st.idle() || a.buildCount(t) != builds {
				t.Errorf("the state is %+v with %d builds, want nothing asked for", st, a.buildCount(t)-builds)
			}
			if logged := a.logs.String(); logged != "" {
				t.Errorf("the log is %q, want nothing", logged)
			}
		})
	}
}

// TestPublishCredentialsChangeInUpdate shows that a request whose account has other credentials when it is saved than when it
// came, such as after a reset, is answered as one without a session: 401, the cookie cleared, and nothing saved or recorded.
func TestPublishCredentialsChangeInUpdate(t *testing.T) {
	a := newAPIServer(t)
	for i, name := range publishOperations {
		t.Run(name, func(t *testing.T) {
			login := "reset" + strconv.Itoa(i)
			victim := a.victim(t, login, account.RoleEditor)
			e, _ := a.draftPage(t, login, "en", "Reset "+name, "reset-"+name)
			w := a.publishWrites(t, e)[i]
			a.waitBuilds(t)
			before := a.versions(t)

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
			n := len(a.records(t, audit.AccessDenied))
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
			a.noPublishRecords(t, e)
		})
	}
}

// TestPublishRetry shows that the header Idempotency-Key makes the operations happen once: a retry is answered as the first
// request was, also if the entry has changed since, it asks for no build, and the activity log has one record.
func TestPublishRetry(t *testing.T) {
	a := newAPIServer(t)
	as := a.as("editor")
	editor := a.people["editor"].account.ID
	e, rev := a.draftPage(t, "editor", "en", "Retry", "retry")
	item := "/api/entries/" + e
	a.waitBuilds(t)
	builds := a.buildCount(t)

	body := a.publishBody(t, e)
	pub := result(t, "the publishing", as.post(item+"/publish", body, key(1)...), http.StatusOK)
	a.waitBuilds(t)
	// The entry is taken back, so that the retry of the first request would be a publishing of an entry that is a draft.
	result(t, "the unpublishing", as.post(item+"/unpublish", a.baseBody(t, e), key(2)...), http.StatusOK)
	a.waitBuilds(t)
	if got := a.buildCount(t); got != builds+2 {
		t.Fatalf("%d builds ended, want 2", got-builds)
	}

	again := result(t, "the retry of the publishing", as.post(item+"/publish", body, key(1)...), http.StatusOK)
	if again.Version != pub.Version || again.Status != content.StatusPublished || again.Revision != rev || !again.Publishes {
		t.Errorf("the retry answers %+v, want %+v", again, pub)
	}
	if got := a.entryDoc(t, e).Status; got != content.StatusDraft {
		t.Errorf("the entry is %s after the retry, want a draft: the retry did nothing", got)
	}
	a.waitBuilds(t)
	if got := a.buildCount(t); got != builds+2 {
		t.Errorf("%d builds ended after the retry, want none more", got-builds-2)
	}
	if r := a.mine(t, audit.EntryPublish, e); len(r) != 1 || r[0].Operation != "op_01JD0000000000000000000001" || r[0].Actor != editor {
		t.Errorf("the records of entry.publish are %+v, want one with the operation ID of the key, by the editor", r)
	}

	// The key is for this request only: another body, another route and another account are other requests.
	checkProblem(t, "the key of a publishing with another body", as.post(item+"/publish", fmt.Sprintf(`{"base":1,"revision":%q}`, rev), key(1)...), "operation-reused")
	checkProblem(t, "the key of a publishing on another route", as.post(item+"/archive", a.baseBody(t, e), key(1)...), "operation-reused")
	checkProblem(t, "the key of a publishing by another account", a.as("admin").post(item+"/publish", body, key(1)...), "operation-reused")

	// A scheduling, and a retry of it after the entry was published by another request.
	at := a.scheduleBody(t, e, time.Hour)
	scheduled := result(t, "the scheduling", as.post(item+"/schedule", at, key(3)...), http.StatusOK)
	result(t, "the publishing after it", as.post(item+"/publish", a.publishBody(t, e), key(4)...), http.StatusOK)
	retried := result(t, "the retry of the scheduling", as.post(item+"/schedule", at, key(3)...), http.StatusOK)
	if retried.Version != scheduled.Version || retried.Status != content.StatusScheduled || retried.Publishes {
		t.Errorf("the retry of the scheduling answers %+v, want %+v", retried, scheduled)
	}
	if got := len(a.mine(t, audit.EntrySchedule, e)); got != 1 {
		t.Errorf("%d records of entry.schedule, want 1", got)
	}

	// An operation without the key is done again, and the old version is a conflict.
	checkProblem(t, "the unpublishing without a key at an old version", as.post(item+"/unpublish", `{"base":1}`), "conflict")
	if logged := a.logs.String(); logged != "" {
		t.Errorf("the log is %q, want nothing", logged)
	}
}

// TestPublishHeavyPlace shows that the routes of the publishing work in the place of heavy work, one at a time, as the other
// routes that call the Editor do, and that the routes of the builds do not wait for it.
func TestPublishHeavyPlace(t *testing.T) {
	a := newAPIServer(t)
	e, _ := a.draftPage(t, "editor", "en", "Place", "place")
	a.waitBuilds(t)
	before := a.versions(t)
	g := newGate(t, a.testServer)

	first := async(func() *http.Response { return a.as("admin").get("/api/settings") })
	g.waitHeld(t)
	writes := a.publishWrites(t, e)
	var waiting []<-chan *http.Response
	for _, w := range writes {
		ch := async(func() *http.Response { return a.as("editor").send(w.method, w.path, w.body) })
		waiting = append(waiting, ch)
		quiet(t, ch, "a request was answered while another had the place: "+w.name)
	}
	if n, held := g.calls.Load(), len(a.heavy); n != 1 || held != 1 {
		t.Errorf("%d requests reached the place and it holds %d, want 1 and 1", n, held)
	}
	if got := a.versions(t); got != before {
		t.Errorf("a request has worked while another had the place:\n%s\nwant\n%s", got, before)
	}

	// The state of the builds and the releases are answered without the place.
	state := async(func() []int {
		return []int{a.as("viewer").get("/api/builds").StatusCode, a.as("viewer").get("/api/releases").StatusCode}
	})
	if got := receive(t, state, "the state of the builds while the place is taken"); got[0] != http.StatusOK || got[1] != http.StatusOK {
		t.Errorf("the state of the builds and the releases answer %v while the place is taken, want 200 and 200", got)
	}

	g.open()
	receive(t, first, "the first request")
	statuses := make([]int, 0, len(waiting))
	for _, ch := range waiting {
		statuses = append(statuses, receive(t, ch, "a request that waited").StatusCode)
	}
	// They went one at a time in an order that is not fixed, so only the first of them could do what it asked: the others find
	// the entry at another version, or in a state they ask for already.
	ok := 0
	for _, s := range statuses {
		switch s {
		case http.StatusOK:
			ok++
		case http.StatusConflict:
		default:
			t.Errorf("a request that waited has the status %d, want 200 or 409", s)
		}
	}
	if ok == 0 {
		t.Errorf("no request that waited was done: %v", statuses)
	}
	if n := len(a.heavy); n != 0 {
		t.Errorf("the place holds %d requests at the end, want none", n)
	}
}

// TestPublishPerAccount shows that the routes of the publishing count among the four heavy requests of an account, and the
// fifth is refused at once.
func TestPublishPerAccount(t *testing.T) {
	a := newAPIServer(t)
	adminID := a.people["admin"].account.ID
	e, _ := a.draftPage(t, "editor", "en", "Count", "count")
	a.waitBuilds(t)
	before := a.versions(t)
	g := newGate(t, a.testServer)

	var four [heavyPerAccount]<-chan *http.Response
	four[0] = async(func() *http.Response { return a.as("admin").get("/api/settings") })
	g.waitHeld(t)
	for i := 1; i < heavyPerAccount; i++ {
		four[i] = async(func() *http.Response { return a.as("admin").get("/api/entries?limit=1") })
	}
	waitFor(t, "four requests of the admin to be counted", func() bool { return a.inflight.count(adminID) == heavyPerAccount })
	for _, w := range a.publishWrites(t, e) {
		checkProblem(t, "the fifth request, "+w.name, a.as("admin").send(w.method, w.path, w.body), "too-many-requests")
	}
	if got := a.versions(t); got != before {
		t.Errorf("the requests that were refused have changed the store:\n%s\nwant\n%s", got, before)
	}
	g.open()
	for i, ch := range four {
		if res := receive(t, ch, "a request of the admin"); res.StatusCode != http.StatusOK {
			t.Errorf("request %d of the admin has the status %d", i+1, res.StatusCode)
		}
	}
	waitFor(t, "the requests of the admin to end", func() bool { return a.inflight.count(adminID) == 0 })
}
