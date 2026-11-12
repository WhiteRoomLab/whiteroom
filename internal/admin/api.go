package admin

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/account"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/edit"
	"github.com/WhiteRoomLab/whiteroom/internal/publish"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

const (
	// documentBodyLimit is the most that the body of a request that carries a document may have: a document of at most
	// content.MaxDocumentBytes, and what wraps it.
	documentBodyLimit = content.MaxDocumentBytes + 64<<10
	// smallBodyLimit is that of the other bodies, which the route reads with readJSON.
	smallBodyLimit = 64 << 10

	// heavyPerAccount is the most requests that an account has among the heavy ones (countAccount).
	heavyPerAccount = 4
	// heavyWait is how long a request waits for the place of heavy work, and heavyRun how long it may work in it.
	heavyWait = 30 * time.Second
	heavyRun  = 30 * time.Second
	// slowTransfer is how long a body may take to be received, and a long answer to be sent.
	slowTransfer = 10 * time.Minute
	// heavyDeadline is the deadline of a route that uses heavyWork: the sum of its stages.
	heavyDeadline = 2*slowTransfer + heavyWait + heavyRun
)

// inflight counts the heavy requests of each account that have not ended.
type inflight struct {
	mu sync.Mutex
	n  map[string]int
}

func (f *inflight) acquire(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.n[id] >= heavyPerAccount {
		return false
	}
	if f.n == nil {
		f.n = make(map[string]int)
	}
	f.n[id]++
	return true
}

func (f *inflight) release(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.n[id] <= 1 {
		delete(f.n, id)
		return
	}
	f.n[id]--
}

func (f *inflight) count(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n[id]
}

// api registers h for pattern, a route of the admin API: h gets the Principal of the session, as withSession gives it.
func (s *Server) api(pattern string, h func(http.ResponseWriter, *http.Request, account.Principal)) {
	s.handle(pattern, s.withSession(h))
}

// apiWithin is api for a route whose request has d to be answered in, and not the 30 seconds of the server.
func (s *Server) apiWithin(pattern string, d time.Duration, h func(http.ResponseWriter, *http.Request, account.Principal)) {
	s.handleWithDeadline(pattern, s.withSession(h), d)
}

// apiHeavy is api for a route that calls heavyWork, whose stages need the deadline of heavyDeadline.
func (s *Server) apiHeavy(pattern string, h func(http.ResponseWriter, *http.Request, account.Principal)) {
	s.apiWithin(pattern, heavyDeadline, h)
}

// handleWithDeadline is handle for a route whose request has the deadline d in place of the server's.
func (s *Server) handleWithDeadline(pattern string, h http.Handler, d time.Duration) {
	s.handle(pattern, h)
	s.deadlinesMu.Lock()
	defer s.deadlinesMu.Unlock()
	if s.deadlines == nil {
		s.deadlines = make(map[string]time.Duration)
	}
	s.deadlines[pattern] = d
}

// deadlineOf returns the deadline of the requests that the route with pattern takes.
func (s *Server) deadlineOf(pattern string) time.Duration {
	s.deadlinesMu.RLock()
	defer s.deadlinesMu.RUnlock()
	if d, ok := s.deadlines[pattern]; ok {
		return d
	}
	return s.deadline
}

// at calls the hook of the tests at point: "checked" when a route has checked the request and is about to do what it asks
// (heavyWork: in the place, before it works; the routes of the builds and of the accounts: before they call the Publisher or
// the Manager; the change of one's own password: after the attempt is counted; the logs: in their place, before they read),
// and "sending" when a place is given back and the answer is not yet written.
func (s *Server) at(point string) {
	if s.hook != nil {
		s.hook(point)
	}
}

// countAccount counts a request of the account of p among its heavy requests: those that heavyWork takes, the uploads,
// the downloads of originals and the readings of the logs, from the one that is received to the one that is sent. It
// returns release, which the caller calls when the request ends. If the account has heavyPerAccount of them already it
// answers 429 and returns nil, without waiting.
func (s *Server) countAccount(w http.ResponseWriter, p account.Principal) (release func()) {
	id := p.Account.ID
	if !s.inflight.acquire(id) {
		writeTooManyRequests(w)
		return nil
	}
	var once sync.Once
	return func() { once.Do(func() { s.inflight.release(id) }) }
}

