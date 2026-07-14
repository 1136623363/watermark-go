package server

import (
	"crypto/aes"
	"encoding/base64"
	"testing"
)

func TestDecryptClientSignatureMatchesCryptoJS(t *testing.T) {
	const key = "example-test-key"
	const expected = "timestamp=1700000000&nonce=example"
	t.Setenv("APP_CLIENT_SIGNATURE_KEY", key)

	encrypted := encryptECBForTest(t, []byte(key), []byte(expected))
	plain, err := decryptClientSignature(encrypted)
	if err != nil {
		t.Fatalf("decryptClientSignature() error = %v", err)
	}
	if plain != expected {
		t.Fatalf("decryptClientSignature() = %q, want %q", plain, expected)
	}
}

func TestDecryptClientSignatureRejectsMissingKey(t *testing.T) {
	t.Setenv("APP_CLIENT_SIGNATURE_KEY", "")
	if got := appClientSignatureKey(); got != "" {
		t.Fatal("appClientSignatureKey() returned an embedded default")
	}
	if _, err := decryptClientSignature("not-used"); err == nil {
		t.Fatal("decryptClientSignature() accepted a missing key")
	}
}

func TestAppClientSignatureKeyReadsExplicitTestPlaceholder(t *testing.T) {
	const placeholder = "invalid-for-test-only"
	t.Setenv("APP_CLIENT_SIGNATURE_KEY", placeholder)
	if got := appClientSignatureKey(); got != placeholder {
		t.Fatalf("appClientSignatureKey() = %q, want the explicit test placeholder", got)
	}
}

func encryptECBForTest(t *testing.T, key, plain []byte) string {
	t.Helper()
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("new test cipher: %v", err)
	}
	padding := aes.BlockSize - len(plain)%aes.BlockSize
	padded := append(append([]byte(nil), plain...), make([]byte, padding)...)
	for index := len(plain); index < len(padded); index++ {
		padded[index] = byte(padding)
	}
	ciphertext := make([]byte, len(padded))
	for start := 0; start < len(padded); start += aes.BlockSize {
		block.Encrypt(ciphertext[start:start+aes.BlockSize], padded[start:start+aes.BlockSize])
	}
	return base64.StdEncoding.EncodeToString(ciphertext)
}
