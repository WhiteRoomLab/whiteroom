package wpimport

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/wprest"
)

// The reasons of the refusals of URLs, after the host or the URL.
const (
	errNotHost       = " is not the host of the old site; allow the host to download from it"
	errNoCredentials = " cannot be downloaded: use an http or https URL without credentials"
)

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// newTestDownloader waits no time before a new attempt.
func newTestDownloader(t *testing.T, hosts ...string) *Downloader {
	t.Helper()
	d := &Downloader{Hosts: hosts, retryWait: func(int) time.Duration { return 0 }}
	t.Cleanup(d.close)
	return d
}

// newDownloadServer counts the requests that reach h.
func newDownloadServer(t *testing.T, h http.HandlerFunc) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func servePNG(data []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(data)
	}
}

// downloadCase is a fetch of a path of a server, which saves into its own originals and counts its saves.
type downloadCase struct {
	t     *testing.T
	d     *Downloader
	o     *originals
	dir   string
	home  *url.URL
	saves atomic.Int64
}

func newDownloadCase(t *testing.T, d *Downloader, home string) *downloadCase {
	t.Helper()
	o, dir := newTestOriginals(t)
	c := &downloadCase{t: t, d: d, o: o, dir: dir}
	if home != "" {
		c.home = mustURL(t, home)
	}
	return c
}

func (c *downloadCase) fetch(ctx context.Context, raw string) (*original, error) {
	c.t.Helper()
	return c.d.fetch(ctx, c.home, mustURL(c.t, raw), func(r io.Reader) (*original, error) {
		c.saves.Add(1)
		return c.o.save(ctx, r)
	})
}

// wantFile checks that orig is the file with data, and keeps it.
func (c *downloadCase) wantFile(orig *original, err error, data []byte) {
	c.t.Helper()
	if err != nil {
		c.t.Fatalf("fetch: %v", err)
	}
	defer orig.discard()
	if want := sha256Hash(data); orig.hash != want || orig.size != int64(len(data)) {
		c.t.Errorf("original = {%s %d}, want {%s %d}", orig.hash, orig.size, want, len(data))
	}
	if err := orig.keep(); err != nil {
		c.t.Fatal(err)
	}
	if got, err := orig.read(); err != nil || !bytes.Equal(got, data) {
		c.t.Errorf("read = %d bytes, %v; want the data", len(got), err)
	}
}

func wantHits(t *testing.T, hits *atomic.Int64, want int64) {
	t.Helper()
	if got := hits.Load(); got != want {
		t.Errorf("requests = %d, want %d", got, want)
	}
}

func TestDownloadRequest(t *testing.T) {
	data := testPNG(16, 12, 5)
	var mu sync.Mutex
	var got []*http.Request
	srv, hits := newDownloadServer(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, r.Clone(context.Background()))
		mu.Unlock()
		switch r.URL.Path {
		case "/old":
			http.SetCookie(w, &http.Cookie{Name: "a", Value: "b"})
			http.Redirect(w, r, "/wp-content/uploads/a.png?x=1", http.StatusMovedPermanently)
		default:
			servePNG(data)(w, r)
		}
	})
	for _, path := range []string{"/wp-content/uploads/a.png", "/old"} {
		mu.Lock()
		got = nil
		mu.Unlock()
		c := newDownloadCase(t, newTestDownloader(t), srv.URL)
		orig, err := c.fetch(context.Background(), srv.URL+path)
		c.wantFile(orig, err, data)
		mu.Lock()
		reqs := slices.Clone(got)
		mu.Unlock()
		if want := map[string]int{"/wp-content/uploads/a.png": 1, "/old": 2}[path]; len(reqs) != want {
			t.Fatalf("%s: requests = %d, want %d", path, len(reqs), want)
		}
		for _, r := range reqs {
			if r.Method != http.MethodGet {
				t.Errorf("%s: method = %s, want GET", path, r.Method)
			}
			if r.Header.Get("User-Agent") != wprest.UserAgent {
				t.Errorf("%s: User-Agent = %q, want %q", path, r.Header.Get("User-Agent"), wprest.UserAgent)
			}
			for k := range r.Header {
				if k != "User-Agent" && k != "Accept-Encoding" {
					t.Errorf("%s: %s is sent in the request for %s: %q", path, k, r.URL, r.Header[k])
				}
			}
		}
	}
	wantHits(t, hits, 3)
}

