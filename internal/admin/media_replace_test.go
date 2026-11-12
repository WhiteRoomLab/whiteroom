package admin

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// TestMediaReplace replaces the file of a medium: its document takes what the new file is, and keeps the name; a base that is
// not the current version is a conflict; a file of the other kind, or in no known format, is refused.
func TestMediaReplace(t *testing.T) {
	a := newAPIServer(t)
	author, editor := a.people["author"].account.ID, a.people["editor"].account.ID
	first, second, third := testPNG(t, 4, 3), testPNG(t, 7, 5), testPNG(t, 9, 2)
	created := result(t, "POST /api/media", a.as("author").upload("logo.png", "image/png", first), http.StatusCreated)
	doc := a.stored(t, content.RefMedia, created.ID)
	loc := "/api/media/" + created.ID + "/file"

	res := a.as("author").replaceFile(created.ID, doc.Seq, "image/png", second)
	replaced := result(t, "PUT "+loc, res, http.StatusOK)
	if got := res.Header.Get("Location"); got != "" {
		t.Errorf("PUT %s has the Location %q, want none", loc, got)
	}
	if replaced.Kind != content.RefMedia || replaced.ID != created.ID || replaced.Version.Seq <= doc.Seq || replaced.Version.SavedBy != author ||
		replaced.Version.Hash == doc.Hash || len(replaced.Warnings) != 0 {
		t.Errorf("the answer of PUT %s is %+v, want a later version of the medium, saved by the author, with another hash", loc, replaced)
	}
	m := a.storedMedium(t, created.ID)
	if m.Hash != hashOf(second) || m.Size != len(second) || m.Width != 7 || m.Height != 5 || m.MIMEType != "image/png" ||
		m.FileName != "logo.png" || m.UploadedBy != author || m.ID != created.ID {
		t.Errorf("the medium is %+v, want the file of 7 by 5 pixels, under the name it had", m)
	}
	if got := a.stored(t, content.RefMedia, created.ID); got.Seq != replaced.Version.Seq || got.Hash != replaced.Version.Hash {
		t.Errorf("the store has the version %+v, the answer says %+v", got.Version, replaced.Version)
	}
	if got := a.original(t, m.Hash); !bytes.Equal(got, second) {
		t.Errorf("the original has %d bytes, want the %d bytes that were uploaded", len(got), len(second))
	}
	// The original that it replaces stays: the releases that use it need it.
	if got := a.original(t, hashOf(first)); !bytes.Equal(got, first) {
		t.Errorf("the first original has %d bytes, want %d", len(got), len(first))
	}
	records := a.records(t, audit.MediaReplace)
	if len(records) != 1 || records[0].Actor != author || records[0].Target != (audit.Target{Kind: "media", ID: created.ID}) ||
		records[0].Detail["hash"] != m.Hash {
		t.Errorf("the records of media.replace are %+v, want one by the author for the medium", records)
	}

	// An editor replaces the file of a medium of another account, and is the account that uploaded it then.
	now := a.stored(t, content.RefMedia, created.ID)
	result(t, "PUT as the editor", a.as("editor").replaceFile(created.ID, now.Seq, "", third), http.StatusOK)
	if m := a.storedMedium(t, created.ID); m.UploadedBy != editor || m.Hash != hashOf(third) || m.Width != 9 || m.Height != 2 {
		t.Errorf("the medium is %+v, want the file of the editor of 9 by 2 pixels", m)
	}

	// What the upload says is only told of.
	now = a.stored(t, content.RefMedia, created.ID)
	warned := result(t, "PUT with another type", a.as("editor").replaceFile(created.ID, now.Seq, "image/gif", second), http.StatusOK)
	if len(warned.Warnings) != 1 || warned.Warnings[0].Pointer != "/mimeType" || warned.Warnings[0].File != "media/"+created.ID+".json" ||
		warned.Warnings[0].Detail != "the file is image/png, not image/gif as uploaded" {
		t.Errorf("the warnings are %+v, want one at /mimeType", warned.Warnings)
	}

	// A base that is not the version of the medium is a conflict, and the medium stays as it is.
	now = a.stored(t, content.RefMedia, created.ID)
	checkProblem(t, "a file with an old base", a.as("editor").replaceFile(created.ID, doc.Seq, "", first), "conflict")
	checkProblem(t, "a file with a base that is not yet", a.as("editor").replaceFile(created.ID, now.Seq+1, "", first), "conflict")
	if got := a.stored(t, content.RefMedia, created.ID); got.Seq != now.Seq {
		t.Errorf("a file that was refused has changed the medium to the version %d from %d", got.Seq, now.Seq)
	}

	// A file in no known format is refused before its original is written; one of the other kind, after.
	originals := a.originals(t)
	e := firstProblem(t, "text", a.as("editor").replaceFile(created.ID, now.Seq, "", []byte("not an image")))
	if e.File != "" || e.Pointer != "" || e.Detail != noFormat {
		t.Errorf("the error of text is %+v, want %s without a file", e, noFormat)
	}
	if got := a.originals(t); !slices.Equal(got, originals) {
		t.Errorf("the file that was refused has written an original: %v, want %v", got, originals)
	}
	pdf, err := os.ReadFile(filepath.FromSlash(pdfOfSite))
	if err != nil {
		t.Fatal(err)
	}
	e = firstProblem(t, "a PDF for an image", a.as("editor").replaceFile(created.ID, now.Seq, "application/pdf", pdf))
	if e.File != "media/"+created.ID+".json" || e.Pointer != "/mimeType" || e.Detail != "the file is application/pdf; a medium cannot change between an image and a PDF" {
		t.Errorf("the error of a PDF for an image is %+v", e)
	}
	if got := a.stored(t, content.RefMedia, created.ID); got.Seq != now.Seq {
		t.Errorf("a file that was refused has changed the medium to the version %d from %d", got.Seq, now.Seq)
	}

	// The query: the version is required, once, and the only name there is.
	base := strconv.FormatInt(now.Seq, 10)
	for _, tt := range []struct{ name, query string }{
		{"no query", ""},
		{"no base", "?other=1"},
		{"an empty base", "?base="},
		{"a base that is not a number", "?base=x"},
		{"a negative base", "?base=-1"},
		{"the base twice", "?base=" + base + "&base=" + base},
		{"another name too", "?base=" + base + "&fileName=a.png"},
	} {
		checkProblem(t, tt.name, a.as("editor").send(http.MethodPut, "/api/media/"+created.ID+"/file"+tt.query, string(first), "Content-Type", "image/png"), "bad-query")
	}
	// The ID in the path.
	checkProblem(t, "an ID that is none", a.as("editor").send(http.MethodPut, "/api/media/x/file?base=1", string(first), "Content-Type", "image/png"), "not-found")
	checkProblem(t, "an ID of another kind", a.as("editor").send(http.MethodPut, "/api/media/"+newsTerm+"/file?base=1", string(first), "Content-Type", "image/png"), "not-found")
	checkProblem(t, "a medium that there is not", a.as("editor").replaceFile(missingMed, 1, "image/png", first), "not-found")
	if got := a.stored(t, content.RefMedia, created.ID); got.Seq != now.Seq {
		t.Errorf("the requests that were refused have changed the medium to the version %d from %d", got.Seq, now.Seq)
	}
	if logged := a.logs.String(); logged != "" {
		t.Errorf("the log is %q, want nothing", logged)
	}
	if files := a.tempFiles(t); len(files) != 0 {
		t.Errorf("%d temporary files stay: %v", len(files), files)
	}
}

