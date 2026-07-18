package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/1136623363/watermark-go/internal/download"
	"github.com/1136623363/watermark-go/internal/observability"
)

func TestRequiredFrontendRoutesRegistered(t *testing.T) {
	router := Router(RouterOptions{
		Download: DownloadHandlers{Service: &routerDownloadService{}},
	})
	for _, route := range []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/client/session"},
		{http.MethodPost, "/api/parse"},
		{http.MethodPost, "/api/parse/task"},
		{http.MethodGet, "/api/parse/task/task_router"},
		{http.MethodGet, "/api/parse/cache/share_router"},
		{http.MethodPost, "/api/download/fallback"},
		{http.MethodGet, "/api/download/fallback/task_router"},
		{http.MethodGet, "/api/download/file/task_router"},
		{http.MethodGet, "/api/m3u8/merge"},
		{http.MethodPost, "/api/m3u8/merge"},
		{http.MethodGet, "/api/task/task_router"},
		{http.MethodPost, "/api/client/performance"},
		{http.MethodGet, "/healthz"},
	} {
		response := performRouterRequest(router, route.method, route.path, nil, nil)
		if response.Code == http.StatusNotFound {
			t.Fatalf("%s %s was not registered", route.method, route.path)
		}
	}
	for _, route := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/download/node/main/file/video.mp4"},
		{http.MethodPost, "/internal/platform-test"},
	} {
		response := performRouterRequest(router, route.method, route.path, nil, nil)
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s %s status = %d, want 404", route.method, route.path, response.Code)
		}
	}
}

