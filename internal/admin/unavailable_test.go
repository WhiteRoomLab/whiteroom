package admin

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
	"github.com/WhiteRoomLab/whiteroom/internal/store/storetest"
)

// unreachableLine starts the line of the log that says that the daemon of the store cannot be reached.
const unreachableLine = "the daemon of the store cannot be reached: "

// newDaemonAPIServer is newAPIServerAt for a store that connects to its daemon on sockets, which the test stops and starts again.
func newDaemonAPIServer(t *testing.T, origin string) (*apiServer, *storetest.Daemon) {
	t.Helper()
	a, d, _ := newWatchedAPIServer(t, origin)
	return a, d
}

// newWatchedAPIServer is newDaemonAPIServer for a store that connects through a Watch of the daemon.
func newWatchedAPIServer(t *testing.T, origin string) (*apiServer, *storetest.Daemon, *storetest.Watch) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	d, err := storetest.Serve(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Errorf("closing the daemon: %v", err)
		}
	})
	w := d.Watch()
	db, err := store.Create(context.Background(), w)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return newAPIServerOn(t, origin, t.TempDir(), db, dir), d, w
}

// receiveWithin is receive with the time that a request takes while the daemon is away: each write waits its 2 seconds to
// reach the daemon, one after the other.
func receiveWithin[T any](t *testing.T, ch <-chan T, what string, within time.Duration) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(within):
		t.Fatalf("timed out waiting for %s", what)
	}
	var zero T
	return zero
}

// linesWith returns how many lines of text start with prefix, and whether every line does.
func linesWith(text, prefix string) (n int, all bool) {
	all = true
	for line := range strings.Lines(text) {
		if strings.HasPrefix(line, prefix) {
			n++
		} else {
			all = false
		}
	}
	return n, all
}

