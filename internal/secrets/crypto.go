package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"fmt"
)

const (
	// KeySize is the master key length: AES-256.
	KeySize   = 32
	nonceSize = 12

	// keyCheckPlaintext is encrypted under the master key and stored in
	// secret_key_check. It is public by design; it only proves the key.
	keyCheckPlaintext = "dbairlock master key check v1"
)

// cipherKey is an AES-256-GCM key with its version.
type cipherKey struct {
	aead    cipher.AEAD
	version int64
}

func newCipherKey(key []byte, version int64) (cipherKey, error) {
	if len(key) != KeySize {
		return cipherKey{}, fmt.Errorf("%w: key must be %d bytes", ErrKeyFile, KeySize)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return cipherKey{}, fmt.Errorf("secrets: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return cipherKey{}, fmt.Errorf("secrets: %w", err)
	}
	return cipherKey{aead: aead, version: version}, nil
}

// seal encrypts plaintext with a fresh random 12-byte nonce.
func (k cipherKey) seal(plaintext, ad []byte) (ciphertext, nonce []byte, err error) {
	nonce = make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, fmt.Errorf("secrets: nonce: %w", err)
	}
	return k.aead.Seal(nil, nonce, plaintext, ad), nonce, nil
}

func (k cipherKey) open(ciphertext, nonce, ad []byte) ([]byte, error) {
	if len(nonce) != nonceSize {
		return nil, ErrDecrypt
	}
	out, err := k.aead.Open(nil, nonce, ciphertext, ad)
	if err != nil {
		return nil, ErrDecrypt
	}
	return out, nil
}

// secretAD is the additional data bound to a secret row: its id, kind, owner
// type and owner id, each length-prefixed so no two rows share an encoding.
// A ciphertext copied to any other row therefore fails to open.
func secretAD(id string, kind Kind, ownerType, ownerID string) []byte {
	return lengthPrefixed("dbairlock/secret/v1", id, string(kind), ownerType, ownerID)
}

func keyCheckAD() []byte { return lengthPrefixed("dbairlock/key-check/v1") }

func lengthPrefixed(parts ...string) []byte {
	var out []byte
	for _, p := range parts {
		out = binary.BigEndian.AppendUint32(out, uint32(len(p))) //nolint:gosec // parts are short identifiers
		out = append(out, p...)
	}
	return out
}
