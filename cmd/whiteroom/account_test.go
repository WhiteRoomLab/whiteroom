//go:build darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd

package main

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/account"
	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

const (
	changedPassword = "amber falcon meadow quartz" // the password that account reset sets
	briefPassword   = "qwerty"                     // refused as too short
)

// noRead is a standard input that must not be read.
type noRead struct{ t *testing.T }

func (r noRead) Read([]byte) (int, error) {
	r.t.Helper()
	r.t.Error("the command reads the standard input")
	return 0, io.EOF
}

// accountData makes a data directory with the first administrator, by whiteroom init without a site.
func accountData(t *testing.T) string {
	t.Helper()
	data := filepath.Join(t.TempDir(), "data")
	if code, _, stderr := runInitCmd(t, initPassword+"\n", adminFlags("-data", data)...); code != 0 {
		t.Fatalf("whiteroom init = %d, with stderr:\n%s", code, stderr)
	}
	return data
}

// withAccounts opens the data directory data, which nothing else has open, as its writer, runs f with its store and its
// accounts, which have cheap password hashes, and closes it: the commands of the tests fail while it is open.
func withAccounts(t *testing.T, data string, f func(s *store.Store, m *account.Manager)) {
	t.Helper()
	s, err := store.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	}()
	m, err := account.NewWithIterations(s, 1)
	if err != nil {
		t.Fatal(err)
	}
	f(s, m)
}

// passwordOf is the password of the account that addAccount makes with the login name.
func passwordOf(login string) string { return login + " has a long password" }

// addAccount invites an account as the system does, and, if withPassword is true, sets its password to passwordOf(login).
// Without it, the account is pending, and has the token of its invitation, which nobody knows.
func addAccount(t *testing.T, data, loginName string, role account.Role, withPassword bool) account.Account {
	t.Helper()
	withAccounts(t, data, func(_ *store.Store, m *account.Manager) {
		in := account.Input{Login: loginName, Name: "Name of " + loginName, Email: loginName + "@example.com", Role: role}
		_, token, err := m.Invite(t.Context(), account.System, in)
		if err != nil {
			t.Fatal(err)
		}
		if withPassword {
			if _, err := m.SetPassword(t.Context(), account.Client{}, token, passwordOf(loginName)); err != nil {
				t.Fatal(err)
			}
		}
	})
	return mustFind(t, data, loginName)
}

// mustFind returns the account with the login name.
func mustFind(t *testing.T, data, login string) account.Account {
	t.Helper()
	var a account.Account
	withAccounts(t, data, func(_ *store.Store, m *account.Manager) {
		var err error
		if a, err = m.Find(t.Context(), login); err != nil {
			t.Fatal(err)
		}
	})
	return a
}

// unreadableAccount puts a row that cannot be read in the accounts of data, and returns its ID. The row is written with the
// store's own API, with the record that it needs, as no operation of the accounts makes such a row.
func unreadableAccount(t *testing.T, data string, n int) string {
	t.Helper()
	id := content.FormatID(content.RefUser, [16]byte{0: 0x02, 15: byte(n)})
	withAccounts(t, data, func(s *store.Store, _ *account.Manager) {
		err := s.Update(t.Context(), func(tx *store.Tx) error {
			if err := tx.PutAccount(id, []byte("not json")); err != nil {
				return err
			}
			return tx.Record(audit.Record{Log: audit.ActivityLog, Actor: audit.System, Action: audit.StoreWrite})
		})
		if err != nil {
			t.Fatal(err)
		}
	})
	return id
}

