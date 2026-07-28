package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/1136623363/watermark-go/internal/admin"
	"github.com/1136623363/watermark-go/internal/auth"
	parseusecase "github.com/1136623363/watermark-go/internal/parse"
	"github.com/1136623363/watermark-go/internal/task"
)

var (
	_ auth.Store                  = (*MySQLRuntimeStore)(nil)
	_ admin.UserStore             = (*MySQLRuntimeStore)(nil)
	_ parseusecase.Store          = (*MySQLRuntimeStore)(nil)
	_ parseusecase.CachedReader   = (*MySQLRuntimeStore)(nil)
	_ parseusecase.AsyncTaskStore = (*MySQLRuntimeStore)(nil)
	_ task.LeaseStore             = (*MySQLRuntimeStore)(nil)
)

type MySQLRuntimeStore struct {
	db    *sql.DB
	clock func() time.Time
}

func NewMySQLRuntimeStore(db *sql.DB) (*MySQLRuntimeStore, error) {
	if db == nil {
		return nil, errors.New("mysql runtime store requires db")
	}
	return &MySQLRuntimeStore{db: db, clock: time.Now}, nil
}

func (store *MySQLRuntimeStore) EnsureIdentity(ctx context.Context, identity auth.Identity) (auth.IdentityResult, error) {
	if store == nil || store.db == nil {
		return auth.IdentityResult{}, errors.New("mysql runtime store is unavailable")
	}
	ctx = contextOrBackground(ctx)
	identity.Type = strings.TrimSpace(identity.Type)
	identity.Key = strings.TrimSpace(identity.Key)
	if identity.Type == "" || identity.Key == "" {
		return auth.IdentityResult{}, errors.New("identity binding is incomplete")
	}
	metadataJSON, err := json.Marshal(identity.Metadata)
	if err != nil {
		return auth.IdentityResult{}, err
	}
	unionKey := metadataString(identity.Metadata, "unionid")
	now := store.now()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return auth.IdentityResult{}, err
	}
	defer tx.Rollback()

	var result auth.IdentityResult
	err = tx.QueryRowContext(ctx, `
SELECT u.id, u.public_id
FROM app_user_identities i
JOIN app_users u ON u.id = i.user_id
WHERE i.identity_type = ? AND i.identity_key = ? AND u.status = 1
FOR UPDATE`, identity.Type, identity.Key).Scan(&result.UserID, &result.PublicID)
	switch {
	case err == nil:
		if _, err := tx.ExecContext(ctx, `
UPDATE app_user_identities
SET union_key = ?, metadata_json = ?, updated_at = ?
WHERE identity_type = ? AND identity_key = ?`, unionKey, string(metadataJSON), now, identity.Type, identity.Key); err != nil {
			return auth.IdentityResult{}, err
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE app_users
SET last_seen_at = ?, updated_at = ?
WHERE id = ?`, now, now, result.UserID); err != nil {
			return auth.IdentityResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return auth.IdentityResult{}, err
		}
		result.IsFirstLogin = false
		return result, nil
	case errors.Is(err, sql.ErrNoRows):
	default:
		return auth.IdentityResult{}, err
	}

	publicID := stableRuntimePublicID(identity.Type, identity.Key)
	insertUser, err := tx.ExecContext(ctx, `
INSERT INTO app_users (public_id, last_seen_at, created_at, updated_at)
VALUES (?, ?, ?, ?)`, publicID, now, now, now)
	if err != nil {
		return auth.IdentityResult{}, err
	}
	userID, err := insertUser.LastInsertId()
	if err != nil || userID <= 0 {
		return auth.IdentityResult{}, errors.New("created app user id is unavailable")
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO app_user_identities (user_id, identity_type, identity_key, union_key, metadata_json, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)`, userID, identity.Type, identity.Key, unionKey, string(metadataJSON), now, now); err != nil {
		return auth.IdentityResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return auth.IdentityResult{}, err
	}
	return auth.IdentityResult{
		UserID:       userID,
		PublicID:     publicID,
		IsFirstLogin: true,
	}, nil
}

