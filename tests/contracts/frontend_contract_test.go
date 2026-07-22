package contracts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/1136623363/watermark-go/internal/auth"
	"github.com/1136623363/watermark-go/internal/download"
	"github.com/1136623363/watermark-go/internal/httpapi"
	"github.com/1136623363/watermark-go/internal/netguard"
	parseusecase "github.com/1136623363/watermark-go/internal/parse"
	coreparser "github.com/1136623363/watermark-go/internal/parser"
	"github.com/1136623363/watermark-go/internal/parser/native"
	"github.com/1136623363/watermark-go/internal/task"
)

func TestFrontendRouteAuthEnvelopeTaskAndDownloadContract(t *testing.T) {
	router := newFrontendContractRouter(t)

	login := postJSON(router, "/api/client/session", `{"clientId":"contract-client","programType":12}`, nil)
	loginEnvelope := decodeEnvelope(t, login)
	if login.Code != http.StatusOK || loginEnvelope.Code != 0 {
		t.Fatalf("client session response = %d %s", login.Code, login.Body.String())
	}
	loginData := loginEnvelope.Data.(map[string]any)
	token := stringField(t, loginData, "token")
	if token == "" || stringField(t, loginData, "uid") != "30000001" || stringField(t, loginData, "publicId") == "30000001" {
		t.Fatalf("client session data = %#v", loginData)
	}

	noToken := postJSON(router, "/api/parse", `{"url":"https://v.douyin.com/contract/"}`, nil)
	assertBusinessCode(t, noToken, http.StatusOK, 1008)

	for _, headers := range []http.Header{
		header("token", token),
		header("Authorization", "Bearer "+token),
	} {
		parsed := postJSON(router, "/api/parse", `{"url":"https://v.douyin.com/gallery-contract/"}`, headers)
		data := assertBusinessCode(t, parsed, http.StatusOK, 0).Data.(map[string]any)
		assertRichMediaProjection(t, data)
	}

	foreignProtocol := postJSON(router, "/api/parse", `{"text":"https://v.douyin.com/contract/"}`, header("token", token))
	foreignBody := foreignProtocol.Body.String()
	if foreignProtocol.Code != http.StatusOK || strings.Contains(foreignBody, `"code":0`) ||
		strings.Contains(foreignBody, "retcode") || strings.Contains(foreignBody, "retdesc") || strings.Contains(foreignBody, "succ") {
		t.Fatalf("foreign media-parser protocol was accepted or leaked: %s", foreignBody)
	}

	cache := perform(router, http.MethodGet, "/api/parse/cache/share_contract", nil, nil)
	cacheData := assertBusinessCode(t, cache, http.StatusOK, 0).Data.(map[string]any)
	if cacheData["sourceUrl"] != "https://v.douyin.com/gallery-contract/" {
		t.Fatalf("cache data = %#v", cacheData)
	}
	missingCache := perform(router, http.MethodGet, "/api/parse/cache/guess", nil, nil)
	assertBusinessCode(t, missingCache, http.StatusOK, 1004)

	taskCreated := postJSON(router, "/api/parse/task", `{"url":"https://v.douyin.com/contract/"}`, nil)
	taskData := assertBusinessCode(t, taskCreated, http.StatusOK, 0).Data.(map[string]any)
	taskID := stringField(t, taskData, "taskId")
	if len(taskID) < 24 || stringField(t, taskData, "pollUrl") == "" {
		t.Fatalf("parse task data = %#v", taskData)
	}
	taskPoll := perform(router, http.MethodGet, "/api/parse/task/"+taskID, nil, nil)
	if status := stringField(t, assertBusinessCode(t, taskPoll, http.StatusOK, 0).Data.(map[string]any), "status"); status != "pending" {
		t.Fatalf("parse task status = %q", status)
	}
	assertBusinessCode(t, perform(router, http.MethodGet, "/api/parse/task/guess", nil, nil), http.StatusOK, 1004)

	fallbackCreated := postJSON(router, "/api/download/fallback", `{"mediaUrl":"https://cdn.example/video.mp4","mediaType":"video","attempt":4}`, nil)
	fallbackData := assertBusinessCode(t, fallbackCreated, http.StatusOK, 0).Data.(map[string]any)
	downloadURL := stringField(t, fallbackData, "downloadUrl")
	if !strings.HasPrefix(downloadURL, "https://watermark.bxsn.cn/api/download/file/") {
		t.Fatalf("fallback downloadUrl is not absolute same-domain HTTPS: %#v", fallbackData)
	}
	assertBusinessCode(t, perform(router, http.MethodGet, "/api/download/fallback/fallback_contract?ticket=poll", nil, nil), http.StatusOK, 0)
	assertBusinessCode(t, perform(router, http.MethodGet, "/api/download/fallback/fallback_contract?ticket=bad", nil, nil), http.StatusOK, 1008)
	assertBusinessCode(t, perform(router, http.MethodGet, "/api/download/file/fallback_contract", nil, nil), http.StatusForbidden, 1008)
	fallbackFile := perform(router, http.MethodGet, "/api/download/file/fallback_contract?ticket=download", nil, nil)
	if fallbackFile.Code != http.StatusOK || fallbackFile.Body.String() != "file:fallback_contract" {
		t.Fatalf("fallback file response = %d %q", fallbackFile.Code, fallbackFile.Body.String())
	}

	m3u8Created := perform(router, http.MethodGet, "/api/m3u8/merge?url=https://example.com/live.m3u8", nil, nil)
	m3u8Data := assertBusinessCode(t, m3u8Created, http.StatusOK, 0).Data.(map[string]any)
	if stringField(t, m3u8Data, "pollUrl") != "/api/task/m3u8_contract" {
		t.Fatalf("m3u8 create data = %#v", m3u8Data)
	}
	m3u8Poll := perform(router, http.MethodGet, "/api/task/m3u8_contract", nil, nil)
	doneData := assertBusinessCode(t, m3u8Poll, http.StatusOK, 0).Data.(map[string]any)
	if stringField(t, doneData, "status") != "done" || !strings.HasPrefix(stringField(t, doneData, "url"), "https://watermark.bxsn.cn/api/task/file/") {
		t.Fatalf("m3u8 done data = %#v", doneData)
	}
	assertBusinessCode(t, perform(router, http.MethodGet, "/api/task/file/m3u8_contract", nil, nil), http.StatusForbidden, 1008)
	m3u8File := perform(router, http.MethodGet, "/api/task/file/m3u8_contract?ticket=file", nil, nil)
	if m3u8File.Code != http.StatusOK || m3u8File.Body.String() != "file:m3u8_contract" {
		t.Fatalf("m3u8 file response = %d %q", m3u8File.Code, m3u8File.Body.String())
	}

	performance := postJSON(router, "/api/client/performance", `{"name":"frontend"}`, nil)
	assertBusinessCode(t, performance, http.StatusOK, 0)

	health := perform(router, http.MethodGet, "/healthz", nil, nil)
	healthData := assertBusinessCode(t, health, http.StatusOK, 0).Data.(map[string]any)
	if _, ok := healthData["node"]; ok {
		t.Fatalf("health leaked node topology: %#v", healthData)
	}
	forbiddenTopology := "clu" + "ster"
	if _, ok := healthData[forbiddenTopology]; ok {
		t.Fatalf("health leaked distributed topology: %#v", healthData)
	}
	if perform(router, http.MethodGet, "/api/download/node/main/file/video.mp4", nil, nil).Code != http.StatusNotFound {
		t.Fatal("cross-node download route must not be registered")
	}
	if perform(router, http.MethodPost, "/internal/platform-test", nil, nil).Code != http.StatusNotFound {
		t.Fatal("internal platform route must not be registered")
	}
}

