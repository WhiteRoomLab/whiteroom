package admin

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/WhiteRoomLab/whiteroom/internal/account"
	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/edit"
	"github.com/WhiteRoomLab/whiteroom/internal/publish"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

const (
	someTerm  = "trm_01JBA4C7E2G5J8K1M4P7R2T5W8"
	someEntry = "ent_01JB8Y6T2WT6X9C3E6G9J2K5M8"
	someOp    = "op_01JB8T7H4MA2C5E8G1J4K7N3P6"
)

// readProblem reads the body of res as a problem.
func readProblem(t *testing.T, res *http.Response) problem {
	t.Helper()
	body := bodyOf(t, res)
	var p problem
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		t.Fatalf("the body %s is not a problem: %v", body, err)
	}
	return p
}

func TestAnswerError(t *testing.T) {
	ts := newTestServer(t, httpsOrigin)
	p := account.Principal{Account: ts.addAccount(t, "editor", account.RoleEditor)}
	denied := fmt.Errorf("%s (editor): %w to edit terms", p.Account.ID, account.ErrForbidden)
	invalid := content.Errors{{Path: "/title", Msg: "missing title"}}
	tried := attempt{action: account.EditTerms, target: audit.Target{Kind: "term", ID: someTerm}}

	for _, tt := range []struct {
		name string
		err  error
		code string
	}{
		{"content.Errors", invalid, "invalid"},
		{"wrapped content.Errors", fmt.Errorf("saving: %w", invalid), "invalid"},
		{"edit.ErrConflict", fmt.Errorf("the document %w", edit.ErrConflict), "conflict"},
		{"account.ErrConflict", fmt.Errorf("the password of usr_x %w", account.ErrConflict), "conflict"},
		{"edit.ErrOperationReused", fmt.Errorf("operation ID %q %w", someOp, edit.ErrOperationReused), "operation-reused"},
		{"fs.ErrNotExist", fmt.Errorf("entries/x.json: %w", fs.ErrNotExist), "not-found"},
		{"account.ErrForbidden", denied, "forbidden"},
		{"account.ErrLastAdmin", fmt.Errorf("usr_x is %w: ending", account.ErrLastAdmin), "last-admin"},
		{"account.ErrInvalidToken", account.ErrInvalidToken, "invalid-token"},
		{"account.ErrDisabled", fmt.Errorf("usr_x %w", account.ErrDisabled), "disabled"},
		{"publish.ErrRollbackRunning", publish.ErrRollbackRunning, "rollback-running"},
		{"errSessionChanged", errSessionChanged, "unauthenticated"},
		{"account.ErrNoSession", fmt.Errorf("changing: %w", account.ErrNoSession), "unauthenticated"},
		{"context.Canceled", fmt.Errorf("reading: %w", context.Canceled), "busy"},
		{"context.DeadlineExceeded", context.DeadlineExceeded, "busy"},
		{"errBadBody", errBadBody, "bad-request"},
		{"another error", errors.New("the secret text of an error"), "internal"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ts.logs.Reset()
			before := len(ts.records(t, audit.AccessDenied))
			rec := httptest.NewRecorder()
			ts.answerError(rec, ts.request(http.MethodGet, "/api/test", ""), p, tt.err, tried)
			res := rec.Result()
			if tt.code == "invalid" {
				got := readProblem(t, res)
				if want := problems["invalid"]; got.Status != want.status || got.Code != "invalid" || got.Detail != want.detail ||
					len(got.Errors) != 1 || got.Errors[0].Pointer != "/title" || got.Errors[0].Detail != "missing title" {
					t.Errorf("the answer is %+v, want the problem invalid with the one error", got)
				}
			} else {
				checkProblem(t, tt.name, res, tt.code)
			}

			wantCookie := tt.code == "unauthenticated"
			if got := res.Header.Values("Set-Cookie"); wantCookie && !equalValues(got, ts.clearLine()) || !wantCookie && len(got) != 0 {
				t.Errorf("the answer sets the cookies %q, want %v", got, wantCookie)
			}

			records := ts.records(t, audit.AccessDenied)
			switch {
			case tt.code != "forbidden":
				if len(records) != before {
					t.Errorf("an answer that is not a refusal made %d records of access.denied", len(records)-before)
				}
			case len(records) != before+1:
				t.Errorf("the refusal made %d records of access.denied, want 1", len(records)-before)
			default:
				last := records[len(records)-1]
				if last.Actor != p.Account.ID || last.Target != tried.target || last.Detail["permission"] != "term.edit" || len(last.Detail) != 1 {
					t.Errorf("the record of the refusal is %+v, want the actor %s, the target %+v and the permission term.edit",
						last, p.Account.ID, tried.target)
				}
			}

			logged := ts.logs.String()
			if tt.code == "internal" {
				if want := "GET \"/api/test\": the secret text of an error\n"; logged != want {
					t.Errorf("the log is %q, want %q", logged, want)
				}
			} else if logged != "" {
				t.Errorf("the log is %q, want nothing", logged)
			}
		})
	}

	// The error of a check in an Update names what it refused, and the record tells that, and not what the route tried.
	named := audit.Target{Kind: "entry", ID: someEntry}
	err := &deniedError{action: account.EditEntry, named: named, err: denied}
	rec := httptest.NewRecorder()
	ts.answerError(rec, ts.request(http.MethodGet, "/api/test", ""), p, fmt.Errorf("saving: %w", err), tried)
	checkProblem(t, "a refusal of a check", rec.Result(), "forbidden")
	records := ts.records(t, audit.AccessDenied)
	if last := records[len(records)-1]; last.Target != named || last.Detail["permission"] != "entry.edit" {
		t.Errorf("the record is %+v, want the target %+v and the permission entry.edit", last, named)
	}
}

