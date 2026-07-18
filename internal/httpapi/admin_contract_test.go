package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/1136623363/watermark-go/internal/admin"
)

func TestAdminHandlersRequireLoginRBACCSRFAndAudit(t *testing.T) {
	ownerPhrase := adminHTTPPhrase("owner")
	viewerPhrase := adminHTTPPhrase("viewer")
	store := &adminHTTPUserStore{users: map[string]admin.User{
		"owner":  {Username: "owner", Role: admin.RoleOwner, PasswordHash: mustAdminHTTPHash(t, ownerPhrase)},
		"viewer": {Username: "viewer", Role: admin.RoleViewer, PasswordHash: mustAdminHTTPHash(t, viewerPhrase)},
	}}
	service, err := admin.NewService(admin.ServiceOptions{
		Auth: admin.AuthOptions{
			CookieSigningKey: []byte("dummy-admin-cookie-signing-material"),
			UserStore:        store,
			Environment:      "production",
			AllowedOrigins:   []string{"https://admin.example"},
			Clock:            func() time.Time { return time.Unix(1_700_000_000, 0) },
		},
		StartedAt: time.Unix(1_700_000_000, 0),
	})
	if err != nil {
		t.Fatalf("admin.NewService() error = %v", err)
	}
	router := newAdminRouter(service)

	unauthenticated := performAdminRequest(router, http.MethodGet, "/admin/api/summary", nil, nil)
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated summary status = %d, want 401", unauthenticated.Code)
	}

	ownerLogin := postAdminJSON(router, "/admin/api/login", loginBody(t, "owner", ownerPhrase), httpsHeaders())
	if ownerLogin.Code != http.StatusOK {
		t.Fatalf("owner login status = %d body=%s", ownerLogin.Code, ownerLogin.Body.String())
	}
	ownerCookie := ownerLogin.Result().Cookies()[0]
	if !ownerCookie.HttpOnly || !ownerCookie.Secure || ownerCookie.SameSite == http.SameSiteDefaultMode {
		t.Fatalf("owner cookie flags = HttpOnly:%t Secure:%t SameSite:%v", ownerCookie.HttpOnly, ownerCookie.Secure, ownerCookie.SameSite)
	}
	ownerCSRF := responseCSRF(t, ownerLogin.Body.String())

	viewerLogin := postAdminJSON(router, "/admin/api/login", loginBody(t, "viewer", viewerPhrase), httpsHeaders())
	viewerCookie := viewerLogin.Result().Cookies()[0]
	viewerCSRF := responseCSRF(t, viewerLogin.Body.String())
	viewerWrite := postAdminJSON(router, "/admin/api/settings", `{"rateLimitEnabled":true}`, headersWithCookie(viewerCookie, viewerCSRF, "https://admin.example"))
	if viewerWrite.Code != http.StatusForbidden {
		t.Fatalf("viewer write status = %d, want 403 body=%s", viewerWrite.Code, viewerWrite.Body.String())
	}

	missingCSRF := postAdminJSON(router, "/admin/api/settings", `{"rateLimitEnabled":true}`, headersWithCookie(ownerCookie, "", "https://admin.example"))
	if missingCSRF.Code != http.StatusForbidden {
		t.Fatalf("missing csrf status = %d, want 403 body=%s", missingCSRF.Code, missingCSRF.Body.String())
	}
	badOrigin := postAdminJSON(router, "/admin/api/settings", `{"rateLimitEnabled":true}`, headersWithCookie(ownerCookie, ownerCSRF, "https://evil.example"))
	if badOrigin.Code != http.StatusForbidden {
		t.Fatalf("bad origin status = %d, want 403 body=%s", badOrigin.Code, badOrigin.Body.String())
	}
	ownerWrite := postAdminJSON(router, "/admin/api/settings", `{"rateLimitEnabled":true}`, headersWithCookie(ownerCookie, ownerCSRF, "https://admin.example"))
	if ownerWrite.Code != http.StatusOK {
		t.Fatalf("owner write status = %d body=%s", ownerWrite.Code, ownerWrite.Body.String())
	}
	if len(store.audits) == 0 || store.audits[len(store.audits)-1].Action != "settings.update" {
		t.Fatalf("audits = %#v", store.audits)
	}

	profile := performAdminRequest(router, http.MethodGet, "/api/profile", nil, nil)
	assertJSONEq(t, `{"code":1002,"msg":"unsupported"}`, profile.Body.String())
}

func newAdminRouter(service *admin.Service) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handlers := AdminHandlers{Service: service}
	handlers.Register(router)
	return router
}

func postAdminJSON(router *gin.Engine, path string, body string, headers http.Header) *httptest.ResponseRecorder {
	return performAdminRequest(router, http.MethodPost, path, []byte(body), headers)
}

func performAdminRequest(router *gin.Engine, method string, path string, body []byte, headers http.Header) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	for key, values := range headers {
		for _, value := range values {
			if strings.EqualFold(key, "Host") {
				request.Host = value
				continue
			}
			request.Header.Add(key, value)
		}
	}
	router.ServeHTTP(recorder, request)
	return recorder
}

type adminHTTPUserStore struct {
	users  map[string]admin.User
	audits []admin.AuditRecord
}

func (store *adminHTTPUserStore) FindUser(_ context.Context, username string) (admin.User, bool, error) {
	user, ok := store.users[username]
	return user, ok, nil
}

func (store *adminHTTPUserStore) RecordAudit(_ context.Context, record admin.AuditRecord) error {
	store.audits = append(store.audits, record)
	return nil
}

func mustAdminHTTPHash(t *testing.T, passphrase string) string {
	t.Helper()
	hash, err := admin.HashPassword(passphrase)
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	return hash
}

func loginBody(t *testing.T, username string, phrase string) string {
	t.Helper()
	body, err := json.Marshal(map[string]string{"username": username, "password": phrase})
	if err != nil {
		t.Fatalf("marshal login body: %v", err)
	}
	return string(body)
}

func adminHTTPPhrase(label string) string {
	return fmt.Sprintf("test-material-%s-phrase", label)
}

func httpsHeaders() http.Header {
	headers := make(http.Header)
	headers.Set("X-Forwarded-Proto", "https")
	headers.Set("Host", "admin.example")
	return headers
}

func headersWithCookie(cookie *http.Cookie, csrf string, origin string) http.Header {
	headers := httpsHeaders()
	headers.Set("Cookie", cookie.String())
	headers.Set("Origin", origin)
	if strings.TrimSpace(csrf) != "" {
		headers.Set("X-CSRF-Token", csrf)
	}
	return headers
}

func responseCSRF(t *testing.T, body string) string {
	t.Helper()
	var envelope struct {
		Data struct {
			CSRFToken string `json:"csrfToken"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatalf("decode login response: %v body=%s", err, body)
	}
	if envelope.Data.CSRFToken == "" {
		t.Fatalf("login response omitted csrf token: %s", body)
	}
	return envelope.Data.CSRFToken
}
