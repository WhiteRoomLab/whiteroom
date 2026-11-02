//go:build linux || darwin

package serve

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestServeNamedPipes checks that named pipes in the public directory, which opening would wait on, are not opened:
// they are not served, not even as the directory index or as the 404 page.
func TestServeNamedPipes(t *testing.T) {
	out := t.TempDir()
	writeRelease(t, out, releaseA, testList(), publicFiles("404.html", "d/index.xml"))
	writeRelease(t, out, releaseB, testList(), nil)
	for _, name := range []string{
		"releases/" + releaseA + "/public/fifo",
		"releases/" + releaseA + "/public/d/index.html",
		"releases/" + releaseB + "/public/404.html",
	} {
		if err := syscall.Mkfifo(filepath.Join(out, filepath.FromSlash(name)), 0o644); err != nil {
			t.Skipf("cannot make named pipes: %v", err)
		}
	}
	setCurrent(t, out, "releases/"+releaseA)
	h := newHandler(t, out)
	get := func(target string, want response) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, target, nil)
		done := make(chan *http.Response, 1)
		go func() { done <- do(h, req) }()
		select {
		case res := <-done:
			checkResponse(t, "GET "+target, res, want)
		case <-time.After(5 * time.Second):
			t.Fatalf("GET %s has not returned in 5 s", target)
		}
	}
	get("/fifo", notFoundWith("404.html"))
	get("/fifo/", notFoundWith("404.html"))
	get("/d/", served("d/index.xml", "application/xml"))
	get("/d/index.html", notFoundWith("404.html"))
	setCurrent(t, out, "releases/"+releaseB)
	get("/missing", plainNotFound)
}
