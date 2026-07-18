package download

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type Purpose string

const (
	PurposePoll     Purpose = "poll"
	PurposeDownload Purpose = "download"
	PurposeM3U8File Purpose = "m3u8_file"
)

var (
	ErrSigningKeyRequired    = errors.New("download signing key is required")
	ErrTicketTaskRequired    = errors.New("download ticket task id is required")
	ErrTicketPurposeRequired = errors.New("download ticket purpose is required")
	ErrInvalidTicket         = errors.New("invalid download ticket")
	ErrExpiredTicket         = errors.New("expired download ticket")
)

type TicketClaims struct {
	TaskID    string
	Purpose   Purpose
	ExpiresAt time.Time
}

type ticketPayload struct {
	TaskID  string  `json:"tid"`
	Purpose Purpose `json:"pur"`
	Expiry  int64   `json:"exp"`
}

func SignTicket(signingKey []byte, claims TicketClaims, now time.Time) (string, error) {
	if len(signingKey) == 0 {
		return "", ErrSigningKeyRequired
	}
	claims.TaskID = strings.TrimSpace(claims.TaskID)
	if claims.TaskID == "" {
		return "", ErrTicketTaskRequired
	}
	if strings.TrimSpace(string(claims.Purpose)) == "" {
		return "", ErrTicketPurposeRequired
	}
	if claims.ExpiresAt.IsZero() || !claims.ExpiresAt.After(now) {
		return "", ErrExpiredTicket
	}
	payload := ticketPayload{
		TaskID:  claims.TaskID,
		Purpose: claims.Purpose,
		Expiry:  claims.ExpiresAt.UnixNano(),
	}
	encodedPayload, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	payloadSegment := base64.RawURLEncoding.EncodeToString(encodedPayload)
	mac := hmac.New(sha256.New, signingKey)
	_, _ = mac.Write([]byte(payloadSegment))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return payloadSegment + "." + signature, nil
}

func VerifyTicket(signingKey []byte, raw string, purpose Purpose, now time.Time) (TicketClaims, error) {
	if len(signingKey) == 0 {
		return TicketClaims{}, ErrSigningKeyRequired
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return TicketClaims{}, ErrInvalidTicket
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return TicketClaims{}, ErrInvalidTicket
	}
	mac := hmac.New(sha256.New, signingKey)
	_, _ = mac.Write([]byte(parts[0]))
	wantSignature := mac.Sum(nil)
	gotSignature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(gotSignature, wantSignature) {
		return TicketClaims{}, ErrInvalidTicket
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return TicketClaims{}, ErrInvalidTicket
	}
	var payload ticketPayload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return TicketClaims{}, ErrInvalidTicket
	}
	claims := TicketClaims{
		TaskID:    strings.TrimSpace(payload.TaskID),
		Purpose:   payload.Purpose,
		ExpiresAt: time.Unix(0, payload.Expiry),
	}
	if claims.TaskID == "" {
		return TicketClaims{}, ErrTicketTaskRequired
	}
	if strings.TrimSpace(string(claims.Purpose)) == "" {
		return TicketClaims{}, ErrTicketPurposeRequired
	}
	if claims.Purpose != purpose {
		return TicketClaims{}, ErrInvalidTicket
	}
	if claims.ExpiresAt.IsZero() || !claims.ExpiresAt.After(now) {
		return TicketClaims{}, ErrExpiredTicket
	}
	return claims, nil
}
