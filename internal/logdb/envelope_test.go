package logdb

import (
	"bytes"
	"crypto/rand"
	"errors"
	"slices"
	"testing"
)

func TestSealedValue(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	keys := map[uint64][]byte{7: key}
	lookup := func(id uint64) ([]byte, bool) {
		k, ok := keys[id]
		return k, ok
	}
	plain := []byte("name: Hanako Yamada, mail: hanako@example.com")
	v, err := sealValue(key, 7, plain)
	if err != nil {
		t.Fatal(err)
	}
	if len(v) != 1+1+12+1+len(plain)+16 || envKind(v[0]) != envSealed || v[1] != 7 || int(v[14]) != len(plain)+16 {
		t.Fatalf("layout of %d bytes: %x", len(v), v)
	}
	if bytes.Contains(v, []byte("Hanako")) || bytes.Contains(v, []byte("example")) {
		t.Fatal("the plain text is in the sealed value")
	}
	if got, err := openValue(lookup, v); err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("open: %q, %v", got, err)
	}
	again, err := sealValue(key, 7, plain)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(again[2:14], v[2:14]) || bytes.Equal(again, v) {
		t.Error("two seals have the same nonce")
	}
	empty, err := sealValue(key, 1<<40, nil)
	if err != nil {
		t.Fatal(err)
	}
	keys[1<<40] = key
	if got, err := openValue(lookup, empty); err != nil || len(got) != 0 {
		t.Errorf("empty value: %q, %v", got, err)
	}

	eachChange(v, func(i int, c []byte) {
		if got, err := openValue(lookup, c); err == nil {
			t.Fatalf("byte %d changed to %#x: opened %q", i, c[i], got)
		}
	})
	for n := range len(v) {
		if _, err := openValue(lookup, v[:n]); !errors.Is(err, errNotSealed) {
			t.Fatalf("cut at %d: %v", n, err)
		}
	}
	if _, err := openValue(lookup, append(slices.Clone(v), 0)); !errors.Is(err, errNotSealed) {
		t.Errorf("a byte after it: %v", err)
	}

	// The key ID is authenticated: under another ID, even with the same key, the value does not open.
	keys[8] = key
	c := slices.Clone(v)
	c[1] = 8
	if _, err := openValue(lookup, c); err == nil || err.Error() != "cannot open the value: cipher: message authentication failed" {
		t.Errorf("another key ID: %v", err)
	}
	other := make([]byte, 32)
	if _, err := openValue(func(uint64) ([]byte, bool) { return other, true }, v); err == nil ||
		err.Error() != "cannot open the value: cipher: message authentication failed" {
		t.Errorf("another key: %v", err)
	}

	// Once its key is deleted, the value stays unreadable.
	delete(keys, 7)
	if _, err := openValue(lookup, v); err == nil || err.Error() != "key 7 is gone" {
		t.Errorf("deleted key: %v", err)
	}

	if _, err := sealValue(key[:16], 1, plain); err == nil || err.Error() != "the key has 16 bytes, want 32" {
		t.Errorf("AES-128 key: %v", err)
	}
	if _, err := openValue(func(uint64) ([]byte, bool) { return key[:24], true }, v); err == nil ||
		err.Error() != "the key has 24 bytes, want 32" {
		t.Errorf("AES-192 key: %v", err)
	}
	if _, err := openValue(lookup, append([]byte{byte(envPlain)}, v[1:]...)); !errors.Is(err, errNotSealed) {
		t.Errorf("plain envelope: %v", err)
	}
}