// TestAnswerErrorWrongPassword shows that the wrong current password of a change of the password is a problem of the field
// current, whatever the rest of the error says, and shows nothing of the account or of the password.
func TestAnswerErrorWrongPassword(t *testing.T) {
	ts := newTestServer(t, httpsOrigin)
	p := account.Principal{Account: ts.addAccount(t, "editor", account.RoleEditor)}
	for _, err := range []error{account.ErrBadCredentials, fmt.Errorf("changing: %w", account.ErrBadCredentials)} {
		rec := httptest.NewRecorder()
		ts.answerError(rec, ts.request(http.MethodPut, "/api/session/password", ""), p, err, attempt{})
		res := rec.Result()
		got := readProblem(t, res)
		if want := problems["invalid"]; got.Status != want.status || got.Code != "invalid" || got.Detail != want.detail ||
			len(got.Errors) != 1 || got.Errors[0].Pointer != "/current" || got.Errors[0].Detail != "wrong password" ||
			got.Errors[0].File != "" || got.Errors[0].Line != 0 || got.OmittedErrors != 0 {
			t.Errorf("the answer is %+v, want the problem invalid with the one error at /current", got)
		}
		if res.Header.Get("Content-Type") != "application/problem+json" || len(res.Header.Values("Set-Cookie")) != 0 {
			t.Errorf("the answer has the Content-Type %q and the cookies %q", res.Header.Get("Content-Type"), res.Header.Values("Set-Cookie"))
		}
	}
	if got := ts.records(t, audit.AccessDenied); len(got) != 0 || ts.logs.String() != "" {
		t.Errorf("a wrong password made %d records of access.denied and the log %q, want none", len(got), ts.logs.String())
	}
}

