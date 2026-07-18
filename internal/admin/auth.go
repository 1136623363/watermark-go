package admin

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const SessionCookieName = "watermark_admin_session"

type Role string

const (
	RoleViewer Role = "viewer"
	RoleOwner  Role = "owner"
)

type AuthMode string

const (
	ModeMySQL       AuthMode = "mysql"
	ModeEnvironment AuthMode = "environment"
	ModeBreakglass  AuthMode = "breakglass"
)

var (
	ErrInvalidCredentials       = errors.New("invalid admin credentials")
	ErrAuthUnavailable          = errors.New("admin auth unavailable")
	ErrEnvironmentAuthDisabled  = errors.New("environment admin auth disabled")
	ErrBreakglassDisabled       = errors.New("breakglass admin auth disabled")
	ErrWeakBreakglassPassphrase = errors.New("breakglass passphrase is too weak")
	ErrCookieSigningKeyRequired = errors.New("admin cookie signing key is required")
	ErrInvalidSessionCookie     = errors.New("invalid admin session cookie")
	ErrForbidden                = errors.New("admin permission denied")
	ErrCSRF                     = errors.New("admin csrf check failed")
	ErrOrigin                   = errors.New("admin origin check failed")
)

type User struct {
	Username     string
	Role         Role
	PasswordHash string
}

type AuditRecord struct {
	Username   string
	Action     string
	Resource   string
	ResourceID string
	Details    map[string]any
	CreatedAt  time.Time
}

type UserStore interface {
	FindUser(context.Context, string) (User, bool, error)
	RecordAudit(context.Context, AuditRecord) error
}

type AuthOptions struct {
	CookieSigningKey     []byte
	UserStore            UserStore
	Environment          string
	EnvUsername          string
	EnvPassword          string
	BreakglassEnabled    bool
	BreakglassUsername   string
	BreakglassPassphrase string
	AllowedOrigins       []string
	Clock                func() time.Time
	SessionTTL           time.Duration
	Entropy              io.Reader
}

type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type Session struct {
	Username  string   `json:"username"`
	Role      Role     `json:"role"`
	Mode      AuthMode `json:"mode"`
	ExpiresAt time.Time
	CSRFToken string `json:"csrfToken"`
}

type WriteRequest struct {
	Method    string
	Origin    string
	Host      string
	CSRFToken string
}

type AuthService struct {
	key                  []byte
	store                UserStore
	environment          string
	envUsername          string
	envPassword          string
	breakglassEnabled    bool
	breakglassUsername   string
	breakglassPassphrase string
	allowedOrigins       map[string]bool
	clock                func() time.Time
	sessionTTL           time.Duration
	entropy              io.Reader
}

type sessionPayload struct {
	Username  string   `json:"u"`
	Role      Role     `json:"r"`
	Mode      AuthMode `json:"m"`
	ExpiresAt int64    `json:"e"`
	CSRFToken string   `json:"c"`
}

func NewAuthService(options AuthOptions) (*AuthService, error) {
	if len(options.CookieSigningKey) == 0 {
		return nil, ErrCookieSigningKeyRequired
	}
	if options.BreakglassEnabled && len([]rune(options.BreakglassPassphrase)) < 16 {
		return nil, ErrWeakBreakglassPassphrase
	}
	clock := options.Clock
	if clock == nil {
		clock = time.Now
	}
	ttl := options.SessionTTL
	if ttl <= 0 {
		ttl = 12 * time.Hour
	}
	entropy := options.Entropy
	if entropy == nil {
		entropy = rand.Reader
	}
	allowed := make(map[string]bool, len(options.AllowedOrigins))
	for _, origin := range options.AllowedOrigins {
		origin = strings.TrimRight(strings.TrimSpace(origin), "/")
		if origin != "" {
			allowed[origin] = true
		}
	}
	return &AuthService{
		key:                  append([]byte(nil), options.CookieSigningKey...),
		store:                options.UserStore,
		environment:          strings.ToLower(strings.TrimSpace(options.Environment)),
		envUsername:          strings.TrimSpace(options.EnvUsername),
		envPassword:          options.EnvPassword,
		breakglassEnabled:    options.BreakglassEnabled,
		breakglassUsername:   strings.TrimSpace(options.BreakglassUsername),
		breakglassPassphrase: options.BreakglassPassphrase,
		allowedOrigins:       allowed,
		clock:                clock,
		sessionTTL:           ttl,
		entropy:              entropy,
	}, nil
}

func HashPassword(passphrase string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(passphrase), bcrypt.MinCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

func (service *AuthService) Login(ctx context.Context, request LoginRequest) (Session, error) {
	username := strings.TrimSpace(request.Username)
	if username == "" {
		return Session{}, ErrInvalidCredentials
	}
	if service.store != nil {
		user, ok, err := service.store.FindUser(ctx, username)
		if err != nil {
			return Session{}, ErrAuthUnavailable
		}
		if !ok {
			return Session{}, ErrInvalidCredentials
		}
		if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(request.Password)) != nil {
			return Session{}, ErrInvalidCredentials
		}
		return service.newSession(user.Username, normalizeRole(user.Role), ModeMySQL)
	}
	if service.environmentAuthAllowed() && username == service.envUsername && hmac.Equal([]byte(request.Password), []byte(service.envPassword)) {
		return service.newSession(username, RoleOwner, ModeEnvironment)
	}
	if service.envUsername != "" && username == service.envUsername && !service.environmentAuthAllowed() {
		return Session{}, ErrEnvironmentAuthDisabled
	}
	if service.breakglassUsername != "" && username == service.breakglassUsername && hmac.Equal([]byte(request.Password), []byte(service.breakglassPassphrase)) {
		if !service.breakglassEnabled {
			return Session{}, ErrBreakglassDisabled
		}
		return service.newSession(username, RoleOwner, ModeBreakglass)
	}
	return Session{}, ErrInvalidCredentials
}

