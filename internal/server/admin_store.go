package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

type authenticatedAdmin struct {
	ID         uint64
	Username   string
	Role       string
	IsDefault  bool
	BreakGlass bool
}

var authenticateAdminMySQLQuery = authenticateAdminMySQL
var adminUsernameValidMySQLQuery = adminUsernameValidMySQL

func authenticateAdmin(username, password string) (authenticatedAdmin, bool, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return authenticatedAdmin{}, false, nil
	}

	if appInfra.mysql != nil {
		admin, found, err := authenticateAdminMySQLQuery(username, password)
		if err == nil && found {
			return admin, true, nil
		}
		if err != nil {
			logWarnf("admin mysql auth unavailable username=%s error=%v", compactLogMessage(username), err)
		}
		if !adminEnvironmentBreakGlassAllowed() {
			return authenticatedAdmin{}, false, nil
		}
		logWarnf("admin break-glass environment authentication requested username=%s", compactLogMessage(username))
	}

	creds := adminCredentials()
	if strings.TrimSpace(creds.Password) == "" {
		return authenticatedAdmin{}, false, nil
	}
	if constantTimeEqual(username, creds.Username) && constantTimeEqual(password, creds.Password) {
		return authenticatedAdmin{
			Username:   creds.Username,
			Role:       "owner",
			IsDefault:  creds.IsDefault,
			BreakGlass: appInfra.mysql != nil,
		}, true, nil
	}
	return authenticatedAdmin{}, false, nil
}

func adminEnvironmentBreakGlassAllowed() bool {
	if !envBoolLocal("ADMIN_ENV_FALLBACK_ENABLED", false) {
		return false
	}
	return validateConfiguredSecret("ADMIN_PASSWORD", os.Getenv("ADMIN_PASSWORD"), 12, false, true) == nil
}

func authenticateAdminMySQL(username, password string) (authenticatedAdmin, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var admin authenticatedAdmin
	var hash string
	err := appInfra.mysql.QueryRowContext(ctx, `
SELECT id, username, password_hash, role
FROM admin_users
WHERE username = ? AND status = 1
LIMIT 1
`, username).Scan(&admin.ID, &admin.Username, &hash, &admin.Role)
	if err != nil {
		if isNoRows(err) {
			return authenticatedAdmin{}, false, nil
		}
		return authenticatedAdmin{}, false, err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return authenticatedAdmin{}, false, nil
	}
	_, _ = appInfra.mysql.ExecContext(ctx, "UPDATE admin_users SET last_login_at = NOW() WHERE id = ?", admin.ID)
	return admin, true, nil
}

func adminUsernameValid(username string) bool {
	username = strings.TrimSpace(username)
	if username == "" {
		return false
	}
	environmentUsername := constantTimeEqual(username, adminCredentials().Username)
	if appInfra.mysql == nil {
		return environmentUsername
	}
	found, err := adminUsernameValidMySQLQuery(username)
	if err != nil {
		logErrorf("admin username validation failed username=%s error=%v", compactLogMessage(username), err)
	}
	return err == nil && found
}

func adminUsernameValidMySQL(username string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var id uint64
	err := appInfra.mysql.QueryRowContext(ctx, "SELECT id FROM admin_users WHERE username = ? AND status = 1 LIMIT 1", username).Scan(&id)
	if err == nil {
		return true, nil
	}
	if isNoRows(err) {
		return false, nil
	}
	return false, err
}

func adminIDByUsername(username string) sql.NullInt64 {
	if appInfra.mysql == nil {
		return sql.NullInt64{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var id int64
	err := appInfra.mysql.QueryRowContext(ctx, "SELECT id FROM admin_users WHERE username = ? LIMIT 1", strings.TrimSpace(username)).Scan(&id)
	if err != nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: id, Valid: true}
}

func writeAdminAudit(c *gin.Context, action, targetType, targetID string, payload interface{}) {
	if strings.TrimSpace(action) == "" {
		return
	}
	if appInfra.mysql == nil {
		logInfof("admin audit action=%s target_type=%s target_id=%s", action, targetType, targetID)
		return
	}

	body, _ := json.Marshal(payload)
	username := currentAdminUsername(c)
	adminID := adminIDByUsername(username)
	var adminIDValue interface{}
	if adminID.Valid {
		adminIDValue = adminID.Int64
	}

	var payloadValue interface{}
	if len(body) > 0 && string(body) != "null" {
		payloadValue = string(body)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := appInfra.mysql.ExecContext(ctx, `
INSERT INTO admin_audit_logs (admin_id, action, target_type, target_id, request_json, client_ip, user_agent)
VALUES (?, ?, ?, ?, ?, INET6_ATON(?), ?)
`, adminIDValue, action, targetType, targetID, payloadValue, c.ClientIP(), c.Request.UserAgent()); err != nil {
		logErrorf("admin audit write failed action=%s error=%v", action, err)
	}
}

func currentAdminUsername(c *gin.Context) string {
	if c == nil {
		return ""
	}
	if value, ok := c.Get("admin_username"); ok {
		if text, ok := value.(string); ok {
			return text
		}
	}
	cookie, err := c.Cookie(adminSessionCookie)
	if err != nil {
		return ""
	}
	claims, ok := parseAdminSessionCookie(cookie)
	if !ok {
		return ""
	}
	return claims.Username
}
