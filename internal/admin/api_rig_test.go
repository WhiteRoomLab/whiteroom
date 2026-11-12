package admin

import (
	"context"
	"encoding/json/v2"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/account"
	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/edit"
	"github.com/WhiteRoomLab/whiteroom/internal/publish"
	"github.com/WhiteRoomLab/whiteroom/internal/sitedir"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

// The tests of the admin API use the sample site of internal/build, imported into a store, with its example theme. The
// accounts of newAPIServer are admin, editor, author and viewer, with the role of their names.
const (
	sampleDir = "../build/testdata/site"    // the sample site of internal/build, a site directory
	themeDir  = "../theme/testdata/example" // its theme, which the site does not copy
	// importer imported the sample site, so that it made its documents; it has no account.
	importer = "usr_01JB8T7H4MA2C5E8G1J4K7N3P6"
)

// sampleSite is a site directory with the example theme, which the directory does not copy.
type sampleSite struct {
	*sitedir.Dir
	theme fs.FS
}

func (s sampleSite) Theme() (fs.FS, error) { return s.theme, nil }

// exampleTheme returns the files of the example theme, which the test closes.
func exampleTheme(t *testing.T) fs.FS {
	t.Helper()
	root, err := os.OpenRoot(themeDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root.FS()
}

func sampleSchema(t *testing.T) content.SiteFile {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(sampleDir, "site", "schema.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return content.SiteFile{Name: "site/schema.yaml", Data: data}
}

// importSample imports the sample site into the empty store st.
func importSample(t *testing.T, st *store.Store) {
	t.Helper()
	d, err := sitedir.Open(sampleDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if _, err := edit.Import(t.Context(), st, sampleSite{Dir: d, theme: exampleTheme(t)}, importer); err != nil {
		t.Fatalf("Import() error = %v", err)
	}
}

// newTestPublisher returns a Publisher of st for the sample site, with the clock of ts, that has not started to run. Its
// output is in a temporary directory, which is out.
func newTestPublisher(t *testing.T, ts *testServer, st *store.Store) (p *publish.Publisher, out string) {
	t.Helper()
	return newTestPublisherWith(t, ts, st, exampleTheme(t))
}

// newTestPublisherWith is newTestPublisher for the theme th.
func newTestPublisherWith(t *testing.T, ts *testServer, st *store.Store, th fs.FS) (p *publish.Publisher, out string) {
	t.Helper()
	out = t.TempDir()
	p, err := publish.New(publish.Options{
		Store: st, Schema: sampleSchema(t), Theme: th, Out: out, CacheDir: t.TempDir(), Workers: 2,
		Now: ts.clock.now,
		OnRecordError: func(a audit.Action, err error) {
			_, _ = ts.logs.Write([]byte("cannot record " + string(a) + ": " + err.Error() + "\n"))
		},
	})
	if err != nil {
		t.Fatalf("publish.New() error = %v", err)
	}
	return p, out
}

// apiServer is a test server with a Publisher of the sample site, which runs until the end of the test, and the accounts
// admin, editor, author and viewer, which are logged in.
type apiServer struct {
	*testServer
	pub    *publish.Publisher
	out    string     // the output directory of the Publisher
	builds *buildGate // holds the builds of pub; nil for a server whose Publisher does not run
	people map[string]*person
}

// person is a logged in account of an apiServer.
type person struct {
	account account.Account
	token   string
}

// apiLogins are the accounts that newAPIServer makes.
var apiLogins = []struct {
	login string
	role  account.Role
}{
	{"admin", account.RoleAdmin}, {"editor", account.RoleEditor}, {"author", account.RoleAuthor}, {"viewer", account.RoleViewer},
}

// newAPIServer returns a server for https://admin.example.com whose store has the imported sample site, with the accounts
// of apiLogins. The clock of the accounts, the Publisher and the Server is the fake one of the test server. It takes a few
// seconds, for the import and the build that the Publisher makes at its start: a test uses one for as much as it can.
func newAPIServer(t *testing.T) *apiServer {
	t.Helper()
	return newAPIServerAt(t, httpsOrigin)
}

// newAPIServerAt is newAPIServer for the server of origin.
func newAPIServerAt(t *testing.T, origin string) *apiServer {
	t.Helper()
	ts := newTestBase()
	db, dir := testStore(t)
	importSample(t, db)
	m := testManager(t, db)
	gate := newBuildGate()
	p, out := newTestPublisherWith(t, ts, db, gatedFS{FS: exampleTheme(t), gate: gate})
	ts.serve(t, Config{Origin: origin, Accounts: m, Publisher: p}, db, dir)
	a := &apiServer{testServer: ts, pub: p, out: out, builds: gate, people: make(map[string]*person)}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run() error = %v", err)
		}
	})
	t.Cleanup(gate.letGo) // before Run is stopped, which waits for the build that is held

	for _, l := range apiLogins {
		acct := ts.addAccount(t, l.login, l.role)
		a.people[l.login] = &person{account: acct, token: ts.logIn(t, l.login)}
	}
	return a
}

// newLiveAPIServer is newAPIServer for a server that listens on a loopback port over HTTP, whose requests the test sends
// over a connection, such as the slow ones of liveServer. The live server is the one of the API server.
func newLiveAPIServer(t *testing.T) (*apiServer, *liveServer) {
	t.Helper()
	srv := httptest.NewUnstartedServer(nil)
	url := "http://" + srv.Listener.Addr().String()
	a := newAPIServerAt(t, url)
	srv.Config.Handler = a.Server
	srv.Start()
	t.Cleanup(srv.Close)
	return a, &liveServer{testServer: a.testServer, srv: srv, url: url}
}

// waitBuilds returns when the Publisher has no build that runs or is due.
func (a *apiServer) waitBuilds(t *testing.T) {
	t.Helper()
	if err := a.pub.Wait(t.Context()); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
}

// principal returns who login is as the Server takes it, from its session.
func (a *apiServer) principal(t *testing.T, login string) account.Principal {
	t.Helper()
	p, err := a.accounts.Authenticate(t.Context(), account.Client{}, a.people[login].token)
	if err != nil {
		t.Fatalf("Authenticate(%q) error = %v", login, err)
	}
	return p
}

// as returns a sender of requests as the logged in account login.
func (a *apiServer) as(login string) asPerson {
	p, ok := a.people[login]
	if !ok {
		panic("no such person " + login)
	}
	return asPerson{a: a, token: p.token}
}

// asPerson sends the requests of a logged in account: with its cookie, the origin of the server, and, for a body, JSON.
type asPerson struct {
	a     *apiServer
	token string
}

// send is ts.send with the cookie and the origin, and Content-Type: application/json for a body, unless headers has one.
func (p asPerson) send(method, path, body string, headers ...string) *http.Response {
	all := append(p.a.cookie(p.token), "Origin", p.a.origin)
	if body != "" && !hasHeader(headers, "Content-Type") {
		all = append(all, "Content-Type", "application/json")
	}
	return p.a.send(method, path, body, append(all, headers...)...)
}

func (p asPerson) get(path string) *http.Response { return p.send(http.MethodGet, path, "") }

func (p asPerson) post(path, body string, headers ...string) *http.Response {
	return p.send(http.MethodPost, path, body, headers...)
}

func hasHeader(headers []string, name string) bool {
	for i := 0; i < len(headers); i += 2 {
		if strings.EqualFold(headers[i], name) {
			return true
		}
	}
	return false
}

// decodeAnswer reads the body of res, which must be JSON, into a T.
func decodeAnswer[T any](t *testing.T, res *http.Response) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(bodyOf(t, res)), &v); err != nil {
		t.Fatalf("the answer (status %d) is not the JSON that was expected: %v", res.StatusCode, err)
	}
	return v
}

// records returns the records of the activity log whose action is the one, or all of them if it is "".
func (ts *testServer) records(t *testing.T, action audit.Action) []audit.Record {
	t.Helper()
	var out []audit.Record
	f := audit.Filter{Log: audit.ActivityLog, Action: string(action)}
	err := ts.db.ReadAudit(t.Context(), f, func(r audit.Record) bool {
		out = append(out, r)
		return true
	})
	if err != nil {
		t.Fatalf("ReadAudit() error = %v", err)
	}
	return out
}

// tempFiles returns the names of the temporary files in files/ of the data directory.
func (ts *testServer) tempFiles(t *testing.T) []string {
	t.Helper()
	return tempFilesIn(t, ts.dir)
}

// tempFilesIn returns the names of the temporary files in files/ of the data directory dir.
func tempFilesIn(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dir, "files"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			names = append(names, e.Name())
		}
	}
	return names
}
