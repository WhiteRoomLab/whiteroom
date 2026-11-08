package account

import (
	"crypto/pbkdf2"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/WhiteRoomLab/whiteroom/internal/nfc"
)

const (
	minPasswordChars  = 15
	maxPasswordChars  = 256
	defaultIterations = 600000
	maxIterations     = 10000000
	saltBytes         = 16
	keyBytes          = 32
	hashPrefix        = "$pbkdf2-sha256$"
)

var errUnreadableHash = errors.New("unreadable password hash")

// checkPassword returns the first problem of the password p of an account with this login name, email address and name,
// or nil. It counts the characters of the NFC form of p, keeps its spaces and has no rule of character classes. Its
// messages show nothing of p.
func checkPassword(p, login, email, name string) error {
	if !utf8.ValidString(p) {
		return errors.New("invalid UTF-8")
	}
	if strings.ContainsFunc(p, unicode.IsControl) {
		return errors.New("contains control characters")
	}
	p = nfc.Normalize(p)
	n := utf8.RuneCountInString(p)
	switch {
	case n < minPasswordChars:
		return fmt.Errorf("too short: %d characters (min %d)", n, minPasswordChars)
	case n > maxPasswordChars:
		return fmt.Errorf("too long: %d characters (max %d)", n, maxPasswordChars)
	}
	for _, other := range []string{login, email, name} {
		if other != "" && strings.EqualFold(p, nfc.Normalize(other)) {
			return errors.New("the same as the login name, the email address or the name")
		}
	}
	if isRepeated(p) {
		return errors.New("one character repeated")
	}
	return nil
}

// isRepeated reports whether s is two or more times the same character.
func isRepeated(s string) bool {
	first, size := utf8.DecodeRuneInString(s)
	rest := s[size:]
	return rest != "" && strings.Trim(rest, string(first)) == ""
}

// derive returns the key of n bytes that PBKDF2 with HMAC-SHA-256 makes of p, which the caller has put in NFC.
func derive(p string, salt []byte, iterations, n int) ([]byte, error) {
	return pbkdf2.Key(sha256.New, p, salt, iterations, n)
}

// hashPassword returns the hash of the NFC form of p in the form that the table accounts keeps,
// $pbkdf2-sha256$i=<iterations>$<salt>$<key>, with a salt read from r. The salt and the key are in unpadded standard
// base64.
func hashPassword(p string, iterations int, r io.Reader) (string, error) {
	if !validIterations(iterations) {
		return "", fmt.Errorf("invalid iteration count %d", iterations)
	}
	salt := make([]byte, saltBytes)
	if _, err := io.ReadFull(r, salt); err != nil {
		return "", fmt.Errorf("cannot read a salt: %w", err)
	}
	key, err := derive(nfc.Normalize(p), salt, iterations, keyBytes)
	if err != nil {
		return "", err
	}
	enc := base64.RawStdEncoding
	return hashPrefix + fmt.Sprintf("i=%d$%s$%s", iterations, enc.EncodeToString(salt), enc.EncodeToString(key)), nil
}

// parseHash reads only the form that hashPassword writes: an iteration count from 1 to maxIterations in digits, a salt
// of at least saltBytes bytes and a key of keyBytes bytes in base64 that decodeBase64 accepts. Anything else is
// errUnreadableHash, which shows nothing of s.
func parseHash(s string) (iterations int, salt, key []byte, err error) {
	rest, hasPrefix := strings.CutPrefix(s, hashPrefix)
	parts := strings.Split(rest, "$")
	if !hasPrefix || len(parts) != 3 {
		return 0, nil, nil, errUnreadableHash
	}
	count, hasCount := strings.CutPrefix(parts[0], "i=")
	iterations, validCount := parseIterations(count)
	salt = decodeBase64(base64.RawStdEncoding, parts[1])
	key = decodeBase64(base64.RawStdEncoding, parts[2])
	if !hasCount || !validCount || len(salt) < saltBytes || len(key) != keyBytes {
		return 0, nil, nil, errUnreadableHash
	}
	return iterations, salt, key, nil
}

func validIterations(n int) bool {
	return 1 <= n && n <= maxIterations
}

// parseIterations reads a count that validIterations accepts, in digits only: no sign, space or leading zero.
func parseIterations(s string) (int, bool) {
	// A text with more digits than maxIterations has is refused here, so that n cannot overflow below.
	if s == "" || s[0] == '0' || len(s) > len(strconv.Itoa(maxIterations)) {
		return 0, false
	}
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		n = n*10 + int(s[i]-'0')
	}
	return n, validIterations(n)
}

// decodeBase64 decodes text, or returns nil if text is not what enc writes. The standard decoder also takes line breaks,
// which it ignores, and padding bits that are not zero.
func decodeBase64(enc *base64.Encoding, text string) []byte {
	b, err := enc.DecodeString(text)
	if err != nil || enc.EncodeToString(b) != text {
		return nil
	}
	return b
}

// matchHash reports whether the NFC form of p is the password of encoded, derived with the salt and the iteration count
// that encoded names, and compared in constant time. rehash is true only for a match, when encoded has fewer iterations
// than the iterations that new hashes get. A hash that does not parse is the error of parseHash.
func matchHash(encoded, p string, iterations int) (ok, rehash bool, err error) {
	n, salt, key, err := parseHash(encoded)
	if err != nil {
		return false, false, err
	}
	got, err := derive(nfc.Normalize(p), salt, n, len(key))
	if err != nil {
		return false, false, err
	}
	if subtle.ConstantTimeCompare(got, key) != 1 {
		return false, false, nil
	}
	return true, n < iterations, nil
}
