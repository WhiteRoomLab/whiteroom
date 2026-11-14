// Package admin is the admin server of 9.6, apart from the public server in internal/serve (9.5). It answers JSON, on
// an origin of its own that browsers reach over HTTPS behind a reverse proxy, or over HTTP on localhost.
package admin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/netip"
	"net/url"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/account"
	"github.com/WhiteRoomLab/whiteroom/internal/edit"
	"github.com/WhiteRoomLab/whiteroom/internal/publish"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

const (
	contentSecurityPolicy   = "default-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"
	strictTransportSecurity = "max-age=31536000"
	defaultDeadline         = 30 * time.Second
)

// Config is what New needs to make a Server. Only Origin and Accounts are required.
type Config struct {
	// Origin is the origin that browsers use: https with any host, or http with localhost, an IPv4 address in
	// 127.0.0.0/8 or [::1]; with an optional port, and nothing else.
	Origin string
	// TrustedProxies are the reverse proxies whose X-Forwarded-For header is read. A prefix that covers every address is
	// refused, since any client could then name itself.
	TrustedProxies []netip.Prefix
	// Accounts does the logins and the sessions. It must be a Manager of a store that can write: a request that
	// authenticates may write the time of its session. Its clock decides the times of the sessions. New sets its
	// OnRecordError, so that a record of the login history that cannot be written is logged to ErrorLog.
	Accounts *account.Manager
	// ErrorLog gets the errors that requests run into, the records of the login history and of the refusals of permission
	// that could not be written, and the warnings about full tables of attempts and about an X-Forwarded-For header that is
	// not read; nil discards them.
	ErrorLog *log.Logger
	// Publisher calls the operations that save the documents (internal/edit) and the builds. It must use the store of
	// Accounts. It may be nil: the server then has no routes for the documents, the media, the publishing and the builds,
	// but has those of the accounts, the passwords and the logs. New does not run it: the caller calls Run.
	Publisher *publish.Publisher
	// Now is the clock of the limits of login attempts, of the log lines that are limited to one a minute, and of the
	// refusals that are recorded one by one; nil is time.Now. It should be monotonic: a clock that goes back makes the waits
	// longer.
	Now func() time.Time
}

type Server struct {
	origin       string // as browsers send it
	https        bool
	hosts        []string // the values of Host that name the origin, in lower case
	proxies      []netip.Prefix
	accounts     *account.Manager
	store        *store.Store       // that of accounts
	publisher    *publish.Publisher // nil: no routes for the documents and the builds
	editor       *edit.Editor       // that of publisher
	now          func() time.Time
	cookieName   string // of the cookie of the session
	log          *log.Logger
	mux          *http.ServeMux
	deadline     time.Duration // of the routes that have none of their own
	attempts     *attempts
	logins       chan struct{} // a place for each login that attempts has allowed and that has not ended
	forwardedDue func() bool   // true at most once a minute: the time to log an X-Forwarded-For that is not read
	handler      http.Handler  // the check of cross-origin requests, then the routes

	deadlinesMu sync.RWMutex
	deadlines   map[string]time.Duration // by pattern, for the routes that have a deadline of their own
	// heavy is the place of heavy work: a request that works in it has it alone (heavyWork). placeWait is how long a
	// request waits for it, and placeRun how long the work in it may take; the tests shorten them.
	heavy     chan struct{}
	placeWait time.Duration
	placeRun  time.Duration
	// logReads is the place of reading the logs, apart from heavy: one request has it at a time and none waits for it (inLogs).
	logReads  chan struct{}
	inflight  inflight
	denials   denials
	passwords *passwordAttempts // the wrong current passwords of the sessions (PUT /api/session/password)
	// denialWait is how long the record of a refusal waits for the store; the tests shorten it.
	denialWait time.Duration
	// hook is nil outside the tests, which run something at a point of a request (at).
	hook func(point string)
}