// TestDaemonAway shows what the server answers while the daemon of its store cannot be reached: every request that needs the
// store is 503 with the problem code busy, whatever it is (a read of a document, one of a document that there is not, a list,
// a login, a save, an upload, a download, the end of a session), and none of them is 404 or 500 or takes a session away; the
// log has one line that says that the daemon cannot be reached for a minute of the clock of the server, with no value of the
// requests; and when the daemon is back the same requests work, with the sessions that were valid, and with no new server.
func TestDaemonAway(t *testing.T) {
	a, d := newDaemonAPIServer(t, httpsOrigin)
	a.waitBuilds(t) // a build that runs while the daemon is away fails and is logged
	news := a.stored(t, content.RefTerm, newsTerm)
	changed := strings.Replace(string(news.Data), "お知らせ", "お知らせ9", 1)
	file := testPNG(t, 5, 5)
	before := a.versions(t)

	type probe struct {
		name string
		send func() *http.Response
	}
	probes := []probe{
		{"a session", func() *http.Response { return a.as("viewer").get("/api/session") }},
		{"a login", func() *http.Response { return a.postLogin("viewer", testPassword("viewer")) }},
		{"a list", func() *http.Response { return a.as("editor").get("/api/entries?limit=1") }},
		{"a document", func() *http.Response { return a.as("editor").get("/api/terms/" + newsTerm) }},
		{"a document that there is not", func() *http.Response { return a.as("editor").get("/api/media/" + missingMed) }},
		{"the accounts", func() *http.Response { return a.as("admin").get("/api/accounts") }},
		{"a save", func() *http.Response { return a.as("editor").put("/api/terms/"+newsTerm, saveBody(news.Seq, changed)) }},
		{"an upload", func() *http.Response { return a.as("editor").upload("away.png", "image/png", file) }},
		{"a download", func() *http.Response { return a.as("viewer").get("/api/media/" + sampleLogo + "/file") }},
		{"the head of a download", func() *http.Response {
			return a.as("viewer").send(http.MethodHead, "/api/media/"+sampleLogo+"/file", "")
		}},
		{"the end of a session", func() *http.Response { return a.as("author").send(http.MethodDelete, "/api/session", "") }},
	}
	away := func(what string) {
		t.Helper()
		answers := make([]<-chan *http.Response, len(probes))
		for i, p := range probes {
			answers[i] = async(p.send)
		}
		for i, p := range probes {
			res := receiveWithin(t, answers[i], p.name, 30*time.Second)
			if got := res.Header.Values("Set-Cookie"); len(got) != 0 {
				t.Errorf("%s, %s, sets the cookies %q, want none: the session is still valid", p.name, what, redacted(got))
			}
			if p.name == "the head of a download" {
				// A HEAD has no body, which checkProblem reads.
				if res.StatusCode != http.StatusServiceUnavailable || res.Header.Get("Retry-After") != "5" {
					t.Errorf("%s, %s, has the status %d and the Retry-After %q, want 503 and 5", p.name, what, res.StatusCode,
						res.Header.Get("Retry-After"))
				}
				_ = bodyOf(t, res)
				continue
			}
			checkProblem(t, p.name+", "+what, res, "busy")
		}
	}

	if err := d.Stop(); err != nil {
		t.Fatal(err)
	}
	away("while the daemon is away")
	if n, all := linesWith(a.logs.String(), unreachableLine); n != 1 || !all {
		t.Errorf("the log has %d lines that the daemon cannot be reached, all lines of that kind: %t, want 1 line and nothing else, "+
			"for the requests of a minute:\n%s", n, all, a.logs)
	}
	for _, secret := range []string{"away.png", "terms", testPassword("viewer"), a.people["viewer"].token} {
		if strings.Contains(a.logs.String(), secret) {
			t.Errorf("the log has %q, which is of a request", secret)
		}
	}

	// A minute later, a line again, and only one for the requests of that minute.
	a.clock.advance(time.Minute + time.Second)
	for range 3 {
		res := a.as("viewer").get("/api/session")
		checkProblem(t, "a session a minute later", res, "busy")
	}
	if n, all := linesWith(a.logs.String(), unreachableLine); n != 2 || !all {
		t.Errorf("the log has %d lines that the daemon cannot be reached, all lines of that kind: %t, want 2 and nothing else, "+
			"one for each minute:\n%s", n, all, a.logs)
	}
	if files := a.leftovers(t); len(files) != 0 {
		t.Errorf("%d temporary files stay while the daemon is away: %v", len(files), files)
	}

	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	if got := a.versions(t); got != before {
		t.Errorf("the requests that were answered 503 have changed the store:\n%s\nwant\n%s", got, before)
	}
	// The sessions that were valid are, and the author's, whose end failed, did not end.
	for _, login := range []string{"viewer", "author", "editor", "admin"} {
		if res := a.as(login).get("/api/session"); res.StatusCode != http.StatusOK {
			t.Errorf("the session of %s after the daemon is back has the status %d, want 200", login, res.StatusCode)
		}
	}
	if res := a.postLogin("viewer", testPassword("viewer")); res.StatusCode != http.StatusOK {
		t.Errorf("a login after the daemon is back has the status %d, want 200", res.StatusCode)
	}
	if res := a.as("editor").get("/api/terms/" + newsTerm); res.StatusCode != http.StatusOK {
		t.Errorf("a document after the daemon is back has the status %d, want 200", res.StatusCode)
	}
	result(t, "a save after the daemon is back", a.as("editor").put("/api/terms/"+newsTerm, saveBody(news.Seq, changed)), http.StatusOK)
	created := result(t, "an upload after the daemon is back", a.as("editor").upload("back.png", "image/png", file), http.StatusCreated)
	logo, err := os.ReadFile(filepath.FromSlash(logoOfSite))
	if err != nil {
		t.Fatal(err)
	}
	checkFile(t, "a download after the daemon is back", a.as("viewer").get("/api/media/"+sampleLogo+"/file"), false, logo,
		"image/png", "attachment; filename=logo.png")
	if m := a.storedMedium(t, created.ID); m.Size != len(file) {
		t.Errorf("the medium that was uploaded after the daemon is back has the size %d, want %d", m.Size, len(file))
	}
	if n, all := linesWith(a.logs.String(), unreachableLine); n != 2 || !all {
		t.Errorf("the log has %d lines that the daemon cannot be reached, all lines of that kind: %t, want 2 and nothing else:\n%s",
			n, all, a.logs)
	}
	if files := a.leftovers(t); len(files) != 0 {
		t.Errorf("%d temporary files stay: %v", len(files), files)
	}
}

