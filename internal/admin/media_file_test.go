package admin

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// checkFile checks that res, the response to what, is the download of file: 200 with all of it and its length, the type of
// its medium, and a name to save it as, and not an answer that can be shown or taken in parts; a HEAD has the same headers.
func checkFile(t *testing.T, what string, res *http.Response, head bool, file []byte, mimeType, disposition string) {
	t.Helper()
	got := bodyOf(t, res)
	if res.StatusCode != http.StatusOK || head && got != "" || !head && got != string(file) {
		t.Errorf("%s: %d with %d bytes, want 200 with the %d bytes of the file", what, res.StatusCode, len(got), len(file))
	}
	for name, want := range map[string]string{
		"Content-Type": mimeType, "Content-Length": strconv.Itoa(len(file)), "Content-Disposition": disposition,
	} {
		if got := res.Header.Values(name); !equalValues(got, want) {
			t.Errorf("%s has the %s %q, want %q", what, name, got, want)
		}
	}
	for _, name := range []string{"Accept-Ranges", "Content-Range", "Etag", "Last-Modified"} {
		if got := res.Header.Values(name); len(got) != 0 {
			t.Errorf("%s has the %s %q, want none", what, name, got)
		}
	}
	checkHeaders(t, what, res, true)
}

// TestMediaFile downloads the originals of the media: all of the file, to be saved, with the name and the type of the medium,
// for every role that reads, and with nothing that lets a browser show it, take it in parts or keep it.
func TestMediaFile(t *testing.T) {
	a := newAPIServer(t)
	pdf, err := os.ReadFile(filepath.FromSlash(pdfOfSite))
	if err != nil {
		t.Fatal(err)
	}
	logo, err := os.ReadFile(filepath.FromSlash(logoOfSite))
	if err != nil {
		t.Fatal(err)
	}
	spaced := result(t, "an upload with a space in its name", a.as("editor").upload("my photo.png", "image/png", testPNG(t, 4, 3)), http.StatusCreated)

	for _, tt := range []struct {
		name, id    string
		file        []byte
		mimeType    string
		disposition string
	}{
		{"a PDF with a name that is not ASCII", samplePDF, pdf, "application/pdf", "attachment; filename*=utf-8''%E4%BC%9A%E7%A4%BE%E6%A1%88%E5%86%85.pdf"},
		{"an image", sampleLogo, logo, "image/png", "attachment; filename=logo.png"},
		{"a name with a space", spaced.ID, testPNG(t, 4, 3), "image/png", `attachment; filename="my photo.png"`},
	} {
		path := "/api/media/" + tt.id + "/file"
		for _, login := range []string{"viewer", "author", "editor", "admin"} {
			checkFile(t, "GET "+path+" as "+login, a.as(login).get(path), false, tt.file, tt.mimeType, tt.disposition)
		}
		// A request for a part of it is answered with all of it.
		checkFile(t, tt.name+" with a Range", a.as("viewer").send(http.MethodGet, path, "", "Range", "bytes=0-9"), false, tt.file, tt.mimeType, tt.disposition)
		checkFile(t, tt.name+" with an If-Range and a Range", a.as("viewer").send(http.MethodGet, path, "", "Range", "bytes=5-", "If-Range", `"x"`),
			false, tt.file, tt.mimeType, tt.disposition)
		// HEAD tells the same, and sends no body.
		checkFile(t, "HEAD "+path, a.as("viewer").send(http.MethodHead, path, ""), true, tt.file, tt.mimeType, tt.disposition)
	}

	// The file of a medium after its file is replaced is the new one.
	doc := a.stored(t, content.RefMedia, spaced.ID)
	next := testPNG(t, 9, 9)
	result(t, "a replacement", a.as("editor").replaceFile(spaced.ID, doc.Seq, "image/png", next), http.StatusOK)
	checkFile(t, "GET after a replacement", a.as("viewer").get("/api/media/"+spaced.ID+"/file"), false, next, "image/png", `attachment; filename="my photo.png"`)

	// A name that is not ASCII, and one with a quote.
	named := result(t, "an upload with a name that is not ASCII", a.as("editor").upload("写真 \"1\".png", "image/png", testPNG(t, 5, 5)), http.StatusCreated)
	res := a.as("viewer").get("/api/media/" + named.ID + "/file")
	if got, want := res.Header.Get("Content-Disposition"), "attachment; filename*=utf-8''%E5%86%99%E7%9C%9F%20%221%22.png"; got != want {
		t.Errorf("the Content-Disposition of a name that is not ASCII is %q, want %q", got, want)
	}
	_ = bodyOf(t, res)

	// What cannot be answered.
	before := a.versions(t)
	for _, tt := range []struct{ name, method, path, code string }{
		{"a medium that there is not", http.MethodGet, "/api/media/" + missingMed + "/file", "not-found"},
		{"an ID that is none", http.MethodGet, "/api/media/x/file", "not-found"},
		{"an ID of another kind", http.MethodGet, "/api/media/" + newsTerm + "/file", "not-found"},
		{"a query", http.MethodGet, "/api/media/" + sampleLogo + "/file?download=1", "bad-query"},
		{"a method that it does not take", http.MethodPost, "/api/media/" + sampleLogo + "/file", "method-not-allowed"},
	} {
		checkProblem(t, tt.name, a.as("viewer").send(tt.method, tt.path, ""), tt.code)
	}
	checkProblem(t, "an operation ID", a.as("viewer").send(http.MethodGet, "/api/media/"+sampleLogo+"/file", "", key(1)...), "bad-header")
	if res := a.send(http.MethodGet, "/api/media/"+sampleLogo+"/file", ""); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("a download without a session has the status %d, want 401", res.StatusCode)
	}
	if got := a.versions(t); got != before {
		t.Errorf("the downloads have changed the store:\n%s\nwant\n%s", got, before)
	}
	if logged := a.logs.String(); logged != "" {
		t.Errorf("the log is %q, want nothing", logged)
	}
	if files := a.tempFiles(t); len(files) != 0 {
		t.Errorf("%d temporary files stay: %v", len(files), files)
	}
}

