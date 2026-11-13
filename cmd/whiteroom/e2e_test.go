//go:build darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd

package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/account"
)

// TestInitServeAdminPublish is the first of the issue's conditions, through the commands and HTTP and nothing else, with
// the real number of rounds of the password hash: a config file; whiteroom init, with the password on a pipe, imports the
// sample site; whiteroom serve admin, on the socket of the file, publishes its release; whiteroom serve public serves it;
// a page that the API makes and publishes is a page of serve public as soon as the build that took the publishing has
// ended; and both servers stop with 0, the socket file gone.
func TestInitServeAdminPublish(t *testing.T) {
	dir, sock := t.TempDir(), socketPath(t)
	s := adminSite{
		data: filepath.Join(dir, "data"), site: copySample(t), out: filepath.Join(dir, "out"), cache: filepath.Join(dir, "cache"),
		theme: exampleTheme,
	}
	cfg := s.configIn(t, dir, "socket: "+strconv.Quote(sock), "origin: http://localhost")

	// init: the password is the first line of a pipe.
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pr.Close() }()
	if _, err := pw.WriteString(initPassword + "\n"); err != nil {
		t.Fatal(err)
	}
	if err := pw.Close(); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	e := testEnv(pr, &stdout, &stderr)
	e.newAccounts = account.New
	if code := run(t.Context(), append([]string{"init"}, adminFlags("-config", cfg, s.site)...), e); code != 0 {
		t.Fatalf("whiteroom init = %d, with stderr:\n%s", code, &stderr)
	}
	checkNoSecret(t, "whiteroom init", initPassword, stdout.String(), stderr.String(), s.data)

	// serve admin on the socket of the file, with no flag for the listening, and the build that it makes at the start.
	a := startAdminWith(t, t.Context(), func(e *env) { e.newAccounts = account.New }, "-config", cfg)
	if want := "serving admin of " + s.data + " at http://localhost (listening on socket " + sock + ")\n"; a.line != want {
		t.Errorf("whiteroom serve admin printed %q, want %q", a.line, want)
	}
	socketMode(t, "the socket of the config file", sock)
	a.waitFor(t, adminWait, "the release of the start build", func() bool {
		_, err := os.Readlink(filepath.Join(s.out, "current"))
		return err == nil
	})

	// serve public on the output directory.
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	line, wait := startServe(t, ctx, "-addr", "127.0.0.1:0", s.out)
	origin := originOf(t, line, s.out, "")
	client := newClient()
	defer client.CloseIdleConnections()
	get := func(path string) (int, string) {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, origin+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer func() { _ = res.Body.Close() }()
		body, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		return res.StatusCode, string(body)
	}

	// The login that the administrator that init has made does, over the socket; a page that is not there yet.
	const (
		title = "Published by the end to end test"
		slug  = "end-to-end"
		path  = "/en/" + slug + "/"
	)
	cookie := a.login(t)
	if status, _ := get(path); status != http.StatusNotFound {
		t.Errorf("GET %s before the page is published = %d, want 404", path, status)
	}

	// The API makes the page, saves a revision of it and publishes it; the build that takes it ends.
	id := a.publishPage(t, cookie, "en", title, slug)
	built := a.waitPublished(t, cookie, id)
	if failedBuild(built) || built.Last.Release == "" || len(built.Last.Errors) != 0 {
		t.Fatalf("the build of the publishing has %+v, want a release\nstderr:\n%s", built.Last, a.stderr)
	}
	if got := currentOf(t, s.out); got != built.Last.Release {
		t.Errorf("the build says that it has published %s, and current is %s", built.Last.Release, got)
	}

	// serve public gives the page, as it serves the release that the build has published.
	if status, body := get(path); status != http.StatusOK || !strings.Contains(body, title) {
		t.Errorf("GET %s after the page is published = %d, with the body:\n%.600s\nwant 200, with the title %q", path, status, body, title)
	}

	// Both servers stop with 0; no file is left of the socket, and the data directory is free.
	cancel()
	wait()
	if code := a.stop(t); code != 0 {
		t.Errorf("whiteroom serve admin = %d after its context is done, want 0", code)
	}
	noFile(t, "after the server has stopped", sock)
	checkUnlocked(t, "after the server has stopped", s.data)
	if got := a.logged(); got != "" {
		t.Errorf("whiteroom serve admin printed on stderr, but the heads of the audit logs:\n%s", got)
	}
	verifyHeads(t, "after the stop", s.data, a.heads())
	if _, err := os.Stat(filepath.Join(s.out, "current", "public", "en", slug, "index.html")); err != nil {
		t.Errorf("the page is not in the output directory: %v", err)
	}
}
