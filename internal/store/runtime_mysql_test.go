package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/1136623363/watermark-go/internal/admin"
	"github.com/1136623363/watermark-go/internal/auth"
	parseusecase "github.com/1136623363/watermark-go/internal/parse"
	"github.com/1136623363/watermark-go/internal/task"
)

func TestMySQLRuntimeStorePersistsParseResultAndShareCache(t *testing.T) {
	runtimeStore, mock, cleanup := newMockRuntimeStore(t)
	defer cleanup()

	data := parseusecase.CompatData{
		Platform: "m3u8",
		Type:     "video",
		Title:    "runtime persistence",
		ShareID:  "share_1234567890123456789012",
		PlayAddr: "https://media.example/video.mp4",
	}
	stored := parseusecase.StoredResult{
		ShareID: data.ShareID,
		Cache: parseusecase.CacheIdentity{
			Platform:      "m3u8",
			CanonicalURL:  "https://test-streams.mux.dev/x36xhzz/x36xhzz.m3u8",
			ParserVersion: "parser-v1",
		},
		Result: parseusecase.Result{
			Platform: "m3u8",
			Type:     "video",
			Title:    data.Title,
			VideoURL: data.PlayAddr,
		},
		Data:      data,
		CreatedAt: time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC),
	}
	mock.ExpectExec("INSERT INTO parse_results").WillReturnResult(sqlmock.NewResult(1, 1))
	if err := runtimeStore.SaveResult(context.Background(), stored); err != nil {
		t.Fatalf("SaveResult() error = %v", err)
	}

	body, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery("SELECT result_json FROM parse_results").
		WithArgs(data.ShareID).
		WillReturnRows(sqlmock.NewRows([]string{"result_json"}).AddRow(body))
	got, ok, err := runtimeStore.GetCached(context.Background(), data.ShareID)
	if err != nil || !ok {
		t.Fatalf("GetCached() ok/error = %t/%v", ok, err)
	}
	if got.ShareID != data.ShareID || got.PlayAddr != data.PlayAddr {
		t.Fatalf("cached data = %#v, want persisted compat data", got)
	}
}

func TestMySQLRuntimeStorePersistsClientIdentityAndSession(t *testing.T) {
	runtimeStore, mock, cleanup := newMockRuntimeStore(t)
	defer cleanup()

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT u.id, u.public_id").
		WithArgs("wechat_mini:12", "openid-runtime").
		WillReturnError(sqlmock.ErrCancelled)
	mock.ExpectRollback()
	if _, err := runtimeStore.EnsureIdentity(context.Background(), auth.Identity{
		Type: "wechat_mini:12",
		Key:  "openid-runtime",
	}); err == nil {
		t.Fatal("EnsureIdentity() swallowed query errors")
	}

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT u.id, u.public_id").
		WithArgs("wechat_mini:12", "openid-runtime").
		WillReturnError(sqlmock.ErrCancelled)
	mock.ExpectRollback()
	if _, err := runtimeStore.EnsureIdentity(context.Background(), auth.Identity{
		Type: "wechat_mini:12",
		Key:  "openid-runtime",
	}); err == nil {
		t.Fatal("EnsureIdentity() swallowed query errors")
	}
}

func TestMySQLRuntimeStoreCreatesIdentityStoresSessionAndLooksItUp(t *testing.T) {
	runtimeStore, mock, cleanup := newMockRuntimeStore(t)
	defer cleanup()

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT u.id, u.public_id").
		WithArgs("wechat_mini:12", "openid-runtime").
		WillReturnRows(sqlmock.NewRows([]string{"id", "public_id"}))
	mock.ExpectExec("INSERT INTO app_users").WillReturnResult(sqlmock.NewResult(42, 1))
	mock.ExpectExec("INSERT INTO app_user_identities").WillReturnResult(sqlmock.NewResult(7, 1))
	mock.ExpectCommit()
	identity, err := runtimeStore.EnsureIdentity(context.Background(), auth.Identity{
		Type: "wechat_mini:12",
		Key:  "openid-runtime",
		Metadata: map[string]any{
			"programType": 12,
			"openid":      "openid-runtime",
		},
	})
	if err != nil {
		t.Fatalf("EnsureIdentity() error = %v", err)
	}
	if identity.UserID != 42 || identity.PublicID == "" || !identity.IsFirstLogin {
		t.Fatalf("identity result = %#v", identity)
	}

	expiresAt := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	hash := auth.TokenHash(strings.Repeat("a", 64))
	mock.ExpectExec("INSERT INTO app_client_sessions").WillReturnResult(sqlmock.NewResult(1, 1))
	if err := runtimeStore.StoreSession(context.Background(), auth.SessionRecord{
		TokenHash:   hash,
		UserID:      identity.UserID,
		PublicID:    identity.PublicID,
		ProgramType: 12,
		ExpiresAt:   expiresAt,
	}); err != nil {
		t.Fatalf("StoreSession() error = %v", err)
	}

	mock.ExpectQuery("SELECT s.user_id, u.public_id, s.program_type, s.expires_at").
		WithArgs(string(hash), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"user_id", "public_id", "program_type", "expires_at"}).
			AddRow(identity.UserID, identity.PublicID, 12, expiresAt))
	session, err := runtimeStore.LookupSession(context.Background(), hash, expiresAt.Add(-time.Second))
	if err != nil {
		t.Fatalf("LookupSession() error = %v", err)
	}
	if session.UserID != identity.UserID || session.PublicID != identity.PublicID || session.ProgramType != 12 {
		t.Fatalf("session = %#v", session)
	}
}

