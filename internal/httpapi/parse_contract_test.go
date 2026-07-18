package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	parseusecase "github.com/1136623363/watermark-go/internal/parse"
)

func TestParseContractPostUsesMsgEnvelopeAndFrontendAliases(t *testing.T) {
	service := &fakeParseService{result: parseusecase.ParseOutput{
		Result: parseusecase.Result{
			Platform: "douyin",
			Type:     "video",
			Title:    "title",
			VideoURL: "https://cdn.example/v.mp4",
			AudioURL: "https://cdn.example/a.mp3",
		},
	}}
	router := newParseRouter(service)

	res := postJSON(t, router, "/api/parse", `{"url":"https://example.com/v"}`, nil)
	if res.Code != http.StatusOK {
		t.Fatalf("HTTP status = %d, want 200", res.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode parse response: %v", err)
	}
	if _, hasMessage := body["message"]; hasMessage {
		t.Fatalf("response used Layzz-style message envelope: %s", res.Body.String())
	}
	if body["msg"] != "ok" || body["code"] != float64(0) {
		t.Fatalf("unexpected envelope: %s", res.Body.String())
	}
	data := body["data"].(map[string]any)
	if data["music"] != "https://cdn.example/a.mp3" || data["mp3"] != data["music"] || data["audioUrl"] != data["music"] {
		t.Fatalf("frontend audio aliases missing: %#v", data)
	}
	if data["playAddr"] != "https://cdn.example/v.mp4" {
		t.Fatalf("frontend video alias missing: %#v", data)
	}
}

func TestParseContractForceRefreshAndErrorMapping(t *testing.T) {
	service := &fakeParseService{err: parseusecase.NewError(parseusecase.ErrorUnsupported, parseusecase.StageInput, "", false)}
	router := newParseRouter(service)

	res := postJSON(t, router, "/api/parse", `{"url":"https://example.com/v","forceRefresh":true}`, nil)
	if res.Code != http.StatusOK {
		t.Fatalf("HTTP status = %d, want 200", res.Code)
	}
	if len(service.requests) != 1 || !service.requests[0].ForceRefresh {
		t.Fatalf("forceRefresh was not passed to parse service: %#v", service.requests)
	}
	assertJSONEq(t, `{"code":1002,"msg":"unsupported platform"}`, res.Body.String())
}

func TestParseContractHybridLegacyAndCacheRoutes(t *testing.T) {
	service := &fakeParseService{result: parseusecase.ParseOutput{
		Result: parseusecase.Result{Platform: "m3u8", Type: "m3u8", M3U8URL: "https://cdn.example/x.m3u8"},
	}}
	service.cached = parseusecase.Normalize(parseusecase.Result{Platform: "douyin", Type: "video", VideoURL: "https://cdn.example/cached.mp4"})
	router := newParseRouter(service)

	hybrid := performRequest(t, router, http.MethodGet, "/api/hybrid/video_data?url=https://example.com/v", nil, nil)
	assertJSONEq(t, `{"code":0,"msg":"ok","data":{"platform":"m3u8","type":"m3u8","title":"","desc":"","cover":"","author":"","avatar":"","music":"","mp3":"","audio":"","audioUrl":"","duration":0,"downloads":[{"url":"https://cdn.example/x.m3u8","label":"m3u8"}],"images":[],"pics":[],"m3u8":"https://cdn.example/x.m3u8","previewUrl":"https://cdn.example/x.m3u8","playAddr":"https://cdn.example/x.m3u8"}}`, hybrid.Body.String())

	legacy := performRequest(t, router, http.MethodGet, "/video/share/url/parse?url=https://example.com/v", nil, nil)
	var legacyBody struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(legacy.Body.Bytes(), &legacyBody); err != nil {
		t.Fatalf("decode legacy response: %v", err)
	}
	if legacyBody.Code != 200 {
		t.Fatalf("legacy code = %d, want 200: %s", legacyBody.Code, legacy.Body.String())
	}

	cache := performRequest(t, router, http.MethodGet, "/api/parse/cache/share-id", nil, nil)
	if !strings.Contains(cache.Body.String(), "cached.mp4") {
		t.Fatalf("cache route did not return cached data: %s", cache.Body.String())
	}
}

func TestParseContractLegacyAndV1IDRoutes(t *testing.T) {
	service := &fakeParseService{idResult: parseusecase.ParseOutput{
		Result: parseusecase.Result{Platform: "douyin", Type: "video", VideoURL: "https://cdn.example/id.mp4"},
	}}
	router := newParseRouter(service)

	legacy := performRequest(t, router, http.MethodGet, "/video/id/parse?source=douyin&video_id=42", nil, nil)
	var legacyBody struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(legacy.Body.Bytes(), &legacyBody); err != nil {
		t.Fatalf("decode legacy id response: %v", err)
	}
	if legacyBody.Code != 200 {
		t.Fatalf("legacy id code = %d, want 200: %s", legacyBody.Code, legacy.Body.String())
	}

	v1 := performRequest(t, router, http.MethodGet, "/api/v1/parse/douyin/42", nil, nil)
	if !strings.Contains(v1.Body.String(), `"status":"success"`) || !strings.Contains(v1.Body.String(), "id.mp4") {
		t.Fatalf("v1 id response is not compatible: %s", v1.Body.String())
	}
	if len(service.idRequests) != 2 || service.idRequests[0].Source != "douyin" || service.idRequests[0].VideoID != "42" {
		t.Fatalf("id requests = %#v", service.idRequests)
	}
}

func TestParseContractInvalidInputIsSanitized(t *testing.T) {
	service := &fakeParseService{err: parseusecase.NewError(parseusecase.ErrorInvalidInput, parseusecase.StageInput, "", false)}
	router := newParseRouter(service)

	res := postJSON(t, router, "/api/parse", `{"url":"not a url with token=opaque"}`, nil)
	assertJSONEq(t, `{"code":1004,"msg":"invalid url"}`, res.Body.String())
	if strings.Contains(res.Body.String(), "opaque") {
		t.Fatalf("error response exposed request material: %s", res.Body.String())
	}
}

func newParseRouter(service *fakeParseService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handlers := ParseHandlers{Service: service}
	handlers.Register(router)
	return router
}

type fakeParseService struct {
	result     parseusecase.ParseOutput
	err        error
	cached     parseusecase.CompatData
	idResult   parseusecase.ParseOutput
	requests   []parseusecase.Request
	idRequests []parseusecase.IDRequest
}

func (service *fakeParseService) Parse(_ context.Context, request parseusecase.Request) (parseusecase.ParseOutput, error) {
	service.requests = append(service.requests, request)
	if service.err != nil {
		return parseusecase.ParseOutput{}, service.err
	}
	output := service.result
	if output.Data.Type == "" {
		output.Data = parseusecase.Normalize(output.Result)
	}
	return output, nil
}

func (service *fakeParseService) ParseID(_ context.Context, request parseusecase.IDRequest) (parseusecase.ParseOutput, error) {
	service.idRequests = append(service.idRequests, request)
	if service.err != nil {
		return parseusecase.ParseOutput{}, service.err
	}
	output := service.idResult
	if output.Data.Type == "" {
		output.Data = parseusecase.Normalize(output.Result)
	}
	return output, nil
}

func (service *fakeParseService) GetCached(_ context.Context, shareID string) (parseusecase.CompatData, bool, error) {
	if shareID == "error" {
		return parseusecase.CompatData{}, false, errors.New("cache unavailable")
	}
	if service.cached.Type == "" {
		return parseusecase.CompatData{}, false, nil
	}
	return service.cached, true, nil
}

func performRequest(t *testing.T, router *gin.Engine, method, path string, body []byte, headers http.Header) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	for key, values := range headers {
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("%s %s HTTP status = %d, want 200", method, path, recorder.Code)
	}
	return recorder
}