func TestMediaParserIntegrationContract(t *testing.T) {
	t.Run("mediaParserIntegration", func(t *testing.T) {
		registry, err := coreparser.NewRegistry(native.Descriptors())
		if err != nil {
			t.Fatalf("registry: %v", err)
		}
		var golden coreparser.Catalog
		readJSONFile(t, filepath.Join("..", "..", "internal", "parser", "native", "testdata", "catalog.golden.json"), &golden)
		if snapshot := registry.CatalogSnapshot(); !reflect.DeepEqual(snapshot, golden) {
			t.Fatalf("registry catalog drifted from golden")
		}

		douyin, ok := registry.Descriptor(coreparser.PlatformKey("douyin"))
		if !ok {
			t.Fatal("douyin descriptor missing")
		}
		normalized, err := coreparser.NormalizeFetchURL(douyin, "https://www.douyin.com/video/1?utm_source=x&modal_id=2&modal_id=2&TOKEN=opaque#frag")
		if err != nil {
			t.Fatalf("normalize query: %v", err)
		}
		var safeURL string
		if err := normalized.Use(func(value string) error {
			safeURL = value
			return nil
		}); err != nil {
			t.Fatalf("use normalized URL: %v", err)
		}
		if safeURL != "https://www.douyin.com/video/1?modal_id=2" {
			t.Fatalf("normalized query = %q", safeURL)
		}

		candidates := []coreparser.MediaCandidate{
			{URL: "https://media.example/no-metadata.mp4", Kind: coreparser.MediaKindVideo, SourceRank: 0},
			{URL: "https://media.example/720.mp4", Kind: coreparser.MediaKindVideo, Quality: 720, Width: 1280, Height: 720, SourceRank: 2},
			{URL: "https://media.example/1080.mp4", Kind: coreparser.MediaKindVideo, Quality: 1080, Width: 1920, Height: 1080, SourceRank: 1},
		}
		coreparser.SortMediaCandidates(candidates)
		if candidates[0].Quality != 1080 || candidates[1].Quality != 720 || candidates[2].Quality != 0 {
			t.Fatalf("candidate ranking = %#v", candidates)
		}
		budget, err := coreparser.NewRequestBudget(coreparser.BudgetOptions{MaxRequests: 2, MaxRedirects: 0, Duration: time.Minute})
		if err != nil {
			t.Fatalf("budget: %v", err)
		}
		attempts := 0
		_, err = coreparser.AttemptMediaCandidates(context.Background(), candidates, budget, func(context.Context, coreparser.MediaCandidate, netguard.FetchURL) error {
			attempts++
			return errors.New("synthetic candidate failure")
		})
		if !errors.Is(err, coreparser.ErrBudgetExceeded) || attempts != 2 {
			t.Fatalf("bounded candidate fallback attempts=%d error=%v", attempts, err)
		}

		normalGallery := parseusecase.Normalize(parseusecase.Result{
			Platform: "xiaohongshu",
			Images:   []parseusecase.ImageAsset{{URL: "https://cdn.example/static.jpg"}},
		})
		if len(normalGallery.Images) != 1 || len(normalGallery.ImageAssets) != 0 {
			t.Fatalf("ordinary gallery projection = %#v", normalGallery)
		}
		livePhoto := parseusecase.Normalize(parseusecase.Result{
			Platform: "xiaohongshu",
			AudioURL: "https://cdn.example/audio.mp3",
			Images: []parseusecase.ImageAsset{{
				URL:          "https://cdn.example/static.jpg",
				LivePhotoURL: "https://cdn.example/live.mp4",
			}},
		})
		if livePhoto.Images[0] != livePhoto.Pics[0] || livePhoto.ImageAssets[0].URL != livePhoto.Images[0] ||
			livePhoto.ImageAssets[0].LivePhotoURL == "" || livePhoto.Music != livePhoto.MP3 || livePhoto.Music != livePhoto.AudioURL {
			t.Fatalf("live photo/audio projection = %#v", livePhoto)
		}
		for _, image := range livePhoto.ImageAssets {
			if _, err := netguard.NewFetchURL(image.URL); err != nil {
				t.Fatalf("static image failed netguard validation: %v", err)
			}
			if _, err := netguard.NewFetchURL(image.LivePhotoURL); err != nil {
				t.Fatalf("live photo failed netguard validation: %v", err)
			}
		}

		v1, err := parseusecase.NewCacheIdentity(parseusecase.CacheIdentityParts{
			Platform: "douyin", CanonicalResourceURL: "https://v.douyin.com/contract/", ParserVersion: "parser-v1", ResultSchemaVersion: "schema-v1",
		})
		if err != nil {
			t.Fatalf("cache identity v1: %v", err)
		}
		v2, err := parseusecase.NewCacheIdentity(parseusecase.CacheIdentityParts{
			Platform: "douyin", CanonicalResourceURL: "https://v.douyin.com/contract/", ParserVersion: "parser-v2", ResultSchemaVersion: "schema-v1",
		})
		if err != nil {
			t.Fatalf("cache identity v2: %v", err)
		}
		if v1.Key == v2.Key || !parseusecase.NegativeCacheable(parseusecase.ErrorUnsupported) ||
			parseusecase.NegativeCacheable(parseusecase.ErrorUpstreamTimeout) {
			t.Fatalf("cache/version/negative semantics drifted: v1=%s v2=%s", v1.Key, v2.Key)
		}

		for _, unsafe := range []string{"data:text/plain,hello", "file:///etc/passwd"} {
			if _, err := netguard.NewFetchURL(unsafe); err == nil {
				t.Fatalf("unsafe URL was accepted: %s", unsafe)
			}
		}
	})
}