// aborted calls ServeHTTP with req, and returns the recorder with what the server panicked with: a response that cannot be
// completed once it has begun is aborted with http.ErrAbortHandler, which net/http turns into a closed connection.
func (a *apiServer) aborted(req *http.Request) (rec *httptest.ResponseRecorder, panicked any) {
	rec = httptest.NewRecorder()
	defer func() { panicked = recover() }()
	a.ServeHTTP(rec, req)
	return rec, nil
}

// TestMediaFileFaults shows what a download does when the store does not have the original that its document says: before
// the answer begins it is a fault of the server (500), and it is logged; once the data is found to be wrong while it is sent,
// the connection is aborted, so that what was sent is not taken for the whole file.
func TestMediaFileFaults(t *testing.T) {
	a := newAPIServer(t)
	pdf, err := os.ReadFile(filepath.FromSlash(pdfOfSite))
	if err != nil {
		t.Fatal(err)
	}
	logoDoc := a.storedMedium(t, sampleLogo)
	pdfDoc := a.storedMedium(t, samplePDF)
	get := func(id string) *http.Request {
		return a.request(http.MethodGet, "/api/media/"+id+"/file", "", append(a.cookie(a.people["viewer"].token), "Origin", a.origin)...)
	}
	expectLog := func(what, want string) {
		t.Helper()
		if logged := a.logs.String(); !strings.Contains(logged, want) {
			t.Errorf("%s: the log is %q, want it to have %q", what, logged, want)
		}
		a.logs.Reset()
	}

	// The data of a size that the document does not say: refused before it is sent.
	path := a.originalPath(pdfDoc.Hash)
	if err := os.WriteFile(path, append(bytes.Clone(pdf), 'x'), 0o600); err != nil {
		t.Fatal(err)
	}
	rec, panicked := a.aborted(get(samplePDF))
	if panicked != nil {
		t.Errorf("a download of data of the wrong size panicked with %v, want an answer", panicked)
	}
	checkProblem(t, "an original that has more bytes than its document says", rec.Result(), "internal")
	expectLog("more bytes", "the original of "+samplePDF+": it has 677 bytes and the document says 676")
	if err := os.WriteFile(path, pdf[:100], 0o600); err != nil {
		t.Fatal(err)
	}
	rec, _ = a.aborted(get(samplePDF))
	checkProblem(t, "an original that has fewer bytes than its document says", rec.Result(), "internal")
	expectLog("fewer bytes", "it has 100 bytes and the document says 676")

	// No file: the medium is there, so it is not a missing resource.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	rec, _ = a.aborted(get(samplePDF))
	checkProblem(t, "an original that is missing", rec.Result(), "internal")
	expectLog("a missing original", "the original of "+samplePDF+": ")

	// Data of the right size and another content is found out at its end: the answer has begun, and the connection is aborted.
	damaged := bytes.Clone(pdf)
	damaged[len(damaged)/2] ^= 0xff
	if err := os.WriteFile(path, damaged, 0o600); err != nil {
		t.Fatal(err)
	}
	rec, panicked = a.aborted(get(samplePDF))
	if err, ok := panicked.(error); !ok || !errors.Is(err, http.ErrAbortHandler) {
		t.Errorf("a download of data that is not the original panicked with %v, want http.ErrAbortHandler", panicked)
	}
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Length") != strconv.Itoa(len(pdf)) {
		t.Errorf("the answer began with %d and the Content-Length %q, want 200 and %d", rec.Code, rec.Header().Get("Content-Length"), len(pdf))
	}
	expectLog("damaged data", "cannot read the original of "+samplePDF+": original "+pdfDoc.Hash+": the data has the hash sha256:")

	// The others are as they were, and the faults are not recorded as anything else.
	if res := a.as("viewer").get("/api/media/" + sampleLogo + "/file"); res.StatusCode != http.StatusOK || res.Header.Get("Content-Length") != strconv.Itoa(logoDoc.Size) {
		t.Errorf("the download of another medium has the status %d", res.StatusCode)
	}
}
