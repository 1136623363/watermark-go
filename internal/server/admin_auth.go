package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	adminSessionCookie = "watermark_admin_session"
	adminSessionTTL    = 12 * time.Hour
)

var adminSessionSecret = loadAdminSessionSecret()

type adminCredential struct {
	Username  string
	Password  string
	IsDefault bool
}

type adminSessionClaims struct {
	Username  string
	ExpiresAt int64
	Mode      adminSessionMode
}

type adminSessionMode string

const (
	adminSessionModeMySQL       adminSessionMode = "mysql"
	adminSessionModeEnvironment adminSessionMode = "environment"
	adminSessionModeBreakGlass  adminSessionMode = "breakglass"
)

func handleAdminLoginPage(c *gin.Context) {
	if validAdminSession(c) {
		c.Redirect(http.StatusFound, "/admin")
		return
	}
	creds := adminCredentials()
	c.HTML(http.StatusOK, "login.html", gin.H{
		"title":      "Admin Login",
		"useDefault": appInfra.mysql == nil && creds.IsDefault,
	})
}

func handleAdminLogin(c *gin.Context) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, httpResponse{Code: 1004, Msg: "invalid login payload"})
		return
	}

	admin, ok, err := authenticateAdmin(req.Username, req.Password)
	if err != nil {
		logErrorf("admin login failed username=%s ip=%s error=%v", compactLogMessage(req.Username), c.ClientIP(), err)
		c.JSON(http.StatusInternalServerError, httpResponse{Code: 1001, Msg: "login failed, please retry later"})
		return
	}
	if !ok {
		logWarnf("admin login failed username=%s ip=%s", compactLogMessage(req.Username), c.ClientIP())
		c.JSON(http.StatusUnauthorized, httpResponse{Code: 1001, Msg: "invalid username or password"})
		return
	}

	mode := adminSessionModeEnvironment
	if appInfra.mysql != nil {
		mode = adminSessionModeMySQL
	}
	if admin.BreakGlass {
		mode = adminSessionModeBreakGlass
	}
	setAdminSessionCookie(c, admin.Username, mode)
	c.Set("admin_username", admin.Username)
	logInfof("admin login succeeded username=%s ip=%s break_glass=%t", admin.Username, c.ClientIP(), admin.BreakGlass)
	writeAdminAudit(c, "admin.login", "admin_user", admin.Username, gin.H{"username": admin.Username, "breakGlass": admin.BreakGlass})
	c.JSON(http.StatusOK, httpResponse{Code: 0, Msg: "ok"})
}

func handleAdminLogout(c *gin.Context) {
	clearAdminSessionCookie(c)
	c.JSON(http.StatusOK, httpResponse{Code: 0, Msg: "ok"})
}

func requireAdminSession() gin.HandlerFunc {
	return func(c *gin.Context) {
		if validAdminSession(c) {
			c.Set("admin_username", currentAdminUsername(c))
			c.Next()
			return
		}
		if strings.HasPrefix(c.Request.URL.Path, "/admin/api") ||
			strings.HasPrefix(c.Request.URL.Path, "/api/settings") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, httpResponse{
				Code: 401,
				Msg:  "please login first",
			})
			return
		}
		c.Redirect(http.StatusFound, "/admin/login")
		c.Abort()
	}
}

func adminCredentials() adminCredential {
	username := firstNonEmpty(
		os.Getenv("ADMIN_USERNAME"),
		os.Getenv("PARSE_VIDEO_USERNAME"),
		"admin",
	)
	password := firstNonEmpty(
		os.Getenv("ADMIN_PASSWORD"),
		os.Getenv("PARSE_VIDEO_PASSWORD"),
	)
	return adminCredential{
		Username: username,
		Password: password,
		IsDefault: strings.TrimSpace(os.Getenv("ADMIN_PASSWORD")) == "" &&
			strings.TrimSpace(os.Getenv("PARSE_VIDEO_PASSWORD")) == "",
	}
}

func setAdminSessionCookie(c *gin.Context, username string, mode adminSessionMode) {
	if !mode.valid() {
		return
	}
	expires := time.Now().Add(adminSessionTTL).Unix()
	payload := username + "|" + formatUnix(expires) + "|" + string(mode)
	signature := signAdminSession(payload)
	value := base64.RawURLEncoding.EncodeToString([]byte(payload + "|" + signature))
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     adminSessionCookie,
		Value:    value,
		Path:     "/",
		MaxAge:   int(adminSessionTTL.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   isHTTPSRequest(c),
	})
}

func clearAdminSessionCookie(c *gin.Context) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     adminSessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   isHTTPSRequest(c),
	})
}

func validAdminSession(c *gin.Context) bool {
	cookie, err := c.Cookie(adminSessionCookie)
	if err != nil {
		return false
	}
	claims, ok := parseAdminSessionCookie(cookie)
	if !ok || time.Now().Unix() > claims.ExpiresAt {
		return false
	}
	switch claims.Mode {
	case adminSessionModeMySQL:
		return appInfra.mysql != nil && adminUsernameValid(claims.Username)
	case adminSessionModeEnvironment:
		return appInfra.mysql == nil && adminUsernameValid(claims.Username)
	case adminSessionModeBreakGlass:
		return appInfra.mysql != nil &&
			adminEnvironmentBreakGlassAllowed() &&
			constantTimeEqual(claims.Username, adminCredentials().Username)
	default:
		return false
	}
}

func parseAdminSessionCookie(cookie string) (adminSessionClaims, bool) {
	decoded, err := base64.RawURLEncoding.DecodeString(cookie)
	if err != nil {
		return adminSessionClaims{}, false
	}
	parts := strings.Split(string(decoded), "|")
	if len(parts) != 4 {
		return adminSessionClaims{}, false
	}
	username := strings.TrimSpace(parts[0])
	expires, ok := parseUnix(parts[1])
	mode := adminSessionMode(parts[2])
	if username == "" || !ok || !mode.valid() {
		return adminSessionClaims{}, false
	}

	payload := username + "|" + parts[1] + "|" + parts[2]
	if !constantTimeEqual(parts[3], signAdminSession(payload)) {
		return adminSessionClaims{}, false
	}
	return adminSessionClaims{
		Username:  username,
		ExpiresAt: expires,
		Mode:      mode,
	}, true
}

func (mode adminSessionMode) valid() bool {
	switch mode {
	case adminSessionModeMySQL, adminSessionModeEnvironment, adminSessionModeBreakGlass:
		return true
	default:
		return false
	}
}

func signAdminSession(payload string) string {
	mac := hmac.New(sha256.New, []byte(adminSessionSecret))
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

func loadAdminSessionSecret() string {
	if value := strings.TrimSpace(os.Getenv("ADMIN_SESSION_SECRET")); value != "" {
		return value
	}
	value, err := secureRandomHex(32)
	if err != nil {
		panic("initialize admin session secret: entropy unavailable")
	}
	return value
}

func constantTimeEqual(left, right string) bool {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	if len(left) != len(right) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

func isHTTPSRequest(c *gin.Context) bool {
	if c == nil || c.Request == nil {
		return false
	}
	if c.Request.TLS != nil {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(c.GetHeader("X-Forwarded-Proto")), "https")
}

func formatUnix(value int64) string {
	return strconv.FormatInt(value, 10)
}

func parseUnix(value string) (int64, bool) {
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return 0, false
	}
	return parsed, true
}