// heavyWork does work that reads or writes the store in big pieces, so that the memory of the API holds one piece however
// many requests come. The request counts among the heavy requests of its account (countAccount); its body, if bodyLimit is
// not 0, is received into a spool before the place is asked for, so that a slow sender holds none; it waits for the place,
// the only one, for placeWait; run works in the place, reads the body from in, reads or writes the store, writes the answer
// to out, and does not touch the network; the place is given back; and then the answer is sent. run returns the status and
// the headers of the answer besides Content-Type (application/json unless it gives one) and Content-Length, or an error,
// which answerError answers after the place is given back. in is nil if bodyLimit is 0. A route that calls heavyWork is
// registered with apiHeavy, whose deadline is the sum of the stages.
func (s *Server) heavyWork(w http.ResponseWriter, r *http.Request, p account.Principal, bodyLimit int64,
	run func(ctx context.Context, in, out *spool) (status int, header http.Header, err error)) {
	release := s.countAccount(w, p)
	if release == nil {
		return
	}
	defer release()
	var in *spool
	if bodyLimit > 0 {
		if in = s.receiveBody(w, r, bodyLimit); in == nil {
			return
		}
		defer func() { _ = in.Close() }()
	}
	out := newSpool(s.store)
	defer func() { _ = out.Close() }()
	var status int
	var header http.Header
	err := s.inPlace(r.Context(), func(ctx context.Context) (err error) {
		status, header, err = run(ctx, in, out)
		return err
	})
	if in != nil {
		_ = in.Close()
	}
	if err != nil {
		s.answerError(w, r, p, err, attempt{})
		return
	}
	s.sendSpool(w, r, status, header, out)
}

// inPlace waits for the place of heavy work for placeWait, and runs f in it with a context that ends placeRun after it has
// the place, after calling at("checked"). It gives the place back when f returns, also if f panics, and then calls
// at("sending"). If the place is not free in time it does not call f, and its error wraps context.DeadlineExceeded, as that
// of a context that has ended does.
func (s *Server) inPlace(ctx context.Context, f func(ctx context.Context) error) error {
	timer := time.NewTimer(s.placeWait)
	defer timer.Stop()
	select {
	case s.heavy <- struct{}{}:
	case <-timer.C:
		return fmt.Errorf("no place for heavy work within %v: %w", s.placeWait, context.DeadlineExceeded)
	case <-ctx.Done():
		return ctx.Err()
	}
	err := s.work(ctx, f)
	s.at("sending")
	return err
}

func (s *Server) work(ctx context.Context, f func(ctx context.Context) error) error {
	defer func() { <-s.heavy }()
	ctx, cancel := context.WithTimeout(ctx, s.placeRun)
	defer cancel()
	s.at("checked")
	return f(ctx)
}

// receiveBody receives the body of r, which must be application/json of at most limit bytes, into a spool, and gives the
// read slowTransfer to take: the deadline of the server, 30 seconds, is for the small bodies. If it fails it has answered
// with the problem, and returns nil.
func (s *Server) receiveBody(w http.ResponseWriter, r *http.Request, limit int64) *spool {
	if mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mediaType != "application/json" {
		writeProblem(w, http.StatusUnsupportedMediaType, codeUnsupportedMediaType, detailUnsupportedMediaType)
		return nil
	}
	if r.ContentLength > limit {
		writeProblem(w, http.StatusRequestEntityTooLarge, codeTooLarge, detailTooLarge)
		return nil
	}
	if err := extendRead(w, slowTransfer); err != nil {
		s.fail(w, r, err)
		return nil
	}
	in := newSpool(s.store)
	body := &readFailure{r: http.MaxBytesReader(unwrap(w), r.Body, limit)}
	if _, err := io.Copy(in, body); err != nil {
		_ = in.Close()
		_ = extendWrite(w, writeTimeout)
		var tooLarge *http.MaxBytesError
		switch {
		case errors.As(body.err, &tooLarge):
			writeProblem(w, http.StatusRequestEntityTooLarge, codeTooLarge, detailTooLarge)
		case body.err != nil:
			writeProblem(w, http.StatusBadRequest, codeBadRequest, detailBadRequest)
		default:
			s.fail(w, r, err)
		}
		return nil
	}
	return in
}

// errBadBody is the error of readDocument for a body that is not the JSON that the route takes.
var errBadBody = errors.New("the body is not the JSON that this resource takes")

// readDocument reads the body that receiveBody has received into v, a pointer, as readJSON reads a body: members that v
// does not have are refused, and so is anything after the value. The error for a body that is not that JSON is
// errBadBody; a jsontext.Value member of v gets its bytes as they were sent, with their white space.
func readDocument(in *spool, v any) error {
	data, err := in.bytes()
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v, json.RejectUnknownMembers(true)); err != nil {
		return errBadBody
	}
	return nil
}

// operationID returns the operation ID that the header Idempotency-Key of r gives: one line, a string of the structured
// fields ("op_<ULID>" with the quotes) that edit.CheckOperationID accepts, and "" without the header. For anything else it
// has answered 400, and returns false.
func operationID(w http.ResponseWriter, r *http.Request) (id string, ok bool) {
	values := r.Header.Values("Idempotency-Key")
	switch len(values) {
	case 0:
		return "", true
	case 1:
		if id, ok := strings.CutPrefix(values[0], `"`); ok {
			if id, ok = strings.CutSuffix(id, `"`); ok && edit.CheckOperationID(id) == nil {
				return id, true
			}
		}
	}
	writeBadHeader(w)
	return "", false
}

