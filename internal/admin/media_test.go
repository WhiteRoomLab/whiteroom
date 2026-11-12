package admin

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/edit"
)

// logoOfSite is the original file of the image sampleLogo, and noFormat the error of a file in no known format.
const (
	logoOfSite = "../build/testdata/site/files/026f7e3a079bd272dacbfad448ed2064991ec5c992056ddff67fccd5388fb85d"
	noFormat   = "cannot read the file: unsupported file format"
)

var regexpMediaFile = regexp.MustCompile(`^media/med_[0-9A-Z]{26}\.json$`)

// testPNG returns a PNG of w by h pixels of noise: a size makes one file, and another size another.
func testPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	rng := rand.New(rand.NewPCG(uint64(w), uint64(h)))
	for i := range img.Pix {
		img.Pix[i] = byte(rng.UintN(256))
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// testJPEG returns a JPEG of 8 by 8 pixels with a comment of comment bytes before its tables, so that more than 512 bytes
// come before the frame that tells its size.
func testJPEG(t *testing.T, comment int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, image.NewNRGBA(image.Rect(0, 0, 8, 8)), nil); err != nil {
		t.Fatal(err)
	}
	data := b.Bytes()
	n := comment + 2 // with the two bytes of the length
	seg := slices.Concat([]byte{0xff, 0xfe, byte(n >> 8), byte(n)}, bytes.Repeat([]byte("c"), comment))
	return slices.Concat(data[:2], seg, data[2:])
}

// hashOf returns the hash of an original as the documents of the media write it.
func hashOf(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// upload sends POST /api/media of file as it is, under the name fileName, from the account. The type that the upload says
// the file has is contentType, or none if it is "".
func (p asPerson) upload(fileName, contentType string, file []byte, headers ...string) *http.Response {
	return p.send(http.MethodPost, "/api/media?fileName="+url.QueryEscape(fileName), string(file), withType(contentType, headers)...)
}

// replaceFile sends PUT /api/media/{id}/file of file as it is, for the medium at the version base.
func (p asPerson) replaceFile(id string, base int64, contentType string, file []byte, headers ...string) *http.Response {
	return p.send(http.MethodPut, "/api/media/"+id+"/file?base="+strconv.FormatInt(base, 10), string(file), withType(contentType, headers)...)
}

// withType returns the headers with the Content-Type of a file: an empty one for none, which a body of JSON is not given.
func withType(contentType string, headers []string) []string {
	return append([]string{"Content-Type", contentType}, headers...)
}

// storedMedium returns the medium id as the store has it.
func (a *apiServer) storedMedium(t *testing.T, id string) content.Media {
	t.Helper()
	var m content.Media
	if err := m.UnmarshalJSON(a.stored(t, content.RefMedia, id).Data); err != nil {
		t.Fatal(err)
	}
	return m
}

// original returns the original with the hash, as the store has it.
func (a *apiServer) original(t *testing.T, hash string) []byte {
	t.Helper()
	f, err := a.db.OpenOriginal(hash)
	if err != nil {
		t.Fatalf("OpenOriginal(%q) error = %v", hash, err)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("reading the original %q: %v", hash, err)
	}
	return data
}

// originals returns the hashes of the originals in the data directory, in order.
func (a *apiServer) originals(t *testing.T) []string {
	t.Helper()
	var out []string
	dirs, err := os.ReadDir(filepath.Join(a.dir, "files"))
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(a.dir, "files", d.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			out = append(out, "sha256:"+f.Name())
		}
	}
	slices.Sort(out)
	return out
}

// originalPath returns where the data directory keeps the original with the hash.
func (a *apiServer) originalPath(hash string) string {
	digits := strings.TrimPrefix(hash, "sha256:")
	return filepath.Join(a.dir, "files", digits[:2], digits)
}

// bodyRequest returns a request to path from the account login, with a body that is read from body and has no known length, as
// a request of a client that sends it in chunks.
func (a *apiServer) bodyRequest(login, method, path string, body io.Reader, headers ...string) *http.Request {
	all := append(a.cookie(a.people[login].token), "Origin", a.origin)
	req := a.request(method, path, "", append(all, headers...)...)
	rc, ok := body.(io.ReadCloser)
	if !ok {
		rc = io.NopCloser(body)
	}
	req.Body, req.ContentLength = rc, -1
	return req
}