func TestMySQLRuntimeStoreSeedsAdminAndRecordsAudit(t *testing.T) {
	runtimeStore, mock, cleanup := newMockRuntimeStore(t)
	defer cleanup()
	adminPhrase := "runtime-admin-" + strings.Repeat("p", 16)

	mock.ExpectExec("INSERT INTO admin_users").WillReturnResult(sqlmock.NewResult(1, 1))
	if err := runtimeStore.SeedAdminUser(context.Background(), "admin", adminPhrase); err != nil {
		t.Fatalf("SeedAdminUser() error = %v", err)
	}

	hash, err := admin.HashPassword(adminPhrase)
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery("SELECT username, role, password_hash FROM admin_users").
		WithArgs("admin").
		WillReturnRows(sqlmock.NewRows([]string{"username", "role", "password_hash"}).
			AddRow("admin", string(admin.RoleOwner), hash))
	user, ok, err := runtimeStore.FindUser(context.Background(), "admin")
	if err != nil || !ok {
		t.Fatalf("FindUser() ok/error = %t/%v", ok, err)
	}
	if user.Username != "admin" || user.Role != admin.RoleOwner || user.PasswordHash != hash {
		t.Fatalf("user = %#v", user)
	}

	mock.ExpectExec("INSERT INTO admin_audit_logs").WillReturnResult(sqlmock.NewResult(1, 1))
	if err := runtimeStore.RecordAudit(context.Background(), admin.AuditRecord{
		Username:  "admin",
		Action:    "settings.update",
		Resource:  "settings",
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("RecordAudit() error = %v", err)
	}
}

func TestMySQLRuntimeStoreCreatesClaimsAndCompletesParseTask(t *testing.T) {
	runtimeStore, mock, cleanup := newMockRuntimeStore(t)
	defer cleanup()
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)

	mock.ExpectQuery("SELECT task_id, task_type, status, payload_json, result_json, error_message, retry_count, max_attempts").
		WithArgs("parse", "request-1", "client-1").
		WillReturnRows(sqlmock.NewRows([]string{
			"task_id", "task_type", "status", "payload_json", "result_json", "error_message", "retry_count", "max_attempts",
			"locked_by", "locked_until", "next_attempt_at", "request_id", "client_id", "created_at", "updated_at", "started_at", "finished_at",
		}))
	mock.ExpectExec("INSERT INTO parse_tasks").WillReturnResult(sqlmock.NewResult(1, 1))
	created, err := runtimeStore.Create(context.Background(), task.CreateRequest{
		ID:          "parse_1234567890123456789012",
		Type:        "parse",
		Payload:     []byte(`{"url":"https://example.com/v"}`),
		RequestID:   "request-1",
		ClientID:    "client-1",
		MaxAttempts: 3,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if created.ID == "" || created.Status != task.Pending || created.MaxAttempts != 3 {
		t.Fatalf("created task = %#v", created)
	}

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id, task_id, task_type, payload_json, retry_count, max_attempts").
		WillReturnRows(sqlmock.NewRows([]string{"id", "task_id", "task_type", "payload_json", "retry_count", "max_attempts", "request_id", "client_id", "created_at", "updated_at"}).
			AddRow(9, created.ID, "parse", []byte(`{"url":"https://example.com/v"}`), 0, 3, "request-1", "client-1", now, now))
	mock.ExpectExec("UPDATE parse_tasks SET status = 'running'").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	claimed, ok, err := runtimeStore.ClaimNext(context.Background(), "worker-a", now, 30*time.Second, 3)
	if err != nil || !ok {
		t.Fatalf("ClaimNext() ok/error = %t/%v", ok, err)
	}
	if claimed.ID != created.ID || claimed.Status != task.Running || claimed.Attempts != 1 || claimed.LockedBy != "worker-a" {
		t.Fatalf("claimed task = %#v", claimed)
	}

	mock.ExpectExec("UPDATE parse_tasks SET status = 'completed'").WillReturnResult(sqlmock.NewResult(0, 1))
	if err := runtimeStore.Complete(context.Background(), claimed.ID, "worker-a", []byte(`{"ok":true}`)); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
}

func newMockRuntimeStore(t *testing.T) (*MySQLRuntimeStore, sqlmock.Sqlmock, func()) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	runtimeStore, err := NewMySQLRuntimeStore(db)
	if err != nil {
		_ = db.Close()
		t.Fatalf("NewMySQLRuntimeStore() error = %v", err)
	}
	return runtimeStore, mock, func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet SQL expectations: %v", err)
		}
		_ = db.Close()
	}
}
