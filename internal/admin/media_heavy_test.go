package admin

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// TestMediaPerAccount shows that the uploads and the downloads of an account count among its four heavy requests, the fifth of
// any of them is refused at once without its body being read, and the others of other accounts go on: nothing that waits for
// the network holds the place.
func TestMediaPerAccount(t *testing.T) {
	a := newAPIServer(t)
	author := a.people["author"].account.ID
	pdf, err := os.ReadFile(filepath.FromSlash(pdfOfSite))
	if err != nil {
		t.Fatal(err)
	}
	mine := a.addFile(t, "author", "mine.pdf", "application/pdf", pdf)
	mineDoc := a.stored(t, content.RefMedia, mine.ID)

	// Two downloads are stopped after their documents are read, as if their readers did not read, and two uploads are being sent.
	var stopped atomic.Int32
	reached := make(chan struct{}, 2)
	open := make(chan struct{})
	var openOnce sync.Once
	t.Cleanup(func() { openOnce.Do(func() { close(open) }) })
	a.hook = func(point string) {
		if point == "sending" && stopped.Add(1) <= 2 {
			reached <- struct{}{}
			<-open
		}
	}
	var downloads [2]<-chan *http.Response
	for i := range downloads {
		downloads[i] = async(func() *http.Response { return a.as("author").get("/api/media/" + samplePDF + "/file") })
	}
	for range downloads {
		receive(t, reached, "a download to be stopped")
	}
	first, second := testPNG(t, 40, 40), testPNG(t, 41, 41)
	up1 := a.startPiped(t, "author", http.MethodPost, "/api/media?fileName=one.png", "image/png", first[:100])
	up2 := a.startPiped(t, "author", http.MethodPost, "/api/media?fileName=two.png", "image/png", second[:700])
	waitFor(t, "four requests of the author to be counted", func() bool { return a.inflight.count(author) == heavyPerAccount })
	// What has come of the second upload is in a temporary file of the store, and not kept in memory.
	waitFor(t, "the part of the upload that has come to be in a temporary file", func() bool {
		files := a.tempFiles(t)
		if len(files) != 1 {
			return false
		}
		info, err := os.Stat(filepath.Join(a.dir, "files", files[0]))
		return err == nil && info.Size() == 700
	})

	// The fifth request is refused at once, whichever it is, and a body that comes with it is not read.
	before := a.versions(t)
	for _, tt := range []struct{ name, method, path string }{
		{"an upload", http.MethodPost, "/api/media?fileName=five.png"},
		{"a replacement", http.MethodPut, "/api/media/" + mine.ID + "/file?base=" + strconv.FormatInt(mineDoc.Seq, 10)},
	} {
		body := &unread{}
		checkProblem(t, "the fifth request, "+tt.name, a.respond(a.bodyRequest("author", tt.method, tt.path, body, "Content-Type", "image/png")), "too-many-requests")
		if body.touched.Load() {
			t.Errorf("the body of the fifth request, %s, was read", tt.name)
		}
	}
	checkProblem(t, "the fifth request, a download", a.as("author").get("/api/media/"+samplePDF+"/file"), "too-many-requests")
	checkProblem(t, "the fifth request, a list", a.as("author").get("/api/entries?limit=1"), "too-many-requests")
	if got := a.versions(t); got != before {
		t.Errorf("the requests that were refused have changed the store:\n%s\nwant\n%s", got, before)
	}
	if n := a.inflight.count(author); n != heavyPerAccount {
		t.Errorf("the author has %d requests counted after the fifth ones were refused, want %d", n, heavyPerAccount)
	}

	// The place is free, and the others work, also if they upload.
	if n := len(a.heavy); n != 0 {
		t.Errorf("the place holds %d requests while a download is stopped and bodies are being received, want none", n)
	}
	for name, do := range map[string]func() *http.Response{
		"a list of an editor":        func() *http.Response { return a.as("editor").get("/api/entries?limit=1") },
		"a list of the admin":        func() *http.Response { return a.as("admin").get("/api/media") },
		"an upload of the admin":     func() *http.Response { return a.as("admin").upload("other.png", "image/png", testPNG(t, 6, 6)) },
		"a download of a viewer":     func() *http.Response { return a.as("viewer").get("/api/media/" + sampleLogo + "/file") },
		"a replacement of an editor": func() *http.Response { return a.as("editor").replaceFile(mine.ID, mineDoc.Seq, "application/pdf", pdf) },
	} {
		if res := receive(t, async(do), name); res.StatusCode != http.StatusOK && res.StatusCode != http.StatusCreated {
			t.Errorf("%s while the author has four requests: %d %s", name, res.StatusCode, bodyOf(t, res))
		}
	}

	// All four end as they were asked to.
	openOnce.Do(func() { close(open) })
	for i, ch := range downloads {
		res := receive(t, ch, "a download")
		if got := bodyOf(t, res); res.StatusCode != http.StatusOK || got != string(pdf) {
			t.Errorf("download %d: %d with %d bytes, want 200 with the %d bytes of the file", i+1, res.StatusCode, len(got), len(pdf))
		}
	}
	up1.finish(first[100:])
	up2.finish(second[700:])
	for i, up := range []pipedRequest{up1, up2} {
		if res := receive(t, up.result, "an upload"); res.StatusCode != http.StatusCreated {
			t.Errorf("upload %d: %d %s, want 201", i+1, res.StatusCode, bodyOf(t, res))
		}
	}
	waitFor(t, "the requests of the author to end", func() bool { return a.inflight.count(author) == 0 })
	if res := a.as("author").get("/api/entries?limit=1"); res.StatusCode != http.StatusOK {
		t.Errorf("a request of the author after them has the status %d, want 200", res.StatusCode)
	}
	if files := a.tempFiles(t); len(files) != 0 {
		t.Errorf("%d temporary files stay: %v", len(files), files)
	}
	if logged := a.logs.String(); logged != "" {
		t.Errorf("the log is %q, want nothing", logged)
	}
}

