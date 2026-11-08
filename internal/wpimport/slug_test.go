package wpimport

import (
	"slices"
	"strings"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

const (
	whyLower  = "normalized to lower case and NFC"
	whyChars  = `characters that a slug cannot have are "-"`
	whyDecode = "it cannot be decoded: "
	whyCut    = "it is cut to 200 bytes"
	whyEnds   = `"-" at the ends is removed`
)

func TestImportSlug(t *testing.T) {
	pa := string(rune(0x30D1)) // PA, which the NFD form writes as HA and a semi-voiced sound mark
	tests := []struct {
		name string
		in   string
		want string
		why  []string
	}{
		{"ASCII", "hello-world", "hello-world", nil},
		{"digits and underscores", "post_2026-1", "post_2026-1", nil},
		{"Japanese", "%e3%81%8a%e7%9f%a5%e3%82%89%e3%81%9b", "お知らせ", nil},
		{"upper case hex digits", "%E3%81%8A%E7%9F%A5%E3%82%89%E3%81%9B", "お知らせ", nil},
		{"Japanese as it is", "お知らせ", "お知らせ", nil},
		{"mixed", "news-%e3%81%8a%e7%9f%a5%e3%82%89%e3%81%9b-2026", "news-お知らせ-2026", nil},
		{"upper case letters", "Hello-World", "hello-world", []string{whyLower}},
		{"upper case letters, Latin-1", "%c3%89t%c3%a9", "été", []string{whyLower}},
		{"NFD", "%e3%83%8f%e3%82%9a", pa, []string{whyLower}},
		{"NFD as it is", string(rune(0x30CF)) + string(rune(0x309A)), pa, []string{whyLower}},
		{"NFC", "%e3%83%91", pa, nil},
		{"undecodable", "%zz", "zz", []string{whyDecode + "invalid percent-encoding at byte 0", whyChars}},
		{"undecodable, a short escape", "a%4", "a-4", []string{whyDecode + "invalid percent-encoding at byte 1", whyChars}},
		{"undecodable, not UTF-8", "a%ffb", "a-ffb", []string{whyDecode + "not valid UTF-8 after decoding", whyChars}},
		{"undecodable, a control character", "ab%0acd", "ab-0acd", []string{whyDecode + "contains a control character after decoding", whyChars}},
		{"a control character", "a" + string(rune(1)) + "B", "ab", []string{whyDecode + "contains a control character after decoding", whyLower}},
		{"dot", "a.b", "a-b", []string{whyChars}},
		{"space", "a b", "a-b", []string{whyChars}},
		{"punctuation of ASCII", "a!b", "a-b", []string{whyChars}},
		{"percent sign", "100%", "100", []string{whyDecode + "invalid percent-encoding at byte 3", whyChars}},
		{"slash", "a/b", "a-b", []string{whyChars}},
		{"several in a row", "a!?. b", "a-b", []string{whyChars}},
		{"a hyphen that was there", "a--b", "a--b", nil},
		{"a hyphen before", "a-!b", "a-b", []string{whyChars}},
		{"a hyphen after", "a!-b", "a-b", []string{whyChars}},
		{"hyphens on both sides", "a-!-b", "a--b", []string{whyChars}},
		{"hyphens at the ends", "-a-", "a", []string{whyEnds}},
		{"hyphens and characters at the ends", "!-a-!", "a", []string{whyChars}},
		{"many hyphens at the ends", "---a---", "a", []string{whyEnds}},
		{"upper case letters and hyphens at the ends", "-A", "a", []string{whyLower, whyEnds}},
		{"full-width space", "a" + string(rune(0x3000)) + "b", "a" + string(rune(0x3000)) + "b", nil},
		{"emoji", "%f0%9f%98%80", string(rune(0x1F600)), nil},
		{"a zero-width space", "a" + string(rune(0x200B)) + "b", "a-b", []string{whyChars}},
		{"a bidi override", "a" + string(rune(0x202E)) + "b", "a-b", []string{whyChars}},
		{"encoded dot", "%2e", "", []string{whyChars}},
		{"symbols only", "!!!", "", []string{whyChars}},
		{"hyphens only", "---", "", []string{whyEnds}},
		{"encoded space only", "%20", "", []string{whyChars}},
		{"only a control character", string(rune(1)), "", []string{whyDecode + "contains a control character after decoding", whyLower}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, why := importSlug(tt.in)
			if got != tt.want || !slices.Equal(why, tt.why) {
				t.Errorf("importSlug(%q) = %q, %q, want %q, %q", tt.in, got, why, tt.want, tt.why)
			}
			if got != "" {
				if err := content.CheckSlug(got); err != nil {
					t.Errorf("importSlug(%q) = %q, which CheckSlug does not accept: %v", tt.in, got, err)
				}
			}
		})
	}
}

