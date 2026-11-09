package account

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

const (
	// tokAlphabet is what a token is written in: base64url without padding.
	tokAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"

	// tokSample is the token of the bytes 0 to 31, and tokSampleHash is its hash, made with Python's hashlib.
	tokSample     = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8"
	tokSampleHash = "sha256:ea866a757e4c38babfa8127cbe9a409d3e1f93a00ff1488ff735fcf917afffd0"
)

// tokBytes returns n bytes that count up from 0.
func tokBytes(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i)
	}
	return b
}

func TestNewToken(t *testing.T) {
	tokens := make(map[string]bool)
	hashes := make(map[string]bool)
	for range 2000 {
		token, hash, err := newToken(rand.Reader)
		if err != nil {
			t.Fatalf("newToken() = %v", err)
		}
		if len(token) != 43 || strings.Trim(token, tokAlphabet) != "" {
			t.Fatalf("a token has %d characters, or characters outside the alphabet; want 43 of base64url", len(token))
		}
		if b, err := base64.RawURLEncoding.DecodeString(token); err != nil || len(b) != 32 {
			t.Fatalf("a token decodes to %d bytes, %v, want 32 bytes", len(b), err)
		}
		if got, ok := tokenHash(token); !ok || got != hash {
			t.Fatal("tokenHash() of a new token is not the hash that newToken returned")
		}
		if !isTokenHash(hash) || strings.Contains(hash, token) {
			t.Fatal("the hash of a new token is not in the form of the hash of a row, or contains the token")
		}
		if tokens[token] || hashes[hash] {
			t.Fatal("two new tokens, or their hashes, are the same")
		}
		tokens[token], hashes[hash] = true, true
	}
}

func TestTokenChars(t *testing.T) {
	if want := base64.RawURLEncoding.EncodedLen(tokenBytes); tokenChars != want || tokenChars != len(tokSample) {
		t.Errorf("tokenChars = %d, want %d: the length of %d bytes in base64url without padding", tokenChars, want, tokenBytes)
	}
	token, _, err := newToken(rand.Reader)
	if err != nil || len(token) != tokenChars {
		t.Errorf("newToken() = a token of %d characters, %v, want %d", len(token), err, tokenChars)
	}
}

func TestNewTokenKnown(t *testing.T) {
	tests := []struct {
		name  string
		in    []byte
		token string
		hash  string
	}{
		{"the bytes 0 to 31", tokBytes(32), tokSample, tokSampleHash},
		{"all ones", bytes.Repeat([]byte{0xff}, 32), strings.Repeat("_", 42) + "8", "sha256:225f7e75329dd45aa354975d73987319309393af3a4c6733bc13601a4f1b8796"},
		{"all zeros", make([]byte, 32), strings.Repeat("A", 43), "sha256:0f007385b6f9d4b7eeb2748605afe1a984a0a3bfa3f014d09e2a784ce9e5cd1a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Three bytes more than a token takes: newToken must leave them.
			r := bytes.NewReader(append(bytes.Clone(tt.in), 0xaa, 0xbb, 0xcc))
			token, hash, err := newToken(r)
			if err != nil {
				t.Fatalf("newToken() = %v", err)
			}
			if token != tt.token {
				t.Errorf("newToken() returned another token than the known one, which differs at byte %d", pwFirstDiff(token, tt.token))
			}
			if hash != tt.hash {
				t.Errorf("newToken() returned another hash than the known one, which differs at byte %d", pwFirstDiff(hash, tt.hash))
			}
			if r.Len() != 3 {
				t.Errorf("newToken() left %d bytes of the reader, want 3: it takes 32", r.Len())
			}
			if got, ok := tokenHash(tt.token); !ok || got != tt.hash {
				t.Error("tokenHash() of the known token is not the known hash")
			}
		})
	}
}