func TestWriteInvalid(t *testing.T) {
	rec := httptest.NewRecorder()
	writeInvalid(rec, content.Errors{
		{File: "revisions/rev_01JB8T7H4MA2C5E8G1J4K7N3P6.json", Path: "/body/3/attrs/level", Msg: "must be 1 to 6", Line: 12, Column: 9},
		{Msg: "too large"},
		{Path: "/title", Msg: "missing title", Line: 2},
	})
	res := rec.Result()
	if got := res.Header.Get("Content-Type"); res.StatusCode != http.StatusUnprocessableEntity || got != "application/problem+json" {
		t.Errorf("writeInvalid wrote %d %q, want 422 application/problem+json", res.StatusCode, got)
	}
	const want = `{"type":"about:blank","title":"Unprocessable Entity","status":422,"detail":"the request has problems","code":"invalid",` +
		`"errors":[{"file":"revisions/rev_01JB8T7H4MA2C5E8G1J4K7N3P6.json","pointer":"/body/3/attrs/level","line":12,"column":9,"detail":"must be 1 to 6"},` +
		`{"pointer":"","detail":"too large"},{"pointer":"/title","line":2,"detail":"missing title"}]}`
	if got := bodyOf(t, res); got != want {
		t.Errorf("writeInvalid wrote\n%s\nwant\n%s", got, want)
	}

	// Only the first maxProblems are listed, in their order, and the others are counted.
	many := make(content.Errors, 0, maxProblems+50)
	for i := range maxProblems + 50 {
		many = append(many, &content.Error{Path: fmt.Sprintf("/items/%d", i), Msg: fmt.Sprintf("problem %d", i)})
	}
	for _, n := range []int{maxProblems - 1, maxProblems, maxProblems + 1, maxProblems + 50} {
		rec := httptest.NewRecorder()
		writeInvalid(rec, many[:n])
		got := readProblem(t, rec.Result())
		listed := min(n, maxProblems)
		if len(got.Errors) != listed || got.OmittedErrors != n-listed {
			t.Errorf("%d problems: %d listed and %d omitted, want %d and %d", n, len(got.Errors), got.OmittedErrors, listed, n-listed)
		}
		for i, e := range got.Errors {
			if e.Pointer != fmt.Sprintf("/items/%d", i) || e.Detail != fmt.Sprintf("problem %d", i) {
				t.Errorf("%d problems: the problem %d is %+v", n, i, e)
				break
			}
		}
		body := rec.Body.String()
		if n <= maxProblems && strings.Contains(body, "omittedErrors") {
			t.Errorf("%d problems: the answer has omittedErrors", n)
		}
	}

	// Text that has no JSON form is written with U+FFFD, and the answer is still the problem.
	rec = httptest.NewRecorder()
	writeInvalid(rec, content.Errors{{File: "a\xff.json", Path: "/x\xff", Msg: "bad \xff text"}})
	got := readProblem(t, rec.Result())
	bad := string(utf8.RuneError)
	if rec.Code != http.StatusUnprocessableEntity || len(got.Errors) != 1 || got.Errors[0].File != "a"+bad+".json" ||
		got.Errors[0].Pointer != "/x"+bad || got.Errors[0].Detail != "bad "+bad+" text" {
		t.Errorf("writeInvalid of text that is not UTF-8 wrote %d %+v", rec.Code, got)
	}
}

