package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/1136623363/watermark-go/internal/download"
)

type DownloadService interface {
	CreateFallback(context.Context, download.CreateRequest) (download.TaskView, error)
	GetFallback(context.Context, string, string) (download.TaskView, bool, error)
	CreateM3U8(context.Context, download.M3U8Request) (download.TaskView, error)
	GetM3U8(context.Context, string) (download.TaskView, bool, error)
	ValidateFileTicket(context.Context, string, string) error
}

type DownloadHandlers struct {
	Service DownloadService
}

type downloadFallbackRequest struct {
	URL       string             `json:"url"`
	MediaURL  string             `json:"mediaUrl"`
	MediaType download.MediaType `json:"mediaType"`
	Attempt   int                `json:"attempt"`
}

type m3u8MergeRequest struct {
	URL string `json:"url"`
}

func (handlers DownloadHandlers) Register(router gin.IRouter) {
	router.POST("/api/download/fallback", handlers.CreateFallback)
	router.GET("/api/download/fallback/:id", handlers.GetFallback)
	router.POST("/api/m3u8/merge", handlers.CreateM3U8)
	router.GET("/api/task/:id", handlers.GetM3U8)
	router.GET("/api/task/file/:id", handlers.GetM3U8File)
}

func (handlers DownloadHandlers) CreateFallback(c *gin.Context) {
	var request downloadFallbackRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusOK, Response{Code: 1004, Msg: "invalid download fallback payload"})
		return
	}
	if handlers.Service == nil {
		c.JSON(http.StatusOK, Response{Code: 1001, Msg: "download fallback unavailable"})
		return
	}
	mediaURL := strings.TrimSpace(request.MediaURL)
	if mediaURL == "" {
		mediaURL = strings.TrimSpace(request.URL)
	}
	view, err := handlers.Service.CreateFallback(c.Request.Context(), download.CreateRequest{
		MediaURL:  mediaURL,
		MediaType: request.MediaType,
		Attempt:   request.Attempt,
		ClientID:  firstHeader(c, "X-Client-ID", "X-Client-Id"),
		RequestID: firstHeader(c, "X-Request-ID", "X-Request-Id"),
	})
	if err != nil || !viewHasCreateURL(view) {
		c.JSON(http.StatusOK, downloadErrorResponse(err))
		return
	}
	c.JSON(http.StatusOK, Response{Code: 0, Msg: "ok", Data: fallbackData(view)})
}

func (handlers DownloadHandlers) GetFallback(c *gin.Context) {
	if handlers.Service == nil {
		c.JSON(http.StatusOK, Response{Code: 1004, Msg: "download task not found"})
		return
	}
	view, ok, err := handlers.Service.GetFallback(c.Request.Context(), strings.TrimSpace(c.Param("id")), strings.TrimSpace(c.Query("ticket")))
	if err != nil {
		c.JSON(http.StatusOK, downloadErrorResponse(err))
		return
	}
	if !ok {
		c.JSON(http.StatusOK, Response{Code: 1004, Msg: "download task not found"})
		return
	}
	if view.Status == download.StatusCompleted && view.DownloadURL == "" {
		c.JSON(http.StatusOK, downloadErrorResponse(download.ErrURLBuild))
		return
	}
	c.JSON(http.StatusOK, Response{Code: 0, Msg: "ok", Data: fallbackData(view)})
}

func (handlers DownloadHandlers) CreateM3U8(c *gin.Context) {
	var request m3u8MergeRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusOK, Response{Code: 1004, Msg: "invalid m3u8 payload"})
		return
	}
	if handlers.Service == nil {
		c.JSON(http.StatusOK, Response{Code: 1001, Msg: "m3u8 task unavailable"})
		return
	}
	view, err := handlers.Service.CreateM3U8(c.Request.Context(), download.M3U8Request{
		URL:       request.URL,
		ClientID:  firstHeader(c, "X-Client-ID", "X-Client-Id"),
		RequestID: firstHeader(c, "X-Request-ID", "X-Request-Id"),
	})
	if err != nil || view.TaskID == "" || view.PollURL == "" {
		c.JSON(http.StatusOK, downloadErrorResponse(err))
		return
	}
	c.JSON(http.StatusOK, Response{Code: 0, Msg: "ok", Data: m3u8Data(view)})
}

func (handlers DownloadHandlers) GetM3U8(c *gin.Context) {
	if handlers.Service == nil {
		c.JSON(http.StatusOK, Response{Code: 1004, Msg: "m3u8 task not found"})
		return
	}
	view, ok, err := handlers.Service.GetM3U8(c.Request.Context(), strings.TrimSpace(c.Param("id")))
	if err != nil {
		c.JSON(http.StatusOK, downloadErrorResponse(err))
		return
	}
	if !ok {
		c.JSON(http.StatusOK, Response{Code: 1004, Msg: "m3u8 task not found"})
		return
	}
	if view.Status == download.StatusCompleted && view.FileURL == "" {
		c.JSON(http.StatusOK, downloadErrorResponse(download.ErrURLBuild))
		return
	}
	c.JSON(http.StatusOK, Response{Code: 0, Msg: "ok", Data: m3u8Data(view)})
}

func (handlers DownloadHandlers) GetM3U8File(c *gin.Context) {
	if handlers.Service == nil {
		c.JSON(http.StatusForbidden, Response{Code: 1008, Msg: "invalid download ticket"})
		return
	}
	if err := handlers.Service.ValidateFileTicket(c.Request.Context(), strings.TrimSpace(c.Param("id")), strings.TrimSpace(c.Query("ticket"))); err != nil {
		c.JSON(http.StatusForbidden, Response{Code: 1008, Msg: "invalid download ticket"})
		return
	}
	c.JSON(http.StatusOK, Response{Code: 0, Msg: "ok"})
}

func viewHasCreateURL(view download.TaskView) bool {
	if view.TaskID == "" {
		return false
	}
	return strings.TrimSpace(view.PollURL) != "" || strings.TrimSpace(view.DownloadURL) != ""
}

func fallbackData(view download.TaskView) gin.H {
	data := gin.H{
		"taskId": view.TaskID,
		"status": string(view.Status),
	}
	if view.Progress != 0 {
		data["progress"] = view.Progress
	}
	if view.PollURL != "" {
		data["pollUrl"] = view.PollURL
	}
	if view.DownloadURL != "" {
		data["downloadUrl"] = view.DownloadURL
	}
	return data
}

func m3u8Data(view download.TaskView) gin.H {
	status := string(view.Status)
	data := gin.H{
		"taskId": view.TaskID,
		"status": status,
	}
	if view.Progress != 0 {
		data["progress"] = view.Progress
	}
	if view.PollURL != "" {
		data["pollUrl"] = view.PollURL
	}
	if view.Status == download.StatusCompleted {
		data["status"] = "done"
		if view.FileURL != "" {
			data["url"] = view.FileURL
		}
	}
	return data
}

func downloadErrorResponse(err error) Response {
	switch {
	case errors.Is(err, download.ErrAttemptTooEarly):
		return Response{Code: 1004, Msg: "download fallback requires later attempts"}
	case errors.Is(err, download.ErrUnsafeTarget):
		return Response{Code: 1004, Msg: "invalid download url"}
	case errors.Is(err, download.ErrConcurrencyLimit):
		return Response{Code: 1009, Msg: "download busy, please retry later"}
	case errors.Is(err, download.ErrInvalidTicket), errors.Is(err, download.ErrExpiredTicket):
		return Response{Code: 1008, Msg: "download ticket invalid"}
	default:
		return Response{Code: 1001, Msg: "download unavailable"}
	}
}