func TestDownloadAnswersWithoutRetry(t *testing.T) {
	for _, status := range []int{
		http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound,
		http.StatusMethodNotAllowed, http.StatusGone, http.StatusTeapot, http.StatusUnavailableForLegalReasons,
		http.StatusNoContent, http.StatusPartialContent, http.StatusNotModified, http.StatusMultipleChoices,
		http.StatusMovedPermanently, // without a Location
	} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			srv, hits := newDownloadServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) })
			c := newDownloadCase(t, newTestDownloader(t), srv.URL)
			u := srv.URL + "/a.png"
			_, err := c.fetch(context.Background(), u)
			wantSkip(t, err, fmt.Sprintf("GET %s: %d %s", u, status, http.StatusText(status)))
			wantHits(t, hits, 1)
			if got := c.saves.Load(); got != 0 {
				t.Errorf("saves = %d, want 0", got)
			}
		})
	}
}

func TestDownloadRetries(t *testing.T) {
	data := testPNG(16, 12, 6)
	for _, status := range []int{
		http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout, 599,
	} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			var n atomic.Int64
			srv, hits := newDownloadServer(t, func(w http.ResponseWriter, r *http.Request) {
				if n.Add(1) <= 2 {
					w.WriteHeader(status)
					return
				}
				servePNG(data)(w, r)
			})
			d := newTestDownloader(t)
			var waits []int
			d.retryWait = func(n int) time.Duration { waits = append(waits, n); return 0 }
			c := newDownloadCase(t, d, srv.URL)
			orig, err := c.fetch(context.Background(), srv.URL+"/a.png")
			c.wantFile(orig, err, data)
			wantHits(t, hits, 3)
			if got := c.saves.Load(); got != 1 {
				t.Errorf("saves = %d, want 1", got)
			}
			if !slices.Equal(waits, []int{2, 3}) {
				t.Errorf("waits before the attempts %v, want [2 3]", waits)
			}
			wantNames(t, c.dir, strings.TrimPrefix(sha256Hash(data), "sha256:"))
		})
	}
}

func TestDownloadGivesUp(t *testing.T) {
	for _, tt := range []struct {
		status, attempts, want int
	}{
		{http.StatusServiceUnavailable, 0, 3},
		{http.StatusServiceUnavailable, 1, 1},
		{http.StatusTooManyRequests, 3, 3},
		{http.StatusInternalServerError, 2, 2},
		{599, 5, 5},
	} {
		t.Run(fmt.Sprintf("%d in %d", tt.status, tt.attempts), func(t *testing.T) {
			srv, hits := newDownloadServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tt.status) })
			d := newTestDownloader(t)
			d.Attempts = tt.attempts
			c := newDownloadCase(t, d, srv.URL)
			u := srv.URL + "/a.png"
			_, err := c.fetch(context.Background(), u)
			after := fmt.Sprintf("%d attempts", tt.want)
			if tt.want == 1 {
				after = "1 attempt"
			}
			wantSkip(t, err, fmt.Sprintf("GET %s: %s (after %s)", u, statusText(tt.status), after))
			wantHits(t, hits, int64(tt.want))
			wantNames(t, c.dir)
		})
	}
}

func TestRetryDelay(t *testing.T) {
	for n, want := range map[int]time.Duration{2: time.Second, 3: 5 * time.Second, 4: 5 * time.Second, 10: 5 * time.Second} {
		if got := retryDelay(n); got != want {
			t.Errorf("retryDelay(%d) = %v, want %v", n, got, want)
		}
	}
	// A Downloader with no retryWait waits retryDelay, for as long as ctx is not done.
	var d Downloader
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := d.wait(ctx, 2); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("wait for 1 second with a context of 20 ms: error = %v, want a deadline", err)
	}
}

// cutBody answers 200 with the Content-Length of data and the first n bytes of it, and closes the connection.
func cutBody(data []byte, n int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = fmt.Fprintf(buf, "HTTP/1.1 200 OK\r\nContent-Type: image/png\r\nContent-Length: %d\r\nConnection: close\r\n\r\n", len(data))
		_, _ = buf.Write(data[:n])
		_ = buf.Flush()
	}
}

