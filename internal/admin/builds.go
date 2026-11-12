package admin

import (
	"errors"
	"io/fs"
	"net/http"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/account"
	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/publish"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

const (
	// maxRequests is the most requests of a build that an answer lists; the others are counted.
	maxRequests = 100
	// reasonOperation is the reason of a request that an operation of the Editor made, whose result the request has.
	reasonOperation = "operation"

	// The sentences that an answer gives for a build or a scheduled publish that failed for a reason other than the
	// problems of the site: the error itself may name paths of the server, and stays in the activity log (7.4).
	detailBuildFailed    = "the build failed for a reason other than the content of the site"
	detailScheduleFailed = "the scheduled publish failed for a reason other than the content of the site"
)

// buildRoutes registers the routes of the builds and the releases. None of them reads the documents of the store or counts among
// the heavy requests of the account: the Publisher runs the builds one at a time, and a rollback waits for the build that runs,
// which it must not do in the place of heavy work.
func (s *Server) buildRoutes() {
	s.api("GET /api/builds", s.showBuilds)
	s.api("POST /api/builds", s.rebuild)
	s.api("POST /api/builds/resume", s.resumeBuilds)
	s.api("GET /api/releases", s.listReleases)
	s.api("POST /api/releases/{name}/rollback", s.rollbackRelease)
}

// buildRequestAnswer is a request that a build covers. The kind, the ID and the status are those of the operation that asked.
type buildRequestAnswer struct {
	At     time.Time       `json:"at"`
	Reason string          `json:"reason"`
	Kind   content.RefKind `json:"kind,omitempty"`
	ID     string          `json:"id,omitempty"`
	Status content.Status  `json:"status,omitempty"`
}

// buildAnswer is a build that runs or has ended. A build that runs has the start and the requests only. A build that has ended
// tells when, and, if it published a release, which one with the numbers of its result, and whether it failed and why: the
// problems of the site if it was that, and else a fixed sentence.
type buildAnswer struct {
	StartedAt       time.Time            `json:"startedAt"`
	EndedAt         *time.Time           `json:"endedAt,omitempty"`
	Requests        []buildRequestAnswer `json:"requests"`
	OmittedRequests int                  `json:"omittedRequests,omitzero"`
	Release         string               `json:"release,omitempty"`
	Changed         *bool                `json:"changed,omitempty"`
	Pages           *int                 `json:"pages,omitempty"`
	Files           *int                 `json:"files,omitempty"`
	Warnings        []problemError       `json:"warnings,omitempty"`
	OmittedWarnings int                  `json:"omittedWarnings,omitzero"`
	Failed          *bool                `json:"failed,omitempty"`
	Errors          []problemError       `json:"errors,omitempty"`
	OmittedErrors   int                  `json:"omittedErrors,omitzero"`
	Error           string               `json:"error,omitempty"`
}

// scheduleFailureAnswer is a scheduled publish that failed at the last step of the scheduler.
type scheduleFailureAnswer struct {
	Entry         string         `json:"entry,omitempty"`
	At            time.Time      `json:"at,omitzero"`
	Tried         time.Time      `json:"tried"`
	Errors        []problemError `json:"errors,omitempty"`
	OmittedErrors int            `json:"omittedErrors,omitzero"`
	Error         string         `json:"error,omitempty"`
}

// buildsAnswer is the state of the builds of the Publisher.
type buildsAnswer struct {
	Running   *buildAnswer            `json:"running,omitempty"`
	Pending   bool                    `json:"pending"`
	Paused    bool                    `json:"paused"`
	Last      *buildAnswer            `json:"last,omitempty"`
	Schedules []scheduleFailureAnswer `json:"schedules"`
}

// releasesAnswer is the releases in the output directory and the one that is published. Without a release the name is ""
// and the list is empty ([], as JSON writes a nil list).
type releasesAnswer struct {
	Current string   `json:"current"`
	Names   []string `json:"names"`
}

// buildsOf returns the answer for st, the state of the builds.
func buildsOf(st publish.Status) buildsAnswer {
	a := buildsAnswer{Pending: st.Pending, Paused: st.Paused, Schedules: make([]scheduleFailureAnswer, 0, len(st.Schedules))}
	if b := st.Running; b != nil {
		a.Running = buildOf(b, false)
	}
	if b := st.Last; b != nil {
		a.Last = buildOf(b, true)
	}
	for _, f := range st.Schedules {
		sf := scheduleFailureAnswer{Entry: f.Entry, At: f.At.UTC(), Tried: f.Tried.UTC()}
		sf.Errors, sf.OmittedErrors, sf.Error = failureOf(f.Err, detailScheduleFailed)
		a.Schedules = append(a.Schedules, sf)
	}
	return a
}

// buildOf returns the answer for b, which has ended if ended is true.
func buildOf(b *publish.Build, ended bool) *buildAnswer {
	n := min(len(b.Requests), maxRequests)
	a := &buildAnswer{StartedAt: b.Started.UTC(), Requests: make([]buildRequestAnswer, 0, n), OmittedRequests: len(b.Requests) - n}
	for _, q := range b.Requests[:n] {
		item := buildRequestAnswer{At: q.At.UTC(), Reason: q.Reason}
		if q.Reason == reasonOperation {
			item.Kind, item.ID, item.Status = q.Result.Kind, q.Result.ID, q.Result.Status
		}
		a.Requests = append(a.Requests, item)
	}
	if !ended {
		return a
	}
	endedAt := b.Ended.UTC()
	a.EndedAt = &endedAt
	if res := b.Result; res != nil {
		a.Release, a.Changed, a.Pages, a.Files = res.Release, &res.Changed, &res.Pages, &res.Files
		a.Warnings, a.OmittedWarnings = problemList(res.Warnings)
	}
	failed := b.Err != nil
	a.Failed = &failed
	if failed {
		a.Errors, a.OmittedErrors, a.Error = failureOf(b.Err, detailBuildFailed)
	}
	return a
}

// failureOf tells why err stopped a build or a scheduled publish: the problems of the site if that is what it is, and else
// the sentence other.
func failureOf(err error, other string) (list []problemError, omitted int, sentence string) {
	var es content.Errors
	if errors.As(err, &es) && len(es) > 0 {
		list, omitted = problemList(es)
		return list, omitted, ""
	}
	return nil, 0, other
}

// showBuilds is GET /api/builds: the state of the builds, which the Publisher keeps in memory only.
func (s *Server) showBuilds(w http.ResponseWriter, r *http.Request, p account.Principal) {
	if !s.allowView(w, r, p) || !noOperationID(w, r) {
		return
	}
	if _, ok := readQuery(w, r, nil); !ok {
		return
	}
	s.writeBuilds(w, http.StatusOK)
}

func (s *Server) writeBuilds(w http.ResponseWriter, status int) {
	writeJSON(w, status, buildsOf(s.publisher.Status()))
}

// allowBuilds is allow for the routes that change the builds or the releases: named is what a refusal names.
func (s *Server) allowBuilds(w http.ResponseWriter, r *http.Request, p account.Principal, named audit.Target) bool {
	return s.allow(w, r, p, account.ManageBuilds, account.Target{}, named) && noOperationID(w, r)
}

// inUpdateBuilds is the check of the permission that the Publisher calls in the Update of its record of a request.
func inUpdateBuilds(p account.Principal, named audit.Target) func(tx *store.Tx) error {
	return inUpdate(p, named, nil, account.ManageBuilds)
}

// rebuild is POST /api/builds: it asks for a build of the store as it is, 202 with the state of the builds.
func (s *Server) rebuild(w http.ResponseWriter, r *http.Request, p account.Principal) {
	if !s.allowBuilds(w, r, p, audit.Target{}) {
		return
	}
	if _, ok := readQuery(w, r, nil); !ok {
		return
	}
	s.at("checked")
	if err := s.publisher.Rebuild(r.Context(), p.Account.ID, inUpdateBuilds(p, audit.Target{})); err != nil {
		s.answerError(w, r, p, err, attempt{})
		return
	}
	s.writeBuilds(w, http.StatusAccepted)
}

// resumeBuilds is POST /api/builds/resume: it ends the pause of a rollback, and asks for a build.
func (s *Server) resumeBuilds(w http.ResponseWriter, r *http.Request, p account.Principal) {
	if !s.allowBuilds(w, r, p, audit.Target{}) {
		return
	}
	if _, ok := readQuery(w, r, nil); !ok {
		return
	}
	s.at("checked")
	if err := s.publisher.Resume(r.Context(), p.Account.ID, inUpdateBuilds(p, audit.Target{})); err != nil {
		s.answerError(w, r, p, err, attempt{})
		return
	}
	s.writeBuilds(w, http.StatusOK)
}

// listReleases is GET /api/releases: the releases in the output directory, and the one that is published. Before the first
// build has made the directory there are none.
func (s *Server) listReleases(w http.ResponseWriter, r *http.Request, p account.Principal) {
	if !s.allowView(w, r, p) || !noOperationID(w, r) {
		return
	}
	if _, ok := readQuery(w, r, nil); !ok {
		return
	}
	releases, err := s.publisher.Releases()
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, releasesAnswer{Current: releases.Current, Names: releases.Names})
}

