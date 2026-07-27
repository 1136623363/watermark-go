package download

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/1136623363/watermark-go/internal/netguard"
)

type MediaType string

const (
	MediaTypeVideo MediaType = "video"
	MediaTypeAudio MediaType = "audio"
	MediaTypeImage MediaType = "image"
)

type Status string

const (
	StatusPending   Status = "pending"
	StatusRunning   Status = "running"
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
)

var (
	ErrAttemptTooEarly    = errors.New("download fallback attempt is too early")
	ErrUnsafeTarget       = errors.New("unsafe download target")
	ErrConcurrencyLimit   = errors.New("download concurrency limit exceeded")
	ErrUnsafePath         = errors.New("unsafe download path")
	ErrURLBuild           = errors.New("download url build failed")
	ErrTaskNotFound       = errors.New("download task not found")
	ErrEntropyUnavailable = errors.New("download entropy unavailable")
	ErrStreamIdleTimeout  = errors.New("download stream idle timeout")
	ErrMediaSizeExceeded  = errors.New("download media size exceeded")
)

const (
	defaultTTL                  = 30 * time.Minute
	defaultGlobalConcurrency    = 2
	defaultPerClientConcurrency = 1
)

var safeTaskIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)

type ServiceOptions struct {
	SigningKey              []byte
	Entropy                 io.Reader
	TempRoot                string
	PublicBaseURL           string
	Clock                   func() time.Time
	TTL                     time.Duration
	MaxGlobalConcurrency    int
	MaxPerClientConcurrency int
}

type CreateRequest struct {
	MediaURL  string
	MediaType MediaType
	Attempt   int
	ClientID  string
	RequestID string
}

type M3U8Request struct {
	URL       string
	ClientID  string
	RequestID string
}

type TaskView struct {
	TaskID      string    `json:"taskId"`
	Status      Status    `json:"status"`
	Progress    int       `json:"progress,omitempty"`
	PollURL     string    `json:"pollUrl,omitempty"`
	DownloadURL string    `json:"downloadUrl,omitempty"`
	FileURL     string    `json:"fileUrl,omitempty"`
	ExpiresAt   time.Time `json:"expiresAt,omitempty"`
}

type fileRecord struct {
	Path        string
	ContentType string
}

type Service struct {
	signingKey              []byte
	entropy                 io.Reader
	tempRoot                string
	publicBaseURL           string
	clock                   func() time.Time
	ttl                     time.Duration
	maxGlobalConcurrency    int
	maxPerClientConcurrency int

	mu             sync.Mutex
	activeGlobal   int
	activeByClient map[string]int
	tasks          map[string]TaskView
	files          map[string]fileRecord
}

func NewService(options ServiceOptions) (*Service, error) {
	if len(options.SigningKey) == 0 {
		return nil, ErrSigningKeyRequired
	}
	entropy := options.Entropy
	if entropy == nil {
		entropy = rand.Reader
	}
	clock := options.Clock
	if clock == nil {
		clock = time.Now
	}
	tempRoot := strings.TrimSpace(options.TempRoot)
	if tempRoot == "" {
		tempRoot = filepath.Join(os.TempDir(), "watermark-download")
	}
	ttl := options.TTL
	if ttl <= 0 {
		ttl = defaultTTL
	}
	global := options.MaxGlobalConcurrency
	if global <= 0 {
		global = defaultGlobalConcurrency
	}
	perClient := options.MaxPerClientConcurrency
	if perClient <= 0 {
		perClient = defaultPerClientConcurrency
	}
	return &Service{
		signingKey:              append([]byte(nil), options.SigningKey...),
		entropy:                 entropy,
		tempRoot:                tempRoot,
		publicBaseURL:           strings.TrimRight(strings.TrimSpace(options.PublicBaseURL), "/"),
		clock:                   clock,
		ttl:                     ttl,
		maxGlobalConcurrency:    global,
		maxPerClientConcurrency: perClient,
		activeByClient:          make(map[string]int),
		tasks:                   make(map[string]TaskView),
		files:                   make(map[string]fileRecord),
	}, nil
}