// wantGaveUp checks that err is the skip of a file that was not read in three attempts, whose reason is not fixed.
func wantGaveUp(t *testing.T, err error, u string) {
	t.Helper()
	var skip *skipError
	if !errors.As(err, &skip) || !strings.HasPrefix(skip.reason, "GET "+u+": ") ||
		!strings.HasSuffix(skip.reason, " (after 3 attempts)") {
		t.Fatalf("error = %v, want a skip of GET %s after 3 attempts", err, u)
	}
}

func TestDownloadCutBody(t *testing.T) {
	data := testPNG(32, 24, 7)
	t.Run("then whole", func(t *testing.T) {
		var n atomic.Int64
		srv, hits := newDownloadServer(t, func(w http.ResponseWriter, r *http.Request) {
			if n.Add(1) == 1 {
				cutBody(data, len(data)/2)(w, r)
				return
			}
			servePNG(data)(w, r)
		})
		c := newDownloadCase(t, newTestDownloader(t), srv.URL)
		orig, err := c.fetch(context.Background(), srv.URL+"/a.png")
		c.wantFile(orig, err, data)
		wantHits(t, hits, 2)
		if got := c.saves.Load(); got != 2 {
			t.Errorf("saves = %d, want 2 (each with a new reader)", got)
		}
	})
	t.Run("always", func(t *testing.T) {
		srv, hits := newDownloadServer(t, cutBody(data, 50))
		c := newDownloadCase(t, newTestDownloader(t), srv.URL)
		u := srv.URL + "/a.png"
		_, err := c.fetch(context.Background(), u)
		wantSkip(t, err, fmt.Sprintf("GET %s: unexpected EOF (after 3 attempts)", u))
		wantHits(t, hits, 3)
		wantNames(t, c.dir)
	})
	t.Run("no response", func(t *testing.T) {
		srv, hits := newDownloadServer(t, func(w http.ResponseWriter, r *http.Request) {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
		})
		c := newDownloadCase(t, newTestDownloader(t), srv.URL)
		u := srv.URL + "/a.png"
		_, err := c.fetch(context.Background(), u)
		wantGaveUp(t, err, u)
		wantHits(t, hits, 3)
	})
}

func TestDownloadConnectionRefused(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	u := srv.URL + "/a.png"
	srv.Close()
	c := newDownloadCase(t, newTestDownloader(t), srv.URL)
	_, err := c.fetch(context.Background(), u)
	wantGaveUp(t, err, u)
	// The reason is that of the connection, not the text of the *url.Error around it.
	if strings.Contains(err.Error(), `Get "`) {
		t.Errorf("error = %q, which has the text of the *url.Error", err)
	}
}

func TestDownloadTimeout(t *testing.T) {
	data := testPNG(16, 12, 8)
	for _, tt := range []struct {
		name string
		h    http.HandlerFunc
	}{
		{"no answer", func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }},
		{"body stops", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", strconv.Itoa(len(data)))
			_, _ = w.Write(data[:10])
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv, hits := newDownloadServer(t, tt.h)
			d := newTestDownloader(t)
			d.Timeout, d.Attempts = 40*time.Millisecond, 2
			c := newDownloadCase(t, d, srv.URL)
			u := srv.URL + "/a.png"
			_, err := c.fetch(context.Background(), u)
			wantSkip(t, err, fmt.Sprintf("GET %s: timed out after 40ms (after 2 attempts)", u))
			wantHits(t, hits, 2)
			wantNames(t, c.dir)
		})
	}
}

