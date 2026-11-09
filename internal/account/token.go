package account

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
)

const (
	tokenBytes      = 32
	tokenChars      = 43 // the length of the text of a token: 32 bytes in base64url without padding
	tokenHashPrefix = "sha256:"
)

// newToken reads 256 random bits from r and returns them as the token that a person is given, in base64url, with the
// hash that the row of the account keeps in its place. The token sets up a credential of any kind.
func newToken(r io.Reader) (token, hash string, err error) {
	b := make([]byte, tokenBytes)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", "", fmt.Errorf("cannot read a token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, sumToken(token), nil
}

// tokenHash returns the hash of token, or false if token is not what newToken returns: 43 characters of base64url that
// decode to 32 bytes. A text of another length is refused before it is decoded, so that a long one costs nothing.
func tokenHash(token string) (string, bool) {
	if len(token) != tokenChars || len(decodeBase64(base64.RawURLEncoding, token)) != tokenBytes {
		return "", false
	}
	return sumToken(token), true
}

// sumToken is "sha256:" and the SHA-256 of the text of the token in lower case hexadecimal, which is what isTokenHash
// accepts. A hash without a key is enough for a secret of 256 random bits.
func sumToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return tokenHashPrefix + hex.EncodeToString(sum[:])
}

// equalHash compares two hashes in constant time.
func equalHash(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
