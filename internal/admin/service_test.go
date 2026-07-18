package admin

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAdminAuthenticationFailClosedAndModeBoundCookies(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	key := []byte("dummy-admin-cookie-signing-material")
	ownerPhrase := testPassphrase("owner")
	envPhrase := testPassphrase("env")
	breakglassPhrase := testPassphrase("breakglass")
	store := &fakeUserStore{
		users: map[string]User{
			"owner": {Username: "owner", Role: RoleOwner, PasswordHash: mustHashPassword(t, ownerPhrase)},
		},
	}
	mysqlAuth := newAuthServiceForTest(t, AuthOptions{
		CookieSigningKey: key,
		UserStore:        store,
		Environment:      "production",
		EnvUsername:      "owner",
		Clock:            func() time.Time { return now },
	})
	setEnvironmentMaterial(mysqlAuth, envPhrase)

	if _, err := mysqlAuth.Login(context.Background(), loginRequest("missing", testPassphrase("missing"))); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("mysql missing user Login() error = %v, want ErrInvalidCredentials", err)
	}
	store.err = errors.New("database unavailable")
	if _, err := mysqlAuth.Login(context.Background(), loginRequest("owner", envPhrase)); !errors.Is(err, ErrAuthUnavailable) {
		t.Fatalf("mysql query error Login() error = %v, want fail-closed ErrAuthUnavailable", err)
	}
	store.err = nil

	mysqlSession, err := mysqlAuth.Login(context.Background(), loginRequest("owner", ownerPhrase))
	if err != nil {
		t.Fatalf("mysql Login() error = %v", err)
	}
	cookie, err := mysqlAuth.SessionCookie(mysqlSession, true)
	if err != nil {
		t.Fatalf("SessionCookie(mysql) error = %v", err)
	}

	envAuth := newAuthServiceForTest(t, AuthOptions{
		CookieSigningKey: key,
		Environment:      "development",
		EnvUsername:      "owner",
		Clock:            func() time.Time { return now },
	})
	setEnvironmentMaterial(envAuth, envPhrase)
	if _, ok := envAuth.ValidateSessionCookie(context.Background(), cookie.Value); ok {
		t.Fatal("mysql mode cookie was accepted by environment auth")
	}
	if _, ok := envAuth.ValidateSessionCookie(context.Background(), "owner|1700000000|signature"); ok {
		t.Fatal("legacy unsigned admin cookie was accepted")
	}
	envSession, err := envAuth.Login(context.Background(), loginRequest("owner", envPhrase))
	if err != nil {
		t.Fatalf("environment Login() error = %v", err)
	}
	if envSession.Mode != ModeEnvironment {
		t.Fatalf("environment session mode = %s, want %s", envSession.Mode, ModeEnvironment)
	}

	productionEnv := newAuthServiceForTest(t, AuthOptions{
		CookieSigningKey: key,
		Environment:      "production",
		EnvUsername:      "owner",
		Clock:            func() time.Time { return now },
	})
	setEnvironmentMaterial(productionEnv, envPhrase)
	if _, err := productionEnv.Login(context.Background(), loginRequest("owner", envPhrase)); !errors.Is(err, ErrEnvironmentAuthDisabled) {
		t.Fatalf("production environment Login() error = %v, want ErrEnvironmentAuthDisabled", err)
	}

	weakBreakglassOptions := AuthOptions{
		CookieSigningKey:   key,
		Environment:        "production",
		BreakglassEnabled:  true,
		BreakglassUsername: "owner",
		Clock:              func() time.Time { return now },
	}
	setBreakglassOptionMaterial(&weakBreakglassOptions, "short")
	if _, err := NewAuthService(weakBreakglassOptions); !errors.Is(err, ErrWeakBreakglassPassphrase) {
		t.Fatalf("NewAuthService(short breakglass) error = %v, want ErrWeakBreakglassPassphrase", err)
	}
	breakglassOptions := AuthOptions{
		CookieSigningKey:   key,
		Environment:        "production",
		BreakglassEnabled:  true,
		BreakglassUsername: "owner",
		Clock:              func() time.Time { return now },
	}
	setBreakglassOptionMaterial(&breakglassOptions, breakglassPhrase)
	breakglass := newAuthServiceForTest(t, breakglassOptions)
	breakglassSession, err := breakglass.Login(context.Background(), loginRequest("owner", breakglassPhrase))
	if err != nil {
		t.Fatalf("breakglass Login() error = %v", err)
	}
	if breakglassSession.Mode != ModeBreakglass {
		t.Fatalf("breakglass session mode = %s, want %s", breakglassSession.Mode, ModeBreakglass)
	}
	disabledBreakglass := newAuthServiceForTest(t, AuthOptions{
		CookieSigningKey:   key,
		Environment:        "production",
		BreakglassUsername: "owner",
		Clock:              func() time.Time { return now },
	})
	setBreakglassMaterial(disabledBreakglass, breakglassPhrase)
	if _, err := disabledBreakglass.Login(context.Background(), loginRequest("owner", breakglassPhrase)); !errors.Is(err, ErrBreakglassDisabled) {
		t.Fatalf("disabled breakglass Login() error = %v, want ErrBreakglassDisabled", err)
	}
}

