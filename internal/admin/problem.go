package admin

import (
	"encoding/json/v2"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

// The codes of the problems are for programs. The details are fixed sentences, with no value of the request and no
// message of a decoder.
const (
	codeMisdirected          = "misdirected"
	codeNotFound             = "not-found"
	codeMethodNotAllowed     = "method-not-allowed"
	codeCrossOrigin          = "cross-origin"
	codeTooLarge             = "too-large"
	codeUnsupportedMediaType = "unsupported-media-type"
	codeBadRequest           = "bad-request"
	codeTooManyAttempts      = "too-many-attempts"
	codeBadCredentials       = "bad-credentials"
	codeUnauthenticated      = "unauthenticated"
	codeBusy                 = "busy"
	codeInternal             = "internal"
	codeTooManyRequests      = "too-many-requests"
	codeForbidden            = "forbidden"
	codeConflict             = "conflict"
	codeInvalid              = "invalid"
	codeOperationReused      = "operation-reused"
	codeLastAdmin            = "last-admin"
	codeInvalidToken         = "invalid-token"
	codeRollbackRunning      = "rollback-running"
	codeBadQuery             = "bad-query"
	codeBadHeader            = "bad-header"
	codeDisabled             = "disabled"

	detailMisdirected          = "the Host of the request is not that of this server"
	detailNotFound             = "there is no such resource"
	detailMethodNotAllowed     = "the resource does not take this method"
	detailCrossOrigin          = "the request does not come from the origin of this server"
	detailTooLarge             = "the body is too large"
	detailUnsupportedMediaType = "the body must be application/json"
	detailBadRequest           = "the body is not the JSON that this resource takes"
	detailTooManyAttempts      = "too many failed logins; try again later"
	detailBadCredentials       = "wrong login name or password"
	detailUnauthenticated      = "not logged in"
	detailBusy                 = "the server could not answer in time; try again later"
	detailInternal             = "the server failed to answer"
	detailTooManyRequests      = "too many requests at once; try again later"
	detailForbidden            = "the account is not allowed to do this"
	detailConflict             = "the document has changed since it was read"
	detailInvalid              = "the request has problems"
	detailOperationReused      = "the Idempotency-Key was used for another request"
	detailLastAdmin            = "it would leave no active administrator"
	detailInvalidToken         = "invalid or expired token"
	detailRollbackRunning      = "another rollback is running"
	detailBadQuery             = "the query is not one that this resource takes"
	detailBadHeader            = "a header of the request is not one that this resource takes"
	detailDisabled             = "the account is disabled; enable it first"
)

// maxProblems is the most problems that an answer lists; the others are counted.
const maxProblems = 100

// problem is a problem detail of RFC 9457, with the extension member code. An invalid one lists the problems of the
// request in Errors, the first maxProblems of them, and counts the others in OmittedErrors.
type problem struct {
	Type          string         `json:"type"`
	Title         string         `json:"title"`
	Status        int            `json:"status"`
	Detail        string         `json:"detail"`
	Code          string         `json:"code"`
	Errors        []problemError `json:"errors,omitempty"`
	OmittedErrors int            `json:"omittedErrors,omitzero"`
}

// problemError is one problem of a document, located as the build locates its own: Pointer is a JSON Pointer in the file,
// and the line and the column are in the file or, for a document that a request carries, in the value that it sent.
type problemError struct {
	File    string `json:"file,omitempty"`
	Pointer string `json:"pointer"`
	Line    int    `json:"line,omitzero"`
	Column  int    `json:"column,omitzero"`
	Detail  string `json:"detail"`
}

// problemList returns the first maxProblems of es as they are answered, and how many it leaves out.
func problemList(es content.Errors) (list []problemError, omitted int) {
	n := min(len(es), maxProblems)
	list = make([]problemError, 0, n)
	for _, e := range es[:n] {
		list = append(list, problemError{
			File: validText(e.File), Pointer: validText(e.Path), Line: e.Line, Column: e.Column, Detail: validText(e.Msg),
		})
	}
	return list, len(es) - n
}

// validText returns s with each invalid byte replaced by U+FFFD, since JSON has no form for them.
func validText(s string) string { return strings.ToValidUTF8(s, string(utf8.RuneError)) }

// writeProblem answers with the problem that has status, code and detail. A 503 asks for a retry in 5 seconds, and a 401
// has the challenge that RFC 9110 asks for, the scheme Session of the cookie of the login: it is not a registered scheme,
// and a browser does not act on it.
func writeProblem(w http.ResponseWriter, status int, code, detail string) {
	writeProblemBody(w, problem{Type: "about:blank", Title: http.StatusText(status), Status: status, Detail: detail, Code: code})
}

// writeInvalid answers 422 with the problems of es: the first maxProblems of them, in their order.
func writeInvalid(w http.ResponseWriter, es content.Errors) {
	list, omitted := problemList(es)
	writeProblemBody(w, problem{
		Type: "about:blank", Title: http.StatusText(http.StatusUnprocessableEntity), Status: http.StatusUnprocessableEntity,
		Detail: detailInvalid, Code: codeInvalid, Errors: list, OmittedErrors: omitted,
	})
}

// writeTooManyRequests answers 429 for an account that has as many heavy requests as it may, and asks for a retry in a second.
func writeTooManyRequests(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "1")
	writeProblem(w, http.StatusTooManyRequests, codeTooManyRequests, detailTooManyRequests)
}

