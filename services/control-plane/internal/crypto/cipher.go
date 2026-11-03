// Package crypto provides authenticated encryption for data source
// credentials. Plaintext secrets never touch the database: callers store
// only ciphertext + nonce, and decrypt exclusively in connector workers.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
)

// ErrInvalidKey indicates the APP_ENCRYPTION_KEY is not 32 hex-encoded bytes.
var ErrInvalidKey = errors.New("crypto: encryption key must be 64 hex characters (32 bytes)")

// Box holds one encrypted secret.
type Box struct {
	Ciphertext []byte // includes the GCM tag
	Nonce      []byte
}

// Cipher encrypts and decrypts with AES-256-GCM.
type Cipher struct {
	aead cipher.AEAD
}

// NewCipher builds a Cipher from a hex-encoded 256-bit key.
func NewCipher(hexKey string) (*Cipher, error) {
	key, err := hex.DecodeString(hexKey)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidKey, err)
	}
	if len(key) != 32 {
		return nil, ErrInvalidKey
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("crypto: init block: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("crypto: init gcm: %w", err)
	}
	return &Cipher{aead: aead}, nil
}

// Encrypt seals plaintext with a fresh random nonce.
func (c *Cipher) Encrypt(plaintext []byte) (Box, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return Box{}, fmt.Errorf("crypto: read nonce: %w", err)
	}
	ct := c.aead.Seal(nil, nonce, plaintext, nil)
	return Box{Ciphertext: ct, Nonce: nonce}, nil
}

// Decrypt opens a Box produced by Encrypt. Tampering yields an error.
func (c *Cipher) Decrypt(box Box) ([]byte, error) {
	plaintext, err := c.aead.Open(nil, box.Nonce, box.Ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("crypto: decrypt: %w", err)
	}
	return plaintext, nil
}
