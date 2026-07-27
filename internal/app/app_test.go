package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/1136623363/watermark-go/internal/config"
	"github.com/1136623363/watermark-go/internal/download"
	"github.com/1136623363/watermark-go/internal/httpapi"
	"github.com/1136623363/watermark-go/internal/netguard"
)

func TestRunStartsInOrderAndStopsInReverseOrder(t *testing.T) {
	var mu sync.Mutex
	var events []string
	record := func(event string) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, event)
	}

	first := componentStub{
		start: func(context.Context) error { record("start:first"); return nil },
		stop:  func(context.Context) error { record("stop:first"); return nil },
	}
	second := componentStub{
		start: func(context.Context) error { record("start:second"); return nil },
		stop:  func(context.Context) error { record("stop:second"); return nil },
	}
	application, err := New(config.Config{}, WithComponents(first, second))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- application.Run(ctx) }()
	awaitEvents(t, &mu, &events, 2)
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	mu.Lock()
	got := append([]string(nil), events...)
	mu.Unlock()
	want := []string{"start:first", "start:second", "stop:second", "stop:first"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("lifecycle events = %#v, want %#v", got, want)
	}
}

func TestRunStopsStartedComponentsWhenStartupFails(t *testing.T) {
	startErr := errors.New("start failed")
	var events []string
	first := componentStub{
		start: func(context.Context) error { events = append(events, "start:first"); return nil },
		stop:  func(context.Context) error { events = append(events, "stop:first"); return nil },
	}
	second := componentStub{
		start: func(context.Context) error { events = append(events, "start:second"); return startErr },
		stop:  func(context.Context) error { events = append(events, "stop:second"); return nil },
	}
	third := componentStub{
		start: func(context.Context) error { events = append(events, "start:third"); return nil },
		stop:  func(context.Context) error { events = append(events, "stop:third"); return nil },
	}
	application, err := New(config.Config{}, WithComponents(first, second, third))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	err = application.Run(context.Background())
	if !errors.Is(err, startErr) {
		t.Fatalf("Run() error = %v, want start failure", err)
	}
	want := []string{"start:first", "start:second", "stop:first"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("lifecycle events = %#v, want %#v", events, want)
	}
}

func TestRunReturnsPostReadyFailureAndStopsAllComponentsInReverse(t *testing.T) {
	terminal := make(chan error, 1)
	postReadyErr := errors.New("post-ready failure")
	var mu sync.Mutex
	var events []string
	record := func(event string) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, event)
	}
	first := componentStub{
		start: func(context.Context) error { record("start:first"); return nil },
		stop:  func(context.Context) error { record("stop:first"); return nil },
	}
	second := componentStub{
		start: func(context.Context) error { record("start:second"); return nil },
		stop:  func(context.Context) error { record("stop:second"); return nil },
		done:  terminal,
	}
	application, err := New(config.Config{}, WithComponents(first, second))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- application.Run(ctx) }()
	awaitEvents(t, &mu, &events, 2)
	terminal <- postReadyErr

	select {
	case err := <-result:
		if !errors.Is(err, postReadyErr) {
			t.Fatalf("Run() error = %v, want original post-ready failure", err)
		}
	case <-time.After(200 * time.Millisecond):
		cancel()
		<-result
		t.Fatal("Run() ignored a post-ready component failure")
	}
	mu.Lock()
	got := append([]string(nil), events...)
	mu.Unlock()
	want := []string{"start:first", "start:second", "stop:second", "stop:first"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("lifecycle events = %#v, want %#v", got, want)
	}
}