// rowExists reports whether the accounts of data have a row with the ID, whatever it holds.
func rowExists(t *testing.T, data, id string) bool {
	t.Helper()
	var ok bool
	withAccounts(t, data, func(s *store.Store, _ *account.Manager) {
		err := s.View(t.Context(), func(tx *store.Tx) error {
			var err error
			_, ok, err = tx.Account(id)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	})
	return ok
}

// logRecords are the records of the activity log and of the login history of a data directory.
type logRecords struct{ activity, logins []audit.Record }

func logsOf(t *testing.T, data string) logRecords {
	t.Helper()
	s, err := store.OpenReader(data)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	var l logRecords
	for log, records := range map[audit.Log]*[]audit.Record{audit.ActivityLog: &l.activity, audit.LoginLog: &l.logins} {
		err := s.ReadAudit(t.Context(), audit.Filter{Log: log}, func(r audit.Record) bool {
			*records = append(*records, r)
			return true
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return l
}

// checkLogs checks that the activity log and the login history of data have no more records than they had before.
func checkLogs(t *testing.T, what, data string, before logRecords) {
	t.Helper()
	now := logsOf(t, data)
	if len(now.activity) != len(before.activity) || len(now.logins) != len(before.logins) {
		t.Errorf("%s: the activity log has %d records and the login history %d, want %d and %d, as before", what,
			len(now.activity), len(now.logins), len(before.activity), len(before.logins))
	}
}

// loginAs logs in as loginName with password in data, and returns the token of the session.
func loginAs(t *testing.T, data, loginName, password string) (token string, err error) {
	t.Helper()
	withAccounts(t, data, func(_ *store.Store, m *account.Manager) {
		_, token, err = m.Login(t.Context(), account.Client{}, loginName, password, "")
	})
	return token, err
}

// checkLogin checks that the account can log in with password, and cannot with a password that is not it.
func checkLogin(t *testing.T, what, data, loginName, password string) {
	t.Helper()
	if _, err := loginAs(t, data, loginName, password); err != nil {
		t.Errorf("%s: the login of %s with the password = %v, want it to work", what, loginName, err)
	}
	if _, err := loginAs(t, data, loginName, password+" x"); !errors.Is(err, account.ErrBadCredentials) {
		t.Errorf("%s: the login of %s with another password = %v, want %v", what, loginName, err, account.ErrBadCredentials)
	}
}

// sessionEnded reports whether the token is no session of data any more.
func sessionEnded(t *testing.T, data, token string) bool {
	t.Helper()
	var err error
	withAccounts(t, data, func(_ *store.Store, m *account.Manager) {
		_, err = m.Authenticate(t.Context(), account.Client{}, token)
	})
	return errors.Is(err, account.ErrNoSession)
}

// TestAccountReset resets the password of the only administrator, of another account, of an account that has no password
// yet, and of one that is given with other letters: the output, the account, the old password and the sessions, the
// records, and that nothing has the new password.
func TestAccountReset(t *testing.T) {
	for _, tt := range []struct {
		what     string
		prepare  func(t *testing.T, data string) account.Account
		oldLogin string // the login name and the password of the account that has a session to end
		password string
		arg      func(a account.Account) string
	}{
		{"the only administrator", func(t *testing.T, data string) account.Account { return mustFind(t, data, initLogin) },
			initLogin, initPassword, func(a account.Account) string { return a.Login }},
		{"the only administrator, by other letters", func(t *testing.T, data string) account.Account {
			return mustFind(t, data, initLogin)
		}, initLogin, initPassword, func(a account.Account) string { return " " + strings.ToUpper(a.Login) + " " }},
		{"an editor", func(t *testing.T, data string) account.Account {
			return addAccount(t, data, "yamada", account.RoleEditor, true)
		}, "yamada", passwordOf("yamada"), func(a account.Account) string { return a.Login }},
		{"an account that has no password yet", func(t *testing.T, data string) account.Account {
			return addAccount(t, data, "suzuki", account.RoleViewer, false)
		}, "", "", func(a account.Account) string { return a.Login }},
	} {
		t.Run(tt.what, func(t *testing.T) {
			data := accountData(t)
			target := tt.prepare(t, data)
			var session string
			if tt.oldLogin != "" {
				var err error
				if session, err = loginAs(t, data, tt.oldLogin, tt.password); err != nil {
					t.Fatal(err)
				}
			}
			before := logsOf(t, data)

			code, stdout, stderr := runCmdIn(t, strings.NewReader(changedPassword+"\n"), "account", "reset", "-data", data,
				tt.arg(target))
			want := "reset the password of " + target.Login + " (" + target.ID + ")\n"
			if code != 0 || stdout != want || stderr != "" {
				t.Fatalf("whiteroom account reset = %d, with stdout %q and stderr %q, want 0, %q and nothing", code, stdout, stderr,
					want)
			}
			checkNoSecret(t, tt.what, changedPassword, stdout, stderr, data)

			// The login history gets the end of the session of the account, and nothing else.
			ended := 0
			if session != "" {
				ended = 1
			}
			now := logsOf(t, data)
			if len(now.activity) != len(before.activity)+2 || len(now.logins) != len(before.logins)+ended {
				t.Fatalf("the activity log has %d records and the login history %d, want %d and %d", len(now.activity),
					len(now.logins), len(before.activity)+2, len(before.logins)+ended)
			}
			if ended == 1 {
				if r := now.logins[len(now.logins)-1]; r.Action != audit.SessionEnd || r.Account != target.ID {
					t.Errorf("the last record of the login history is %s of %q, want %s of %s", r.Action, r.Account,
						audit.SessionEnd, target.ID)
				}
			}
			reset, set := now.activity[len(now.activity)-2], now.activity[len(now.activity)-1]
			if reset.Action != audit.AccountReset || reset.Actor != audit.System || reset.Target.ID != target.ID {
				t.Errorf("the second last record is %s by %q about %q, want %s by %s about %s", reset.Action, reset.Actor,
					reset.Target.ID, audit.AccountReset, audit.System, target.ID)
			}
			if set.Action != audit.AccountSetPassword || set.Actor != target.ID || set.Target.ID != target.ID {
				t.Errorf("the last record is %s by %q about %q, want %s by %s about %s", set.Action, set.Actor, set.Target.ID,
					audit.AccountSetPassword, target.ID, target.ID)
			}

			withAccounts(t, data, func(_ *store.Store, m *account.Manager) {
				got, err := m.Get(t.Context(), target.ID)
				if err != nil || got.Status != account.StatusActive || got.Token != (account.TokenInfo{}) {
					t.Errorf("the account is %+v, %v, want an active one without a token", got, err)
				}
				if got.Login != target.Login || got.Role != target.Role || got.Email != target.Email {
					t.Errorf("the account is %+v, want its login name, role and email address as they were, %+v", got, target)
				}
			})
			if tt.password != "" {
				if _, err := loginAs(t, data, target.Login, tt.password); !errors.Is(err, account.ErrBadCredentials) {
					t.Errorf("the login with the old password = %v, want %v", err, account.ErrBadCredentials)
				}
			}
			checkLogin(t, tt.what, data, target.Login, changedPassword)
			if session != "" && !sessionEnded(t, data, session) {
				t.Error("the session of the account has not ended")
			}
		})
	}
}

// TestAccountResetErrors checks the failures that leave everything as it was: each has the exit code 1 and nothing on
// stdout, no record, and the old password still works. The standard input is not read where the command fails before it
// asks for the password.
func TestAccountResetErrors(t *testing.T) {
	clean := accountData(t)
	addAccount(t, clean, "yamada", account.RoleEditor, true)
	addAccount(t, clean, "a-long-login-name", account.RoleEditor, true)
	suzuki := addAccount(t, clean, "suzuki", account.RoleEditor, true)
	withAccounts(t, clean, func(_ *store.Store, m *account.Manager) {
		if _, err := m.Disable(t.Context(), account.System, suzuki.ID); err != nil {
			t.Fatal(err)
		}
	})
	broken := accountData(t)
	badID := unreadableAccount(t, broken, 1)

	unread := func(t *testing.T) io.Reader { return noRead{t} }
	input := func(text string) func(t *testing.T) io.Reader {
		return func(*testing.T) io.Reader { return strings.NewReader(text) }
	}
	const command = "whiteroom account reset: "
	for _, tt := range []struct {
		what   string
		data   string
		stdin  func(t *testing.T) io.Reader // noRead where the command fails before it asks for the password
		login  string
		wants  []string // that stderr has
		exact  bool     // stderr is the one line of wants
		secret string   // that the command was given
	}{
		{"a login name that no account has", clean, unread, "nobody", []string{command, `"nobody"`}, false, ""},
		{"an account that is disabled", clean, unread, "suzuki", []string{command, "suzuki", "is disabled"}, false, ""},
		{"a row that cannot be read", broken, unread, initLogin,
			[]string{command + "accounts \"" + badID + "\": invalid value\n",
				command + "delete it with: whiteroom account delete -data " + broken + " " + badID + "\n"}, false, ""},
		{"a password that is too short", clean, input(briefPassword + "\n"), initLogin,
			[]string{command + "the password: too short: 6 characters (min 15)\n"}, true, briefPassword},
		{"the login name as the password", clean, input("a-long-login-name\n"), "a-long-login-name",
			[]string{command + "the password: the same as the login name, the email address or the name\n"}, true, ""},
		{"an empty input", clean, input(""), initLogin, []string{command + "no password was read from the standard input\n"}, true, ""},
		{"an empty line", clean, input("\n"), initLogin, []string{command + "no password was read from the standard input\n"}, true, ""},
		{"a line of 8193 bytes", clean, input(strings.Repeat("a", 8193) + "\n"), initLogin,
			[]string{command + "the password is longer than 8192 bytes\n"}, true, ""},
	} {
		before := logsOf(t, tt.data)
		code, stdout, stderr := runCmdIn(t, tt.stdin(t), "account", "reset", "-data", tt.data, tt.login)
		if code != 1 || stdout != "" {
			t.Errorf("%s: whiteroom account reset = %d, with stdout %q and stderr:\n%s\nwant 1 and nothing on stdout", tt.what, code,
				stdout, stderr)
		}
		for _, want := range tt.wants {
			if !strings.Contains(stderr, want) {
				t.Errorf("%s: stderr is:\n%s\nwant %q in it", tt.what, stderr, want)
			}
		}
		if tt.exact && stderr != tt.wants[0] {
			t.Errorf("%s: stderr is:\n%s\nwant only %q", tt.what, stderr, tt.wants[0])
		}
		if tt.secret != "" {
			checkNoSecret(t, tt.what, tt.secret, stdout, stderr, tt.data)
		}
		checkLogs(t, tt.what, tt.data, before)
		checkLogin(t, tt.what, tt.data, initLogin, initPassword)
	}

	missing := filepath.Join(t.TempDir(), "missing")
	code, stdout, stderr := runCmdIn(t, noRead{t}, "account", "reset", "-data", missing, initLogin)
	if code != 1 || stdout != "" || !strings.HasPrefix(stderr, "whiteroom account reset: ") || !strings.HasSuffix(stderr, "\n") ||
		strings.Count(stderr, "\n") != 1 {
		t.Errorf("whiteroom account reset of a data directory that is none = %d, with stdout %q and stderr %q, want 1, nothing and one line",
			code, stdout, stderr)
	}
	if s := snapshot(t, missing); s != "missing" {
		t.Errorf("the data directory that was none is made:\n%s", s)
	}
	empty := t.TempDir()
	was := snapshot(t, empty)
	code, stdout, stderr = runCmdIn(t, noRead{t}, "account", "reset", "-data", empty, initLogin)
	if code != 1 || stdout != "" || !strings.HasPrefix(stderr, "whiteroom account reset: ") || strings.Count(stderr, "\n") != 1 {
		t.Errorf("whiteroom account reset of an empty directory = %d, with stdout %q and stderr %q, want 1, nothing and one line",
			code, stdout, stderr)
	}
	if s := snapshot(t, empty); s != was {
		t.Errorf("the empty directory is now:\n%s\nwant it as it was:\n%s", s, was)
	}
}

// fakeResetter is a resetter that fails as the test says and keeps what it was given.
type fakeResetter struct {
	token                          string // that Reset returns
	resetErr, setErr               error
	resetBy                        account.Actor
	resetID, setToken, setPassword string
	setClient                      account.Client
	resetCalls, setCalls           int
}

func (f *fakeResetter) Reset(_ context.Context, by account.Actor, id string) (string, error) {
	f.resetCalls++
	f.resetBy, f.resetID = by, id
	if f.resetErr != nil {
		return "", f.resetErr
	}
	return f.token, nil
}

func (f *fakeResetter) SetPassword(_ context.Context, c account.Client, token, password string) (account.Account, error) {
	f.setCalls++
	f.setClient, f.setToken, f.setPassword = c, token, password
	return account.Account{}, f.setErr
}

// TestResetPasswordFails checks resetPassword with a fake that fails: a failure of SetPassword says that the account has
// no password now and that the command can be run again, with nothing of the token; a failure of Reset is told as it is, and
// SetPassword is not called.
func TestResetPasswordFails(t *testing.T) {
	const token = "the-token-of-the-fake-reset"
	a := account.Account{ID: content.FormatID(content.RefUser, [16]byte{0: 0x03}), Login: "yamada"}

	f := &fakeResetter{token: token, setErr: errors.New("the store failed")}
	err := resetPassword(t.Context(), f, a, changedPassword)
	if err == nil {
		t.Fatal("resetPassword with a SetPassword that fails = nil, want an error")
	}
	for _, want := range []string{"the store failed", "the account yamada has no password now", "run the command again"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("resetPassword with a SetPassword that fails = %q, want %q in it", err, want)
		}
	}
	if strings.Contains(err.Error(), token) || strings.Contains(err.Error(), changedPassword) {
		t.Error("the error of resetPassword has the token or the password")
	}
	if f.resetCalls != 1 || f.resetBy != account.System || f.resetID != a.ID || f.setCalls != 1 || f.setToken != token ||
		f.setPassword != changedPassword || f.setClient != (account.Client{}) {
		t.Errorf("the calls are %d Reset by the system of %q and %d SetPassword, want 1 by the system of %q and 1 with the token and the password",
			f.resetCalls, f.resetID, f.setCalls, a.ID)
	}

	// An interrupted command says it, and that the account has no password.
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	f = &fakeResetter{token: token, setErr: context.Canceled}
	err = resetPassword(canceled, f, a, changedPassword)
	var b strings.Builder
	errorPrinter{w: &b, command: "whiteroom account reset"}.printError(canceled, err)
	if want := "whiteroom account reset: interrupted\nwhiteroom account reset: the account yamada has no password now: run the command again\n"; b.String() != want {
		t.Errorf("an interrupted command printed:\n%s\nwant:\n%s", b.String(), want)
	}

	f = &fakeResetter{token: token, resetErr: errors.New("no reset")}
	err = resetPassword(t.Context(), f, a, changedPassword)
	if err == nil || err.Error() != "no reset" || f.setCalls != 0 {
		t.Errorf("resetPassword with a Reset that fails = %v after %d calls of SetPassword, want that error after none", err, f.setCalls)
	}

	f = &fakeResetter{token: token}
	if err := resetPassword(t.Context(), f, a, changedPassword); err != nil || f.resetCalls != 1 || f.setCalls != 1 {
		t.Errorf("resetPassword = %v after %d and %d calls, want nil after one each", err, f.resetCalls, f.setCalls)
	}
}

// TestAccountResetAfterInterrupted makes the state that a command leaves when it ends between its two steps, an account that
// has no password, whose token nobody knows, and checks that the command then makes it whole.
func TestAccountResetAfterInterrupted(t *testing.T) {
	data := accountData(t)
	admin := mustFind(t, data, initLogin)
	withAccounts(t, data, func(_ *store.Store, m *account.Manager) {
		if _, err := m.Reset(t.Context(), account.System, admin.ID); err != nil {
			t.Fatal(err)
		}
	})
	if _, err := loginAs(t, data, initLogin, initPassword); !errors.Is(err, account.ErrBadCredentials) {
		t.Fatalf("the login after a Reset = %v, want %v", err, account.ErrBadCredentials)
	}
	code, stdout, stderr := runCmdIn(t, strings.NewReader(changedPassword+"\n"), "account", "reset", "-data", data, initLogin)
	if want := "reset the password of " + initLogin + " (" + admin.ID + ")\n"; code != 0 || stdout != want || stderr != "" {
		t.Fatalf("whiteroom account reset = %d, with stdout %q and stderr %q, want 0, %q and nothing", code, stdout, stderr, want)
	}
	checkLogin(t, "after the command", data, initLogin, changedPassword)
	if got := mustFind(t, data, initLogin); got.Status != account.StatusActive {
		t.Errorf("the account is %s, want active", got.Status)
	}
}

// TestAccountDelete deletes rows that cannot be read, one at a time and however many there are, and an account that can
// be read; and the deletions that the rules of the accounts refuse, which leave everything as it was.
func TestAccountDelete(t *testing.T) {
	deleteCmd := func(t *testing.T, data string, args ...string) (code int, stdout, stderr string) {
		t.Helper()
		return runCmdIn(t, noRead{t}, append([]string{"account", "delete", "-data", data}, args...)...)
	}
	// deleted checks the success of whiteroom account delete, and the record of it.
	// A session of the account that ends is a record of the login history.
	deleted := func(t *testing.T, data, id, text string, unreadable bool, sessions int) {
		t.Helper()
		before := logsOf(t, data)
		code, stdout, stderr := deleteCmd(t, data, id)
		if want := "deleted account " + id + " (" + text + ")\n"; code != 0 || stdout != want || stderr != "" {
			t.Fatalf("whiteroom account delete %s = %d, with stdout %q and stderr %q, want 0, %q and nothing", id, code, stdout,
				stderr, want)
		}
		now := logsOf(t, data)
		if len(now.activity) != len(before.activity)+1 || len(now.logins) != len(before.logins)+sessions {
			t.Fatalf("the activity log has %d records and the login history %d, want %d and %d", len(now.activity),
				len(now.logins), len(before.activity)+1, len(before.logins)+sessions)
		}
		r := now.activity[len(now.activity)-1]
		if r.Action != audit.AccountDelete || r.Actor != audit.System || r.Target.ID != id || (r.Detail["unreadable"] == "true") != unreadable {
			t.Errorf("the last record is %s by %q about %q with the detail %v, want %s by %s about %s, unreadable %t", r.Action,
				r.Actor, r.Target.ID, r.Detail, audit.AccountDelete, audit.System, id, unreadable)
		}
		if rowExists(t, data, id) {
			t.Errorf("the row of %s is still there", id)
		}
	}
	const unreadableText = "its row could not be read"

	t.Run("a row that cannot be read", func(t *testing.T) {
		data := accountData(t)
		id := unreadableAccount(t, data, 1)
		code, stdout, stderr := runCmdIn(t, noRead{t}, "account", "reset", "-data", data, initLogin)
		if code != 1 || stdout != "" || !strings.Contains(stderr, id) || !strings.Contains(stderr, "account delete") {
			t.Fatalf("whiteroom account reset with the row = %d, with stdout %q and stderr:\n%s\nwant 1 and its ID, and account delete", code,
				stdout, stderr)
		}
		deleted(t, data, id, unreadableText, true, 0)
		code, stdout, stderr = runCmdIn(t, strings.NewReader(changedPassword+"\n"), "account", "reset", "-data", data, initLogin)
		if code != 0 {
			t.Errorf("whiteroom account reset after the deletion = %d, with stdout %q and stderr:\n%s\nwant 0", code, stdout, stderr)
		}
	})

	for _, order := range [][2]int{{0, 1}, {1, 0}} {
		t.Run("two rows that cannot be read", func(t *testing.T) {
			data := accountData(t)
			ids := []string{unreadableAccount(t, data, 1), unreadableAccount(t, data, 2)}
			first, second := ids[order[0]], ids[order[1]]
			deleted(t, data, first, unreadableText, true, 0)
			if !rowExists(t, data, second) {
				t.Errorf("the other row, %s, is gone with the first", second)
			}
			deleted(t, data, second, unreadableText, true, 0)
		})
	}

	t.Run("an account that can be read", func(t *testing.T) {
		data := accountData(t)
		yamada := addAccount(t, data, "yamada", account.RoleEditor, true)
		session, err := loginAs(t, data, "yamada", passwordOf("yamada"))
		if err != nil {
			t.Fatal(err)
		}
		deleted(t, data, yamada.ID, "yamada", false, 1)
		withAccounts(t, data, func(_ *store.Store, m *account.Manager) {
			if _, err := m.Get(t.Context(), yamada.ID); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("Get of the deleted account = %v, want %v", err, fs.ErrNotExist)
			}
		})
		if !sessionEnded(t, data, session) {
			t.Error("the session of the deleted account has not ended")
		}
		checkLogin(t, "the administrator", data, initLogin, initPassword)
	})

	t.Run("the last active administrator", func(t *testing.T) {
		data := accountData(t)
		admin := mustFind(t, data, initLogin)
		before := logsOf(t, data)
		code, stdout, stderr := deleteCmd(t, data, admin.ID)
		if code != 1 || stdout != "" || !strings.Contains(stderr, "is the last active administrator: it cannot be deleted") {
			t.Errorf("whiteroom account delete of the last administrator = %d, with stdout %q and stderr:\n%s\nwant 1 and the reason",
				code, stdout, stderr)
		}
		checkLogs(t, "the last administrator", data, before)
		checkLogin(t, "the last administrator", data, initLogin, initPassword)
	})

	t.Run("no account", func(t *testing.T) {
		data := accountData(t)
		gone := content.FormatID(content.RefUser, [16]byte{0: 0x09})
		for _, arg := range []string{gone, initLogin, "usr_x"} {
			before := logsOf(t, data)
			code, stdout, stderr := deleteCmd(t, data, arg)
			if code != 1 || stdout != "" || !strings.Contains(stderr, "not found") {
				t.Errorf("whiteroom account delete %s = %d, with stdout %q and stderr:\n%s\nwant 1 and not found", arg, code, stdout,
					stderr)
			}
			checkLogs(t, arg, data, before)
		}
		checkLogin(t, "the administrator", data, initLogin, initPassword)
	})

	t.Run("an account that can be read, beside a row that cannot", func(t *testing.T) {
		data := accountData(t)
		yamada := addAccount(t, data, "yamada", account.RoleEditor, true)
		id := unreadableAccount(t, data, 1)
		before := logsOf(t, data)
		code, stdout, stderr := deleteCmd(t, data, yamada.ID)
		if code != 1 || stdout != "" || !strings.Contains(stderr, id) {
			t.Errorf("whiteroom account delete of an account that can be read = %d, with stdout %q and stderr:\n%s\nwant 1 and the ID %s",
				code, stdout, stderr, id)
		}
		checkLogs(t, "an account that can be read", data, before)
		if !rowExists(t, data, yamada.ID) || !rowExists(t, data, id) {
			t.Error("a row is gone")
		}
	})
}

// TestAccountConfig runs the commands with the data directory of a config file, which -data overrides, and checks that the
// advice names the data directory that was chosen.
func TestAccountConfig(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "data")
	if code, _, stderr := runInitCmd(t, initPassword+"\n", adminFlags("-data", data)...); code != 0 {
		t.Fatalf("whiteroom init = %d, with stderr:\n%s", code, stderr)
	}
	conf := filepath.Join(root, "conf")
	file := writeConfig(t, conf, "whiteroom.yaml", "# the settings of every command\ndata: ../data\nworkers: 2\naddr: 127.0.0.1:8081\n")
	noData := writeConfig(t, conf, "no-data.yaml", "data: ../no-such-data\n")

	// reset: the item data of the file.
	code, stdout, stderr := runCmdIn(t, strings.NewReader(changedPassword+"\n"), "account", "reset", "-config", file, initLogin)
	admin := mustFind(t, data, initLogin)
	if want := "reset the password of " + initLogin + " (" + admin.ID + ")\n"; code != 0 || stdout != want || stderr != "" {
		t.Fatalf("whiteroom account reset -config = %d, with stdout %q and stderr %q, want 0, %q and nothing", code, stdout, stderr, want)
	}
	checkLogin(t, "the item data", data, initLogin, changedPassword)

	// -data wins over the item, which names a directory that is none.
	code, stdout, stderr = runCmdIn(t, strings.NewReader(initPassword+"\n"), "account", "reset", "-config", noData, "-data", data,
		initLogin)
	if code != 0 || stderr != "" || !strings.HasPrefix(stdout, "reset the password of ") {
		t.Fatalf("whiteroom account reset -config -data = %d, with stdout %q and stderr %q, want 0", code, stdout, stderr)
	}
	checkLogin(t, "-data over the item", data, initLogin, initPassword)
	code, stdout, stderr = runCmdIn(t, noRead{t}, "account", "reset", "-config", noData, initLogin)
	if code != 1 || stdout != "" || !strings.HasPrefix(stderr, "whiteroom account reset: ") {
		t.Errorf("whiteroom account reset with an item data that is none = %d, with stdout %q and stderr %q, want 1", code, stdout, stderr)
	}

	// delete: the item data of the file, and -data over it.
	yamada := addAccount(t, data, "yamada", account.RoleEditor, true)
	code, stdout, stderr = runCmdIn(t, noRead{t}, "account", "delete", "-config", noData, "-data", data, yamada.ID)
	if want := "deleted account " + yamada.ID + " (yamada)\n"; code != 0 || stdout != want || stderr != "" {
		t.Fatalf("whiteroom account delete -config -data = %d, with stdout %q and stderr %q, want 0, %q and nothing", code, stdout, stderr, want)
	}
	suzuki := addAccount(t, data, "suzuki", account.RoleEditor, true)
	code, stdout, stderr = runCmdIn(t, noRead{t}, "account", "delete", "-config", file, suzuki.ID)
	if want := "deleted account " + suzuki.ID + " (suzuki)\n"; code != 0 || stdout != want || stderr != "" {
		t.Fatalf("whiteroom account delete -config = %d, with stdout %q and stderr %q, want 0, %q and nothing", code, stdout, stderr, want)
	}

	// The advice names the data directory that the file gives, as a path from the working directory.
	id := unreadableAccount(t, data, 1)
	code, stdout, stderr = runCmdIn(t, noRead{t}, "account", "reset", "-config", file, initLogin)
	if want := "whiteroom account reset: delete it with: whiteroom account delete -data " + data + " " + id + "\n"; code != 1 ||
		stdout != "" || !strings.Contains(stderr, want) {
		t.Errorf("whiteroom account reset -config with a row that cannot be read = %d, with stdout %q and stderr:\n%s\nwant 1 and %q in it",
			code, stdout, stderr, want)
	}
	code, stdout, stderr = runCmdIn(t, noRead{t}, "account", "delete", "-config", file, id)
	if want := "deleted account " + id + " (" + "its row could not be read" + ")\n"; code != 0 || stdout != want || stderr != "" {
		t.Errorf("whiteroom account delete -config of the row = %d, with stdout %q and stderr %q, want 0, %q and nothing", code, stdout, stderr, want)
	}

	// A problem in the file is a usage error, found before anything is read or changed.
	before := logsOf(t, data)
	unknown := writeConfig(t, conf, "unknown.yaml", "data: ../data\nsnack: 1\n")
	for _, args := range [][]string{{"reset", initLogin}, {"delete", admin.ID}} {
		code, stdout, stderr = runCmdIn(t, noRead{t}, append([]string{"account", args[0], "-config", unknown}, args[1])...)
		if want := unknown + ":2:1: /snack: unknown item\n"; code != 2 || stdout != "" || stderr != want {
			t.Errorf("whiteroom account %s -config with an unknown item = %d, with stdout %q and stderr %q, want 2, nothing and %q",
				args[0], code, stdout, stderr, want)
		}
	}
	checkLogs(t, "a config file with a problem", data, before)

	// No data directory, from the flags or the file, is a usage error.
	other := writeConfig(t, conf, "other.yaml", "workers: 2\n")
	for _, args := range [][]string{{"reset", initLogin}, {"delete", admin.ID}} {
		code, stdout, stderr = runCmdIn(t, noRead{t}, append([]string{"account", args[0], "-config", other}, args[1])...)
		if code != 2 || stdout != "" || !strings.HasPrefix(stderr, "whiteroom account "+args[0]+": no data directory: ") {
			t.Errorf("whiteroom account %s with no data directory = %d, with stdout %q and stderr %q, want 2 and the usage error", args[0],
				code, stdout, stderr)
		}
	}
	checkLogs(t, "no data directory", data, before)
}

// TestAccountBesideAdmin runs the commands on a data directory that a whiteroom serve admin has open: they fail after the
// wait for the lock, with the advice, without reading the standard input, and the server is as it was.
func TestAccountBesideAdmin(t *testing.T) {
	s := initSample(t)
	a := startAdmin(t, t.Context(), s.args()...)
	cookie := a.login(t)
	if built := a.waitBuilt(t, cookie); failedBuild(built) || built.Last.Release == "" {
		t.Fatalf("the start build has %+v, want a release\nstderr:\n%s", built.Last, a.stderr)
	}
	for _, tt := range []struct {
		args   []string
		advice string
	}{
		{[]string{"account", "reset", "-data", s.data, initLogin}, "POST /api/accounts/{id}/reset"},
		{[]string{"account", "delete", "-data", s.data, "usr_x"}, "DELETE /api/accounts/{id}"},
	} {
		start := time.Now()
		code, stdout, stderr := runCmdIn(t, noRead{t}, tt.args...)
		if elapsed := time.Since(start); elapsed > 30*time.Second {
			t.Errorf("whiteroom %s took %v, want a few seconds", strings.Join(tt.args, " "), elapsed)
		}
		name := "whiteroom " + tt.args[0] + " " + tt.args[1]
		lines := strings.Split(strings.TrimSuffix(stderr, "\n"), "\n")
		if code != 1 || stdout != "" || len(lines) != 2 || !strings.HasPrefix(lines[0], name+": ") ||
			!strings.HasSuffix(lines[0], ": another process holds the writer lock") ||
			!strings.HasPrefix(lines[1], name+": stop whiteroom serve admin") || !strings.Contains(lines[1], tt.advice) {
			t.Errorf("%s beside the server = %d, with stdout %q and stderr:\n%s\nwant 1, the error of the writer lock and the advice", name, code,
				stdout, stderr)
		}
		if status, _ := a.exchange(t, http.MethodGet, "/api/builds", "", "Cookie", cookie); status != http.StatusOK {
			t.Errorf("the server answers %d after %s, want %d", status, name, http.StatusOK)
		}
	}
	if status, cookie := a.tryLogin(t, initLogin, initPassword); status != http.StatusOK || cookie == "" {
		t.Errorf("the login with the old password after the commands = %d, want %d", status, http.StatusOK)
	}
	if code := a.stop(t); code != 0 {
		t.Errorf("whiteroom serve admin = %d after its context is done, want 0", code)
	}
	if _, err := os.Stat(s.data); err != nil {
		t.Error(err)
	}
	checkUnlocked(t, "after the server", s.data)
}