func TestWriteResult(t *testing.T) {
	at := time.Date(2026, time.November, 9, 21, 0, 0, 123000000, time.FixedZone("JST", 9*3600))
	version := store.Version{
		Seq: 7, Kind: content.RefEntry, ID: someEntry, Hash: "sha256:" + strings.Repeat("ab", 32), SavedAt: at,
		SavedBy: "usr_01JB8T7H4MA2C5E8G1J4K7N3P6",
	}
	const versionJSON = `{"seq":7,"hash":"sha256:abababababababababababababababababababababababababababababababab",` +
		`"savedAt":"2026-11-09T12:00:00.123Z","savedBy":"usr_01JB8T7H4MA2C5E8G1J4K7N3P6"}`
	for _, tt := range []struct {
		name string
		res  edit.Result
		want string
	}{
		{
			"everything",
			edit.Result{
				Kind: content.RefEntry, ID: someEntry, Version: version, Revision: "rev_01JB8T7H4MA2C5E8G1J4K7N3P6", Redirect: "/old/",
				Number: 12, Status: content.StatusPublished, Publishes: true,
				Warnings: content.Errors{
					{File: "entries/x.json", Path: "/slug", Msg: "changed", Line: 4, Column: 3},
					{Msg: "second"},
				},
			},
			`{"kind":"entry","id":"` + someEntry + `","version":` + versionJSON + `,"revision":"rev_01JB8T7H4MA2C5E8G1J4K7N3P6",` +
				`"redirect":"/old/","number":12,"status":"published","publishes":true,` +
				`"warnings":[{"file":"entries/x.json","pointer":"/slug","line":4,"column":3,"detail":"changed"},{"pointer":"","detail":"second"}]}`,
		},
		{
			"what is empty, zero or false is left out, but publishes",
			edit.Result{Kind: content.RefTerm, ID: someTerm, Version: version},
			`{"kind":"term","id":"` + someTerm + `","version":` + versionJSON + `,"publishes":false}`,
		},
		{
			"the settings have no ID",
			edit.Result{Kind: store.SettingsKind, Version: version},
			`{"kind":"settings","version":` + versionJSON + `,"publishes":false}`,
		},
	} {
		var out bytes.Buffer
		if err := writeResult(&out, tt.res); err != nil {
			t.Fatalf("%s: writeResult() error = %v", tt.name, err)
		}
		if got := out.String(); got != tt.want {
			t.Errorf("%s: writeResult wrote\n%s\nwant\n%s", tt.name, got, tt.want)
		}
	}

	// Warnings are listed as problems are: the first maxProblems, and the others counted.
	warnings := make(content.Errors, maxProblems+3)
	for i := range warnings {
		warnings[i] = &content.Error{Msg: fmt.Sprintf("warning %d", i)}
	}
	var out bytes.Buffer
	if err := writeResult(&out, edit.Result{Kind: content.RefEntry, ID: someEntry, Version: version, Warnings: warnings}); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Warnings        []problemError `json:"warnings"`
		OmittedWarnings int            `json:"omittedWarnings"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Warnings) != maxProblems || got.OmittedWarnings != 3 || got.Warnings[maxProblems-1].Detail != "warning 99" {
		t.Errorf("%d warnings: %d listed and %d omitted, want %d and 3", len(warnings), len(got.Warnings), got.OmittedWarnings, maxProblems)
	}
}

// sampleDocument is JSON as a document of the store may be: with white space and line breaks that must stay.
const sampleDocument = "{\n  \"schemaVersion\": 1,\n  \"title\" : \"Hello \\u00e9\\n\",\n\t\"items\": [ 1,2 , 3 ]\n}\n"

func TestWriteDocuments(t *testing.T) {
	at := time.Date(2026, time.November, 9, 12, 0, 0, 0, time.UTC)
	docs := []store.Document{
		{Version: store.Version{Seq: 3, Hash: "sha256:" + strings.Repeat("0a", 32), SavedAt: at, SavedBy: "usr_a"}, Data: []byte(sampleDocument)},
		{Version: store.Version{Seq: 9, Hash: "sha256:" + strings.Repeat("1b", 32), SavedAt: at.Add(time.Hour), SavedBy: "usr_b"}, Data: []byte("{}")},
	}
	versionOfDoc := func(d store.Document) string {
		b, err := json.Marshal(versionOf(d.Version))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	var one bytes.Buffer
	if err := writeDocument(&one, docs[0]); err != nil {
		t.Fatal(err)
	}
	if want := `{"version":` + versionOfDoc(docs[0]) + `,"document":` + sampleDocument + `}`; one.String() != want {
		t.Errorf("writeDocument wrote\n%q\nwant\n%q", one.String(), want)
	}

	var list bytes.Buffer
	if err := writeDocuments(&list, docs, 25, 20, 5); err != nil {
		t.Fatal(err)
	}
	want := `{"items":[{"version":` + versionOfDoc(docs[0]) + `,"document":` + sampleDocument + `},` +
		`{"version":` + versionOfDoc(docs[1]) + `,"document":{}}],"total":25,"offset":20,"limit":5}`
	if list.String() != want {
		t.Errorf("writeDocuments wrote\n%q\nwant\n%q", list.String(), want)
	}
	var empty bytes.Buffer
	if err := writeDocuments(&empty, nil, 0, 0, 50); err != nil {
		t.Fatal(err)
	}
	if want := `{"items":[],"total":0,"offset":0,"limit":50}`; empty.String() != want {
		t.Errorf("writeDocuments of no documents wrote %s, want %s", empty.String(), want)
	}

	// The answer is JSON, and as a value it is what json.Marshal writes for the same structure, the white space apart.
	type item struct {
		Version versionAnswer  `json:"version"`
		Doc     jsontext.Value `json:"document"`
	}
	type page struct {
		Items  []item `json:"items"`
		Total  int    `json:"total"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	marshaled, err := json.Marshal(page{
		Items: []item{{versionOf(docs[0].Version), docs[0].Data}, {versionOf(docs[1].Version), docs[1].Data}}, Total: 25, Offset: 20, Limit: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	var fromAnswer, fromMarshal any
	if err := json.Unmarshal(list.Bytes(), &fromAnswer); err != nil {
		t.Fatalf("the answer is not JSON: %v", err)
	}
	if err := json.Unmarshal(marshaled, &fromMarshal); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fromAnswer, fromMarshal) {
		t.Errorf("the answer reads as %v, and json.Marshal's as %v", fromAnswer, fromMarshal)
	}
	if bytes.Contains(marshaled, []byte(sampleDocument)) || !bytes.Contains(list.Bytes(), []byte(sampleDocument)) {
		t.Error("the white space of the document is changed by json.Marshal and kept by writeDocuments: this test does not show the difference")
	}

	// A document that is missing is a fault, not a part of an answer.
	if err := writeDocument(io.Discard, store.Document{}); err == nil {
		t.Error("writeDocument of a document without data = nil, want an error")
	}
}

func TestJSONStream(t *testing.T) {
	var out bytes.Buffer
	j := newJSONStream(&out)
	j.object()
	j.key("a")
	j.array()
	j.value(1)
	j.object()
	j.end()
	j.array()
	j.end()
	j.raw([]byte(" 2 "))
	j.end()
	j.key("b")
	j.value("x")
	j.key("c")
	j.object()
	j.key("d")
	j.raw([]byte("null"))
	j.end()
	j.end()
	if err := j.done(); err != nil {
		t.Fatal(err)
	}
	if want := `{"a":[1,{},[], 2 ],"b":"x","c":{"d":null}}`; out.String() != want {
		t.Errorf("jsonStream wrote %q, want %q", out.String(), want)
	}
	var v any
	if err := json.Unmarshal(out.Bytes(), &v); err != nil {
		t.Errorf("what jsonStream wrote is not JSON: %v", err)
	}

	open := newJSONStream(io.Discard)
	open.object()
	if open.done() == nil {
		t.Error("done() of a stream with an object open = nil, want an error")
	}
	extra := newJSONStream(io.Discard)
	extra.end()
	if extra.done() == nil {
		t.Error("end() of a stream with nothing open did not fail it")
	}
	failing := newJSONStream(failingWriter{})
	failing.object()
	failing.key("a")
	failing.value(1)
	failing.end()
	if !errors.Is(failing.done(), errWrite) {
		t.Errorf("a stream whose writer fails = %v, want its error", failing.done())
	}
}

var errWrite = errors.New("write failed")

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errWrite }

