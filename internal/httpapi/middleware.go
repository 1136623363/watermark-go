package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/1136623363/watermark-go/internal/download"
	"github.com/1136623363/watermark-go/internal/observability"
)

const RequestIDHeader = "X-Request-ID"

var requestIDPattern = regexp.MustCompile(`^[a-fA-F0-9]{32}$`)

type EventLogger interface {
	Log(observability.Event)
}

type StreamingOptions struct {
	IdleTimeout time.Duration
	MaxBytes    int64
	BufferSize  int
}

func RequestIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		requestID := strings.TrimSpace(c.GetHeader(RequestIDHeader))
		if !requestIDPattern.MatchString(requestID) {
			requestID = newRequestID()
		}
		requestID = strings.ToLower(requestID)
		c.Set("request_id", requestID)
		c.Header(RequestIDHeader, requestID)
		c.Next()
	}
}

func CORSMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if origin := strings.TrimSpace(c.GetHeader("Origin")); origin != "" {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Vary", "Origin")
		}
		c.Header("Access-Control-Allow-Methods", "GET,POST,OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization, token, X-Request-ID, X-CSRF-Token, X-Client-ID")
		c.Header("Access-Control-Expose-Headers", "X-Request-ID, Content-Length, Content-Range")
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

func RequestLogMiddleware(logger EventLogger) gin.HandlerFunc {
	return func(c *gin.Context) {
		startedAt := time.Now()
		c.Next()
		if logger == nil {
			return
		}
		logger.Log(observability.Event{
			RequestID: requestIDFromContext(c),
			Stage:     "http",
			ErrorKind: statusErrorKind(c.Writer.Status()),
			Duration:  time.Since(startedAt),
		})
	}
}

func StreamingCopyWithDeadline(writer io.Writer, reader io.Reader, options StreamingOptions) (int64, error) {
	return download.CopyWithIdleDeadline(context.Background(), writer, reader, download.StreamOptions{
		IdleTimeout: options.IdleTimeout,
		MaxBytes:    options.MaxBytes,
		BufferSize:  options.BufferSize,
	})
}

func newRequestID() string {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "00000000000000000000000000000000"
	}
	return hex.EncodeToString(raw)
}

func requestIDFromContext(c *gin.Context) string {
	value, ok := c.Get("request_id")
	if !ok {
		return ""
	}
	requestID, _ := value.(string)
	return requestID
}

func statusErrorKind(status int) string {
	switch {
	case status >= 500:
		return "http_5xx"
	case status >= 400:
		return "http_4xx"
	default:
		return ""
	}
}
