package content

import (
	"strconv"
	"strings"
	"testing"
)

func TestCheckEmail(t *testing.T) {
	const (
		normalize  = "contains control characters or surrounding spaces; normalize it first"
		nonASCII   = "non-ASCII characters are not supported"
		oneAt      = "needs exactly one @"
		localDots  = `the local part cannot start or end with "." or contain ".."`
		domainDots = `the domain cannot start or end with "." or contain ".."`
		hyphens    = `labels of the domain cannot start or end with "-"`
	)
	local64 := strings.Repeat("a", 64)
	// longest has a local part of 64 bytes and labels of 63 characters.
	longest := local64 + "@" + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 61)
	tests := []struct {
		name   string
		in     string
		reason string // the error is `invalid email address "<in>": <reason>`
		msg    string // the whole error, if it does not quote the address
	}{
		{name: "simple", in: "info@example.com"},
		{name: "dot and plus", in: "a.b+tag@example.co.jp"},
		{name: "digits and symbols", in: "0123456789!#$%&'*+-/=?^_`{|}~@example.com"},
		{name: "upper case in the local part", in: "Info@example.com"},
		{name: "first and last letters and digits", in: "AZaz09@example.com"},
		{name: "letter and digit in the last label", in: "info@example.a1"},
		{name: "IDN in the xn-- form", in: "info@xn--eckwd4c7c.xn--zckzah"}, // ドメイン.テスト
		{name: "hyphens in labels", in: "info@my-company.example-1.com"},
		{name: "digits in labels", in: "info@123.example.com"},
		{name: "digits in the last label", in: "info@example.c0m"},
		{name: "local part of 64 bytes", in: local64 + "@example.com"},
		{name: "label of 63 characters", in: "info@" + strings.Repeat("a", 63) + ".com"},
		{name: "254 bytes", in: longest},

		{name: "too long for a text", in: strings.Repeat("a", MaxTextBytes+1), msg: "too long: 1048577 bytes (max 1048576)"},
		{name: "invalid UTF-8", in: "info@example.com\xff", msg: "invalid UTF-8"},
		{name: "control character", in: "info@exa\x00mple.com", msg: normalize},
		{name: "tab", in: "info@\texample.com", msg: normalize},
		{name: "leading space", in: " info@example.com", msg: normalize},
		{name: "trailing newline", in: "info@example.com\n", msg: normalize},
		{name: "trailing ideographic space", in: "info@example.com" + string(rune(0x3000)), msg: normalize},
		{name: "Japanese local part", in: "山田@example.com", reason: nonASCII},
		{name: "Japanese domain", in: "info@例え.jp", reason: nonASCII + "; write the domain in the xn-- form"},
		{name: "Japanese local part and domain", in: "山田@例え.jp", reason: nonASCII},
		{name: "Japanese domain without a local part", in: "@例え.jp", reason: nonASCII + "; write the domain in the xn-- form"},
		{name: "non-ASCII without @", in: "山田", reason: nonASCII},
		{name: "255 bytes", in: longest + "d", reason: "longer than 254 bytes"},
		{name: "no @", in: "info.example.com", reason: oneAt},
		{name: "two @", in: "info@a@example.com", reason: oneAt},
		{name: "empty local part", in: "@example.com", reason: "empty local part"},
		{name: "local part of 65 bytes", in: local64 + "a@example.com", reason: "the local part is longer than 64 bytes"},
		{name: "space", in: "info @example.com", reason: "character ' ' is not allowed in the local part"},
		{name: "quoted local part", in: `"a b"@example.com`, reason: `character '"' is not allowed in the local part`},
		{name: "leading dot", in: ".info@example.com", reason: localDots},
		{name: "trailing dot", in: "info.@example.com", reason: localDots},
		{name: "consecutive dots", in: "in..fo@example.com", reason: localDots},
		{name: "empty domain", in: "info@", reason: "empty domain"},
		{name: "space in the domain", in: "info@exa mple.com", reason: "character ' ' is not allowed in the domain"},
		{name: "underscore in the domain", in: "info@ex_ample.com", reason: "character '_' is not allowed in the domain"},
		{name: "comma for a dot", in: "info@example,com", reason: "character ',' is not allowed in the domain"},
		{name: "IP address", in: "info@[192.0.2.1]", reason: "character '[' is not allowed in the domain"},
		{
			name:   "IP address without brackets",
			in:     "info@192.0.2.1",
			reason: "the last label of the domain cannot be all digits (IP addresses are not supported)",
		},
		{name: "single label", in: "user@localhost", reason: "the domain needs a dot"},
		{name: "empty label", in: "info@example..com", reason: domainDots},
		{name: "leading dot in the domain", in: "info@.example.com", reason: domainDots},
		{name: "trailing dot in the domain", in: "info@example.com.", reason: domainDots},
		{
			name:   "label of 64 characters",
			in:     "info@" + strings.Repeat("a", 64) + ".com",
			reason: "a label of the domain is longer than 63 characters",
		},
		{name: "leading hyphen", in: "info@-example.com", reason: hyphens},
		{name: "trailing hyphen", in: "info@example-.com", reason: hyphens},
		{name: "upper case in the domain", in: "Info@Example.COM", reason: "upper case in the domain; normalize it first"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := tt.msg
			if tt.reason != "" {
				want = "invalid email address " + strconv.Quote(tt.in) + ": " + tt.reason
			}
			err := checkEmail(tt.in)
			switch {
			case want == "" && err != nil:
				t.Errorf("error = %q, want nil", err)
			case want != "" && (err == nil || err.Error() != want):
				t.Errorf("error = %v, want %q", err, want)
			}
		})
	}
}