func TestImportSlugLength(t *testing.T) {
	for _, tt := range []struct {
		name string
		in   string
		want string
		why  []string
	}{
		{"200 bytes", strings.Repeat("a", 200), strings.Repeat("a", 200), nil},
		{"201 bytes", strings.Repeat("a", 201), strings.Repeat("a", 200), []string{whyCut}},
		{"much longer", strings.Repeat("ab", 5000), strings.Repeat("ab", 100), []string{whyCut}},
		{"Japanese, 66 characters of 3 bytes", strings.Repeat("あ", 66), strings.Repeat("あ", 66), nil},
		{"Japanese, 67 characters of 3 bytes", strings.Repeat("あ", 67), strings.Repeat("あ", 66), []string{whyCut}},
		{"cut at the start of a character", strings.Repeat("a", 199) + "あ", strings.Repeat("a", 199), []string{whyCut}},
		{"cut after a hyphen", strings.Repeat("a", 199) + "-bbb", strings.Repeat("a", 199), []string{whyCut}},
		{"cut after hyphens", strings.Repeat("a", 195) + "-----bbbb", strings.Repeat("a", 195), []string{whyCut}},
		{"cut before more hyphens", strings.Repeat("a", 190) + "-----b" + strings.Repeat("a", 20), strings.Repeat("a", 190) + "-----b" + strings.Repeat("a", 4), []string{whyCut}},
		{"percent-encoded Japanese, 70 characters", strings.Repeat("%e3%81%82", 70), strings.Repeat("あ", 66), []string{whyCut}},
		{"long and with a character that is replaced", strings.Repeat("a", 150) + "!" + strings.Repeat("b", 100), strings.Repeat("a", 150) + "-" + strings.Repeat("b", 49), []string{whyChars, whyCut}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, why := importSlug(tt.in)
			if got != tt.want || !slices.Equal(why, tt.why) {
				t.Errorf("importSlug(%d bytes) = %d bytes %q, %q, want %d bytes %q, %q", len(tt.in), len(got), got, why, len(tt.want), tt.want, tt.why)
			}
			if err := content.CheckSlug(got); err != nil {
				t.Errorf("importSlug(%d bytes) = %q, which CheckSlug does not accept: %v", len(tt.in), got, err)
			}
		})
	}
}

func FuzzImportSlug(f *testing.F) {
	for _, s := range []string{
		"", "hello", "Hello World", "%e3%81%8a%e7%9f%a5%e3%82%89%e3%81%9b", "%zz", "%", "a%", "%2e", "!!!", "a--b", "-a-",
		"%e3%83%8f%e3%82%9a", "お知らせ", strings.Repeat("a", 300), strings.Repeat("あ", 100), "a" + string(rune(0x3000)) + "b",
		"a" + string(rune(0x202E)) + "b", "\xff", "a\x00b",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		got, why := importSlug(s)
		if got != "" {
			if err := content.CheckSlug(got); err != nil {
				t.Fatalf("importSlug(%q) = %q, which CheckSlug does not accept: %v", s, got, err)
			}
		}
		for _, w := range why {
			if w == "" || strings.ContainsAny(w, "\n\r") {
				t.Fatalf("importSlug(%q) says %q", s, why)
			}
		}
		// A slug is made once: the slug that came out stays as it is.
		again, whyAgain := importSlug(got)
		if again != got || len(whyAgain) != 0 {
			t.Fatalf("importSlug(%q) = %q, and again %q, %q", s, got, again, whyAgain)
		}
	})
}