// TestMediaRetry shows that the header Idempotency-Key makes an upload happen once: a retry is answered as the first request
// was, also if the medium has changed since, and a key that is used for another file is refused.
func TestMediaRetry(t *testing.T) {
	a := newAPIServer(t)
	as := a.as("editor")
	editor := a.people["editor"].account.ID
	file := testPNG(t, 4, 3)
	count := func(action audit.Action) int { return len(a.records(t, action)) }

	first := as.upload("retry.png", "image/png", file, key(1)...)
	location := first.Header.Get("Location")
	created := result(t, "the upload", first, http.StatusCreated)
	versions := a.versions(t)
	retry := as.upload("retry.png", "image/png", file, key(1)...)
	if retry.Header.Get("Location") != location {
		t.Errorf("the retry has the Location %q, want %q", retry.Header.Get("Location"), location)
	}
	if again := result(t, "the retry of the upload", retry, http.StatusCreated); again.ID != created.ID || again.Version != created.Version {
		t.Errorf("the retry answers %+v, want %+v", again, created)
	}
	if got := a.versions(t); got != versions || count(audit.MediaAdd) != 1 {
		t.Errorf("the retry has changed the store, or recorded again: %d records of media.add", count(audit.MediaAdd))
	}
	if r := a.records(t, audit.MediaAdd)[0]; r.Operation != "op_01JD0000000000000000000001" || r.Actor != editor {
		t.Errorf("the record is %+v, want the operation ID of the key and the editor", r)
	}

	// The key is for this request only: another file, another name, another type, another route, another account.
	for _, tt := range []struct {
		name string
		res  *http.Response
	}{
		{"another file", as.upload("retry.png", "image/png", testPNG(t, 5, 5), key(1)...)},
		{"another name", as.upload("other.png", "image/png", file, key(1)...)},
		{"another type", as.upload("retry.png", "image/jpeg", file, key(1)...)},
		{"another account", a.as("admin").upload("retry.png", "image/png", file, key(1)...)},
		{"a replacement", as.replaceFile(created.ID, created.Version.Seq, "image/png", file, key(1)...)},
	} {
		checkProblem(t, "the key of the upload with "+tt.name, tt.res, "operation-reused")
	}

	// A replacement: the retry returns what the first did, not a conflict, even though the medium has changed since.
	other, last := testPNG(t, 6, 6), testPNG(t, 8, 8)
	replaced := result(t, "the replacement", as.replaceFile(created.ID, created.Version.Seq, "image/png", other, key(2)...), http.StatusOK)
	now := a.stored(t, content.RefMedia, created.ID)
	result(t, "another replacement", as.replaceFile(created.ID, now.Seq, "image/png", last), http.StatusOK)
	versions = a.versions(t)
	if again := result(t, "the retry of the replacement", as.replaceFile(created.ID, created.Version.Seq, "image/png", other, key(2)...), http.StatusOK); again.Version != replaced.Version {
		t.Errorf("the retry of the replacement answers the version %+v, want %+v", again.Version, replaced.Version)
	}
	if got := a.versions(t); got != versions {
		t.Errorf("the retry of the replacement has changed the store:\n%s\nwant\n%s", got, versions)
	}
	if got := count(audit.MediaReplace); got != 2 {
		t.Errorf("%d records of media.replace, want 2: the replacement with the key once, and the other", got)
	}
	checkProblem(t, "a replacement without the key at the old version", as.replaceFile(created.ID, created.Version.Seq, "image/png", other), "conflict")
	if logged := a.logs.String(); logged != "" {
		t.Errorf("the log is %q, want nothing", logged)
	}
}

