package account

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strings"
	"testing"
	"testing/iotest"
	"unicode"
	"unicode/utf8"

	"github.com/WhiteRoomLab/whiteroom/internal/nfc"
)

const (
	pwGood = "correct horse battery staple" // passes every rule
	pwLow  = 2                              // the iteration count of the tests that do not test the cost

	pwInvalidUTF8 = "invalid UTF-8"
	pwControl     = "contains control characters"
	pwSame        = "the same as the login name, the email address or the name"
	pwRepeated    = "one character repeated"
	pwUnreadable  = "unreadable password hash"

	// pwAcuteBases are letters that each compose with a combining acute accent into one character.
	pwAcuteBases = "aeiouycnszlrgkm"

	// A hash of pwGood with two iterations and the salt of the bytes 0 to 15, made with Python's hashlib.pbkdf2_hmac.
	pwSaltText = "AAECAwQFBgcICQoLDA0ODw"
	pwKeyText  = "S51594wKma1bAMYvsz+TZOtBMNNA7lVsuSyfxwJ1Qoo"
	pwKnown    = "$pbkdf2-sha256$i=2$" + pwSaltText + "$" + pwKeyText
)

// pwMessage matches every message that checkPassword can return, and nothing else.
var pwMessage = regexp.MustCompile(`^(` + strings.Join([]string{
	pwInvalidUTF8, pwControl, pwSame, pwRepeated,
	`too short: [0-9]+ characters \(min 15\)`, `too long: [0-9]+ characters \(max 256\)`,
}, "|") + `)$`)

func pwShort(n int) string { return fmt.Sprintf("too short: %d characters (min 15)", n) }
func pwLong(n int) string  { return fmt.Sprintf("too long: %d characters (max 256)", n) }

// pwSequence is a reader of the bytes 0, 1, 2, ... (wrapping at 256) that goes on where its last read stopped.
type pwSequence struct{ next int }

func (s *pwSequence) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(s.next)
		s.next++
	}
	return len(p), nil
}

// pwFill returns the first n characters of pattern, repeated.
func pwFill(pattern string, n int) string {
	runes := []rune(pattern)
	var b strings.Builder
	for i := range n {
		b.WriteRune(runes[i%len(runes)])
	}
	return b.String()
}

// pwAcutes returns n letters, each followed by a combining acute accent: 2n code points that NFC makes n characters of.
func pwAcutes(n int) string {
	var b strings.Builder
	for i := range n {
		b.WriteByte(pwAcuteBases[i%len(pwAcuteBases)])
		b.WriteString(acute)
	}
	return b.String()
}

// pwJamo returns n Hangul syllables, each as a leading consonant and a vowel, which NFC composes.
func pwJamo(n int) string {
	var b strings.Builder
	for i := range n {
		b.WriteRune(rune(0x1100 + i%19))
		b.WriteRune(rune(0x1161 + i%21))
	}
	return b.String()
}

// pwEmoji returns n characters of four bytes each.
func pwEmoji(n int) string {
	var b strings.Builder
	for i := range n {
		b.WriteRune(rune(0x1f600 + i%64))
	}
	return b.String()
}

// pwB64 is the unpadded standard base64 of n bytes b.
func pwB64(n int, b byte) string {
	return base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{b}, n))
}

// pwForm writes a hash of the form of hashPassword from the text of its three parts.
func pwForm(count, salt, key string) string {
	return "$pbkdf2-sha256$i=" + count + "$" + salt + "$" + key
}

// pwFirstDiff returns where a and b first differ, so that a failure message can say so without showing either.
func pwFirstDiff(a, b string) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return min(len(a), len(b))
}

func TestDeriveRFC7914(t *testing.T) {
	// The two PBKDF2-HMAC-SHA-256 test vectors of RFC 7914, section 11.
	tests := []struct {
		password   string
		salt       string
		iterations int
		want       string
	}{
		{"passwd", "salt", 1, "55ac046e56e3089fec1691c22544b605f94185216dde0465e68b9d57c20dacbc49ca9cccf179b645991664b39d77ef317c71b845b1e30bd509112041d3a19783"},
		{"Password", "NaCl", 80000, "4ddcd8f60b98be21830cee5ef22701f9641a4418d04c0414aeff08876b34ab56a1d425a1225833549adb841b51c9b3176a272bdebba1d078478f62b397f33c8d"},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("c=%d", tt.iterations), func(t *testing.T) {
			got, err := derive(tt.password, []byte(tt.salt), tt.iterations, 64)
			if err != nil {
				t.Fatalf("derive() = %v", err)
			}
			if h := hex.EncodeToString(got); h != tt.want {
				t.Errorf("derive() = %s, want %s", h, tt.want)
			}
		})
	}
	if _, err := derive("passwd", []byte("salt"), 1, 0); err == nil {
		t.Error("derive() of a key of 0 bytes succeeded")
	}
}