// noOperationID answers 400 and returns false if r has the header Idempotency-Key, for a route that takes no operation ID.
func noOperationID(w http.ResponseWriter, r *http.Request) bool {
	if len(r.Header.Values("Idempotency-Key")) > 0 {
		writeBadHeader(w)
		return false
	}
	return true
}

// answerError answers r with the problem for err, which an operation or a read returned, and p is who asked. tried says what
// the request tried, for a refusal whose error does not say; a refusal is recorded in the activity log (recordDenied) before
// it is answered. An error that is none of the known ones is logged and answered with 500, or with 503 if the context of the
// request has ended. The write gets a new time limit: the request may have taken long.
func (s *Server) answerError(w http.ResponseWriter, r *http.Request, p account.Principal, err error, tried attempt) {
	_ = extendWrite(w, writeTimeout)
	var denied *deniedError
	var invalid content.Errors
	switch {
	case errors.Is(err, errSessionChanged), errors.Is(err, account.ErrNoSession):
		s.clearSessionCookie(w)
		writeProblem(w, http.StatusUnauthorized, codeUnauthenticated, detailUnauthenticated)
	case errors.Is(err, account.ErrForbidden):
		if errors.As(err, &denied) {
			tried = attempt{action: denied.action, target: denied.named}
		}
		s.refuse(w, r, p, tried)
	case errors.As(err, &invalid):
		writeInvalid(w, invalid)
	case errors.Is(err, edit.ErrConflict), errors.Is(err, account.ErrConflict):
		writeProblem(w, http.StatusConflict, codeConflict, detailConflict)
	case errors.Is(err, edit.ErrOperationReused):
		writeProblem(w, http.StatusUnprocessableEntity, codeOperationReused, detailOperationReused)
	case errors.Is(err, fs.ErrNotExist):
		writeProblem(w, http.StatusNotFound, codeNotFound, detailNotFound)
	case errors.Is(err, account.ErrLastAdmin):
		writeProblem(w, http.StatusConflict, codeLastAdmin, detailLastAdmin)
	case errors.Is(err, account.ErrInvalidToken):
		writeProblem(w, http.StatusUnauthorized, codeInvalidToken, detailInvalidToken)
	case errors.Is(err, account.ErrDisabled):
		writeProblem(w, http.StatusConflict, codeDisabled, detailDisabled)
	case errors.Is(err, account.ErrBadCredentials):
		// Only the change of the password of the session checks a password that a request gives: its current one.
		writeInvalid(w, content.Errors{{Path: "/current", Msg: msgWrongCurrent}})
	case errors.Is(err, publish.ErrRollbackRunning):
		writeProblem(w, http.StatusConflict, codeRollbackRunning, detailRollbackRunning)
	case errors.Is(err, errBadBody):
		writeProblem(w, http.StatusBadRequest, codeBadRequest, detailBadRequest)
	default:
		s.fail(w, r, err)
	}
}

// versionAnswer is the JSON of the version of a document, which every answer that has one gives in this form.
type versionAnswer struct {
	Seq     int64     `json:"seq"`
	Hash    string    `json:"hash,omitempty"` // a deletion has none
	SavedAt time.Time `json:"savedAt"`
	SavedBy string    `json:"savedBy"`
}

func versionOf(v store.Version) versionAnswer {
	return versionAnswer{Seq: v.Seq, Hash: v.Hash, SavedAt: v.SavedAt.UTC(), SavedBy: v.SavedBy}
}

// resultAnswer is the JSON of what an operation of internal/edit has done.
type resultAnswer struct {
	Kind            content.RefKind `json:"kind,omitempty"`
	ID              string          `json:"id,omitempty"`
	Version         versionAnswer   `json:"version"`
	Revision        string          `json:"revision,omitempty"`
	Redirect        string          `json:"redirect,omitempty"`
	Number          int             `json:"number,omitzero"`
	Status          content.Status  `json:"status,omitempty"`
	Publishes       bool            `json:"publishes"`
	Warnings        []problemError  `json:"warnings,omitempty"`
	OmittedWarnings int             `json:"omittedWarnings,omitzero"`
}

// writeResult writes the JSON of res to out.
func writeResult(out io.Writer, res edit.Result) error {
	warnings, omitted := problemList(res.Warnings)
	return json.MarshalWrite(out, resultAnswer{
		Kind: res.Kind, ID: res.ID, Version: versionOf(res.Version), Revision: res.Revision, Redirect: res.Redirect,
		Number: res.Number, Status: res.Status, Publishes: res.Publishes, Warnings: warnings, OmittedWarnings: omitted,
	})
}

