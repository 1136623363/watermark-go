package server

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestClientSessionTokenHas256Bits(t *testing.T) {
	store := installFreshMemoryClientSessionStore(t)
	t.Setenv("APP_ENV", "test")
	t.Setenv("WECHAT_MINI_APP_ID", "")
	t.Setenv("WECHAT_MINI_APP_SECRET", "")

	response := performClientSessionRequest(t, `{"clientId":"test-client","programType":12}`)
	if response.Code != 0 {
		t.Fatalf("session response code = %d, want 0", response.Code)
	}
	var payload clientSessionPayload
	if err := json.Unmarshal(response.Data, &payload); err != nil {
		t.Fatalf("decode session payload: %v", err)
	}
	if len(payload.Token) != 64 {
		t.Fatalf("token length = %d, want 64 hex characters", len(payload.Token))
	}
	if _, err := hex.DecodeString(payload.Token); err != nil {
		t.Fatalf("token is not hexadecimal: %v", err)
	}
	if got := memorySessionCount(store); got != 1 {
		t.Fatalf("stored sessions = %d, want 1", got)
	}
}

func TestClientSessionEntropyFailureDoesNotStoreSession(t *testing.T) {
	store := installFreshMemoryClientSessionStore(t)
	t.Setenv("APP_ENV", "test")
	t.Setenv("WECHAT_MINI_APP_ID", "")
	t.Setenv("WECHAT_MINI_APP_SECRET", "")
	restoreReader := replaceSecureRandomReaderForTest(failingRandomReader{})
	t.Cleanup(restoreReader)

	response := performClientSessionRequest(t, `{"clientId":"entropy-failure","programType":12}`)
	if response.HTTPStatus != http.StatusOK || response.Code != 1008 {
		t.Fatalf("session entropy failure = HTTP %d code %d, want HTTP 200 code 1008", response.HTTPStatus, response.Code)
	}
	if got := memorySessionCount(store); got != 0 {
		t.Fatalf("stored sessions after entropy failure = %d, want 0", got)
	}
	if got := memoryIdentityCount(store); got != 0 {
		t.Fatalf("stored identities after entropy failure = %d, want 0", got)
	}
}

type clientSessionTestResponse struct {
	HTTPStatus int
	Code       int
	Data       json.RawMessage
	Body       string
}

func performClientSessionRequest(t *testing.T, body string) clientSessionTestResponse {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/api/client/session", bytes.NewBufferString(body))
	context.Request.Header.Set("Content-Type", "application/json")
	handleClientSessionCreate(context)

	var envelope struct {
		Code int             `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode session response: %v", err)
	}
	return clientSessionTestResponse{HTTPStatus: recorder.Code, Code: envelope.Code, Data: envelope.Data, Body: recorder.Body.String()}
}

func installFreshMemoryClientSessionStore(t *testing.T) *memoryClientSessionStore {
	t.Helper()
	originalStore := fallbackClientSessions
	originalMySQL := appInfra.mysql
	store := &memoryClientSessionStore{
		identities: make(map[string]clientIdentityResult),
		sessions:   make(map[string]clientSessionRecord),
	}
	fallbackClientSessions = store
	appInfra.mysql = nil
	t.Cleanup(func() {
		fallbackClientSessions = originalStore
		appInfra.mysql = originalMySQL
	})
	return store
}

func memorySessionCount(store *memoryClientSessionStore) int {
	store.mu.RLock()
	defer store.mu.RUnlock()
	return len(store.sessions)
}

func memoryIdentityCount(store *memoryClientSessionStore) int {
	store.mu.RLock()
	defer store.mu.RUnlock()
	return len(store.identities)
}
