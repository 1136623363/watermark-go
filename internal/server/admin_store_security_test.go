package server

import (
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestAuthenticateAdminConfiguredMySQLFailsClosed(t *testing.T) {
	installConfiguredMySQLAuthStub(t, func(string, string) (authenticatedAdmin, bool, error) {
		return authenticatedAdmin{}, false, errors.New("test database failure")
	})
	t.Setenv("ADMIN_USERNAME", "owner")
	t.Setenv("ADMIN_PASSWORD", strings.Repeat("A7", 8))
	t.Setenv("ADMIN_ENV_FALLBACK_ENABLED", "false")

	_, ok, err := authenticateAdmin("owner", strings.Repeat("A7", 8))
	if err != nil {
		t.Fatalf("authenticateAdmin() surfaced database details: %v", err)
	}
	if ok {
		t.Fatal("authenticateAdmin() fell back after a configured MySQL error")
	}
}

func TestAuthenticateAdminConfiguredMySQLNotFoundFailsClosed(t *testing.T) {
	installConfiguredMySQLAuthStub(t, func(string, string) (authenticatedAdmin, bool, error) {
		return authenticatedAdmin{}, false, nil
	})
	t.Setenv("ADMIN_USERNAME", "owner")
	t.Setenv("ADMIN_PASSWORD", strings.Repeat("A7", 8))
	t.Setenv("ADMIN_ENV_FALLBACK_ENABLED", "false")

	_, ok, err := authenticateAdmin("owner", strings.Repeat("A7", 8))
	if err != nil || ok {
		t.Fatalf("authenticateAdmin() = ok %t error %v, want fail closed", ok, err)
	}
}

func TestAuthenticateAdminBreakGlassRequiresExplicitFlagAndStrongPassword(t *testing.T) {
	installConfiguredMySQLAuthStub(t, func(string, string) (authenticatedAdmin, bool, error) {
		return authenticatedAdmin{}, false, nil
	})
	t.Setenv("ADMIN_USERNAME", "owner")
	t.Setenv("ADMIN_ENV_FALLBACK_ENABLED", "true")

	t.Setenv("ADMIN_PASSWORD", "change-me")
	if _, ok, _ := authenticateAdmin("owner", "change-me"); ok {
		t.Fatal("authenticateAdmin() accepted a weak break-glass password")
	}

	strongPassword := strings.Repeat("A7", 8)
	t.Setenv("ADMIN_PASSWORD", strongPassword)
	admin, ok, err := authenticateAdmin("owner", strongPassword)
	if err != nil || !ok || !admin.BreakGlass {
		t.Fatalf("authenticateAdmin() break-glass = ok %t marked %t error %v", ok, admin.BreakGlass, err)
	}
}

func TestAuthenticateAdminWithoutMySQLUsesDevelopmentEnvironmentCredential(t *testing.T) {
	originalMySQL := appInfra.mysql
	appInfra.mysql = nil
	t.Cleanup(func() { appInfra.mysql = originalMySQL })
	t.Setenv("ADMIN_USERNAME", "owner")
	password := strings.Repeat("A7", 8)
	t.Setenv("ADMIN_PASSWORD", password)
	t.Setenv("ADMIN_ENV_FALLBACK_ENABLED", "false")

	admin, ok, err := authenticateAdmin("owner", password)
	if err != nil || !ok || admin.BreakGlass {
		t.Fatalf("authenticateAdmin() development credential = ok %t break-glass %t error %v", ok, admin.BreakGlass, err)
	}
}

func TestBreakGlassSessionIsRevokedWhenEnvironmentFallbackIsDisabled(t *testing.T) {
	installConfiguredMySQLUsernameStub(t, func(string) (bool, error) { return true, nil })
	t.Setenv("ADMIN_USERNAME", "owner")
	t.Setenv("ADMIN_PASSWORD", strings.Repeat("A7", 8))
	t.Setenv("ADMIN_ENV_FALLBACK_ENABLED", "true")

	context := signedAdminSessionContext(t, "owner", adminSessionModeBreakGlass)
	if !validAdminSession(context) {
		t.Fatal("explicitly enabled break-glass session was not valid")
	}
	t.Setenv("ADMIN_ENV_FALLBACK_ENABLED", "false")
	if validAdminSession(context) {
		t.Fatal("break-glass session remained valid through a same-name MySQL user after fallback was disabled")
	}
}

func TestMySQLSessionIsIndependentOfEnvironmentFallbackSwitch(t *testing.T) {
	installConfiguredMySQLUsernameStub(t, func(string) (bool, error) { return true, nil })
	t.Setenv("ADMIN_USERNAME", "owner")
	t.Setenv("ADMIN_PASSWORD", strings.Repeat("A7", 8))
	t.Setenv("ADMIN_ENV_FALLBACK_ENABLED", "false")

	context := signedAdminSessionContext(t, "owner", adminSessionModeMySQL)
	if !validAdminSession(context) {
		t.Fatal("active MySQL session was invalidated by the environment fallback switch")
	}
}

func TestMySQLSessionCannotEscalateToBreakGlassAfterDatabaseUserIsDisabled(t *testing.T) {
	installConfiguredMySQLUsernameStub(t, func(string) (bool, error) { return false, nil })
	t.Setenv("ADMIN_USERNAME", "owner")
	t.Setenv("ADMIN_PASSWORD", strings.Repeat("A7", 8))
	t.Setenv("ADMIN_ENV_FALLBACK_ENABLED", "true")

	context := signedAdminSessionContext(t, "owner", adminSessionModeMySQL)
	if validAdminSession(context) {
		t.Fatal("disabled MySQL user session escalated through a same-name environment break-glass account")
	}
}

func installConfiguredMySQLAuthStub(t *testing.T, stub func(string, string) (authenticatedAdmin, bool, error)) {
	t.Helper()
	originalMySQL := appInfra.mysql
	originalQuery := authenticateAdminMySQLQuery
	appInfra.mysql = &sql.DB{}
	authenticateAdminMySQLQuery = stub
	t.Cleanup(func() {
		appInfra.mysql = originalMySQL
		authenticateAdminMySQLQuery = originalQuery
	})
}

func installConfiguredMySQLUsernameStub(t *testing.T, stub func(string) (bool, error)) {
	t.Helper()
	originalMySQL := appInfra.mysql
	originalQuery := adminUsernameValidMySQLQuery
	appInfra.mysql = &sql.DB{}
	adminUsernameValidMySQLQuery = stub
	t.Cleanup(func() {
		appInfra.mysql = originalMySQL
		adminUsernameValidMySQLQuery = originalQuery
	})
}

func signedAdminSessionContext(t *testing.T, username string, mode adminSessionMode) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	writerContext, _ := gin.CreateTestContext(recorder)
	writerContext.Request = httptest.NewRequest(http.MethodGet, "/admin", nil)
	setAdminSessionCookie(writerContext, username, mode)
	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("session cookies = %d, want 1", len(cookies))
	}
	readerContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	readerContext.Request = httptest.NewRequest(http.MethodGet, "/admin", nil)
	readerContext.Request.AddCookie(cookies[0])
	return readerContext
}
