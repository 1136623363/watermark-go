package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"strings"
	"sync"
	"time"

	"github.com/1136623363/watermark-go/internal/netguard"
)

const (
	defaultProgramType = 12
	defaultTokenTTL    = 30 * 24 * time.Hour
)

var (
	ErrClientSessionUnavailable = errors.New("client session unavailable")
	ErrInvalidToken             = errors.New("client token invalid")
	ErrEntropyUnavailable       = errors.New("client entropy unavailable")
)

type ErrorKind string

const (
	ErrorKindEntropy  ErrorKind = "entropy"
	ErrorKindIdentity ErrorKind = "identity"
	ErrorKindSession  ErrorKind = "session"
	ErrorKindWechat   ErrorKind = "wechat"
	ErrorKindAuth     ErrorKind = "client_auth"
)

type ClientError struct {
	Kind ErrorKind
}

func (err *ClientError) Error() string {
	if err == nil || err.Kind == "" {
		return ErrClientSessionUnavailable.Error()
	}
	return ErrClientSessionUnavailable.Error() + ": " + string(err.Kind)
}

func (err *ClientError) Is(target error) bool {
	if target == ErrClientSessionUnavailable {
		return true
	}
	if target == ErrEntropyUnavailable && err != nil && err.Kind == ErrorKindEntropy {
		return true
	}
	if target == ErrInvalidToken && err != nil && err.Kind == ErrorKindAuth {
		return true
	}
	return false
}

type Event struct {
	Category string
}

type Logger interface {
	ClientAuthEvent(Event)
}

type ServiceOptions struct {
	Environment     string
	Store           Store
	Entropy         io.Reader
	Clock           func() time.Time
	TokenTTL        time.Duration
	DefaultProgram  int
	WeChat          WeChatConfig
	WeChatExchanger WeChatExchanger
	Logger          Logger
}

type Service struct {
	environment string
	store       Store
	entropy     io.Reader
	clock       func() time.Time
	tokenTTL    time.Duration
	programType int
	wechat      WeChatConfig
	exchanger   WeChatExchanger
	logger      Logger
}

type ClientLoginRequest struct {
	Code        string `json:"code"`
	ProgramType int    `json:"programType"`
	ClientID    string `json:"clientId"`
}

type LoginResult struct {
	UserID       int64     `json:"userId"`
	UID          string    `json:"uid"`
	PublicID     string    `json:"publicId"`
	Token        string    `json:"token"`
	UserType     int       `json:"userType"`
	IsFirstLogin bool      `json:"isFirstLogin"`
	ExpiresAt    time.Time `json:"expiresAt"`
}

type AuthenticatedClient struct {
	UserID      int64     `json:"userId"`
	UID         string    `json:"uid"`
	PublicID    string    `json:"publicId"`
	ProgramType int       `json:"programType"`
	ExpiresAt   time.Time `json:"expiresAt"`
}

type ParserClientContext struct {
	UserID      int64  `json:"userId"`
	UID         string `json:"uid"`
	PublicID    string `json:"publicId"`
	ProgramType int    `json:"programType"`
}

func (client AuthenticatedClient) ParserContext() ParserClientContext {
	return ParserClientContext{
		UserID:      client.UserID,
		UID:         client.UID,
		PublicID:    client.PublicID,
		ProgramType: client.ProgramType,
	}
}

func NewService(options ServiceOptions) (*Service, error) {
	store := options.Store
	if store == nil {
		store = NewMemoryStore()
	}
	clock := options.Clock
	if clock == nil {
		clock = time.Now
	}
	ttl := options.TokenTTL
	if ttl <= 0 {
		ttl = defaultTokenTTL
	}
	program := options.DefaultProgram
	if program <= 0 {
		program = defaultProgramType
	}
	exchanger := options.WeChatExchanger
	if exchanger == nil && options.WeChat.Configured() {
		exchanger = NetguardWeChatExchanger{}
	}
	return &Service{
		environment: strings.ToLower(strings.TrimSpace(options.Environment)),
		store:       store,
		entropy:     options.Entropy,
		clock:       clock,
		tokenTTL:    ttl,
		programType: program,
		wechat:      options.WeChat,
		exchanger:   exchanger,
		logger:      options.Logger,
	}, nil
}