func (service *AuthService) SessionCookie(session Session, secure bool) (*http.Cookie, error) {
	if session.ExpiresAt.IsZero() {
		session.ExpiresAt = service.clock().Add(service.sessionTTL)
	}
	if session.CSRFToken == "" {
		token, err := service.randomToken()
		if err != nil {
			return nil, err
		}
		session.CSRFToken = token
	}
	payload := sessionPayload{
		Username:  session.Username,
		Role:      normalizeRole(session.Role),
		Mode:      session.Mode,
		ExpiresAt: session.ExpiresAt.Unix(),
		CSRFToken: session.CSRFToken,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	payloadSegment := base64.RawURLEncoding.EncodeToString(body)
	signature := service.sign(payloadSegment)
	return &http.Cookie{
		Name:     SessionCookieName,
		Value:    payloadSegment + "." + signature,
		Path:     "/",
		MaxAge:   int(time.Until(session.ExpiresAt).Seconds()),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	}, nil
}

func (service *AuthService) ValidateSessionCookie(ctx context.Context, raw string) (Session, bool) {
	raw = strings.TrimSpace(raw)
	parts := strings.Split(raw, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return Session{}, false
	}
	if !hmac.Equal([]byte(parts[1]), []byte(service.sign(parts[0]))) {
		return Session{}, false
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return Session{}, false
	}
	var payload sessionPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return Session{}, false
	}
	session := Session{
		Username:  strings.TrimSpace(payload.Username),
		Role:      normalizeRole(payload.Role),
		Mode:      payload.Mode,
		ExpiresAt: time.Unix(payload.ExpiresAt, 0),
		CSRFToken: payload.CSRFToken,
	}
	if session.Username == "" || session.CSRFToken == "" || !session.ExpiresAt.After(service.clock()) {
		return Session{}, false
	}
	switch session.Mode {
	case ModeMySQL:
		if service.store == nil {
			return Session{}, false
		}
		user, ok, err := service.store.FindUser(ctx, session.Username)
		if err != nil || !ok {
			return Session{}, false
		}
		session.Role = normalizeRole(user.Role)
		return session, true
	case ModeEnvironment:
		return session, service.store == nil && service.environmentAuthAllowed() && session.Username == service.envUsername
	case ModeBreakglass:
		return session, service.breakglassEnabled && session.Username == service.breakglassUsername
	default:
		return Session{}, false
	}
}

func (service *AuthService) CheckWriteRequest(session Session, request WriteRequest) error {
	if normalizeRole(session.Role) != RoleOwner {
		return ErrForbidden
	}
	if strings.TrimSpace(request.CSRFToken) == "" || !hmac.Equal([]byte(request.CSRFToken), []byte(session.CSRFToken)) {
		return ErrCSRF
	}
	if !service.originAllowed(request.Origin, request.Host) {
		return ErrOrigin
	}
	return nil
}

func (service *AuthService) RecordAudit(ctx context.Context, session Session, action string, resource string, resourceID string, details map[string]any) error {
	if service.store == nil {
		return nil
	}
	return service.store.RecordAudit(ctx, AuditRecord{
		Username:   session.Username,
		Action:     strings.TrimSpace(action),
		Resource:   strings.TrimSpace(resource),
		ResourceID: strings.TrimSpace(resourceID),
		Details:    details,
		CreatedAt:  service.clock(),
	})
}

func (service *AuthService) newSession(username string, role Role, mode AuthMode) (Session, error) {
	csrf, err := service.randomToken()
	if err != nil {
		return Session{}, err
	}
	return Session{
		Username:  username,
		Role:      normalizeRole(role),
		Mode:      mode,
		ExpiresAt: service.clock().Add(service.sessionTTL),
		CSRFToken: csrf,
	}, nil
}

func (service *AuthService) environmentAuthAllowed() bool {
	return service.store == nil && (service.environment == "development" || service.environment == "test")
}

func (service *AuthService) originAllowed(origin string, host string) bool {
	origin = strings.TrimRight(strings.TrimSpace(origin), "/")
	if origin == "" {
		return false
	}
	if len(service.allowedOrigins) > 0 {
		return service.allowedOrigins[origin]
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" {
		return false
	}
	return strings.EqualFold(parsed.Host, strings.TrimSpace(host))
}

func (service *AuthService) randomToken() (string, error) {
	raw := make([]byte, 16)
	if _, err := io.ReadFull(service.entropy, raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func (service *AuthService) sign(payloadSegment string) string {
	mac := hmac.New(sha256.New, service.key)
	_, _ = mac.Write([]byte(payloadSegment))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func normalizeRole(role Role) Role {
	if role == RoleOwner {
		return RoleOwner
	}
	return RoleViewer
}
