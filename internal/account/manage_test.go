package account

import (
	"errors"
	"io/fs"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

func TestUpdateOwnNameAndEmail(t *testing.T) {
	for _, role := range Roles() {
		t.Run(string(role), func(t *testing.T) {
			e := newMgr(t)
			e.first(t)
			a := e.add(t, "writer", role)
			before := e.row(t, a.ID)
			got, err := e.m.Update(t.Context(), By(a.ID), a.ID, Change{Name: ptr("  New Name "), Email: ptr("New.Writer@Example.COM")})
			if err != nil {
				t.Fatalf("Update() = %v", err)
			}
			want := a
			want.Name, want.Email = "  New Name ", "New.Writer@example.com"
			if got != want {
				t.Errorf("Update() = %+v, want %+v", got, want)
			}
			// Nothing else of the row changes, not the credentials.
			wantRow := before
			wantRow.Name, wantRow.Email = want.Name, want.Email
			if after := e.row(t, a.ID); after != wantRow {
				t.Error("the stored row changed in more than the name and the email address")
			}
			if g, err := e.m.Get(t.Context(), a.ID); err != nil || g != want {
				t.Errorf("Get() = %+v, %v, want %+v", g, err, want)
			}
			if _, err := e.m.VerifyPasswordFactor(t.Context(), "writer", mgrPassword("writer")); err != nil {
				t.Errorf("VerifyPasswordFactor() after the update = %v, want the password to work", err)
			}
		})
	}
}

func TestUpdateOwnLoginRoleAndAuthor(t *testing.T) {
	changes := map[string]Change{
		"login":               {Login: ptr("other")},
		"role":                {Role: ptr(RoleAdmin)},
		"the same role":       {Role: ptr(RoleEditor)},
		"author":              {Author: ptr(mgrAuthorID)},
		"no author":           {Author: ptr("")},
		"the name and a role": {Name: ptr("New Name"), Role: ptr(RoleAdmin)},
	}
	for _, role := range []Role{RoleEditor, RoleAuthor, RoleViewer} {
		e := newMgr(t)
		e.first(t)
		e.putAuthor(t, mgrAuthorID)
		a := e.add(t, "writer", role)
		before := e.mark(t)
		for name, c := range changes {
			_, err := e.m.Update(t.Context(), By(a.ID), a.ID, c)
			mgrError(t, err, ErrForbidden, mgrNotAllowed(a, "manage accounts"))
			if t.Failed() {
				t.Logf("the change of %s by the %s", name, role)
			}
		}
		e.unchanged(t, before)
	}
}

func TestUpdateOtherAccounts(t *testing.T) {
	e := newMgr(t)
	admin := e.first(t)
	e.putAuthor(t, mgrAuthorID)
	a := e.add(t, "writer", RoleAuthor)
	steps := []struct {
		name   string
		change Change
		apply  func(*Account)
	}{
		{"the login name", Change{Login: ptr(" New.Login ")}, func(a *Account) { a.Login = "new.login" }},
		{"the name", Change{Name: ptr("New Name")}, func(a *Account) { a.Name = "New Name" }},
		{"the email address", Change{Email: ptr("Writer@Example.ORG")}, func(a *Account) { a.Email = "Writer@example.org" }},
		{"the role", Change{Role: ptr(RoleEditor)}, func(a *Account) { a.Role = RoleEditor }},
		{"the author profile", Change{Author: ptr(" " + mgrAuthorID + " ")}, func(a *Account) { a.Author = mgrAuthorID }},
		{"no author profile", Change{Author: ptr("")}, func(a *Account) { a.Author = "" }},
		{"everything", Change{Login: ptr("all"), Name: ptr("All"), Email: ptr("all@example.com"), Role: ptr(RoleViewer), Author: ptr(mgrAuthorID)},
			func(a *Account) {
				a.Login, a.Name, a.Email, a.Role, a.Author = "all", "All", "all@example.com", RoleViewer, mgrAuthorID
			}},
	}
	for _, by := range []struct {
		name string
		who  Actor
	}{{"an administrator", By(admin.ID)}, {"System", System}} {
		want, err := e.m.Get(t.Context(), a.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, step := range steps {
			step.apply(&want)
			got, err := e.m.Update(t.Context(), by.who, a.ID, step.change)
			if err != nil || got != want {
				t.Fatalf("Update() of %s by %s = %+v, %v, want %+v", step.name, by.name, got, err, want)
			}
			if stored, err := e.m.Get(t.Context(), a.ID); err != nil || stored != want {
				t.Fatalf("Get() after the update of %s by %s = %+v, %v, want %+v", step.name, by.name, stored, err, want)
			}
		}
	}
	// The password, the times and the creator stay.
	r := e.row(t, a.ID)
	if r.Password == "" || !r.CreatedAt.Equal(mgrTime) || r.CreatedBy != "" || !r.CredentialsChangedAt.Equal(mgrTime) {
		t.Error("the update changed the credentials, the times or the creator")
	}
	if _, err := e.m.VerifyPasswordFactor(t.Context(), "all", mgrPassword("writer")); err != nil {
		t.Errorf("VerifyPasswordFactor() with the new login name = %v, want the old password to work", err)
	}
}

func TestUpdateOtherAccountsByOthers(t *testing.T) {
	e := newMgr(t)
	e.first(t)
	target := e.add(t, "target", RoleEditor)
	for _, role := range []Role{RoleEditor, RoleAuthor, RoleViewer} {
		by := e.add(t, "by-"+string(role), role)
		before := e.mark(t)
		for _, c := range []Change{{Name: ptr("Taken over")}, {Email: ptr("taken@example.com")}, {Role: ptr(RoleAdmin)}, {}} {
			_, err := e.m.Update(t.Context(), By(by.ID), target.ID, c)
			mgrError(t, err, ErrForbidden, mgrNotAllowed(by, "manage accounts"))
		}
		e.unchanged(t, before)
	}
}

func TestUpdateNotActive(t *testing.T) {
	e := newMgr(t)
	e.first(t)
	pending, _ := e.invite(t, "pending", RoleEditor)
	off := e.add(t, "off", RoleEditor)
	if _, err := e.m.Disable(t.Context(), System, off.ID); err != nil {
		t.Fatal(err)
	}
	pending.Status, off.Status = StatusPending, StatusDisabled
	before := e.mark(t)
	for _, a := range []Account{pending, off} {
		// An account that is not active may not even change its own name, nor make an empty change.
		for _, c := range []Change{{Name: ptr("New Name")}, {}} {
			_, err := e.m.Update(t.Context(), By(a.ID), a.ID, c)
			mgrError(t, err, ErrForbidden, mgrNotAllowed(a, "edit its own account"))
		}
	}
	e.unchanged(t, before)
	// System may change them.
	for _, a := range []Account{pending, off} {
		before := e.row(t, a.ID)
		got, err := e.m.Update(t.Context(), System, a.ID, Change{Name: ptr("Renamed")})
		if err != nil || got.Name != "Renamed" || got.Status != a.Status || got.Token != a.Token {
			t.Errorf("Update() of the %s account = %+v, %v, want the new name, and the status and token as they were", a.Status, got, err)
		}
		want := before
		want.Name = "Renamed"
		if after := e.row(t, a.ID); after != want {
			t.Errorf("the row of the %s account changed in more than the name", a.Status)
		}
	}
}

func TestUpdateNothing(t *testing.T) {
	e := newMgr(t)
	admin := e.first(t)
	a := e.add(t, "writer", RoleAuthor)
	e.clock.add(time.Hour)
	before := e.mark(t)
	same := []Change{
		{}, {Name: ptr(a.Name)}, {Login: ptr("WRITER")}, {Login: ptr(" writer ")}, {Email: ptr(a.Email)}, {Role: ptr(RoleAuthor)},
		{Author: ptr("")}, {Login: ptr(a.Login), Name: ptr(a.Name), Email: ptr(a.Email), Role: ptr(a.Role), Author: ptr(a.Author)},
	}
	var names []string
	var want []audit.Record
	for i, c := range same {
		for _, by := range []Actor{By(a.ID), By(admin.ID), System} {
			if c.Login != nil || c.Role != nil || c.Author != nil {
				if by == By(a.ID) {
					continue
				}
			}
			got, err := e.m.Update(t.Context(), by, a.ID, c)
			if err != nil || got != a {
				t.Errorf("Update() with the change %d = %+v, %v, want the account as it is", i, got, err)
			}
			names = append(names, "account.update")
			want = append(want, activityRecord(by.recorded(), audit.AccountUpdate, a.ID, false, nil))
		}
	}
	// No row is written, and each change says in its record that it changed nothing.
	e.recorded(t, before, names...)
	histWant(t, e.activityRecords(t, before), want...)
}

func TestUpdateProblems(t *testing.T) {
	e := newMgr(t)
	e.first(t)
	e.putAuthor(t, mgrAuthorID)
	yamada := e.add(t, "yamada", RoleEditor)
	pending, _ := e.invite(t, "pending", RoleViewer)
	a := e.add(t, "writer", RoleAuthor)
	if _, err := e.m.Update(t.Context(), System, yamada.ID, Change{Email: ptr("Yamada@example.com")}); err != nil {
		t.Fatal(err)
	}
	before := e.mark(t)
	tests := []struct {
		name   string
		change Change
		want   []problem
	}{
		{"the login name of another account", Change{Login: ptr("yamada")}, []problem{{"/login", `login name "yamada" is already used by ` + yamada.ID}}},
		{"the login name in upper case", Change{Login: ptr(" YAMADA ")}, []problem{{"/login", `login name "yamada" is already used by ` + yamada.ID}}},
		{"the login name of a pending account", Change{Login: ptr("pending")}, []problem{{"/login", `login name "pending" is already used by ` + pending.ID}}},
		{"the email address of another account", Change{Email: ptr("yamada@example.com")}, []problem{{"/email", `email address "yamada@example.com" is already used by ` + yamada.ID}}},
		{"the email address in upper case", Change{Email: ptr("YAMADA@EXAMPLE.COM")}, []problem{{"/email", `email address "YAMADA@example.com" is already used by ` + yamada.ID}}},
		{"both", Change{Login: ptr("yamada"), Email: ptr("pending@example.com")}, []problem{
			{"/login", `login name "yamada" is already used by ` + yamada.ID},
			{"/email", `email address "pending@example.com" is already used by ` + pending.ID},
		}},
		{"an author profile that is missing", Change{Author: ptr(mgrOtherAuthorID)}, []problem{{"/author", `author profile "` + mgrOtherAuthorID + `" does not exist`}}},
		{"the login name and an author profile that is missing", Change{Login: ptr("yamada"), Author: ptr(mgrOtherAuthorID)}, []problem{
			{"/login", `login name "yamada" is already used by ` + yamada.ID},
			{"/author", `author profile "` + mgrOtherAuthorID + `" does not exist`},
		}},
		{"every field is invalid", Change{Login: ptr("Not A Login"), Name: ptr(""), Email: ptr("x"), Role: ptr(Role("boss")), Author: ptr("x")}, []problem{
			{"/login", badLogin("not a login")}, {"/name", "missing name"},
			{"/email", `invalid email address "x": needs exactly one @`},
			{"/role", `unknown role "boss"; use admin, editor, author or viewer`}, {"/author", `invalid author ID "x": ` + badFormat},
		}},
		{"an invalid field alone", Change{Login: ptr("yamada"), Name: ptr("")}, []problem{{"/name", "missing name"}}},
		{"no role", Change{Role: ptr(Role(""))}, []problem{{"/role", "missing role"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := e.m.Update(t.Context(), System, a.ID, tt.change)
			mgrProblems(t, err, tt.want...)
			if got != (Account{}) {
				t.Errorf("Update() returned %+v with an error", got)
			}
			e.unchanged(t, before)
		})
	}
	// Its own values are not duplicates of another account.
	if got, err := e.m.Update(t.Context(), System, yamada.ID, Change{Login: ptr("yamada"), Email: ptr("YAMADA@example.com")}); err != nil || got.Email != "YAMADA@example.com" {
		t.Errorf("Update() of the email address in another case = %+v, %v, want the new case", got, err)
	}
}

// TestUpdateEmailAddressTaken checks that an account that changes its own email address to one that another account
// has is not told which account that is, while the same change by an administrator of another account, or by System,
// names it.
func TestUpdateEmailAddressTaken(t *testing.T) {
	e := newMgr(t)
	admin := e.first(t)
	yamada := e.add(t, "yamada", RoleEditor)
	pending, _ := e.invite(t, "pending", RoleViewer)
	writer := e.add(t, "writer", RoleAuthor)
	before := e.mark(t)
	anonymous := func(email string) problem {
		return problem{"/email", `email address "` + email + `" is already used by another account`}
	}
	named := func(email, id string) problem {
		return problem{"/email", `email address "` + email + `" is already used by ` + id}
	}
	tests := []struct {
		name   string
		by     Actor
		id     string
		change Change
		want   problem
	}{
		{"an author changes its own", By(writer.ID), writer.ID, Change{Email: ptr("yamada@example.com")}, anonymous("yamada@example.com")},
		{"the address of a pending account", By(writer.ID), writer.ID, Change{Email: ptr("Pending@Example.com")}, anonymous("Pending@example.com")},
		{"the address in another case", By(writer.ID), writer.ID, Change{Email: ptr("YAMADA@EXAMPLE.COM")}, anonymous("YAMADA@example.com")},
		{"the name and the address", By(writer.ID), writer.ID, Change{Name: ptr("New Name"), Email: ptr("yamada@example.com")}, anonymous("yamada@example.com")},
		{"an editor changes its own", By(yamada.ID), yamada.ID, Change{Email: ptr("writer@example.com")}, anonymous("writer@example.com")},
		{"an administrator changes its own", By(admin.ID), admin.ID, Change{Email: ptr("yamada@example.com")}, anonymous("yamada@example.com")},
		{"an administrator changes another", By(admin.ID), writer.ID, Change{Email: ptr("yamada@example.com")}, named("yamada@example.com", yamada.ID)},
		{"an administrator changes another to a pending address", By(admin.ID), writer.ID, Change{Email: ptr("pending@example.com")}, named("pending@example.com", pending.ID)},
		{"an administrator changes its own address and its role", By(admin.ID), admin.ID, Change{Email: ptr("yamada@example.com"), Role: ptr(RoleAdmin)}, named("yamada@example.com", yamada.ID)},
		{"System changes", System, writer.ID, Change{Email: ptr("yamada@example.com")}, named("yamada@example.com", yamada.ID)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := e.m.Update(t.Context(), tt.by, tt.id, tt.change)
			mgrProblems(t, err, tt.want)
			if got != (Account{}) {
				t.Errorf("Update() returned %+v with an error", got)
			}
			if err != nil && strings.HasSuffix(tt.want.msg, "another account") && strings.Contains(err.Error(), "usr_") {
				t.Errorf("the error %q has the ID of an account", err)
			}
			e.unchanged(t, before)
		})
	}
}

func TestUpdateAuthorProfile(t *testing.T) {
	e := newMgr(t)
	e.first(t)
	e.putAuthor(t, mgrAuthorID)
	in := mgrInput("writer", RoleAuthor)
	in.Author = mgrAuthorID
	a, _, err := e.m.Invite(t.Context(), System, in)
	if err != nil {
		t.Fatal(err)
	}
	// The profile is deleted later: the link stays, and an update of another field does not look at it.
	err = e.s.Update(t.Context(), storeWrite(func(tx *store.Tx) error {
		_, err := tx.Delete(content.RefAuthor, mgrAuthorID, testCreatorID)
		return err
	}))
	if err != nil {
		t.Fatal(err)
	}
	got, err := e.m.Update(t.Context(), System, a.ID, Change{Name: ptr("Renamed")})
	if err != nil || got.Author != mgrAuthorID {
		t.Fatalf("Update() = %+v, %v, want the link to the deleted profile to stay", got, err)
	}
	if got, err := e.m.Update(t.Context(), System, a.ID, Change{Author: ptr(mgrAuthorID)}); err != nil || got.Author != mgrAuthorID {
		t.Errorf("Update() with the link as it is = %+v, %v, want no problem", got, err)
	}
	// A new link is checked.
	mgrProblems(t, mgrErr(e.m.Update(t.Context(), System, a.ID, Change{Author: ptr(mgrOtherAuthorID)})),
		problem{"/author", `author profile "` + mgrOtherAuthorID + `" does not exist`})
	if got, err := e.m.Update(t.Context(), System, a.ID, Change{Author: ptr("")}); err != nil || got.Author != "" {
		t.Errorf("Update() to remove the link = %+v, %v, want none", got, err)
	}
	mgrProblems(t, mgrErr(e.m.Update(t.Context(), System, a.ID, Change{Author: ptr(mgrAuthorID)})),
		problem{"/author", `author profile "` + mgrAuthorID + `" does not exist`})
	// Any number of accounts may link to a profile.
	e.putAuthor(t, mgrOtherAuthorID)
	for _, login := range []string{"one", "two", "three"} {
		in := mgrInput(login, RoleAuthor)
		in.Author = mgrOtherAuthorID
		if _, _, err := e.m.Invite(t.Context(), System, in); err != nil {
			t.Errorf("Invite() of %s with the profile of the others = %v, want nil", login, err)
		}
	}
}

func mgrErr(_ Account, err error) error {
	return err
}

func TestUpdateDoesNotWriteThroughThePointers(t *testing.T) {
	e := newMgr(t)
	e.first(t)
	a := e.add(t, "writer", RoleAuthor)
	login, name, email, author := " NEW ", " Name ", "New@Example.COM", " "
	c := Change{Login: &login, Name: &name, Email: &email, Author: &author}
	if _, err := e.m.Update(t.Context(), System, a.ID, c); err != nil {
		t.Fatal(err)
	}
	if login != " NEW " || name != " Name " || email != "New@Example.COM" || author != " " {
		t.Error("Update() changed the values that the Change points to")
	}
	if c.Login != &login || c.Email != &email {
		t.Error("Update() changed the Change of the caller")
	}
}

func TestUpdateMissing(t *testing.T) {
	e := newMgr(t)
	admin := e.first(t)
	editor := e.add(t, "editor", RoleEditor)
	before := e.mark(t)
	for _, by := range []Actor{System, By(admin.ID)} {
		_, err := e.m.Update(t.Context(), by, mgrMissingID, Change{Name: ptr("x")})
		mgrError(t, err, fs.ErrNotExist, mgrNotFound(mgrMissingID))
	}
	// The actor that has no right learns nothing.
	_, err := e.m.Update(t.Context(), By(editor.ID), mgrMissingID, Change{Name: ptr("x")})
	mgrError(t, err, ErrForbidden, mgrNotAllowed(editor, "manage accounts"))
	// An empty ID is the account of nobody, also for an actor with no ID.
	_, err = e.m.Update(t.Context(), Actor{}, "", Change{})
	mgrError(t, err, ErrForbidden, "not allowed: the operation has no actor")
	_, err = e.m.Update(t.Context(), System, "", Change{})
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Update() of an empty ID = %v, want fs.ErrNotExist", err)
	}
	e.unchanged(t, before)
}

func TestUpdateLastAdmin(t *testing.T) {
	const ending = "its role cannot be changed"
	t.Run("by itself", func(t *testing.T) {
		e := newMgr(t)
		admin := e.first(t)
		before := e.mark(t)
		for _, role := range []Role{RoleEditor, RoleAuthor, RoleViewer} {
			got, err := e.m.Update(t.Context(), By(admin.ID), admin.ID, Change{Role: ptr(role)})
			mgrError(t, err, ErrLastAdmin, mgrLastAdmin(admin.ID, ending))
			if got != (Account{}) {
				t.Errorf("Update() returned %+v with an error", got)
			}
		}
		e.unchanged(t, before)
	})
	t.Run("by System", func(t *testing.T) {
		e := newMgr(t)
		admin := e.first(t)
		before := e.mark(t)
		_, err := e.m.Update(t.Context(), System, admin.ID, Change{Role: ptr(RoleEditor)})
		mgrError(t, err, ErrLastAdmin, mgrLastAdmin(admin.ID, ending))
		e.unchanged(t, before)
	})
	t.Run("the other fields are free", func(t *testing.T) {
		e := newMgr(t)
		admin := e.first(t)
		got, err := e.m.Update(t.Context(), By(admin.ID), admin.ID, Change{Login: ptr("root"), Name: ptr("Root"), Email: ptr("root@example.com"), Role: ptr(RoleAdmin)})
		if err != nil || got.Login != "root" || got.Role != RoleAdmin || got.Status != StatusActive {
			t.Errorf("Update() = %+v, %v, want the new fields of an active administrator", got, err)
		}
	})
	t.Run("another active administrator", func(t *testing.T) {
		e := newMgr(t)
		a, b := e.first(t), e.add(t, "boss", RoleAdmin)
		if _, err := e.m.Update(t.Context(), By(a.ID), a.ID, Change{Role: ptr(RoleEditor)}); err != nil {
			t.Fatalf("Update() of the role of one of two administrators = %v, want nil", err)
		}
		// Now b is the last, and a is an editor, who may not change anything of b.
		_, err := e.m.Update(t.Context(), By(b.ID), b.ID, Change{Role: ptr(RoleViewer)})
		mgrError(t, err, ErrLastAdmin, mgrLastAdmin(b.ID, ending))
		_, err = e.m.Update(t.Context(), By(a.ID), b.ID, Change{Role: ptr(RoleViewer)})
		mgrError(t, err, ErrForbidden, mgrNotAllowed(Account{ID: a.ID, Role: RoleEditor, Status: StatusActive}, "manage accounts"))
	})
	t.Run("a pending administrator does not count", func(t *testing.T) {
		e := newMgr(t)
		a := e.first(t)
		e.invite(t, "boss", RoleAdmin)
		_, err := e.m.Update(t.Context(), System, a.ID, Change{Role: ptr(RoleEditor)})
		mgrError(t, err, ErrLastAdmin, mgrLastAdmin(a.ID, ending))
	})
	t.Run("a disabled administrator does not count", func(t *testing.T) {
		e := newMgr(t)
		a, b := e.first(t), e.add(t, "boss", RoleAdmin)
		if _, err := e.m.Disable(t.Context(), System, b.ID); err != nil {
			t.Fatal(err)
		}
		_, err := e.m.Update(t.Context(), System, a.ID, Change{Role: ptr(RoleEditor)})
		mgrError(t, err, ErrLastAdmin, mgrLastAdmin(a.ID, ending))
	})
	t.Run("an administrator that cannot sign in", func(t *testing.T) {
		e := newMgr(t)
		e.first(t)
		pending, _ := e.invite(t, "boss", RoleAdmin)
		off := e.add(t, "off", RoleAdmin)
		if _, err := e.m.Disable(t.Context(), System, off.ID); err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{pending.ID, off.ID} {
			if got, err := e.m.Update(t.Context(), System, id, Change{Role: ptr(RoleViewer)}); err != nil || got.Role != RoleViewer {
				t.Errorf("Update() of the role of %s = %+v, %v, want the new role", id, got, err)
			}
		}
	})
	t.Run("a new administrator", func(t *testing.T) {
		e := newMgr(t)
		e.first(t)
		editor := e.add(t, "editor", RoleEditor)
		if got, err := e.m.Update(t.Context(), System, editor.ID, Change{Role: ptr(RoleAdmin)}); err != nil || got.Role != RoleAdmin {
			t.Errorf("Update() = %+v, %v, want an administrator", got, err)
		}
	})
}

func TestDisableAndEnable(t *testing.T) {
	e := newMgr(t)
	admin := e.first(t)
	active := e.add(t, "active", RoleEditor)
	invited, token := e.invite(t, "invited", RoleViewer)
	other := e.raw(t)[admin.ID]
	beforeActive, beforeInvited := e.row(t, active.ID), e.row(t, invited.ID)

	got, err := e.m.Disable(t.Context(), By(admin.ID), active.ID)
	want := active
	want.Status = StatusDisabled
	if err != nil || got != want {
		t.Fatalf("Disable() = %+v, %v, want %+v", got, err, want)
	}
	// It keeps every field but the token, the credentials too.
	wantRow := beforeActive
	wantRow.Disabled = true
	if after := e.row(t, active.ID); after != wantRow {
		t.Error("the row of the disabled account changed in more than disabled")
	}
	if _, err := e.m.VerifyPasswordFactor(t.Context(), "active", mgrPassword("active")); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("VerifyPasswordFactor() of a disabled account = %v, want ErrBadCredentials", err)
	}

	got, err = e.m.Enable(t.Context(), By(admin.ID), active.ID)
	if err != nil || got != active {
		t.Fatalf("Enable() = %+v, %v, want %+v", got, err, active)
	}
	if after := e.row(t, active.ID); after != beforeActive {
		t.Error("the row after Disable and Enable is not the one before")
	}
	if _, err := e.m.VerifyPasswordFactor(t.Context(), "active", mgrPassword("active")); err != nil {
		t.Errorf("VerifyPasswordFactor() after Enable = %v, want the password to work", err)
	}

	// An account with a token loses it, and has none again after Enable: it is pending, and needs a reset.
	got, err = e.m.Disable(t.Context(), System, invited.ID)
	want = invited
	want.Status, want.Token = StatusDisabled, TokenInfo{}
	if err != nil || got != want {
		t.Fatalf("Disable() of an invited account = %+v, %v, want %+v", got, err, want)
	}
	if _, err := e.m.SetPassword(t.Context(), mgrClient, token, mgrPassword("invited")); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("SetPassword() of a disabled account = %v, want ErrInvalidToken", err)
	}
	got, err = e.m.Enable(t.Context(), System, invited.ID)
	want.Status = StatusPending
	if err != nil || got != want {
		t.Fatalf("Enable() of an account without a credential = %+v, %v, want %+v", got, err, want)
	}
	if _, err := e.m.SetPassword(t.Context(), mgrClient, token, mgrPassword("invited")); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("SetPassword() after Enable = %v, want ErrInvalidToken", err)
	}
	wantRow = beforeInvited
	wantRow.Token = tokenRow{}
	if after := e.row(t, invited.ID); after != wantRow {
		t.Error("the row of the invited account changed in more than the token")
	}
	next, err := e.m.Reset(t.Context(), By(admin.ID), invited.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.SetPassword(t.Context(), mgrClient, next, mgrPassword("invited")); err != nil {
		t.Errorf("SetPassword() with the token of the reset = %v, want nil", err)
	}
	if e.raw(t)[admin.ID] != other {
		t.Error("the row of another account changed")
	}
}