func TestOperationID(t *testing.T) {
	for _, tt := range []struct {
		name   string
		header []string // the lines of the header
		want   string
		ok     bool
	}{
		{"none", nil, "", true},
		{"an operation ID", []string{`"` + someOp + `"`}, someOp, true},
		{"the largest first character of a ULID", []string{`"op_7ZZZZZZZZZZZZZZZZZZZZZZZZZ"`}, "op_7ZZZZZZZZZZZZZZZZZZZZZZZZZ", true},
		{"without quotes", []string{someOp}, "", false},
		{"one quote", []string{`"` + someOp}, "", false},
		{"single quotes", []string{`'` + someOp + `'`}, "", false},
		{"a parameter", []string{`"` + someOp + `";a=1`}, "", false},
		{"an escape", []string{`"op_01JB8T7H4MA2C5E8G1J4K7N3P\6"`}, "", false},
		{"a backslash and a quote", []string{`"` + someOp + `\"`}, "", false},
		{"empty", []string{`""`}, "", false},
		{"a quote only", []string{`"`}, "", false},
		{"an empty header", []string{""}, "", false},
		{"too short", []string{`"op_01JB8T7H4MA2C5E8G1J4K7N3P"`}, "", false},
		{"too long", []string{`"op_01JB8T7H4MA2C5E8G1J4K7N3P6X"`}, "", false},
		{"lower case", []string{`"op_01jb8t7h4ma2c5e8g1j4k7n3p6"`}, "", false},
		{"a ULID that is too large", []string{`"op_8ZZZZZZZZZZZZZZZZZZZZZZZZZ"`}, "", false},
		{"another prefix", []string{`"ent_01JB8T7H4MA2C5E8G1J4K7N3P6"`}, "", false},
		{"two lines", []string{`"` + someOp + `"`, `"` + someOp + `"`}, "", false},
		{"two values in one line", []string{`"` + someOp + `", "` + someOp + `"`}, "", false},
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/test", nil)
		for _, line := range tt.header {
			req.Header.Add("Idempotency-Key", line)
		}
		rec := httptest.NewRecorder()
		got, ok := operationID(rec, req)
		if got != tt.want || ok != tt.ok {
			t.Errorf("%s: operationID = %q, %v, want %q, %v", tt.name, got, ok, tt.want, tt.ok)
		}
		if tt.ok != (rec.Body.Len() == 0) {
			t.Errorf("%s: operationID wrote an answer: %v, want %v", tt.name, rec.Body.Len() != 0, !tt.ok)
		}
		if !tt.ok {
			checkProblem(t, tt.name, rec.Result(), "bad-header")
		}

		rec = httptest.NewRecorder()
		if got := noOperationID(rec, req); got != (len(tt.header) == 0) {
			t.Errorf("%s: noOperationID = %v, want %v", tt.name, got, len(tt.header) == 0)
		}
		if len(tt.header) > 0 {
			checkProblem(t, tt.name+" for a route without operation IDs", rec.Result(), "bad-header")
		}
	}
}