func (service *Service) Login(ctx context.Context, request ClientLoginRequest) (LoginResult, error) {
	if service == nil || service.store == nil {
		return LoginResult{}, clientFailure(ErrorKindSession)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	request.Code = strings.TrimSpace(request.Code)
	request.ClientID = strings.TrimSpace(request.ClientID)
	if request.ProgramType <= 0 {
		request.ProgramType = service.programType
	}

	token, hash, err := GenerateToken(service.entropy)
	if err != nil {
		service.log("client_entropy_unavailable")
		return LoginResult{}, clientFailure(ErrorKindEntropy)
	}

	identity, err := service.resolveIdentity(ctx, request)
	if err != nil {
		service.log(identityFailureCategory(err))
		return LoginResult{}, clientFailure(ErrorKindWechat)
	}
	result, err := service.store.EnsureIdentity(ctx, identity)
	if err != nil {
		service.log("client_identity_store_unavailable")
		return LoginResult{}, clientFailure(ErrorKindIdentity)
	}
	expiresAt := service.clock().Add(service.tokenTTL)
	if err := service.store.StoreSession(ctx, SessionRecord{
		TokenHash:   hash,
		UserID:      result.UserID,
		PublicID:    result.PublicID,
		ProgramType: request.ProgramType,
		ExpiresAt:   expiresAt,
	}); err != nil {
		service.log("client_session_store_unavailable")
		return LoginResult{}, clientFailure(ErrorKindSession)
	}
	return LoginResult{
		UserID:       result.UserID,
		UID:          VisibleUID(result.UserID),
		PublicID:     result.PublicID,
		Token:        token,
		UserType:     0,
		IsFirstLogin: result.IsFirstLogin,
		ExpiresAt:    expiresAt,
	}, nil
}

func (service *Service) Authenticate(ctx context.Context, header http.Header) (AuthenticatedClient, error) {
	if service == nil || service.store == nil {
		return AuthenticatedClient{}, &ClientError{Kind: ErrorKindAuth}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	token := ExtractToken(header)
	if token == "" {
		return AuthenticatedClient{}, &ClientError{Kind: ErrorKindAuth}
	}
	hash := HashToken(token)
	if !validTokenHash(hash) {
		return AuthenticatedClient{}, &ClientError{Kind: ErrorKindAuth}
	}
	session, err := service.store.LookupSession(ctx, hash, service.clock())
	if err != nil {
		return AuthenticatedClient{}, &ClientError{Kind: ErrorKindAuth}
	}
	return AuthenticatedClient{
		UserID:      session.UserID,
		UID:         VisibleUID(session.UserID),
		PublicID:    session.PublicID,
		ProgramType: session.ProgramType,
		ExpiresAt:   session.ExpiresAt,
	}, nil
}

func (service *Service) resolveIdentity(ctx context.Context, request ClientLoginRequest) (Identity, error) {
	if service.wechat.Configured() {
		if strings.TrimSpace(request.Code) == "" {
			return Identity{}, NewWeChatError(WeChatBusinessError, errors.New("missing login code"))
		}
		if service.exchanger == nil {
			return Identity{}, NewWeChatError(WeChatTransportError, errors.New("wechat exchanger unavailable"))
		}
		session, err := service.exchanger.Exchange(ctx, WeChatExchangeRequest{
			Code:        request.Code,
			ProgramType: request.ProgramType,
			AppID:       service.wechat.AppID,
			AppSecret:   service.wechat.AppSecret,
		})
		if err != nil {
			return Identity{}, err
		}
		if strings.TrimSpace(session.OpenID) == "" {
			return Identity{}, NewWeChatError(WeChatBusinessError, errors.New("missing openid"))
		}
		metadata := map[string]any{
			"programType": request.ProgramType,
			"openid":      strings.TrimSpace(session.OpenID),
		}
		if strings.TrimSpace(session.UnionID) != "" {
			metadata["unionid"] = strings.TrimSpace(session.UnionID)
		}
		return Identity{
			Type:     fmt.Sprintf("wechat_mini:%d", request.ProgramType),
			Key:      strings.TrimSpace(session.OpenID),
			Metadata: metadata,
		}, nil
	}
	if service.production() {
		return Identity{}, errors.New("wechat identity unavailable")
	}
	stable := request.ClientID
	if stable == "" {
		stable = request.Code
	}
	if stable == "" {
		return Identity{}, errors.New("missing development client identity")
	}
	return Identity{
		Type: fmt.Sprintf("client_fallback:%d", request.ProgramType),
		Key:  stableIdentityKey(stable),
		Metadata: map[string]any{
			"programType": request.ProgramType,
			"mode":        "client_fallback",
		},
	}, nil
}

func (service *Service) production() bool {
	return service.environment == "production" || service.environment == "prod"
}

func (service *Service) log(category string) {
	if service == nil || service.logger == nil {
		return
	}
	category = strings.TrimSpace(category)
	if category == "" {
		category = "client_auth_unavailable"
	}
	service.logger.ClientAuthEvent(Event{Category: category})
}

func identityFailureCategory(err error) string {
	if class := WeChatErrorClassOf(err); class != "" {
		return "wechat_" + string(class)
	}
	return "client_identity_unavailable"
}

func clientFailure(kind ErrorKind) error {
	return &ClientError{Kind: kind}
}

func VisibleUID(userID int64) string {
	return fmt.Sprintf("%d", 30000000+userID)
}

type Store interface {
	EnsureIdentity(context.Context, Identity) (IdentityResult, error)
	StoreSession(context.Context, SessionRecord) error
	LookupSession(context.Context, TokenHash, time.Time) (SessionRecord, error)
}

type Identity struct {
	Type     string
	Key      string
	Metadata map[string]any
}

type IdentityResult struct {
	UserID       int64
	PublicID     string
	IsFirstLogin bool
}

type SessionRecord struct {
	TokenHash   TokenHash
	UserID      int64
	PublicID    string
	ProgramType int
	ExpiresAt   time.Time
}

type memoryIdentity struct {
	result   IdentityResult
	metadata map[string]any
}

type MemoryStore struct {
	mu             sync.RWMutex
	nextUserID     int64
	identities     map[string]memoryIdentity
	sessions       map[TokenHash]SessionRecord
	identityWrites int
	sessionWrites  int
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		identities: make(map[string]memoryIdentity),
		sessions:   make(map[TokenHash]SessionRecord),
	}
}

func (store *MemoryStore) EnsureIdentity(_ context.Context, identity Identity) (IdentityResult, error) {
	if store == nil {
		return IdentityResult{}, errors.New("nil memory store")
	}
	identity.Type = strings.TrimSpace(identity.Type)
	identity.Key = strings.TrimSpace(identity.Key)
	if identity.Type == "" || identity.Key == "" {
		return IdentityResult{}, errors.New("identity binding is incomplete")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.identityWrites++
	key := identity.Type + "\x00" + identity.Key
	if existing, ok := store.identities[key]; ok {
		existing.result.IsFirstLogin = false
		existing.metadata = cloneMetadata(identity.Metadata)
		store.identities[key] = existing
		return existing.result, nil
	}
	store.nextUserID++
	publicID := stablePublicID(identity.Type, identity.Key)
	result := IdentityResult{
		UserID:       store.nextUserID,
		PublicID:     publicID,
		IsFirstLogin: true,
	}
	store.identities[key] = memoryIdentity{result: result, metadata: cloneMetadata(identity.Metadata)}
	return result, nil
}

func (store *MemoryStore) StoreSession(_ context.Context, session SessionRecord) error {
	if store == nil {
		return errors.New("nil memory store")
	}
	if !validTokenHash(session.TokenHash) || session.UserID <= 0 || session.ExpiresAt.IsZero() {
		return errors.New("session record is incomplete")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.sessionWrites++
	if session.PublicID == "" {
		session.PublicID = stablePublicID("user", fmt.Sprint(session.UserID))
	}
	store.sessions[session.TokenHash] = session
	return nil
}

func (store *MemoryStore) LookupSession(_ context.Context, hash TokenHash, now time.Time) (SessionRecord, error) {
	if store == nil {
		return SessionRecord{}, ErrInvalidToken
	}
	if now.IsZero() {
		now = time.Now()
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	session, ok := store.sessions[hash]
	if !ok || !now.Before(session.ExpiresAt) {
		return SessionRecord{}, ErrInvalidToken
	}
	return session, nil
}

func (store *MemoryStore) IdentityWriteCount() int {
	store.mu.RLock()
	defer store.mu.RUnlock()
	return store.identityWrites
}

func (store *MemoryStore) SessionWriteCount() int {
	store.mu.RLock()
	defer store.mu.RUnlock()
	return store.sessionWrites
}

func (store *MemoryStore) IdentityMetadata(identityType, identityKey string) (map[string]any, bool) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	identity, ok := store.identities[strings.TrimSpace(identityType)+"\x00"+strings.TrimSpace(identityKey)]
	if !ok {
		return nil, false
	}
	return cloneMetadata(identity.metadata), true
}

func cloneMetadata(metadata map[string]any) map[string]any {
	if len(metadata) == 0 {
		return map[string]any{}
	}
	cloned := make(map[string]any, len(metadata))
	for key, value := range metadata {
		cloned[key] = value
	}
	return cloned
}

func stablePublicID(identityType, identityKey string) string {
	sum := sha256.Sum256([]byte(identityType + "\x00" + identityKey))
	return hex.EncodeToString(sum[:])[:26]
}

func stableIdentityKey(value string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(value)))
	return hex.EncodeToString(sum[:])
}

type WeChatConfig struct {
	AppID     string
	AppSecret string
}

func (config WeChatConfig) Configured() bool {
	return strings.TrimSpace(config.AppID) != "" && strings.TrimSpace(config.AppSecret) != ""
}

type WeChatExchangeRequest struct {
	Code        string
	ProgramType int
	AppID       string
	AppSecret   string
}

type WeChatSession struct {
	OpenID     string
	UnionID    string
	SessionKey string
}

type WeChatExchanger interface {
	Exchange(context.Context, WeChatExchangeRequest) (WeChatSession, error)
}

type WeChatErrorClass string

const (
	WeChatTransportError WeChatErrorClass = "transport"
	WeChatStatusError    WeChatErrorClass = "status"
	WeChatBodyError      WeChatErrorClass = "body"
	WeChatJSONError      WeChatErrorClass = "json"
	WeChatBusinessError  WeChatErrorClass = "business"
)

type WeChatError struct {
	Class WeChatErrorClass
}

func NewWeChatError(class WeChatErrorClass, _ error) *WeChatError {
	if class == "" {
		class = WeChatTransportError
	}
	return &WeChatError{Class: class}
}

func (err *WeChatError) Error() string {
	if err == nil || err.Class == "" {
		return "wechat login unavailable"
	}
	return "wechat login unavailable: " + string(err.Class)
}

func (err WeChatError) Format(state fmt.State, _ rune) {
	_, _ = state.Write([]byte(err.Error()))
}

func WeChatErrorClassOf(err error) WeChatErrorClass {
	var typed *WeChatError
	if errors.As(err, &typed) && typed != nil {
		return typed.Class
	}
	return ""
}

type NetguardWeChatExchanger struct {
	Fetcher      *netguard.Fetcher
	Timeout      time.Duration
	MaxBodyBytes int64
}

type wechatWireSession struct {
	OpenID     string `json:"openid"`
	SessionKey string `json:"session_key"`
	UnionID    string `json:"unionid"`
	ErrCode    int    `json:"errcode"`
	ErrMsg     string `json:"errmsg"`
}

func (exchanger NetguardWeChatExchanger) Exchange(ctx context.Context, request WeChatExchangeRequest) (WeChatSession, error) {
	values := neturl.Values{}
	values.Set("appid", strings.TrimSpace(request.AppID))
	values.Set("secret", strings.TrimSpace(request.AppSecret))
	values.Set("js_code", strings.TrimSpace(request.Code))
	values.Set("grant_type", "authorization_code")
	target, err := netguard.NewFetchURL("https://api.weixin.qq.com/sns/jscode2session?" + values.Encode())
	if err != nil {
		return WeChatSession{}, NewWeChatError(WeChatTransportError, err)
	}
	registry, err := netguard.NewAuthorityRegistry([]netguard.AuthorityOwner{{
		Owner: "wechat-mini",
		Rules: []netguard.AuthorityRule{{
			Purpose: netguard.PurposeSessionBootstrap,
			Host:    "api.weixin.qq.com",
		}},
	}})
	if err != nil {
		return WeChatSession{}, NewWeChatError(WeChatTransportError, err)
	}
	decision, err := registry.Authorize(netguard.AuthorityRequest{
		Owner:   "wechat-mini",
		Purpose: netguard.PurposeSessionBootstrap,
		URL:     target,
		Header:  make(http.Header),
	})
	if err != nil {
		return WeChatSession{}, NewWeChatError(WeChatTransportError, err)
	}
	fetcher := exchanger.Fetcher
	if fetcher == nil {
		fetcher, err = netguard.NewDefaultFetcher()
		if err != nil {
			return WeChatSession{}, NewWeChatError(WeChatTransportError, err)
		}
	}
	timeout := exchanger.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	bodyLimit := exchanger.MaxBodyBytes
	if bodyLimit <= 0 {
		bodyLimit = 64 << 10
	}
	if ctx == nil {
		ctx = context.Background()
	}
	exchangeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	response, err := fetcher.Fetch(exchangeCtx, netguard.FetchRequest{
		Method:       http.MethodGet,
		URL:          target,
		Header:       decision.SanitizedHeader(),
		MaxRedirects: 0,
	})
	if err != nil {
		return WeChatSession{}, NewWeChatError(WeChatTransportError, err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return WeChatSession{}, NewWeChatError(WeChatStatusError, nil)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, bodyLimit+1))
	if err != nil {
		return WeChatSession{}, NewWeChatError(WeChatBodyError, err)
	}
	if int64(len(body)) > bodyLimit {
		return WeChatSession{}, NewWeChatError(WeChatBodyError, nil)
	}
	var wire wechatWireSession
	if err := json.Unmarshal(body, &wire); err != nil {
		return WeChatSession{}, NewWeChatError(WeChatJSONError, err)
	}
	if wire.ErrCode != 0 {
		return WeChatSession{}, NewWeChatError(WeChatBusinessError, nil)
	}
	if strings.TrimSpace(wire.OpenID) == "" {
		return WeChatSession{}, NewWeChatError(WeChatBusinessError, nil)
	}
	return WeChatSession{
		OpenID:     strings.TrimSpace(wire.OpenID),
		UnionID:    strings.TrimSpace(wire.UnionID),
		SessionKey: strings.TrimSpace(wire.SessionKey),
	}, nil
}
