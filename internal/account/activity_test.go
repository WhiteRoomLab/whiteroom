package account

import (
	"errors"
	"io/fs"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

// recDetail is the detail of a record.
type recDetail = map[string]string

// activityRecord is the record of the activity log that an account operation is expected to make, without what the store
// sets.
func activityRecord(actor string, action audit.Action, id string, changed bool, detail recDetail) audit.Record {
	return audit.Record{
		Log: audit.ActivityLog, Actor: actor, Action: action, Target: audit.Target{Kind: "user", ID: id}, Changed: changed,
		Detail: detail,
	}
}

// expiryAt is how the detail of a record writes the time at which a token made at mgrTime stops working after ttl.
func expiryAt(ttl time.Duration) string {
	return mgrTime.Add(ttl).UTC().Format(time.RFC3339Nano)
}

// TestAccountRecords runs every account operation in each way that it succeeds, and checks the one record that it makes
// in the activity log: who did it, to which account, whether it changed anything, and what it changed, without a name, an
// email address, a password or a token. The chain of the log holds after all of them.
func TestAccountRecords(t *testing.T) {
	e := newMgr(t)
	ctx := t.Context()
	e.putAuthor(t, mgrAuthorID)
	e.putAuthor(t, mgrOtherAuthorID)
	var admin, yamada, taro Account
	var inviteToken, resetToken string

	// step does an operation and checks that it made the record that want returns in the activity log, and no other.
	step := func(name string, do func() error, want func() audit.Record) {
		t.Helper()
		before := e.mark(t)
		if err := do(); err != nil {
			t.Fatalf("%s = %v", name, err)
		}
		histWant(t, e.activityRecords(t, before), want())
		if t.Failed() {
			t.Fatalf("the record of %s is not the one that is expected", name)
		}
	}

	step("CreateFirst", func() (err error) {
		admin, err = e.m.CreateFirst(ctx, mgrInput("admin", RoleAdmin), mgrPassword("admin"))
		return err
	}, func() audit.Record {
		return activityRecord(audit.System, audit.AccountCreateFirst, admin.ID, true, recDetail{
			"loginAfter": "admin", "roleAfter": "admin", "statusAfter": "active",
		})
	})
	step("Invite by an administrator, with a link to an author profile", func() (err error) {
		in := mgrInput("yamada", RoleEditor)
		in.Author = mgrAuthorID
		yamada, inviteToken, err = e.m.Invite(ctx, By(admin.ID), in)
		return err
	}, func() audit.Record {
		return activityRecord(admin.ID, audit.AccountInvite, yamada.ID, true, recDetail{
			"authorAfter": mgrAuthorID, "loginAfter": "yamada", "roleAfter": "editor", "statusAfter": "pending",
			"tokenPurpose": "invitation", "tokenExpiresAt": expiryAt(invitationTTL),
		})
	})
	step("Invite by System", func() (err error) {
		taro, _, err = e.m.Invite(ctx, System, mgrInput("taro", RoleViewer))
		return err
	}, func() audit.Record {
		return activityRecord(audit.System, audit.AccountInvite, taro.ID, true, recDetail{
			"loginAfter": "taro", "roleAfter": "viewer", "statusAfter": "pending",
			"tokenPurpose": "invitation", "tokenExpiresAt": expiryAt(invitationTTL),
		})
	})
	step("SetPassword with an invitation", func() (err error) {
		yamada, err = e.m.SetPassword(ctx, mgrClient, inviteToken, mgrPassword("yamada"))
		return err
	}, func() audit.Record {
		// The account sets its own password, and is its own actor.
		return activityRecord(yamada.ID, audit.AccountSetPassword, yamada.ID, true, recDetail{
			"fields": "password, status, token", "statusBefore": "pending", "statusAfter": "active",
		})
	})
	step("ChangePassword", func() error {
		return e.m.ChangePassword(ctx, yamada.ID, mgrPassword("yamada"), "yamada has a changed password")
	}, func() audit.Record {
		return activityRecord(yamada.ID, audit.AccountChangePassword, yamada.ID, true, recDetail{"fields": "password"})
	})
	step("Update of the name and the email address by an administrator", func() (err error) {
		yamada, err = e.m.Update(ctx, By(admin.ID), yamada.ID, Change{Name: ptr("Marker New Name"), Email: ptr("marker.new@example.org")})
		return err
	}, func() audit.Record {
		return activityRecord(admin.ID, audit.AccountUpdate, yamada.ID, true, recDetail{"fields": "email, name"})
	})
	step("Update of the name by the account itself", func() (err error) {
		yamada, err = e.m.Update(ctx, By(yamada.ID), yamada.ID, Change{Name: ptr("Marker Newer Name")})
		return err
	}, func() audit.Record {
		return activityRecord(yamada.ID, audit.AccountUpdate, yamada.ID, true, recDetail{"fields": "name"})
	})
	step("Update of the login name, the role and the link", func() (err error) {
		yamada, err = e.m.Update(ctx, By(admin.ID), yamada.ID,
			Change{Login: ptr("yamada2"), Role: ptr(RoleAuthor), Author: ptr(mgrOtherAuthorID)})
		return err
	}, func() audit.Record {
		return activityRecord(admin.ID, audit.AccountUpdate, yamada.ID, true, recDetail{
			"fields": "author, login, role", "authorBefore": mgrAuthorID, "authorAfter": mgrOtherAuthorID,
			"loginBefore": "yamada", "loginAfter": "yamada2", "roleBefore": "editor", "roleAfter": "author",
		})
	})
	step("Update that removes the link, by System", func() (err error) {
		yamada, err = e.m.Update(ctx, System, yamada.ID, Change{Author: ptr("")})
		return err
	}, func() audit.Record {
		return activityRecord(audit.System, audit.AccountUpdate, yamada.ID, true, recDetail{
			"fields": "author", "authorBefore": mgrOtherAuthorID,
		})
	})
	step("Update that changes nothing", func() (err error) {
		yamada, err = e.m.Update(ctx, By(yamada.ID), yamada.ID, Change{})
		return err
	}, func() audit.Record {
		return activityRecord(yamada.ID, audit.AccountUpdate, yamada.ID, false, nil)
	})
	step("Disable", func() (err error) {
		yamada, err = e.m.Disable(ctx, By(admin.ID), yamada.ID)
		return err
	}, func() audit.Record {
		return activityRecord(admin.ID, audit.AccountDisable, yamada.ID, true, recDetail{
			"fields": "status", "statusBefore": "active", "statusAfter": "disabled",
		})
	})
	step("Disable of a disabled account", func() (err error) {
		yamada, err = e.m.Disable(ctx, By(admin.ID), yamada.ID)
		return err
	}, func() audit.Record {
		return activityRecord(admin.ID, audit.AccountDisable, yamada.ID, false, nil)
	})
	step("Enable by System", func() (err error) {
		yamada, err = e.m.Enable(ctx, System, yamada.ID)
		return err
	}, func() audit.Record {
		return activityRecord(audit.System, audit.AccountEnable, yamada.ID, true, recDetail{
			"fields": "status", "statusBefore": "disabled", "statusAfter": "active",
		})
	})
	step("Enable of an enabled account", func() (err error) {
		yamada, err = e.m.Enable(ctx, System, yamada.ID)
		return err
	}, func() audit.Record {
		return activityRecord(audit.System, audit.AccountEnable, yamada.ID, false, nil)
	})
	step("Reset of an account that has a password", func() (err error) {
		resetToken, err = e.m.Reset(ctx, By(admin.ID), yamada.ID)
		return err
	}, func() audit.Record {
		return activityRecord(admin.ID, audit.AccountReset, yamada.ID, true, recDetail{
			"fields": "password, status, token", "statusBefore": "active", "statusAfter": "pending",
			"tokenPurpose": "reset", "tokenExpiresAt": expiryAt(resetTTL),
		})
	})
	step("Reset of an account that never had a password, by System", func() error {
		_, err := e.m.Reset(ctx, System, taro.ID)
		return err
	}, func() audit.Record {
		// Only the token is replaced: it is an invitation again, since the account has never had a credential.
		return activityRecord(audit.System, audit.AccountReset, taro.ID, true, recDetail{
			"fields": "token", "tokenPurpose": "invitation", "tokenExpiresAt": expiryAt(invitationTTL),
		})
	})
	step("SetPassword with the token of a reset", func() (err error) {
		yamada, err = e.m.SetPassword(ctx, mgrClient, resetToken, mgrPassword("yamada"))
		return err
	}, func() audit.Record {
		return activityRecord(yamada.ID, audit.AccountSetPassword, yamada.ID, true, recDetail{
			"fields": "password, status, token", "statusBefore": "pending", "statusAfter": "active",
		})
	})
	step("Disable of an account that has a token", func() (err error) {
		taro, err = e.m.Disable(ctx, By(admin.ID), taro.ID)
		return err
	}, func() audit.Record {
		return activityRecord(admin.ID, audit.AccountDisable, taro.ID, true, recDetail{
			"fields": "status, token", "statusBefore": "pending", "statusAfter": "disabled",
		})
	})
	step("Enable of an account without a credential", func() (err error) {
		taro, err = e.m.Enable(ctx, By(admin.ID), taro.ID)
		return err
	}, func() audit.Record {
		return activityRecord(admin.ID, audit.AccountEnable, taro.ID, true, recDetail{
			"fields": "status", "statusBefore": "disabled", "statusAfter": "pending",
		})
	})

	// The session that asks ends, and a new one takes its place; the activity record is the last that the Update writes.
	_, adminToken := e.login(t, "admin")
	before := e.mark(t)
	if _, _, err := e.m.ChangeOwnPassword(ctx, mgrClient, adminToken, mgrPassword("admin"), "admin has a new long password"); err != nil {
		t.Fatalf("ChangeOwnPassword() = %v", err)
	}
	e.gained(t, before, "session.end/password", "session.start", "account.changePassword")
	histWant(t, e.activityRecords(t, before),
		activityRecord(admin.ID, audit.AccountChangePassword, admin.ID, true, recDetail{"fields": "password"}))

	step("Delete of an account that has not been used", func() error {
		return e.m.Delete(ctx, By(admin.ID), taro.ID)
	}, func() audit.Record {
		return activityRecord(admin.ID, audit.AccountDelete, taro.ID, true, recDetail{
			"loginBefore": "taro", "roleBefore": "viewer", "statusBefore": "pending",
		})
	})
	step("Delete by System", func() error {
		return e.m.Delete(ctx, System, yamada.ID)
	}, func() audit.Record {
		return activityRecord(audit.System, audit.AccountDelete, yamada.ID, true, recDetail{
			"loginBefore": "yamada2", "roleBefore": "author", "statusBefore": "active",
		})
	})
	bad := mgrRow(7, "bad", RoleViewer).ID
	e.putRaw(t, bad, "not json")
	step("Delete of a row that cannot be read, by System", func() error {
		return e.m.Delete(ctx, System, bad)
	}, func() audit.Record {
		return activityRecord(audit.System, audit.AccountDelete, bad, true, recDetail{"unreadable": "true"})
	})

	if err := e.s.VerifyAudit(ctx, nil); err != nil {
		t.Errorf("VerifyAudit() = %v", err)
	}
}

// TestAccountRecordsRefused checks that an operation that is refused makes no record in the activity log, for each kind
// of refusal that the operations have, and writes no row.
func TestAccountRecordsRefused(t *testing.T) {
	// refusalEnv is a store with an administrator, who is the last active one, two editors, a viewer that is invited and
	// an editor that is disabled.
	type refusalEnv struct {
		e                   *mgrEnv
		admin, editor, peer Account
		invited, off        Account
		token               string // of the invitation
	}
	setup := func(t *testing.T) *refusalEnv {
		t.Helper()
		e := newMgr(t)
		r := &refusalEnv{e: e}
		r.admin = e.first(t)
		r.editor = e.add(t, "editor", RoleEditor)
		r.peer = e.add(t, "peer", RoleEditor)
		r.invited, r.token = e.invite(t, "invited", RoleViewer)
		r.off = e.add(t, "off", RoleEditor)
		if _, err := e.m.Disable(t.Context(), System, r.off.ID); err != nil {
			t.Fatal(err)
		}
		return r
	}
	is := func(target error) func(error) bool {
		return func(err error) bool { return errors.Is(err, target) }
	}
	problems := func(err error) bool {
		var errs content.Errors
		return errors.As(err, &errs)
	}
	disabled := func(err error) bool {
		return err != nil && strings.HasSuffix(err.Error(), " is disabled: enable it first")
	}
	type action = func() error
	tests := []struct {
		name    string
		prepare func(t *testing.T, r *refusalEnv) action
		want    func(error) bool
		// records are the records of the login history that the call makes.
		records []string
	}{
		{"Invite by an editor", func(t *testing.T, r *refusalEnv) action {
			return func() error {
				_, _, err := r.e.m.Invite(t.Context(), By(r.editor.ID), mgrInput("new", RoleViewer))
				return err
			}
		}, is(ErrForbidden), nil},
		{"Invite by nobody", func(t *testing.T, r *refusalEnv) action {
			return func() error { _, _, err := r.e.m.Invite(t.Context(), Actor{}, mgrInput("new", RoleViewer)); return err }
		}, is(ErrForbidden), nil},
		{"Invite with a login name that another account has", func(t *testing.T, r *refusalEnv) action {
			return func() error { _, _, err := r.e.m.Invite(t.Context(), System, mgrInput("peer", RoleViewer)); return err }
		}, problems, nil},
		{"Invite with a role that is none", func(t *testing.T, r *refusalEnv) action {
			return func() error {
				_, _, err := r.e.m.Invite(t.Context(), System, mgrInput("new", Role("boss")))
				return err
			}
		}, problems, nil},
		{"CreateFirst in a store that has accounts", func(t *testing.T, r *refusalEnv) action {
			return func() error {
				_, err := r.e.m.CreateFirst(t.Context(), mgrInput("first", RoleAdmin), mgrPassword("first"))
				return err
			}
		}, func(err error) bool {
			return err != nil && err.Error() == "the store has accounts: CreateFirst needs a store without any"
		}, nil},
		{"SetPassword with a token that is none", func(t *testing.T, r *refusalEnv) action {
			return func() error {
				_, err := r.e.m.SetPassword(t.Context(), mgrClient, tokSample, mgrPassword("invited"))
				return err
			}
		}, is(ErrInvalidToken), []string{"token.failure/unknown"}},
		{"SetPassword with a password that is too short", func(t *testing.T, r *refusalEnv) action {
			return func() error { _, err := r.e.m.SetPassword(t.Context(), mgrClient, r.token, "short"); return err }
		}, problems, nil},
		{"Reset by an editor", func(t *testing.T, r *refusalEnv) action {
			return func() error { _, err := r.e.m.Reset(t.Context(), By(r.editor.ID), r.peer.ID); return err }
		}, is(ErrForbidden), nil},
		{"Reset of the last active administrator", func(t *testing.T, r *refusalEnv) action {
			return func() error { _, err := r.e.m.Reset(t.Context(), By(r.admin.ID), r.admin.ID); return err }
		}, is(ErrLastAdmin), nil},
		{"Reset of an account that is disabled", func(t *testing.T, r *refusalEnv) action {
			return func() error { _, err := r.e.m.Reset(t.Context(), System, r.off.ID); return err }
		}, disabled, nil},
		{"Reset of an account that is not there", func(t *testing.T, r *refusalEnv) action {
			return func() error { _, err := r.e.m.Reset(t.Context(), System, mgrMissingID); return err }
		}, is(fs.ErrNotExist), nil},
		{"ChangePassword with a wrong password", func(t *testing.T, r *refusalEnv) action {
			return func() error { return r.e.m.ChangePassword(t.Context(), r.peer.ID, mgrPassword("other"), endPassword) }
		}, is(ErrBadCredentials), nil},
		{"ChangePassword to a password that is too short", func(t *testing.T, r *refusalEnv) action {
			return func() error { return r.e.m.ChangePassword(t.Context(), r.peer.ID, mgrPassword("peer"), "short") }
		}, problems, nil},
		{"ChangePassword of an account that is not there", func(t *testing.T, r *refusalEnv) action {
			return func() error { return r.e.m.ChangePassword(t.Context(), mgrMissingID, mgrPassword("peer"), endPassword) }
		}, is(fs.ErrNotExist), nil},
		{"ChangePassword of an account that is disabled", func(t *testing.T, r *refusalEnv) action {
			return func() error { return r.e.m.ChangePassword(t.Context(), r.off.ID, mgrPassword("off"), endPassword) }
		}, is(ErrBadCredentials), nil},
		{"ChangePassword of an account that has no password", func(t *testing.T, r *refusalEnv) action {
			return func() error {
				return r.e.m.ChangePassword(t.Context(), r.invited.ID, mgrPassword("invited"), endPassword)
			}
		}, is(ErrBadCredentials), nil},
		{"ChangeOwnPassword with a token that is none", func(t *testing.T, r *refusalEnv) action {
			return func() error {
				_, _, err := r.e.m.ChangeOwnPassword(t.Context(), mgrClient, tokSample, mgrPassword("peer"), endPassword)
				return err
			}
		}, is(ErrNoSession), nil},
		{"ChangeOwnPassword with a wrong password", func(t *testing.T, r *refusalEnv) action {
			_, token := r.e.login(t, "peer")
			return func() error {
				_, _, err := r.e.m.ChangeOwnPassword(t.Context(), mgrClient, token, mgrPassword("other"), endPassword)
				return err
			}
		}, is(ErrBadCredentials), nil},
		{"ChangeOwnPassword to a password that is too short", func(t *testing.T, r *refusalEnv) action {
			_, token := r.e.login(t, "peer")
			return func() error {
				_, _, err := r.e.m.ChangeOwnPassword(t.Context(), mgrClient, token, mgrPassword("peer"), "short")
				return err
			}
		}, problems, nil},
		{"Update by an editor of another account", func(t *testing.T, r *refusalEnv) action {
			return func() error {
				_, err := r.e.m.Update(t.Context(), By(r.editor.ID), r.peer.ID, Change{Name: ptr("Taken over")})
				return err
			}
		}, is(ErrForbidden), nil},
		{"Update of the role by the account itself", func(t *testing.T, r *refusalEnv) action {
			return func() error {
				_, err := r.e.m.Update(t.Context(), By(r.editor.ID), r.editor.ID, Change{Role: ptr(RoleAdmin)})
				return err
			}
		}, is(ErrForbidden), nil},
		{"Update with a login name that another account has", func(t *testing.T, r *refusalEnv) action {
			return func() error {
				_, err := r.e.m.Update(t.Context(), System, r.peer.ID, Change{Login: ptr("editor")})
				return err
			}
		}, problems, nil},
		{"Update with a value that is invalid", func(t *testing.T, r *refusalEnv) action {
			return func() error {
				_, err := r.e.m.Update(t.Context(), System, r.peer.ID, Change{Email: ptr("x")})
				return err
			}
		}, problems, nil},
		{"Update of the role of the last active administrator", func(t *testing.T, r *refusalEnv) action {
			return func() error {
				_, err := r.e.m.Update(t.Context(), System, r.admin.ID, Change{Role: ptr(RoleEditor)})
				return err
			}
		}, is(ErrLastAdmin), nil},
		{"Update of an account that is not there", func(t *testing.T, r *refusalEnv) action {
			return func() error {
				_, err := r.e.m.Update(t.Context(), System, mgrMissingID, Change{Name: ptr("x")})
				return err
			}
		}, is(fs.ErrNotExist), nil},
		{"Disable by an editor", func(t *testing.T, r *refusalEnv) action {
			return func() error { _, err := r.e.m.Disable(t.Context(), By(r.editor.ID), r.peer.ID); return err }
		}, is(ErrForbidden), nil},
		{"Disable of the last active administrator", func(t *testing.T, r *refusalEnv) action {
			return func() error { _, err := r.e.m.Disable(t.Context(), System, r.admin.ID); return err }
		}, is(ErrLastAdmin), nil},
		{"Disable of an account that is not there", func(t *testing.T, r *refusalEnv) action {
			return func() error { _, err := r.e.m.Disable(t.Context(), System, mgrMissingID); return err }
		}, is(fs.ErrNotExist), nil},
		{"Enable by an editor", func(t *testing.T, r *refusalEnv) action {
			return func() error { _, err := r.e.m.Enable(t.Context(), By(r.editor.ID), r.off.ID); return err }
		}, is(ErrForbidden), nil},
		{"Enable of an account that is not there", func(t *testing.T, r *refusalEnv) action {
			return func() error { _, err := r.e.m.Enable(t.Context(), System, mgrMissingID); return err }
		}, is(fs.ErrNotExist), nil},
		{"Delete by an editor", func(t *testing.T, r *refusalEnv) action {
			return func() error { return r.e.m.Delete(t.Context(), By(r.editor.ID), r.peer.ID) }
		}, is(ErrForbidden), nil},
		{"Delete of the last active administrator", func(t *testing.T, r *refusalEnv) action {
			return func() error { return r.e.m.Delete(t.Context(), System, r.admin.ID) }
		}, is(ErrLastAdmin), nil},
		{"Delete of an account that is not there", func(t *testing.T, r *refusalEnv) action {
			return func() error { return r.e.m.Delete(t.Context(), System, mgrMissingID) }
		}, is(fs.ErrNotExist), nil},
		{"Delete of a row that cannot be read, by an administrator", func(t *testing.T, r *refusalEnv) action {
			r.e.putRaw(t, mgrMissingID, "not json")
			return func() error { return r.e.m.Delete(t.Context(), By(r.admin.ID), r.peer.ID) }
		}, func(err error) bool { return err != nil && err.Error() == invalidValue(mgrMissingID) }, nil},
		{"Update with a row that cannot be read, by System", func(t *testing.T, r *refusalEnv) action {
			r.e.putRaw(t, mgrMissingID, "not json")
			return func() error {
				_, err := r.e.m.Update(t.Context(), System, r.peer.ID, Change{Name: ptr("x")})
				return err
			}
		}, func(err error) bool { return err != nil && err.Error() == invalidValue(mgrMissingID) }, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := setup(t)
			act := tt.prepare(t, r)
			before := r.e.mark(t)
			if err := act(); !tt.want(err) {
				t.Errorf("the operation = %v, which is not the refusal that the test expects", err)
			}
			if len(tt.records) == 0 {
				r.e.unchanged(t, before)
				return
			}
			r.e.recorded(t, before, tt.records...)
			if got := r.e.activityRecords(t, before); len(got) != 0 {
				t.Errorf("the refusal made %d records in the activity log", len(got))
			}
		})
	}

	t.Run("CreateFirst with problems", func(t *testing.T) {
		e := newMgr(t)
		before := e.mark(t)
		_, err := e.m.CreateFirst(t.Context(), Input{Login: "Not A Login", Name: "", Email: "x", Role: RoleViewer}, "short")
		if !problems(err) {
			t.Errorf("CreateFirst() = %v, want problems of the input", err)
		}
		e.unchanged(t, before)
	})

	t.Run("ChangePassword that finds the password changed", func(t *testing.T) {
		r := setup(t)
		var after mgrMark
		r.e.m.hook = func(point string) {
			if point == "hashed" {
				r.e.m.hook = nil
				r.e.edit(t, r.peer.ID, func(x *row) { x.Password = mgrHashed(t, "a password that someone set") })
				after = r.e.mark(t)
			}
		}
		if err := r.e.m.ChangePassword(t.Context(), r.peer.ID, mgrPassword("peer"), endPassword); !errors.Is(err, ErrConflict) {
			t.Errorf("ChangePassword() = %v, want ErrConflict", err)
		}
		r.e.unchanged(t, after)
	})
}