// TestMediaDownloadPlace shows that a download has the place of heavy work to read the document, and the others wait for it as
// for any other reader.
func TestMediaDownloadPlace(t *testing.T) {
	a := newAPIServer(t)
	logo, err := os.ReadFile(filepath.FromSlash(logoOfSite))
	if err != nil {
		t.Fatal(err)
	}
	g := newGate(t, a.testServer)

	first := async(func() *http.Response { return a.as("viewer").get("/api/media/" + sampleLogo + "/file") })
	g.waitHeld(t)
	if n := len(a.heavy); n != 1 {
		t.Errorf("the place holds %d requests while a download reads the document, want 1", n)
	}
	other := async(func() *http.Response { return a.as("editor").get("/api/terms") })
	quiet(t, other, "a request was answered while a download had the place")
	g.open()
	checkFile(t, "the download", receive(t, first, "the download"), false, logo, "image/png", "attachment; filename=logo.png")
	if res := receive(t, other, "the request that waited"); res.StatusCode != http.StatusOK {
		t.Errorf("the request that waited has the status %d, want 200", res.StatusCode)
	}
	if n := len(a.heavy); n != 0 {
		t.Errorf("the place holds %d requests at the end, want none", n)
	}
}

// TestMediaDownloadNotRead shows that a download that is not read does not hold the place: the document is read and the place is
// given back before the file is sent, and the file waits in the socket for a client that does not read, which counts for its
// account alone. The file is more than the buffers of both ends of a connection hold.
func TestMediaDownloadNotRead(t *testing.T) {
	a, ls := newLiveAPIServer(t)
	viewer := a.people["viewer"].account.ID
	big := bigPDF(48 << 20)
	added := a.addFile(t, "admin", "big.pdf", "application/pdf", big)

	sending := make(chan struct{})
	var once sync.Once
	var held atomic.Int32
	a.hook = func(point string) {
		if point == "sending" {
			if len(a.heavy) != 0 {
				held.Add(1)
			}
			once.Do(func() { close(sending) })
		}
	}
	host := strings.TrimPrefix(ls.url, "http://")
	request := "GET /api/media/" + added.ID + "/file HTTP/1.1\r\nHost: " + host + "\r\nCookie: " + ls.wantCookieName() + "=" +
		a.people["viewer"].token + "\r\nConnection: close\r\n\r\n"
	conn := dialAndSend(t, host, request)
	receive(t, sending, "the place to be given back")

	// The connection reads nothing, and the handler is writing; yet nobody waits for the place.
	for name, do := range map[string]func() *http.Response{
		"a list of an editor": func() *http.Response { return a.as("editor").get("/api/entries?limit=1") },
		"an upload":           func() *http.Response { return a.as("admin").upload("other.png", "image/png", testPNG(t, 6, 6)) },
		"a download":          func() *http.Response { return a.as("admin").get("/api/media/" + sampleLogo + "/file") },
	} {
		if res := receive(t, async(do), name+" while a download is not read"); res.StatusCode != http.StatusOK && res.StatusCode != http.StatusCreated {
			t.Errorf("%s while a download is not read: %d %s", name, res.StatusCode, bodyOf(t, res))
		}
	}
	if n := len(a.heavy); n != 0 {
		t.Errorf("the place holds %d requests while a download is not read, want none", n)
	}
	if n := a.inflight.count(viewer); n != 1 {
		t.Errorf("the viewer has %d requests counted while the file is not read, want 1", n)
	}

	// The file that is not read is gone when its connection is.
	_ = conn.Close()
	waitFor(t, "the download to end", func() bool { return a.inflight.count(viewer) == 0 })
	if n := held.Load(); n != 0 {
		t.Errorf("the hook \"sending\" found the place taken %d times", n)
	}
	if files := a.tempFiles(t); len(files) != 0 {
		t.Errorf("%d temporary files stay: %v", len(files), files)
	}
	if logged := a.logs.String(); logged != "" {
		t.Errorf("the log is %q, want nothing", logged)
	}
}