func (store *MySQLRuntimeStore) StoreSession(ctx context.Context, session auth.SessionRecord) error {
	if store == nil || store.db == nil {
		return errors.New("mysql runtime store is unavailable")
	}
	ctx = contextOrBackground(ctx)
	if !validRuntimeTokenHash(session.TokenHash) || session.UserID <= 0 || session.ExpiresAt.IsZero() {
		return errors.New("session record is incomplete")
	}
	now := store.now()
	_, err := store.db.ExecContext(ctx, `
INSERT INTO app_client_sessions (token_hash, user_id, program_type, status, expires_at, created_at, last_seen_at)
VALUES (?, ?, ?, 1, ?, ?, ?)
ON DUPLICATE KEY UPDATE
  user_id = VALUES(user_id),
  program_type = VALUES(program_type),
  status = 1,
  expires_at = VALUES(expires_at),
  last_seen_at = VALUES(last_seen_at)`, string(session.TokenHash), session.UserID, session.ProgramType, session.ExpiresAt, now, now)
	return err
}

func (store *MySQLRuntimeStore) LookupSession(ctx context.Context, hash auth.TokenHash, now time.Time) (auth.SessionRecord, error) {
	if store == nil || store.db == nil {
		return auth.SessionRecord{}, auth.ErrInvalidToken
	}
	ctx = contextOrBackground(ctx)
	if !validRuntimeTokenHash(hash) {
		return auth.SessionRecord{}, auth.ErrInvalidToken
	}
	if now.IsZero() {
		now = store.now()
	}
	var session auth.SessionRecord
	err := store.db.QueryRowContext(ctx, `
SELECT s.user_id, u.public_id, s.program_type, s.expires_at
FROM app_client_sessions s
JOIN app_users u ON u.id = s.user_id
WHERE s.token_hash = ? AND s.status = 1 AND (s.expires_at IS NULL OR s.expires_at > ?) AND u.status = 1`, string(hash), now).
		Scan(&session.UserID, &session.PublicID, &session.ProgramType, &session.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return auth.SessionRecord{}, auth.ErrInvalidToken
	}
	if err != nil {
		return auth.SessionRecord{}, err
	}
	session.TokenHash = hash
	return session, nil
}

func (store *MySQLRuntimeStore) SeedAdminUser(ctx context.Context, username string, passphrase string) error {
	if store == nil || store.db == nil {
		return errors.New("mysql runtime store is unavailable")
	}
	ctx = contextOrBackground(ctx)
	username = strings.TrimSpace(username)
	if username == "" {
		username = "admin"
	}
	passphrase = strings.TrimSpace(passphrase)
	if passphrase == "" {
		return nil
	}
	hash, err := admin.HashPassword(passphrase)
	if err != nil {
		return err
	}
	now := store.now()
	_, err = store.db.ExecContext(ctx, `
INSERT INTO admin_users (username, role, password_hash, status, created_at, updated_at)
VALUES (?, ?, ?, 1, ?, ?)
ON DUPLICATE KEY UPDATE
  role = VALUES(role),
  password_hash = VALUES(password_hash),
  status = 1,
  updated_at = VALUES(updated_at)`, username, string(admin.RoleOwner), hash, now, now)
	return err
}

func (store *MySQLRuntimeStore) FindUser(ctx context.Context, username string) (admin.User, bool, error) {
	if store == nil || store.db == nil {
		return admin.User{}, false, errors.New("mysql runtime store is unavailable")
	}
	ctx = contextOrBackground(ctx)
	username = strings.TrimSpace(username)
	if username == "" {
		return admin.User{}, false, nil
	}
	var user admin.User
	var role string
	err := store.db.QueryRowContext(ctx, `
SELECT username, role, password_hash
FROM admin_users
WHERE username = ? AND status = 1`, username).Scan(&user.Username, &role, &user.PasswordHash)
	if errors.Is(err, sql.ErrNoRows) {
		return admin.User{}, false, nil
	}
	if err != nil {
		return admin.User{}, false, err
	}
	user.Role = admin.Role(strings.TrimSpace(role))
	if user.Role == "" {
		user.Role = admin.RoleOwner
	}
	return user, true, nil
}