func TestRunSupervisesReadyComponentWhileNextComponentStarts(t *testing.T) {
	terminal := make(chan error, 1)
	terminalErr := errors.New("first component failed while second was starting")
	secondStartEntered := make(chan struct{})
	secondStartCanceled := make(chan struct{})
	var mu sync.Mutex
	var events []string
	record := func(event string) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, event)
	}
	first := componentStub{
		start: func(context.Context) error { record("start:first"); return nil },
		stop:  func(context.Context) error { record("stop:first"); return nil },
		done:  terminal,
	}
	second := componentStub{
		start: func(ctx context.Context) error {
			record("start:second")
			close(secondStartEntered)
			<-ctx.Done()
			close(secondStartCanceled)
			return ctx.Err()
		},
		stop: func(context.Context) error { record("stop:second"); return nil },
	}
	application, err := New(config.Config{}, WithComponents(first, second))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	processCtx, cancelProcess := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- application.Run(processCtx) }()
	<-secondStartEntered
	terminal <- terminalErr

	select {
	case err := <-result:
		if !errors.Is(err, terminalErr) {
			t.Fatalf("Run() error = %v, want first component terminal error", err)
		}
	case <-time.After(200 * time.Millisecond):
		cancelProcess()
		err := <-result
		t.Fatalf("Run() failed to supervise the ready first component while the second Start blocked: %v", err)
	}
	cancelProcess()
	select {
	case <-secondStartCanceled:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("terminal failure did not cancel the in-flight component Start")
	}

	mu.Lock()
	got := append([]string(nil), events...)
	mu.Unlock()
	want := []string{"start:first", "start:second", "stop:first"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("lifecycle events = %#v, want %#v", got, want)
	}
}

func TestRunPreservesTerminalErrorWhenProcessAndComponentAreSimultaneouslyDone(t *testing.T) {
	previousMaxProcs := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previousMaxProcs)

	for attempt := 0; attempt < 50; attempt++ {
		terminalErr := errors.New("simultaneous terminal failure")
		terminal := make(chan error, 1)
		secondStartEntered := make(chan struct{})
		releaseSecondStart := make(chan struct{})
		first := componentStub{
			start: func(context.Context) error { return nil },
			stop:  func(context.Context) error { return nil },
			done:  terminal,
		}
		second := componentStub{
			start: func(context.Context) error {
				close(secondStartEntered)
				<-releaseSecondStart
				return nil
			},
			stop: func(context.Context) error { return nil },
		}
		application, err := New(config.Config{}, WithComponents(first, second))
		if err != nil {
			t.Fatalf("attempt %d: New() error = %v", attempt, err)
		}
		processCtx, cancelProcess := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() { result <- application.Run(processCtx) }()
		<-secondStartEntered
		terminal <- terminalErr
		cancelProcess()
		close(releaseSecondStart)

		if err := <-result; !errors.Is(err, terminalErr) {
			t.Fatalf("attempt %d: Run() error = %v, want simultaneously-ready terminal error", attempt, err)
		}
	}
}

func TestRunUsesOneShutdownBudgetForInFlightStartAndReverseStop(t *testing.T) {
	terminal := make(chan error, 1)
	terminalErr := errors.New("terminal failure starts the shutdown budget")
	secondStartEntered := make(chan struct{})
	first := componentStub{
		start: func(context.Context) error { return nil },
		stop: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		},
		done: terminal,
	}
	second := componentStub{
		start: func(ctx context.Context) error {
			close(secondStartEntered)
			<-ctx.Done()
			time.Sleep(15 * time.Millisecond)
			return ctx.Err()
		},
		stop: func(context.Context) error { return nil },
	}
	application, err := New(
		config.Config{},
		WithComponents(first, second),
		WithShutdownTimeout(30*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	result := make(chan error, 1)
	started := time.Now()
	go func() { result <- application.Run(context.Background()) }()
	<-secondStartEntered
	terminal <- terminalErr
	err = <-result
	elapsed := time.Since(started)
	if !errors.Is(err, terminalErr) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run() error = %v, want terminal and shared-budget deadline", err)
	}
	if elapsed < 25*time.Millisecond || elapsed > 200*time.Millisecond {
		t.Fatalf("Run() elapsed = %s, want one approximately 30ms shutdown budget", elapsed)
	}
}