// respond sends req to the server, as ServeHTTP takes it, and returns the response. It closes the body as a server does when
// the handler is done, which ends the writes to a pipe that nobody reads any more.
func (a *apiServer) respond(req *http.Request) *http.Response {
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, req)
	_ = req.Body.Close()
	return rec.Result()
}

// unread is a body that tells whether it was read: a request that is refused before its body is received must not read it.
type unread struct{ touched atomic.Bool }

func (u *unread) Read([]byte) (int, error) {
	u.touched.Store(true)
	return 0, errors.New("the body was read")
}

// zeros is the rest of a big file that need not be in memory.
type zeros struct{}

func (zeros) Read(p []byte) (int, error) { clear(p); return len(p), nil }

// bigFile returns a reader of the start and of zeros, n bytes in all.
func bigFile(start []byte, n int64) io.Reader {
	return io.MultiReader(bytes.NewReader(start), io.LimitReader(zeros{}, n-int64(len(start))))
}

// bigPDF returns n bytes that start as a PDF file, which the store takes as one: Probe reads no more of it.
func bigPDF(n int) []byte {
	return slices.Concat([]byte("%PDF-1.7\n"), make([]byte, n-len("%PDF-1.7\n")))
}

// addFile adds a medium of the file, as the account login uploads it, and returns the result.
func (a *apiServer) addFile(t *testing.T, login, fileName, declared string, file []byte) edit.Result {
	t.Helper()
	op := edit.Op{By: a.people[login].account.ID}
	res, err := a.pub.Editor().AddMedia(t.Context(), op, bytes.NewReader(file), fileName, declared)
	if err != nil {
		t.Fatalf("AddMedia(%q) error = %v", fileName, err)
	}
	return res
}

// TestMediaUpload uploads files and checks what the medium is: its document, its original, the answer, and the record.
func TestMediaUpload(t *testing.T) {
	a := newAPIServer(t)
	author := a.people["author"].account.ID
	file := testPNG(t, 4, 3)

	res := a.as("author").upload("small.png", "image/png", file)
	location := res.Header.Get("Location")
	created := result(t, "POST /api/media", res, http.StatusCreated)
	if err := content.CheckID(content.RefMedia, created.ID); err != nil || created.Kind != content.RefMedia ||
		created.Version.Seq == 0 || created.Version.SavedBy != author || created.Version.Hash == "" || len(created.Warnings) != 0 {
		t.Errorf("the answer of POST /api/media is %+v, want the new medium, saved by the author, with no warning", created)
	}
	if want := "/api/media/" + created.ID; location != want {
		t.Errorf("POST /api/media has the Location %q, want %q", location, want)
	}
	if got := res.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("POST /api/media has the Content-Type %q, want application/json", got)
	}

	// The document says what the file is, from its contents, and that the author uploaded it, at the time of the operation.
	m := a.storedMedium(t, created.ID)
	if uploaded, err := time.Parse(time.RFC3339, m.UploadedAt); err != nil || !uploaded.Equal(a.clock.now()) {
		t.Errorf("uploadedAt is %q (%v), want the time of the operation %v", m.UploadedAt, err, a.clock.now())
	}
	want := content.Media{
		ID: created.ID, Hash: hashOf(file), FileName: "small.png", MIMEType: "image/png", Size: len(file), Width: 4, Height: 3,
		UploadedAt: m.UploadedAt, UploadedBy: author,
	}
	if !reflect.DeepEqual(m, want) {
		t.Errorf("the medium is %+v, want %+v", m, want)
	}
	if got := a.original(t, m.Hash); !bytes.Equal(got, file) {
		t.Errorf("the original has %d bytes, want the %d bytes that were uploaded", len(got), len(file))
	}
	if doc := a.stored(t, content.RefMedia, created.ID); doc.Seq != created.Version.Seq || doc.Hash != created.Version.Hash {
		t.Errorf("the store has the version %+v, the answer says %+v", doc.Version, created.Version)
	}

	// The record of the activity log is the operation's, by the account, with what the file is.
	records := a.records(t, audit.MediaAdd)
	if len(records) != 1 || records[0].Actor != author || records[0].Target != (audit.Target{Kind: "media", ID: created.ID}) {
		t.Fatalf("the records of media.add are %+v, want one by the author for the medium", records)
	}
	for key, want := range map[string]string{
		"hash": m.Hash, "size": strconv.Itoa(len(file)), "mimeType": "image/png", "declaredType": "image/png", "fileName": "small.png",
	} {
		if got := records[0].Detail[key]; got != want {
			t.Errorf("the record of media.add has %s %q, want %q", key, got, want)
		}
	}

	// The name is not what the file is, nor is the type that the upload says: the contents decide.
	named := result(t, "a PNG named .txt", a.as("editor").upload("notes.txt", "text/plain", testPNG(t, 6, 6)), http.StatusCreated)
	if m := a.storedMedium(t, named.ID); m.MIMEType != "image/png" || m.FileName != "notes.txt" || m.Width != 6 {
		t.Errorf("a PNG named notes.txt is %+v, want an image/png with that name", m)
	}
	// A file of which the first 512 bytes are not enough to read its size is saved, since only a format that is not known is
	// refused before the original is written.
	jpg := testJPEG(t, 1000)
	if len(jpg) < 1000 || !bytes.Equal(jpg[:2], []byte{0xff, 0xd8}) {
		t.Fatalf("the JPEG has %d bytes and starts %x", len(jpg), jpg[:2])
	}
	made := result(t, "a JPEG with a long comment", a.as("editor").upload("c.jpg", "image/jpeg", jpg), http.StatusCreated)
	if m := a.storedMedium(t, made.ID); m.MIMEType != "image/jpeg" || m.Width != 8 || m.Height != 8 || m.Size != len(jpg) {
		t.Errorf("a JPEG with a long comment is %+v, want an image/jpeg of 8 by 8 pixels", m)
	}
	// And a PDF, which has no size in pixels.
	pdf := bigPDF(2000)
	doc := result(t, "a PDF", a.as("admin").upload("a.pdf", "application/pdf", pdf), http.StatusCreated)
	if m := a.storedMedium(t, doc.ID); m.MIMEType != "application/pdf" || m.Width != 0 || m.Height != 0 || m.Size != len(pdf) {
		t.Errorf("a PDF is %+v, want an application/pdf without a size in pixels", m)
	}

	a.waitBuilds(t)
	if logged := a.logs.String(); logged != "" {
		t.Errorf("the log is %q, want nothing", logged)
	}
	if files := a.tempFiles(t); len(files) != 0 {
		t.Errorf("%d temporary files stay: %v", len(files), files)
	}
}