// TestHashPassword is the one test that hashes with the cost of real passwords.
func TestHashPassword(t *testing.T) {
	encoded, err := hashPassword(pwGood, defaultIterations, rand.Reader)
	if err != nil {
		t.Fatalf("hashPassword() = %v", err)
	}
	if !strings.HasPrefix(encoded, "$pbkdf2-sha256$i=600000$") {
		t.Error("the hash does not start with $pbkdf2-sha256$i=600000$")
	}
	if strings.Contains(encoded, pwGood) {
		t.Error("the hash contains the password")
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 5 || parts[0] != "" || parts[1] != "pbkdf2-sha256" || parts[2] != "i=600000" {
		t.Fatalf("the hash has %d parts between dollar signs, want an empty one, the algorithm, i=600000, a salt and a key", len(parts))
	}
	if salt, err := base64.RawStdEncoding.DecodeString(parts[3]); err != nil || len(salt) != 16 {
		t.Errorf("the salt is %d bytes of unpadded base64, %v, want 16 bytes", len(salt), err)
	}
	if key, err := base64.RawStdEncoding.DecodeString(parts[4]); err != nil || len(key) != 32 {
		t.Errorf("the key is %d bytes of unpadded base64, %v, want 32 bytes", len(key), err)
	}
	n, salt, key, err := parseHash(encoded)
	if err != nil || n != 600000 || len(salt) != 16 || len(key) != 32 {
		t.Errorf("parseHash() = %d, %d bytes, %d bytes, %v, want 600000, 16, 32, nil", n, len(salt), len(key), err)
	}

	ok, rehash, err := matchHash(encoded, pwGood, defaultIterations)
	if !ok || rehash || err != nil {
		t.Errorf("matchHash() of the password = %v, %v, %v, want true, false, nil", ok, rehash, err)
	}
	ok, rehash, err = matchHash(encoded, pwGood+"x", defaultIterations)
	if ok || rehash || err != nil {
		t.Errorf("matchHash() of another password = %v, %v, %v, want false, false, nil", ok, rehash, err)
	}
}

func TestHashCostConstants(t *testing.T) {
	// The cost of new hashes and the largest cost that a hash may name are part of what 7.3 says.
	if got, want := [2]int{defaultIterations, maxIterations}, [2]int{600000, 10000000}; got != want {
		t.Errorf("defaultIterations and maxIterations = %v, want %v", got, want)
	}
	if got, want := [4]int{minPasswordChars, maxPasswordChars, saltBytes, keyBytes}, [4]int{15, 256, 16, 32}; got != want {
		t.Errorf("the lengths of the passwords, the salt and the key = %v, want %v", got, want)
	}
}

func TestValidIterations(t *testing.T) {
	for n, want := range map[int]bool{
		math.MinInt: false, -1: false, 0: false, 1: true, 2: true, defaultIterations: true,
		maxIterations - 1: true, maxIterations: true, maxIterations + 1: false, math.MaxInt: false,
	} {
		if got := validIterations(n); got != want {
			t.Errorf("validIterations(%d) = %v, want %v", n, got, want)
		}
	}
}

func TestHashPasswordKnownAnswers(t *testing.T) {
	const cafe = "$pbkdf2-sha256$i=3$" + pwSaltText + "$ZMMVbbHi1zF5w6131jp/AA5nIwALMaXm9JmfUVJ1XH8"
	tests := []struct {
		name       string
		password   string
		iterations int
		want       string
	}{
		{"two iterations", pwGood, 2, pwKnown},
		{"one iteration", pwGood, 1, "$pbkdf2-sha256$i=1$" + pwSaltText + "$1j+h90zX49tJj90UoeiqNJN7oenPZj24OnkNRYqOhJo"},
		{"spaces and a composed character", " Caf" + eAcute + " au lait ", 3, cafe},
		{"spaces and the decomposed character", " Cafe" + acute + " au lait ", 3, cafe},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := hashPassword(tt.password, tt.iterations, &pwSequence{})
			if err != nil {
				t.Fatalf("hashPassword() = %v", err)
			}
			if got != tt.want {
				t.Errorf("hashPassword() differs from the known hash at byte %d of %d", pwFirstDiff(got, tt.want), len(tt.want))
			}
			if ok, _, err := matchHash(tt.want, tt.password, tt.iterations); !ok || err != nil {
				t.Errorf("matchHash() of the known hash = %v, %v, want true, nil", ok, err)
			}
		})
	}
}

func TestHashPasswordFresh(t *testing.T) {
	a, err := hashPassword(pwGood, pwLow, rand.Reader)
	if err != nil {
		t.Fatalf("hashPassword() = %v", err)
	}
	b, err := hashPassword(pwGood, pwLow, rand.Reader)
	if err != nil {
		t.Fatalf("hashPassword() = %v", err)
	}
	if a == b {
		t.Error("two hashes of one password are the same")
	}
	_, saltA, keyA, _ := parseHash(a)
	_, saltB, keyB, _ := parseHash(b)
	if bytes.Equal(saltA, saltB) || bytes.Equal(keyA, keyB) {
		t.Error("two hashes of one password have the same salt or the same key")
	}
	for name, h := range map[string]string{"first": a, "second": b} {
		if ok, _, err := matchHash(h, pwGood, pwLow); !ok || err != nil {
			t.Errorf("matchHash() of the %s hash = %v, %v, want true, nil", name, ok, err)
		}
	}

	// A reader that goes on gives a new salt each time, and a hash takes exactly the bytes of a salt.
	r := &pwSequence{}
	first, err := hashPassword(pwGood, pwLow, r)
	if err != nil {
		t.Fatalf("hashPassword() = %v", err)
	}
	second, err := hashPassword(pwGood, pwLow, r)
	if err != nil {
		t.Fatalf("hashPassword() = %v", err)
	}
	if first == second || r.next != 2*saltBytes {
		t.Errorf("two hashes from one reader: the same = %v, bytes read = %d, want different hashes and %d bytes", first == second, r.next, 2*saltBytes)
	}
}

func TestHashPasswordNormalization(t *testing.T) {
	hash := func(p string) string {
		t.Helper()
		h, err := hashPassword(p, pwLow, &pwSequence{})
		if err != nil {
			t.Fatalf("hashPassword() = %v", err)
		}
		return h
	}
	composed := "Caf" + eAcute + " au lait!"
	decomposed := "Cafe" + acute + " au lait!"
	if hash(composed) != hash(decomposed) {
		t.Error("the hash of the decomposed password differs from the hash of the composed one")
	}
	// Only NFC: no case folding, no compatibility mapping, no trimming.
	for name, other := range map[string]string{
		"another case":       strings.ToUpper(composed),
		"a full-width C":     string(rune(0xff23)) + composed[1:],
		"a leading space":    " " + composed,
		"a trailing space":   composed + " ",
		"a no-break space":   strings.Replace(composed, " ", noBreakSpace, 1),
		"a trailing newline": composed + "\n",
	} {
		if hash(composed) == hash(other) {
			t.Errorf("the hash of a password with %s is the hash of the password without", name)
		}
	}
}

func TestHashPasswordErrors(t *testing.T) {
	for _, n := range []int{0, -1, maxIterations + 1, math.MaxInt} {
		r := &pwSequence{}
		got, err := hashPassword(pwGood, n, r)
		if want := fmt.Sprintf("invalid iteration count %d", n); err == nil || err.Error() != want {
			t.Errorf("hashPassword() with %d iterations = %v, want %q", n, err, want)
		}
		if got != "" || r.next != 0 {
			t.Errorf("hashPassword() with %d iterations returned a hash or read %d bytes", n, r.next)
		}
	}

	boom := errors.New("boom")
	tests := []struct {
		name string
		r    io.Reader
		is   error
	}{
		{"an empty reader", bytes.NewReader(nil), io.EOF},
		{"15 bytes", io.LimitReader(&pwSequence{}, saltBytes-1), io.ErrUnexpectedEOF},
		{"a failing reader", iotest.ErrReader(boom), boom},
		{"a reader that fails after some bytes", io.MultiReader(io.LimitReader(&pwSequence{}, 10), iotest.ErrReader(boom)), boom},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := hashPassword(pwGood, pwLow, tt.r)
			if !errors.Is(err, tt.is) {
				t.Errorf("hashPassword() = %v, want an error that wraps %v", err, tt.is)
			}
			if got != "" {
				t.Error("hashPassword() returned a hash with its error")
			}
			if err != nil && strings.Contains(err.Error(), pwGood) {
				t.Error("the error contains the password")
			}
		})
	}
}

