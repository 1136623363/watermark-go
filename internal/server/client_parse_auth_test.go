package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestValidateClientParseSignatureAlwaysRequiresSessionToken(t *testing.T) {
	store := installFreshMemoryClientSessionStore(t)
	t.Setenv("APP_CLIENT_SIGNATURE_REQUIRED", "false")

	for _, tc := range []struct {
		name  string
		token string
	}{
		{name: "missing"},
		{name: "invalid", token: "invalid-for-test-only"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			context, recorder := parseAuthTestContext(tc.token)
			ok := validateClientParseSignature(context, parseRequest{URL: "https://example.invalid/video", Source: 12, Timestamp: time.Now().Unix()})
			if ok {
				t.Fatal("validateClientParseSignature() accepted a missing or invalid token")
			}
			if got := responseBusinessCode(t, recorder); got != 1008 {
				t.Fatalf("business code = %d, want 1008", got)
			}
		})
	}
	if got := memorySessionCount(store); got != 0 {
		t.Fatalf("unexpected session count = %d", got)
	}
}

func TestClientParseSignatureDefaultsToTokenOnly(t *testing.T) {
	t.Setenv("APP_CLIENT_SIGNATURE_REQUIRED", "")
	if appClientSignatureRequired() {
		t.Fatal("APP_CLIENT_SIGNATURE_REQUIRED defaulted to enabled")
	}
}

func TestValidateClientParseSignatureTokenOnlyFrontendRequest(t *testing.T) {
	store := installFreshMemoryClientSessionStore(t)
	t.Setenv("APP_CLIENT_SIGNATURE_REQUIRED", "false")
	const token = "example-session-token"
	store.storeSession(sha256Hex(token), 42, "public-example", time.Now().Add(time.Hour))

	context, recorder := parseAuthTestContext(token)
	ok := validateClientParseSignature(context, parseRequest{URL: "https://example.invalid/video", Source: 12, Timestamp: time.Now().Unix()})
	if !ok {
		t.Fatalf("token-only request was rejected: %s", recorder.Body.String())
	}
	if got, exists := context.Get(clientUserIDContextKey); !exists || got != int64(42) {
		t.Fatalf("client user context = %#v, exists=%t", got, exists)
	}
}

func TestValidateClientParseSignatureOptionalAESStillReturns1009(t *testing.T) {
	store := installFreshMemoryClientSessionStore(t)
	t.Setenv("APP_CLIENT_SIGNATURE_REQUIRED", "true")
	const token = "example-session-token"
	store.storeSession(sha256Hex(token), 42, "public-example", time.Now().Add(time.Hour))

	context, recorder := parseAuthTestContext(token)
	ok := validateClientParseSignature(context, parseRequest{URL: "https://example.invalid/video", Source: 12, Timestamp: time.Now().Unix()})
	if ok {
		t.Fatal("AES-required request was accepted without a signature")
	}
	if got := responseBusinessCode(t, recorder); got != 1009 {
		t.Fatalf("business code = %d, want 1009", got)
	}
}

func TestValidateClientParseSignatureRejectsUnexpectedSourceInTokenOnlyMode(t *testing.T) {
	store := installFreshMemoryClientSessionStore(t)
	t.Setenv("APP_CLIENT_SIGNATURE_REQUIRED", "false")
	t.Setenv("APP_CLIENT_SOURCE", "12")
	const token = "example-session-token"
	store.storeSession(sha256Hex(token), 42, "public-example", time.Now().Add(time.Hour))

	context, recorder := parseAuthTestContext(token)
	ok := validateClientParseSignature(context, parseRequest{URL: "https://example.invalid/video", Source: 99, Timestamp: time.Now().Unix()})
	if ok {
		t.Fatal("token-only request accepted an unexpected client source")
	}
	if got := responseBusinessCode(t, recorder); got != 1009 {
		t.Fatalf("business code = %d, want 1009", got)
	}
}

func TestResolveClientIdentityRejectsProductionFallback(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("WECHAT_MINI_APP_ID", "")
	t.Setenv("WECHAT_MINI_APP_SECRET", "")
	_, _, _, err := resolveClientIdentity(context.Background(), clientSessionRequest{ClientID: "test-client", ProgramType: 12})
	if err == nil {
		t.Fatal("resolveClientIdentity() allowed production fallback identity")
	}
}

func TestResolveClientIdentityAllowsTestFallback(t *testing.T) {
	t.Setenv("APP_ENV", "test")
	t.Setenv("WECHAT_MINI_APP_ID", "")
	t.Setenv("WECHAT_MINI_APP_SECRET", "")
	identityType, identityKey, _, err := resolveClientIdentity(context.Background(), clientSessionRequest{ClientID: "test-client", ProgramType: 12})
	if err != nil || identityType == "" || identityKey == "" {
		t.Fatalf("resolveClientIdentity() test fallback = type %q key-present=%t error=%v", identityType, identityKey != "", err)
	}
}