func TestSlugSet(t *testing.T) {
	var s slugSet
	for _, tt := range []struct {
		scope, slug, want string
	}{
		{"category", "news", "news"},
		{"category", "news", "news-2"},
		{"category", "news", "news-3"},
		{"tag", "news", "news"}, // another scope
		{"tag", "news", "news-2"},
		{"category", "news-2", "news-2-2"}, // news-2 was given out above
		{"category", "other", "other"},
		{"category", "news", "news-4"},     // goes on from the last number
		{"category\x00en", "news", "news"}, // another language
		{"category", "お知らせ", "お知らせ"},
		{"category", "お知らせ", "お知らせ-2"},
		{"category", "news-4", "news-4-2"}, // news-4 was given out above
		{"category", "news-5", "news-5"},
		{"category", "news", "news-6"}, // news-5 is taken, so it is skipped
		{"category", "news-2", "news-2-3"},
	} {
		if got := s.unique(tt.scope, tt.slug); got != tt.want {
			t.Errorf("unique(%q, %q) = %q, want %q", tt.scope, tt.slug, got, tt.want)
		}
	}
}

func TestSlugSetTake(t *testing.T) {
	var s slugSet
	for _, tt := range []struct {
		scope, slug string
		want        bool
	}{
		{"category", "news", true},
		{"category", "news", false},
		{"tag", "news", true}, // another scope
		{"category", "other", true},
		{"category", "news", false},
	} {
		if got := s.take(tt.scope, tt.slug); got != tt.want {
			t.Errorf("take(%q, %q) = %v, want %v", tt.scope, tt.slug, got, tt.want)
		}
	}
	// A slug that was taken is not given out by unique.
	if got := s.unique("category", "news"); got != "news-2" {
		t.Errorf("unique of a slug that was taken = %q, want news-2", got)
	}
	if !s.take("category", "news-3") {
		t.Error("take of a slug that unique did not give out failed")
	}
	if got := s.unique("category", "news"); got != "news-4" {
		t.Errorf("unique goes on to %q, want news-4, as news-3 was taken", got)
	}
}

func TestClaimSlugs(t *testing.T) {
	claim := func(scope, slug string, first bool) slugClaim {
		return slugClaim{scope: scope, slug: slug, first: first}
	}
	for _, tt := range []struct {
		name   string
		claims []slugClaim
		want   []string
	}{
		{"none", nil, []string{}},
		{"slugs that differ", []slugClaim{claim("c", "x", false), claim("c", "y", true)}, []string{"x", "y"}},
		{"none is first: in order", []slugClaim{claim("c", "x", false), claim("c", "x", false), claim("c", "x", false)}, []string{"x", "x-2", "x-3"}},
		{"a first claim comes before the others", []slugClaim{claim("c", "x", false), claim("c", "x", true)}, []string{"x-2", "x"}},
		{
			"a first claim keeps the slug that another is given later",
			[]slugClaim{claim("c", "x", false), claim("c", "x-2", true), claim("c", "x", true)},
			[]string{"x-3", "x-2", "x"},
		},
		{
			"a first claim that could not take its slug is given a number before the others",
			[]slugClaim{claim("c", "x", true), claim("c", "x-2", false), claim("c", "x", true)},
			[]string{"x", "x-2-2", "x-2"},
		},
		{"first claims in order", []slugClaim{claim("c", "x", true), claim("c", "x", true), claim("c", "x", true)}, []string{"x", "x-2", "x-3"}},
		{"scopes", []slugClaim{claim("a", "x", true), claim("b", "x", false), claim("a", "x", false), claim("b", "x", true)}, []string{"x", "x-2", "x-2", "x"}},
	} {
		got := claimSlugs(tt.claims)
		if !slices.Equal(got, tt.want) {
			t.Errorf("%s: claimSlugs = %q, want %q", tt.name, got, tt.want)
		}
		if again := claimSlugs(tt.claims); !slices.Equal(got, again) {
			t.Errorf("%s: claimSlugs gives %q and then %q", tt.name, got, again)
		}
	}
}