// deadlineRecorder is a recorder of the response that has the deadlines of a connection, which it keeps.
type deadlineRecorder struct {
	*httptest.ResponseRecorder
	mu          sync.Mutex
	read, write []time.Time
}

func newDeadlineRecorder() *deadlineRecorder {
	return &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
}

func (d *deadlineRecorder) SetReadDeadline(t time.Time) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.read = append(d.read, t)
	return nil
}

func (d *deadlineRecorder) SetWriteDeadline(t time.Time) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.write = append(d.write, t)
	return nil
}

// deadlines returns the deadlines that were set so far.
func (d *deadlineRecorder) deadlines() (read, write []time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]time.Time(nil), d.read...), append([]time.Time(nil), d.write...)
}

// checkDeadline checks that got, set between before and after, is d after the time of the setting.
func checkDeadline(t *testing.T, what string, got, before, after time.Time, d time.Duration) {
	t.Helper()
	if got.Before(before.Add(d)) || got.After(after.Add(d)) {
		t.Errorf("%s: the deadline is %v from the time of the call, want %v", what, got.Sub(before).Round(time.Second), d)
	}
}

func TestSpool(t *testing.T) {
	st, dir := testStore(t)
	pattern := func(n, seed int) []byte {
		b := make([]byte, n)
		for i := range b {
			b[i] = byte((i*7 + seed) % 251)
		}
		return b
	}
	readAll := func(s *spool) []byte {
		t.Helper()
		r, err := s.Reader()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}

	// Up to spoolMemory bytes stay in memory, whatever the pieces they come in.
	s := newSpool(st)
	data := pattern(spoolMemory, 1)
	for rest := data; len(rest) > 0; {
		n := min(len(rest), 10000)
		if _, err := s.Write(rest[:n]); err != nil {
			t.Fatal(err)
		}
		rest = rest[n:]
	}
	if s.Len() != spoolMemory || s.spilled() || len(tempFilesIn(t, dir)) != 0 {
		t.Errorf("%d bytes: Len %d, spilled %v, %d temporary files, want %d, false and none", spoolMemory, s.Len(), s.spilled(),
			len(tempFilesIn(t, dir)), spoolMemory)
	}
	for range 2 {
		if !bytes.Equal(readAll(s), data) {
			t.Error("a spool in memory does not read back what was written, from its start each time")
		}
	}

	// The byte after them moves everything to a temporary file, and the writing goes on in it.
	if _, err := s.Write([]byte{9}); err != nil {
		t.Fatal(err)
	}
	data = append(data, 9)
	more := pattern(100000, 2)
	if _, err := s.Write(more); err != nil {
		t.Fatal(err)
	}
	data = append(data, more...)
	if s.Len() != int64(len(data)) || !s.spilled() || len(tempFilesIn(t, dir)) != 1 {
		t.Errorf("%d bytes: Len %d, spilled %v, %d temporary files, want %d, true and 1", len(data), s.Len(), s.spilled(),
			len(tempFilesIn(t, dir)), len(data))
	}
	for range 2 {
		if !bytes.Equal(readAll(s), data) {
			t.Error("a spool in a file does not read back what was written, from its start each time")
		}
	}
	if got, err := s.bytes(); err != nil || !bytes.Equal(got, data) {
		t.Errorf("bytes() of a spool in a file = %d bytes, %v, want what was written", len(got), err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("Close() error = %v", err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("the second Close() error = %v, want nil", err)
	}
	if files := tempFilesIn(t, dir); len(files) != 0 {
		t.Errorf("%d temporary files stay after Close: %v", len(files), files)
	}

	// A write that is more than the memory moves to a file at once; the memory of a spool is returned as it is.
	s = newSpool(st)
	big := pattern(spoolMemory+1, 3)
	if _, err := s.Write(big); err != nil {
		t.Fatal(err)
	}
	if !s.spilled() || !bytes.Equal(readAll(s), big) {
		t.Errorf("a write of %d bytes: spilled %v, want true, and the same bytes", len(big), s.spilled())
	}
	_ = s.Close()
	s = newSpool(st)
	if _, err := s.Write([]byte("small")); err != nil {
		t.Fatal(err)
	}
	if got, err := s.bytes(); err != nil || string(got) != "small" {
		t.Errorf("bytes() of a spool in memory = %q, %v, want small", got, err)
	}
	_ = s.Close()
	if s.Len() != 5 || s.mem.Len() != 0 {
		t.Errorf("Close left the memory of a spool: %d bytes", s.mem.Len())
	}
	if files := tempFilesIn(t, dir); len(files) != 0 {
		t.Errorf("%d temporary files stay: %v", len(files), files)
	}
}

// TestSpoolWithoutTempFiles shows that a spool whose store cannot make a temporary file fails the write that needs one,
// and keeps what it has.
func TestSpoolWithoutTempFiles(t *testing.T) {
	st, dir := testStore(t)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	reader, err := store.OpenReader(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	s := newSpool(reader)
	if _, err := s.Write(bytes.Repeat([]byte("a"), spoolMemory)); err != nil {
		t.Fatalf("Write() of the memory of a spool error = %v", err)
	}
	if n, err := s.Write([]byte("b")); err == nil || n != 0 || s.spilled() || s.Len() != spoolMemory {
		t.Errorf("Write() that needs a file = %d, %v with Len %d and spilled %v, want 0, an error, %d and false", n, err, s.Len(), s.spilled(), spoolMemory)
	}
	if got, err := s.bytes(); err != nil || len(got) != spoolMemory {
		t.Errorf("bytes() = %d bytes, %v, want what it has", len(got), err)
	}
}

// TestProblemHelpers shows that the helpers of the problems of this package write the codes and the details that the
// routes are told to use.
func TestProblemHelpers(t *testing.T) {
	for _, tt := range []struct {
		code  string
		write func(http.ResponseWriter)
	}{
		{"bad-query", writeBadQuery},
		{"bad-header", writeBadHeader},
		{"forbidden", writeForbidden},
		{"too-many-requests", writeTooManyRequests},
	} {
		rec := httptest.NewRecorder()
		tt.write(rec)
		checkProblem(t, tt.code, rec.Result(), tt.code)
	}
}

// TestSmallBodyLimit shows the limit of the bodies that a route reads with readJSON with smallBodyLimit.
func TestSmallBodyLimit(t *testing.T) {
	ts := newTestServer(t, httpsOrigin)
	ts.handle("POST /api/small", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in testIn
		if readJSON(w, r, smallBodyLimit, &in) {
			writeJSON(w, http.StatusOK, reply{OK: true})
		}
	}))
	name := func(n int) string { return `{"name":"` + strings.Repeat("a", n-len(`{"name":""}`)) + `"}` }
	// 64 KiB, as the login body is.
	if res := ts.sendJSON("/api/small", "application/json", name(65536)); res.StatusCode != http.StatusOK {
		t.Errorf("a body of 65536 bytes has the status %d, want 200", res.StatusCode)
	}
	checkProblem(t, "a body of one byte more", ts.sendJSON("/api/small", "application/json", name(65537)), "too-large")
}