func (service *Service) CreateFallback(ctx context.Context, request CreateRequest) (TaskView, error) {
	if err := ctx.Err(); err != nil {
		return TaskView{}, err
	}
	if request.Attempt < 4 {
		return TaskView{}, ErrAttemptTooEarly
	}
	if _, err := validatedMediaTarget(request.MediaURL); err != nil {
		return TaskView{}, errors.Join(ErrUnsafeTarget, err)
	}
	if _, err := MaxBytesForMediaType(request.MediaType); err != nil {
		return TaskView{}, err
	}
	taskID, err := service.generateID("download")
	if err != nil {
		return TaskView{}, err
	}
	expiresAt := service.clock().Add(service.ttl)
	pollURL, err := service.buildTicketURL("/api/download/fallback/"+taskID, taskID, PurposePoll, expiresAt)
	if err != nil {
		return TaskView{}, err
	}
	view := TaskView{TaskID: taskID, Status: StatusPending, Progress: 0, PollURL: pollURL, ExpiresAt: expiresAt}
	service.mu.Lock()
	service.tasks[taskID] = view
	service.mu.Unlock()
	return view, nil
}

func (service *Service) GetFallback(_ context.Context, taskID string, ticket string) (TaskView, bool, error) {
	taskID = strings.TrimSpace(taskID)
	claims, err := VerifyTicket(service.signingKey, ticket, PurposePoll, service.clock())
	if err != nil {
		return TaskView{}, false, err
	}
	if claims.TaskID != taskID {
		return TaskView{}, false, ErrInvalidTicket
	}
	service.mu.Lock()
	view, ok := service.tasks[taskID]
	service.mu.Unlock()
	if !ok {
		return TaskView{}, false, nil
	}
	if view.Status == StatusCompleted && view.DownloadURL == "" {
		downloadURL, err := service.buildTicketURL("/api/download/file/"+taskID, taskID, PurposeDownload, service.clock().Add(service.ttl))
		if err != nil {
			return TaskView{}, false, err
		}
		view.DownloadURL = downloadURL
	}
	return view, true, nil
}

func (service *Service) CreateM3U8(ctx context.Context, request M3U8Request) (TaskView, error) {
	if err := ctx.Err(); err != nil {
		return TaskView{}, err
	}
	if _, err := validatedMediaTarget(request.URL); err != nil {
		return TaskView{}, errors.Join(ErrUnsafeTarget, err)
	}
	taskID, err := service.generateID("media")
	if err != nil {
		return TaskView{}, err
	}
	view := TaskView{
		TaskID:    taskID,
		Status:    StatusPending,
		Progress:  0,
		PollURL:   service.withBaseURL("/api/task/" + taskID),
		ExpiresAt: service.clock().Add(service.ttl),
	}
	service.mu.Lock()
	service.tasks[taskID] = view
	service.mu.Unlock()
	return view, nil
}

func (service *Service) GetM3U8(_ context.Context, taskID string) (TaskView, bool, error) {
	taskID = strings.TrimSpace(taskID)
	service.mu.Lock()
	view, ok := service.tasks[taskID]
	service.mu.Unlock()
	if !ok {
		return TaskView{}, false, nil
	}
	if view.Status == StatusCompleted && view.FileURL == "" {
		fileURL, err := service.buildTicketURL("/api/task/file/"+taskID, taskID, PurposeM3U8File, service.clock().Add(service.ttl))
		if err != nil {
			return TaskView{}, false, err
		}
		view.FileURL = fileURL
	}
	return view, true, nil
}

func (service *Service) ValidateFileTicket(_ context.Context, taskID string, ticket string) error {
	claims, err := VerifyTicket(service.signingKey, ticket, PurposeM3U8File, service.clock())
	if err != nil {
		return err
	}
	if claims.TaskID != strings.TrimSpace(taskID) {
		return ErrInvalidTicket
	}
	return nil
}

func (service *Service) ValidateDownloadTicket(_ context.Context, taskID string, ticket string) error {
	claims, err := VerifyTicket(service.signingKey, ticket, PurposeDownload, service.clock())
	if err != nil {
		return err
	}
	if claims.TaskID != strings.TrimSpace(taskID) {
		return ErrInvalidTicket
	}
	return nil
}

func (service *Service) AcquireTransfer(ctx context.Context, clientID string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	clientID = strings.TrimSpace(clientID)
	if clientID == "" {
		clientID = "anonymous"
	}
	service.mu.Lock()
	if service.activeGlobal >= service.maxGlobalConcurrency || service.activeByClient[clientID] >= service.maxPerClientConcurrency {
		service.mu.Unlock()
		return nil, ErrConcurrencyLimit
	}
	service.activeGlobal++
	service.activeByClient[clientID]++
	service.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			service.mu.Lock()
			defer service.mu.Unlock()
			if service.activeGlobal > 0 {
				service.activeGlobal--
			}
			if service.activeByClient[clientID] > 1 {
				service.activeByClient[clientID]--
			} else {
				delete(service.activeByClient, clientID)
			}
		})
	}, nil
}