func TestNewTokenErrors(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name string
		r    io.Reader
		is   error
	}{
		{"an empty reader", bytes.NewReader(nil), io.EOF},
		{"31 bytes", bytes.NewReader(tokBytes(31)), io.ErrUnexpectedEOF},
		{"a failing reader", iotest.ErrReader(boom), boom},
		{"a reader that fails after some bytes", io.MultiReader(bytes.NewReader(tokBytes(20)), iotest.ErrReader(boom)), boom},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token, hash, err := newToken(tt.r)
			if !errors.Is(err, tt.is) {
				t.Errorf("newToken() = %v, want an error that wraps %v", err, tt.is)
			}
			if token != "" || hash != "" {
				t.Error("newToken() returned a token or a hash with its error")
			}
			// The error shows nothing of the bytes that were read: they are the start of tokSample.
			if err != nil && strings.Contains(err.Error(), tokSample[:6]) {
				t.Error("the error contains a part of the token")
			}
		})
	}
}

func TestTokenHash(t *testing.T) {
	got, ok := tokenHash(tokSample)
	if !ok || got != tokSampleHash {
		t.Error("tokenHash() of the sample is not its known hash")
	}
	// The hash is of the text of the token, as the person types it back, and not of the bytes that it stands for.
	sum := sha256.Sum256([]byte(tokSample))
	if want := "sha256:" + hex.EncodeToString(sum[:]); got != want {
		t.Error("tokenHash() is not the SHA-256 of the text")
	}
	raw := sha256.Sum256(tokBytes(32))
	if got == "sha256:"+hex.EncodeToString(raw[:]) {
		t.Error("tokenHash() is the SHA-256 of the decoded bytes")
	}
	if !isTokenHash(got) {
		t.Error("tokenHash() is not in the form of the hash of a row")
	}

	for _, token := range []string{
		strings.Repeat("A", 43), strings.Repeat("-", 42) + "A", strings.Repeat("_", 42) + "8", "-" + tokSample[1:], "_" + tokSample[1:],
		tokSample[:20] + "-" + tokSample[21:], tokSample[:20] + "_" + tokSample[21:],
	} {
		if _, valid := tokenHash(token); !valid {
			t.Errorf("tokenHash() refused a token of 43 characters of the alphabet that has no bits left over")
		}
	}
}

