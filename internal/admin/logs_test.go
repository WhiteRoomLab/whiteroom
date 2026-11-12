package admin

import (
	"bufio"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/account"
	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

// logPage is the answer to a reading of a log: the records as the JSON that was sent, and the number to give as before.
type logPage struct {
	Records []jsontext.Value `json:"records"`
	Next    *uint64          `json:"next"`
}

// logsOf reads a page of the log (login or activity) as the account login, and returns it with the records decoded. The
// query is the part of the URL from the question mark, or empty.
func (a *apiServer) logsOf(t *testing.T, login, log, query string) (logPage, []audit.Record) {
	t.Helper()
	what := "GET /api/logs/" + log + query
	res := a.as(login).get("/api/logs/" + log + query)
	text := body(t, what, res, http.StatusOK)
	var page logPage
	if err := json.Unmarshal([]byte(text), &page); err != nil {
		t.Fatalf("%s: the answer %.300s is not a page of a log: %v", what, text, err)
	}
	records := make([]audit.Record, 0, len(page.Records))
	for _, raw := range page.Records {
		r, err := audit.Decode(raw)
		if err != nil {
			t.Fatalf("%s: a record of the answer is not one: %v", what, err)
		}
		records = append(records, r)
	}
	return page, records
}

// addWrites adds n records of store.write to the activity log by the actor, in one Update; the detail i of a record is its
// number among them, from 1.
func (a *apiServer) addWrites(t *testing.T, actor string, n int) {
	t.Helper()
	a.addPaddedWrites(t, actor, n, 0)
}

// addPaddedWrites is addWrites with a detail pad of pad bytes in each record, which makes a page of them as long as a test needs.
func (a *apiServer) addPaddedWrites(t *testing.T, actor string, n, pad int) {
	t.Helper()
	err := a.db.Update(t.Context(), func(tx *store.Tx) error {
		for i := 1; i <= n; i++ {
			detail := map[string]string{"i": strconv.Itoa(i)}
			if pad > 0 {
				detail["pad"] = strings.Repeat("x", pad)
			}
			r := audit.Record{Log: audit.ActivityLog, Actor: actor, Action: audit.StoreWrite, Detail: detail}
			if err := tx.Record(r); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// details returns the detail i of the records.
func details(records []audit.Record) []string {
	out := make([]string, len(records))
	for i, r := range records {
		out[i] = r.Detail["i"]
	}
	return out
}

// countdown returns the numbers from first down to last as the texts of the detail i.
func countdown(first, last int) []string {
	var out []string
	for i := first; i >= last; i-- {
		out = append(out, strconv.Itoa(i))
	}
	return out
}

func equalTexts(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// TestLogsPages shows the pages of a log: the newest records first, a page of the limit that was asked for or of fifty, and a
// number in each page but the last with which the page after it is asked for.
func TestLogsPages(t *testing.T) {
	a := newAPIServer(t)
	editor := a.people["editor"].account.ID
	a.addWrites(t, editor, 120)
	q := "action=store.write&account=" + editor

	page, records := a.logsOf(t, "admin", "activity", "?"+q)
	if !equalTexts(details(records), countdown(120, 71)) {
		t.Fatalf("the first page has the records %v, want the last 50, newest first", details(records))
	}
	for i := 1; i < len(records); i++ {
		if records[i].N >= records[i-1].N {
			t.Fatalf("the numbers of the first page are not decreasing: %d then %d", records[i-1].N, records[i].N)
		}
	}
	if page.Next == nil || *page.Next != records[49].N {
		t.Fatalf("the first page has the next %v, want the number of its oldest record, %d", page.Next, records[49].N)
	}
	// The page after: the records before that one, and not that one.
	page, records = a.logsOf(t, "admin", "activity", "?"+q+"&before="+strconv.FormatUint(*page.Next, 10))
	if !equalTexts(details(records), countdown(70, 21)) || page.Next == nil || *page.Next != records[49].N {
		t.Fatalf("the second page has the records %v and the next %v, want 70 to 21 and the number of the oldest", details(records), page.Next)
	}
	page, records = a.logsOf(t, "admin", "activity", "?"+q+"&before="+strconv.FormatUint(*page.Next, 10))
	if !equalTexts(details(records), countdown(20, 1)) || page.Next != nil {
		t.Fatalf("the last page has the records %v and the next %v, want 20 to 1 and no next", details(records), page.Next)
	}
	oldest := records[19].N

	// The limit.
	page, records = a.logsOf(t, "admin", "activity", "?"+q+"&limit=1")
	if !equalTexts(details(records), []string{"120"}) || page.Next == nil || *page.Next != records[0].N {
		t.Errorf("the page of limit 1 has %v and the next %v, want the newest record and its number", details(records), page.Next)
	}
	page, records = a.logsOf(t, "admin", "activity", "?"+q+"&limit=100")
	if !equalTexts(details(records), countdown(120, 21)) || page.Next == nil {
		t.Errorf("the page of limit 100 has %d records and the next %v, want 120 to 21 and a next", len(records), page.Next)
	}
	page, records = a.logsOf(t, "admin", "activity", "?"+q+"&limit=100&before="+strconv.FormatUint(*page.Next, 10))
	if !equalTexts(details(records), countdown(20, 1)) || page.Next != nil {
		t.Errorf("the page of limit 100 after it has %v and the next %v, want 20 to 1 and no next", details(records), page.Next)
	}
	// A page that is full and has nothing before it has no next: the records are exactly the limit.
	page, records = a.logsOf(t, "admin", "activity", "?"+q+"&limit=20&before="+strconv.FormatUint(oldest+20, 10))
	if !equalTexts(details(records), countdown(20, 1)) || page.Next != nil {
		t.Errorf("the page of the 20 oldest has %v and the next %v, want 20 to 1 and no next", details(records), page.Next)
	}

	// Nothing before the first record, nor of what the query does not select.
	for what, query := range map[string]string{
		"before the first record": "?" + q + "&before=" + strconv.FormatUint(oldest, 10),
		"before 1":                "?before=1",
		"of another account":      "?action=store.write&account=" + a.people["viewer"].account.ID,
	} {
		if got := body(t, "GET the log "+what, a.as("admin").get("/api/logs/activity"+query), http.StatusOK); got != `{"records":[]}` {
			t.Errorf("the page %s is %s, want an empty list and no next", what, got)
		}
	}
	if logged := a.logs.String(); logged != "" {
		t.Errorf("the log is %q, want nothing", logged)
	}
}

// TestLogsRecordsAsStored shows that a record is answered as the store has it: the same keys in the same order, and the
// same bytes.
func TestLogsRecordsAsStored(t *testing.T) {
	a := newAPIServer(t)
	editor := a.people["editor"].account.ID
	a.addWrites(t, editor, 5)
	a.newEntry(t, "editor", "post", "ja")
	want := map[uint64]string{}
	err := a.db.ReadAudit(t.Context(), audit.Filter{Log: audit.ActivityLog, Account: editor}, func(r audit.Record) bool {
		data, err := audit.Encode(r)
		if err != nil {
			t.Fatal(err)
		}
		want[r.N] = string(data)
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	page, records := a.logsOf(t, "admin", "activity", "?account="+editor)
	if len(records) != len(want) || len(want) < 6 {
		t.Fatalf("the page has %d records, the store %d, want the same and at least 6", len(records), len(want))
	}
	for i, raw := range page.Records {
		if string(raw) != want[records[i].N] {
			t.Errorf("record %d is\n%s\nwant\n%s", records[i].N, raw, want[records[i].N])
		}
	}
	if first := string(page.Records[0]); !strings.HasPrefix(first, `{"log":"activity","n":`) || !strings.Contains(first, `,"actor":"`+editor+`","action":"entry.create","target":{"kind":"entry","id":"ent_`) {
		t.Errorf("the newest record is %s, want the keys of a record in the order that the store writes them", first)
	}
	if got := string(page.Records[1]); !strings.HasSuffix(got, `"action":"store.write","detail":{"i":"5"}}`) {
		t.Errorf("the second record is %s, want the record of store.write with its detail", got)
	}
}

// TestLogsFilters shows what each name of the query selects.
func TestLogsFilters(t *testing.T) {
	a := newAPIServer(t)
	editor, admin := a.people["editor"].account.ID, a.people["admin"].account.ID
	e1 := a.newEntry(t, "editor", "post", "ja").ID
	e2 := a.newEntry(t, "admin", "post", "ja").ID
	a.newEntry(t, "editor", "page", "ja")
	a.addWrites(t, admin, 3)

	ids := func(query string) []string {
		t.Helper()
		_, records := a.logsOf(t, "admin", "activity", query)
		out := make([]string, len(records))
		for i, r := range records {
			out[i] = r.Target.ID
		}
		return out
	}
	if _, records := a.logsOf(t, "admin", "activity", "?action=entry.create"); len(records) != 3 {
		t.Errorf("the name of an action selects %d records, want the 3 entries that were made", len(records))
	}
	_, records := a.logsOf(t, "admin", "activity", "?action=entry.")
	for _, r := range records {
		if !strings.HasPrefix(string(r.Action), "entry.") {
			t.Errorf("a prefix selects the record of %s", r.Action)
		}
	}
	if len(records) != 3 {
		t.Errorf("a prefix selects %d records, want the 3 entries that were made", len(records))
	}
	if _, records := a.logsOf(t, "admin", "activity", "?action=store.write"); len(records) != 3 {
		t.Errorf("the name of an action selects %d records, want the 3 that were added", len(records))
	}

	// The account is the actor, the account or the target of a record.
	_, records = a.logsOf(t, "admin", "activity", "?account="+editor+"&action=entry.create")
	if len(records) != 2 {
		t.Errorf("the account of the editor has %d records of entry.create, want 2", len(records))
	}
	_, records = a.logsOf(t, "admin", "activity", "?account="+admin+"&action=entry.create")
	if len(records) != 1 || records[0].Target.ID != e2 {
		t.Errorf("the account of the admin has the records %+v of entry.create, want the one of its entry", records)
	}
	_, records = a.logsOf(t, "admin", "activity", "?account="+editor+"&action=account.invite")
	if len(records) != 1 || records[0].Target.ID != editor {
		t.Errorf("the account of the editor has %+v of account.invite, want the invitation that is about it", records)
	}
	_, records = a.logsOf(t, "admin", "activity", "?account=system&limit=100")
	if len(records) < 5 {
		t.Errorf("the system has %d records, want at least the creation of the store and the 4 invitations", len(records))
	}
	for _, r := range records {
		if r.Actor != audit.System {
			t.Errorf("the system has a record by %q", r.Actor)
		}
	}

	// The target.
	if got := ids("?targetKind=entry&targetId=" + e1); len(got) != 1 || got[0] != e1 {
		t.Errorf("the target of an entry selects %v, want its record", got)
	}
	if got := ids("?targetId=" + e1); len(got) != 1 || got[0] != e1 {
		t.Errorf("an ID without a kind selects %v, want the record of the entry", got)
	}
	_, records = a.logsOf(t, "admin", "activity", "?targetKind=entry&limit=100")
	for _, r := range records {
		if r.Target.Kind != "entry" {
			t.Errorf("a kind selects the record of %+v", r.Target)
		}
	}
	if len(records) < 3 {
		t.Errorf("the kind entry selects %d records, want at least 3", len(records))
	}
	_, records = a.logsOf(t, "admin", "activity", "?targetKind=user&limit=100")
	if len(records) < 8 {
		t.Errorf("the kind user selects %d records, want at least the 8 of the accounts of the test", len(records))
	}

	// The time: from is included and to is not.
	_, all := a.logsOf(t, "admin", "activity", "?action=entry.create")
	newest := all[0].At
	at := url.QueryEscape(newest.Format(time.RFC3339Nano))
	if _, records := a.logsOf(t, "admin", "activity", "?action=entry.create&from="+at); len(records) != 1 || records[0].N != all[0].N {
		t.Errorf("from the time of the newest record selects %d records, want it alone", len(records))
	}
	if _, records := a.logsOf(t, "admin", "activity", "?action=entry.create&to="+at); len(records) != 2 {
		t.Errorf("up to the time of the newest record selects %d records, want the 2 before it", len(records))
	}
	if _, records := a.logsOf(t, "admin", "activity", "?action=entry.create&from=2000-01-01T00:00:00Z&to=2100-01-01T00:00:00%2B09:00"); len(records) != 3 {
		t.Errorf("a time from long ago to long after selects %d records, want 3", len(records))
	}
	for what, query := range map[string]string{"from the future": "?from=2100-01-01T00:00:00Z", "up to the past": "?to=2000-01-01T00:00:00Z"} {
		if got := body(t, "GET the log "+what, a.as("admin").get("/api/logs/activity"+query), http.StatusOK); got != `{"records":[]}` {
			t.Errorf("the page %s is %s, want an empty list", what, got)
		}
	}

	// The two logs are two logs.
	if _, records := a.logsOf(t, "admin", "login", "?action=entry.create"); len(records) != 0 {
		t.Errorf("the login history has %d records of entry.create", len(records))
	}
	if _, records := a.logsOf(t, "admin", "activity", "?action=login"); len(records) != 0 {
		t.Errorf("the activity log has %d records of login", len(records))
	}
}

// TestLogsLoginHistory shows what the login history of a test server holds: the logins, with where from, and the failures,
// with the login name that was typed.
func TestLogsLoginHistory(t *testing.T) {
	a := newAPIServer(t)
	const typed = "someone.else"
	a.postLogin(typed, "a password for nobody", "User-Agent", "TestAgent/1.0")
	_, logins := a.logsOf(t, "admin", "login", "?action=login")
	if len(logins) != 4 {
		t.Fatalf("the login history has %d records of login, want the 4 of the accounts", len(logins))
	}
	ids := map[string]bool{}
	for login, p := range a.people {
		ids[p.account.ID] = true
		_, mine := a.logsOf(t, "admin", "login", "?account="+p.account.ID)
		if len(mine) == 0 {
			t.Errorf("the login history has no record of %s", login)
		}
	}
	for _, r := range logins {
		if !ids[r.Account] || len(r.Session) != 64 || r.Client.Addr != "192.0.2.1" || r.Actor != "" || r.Log != audit.LoginLog {
			t.Errorf("the record of a login is %+v, want an account of the test, the key of a session and the address of the client", r)
		}
	}
	_, failed := a.logsOf(t, "admin", "login", "?action=login.failure")
	if len(failed) != 1 || failed[0].Login != typed || failed[0].Result != "unknown-login" || failed[0].Client.UserAgent != "TestAgent/1.0" {
		t.Errorf("the failures are %+v, want the one with the login name that was typed, its reason and the user agent", failed)
	}
}

// TestLogsRoles shows that only the administrator reads the logs, and that a refusal is recorded with the permission.
func TestLogsRoles(t *testing.T) {
	a := newAPIServer(t)
	for _, login := range []string{"editor", "author", "viewer"} {
		for _, log := range []string{"activity", "login"} {
			a.wantForbidden(t, login, write{"read the " + log + " log", http.MethodGet, "/api/logs/" + log, "", account.ViewLogs, audit.Target{}})
		}
		// The permission is asked first: a request that would be refused for its query is a refusal for its account.
		a.clock.advance(time.Minute)
		checkProblem(t, "a bad query by "+login, a.as(login).get("/api/logs/activity?limit=0"), "forbidden")
		a.clock.advance(time.Minute)
		checkProblem(t, "a key by "+login, a.as(login).send(http.MethodGet, "/api/logs/activity", "", key(1)...), "forbidden")
	}
	checkProblem(t, "no session", a.send(http.MethodGet, "/api/logs/activity", ""), "unauthenticated")
	checkProblem(t, "POST", a.as("admin").post("/api/logs/activity", ""), "method-not-allowed")
	checkProblem(t, "a log that is none", a.as("admin").get("/api/logs/other"), "not-found")
	if logged := a.logs.String(); logged != "" {
		t.Errorf("the log is %q, want nothing", logged)
	}
}

// TestLogsQueries shows the queries that are none.
func TestLogsQueries(t *testing.T) {
	a := newAPIServer(t)
	admin := a.as("admin")
	for _, q := range []string{
		"from=yesterday", "from=2026-11-12", "from=2026-11-12T10:00:00", "to=1", "to=", "action=", "action=nope", "action=entry", "action=entry.nope.",
		"account=editor", "account=usr_x", "account=", "targetKind=nope", "targetKind=", "targetId=x", "targetId=", "targetKind=user&targetId=ent_01JB8Y6T2WT6X9C3E6G9J2K5M8",
		"before=0", "before=-1", "before=abc", "before=", "before=9223372036854775808", "limit=0", "limit=101", "limit=-1", "limit=x", "limit=",
		"offset=0", "page=1", "q=a", "limit=1&limit=2", "action=entry.&action=term.", "limit=1;before=2",
	} {
		checkProblem(t, "GET the log with the query "+q, admin.get("/api/logs/activity?"+q), "bad-query")
		checkProblem(t, "GET the login history with the query "+q, admin.get("/api/logs/login?"+q), "bad-query")
	}
	checkProblem(t, "GET the log with a key", admin.send(http.MethodGet, "/api/logs/activity", "", key(1)...), "bad-header")
	if got := len(a.records(t, audit.AccessDenied)); got != 0 {
		t.Errorf("queries that are none made %d records of access.denied, want none", got)
	}
	for _, q := range []string{"limit=1", "limit=100", "before=1", "before=9223372036854775807", "from=2026-11-12T10:00:00%2B09:00&to=2026-11-12T10:00:00.5Z",
		"account=system", "action=build.", "targetKind=release", "targetKind=settings"} {
		body(t, "GET the log with the query "+q, admin.get("/api/logs/activity?"+q), http.StatusOK)
	}
}

// TestLogsPlace shows that the logs have a place of their own, for one reading at a time: a second reading is told to come
// back, and does not wait; and the places of the logs and of heavy work are two.
func TestLogsPlace(t *testing.T) {
	a := newAPIServer(t)
	admin := a.as("admin")
	adminID := a.people["admin"].account.ID
	g := newGate(t, a.testServer)

	first := async(func() *http.Response { return admin.get("/api/logs/activity") })
	g.waitHeld(t)
	if n, h := len(a.logReads), len(a.heavy); n != 1 || h != 0 {
		t.Errorf("the place of the logs holds %d readings and that of heavy work %d, want 1 and 0", n, h)
	}
	for _, path := range []string{"/api/logs/activity", "/api/logs/login", "/api/logs/activity?limit=1&action=store.create"} {
		start := time.Now()
		res := admin.get(path)
		checkProblem(t, "a second reading, "+path, res, "busy")
		if took := time.Since(start); took > 2*time.Second {
			t.Errorf("a second reading waited %v, want no wait", took)
		}
	}
	if n := a.inflight.count(adminID); n != 1 {
		t.Errorf("the admin has %d requests counted, want the one that reads: a reading that is told to come back is not counted", n)
	}
	// What is not a reading of a log has its own places, or none.
	body(t, "GET /api/entries while a log is read", a.as("editor").get("/api/entries?limit=1"), http.StatusOK)
	body(t, "GET /api/accounts while a log is read", a.as("viewer").get("/api/accounts"), http.StatusOK)
	body(t, "GET /api/builds while a log is read", a.as("viewer").get("/api/builds"), http.StatusOK)
	if got := g.calls.Load(); got < 2 {
		t.Errorf("the hook was reached %d times, want the reading and the entries", got)
	}
	// A person who may not read is refused, and not told that the place is taken.
	a.wantForbidden(t, "editor", write{"read the log", http.MethodGet, "/api/logs/activity", "", account.ViewLogs, audit.Target{}})

	g.open()
	res := receive(t, first, "the first reading")
	page := body(t, "the first reading", res, http.StatusOK)
	if !strings.HasPrefix(page, `{"records":[`) {
		t.Errorf("the first reading answers %.100s", page)
	}
	waitFor(t, "the place of the logs to be free", func() bool { return len(a.logReads) == 0 && a.inflight.count(adminID) == 0 })
	body(t, "a reading after the first", admin.get("/api/logs/login"), http.StatusOK)
	if logged := a.logs.String(); logged != "" {
		t.Errorf("the log is %q, want nothing", logged)
	}
}

// TestLogsAnswerIsSentWithoutThePlace shows that the place of the logs is given back before the answer is sent, so that a
// reader who does not read holds a request of its account and nothing more.
func TestLogsAnswerIsSentWithoutThePlace(t *testing.T) {
	a := newAPIServer(t)
	admin := a.as("admin")
	adminID := a.people["admin"].account.ID
	stopped, opened := make(chan struct{}, 1), make(chan struct{})
	var calls atomic.Int32
	a.hook = func(point string) {
		if point == "sending" && calls.Add(1) == 1 {
			stopped <- struct{}{}
			<-opened
		}
	}
	t.Cleanup(func() {
		select {
		case <-opened:
		default:
			close(opened)
		}
	})

	first := async(func() *http.Response { return admin.get("/api/logs/activity") })
	receive(t, stopped, "the first answer to be made")
	if n := len(a.logReads); n != 0 {
		t.Errorf("the place of the logs holds %d readings while an answer waits to be sent, want none", n)
	}
	body(t, "a reading while the answer of another is not sent", admin.get("/api/logs/login"), http.StatusOK)
	quiet(t, first, "the first reading was answered while it was stopped before it sent")
	if n := a.inflight.count(adminID); n != 1 {
		t.Errorf("the admin has %d requests counted while an answer waits, want 1", n)
	}
	close(opened)
	body(t, "the first reading", receive(t, first, "the first reading"), http.StatusOK)
	waitFor(t, "the request of the admin to end", func() bool { return a.inflight.count(adminID) == 0 })
}

// TestLogsCountAmongFour shows that a reading of a log is one of the four requests of its account, with the heavy ones.
func TestLogsCountAmongFour(t *testing.T) {
	a := newAPIServer(t)
	admin := a.as("admin")
	adminID := a.people["admin"].account.ID
	held, opened := make(chan struct{}, 8), make(chan struct{})
	a.hook = func(point string) {
		if point == "checked" {
			held <- struct{}{}
			<-opened
		}
	}
	t.Cleanup(func() {
		select {
		case <-opened:
		default:
			close(opened)
		}
	})

	reading := async(func() *http.Response { return admin.get("/api/logs/activity") })
	receive(t, held, "the reading to have its place")
	var heavy [heavyPerAccount - 1]<-chan *http.Response
	heavy[0] = async(func() *http.Response { return admin.get("/api/entries?limit=1") })
	receive(t, held, "the first heavy request to have its place")
	for i := 1; i < len(heavy); i++ {
		heavy[i] = async(func() *http.Response { return admin.get("/api/entries?limit=1") })
	}
	waitFor(t, "four requests of the admin to be counted", func() bool { return a.inflight.count(adminID) == heavyPerAccount })
	// The fifth is refused for the count, and not for the place.
	checkProblem(t, "a fifth request, a reading of the activity log", admin.get("/api/logs/activity"), "too-many-requests")
	checkProblem(t, "a fifth request, a reading of the login history", admin.get("/api/logs/login"), "too-many-requests")

	close(opened)
	body(t, "the reading", receive(t, reading, "the reading"), http.StatusOK)
	for i, ch := range heavy {
		if res := receive(t, ch, "a heavy request"); res.StatusCode != http.StatusOK {
			t.Errorf("heavy request %d has the status %d", i+1, res.StatusCode)
		}
	}
	waitFor(t, "the requests of the admin to end", func() bool { return a.inflight.count(adminID) == 0 })
	body(t, "a reading after them", admin.get("/api/logs/activity"), http.StatusOK)
}

// TestLogsDeadline shows the time that a reading may take, and that a reading which is not done when it ends has no answer but
// the one that tells to come back.
func TestLogsDeadline(t *testing.T) {
	a := newAPIServer(t)
	admin := a.as("admin")
	adminID := a.people["admin"].account.ID
	if logDeadline != 120*time.Second {
		t.Errorf("logDeadline is %v, want 120 seconds", logDeadline)
	}
	routes := []string{"GET /api/logs/login", "GET /api/logs/activity"}
	for _, route := range routes {
		if got := a.deadlineOf(route); got != 120*time.Second {
			t.Errorf("%s has the deadline %v, want 120 seconds, and not the %v of the server", route, got, defaultDeadline)
		}
	}
	// With a deadline that ends while the log is read, the answer is the 503, whole, with none of what was read.
	a.deadlinesMu.Lock()
	a.deadlines[routes[1]] = 100 * time.Millisecond
	a.deadlinesMu.Unlock()
	a.hook = func(point string) {
		if point == "checked" {
			time.Sleep(400 * time.Millisecond)
		}
	}
	checkProblem(t, "a reading that is not done in time", admin.get("/api/logs/activity"), "busy")
	a.hook = nil
	if n := len(a.logReads); n != 0 || a.inflight.count(adminID) != 0 {
		t.Errorf("the place of the logs holds %d readings and the admin has %d requests after it, want none", n, a.inflight.count(adminID))
	}
	if logged := a.logs.String(); logged != "" {
		t.Errorf("the log is %q, want nothing: a request that is not answered in time is not logged", logged)
	}
	a.deadlinesMu.Lock()
	a.deadlines[routes[1]] = logDeadline
	a.deadlinesMu.Unlock()
	body(t, "a reading after it", admin.get("/api/logs/activity"), http.StatusOK)
}

// TestLogsReaderGoesAway shows that a reading whose request has ended is dropped: the place is given back, and the account's
// count with it.
func TestLogsReaderGoesAway(t *testing.T) {
	a := newAPIServer(t)
	adminID := a.people["admin"].account.ID
	ctx, cancel := context.WithCancel(t.Context())
	a.hook = func(point string) {
		if point == "checked" {
			cancel()
		}
	}
	res := a.sendWithContext(ctx, http.MethodGet, "/api/logs/activity", "", a.cookie(a.people["admin"].token)...)
	a.hook = nil
	checkProblem(t, "a reading whose request has ended", res, "busy")
	if n := len(a.logReads); n != 0 || a.inflight.count(adminID) != 0 {
		t.Errorf("the place of the logs holds %d readings and the admin has %d requests after it, want none", n, a.inflight.count(adminID))
	}
	body(t, "a reading after it", a.as("admin").get("/api/logs/activity"), http.StatusOK)
}

// TestLogsSlowReader shows that the answer is not written before the place is given back, on a real connection: while the
// answer waits to be sent, nothing has gone out on it, and another reading is let in. The answer is much longer than what the
// server keeps before it writes to the connection, so that a part of it would be on the connection if it were written.
func TestLogsSlowReader(t *testing.T) {
	a, ls := newLiveAPIServer(t)
	a.addPaddedWrites(t, a.people["editor"].account.ID, 100, 1500)
	stopped, opened := make(chan struct{}, 1), make(chan struct{})
	var calls atomic.Int32
	var holding atomic.Int32
	a.hook = func(point string) {
		if point == "sending" && calls.Add(1) == 1 {
			if len(a.logReads) != 0 {
				holding.Add(1)
			}
			stopped <- struct{}{}
			<-opened
		}
	}
	t.Cleanup(func() {
		select {
		case <-opened:
		default:
			close(opened)
		}
	})

	host := strings.TrimPrefix(ls.url, "http://")
	request := "GET /api/logs/activity?limit=100&action=store.write HTTP/1.1\r\nHost: " + host + "\r\nCookie: " + ls.wantCookieName() + "=" + a.people["admin"].token +
		"\r\nConnection: close\r\n\r\n"
	conn := dialAndSend(t, host, request)
	receive(t, stopped, "the answer to be ready")

	// Nothing has gone out on the connection, though the answer is made.
	_ = conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	n, err := conn.Read(make([]byte, 1))
	var timeout net.Error
	if n != 0 || !errors.As(err, &timeout) || !timeout.Timeout() {
		t.Errorf("a read of the connection returned %d bytes and %v while the answer waited, want a timeout with nothing read", n, err)
	}
	if n := len(a.logReads); n != 0 {
		t.Errorf("the place of the logs holds %d readings while an answer waits to be sent, want none", n)
	}
	body(t, "a reading while the answer is not sent", a.as("admin").get("/api/logs/login"), http.StatusOK)

	close(opened)
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	res, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	text := body(t, "the answer on the connection", res, http.StatusOK)
	if !strings.HasPrefix(text, `{"records":[`) || len(text) < 100000 {
		t.Errorf("the answer on the connection is %d bytes, %.100s, want one of 100 records of more than 1500 bytes", len(text), text)
	}
	if holding.Load() != 0 {
		t.Errorf("the hook \"sending\" found the place of the logs taken %d times", holding.Load())
	}
	waitFor(t, "the place of the logs to be free", func() bool { return len(a.logReads) == 0 })
}

func TestNewestRecords(t *testing.T) {
	for _, tt := range []struct {
		limit, count int
		want         []uint64
		dropped      bool
	}{
		{3, 0, nil, false},
		{3, 2, []uint64{2, 1}, false},
		{3, 3, []uint64{3, 2, 1}, false},
		{3, 4, []uint64{4, 3, 2}, true},
		{3, 7, []uint64{7, 6, 5}, true},
		{3, 9, []uint64{9, 8, 7}, true},
		{1, 1, []uint64{1}, false},
		{1, 5, []uint64{5}, true},
	} {
		n := newestRecords{limit: tt.limit}
		for i := 1; i <= tt.count; i++ {
			n.add(audit.Record{N: uint64(i)})
		}
		var got []uint64
		for _, r := range n.newestFirst() {
			got = append(got, r.N)
		}
		if len(got) != len(tt.want) || n.dropped != tt.dropped {
			t.Errorf("limit %d with %d records: %v and dropped %v, want %v and %v", tt.limit, tt.count, got, n.dropped, tt.want, tt.dropped)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("limit %d with %d records: %v, want %v", tt.limit, tt.count, got, tt.want)
				break
			}
		}
	}
}

func TestWriteRecords(t *testing.T) {
	at := time.Date(2026, time.November, 12, 1, 2, 3, 456789000, time.UTC)
	older := audit.Record{Log: audit.ActivityLog, N: 7, At: at, Actor: "system", Action: audit.StoreWrite, Detail: map[string]string{"b": "2", "a": "1"}}
	newer := audit.Record{Log: audit.ActivityLog, N: 8, Prev: "sha256:" + strings.Repeat("a", 64), At: at.Add(time.Second), Action: "future.action"}
	text := func(r audit.Record) string {
		data, err := json.Marshal(r, json.Deterministic(true))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	for _, tt := range []struct {
		name    string
		records []audit.Record
		more    bool
		want    string
	}{
		{"none", nil, false, `{"records":[]}`},
		{"none and more", nil, true, `{"records":[]}`},
		{"two", []audit.Record{newer, older}, false, `{"records":[` + text(newer) + `,` + text(older) + `]}`},
		{"two and more", []audit.Record{newer, older}, true, `{"records":[` + text(newer) + `,` + text(older) + `],"next":7}`},
	} {
		var out strings.Builder
		if err := writeRecords(&out, tt.records, tt.more); err != nil || out.String() != tt.want {
			t.Errorf("%s: writeRecords() = %s, %v, want %s", tt.name, out.String(), err, tt.want)
		}
	}
	// A record is written as it is read, with the detail sorted, and an action that this build does not know is no failure.
	if got := text(older); !strings.Contains(got, `"detail":{"a":"1","b":"2"}`) || !strings.HasPrefix(got, `{"log":"activity","n":7,"at":"2026-11-12T01:02:03.456789Z"`) {
		t.Errorf("a record is written as %s", got)
	}
	if got := text(newer); !strings.Contains(got, `"action":"future.action"`) {
		t.Errorf("a record of an action that is not known is written as %s", got)
	}
}