func TestFrontendProvenanceGuardScript(t *testing.T) {
	script := filepath.Join("..", "..", "scripts", "verify-frontend-provenance.sh")
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("frontend provenance guard is missing: %v", err)
	}
	command := exec.Command("bash", script)
	frontendRepo := os.Getenv("FRONTEND_REPO")
	if frontendRepo == "" {
		frontendRepo = "/srv/watermark"
	}
	command.Env = append(os.Environ(), "FRONTEND_REPO="+frontendRepo)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("frontend provenance guard failed: %v\n%s", err, output)
	}
}

func newFrontendContractRouter(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	authService, err := auth.NewService(auth.ServiceOptions{
		Environment: "test",
		Store:       auth.NewMemoryStore(),
		Entropy:     &sequenceReader{},
		Clock:       func() time.Time { return time.Unix(1_700_000_000, 0) },
	})
	if err != nil {
		t.Fatalf("auth service: %v", err)
	}
	parseService := &contractParseService{}
	return httpapi.Router(httpapi.RouterOptions{
		Client: httpapi.ClientHandlers{
			Auth:  authService,
			Parse: authenticatedParse(parseService),
		},
		Parse:      httpapi.ParseHandlers{Service: parseService},
		ParseTasks: httpapi.ParseTaskHandlers{Service: parseusecase.NewAsyncTasks(parseusecase.AsyncTaskDependencies{Store: task.NewMemoryStore(), Entropy: &sequenceReader{}})},
		Download:   httpapi.DownloadHandlers{Service: &contractDownloadService{}},
	})
}