func TestMatchHash(t *testing.T) {
	hash := func(p string) string {
		t.Helper()
		h, err := hashPassword(p, pwLow, rand.Reader)
		if err != nil {
			t.Fatalf("hashPassword() = %v", err)
		}
		return h
	}
	stored := hash(pwGood)
	composed := " Caf" + eAcute + " au lait "
	decomposed := " Cafe" + acute + " au lait "

	tests := []struct {
		name       string
		encoded    string
		password   string
		iterations int
		ok, rehash bool
	}{
		{"the password", stored, pwGood, pwLow, true, false},
		{"a cost below the stored one", stored, pwGood, pwLow - 1, true, false},
		{"a cost of zero", stored, pwGood, 0, true, false},
		{"a negative cost", stored, pwGood, -5, true, false},
		{"a cost above the stored one", stored, pwGood, pwLow + 1, true, true},
		{"a cost far above the stored one", stored, pwGood, defaultIterations, true, true},
		{"a wrong password", stored, "correct horse battery stapl", pwLow, false, false},
		{"a wrong password and a higher cost", stored, "correct horse battery stapler", pwLow + 1, false, false},
		{"the empty password", stored, "", pwLow, false, false},
		{"the password in upper case", stored, strings.ToUpper(pwGood), pwLow, false, false},
		{"the password with a trailing space", stored, pwGood + " ", pwLow, false, false},
		{"the password with a leading space", stored, " " + pwGood, pwLow, false, false},
		{"the password with a trailing line break", stored, pwGood + "\n", pwLow, false, false},
		{"the password with a NUL inside", stored, pwGood[:10] + "\x00" + pwGood[10:], pwLow, false, false},
		{"composed, for the composed hash", hash(composed), composed, pwLow, true, false},
		{"decomposed, for the composed hash", hash(composed), decomposed, pwLow, true, false},
		{"composed, for the decomposed hash", hash(decomposed), composed, pwLow, true, false},
		{"decomposed, for the decomposed hash", hash(decomposed), decomposed, pwLow, true, false},
		{"without the spaces, for the hash with them", hash(composed), strings.TrimSpace(composed), pwLow, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok, rehash, err := matchHash(tt.encoded, tt.password, tt.iterations)
			if ok != tt.ok || rehash != tt.rehash || err != nil {
				t.Errorf("matchHash() = %v, %v, %v, want %v, %v, nil", ok, rehash, err, tt.ok, tt.rehash)
			}
		})
	}
}

// TestMatchHashKeyPadding records a property of PBKDF2 with HMAC that matchHash does not change: HMAC pads a key that
// is shorter than its block of 64 bytes with NUL bytes, so a password and the same password followed by NULs have one
// key. No password has a NUL, because checkPassword refuses control characters, so it does not weaken a password.
func TestMatchHashKeyPadding(t *testing.T) {
	hash, err := hashPassword(pwGood, pwLow, rand.Reader)
	if err != nil {
		t.Fatalf("hashPassword() = %v", err)
	}
	for _, p := range []string{pwGood + "\x00", pwGood + "\x00\x00\x00"} {
		if ok, _, err := matchHash(hash, p, pwLow); !ok || err != nil {
			t.Errorf("matchHash() of the password and %d NULs = %v, %v, want true, nil (HMAC pads the key)", len(p)-len(pwGood), ok, err)
		}
	}
	if err := checkPassword(pwGood+"\x00", "", "", ""); err == nil || err.Error() != pwControl {
		t.Errorf("checkPassword() of the password and a NUL = %v, want %q", err, pwControl)
	}
}

// TestMatchHashParts checks that the salt, the count and the whole key of the hash are used.
func TestMatchHashParts(t *testing.T) {
	enc := base64.RawStdEncoding
	const password = "a password of the tests"
	salt := bytes.Repeat([]byte{7}, 20)
	key, err := derive(password, salt, 5, keyBytes)
	if err != nil {
		t.Fatalf("derive() = %v", err)
	}
	form := func(count string, salt, key []byte) string {
		return pwForm(count, enc.EncodeToString(salt), enc.EncodeToString(key))
	}
	flipped := func(b []byte, i int) []byte {
		c := bytes.Clone(b)
		c[i] ^= 1
		return c
	}

	// A hash made elsewhere, with another count and a longer salt.
	if ok, rehash, err := matchHash(form("5", salt, key), password, 6); !ok || !rehash || err != nil {
		t.Errorf("matchHash() of a hash of 5 iterations and a salt of 20 bytes = %v, %v, %v, want true, true, nil", ok, rehash, err)
	}
	for name, changed := range map[string]string{
		"the first byte of the key": form("5", salt, flipped(key, 0)),
		"the last byte of the key":  form("5", salt, flipped(key, keyBytes-1)),
		"the first byte of salt":    form("5", flipped(salt, 0), key),
		"the last byte of the salt": form("5", flipped(salt, len(salt)-1), key),
		"the count":                 form("6", salt, key),
	} {
		if ok, rehash, err := matchHash(changed, password, 5); ok || rehash || err != nil {
			t.Errorf("matchHash() of a hash with another %s = %v, %v, %v, want false, false, nil", name, ok, rehash, err)
		}
	}
}

func TestMatchHashUnreadable(t *testing.T) {
	for _, encoded := range []string{
		"", "x", "$", "plaintext password", pwGood, pwForm("0", pwB64(16, 1), pwB64(32, 1)),
		pwForm("2", pwB64(15, 1), pwB64(32, 1)), pwForm("2", pwB64(16, 1), pwB64(31, 1)), pwKnown + "\n",
	} {
		ok, rehash, err := matchHash(encoded, pwGood, pwLow)
		if ok || rehash || !errors.Is(err, errUnreadableHash) || err.Error() != pwUnreadable {
			t.Errorf("matchHash() of an unreadable hash of %d bytes = %v, %v, %v, want false, false, %q", len(encoded), ok, rehash, err, pwUnreadable)
		}
	}
}