// isReleaseName reports whether name has the form of the name of a release that a build makes: 16 lower case hexadecimal digits.
func isReleaseName(name string) bool {
	if len(name) != 16 {
		return false
	}
	for i := range len(name) {
		if c := name[i]; c < '0' || c > '9' && c < 'a' || c > 'f' {
			return false
		}
	}
	return true
}

// rollbackRelease is POST /api/releases/{name}/rollback: it publishes the release instead of the one that is published, and
// pauses the builds until they are resumed, 200 with the state of the builds. It waits for the build that runs.
func (s *Server) rollbackRelease(w http.ResponseWriter, r *http.Request, p account.Principal) {
	name := r.PathValue("name")
	named := audit.Target{Kind: audit.KindRelease, ID: name}
	if !s.allowBuilds(w, r, p, named) {
		return
	}
	if _, ok := readQuery(w, r, nil); !ok {
		return
	}
	if !isReleaseName(name) {
		writeProblem(w, http.StatusNotFound, codeNotFound, detailNotFound)
		return
	}
	s.at("checked")
	if err := s.publisher.Rollback(r.Context(), p.Account.ID, name, inUpdateBuilds(p, named)); err != nil {
		s.answerError(w, r, p, err, attempt{})
		return
	}
	s.writeBuilds(w, http.StatusOK)
}
