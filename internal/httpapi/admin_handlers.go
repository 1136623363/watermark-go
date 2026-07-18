package httpapi

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/1136623363/watermark-go/internal/admin"
)

type AdminHandlers struct {
	Service *admin.Service
}

func (handlers AdminHandlers) Register(router gin.IRouter) {
	router.POST("/admin/api/login", handlers.Login)
	router.GET("/admin/api/summary", handlers.Summary)
	router.POST("/admin/api/settings", handlers.UpdateSettings)
	router.GET("/api/profile", handlers.Profile)
}

func (handlers AdminHandlers) Login(c *gin.Context) {
	if handlers.Service == nil || handlers.Service.Auth() == nil {
		c.JSON(http.StatusInternalServerError, Response{Code: 1001, Msg: "admin unavailable"})
		return
	}
	var request admin.LoginRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, Response{Code: 1004, Msg: "invalid login payload"})
		return
	}
	session, err := handlers.Service.Auth().Login(c.Request.Context(), request)
	if err != nil {
		c.JSON(http.StatusUnauthorized, Response{Code: 1001, Msg: "invalid username or password"})
		return
	}
	cookie, err := handlers.Service.Auth().SessionCookie(session, adminHTTPS(c))
	if err != nil {
		c.JSON(http.StatusInternalServerError, Response{Code: 1001, Msg: "admin session unavailable"})
		return
	}
	http.SetCookie(c.Writer, cookie)
	c.JSON(http.StatusOK, Response{Code: 0, Msg: "ok", Data: gin.H{
		"username":  session.Username,
		"role":      session.Role,
		"mode":      session.Mode,
		"csrfToken": session.CSRFToken,
	}})
}

func (handlers AdminHandlers) Summary(c *gin.Context) {
	if session, ok := handlers.session(c); ok {
		_ = session
		c.JSON(http.StatusOK, Response{Code: 0, Msg: "ok", Data: handlers.Service.Summary(c.Request.Context())})
		return
	}
	c.JSON(http.StatusUnauthorized, Response{Code: 401, Msg: "please login first"})
}

func (handlers AdminHandlers) UpdateSettings(c *gin.Context) {
	session, ok := handlers.session(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, Response{Code: 401, Msg: "please login first"})
		return
	}
	if err := handlers.Service.Auth().CheckWriteRequest(session, admin.WriteRequest{
		Method:    c.Request.Method,
		Origin:    c.GetHeader("Origin"),
		Host:      c.Request.Host,
		CSRFToken: c.GetHeader("X-CSRF-Token"),
	}); err != nil {
		c.JSON(http.StatusForbidden, Response{Code: 403, Msg: "forbidden"})
		return
	}
	var payload map[string]any
	_ = c.ShouldBindJSON(&payload)
	if err := handlers.Service.UpdateSettings(c.Request.Context(), session, payload); err != nil {
		c.JSON(http.StatusInternalServerError, Response{Code: 1001, Msg: "settings update failed"})
		return
	}
	c.JSON(http.StatusOK, Response{Code: 0, Msg: "ok"})
}

func (handlers AdminHandlers) Profile(c *gin.Context) {
	c.JSON(http.StatusOK, Response{Code: 1002, Msg: "unsupported"})
}

func (handlers AdminHandlers) session(c *gin.Context) (admin.Session, bool) {
	if handlers.Service == nil || handlers.Service.Auth() == nil {
		return admin.Session{}, false
	}
	cookie, err := c.Cookie(admin.SessionCookieName)
	if err != nil {
		return admin.Session{}, false
	}
	return handlers.Service.Auth().ValidateSessionCookie(c.Request.Context(), cookie)
}

func adminHTTPS(c *gin.Context) bool {
	if c.Request.TLS != nil {
		return true
	}
	return strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https")
}
