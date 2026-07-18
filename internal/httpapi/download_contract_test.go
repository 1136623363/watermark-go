package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/1136623363/watermark-go/internal/download"
)

func TestDownloadBuilderErrorsReturnNonZero(t *testing.T) {
	service := &fakeDownloadService{err: download.ErrURLBuild}
	router := newDownloadRouter(service)

	response := postJSON(t, router, "/api/download/fallback", `{"url":"https://example.com/video.mp4","mediaType":"video","attempt":4}`, nil)
	assertBusinessFailureWithoutURL(t, response.Body.String())

	service.err = nil
	service.createFallback = download.TaskView{TaskID: "task_empty", Status: download.StatusPending}
	response = postJSON(t, router, "/api/download/fallback", `{"url":"https://example.com/video.mp4","mediaType":"video","attempt":4}`, nil)
	assertBusinessFailureWithoutURL(t, response.Body.String())
}

func TestDownloadFrontendStatusCompatibility(t *testing.T) {
	service := &fakeDownloadService{
		createFallback: download.TaskView{TaskID: "fallback_task", Status: download.StatusPending, PollURL: "/api/download/fallback/fallback_task?ticket=poll"},
		fallbackPolls: []download.TaskView{{
			TaskID:      "fallback_task",
			Status:      download.StatusCompleted,
			DownloadURL: "/api/download/file/fallback_task?ticket=download",
		}},
		m3u8Create: download.TaskView{TaskID: "m3u8_task", Status: download.StatusPending, PollURL: "/api/task/m3u8_task"},
		m3u8Polls: []download.TaskView{{
			TaskID:  "m3u8_task",
			Status:  download.StatusCompleted,
			FileURL: "/api/task/file/m3u8_task?ticket=file",
		}},
	}
	router := newDownloadRouter(service)

	createdFallback := postJSON(t, router, "/api/download/fallback", `{"url":"https://example.com/video.mp4","mediaType":"video","attempt":4}`, nil)
	assertCodeZeroWithField(t, createdFallback.Body.String(), "pollUrl")

	fallbackPoll := performRequest(t, router, http.MethodGet, "/api/download/fallback/fallback_task?ticket=poll", nil, nil)
	var fallbackEnvelope struct {
		Code int `json:"code"`
		Data struct {
			Status      string `json:"status"`
			DownloadURL string `json:"downloadUrl"`
		} `json:"data"`
	}
	if err := json.Unmarshal(fallbackPoll.Body.Bytes(), &fallbackEnvelope); err != nil {
		t.Fatalf("decode fallback poll: %v body=%s", err, fallbackPoll.Body.String())
	}
	if fallbackEnvelope.Code != 0 || fallbackEnvelope.Data.Status != "completed" || fallbackEnvelope.Data.DownloadURL == "" {
		t.Fatalf("fallback poll response = %s", fallbackPoll.Body.String())
	}

	createdM3U8 := postJSON(t, router, "/api/m3u8/merge", `{"url":"https://example.com/live.m3u8"}`, nil)
	assertCodeZeroWithField(t, createdM3U8.Body.String(), "pollUrl")

	m3u8Poll := performRequest(t, router, http.MethodGet, "/api/task/m3u8_task", nil, nil)
	var m3u8Envelope struct {
		Code int `json:"code"`
		Data struct {
			Status string `json:"status"`
			URL    string `json:"url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(m3u8Poll.Body.Bytes(), &m3u8Envelope); err != nil {
		t.Fatalf("decode m3u8 poll: %v body=%s", err, m3u8Poll.Body.String())
	}
	if m3u8Envelope.Code != 0 || m3u8Envelope.Data.Status != "done" || m3u8Envelope.Data.URL == "" {
		t.Fatalf("m3u8 poll response = %s", m3u8Poll.Body.String())
	}
}

func TestDownloadTicketsRequiredOnlyForFinalFiles(t *testing.T) {
	service := &fakeDownloadService{
		m3u8Create: download.TaskView{TaskID: "m3u8_task", Status: download.StatusPending, PollURL: "/api/task/m3u8_task"},
		m3u8Polls: []download.TaskView{{
			TaskID:  "m3u8_task",
			Status:  download.StatusCompleted,
			FileURL: "/api/task/file/m3u8_task?ticket=file",
		}},
		err: nil,
	}
	router := newDownloadRouter(service)

	poll := performRequest(t, router, http.MethodGet, "/api/task/m3u8_task", nil, nil)
	assertCodeZeroWithField(t, poll.Body.String(), "url")

	fileWithoutTicket := performDownloadRequest(router, http.MethodGet, "/api/task/file/m3u8_task")
	if fileWithoutTicket.Code != http.StatusForbidden {
		t.Fatalf("file without ticket status = %d body=%s, want 403", fileWithoutTicket.Code, fileWithoutTicket.Body.String())
	}

	fileWithWrongPurpose := performDownloadRequest(router, http.MethodGet, "/api/task/file/m3u8_task?ticket=wrong-purpose")
	if fileWithWrongPurpose.Code != http.StatusForbidden {
		t.Fatalf("file with wrong-purpose ticket status = %d body=%s, want 403", fileWithWrongPurpose.Code, fileWithWrongPurpose.Body.String())
	}

	fileWithTicket := performDownloadRequest(router, http.MethodGet, "/api/task/file/m3u8_task?ticket=file")
	if fileWithTicket.Code != http.StatusOK || fileWithTicket.Body.String() != "file:m3u8_task" {
		t.Fatalf("file with ticket response = %d %q, want file body", fileWithTicket.Code, fileWithTicket.Body.String())
	}
}

func TestFallbackDownloadFileRequiresDownloadPurposeTicket(t *testing.T) {
	service := &fakeDownloadService{}
	router := newDownloadRouter(service)

	missingTicket := performDownloadRequest(router, http.MethodGet, "/api/download/file/fallback_task")
	if missingTicket.Code != http.StatusForbidden {
		t.Fatalf("fallback file missing ticket status = %d body=%s, want 403", missingTicket.Code, missingTicket.Body.String())
	}

	wrongPurpose := performDownloadRequest(router, http.MethodGet, "/api/download/file/fallback_task?ticket=file")
	if wrongPurpose.Code != http.StatusForbidden {
		t.Fatalf("fallback file wrong-purpose ticket status = %d body=%s, want 403", wrongPurpose.Code, wrongPurpose.Body.String())
	}

	withTicket := performDownloadRequest(router, http.MethodGet, "/api/download/file/fallback_task?ticket=download")
	if withTicket.Code != http.StatusOK || withTicket.Body.String() != "file:fallback_task" {
		t.Fatalf("fallback file response = %d %q, want file body", withTicket.Code, withTicket.Body.String())
	}
}

func performDownloadRequest(router *gin.Engine, method, path string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(method, path, nil))
	return recorder
}

func newDownloadRouter(service DownloadService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handlers := DownloadHandlers{Service: service}
	handlers.Register(router)
	return router
}

type fakeDownloadService struct {
	createFallback download.TaskView
	fallbackPolls  []download.TaskView
	m3u8Create     download.TaskView
	m3u8Polls      []download.TaskView
	fallbackIndex  int
	m3u8Index      int
	err            error
}

func (service *fakeDownloadService) CreateFallback(_ context.Context, _ download.CreateRequest) (download.TaskView, error) {
	if service.err != nil {
		return download.TaskView{}, service.err
	}
	return service.createFallback, nil
}

func (service *fakeDownloadService) GetFallback(_ context.Context, _ string, _ string) (download.TaskView, bool, error) {
	if service.err != nil {
		return download.TaskView{}, false, service.err
	}
	if len(service.fallbackPolls) == 0 {
		return download.TaskView{}, false, nil
	}
	index := service.fallbackIndex
	if index >= len(service.fallbackPolls) {
		index = len(service.fallbackPolls) - 1
	}
	service.fallbackIndex++
	return service.fallbackPolls[index], true, nil
}

func (service *fakeDownloadService) CreateM3U8(_ context.Context, _ download.M3U8Request) (download.TaskView, error) {
	if service.err != nil {
		return download.TaskView{}, service.err
	}
	return service.m3u8Create, nil
}

func (service *fakeDownloadService) GetM3U8(_ context.Context, _ string) (download.TaskView, bool, error) {
	if service.err != nil {
		return download.TaskView{}, false, service.err
	}
	if len(service.m3u8Polls) == 0 {
		return download.TaskView{}, false, nil
	}
	index := service.m3u8Index
	if index >= len(service.m3u8Polls) {
		index = len(service.m3u8Polls) - 1
	}
	service.m3u8Index++
	return service.m3u8Polls[index], true, nil
}

func (service *fakeDownloadService) ValidateFileTicket(_ context.Context, id string, ticket string) error {
	if id == "" || ticket != "file" {
		return download.ErrInvalidTicket
	}
	return nil
}

func (service *fakeDownloadService) ValidateDownloadTicket(_ context.Context, id string, ticket string) error {
	if id == "" || ticket != "download" {
		return download.ErrInvalidTicket
	}
	return nil
}

func (service *fakeDownloadService) ServeTaskFile(writer http.ResponseWriter, _ *http.Request, id string) error {
	if id == "" {
		return download.ErrTaskNotFound
	}
	_, _ = writer.Write([]byte("file:" + id))
	return nil
}

func assertBusinessFailureWithoutURL(t *testing.T, body string) {
	t.Helper()
	var envelope struct {
		Code int             `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatalf("decode response: %v body=%s", err, body)
	}
	if envelope.Code == 0 {
		t.Fatalf("response used success code for URL builder failure: %s", body)
	}
	if string(envelope.Data) != "" && (jsonContainsField(envelope.Data, "downloadUrl") || jsonContainsField(envelope.Data, "pollUrl")) {
		t.Fatalf("failure response leaked empty URL fields: %s", body)
	}
}

func assertCodeZeroWithField(t *testing.T, body string, field string) {
	t.Helper()
	var envelope struct {
		Code int             `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatalf("decode response: %v body=%s", err, body)
	}
	if envelope.Code != 0 {
		t.Fatalf("response code = %d body=%s, want 0", envelope.Code, body)
	}
	if !jsonContainsField(envelope.Data, field) {
		t.Fatalf("response missing field %q: %s", field, body)
	}
}

func jsonContainsField(raw json.RawMessage, field string) bool {
	if len(raw) == 0 {
		return false
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return false
	}
	value, ok := object[field]
	if !ok {
		return false
	}
	switch typed := value.(type) {
	case string:
		return typed != ""
	default:
		return true
	}
}
