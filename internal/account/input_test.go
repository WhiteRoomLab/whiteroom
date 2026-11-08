package account

import (
	"strconv"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

const (
	loginRule = `use up to 64 lower case ASCII letters, digits, ".", "-" and "_", starting with a letter or a digit`
	roleRule  = `use admin, editor, author or viewer`
	badFormat = `use "aut_" and a ULID (26 characters in upper case)`
)

var (
	ctrl             = string(rune(0x0001))
	del              = string(rune(0x007f))
	noBreakSpace     = string(rune(0x00a0))
	ideographicSpace = string(rune(0x3000))
	kelvin           = string(rune(0x212a)) // KELVIN SIGN, which strings.ToLower maps to "k"
	fullwidthA       = string(rune(0xff21))
	capitalEAcute    = string(rune(0x00c9))
	eAcute           = string(rune(0x00e9))
	acute            = string(rune(0x0301)) // "e" and this are the NFD form of eAcute
	lineSeparator    = string(rune(0x2028))
	replacement      = string(rune(0xfffd))
)

func badLogin(v string) string {
	return "invalid login name " + strconv.Quote(v) + ": " + loginRule
}

// problem is the path and the message of one of the problems of an Input or a Change.
type problem struct{ path, msg string }

func assertProblems(t *testing.T, got content.Errors, want ...problem) {
	t.Helper()
	if len(want) == 0 {
		if got != nil {
			t.Errorf("problems = %q, want none, nil", got.Error())
		}
		return
	}
	if len(got) != len(want) {
		t.Errorf("problems = %q, want %d: %v", got.Error(), len(want), want)
		return
	}
	for i, w := range want {
		if e := got[i]; e.Path != w.path || e.Msg != w.msg || e.File != "" || e.Line != 0 || e.Column != 0 {
			t.Errorf("problem %d = %+v, want the path %q and the message %q", i, *e, w.path, w.msg)
		}
	}
}

// validInput returns an Input with nothing wrong in it.
func validInput() Input {
	return Input{Login: "yamada", Name: "山田 太郎", Email: "yamada@example.com", Role: RoleEditor}
}

func TestNormalizeLogin(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"normalized", "yamada", "yamada"},
		{"upper case", "Yamada.TARO-1_X", "yamada.taro-1_x"},
		{"A and Z", "AZaz", "azaz"},
		{"surrounding spaces", "  yamada \t\n", "yamada"},
		{"surrounding no-break and ideographic spaces", noBreakSpace + ideographicSpace + "Yamada" + ideographicSpace, "yamada"},
		{"control characters", "ya" + ctrl + "ma\x00" + del + "da", "yamada"},
		{"spaces inside stay for the check", "ya mada", "ya mada"},
		{"a space left by a removed control character", " " + ctrl + " yamada", "yamada"},
		{"kelvin sign is not folded into k", kelvin + "elvin", kelvin + "elvin"},
		{"non-ASCII upper case stays", capitalEAcute + "A", capitalEAcute + "a"},
		{"full-width letters stay", fullwidthA, fullwidthA},
		{"invalid UTF-8", "ya\xffda", "ya" + replacement + "da"},
		{"only spaces and controls", " \x00\t ", ""},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeLogin(tt.in)
			if got != tt.want {
				t.Errorf("normalizeLogin(%q) = %q, want %q", tt.in, got, tt.want)
			}
			if again := normalizeLogin(got); again != got {
				t.Errorf("normalizeLogin(%q) = %q, want it unchanged", got, again)
			}
		})
	}
}

func TestCheckLogin(t *testing.T) {
	valid := []string{
		"a", "0", "9z", "1abc", "yamada", "yamada.taro", "a-b_c.d", "a.", "a-", "a_", "a..b", "a--b",
		strings.Repeat("a", 64), strings.Repeat("0", 64), "a" + strings.Repeat("-", 63),
	}
	for _, in := range valid {
		if err := checkLogin(in); err != nil {
			t.Errorf("checkLogin(%q) = %v, want nil", in, err)
		}
	}
	invalid := []string{
		".", "-", "_", ".a", "-a", "_a", "A", "aB", "a b", " a", "a ", "a\n", "a\x00", "a@b", "a/b", "a:b", "a+b", "a\\b",
		"山田", "a山", capitalEAcute, "a" + eAcute, kelvin, fullwidthA, "ya\xffda", strings.Repeat("a", 65), strings.Repeat("a", 1000),
	}
	for _, in := range invalid {
		if err := checkLogin(in); err == nil || err.Error() != badLogin(in) {
			t.Errorf("checkLogin(%q) = %v, want %q", in, err, badLogin(in))
		}
	}
	if err := checkLogin(""); err == nil || err.Error() != "missing login" {
		t.Errorf(`checkLogin("") = %v, want "missing login"`, err)
	}
}