func New(c Config) (*Server, error) {
	o, err := parseOrigin(c.Origin)
	if err != nil {
		return nil, err
	}
	for _, p := range c.TrustedProxies {
		switch {
		case !p.IsValid():
			return nil, fmt.Errorf("admin: invalid trusted proxy %v", p)
		case p.Bits() == 0:
			return nil, fmt.Errorf("admin: invalid trusted proxy %v: it covers every address", p)
		}
	}
	if c.Accounts == nil {
		return nil, errors.New("admin: Config.Accounts is required")
	}
	if c.Publisher != nil && c.Publisher.Store() != c.Accounts.Store() {
		return nil, errors.New("admin: Config.Publisher must use the store of Config.Accounts")
	}
	https := o.scheme == "https"
	now := c.Now
	if now == nil {
		now = time.Now
	}
	s := &Server{
		origin:       o.String(),
		https:        https,
		hosts:        o.hosts(),
		proxies:      slices.Clone(c.TrustedProxies),
		accounts:     c.Accounts,
		store:        c.Accounts.Store(),
		publisher:    c.Publisher,
		now:          now,
		cookieName:   sessionCookieName(https),
		log:          c.ErrorLog,
		mux:          http.NewServeMux(),
		deadline:     defaultDeadline,
		logins:       make(chan struct{}, maxLogins),
		forwardedDue: everyMinute(now),
		heavy:        make(chan struct{}, 1),
		logReads:     make(chan struct{}, 1),
		passwords:    newPasswordAttempts(now, passwordAttemptsCapacity),
		placeWait:    heavyWait,
		placeRun:     heavyRun,
		denialWait:   denialWait,
	}
	if c.Publisher != nil {
		s.editor = c.Publisher.Editor()
	}
	if s.log == nil {
		s.log = log.New(io.Discard, "", 0)
	}
	s.attempts = newAttempts(now, attemptsCapacity, warnEveryMinute(s.log, now))
	c.Accounts.OnRecordError(func(operation string, err error) { s.log.Printf("cannot record %s: %v", operation, err) })
	s.handle("POST /api/session", http.HandlerFunc(s.login))
	s.handle("GET /api/session", s.withSession(s.showSession))
	s.handle("DELETE /api/session", http.HandlerFunc(s.logout))
	s.routes()
	s.handler = s.csrf(http.HandlerFunc(s.serveRoutes))
	return s, nil
}

// Origin returns the origin of the server in the form that the Origin header of a request is compared with.
func (s *Server) Origin() string { return s.origin }

// routes registers the routes of the admin API besides those of the session: first those that need only the store of the
// Accounts (the accounts, the passwords and the logs), then, if there is a Publisher, those that call its Editor and it.
func (s *Server) routes() {
	s.accountRoutes()
	s.passwordRoutes()
	s.logRoutes()
	if s.publisher == nil {
		return
	}
	s.api("GET /api/schema", s.showSchema)
	s.api("GET /api/theme", s.showTheme)
	s.documentRoutes()
	s.entryRoutes()
	s.mediaRoutes()
	s.publishingRoutes()
	s.buildRoutes()
}

// ServeHTTP checks the Host (421) and the path (404), then that a request that changes something comes from the
// origin (403), and then the routes answer within the deadline. A panic of a route is logged and answered with 500.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Security-Policy", contentSecurityPolicy)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "no-store")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	if s.https {
		h.Set("Strict-Transport-Security", strictTransportSecurity)
	}
	tw := &trackedWriter{ResponseWriter: w}
	defer s.recoverPanic(tw, r)
	switch {
	case !s.hostAllowed(r.Host):
		writeProblem(tw, http.StatusMisdirectedRequest, codeMisdirected, detailMisdirected)
	case !isCleanPath(r.URL.EscapedPath()):
		writeProblem(tw, http.StatusNotFound, codeNotFound, detailNotFound)
	default:
		s.handler.ServeHTTP(tw, r)
	}
}

// handle registers h for pattern, "METHOD /path" as ServeMux takes it, with wildcards. A request for the path with
// another method gets 405, and Allow lists the methods of the patterns that match the path.
func (s *Server) handle(pattern string, h http.Handler) {
	if method, path, ok := strings.Cut(pattern, " "); !ok || method == "" || !strings.HasPrefix(path, "/") {
		panic(fmt.Sprintf("admin: the pattern %q is not METHOD /path", pattern))
	}
	s.mux.Handle(pattern, h)
}

func (s *Server) serveRoutes(w http.ResponseWriter, r *http.Request) {
	h, pattern := s.mux.Handler(r)
	if pattern == "" {
		noRoute(w, r, h)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.deadlineOf(pattern))
	defer cancel()
	s.mux.ServeHTTP(w, r.WithContext(ctx))
}