func TestTokenHashRejects(t *testing.T) {
	rejected := []struct {
		name string
		s    string
	}{
		{"empty", ""},
		{"a space", " "},
		{"42 characters", tokSample[:42]},
		{"44 characters", tokSample + "A"},
		{"a megabyte of letters", strings.Repeat("A", 1<<20)},
		{"a megabyte that is the token again and again", strings.Repeat(tokSample, 1<<20/len(tokSample))},
		{"a character of 4 bytes less", tokSample[:39]},
		{"twice the token", tokSample + tokSample},
		{"a trailing padding sign", tokSample + "="},
		{"padding in place of the last character", tokSample[:42] + "="},
		{"a trailing line break", tokSample + "\n"},
		{"a trailing carriage return and line break", tokSample + "\r\n"},
		{"a leading line break", "\n" + tokSample},
		{"a leading space", " " + tokSample},
		{"a trailing space", tokSample + " "},
		{"a line break inside", tokSample[:20] + "\n" + tokSample[20:]},
		{"a carriage return inside", tokSample[:20] + "\r" + tokSample[20:]},
		{"a line break in place of a character", tokSample[:20] + "\n" + tokSample[21:]},
		{"a non-ASCII character inside", tokSample[:20] + eAcute + tokSample[20:]},
		{"a non-ASCII character in place of two", tokSample[:20] + eAcute + tokSample[22:]},
		{"a non-ASCII character at the end", tokSample[:41] + eAcute},
		{"the hash of a token", tokSampleHash},
		{"the token and the hash", tokSample + tokSampleHash},
		{"hexadecimal digits", strings.Repeat("0123456789abcdef", 3)[:43]},
		{"a key of a password hash", pwKeyText},
		{"a token of the standard alphabet with padding", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0xfb}, 32))},
	}
	for _, tt := range rejected {
		t.Run(tt.name, func(t *testing.T) {
			if got, ok := tokenHash(tt.s); ok || got != "" {
				t.Errorf("tokenHash() of a text of %d bytes was accepted", len(tt.s))
			}
		})
	}

	// A text of another length is refused before it is decoded: whatever its length, it allocates nothing.
	for _, long := range []string{strings.Repeat("A", 1<<20), strings.Repeat(tokSample, 1000), tokSample + tokSample} {
		var accepted bool
		if allocs := testing.AllocsPerRun(5, func() {
			_, ok := tokenHash(long)
			accepted = accepted || ok
		}); allocs != 0 || accepted {
			t.Errorf("tokenHash() of a text of %d bytes allocated %v times and accepted = %v, want 0 and false", len(long), allocs, accepted)
		}
	}

	// A character that is not in the alphabet, at every place of a token.
	for i := range len(tokSample) {
		for _, bad := range []string{"+", "/", "=", " ", "*", "\x00", "\n", "\r", "\t", "!", ".", ",", "$", "%"} {
			if _, ok := tokenHash(tokSample[:i] + bad + tokSample[i+1:]); ok {
				t.Errorf("tokenHash() accepted %q at place %d", bad, i)
			}
		}
	}
	// Every character of the alphabet is good at every place but the last, which has 2 bits left over: of the 64
	// characters, 16 end a token.
	for i := range len(tokAlphabet) {
		if _, ok := tokenHash(string(tokAlphabet[i]) + tokSample[1:]); !ok {
			t.Errorf("tokenHash() refused %q at the start", tokAlphabet[i])
		}
		_, ok := tokenHash(tokSample[:42] + string(tokAlphabet[i]))
		if want := i%4 == 0; ok != want {
			t.Errorf("tokenHash() with %q at the end: accepted = %v, want %v", tokAlphabet[i], ok, want)
		}
	}
}

func TestEqualHash(t *testing.T) {
	h := tokSampleHash
	other, _ := tokenHash(strings.Repeat("A", 43))
	tests := []struct {
		name string
		a, b string
		want bool
	}{
		{"the same hash", h, h, true},
		{"a copy", h, strings.Clone(h), true},
		{"both empty", "", "", true},
		{"another hash", h, other, false},
		{"the last character", h, h[:len(h)-1] + "1", false},
		{"the first character", h, "x" + h[1:], false},
		{"the prefix", h, h[:len("sha256:")], false},
		{"a longer text", h, h + "0", false},
		{"a shorter text", h[:len(h)-1], h, false},
		{"empty and a hash", "", h, false},
		{"a hash and empty", h, "", false},
		{"the token", h, tokSample, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := equalHash(tt.a, tt.b); got != tt.want {
				t.Errorf("equalHash() = %v, want %v", got, tt.want)
			}
			if got := equalHash(tt.b, tt.a); got != tt.want {
				t.Errorf("equalHash() with the arguments swapped = %v, want %v", got, tt.want)
			}
		})
	}
}

func FuzzTokenHash(f *testing.F) {
	for _, s := range []string{
		tokSample, "", tokSample[:42], tokSample + "A", tokSample + "\n", tokSample[:42] + "B", tokSample[:20] + "\n" + tokSample[20:],
		strings.Repeat("_", 42) + "8", tokSampleHash,
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		hash, ok := tokenHash(s)
		if !ok {
			if hash != "" {
				t.Fatal("tokenHash() returned a hash with false")
			}
			return
		}
		// What it accepts is exactly what newToken writes: 32 bytes in base64url, nothing else.
		b, err := base64.RawURLEncoding.DecodeString(s)
		if err != nil || len(b) != 32 || base64.RawURLEncoding.EncodeToString(b) != s || len(s) != 43 {
			t.Fatalf("tokenHash() accepted a text of %d bytes that newToken does not write", len(s))
		}
		if !isTokenHash(hash) {
			t.Fatal("tokenHash() is not in the form of the hash of a row")
		}
	})
}
