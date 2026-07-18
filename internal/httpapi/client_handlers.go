package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/1136623363/watermark-go/internal/auth"
)

const invalidTokenCode = 1008

type Response struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data any    `json:"data,omitempty"`
}

type ParseRequest struct {
	URL          string `json:"url"`
	ForceRefresh bool   `json:"forceRefresh,omitempty"`
	Source       int    `json:"source,omitempty"`
	Timestamp    int64  `json:"timestamp,omitempty"`
	Signature    string `json:"signature,omitempty"`
	Version      int    `json:"version,omitempty"`
}

type ParseFunc func(context.Context, auth.AuthenticatedClient, ParseRequest) (any, error)

type ClientHandlers struct {
	Auth  *auth.Service
	Parse ParseFunc
}

func (handlers ClientHandlers) Register(router gin.IRouter) {
	router.POST("/api/client/session", handlers.ClientSession)
	router.POST("/api/parse", handlers.ParseAuthenticated)
}

func (handlers ClientHandlers) ClientSession(c *gin.Context) {
	var request auth.ClientLoginRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusOK, Response{Code: 1004, Msg: "invalid client session payload"})
		return
	}
	if handlers.Auth == nil {
		c.JSON(http.StatusOK, Response{Code: invalidTokenCode, Msg: "客户端登录暂不可用，请重试"})
		return
	}
	login, err := handlers.Auth.Login(c.Request.Context(), request)
	if err != nil {
		c.JSON(http.StatusOK, Response{Code: invalidTokenCode, Msg: "客户端登录暂不可用，请重试"})
		return
	}
	c.JSON(http.StatusOK, Response{Code: 0, Msg: "ok", Data: login})
}

func (handlers ClientHandlers) ParseAuthenticated(c *gin.Context) {
	var request ParseRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusOK, Response{Code: 1004, Msg: "invalid parse payload"})
		return
	}
	client, err := handlers.authenticate(c)
	if err != nil {
		c.JSON(http.StatusOK, Response{Code: invalidTokenCode, Msg: "登录状态已失效，请重试"})
		return
	}
	if handlers.Parse == nil {
		c.JSON(http.StatusOK, Response{Code: 1004, Msg: "parse handler unavailable"})
		return
	}
	result, err := handlers.Parse(c.Request.Context(), client, normalizeParseRequest(request))
	if err != nil {
		c.JSON(http.StatusOK, Response{Code: 1001, Msg: "parse failed"})
		return
	}
	c.JSON(http.StatusOK, Response{Code: 0, Msg: "ok", Data: result})
}

func (handlers ClientHandlers) authenticate(c *gin.Context) (auth.AuthenticatedClient, error) {
	if handlers.Auth == nil {
		return auth.AuthenticatedClient{}, auth.ErrInvalidToken
	}
	client, err := handlers.Auth.Authenticate(c.Request.Context(), c.Request.Header)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidToken) {
			return auth.AuthenticatedClient{}, err
		}
		return auth.AuthenticatedClient{}, auth.ErrInvalidToken
	}
	return client, nil
}

func normalizeParseRequest(request ParseRequest) ParseRequest {
	request.URL = strings.TrimSpace(request.URL)
	return request
}
