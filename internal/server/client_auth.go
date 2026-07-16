package server

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/1136623363/watermark-go/internal/runtimecfg"
)

const (
	clientUserIDContextKey   = "client_user_id"
	clientPublicIDContextKey = "client_public_id"
)

type clientSessionRequest struct {
	Code        string `json:"code"`
	ProgramType int    `json:"programType"`
	ClientID    string `json:"clientId"`
}

type clientSessionPayload struct {
	UserID       int64     `json:"userId"`
	UID          string    `json:"uid"`
	PublicID     string    `json:"publicId"`
	Token        string    `json:"token"`
	UserType     int       `json:"userType"`
	IsFirstLogin bool      `json:"isFirstLogin"`
	ExpiresAt    time.Time `json:"expiresAt"`
}

type clientSessionRecord struct {
	UserID    int64
	PublicID  string
	ExpiresAt time.Time
}

type clientIdentityResult struct {
	UserID       int64
	PublicID     string
	IsFirstLogin bool
}

type wechatCodeSession struct {
	OpenID     string `json:"openid"`
	SessionKey string `json:"session_key"`
	UnionID    string `json:"unionid"`
	ErrCode    int    `json:"errcode"`
	ErrMsg     string `json:"errmsg"`
}

type memoryClientSessionStore struct {
	mu         sync.RWMutex
	identities map[string]clientIdentityResult
	sessions   map[string]clientSessionRecord
}

var fallbackClientSessions = &memoryClientSessionStore{
	identities: make(map[string]clientIdentityResult),
	sessions:   make(map[string]clientSessionRecord),
}

var weChatHTTPDo = func(request *http.Request) (*http.Response, error) {
	client := &http.Client{
		Transport: runtimecfg.NewHTTPTransport(),
		Timeout:   5 * time.Second,
	}
	return client.Do(request)
}

func handleClientSessionCreate(c *gin.Context) {
	var req clientSessionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, httpResponse{Code: 1004, Msg: "invalid client session payload"})
		return
	}

	req.Code = strings.TrimSpace(req.Code)
	req.ClientID = strings.TrimSpace(req.ClientID)
	if req.ProgramType <= 0 {
		req.ProgramType = appClientSource()
	}
	if req.Code == "" && req.ClientID == "" {
		c.JSON(http.StatusOK, httpResponse{Code: 1004, Msg: "missing login code"})
		return
	}
	token, err := secureRandomHex(32)
	if err != nil {
		logErrorf("client session entropy unavailable")
		c.JSON(http.StatusOK, httpResponse{Code: 1008, Msg: "client session unavailable"})
		return
	}

	identityType, identityKey, metadata, err := resolveClientIdentity(c.Request.Context(), req)
	if err != nil {
		logWarnf("client identity resolution failed")
		c.JSON(http.StatusOK, httpResponse{Code: 1008, Msg: "client session unavailable"})
		return
	}

	identity, err := ensureClientIdentity(c.Request.Context(), identityType, identityKey, metadata)
	if err != nil {
		c.JSON(http.StatusOK, httpResponse{Code: 1008, Msg: "client session unavailable"})
		return
	}

	expiresAt := time.Now().Add(appClientTokenTTL())
	if err := storeClientSession(c.Request.Context(), token, identity.UserID, identity.PublicID, req.ProgramType, expiresAt); err != nil {
		c.JSON(http.StatusOK, httpResponse{Code: 1008, Msg: "client session unavailable"})
		return
	}

	c.JSON(http.StatusOK, httpResponse{
		Code: 0,
		Msg:  "ok",
		Data: clientSessionPayload{
			UserID:       identity.UserID,
			UID:          clientVisibleUID(identity.UserID),
			PublicID:     identity.PublicID,
			Token:        token,
			UserType:     0,
			IsFirstLogin: identity.IsFirstLogin,
			ExpiresAt:    expiresAt,
		},
	})
}