func (store *MySQLRuntimeStore) RecordAudit(ctx context.Context, record admin.AuditRecord) error {
	if store == nil || store.db == nil {
		return errors.New("mysql runtime store is unavailable")
	}
	ctx = contextOrBackground(ctx)
	record.Username = strings.TrimSpace(record.Username)
	record.Action = strings.TrimSpace(record.Action)
	if record.Username == "" || record.Action == "" {
		return errors.New("admin audit record is incomplete")
	}
	details, err := json.Marshal(record.Details)
	if err != nil {
		return err
	}
	createdAt := record.CreatedAt
	if createdAt.IsZero() {
		createdAt = store.now()
	}
	_, err = store.db.ExecContext(ctx, `
INSERT INTO admin_audit_logs (username, action, resource, resource_id, details_json, created_at)
VALUES (?, ?, ?, ?, ?, ?)`, record.Username, record.Action, strings.TrimSpace(record.Resource), strings.TrimSpace(record.ResourceID), string(details), createdAt)
	return err
}

func (store *MySQLRuntimeStore) SaveResult(ctx context.Context, result parseusecase.StoredResult) error {
	if store == nil || store.db == nil {
		return errors.New("mysql runtime store is unavailable")
	}
	ctx = contextOrBackground(ctx)
	result.ShareID = strings.TrimSpace(result.ShareID)
	if result.ShareID == "" {
		return nil
	}
	data := result.Data
	if strings.TrimSpace(data.ShareID) == "" {
		data.ShareID = result.ShareID
	}
	resultJSON, err := json.Marshal(data)
	if err != nil {
		return err
	}
	now := result.CreatedAt
	if now.IsZero() {
		now = store.now()
	}
	canonicalURL := strings.TrimSpace(result.Cache.CanonicalURL)
	if canonicalURL == "" {
		canonicalURL = strings.TrimSpace(data.SourceURL)
	}
	if canonicalURL == "" {
		canonicalURL = result.ShareID
	}
	platform := firstNonEmptyRuntime(result.Cache.Platform, result.Result.Platform, data.Platform, "unknown")
	resultType := firstNonEmptyRuntime(data.Type, result.Result.Type, "unknown")
	_, err = store.db.ExecContext(ctx, `
INSERT INTO parse_results
  (share_id, url_hash, source_url, normalized_url, platform, result_type, title, cover_url, author_name, result_json, status, created_at, updated_at)
VALUES
  (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)
ON DUPLICATE KEY UPDATE
  share_id = VALUES(share_id),
  source_url = VALUES(source_url),
  normalized_url = VALUES(normalized_url),
  platform = VALUES(platform),
  result_type = VALUES(result_type),
  title = VALUES(title),
  cover_url = VALUES(cover_url),
  author_name = VALUES(author_name),
  result_json = VALUES(result_json),
  status = 1,
  updated_at = VALUES(updated_at)`,
		result.ShareID,
		sha256Hex(canonicalURL),
		canonicalURL,
		canonicalURL,
		platform,
		resultType,
		strings.TrimSpace(data.Title),
		strings.TrimSpace(data.Cover),
		strings.TrimSpace(data.Author),
		string(resultJSON),
		now,
		now,
	)
	return err
}