// noRoute answers a request that no route takes, for which ServeMux returns h: with 405 and its Allow if h says 405,
// and else with 404. The 405 comes from ServeMux because the other way, a pattern without a method for each path,
// conflicts with wildcards: GET /a/{id} with /a/me.
func noRoute(w http.ResponseWriter, r *http.Request, h http.Handler) {
	p := &probe{header: make(http.Header)}
	h.ServeHTTP(p, r)
	if p.status != http.StatusMethodNotAllowed {
		writeProblem(w, http.StatusNotFound, codeNotFound, detailNotFound)
		return
	}
	if allow := p.header.Get("Allow"); allow != "" {
		w.Header().Set("Allow", allow)
	}
	writeProblem(w, http.StatusMethodNotAllowed, codeMethodNotAllowed, detailMethodNotAllowed)
}

// probe keeps the status and the headers of a response, and drops its body.
type probe struct {
	header http.Header
	status int
}

func (p *probe) Header() http.Header { return p.header }

func (p *probe) WriteHeader(status int) {
	if p.status == 0 {
		p.status = status
	}
}

func (p *probe) Write(b []byte) (int, error) {
	p.WriteHeader(http.StatusOK)
	return len(b), nil
}

// csrf puts the check of cross-origin requests before next. CrossOriginProtection lets a request with neither
// Sec-Fetch-Site nor Origin through, for programs that are not browsers; here they must send the Origin of the server.
func (s *Server) csrf(next http.Handler) http.Handler {
	deny := func(w http.ResponseWriter) {
		writeProblem(w, http.StatusForbidden, codeCrossOrigin, detailCrossOrigin)
	}
	protection := http.NewCrossOriginProtection()
	protection.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { deny(w) }))
	checked := protection.Handler(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isSafeMethod(r.Method) && r.Header.Get("Sec-Fetch-Site") == "" && r.Header.Get("Origin") != s.origin {
			deny(w)
			return
		}
		checked.ServeHTTP(w, r)
	})
}

func isSafeMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}

func (s *Server) hostAllowed(host string) bool {
	return slices.ContainsFunc(s.hosts, func(h string) bool { return equalLower(host, h) })
}

// equalLower reports whether s, with its ASCII letters in lower case, is lower.
func equalLower(s, lower string) bool {
	if len(s) != len(lower) {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c != lower[i] {
			return false
		}
	}
	return true
}

// isCleanPath reports whether p, an escaped path, has the form that ServeMux takes as it is: "/", or a path with no
// empty, "." or ".." segment, also with the dots escaped. ServeMux redirects the others to a cleaned path.
func isCleanPath(p string) bool {
	rest, ok := strings.CutPrefix(p, "/")
	if !ok {
		return false
	}
	if rest == "" {
		return true
	}
	for segment := range strings.SplitSeq(rest, "/") {
		decoded, err := url.PathUnescape(segment)
		if err != nil || decoded == "" || decoded == "." || decoded == ".." {
			return false
		}
	}
	return true
}

// fail answers r with 503 if err says that its context has ended, and else with 500 and logs err.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		writeProblem(w, http.StatusServiceUnavailable, codeBusy, detailBusy)
		return
	}
	s.log.Printf("%s %q: %v", r.Method, r.RequestURI, err)
	writeProblem(w, http.StatusInternalServerError, codeInternal, detailInternal)
}

// recoverPanic answers a panic of a handler with 500, and logs it with the stack, but not the headers or the body of r.
// http.ErrAbortHandler goes on, and so does a panic after the response has begun, as http.ErrAbortHandler: net/http
// then closes the connection, and the client does not take a part of a response for the whole.
func (s *Server) recoverPanic(w *trackedWriter, r *http.Request) {
	v := recover()
	if v == nil {
		return
	}
	if err, ok := v.(error); ok && errors.Is(err, http.ErrAbortHandler) {
		panic(v)
	}
	s.log.Printf("panic serving %s %q: %v\n%s", r.Method, r.RequestURI, v, debug.Stack())
	if w.began {
		panic(http.ErrAbortHandler)
	}
	writeProblem(w, http.StatusInternalServerError, codeInternal, detailInternal)
}

// trackedWriter remembers whether the response has begun.
type trackedWriter struct {
	http.ResponseWriter
	began bool
}

func (w *trackedWriter) WriteHeader(status int) {
	w.began = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *trackedWriter) Write(b []byte) (int, error) {
	w.began = true
	return w.ResponseWriter.Write(b)
}

func (w *trackedWriter) FlushError() error {
	w.began = true
	return http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *trackedWriter) Flush() { _ = w.FlushError() }

// Unwrap lets http.ResponseController reach the writer of net/http.
func (w *trackedWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
