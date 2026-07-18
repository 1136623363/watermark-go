package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
)

const tokenEntropyBytes = 32

type TokenHash string

func GenerateToken(reader io.Reader) (string, TokenHash, error) {
	if reader == nil {
		reader = rand.Reader
	}
	raw := make([]byte, tokenEntropyBytes)
	if _, err := io.ReadFull(reader, raw); err != nil {
		return "", "", ErrEntropyUnavailable
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	return token, HashToken(token), nil
}

func HashToken(token string) TokenHash {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return TokenHash(hex.EncodeToString(sum[:]))
}

func ExtractToken(header http.Header) string {
	token := strings.TrimSpace(header.Get("token"))
	if token != "" {
		return token
	}
	authorization := strings.TrimSpace(header.Get("Authorization"))
	if len(authorization) > len("Bearer ") && strings.EqualFold(authorization[:len("Bearer ")], "Bearer ") {
		return strings.TrimSpace(authorization[len("Bearer "):])
	}
	return ""
}

func validTokenHash(hash TokenHash) bool {
	value := string(hash)
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