func (store *MySQLRuntimeStore) GetCached(ctx context.Context, shareID string) (parseusecase.CompatData, bool, error) {
	if store == nil || store.db == nil {
		return parseusecase.CompatData{}, false, errors.New("mysql runtime store is unavailable")
	}
	ctx = contextOrBackground(ctx)
	shareID = strings.TrimSpace(shareID)
	if shareID == "" {
		return parseusecase.CompatData{}, false, nil
	}
	var raw []byte
	err := store.db.QueryRowContext(ctx, `
SELECT result_json
FROM parse_results
WHERE share_id = ? AND status = 1`, shareID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return parseusecase.CompatData{}, false, nil
	}
	if err != nil {
		return parseusecase.CompatData{}, false, err
	}
	var data parseusecase.CompatData
	if err := json.Unmarshal(raw, &data); err != nil {
		return parseusecase.CompatData{}, false, err
	}
	if strings.TrimSpace(data.ShareID) == "" {
		data.ShareID = shareID
	}
	return data, true, nil
}

func (store *MySQLRuntimeStore) Create(ctx context.Context, request task.CreateRequest) (task.Task, error) {
	if store == nil || store.db == nil {
		return task.Task{}, errors.New("mysql runtime store is unavailable")
	}
	ctx = contextOrBackground(ctx)
	request.ID = strings.TrimSpace(request.ID)
	request.Type = strings.TrimSpace(request.Type)
	if request.ID == "" || request.Type == "" {
		return task.Task{}, errors.New("task identity is incomplete")
	}
	maxAttempts := request.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 2
	}
	now := store.now()
	if strings.TrimSpace(request.RequestID) != "" && strings.TrimSpace(request.ClientID) != "" {
		if existing, ok, err := store.findExistingTaskByRequest(ctx, request.Type, request.RequestID, request.ClientID); err != nil || ok {
			return existing, err
		}
	}
	_, err := store.db.ExecContext(ctx, `
INSERT INTO parse_tasks
  (task_id, task_type, status, payload_json, retry_count, max_attempts, available_at, next_attempt_at, request_id, client_id, created_at, updated_at)
VALUES
  (?, ?, 'pending', ?, 0, ?, ?, ?, ?, ?, ?, ?)`,
		request.ID,
		request.Type,
		append([]byte(nil), request.Payload...),
		maxAttempts,
		now,
		now,
		strings.TrimSpace(request.RequestID),
		strings.TrimSpace(request.ClientID),
		now,
		now,
	)
	if err != nil {
		return task.Task{}, err
	}
	return task.Task{
		ID:            request.ID,
		Type:          request.Type,
		Status:        task.Pending,
		Payload:       append([]byte(nil), request.Payload...),
		MaxAttempts:   maxAttempts,
		NextAttemptAt: now,
		RequestID:     strings.TrimSpace(request.RequestID),
		ClientID:      strings.TrimSpace(request.ClientID),
		CreatedAt:     now,
		UpdatedAt:     now,
	}, nil
}

func (store *MySQLRuntimeStore) Get(ctx context.Context, id string) (task.Task, bool, error) {
	if store == nil || store.db == nil {
		return task.Task{}, false, errors.New("mysql runtime store is unavailable")
	}
	ctx = contextOrBackground(ctx)
	return store.findTaskByPredicate(ctx, "task_id = ?", strings.TrimSpace(id))
}