// TestMediaDeclaredType shows that the type that an upload says is only told of: a warning at /mimeType if the contents are
// of another, and none for the same, also with parameters, or for a type that cannot be read.
func TestMediaDeclaredType(t *testing.T) {
	a := newAPIServer(t)
	file := testPNG(t, 4, 3)
	for _, tt := range []struct {
		name, contentType string
		warning           string
	}{
		{"the same type", "image/png", ""},
		{"the same type in other case", "IMAGE/PNG", ""},
		{"the same type with a parameter", "image/png; charset=binary", ""},
		{"no type", "", ""},
		{"a type that cannot be read", `image/png; a="`, ""},
		{"another type", "image/jpeg", "the file is image/png, not image/jpeg as uploaded"},
		{"a type for anything", "application/octet-stream", "the file is image/png, not application/octet-stream as uploaded"},
		{"a PDF", "application/pdf", "the file is image/png, not application/pdf as uploaded"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			created := result(t, tt.name, a.as("editor").upload("a.png", tt.contentType, file), http.StatusCreated)
			switch w := created.Warnings; {
			case tt.warning == "" && len(w) != 0:
				t.Errorf("the warnings are %+v, want none", w)
			case tt.warning != "" && (len(w) != 1 || w[0].File != "media/"+created.ID+".json" || w[0].Pointer != "/mimeType" ||
				w[0].Detail != tt.warning || w[0].Line == 0):
				t.Errorf("the warnings are %+v, want one at /mimeType of the medium: %s", w, tt.warning)
			}
			if m := a.storedMedium(t, created.ID); m.MIMEType != "image/png" {
				t.Errorf("the medium is of the type %q, want image/png whatever the upload says", m.MIMEType)
			}
		})
	}
}

