package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/1136623363/watermark-go/internal/cache"
	coreparser "github.com/1136623363/watermark-go/internal/parser"
)

func TestClientEmptyWechatCodeCreatesDevelopmentIdentityAndStableUID(t *testing.T) {
	store := NewMemoryStore()
	service := newTestService(t, store, ServiceOptions{
		Environment: "test",
		Entropy:     &sequenceReader{},
	})

	first, err := service.Login(context.Background(), ClientLoginRequest{
		ClientID:    "browser-local-client",
		ProgramType: 12,
	})
	if err != nil {
		t.Fatalf("first development login failed: %v", err)
	}
	second, err := service.Login(context.Background(), ClientLoginRequest{
		ClientID:    "browser-local-client",
		ProgramType: 12,
	})
	if err != nil {
		t.Fatalf("second development login failed: %v", err)
	}

	if first.UID != "30000001" || second.UID != first.UID {
		t.Fatalf("stable visible UID = first %q second %q, want both 30000001", first.UID, second.UID)
	}
	if first.Token == "" || second.Token == "" || first.Token == second.Token {
		t.Fatalf("login tokens should be non-empty one-time plaintext values: first=%q second=%q", first.Token, second.Token)
	}
	if !first.IsFirstLogin || second.IsFirstLogin {
		t.Fatalf("first-login flags = first %t second %t", first.IsFirstLogin, second.IsFirstLogin)
	}
	if got := store.IdentityWriteCount(); got != 2 {
		t.Fatalf("identity writes = %d, want 2 ensure attempts for two logins", got)
	}
	if got := store.SessionWriteCount(); got != 2 {
		t.Fatalf("session writes = %d, want 2", got)
	}
}

func TestClientTokenExpiresAndHeaderCompatibility(t *testing.T) {
	store := NewMemoryStore()
	now := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)
	service := newTestService(t, store, ServiceOptions{
		Environment: "test",
		Entropy:     &sequenceReader{},
		Clock:       func() time.Time { return now },
		TokenTTL:    time.Hour,
	})
	login, err := service.Login(context.Background(), ClientLoginRequest{ClientID: "compat-client", ProgramType: 12})
	if err != nil {
		t.Fatalf("login failed: %v", err)
	}

	for _, tc := range []struct {
		name   string
		header http.Header
	}{
		{name: "token header", header: header("token", login.Token)},
		{name: "bearer header", header: header("Authorization", "Bearer "+login.Token)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, err := service.Authenticate(context.Background(), tc.header)
			if err != nil {
				t.Fatalf("Authenticate() error = %v", err)
			}
			if client.UserID != login.UserID || client.PublicID != login.PublicID || client.UID != login.UID {
				t.Fatalf("authenticated client = %#v, login = %#v", client, login)
			}
		})
	}

	now = now.Add(time.Hour + time.Second)
	if _, err := service.Authenticate(context.Background(), header("token", login.Token)); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expired token error = %v, want ErrInvalidToken", err)
	}
}

func TestClientEntropyFailureDoesNotStoreIdentityOrSession(t *testing.T) {
	store := NewMemoryStore()
	logger := &recordingLogger{}
	service := newTestService(t, store, ServiceOptions{
		Environment: "test",
		Entropy:     failingReader{},
		Logger:      logger,
	})

	result, err := service.Login(context.Background(), ClientLoginRequest{ClientID: "entropy-client", ProgramType: 12})
	if !errors.Is(err, ErrEntropyUnavailable) || !errors.Is(err, ErrClientSessionUnavailable) {
		t.Fatalf("entropy failure error = %v, want entropy/unavailable classification", err)
	}
	if result.Token != "" || result.UserID != 0 {
		t.Fatalf("entropy failure returned client material: %#v", result)
	}
	if got := store.IdentityWriteCount(); got != 0 {
		t.Fatalf("identity writes after entropy failure = %d, want 0", got)
	}
	if got := store.SessionWriteCount(); got != 0 {
		t.Fatalf("session writes after entropy failure = %d, want 0", got)
	}
	if !logger.hasCategory("client_entropy_unavailable") {
		t.Fatalf("logger categories = %v, want client_entropy_unavailable", logger.categories)
	}
}