func (store *MySQLRuntimeStore) ClaimNext(ctx context.Context, workerID string, now time.Time, lease time.Duration, maxAttempts int) (task.Task, bool, error) {
	if store == nil || store.db == nil {
		return task.Task{}, false, errors.New("mysql runtime store is unavailable")
	}
	ctx = contextOrBackground(ctx)
	if now.IsZero() {
		now = store.now()
	}
	if lease <= 0 {
		lease = 15 * time.Second
	}
	if maxAttempts <= 0 {
		maxAttempts = 2
	}
	workerID = strings.TrimSpace(workerID)
	if workerID == "" {
		workerID = "worker"
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return task.Task{}, false, err
	}
	defer tx.Rollback()
	var rowID int64
	var item task.Task
	var status string
	err = tx.QueryRowContext(ctx, `
SELECT id, task_id, task_type, payload_json, retry_count, max_attempts, request_id, client_id, created_at, updated_at
FROM parse_tasks
WHERE status = 'pending'
  AND (next_attempt_at IS NULL OR next_attempt_at <= ?)
  AND retry_count < max_attempts
ORDER BY created_at ASC
LIMIT 1
FOR UPDATE SKIP LOCKED`, now).Scan(
		&rowID,
		&item.ID,
		&item.Type,
		&item.Payload,
		&item.Attempts,
		&item.MaxAttempts,
		&item.RequestID,
		&item.ClientID,
		&item.CreatedAt,
		&item.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return task.Task{}, false, err
		}
		return task.Task{}, false, nil
	}
	if err != nil {
		return task.Task{}, false, err
	}
	if item.MaxAttempts <= 0 {
		item.MaxAttempts = maxAttempts
	}
	item.Attempts++
	item.Status = task.Running
	item.LockedBy = workerID
	item.LockedUntil = now.Add(lease)
	item.StartedAt = now
	item.UpdatedAt = now
	status = string(task.Running)
	if _, err := tx.ExecContext(ctx, `
UPDATE parse_tasks SET status = 'running',
  retry_count = retry_count + 1,
  locked_by = ?,
  locked_until = ?,
  started_at = COALESCE(started_at, ?),
  updated_at = ?
WHERE id = ? AND status = 'pending'`, workerID, item.LockedUntil, now, now, rowID); err != nil {
		return task.Task{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return task.Task{}, false, err
	}
	item.Status = task.Status(status)
	return item, true, nil
}

func (store *MySQLRuntimeStore) RenewLease(ctx context.Context, id, workerID string, until time.Time) error {
	if until.IsZero() {
		until = store.now().Add(15 * time.Second)
	}
	result, err := store.execOwnedTaskUpdate(ctx, `
UPDATE parse_tasks
SET locked_until = ?, updated_at = ?
WHERE task_id = ? AND locked_by = ? AND status = 'running'`, until, store.now(), strings.TrimSpace(id), strings.TrimSpace(workerID))
	if err != nil {
		return err
	}
	return requireAffected(result, "task lease not owned")
}

func (store *MySQLRuntimeStore) Complete(ctx context.Context, id, workerID string, resultJSON []byte) error {
	result, err := store.execOwnedTaskUpdate(ctx, `
UPDATE parse_tasks SET status = 'completed',
  result_json = ?,
  locked_by = '',
  locked_until = NULL,
  finished_at = ?,
  updated_at = ?
WHERE task_id = ? AND locked_by = ? AND status = 'running'`, append([]byte(nil), resultJSON...), store.now(), store.now(), strings.TrimSpace(id), strings.TrimSpace(workerID))
	if err != nil {
		return err
	}
	return requireAffected(result, "task lease not owned")
}

func (store *MySQLRuntimeStore) Fail(ctx context.Context, id, workerID, message string, retryAt time.Time) error {
	if retryAt.IsZero() {
		retryAt = store.now().Add(2 * time.Second)
	}
	now := store.now()
	result, err := store.execOwnedTaskUpdate(ctx, `
UPDATE parse_tasks
SET error_message = ?,
  status = CASE WHEN retry_count >= max_attempts THEN 'failed' ELSE 'pending' END,
  locked_by = '',
  locked_until = NULL,
  next_attempt_at = CASE WHEN retry_count >= max_attempts THEN next_attempt_at ELSE ? END,
  finished_at = CASE WHEN retry_count >= max_attempts THEN ? ELSE finished_at END,
  updated_at = ?
WHERE task_id = ? AND locked_by = ? AND status = 'running'`, strings.TrimSpace(message), retryAt, now, now, strings.TrimSpace(id), strings.TrimSpace(workerID))
	if err != nil {
		return err
	}
	return requireAffected(result, "task lease not owned")
}

