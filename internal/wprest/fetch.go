package wprest

import (
	"context"
	"crypto/tls"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The _fields of the requests, which keep the responses to what is read, and make site_logo and site_icon appear in the
// index. The taxonomies and the post types are asked for whole: WordPress applies _fields to the top-level keys of a
// response that is keyed by name, which are not the fields of the items.
const (
	indexFields = "name,description,url,home,gmt_offset,timezone_string,show_on_front,page_on_front,page_for_posts,site_logo,site_icon,namespaces"
	termFields  = "id,taxonomy,slug,name,parent,link"
	userFields  = "id,name,slug,description,url,link"

	maxErrorBody = 64 << 10 // of an error response, which is read only for the code of WordPress in it
)

// skippedTaxonomies hold nothing of the content of a site: the menus, the parts of themes and blocks, the formats of
// posts, and the data of Polylang.
var skippedTaxonomies = []string{
	"nav_menu", "wp_pattern_category", "wp_theme", "wp_template_part_area", "post_format", "link_category",
	"language", "term_language", "post_translations", "term_translations",
}

// Fetch reads the REST API of the WordPress site whose home page is at home, such as https://example.com. It sends
// only GET requests, one at a time, without cookies and credentials, and follows a redirect only if it stays on the
// site. A route that the site refuses is in Snapshot.Unavailable, and Fetch goes on without it. It is an error if the
// index cannot be read, if the site fails to answer or answers with what WordPress does not send, or if a limit is
// exceeded; the error says which request it was. If ctx is done, the error is the error of ctx.
//
// MaxSnapshotBytes limits the bodies of the responses that Fetch keeps, together, and the snapshot as WriteJSON writes it,
// so that what Fetch returns can be saved and read back by ReadSnapshot with the same Options.
func Fetch(ctx context.Context, home string, opts Options) (*Snapshot, error) {
	opts = opts.withDefaults()
	if _, err := parseBase(home, false); err != nil {
		return nil, fmt.Errorf("invalid home URL %q: use an http or https URL without a user, a query or a fragment", home)
	}
	home = strings.TrimRight(home, "/")
	roots, where := []string{home + "/wp-json/", home + "/?rest_route=/"}, home
	if opts.API != "" {
		if _, err := parseBase(opts.API, true); err != nil {
			return nil, fmt.Errorf("invalid REST API URL %q: use an http or https URL without a user or a fragment, "+
				"and with no query but rest_route", opts.API)
		}
		roots, where = []string{opts.API}, opts.API
	}

	f := &fetcher{ctx: ctx, opts: opts}
	transport := opts.Transport
	if transport == nil {
		t := newTransport()
		defer t.CloseIdleConnections()
		transport = t
	}
	f.client = &http.Client{Transport: transport, Timeout: opts.Timeout, CheckRedirect: checkRedirect}

	s := new(Snapshot)
	var reasons []string
	for _, root := range roots {
		var err error
		if f.root, err = url.Parse(root); err != nil {
			return nil, err
		}
		ix, reason, err := f.index()
		if err != nil {
			return nil, err
		}
		if reason != "" {
			reasons = append(reasons, reason)
			continue
		}
		s.API, s.Index = root, ix
		break
	}
	if s.API == "" {
		return nil, fmt.Errorf("no REST API at %s: %s", where, strings.Join(reasons, "; "))
	}

	taxonomies := []Taxonomy{{Slug: "category", RestBase: "categories"}, {Slug: "post_tag", RestBase: "tags"}}
	blocked, err := f.once("/wp/v2/taxonomies", func(body []byte) (err error) {
		s.Taxonomies, err = decodeKeyed(body, "taxonomies", opts.MaxTaxonomies, (*decoder).taxonomy)
		return err
	})
	if err != nil {
		return nil, err
	}
	if blocked != nil {
		s.Unavailable = append(s.Unavailable, *blocked)
	} else {
		s.sort()
		taxonomies = s.Taxonomies
	}
	blocked, err = f.once("/wp/v2/types", func(body []byte) (err error) {
		s.Types, err = decodeKeyed(body, "types", opts.MaxTaxonomies, (*decoder).postType)
		return err
	})
	if err != nil {
		return nil, err
	}
	if blocked != nil {
		s.Unavailable = append(s.Unavailable, *blocked)
	}

	for _, tx := range taxonomies {
		if slices.Contains(skippedTaxonomies, tx.Slug) {
			continue
		}
		route, ok := restRoute(tx.Slug, tx.RestNamespace, tx.RestBase)
		if !ok {
			s.Unavailable = append(s.Unavailable, Unavailable{Taxonomy: tx.Slug, Code: "invalid_route"})
			continue
		}
		terms, blocked, err := readPages(f, route, termFields, "terms", len(s.Terms), opts.MaxTerms,
			func(body []byte) ([]Term, error) { return decodeTerms(body, tx.Slug) }, func(t Term) int64 { return t.ID })
		if err != nil {
			return nil, err
		}
		if blocked != nil {
			blocked.Taxonomy = tx.Slug
			s.Unavailable = append(s.Unavailable, *blocked)
			continue
		}
		s.Terms = append(s.Terms, terms...)
	}

	users, blocked, err := readPages(f, "/wp/v2/users", userFields, "users", 0, opts.MaxUsers, decodeUsers,
		func(u User) int64 { return u.ID })
	if err != nil {
		return nil, err
	}
	if blocked != nil {
		s.Unavailable = append(s.Unavailable, *blocked)
	}
	s.Users = users

	// A request that was cancelled while its error response was read does not fail.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.check(opts); err != nil {
		return nil, err
	}
	s.sort()

	// What ReadSnapshot reads back is the file that WriteJSON writes, which is larger or smaller than the responses.
	size := &sizeWriter{limit: opts.MaxSnapshotBytes}
	err = s.WriteJSON(size)
	switch {
	case size.n > size.limit:
		return nil, fmt.Errorf("the snapshot is too large to save: at least %d bytes (max %d)", size.limit+1, size.limit)
	case err != nil:
		return nil, err
	}
	return s, nil
}