// TestAccountRecordsOfOddRows checks that an account whose row has values that another build wrote, such as a login name
// with a control character and a role of more than a hundred bytes, can be operated on, and that the record keeps of
// these values what a record can hold.
func TestAccountRecordsOfOddRows(t *testing.T) {
	e := newMgr(t)
	ctx := t.Context()
	admin := e.first(t)
	odd := mgrRow(7, "odd", RoleViewer)
	odd.Login = "bad" + string(rune(7)) + strings.Repeat("x", 100)
	oddRole := "owner" + strings.Repeat("e", 100)
	odd.Role = Role(oddRole)
	e.set(t, odd)
	fffd := string(rune(0xfffd))
	wantLogin := "bad" + fffd + strings.Repeat("x", 58)
	if len(wantLogin) != 64 {
		t.Fatalf("the test expects a login name of %d bytes, want 64", len(wantLogin))
	}

	step := func(name string, do func() error, want audit.Record) {
		t.Helper()
		before := e.mark(t)
		if err := do(); err != nil {
			t.Fatalf("%s = %v", name, err)
		}
		histWant(t, e.activityRecords(t, before), want)
	}
	step("Disable", func() error {
		_, err := e.m.Disable(ctx, By(admin.ID), odd.ID)
		return err
	}, activityRecord(admin.ID, audit.AccountDisable, odd.ID, true, recDetail{
		"fields": "status", "statusBefore": "active", "statusAfter": "disabled",
	}))
	step("Update of the role", func() error {
		_, err := e.m.Update(ctx, System, odd.ID, Change{Role: ptr(RoleEditor)})
		return err
	}, activityRecord(audit.System, audit.AccountUpdate, odd.ID, true, recDetail{
		"fields": "role", "roleBefore": oddRole[:64], "roleAfter": "editor",
	}))
	step("Delete", func() error {
		return e.m.Delete(ctx, System, odd.ID)
	}, activityRecord(audit.System, audit.AccountDelete, odd.ID, true, recDetail{
		"loginBefore": wantLogin, "roleBefore": "editor", "statusBefore": "disabled",
	}))
}