func authenticatedParse(service *contractParseService) httpapi.ParseFunc {
	return func(ctx context.Context, _ auth.AuthenticatedClient, request httpapi.ParseRequest) (any, error) {
		output, err := service.Parse(ctx, parseusecase.Request{
			URL:          request.URL,
			ForceRefresh: request.ForceRefresh,
			Source:       request.Source,
			Timestamp:    request.Timestamp,
			Signature:    request.Signature,
			Version:      request.Version,
		})
		if err != nil {
			return nil, err
		}
		return output.Data, nil
	}
}

type contractParseService struct{}

func (service *contractParseService) Parse(_ context.Context, request parseusecase.Request) (parseusecase.ParseOutput, error) {
	if strings.TrimSpace(request.URL) == "" {
		return parseusecase.ParseOutput{}, parseusecase.NewError(parseusecase.ErrorInvalidInput, parseusecase.StageInput, "", false)
	}
	result := parseusecase.Result{
		Platform: "douyin",
		Type:     "video",
		Title:    "contract",
		VideoURL: "https://cdn.example/video.mp4",
		AudioURL: "https://cdn.example/audio.mp3",
	}
	if strings.Contains(request.URL, "gallery") {
		result.Type = "gallery"
		result.VideoURL = ""
		result.Images = []parseusecase.ImageAsset{{
			URL:          "https://cdn.example/static.jpg",
			LivePhotoURL: "https://cdn.example/live.mp4",
		}}
	}
	data := parseusecase.Normalize(result)
	data.ShareID = "share_contract"
	data.SourceURL = request.URL
	return parseusecase.ParseOutput{Result: result, Data: data}, nil
}