// TestMediaRoutes shows which routes the files have, and their deadlines: the time to receive a file and to save it, and to send
// one, and not the 30 seconds of the other routes.
func TestMediaRoutes(t *testing.T) {
	a := newAPIServer(t)
	if uploadDeadline != 10*time.Minute+30*time.Second || slowTransfer != 10*time.Minute {
		t.Errorf("the deadline of an upload is %v and of a download %v, want 10 minutes 30 seconds and 10 minutes", uploadDeadline, slowTransfer)
	}
	item := "/api/media/" + sampleLogo
	for _, r := range []struct {
		method, path string
		want         time.Duration
	}{
		{http.MethodPost, "/api/media", uploadDeadline},
		{http.MethodPut, item + "/file", uploadDeadline},
		{http.MethodGet, item + "/file", slowTransfer},
		{http.MethodHead, item + "/file", slowTransfer},
		// The routes of the documents of the media keep theirs.
		{http.MethodGet, "/api/media", heavyDeadline},
		{http.MethodGet, item, heavyDeadline},
		{http.MethodPut, item, heavyDeadline},
	} {
		_, pattern := a.mux.Handler(httptest.NewRequest(r.method, r.path, nil))
		if pattern == "" {
			t.Errorf("%s %s: no route", r.method, r.path)
		} else if got := a.deadlineOf(pattern); got != r.want {
			t.Errorf("%s %s (%s) has the deadline %v, want %v", r.method, r.path, pattern, got, r.want)
		}
	}
	for _, r := range []struct{ method, path string }{
		{http.MethodDelete, item + "/file"}, {http.MethodPost, item + "/file"}, {http.MethodPut, "/api/media"}, {http.MethodGet, item + "/files"},
	} {
		res := a.as("admin").send(r.method, r.path, "")
		want := "method-not-allowed"
		if r.path == item+"/files" {
			want = "not-found"
		}
		checkProblem(t, r.method+" "+r.path, res, want)
	}
}