// TestDaemonGoesMidRequest stops the daemon after a request has been authenticated, at the points of the server where the
// work of the route itself, and not the check of the session, meets the daemon that is away: "checked" is in the place of heavy
// work (or of the logs, or before a build is asked for), before the route works, and "sending" is after the place is given
// back, with the answer not yet made. Each answer is 503 with the problem busy and no cookie, the store is not changed, the
// sessions are valid when the daemon is back, and the log has one line for all of them.
func TestDaemonGoesMidRequest(t *testing.T) {
	a, d := newDaemonAPIServer(t, httpsOrigin)
	a.waitBuilds(t)
	news := a.stored(t, content.RefTerm, newsTerm)
	changed := strings.Replace(string(news.Data), "お知らせ", "お知らせ9", 1)
	before := a.versions(t)
	logo := "/api/media/" + sampleLogo + "/file"
	probes := []struct {
		name, point string
		send        func() *http.Response
	}{
		{"a document", "checked", func() *http.Response { return a.as("editor").get("/api/terms/" + newsTerm) }},
		{"a document that there is not", "checked", func() *http.Response { return a.as("editor").get("/api/media/" + missingMed) }},
		{"a list", "checked", func() *http.Response { return a.as("editor").get("/api/entries?limit=1") }},
		{"the settings", "checked", func() *http.Response { return a.as("editor").get("/api/settings") }},
		{"a log", "checked", func() *http.Response { return a.as("admin").get("/api/logs/activity?limit=1") }},
		{"a save", "checked", func() *http.Response { return a.as("editor").put("/api/terms/"+newsTerm, saveBody(news.Seq, changed)) }},
		{"a download, before its document is read", "checked", func() *http.Response { return a.as("viewer").get(logo) }},
		{"a download, after its document is read", "sending", func() *http.Response { return a.as("viewer").get(logo) }},
		{"the head of a download", "sending", func() *http.Response { return a.as("viewer").send(http.MethodHead, logo, "") }},
		{"a rebuild", "checked", func() *http.Response { return a.as("admin").post("/api/builds", "") }},
	}
	for _, p := range probes {
		var once sync.Once
		stopped := false
		a.hook = func(point string) {
			if point == p.point {
				once.Do(func() {
					stopped = true
					if err := d.Stop(); err != nil {
						t.Errorf("%s: stopping the daemon: %v", p.name, err)
					}
				})
			}
		}
		res := receiveWithin(t, async(p.send), p.name, 60*time.Second)
		a.hook = nil
		if !stopped {
			t.Errorf("%s: the point %q of the server did not come, so that the daemon was not stopped in the request", p.name, p.point)
		}
		if got := res.Header.Values("Set-Cookie"); len(got) != 0 {
			t.Errorf("%s sets the cookies %q, want none: the session is still valid", p.name, redacted(got))
		}
		if p.name == "the head of a download" {
			// A HEAD has no body, which checkProblem reads.
			if res.StatusCode != http.StatusServiceUnavailable || res.Header.Get("Retry-After") != "5" {
				t.Errorf("%s has the status %d and the Retry-After %q, want 503 and 5", p.name, res.StatusCode,
					res.Header.Get("Retry-After"))
			}
			_ = bodyOf(t, res)
		} else {
			checkProblem(t, p.name+", with the daemon gone at "+p.point, res, "busy")
		}
		if err := d.Start(); err != nil {
			t.Fatal(err)
		}
		a.waitBuilds(t)
		for _, login := range []string{"viewer", "editor", "admin"} {
			if res := a.as(login).get("/api/session"); res.StatusCode != http.StatusOK {
				t.Errorf("the session of %s after %s, with the daemon back, has the status %d, want 200", login, p.name,
					res.StatusCode)
			}
		}
	}
	if got := a.versions(t); got != before {
		t.Errorf("the requests that were answered 503 have changed the store:\n%s\nwant\n%s", got, before)
	}
	if n, all := linesWith(a.logs.String(), unreachableLine); n != 1 || !all {
		t.Errorf("the log has %d lines that the daemon cannot be reached, all lines of that kind: %t, want 1 line and nothing else, "+
			"for the requests of a minute:\n%s", n, all, a.logs)
	}
	if files := a.leftovers(t); len(files) != 0 {
		t.Errorf("%d temporary files stay: %v", len(files), files)
	}
}

// TestDownloadSpooled shows that a download that is not read does not need the daemon any more once the answer is ready: the
// original has been copied into the spool of the server, so that a client that stops reading does not hold a connection of the
// store, and the daemon may go away while the client reads the file, which it gets whole. The file is more than the buffers of
// both ends of a connection hold.
func TestDownloadSpooled(t *testing.T) {
	srv := httptest.NewUnstartedServer(nil)
	url := "http://" + srv.Listener.Addr().String()
	a, d := newDaemonAPIServer(t, url)
	srv.Config.Handler = a.Server
	srv.Start()
	t.Cleanup(srv.Close)
	big := bigPDF(48 << 20)
	added := a.addFile(t, "admin", "big.pdf", "application/pdf", big)
	a.waitBuilds(t)

	host := strings.TrimPrefix(url, "http://")
	request := "GET /api/media/" + added.ID + "/file HTTP/1.1\r\nHost: " + host + "\r\nCookie: " + a.wantCookieName() + "=" +
		a.people["viewer"].token + "\r\nConnection: close\r\n\r\n"
	conn := dialAndSend(t, host, request)

	// The copy that the server makes before it sends is whole, in a temporary file of its own, and then it waits for the client.
	waitFor(t, "the file to be copied into the spool of the server", func() bool {
		files := a.tempFiles(t)
		if len(files) != 1 {
			return false
		}
		info, err := os.Stat(filepath.Join(a.tmp, files[0]))
		return err == nil && info.Size() == int64(len(big))
	})
	if err := d.Stop(); err != nil {
		t.Fatal(err)
	}
	res, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("the response to the download, while the daemon is away: %v", err)
	}
	got, err := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if err != nil || res.StatusCode != http.StatusOK || !bytes.Equal(got, big) {
		t.Errorf("the download after the daemon went away is %d with %d bytes (%v), want 200 with the %d bytes of the file",
			res.StatusCode, len(got), err, len(big))
	}
	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the spool to be removed", func() bool { return len(a.leftovers(t)) == 0 })
	if logged := a.logs.String(); logged != "" {
		t.Errorf("the log is %q, want nothing", logged)
	}
}