// newTimedAPIServer is newLiveAPIServer for a server whose connections have short times to read and to write, as NewHTTPServer
// gives them long ones, to show what a route does about them.
func newTimedAPIServer(t *testing.T, read, write time.Duration) (*apiServer, string) {
	t.Helper()
	srv := httptest.NewUnstartedServer(nil)
	url := "http://" + srv.Listener.Addr().String()
	a := newAPIServerAt(t, url)
	srv.Config.Handler = a.Server
	srv.Config.ReadTimeout, srv.Config.WriteTimeout = read, write
	srv.Start()
	t.Cleanup(srv.Close)
	return a, url
}

// TestMediaLongTransfers shows that a file can take longer to be received and to be sent than the times of the connection, which
// are short here: the routes of the files give the connection the times they need. Without it the connection is closed.
func TestMediaLongTransfers(t *testing.T) {
	const slow = 500 * time.Millisecond
	a, url := newTimedAPIServer(t, slow, slow)
	a.waitBuilds(t) // the build that the Publisher makes at its start must not slow the connections down
	file := testPNG(t, 40, 40)
	pdf, err := os.ReadFile(filepath.FromSlash(pdfOfSite))
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}

	// An upload whose body comes after the time to read, and whose answer is written after the time to write.
	pr, pw := io.Pipe()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url+"/api/media?fileName=slow.png", pr)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", a.origin)
	req.Header.Set("Content-Type", "image/png")
	req.Header.Set("Cookie", a.wantCookieName()+"="+a.people["editor"].token)
	done := async(func() *http.Response {
		res, err := client.Do(req)
		if err != nil {
			t.Errorf("the slow upload failed: %v", err)
			return nil
		}
		return res
	})
	go func() {
		_, _ = pw.Write(file[:600])
		time.Sleep(3 * slow)
		_, _ = pw.Write(file[600:])
		_ = pw.Close()
	}()
	res := receive(t, done, "the slow upload")
	if res == nil {
		return
	}
	created := result(t, "the slow upload", res, http.StatusCreated)

	// A download whose file is sent after the time to write.
	var late atomic.Bool
	a.hook = func(point string) {
		if point == "sending" && late.CompareAndSwap(false, true) {
			time.Sleep(3 * slow)
		}
	}
	get, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url+"/api/media/"+samplePDF+"/file", nil)
	if err != nil {
		t.Fatal(err)
	}
	get.Header.Set("Cookie", a.wantCookieName()+"="+a.people["viewer"].token)
	res, err = client.Do(get)
	if err != nil {
		t.Fatalf("the late download failed: %v", err)
	}
	if got := bodyOf(t, res); res.StatusCode != http.StatusOK || !bytes.Equal([]byte(got), pdf) {
		t.Errorf("the late download is %d with %d bytes, want 200 with the %d bytes of the file", res.StatusCode, len(got), len(pdf))
	}
	if m := a.storedMedium(t, created.ID); m.Size != len(file) {
		t.Errorf("the medium has the size %d, want %d", m.Size, len(file))
	}
}