func (service *contractParseService) GetCached(_ context.Context, shareID string) (parseusecase.CompatData, bool, error) {
	if shareID != "share_contract" {
		return parseusecase.CompatData{}, false, nil
	}
	data := parseusecase.Normalize(parseusecase.Result{
		Platform: "douyin",
		Type:     "gallery",
		Images: []parseusecase.ImageAsset{{
			URL:          "https://cdn.example/static.jpg",
			LivePhotoURL: "https://cdn.example/live.mp4",
		}},
	})
	data.ShareID = shareID
	data.SourceURL = "https://v.douyin.com/gallery-contract/"
	return data, true, nil
}

func (service *contractParseService) ParseID(context.Context, parseusecase.IDRequest) (parseusecase.ParseOutput, error) {
	return parseusecase.ParseOutput{}, parseusecase.NewError(parseusecase.ErrorUnsupported, parseusecase.StageInput, "", false)
}

type contractDownloadService struct{}

func (service *contractDownloadService) CreateFallback(_ context.Context, request download.CreateRequest) (download.TaskView, error) {
	if request.Attempt < 4 {
		return download.TaskView{}, download.ErrAttemptTooEarly
	}
	if _, err := netguard.NewFetchURL(request.MediaURL); err != nil {
		return download.TaskView{}, download.ErrUnsafeTarget
	}
	return download.TaskView{
		TaskID:      "fallback_contract",
		Status:      download.StatusCompleted,
		Progress:    100,
		PollURL:     "https://watermark.bxsn.cn/api/download/fallback/fallback_contract?ticket=poll",
		DownloadURL: "https://watermark.bxsn.cn/api/download/file/fallback_contract?ticket=download",
	}, nil
}

func (service *contractDownloadService) GetFallback(_ context.Context, id string, ticket string) (download.TaskView, bool, error) {
	if id != "fallback_contract" {
		return download.TaskView{}, false, nil
	}
	if ticket != "poll" {
		return download.TaskView{}, false, download.ErrInvalidTicket
	}
	return download.TaskView{
		TaskID:      id,
		Status:      download.StatusCompleted,
		Progress:    100,
		DownloadURL: "https://watermark.bxsn.cn/api/download/file/fallback_contract?ticket=download",
	}, true, nil
}

func (service *contractDownloadService) CreateM3U8(_ context.Context, request download.M3U8Request) (download.TaskView, error) {
	if _, err := netguard.NewFetchURL(request.URL); err != nil {
		return download.TaskView{}, download.ErrUnsafeTarget
	}
	return download.TaskView{TaskID: "m3u8_contract", Status: download.StatusPending, PollURL: "/api/task/m3u8_contract"}, nil
}

func (service *contractDownloadService) GetM3U8(_ context.Context, id string) (download.TaskView, bool, error) {
	if id != "m3u8_contract" {
		return download.TaskView{}, false, nil
	}
	return download.TaskView{
		TaskID:   id,
		Status:   download.StatusCompleted,
		Progress: 100,
		FileURL:  "https://watermark.bxsn.cn/api/task/file/m3u8_contract?ticket=file",
	}, true, nil
}