func TestNormalizeEmail(t *testing.T) {
	const (
		ctrl             = string(rune(0x0001))
		ideographicSpace = string(rune(0x3000))
		capitalEAcute    = string(rune(0x00C9))
		kelvin           = string(rune(0x212A)) // KELVIN SIGN, which strings.ToLower maps to "k"
	)
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "surrounding spaces", in: " info@example.com\n", want: "info@example.com"},
		{name: "surrounding ideographic spaces", in: ideographicSpace + "info@example.com" + ideographicSpace, want: "info@example.com"},
		{name: "control characters", in: "in" + ctrl + "fo@exa\tmple.com\x00", want: "info@example.com"},
		{name: "domain in lower case", in: "Info@Example.COM", want: "Info@example.com"},
		{name: "after the last @", in: "A@B@Example.COM", want: "A@B@example.com"},
		{name: "empty local part", in: "@Example.COM", want: "@example.com"},
		{name: "A and Z", in: "AZ@AZ.az", want: "AZ@az.az"},
		{name: "non-ASCII left as it is", in: "Info@" + capitalEAcute + "XAMPLE.COM", want: "Info@" + capitalEAcute + "xample.com"},
		{name: "kelvin sign left as it is", in: "info@" + kelvin + "ELVIN.COM", want: "info@" + kelvin + "elvin.com"},
		{name: "no @", in: "Info.Example.COM", want: "Info.Example.COM"},
		{name: "invalid UTF-8", in: "info@EXAMPLE.com\xff", want: "info@example.com" + string(rune(0xFFFD))},
		{name: "normalized", in: "info@example.com", want: "info@example.com"},
		{name: "empty", in: "", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeEmail(tt.in)
			if got != tt.want {
				t.Errorf("normalizeEmail(%q) = %q, want %q", tt.in, got, tt.want)
			}
			if again := normalizeEmail(got); again != got {
				t.Errorf("normalizeEmail(%q) = %q, want it unchanged", got, again)
			}
		})
	}
}

// FuzzNormalizeEmail checks that a normalized address does not change when normalized again,
// and that checkEmail does not ask to normalize it.
func FuzzNormalizeEmail(f *testing.F) {
	for _, s := range []string{"info@example.com", " Info@Example.COM\n", "a@b@C.d", "@Example.COM", "info@exa\x00mple.com\xff"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		n := normalizeEmail(s)
		if again := normalizeEmail(n); again != n {
			t.Fatalf("normalizeEmail(%q) = %q, and again %q", s, n, again)
		}
		if err := checkEmail(n); err != nil && strings.Contains(err.Error(), "normalize it first") {
			t.Fatalf("checkEmail(%q) = %v for the normalized form of %q", n, err, s)
		}
	})
}