func TestDownloadTooLarge(t *testing.T) {
	data := testPNG(16, 12, 9)
	big := bytes.Repeat([]byte{1}, 5000)
	t.Run("Content-Length", func(t *testing.T) {
		lowerMediaLimit(t, 1000)
		srv, hits := newDownloadServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", strconv.Itoa(len(big)))
			_, _ = w.Write(big)
		})
		c := newDownloadCase(t, newTestDownloader(t), srv.URL)
		u := srv.URL + "/a.png"
		_, err := c.fetch(context.Background(), u)
		wantSkip(t, err, fmt.Sprintf("GET %s: too large: 5000 bytes (max 1000)", u))
		wantHits(t, hits, 1)
		if got := c.saves.Load(); got != 0 {
			t.Errorf("saves = %d, want 0: the body is not read", got)
		}
	})
	t.Run("chunked", func(t *testing.T) {
		lowerMediaLimit(t, 1000)
		srv, hits := newDownloadServer(t, func(w http.ResponseWriter, r *http.Request) {
			for i := range 5 {
				_, _ = w.Write(big[i*1000 : (i+1)*1000])
				w.(http.Flusher).Flush()
			}
		})
		c := newDownloadCase(t, newTestDownloader(t), srv.URL)
		_, err := c.fetch(context.Background(), srv.URL+"/a.png")
		wantSkip(t, err, "too large: more than 1000 bytes")
		wantHits(t, hits, 1)
		wantNames(t, c.dir)
	})
	t.Run("gzip", func(t *testing.T) {
		// The size that is counted is that of what the file is after the transport has unpacked it, not the Content-Length.
		lowerMediaLimit(t, 1000)
		var packed bytes.Buffer
		zw := gzip.NewWriter(&packed)
		_, _ = zw.Write(big)
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		if packed.Len() >= 1000 {
			t.Fatalf("the packed data is %d bytes, which the test needs to be under the limit", packed.Len())
		}
		srv, hits := newDownloadServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Encoding", "gzip")
			w.Header().Set("Content-Length", strconv.Itoa(packed.Len()))
			_, _ = w.Write(packed.Bytes())
		})
		c := newDownloadCase(t, newTestDownloader(t), srv.URL)
		_, err := c.fetch(context.Background(), srv.URL+"/a.png")
		wantSkip(t, err, "too large: more than 1000 bytes")
		wantHits(t, hits, 1)
		if got := c.saves.Load(); got != 1 {
			t.Errorf("saves = %d, want 1: the body is read up to the limit", got)
		}
		wantNames(t, c.dir)
	})
	t.Run("exactly the limit", func(t *testing.T) {
		lowerMediaLimit(t, int64(len(data)))
		srv, _ := newDownloadServer(t, servePNG(data))
		c := newDownloadCase(t, newTestDownloader(t), srv.URL)
		orig, err := c.fetch(context.Background(), srv.URL+"/a.png")
		c.wantFile(orig, err, data)
	})
}

func TestDownloadNotAMedium(t *testing.T) {
	srv, hits := newDownloadServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<!doctype html><title>Not found</title>"))
	})
	c := newDownloadCase(t, newTestDownloader(t), srv.URL)
	_, err := c.fetch(context.Background(), srv.URL+"/a.png")
	wantSkip(t, err, "cannot read the file: unsupported file format")
	wantHits(t, hits, 1)
	wantNames(t, c.dir)
}

