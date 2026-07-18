package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	parseusecase "github.com/1136623363/watermark-go/internal/parse"
	"github.com/1136623363/watermark-go/internal/task"
)

func TestParseTaskFrontendLifecycle(t *testing.T) {
	service := &fakeParseTaskService{
		submit: parseusecase.TaskView{
			TaskID:    "task_test",
			Status:    string(task.Pending),
			Progress:  0,
			PollURL:   "/api/parse/task/task_test",
			RequestID: "request-1",
		},
		polls: []parseusecase.TaskView{
			{TaskID: "task_test", Status: string(task.Pending), Progress: 0, PollURL: "/api/parse/task/task_test", RequestID: "request-1"},
			{TaskID: "task_test", Status: string(task.Running), Progress: 50, PollURL: "/api/parse/task/task_test", RequestID: "request-1"},
			{TaskID: "task_test", Status: string(task.Completed), Progress: 100, PollURL: "/api/parse/task/task_test", RequestID: "request-1", Result: &parseusecase.CompatData{Type: "video", PlayAddr: "https://cdn.example/v.mp4"}},
		},
	}
	router := newParseTaskRouter(service)

	created := postJSON(t, router, "/api/parse/task", `{"url":"https://example.com/v"}`, header("X-Request-ID", "request-1"))
	assertTaskEnvelope(t, created.Body.String(), string(task.Pending), false)
	for _, status := range []string{string(task.Pending), string(task.Running), string(task.Completed)} {
		poll := performRequest(t, router, http.MethodGet, "/api/parse/task/task_test", nil, nil)
		assertTaskEnvelope(t, poll.Body.String(), status, status == string(task.Completed))
	}
}

func TestParseTaskSubmitIsIdempotentByRequestAndClient(t *testing.T) {
	store := task.NewMemoryStore()
	service := parseusecase.NewAsyncTasks(parseusecase.AsyncTaskDependencies{
		Store:   store,
		Entropy: &sequenceReader{},
	})
	router := newParseTaskRouter(service)

	first := postJSON(t, router, "/api/parse/task", `{"url":"https://example.com/v"}`, headers(
		"X-Request-ID", "same-request",
		"X-Client-ID", "client-a",
	))
	second := postJSON(t, router, "/api/parse/task", `{"url":"https://example.com/v"}`, headers(
		"X-Request-ID", "same-request",
		"X-Client-ID", "client-a",
	))
	firstID := taskIDFromResponse(t, first.Body.String())
	secondID := taskIDFromResponse(t, second.Body.String())
	if firstID == "" || firstID != secondID {
		t.Fatalf("idempotent submit IDs = first %q second %q", firstID, secondID)
	}
	if store.Count() != 1 {
		t.Fatalf("store count = %d, want 1 idempotent task", store.Count())
	}
}

func TestParseTaskIDEntropyFailureDoesNotWrite(t *testing.T) {
	store := task.NewMemoryStore()
	service := parseusecase.NewAsyncTasks(parseusecase.AsyncTaskDependencies{
		Store:   store,
		Entropy: failingReader{},
	})
	router := newParseTaskRouter(service)

	res := postJSON(t, router, "/api/parse/task", `{"url":"https://example.com/v"}`, nil)
	assertJSONEq(t, `{"code":1001,"msg":"parse task unavailable"}`, res.Body.String())
	if store.Count() != 0 {
		t.Fatalf("store count after entropy failure = %d, want 0", store.Count())
	}
}

func newParseTaskRouter(service ParseTaskService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handlers := ParseTaskHandlers{Service: service}
	handlers.Register(router)
	return router
}

type fakeParseTaskService struct {
	submit    parseusecase.TaskView
	polls     []parseusecase.TaskView
	pollIndex int
	err       error
}

func (service *fakeParseTaskService) Submit(_ context.Context, _ parseusecase.Request, _ parseusecase.TaskMeta) (parseusecase.TaskView, error) {
	if service.err != nil {
		return parseusecase.TaskView{}, service.err
	}
	return service.submit, nil
}

func (service *fakeParseTaskService) Get(_ context.Context, _ string) (parseusecase.TaskView, bool, error) {
	if service.err != nil {
		return parseusecase.TaskView{}, false, service.err
	}
	if len(service.polls) == 0 {
		return parseusecase.TaskView{}, false, nil
	}
	index := service.pollIndex
	if index >= len(service.polls) {
		index = len(service.polls) - 1
	}
	service.pollIndex++
	return service.polls[index], true, nil
}

func assertTaskEnvelope(t *testing.T, body string, status string, wantResult bool) {
	t.Helper()
	var envelope struct {
		Code int `json:"code"`
		Data struct {
			TaskID    string           `json:"taskId"`
			Status    string           `json:"status"`
			Progress  int              `json:"progress"`
			PollURL   string           `json:"pollUrl"`
			RequestID string           `json:"requestId"`
			Result    *json.RawMessage `json:"result,omitempty"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatalf("decode task envelope: %v body=%s", err, body)
	}
	if envelope.Code != 0 || envelope.Data.TaskID == "" || envelope.Data.Status != status ||
		envelope.Data.PollURL == "" || envelope.Data.RequestID == "" {
		t.Fatalf("task envelope = %#v body=%s", envelope, body)
	}
	if wantResult && envelope.Data.Result == nil {
		t.Fatalf("completed task omitted result: %s", body)
	}
	if !wantResult && envelope.Data.Result != nil {
		t.Fatalf("non-completed task returned result: %s", body)
	}
	if strings.Contains(body, `"queued"`) {
		t.Fatalf("task envelope exposed legacy queued status: %s", body)
	}
}

func taskIDFromResponse(t *testing.T, body string) string {
	t.Helper()
	var envelope struct {
		Data struct {
			TaskID string `json:"taskId"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatalf("decode task ID: %v", err)
	}
	return envelope.Data.TaskID
}

func headers(values ...string) http.Header {
	result := make(http.Header)
	for index := 0; index+1 < len(values); index += 2 {
		result.Set(values[index], values[index+1])
	}
	return result
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, errors.New("entropy unavailable")
}
