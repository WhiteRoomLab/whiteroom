package admin

import (
	"net/http"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/account"
	"github.com/WhiteRoomLab/whiteroom/internal/audit"
)

// buildWrites returns a request of each route that changes the builds or the releases, with the permission that it needs and
// the target that a refusal names: a rebuild, a resuming, a rollback, and a rollback to a name that is no release.
func (a *apiServer) buildWrites(t *testing.T) []write {
	t.Helper()
	name := readReleases(t, a.as("admin")).Current
	return []write{
		{"rebuild", http.MethodPost, "/api/builds", "", account.ManageBuilds, audit.Target{}},
		{"resume", http.MethodPost, "/api/builds/resume", "", account.ManageBuilds, audit.Target{}},
		{"rollback", http.MethodPost, "/api/releases/" + name + "/rollback", "", account.ManageBuilds, audit.Target{Kind: audit.KindRelease, ID: name}},
		{"rollback to a name that is no release", http.MethodPost, "/api/releases/xyz/rollback", "", account.ManageBuilds, audit.Target{}},
	}
}

var buildOperations = []string{"rebuild", "resume", "rollback"}

// noBuildRequests checks that nothing asked for a build, paused the builds, or was recorded as an operation of the builds.
func (a *apiServer) noBuildRequests(t *testing.T, builds int) {
	t.Helper()
	a.waitBuilds(t)
	st := readBuilds(t, a.as("admin"))
	if !st.idle() || st.Paused {
		t.Errorf("the state is %+v, want nothing wanted, no build, and no pause", st)
	}
	if got := a.buildCount(t); got != builds {
		t.Errorf("%d builds ended, want none", got-builds)
	}
	for _, action := range []audit.Action{audit.BuildRebuild, audit.BuildResume, audit.ReleaseRollback} {
		if got := a.records(t, action); len(got) != 0 {
			t.Errorf("%d records of %s: %+v, want none", len(got), action, got)
		}
	}
}

// TestBuildsRoles shows that only an administrator changes the builds and the releases, and every role reads them: the refusal
// comes before the checks of the header, the query and the name, and is recorded with the permission and the release.
func TestBuildsRoles(t *testing.T) {
	a := newAPIServer(t)
	a.waitBuilds(t)
	builds := a.buildCount(t)
	for _, login := range []string{"viewer", "author", "editor"} {
		for _, w := range a.buildWrites(t) {
			a.wantForbidden(t, login, w)
		}
	}
	for _, w := range a.buildWrites(t) {
		a.clock.advance(time.Minute)
		checkProblem(t, w.name+" with a key, from a viewer", a.as("viewer").send(w.method, w.path+"?x=1", w.body, key(1)...), "forbidden")
	}
	if got, want := len(a.records(t, audit.AccessDenied)), 3*4+4; got != want {
		t.Errorf("%d records of access.denied, want %d", got, want)
	}
	a.noBuildRequests(t, builds)

	for _, l := range apiLogins {
		for _, path := range []string{"/api/builds", "/api/releases"} {
			body(t, "GET "+path+" as "+l.login, a.as(l.login).get(path), http.StatusOK)
		}
	}
	if got := len(a.records(t, audit.AccessDenied)); got != 3*4+4 {
		t.Errorf("reading made records of access.denied: %d, want 16", got)
	}
	if logged := a.logs.String(); logged != "" {
		t.Errorf("the log is %q, want nothing", logged)
	}
}

// TestBuildsRoleChangesInUpdate shows that the permission is checked again in the Update that records the request, with the role
// that the store has then: an administrator who is made an editor while the request comes is refused, and nothing is recorded
// or done.
func TestBuildsRoleChangesInUpdate(t *testing.T) {
	a := newAPIServer(t)
	a.waitBuilds(t)
	builds := a.buildCount(t)
	for i, name := range buildOperations {
		t.Run(name, func(t *testing.T) {
			login := "second" + strconv.Itoa(i)
			victim := a.victim(t, login, account.RoleAdmin) // a second administrator, so that the role of the first is not the last one's
			w := a.buildWrites(t)[i]

			var fired atomic.Int32
			a.hook = func(point string) {
				if point == "checked" && fired.Add(1) == 1 {
					down := account.RoleEditor
					if _, err := a.accounts.Update(t.Context(), account.System, victim, account.Change{Role: &down}); err != nil {
						t.Errorf("Update() error = %v", err)
					}
				}
			}
			a.clock.advance(time.Minute)
			n := len(a.records(t, audit.AccessDenied))
			checkProblem(t, w.name+" by an administrator who is an editor by then", a.as(login).send(w.method, w.path, w.body), "forbidden")
			a.hook = nil
			if fired.Load() != 1 {
				t.Fatalf("the hook ran %d times, want 1", fired.Load())
			}
			records := a.records(t, audit.AccessDenied)
			if len(records) != n+1 {
				t.Fatalf("the refusal made %d records of access.denied, want 1", len(records)-n)
			}
			if r := records[n]; r.Actor != victim || r.Target != w.target || r.Detail["permission"] != string(account.ManageBuilds) {
				t.Errorf("the record is %+v, want the actor %s, the permission builds.manage and the target %+v", r, victim, w.target)
			}
			a.noBuildRequests(t, builds)
			if logged := a.logs.String(); logged != "" {
				t.Errorf("the log is %q, want nothing", logged)
			}
		})
	}
}

// TestBuildsCredentialsChangeInUpdate shows that a request whose account has other credentials when the request is recorded than
// when it came is answered as one without a session: 401, the cookie cleared, and nothing recorded or done.
func TestBuildsCredentialsChangeInUpdate(t *testing.T) {
	a := newAPIServer(t)
	a.waitBuilds(t)
	builds := a.buildCount(t)
	for i, name := range buildOperations {
		t.Run(name, func(t *testing.T) {
			login := "reset" + strconv.Itoa(i)
			victim := a.victim(t, login, account.RoleAdmin)
			w := a.buildWrites(t)[i]

			var fired atomic.Int32
			a.hook = func(point string) {
				if point == "checked" && fired.Add(1) == 1 {
					a.clock.advance(time.Minute) // the time of the credentials is that of the clock
					token, err := a.accounts.Reset(t.Context(), account.System, victim)
					if err != nil {
						t.Errorf("Reset() error = %v", err)
					}
					a.seen.token(token)
				}
			}
			n := len(a.records(t, audit.AccessDenied))
			res := a.as(login).send(w.method, w.path, w.body)
			checkProblem(t, w.name+" by an account whose credentials change", res, "unauthenticated")
			if got := res.Header.Values("Set-Cookie"); !equalValues(got, a.clearLine()) {
				t.Errorf("the answer sets the cookies %q, want the one that clears the session", got)
			}
			a.hook = nil
			if fired.Load() != 1 {
				t.Fatalf("the hook ran %d times, want 1", fired.Load())
			}
			if got := len(a.records(t, audit.AccessDenied)); got != n {
				t.Errorf("the request made %d records of access.denied, want none: it is not a refusal of a permission", got-n)
			}
			a.noBuildRequests(t, builds)
		})
	}
}