func TestRequestIDCORSAndHealthPayload(t *testing.T) {
	router := Router(RouterOptions{})
	requestID := "0123456789abcdef0123456789abcdef"
	headers := http.Header{
		"X-Request-ID": {requestID},
		"Origin":       {"https://watermark.example"},
	}
	response := performRouterRequest(router, http.MethodGet, "/healthz", nil, headers)
	if got := response.Header().Get("X-Request-ID"); got != requestID {
		t.Fatalf("X-Request-ID = %q, want accepted request id %q", got, requestID)
	}
	if got := response.Header().Get("Access-Control-Allow-Origin"); got != "https://watermark.example" {
		t.Fatalf("Access-Control-Allow-Origin = %q", got)
	}
	if exposed := response.Header().Get("Access-Control-Expose-Headers"); !strings.Contains(exposed, "X-Request-ID") {
		t.Fatalf("Access-Control-Expose-Headers = %q, want X-Request-ID exposed", exposed)
	}
	var envelope struct {
		Code int            `json:"code"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode health response: %v body=%s", err, response.Body.String())
	}
	if envelope.Code != 0 || envelope.Data["status"] != "ok" {
		t.Fatalf("health response = %s", response.Body.String())
	}
	if _, ok := envelope.Data["node"]; ok {
		t.Fatalf("health response leaked node field: %s", response.Body.String())
	}
	distributedField := "clu" + "ster"
	if _, ok := envelope.Data[distributedField]; ok {
		t.Fatalf("health response leaked distributed topology field: %s", response.Body.String())
	}

	invalidID := performRouterRequest(router, http.MethodGet, "/healthz", nil, http.Header{"X-Request-ID": {"not-a-request-id"}})
	generated := invalidID.Header().Get("X-Request-ID")
	if generated == "" || generated == "not-a-request-id" || len(generated) != 32 {
		t.Fatalf("generated request id = %q, want fresh 32 hex", generated)
	}
}

func TestRouterRequestLoggingDoesNotLeakRequestMaterial(t *testing.T) {
	var logs bytes.Buffer
	router := Router(RouterOptions{Logger: observability.NewJSONLogger(&logs)})
	requestID := "abcdefabcdefabcdefabcdefabcdefab"
	headers := http.Header{
		"X-Request-ID":  {requestID},
		"Cookie":        {"sentinel-cookie"},
		"Authorization": {"Bearer sentinel-auth"},
	}
	response := performRouterRequest(router, http.MethodGet, "/healthz?url=https://example.com/private&opaque=sentinel-query", nil, headers)
	if response.Code != http.StatusOK {
		t.Fatalf("health status = %d body=%s", response.Code, response.Body.String())
	}
	line := logs.String()
	for _, forbidden := range []string{"sentinel-cookie", "sentinel-auth", "sentinel-query", "https://example.com/private", "/healthz"} {
		if strings.Contains(line, forbidden) {
			t.Fatalf("request log leaked %q: %s", forbidden, line)
		}
	}
	var record map[string]any
	if err := json.Unmarshal([]byte(line), &record); err != nil {
		t.Fatalf("decode request log: %v line=%s", err, line)
	}
	if record["requestId"] != requestID || record["stage"] != "http" {
		t.Fatalf("request log record = %#v", record)
	}
}

func TestRouterServerTimeoutsAndStreamingSeam(t *testing.T) {
	server := NewServer(ServerOptions{Addr: "127.0.0.1:0", Handler: Router(RouterOptions{})})
	config := server.HTTPServer()
	if config.ReadHeaderTimeout != 10*time.Second || config.ReadTimeout != 20*time.Second ||
		config.WriteTimeout != 40*time.Second || config.IdleTimeout != 60*time.Second {
		t.Fatalf("server timeouts = header %s read %s write %s idle %s", config.ReadHeaderTimeout, config.ReadTimeout, config.WriteTimeout, config.IdleTimeout)
	}

	var output bytes.Buffer
	reader := &progressReader{chunks: []string{"a", "b", "c"}, delay: 5 * time.Millisecond}
	written, err := StreamingCopyWithDeadline(&output, reader, StreamingOptions{IdleTimeout: 20 * time.Millisecond, BufferSize: 1})
	if err != nil {
		t.Fatalf("StreamingCopyWithDeadline(progress) error = %v", err)
	}
	if written != 3 || output.String() != "abc" {
		t.Fatalf("streaming progress wrote %d %q, want 3 abc", written, output.String())
	}
	stalled := &progressReader{chunks: []string{"late"}, delay: 30 * time.Millisecond}
	if _, err := StreamingCopyWithDeadline(io.Discard, stalled, StreamingOptions{IdleTimeout: 5 * time.Millisecond, BufferSize: 4}); err == nil {
		t.Fatal("StreamingCopyWithDeadline(stall) succeeded, want idle timeout")
	}
}

func performRouterRequest(router *gin.Engine, method string, path string, body []byte, headers http.Header) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	for key, values := range headers {
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}
	router.ServeHTTP(recorder, request)
	return recorder
}

type progressReader struct {
	chunks []string
	delay  time.Duration
	index  int
}

func (reader *progressReader) Read(p []byte) (int, error) {
	if reader.index >= len(reader.chunks) {
		return 0, io.EOF
	}
	time.Sleep(reader.delay)
	chunk := reader.chunks[reader.index]
	reader.index++
	return copy(p, chunk), nil
}

type routerDownloadService struct{}

func (service *routerDownloadService) CreateFallback(_ context.Context, _ download.CreateRequest) (download.TaskView, error) {
	return download.TaskView{TaskID: "task_router", Status: download.StatusPending, PollURL: "/api/download/fallback/task_router?ticket=poll"}, nil
}

func (service *routerDownloadService) GetFallback(_ context.Context, _ string, _ string) (download.TaskView, bool, error) {
	return download.TaskView{TaskID: "task_router", Status: download.StatusPending, PollURL: "/api/download/fallback/task_router?ticket=poll"}, true, nil
}

func (service *routerDownloadService) CreateM3U8(_ context.Context, _ download.M3U8Request) (download.TaskView, error) {
	return download.TaskView{TaskID: "task_router", Status: download.StatusPending, PollURL: "/api/task/task_router"}, nil
}

func (service *routerDownloadService) GetM3U8(_ context.Context, _ string) (download.TaskView, bool, error) {
	return download.TaskView{TaskID: "task_router", Status: download.StatusPending, PollURL: "/api/task/task_router"}, true, nil
}

func (service *routerDownloadService) ValidateFileTicket(context.Context, string, string) error {
	return nil
}

func (service *routerDownloadService) ValidateDownloadTicket(context.Context, string, string) error {
	return nil
}

func (service *routerDownloadService) ServeTaskFile(writer http.ResponseWriter, _ *http.Request, id string) error {
	_, _ = writer.Write([]byte("file:" + id))
	return nil
}

var _ = observability.Event{}