func TestClientWechatUpstreamErrorsAreSanitizedAndCategorized(t *testing.T) {
	rawLoginCode := "dummy-raw-login-code"
	rawAppMaterial := "dummy-raw-application-material"
	rawURL := "https://api.weixin.qq.com/sns/jscode2session?appid=wx-test&secret=" + rawAppMaterial + "&js_code=" + rawLoginCode
	rawErr := errors.New("GET " + rawURL + " rejected with " + rawLoginCode + " and " + rawAppMaterial)
	for _, class := range []WeChatErrorClass{
		WeChatTransportError,
		WeChatStatusError,
		WeChatBodyError,
		WeChatJSONError,
		WeChatBusinessError,
	} {
		t.Run(string(class), func(t *testing.T) {
			store := NewMemoryStore()
			logger := &recordingLogger{}
			service := newTestService(t, store, ServiceOptions{
				Environment:     "production",
				Entropy:         &sequenceReader{},
				WeChat:          WeChatConfig{AppID: "wx-test", AppSecret: rawAppMaterial},
				WeChatExchanger: fakeWeChatExchanger{err: NewWeChatError(class, rawErr)},
				Logger:          logger,
			})

			_, err := service.Login(context.Background(), ClientLoginRequest{Code: rawLoginCode, ProgramType: 12})
			if !errors.Is(err, ErrClientSessionUnavailable) {
				t.Fatalf("login error = %v, want ErrClientSessionUnavailable", err)
			}
			combined := err.Error() + "\n" + strings.Join(logger.categories, "\n")
			for _, forbidden := range []string{rawLoginCode, rawAppMaterial, rawURL} {
				if strings.Contains(combined, forbidden) {
					t.Fatalf("sanitized error/log exposed %q in %q", forbidden, combined)
				}
			}
			if !logger.hasCategory("wechat_" + string(class)) {
				t.Fatalf("logger categories = %v, want wechat_%s", logger.categories, class)
			}
			if got := store.IdentityWriteCount(); got != 0 {
				t.Fatalf("identity writes after upstream failure = %d, want 0", got)
			}
			if got := store.SessionWriteCount(); got != 0 {
				t.Fatalf("session writes after upstream failure = %d, want 0", got)
			}
		})
	}
}

func TestClientWechatIdentityMetadataOnlyPersistsSafeBindingFields(t *testing.T) {
	const upstreamSessionValue = "dummy-returned-upstream-session-material"
	configuredAppMaterial := "dummy-configured-upstream-material"
	store := NewMemoryStore()
	service := newTestService(t, store, ServiceOptions{
		Environment: "production",
		Entropy:     &sequenceReader{},
		WeChat:      WeChatConfig{AppID: "wx-test", AppSecret: configuredAppMaterial},
		WeChatExchanger: fakeWeChatExchanger{session: WeChatSession{
			OpenID:     "openid-for-client-binding",
			UnionID:    "unionid-for-client-binding",
			SessionKey: upstreamSessionValue,
		}},
	})

	login, err := service.Login(context.Background(), ClientLoginRequest{Code: "valid-code", ProgramType: 12})
	if err != nil {
		t.Fatalf("wechat login failed: %v", err)
	}
	metadata, ok := store.IdentityMetadata("wechat_mini:12", "openid-for-client-binding")
	if !ok {
		t.Fatal("wechat identity metadata was not stored")
	}
	encoded := mustJSON(t, metadata)
	if login.UID != "30000001" {
		t.Fatalf("visible UID = %q, want 30000001", login.UID)
	}
	for _, forbidden := range []string{"session_key", "sessionKey", upstreamSessionValue} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("identity metadata exposed upstream session material: %s", encoded)
		}
	}
	want := map[string]any{
		"programType": float64(12),
		"openid":      "openid-for-client-binding",
		"unionid":     "unionid-for-client-binding",
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	if !reflect.DeepEqual(decoded, want) {
		t.Fatalf("metadata = %#v, want exactly %#v", decoded, want)
	}
}