func TestWechatTransportFailureNeverLeaksRequestMaterial(t *testing.T) {
	store := installFreshMemoryClientSessionStore(t)
	t.Setenv("APP_ENV", "production")
	t.Setenv("WECHAT_MINI_APP_ID", "mini-app-for-test")
	upstreamMaterial := strings.Join([]string{"wechat", "upstream", "material"}, "-")
	loginCode := strings.Join([]string{"single", "use", "login", "code"}, "-")
	t.Setenv("WECHAT_MINI_APP_SECRET", upstreamMaterial)

	var requestedURL string
	restoreHTTPDo := replaceWeChatHTTPDoForTest(func(request *http.Request) (*http.Response, error) {
		requestedURL = request.URL.String()
		return nil, &neturl.Error{Op: request.Method, URL: requestedURL, Err: errors.New("transport unavailable")}
	})
	t.Cleanup(restoreHTTPDo)

	var logs bytes.Buffer
	originalOutput := appLogger.Writer()
	originalFlags := appLogger.Flags()
	appLogger.SetOutput(&logs)
	appLogger.SetFlags(0)
	t.Cleanup(func() {
		appLogger.SetOutput(originalOutput)
		appLogger.SetFlags(originalFlags)
	})

	requestBody, err := json.Marshal(clientSessionRequest{Code: loginCode, ProgramType: 12})
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}
	response := performClientSessionRequest(t, string(requestBody))
	if response.HTTPStatus != http.StatusOK || response.Code != 1008 {
		t.Fatalf("transport failure = HTTP %d code %d, want HTTP 200 code 1008", response.HTTPStatus, response.Code)
	}
	if requestedURL == "" {
		t.Fatal("test transport did not receive the WeChat request")
	}
	for location, output := range map[string]string{
		"client response": response.Body,
		"application log": logs.String(),
	} {
		for _, forbidden := range []string{upstreamMaterial, loginCode, requestedURL} {
			if strings.Contains(output, forbidden) {
				t.Fatalf("%s exposed upstream request material", location)
			}
		}
	}
	if got := memoryIdentityCount(store); got != 0 {
		t.Fatalf("stored identities after upstream failure = %d, want 0", got)
	}
	if got := memorySessionCount(store); got != 0 {
		t.Fatalf("stored sessions after upstream failure = %d, want 0", got)
	}
}

func TestWechatIdentityMetadataNeverPersistsUpstreamSessionMaterial(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("WECHAT_MINI_APP_ID", "mini-app-for-test")
	t.Setenv("WECHAT_MINI_APP_SECRET", strings.Join([]string{"configured", "upstream", "material"}, "-"))
	sessionMaterial := strings.Join([]string{"returned", "session", "material"}, "-")
	body, err := json.Marshal(wechatCodeSession{
		OpenID:     "openid-for-test",
		UnionID:    "unionid-for-test",
		SessionKey: sessionMaterial,
	})
	if err != nil {
		t.Fatalf("encode upstream response: %v", err)
	}
	restoreHTTPDo := replaceWeChatHTTPDoForTest(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewReader(body)),
			Header:     make(http.Header),
		}, nil
	})
	t.Cleanup(restoreHTTPDo)

	identityType, identityKey, metadata, err := resolveClientIdentity(context.Background(), clientSessionRequest{Code: "valid-code", ProgramType: 12})
	if err != nil {
		t.Fatal("resolve WeChat identity")
	}
	if identityType == "" || identityKey != "openid-for-test" {
		t.Fatal("resolved WeChat identity did not retain the expected binding")
	}
	if strings.Contains(metadata, sessionMaterial) {
		t.Fatal("identity metadata persisted upstream session material")
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal([]byte(metadata), &decoded); err != nil {
		t.Fatalf("decode identity metadata: %v", err)
	}
	for _, forbiddenKey := range []string{strings.Join([]string{"session", "Key"}, ""), strings.Join([]string{"session", "key"}, "_")} {
		if _, exists := decoded[forbiddenKey]; exists {
			t.Fatal("identity metadata retained an upstream session field")
		}
	}
	if decoded["unionid"] != "unionid-for-test" || decoded["programType"] != float64(12) {
		t.Fatal("identity metadata lost required binding fields")
	}
}

func parseAuthTestContext(token string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "/api/parse", nil)
	if token != "" {
		context.Request.Header.Set("token", token)
	}
	return context, recorder
}

func responseBusinessCode(t *testing.T, recorder *httptest.ResponseRecorder) int {
	t.Helper()
	var envelope struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("HTTP status = %d, want 200", recorder.Code)
	}
	return envelope.Code
}

func replaceWeChatHTTPDoForTest(do func(*http.Request) (*http.Response, error)) func() {
	original := weChatHTTPDo
	weChatHTTPDo = do
	return func() {
		weChatHTTPDo = original
	}
}