func TestParseHashAccepts(t *testing.T) {
	std := base64.RawStdEncoding
	wantSalt, _ := std.DecodeString(pwSaltText)
	wantKey, _ := std.DecodeString(pwKeyText)
	n, salt, key, err := parseHash(pwKnown)
	if err != nil || n != 2 || !bytes.Equal(salt, wantSalt) || !bytes.Equal(key, wantKey) {
		t.Errorf("parseHash() = %d, %d bytes, %d bytes, %v, want 2, the salt and the key of the hash, nil", n, len(salt), len(key), err)
	}

	counts := []struct {
		text string
		want int
	}{{"1", 1}, {"2", 2}, {"9", 9}, {"10", 10}, {"99", 99}, {"600000", 600000}, {"9999999", 9999999}, {"10000000", 10000000}}
	for _, c := range counts {
		if n, _, _, err := parseHash(pwForm(c.text, pwB64(16, 1), pwB64(32, 2))); err != nil || n != c.want {
			t.Errorf("parseHash() with i=%s = %d, %v, want %d, nil", c.text, n, err, c.want)
		}
	}
	for _, size := range []int{16, 17, 18, 19, 20, 24, 32, 33, 64, 1000} {
		salt := bytes.Repeat([]byte{0x5a}, size)
		if _, got, _, err := parseHash(pwForm("1", std.EncodeToString(salt), pwB64(32, 2))); err != nil || !bytes.Equal(got, salt) {
			t.Errorf("parseHash() with a salt of %d bytes = %v, want the salt", size, err)
		}
	}

	// The characters of the standard alphabet that the URL one writes otherwise: plus signs and slashes.
	for name, salt := range map[string][]byte{"plus signs and slashes": bytes.Repeat([]byte{0xfb}, 16), "slashes": bytes.Repeat([]byte{0xff}, 16)} {
		text := std.EncodeToString(salt)
		if !strings.ContainsAny(text, "+/") {
			t.Fatalf("the salt of %s has no plus sign or slash", name)
		}
		if _, got, _, err := parseHash(pwForm("1", text, pwB64(32, 2))); err != nil || !bytes.Equal(got, salt) {
			t.Errorf("parseHash() with a salt that has %s = %v, want the salt", name, err)
		}
		if _, _, _, err := parseHash(pwForm("1", base64.RawURLEncoding.EncodeToString(salt), pwB64(32, 2))); err == nil {
			t.Errorf("parseHash() accepted the URL alphabet in a salt that has %s", name)
		}
	}

	// The hashes that the tests of the row make are in the form too.
	for i := range 256 {
		if _, _, _, err := parseHash(testPasswordHash(byte(i))); err != nil {
			t.Errorf("parseHash() of the test hash %d = %v", i, err)
		}
	}
}