// sizeWriter counts what is written to it, which it discards, and fails once the count is over limit.
type sizeWriter struct {
	n, limit int64
}

func (w *sizeWriter) Write(p []byte) (int, error) {
	w.n += int64(len(p))
	if w.n > w.limit {
		return 0, errors.New("over the limit")
	}
	return len(p), nil
}

// parseBase checks the URL of a site, or if api is true, of the root of its REST API, which may be a query of rest_route.
func parseBase(raw string, api bool) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	bad := (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || strings.Contains(raw, "#")
	if api && (u.RawQuery != "" || u.ForceQuery) {
		q, err := url.ParseQuery(u.RawQuery)
		bad = bad || err != nil || len(q) != 1 || !q.Has("rest_route")
	} else if strings.Contains(raw, "?") {
		bad = true
	}
	if bad {
		return nil, errors.New("invalid URL")
	}
	return u, nil
}

func newTransport() *http.Transport {
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

// checkRedirect follows a redirect only to the site of the first request, which can be the same site with https if it
// was http. The Client has set the Referer of the new request, which is not sent.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) > MaxRedirects {
		return fmt.Errorf("stopped after %d redirects", MaxRedirects)
	}
	req.Header.Del("Referer")
	first := via[0].URL
	switch {
	case req.URL.User != nil:
		return fmt.Errorf("redirected to %s, which has credentials in it", req.URL.Redacted())
	case !sameSite(first, req.URL):
		return fmt.Errorf("redirected to %s, which is not on %s://%s; read the REST API there",
			req.URL.Redacted(), first.Scheme, first.Host)
	}
	return nil
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

type fetcher struct {
	ctx    context.Context
	client *http.Client
	opts   Options
	root   *url.URL // of the REST API
	kept   int64    // the bytes of the bodies of the responses that are kept
}