func (service *Service) EnsureRoot() error {
	if service == nil || strings.TrimSpace(service.tempRoot) == "" {
		return ErrUnsafePath
	}
	if err := os.MkdirAll(service.tempRoot, 0o700); err != nil {
		return err
	}
	return os.Chmod(service.tempRoot, 0o700)
}

func (service *Service) TaskFilePath(taskID string) (string, error) {
	if service == nil {
		return "", ErrUnsafePath
	}
	if !safeTaskIDPattern.MatchString(strings.TrimSpace(taskID)) {
		return "", ErrUnsafePath
	}
	root, err := filepath.Abs(service.tempRoot)
	if err != nil {
		return "", ErrUnsafePath
	}
	path := filepath.Join(root, strings.TrimSpace(taskID)+".bin")
	if !strings.HasPrefix(path, root+string(filepath.Separator)) {
		return "", ErrUnsafePath
	}
	return path, nil
}

func (service *Service) WriteCompletedFile(ctx context.Context, taskID string, body []byte, contentType string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := service.EnsureRoot(); err != nil {
		return err
	}
	path, err := service.TaskFilePath(taskID)
	if err != nil {
		return err
	}
	if err := rejectSymlink(path); err != nil {
		return err
	}
	partPath := path + ".part"
	defer func() {
		_ = os.Remove(partPath)
	}()
	if err := rejectSymlink(partPath); err != nil {
		return err
	}
	file, err := os.OpenFile(partPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(body)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Chmod(partPath, 0o600); err != nil {
		return err
	}
	if err := rejectSymlink(path); err != nil {
		return err
	}
	if err := os.Rename(partPath, path); err != nil {
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return err
	}
	if strings.TrimSpace(contentType) == "" {
		contentType = "application/octet-stream"
	}
	view := TaskView{TaskID: taskID, Status: StatusCompleted, Progress: 100, ExpiresAt: service.clock().Add(service.ttl)}
	service.mu.Lock()
	if existing, ok := service.tasks[taskID]; ok {
		view.PollURL = existing.PollURL
		view.ExpiresAt = existing.ExpiresAt
	}
	service.tasks[taskID] = view
	service.files[taskID] = fileRecord{Path: path, ContentType: contentType}
	service.mu.Unlock()
	return nil
}

func (service *Service) MarkFailed(ctx context.Context, taskID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	taskID = strings.TrimSpace(taskID)
	if !safeTaskIDPattern.MatchString(taskID) {
		return ErrUnsafePath
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	view, ok := service.tasks[taskID]
	if !ok {
		return FormatTaskNotFound(taskID)
	}
	view.Status = StatusFailed
	view.Progress = 0
	if view.ExpiresAt.IsZero() {
		view.ExpiresAt = service.clock().Add(service.ttl)
	}
	service.tasks[taskID] = view
	return nil
}

func (service *Service) ServeTaskFile(writer http.ResponseWriter, request *http.Request, taskID string) error {
	path, err := service.TaskFilePath(taskID)
	if err != nil {
		return err
	}
	service.mu.Lock()
	record := service.files[taskID]
	service.mu.Unlock()
	if record.Path != "" {
		path = record.Path
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() {
		_ = file.Close()
	}()
	stat, err := file.Stat()
	if err != nil {
		return err
	}
	contentType := record.ContentType
	if strings.TrimSpace(contentType) == "" {
		contentType = mime.TypeByExtension(filepath.Ext(path))
	}
	if strings.TrimSpace(contentType) == "" {
		contentType = "application/octet-stream"
	}
	writer.Header().Set("Content-Type", contentType)
	http.ServeContent(writer, request, filepath.Base(path), stat.ModTime(), file)
	return nil
}

func (service *Service) CleanupExpired(ctx context.Context, now time.Time) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if now.IsZero() {
		now = service.clock()
	}
	type deletion struct {
		taskID string
		path   string
	}
	var deletions []deletion
	service.mu.Lock()
	for taskID, view := range service.tasks {
		if view.Status == StatusRunning {
			continue
		}
		if view.ExpiresAt.IsZero() || view.ExpiresAt.After(now) {
			continue
		}
		record := service.files[taskID]
		delete(service.tasks, taskID)
		delete(service.files, taskID)
		deletions = append(deletions, deletion{taskID: taskID, path: record.Path})
	}
	service.mu.Unlock()
	for _, item := range deletions {
		path := item.path
		if strings.TrimSpace(path) == "" {
			var err error
			path, err = service.TaskFilePath(item.taskID)
			if err != nil {
				continue
			}
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return len(deletions), err
		}
		if err := os.Remove(path + ".part"); err != nil && !errors.Is(err, os.ErrNotExist) {
			return len(deletions), err
		}
	}
	return len(deletions), nil
}

func MaxBytesForMediaType(mediaType MediaType) (int64, error) {
	switch mediaType {
	case MediaTypeVideo:
		return 300 << 20, nil
	case MediaTypeAudio:
		return 50 << 20, nil
	case MediaTypeImage:
		return 20 << 20, nil
	default:
		return 0, errors.New("unsupported download media type")
	}
}

type StreamOptions struct {
	IdleTimeout time.Duration
	MaxBytes    int64
	BufferSize  int
}

func CopyWithIdleDeadline(ctx context.Context, destination io.Writer, source io.Reader, options StreamOptions) (int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if destination == nil || source == nil {
		return 0, errors.New("download stream endpoints are required")
	}
	if options.BufferSize <= 0 {
		options.BufferSize = 32 << 10
	}
	buffer := make([]byte, options.BufferSize)
	var written int64
	for {
		type readResult struct {
			n   int
			err error
		}
		resultc := make(chan readResult, 1)
		go func() {
			n, err := source.Read(buffer)
			resultc <- readResult{n: n, err: err}
		}()
		var timer <-chan time.Time
		var stopTimer func()
		if options.IdleTimeout > 0 {
			timeout := time.NewTimer(options.IdleTimeout)
			timer = timeout.C
			stopTimer = func() {
				if !timeout.Stop() {
					select {
					case <-timeout.C:
					default:
					}
				}
			}
		} else {
			stopTimer = func() {}
		}
		select {
		case <-ctx.Done():
			stopTimer()
			return written, ctx.Err()
		case <-timer:
			return written, ErrStreamIdleTimeout
		case result := <-resultc:
			stopTimer()
			if result.n > 0 {
				if options.MaxBytes > 0 && written+int64(result.n) > options.MaxBytes {
					return written, ErrMediaSizeExceeded
				}
				n, err := destination.Write(buffer[:result.n])
				written += int64(n)
				if err != nil {
					return written, err
				}
				if n != result.n {
					return written, io.ErrShortWrite
				}
			}
			if result.err != nil {
				if errors.Is(result.err, io.EOF) {
					return written, nil
				}
				return written, result.err
			}
		}
	}
}

func validatedMediaTarget(raw string) (netguard.FetchURL, error) {
	target, err := netguard.NewFetchURL(strings.TrimSpace(raw))
	if err != nil {
		return netguard.FetchURL{}, err
	}
	return target, nil
}

func (service *Service) buildTicketURL(path string, taskID string, purpose Purpose, expiresAt time.Time) (string, error) {
	ticket, err := SignTicket(service.signingKey, TicketClaims{TaskID: taskID, Purpose: purpose, ExpiresAt: expiresAt}, service.clock())
	if err != nil {
		return "", errors.Join(ErrURLBuild, err)
	}
	return service.withBaseURL(path + "?ticket=" + ticket), nil
}

func (service *Service) withBaseURL(path string) string {
	if service.publicBaseURL == "" {
		return path
	}
	if strings.HasPrefix(path, "/") {
		return service.publicBaseURL + path
	}
	return service.publicBaseURL + "/" + path
}

func (service *Service) generateID(prefix string) (string, error) {
	if service == nil || service.entropy == nil {
		return "", errors.New("download entropy unavailable")
	}
	raw := make([]byte, 16)
	if _, err := io.ReadFull(service.entropy, raw); err != nil {
		return "", errors.Join(ErrEntropyUnavailable, err)
	}
	return prefix + "_" + base64.RawURLEncoding.EncodeToString(raw), nil
}

func rejectSymlink(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return ErrUnsafePath
	}
	return nil
}

func FormatTaskNotFound(taskID string) error {
	return fmt.Errorf("%w: %s", ErrTaskNotFound, strings.TrimSpace(taskID))
}