func TestParseHashRejects(t *testing.T) {
	salt16, key32 := pwB64(16, 1), pwB64(32, 2)
	rejected := []struct {
		name string
		s    string
	}{
		{"empty", ""},
		{"a space", " "},
		{"a dollar sign", "$"},
		{"dollar signs only", "$$$$$"},
		{"plain text", pwGood},
		{"no leading dollar sign", pwKnown[1:]},
		{"a leading space", " " + pwKnown},
		{"a trailing space", pwKnown + " "},
		{"a trailing line break", pwKnown + "\n"},
		{"a trailing carriage return and line break", pwKnown + "\r\n"},
		{"a trailing dollar sign", pwKnown + "$"},
		{"a trailing part", pwKnown + "$x"},
		{"a trailing part that is a key", pwKnown + "$" + pwKeyText},
		{"a second dollar sign at the start", "$" + pwKnown},
		{"the hash twice", pwKnown + pwKnown},
		{"a NUL at the end", pwKnown + "\x00"},

		{"another algorithm: sha512", strings.Replace(pwKnown, "sha256", "sha512", 1)},
		{"another algorithm: sha1", strings.Replace(pwKnown, "sha256", "sha1", 1)},
		{"another algorithm: none", strings.Replace(pwKnown, "-sha256", "", 1)},
		{"the algorithm in upper case", strings.Replace(pwKnown, "pbkdf2-sha256", "PBKDF2-SHA256", 1)},
		{"the algorithm with a space", strings.Replace(pwKnown, "sha256", "sha256 ", 1)},
		{"the algorithm with an underscore", strings.Replace(pwKnown, "-sha256", "_sha256", 1)},
		{"an empty algorithm", strings.Replace(pwKnown, "pbkdf2-sha256", "", 1)},
		{"the argon2id form", "$argon2id$v=19$m=65536,t=3,p=4$" + pwSaltText + "$" + pwKeyText},
		{"the bcrypt form", "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"},
		{"the scrypt form", "$scrypt$ln=17,r=8,p=1$" + pwSaltText + "$" + pwKeyText},

		{"the three parts without the algorithm", "i=2$" + pwSaltText + "$" + pwKeyText},
		{"the three parts after another prefix", "x" + pwKnown[len("$pbkdf2-sha256$"):]},
		{"no key", "$pbkdf2-sha256$i=2$" + pwSaltText},
		{"no salt and no key", "$pbkdf2-sha256$i=2"},
		{"no count, salt and key", "$pbkdf2-sha256$"},
		{"no count", "$pbkdf2-sha256$" + pwSaltText + "$" + pwKeyText},
		{"an empty count between dollar signs", "$pbkdf2-sha256$$" + pwSaltText + "$" + pwKeyText},
		{"an empty salt", pwForm("2", "", pwKeyText)},
		{"an empty key", pwForm("2", pwSaltText, "")},
		{"an empty salt and key", pwForm("2", "", "")},
		{"an extra part before the salt", pwForm("2", "x$"+pwSaltText, pwKeyText)},
		{"two keys", pwForm("2", pwSaltText, pwKeyText+"$"+pwKeyText)},

		{"zero iterations", pwForm("0", salt16, key32)},
		{"two zeros", pwForm("00", salt16, key32)},
		{"a leading zero", pwForm("01", salt16, key32)},
		{"a leading zero in a large count", pwForm("0600000", salt16, key32)},
		{"a plus sign", pwForm("+5", salt16, key32)},
		{"a minus sign", pwForm("-5", salt16, key32)},
		{"a minus sign and zero", pwForm("-0", salt16, key32)},
		{"a decimal point", pwForm("5.0", salt16, key32)},
		{"an exponent", pwForm("1e3", salt16, key32)},
		{"hexadecimal with a prefix", pwForm("0x10", salt16, key32)},
		{"hexadecimal digits", pwForm("ff", salt16, key32)},
		{"an underscore", pwForm("1_000", salt16, key32)},
		{"a leading space in the count", pwForm(" 5", salt16, key32)},
		{"a trailing space in the count", pwForm("5 ", salt16, key32)},
		{"an empty count", pwForm("", salt16, key32)},
		{"another parameter after the count", pwForm("5,m=3", salt16, key32)},
		{"above the maximum", pwForm("10000001", salt16, key32)},
		{"nine digits", pwForm("100000000", salt16, key32)},
		{"a count that overflows", pwForm("99999999999999999999999999", salt16, key32)},
		{"a count that wraps around to 1 in 64 bits", pwForm("18446744073709551617", salt16, key32)},
		{"a count that wraps around to 1 in 32 bits", pwForm("4294967297", salt16, key32)},
		{"a count of full-width digits", pwForm(string(rune(0xff15)), salt16, key32)},
		{"a count of Arabic-Indic digits", pwForm(string(rune(0x665)), salt16, key32)},
		{"the count in upper case", strings.Replace(pwKnown, "i=", "I=", 1)},
		{"another name for the count", strings.Replace(pwKnown, "i=", "n=", 1)},
		{"a long name for the count", strings.Replace(pwKnown, "i=", "iter=", 1)},
		{"a count without a name", strings.Replace(pwKnown, "i=2", "2", 1)},

		{"a salt of 15 bytes", pwForm("2", pwB64(15, 1), key32)},
		{"a salt of 8 bytes", pwForm("2", pwB64(8, 1), key32)},
		{"a salt of 1 byte", pwForm("2", pwB64(1, 1), key32)},
		{"a salt of 16 bytes with padding", pwForm("2", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 16)), key32)},
		{"a salt of 17 bytes with padding", pwForm("2", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 17)), key32)},
		{"a salt with a single padding sign", pwForm("2", salt16+"=", key32)},
		{"a salt in the URL alphabet", pwForm("2", base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0xfb}, 16)), key32)},
		{"a salt with a space", pwForm("2", pwSaltText[:10]+" "+pwSaltText[11:], key32)},
		{"a salt with a leading space", pwForm("2", " "+pwSaltText, key32)},
		{"a salt with a trailing space", pwForm("2", pwSaltText+" ", key32)},
		{"a salt with an asterisk", pwForm("2", pwSaltText[:10]+"*"+pwSaltText[11:], key32)},
		{"a salt with a NUL", pwForm("2", pwSaltText[:10]+"\x00"+pwSaltText[11:], key32)},
		{"a salt with a non-ASCII character", pwForm("2", pwSaltText[:10]+eAcute+pwSaltText[11:], key32)},
		{"a salt with a line break inside", pwForm("2", pwSaltText[:10]+"\n"+pwSaltText[10:], key32)},
		{"a salt with a carriage return inside", pwForm("2", pwSaltText[:10]+"\r"+pwSaltText[10:], key32)},
		{"a salt with a line break at the end", pwForm("2", pwSaltText+"\n", key32)},
		{"a salt with a line break at the start", pwForm("2", "\n"+pwSaltText, key32)},
		{"a salt with bits left over", pwForm("2", pwSaltText[:21]+"x", key32)},
		{"a salt with the lowest bit left over", pwForm("2", pwSaltText[:21]+"B", key32)},
		{"a salt of 21 characters", pwForm("2", pwSaltText[:21], key32)},

		{"a key of 31 bytes", pwForm("2", salt16, pwB64(31, 2))},
		{"a key of 33 bytes", pwForm("2", salt16, pwB64(33, 2))},
		{"a key of 16 bytes", pwForm("2", salt16, pwB64(16, 2))},
		{"a key of 64 bytes", pwForm("2", salt16, pwB64(64, 2))},
		{"a key of 1 byte", pwForm("2", salt16, pwB64(1, 2))},
		{"a key of 32 bytes with padding", pwForm("2", salt16, base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32)))},
		{"a key with a single padding sign", pwForm("2", salt16, key32+"=")},
		{"a key in the URL alphabet", pwForm("2", salt16, base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0xfb}, 32)))},
		{"a key with a space", pwForm("2", salt16, key32[:10]+" "+key32[11:])},
		{"a key with a trailing space", pwForm("2", salt16, key32+" ")},
		{"a key with an asterisk", pwForm("2", salt16, key32[:10]+"*"+key32[11:])},
		{"a key with a NUL", pwForm("2", salt16, key32[:10]+"\x00"+key32[11:])},
		{"a key with a non-ASCII character", pwForm("2", salt16, key32[:10]+eAcute+key32[11:])},
		{"a key with a line break inside", pwForm("2", salt16, key32[:10]+"\n"+key32[10:])},
		{"a key with a carriage return inside", pwForm("2", salt16, key32[:10]+"\r"+key32[10:])},
		{"a key with a line break at the end", pwForm("2", salt16, key32+"\n")},
		{"a key with a line break at the start", pwForm("2", salt16, "\n"+key32)},
		{"a key with bits left over", pwForm("2", salt16, pwKeyText[:42]+"p")},
		{"a key with the lowest bit left over", pwForm("2", salt16, pwKeyText[:42]+"B")},
		{"a key of 42 characters", pwForm("2", salt16, pwKeyText[:42])},
		{"a key of 44 characters", pwForm("2", salt16, pwKeyText+"A")},
	}
	for _, tt := range rejected {
		t.Run(tt.name, func(t *testing.T) {
			n, salt, key, err := parseHash(tt.s)
			if err == nil || err.Error() != pwUnreadable || !errors.Is(err, errUnreadableHash) {
				t.Errorf("parseHash() of a text of %d bytes = %v, want %q", len(tt.s), err, pwUnreadable)
			}
			if n != 0 || salt != nil || key != nil {
				t.Errorf("parseHash() returned %d, %d bytes, %d bytes with its error", n, len(salt), len(key))
			}
		})
	}

	// The bits that a base64 text has left over must be zero: the last character of a salt of 16 bytes has 4 of them and
	// that of a key of 32 bytes has 2.
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	for i := range len(alphabet) {
		_, _, _, err := parseHash(pwForm("2", pwSaltText[:21]+string(alphabet[i]), key32))
		if wantOK := i%16 == 0; (err == nil) != wantOK {
			t.Errorf("parseHash() with %q ending the salt: error %v, want accepted = %v", alphabet[i], err, wantOK)
		}
		_, _, _, err = parseHash(pwForm("2", salt16, pwKeyText[:42]+string(alphabet[i])))
		if wantOK := i%4 == 0; (err == nil) != wantOK {
			t.Errorf("parseHash() with %q ending the key: error %v, want accepted = %v", alphabet[i], err, wantOK)
		}
	}
}