// TestMediaDeadlines shows the time limits that the routes give the connection: the body of an upload ten minutes to be
// received, and a minute to write the answer, which comes after them; a download ten minutes to be sent; and a refusal, which
// comes at once, none or a minute.
func TestMediaDeadlines(t *testing.T) {
	a := newAPIServer(t)
	file := testPNG(t, 4, 3)
	do := func(req *http.Request) (rec *deadlineRecorder, before, after time.Time) {
		rec = newDeadlineRecorder()
		before = time.Now()
		a.ServeHTTP(rec, req)
		return rec, before, time.Now()
	}
	post := func(login string, body []byte, contentType string) *http.Request {
		return a.bodyRequest(login, http.MethodPost, "/api/media?fileName=a.png", bytes.NewReader(body), "Content-Type", contentType)
	}
	expect := func(what string, rec *deadlineRecorder, before, after time.Time, status int, read, write time.Duration) {
		t.Helper()
		if rec.Code != status {
			t.Errorf("%s has the status %d, want %d: %s", what, rec.Code, status, rec.Body.String())
		}
		reads, writes := rec.deadlines()
		for _, c := range []struct {
			name string
			got  []time.Time
			want time.Duration
		}{{"read", reads, read}, {"write", writes, write}} {
			switch {
			case c.want == 0 && len(c.got) != 0:
				t.Errorf("%s set %d %s deadlines, want none", what, len(c.got), c.name)
			case c.want != 0 && len(c.got) != 1:
				t.Errorf("%s set %d %s deadlines, want 1", what, len(c.got), c.name)
			case c.want != 0:
				checkDeadline(t, what+": the "+c.name+" deadline", c.got[0], before, after, c.want)
			}
		}
	}

	rec, before, after := do(post("editor", file, "image/png"))
	expect("an upload", rec, before, after, http.StatusCreated, slowTransfer, writeTimeout)
	created := result(t, "the upload", rec.Result(), http.StatusCreated)
	rec, before, after = do(post("editor", []byte("text"), "text/plain"))
	expect("an upload in no known format", rec, before, after, http.StatusUnprocessableEntity, slowTransfer, writeTimeout)
	rec, before, after = do(a.bodyRequest("editor", http.MethodPost, "/api/media?fileName=a.png", bigFile(file, content.MaxMediaBytes+1), "Content-Type", "image/png"))
	expect("an upload of more than a file may have", rec, before, after, http.StatusRequestEntityTooLarge, slowTransfer, writeTimeout)
	declared := a.bodyRequest("editor", http.MethodPost, "/api/media?fileName=a.png", &unread{}, "Content-Type", "image/png")
	declared.ContentLength = content.MaxMediaBytes + 1
	rec, before, after = do(declared)
	expect("an upload that says it is too large", rec, before, after, http.StatusRequestEntityTooLarge, 0, 0)
	rec, before, after = do(post("viewer", file, "image/png"))
	expect("an upload of a viewer", rec, before, after, http.StatusForbidden, 0, 0)

	doc := a.stored(t, content.RefMedia, created.ID)
	put := a.bodyRequest("editor", http.MethodPut, "/api/media/"+created.ID+"/file?base="+strconv.FormatInt(doc.Seq, 10),
		bytes.NewReader(testPNG(t, 5, 5)), "Content-Type", "image/png")
	rec, before, after = do(put)
	expect("a replacement", rec, before, after, http.StatusOK, slowTransfer, writeTimeout)

	get := func(login, id string) *http.Request {
		return a.request(http.MethodGet, "/api/media/"+id+"/file", "", append(a.cookie(a.people[login].token), "Origin", a.origin)...)
	}
	rec, before, after = do(get("viewer", sampleLogo))
	expect("a download", rec, before, after, http.StatusOK, 0, slowTransfer)
	rec, before, after = do(get("viewer", missingMed))
	expect("a download of a medium that there is not", rec, before, after, http.StatusNotFound, 0, writeTimeout)
}

