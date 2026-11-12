package admin

import (
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/account"
	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

// TestSecretsNotShown runs the flows of the session endpoints in which a password or a token is in the request, the ones that
// work and the ones that are refused for every reason, and then a store that fails. No answer has a password, no answer but
// the Set-Cookie of a login has a token, and the log has neither.
func TestSecretsNotShown(t *testing.T) {
	ts := newTestServer(t, httpsOrigin, "192.0.2.0/24")
	admin := ts.addAccount(t, "admin", account.RoleAdmin)
	ts.addAccount(t, "editor", account.RoleEditor)
	ts.inviteAccount(t, "invited", account.RoleViewer)
	left := ts.addAccount(t, "left", account.RoleViewer)
	if _, err := ts.accounts.Disable(t.Context(), account.By(admin.ID), left.ID); err != nil {
		t.Fatal(err)
	}
	const typed = "a password that the editor typed in a body that is refused"
	ts.seen.password(typed)
	pw := testPassword("editor")

	// What works.
	token := ts.logIn(t, "editor")
	ts.getSession(ts.cookie(token)...)
	ts.send(http.MethodHead, "/api/session", "", ts.cookie(token)...)
	again := ts.logIn(t, "editor", ts.cookie(token)...)
	ts.getSession(ts.cookie(token)...)
	ts.deleteSession(ts.cookie(again)...)
	ts.getSession(ts.cookie(again)...)

	// Logins that fail.
	for _, tt := range []struct{ login, password string }{
		{"nobody", testPassword("nobody")}, {"editor", typed}, {"invited", testPassword("invited")}, {"left", testPassword("left")},
		{"editor", strings.Repeat("p", 9000)}, {"", ""},
	} {
		ts.postLogin(tt.login, tt.password, ts.cookie(token)...)
	}
	for range 6 {
		ts.postLogin("editor", typed, viaProxy("203.0.113.5")...)
	}
	ts.postLogin("editor", pw, viaProxy("203.0.113.5")...)

	// Bodies that are refused, with a password in them and a session in the cookie.
	for _, body := range []string{
		`{"login":"editor","password":"` + typed + `","extra":1}`, `{"login":"editor","password":["` + typed + `"]}`,
		`{"login":"editor","password":"` + typed + `","password":"` + typed + `"}`, `{"login":"editor","password":"` + typed,
		`{"login":null,"password":"` + typed + `"}`, `"` + typed + `"`,
	} {
		ts.sendJSON("/api/session", "application/json", body)
		ts.send(http.MethodPost, "/api/session", body, "Origin", ts.origin, "Content-Type", "application/json", "Cookie", ts.wantCookieName()+"="+token)
	}
	ts.sendJSON("/api/session", "text/plain", loginBody("editor", typed))
	ts.sendJSON("/api/session", "application/json", loginBody("editor", typed)+strings.Repeat(" ", 70000))
	ts.send(http.MethodPost, "/api/session", loginBody("editor", typed), "Content-Type", "application/json", "Cookie", ts.wantCookieName()+"="+token)
	ts.send(http.MethodDelete, "/api/session", "", "Sec-Fetch-Site", "cross-site", "Cookie", ts.wantCookieName()+"="+token)
	ts.send(http.MethodGet, "/api/session", "", "Cookie", ts.wantCookieName()+"="+strings.Repeat("Z", 43))
	ts.send(http.MethodGet, "/api/session", "", "Cookie", ts.wantCookieName()+"="+token+"; "+ts.wantCookieName()+"="+token)
	ts.send(http.MethodGet, "/api/session", "", "Cookie", ts.wantCookieName()+"="+token+"x")
	ts.send(http.MethodGet, "/api/session?password="+url.QueryEscape(typed)+"&token="+token, "", ts.cookie(token)...)

	// A session that has expired, a request that is not answered in time, and a store that fails.
	stale := ts.logIn(t, "editor")
	ts.clock.advance(2 * time.Hour)
	ts.getSession(ts.cookie(stale)...)
	fresh := ts.logIn(t, "editor")
	ts.deadline = time.Nanosecond
	ts.postLogin("editor", pw)
	ts.getSession(ts.cookie(fresh)...)
	ts.deleteSession(ts.cookie(fresh)...)
	ts.deadline = defaultDeadline

	// The audit log shows none of the secrets either: it names a session by the key of its row.
	var history strings.Builder
	err := ts.db.ReadAudit(t.Context(), audit.Filter{}, func(r audit.Record) bool {
		data, err := audit.Encode(r)
		if err != nil {
			t.Errorf("a record of %s cannot be encoded: %v", r.Action, err)
		}
		history.Write(data)
		history.WriteByte('\n')
		return true
	})
	if err != nil {
		t.Fatalf("ReadAudit() = %v", err)
	}
	if got := strings.Count(history.String(), `"action":"login.failure"`); got < 10 {
		t.Errorf("the audit log has %d records of failed logins, want at least 10: the flows did not reach it", got)
	}
	for _, p := range ts.seen.problems(history.String()) {
		t.Errorf("the audit log: %s", p)
	}

	if err := ts.db.Close(); err != nil {
		t.Fatal(err)
	}
	ts.postLogin("editor", pw, ts.cookie(fresh)...)
	ts.getSession(ts.cookie(fresh)...)
	ts.deleteSession(ts.cookie(fresh)...)

	passwords, tokens, answers := ts.seen.watching()
	if passwords < 5 || tokens < 5 || answers < 40 {
		t.Errorf("the test watched for %d passwords and %d tokens in %d answers, want at least 5, 5 and 40", passwords, tokens, answers)
	}
	if got := strings.Count(ts.logs.String(), "\n"); got < 3 {
		t.Errorf("the failures have logged %d lines, want at least 3: the flows did not reach the log", got)
	}
	ts.seen.check(t, ts.logs.String())
}

// TestSecretWatch shows that secretWatch finds a secret where it must not be, and says where, and never says the secret.
func TestSecretWatch(t *testing.T) {
	const password = "hunter2-hunter2"
	token := strings.Repeat("T", 43)
	w := newSecretWatch()
	w.password(password)
	w.password("short")
	w.token(token)
	w.token("short")
	if p, tk, a := w.watching(); p != 1 || tk != 1 || a != 0 {
		t.Errorf("watching() = %d, %d, %d, want 1, 1, 0: a text that is shorter than a secret is not watched", p, tk, a)
	}
	w.record("GET /fine", http.StatusOK, http.Header{"Content-Type": {"application/json"}}, []byte(`{"a":"short"}`))
	w.record("POST /login", http.StatusOK, http.Header{"Set-Cookie": {"session=" + token + "; Path=/"}}, []byte(`{}`))
	if got := w.problems("a line"); len(got) != 0 {
		t.Errorf("problems() = %q for answers that show no secret, want none", got)
	}
	w.watchRequest(http.Header{"Cookie": {"session=" + strings.Repeat("U", 43) + "; a=b"}})
	if _, tk, _ := w.watching(); tk != 2 {
		t.Errorf("watching() has %d tokens after a request with a cookie of the length of a token, want 2", tk)
	}
	w.record("GET /echo", http.StatusBadRequest, http.Header{"X-Echo": {token}}, nil)
	w.record("GET /body", http.StatusBadRequest, http.Header{}, []byte("the password is "+password))
	w.record("GET /cookie", http.StatusOK, http.Header{"Set-Cookie": {"other=" + token}}, []byte(token))
	want := []string{
		"the answer to GET /body has a password of the test",
		"the answer to GET /cookie has a token of the test",
		"the answer to GET /echo has a token of the test",
		"the log has a password of the test",
		"the log has a token of the test",
	}
	got := w.problems("the log: " + password + " and " + token)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("problems() = %q, want %q", got, want)
	}
	for _, line := range got {
		if strings.Contains(line, password) || strings.Contains(line, token) {
			t.Errorf("the problem %q shows the secret", line)
		}
	}
}