func TestPasswordRules(t *testing.T) {
	type who struct{ login, email, name string }
	defaults := who{login: "taro.yamada.2026", email: "taro.yamada@example.com", name: "Ren" + eAcute + " Zellweger Jr"}
	nameDecomposed := "Rene" + acute + " Zellweger Jr"
	letters := "abcdefghij"
	hiragana := "あいうえおかきくけこ"
	expands := string(rune(0x958)) // DEVANAGARI LETTER QA: NFC makes two characters of it

	tests := []struct {
		name string
		p    string
		want string // the message, or "" for a password that passes
		who  *who   // the login name, the email address and the name; defaults if nil
	}{
		{name: "the password of the tests", p: pwGood},
		{name: "15 characters", p: pwFill(letters, 15)},
		{name: "256 characters", p: pwFill(letters, 256)},
		{name: "15 hiragana", p: pwFill(hiragana, 15)},
		{name: "256 hiragana", p: pwFill(hiragana, 256)},
		{name: "15 characters of four bytes", p: pwEmoji(15)},
		{name: "256 characters of four bytes", p: pwEmoji(256)},
		{name: "digits that are not one digit", p: "123456789012345"},
		{name: "only punctuation", p: "!@#$%^&*()_+-=[]{}"},
		{name: "only lower case letters", p: "abcdefghijklmnopqrstuvwxyz"},
		{name: "a sentence", p: "Correct horse, battery staple!"},
		{name: "letters in two cases that differ", p: "aAaAaAaAaAaAaAa"},
		{name: "14 times a and a b", p: strings.Repeat("a", 14) + "b"},
		{name: "a b and 14 times a", p: "b" + strings.Repeat("a", 14)},
		{name: "the same two characters again and again", p: strings.Repeat("ab", 8)},

		{name: "empty", p: "", want: pwShort(0)},
		{name: "1 character", p: "a", want: pwShort(1)},
		{name: "14 characters", p: pwFill(letters, 14), want: pwShort(14)},
		{name: "14 hiragana", p: pwFill(hiragana, 14), want: pwShort(14)},
		{name: "14 characters of four bytes", p: pwEmoji(14), want: pwShort(14)},
		{name: "14 spaces", p: strings.Repeat(" ", 14), want: pwShort(14)},
		{name: "14 times a", p: strings.Repeat("a", 14), want: pwShort(14)},
		{name: "257 characters", p: pwFill(letters, 257), want: pwLong(257)},
		{name: "1000 characters", p: pwFill(letters, 1000), want: pwLong(1000)},
		{name: "257 hiragana", p: pwFill(hiragana, 257), want: pwLong(257)},
		{name: "257 characters of four bytes", p: pwEmoji(257), want: pwLong(257)},
		{name: "257 times a", p: strings.Repeat("a", 257), want: pwLong(257)},

		{name: "15 letters with an accent, decomposed (30 code points)", p: pwAcutes(15)},
		{name: "14 letters with an accent, decomposed (28 code points)", p: pwAcutes(14), want: pwShort(14)},
		{name: "256 letters with an accent, decomposed (512 code points)", p: pwAcutes(256)},
		{name: "257 letters with an accent, decomposed", p: pwAcutes(257), want: pwLong(257)},
		{name: "15 letters with an accent, composed", p: nfc.Normalize(pwAcutes(15))},
		{name: "14 letters with an accent, composed", p: nfc.Normalize(pwAcutes(14)), want: pwShort(14)},
		{name: "256 letters with an accent, composed", p: nfc.Normalize(pwAcutes(256))},
		{name: "257 letters with an accent, composed", p: nfc.Normalize(pwAcutes(257)), want: pwLong(257)},
		{name: "composed and decomposed mixed", p: nfc.Normalize(pwAcutes(7)) + pwAcutes(8)},
		{name: "15 Hangul syllables as jamo (30 code points)", p: pwJamo(15)},
		{name: "14 Hangul syllables as jamo (28 code points)", p: pwJamo(14), want: pwShort(14)},
		{name: "256 Hangul syllables as jamo (512 code points)", p: pwJamo(256)},
		{name: "257 Hangul syllables as jamo", p: pwJamo(257), want: pwLong(257)},
		{name: "8 characters that NFC makes 16 of", p: strings.Repeat(expands, 8)},
		{name: "7 characters that NFC makes 14 of", p: strings.Repeat(expands, 7), want: pwShort(14)},
		{name: "128 characters that NFC makes 256 of", p: strings.Repeat(expands, 128)},
		{name: "129 characters that NFC makes 258 of", p: strings.Repeat(expands, 129), want: pwLong(258)},

		{name: "a leading space is kept", p: " " + pwFill(letters, 14)},
		{name: "a trailing space is kept", p: pwFill(letters, 14) + " "},
		{name: "surrounding spaces are kept", p: "  " + pwFill(letters, 11) + "  "},
		{name: "spaces in the middle", p: pwFill(letters, 7) + "  " + pwFill(letters, 6)},
		{name: "without the spaces it is too short", p: pwFill(letters, 11), want: pwShort(11)},
		{name: "a no-break space and an ideographic space count", p: pwFill(letters, 13) + noBreakSpace + ideographicSpace},
		{name: "a line separator and a zero width space are no control characters", p: pwFill(letters, 13) + lineSeparator + string(rune(0x200b))},
		{name: "15 spaces", p: strings.Repeat(" ", 15), want: pwRepeated},

		{name: "an invalid byte", p: "\xff" + pwFill(letters, 14), want: pwInvalidUTF8},
		{name: "an invalid byte in the middle", p: pwFill(letters, 7) + "\xfe" + pwFill(letters, 7), want: pwInvalidUTF8},
		{name: "a cut character", p: pwFill(letters, 14) + "\xe3\x81", want: pwInvalidUTF8},
		{name: "an overlong form", p: pwFill(letters, 14) + "\xc0\xaf", want: pwInvalidUTF8},
		{name: "a surrogate", p: pwFill(letters, 14) + "\xed\xa0\x80", want: pwInvalidUTF8},
		{name: "a code point above the maximum", p: pwFill(letters, 14) + "\xf4\x90\x80\x80", want: pwInvalidUTF8},
		{name: "only an invalid byte", p: "\xff", want: pwInvalidUTF8},
		{name: "an invalid byte before a control character", p: "\xff\x00", want: pwInvalidUTF8},
		{name: "an invalid byte in a password that is too long", p: pwFill(letters, 300) + "\xff", want: pwInvalidUTF8},
		{name: "the replacement character itself", p: pwFill(letters, 14) + replacement},

		{name: "a control character in a password that is too short", p: "a\tb", want: pwControl},
		{name: "a control character before a password that is too long", p: "\n" + pwFill(letters, 300), want: pwControl},
		{name: "a control character in a password that is the login name", p: defaults.login + "\x00", want: pwControl, who: &who{login: defaults.login + "\x00"}},
		{name: "15 tabs", p: strings.Repeat("\t", 15), want: pwControl},

		{name: "the login name", p: defaults.login, want: pwSame},
		{name: "the login name in upper case", p: strings.ToUpper(defaults.login), want: pwSame},
		{name: "the login name in mixed case", p: "Taro.Yamada.2026", want: pwSame},
		{name: "the email address", p: defaults.email, want: pwSame},
		{name: "the email address in upper case", p: strings.ToUpper(defaults.email), want: pwSame},
		{name: "the email address with the domain in upper case", p: "taro.yamada@EXAMPLE.com", want: pwSame},
		{name: "the name", p: defaults.name, want: pwSame},
		{name: "the name in upper case", p: strings.ToUpper(defaults.name), want: pwSame},
		{name: "the name in lower case", p: strings.ToLower(defaults.name), want: pwSame},
		{name: "the name decomposed, for the name composed", p: nameDecomposed, want: pwSame},
		{name: "the name composed, for the name decomposed", p: defaults.name, want: pwSame, who: &who{name: nameDecomposed}},
		{name: "the name decomposed, for the name decomposed", p: nameDecomposed, want: pwSame, who: &who{name: nameDecomposed}},
		{name: "a Japanese name", p: pwFill("山田太郎", 16), want: pwSame, who: &who{name: pwFill("山田太郎", 16)}},
		{name: "a login name of one repeated character is the same before it is repeated", p: strings.Repeat("a", 16), want: pwSame, who: &who{login: strings.Repeat("a", 16)}},
		{name: "the login name and a space", p: defaults.login + " "},
		{name: "a space and the login name", p: " " + defaults.login},
		{name: "the login name and a letter", p: defaults.login + "x"},
		{name: "the login name inside a longer password", p: "my " + defaults.login + " password"},
		{name: "the email address inside a longer password", p: "my " + defaults.email + " password"},
		{name: "the part of the email address before the at sign", p: "taro.yamada" + pwFill(letters, 4)},
		{name: "fields that are empty are not compared", p: pwGood, who: &who{}},
		{name: "a field that is not empty is compared when the others are empty", p: defaults.email, want: pwSame, who: &who{email: defaults.email}},

		{name: "15 times a", p: strings.Repeat("a", 15), want: pwRepeated},
		{name: "256 times a", p: strings.Repeat("a", 256), want: pwRepeated},
		{name: "15 times a hiragana", p: strings.Repeat("あ", 15), want: pwRepeated},
		{name: "15 times a character of four bytes", p: strings.Repeat(string(rune(0x1f600)), 15), want: pwRepeated},
		{name: "15 times a letter with an accent, decomposed", p: strings.Repeat("e"+acute, 15), want: pwRepeated},
		{name: "15 times a letter with an accent, composed", p: strings.Repeat(eAcute, 15), want: pwRepeated},
		{name: "15 times a no-break space", p: strings.Repeat(noBreakSpace, 15), want: pwRepeated},
		{name: "15 times the replacement character", p: strings.Repeat(replacement, 15), want: pwRepeated},
		{name: "15 times a digit", p: strings.Repeat("7", 15), want: pwRepeated},
		{name: "14 times a letter with an accent, decomposed", p: strings.Repeat("e"+acute, 14), want: pwShort(14)},
		{name: "257 times a hiragana", p: strings.Repeat("あ", 257), want: pwLong(257)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := defaults
			if tt.who != nil {
				w = *tt.who
			}
			err := checkPassword(tt.p, w.login, w.email, w.name)
			switch {
			case tt.want == "" && err != nil:
				t.Errorf("checkPassword() = %v, want nil", err)
			case tt.want != "" && (err == nil || err.Error() != tt.want):
				t.Errorf("checkPassword() = %v, want %q", err, tt.want)
			}
			if err != nil && !pwMessage.MatchString(err.Error()) {
				t.Errorf("the message of %d bytes is not one of the fixed messages", len(err.Error()))
			}
		})
	}

	controls := []struct{ name, c string }{
		{"NUL", "\x00"}, {"tab", "\t"}, {"line feed", "\n"}, {"vertical tab", "\v"}, {"form feed", "\f"}, {"carriage return", "\r"},
		{"escape", "\x1b"}, {"unit separator", "\x1f"}, {"delete", del}, {"U+0080", string(rune(0x80))},
		{"next line", string(rune(0x85))}, {"U+009F", string(rune(0x9f))},
	}
	long := pwFill(letters, 20)
	for _, c := range controls {
		for place, p := range map[string]string{"at the start": c.c + long, "in the middle": long[:10] + c.c + long[10:], "at the end": long + c.c} {
			if err := checkPassword(p, "", "", ""); err == nil || err.Error() != pwControl {
				t.Errorf("checkPassword() of a password with %s %s = %v, want %q", c.name, place, err, pwControl)
			}
		}
	}

	// Only the control characters (category Cc) are refused: the other invisible or odd characters are passwords too.
	for _, r := range []rune{0x20, 0x21, 0x7e, 0xa0, 0xa1, 0xad, 0x2028, 0x2029, 0x200b, 0x200e, 0x202e, 0xfeff, 0xe000} {
		if err := checkPassword(long+string(r), "", "", ""); err != nil {
			t.Errorf("checkPassword() of a password with U+%04X = %v, want nil", r, err)
		}
	}
}