// routeURL returns the URL of the route, with the query. The root tells how to make it: a path after the root, or a
// rest_route in the query.
func (f *fetcher) routeURL(route string, query url.Values) string {
	u := *f.root
	q := make(url.Values, len(query)+1)
	for k, v := range query {
		q[k] = v
	}
	if f.root.Query().Has("rest_route") {
		q.Set("rest_route", strings.TrimSuffix(f.root.Query().Get("rest_route"), "/")+route)
	} else {
		u.Path = strings.TrimSuffix(u.Path, "/") + route
		if u.RawPath != "" {
			u.RawPath = strings.TrimSuffix(u.RawPath, "/") + route
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// response is what the site answered. It has the body only of a response with a status of 2xx and a media type of JSON,
// and, of an error response of 4xx with JSON, the code of WordPress.
type response struct {
	url         string
	status      int
	header      http.Header
	mediaType   string // "" if there is none, or it is not valid
	contentType string // the media type, or the header as it is, or a word for none; for messages
	body        []byte
	code        string
}

func (r *response) ok() bool     { return r.status >= 200 && r.status < 300 }
func (r *response) isJSON() bool { return r.mediaType == "application/json" }

// blocked reports whether the site refuses the route, as a site does that hides it from visitors or has a security
// plugin, and not as a site does that is in trouble (408, 5xx) or slows requests down (429): that the route gives
// nothing is not an error of the import.
func (r *response) blocked() bool {
	switch {
	case r.ok():
		return !r.isJSON()
	case r.status >= 400 && r.status < 500:
		return r.status != http.StatusRequestTimeout && r.status != http.StatusTooManyRequests
	}
	return false
}

func (r *response) unavailable(route string) *Unavailable {
	u := &Unavailable{Route: route, Status: r.status, Code: r.code}
	if r.ok() {
		u.Code = "not_json"
	}
	return u
}

func (r *response) problem() string {
	if r.ok() && !r.isJSON() {
		return fmt.Sprintf("not JSON (%s)", r.contentType)
	}
	s := strconv.Itoa(r.status)
	if text := http.StatusText(r.status); text != "" {
		s += " " + text
	}
	if r.code != "" {
		s += " (" + r.code + ")"
	}
	return s
}

func (r *response) err() error {
	return fmt.Errorf("GET %s: %s", r.url, r.problem())
}

// get requests the route. The error is the error of the context if it is done, and otherwise says which request failed.
func (f *fetcher) get(route string, query url.Values) (*response, error) {
	u := f.routeURL(route, query)
	req, err := http.NewRequestWithContext(f.ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, f.fail(u, err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", UserAgent)
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, f.fail(u, err)
	}
	defer func() { _ = resp.Body.Close() }()

	r := &response{url: u, status: resp.StatusCode, header: resp.Header}
	h := resp.Header.Get("Content-Type")
	mt, _, err := mime.ParseMediaType(h)
	if err == nil || errors.Is(err, mime.ErrInvalidMediaParameter) {
		r.mediaType = mt
	}
	switch r.contentType = r.mediaType; {
	case r.mediaType != "":
	case strings.TrimSpace(h) != "":
		r.contentType = strings.TrimSpace(h)
	default:
		r.contentType = "no Content-Type"
	}

	switch {
	case r.ok() && r.isJSON():
		body, err := io.ReadAll(io.LimitReader(resp.Body, f.opts.MaxResponseBytes+1))
		if err != nil {
			return nil, f.fail(u, err)
		}
		if int64(len(body)) > f.opts.MaxResponseBytes {
			return nil, f.fail(u, fmt.Errorf("the response is larger than %d bytes", f.opts.MaxResponseBytes))
		}
		if f.kept += int64(len(body)); f.kept > f.opts.MaxSnapshotBytes {
			return nil, f.fail(u, fmt.Errorf("the responses are larger than %d bytes together", f.opts.MaxSnapshotBytes))
		}
		r.body = body
	case r.status >= 400 && r.status < 500 && r.isJSON():
		if body, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody+1)); err == nil && len(body) <= maxErrorBody {
			r.code = errorCode(body)
		}
	}
	return r, nil
}

func (f *fetcher) fail(u string, err error) error {
	if ctxErr := f.ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return fmt.Errorf("GET %s: %w", u, err)
}

// errorCode returns the code in the body of the error response of WordPress, such as {"code":"rest_forbidden"}, if the
// body is one and the code is made of the characters that WordPress uses; the messages are not kept.
func errorCode(body []byte) string {
	var e struct {
		Code string `json:"code"`
	}
	if json.Unmarshal(body, &e) != nil || e.Code == "" || len(e.Code) > 100 {
		return ""
	}
	for _, c := range e.Code {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '.', c == '-':
		default:
			return ""
		}
	}
	return e.Code
}

// index reads the index of the REST API at the root. If there is none, the second result says why, to try the next
// root: a site without pretty permalinks answers /wp-json/ with its page for what is not found.
func (f *fetcher) index() (Index, string, error) {
	resp, err := f.get("/", url.Values{"_fields": {indexFields}})
	if err != nil {
		return Index{}, "", err
	}
	switch {
	case resp.status == http.StatusOK && resp.isJSON():
		var ix Index
		var namespaces bool
		err := decodeBody(resp.body, func(d *decoder) (err error) {
			ix, err = d.index(&namespaces)
			return err
		})
		switch {
		case err != nil:
			return Index{}, fmt.Sprintf("%s: not the index of a REST API: %v", resp.url, err), nil
		case !namespaces:
			return Index{}, resp.url + ": not the index of a REST API", nil
		}
		return ix, "", nil
	case resp.status == http.StatusOK, slices.Contains([]int{http.StatusNotFound, http.StatusUnauthorized,
		http.StatusForbidden, http.StatusNotAcceptable, http.StatusGone}, resp.status):
		return Index{}, resp.url + ": " + resp.problem(), nil
	}
	return Index{}, "", resp.err()
}

// once reads a route without pages, and decodes the response with decode. A route that the site refuses is the first
// result.
func (f *fetcher) once(route string, decode func(body []byte) error) (*Unavailable, error) {
	resp, err := f.get(route, nil)
	if err != nil {
		return nil, err
	}
	switch {
	case resp.ok() && resp.isJSON():
		if err := decode(resp.body); err != nil {
			return nil, fmt.Errorf("GET %s: %w", resp.url, err)
		}
		return nil, nil
	case resp.blocked():
		return resp.unavailable(route), nil
	}
	return nil, resp.err()
}

// decodeKeyed reads what WordPress answers for the taxonomies and the post types: an object that has the objects of
// the list as its values, with their slugs as the keys.
func decodeKeyed[T any](body []byte, noun string, limit int, elem func(d *decoder, ptr, key string) (T, error)) ([]T, error) {
	var list []T
	err := decodeBody(body, func(d *decoder) error {
		return d.keyed(func(key string) error {
			ptr := d.pointer()
			if len(list) >= limit {
				return &Error{Pointer: ptr, Msg: tooMany(noun, limit+1, limit)}
			}
			v, err := elem(d, ptr, key)
			if err != nil {
				return err
			}
			list = append(list, v)
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	return list, nil
}

func decodeTerms(body []byte, taxonomy string) ([]Term, error) {
	var terms []Term
	err := decodeBody(body, func(d *decoder) error {
		return d.items(func(ptr string) error {
			t, err := d.term(ptr, taxonomy)
			if err != nil {
				return err
			}
			terms = append(terms, t)
			return nil
		})
	})
	return terms, err
}

func decodeUsers(body []byte) ([]User, error) {
	var users []User
	err := decodeBody(body, func(d *decoder) error {
		return d.items(func(ptr string) error {
			u, err := d.user(ptr)
			if err != nil {
				return err
			}
			users = append(users, u)
			return nil
		})
	})
	return users, err
}

// restRoute returns the route of the terms of a taxonomy, which is made of what the site said, so it must not be able to
// point to another place. It is false if there is none.
func restRoute(slug, namespace, base string) (string, bool) {
	if namespace == "" {
		namespace = "wp/v2"
	}
	if base == "" {
		base = slug
	}
	if !safeRoute(namespace) || !safeRoute(base) {
		return "", false
	}
	return "/" + namespace + "/" + base, true
}

func safeRoute(s string) bool {
	for _, part := range strings.Split(s, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
		for _, c := range part {
			switch {
			case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '_', c == '-':
			default:
				return false
			}
		}
	}
	return true
}

// readPages reads the pages of a route in order, and returns the items that decode makes of them, with the first of the
// items that have an ID twice. have is the number of items of the routes before, which count for the limit. A route that
// the site refuses is the second result.
//
// It stops after the last page that X-WP-TotalPages tells, or without it, after a page with fewer items than PerPage;
// in both cases also at an empty page, and at the status 400 that WordPress gives for a page after the last.
func readPages[T any](f *fetcher, route, fields, noun string, have, limit int,
	decode func(body []byte) ([]T, error), id func(T) int64) ([]T, *Unavailable, error) {
	var all []T
	seen := make(map[int64]bool)
	pages := -1 // the number of pages that the site says it has
	maxPages := (limit+PerPage-1)/PerPage + 1
	for page := 1; ; page++ {
		resp, err := f.get(route, url.Values{
			"_fields": {fields}, "order": {"asc"}, "orderby": {"id"},
			"page": {strconv.Itoa(page)}, "per_page": {strconv.Itoa(PerPage)},
		})
		if err != nil {
			return nil, nil, err
		}
		switch {
		case resp.ok() && resp.isJSON():
		case page == 1 && resp.blocked():
			return nil, resp.unavailable(route), nil
		case page > 1 && resp.status == http.StatusBadRequest:
			return all, nil, nil
		default:
			return nil, nil, resp.err()
		}
		items, err := decode(resp.body)
		if err != nil {
			return nil, nil, fmt.Errorf("GET %s: %w", resp.url, err)
		}
		fresh := 0
		for _, it := range items {
			if !seen[id(it)] {
				seen[id(it)] = true
				all = append(all, it)
				fresh++
			}
		}
		switch {
		case page > 1 && len(items) > 0 && fresh == 0:
			return nil, nil, fmt.Errorf("GET %s: page %d repeats the items of the pages before it; the site may ignore the "+
				"page parameter", resp.url, page)
		case have+len(all) > limit:
			return nil, nil, fmt.Errorf("GET %s: %s", resp.url, tooMany(noun, limit+1, limit))
		}
		if page == 1 {
			if n, err := strconv.Atoi(strings.TrimSpace(resp.header.Get("X-WP-TotalPages"))); err == nil && n >= 0 {
				pages = max(n, 1)
			}
		}
		switch {
		case len(items) == 0, pages >= 0 && page >= pages, pages < 0 && len(items) < PerPage:
			return all, nil, nil
		case page >= maxPages:
			return nil, nil, fmt.Errorf("GET %s: more than %d pages of %s", resp.url, maxPages, noun)
		}
	}
}