func validateClientParseSignature(c *gin.Context, req parseRequest) bool {
	token := clientTokenFromRequest(c)
	if token == "" {
		c.JSON(http.StatusOK, httpResponse{Code: 1008, Msg: "client token required"})
		return false
	}

	session, err := lookupClientSession(c.Request.Context(), token)
	if err != nil {
		c.JSON(http.StatusOK, httpResponse{Code: 1008, Msg: "client token expired"})
		return false
	}
	c.Set(clientUserIDContextKey, session.UserID)
	c.Set(clientPublicIDContextKey, session.PublicID)

	if req.Source <= 0 {
		req.Source = appClientSource()
	}
	if req.Source != appClientSource() {
		c.JSON(http.StatusOK, httpResponse{Code: 1009, Msg: "client source invalid"})
		return false
	}
	if req.Timestamp <= 0 || !clientTimestampAllowed(req.Timestamp) {
		c.JSON(http.StatusOK, httpResponse{Code: 1010, Msg: "client timestamp invalid"})
		return false
	}
	if !appClientSignatureRequired() {
		return true
	}
	if strings.TrimSpace(req.Signature) == "" {
		c.JSON(http.StatusOK, httpResponse{Code: 1009, Msg: "client signature required"})
		return false
	}

	plain, err := decryptClientSignature(req.Signature)
	if err != nil {
		c.JSON(http.StatusOK, httpResponse{Code: 1009, Msg: "client signature invalid"})
		return false
	}
	expected := fmt.Sprintf("%s######%d######%s######%d", req.URL, req.Timestamp, token, req.Source)
	if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(plain)), []byte(expected)) != 1 {
		c.JSON(http.StatusOK, httpResponse{Code: 1009, Msg: "client signature invalid"})
		return false
	}

	return true
}

func attachOptionalClientSession(c *gin.Context) {
	if c == nil {
		return
	}
	if _, ok := c.Get(clientUserIDContextKey); ok {
		return
	}
	token := clientTokenFromRequest(c)
	if token == "" {
		return
	}
	session, err := lookupClientSession(c.Request.Context(), token)
	if err != nil {
		return
	}
	c.Set(clientUserIDContextKey, session.UserID)
	c.Set(clientPublicIDContextKey, session.PublicID)
}

func clientTokenFromRequest(c *gin.Context) string {
	if c == nil {
		return ""
	}
	token := strings.TrimSpace(c.GetHeader("token"))
	if token == "" {
		token = bearerToken(c.GetHeader("Authorization"))
	}
	return token
}

func resolveClientIdentity(ctx context.Context, req clientSessionRequest) (string, string, string, error) {
	if session, configured, err := exchangeWeChatCode(ctx, req.Code); configured {
		if err != nil {
			return "", "", "", err
		}
		metadata := map[string]interface{}{
			"programType": req.ProgramType,
			"unionid":     session.UnionID,
		}
		body, _ := json.Marshal(metadata)
		return fmt.Sprintf("wechat_mini:%d", req.ProgramType), session.OpenID, string(body), nil
	}
	if isProductionEnvironment() {
		return "", "", "", errors.New("wechat client identity is not configured")
	}

	stable := firstNonEmptyString(req.ClientID, req.Code)
	if stable == "" {
		return "", "", "", errors.New("missing login code")
	}
	metadata := map[string]interface{}{
		"programType": req.ProgramType,
		"mode":        "client_fallback",
	}
	body, _ := json.Marshal(metadata)
	return fmt.Sprintf("client_fallback:%d", req.ProgramType), sha256Hex(stable), string(body), nil
}

func exchangeWeChatCode(ctx context.Context, code string) (wechatCodeSession, bool, error) {
	appID := strings.TrimSpace(os.Getenv("WECHAT_MINI_APP_ID"))
	secret := strings.TrimSpace(os.Getenv("WECHAT_MINI_APP_SECRET"))
	if appID == "" || secret == "" {
		return wechatCodeSession{}, false, nil
	}
	if strings.TrimSpace(code) == "" {
		return wechatCodeSession{}, true, errors.New("missing login code")
	}

	values := neturl.Values{}
	values.Set("appid", appID)
	values.Set("secret", secret)
	values.Set("js_code", strings.TrimSpace(code))
	values.Set("grant_type", "authorization_code")
	endpoint := "https://api.weixin.qq.com/sns/jscode2session?" + values.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return wechatCodeSession{}, true, err
	}
	resp, err := weChatHTTPDo(req)
	if err != nil {
		return wechatCodeSession{}, true, errors.New("wechat login unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return wechatCodeSession{}, true, errors.New("wechat login unavailable")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return wechatCodeSession{}, true, errors.New("wechat login unavailable")
	}
	var session wechatCodeSession
	if err := json.Unmarshal(body, &session); err != nil {
		return wechatCodeSession{}, true, errors.New("wechat login unavailable")
	}
	if session.ErrCode != 0 {
		return wechatCodeSession{}, true, errors.New("wechat login rejected")
	}
	if strings.TrimSpace(session.OpenID) == "" {
		return wechatCodeSession{}, true, errors.New("wechat login missing openid")
	}
	return session, true, nil
}