// TestSecretWatchHandedToken shows that the token in the body of the answer to an invitation or a reset is the one exception: it
// is watched for in every other answer and in the log, and left out of the answer that gives it.
func TestSecretWatchHandedToken(t *testing.T) {
	token := strings.Repeat("I", 43)
	body := []byte(`{"account":{"id":"usr_x"},"token":"` + token + `","expiresAt":"2026-11-19T12:00:00Z"}`)
	for _, tt := range []struct {
		what   string
		status int
		handed bool
	}{
		{"POST /api/accounts", http.StatusCreated, true},
		{"POST /api/accounts/usr_x/reset", http.StatusOK, true},
		{"POST /api/accounts", http.StatusUnprocessableEntity, false},
		{"POST /api/accounts/usr_x/reset", http.StatusConflict, false},
		{"POST /api/accounts/usr_x/disable", http.StatusOK, false},
		{"GET /api/accounts/usr_x", http.StatusOK, false},
		{"PATCH /api/accounts/usr_x", http.StatusOK, false},
		{"POST /api/other", http.StatusOK, false},
	} {
		w := newSecretWatch()
		w.record(tt.what, tt.status, http.Header{}, body)
		wantTokens := 0
		if tt.handed {
			wantTokens = 1
		}
		if _, tokens, answers := w.watching(); tokens != wantTokens || answers != 1 {
			t.Errorf("%s with %d: the watch has %d tokens and %d answers after it, want %d and 1", tt.what, tt.status, tokens, answers, wantTokens)
		}
		if got := w.problems("a line"); len(got) != 0 {
			t.Errorf("%s with %d: problems() = %q, want none: the answer that gives a token does not show it", tt.what, tt.status, got)
		}
	}

	w := newSecretWatch()
	w.record("POST /api/accounts", http.StatusCreated, http.Header{}, body)
	w.record("GET /api/accounts/usr_x", http.StatusOK, http.Header{}, []byte(`{"token":"`+token+`"}`))
	w.record("POST /api/accounts/usr_x/disable", http.StatusOK, http.Header{"Location": {"/x?token=" + token}}, []byte(`{}`))
	w.record("POST /api/accounts/usr_x/reset", http.StatusOK, http.Header{}, []byte(`{"token":"`+token+`"}`))
	want := []string{
		"the answer to GET /api/accounts/usr_x has a token of the test",
		"the answer to POST /api/accounts/usr_x/disable has a token of the test",
		"the log has a token of the test",
	}
	got := w.problems("the log: " + token)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("problems() = %q, want %q: the token of a reset that is given again is no more shown than the first", got, want)
	}
	for _, line := range got {
		if strings.Contains(line, token) {
			t.Errorf("the problem %q shows the secret", line)
		}
	}
}

