package crypto

import (
	"bytes"
	"encoding/hex"
	"testing"
)

func testCipher(t *testing.T) *Cipher {
	t.Helper()
	c, err := NewCipher("a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90")
	if err != nil {
		t.Fatalf("NewCipher: %v", err)
	}
	return c
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	c := testCipher(t)
	box, err := c.Encrypt([]byte("s3cret-database-password"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	got, err := c.Decrypt(box)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if !bytes.Equal(got, []byte("s3cret-database-password")) {
		t.Errorf("round trip mismatch: %q", got)
	}
}

func TestEncryptNoncesUnique(t *testing.T) {
	c := testCipher(t)
	a, _ := c.Encrypt([]byte("same"))
	b, _ := c.Encrypt([]byte("same"))
	if bytes.Equal(a.Nonce, b.Nonce) {
		t.Error("nonces repeated across encryptions")
	}
}

func TestDecryptTamperedCiphertext(t *testing.T) {
	c := testCipher(t)
	box, _ := c.Encrypt([]byte("secret"))
	box.Ciphertext[0] ^= 0xff
	if _, err := c.Decrypt(box); err == nil {
		t.Error("tampered ciphertext accepted")
	}
}

func TestNewCipherKeyValidation(t *testing.T) {
	if _, err := NewCipher("tooshort"); err == nil {
		t.Error("short key accepted")
	}
	if _, err := NewCipher("zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"); err == nil {
		t.Error("non-hex key accepted")
	}
	if _, err := NewCipher(hex.EncodeToString(make([]byte, 16))); err == nil {
		t.Error("128-bit key accepted for AES-256")
	}
}