func ensureClientIdentity(ctx context.Context, identityType, identityKey, metadata string) (clientIdentityResult, error) {
	if appInfra.mysql == nil {
		return fallbackClientSessions.ensureIdentity(identityType, identityKey), nil
	}

	tx, err := appInfra.mysql.BeginTx(ctx, nil)
	if err != nil {
		return clientIdentityResult{}, err
	}
	defer tx.Rollback()

	var userID int64
	err = tx.QueryRowContext(ctx, `
SELECT user_id
FROM app_user_identities
WHERE identity_type = ? AND identity_key = ?
LIMIT 1`, identityType, identityKey).Scan(&userID)
	if err != nil && !isNoRows(err) {
		return clientIdentityResult{}, err
	}

	firstLogin := false
	publicID := ""
	if isNoRows(err) {
		publicID, err = secureRandomHex(13)
		if err != nil {
			return clientIdentityResult{}, err
		}
		res, err := tx.ExecContext(ctx, `
INSERT INTO app_users (public_id, last_seen_at)
VALUES (?, NOW())`, publicID)
		if err != nil {
			return clientIdentityResult{}, err
		}
		userID, err = res.LastInsertId()
		if err != nil {
			return clientIdentityResult{}, err
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO app_user_identities (user_id, identity_type, identity_key, metadata_json)
VALUES (?, ?, ?, ?)`, userID, identityType, identityKey, nullJSONString(metadata)); err != nil {
			return clientIdentityResult{}, err
		}
		firstLogin = true
	} else {
		if err := tx.QueryRowContext(ctx, `SELECT public_id FROM app_users WHERE id = ?`, userID).Scan(&publicID); err != nil {
			return clientIdentityResult{}, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE app_users SET last_seen_at = NOW() WHERE id = ?`, userID); err != nil {
			return clientIdentityResult{}, err
		}
		if metadata != "" {
			_, _ = tx.ExecContext(ctx, `
UPDATE app_user_identities
SET metadata_json = ?
WHERE identity_type = ? AND identity_key = ?`, nullJSONString(metadata), identityType, identityKey)
		}
	}

	if err := tx.Commit(); err != nil {
		return clientIdentityResult{}, err
	}
	return clientIdentityResult{
		UserID:       userID,
		PublicID:     publicID,
		IsFirstLogin: firstLogin,
	}, nil
}

func storeClientSession(ctx context.Context, token string, userID int64, publicID string, programType int, expiresAt time.Time) error {
	tokenHash := sha256Hex(token)
	if appInfra.mysql == nil {
		fallbackClientSessions.storeSession(tokenHash, userID, publicID, expiresAt)
		return nil
	}
	_, err := appInfra.mysql.ExecContext(ctx, `
INSERT INTO app_client_sessions (user_id, program_type, token_hash, expires_at, last_seen_at)
VALUES (?, ?, ?, ?, NOW())`, userID, programType, tokenHash, expiresAt)
	return err
}

func lookupClientSession(ctx context.Context, token string) (clientSessionRecord, error) {
	tokenHash := sha256Hex(token)
	if appInfra.mysql == nil {
		return fallbackClientSessions.lookupSession(tokenHash)
	}

	var session clientSessionRecord
	err := appInfra.mysql.QueryRowContext(ctx, `
SELECT s.user_id, u.public_id, s.expires_at
FROM app_client_sessions s
JOIN app_users u ON u.id = s.user_id
WHERE s.token_hash = ?
  AND s.status = 1
  AND u.status = 1
  AND (s.expires_at IS NULL OR s.expires_at > NOW())
LIMIT 1`, tokenHash).Scan(&session.UserID, &session.PublicID, &session.ExpiresAt)
	if err != nil {
		return clientSessionRecord{}, err
	}
	_, _ = appInfra.mysql.ExecContext(ctx, `UPDATE app_client_sessions SET last_seen_at = NOW() WHERE token_hash = ?`, tokenHash)
	return session, nil
}