// TestRecordAccountFailsTheUpdate checks that a record that the store refuses fails the Update that holds it, and that the
// row written before it is gone too: the row and its record are one commit.
func TestRecordAccountFailsTheUpdate(t *testing.T) {
	e := newMgr(t)
	r := mgrRow(7, "x", RoleViewer)
	before := e.mark(t)
	err := e.s.Update(t.Context(), func(tx *store.Tx) error {
		if err := putRow(tx, r); err != nil {
			return err
		}
		return recordAccount(tx, audit.AccountUpdate, "not a user ID", r.ID, nil)
	})
	if err == nil || !strings.HasPrefix(err.Error(), "audit record: actor") {
		t.Errorf("Update() = %v, want the error of the record", err)
	}
	e.unchanged(t, before)
}

func TestRecordedActor(t *testing.T) {
	for _, tt := range []struct {
		by   Actor
		want string
	}{
		{By(mgrMissingID), mgrMissingID},
		{System, "system"},
		{Actor{}, ""},
	} {
		if got := tt.by.recorded(); got != tt.want {
			t.Errorf("recorded() of %+v = %q, want %q", tt.by, got, tt.want)
		}
	}
}

func TestChangeDetail(t *testing.T) {
	base := mgrRow(1, "yamada", RoleEditor)
	base.Author = mgrAuthorID
	base.Token = tokenRow{Purpose: PurposeReset, Hash: testTokenHash(0xcd), ExpiresAt: mgrTime}
	tests := []struct {
		name   string
		change func(r *row)
		want   recDetail
	}{
		{"nothing", func(*row) {}, nil},
		{"the times and the creator, which a record does not name", func(r *row) {
			r.CreatedAt, r.CreatedBy, r.CredentialsChangedAt = r.CreatedAt.Add(time.Hour), mgrMissingID, r.CredentialsChangedAt.Add(time.Hour)
		}, nil},
		{"the login name", func(r *row) { r.Login = "other" }, recDetail{"fields": "login", "loginBefore": "yamada", "loginAfter": "other"}},
		{"the name", func(r *row) { r.Name = "Another Name" }, recDetail{"fields": "name"}},
		{"the email address", func(r *row) { r.Email = "other@example.org" }, recDetail{"fields": "email"}},
		{"the role", func(r *row) { r.Role = RoleViewer }, recDetail{"fields": "role", "roleBefore": "editor", "roleAfter": "viewer"}},
		{"a role that is none", func(r *row) { r.Role = "boss" }, recDetail{"fields": "role", "roleBefore": "editor", "roleAfter": "boss"}},
		{"a role with a line feed, which a value of the detail may have", func(r *row) { r.Role = "a\nb" }, recDetail{
			"fields": "role", "roleBefore": "editor", "roleAfter": "a\nb",
		}},
		{"another link", func(r *row) { r.Author = mgrOtherAuthorID }, recDetail{
			"fields": "author", "authorBefore": mgrAuthorID, "authorAfter": mgrOtherAuthorID,
		}},
		{"no link", func(r *row) { r.Author = "" }, recDetail{"fields": "author", "authorBefore": mgrAuthorID}},
		{"the password", func(r *row) { r.Password = testPasswordHash(2) }, recDetail{"fields": "password"}},
		{"the token", func(r *row) { r.Token.Hash = testTokenHash(0xef) }, recDetail{"fields": "token"}},
		{"no token", func(r *row) { r.Token = tokenRow{} }, recDetail{"fields": "token"}},
		{"disabled", func(r *row) { r.Disabled = true }, recDetail{"fields": "status", "statusBefore": "active", "statusAfter": "disabled"}},
		{"no credential", func(r *row) { r.Password = "" }, recDetail{
			"fields": "password, status", "statusBefore": "active", "statusAfter": "pending",
		}},
		{"everything", func(r *row) {
			r.Login, r.Name, r.Email, r.Role, r.Author = "other", "Another Name", "other@example.org", RoleViewer, ""
			r.Password, r.Disabled, r.Token = testPasswordHash(2), true, tokenRow{}
		}, recDetail{
			"fields": "author, email, login, name, password, role, status, token", "authorBefore": mgrAuthorID,
			"loginBefore": "yamada", "loginAfter": "other", "roleBefore": "editor", "roleAfter": "viewer",
			"statusBefore": "active", "statusAfter": "disabled",
		}},
	}
	for _, tt := range tests {
		after := base
		tt.change(&after)
		got := changeDetail(base, after)
		if len(got) != len(tt.want) || len(got) > 0 && !reflect.DeepEqual(got, tt.want) {
			t.Errorf("changeDetail() after %s = %v, want %v", tt.name, got, tt.want)
		}
		r := audit.Record{Log: audit.ActivityLog, Action: audit.AccountUpdate, Detail: got}
		if err := r.Check(); err != nil {
			t.Errorf("the record after %s is refused: %v", tt.name, err)
		}
		// Neither the name nor the email address, nor a password or token hash, is in the detail.
		for key, value := range got {
			for _, secret := range []string{base.Name, after.Name, base.Email, after.Email, base.Password, after.Password, base.Token.Hash, after.Token.Hash} {
				if secret != "" && strings.Contains(value, secret) {
					t.Errorf("the detail %s after %s has a value that a record must not keep", key, tt.name)
				}
			}
		}
	}
}