func TestSlugSetLength(t *testing.T) {
	var s slugSet
	long := strings.Repeat("a", content.MaxSlugBytes)
	if got := s.unique("c", long); got != long {
		t.Fatalf("unique of a slug of %d bytes = %d bytes", len(long), len(got))
	}
	for i, suffix := range []string{"-2", "-3", "-4"} {
		want := long[:content.MaxSlugBytes-len(suffix)] + suffix
		got := s.unique("c", long)
		if got != want {
			t.Errorf("repeat %d: unique = %q, want %d bytes ending with %s", i, got, len(want), suffix)
		}
		if err := content.CheckSlug(got); err != nil {
			t.Errorf("repeat %d: %q is not a slug: %v", i, got, err)
		}
	}
	// A cut is at the start of a character: 66 characters of 3 bytes are 198 bytes, and "-2" makes 200.
	japanese := strings.Repeat("あ", 66)
	s.unique("c", japanese)
	got := s.unique("c", japanese)
	if want := strings.Repeat("あ", 66) + "-2"; got != want {
		t.Errorf("unique of a Japanese slug of 198 bytes = %q, want %q", got, want)
	}
	if err := content.CheckSlug(got); err != nil {
		t.Error(err)
	}
	// "-2" leaves 198 bytes for the slug, and a character of 3 bytes does not end there.
	odd := "a" + strings.Repeat("あ", 66)
	s.unique("c", odd)
	got = s.unique("c", odd)
	if want := "a" + strings.Repeat("あ", 65) + "-2"; got != want {
		t.Errorf("unique of a slug of %d bytes = %q, want %q", len(odd), got, want)
	}
	if err := content.CheckSlug(got); err != nil {
		t.Error(err)
	}
}

// TestSlugSetMany checks that taking many slugs that are the same takes little time, as a file that is made of
// terms with the same slug would need.
func TestSlugSetMany(t *testing.T) {
	var s slugSet
	seen := make(map[string]bool)
	for i := range 20000 {
		got := s.unique("c", "same")
		if seen[got] {
			t.Fatalf("unique returned %q twice (the %dth)", got, i)
		}
		seen[got] = true
	}
	if !seen["same"] || !seen["same-2"] || !seen["same-20000"] || seen["same-20001"] {
		t.Error("the slugs are not same, same-2 and so on to same-20000")
	}
}

func TestNicename(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"admin", "admin"},
		{"Admin", "admin"},
		{"ADMIN", "admin"},
		{"john.doe", "john-doe"},
		{"John.Doe", "john-doe"},
		{"user@example.com", "userexample-com"},
		{"a  b", "a-b"},
		{"a \t b", "a-b"},
		{"a b c", "a-b-c"},
		{"--x--", "x"},
		{"x_y-z", "x_y-z"},
		{"x__y", "x__y"},
		{"a.-.b", "a-b"},
		{"a@-b", "a-b"},
		{"-@-", ""},
		{"a!#$%&'()*+,/:;<=>?[]^`{|}~b", "ab"},
		{"user123", "user123"},
		{"123", "123"},
		{strings.Repeat("a", 50), strings.Repeat("a", 50)},
		{strings.Repeat("a", 60), strings.Repeat("a", 50)},
		{"山田", "山田"},
		{"山田太郎", "山田太郎"},
		{strings.Repeat("山", 60), strings.Repeat("山", 50)},
		{strings.Repeat("a.", 40), strings.TrimRight(strings.Repeat("a-", 25), "-")},
		{strings.Repeat("@", 49) + "ab", "a"},
		{"", ""},
		{"...", ""},
		{"@@@", ""},
		{"Ünal", "Ünal"},
	} {
		if got := nicename(tt.in); got != tt.want {
			t.Errorf("nicename(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