func TestDownloadRedirects(t *testing.T) {
	data := testPNG(16, 12, 10)
	var mu sync.Mutex
	var seen []*http.Request
	other, otherHits := newDownloadServer(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Clone(context.Background()))
		mu.Unlock()
		servePNG(data)(w, r)
	})
	srv, hits := newDownloadServer(t, func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.HasPrefix(path, "/chain/"):
			n, _ := strconv.Atoi(strings.TrimPrefix(path, "/chain/"))
			if n > 0 {
				w.Header().Set("Set-Cookie", "a=b")
				http.Redirect(w, r, "/chain/"+strconv.Itoa(n-1), http.StatusFound)
				return
			}
		case strings.HasPrefix(path, "/status/"):
			code, _ := strconv.Atoi(strings.TrimPrefix(path, "/status/"))
			http.Redirect(w, r, "/img.png", code)
			return
		case path == "/to-other":
			http.Redirect(w, r, other.URL+"/img.png", http.StatusFound)
			return
		case path == "/to-credentials":
			http.Redirect(w, r, "http://user:secret@"+r.Host+"/img.png", http.StatusFound)
			return
		case path == "/to-ftp":
			w.Header().Set("Location", "ftp://"+r.Host+"/img.png")
			w.WriteHeader(http.StatusFound)
			return
		}
		servePNG(data)(w, r)
	})

	t.Run("statuses", func(t *testing.T) {
		for _, code := range []int{301, 302, 303, 307, 308} {
			c := newDownloadCase(t, newTestDownloader(t), srv.URL)
			orig, err := c.fetch(context.Background(), srv.URL+"/status/"+strconv.Itoa(code))
			c.wantFile(orig, err, data)
		}
	})
	t.Run("five", func(t *testing.T) {
		hits.Store(0)
		c := newDownloadCase(t, newTestDownloader(t), srv.URL)
		orig, err := c.fetch(context.Background(), srv.URL+"/chain/5")
		c.wantFile(orig, err, data)
		wantHits(t, hits, 6)
	})
	t.Run("six", func(t *testing.T) {
		hits.Store(0)
		c := newDownloadCase(t, newTestDownloader(t), srv.URL)
		u := srv.URL + "/chain/6"
		_, err := c.fetch(context.Background(), u)
		wantSkip(t, err, fmt.Sprintf("GET %s: stopped after 5 redirects", u))
		wantHits(t, hits, 6)
		if got := c.saves.Load(); got != 0 {
			t.Errorf("saves = %d, want 0", got)
		}
	})
	t.Run("away", func(t *testing.T) {
		otherHits.Store(0)
		c := newDownloadCase(t, newTestDownloader(t), srv.URL)
		u := srv.URL + "/to-other"
		_, err := c.fetch(context.Background(), u)
		wantSkip(t, err, fmt.Sprintf("GET %s: redirected to %s/img.png, which is not on the old site or an allowed host", u, other.URL))
		wantHits(t, otherHits, 0)
	})
	t.Run("away to ftp", func(t *testing.T) {
		c := newDownloadCase(t, newTestDownloader(t, "127.0.0.1"), srv.URL)
		u := srv.URL + "/to-ftp"
		_, err := c.fetch(context.Background(), u)
		var skip *skipError
		if !errors.As(err, &skip) || !strings.Contains(skip.reason, "which is not on the old site or an allowed host") {
			t.Errorf("error = %v, want a skip for a redirect to ftp", err)
		}
	})
	t.Run("allowed host", func(t *testing.T) {
		otherHits.Store(0)
		mu.Lock()
		seen = nil
		mu.Unlock()
		c := newDownloadCase(t, newTestDownloader(t, "127.0.0.1"), srv.URL)
		orig, err := c.fetch(context.Background(), srv.URL+"/to-other")
		c.wantFile(orig, err, data)
		wantHits(t, otherHits, 1)
		orig, err = c.fetch(context.Background(), other.URL+"/img.png")
		c.wantFile(orig, err, data)
		wantHits(t, otherHits, 2)
		mu.Lock()
		defer mu.Unlock()
		if len(seen) != 2 {
			t.Fatalf("requests to the other server = %d, want 2", len(seen))
		}
		for _, r := range seen {
			if r.Header.Get("User-Agent") != wprest.UserAgent || r.Header.Get("Referer") != "" || r.Header.Get("Cookie") != "" ||
				r.Header.Get("Authorization") != "" {
				t.Errorf("headers of the request to the other server = %v", r.Header)
			}
		}
	})
	t.Run("credentials", func(t *testing.T) {
		c := newDownloadCase(t, newTestDownloader(t), srv.URL)
		u := srv.URL + "/to-credentials"
		_, err := c.fetch(context.Background(), u)
		wantSkip(t, err, fmt.Sprintf("GET %s: redirected to http://user:xxxxx@%s/img.png, which has credentials in it",
			u, strings.TrimPrefix(srv.URL, "http://")))
		if strings.Contains(err.Error(), "secret") {
			t.Errorf("error = %q, which has the password", err)
		}
	})
}

func TestDownloadAllowed(t *testing.T) {
	d := &Downloader{Hosts: []string{"cdn.example.org", "Images.Example.NET"}}
	for _, tt := range []struct {
		home, u string
		want    bool
	}{
		{"http://example.com", "http://example.com/a.png", true},
		{"http://example.com", "http://EXAMPLE.com/a.png", true},
		{"http://example.com", "HTTP://example.com/a.png", true},
		{"http://example.com", "http://example.com:80/a.png", true},
		{"http://example.com", "https://example.com/a.png", true},
		{"http://example.com", "https://example.com:443/a.png", true},
		{"http://example.com", "https://example.com:8443/a.png", false},
		{"http://example.com", "http://example.com:8080/a.png", false},
		{"http://example.com:8080", "https://example.com:443/a.png", true},
		{"http://example.com:8080", "https://example.com:8080/a.png", false},
		{"http://example.com:8080", "http://example.com:8080/a.png", true},
		{"http://example.com:8080", "http://example.com/a.png", false},
		{"https://example.com", "https://example.com/a.png", true},
		{"https://example.com", "http://example.com/a.png", false},
		{"https://example.com", "https://example.com:8443/a.png", false},
		{"http://example.com", "http://www.example.com/a.png", false},
		{"http://example.com", "http://example.com.evil.test/a.png", false},
		{"http://[::1]:8080", "http://[::1]:8080/a.png", true},
		{"http://[::1]:8080", "http://[::2]:8080/a.png", false},
		// The hosts are by name only, with any scheme and port.
		{"http://example.com", "http://cdn.example.org/a.png", true},
		{"http://example.com", "https://CDN.example.org:8443/a.png", true},
		{"https://example.com", "http://images.example.net/a.png", true},
		{"http://example.com", "http://example.org/a.png", false},
		{"http://example.com", "http://sub.cdn.example.org/a.png", false},
		{"", "http://example.com/a.png", false},
		{"", "http://cdn.example.org/a.png", true},
	} {
		t.Run(tt.home+" "+tt.u, func(t *testing.T) {
			var home *url.URL
			if tt.home != "" {
				home = mustURL(t, tt.home)
			}
			err := d.refuse(home, mustURL(t, tt.u))
			if tt.want {
				if err != nil {
					t.Errorf("refused: %v", err)
				}
				return
			}
			wantSkip(t, err, mustURL(t, tt.u).Host+errNotHost)
		})
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDownloadRefuses(t *testing.T) {
	for _, tt := range []struct {
		home, u, reason string
	}{
		{"http://old.example", "http://other.example/a.png", "other.example" + errNotHost},
		{"http://old.example", "https://old.example:8443/a.png", "old.example:8443" + errNotHost},
		{"http://old.example", "ftp://old.example/a.png", "ftp://old.example/a.png" + errNoCredentials},
		{"http://old.example", "file:///etc/passwd", "file:///etc/passwd" + errNoCredentials},
		{"http://old.example", "//old.example/a.png", "//old.example/a.png" + errNoCredentials},
		{"http://old.example", "/a.png", "/a.png" + errNoCredentials},
		{"http://old.example", "http:///a.png", "http:///a.png" + errNoCredentials},
		{"http://old.example", "mailto:a@old.example", "mailto:a@old.example" + errNoCredentials},
		{"http://old.example", "http://user:secret@old.example/a.png", "http://user:xxxxx@old.example/a.png" + errNoCredentials},
		{"http://old.example", "http://user@old.example/a.png", "http://user@old.example/a.png" + errNoCredentials},
		{"http://old.example", "http://user:secret@other.example/a.png", "http://user:xxxxx@other.example/a.png" + errNoCredentials},
		{"", "http://old.example/a.png", "old.example" + errNotHost},
	} {
		t.Run(tt.u, func(t *testing.T) {
			d := newTestDownloader(t)
			d.Transport = roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				t.Errorf("a request was sent to %s", r.URL)
				return nil, errors.New("no requests")
			})
			var saved bool
			var home *url.URL
			if tt.home != "" {
				home = mustURL(t, tt.home)
			}
			// A failure before is not forgotten by a refused URL, which is not an answer of the old site.
			d.failures.Store(7)
			_, err := d.fetch(context.Background(), home, mustURL(t, tt.u), func(io.Reader) (*original, error) {
				saved = true
				return nil, errors.New("no saves")
			})
			wantSkip(t, err, tt.reason)
			if saved {
				t.Error("save was called")
			}
			if got := d.failures.Load(); got != 7 {
				t.Errorf("failures in a row = %d, want 7", got)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Errorf("error = %q, which has the password", err)
			}
		})
	}
}

func TestDownloadNoHome(t *testing.T) {
	data := testPNG(16, 12, 11)
	srv, hits := newDownloadServer(t, servePNG(data))
	u := srv.URL + "/a.png"

	c := newDownloadCase(t, newTestDownloader(t), "")
	_, err := c.fetch(context.Background(), u)
	wantSkip(t, err, strings.TrimPrefix(srv.URL, "http://")+errNotHost)
	wantHits(t, hits, 0)

	c = newDownloadCase(t, newTestDownloader(t, "127.0.0.1"), "")
	orig, err := c.fetch(context.Background(), u)
	c.wantFile(orig, err, data)
	wantHits(t, hits, 1)
}

func TestDownloadStopsWhenTheSiteDoesNotAnswer(t *testing.T) {
	data := testPNG(16, 12, 12)
	srv, _ := newDownloadServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok.png":
			servePNG(data)(w, r)
		case "/missing.png":
			http.NotFound(w, r)
		case "/html.png":
			_, _ = w.Write([]byte("<html>"))
		default:
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	})
	d := newTestDownloader(t)
	d.Attempts = 1
	c := newDownloadCase(t, d, srv.URL)
	fail := func(i int) error {
		_, err := c.fetch(context.Background(), srv.URL+"/fail"+strconv.Itoa(i)+".png")
		return err
	}
	// 19 files in a row are skipped, whatever comes between the runs.
	run := func(answer string) {
		t.Helper()
		for i := 1; i < maxDownloadFailures; i++ {
			err := fail(i)
			wantSkip(t, err, fmt.Sprintf("GET %s/fail%d.png: 503 Service Unavailable (after 1 attempt)", srv.URL, i))
		}
		orig, err := c.fetch(context.Background(), srv.URL+answer)
		switch answer {
		case "/ok.png":
			c.wantFile(orig, err, data)
		case "/missing.png":
			wantSkip(t, err, fmt.Sprintf("GET %s/missing.png: 404 Not Found", srv.URL))
		default:
			wantSkip(t, err, "cannot read the file: unsupported file format")
		}
	}
	run("/ok.png")
	run("/missing.png")
	run("/html.png")
	// No answer comes after the last 19.
	for i := 1; i < maxDownloadFailures; i++ {
		wantSkip(t, fail(i), fmt.Sprintf("GET %s/fail%d.png: 503 Service Unavailable (after 1 attempt)", srv.URL, i))
	}
	err := fail(20)
	wantStopping(t, err)
	want := fmt.Sprintf("the old site does not answer: 20 files in a row could not be downloaded; the last: GET %s/fail20.png: 503 Service Unavailable", srv.URL)
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err, want)
	}

	// Every failure after that stops the import too, until an answer comes.
	wantStopping(t, fail(21))
	orig, err := c.fetch(context.Background(), srv.URL+"/ok.png")
	c.wantFile(orig, err, data)
	wantSkip(t, fail(22), fmt.Sprintf("GET %s/fail22.png: 503 Service Unavailable (after 1 attempt)", srv.URL))
}

func TestDownloadRetriesCountOnce(t *testing.T) {
	// A file that is read in the attempts is one failure, not three: 19 files of 3 attempts do not stop the import.
	srv, hits := newDownloadServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadGateway) })
	c := newDownloadCase(t, newTestDownloader(t), srv.URL)
	for i := 1; i < maxDownloadFailures; i++ {
		_, err := c.fetch(context.Background(), srv.URL+"/"+strconv.Itoa(i))
		wantSkip(t, err, fmt.Sprintf("GET %s/%d: 502 Bad Gateway (after 3 attempts)", srv.URL, i))
	}
	wantHits(t, hits, 3*(maxDownloadFailures-1))
	_, err := c.fetch(context.Background(), srv.URL+"/last")
	wantStopping(t, err)
	wantHits(t, hits, 3*maxDownloadFailures)
}

func TestDownloadCancel(t *testing.T) {
	data := testPNG(16, 12, 13)
	t.Run("before", func(t *testing.T) {
		srv, hits := newDownloadServer(t, servePNG(data))
		c := newDownloadCase(t, newTestDownloader(t), srv.URL)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		for _, u := range []string{srv.URL + "/a.png", "ftp://example.com/a.png"} {
			if _, err := c.fetch(ctx, u); !errors.Is(err, context.Canceled) {
				t.Errorf("%s: error = %v, want context.Canceled", u, err)
			}
		}
		wantHits(t, hits, 0)
		wantNames(t, c.dir)
	})
	t.Run("during the answer", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		srv, hits := newDownloadServer(t, func(w http.ResponseWriter, r *http.Request) {
			cancel()
			<-r.Context().Done()
		})
		// With one attempt, there is no wait after it that would return the error of ctx.
		d := newTestDownloader(t)
		d.Attempts = 1
		c := newDownloadCase(t, d, srv.URL)
		if _, err := c.fetch(ctx, srv.URL+"/a.png"); !errors.Is(err, context.Canceled) {
			t.Errorf("error = %v, want context.Canceled", err)
		}
		wantHits(t, hits, 1)
	})
	t.Run("during the body", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		srv, hits := newDownloadServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", strconv.Itoa(len(data)))
			_, _ = w.Write(data[:10])
			w.(http.Flusher).Flush()
			cancel()
			<-r.Context().Done()
		})
		d := newTestDownloader(t)
		d.Attempts = 1
		c := newDownloadCase(t, d, srv.URL)
		if _, err := c.fetch(ctx, srv.URL+"/a.png"); !errors.Is(err, context.Canceled) {
			t.Errorf("error = %v, want context.Canceled", err)
		}
		wantHits(t, hits, 1)
		wantNames(t, c.dir)
	})
	t.Run("during the wait", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		srv, hits := newDownloadServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) })
		d := newTestDownloader(t)
		d.retryWait = func(int) time.Duration { cancel(); return time.Hour }
		c := newDownloadCase(t, d, srv.URL)
		if _, err := c.fetch(ctx, srv.URL+"/a.png"); !errors.Is(err, context.Canceled) {
			t.Errorf("error = %v, want context.Canceled", err)
		}
		wantHits(t, hits, 1)
		if got := d.failures.Load(); got != 0 {
			t.Errorf("failures in a row = %d, want 0: a cancelled download is not a failure", got)
		}
	})
}

func TestDownloadConcurrent(t *testing.T) {
	const files = 8
	datas := make([][]byte, files)
	for i := range datas {
		datas[i] = testPNG(16, 12, byte(i))
	}
	var mu sync.Mutex
	tried := make(map[string]int)
	srv, hits := newDownloadServer(t, func(w http.ResponseWriter, r *http.Request) {
		i, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/"))
		mu.Lock()
		tried[r.URL.Path]++
		n := tried[r.URL.Path]
		mu.Unlock()
		if i%2 == 1 && n == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		servePNG(datas[i])(w, r)
	})
	d := newTestDownloader(t)
	c := newDownloadCase(t, d, srv.URL)
	var wg sync.WaitGroup
	for i := range files {
		wg.Go(func() {
			ctx := context.Background()
			orig, err := d.fetch(ctx, c.home, mustURL(t, srv.URL+"/"+strconv.Itoa(i)), func(r io.Reader) (*original, error) {
				return c.o.save(ctx, r)
			})
			if err != nil {
				t.Errorf("file %d: %v", i, err)
				return
			}
			if want := sha256Hash(datas[i]); orig.hash != want {
				t.Errorf("file %d: hash = %s, want %s", i, orig.hash, want)
			}
			if err := orig.keep(); err != nil {
				t.Errorf("file %d: %v", i, err)
			}
		})
	}
	wg.Wait()
	wantHits(t, hits, files+files/2)
	if got := len(dirNames(t, c.dir)); got != files {
		t.Errorf("files in the directory = %d, want %d", got, files)
	}
	if got := d.failures.Load(); got != 0 {
		t.Errorf("failures in a row = %d, want 0", got)
	}
}

func TestDownloaderTransport(t *testing.T) {
	data := testPNG(16, 12, 14)
	srv, hits := newDownloadServer(t, servePNG(data))

	// The Downloader makes its transport when it is first used, once, and close does not stop it from being used.
	var d Downloader
	d.close()
	if d.own != nil {
		t.Error("a transport was made before the first request")
	}
	c := newDownloadCase(t, &d, srv.URL)
	orig, err := c.fetch(context.Background(), srv.URL+"/a.png")
	c.wantFile(orig, err, data)
	first := d.own
	d.close()
	orig, err = c.fetch(context.Background(), srv.URL+"/a.png")
	c.wantFile(orig, err, data)
	if first == nil || d.own != first {
		t.Errorf("transport = %p, want the one that was made first (%p)", d.own, first)
	}
	wantHits(t, hits, 2)

	// A transport that is given is the one used, and the Downloader makes none.
	var used atomic.Int64
	e := Downloader{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		used.Add(1)
		return http.DefaultTransport.RoundTrip(r)
	})}
	c = newDownloadCase(t, &e, srv.URL)
	orig, err = c.fetch(context.Background(), srv.URL+"/a.png")
	c.wantFile(orig, err, data)
	e.close()
	if used.Load() != 1 || e.own != nil {
		t.Errorf("requests through the given transport = %d, own = %v; want 1, nil", used.Load(), e.own)
	}
}