func (service *contractDownloadService) ValidateDownloadTicket(_ context.Context, id string, ticket string) error {
	if id != "fallback_contract" || ticket != "download" {
		return download.ErrInvalidTicket
	}
	return nil
}

func (service *contractDownloadService) ValidateFileTicket(_ context.Context, id string, ticket string) error {
	if id != "m3u8_contract" || ticket != "file" {
		return download.ErrInvalidTicket
	}
	return nil
}

func (service *contractDownloadService) ServeTaskFile(writer http.ResponseWriter, _ *http.Request, id string) error {
	if id == "" {
		return download.ErrTaskNotFound
	}
	_, _ = writer.Write([]byte("file:" + id))
	return nil
}

type sequenceReader struct{ next byte }

func (reader *sequenceReader) Read(p []byte) (int, error) {
	for index := range p {
		reader.next++
		p[index] = reader.next
	}
	return len(p), nil
}

type envelope struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data any    `json:"data"`
}

func postJSON(router http.Handler, path string, body string, headers http.Header) *httptest.ResponseRecorder {
	return perform(router, http.MethodPost, path, []byte(body), withContentType(headers))
}

func perform(router http.Handler, method string, path string, body []byte, headers http.Header) *httptest.ResponseRecorder {
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

func withContentType(headers http.Header) http.Header {
	if headers == nil {
		headers = make(http.Header)
	}
	headers.Set("Content-Type", "application/json")
	return headers
}

func header(key string, value string) http.Header {
	headers := make(http.Header)
	headers.Set(key, value)
	return headers
}

func decodeEnvelope(t *testing.T, recorder *httptest.ResponseRecorder) envelope {
	t.Helper()
	var body envelope
	decoder := json.NewDecoder(recorder.Body)
	decoder.UseNumber()
	if err := decoder.Decode(&body); err != nil {
		t.Fatalf("decode envelope: %v body=%s", err, recorder.Body.String())
	}
	return body
}

func assertBusinessCode(t *testing.T, recorder *httptest.ResponseRecorder, status int, code int) envelope {
	t.Helper()
	if recorder.Code != status {
		t.Fatalf("HTTP status = %d body=%s, want %d", recorder.Code, recorder.Body.String(), status)
	}
	body := decodeEnvelope(t, recorder)
	if body.Code != code {
		t.Fatalf("business code = %d body=%s, want %d", body.Code, recorder.Body.String(), code)
	}
	return body
}

func assertRichMediaProjection(t *testing.T, data map[string]any) {
	t.Helper()
	if data["music"] != data["mp3"] || data["music"] != data["audioUrl"] {
		t.Fatalf("audio aliases drifted: %#v", data)
	}
	images, ok := data["images"].([]any)
	if !ok || len(images) != 1 {
		t.Fatalf("images projection = %#v", data["images"])
	}
	if _, ok := images[0].(string); !ok {
		t.Fatalf("images must remain string URLs: %#v", images)
	}
	assets, ok := data["imageAssets"].([]any)
	if !ok || len(assets) != 1 {
		t.Fatalf("imageAssets projection = %#v", data["imageAssets"])
	}
	first, ok := assets[0].(map[string]any)
	if !ok || first["url"] != images[0] || first["livePhotoUrl"] == "" {
		t.Fatalf("live photo asset pairing drifted: %#v", assets)
	}
}

func stringField(t *testing.T, data map[string]any, field string) string {
	t.Helper()
	value, ok := data[field].(string)
	if !ok {
		t.Fatalf("field %s is not string in %#v", field, data)
	}
	return value
}

func readJSONFile(t *testing.T, path string, target any) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() {
		_ = file.Close()
	}()
	if err := json.NewDecoder(file).Decode(target); err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("decode %s: %v", path, err)
	}
}