func TestAnswerFor(t *testing.T) {
	at := time.Date(2026, time.November, 9, 12, 0, 0, 500000000, time.UTC)
	p := account.Principal{
		Account: account.Account{
			ID: "usr_01JB8T7H4MA2C5E8G1J4K7N3P7", Login: "writer", Name: "A Writer", Email: "writer@example.com", Role: account.RoleAuthor,
			Author: "aut_01JB8Y6T2RG7M4XK9P2D5W8NQ3", Status: account.StatusActive,
		},
		Session: account.Session{AuthenticatedAt: at, ExpiresAt: at.Add(12 * time.Hour), Factors: []account.Factor{account.FactorPassword}},
	}
	got, err := json.Marshal(answerFor(p))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"account":{"id":"usr_01JB8T7H4MA2C5E8G1J4K7N3P7","login":"writer","name":"A Writer","email":"writer@example.com",` +
		`"role":"author","author":"aut_01JB8Y6T2RG7M4XK9P2D5W8NQ3"},"session":{"authenticatedAt":"2026-11-09T12:00:00.5Z",` +
		`"expiresAt":"2026-11-10T00:00:00.5Z","idleTimeout":3600,"factors":["password"]}}`
	if string(got) != want {
		t.Errorf("answerFor() = %s\nwant %s", got, want)
	}
	p.Account.Author = ""
	got, err = json.Marshal(answerFor(p))
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.Replace(want, `,"author":"aut_01JB8Y6T2RG7M4XK9P2D5W8NQ3"`, "", 1); string(got) != want {
		t.Errorf("answerFor() without an author profile = %s\nwant %s", got, want)
	}
}

// TestLoginAuthor shows the link of an account to its author profile in the answer.
func TestLoginAuthor(t *testing.T) {
	const authorID = "aut_01JB8Y6T2RG7M4XK9P2D5W8NQ3"
	ts := newTestServer(t, httpsOrigin)
	err := ts.db.Update(t.Context(), storeWrite(func(tx *store.Tx) error {
		data := `{"schemaVersion": 1, "id": "` + authorID + `", "slug": "writer"}`
		_, err := tx.Put(content.RefAuthor, authorID, []byte(data), "usr_01JB8T7H4MA2C5E8G1J4K7N3P7")
		return err
	}))
	if err != nil {
		t.Fatal(err)
	}
	in := account.Input{Login: "writer", Name: "Name of writer", Email: "writer@example.com", Role: account.RoleAuthor, Author: authorID}
	_, invitation, err := ts.accounts.Invite(t.Context(), account.System, in)
	if err != nil {
		t.Fatal(err)
	}
	ts.seen.password(testPassword("writer"))
	writer, err := ts.accounts.SetPassword(t.Context(), account.Client{}, invitation, testPassword("writer"))
	if err != nil {
		t.Fatal(err)
	}
	if writer.Author != authorID {
		t.Fatalf("the account has the author profile %q, want %q", writer.Author, authorID)
	}
	res := ts.postLogin("writer", testPassword("writer"))
	token := ts.tokenOf(t, res)
	ts.checkAnswer(t, "login of an account with an author profile", res, writer, ts.clock.now(), ts.setCookieLine(token))
	if !strings.Contains(answerJSON(writer, ts.clock.now()), `"author":"`+authorID+`"`) {
		t.Error("the test does not expect the author profile")
	}
	ts.checkAnswer(t, "GET", ts.getSession(ts.cookie(token)...), writer, ts.clock.now(), "")
}

func TestWriteTooManyAttempts(t *testing.T) {
	for _, tt := range []struct {
		wait time.Duration
		want string
	}{
		{time.Nanosecond, "1"}, {time.Second - 1, "1"}, {time.Second, "1"}, {time.Second + 1, "2"}, {1500 * time.Millisecond, "2"},
		{30 * time.Second, "30"}, {15 * time.Minute, "900"}, {time.Hour, "3600"},
	} {
		rec := httptest.NewRecorder()
		writeTooManyAttempts(rec, tt.wait)
		res := rec.Result()
		if got := res.Header.Get("Retry-After"); got != tt.want {
			t.Errorf("writeTooManyAttempts(%v) has the Retry-After %q, want %q", tt.wait, got, tt.want)
		}
		checkProblem(t, "writeTooManyAttempts", res, "too-many-attempts")
	}
}