// TestMediaUploadRefused shows what is refused, and that a file that is not in a known format is refused before its original
// is written, and a problem of the file that the first bytes do not show only after it.
func TestMediaUploadRefused(t *testing.T) {
	a := newAPIServer(t)
	as := a.as("editor")
	before, originals := a.versions(t), a.originals(t)

	// No format that is known: 422 with one error that is no document's, and no original.
	for _, tt := range []struct {
		name        string
		contentType string
		file        string
	}{
		{"text", "text/plain", "this is not an image"},
		{"text that says it is a PNG", "image/png", "this is not an image"},
		{"nothing", "image/png", ""},
		{"one byte", "image/png", "\x89"},
		{"HTML", "text/html", "<!DOCTYPE html><title>x</title>"},
		{"SVG", "image/svg+xml", `<svg xmlns="http://www.w3.org/2000/svg"/>`},
		{"a signature that is cut", "image/png", "\x89PNG\r\n\x1a"},
		{"a PDF header without its version", "application/pdf", "%PDF-"},
	} {
		e := firstProblem(t, tt.name, as.upload("a.bin", tt.contentType, []byte(tt.file)))
		if e.File != "" || e.Pointer != "" || e.Detail != noFormat || e.Line != 0 || e.Column != 0 {
			t.Errorf("%s: the error is %+v, want one without a file or a place: %s", tt.name, e, noFormat)
		}
	}
	if got := a.versions(t); got != before {
		t.Errorf("the uploads that were refused have changed the store:\n%s\nwant\n%s", got, before)
	}
	if got := a.originals(t); !slices.Equal(got, originals) {
		t.Errorf("the uploads that were refused have written originals: %v, want %v", got, originals)
	}
	if files := a.tempFiles(t); len(files) != 0 {
		t.Errorf("%d temporary files stay: %v", len(files), files)
	}

	// A format that is known, with a file that is not whole: the operation refuses it after the original is written. The
	// first bytes do not decide, so this is not the early refusal, and the error is the one of the document of the medium.
	for _, tt := range []struct{ name, file string }{
		{"a JPEG that is cut", "\xff\xd8\xff\xe0\x00\x10JFIF"},
		{"a PNG that is cut", "\x89PNG\r\n\x1a\n\x00\x00\x00\x0dIHDR"},
		{"a GIF that is cut", "GIF89a"},
	} {
		e := firstProblem(t, tt.name, as.upload("cut.img", "", []byte(tt.file)))
		if !regexpMediaFile.MatchString(e.File) || e.Pointer != "" || !strings.HasPrefix(e.Detail, "cannot read the file: invalid ") {
			t.Errorf("%s: the error is %+v, want the error of the medium's document: cannot read the file: invalid ...", tt.name, e)
		}
	}

	// The name of the file is the operation's to check: it is a problem of the document, at /fileName.
	e := firstProblem(t, "a name with a slash", as.upload("a/b.png", "image/png", testPNG(t, 3, 3)))
	if !regexpMediaFile.MatchString(e.File) || e.Pointer != "/fileName" || !strings.Contains(e.Detail, "not allowed in file names") {
		t.Errorf("the error of a name with a slash is %+v", e)
	}
	e = firstProblem(t, "a name that is too long", as.upload(strings.Repeat("a", 256)+".png", "image/png", testPNG(t, 3, 3)))
	if e.Pointer != "/fileName" || !strings.Contains(e.Detail, "too long") {
		t.Errorf("the error of a name that is too long is %+v", e)
	}
	if got := a.versions(t); got != before {
		t.Errorf("the uploads that were refused have changed the store:\n%s\nwant\n%s", got, before)
	}

	// The query: the name is required, once, and the only name there is.
	file := string(testPNG(t, 3, 3))
	for _, tt := range []struct{ name, path string }{
		{"no query", "/api/media"},
		{"no name", "/api/media?other=1"},
		{"an empty name", "/api/media?fileName="},
		{"the name in other case", "/api/media?filename=a.png"},
		{"the name twice", "/api/media?fileName=a.png&fileName=b.png"},
		{"another name too", "/api/media?fileName=a.png&base=1"},
		{"a query that cannot be read", "/api/media?fileName=a.png&x=%zz"},
	} {
		checkProblem(t, tt.name, as.send(http.MethodPost, tt.path, file, "Content-Type", "image/png"), "bad-query")
	}
	// And the header of the operation ID is checked as it is for the other routes.
	checkProblem(t, "a key without quotes", as.upload("a.png", "image/png", []byte(file), "Idempotency-Key", "op_01JD0000000000000000000009"), "bad-header")
	if got := a.versions(t); got != before {
		t.Errorf("the requests that were refused have changed the store:\n%s\nwant\n%s", got, before)
	}

	// A method that the route does not take.
	checkProblem(t, "DELETE /api/media", as.del("/api/media"), "method-not-allowed")
	if logged := a.logs.String(); logged != "" {
		t.Errorf("the log is %q, want nothing", logged)
	}
}