func (store *memoryClientSessionStore) ensureIdentity(identityType, identityKey string) clientIdentityResult {
	store.mu.Lock()
	defer store.mu.Unlock()

	key := identityType + ":" + identityKey
	if existing, ok := store.identities[key]; ok {
		existing.IsFirstLogin = false
		return existing
	}
	hash := sha256Hex(key)
	id, _ := strconv.ParseInt(hash[:12], 16, 64)
	identity := clientIdentityResult{
		UserID:       id,
		PublicID:     hash[:26],
		IsFirstLogin: true,
	}
	store.identities[key] = identity
	return identity
}

func (store *memoryClientSessionStore) storeSession(tokenHash string, userID int64, publicID string, expiresAt time.Time) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.sessions[tokenHash] = clientSessionRecord{
		UserID:    userID,
		PublicID:  firstNonEmptyString(publicID, strconv.FormatInt(userID, 10)),
		ExpiresAt: expiresAt,
	}
}

func (store *memoryClientSessionStore) lookupSession(tokenHash string) (clientSessionRecord, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	session, ok := store.sessions[tokenHash]
	if !ok || time.Now().After(session.ExpiresAt) {
		return clientSessionRecord{}, sql.ErrNoRows
	}
	return session, nil
}

func decryptClientSignature(signature string) (string, error) {
	key := []byte(appClientSignatureKey())
	switch len(key) {
	case 16, 24, 32:
	default:
		return "", errors.New("invalid AES key length")
	}
	ciphertext, err := base64.StdEncoding.DecodeString(strings.TrimSpace(signature))
	if err != nil {
		return "", err
	}
	if len(ciphertext) == 0 || len(ciphertext)%aes.BlockSize != 0 {
		return "", errors.New("invalid ciphertext size")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	plain := make([]byte, len(ciphertext))
	for start := 0; start < len(ciphertext); start += aes.BlockSize {
		block.Decrypt(plain[start:start+aes.BlockSize], ciphertext[start:start+aes.BlockSize])
	}
	plain, err = pkcs7Unpad(plain, aes.BlockSize)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func pkcs7Unpad(data []byte, blockSize int) ([]byte, error) {
	if len(data) == 0 || len(data)%blockSize != 0 {
		return nil, errors.New("invalid pkcs7 data")
	}
	padding := int(data[len(data)-1])
	if padding == 0 || padding > blockSize || padding > len(data) {
		return nil, errors.New("invalid pkcs7 padding")
	}
	if !bytes.Equal(data[len(data)-padding:], bytes.Repeat([]byte{byte(padding)}, padding)) {
		return nil, errors.New("invalid pkcs7 padding")
	}
	return data[:len(data)-padding], nil
}

func nullJSONString(value string) interface{} {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return value
}

func bearerToken(header string) string {
	header = strings.TrimSpace(header)
	if len(header) > 7 && strings.EqualFold(header[:7], "Bearer ") {
		return strings.TrimSpace(header[7:])
	}
	return ""
}

func clientTimestampAllowed(timestamp int64) bool {
	skew := int64(envInt("APP_CLIENT_TIMESTAMP_SKEW_SECONDS", 600))
	if skew <= 0 {
		skew = 600
	}
	now := time.Now().Unix()
	diff := now - timestamp
	if diff < 0 {
		diff = -diff
	}
	return diff <= skew
}

func appClientSignatureRequired() bool {
	return envBoolLocal("APP_CLIENT_SIGNATURE_REQUIRED", false)
}

func isProductionEnvironment() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("APP_ENV")), "production")
}

func appClientSignatureKey() string {
	return strings.TrimSpace(os.Getenv("APP_CLIENT_SIGNATURE_KEY"))
}

func appClientSource() int {
	source := envInt("APP_CLIENT_SOURCE", 12)
	if source <= 0 {
		return 12
	}
	return source
}

func appClientTokenTTL() time.Duration {
	seconds := envInt("APP_CLIENT_TOKEN_TTL_SECONDS", 30*24*3600)
	if seconds <= 0 {
		seconds = 30 * 24 * 3600
	}
	return time.Duration(seconds) * time.Second
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