func TestDisableAndEnableAlreadyDone(t *testing.T) {
	e := newMgr(t)
	e.first(t)
	a := e.add(t, "yamada", RoleEditor)
	before := e.mark(t)
	// An account that is enabled is enabled already.
	if got, err := e.m.Enable(t.Context(), System, a.ID); err != nil || got != a {
		t.Errorf("Enable() of an enabled account = %+v, %v, want %+v", got, err, a)
	}
	// Nothing is written but the record, which says that nothing changed.
	e.recorded(t, before, "account.enable")
	histWant(t, e.activityRecords(t, before), activityRecord(audit.System, audit.AccountEnable, a.ID, false, nil))
	if _, err := e.m.Disable(t.Context(), System, a.ID); err != nil {
		t.Fatal(err)
	}
	before = e.mark(t)
	want := a
	want.Status = StatusDisabled
	if got, err := e.m.Disable(t.Context(), System, a.ID); err != nil || got != want {
		t.Errorf("Disable() of a disabled account = %+v, %v, want %+v", got, err, want)
	}
	e.recorded(t, before, "account.disable")
	histWant(t, e.activityRecords(t, before), activityRecord(audit.System, audit.AccountDisable, a.ID, false, nil))
}

func TestDisableLastAdmin(t *testing.T) {
	const ending = "it cannot be disabled"
	t.Run("by itself", func(t *testing.T) {
		e := newMgr(t)
		admin := e.first(t)
		before := e.mark(t)
		got, err := e.m.Disable(t.Context(), By(admin.ID), admin.ID)
		mgrError(t, err, ErrLastAdmin, mgrLastAdmin(admin.ID, ending))
		if got != (Account{}) {
			t.Errorf("Disable() returned %+v with an error", got)
		}
		e.unchanged(t, before)
	})
	t.Run("by System", func(t *testing.T) {
		e := newMgr(t)
		admin := e.first(t)
		before := e.mark(t)
		_, err := e.m.Disable(t.Context(), System, admin.ID)
		mgrError(t, err, ErrLastAdmin, mgrLastAdmin(admin.ID, ending))
		e.unchanged(t, before)
	})
	t.Run("one of two, and then the other", func(t *testing.T) {
		e := newMgr(t)
		a, b := e.first(t), e.add(t, "boss", RoleAdmin)
		if _, err := e.m.Disable(t.Context(), By(a.ID), b.ID); err != nil {
			t.Fatalf("Disable() of one of two administrators = %v, want nil", err)
		}
		_, err := e.m.Disable(t.Context(), By(a.ID), a.ID)
		mgrError(t, err, ErrLastAdmin, mgrLastAdmin(a.ID, ending))
		// The one that is disabled cannot act, and the other cannot be disabled by System either.
		_, err = e.m.Disable(t.Context(), System, a.ID)
		mgrError(t, err, ErrLastAdmin, mgrLastAdmin(a.ID, ending))
		_, err = e.m.Disable(t.Context(), By(b.ID), a.ID)
		mgrError(t, err, ErrForbidden, mgrNotAllowed(Account{ID: b.ID, Role: RoleAdmin, Status: StatusDisabled}, "manage accounts"))
		// With b enabled again, a may be disabled.
		if _, err := e.m.Enable(t.Context(), By(a.ID), b.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := e.m.Disable(t.Context(), By(b.ID), a.ID); err != nil {
			t.Errorf("Disable() after Enable = %v, want nil", err)
		}
	})
	t.Run("a pending administrator does not count", func(t *testing.T) {
		e := newMgr(t)
		a := e.first(t)
		e.invite(t, "boss", RoleAdmin)
		_, err := e.m.Disable(t.Context(), System, a.ID)
		mgrError(t, err, ErrLastAdmin, mgrLastAdmin(a.ID, ending))
	})
	t.Run("an administrator that is reset does not count", func(t *testing.T) {
		e := newMgr(t)
		a, b := e.first(t), e.add(t, "boss", RoleAdmin)
		if _, err := e.m.Reset(t.Context(), By(a.ID), b.ID); err != nil {
			t.Fatal(err)
		}
		_, err := e.m.Disable(t.Context(), System, a.ID)
		mgrError(t, err, ErrLastAdmin, mgrLastAdmin(a.ID, ending))
	})
	t.Run("an editor, and an administrator that cannot sign in", func(t *testing.T) {
		e := newMgr(t)
		e.first(t)
		editor := e.add(t, "editor", RoleEditor)
		pending, _ := e.invite(t, "boss", RoleAdmin)
		for _, id := range []string{editor.ID, pending.ID} {
			if got, err := e.m.Disable(t.Context(), System, id); err != nil || got.Status != StatusDisabled {
				t.Errorf("Disable() of %s = %+v, %v, want a disabled account", id, got, err)
			}
		}
	})
	t.Run("a store without an active administrator", func(t *testing.T) {
		e := newMgr(t)
		a, _ := e.invite(t, "boss", RoleAdmin)
		if got, err := e.m.Disable(t.Context(), System, a.ID); err != nil || got.Status != StatusDisabled {
			t.Errorf("Disable() = %+v, %v, want a disabled account", got, err)
		}
	})
}

func TestDisableAndEnableErrors(t *testing.T) {
	e := newMgr(t)
	e.first(t)
	editor := e.add(t, "editor", RoleEditor)
	other := e.add(t, "other", RoleEditor)
	off := e.add(t, "off", RoleEditor)
	if _, err := e.m.Disable(t.Context(), System, off.ID); err != nil {
		t.Fatal(err)
	}
	before := e.mark(t)
	for name, do := range map[string]func(Actor, string) error{
		"Disable": func(by Actor, id string) error { _, err := e.m.Disable(t.Context(), by, id); return err },
		"Enable":  func(by Actor, id string) error { _, err := e.m.Enable(t.Context(), by, id); return err },
	} {
		mgrError(t, do(System, mgrMissingID), fs.ErrNotExist, mgrNotFound(mgrMissingID))
		mgrError(t, do(By(editor.ID), other.ID), ErrForbidden, mgrNotAllowed(editor, "manage accounts"))
		mgrError(t, do(By(editor.ID), editor.ID), ErrForbidden, mgrNotAllowed(editor, "manage accounts"))
		mgrError(t, do(By(editor.ID), mgrMissingID), ErrForbidden, mgrNotAllowed(editor, "manage accounts"))
		mgrError(t, do(Actor{}, other.ID), ErrForbidden, "not allowed: the operation has no actor")
		if t.Failed() {
			t.Logf("the operation was %s", name)
		}
	}
	e.unchanged(t, before)
}

func TestDelete(t *testing.T) {
	e := newMgr(t)
	admin := e.first(t)
	a := e.add(t, "yamada", RoleEditor)
	other := e.add(t, "other", RoleEditor)
	otherRow := e.raw(t)[other.ID]
	if err := e.m.Delete(t.Context(), By(admin.ID), a.ID); err != nil {
		t.Fatalf("Delete() = %v", err)
	}
	_, err := e.m.Get(t.Context(), a.ID)
	mgrError(t, err, fs.ErrNotExist, mgrNotFound(a.ID))
	if _, err := e.m.Find(t.Context(), "yamada"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Find() = %v, want fs.ErrNotExist", err)
	}
	if _, err := e.m.VerifyPasswordFactor(t.Context(), "yamada", mgrPassword("yamada")); !errors.Is(err, ErrBadCredentials) {
		t.Errorf("VerifyPasswordFactor() = %v, want ErrBadCredentials", err)
	}
	if list, err := e.m.List(t.Context()); err != nil || len(list) != 2 {
		t.Errorf("List() = %d accounts, %v, want 2", len(list), err)
	}
	if e.raw(t)[other.ID] != otherRow {
		t.Error("the row of another account changed")
	}
	// The row is gone with its credentials, and the login name and the email address are free.
	if _, ok := e.raw(t)[a.ID]; ok {
		t.Error("the row is still in the store")
	}
	again, _, err := e.m.Invite(t.Context(), By(admin.ID), mgrInput("yamada", RoleViewer))
	if err != nil || again.ID == a.ID || again.Status != StatusPending {
		t.Errorf("Invite() with the login name and email address of the deleted account = %+v, %v, want a new account", again, err)
	}
	// The account no longer is an actor.
	_, _, err = e.m.Invite(t.Context(), By(a.ID), mgrInput("third", RoleViewer))
	mgrError(t, err, ErrForbidden, a.ID+": not allowed: no such account")
	// A second deletion finds nothing.
	mgrError(t, e.m.Delete(t.Context(), System, a.ID), fs.ErrNotExist, mgrNotFound(a.ID))
}

func TestDeleteChangesNoDocument(t *testing.T) {
	e := newMgr(t)
	e.first(t)
	a := e.add(t, "writer", RoleAuthor)
	entryID := content.FormatID(content.RefEntry, [16]byte{0: 1, 15: 1})
	mediaID := content.FormatID(content.RefMedia, [16]byte{0: 1, 15: 2})
	documents := map[content.RefKind]struct{ id, data string }{
		content.RefEntry: {entryID, `{"schemaVersion": 1, "id": "` + entryID + `", "number": 1, "createdAt": "2026-11-08T12:00:00Z", "createdBy": "` + a.ID + `"}`},
		content.RefMedia: {mediaID, `{"schemaVersion": 1, "id": "` + mediaID + `", "hash": "sha256:` + strings.Repeat("a", 64) +
			`", "mimeType": "image/jpeg", "uploadedBy": "` + a.ID + `"}`},
	}
	e.putAuthor(t, mgrAuthorID)
	in := mgrInput("linked", RoleAuthor)
	in.Author = mgrAuthorID
	linked, _, err := e.m.Invite(t.Context(), System, in)
	if err != nil {
		t.Fatal(err)
	}
	err = e.s.Update(t.Context(), storeWrite(func(tx *store.Tx) error {
		for kind, d := range documents {
			if _, err := tx.Put(kind, d.id, []byte(d.data), a.ID); err != nil {
				return err
			}
		}
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	read := func() (docs map[content.RefKind]store.Document, history map[content.RefKind][]store.Version) {
		docs, history = make(map[content.RefKind]store.Document), make(map[content.RefKind][]store.Version)
		err := e.s.View(t.Context(), func(tx *store.Tx) error {
			for kind, d := range documents {
				doc, err := tx.Document(kind, d.id)
				if err != nil {
					return err
				}
				docs[kind] = doc
				if history[kind], err = tx.History(kind, d.id); err != nil {
					return err
				}
			}
			doc, err := tx.Document(content.RefAuthor, mgrAuthorID)
			docs[content.RefAuthor] = doc
			if err != nil {
				return err
			}
			history[content.RefAuthor], err = tx.History(content.RefAuthor, mgrAuthorID)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return docs, history
	}
	docsBefore, historyBefore := read()
	for _, id := range []string{a.ID, linked.ID} {
		if err := e.m.Delete(t.Context(), System, id); err != nil {
			t.Fatalf("Delete() = %v", err)
		}
	}
	docsAfter, historyAfter := read()
	for kind, doc := range docsBefore {
		if !slices.Equal(doc.Data, docsAfter[kind].Data) || doc.Version != docsAfter[kind].Version {
			t.Errorf("the %s document changed", kind)
		}
		if !slices.Equal(historyBefore[kind], historyAfter[kind]) {
			t.Errorf("the history of the %s document changed", kind)
		}
	}
	if len(docsBefore) != 3 {
		t.Errorf("the test read %d documents, want 3", len(docsBefore))
	}
	// The versions keep the ID of the account that saved them.
	if got := historyAfter[content.RefEntry]; len(got) != 1 || got[0].SavedBy != a.ID {
		t.Errorf("the history of the entry = %+v, want one version saved by %s", got, a.ID)
	}
}

func TestDeleteLastAdmin(t *testing.T) {
	const ending = "it cannot be deleted"
	t.Run("by itself", func(t *testing.T) {
		e := newMgr(t)
		admin := e.first(t)
		before := e.mark(t)
		mgrError(t, e.m.Delete(t.Context(), By(admin.ID), admin.ID), ErrLastAdmin, mgrLastAdmin(admin.ID, ending))
		e.unchanged(t, before)
	})
	t.Run("by System", func(t *testing.T) {
		e := newMgr(t)
		admin := e.first(t)
		before := e.mark(t)
		mgrError(t, e.m.Delete(t.Context(), System, admin.ID), ErrLastAdmin, mgrLastAdmin(admin.ID, ending))
		e.unchanged(t, before)
	})
	t.Run("one of two, and then the other", func(t *testing.T) {
		e := newMgr(t)
		a, b := e.first(t), e.add(t, "boss", RoleAdmin)
		if err := e.m.Delete(t.Context(), By(a.ID), b.ID); err != nil {
			t.Fatalf("Delete() of one of two administrators = %v, want nil", err)
		}
		mgrError(t, e.m.Delete(t.Context(), By(a.ID), a.ID), ErrLastAdmin, mgrLastAdmin(a.ID, ending))
	})
	t.Run("an administrator deletes itself when there is another", func(t *testing.T) {
		e := newMgr(t)
		a, b := e.first(t), e.add(t, "boss", RoleAdmin)
		if err := e.m.Delete(t.Context(), By(a.ID), a.ID); err != nil {
			t.Fatalf("Delete() = %v, want nil", err)
		}
		mgrError(t, e.m.Delete(t.Context(), By(b.ID), b.ID), ErrLastAdmin, mgrLastAdmin(b.ID, ending))
	})
	t.Run("a pending administrator does not count", func(t *testing.T) {
		e := newMgr(t)
		a := e.first(t)
		pending, _ := e.invite(t, "boss", RoleAdmin)
		mgrError(t, e.m.Delete(t.Context(), System, a.ID), ErrLastAdmin, mgrLastAdmin(a.ID, ending))
		if err := e.m.Delete(t.Context(), System, pending.ID); err != nil {
			t.Errorf("Delete() of a pending administrator = %v, want nil", err)
		}
	})
	t.Run("a disabled administrator does not count", func(t *testing.T) {
		e := newMgr(t)
		a, b := e.first(t), e.add(t, "boss", RoleAdmin)
		if _, err := e.m.Disable(t.Context(), System, b.ID); err != nil {
			t.Fatal(err)
		}
		mgrError(t, e.m.Delete(t.Context(), System, a.ID), ErrLastAdmin, mgrLastAdmin(a.ID, ending))
		if err := e.m.Delete(t.Context(), System, b.ID); err != nil {
			t.Errorf("Delete() of a disabled administrator = %v, want nil", err)
		}
	})
	t.Run("a store without an active administrator", func(t *testing.T) {
		e := newMgr(t)
		a, _ := e.invite(t, "boss", RoleAdmin)
		b, _ := e.invite(t, "other", RoleViewer)
		for _, id := range []string{a.ID, b.ID} {
			if err := e.m.Delete(t.Context(), System, id); err != nil {
				t.Errorf("Delete() = %v, want nil", err)
			}
		}
		if list, err := e.m.List(t.Context()); err != nil || len(list) != 0 {
			t.Errorf("List() = %v, %v, want none", list, err)
		}
	})
}

func TestDeleteErrors(t *testing.T) {
	e := newMgr(t)
	e.first(t)
	editor := e.add(t, "editor", RoleEditor)
	other := e.add(t, "other", RoleEditor)
	before := e.mark(t)
	mgrError(t, e.m.Delete(t.Context(), System, mgrMissingID), fs.ErrNotExist, mgrNotFound(mgrMissingID))
	mgrError(t, e.m.Delete(t.Context(), By(editor.ID), other.ID), ErrForbidden, mgrNotAllowed(editor, "manage accounts"))
	mgrError(t, e.m.Delete(t.Context(), By(editor.ID), editor.ID), ErrForbidden, mgrNotAllowed(editor, "manage accounts"))
	mgrError(t, e.m.Delete(t.Context(), By(editor.ID), mgrMissingID), ErrForbidden, mgrNotAllowed(editor, "manage accounts"))
	mgrError(t, e.m.Delete(t.Context(), Actor{}, other.ID), ErrForbidden, "not allowed: the operation has no actor")
	e.unchanged(t, before)
}