// TestPasswordRulesOrder checks which problem comes first when a password has several.
func TestPasswordRulesOrder(t *testing.T) {
	login := strings.Repeat("a", 16)
	tests := []struct {
		name string
		p    string
		want string
	}{
		{"invalid UTF-8 before control characters", "\xff\x00", pwInvalidUTF8},
		{"invalid UTF-8 before the length", "\xff", pwInvalidUTF8},
		{"control characters before the length", "\x00", pwControl},
		{"the length before the login name", "aaaa", pwShort(4)},
		{"the login name before the repeat", login, pwSame},
		{"the repeat last", strings.Repeat("b", 16), pwRepeated},
		{"the length of the composed form", pwAcutes(14), pwShort(14)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := checkPassword(tt.p, login, "", ""); err == nil || err.Error() != tt.want {
				t.Errorf("checkPassword() = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestIsRepeated(t *testing.T) {
	for s, want := range map[string]bool{
		"": false, "a": false, "aa": true, "ab": false, "aaa": true, "aab": false, "baa": false, "aba": false,
		"あああ": true, "あいあ": false, strings.Repeat(eAcute, 3): true, strings.Repeat("e"+acute, 3): false,
		strings.Repeat(replacement, 2): true, strings.Repeat(" ", 15): true, strings.Repeat("a", 14) + "b": false,
	} {
		if got := isRepeated(s); got != want {
			t.Errorf("isRepeated(%q) = %v, want %v", s, got, want)
		}
	}
}

// TestPasswordMessagesShowNothing checks that no part of a password is in the message about it.
func TestPasswordMessagesShowNothing(t *testing.T) {
	marker := "Qx9-zebra-" + string(rune(0x3042)) + string(rune(0x3044)) + "-Vk7"
	tests := []struct {
		name  string
		p     string
		login string
	}{
		{"invalid UTF-8", marker + "\xff", ""},
		{"a control character", marker + "\t", ""},
		{"too short", marker[:8], ""},
		{"too long", strings.Repeat(marker, 30), ""},
		{"the login name", marker + marker, marker + marker},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkPassword(tt.p, tt.login, "", "")
			if err == nil {
				t.Fatal("checkPassword() = nil, want a message")
			}
			runes := []rune(marker)
			for i := 0; i+4 <= len(runes); i++ {
				if part := string(runes[i : i+4]); strings.Contains(err.Error(), part) {
					t.Errorf("the message of %d bytes contains a part of the password (from character %d)", len(err.Error()), i)
				}
			}
		})
	}
}

// TestPasswordTestHelpers checks the text that the tests build: what they count on NFC to do.
func TestPasswordTestHelpers(t *testing.T) {
	for _, n := range []int{1, 14, 15, 256, 257} {
		if got := utf8.RuneCountInString(pwAcutes(n)); got != 2*n {
			t.Errorf("pwAcutes(%d) has %d code points, want %d", n, got, 2*n)
		}
		if got := utf8.RuneCountInString(nfc.Normalize(pwAcutes(n))); got != n {
			t.Errorf("NFC of pwAcutes(%d) has %d characters, want %d", n, got, n)
		}
		if got := utf8.RuneCountInString(nfc.Normalize(pwJamo(n))); got != n {
			t.Errorf("NFC of pwJamo(%d) has %d characters, want %d", n, got, n)
		}
		if got := utf8.RuneCountInString(nfc.Normalize(strings.Repeat(string(rune(0x958)), n))); got != 2*n {
			t.Errorf("NFC of %d times U+0958 has %d characters, want %d", n, got, 2*n)
		}
		if got := len(pwEmoji(n)); got != 4*n {
			t.Errorf("pwEmoji(%d) is %d bytes, want %d", n, got, 4*n)
		}
	}
	if got := pwFill("ab", 5); got != "ababa" {
		t.Errorf("pwFill() = %q, want ababa", got)
	}
}

func FuzzCheckPassword(f *testing.F) {
	for _, s := range []string{
		pwGood, "", "a", strings.Repeat("a", 15), strings.Repeat("a", 257), pwAcutes(15), pwAcutes(14), pwJamo(15),
		"\xff", "a\tb", strings.Repeat("a", 14) + "\x00", "taro.yamada.2026", "Taro.Yamada.2026", strings.Repeat("e"+acute, 15),
		strings.Repeat(string(rune(0x958)), 8),
	} {
		f.Add(s, "taro.yamada.2026", "taro.yamada@example.com", "Ren"+eAcute+" Zellweger Jr")
	}
	f.Fuzz(func(t *testing.T, p, login, email, name string) {
		err := checkPassword(p, login, email, name)
		if err != nil {
			if !pwMessage.MatchString(err.Error()) {
				t.Fatalf("checkPassword() returned a message of %d bytes that is not one of the fixed ones", len(err.Error()))
			}
			return
		}
		norm := nfc.Normalize(p)
		if n := utf8.RuneCountInString(norm); !utf8.ValidString(p) || strings.IndexFunc(p, unicode.IsControl) >= 0 || n < 15 || n > 256 {
			t.Fatalf("checkPassword() accepted a password of %d bytes that is invalid, has control characters or has %d characters", len(p), n)
		}
		for _, other := range []string{login, email, name} {
			if other != "" && strings.EqualFold(norm, nfc.Normalize(other)) {
				t.Fatal("checkPassword() accepted a password that is the same as the login name, the email address or the name")
			}
		}
		distinct := make(map[rune]bool)
		for _, r := range norm {
			distinct[r] = true
		}
		if len(distinct) < 2 {
			t.Fatal("checkPassword() accepted one character repeated")
		}
	})
}

func FuzzParseHash(f *testing.F) {
	for _, s := range []string{
		pwKnown, "", "$", testPasswordHash(0xab), pwKnown + "\n", pwKnown + "$x", strings.Replace(pwKnown, "i=2", "i=02", 1),
		strings.Replace(pwKnown, "i=2", "i=10000000", 1), strings.Replace(pwKnown, "i=2", "i=10000001", 1),
		pwKnown[:len(pwKnown)-1] + "p", strings.Replace(pwKnown, pwSaltText[:14], pwSaltText[:14]+"\n", 1),
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		n, salt, key, err := parseHash(s)
		if err != nil {
			if err.Error() != pwUnreadable || n != 0 || salt != nil || key != nil {
				t.Fatalf("parseHash() = %d, %d bytes, %d bytes, %v, want the zero values and %q", n, len(salt), len(key), err, pwUnreadable)
			}
			return
		}
		// What it accepts is exactly what hashPassword writes.
		enc := base64.RawStdEncoding
		if again := fmt.Sprintf("$pbkdf2-sha256$i=%d$%s$%s", n, enc.EncodeToString(salt), enc.EncodeToString(key)); again != s {
			t.Fatalf("parseHash() accepted a text of %d bytes that is not the form of hashPassword", len(s))
		}
		if n < 1 || n > maxIterations || len(salt) < saltBytes || len(key) != keyBytes {
			t.Fatalf("parseHash() = %d, %d bytes, %d bytes, out of bounds", n, len(salt), len(key))
		}
	})
}