func writeForbidden(w http.ResponseWriter) {
	writeProblem(w, http.StatusForbidden, codeForbidden, detailForbidden)
}

func writeBadQuery(w http.ResponseWriter) {
	writeProblem(w, http.StatusBadRequest, codeBadQuery, detailBadQuery)
}

func writeBadHeader(w http.ResponseWriter) {
	writeProblem(w, http.StatusBadRequest, codeBadHeader, detailBadHeader)
}

func writeProblemBody(w http.ResponseWriter, p problem) {
	body, err := json.Marshal(p)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Del("Content-Length")
	h.Set("Content-Type", "application/problem+json")
	switch p.Status {
	case http.StatusServiceUnavailable:
		h.Set("Retry-After", "5")
	case http.StatusUnauthorized:
		h.Set("WWW-Authenticate", "Session")
	}
	w.WriteHeader(p.Status)
	_, _ = w.Write(body)
}

// writeTooManyAttempts answers 429 and asks for a retry after wait, rounded up to whole seconds. The answer does not tell
// which limit refused, or whether an account has the login name.
func writeTooManyAttempts(w http.ResponseWriter, wait time.Duration) {
	w.Header().Set("Retry-After", strconv.FormatInt(int64((wait+time.Second-1)/time.Second), 10))
	writeProblem(w, http.StatusTooManyRequests, codeTooManyAttempts, detailTooManyAttempts)
}

// writeJSON answers with status and v as JSON. It answers 500 if v has no JSON form.
func writeJSON(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, codeInternal, detailInternal)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// readJSON reads the body of r, which must be application/json of at most limit bytes, into v, a pointer. Members that
// v does not have are refused, and so is anything after the value. If it fails it has answered with the problem, and
// returns false.
func readJSON(w http.ResponseWriter, r *http.Request, limit int64, v any) bool {
	if mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mediaType != "application/json" {
		writeProblem(w, http.StatusUnsupportedMediaType, codeUnsupportedMediaType, detailUnsupportedMediaType)
		return false
	}
	data, err := io.ReadAll(http.MaxBytesReader(unwrap(w), r.Body, limit))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeProblem(w, http.StatusRequestEntityTooLarge, codeTooLarge, detailTooLarge)
		} else {
			writeProblem(w, http.StatusBadRequest, codeBadRequest, detailBadRequest)
		}
		return false
	}
	if err := json.Unmarshal(data, v, json.RejectUnknownMembers(true)); err != nil {
		writeProblem(w, http.StatusBadRequest, codeBadRequest, detailBadRequest)
		return false
	}
	return true
}

// unwrap returns the ResponseWriter of net/http under the wrappers of this package. http.MaxBytesReader needs it to
// make the server close the connection after a body that is too large.
func unwrap(w http.ResponseWriter) http.ResponseWriter {
	for {
		u, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return w
		}
		w = u.Unwrap()
	}
}
