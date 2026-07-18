package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	parseusecase "github.com/1136623363/watermark-go/internal/parse"
)

type ParseService interface {
	Parse(context.Context, parseusecase.Request) (parseusecase.ParseOutput, error)
	GetCached(context.Context, string) (parseusecase.CompatData, bool, error)
}

type ParseIDService interface {
	ParseID(context.Context, parseusecase.IDRequest) (parseusecase.ParseOutput, error)
}

type ParseHandlers struct {
	Service ParseService
}

func (handlers ParseHandlers) Register(router gin.IRouter) {
	router.POST("/api/parse", handlers.Parse)
	router.GET("/api/hybrid/video_data", handlers.HybridVideoData)
	router.GET("/video/share/url/parse", handlers.LegacyShareURLParse)
	router.GET("/video/id/parse", handlers.LegacyIDParse)
	router.GET("/api/parse/cache/:id", handlers.ParseCache)
	router.GET("/api/v1/parse", handlers.V1Parse)
	router.GET("/api/v1/parse/:source/:video_id", handlers.V1ParseID)
}

func (handlers ParseHandlers) Parse(c *gin.Context) {
	var request ParseRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusOK, Response{Code: 1004, Msg: "invalid parse payload"})
		return
	}
	output, err := handlers.parse(c.Request.Context(), parseusecase.Request{
		URL:          request.URL,
		ForceRefresh: request.ForceRefresh,
		Source:       request.Source,
		Timestamp:    request.Timestamp,
		Signature:    request.Signature,
		Version:      request.Version,
	})
	if err != nil {
		c.JSON(http.StatusOK, parseErrorResponse(err))
		return
	}
	c.JSON(http.StatusOK, Response{Code: 0, Msg: "ok", Data: output.Data})
}

func (handlers ParseHandlers) HybridVideoData(c *gin.Context) {
	output, err := handlers.parse(c.Request.Context(), parseusecase.Request{URL: c.Query("url")})
	if err != nil {
		c.JSON(http.StatusOK, parseErrorResponse(err))
		return
	}
	c.JSON(http.StatusOK, Response{Code: 0, Msg: "ok", Data: output.Data})
}

func (handlers ParseHandlers) LegacyShareURLParse(c *gin.Context) {
	output, err := handlers.parse(c.Request.Context(), parseusecase.Request{URL: c.Query("url")})
	if err != nil {
		response := parseErrorResponse(err)
		response.Code = 201
		c.JSON(http.StatusOK, response)
		return
	}
	c.JSON(http.StatusOK, Response{Code: 200, Msg: "ok", Data: output.Data})
}

func (handlers ParseHandlers) LegacyIDParse(c *gin.Context) {
	output, err := handlers.parseID(c.Request.Context(), parseusecase.IDRequest{
		Source:  c.Query("source"),
		VideoID: c.Query("video_id"),
	})
	if err != nil {
		response := parseErrorResponse(err)
		response.Code = 201
		c.JSON(http.StatusOK, response)
		return
	}
	c.JSON(http.StatusOK, Response{Code: 200, Msg: "ok", Data: output.Data})
}

func (handlers ParseHandlers) ParseCache(c *gin.Context) {
	if handlers.Service == nil {
		c.JSON(http.StatusOK, Response{Code: 1004, Msg: "分享内容已失效"})
		return
	}
	shareID := strings.TrimSpace(c.Param("id"))
	data, ok, err := handlers.Service.GetCached(c.Request.Context(), shareID)
	if err != nil {
		c.JSON(http.StatusOK, Response{Code: 1001, Msg: "parse cache unavailable"})
		return
	}
	if !ok {
		c.JSON(http.StatusOK, Response{Code: 1004, Msg: "分享内容已失效"})
		return
	}
	c.JSON(http.StatusOK, Response{Code: 0, Msg: "ok", Data: data})
}

func (handlers ParseHandlers) V1Parse(c *gin.Context) {
	if strings.TrimSpace(c.Query("url")) == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"status": "error",
			"error": gin.H{
				"code":    "MISSING_PARAMETER",
				"message": "url parameter is required",
			},
		})
		return
	}
	output, err := handlers.parse(c.Request.Context(), parseusecase.Request{URL: c.Query("url")})
	if err != nil {
		response := parseErrorResponse(err)
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"status": "error",
			"error": gin.H{
				"code":    v1ParseErrorCode(err),
				"message": response.Msg,
			},
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "success", "data": output.Data})
}

func (handlers ParseHandlers) V1ParseID(c *gin.Context) {
	output, err := handlers.parseID(c.Request.Context(), parseusecase.IDRequest{
		Source:  c.Param("source"),
		VideoID: c.Param("video_id"),
	})
	if err != nil {
		response := parseErrorResponse(err)
		c.JSON(http.StatusBadRequest, gin.H{
			"status": "error",
			"error": gin.H{
				"code":    v1ParseErrorCode(err),
				"message": response.Msg,
			},
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "success", "data": output.Data})
}

func (handlers ParseHandlers) parse(ctx context.Context, request parseusecase.Request) (parseusecase.ParseOutput, error) {
	if handlers.Service == nil {
		return parseusecase.ParseOutput{}, parseusecase.NewError(parseusecase.ErrorInternal, parseusecase.StageParser, "", true)
	}
	return handlers.Service.Parse(ctx, request)
}

func (handlers ParseHandlers) parseID(ctx context.Context, request parseusecase.IDRequest) (parseusecase.ParseOutput, error) {
	service, ok := handlers.Service.(ParseIDService)
	if !ok || service == nil {
		return parseusecase.ParseOutput{}, parseusecase.NewError(parseusecase.ErrorUnsupported, parseusecase.StageInput, request.Source, false)
	}
	return service.ParseID(ctx, request)
}

func parseErrorResponse(err error) Response {
	switch parseusecase.ClassOf(err) {
	case parseusecase.ErrorInvalidInput:
		return Response{Code: 1004, Msg: "invalid url"}
	case parseusecase.ErrorUnsupported:
		return Response{Code: 1002, Msg: "unsupported platform"}
	case parseusecase.ErrorCredentialRequired:
		return Response{Code: 1001, Msg: "credential required"}
	case parseusecase.ErrorCanceled, parseusecase.ErrorUpstreamTimeout:
		return Response{Code: 1001, Msg: "parse timeout, please retry later"}
	default:
		if errors.Is(err, parseusecase.ErrEntropyUnavailable) {
			return Response{Code: 1001, Msg: "parse failed, please retry later"}
		}
		return Response{Code: 1001, Msg: "parse failed, please retry later"}
	}
}

func v1ParseErrorCode(err error) string {
	switch parseusecase.ClassOf(err) {
	case parseusecase.ErrorInvalidInput:
		return "MISSING_PARAMETER"
	case parseusecase.ErrorUnsupported:
		return "UNSUPPORTED_URL"
	default:
		return "PARSE_FAILED"
	}
}