func (store *MySQLRuntimeStore) RecoverExpired(ctx context.Context, now time.Time) (int, error) {
	if store == nil || store.db == nil {
		return 0, errors.New("mysql runtime store is unavailable")
	}
	ctx = contextOrBackground(ctx)
	if now.IsZero() {
		now = store.now()
	}
	result, err := store.db.ExecContext(ctx, `
UPDATE parse_tasks
SET status = 'pending',
  locked_by = '',
  locked_until = NULL,
  next_attempt_at = ?,
  updated_at = ?
WHERE status = 'running' AND locked_until IS NOT NULL AND locked_until <= ?`, now, now, now)
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(count), nil
}

func (store *MySQLRuntimeStore) findExistingTaskByRequest(ctx context.Context, taskType, requestID, clientID string) (task.Task, bool, error) {
	return store.findTaskByPredicate(ctx, "task_type = ? AND request_id = ? AND client_id = ?", strings.TrimSpace(taskType), strings.TrimSpace(requestID), strings.TrimSpace(clientID))
}

func (store *MySQLRuntimeStore) findTaskByPredicate(ctx context.Context, predicate string, args ...any) (task.Task, bool, error) {
	query := fmt.Sprintf(`
SELECT task_id, task_type, status, payload_json, result_json, error_message, retry_count, max_attempts,
       locked_by, locked_until, next_attempt_at, request_id, client_id, created_at, updated_at, started_at, finished_at
FROM parse_tasks
WHERE %s
LIMIT 1`, predicate)
	row := store.db.QueryRowContext(ctx, query, args...)
	item, err := scanRuntimeTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return task.Task{}, false, nil
	}
	if err != nil {
		return task.Task{}, false, err
	}
	return item, true, nil
}

type runtimeTaskScanner interface {
	Scan(dest ...any) error
}

func scanRuntimeTask(row runtimeTaskScanner) (task.Task, error) {
	var item task.Task
	var status string
	var resultJSON []byte
	var errorMessage string
	var lockedUntil, nextAttemptAt, startedAt, finishedAt sql.NullTime
	if err := row.Scan(
		&item.ID,
		&item.Type,
		&status,
		&item.Payload,
		&resultJSON,
		&errorMessage,
		&item.Attempts,
		&item.MaxAttempts,
		&item.LockedBy,
		&lockedUntil,
		&nextAttemptAt,
		&item.RequestID,
		&item.ClientID,
		&item.CreatedAt,
		&item.UpdatedAt,
		&startedAt,
		&finishedAt,
	); err != nil {
		return task.Task{}, err
	}
	item.Status = task.NormalizeStatus(task.Status(status))
	item.Result = append([]byte(nil), resultJSON...)
	item.ErrorMessage = strings.TrimSpace(errorMessage)
	item.LockedUntil = nullableTime(lockedUntil)
	item.NextAttemptAt = nullableTime(nextAttemptAt)
	item.StartedAt = nullableTime(startedAt)
	item.FinishedAt = nullableTime(finishedAt)
	return item, nil
}

func (store *MySQLRuntimeStore) execOwnedTaskUpdate(ctx context.Context, statement string, args ...any) (sql.Result, error) {
	if store == nil || store.db == nil {
		return nil, errors.New("mysql runtime store is unavailable")
	}
	return store.db.ExecContext(contextOrBackground(ctx), statement, args...)
}

func requireAffected(result sql.Result, message string) error {
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count <= 0 {
		return errors.New(message)
	}
	return nil
}

func (store *MySQLRuntimeStore) now() time.Time {
	if store != nil && store.clock != nil {
		return store.clock().UTC()
	}
	return time.Now().UTC()
}

func contextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func nullableTime(value sql.NullTime) time.Time {
	if value.Valid {
		return value.Time
	}
	return time.Time{}
}

func stableRuntimePublicID(identityType, identityKey string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(identityType) + "\x00" + strings.TrimSpace(identityKey)))
	return hex.EncodeToString(sum[:])[:26]
}

func validRuntimeTokenHash(hash auth.TokenHash) bool {
	value := string(hash)
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func metadataString(metadata map[string]any, key string) string {
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}

func firstNonEmptyRuntime(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(value)))
	return hex.EncodeToString(sum[:])
}