// noDeadlines is a recorder of the response whose connection takes no time limits: it is not that httptest has none, which the
// routes go on without, but that setting one fails.
type noDeadlines struct{ *httptest.ResponseRecorder }

func (noDeadlines) SetReadDeadline(time.Time) error  { return errors.New("no read deadline") }
func (noDeadlines) SetWriteDeadline(time.Time) error { return errors.New("no write deadline") }

// TestMediaDeadlineFails shows that a route whose time limits cannot be set answers 500 and does what it was asked not to: it
// reads no body, saves nothing and sends no file.
func TestMediaDeadlineFails(t *testing.T) {
	a := newAPIServer(t)
	before, originals := a.versions(t), a.originals(t)

	body := &unread{}
	rec := noDeadlines{httptest.NewRecorder()}
	a.ServeHTTP(rec, a.bodyRequest("editor", http.MethodPost, "/api/media?fileName=a.png", body, "Content-Type", "image/png"))
	checkProblem(t, "an upload whose read deadline cannot be set", rec.Result(), "internal")
	if body.touched.Load() {
		t.Error("the body was read though its time limit could not be set")
	}
	if got := a.versions(t); got != before {
		t.Errorf("the upload has changed the store:\n%s\nwant\n%s", got, before)
	}
	if got := a.originals(t); !slices.Equal(got, originals) {
		t.Errorf("the upload has written originals: %v, want %v", got, originals)
	}
	if logged := a.logs.String(); !strings.Contains(logged, `POST "/api/media?fileName=a.png": no read deadline`) {
		t.Errorf("the log is %q, want the error of the deadline", logged)
	}
	a.logs.Reset()

	rec = noDeadlines{httptest.NewRecorder()}
	a.ServeHTTP(rec, a.request(http.MethodGet, "/api/media/"+sampleLogo+"/file", "", append(a.cookie(a.people["viewer"].token), "Origin", a.origin)...))
	checkProblem(t, "a download whose write deadline cannot be set", rec.Result(), "internal")
	if got := rec.Header().Values("Content-Disposition"); len(got) != 0 {
		t.Errorf("the answer has the Content-Disposition %q, want none", got)
	}
	if logged := a.logs.String(); !strings.Contains(logged, "no write deadline") {
		t.Errorf("the log is %q, want the error of the deadline", logged)
	}
}