// writeAnswer writes v as JSON to out, for a piece of work that answers with a value that json.Marshal can write.
func writeAnswer(out io.Writer, v any) error { return json.MarshalWrite(out, v) }

// jsonStream writes JSON to w by hand, which a document of the store needs: it is JSON that the store has checked and
// keeps as bytes, and the answer must hold it as it is, with its white space, and without the copy that the encoder makes
// (json.Marshal compacts it, and a document of 60 MiB then costs 300 MiB). It keeps the commas. The first error stops it.
type jsonStream struct {
	w        io.Writer
	err      error
	open     []byte // the closing characters of the objects and arrays that are open
	has      []bool // whether each of them has a member
	afterKey bool   // a key has been written, and its value is next
}

func newJSONStream(w io.Writer) *jsonStream { return &jsonStream{w: w} }

func (j *jsonStream) write(b ...byte) {
	if j.err == nil {
		_, j.err = j.w.Write(b)
	}
}

// member writes the comma that a member or an element needs before it.
func (j *jsonStream) member() {
	if j.afterKey {
		j.afterKey = false
		return
	}
	if n := len(j.has); n > 0 {
		if j.has[n-1] {
			j.write(',')
		}
		j.has[n-1] = true
	}
}

func (j *jsonStream) object() { j.begin('{', '}') }
func (j *jsonStream) array()  { j.begin('[', ']') }

func (j *jsonStream) begin(open, closing byte) {
	j.member()
	j.write(open)
	j.open = append(j.open, closing)
	j.has = append(j.has, false)
}

// end closes the object or the array that is open.
func (j *jsonStream) end() {
	n := len(j.open)
	if n == 0 {
		j.err = errors.Join(j.err, errors.New("jsonStream: nothing is open"))
		return
	}
	j.write(j.open[n-1])
	j.open, j.has = j.open[:n-1], j.has[:n-1]
}

// key writes the name of a member of the object that is open.
func (j *jsonStream) key(name string) {
	j.member()
	b, err := json.Marshal(name)
	if err != nil {
		j.err = errors.Join(j.err, err)
		return
	}
	j.write(b...)
	j.write(':')
	j.afterKey = true
}

// value writes v as json.Marshal does.
func (j *jsonStream) value(v any) {
	j.member()
	b, err := json.Marshal(v)
	if err != nil {
		j.err = errors.Join(j.err, err)
		return
	}
	j.write(b...)
}

// raw writes data, JSON of one value, as it is.
func (j *jsonStream) raw(data []byte) {
	j.member()
	if len(data) == 0 {
		j.err = errors.Join(j.err, errors.New("jsonStream: an empty value"))
		return
	}
	if j.err == nil {
		_, j.err = j.w.Write(data)
	}
}

// versioned writes the members version and document of d: the version as versionAnswer, and the document as the store
// keeps it.
func (j *jsonStream) versioned(d store.Document) {
	j.key("version")
	j.value(versionOf(d.Version))
	j.key("document")
	j.raw(d.Data)
}

// done returns the error that stopped the stream, or one if an object or an array is still open.
func (j *jsonStream) done() error {
	if j.err == nil && len(j.open) > 0 {
		return errors.New("jsonStream: an object or an array is still open")
	}
	return j.err
}

// writeDocument writes {"version":…,"document":…} for d to out: the document is d.Data as it is.
func writeDocument(out io.Writer, d store.Document) error {
	j := newJSONStream(out)
	j.object()
	j.versioned(d)
	j.end()
	return j.done()
}

// writeDocuments writes {"items":[{"version":…,"document":…},…],"total":…,"offset":…,"limit":…} to out: the documents of
// a page of a list as they are, one for each item.
func writeDocuments(out io.Writer, docs []store.Document, total, offset, limit int) error {
	return writeList(out, total, page{offset: offset, limit: limit}, func(j *jsonStream) error {
		for _, d := range docs {
			j.object()
			j.versioned(d)
			j.end()
		}
		return nil
	})
}

// extendWrite gives the write of the response d from now, for an answer that comes after a long wait or is long. It does
// nothing where the writer has no deadline to set (the recorder of httptest).
func extendWrite(w http.ResponseWriter, d time.Duration) error {
	err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(d))
	if errors.Is(err, http.ErrNotSupported) {
		return nil
	}
	return err
}

// extendRead gives the read of the request d from now, as extendWrite does for the write.
func extendRead(w http.ResponseWriter, d time.Duration) error {
	err := http.NewResponseController(w).SetReadDeadline(time.Now().Add(d))
	if errors.Is(err, http.ErrNotSupported) {
		return nil
	}
	return err
}