func TestClientSessionSecretsNeverReachParserDependencies(t *testing.T) {
	wechatSessionMaterial := "dummy-wechat-session-key-value"
	configuredAppMaterial := "dummy-configured-wechat-application-material"
	store := NewMemoryStore()
	service := newTestService(t, store, ServiceOptions{
		Environment: "production",
		Entropy:     &sequenceReader{},
		WeChat:      WeChatConfig{AppID: "wx-test", AppSecret: configuredAppMaterial},
		WeChatExchanger: fakeWeChatExchanger{session: WeChatSession{
			OpenID:     "openid-for-client-binding",
			SessionKey: wechatSessionMaterial,
		}},
	})
	login, err := service.Login(context.Background(), ClientLoginRequest{
		Code:        "single-use-login-code",
		ProgramType: 12,
	})
	if err != nil {
		t.Fatalf("login failed: %v", err)
	}
	sentinels := []string{
		wechatSessionMaterial,
		login.Token,
		"openid-for-client-binding",
		"single-use-login-code",
		configuredAppMaterial,
	}
	authenticated, err := service.Authenticate(context.Background(), header("Authorization", "Bearer "+login.Token))
	if err != nil {
		t.Fatalf("authenticate failed: %v", err)
	}
	parserClient := authenticated.ParserContext()
	parserSessions, err := coreparser.NewSessionMaterialProvider(coreparser.SessionMaterialOptions{
		TTL:      time.Minute,
		Capacity: 1,
	})
	if err != nil {
		t.Fatalf("create parser session provider: %v", err)
	}
	parserHeader := http.Header{"X-Trace": {"parse-only"}}
	parserCacheKey, err := cache.NewKey(cache.KeyParts{
		Platform:            "douyin",
		CanonicalResourceID: "stable-video-id",
		ParserVersion:       "parser-v1",
		ResultSchemaVersion: "result-schema-v1",
	})
	if err != nil {
		t.Fatalf("create parser cache key: %v", err)
	}
	_, err = parserSessions.Get(context.Background(), coreparser.SessionMaterialKey{
		Platform: "sohu",
		Host:     "api.tv.sohu.com",
	}, func(context.Context) (coreparser.SensitiveMaterial, error) {
		return coreparser.NewSensitiveMaterial("parser-upstream-session-material"), nil
	})
	if err != nil {
		t.Fatalf("store parser upstream session material: %v", err)
	}
	boundary := struct {
		ClientContext ParserClientContext           `json:"clientContext"`
		Dependencies  coreparser.Dependencies       `json:"-"`
		FetcherHeader map[string][]string           `json:"fetcherHeader"`
		CacheKey      string                        `json:"cacheKey"`
		SessionKey    coreparser.SessionMaterialKey `json:"sessionKey"`
		SessionString string                        `json:"sessionString"`
	}{
		ClientContext: parserClient,
		Dependencies:  coreparser.Dependencies{Sessions: parserSessions},
		FetcherHeader: map[string][]string{
			"X-Trace": parserHeader.Values("X-Trace"),
		},
		CacheKey:      parserCacheKey.String(),
		SessionKey:    coreparser.SessionMaterialKey{Platform: "sohu", Host: "api.tv.sohu.com"},
		SessionString: fmt.Sprint(parserSessions),
	}
	encoded := mustJSON(t, boundary)
	for _, forbidden := range sentinels {
		if bytes.Contains(encoded, []byte(forbidden)) {
			t.Fatalf("client session secret %q crossed into parser boundary: %s", forbidden, encoded)
		}
	}
	if strings.Contains(fmt.Sprintf("%#v", boundary.Dependencies), login.Token) {
		t.Fatal("parser Dependencies included the client token")
	}
}

func newTestService(t *testing.T, store *MemoryStore, options ServiceOptions) *Service {
	t.Helper()
	options.Store = store
	if options.Clock == nil {
		now := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)
		options.Clock = func() time.Time { return now }
	}
	service, err := NewService(options)
	if err != nil {
		t.Fatalf("new auth service: %v", err)
	}
	return service
}

func header(key, value string) http.Header {
	headers := make(http.Header)
	headers.Set(key, value)
	return headers
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal JSON: %v", err)
	}
	return body
}

type sequenceReader struct{ next byte }

func (reader *sequenceReader) Read(p []byte) (int, error) {
	for index := range p {
		reader.next++
		p[index] = reader.next
	}
	return len(p), nil
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, errors.New("test entropy unavailable")
}

type fakeWeChatExchanger struct {
	session WeChatSession
	err     error
}

func (exchanger fakeWeChatExchanger) Exchange(context.Context, WeChatExchangeRequest) (WeChatSession, error) {
	if exchanger.err != nil {
		return WeChatSession{}, exchanger.err
	}
	return exchanger.session, nil
}

type recordingLogger struct {
	categories []string
}

func (logger *recordingLogger) ClientAuthEvent(event Event) {
	logger.categories = append(logger.categories, event.Category)
}

func (logger *recordingLogger) hasCategory(category string) bool {
	for _, existing := range logger.categories {
		if existing == category {
			return true
		}
	}
	return false
}
