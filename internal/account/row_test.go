package account

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// jsonOf returns the JSON that encodeRow writes for r.
func jsonOf(t *testing.T, r row) string {
	t.Helper()
	b, err := encodeRow(r)
	if err != nil {
		t.Fatalf("encodeRow() = %v", err)
	}
	return string(b)
}

// replaceOnce returns s with new in place of old, which must be in s exactly once.
func replaceOnce(t *testing.T, s, old, new string) string {
	t.Helper()
	if n := strings.Count(s, old); n != 1 {
		t.Fatalf("%q is %d times in %q, want once", old, n, s)
	}
	return strings.Replace(s, old, new, 1)
}

func invalidValue(id string) string {
	return `accounts "` + id + `": invalid value`
}

func TestEncodeRow(t *testing.T) {
	const (
		createdAt = `"createdAt":"2026-11-08T01:02:03.456789Z"`
		changedAt = `"credentialsChangedAt":"2026-11-08T11:02:03.456789Z"`
		login     = `"id":"` + testUserID + `","login":"yamada","name":"山田 太郎","email":"yamada@example.com"`
	)
	password := `"password":"` + testPasswordHash(0xab) + `"`
	resetToken := `"token":{"purpose":"reset","hash":"` + testTokenHash(0xcd) + `","expiresAt":"2026-11-09T11:02:03.456789Z"}`
	invitation := `"token":{"purpose":"invitation","hash":"` + testTokenHash(0xef) + `","expiresAt":"2026-11-15T01:02:03.456789Z"}`

	disabled := testRow()
	disabled.Role, disabled.Author, disabled.CreatedBy, disabled.Disabled, disabled.Token = RoleAdmin, "", "", true, tokenRow{}
	invited := testMinimalRow()
	invited.Token = tokenRow{Purpose: PurposeInvitation, Hash: testTokenHash(0xef), ExpiresAt: testTime.Add(7 * 24 * time.Hour)}

	tests := []struct {
		name string
		row  row
		want string
	}{
		{
			name: "every key but disabled",
			row:  testRow(),
			want: `{` + login + `,"role":"editor","author":"` + testAuthorID + `",` + createdAt + `,"createdBy":"` + testCreatorID + `",` +
				password + `,` + changedAt + `,` + resetToken + `}`,
		},
		{
			name: "every key but the token",
			row:  disabled,
			want: `{` + login + `,"role":"admin","disabled":true,` + createdAt + `,` + password + `,` + changedAt + `}`,
		},
		{
			name: "only the required keys",
			row:  testMinimalRow(),
			want: `{"id":"` + testUserID + `","login":"a","name":"A","email":"a@example.com","role":"viewer",` + createdAt + `}`,
		},
		{
			name: "an invitation",
			row:  invited,
			want: `{"id":"` + testUserID + `","login":"a","name":"A","email":"a@example.com","role":"viewer",` + createdAt + `,` + invitation + `}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := jsonOf(t, tt.row); got != tt.want {
				t.Errorf("encodeRow() =\n%s\nwant\n%s", got, tt.want)
			}
		})
	}
}

func TestEncodeRowWritesUTC(t *testing.T) {
	jst := time.FixedZone("JST", 9*3600)
	r := testRow()
	r.CreatedAt = time.Date(2026, 11, 8, 10, 2, 3, 456789000, jst)
	r.CredentialsChangedAt = time.Date(2026, 11, 8, 20, 2, 3, 456789000, jst)
	r.Token.ExpiresAt = time.Date(2026, 11, 9, 20, 2, 3, 456789000, jst)
	if got, want := jsonOf(t, r), jsonOf(t, testRow()); got != want {
		t.Errorf("encodeRow() of the times in another zone =\n%s\nwant\n%s", got, want)
	}
}

func TestEncodeRowErrors(t *testing.T) {
	r := testRow()
	r.CreatedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC) // a year that RFC 3339 does not have
	if b, err := encodeRow(r); err == nil {
		t.Errorf("encodeRow() of the year 10000 = %s, want an error", b)
	}
	r = testRow()
	r.Name = "a\xffb"
	if b, err := encodeRow(r); err == nil {
		t.Errorf("encodeRow() of invalid UTF-8 = %s, want an error", b)
	}
}

func TestRowRoundTrip(t *testing.T) {
	disabled := testRow()
	disabled.Disabled, disabled.Token = true, tokenRow{}
	invited := testMinimalRow()
	invited.Token = tokenRow{Purpose: PurposeInvitation, Hash: testTokenHash(0xef), ExpiresAt: testTime.Add(7 * 24 * time.Hour)}
	reset := testRow()
	reset.Password, reset.CredentialsChangedAt = "", testTime.Add(time.Hour)
	special := testRow()
	special.Name = `Taro "T" <Yamada> & Co.\ ` + string(rune(0x1f600)) + " Caf" + eAcute + " " + string(rune(0x00a0)) + string(rune(0x3000))
	longest := testMinimalRow()
	longest.Login, longest.Name = strings.Repeat("a", 64), strings.Repeat("あ", 256)
	nanos := testRow()
	nanos.CreatedAt = time.Date(2026, 11, 8, 1, 2, 3, 1, time.UTC)
	nanos.CredentialsChangedAt = time.Date(2026, 11, 8, 1, 2, 3, 100000000, time.UTC)
	nanos.Token.ExpiresAt = time.Date(2026, 11, 8, 1, 2, 3, 123456789, time.UTC)
	epoch := testMinimalRow()
	epoch.CreatedAt = time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)

	for name, r := range map[string]row{
		"full": testRow(), "disabled": disabled, "minimal": testMinimalRow(), "invited": invited, "reset": reset,
		"special characters": special, "longest": longest, "fractions of a second": nanos, "epoch": epoch,
	} {
		t.Run(name, func(t *testing.T) {
			data := jsonOf(t, r)
			got, err := decodeRow(r.ID, []byte(data))
			if err != nil {
				t.Fatalf("decodeRow(%s) = %v", data, err)
			}
			if got != r {
				t.Errorf("decodeRow() = %+v, want %+v", got, r)
			}
			if again := jsonOf(t, got); again != data {
				t.Errorf("encodeRow() of the decoded row =\n%s\nwant\n%s", again, data)
			}
			if got.account() != r.account() {
				t.Errorf("the Account of the decoded row is %+v, want %+v", got.account(), r.account())
			}
		})
	}
}

func TestDecodeRowRejects(t *testing.T) {
	full := jsonOf(t, testRow())
	// The escape of a character in a JSON string, written out here so that the source holds no such escape.
	escape := string(rune(0x5c)) + "u0061"
	otherID := "usr_01JB8T7H4MA2C5E8G1J4K7N3P8"
	const (
		zero      = `"0001-01-01T00:00:00Z"`
		createdAt = `"createdAt":"2026-11-08T01:02:03.456789Z"`
		changedAt = `"credentialsChangedAt":"2026-11-08T11:02:03.456789Z"`
		expiresAt = `"expiresAt":"2026-11-09T11:02:03.456789Z"`
	)
	password := `"password":"` + testPasswordHash(0xab) + `"`
	token := `"token":{"purpose":"reset","hash":"` + testTokenHash(0xcd) + `",` + expiresAt + `}`

	// edit returns the JSON of the full row with new in place of old.
	edit := func(old, new string) string { return replaceOnce(t, full, old, new) }
	// with returns the JSON that encodeRow writes for the full row after f: a row that is not valid, in the one form.
	with := func(f func(*row)) string {
		r := testRow()
		f(&r)
		return jsonOf(t, r)
	}

	tests := []struct {
		name string
		id   string // the ID that the row is read for; testUserID if ""
		data string
	}{
		{name: "empty", data: ""},
		{name: "null", data: "null"},
		{name: "an array", data: "[]"},
		{name: "a string", data: `"x"`},
		{name: "no members", data: "{}"},
		{name: "broken", data: full[:len(full)-1]},
		{name: "cut in the middle", data: full[:60]},
		{name: "a member of the wrong type", data: edit(`"role":"editor"`, `"role":5`)},
		{name: "a string that is an object", data: edit(`"login":"yamada"`, `"login":{}`)},
		{name: "disabled of the wrong type", data: edit(`"login":`, `"disabled":"yes","login":`)},
		{name: "a time of the wrong type", data: edit(createdAt, `"createdAt":5`)},
		{name: "a time without a zone", data: edit(createdAt, `"createdAt":"2026-11-08T01:02:03.456789"`)},
		{name: "invalid UTF-8", data: edit(`"login":"yamada"`, "\"login\":\"yama\xffda\"")},
		{name: "a byte order mark", data: "\xef\xbb\xbf" + full},

		{name: "no id key", data: edit(`"id":"`+testUserID+`",`, "")},
		{name: "no login key", data: edit(`"login":"yamada",`, "")},
		{name: "no name key", data: edit(`"name":"山田 太郎",`, "")},
		{name: "no email key", data: edit(`"email":"yamada@example.com",`, "")},
		{name: "no role key", data: edit(`"role":"editor",`, "")},
		{name: "no createdAt key", data: edit(createdAt+",", "")},

		{name: "an unknown key", data: edit(`"login":`, `"extra":1,"login":`)},
		{name: "an unknown key at the end", data: full[:len(full)-1] + `,"extra":null}`},
		{name: "an unknown key in the token", data: edit(`"purpose":"reset"`, `"purpose":"reset","extra":1`)},
		{name: "the name of a key in another case", data: edit(`"login":`, `"Login":`)},
		{name: "a duplicate key", data: edit(`"login":"yamada"`, `"login":"yamada","login":"yamada"`)},
		{name: "a duplicate key with another value", data: edit(`"login":"yamada"`, `"login":"yamada","login":"taro"`)},
		{name: "a duplicate key in the token", data: edit(`"purpose":"reset"`, `"purpose":"reset","purpose":"reset"`)},
		{name: "login and name in another order", data: edit(`"login":"yamada","name":"山田 太郎"`, `"name":"山田 太郎","login":"yamada"`)},
		{name: "id after login", data: edit(`"id":"`+testUserID+`","login":"yamada"`, `"login":"yamada","id":"`+testUserID+`"`)},
		{name: "password and credentialsChangedAt in another order", data: edit(password+`,`+changedAt, changedAt+`,`+password)},
		{
			name: "the keys of the token in another order",
			data: edit(`{"purpose":"reset","hash":"`+testTokenHash(0xcd)+`"`, `{"hash":"`+testTokenHash(0xcd)+`","purpose":"reset"`),
		},
		{name: "a space after a colon", data: edit(`"id":`, `"id": `)},
		{name: "a space after a comma", data: edit(`,"login"`, `, "login"`)},
		{name: "a line break between members", data: edit(`,"login"`, ",\n\"login\"")},
		{name: "a tab before a colon", data: edit(`"login":`, "\"login\"\t:")},
		{name: "a leading space", data: " " + full},
		{name: "a trailing line break", data: full + "\n"},
		{name: "a trailing value", data: full + " {}"},
		{name: "two rows", data: full + full},
		{name: "an escaped character", data: edit(`"login":"yamada"`, `"login":"yamad`+escape+`"`)},

		{name: "an empty author", data: edit(`"author":"`+testAuthorID+`",`, `"author":"",`)},
		{name: "an empty creator", data: edit(`"createdBy":"`+testCreatorID+`",`, `"createdBy":"",`)},
		{name: "an empty password", data: edit(password+`,`, `"password":"",`)},
		{name: "disabled false", data: edit(`"login":`, `"disabled":false,"login":`)},
		{name: "a zero time of the credentials", data: edit(changedAt, `"credentialsChangedAt":`+zero)},
		{name: "an empty token", data: edit(token, `"token":{}`)},
		{name: "a null token", data: edit(token, `"token":null`)},
		{name: "a token of empty values", data: edit(token, `"token":{"purpose":"","hash":"","expiresAt":`+zero+`}`)},

		{name: "the ID of another account", id: otherID, data: full},
		{name: "a key that is not an ID", id: "usr_x", data: with(func(r *row) { r.ID = "usr_x" })},
		{name: "a key of another kind", id: testAuthorID, data: with(func(r *row) { r.ID = testAuthorID })},
		{name: "a key in lower case", id: strings.ToLower(testUserID), data: with(func(r *row) { r.ID = strings.ToLower(testUserID) })},

		{name: "no login", data: with(func(r *row) { r.Login = "" })},
		{name: "a login in upper case", data: with(func(r *row) { r.Login = "Yamada" })},
		{name: "a login of 65 bytes", data: with(func(r *row) { r.Login = strings.Repeat("a", 65) })},
		{name: "a login starting with a dot", data: with(func(r *row) { r.Login = ".yamada" })},
		{name: "a login with a surrounding space", data: with(func(r *row) { r.Login = "yamada " })},
		{name: "a login with the kelvin sign", data: with(func(r *row) { r.Login = kelvin })},
		{name: "no name", data: with(func(r *row) { r.Name = "" })},
		{name: "a name of 257 characters", data: with(func(r *row) { r.Name = strings.Repeat("あ", 257) })},
		{name: "a name in NFD", data: with(func(r *row) { r.Name = "Cafe" + acute })},
		{name: "a name with a tab", data: with(func(r *row) { r.Name = "a\tb" })},
		{name: "a name with a line break", data: with(func(r *row) { r.Name = "a\nb" })},
		{name: "a name with a control character", data: with(func(r *row) { r.Name = "a" + ctrl + "b" })},
		{name: "no email", data: with(func(r *row) { r.Email = "" })},
		{name: "an email that is not one", data: with(func(r *row) { r.Email = "yamada" })},
		{name: "an email with a domain in upper case", data: with(func(r *row) { r.Email = "yamada@Example.com" })},
		{name: "an email with a surrounding space", data: with(func(r *row) { r.Email = " yamada@example.com" })},
		{name: "no role", data: with(func(r *row) { r.Role = "" })},
		{name: "an unknown role", data: with(func(r *row) { r.Role = "root" })},
		{name: "a role in upper case", data: with(func(r *row) { r.Role = "Editor" })},
		{name: "an author that is not an ID", data: with(func(r *row) { r.Author = "x" })},
		{name: "an author of another kind", data: with(func(r *row) { r.Author = testUserID })},
		{name: "an author with a surrounding space", data: with(func(r *row) { r.Author = " " + testAuthorID })},
		{name: "a creator that is not an ID", data: with(func(r *row) { r.CreatedBy = "x" })},
		{name: "a creator of another kind", data: with(func(r *row) { r.CreatedBy = testAuthorID })},
		{name: "no creation time", data: with(func(r *row) { r.CreatedAt = time.Time{} })},
		{name: "a creation time of the zero value", data: edit(createdAt, `"createdAt":`+zero)},

		{name: "a time in another zone", data: edit(createdAt, `"createdAt":"2026-11-08T10:02:03.456789+09:00"`)},
		{name: "a time with an offset of zero", data: edit(createdAt, `"createdAt":"2026-11-08T01:02:03.456789+00:00"`)},
		{name: "a time with a zero at the end", data: edit(createdAt, `"createdAt":"2026-11-08T01:02:03.4567890Z"`)},
		{name: "a time with a zero fraction", data: edit(changedAt, `"credentialsChangedAt":"2026-11-08T11:02:03.000Z"`)},
		{name: "a time of the credentials in another zone", data: edit(changedAt, `"credentialsChangedAt":"2026-11-08T20:02:03.456789+09:00"`)},
		{name: "an expiry in another zone", data: edit(expiresAt, `"expiresAt":"2026-11-09T20:02:03.456789+09:00"`)},

		{name: "a token without a purpose", data: with(func(r *row) { r.Token.Purpose = "" })},
		{name: "a token of an unknown purpose", data: with(func(r *row) { r.Token.Purpose = "login" })},
		{name: "a token of a purpose in upper case", data: with(func(r *row) { r.Token.Purpose = "Reset" })},
		{name: "a token without a hash", data: with(func(r *row) { r.Token.Hash = "" })},
		{name: "a token hash without the prefix", data: with(func(r *row) { r.Token.Hash = strings.TrimPrefix(testTokenHash(0xcd), "sha256:") })},
		{name: "a token hash of another algorithm", data: with(func(r *row) { r.Token.Hash = "sha1:" + strings.TrimPrefix(testTokenHash(0xcd), "sha256:") })},
		{name: "a token hash in upper case", data: with(func(r *row) {
			r.Token.Hash = "sha256:" + strings.ToUpper(strings.TrimPrefix(testTokenHash(0xcd), "sha256:"))
		})},
		{name: "a token hash of 63 digits", data: with(func(r *row) { r.Token.Hash = r.Token.Hash[:len(r.Token.Hash)-1] })},
		{name: "a token hash of 65 digits", data: with(func(r *row) { r.Token.Hash += "0" })},
		{name: "a token hash with a digit that is not hexadecimal", data: with(func(r *row) { r.Token.Hash = "sha256:" + strings.Repeat("g", 64) })},
		{name: "a token without an expiry", data: with(func(r *row) { r.Token.ExpiresAt = time.Time{} })},
		{name: "a disabled account with a token", data: with(func(r *row) { r.Disabled = true })},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := tt.id
			if id == "" {
				id = testUserID
			}
			got, err := decodeRow(id, []byte(tt.data))
			if err == nil || err.Error() != invalidValue(id) {
				t.Errorf("decodeRow(%q, %s) = %+v, %v, want the error %q", id, tt.data, got, err, invalidValue(id))
			}
			if got != (row{}) {
				t.Errorf("decodeRow() returned a row with its error: %+v", got)
			}
		})
	}
}

// TestDecodeRowNoID checks the key of a row that is not an ID: the error names the key as it is.
func TestDecodeRowNoID(t *testing.T) {
	r := testRow()
	r.ID = ""
	for _, id := range []string{"", "x", "usr_", " " + testUserID} {
		_, err := decodeRow(id, []byte(jsonOf(t, r)))
		if want := invalidValue(id); err == nil || err.Error() != want {
			t.Errorf("decodeRow(%q) = %v, want %q", id, err, want)
		}
	}
}

// TestDecodeRowErrorHidesData checks that nothing of a row is in the error that its reading returns.
func TestDecodeRowErrorHidesData(t *testing.T) {
	r := testRow()
	r.Role = Role("root-" + testPasswordHash(0xab) + testTokenHash(0xcd))
	for _, data := range []string{jsonOf(t, r), strings.Replace(jsonOf(t, testRow()), `"login":"yamada"`, `"login":"yamada","login":"yamada"`, 1)} {
		_, err := decodeRow(testUserID, []byte(data))
		if err == nil {
			t.Fatal("decodeRow() accepted a row that is not valid")
		}
		for _, secret := range []string{"yamada", "root", "example.com", "pbkdf2", "sha256", testPasswordHash(0xab), testTokenHash(0xcd), "山田"} {
			if strings.Contains(err.Error(), secret) {
				t.Errorf("the error %q contains %q", err, secret)
			}
		}
	}
}

func FuzzDecodeRow(f *testing.F) {
	seed := func(r row) string {
		b, err := encodeRow(r)
		if err != nil {
			f.Fatal(err)
		}
		return string(b)
	}
	full := seed(testRow())
	for _, s := range []string{
		full, seed(testMinimalRow()), "", "null", "{}", full + "\n", " " + full,
		strings.Replace(full, `"login":"yamada"`, `"login":"yamada","login":"yamada"`, 1),
		strings.Replace(full, `"role":"editor"`, `"role":"root"`, 1),
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		r, err := decodeRow(testUserID, data)
		if err != nil {
			if err.Error() != invalidValue(testUserID) {
				t.Fatalf("decodeRow() = %v, want %q", err, invalidValue(testUserID))
			}
			return
		}
		// What decodeRow accepts is what encodeRow writes.
		enc, err := encodeRow(r)
		if err != nil || !bytes.Equal(enc, data) {
			t.Fatalf("encodeRow() of the row read from %q = %q, %v", data, enc, err)
		}
		if a := r.account(); a.ID != testUserID || a.Status == "" {
			t.Fatalf("account() of the row read from %q = %+v", data, a)
		}
	})
}
