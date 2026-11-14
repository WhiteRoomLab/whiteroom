package logdb

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
)

const gcmNonceSize = 12

var errNotSealed = errors.New("not a sealed value")

func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("the key has %d bytes, want 32", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// sealValue seals plain with AES-256-GCM under key into a value of envelope 1: the envelope byte, keyID as a uvarint,
// a random 12-byte nonce, the length of the ciphertext with its tag as a uvarint, and those. The envelope byte and
// keyID are the additional data.
//
// It is for the PoC only: the engine writes plain values. Keys must never be stored in the log, from which they could
// not be deleted; deleting a key is what makes its values unreadable.
func sealValue(key []byte, keyID uint64, plain []byte) ([]byte, error) {
	aead, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	ad := binary.AppendUvarint([]byte{byte(envSealed)}, keyID)
	nonce := make([]byte, gcmNonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	n := len(plain) + aead.Overhead()
	v := make([]byte, 0, len(ad)+gcmNonceSize+binary.MaxVarintLen64+n)
	v = binary.AppendUvarint(append(append(v, ad...), nonce...), fromInt(n))
	return aead.Seal(v, nonce, plain, ad), nil
}

// openValue opens v, a value of envelope 1, with the key that keys returns for its key ID.
func openValue(keys func(keyID uint64) ([]byte, bool), v []byte) ([]byte, error) {
	d := &decoder{b: v, short: errNotSealed.Error()}
	if envKind(d.u8()) != envSealed {
		return nil, errNotSealed
	}
	keyID := d.uvarint()
	ad := v[:d.i]
	nonce := d.bytes(gcmNonceSize)
	sealed := d.bytes(d.uvarint())
	if d.reason != "" || d.i != len(v) {
		return nil, errNotSealed
	}
	key, ok := keys(keyID)
	if !ok {
		return nil, fmt.Errorf("key %d is gone", keyID)
	}
	aead, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	plain, err := aead.Open(nil, nonce, sealed, ad)
	if err != nil {
		return nil, fmt.Errorf("cannot open the value: %w", err)
	}
	return plain, nil
}