// TestMediaTooLarge shows the most that a file may have: exactly content.MaxMediaBytes is saved, one byte more is refused with
// 413 and leaves no original, and a body that says it has more is refused without being read.
func TestMediaTooLarge(t *testing.T) {
	a := newAPIServer(t)
	head := []byte("%PDF-1.7\n")

	// Declared: refused at once, without reading.
	declared := &unread{}
	req := a.bodyRequest("editor", http.MethodPost, "/api/media?fileName=big.pdf", declared, "Content-Type", "application/pdf")
	req.ContentLength = content.MaxMediaBytes + 1
	checkProblem(t, "a body that says it has more than a file may", a.respond(req), "too-large")
	if declared.touched.Load() {
		t.Error("a body that says it has more than a file may was read")
	}

	// Not declared: the reader stops it at the limit, and nothing stays.
	before, originals := a.versions(t), a.originals(t)
	req = a.bodyRequest("editor", http.MethodPost, "/api/media?fileName=big.pdf", bigFile(head, content.MaxMediaBytes+1), "Content-Type", "application/pdf")
	checkProblem(t, "a file of one byte more than the most", a.respond(req), "too-large")
	if got := a.versions(t); got != before {
		t.Errorf("the upload that was refused has changed the store:\n%s\nwant\n%s", got, before)
	}
	if got := a.originals(t); !slices.Equal(got, originals) {
		t.Errorf("the upload that was refused has left an original: %v, want %v", got, originals)
	}
	if files := a.tempFiles(t); len(files) != 0 {
		t.Errorf("%d temporary files stay: %v", len(files), files)
	}

	// The most is saved.
	req = a.bodyRequest("editor", http.MethodPost, "/api/media?fileName=big.pdf", bigFile(head, content.MaxMediaBytes), "Content-Type", "application/pdf")
	created := result(t, "a file of the most that is allowed", a.respond(req), http.StatusCreated)
	if m := a.storedMedium(t, created.ID); m.Size != content.MaxMediaBytes || m.MIMEType != "application/pdf" {
		t.Errorf("the medium is %+v, want %d bytes of a PDF", m, content.MaxMediaBytes)
	}
	if logged := a.logs.String(); logged != "" {
		t.Errorf("the log is %q, want nothing", logged)
	}
}

// TestMediaBodyFails shows that a body that cannot be read to its end is a 400, and leaves nothing: neither a medium, nor an
// original, nor a temporary file.
func TestMediaBodyFails(t *testing.T) {
	a := newAPIServer(t)
	before, originals := a.versions(t), a.originals(t)
	file := testPNG(t, 40, 40)
	broken := errors.New("the connection broke")

	for _, tt := range []struct {
		name string
		body io.Reader
	}{
		{"in the first bytes", io.MultiReader(bytes.NewReader(file[:10]), errReader{broken})},
		{"after the first bytes", io.MultiReader(bytes.NewReader(file[:len(file)/2]), errReader{broken})},
		{"at once", errReader{broken}},
	} {
		req := a.bodyRequest("editor", http.MethodPost, "/api/media?fileName=a.png", tt.body, "Content-Type", "image/png")
		checkProblem(t, "a body that fails "+tt.name, a.respond(req), "bad-request")
	}
	if got := a.versions(t); got != before {
		t.Errorf("the uploads that failed have changed the store:\n%s\nwant\n%s", got, before)
	}
	if got := a.originals(t); !slices.Equal(got, originals) {
		t.Errorf("the uploads that failed have written originals: %v, want %v", got, originals)
	}
	if files := a.tempFiles(t); len(files) != 0 {
		t.Errorf("%d temporary files stay: %v", len(files), files)
	}
	if logged := a.logs.String(); logged != "" {
		t.Errorf("the log is %q, want nothing", logged)
	}
}

// errReader fails every read with err.
type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }
