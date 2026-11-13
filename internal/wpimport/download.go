package wpimport

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/wprest"
)

const (
	// DownloadTimeout is the default time of one attempt to download a file, until its body is read.
	DownloadTimeout = 10 * time.Minute
	// DownloadAttempts is the default number of attempts to download a file.
	DownloadAttempts = 3

	// maxDownloadFailures is the number of files in a row that could not be downloaded, after which the import stops.
	maxDownloadFailures = 20
)

// Downloader reads the files of attachments from the old site (6.10.5). It sends only GET requests, with the User-Agent
// of wprest and nothing else that identifies the importer: no cookies, no credentials, no Referer. It reads only from
// the site of the home page, which is the same host with the same scheme and port (or https at port 443 for an http
// home page), and from the hosts in Hosts, and it follows at most wprest.MaxRedirects redirects, to the same places.
//
// A file is read again, up to Attempts times in all, if the old site answers 408, 429 or 5xx, does not answer, fails
// within Timeout, or cuts the body; Retry-After is not read. Any other answer that is not 200, such as 404 or 403, ends
// the download at once. Both end in a *skipError for that file. After maxDownloadFailures files in a row that could not
// be read in the attempts, the old site is taken to be gone, and the error is one that stops the import. The zero value
// is ready to use, and a Downloader must not be copied.
type Downloader struct {
	// Hosts are the host names, besides that of the home page, that files are read from, such as that of a CDN.
	Hosts []string
	// Transport is for the requests; nil means one that limits the time of connecting, of the TLS handshake and of the
	// response header, and uses the proxy of the environment.
	Transport http.RoundTripper
	// Timeout is the time of one attempt; zero or less means DownloadTimeout.
	Timeout time.Duration
	// Attempts is the number of attempts; zero or less means DownloadAttempts.
	Attempts int

	// retryWait is the time to wait before the attempt that is the n-th (2 or more); the tests set it.
	retryWait func(n int) time.Duration
	failures  atomic.Int64 // the files in a row that were not read in all the attempts

	mu  sync.Mutex
	own *http.Transport // made when it is first used
}

// fetch reads the file at u, from the home page home (nil if there is none), and passes its body to save, whose result it
// returns. u must be allowed, or the error is a *skipError and nothing is sent. The other errors are those of save that
// are not a *readError, and, if the old site does not answer, the one that stops the import; if ctx is done, its error.
func (d *Downloader) fetch(ctx context.Context, home, u *url.URL,
	save func(io.Reader) (*original, error)) (*original, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := d.refuse(home, u); err != nil {
		return nil, err
	}
	attempts := d.Attempts
	if attempts <= 0 {
		attempts = DownloadAttempts
	}
	client := &http.Client{Transport: d.transport(), CheckRedirect: d.checkRedirect(home, u)}
	var reason string
	for n := 1; n <= attempts; n++ {
		if n > 1 {
			if err := d.wait(ctx, n); err != nil {
				return nil, err
			}
		}
		var orig *original
		var err error
		if orig, reason, err = d.attempt(ctx, client, u, save); reason == "" {
			// The old site answered, or ctx is done, or save failed on its side.
			var skip *skipError
			if err == nil || errors.As(err, &skip) {
				d.failures.Store(0)
			}
			return orig, err
		}
	}
	last := fmt.Sprintf("GET %s: %s", u.Redacted(), reason)
	if n := d.failures.Add(1); n >= maxDownloadFailures {
		return nil, fmt.Errorf("the old site does not answer: %d files in a row could not be downloaded; the last: %s", n, last)
	}
	return nil, skipf("%s (after %s)", last, count(attempts, "attempt"))
}

// attempt makes one request. A reason is the result if the attempt failed in a way that a new attempt may mend, and then
// there is no other result.
func (d *Downloader) attempt(ctx context.Context, client *http.Client, u *url.URL,
	save func(io.Reader) (*original, error)) (orig *original, reason string, err error) {
	timeout := d.Timeout
	if timeout <= 0 {
		timeout = DownloadTimeout
	}
	actx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// The reason of a failure, which a time-out of the attempt makes of any error that it causes.
	failed := func(err error) (*original, string, error) {
		switch {
		case ctx.Err() != nil:
			return nil, "", ctx.Err()
		case actx.Err() != nil:
			return nil, fmt.Sprintf("timed out after %s", timeout), nil
		}
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, err.Error(), nil
	}

	req, err := http.NewRequestWithContext(actx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", skipf("%s cannot be downloaded: invalid URL", u.Redacted())
	}
	req.Header.Set("User-Agent", wprest.UserAgent)
	resp, err := client.Do(req)
	if err != nil {
		var skip *skipError
		if ctx.Err() == nil && errors.As(err, &skip) {
			return nil, "", skip
		}
		return failed(err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch status := resp.StatusCode; {
	case status == http.StatusOK:
	case status == http.StatusRequestTimeout, status == http.StatusTooManyRequests, status >= http.StatusInternalServerError:
		return nil, statusText(status), nil
	default:
		return nil, "", skipf("GET %s: %s", u.Redacted(), statusText(status))
	}
	if resp.ContentLength > maxMediaBytes {
		return nil, "", skipf("GET %s: too large: %d bytes (max %d)", u.Redacted(), resp.ContentLength, maxMediaBytes)
	}
	orig, err = save(resp.Body)
	var read *readError
	switch {
	case err == nil:
		return orig, "", nil
	case ctx.Err() != nil:
		return nil, "", ctx.Err()
	case errors.As(err, &read):
		return failed(read.err)
	}
	return nil, "", err
}

func statusText(status int) string {
	if text := http.StatusText(status); text != "" {
		return fmt.Sprintf("%d %s", status, text)
	}
	return fmt.Sprintf("%d", status)
}

// retryDelay is the time before the n-th attempt (2 or more): 1 second before the second, and 5 seconds before the others.
func retryDelay(n int) time.Duration {
	if n == 2 {
		return time.Second
	}
	return 5 * time.Second
}

// wait waits for the time before the n-th attempt, or until ctx is done, whose error it then returns.
func (d *Downloader) wait(ctx context.Context, n int) error {
	wait := retryDelay(n)
	if d.retryWait != nil {
		wait = d.retryWait(n)
	}
	if wait <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// refuse returns a *skipError unless u is a URL that may be downloaded from.
func (d *Downloader) refuse(home, u *url.URL) error {
	switch {
	case (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil:
		return skipf("%s cannot be downloaded: use an http or https URL without credentials", u.Redacted())
	case !d.allowed(home, u):
		return skipf("%s is not the host of the old site; allow the host to download from it", u.Host)
	}
	return nil
}

// allowed reports whether u, an http or https URL, is on the site of home or on one of the hosts.
func (d *Downloader) allowed(home, u *url.URL) bool {
	if home != nil && sameSite(home, u) {
		return true
	}
	for _, h := range d.Hosts {
		if strings.EqualFold(h, u.Hostname()) {
			return true
		}
	}
	return false
}

// checkRedirect stops a redirect that goes where fetch does not: it is the error of the download of u, and not one to try
// again. The Client has set the Referer of the new request, which is not sent.
func (d *Downloader) checkRedirect(home, u *url.URL) func(req *http.Request, via []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		req.Header.Del("Referer")
		switch {
		case len(via) > wprest.MaxRedirects:
			return skipf("GET %s: stopped after %d redirects", u.Redacted(), wprest.MaxRedirects)
		case req.URL.User != nil:
			return skipf("GET %s: redirected to %s, which has credentials in it", u.Redacted(), req.URL.Redacted())
		case (req.URL.Scheme != "http" && req.URL.Scheme != "https") || !d.allowed(home, req.URL):
			return skipf("GET %s: redirected to %s, which is not on the old site or an allowed host",
				u.Redacted(), req.URL.Redacted())
		}
		return nil
	}
}

// transport returns Transport, or the one that the Downloader makes when it is first needed.
func (d *Downloader) transport() http.RoundTripper {
	if d.Transport != nil {
		return d.Transport
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.own == nil {
		d.own = newDownloadTransport()
	}
	return d.own
}

// close closes the idle connections of the transport that the Downloader made.
func (d *Downloader) close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.own != nil {
		d.own.CloseIdleConnections()
	}
}

// newDownloadTransport has the limits of the transport of wprest; the time of the body is limited by the attempt.
func newDownloadTransport() *http.Transport {
	return &http.Transport{
		Proxy:                  http.ProxyFromEnvironment,
		DialContext:            (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		TLSHandshakeTimeout:    10 * time.Second,
		ResponseHeaderTimeout:  30 * time.Second,
		MaxResponseHeaderBytes: 64 << 10,
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2:      true,
	}
}

func sameSite(from, to *url.URL) bool {
	if !strings.EqualFold(from.Hostname(), to.Hostname()) {
		return false
	}
	if from.Scheme == to.Scheme && port(from) == port(to) {
		return true
	}
	return from.Scheme == "http" && to.Scheme == "https" && port(to) == "443"
}

func port(u *url.URL) string {
	switch {
	case u.Port() != "":
		return u.Port()
	case u.Scheme == "https":
		return "443"
	}
	return "80"
}