func TestCreatedAndDeletedDetail(t *testing.T) {
	r := mgrRow(1, "yamada", RoleEditor)
	r.Author = mgrAuthorID
	if got, want := createdDetail(r), (recDetail{
		"authorAfter": mgrAuthorID, "loginAfter": "yamada", "roleAfter": "editor", "statusAfter": "active",
	}); !reflect.DeepEqual(got, want) {
		t.Errorf("createdDetail() = %v, want %v", got, want)
	}
	r.Author, r.Password = "", ""
	if got, want := createdDetail(r), (recDetail{"loginAfter": "yamada", "roleAfter": "editor", "statusAfter": "pending"}); !reflect.DeepEqual(got, want) {
		t.Errorf("createdDetail() without a link and a credential = %v, want %v", got, want)
	}
	r.Disabled = true
	if got, want := deletedDetail(r), (recDetail{"loginBefore": "yamada", "roleBefore": "editor", "statusBefore": "disabled"}); !reflect.DeepEqual(got, want) {
		t.Errorf("deletedDetail() = %v, want %v", got, want)
	}

	detail := recDetail{}
	addToken(detail, tokenRow{Purpose: PurposeInvitation, Hash: testTokenHash(0xcd), ExpiresAt: mgrTime.In(time.FixedZone("JST", 9*3600))})
	if want := (recDetail{"tokenPurpose": "invitation", "tokenExpiresAt": "2026-11-08T12:00:00.123456Z"}); !reflect.DeepEqual(detail, want) {
		t.Errorf("addToken() made %v, want %v", detail, want)
	}
}