func TestInputNormalize(t *testing.T) {
	in := Input{
		Login:  " Yamada" + ctrl + "Taro ",
		Name:   "Cafe" + acute + " \x00Taro",
		Email:  " Taro@Example.COM\n",
		Role:   "Admin",
		Author: "  " + testAuthorID + " \t",
	}
	want := Input{Login: "yamadataro", Name: "Caf" + eAcute + " Taro", Email: "Taro@example.com", Role: "Admin", Author: testAuthorID}
	in.normalize()
	if in != want {
		t.Errorf("normalize() made %+v, want %+v", in, want)
	}
	again := in
	again.normalize()
	if again != in {
		t.Errorf("normalizing %+v again made %+v", in, again)
	}

	// The name keeps its surrounding spaces and the tab and line break that the check reports, as the other documents'
	// names do; the role is kept as given.
	in = Input{Name: " a\tb ", Role: "ADMIN"}
	in.normalize()
	if in.Name != " a\tb " || in.Role != "ADMIN" {
		t.Errorf("normalize() made the name %q and the role %q, want them as they were", in.Name, in.Role)
	}
}

func TestInputCheck(t *testing.T) {
	long := func(n int, s string) string { return strings.Repeat(s, n) }
	tests := []struct {
		name   string
		modify func(*Input)
		want   []problem
	}{
		{name: "valid", modify: func(in *Input) {}},
		{name: "valid with an author", modify: func(in *Input) { in.Author = testAuthorID }},
		{name: "the longest login", modify: func(in *Input) { in.Login = long(64, "a") }},
		{name: "the longest name", modify: func(in *Input) { in.Name = long(256, "あ") }},
		{name: "the longest name of 4 bytes a character", modify: func(in *Input) { in.Name = long(256, string(rune(0x1f600))) }},
		{name: "a name with quotes", modify: func(in *Input) { in.Name = `Taro "T" <Yamada> & Co.\` }},
		{name: "admin", modify: func(in *Input) { in.Role = RoleAdmin }},
		{name: "author", modify: func(in *Input) { in.Role = RoleAuthor }},
		{name: "viewer", modify: func(in *Input) { in.Role = RoleViewer }},

		{
			name:   "nothing",
			modify: func(in *Input) { *in = Input{} },
			want: []problem{
				{"/login", "missing login"}, {"/name", "missing name"}, {"/email", "missing email"}, {"/role", "missing role"},
			},
		},
		{name: "login in upper case", modify: func(in *Input) { in.Login = "Yamada" }, want: []problem{{"/login", badLogin("Yamada")}}},
		{name: "login starting with a dot", modify: func(in *Input) { in.Login = ".yamada" }, want: []problem{{"/login", badLogin(".yamada")}}},
		{name: "login with a space", modify: func(in *Input) { in.Login = "ya mada" }, want: []problem{{"/login", badLogin("ya mada")}}},
		{name: "login with a surrounding space", modify: func(in *Input) { in.Login = " yamada" }, want: []problem{{"/login", badLogin(" yamada")}}},
		{name: "login of 65 bytes", modify: func(in *Input) { in.Login = long(65, "a") }, want: []problem{{"/login", badLogin(long(65, "a"))}}},
		{name: "login with an email", modify: func(in *Input) { in.Login = "yamada@example.com" }, want: []problem{{"/login", badLogin("yamada@example.com")}}},
		{name: "login with a kelvin sign", modify: func(in *Input) { in.Login = kelvin }, want: []problem{{"/login", badLogin(kelvin)}}},
		{name: "login in Japanese", modify: func(in *Input) { in.Login = "山田" }, want: []problem{{"/login", badLogin("山田")}}},
		{name: "login with a control character", modify: func(in *Input) { in.Login = "a" + ctrl }, want: []problem{{"/login", badLogin("a" + ctrl)}}},
		{
			name:   "name of 257 characters",
			modify: func(in *Input) { in.Name = long(257, "あ") },
			want:   []problem{{"/name", "too long: 257 characters (max 256)"}},
		},
		{
			name:   "name of 1000 characters",
			modify: func(in *Input) { in.Name = long(1000, "a") },
			want:   []problem{{"/name", "too long: 1000 characters (max 256)"}},
		},
		{
			name:   "name of more than the size of a text",
			modify: func(in *Input) { in.Name = long(content.MaxTextBytes+1, "a") },
			want:   []problem{{"/name", "too long: 1048577 bytes (max 1048576)"}},
		},
		{
			name:   "name in NFD",
			modify: func(in *Input) { in.Name = "Cafe" + acute },
			want:   []problem{{"/name", "not in Unicode NFC; normalize it first"}},
		},
		{
			name:   "name with a tab",
			modify: func(in *Input) { in.Name = "a\tb" },
			want:   []problem{{"/name", "contains a line break or tab"}},
		},
		{
			name:   "name with a line break",
			modify: func(in *Input) { in.Name = "a\nb" },
			want:   []problem{{"/name", "contains a line break or tab"}},
		},
		{
			name:   "name with a line separator",
			modify: func(in *Input) { in.Name = "a" + lineSeparator + "b" },
			want:   []problem{{"/name", "contains a line break or tab"}},
		},
		{
			name:   "name with a control character",
			modify: func(in *Input) { in.Name = "a" + ctrl + "b" },
			want:   []problem{{"/name", "contains control characters; normalize it first"}},
		},
		{
			name:   "name with invalid UTF-8",
			modify: func(in *Input) { in.Name = "a\xffb" },
			want:   []problem{{"/name", "invalid UTF-8"}},
		},
		{
			name:   "email without @",
			modify: func(in *Input) { in.Email = "yamada" },
			want:   []problem{{"/email", `invalid email address "yamada": needs exactly one @`}},
		},
		{
			name:   "email with a domain in upper case",
			modify: func(in *Input) { in.Email = "yamada@Example.com" },
			want:   []problem{{"/email", `invalid email address "yamada@Example.com": upper case in the domain; normalize it first`}},
		},
		{
			name:   "email with a surrounding space",
			modify: func(in *Input) { in.Email = " yamada@example.com" },
			want:   []problem{{"/email", "contains control characters or surrounding spaces; normalize it first"}},
		},
		{
			name:   "email in Japanese",
			modify: func(in *Input) { in.Email = "山田@example.com" },
			want:   []problem{{"/email", `invalid email address "山田@example.com": non-ASCII characters are not supported`}},
		},
		{
			name:   "email of a domain without a dot",
			modify: func(in *Input) { in.Email = "yamada@localhost" },
			want:   []problem{{"/email", `invalid email address "yamada@localhost": the domain needs a dot`}},
		},
		{
			name:   "unknown role",
			modify: func(in *Input) { in.Role = "root" },
			want:   []problem{{"/role", `unknown role "root"; ` + roleRule}},
		},
		{
			name:   "role in upper case",
			modify: func(in *Input) { in.Role = "Admin" },
			want:   []problem{{"/role", `unknown role "Admin"; ` + roleRule}},
		},
		{
			name:   "role with a space",
			modify: func(in *Input) { in.Role = "admin " },
			want:   []problem{{"/role", `unknown role "admin "; ` + roleRule}},
		},
		{
			name:   "author with the ID of an entry",
			modify: func(in *Input) { in.Author = "ent_01JB8Y6T2RG7M4XK9P2D5W8NQ3" },
			want:   []problem{{"/author", `invalid author ID "ent_01JB8Y6T2RG7M4XK9P2D5W8NQ3": ` + badFormat}},
		},
		{
			name:   "author in lower case",
			modify: func(in *Input) { in.Author = "aut_01jb8y6t2rg7m4xk9p2d5w8nq3" },
			want:   []problem{{"/author", `invalid author ID "aut_01jb8y6t2rg7m4xk9p2d5w8nq3": ` + badFormat}},
		},
		{
			name:   "author with a surrounding space",
			modify: func(in *Input) { in.Author = testAuthorID + " " },
			want:   []problem{{"/author", `invalid author ID "` + testAuthorID + ` ": ` + badFormat}},
		},
		{
			name:   "author that is not an ID",
			modify: func(in *Input) { in.Author = "x" },
			want:   []problem{{"/author", `invalid author ID "x": ` + badFormat}},
		},
		{
			name:   "every problem, in the order of the fields",
			modify: func(in *Input) { *in = Input{Login: "Bad Login", Name: "a\tb", Email: "x", Role: "boss", Author: "x"} },
			want: []problem{
				{"/login", badLogin("Bad Login")}, {"/name", "contains a line break or tab"},
				{"/email", `invalid email address "x": needs exactly one @`}, {"/role", `unknown role "boss"; ` + roleRule},
				{"/author", `invalid author ID "x": ` + badFormat},
			},
		},
		{
			name:   "the last problems only",
			modify: func(in *Input) { in.Role, in.Author = "", "x" },
			want:   []problem{{"/role", "missing role"}, {"/author", `invalid author ID "x": ` + badFormat}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := validInput()
			tt.modify(&in)
			assertProblems(t, in.check(), tt.want...)
		})
	}
}

// TestInputCheckAfterNormalize checks that normalizing makes the inputs that people are likely to type valid.
func TestInputCheckAfterNormalize(t *testing.T) {
	in := Input{
		Login: "  Yamada.Taro ", Name: "Cafe" + acute + " " + ctrl + "Taro", Email: " Yamada@Example.COM ", Role: RoleAuthor,
		Author: " " + testAuthorID + " ",
	}
	in.normalize()
	assertProblems(t, in.check())
	if in.Login != "yamada.taro" || in.Name != "Caf"+eAcute+" Taro" || in.Email != "Yamada@example.com" || in.Author != testAuthorID {
		t.Errorf("normalized input = %+v", in)
	}
}

// FuzzInputNormalize checks that normalizing twice is normalizing once, and that an Input with no problems is one that
// normalize leaves as it is, which row.check relies on.
func FuzzInputNormalize(f *testing.F) {
	f.Add("  Yamada ", "Cafe"+acute, " A@B.COM ", "admin", " "+testAuthorID+" ")
	f.Add("yamada", "山田 太郎", "yamada@example.com", "editor", testAuthorID)
	f.Add(kelvin, "a\tb", "a@Example.COM", "Admin", "x")
	f.Add("a"+ctrl+"b", "a"+ctrl+"b", "a@exa"+ctrl+"mple.com", "", "")
	f.Fuzz(func(t *testing.T, login, name, email, role, author string) {
		in := Input{Login: login, Name: name, Email: email, Role: Role(role), Author: author}
		if len(in.check()) == 0 {
			n := in
			n.normalize()
			if n != in {
				t.Fatalf("check() accepts %+v, which normalize() changes to %+v", in, n)
			}
		}
		in.normalize()
		again := in
		again.normalize()
		if again != in {
			t.Fatalf("normalize() made %+v, and again %+v", in, again)
		}
	})
}

func TestChangeNormalize(t *testing.T) {
	login, name, email, author := " Yamada ", "Cafe"+acute, " Taro@Example.COM\n", " "+testAuthorID+" "
	role := Role("Admin")
	c := Change{Login: &login, Name: &name, Email: &email, Role: &role, Author: &author}
	c.normalize()
	if *c.Login != "yamada" || *c.Name != "Caf"+eAcute || *c.Email != "Taro@example.com" || *c.Author != testAuthorID || *c.Role != "Admin" {
		t.Errorf("normalize() made %q, %q, %q, %q and %q", *c.Login, *c.Name, *c.Email, *c.Author, *c.Role)
	}
	// The values that the Change pointed to are the caller's, and stay as they were.
	if login != " Yamada " || name != "Cafe"+acute || email != " Taro@Example.COM\n" || author != " "+testAuthorID+" " || role != "Admin" {
		t.Errorf("normalize() changed the values that the fields pointed to: %q, %q, %q, %q and %q", login, name, email, author, role)
	}
	if c.Login == &login || c.Name == &name || c.Email == &email || c.Author == &author {
		t.Error("normalize() left a field pointing to the caller's value")
	}

	var empty Change
	empty.normalize()
	if empty != (Change{}) {
		t.Errorf("normalize() of a Change that sets nothing made %+v", empty)
	}
	removal := ""
	c = Change{Author: &removal}
	c.normalize()
	if c.Author == nil || *c.Author != "" || c.Login != nil || c.Name != nil || c.Email != nil || c.Role != nil {
		t.Errorf("normalize() of a removal of the link made %+v", c)
	}
}

func TestChangeCheck(t *testing.T) {
	long := func(n int, s string) string { return strings.Repeat(s, n) }
	tests := []struct {
		name string
		c    Change
		want []problem
	}{
		{name: "nothing", c: Change{}},
		{name: "login", c: Change{Login: ptr("new-login")}},
		{name: "name", c: Change{Name: ptr("山田 花子")}},
		{name: "email", c: Change{Email: ptr("hanako@example.com")}},
		{name: "role", c: Change{Role: ptr(RoleViewer)}},
		{name: "author", c: Change{Author: ptr(testAuthorID)}},
		{name: "removal of the link", c: Change{Author: ptr("")}},
		{name: "every field", c: Change{Login: ptr("a"), Name: ptr("A"), Email: ptr("a@example.com"), Role: ptr(RoleAdmin), Author: ptr(testAuthorID)}},
		{name: "the longest values", c: Change{Login: ptr(long(64, "a")), Name: ptr(long(256, "あ"))}},

		{name: "empty login", c: Change{Login: ptr("")}, want: []problem{{"/login", "missing login"}}},
		{name: "login in upper case", c: Change{Login: ptr("Yamada")}, want: []problem{{"/login", badLogin("Yamada")}}},
		{name: "login of 65 bytes", c: Change{Login: ptr(long(65, "a"))}, want: []problem{{"/login", badLogin(long(65, "a"))}}},
		{name: "empty name", c: Change{Name: ptr("")}, want: []problem{{"/name", "missing name"}}},
		{name: "name of 257 characters", c: Change{Name: ptr(long(257, "あ"))}, want: []problem{{"/name", "too long: 257 characters (max 256)"}}},
		{name: "name with a tab", c: Change{Name: ptr("a\tb")}, want: []problem{{"/name", "contains a line break or tab"}}},
		{name: "empty email", c: Change{Email: ptr("")}, want: []problem{{"/email", "missing email"}}},
		{
			name: "email without @", c: Change{Email: ptr("hanako")},
			want: []problem{{"/email", `invalid email address "hanako": needs exactly one @`}},
		},
		{name: "empty role", c: Change{Role: ptr(Role(""))}, want: []problem{{"/role", "missing role"}}},
		{name: "unknown role", c: Change{Role: ptr(Role("boss"))}, want: []problem{{"/role", `unknown role "boss"; ` + roleRule}}},
		{name: "author that is not an ID", c: Change{Author: ptr("x")}, want: []problem{{"/author", `invalid author ID "x": ` + badFormat}}},
		{
			name: "every problem, in the order of the fields",
			c:    Change{Author: ptr("x"), Role: ptr(Role("")), Email: ptr(""), Name: ptr(""), Login: ptr("")},
			want: []problem{
				{"/login", "missing login"}, {"/name", "missing name"}, {"/email", "missing email"}, {"/role", "missing role"},
				{"/author", `invalid author ID "x": ` + badFormat},
			},
		},
		{
			name: "only the fields that are set",
			c:    Change{Name: ptr("a\tb"), Role: ptr(RoleEditor)},
			want: []problem{{"/name", "contains a line break or tab"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertProblems(t, tt.c.check(), tt.want...)
		})
	}
}

// TestChangeCheckSameMessages checks that a Change says what an Input says about the same value.
func TestChangeCheckSameMessages(t *testing.T) {
	bad := Input{Login: "Bad Login", Name: "a\tb", Email: "x", Role: "boss", Author: "x"}
	asInput := bad.check()
	asChange := Change{Login: &bad.Login, Name: &bad.Name, Email: &bad.Email, Role: &bad.Role, Author: &bad.Author}.check()
	if asInput.Error() != asChange.Error() || len(asInput) != 5 {
		t.Errorf("Change.check() = %q, Input.check() = %q, want the same 5 problems", asChange.Error(), asInput.Error())
	}
}
