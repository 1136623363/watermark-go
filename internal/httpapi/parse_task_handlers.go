package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	parseusecase "github.com/1136623363/watermark-go/internal/parse"
	"github.com/1136623363/watermark-go/internal/task"
)

type ParseTaskService interface {
	Submit(context.Context, parseusecase.Request, parseusecase.TaskMeta) (parseusecase.TaskView, error)
	Get(context.Context, string) (parseusecase.TaskView, bool, error)
}

type ParseTaskHandlers struct {
	Service ParseTaskService
}

func (handlers ParseTaskHandlers) Register(router gin.IRouter) {
	router.POST("/api/parse/task", handlers.Submit)
	router.GET("/api/parse/task/:id", handlers.Get)
}

func (handlers ParseTaskHandlers) Submit(c *gin.Context) {
	var request ParseRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusOK, Response{Code: 1004, Msg: "invalid parse task payload"})
		return
	}
	if handlers.Service == nil {
		c.JSON(http.StatusOK, Response{Code: 1001, Msg: "parse task unavailable"})
		return
	}
	view, err := handlers.Service.Submit(c.Request.Context(), parseusecase.Request{
		URL:          request.URL,
		ForceRefresh: request.ForceRefresh,
		Source:       request.Source,
		Timestamp:    request.Timestamp,
		Signature:    request.Signature,
		Version:      request.Version,
	}, parseusecase.TaskMeta{
		RequestID: firstHeader(c, "X-Request-ID", "X-Request-Id"),
		ClientID:  firstHeader(c, "X-Client-ID", "X-Client-Id"),
	})
	if err != nil {
		c.JSON(http.StatusOK, parseTaskErrorResponse(err))
		return
	}
	c.JSON(http.StatusOK, Response{Code: 0, Msg: "ok", Data: view})
}

func (handlers ParseTaskHandlers) Get(c *gin.Context) {
	if handlers.Service == nil {
		c.JSON(http.StatusOK, Response{Code: 1004, Msg: "parse task not found"})
		return
	}
	view, ok, err := handlers.Service.Get(c.Request.Context(), strings.TrimSpace(c.Param("id")))
	if err != nil {
		c.JSON(http.StatusOK, parseTaskErrorResponse(err))
		return
	}
	if !ok {
		c.JSON(http.StatusOK, Response{Code: 1004, Msg: "parse task not found"})
		return
	}
	c.JSON(http.StatusOK, Response{Code: 0, Msg: "ok", Data: view})
}

func parseTaskErrorResponse(err error) Response {
	switch {
	case errors.Is(err, task.ErrEntropyUnavailable):
		return Response{Code: 1001, Msg: "parse task unavailable"}
	case parseusecase.ClassOf(err) == parseusecase.ErrorInvalidInput:
		return Response{Code: 1004, Msg: "invalid url"}
	default:
		return Response{Code: 1001, Msg: "parse task unavailable"}
	}
}

func firstHeader(c *gin.Context, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(c.GetHeader(key)); value != "" {
			return value
		}
	}
	return ""
}