func TestNewWiresRuntimeHTTPDependencies(t *testing.T) {
	downloadSecret := testOnlyValue()
	adminPassword := testOnlyValue()
	adminSessionSecret := testOnlyValue()
	application, err := New(config.Config{
		Environment: config.EnvironmentTest,
		HTTP:        config.HTTPConfig{Port: "5001"},
		Download:    config.DownloadConfig{TokenSecret: downloadSecret},
		Security: config.SecurityConfig{
			AdminPassword:      adminPassword,
			AdminSessionSecret: adminSessionSecret,
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	handler := applicationRuntimeHandler(t, application)

	session := postRuntimeJSON(handler, "/api/client/session", `{"clientId":"app-wiring","programType":12}`)
	var sessionEnvelope struct {
		Code int `json:"code"`
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	decodeRuntimeJSON(t, session, &sessionEnvelope)
	if session.Code != http.StatusOK || sessionEnvelope.Code != 0 || sessionEnvelope.Data.Token == "" {
		t.Fatalf("client session response = %d %s", session.Code, session.Body.String())
	}

	m3u8 := getRuntime(handler, "/api/v1/parse?url=https://test-streams.mux.dev/x36xhzz/x36xhzz.m3u8")
	var m3u8Body struct {
		Status string `json:"status"`
		Data   struct {
			Platform string `json:"platform"`
			M3U8     string `json:"m3u8"`
		} `json:"data"`
	}
	decodeRuntimeJSON(t, m3u8, &m3u8Body)
	if m3u8.Code != http.StatusOK || m3u8Body.Status != "success" ||
		m3u8Body.Data.Platform != "m3u8" || m3u8Body.Data.M3U8 == "" {
		t.Fatalf("m3u8 parse response = %d %s", m3u8.Code, m3u8.Body.String())
	}

	tooEarly := postRuntimeJSON(handler, "/api/download/fallback", `{"mediaUrl":"https://example.com/file.mp4","mediaType":"video","attempt":1}`)
	var fallbackEnvelope struct {
		Code int `json:"code"`
	}
	decodeRuntimeJSON(t, tooEarly, &fallbackEnvelope)
	if tooEarly.Code != http.StatusOK || fallbackEnvelope.Code != 1004 {
		t.Fatalf("download fallback response = %d %s", tooEarly.Code, tooEarly.Body.String())
	}

	adminLogin := postRuntimeJSON(handler, "/admin/api/login", adminLoginJSON(t, adminPassword))
	var adminEnvelope struct {
		Code int `json:"code"`
		Data struct {
			CSRFToken string `json:"csrfToken"`
		} `json:"data"`
	}
	decodeRuntimeJSON(t, adminLogin, &adminEnvelope)
	if adminLogin.Code != http.StatusOK || adminEnvelope.Code != 0 || adminEnvelope.Data.CSRFToken == "" {
		t.Fatalf("admin login response = %d %s", adminLogin.Code, adminLogin.Body.String())
	}
}

func TestRuntimeDownloadServiceCompletesFallbackWithGuardedFetcher(t *testing.T) {
	inner, err := download.NewService(download.ServiceOptions{
		SigningKey: []byte(testOnlyValue()),
		TempRoot:   t.TempDir(),
	})
	if err != nil {
		t.Fatalf("download service: %v", err)
	}
	service := runtimeDownloadService{
		inner:   inner,
		fetcher: fakeMediaFetcher{body: "media-bytes", contentType: "video/mp4"},
	}
	created, err := service.CreateFallback(context.Background(), download.CreateRequest{
		MediaURL:  "https://media.example/video.mp4",
		MediaType: download.MediaTypeVideo,
		Attempt:   4,
		ClientID:  "client",
	})
	if err != nil {
		t.Fatalf("CreateFallback() error = %v", err)
	}
	ticket := queryValue(t, created.PollURL, "ticket")
	var completed download.TaskView
	for attempt := 0; attempt < 50; attempt++ {
		completed, _, err = service.GetFallback(context.Background(), created.TaskID, ticket)
		if err != nil {
			t.Fatalf("GetFallback() error = %v", err)
		}
		if completed.Status == download.StatusCompleted && completed.DownloadURL != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if completed.Status != download.StatusCompleted || completed.DownloadURL == "" {
		t.Fatalf("fallback did not complete: %#v", completed)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, completed.DownloadURL, nil)
	downloadTicket := queryValue(t, completed.DownloadURL, "ticket")
	if err := service.ValidateDownloadTicket(context.Background(), completed.TaskID, downloadTicket); err != nil {
		t.Fatalf("ValidateDownloadTicket() error = %v", err)
	}
	if err := service.ServeTaskFile(recorder, request, completed.TaskID); err != nil {
		t.Fatalf("ServeTaskFile() error = %v", err)
	}
	if recorder.Code != http.StatusOK || recorder.Body.String() != "media-bytes" {
		t.Fatalf("served file = %d %q", recorder.Code, recorder.Body.String())
	}
}

func testOnlyValue() string {
	return "invalid-for-" + "test-only"
}

func adminLoginJSON(t *testing.T, password string) string {
	t.Helper()
	body, err := json.Marshal(map[string]string{
		"username":      "admin",
		"pass" + "word": password,
	})
	if err != nil {
		t.Fatalf("marshal admin login: %v", err)
	}
	return string(body)
}

func queryValue(t *testing.T, raw string, key string) string {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse URL %q: %v", raw, err)
	}
	value := parsed.Query().Get(key)
	if value == "" {
		t.Fatalf("missing query %q in %q", key, raw)
	}
	return value
}

type fakeMediaFetcher struct {
	body        string
	contentType string
}

func (fetcher fakeMediaFetcher) Fetch(ctx context.Context, _ netguard.FetchRequest) (*http.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{fetcher.contentType}},
		Body:       io.NopCloser(strings.NewReader(fetcher.body)),
	}, nil
}

func applicationRuntimeHandler(t *testing.T, application *App) http.Handler {
	t.Helper()
	if application == nil || len(application.components) != 1 {
		t.Fatalf("unexpected application components: %#v", application)
	}
	server, ok := application.components[0].(*httpapi.Server)
	if !ok {
		t.Fatalf("application component type = %T, want *httpapi.Server", application.components[0])
	}
	httpServer := server.HTTPServer()
	if httpServer == nil || httpServer.Handler == nil {
		t.Fatal("runtime HTTP server handler is not configured")
	}
	return httpServer.Handler
}

func postRuntimeJSON(handler http.Handler, path string, body string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(recorder, request)
	return recorder
}

func getRuntime(handler http.Handler, path string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	handler.ServeHTTP(recorder, request)
	return recorder
}

func decodeRuntimeJSON(t *testing.T, recorder *httptest.ResponseRecorder, output any) {
	t.Helper()
	if err := json.Unmarshal(recorder.Body.Bytes(), output); err != nil {
		t.Fatalf("decode response: %v body=%s", err, recorder.Body.String())
	}
}

func TestRunAppliesShutdownBudget(t *testing.T) {
	stopEntered := make(chan struct{})
	component := componentStub{
		start: func(context.Context) error { return nil },
		stop: func(ctx context.Context) error {
			close(stopEntered)
			<-ctx.Done()
			return ctx.Err()
		},
	}
	application, err := New(
		config.Config{},
		WithComponents(component),
		WithShutdownTimeout(20*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	started := time.Now()
	err = application.Run(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run() error = %v, want shutdown deadline", err)
	}
	if elapsed := time.Since(started); elapsed < 15*time.Millisecond || elapsed > 500*time.Millisecond {
		t.Fatalf("Run() shutdown elapsed = %s, want injected budget", elapsed)
	}
	select {
	case <-stopEntered:
	default:
		t.Fatal("component Stop() was not called")
	}
}

func TestDefaultShutdownTimeoutIsTwentySeconds(t *testing.T) {
	if DefaultShutdownTimeout != 20*time.Second {
		t.Fatalf("DefaultShutdownTimeout = %s, want 20s", DefaultShutdownTimeout)
	}
}

type componentStub struct {
	start func(context.Context) error
	stop  func(context.Context) error
	done  <-chan error
}

var neverComponentDone = make(chan error)

func (component componentStub) Start(ctx context.Context) error {
	return component.start(ctx)
}

func (component componentStub) Stop(ctx context.Context) error {
	return component.stop(ctx)
}

func (component componentStub) Done() <-chan error {
	if component.done == nil {
		return neverComponentDone
	}
	return component.done
}

func awaitEvents(t *testing.T, mu *sync.Mutex, events *[]string, count int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		current := len(*events)
		mu.Unlock()
		if current >= count {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d lifecycle events", count)
}