func TestAdminWriteGuardRequiresOwnerCSRFOriginAndAudits(t *testing.T) {
	key := []byte("dummy-admin-cookie-signing-material")
	store := &fakeUserStore{}
	service := newAuthServiceForTest(t, AuthOptions{
		CookieSigningKey: key,
		UserStore:        store,
		AllowedOrigins:   []string{"https://admin.example"},
		Environment:      "production",
	})
	viewer := testSession("viewer", RoleViewer, testNonce("viewer"))
	if err := service.CheckWriteRequest(viewer, writeRequest("https://admin.example", viewer.CSRFToken)); !errors.Is(err, ErrForbidden) {
		t.Fatalf("viewer write error = %v, want ErrForbidden", err)
	}
	owner := testSession("owner", RoleOwner, testNonce("owner"))
	if err := service.CheckWriteRequest(owner, writeRequest("https://admin.example", testNonce("bad"))); !errors.Is(err, ErrCSRF) {
		t.Fatalf("bad csrf write error = %v, want ErrCSRF", err)
	}
	if err := service.CheckWriteRequest(owner, writeRequest("https://evil.example", owner.CSRFToken)); !errors.Is(err, ErrOrigin) {
		t.Fatalf("bad origin write error = %v, want ErrOrigin", err)
	}
	if err := service.CheckWriteRequest(owner, writeRequest("https://admin.example", owner.CSRFToken)); err != nil {
		t.Fatalf("owner write error = %v", err)
	}
	if err := service.RecordAudit(context.Background(), owner, "settings.update", "settings", "runtime", map[string]any{"field": "value"}); err != nil {
		t.Fatalf("RecordAudit() error = %v", err)
	}
	if len(store.audits) != 1 || store.audits[0].Action != "settings.update" || store.audits[0].Username != "owner" {
		t.Fatalf("audits = %#v", store.audits)
	}
}

func mustHashPassword(t *testing.T, passphrase string) string {
	t.Helper()
	hash, err := HashPassword(passphrase)
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	return hash
}

func loginRequest(username string, phrase string) LoginRequest {
	request := LoginRequest{Username: username}
	request.Password = phrase
	return request
}

func testSession(username string, role Role, nonce string) Session {
	session := Session{Username: username, Role: role, Mode: ModeMySQL}
	session.CSRFToken = nonce
	return session
}

func writeRequest(origin string, nonce string) WriteRequest {
	request := WriteRequest{Method: "POST", Origin: origin, Host: "admin.example"}
	request.CSRFToken = nonce
	return request
}

func setEnvironmentMaterial(service *AuthService, phrase string) {
	service.envPassword = phrase
}

func setBreakglassMaterial(service *AuthService, phrase string) {
	service.breakglassPassphrase = phrase
}

func setBreakglassOptionMaterial(options *AuthOptions, phrase string) {
	options.BreakglassPassphrase = phrase
}

func testPassphrase(label string) string {
	return "test-material-" + label + "-phrase"
}

func testNonce(label string) string {
	return "test-nonce-" + label
}

func newAuthServiceForTest(t *testing.T, options AuthOptions) *AuthService {
	t.Helper()
	service, err := NewAuthService(options)
	if err != nil {
		t.Fatalf("NewAuthService() error = %v", err)
	}
	return service
}

type fakeUserStore struct {
	users  map[string]User
	audits []AuditRecord
	err    error
}

func (store *fakeUserStore) FindUser(_ context.Context, username string) (User, bool, error) {
	if store.err != nil {
		return User{}, false, store.err
	}
	user, ok := store.users[username]
	return user, ok, nil
}

func (store *fakeUserStore) RecordAudit(_ context.Context, record AuditRecord) error {
	store.audits = append(store.audits, record)
	return nil
}
