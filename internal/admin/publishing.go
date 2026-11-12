package admin

import (
	"context"
	"net/http"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/account"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/edit"
)

// entryPublish is who may set the state of an entry: publish it, take it back, archive it or schedule it. An author may do so
// to the entries that it made.
var entryPublish = docRule{account.PublishEntry, scopeCreator}

// publishingRoutes registers the routes that set the state of an entry. Each calls an operation of the Editor, so each is a
// route of heavy work.
func (s *Server) publishingRoutes() {
	s.apiHeavy("POST /api/entries/{id}/publish", s.publish)
	s.apiHeavy("POST /api/entries/{id}/unpublish", s.unpublish)
	s.apiHeavy("POST /api/entries/{id}/archive", s.archive)
	s.apiHeavy("POST /api/entries/{id}/schedule", s.schedule)
}

// stateChange is an operation of the Editor that sets the state of an entry, with what the body of the request gave it.
type stateChange func(ctx context.Context, op edit.Op, entry string) (edit.Result, error)

// stateReader reads the body of a request and returns the operation that it asks for. If it fails it has answered, and
// returns false.
type stateReader func(w http.ResponseWriter, r *http.Request) (stateChange, bool)

// changeState is the route of an operation that sets the state of an entry: the checks of every route that changes a
// document, the small body (64 KiB) before the request waits for the place of heavy work, and the operation in the place.
func (s *Server) changeState(w http.ResponseWriter, r *http.Request, p account.Principal, read stateReader) {
	c, ok := s.beginChange(w, r, p, &entryKind, entryPublish, nil)
	if !ok {
		return
	}
	do, ok := read(w, r)
	if !ok {
		return
	}
	s.heavyWork(w, r, p, 0, func(ctx context.Context, _, out *spool) (int, http.Header, error) {
		op := edit.Op{By: p.Account.ID, ID: c.opID, Check: entryPublish.check(p, c.id, c.named)}
		res, err := do(ctx, op, c.id)
		if err != nil {
			return 0, nil, err
		}
		return http.StatusOK, nil, writeResult(out, res)
	})
}

// isSeq reports whether a member base of a body, which is nil if the body has none, is the Seq of a version of a document.
func isSeq(base *int64) bool { return base != nil && *base >= 0 }

// isRevisionID reports whether a member revision of a body, which is nil if the body has none, is the ID of a revision.
func isRevisionID(revision *string) bool {
	return revision != nil && content.CheckID(content.RefRevision, *revision) == nil
}

// publish is POST /api/entries/{id}/publish: the body {"base":…,"revision":…} names the version of the entry and its
// last revision, which become the published one.
func (s *Server) publish(w http.ResponseWriter, r *http.Request, p account.Principal) {
	s.changeState(w, r, p, func(w http.ResponseWriter, r *http.Request) (stateChange, bool) {
		var in struct {
			Base     *int64  `json:"base"`
			Revision *string `json:"revision"`
		}
		if !readJSON(w, r, smallBodyLimit, &in) {
			return nil, false
		}
		if !isSeq(in.Base) || !isRevisionID(in.Revision) {
			writeProblem(w, http.StatusBadRequest, codeBadRequest, detailBadRequest)
			return nil, false
		}
		base, revision := *in.Base, *in.Revision
		return func(ctx context.Context, op edit.Op, entry string) (edit.Result, error) {
			return s.editor.Publish(ctx, op, entry, base, revision)
		}, true
	})
}

// unpublish is POST /api/entries/{id}/unpublish: the body {"base":…} names the version of the entry, which becomes a draft.
func (s *Server) unpublish(w http.ResponseWriter, r *http.Request, p account.Principal) {
	s.changeState(w, r, p, s.readRetire(func(ctx context.Context, op edit.Op, entry string, base int64) (edit.Result, error) {
		return s.editor.Unpublish(ctx, op, entry, base)
	}))
}

// archive is POST /api/entries/{id}/archive: the body {"base":…} names the version of the entry, which becomes archived.
func (s *Server) archive(w http.ResponseWriter, r *http.Request, p account.Principal) {
	s.changeState(w, r, p, s.readRetire(func(ctx context.Context, op edit.Op, entry string, base int64) (edit.Result, error) {
		return s.editor.Archive(ctx, op, entry, base)
	}))
}

// readRetire returns the reader of the body {"base":…} of the operations that take the entry back from the public.
func (s *Server) readRetire(retire func(ctx context.Context, op edit.Op, entry string, base int64) (edit.Result, error)) stateReader {
	return func(w http.ResponseWriter, r *http.Request) (stateChange, bool) {
		var in struct {
			Base *int64 `json:"base"`
		}
		if !readJSON(w, r, smallBodyLimit, &in) {
			return nil, false
		}
		if !isSeq(in.Base) {
			writeProblem(w, http.StatusBadRequest, codeBadRequest, detailBadRequest)
			return nil, false
		}
		base := *in.Base
		return func(ctx context.Context, op edit.Op, entry string) (edit.Result, error) {
			return retire(ctx, op, entry, base)
		}, true
	}
}

// schedule is POST /api/entries/{id}/schedule: the body {"base":…,"revision":…,"at":…} names the version of the entry,
// its last revision, and the time (RFC 3339) at which it is published.
func (s *Server) schedule(w http.ResponseWriter, r *http.Request, p account.Principal) {
	s.changeState(w, r, p, func(w http.ResponseWriter, r *http.Request) (stateChange, bool) {
		var in struct {
			Base     *int64  `json:"base"`
			Revision *string `json:"revision"`
			At       *string `json:"at"`
		}
		if !readJSON(w, r, smallBodyLimit, &in) {
			return nil, false
		}
		if !isSeq(in.Base) || !isRevisionID(in.Revision) || in.At == nil {
			writeProblem(w, http.StatusBadRequest, codeBadRequest, detailBadRequest)
			return nil, false
		}
		at, err := time.Parse(time.RFC3339, *in.At)
		if err != nil {
			writeProblem(w, http.StatusBadRequest, codeBadRequest, detailBadRequest)
			return nil, false
		}
		base, revision := *in.Base, *in.Revision
		return func(ctx context.Context, op edit.Op, entry string) (edit.Result, error) {
			return s.editor.Schedule(ctx, op, entry, base, revision, at)
		}, true
	})
}
