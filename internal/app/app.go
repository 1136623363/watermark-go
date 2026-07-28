package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/1136623363/watermark-go/internal/admin"
	"github.com/1136623363/watermark-go/internal/auth"
	sharedcache "github.com/1136623363/watermark-go/internal/cache"
	"github.com/1136623363/watermark-go/internal/config"
	"github.com/1136623363/watermark-go/internal/download"
	"github.com/1136623363/watermark-go/internal/httpapi"
	"github.com/1136623363/watermark-go/internal/netguard"
	parseusecase "github.com/1136623363/watermark-go/internal/parse"
	coreparser "github.com/1136623363/watermark-go/internal/parser"
	"github.com/1136623363/watermark-go/internal/parser/native"
	dbstore "github.com/1136623363/watermark-go/internal/store"
	"github.com/1136623363/watermark-go/internal/task"
	"github.com/redis/go-redis/v9"
)

const DefaultShutdownTimeout = 20 * time.Second

type Component interface {
	Start(context.Context) error
	Done() <-chan error
	Stop(context.Context) error
}

type Option func(*options) error

type options struct {
	components      []Component
	componentsSet   bool
	shutdownTimeout time.Duration
}

type App struct {
	components      []Component
	doneChannels    []<-chan error
	shutdownTimeout time.Duration
}

type startedComponent struct {
	index     int
	component Component
	done      <-chan error
}

type lifecycleEventKind uint8

const (
	lifecycleStartCompleted lifecycleEventKind = iota
	lifecycleComponentTerminated
	lifecycleProcessCanceled
)

type lifecycleEvent struct {
	kind           lifecycleEventKind
	componentIndex int
	err            error
	startCompleted bool
	startErr       error
}

func New(cfg config.Config, supplied ...Option) (*App, error) {
	settings := options{
		shutdownTimeout: DefaultShutdownTimeout,
	}
	for _, option := range supplied {
		if option == nil {
			return nil, errors.New("nil app option")
		}
		if err := option(&settings); err != nil {
			return nil, err
		}
	}
	if settings.shutdownTimeout <= 0 {
		return nil, errors.New("shutdown timeout must be positive")
	}
	if !settings.componentsSet {
		components, err := buildRuntimeComponents(cfg)
		if err != nil {
			return nil, err
		}
		settings.components = components
	}
	doneChannels := make([]<-chan error, len(settings.components))
	for index, component := range settings.components {
		if component == nil {
			return nil, errors.New("nil app component")
		}
		doneChannels[index] = component.Done()
		if doneChannels[index] == nil {
			return nil, errors.New("app component has no terminal event channel")
		}
	}
	return &App{
		components:      append([]Component(nil), settings.components...),
		doneChannels:    doneChannels,
		shutdownTimeout: settings.shutdownTimeout,
	}, nil
}

func WithComponents(components ...Component) Option {
	return func(settings *options) error {
		settings.components = append([]Component(nil), components...)
		settings.componentsSet = true
		return nil
	}
}

func WithShutdownTimeout(timeout time.Duration) Option {
	return func(settings *options) error {
		if timeout <= 0 {
			return errors.New("shutdown timeout must be positive")
		}
		settings.shutdownTimeout = timeout
		return nil
	}
}

func (application *App) Run(ctx context.Context) error {
	if application == nil {
		return errors.New("nil app")
	}
	if ctx == nil {
		return errors.New("nil run context")
	}

	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()

	started := make([]startedComponent, 0, len(application.components))
	for index, component := range application.components {
		startResult := make(chan error, 1)
		go func(component Component) {
			startResult <- component.Start(runCtx)
		}(component)

		event := waitLifecycleEvent(ctx, startResult, started)
		current := startedComponent{index: index, component: component, done: application.doneChannels[index]}
		switch event.kind {
		case lifecycleStartCompleted:
			if event.err != nil {
				cancelRun()
				shutdownErr := application.stopWithNewBudget(started)
				return errors.Join(fmt.Errorf("start component %d: %w", index, event.err), shutdownErr)
			}
			started = append(started, current)
		case lifecycleComponentTerminated:
			cancelRun()
			shutdownErr := application.settleStartAndStop(started, current, startResult, event)
			return errors.Join(componentTerminalError(event), shutdownErr)
		case lifecycleProcessCanceled:
			cancelRun()
			return application.settleStartAndStop(started, current, startResult, event)
		default:
			cancelRun()
			return errors.New("invalid application lifecycle event")
		}
	}

	event := waitLifecycleEvent(ctx, nil, started)
	cancelRun()
	switch event.kind {
	case lifecycleComponentTerminated:
		return errors.Join(componentTerminalError(event), application.stopWithNewBudget(started))
	case lifecycleProcessCanceled:
		return application.stopWithNewBudget(started)
	default:
		return errors.New("invalid application lifecycle event")
	}
}

func waitLifecycleEvent(ctx context.Context, startResult <-chan error, started []startedComponent) lifecycleEvent {
	if event, ok := pollTerminalEvent(started); ok {
		return event
	}
	if err := ctx.Err(); err != nil {
		if event, ok := pollTerminalEvent(started); ok {
			return event
		}
		return lifecycleEvent{kind: lifecycleProcessCanceled, err: err}
	}
	if startResult != nil {
		select {
		case err := <-startResult:
			if event, ok := pollTerminalEvent(started); ok {
				event.startCompleted = true
				event.startErr = err
				return event
			}
			if processErr := ctx.Err(); processErr != nil {
				return lifecycleEvent{
					kind:           lifecycleProcessCanceled,
					err:            processErr,
					startCompleted: true,
					startErr:       err,
				}
			}
			return lifecycleEvent{kind: lifecycleStartCompleted, err: err, startCompleted: true, startErr: err}
		default:
		}
	}

	cases := make([]reflect.SelectCase, 0, len(started)+2)
	for _, component := range started {
		cases = append(cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(component.done)})
	}
	startCase := -1
	if startResult != nil {
		startCase = len(cases)
		cases = append(cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(startResult)})
	}
	processCase := len(cases)
	cases = append(cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(ctx.Done())})

	chosen, received, open := reflect.Select(cases)
	if chosen < len(started) {
		return lifecycleEvent{
			kind:           lifecycleComponentTerminated,
			componentIndex: started[chosen].index,
			err:            reflectedError(received, open),
		}
	}
	if chosen == startCase {
		startErr := reflectedError(received, open)
		if event, ok := pollTerminalEvent(started); ok {
			event.startCompleted = true
			event.startErr = startErr
			return event
		}
		if processErr := ctx.Err(); processErr != nil {
			return lifecycleEvent{
				kind:           lifecycleProcessCanceled,
				err:            processErr,
				startCompleted: true,
				startErr:       startErr,
			}
		}
		return lifecycleEvent{kind: lifecycleStartCompleted, err: startErr, startCompleted: true, startErr: startErr}
	}
	if chosen == processCase {
		if event, ok := pollTerminalEvent(started); ok {
			return event
		}
		return lifecycleEvent{kind: lifecycleProcessCanceled, err: ctx.Err()}
	}
	return lifecycleEvent{kind: lifecycleProcessCanceled, err: errors.New("invalid lifecycle selection")}
}

func pollTerminalEvent(started []startedComponent) (lifecycleEvent, bool) {
	if len(started) == 0 {
		return lifecycleEvent{}, false
	}
	cases := make([]reflect.SelectCase, 0, len(started)+1)
	for _, component := range started {
		cases = append(cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(component.done)})
	}
	cases = append(cases, reflect.SelectCase{Dir: reflect.SelectDefault})
	chosen, received, open := reflect.Select(cases)
	if chosen == len(started) {
		return lifecycleEvent{}, false
	}
	return lifecycleEvent{
		kind:           lifecycleComponentTerminated,
		componentIndex: started[chosen].index,
		err:            reflectedError(received, open),
	}, true
}

func reflectedError(value reflect.Value, open bool) error {
	if !open || !value.IsValid() || value.IsNil() {
		return nil
	}
	return value.Interface().(error)
}

func componentTerminalError(event lifecycleEvent) error {
	terminalErr := event.err
	if terminalErr == nil {
		terminalErr = errors.New("component stopped unexpectedly")
	}
	return fmt.Errorf("component %d terminated: %w", event.componentIndex, terminalErr)
}

func (application *App) settleStartAndStop(
	started []startedComponent,
	current startedComponent,
	startResult <-chan error,
	event lifecycleEvent,
) error {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), application.shutdownTimeout)
	defer cancel()

	startCompleted := event.startCompleted
	startErr := event.startErr
	var result error
	if !startCompleted {
		select {
		case startErr = <-startResult:
			startCompleted = true
		case <-shutdownCtx.Done():
			result = errors.Join(result, fmt.Errorf("cancel start component %d: %w", current.index, shutdownCtx.Err()))
		}
	}
	if startCompleted && startErr == nil {
		started = append(started, current)
		if terminalEvent, ok := pollTerminalEvent([]startedComponent{current}); ok {
			result = errors.Join(result, componentTerminalError(terminalEvent))
		}
	}
	return errors.Join(result, application.stop(started, shutdownCtx))
}

func (application *App) stopWithNewBudget(started []startedComponent) error {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), application.shutdownTimeout)
	defer cancel()
	return application.stop(started, shutdownCtx)
}

func (application *App) stop(started []startedComponent, shutdownCtx context.Context) error {
	var result error
	for index := len(started) - 1; index >= 0; index-- {
		if err := started[index].component.Stop(shutdownCtx); err != nil {
			result = errors.Join(result, fmt.Errorf("stop component %d: %w", started[index].index, err))
		}
	}
	return result
}

func buildRuntimeComponents(cfg config.Config) ([]Component, error) {
	handler, closers, background, err := buildRuntimeHandler(cfg)
	if err != nil {
		return nil, err
	}
	components := make([]Component, 0, 2+len(background))
	if len(closers) > 0 {
		components = append(components, newRuntimeResourceComponent(closers...))
	}
	components = append(components, background...)
	components = append(components, httpapi.NewServerFromConfig(cfg, handler))
	return components, nil
}

func buildRuntimeHandler(cfg config.Config) (http.Handler, []io.Closer, []Component, error) {
	stores, err := newRuntimeStores(cfg)
	if err != nil {
		return nil, nil, nil, err
	}
	authService, err := auth.NewService(auth.ServiceOptions{
		Environment: cfg.Environment,
		Store:       stores.authStore,
		Entropy:     rand.Reader,
		WeChat: auth.WeChatConfig{
			AppID:     cfg.Security.WechatMiniAppID,
			AppSecret: cfg.Security.WechatMiniAppSecret,
		},
	})
	if err != nil {
		closeRuntimeClosers(stores.closers)
		return nil, nil, nil, fmt.Errorf("construct client auth: %w", err)
	}
	parseService, err := newRuntimeParseService(cfg, stores.parseStore, stores.parseCache)
	if err != nil {
		closeRuntimeClosers(stores.closers)
		return nil, nil, nil, err
	}
	parseTasks := parseusecase.NewAsyncTasks(parseusecase.AsyncTaskDependencies{
		Store:   stores.taskStore,
		Entropy: rand.Reader,
	})
	rawDownloadService, err := download.NewService(download.ServiceOptions{
		SigningKey:    []byte(cfg.Download.TokenSecret),
		Entropy:       rand.Reader,
		TempRoot:      runtimeTempRoot(cfg),
		PublicBaseURL: "",
	})
	if err != nil {
		closeRuntimeClosers(stores.closers)
		return nil, nil, nil, fmt.Errorf("construct download service: %w", err)
	}
	downloadFetcher, err := newRuntimeDownloadFetcher()
	if err != nil {
		closeRuntimeClosers(stores.closers)
		return nil, nil, nil, fmt.Errorf("construct download fetcher: %w", err)
	}
	downloadService := &runtimeDownloadService{
		inner:   rawDownloadService,
		fetcher: downloadFetcher,
	}
	adminService, err := newRuntimeAdminService(cfg, stores.adminStore)
	if err != nil {
		closeRuntimeClosers(stores.closers)
		return nil, nil, nil, err
	}
	var background []Component
	if cfg.Environment == config.EnvironmentProduction {
		worker := task.NewWorker(
			stores.taskStore,
			parseusecase.TaskExecutor{Parser: parseService},
			task.WithWorkerID(runtimeWorkerID(cfg)),
		)
		background = append(background, newRuntimeTaskWorkerComponent(worker, runtimeTaskWorkerOptions{
			Concurrency: cfg.Tasks.WorkerConcurrency,
		}))
	}
	handler := httpapi.Router(httpapi.RouterOptions{
		Client: httpapi.ClientHandlers{
			Auth:  authService,
			Parse: authenticatedParse(parseService),
		},
		Parse:      httpapi.ParseHandlers{Service: parseService},
		ParseTasks: httpapi.ParseTaskHandlers{Service: parseTasks},
		Download:   httpapi.DownloadHandlers{Service: downloadService},
		Admin:      httpapi.AdminHandlers{Service: adminService},
	})
	return handler, stores.closers, background, nil
}

type runtimeStores struct {
	authStore  auth.Store
	parseStore runtimeParseResultStore
	parseCache parseusecase.Cache
	taskStore  runtimeTaskStore
	adminStore admin.UserStore
	closers    []io.Closer
}

type runtimeParseResultStore interface {
	parseusecase.Store
	parseusecase.CachedReader
}

type runtimeTaskStore interface {
	parseusecase.AsyncTaskStore
	task.LeaseStore
}

func newRuntimeStores(cfg config.Config) (runtimeStores, error) {
	parseStore := newMemoryParseStore()
	taskStore := task.NewMemoryStore()
	stores := runtimeStores{
		authStore:  auth.NewMemoryStore(),
		parseStore: parseStore,
		taskStore:  taskStore,
		adminStore: nil,
	}
	cacheAdapter, cacheCloser, err := newRuntimeParseCache(cfg)
	if err != nil {
		return runtimeStores{}, err
	}
	if cacheCloser != nil {
		stores.closers = append(stores.closers, cacheCloser)
	}
	stores.parseCache = cacheAdapter
	if cfg.Environment != config.EnvironmentProduction {
		return stores, nil
	}
	if strings.TrimSpace(cfg.MySQL.DSN) == "" {
		closeRuntimeClosers(stores.closers)
		return runtimeStores{}, errors.New("production runtime requires MYSQL_DSN")
	}
	db, err := dbstore.OpenMySQL(context.Background(), dbstore.MySQLConfig{DSN: cfg.MySQL.DSN})
	if err != nil {
		closeRuntimeClosers(stores.closers)
		return runtimeStores{}, fmt.Errorf("open production MYSQL_DSN: %w", err)
	}
	mysqlStore, err := dbstore.NewMySQLRuntimeStore(db)
	if err != nil {
		_ = db.Close()
		closeRuntimeClosers(stores.closers)
		return runtimeStores{}, err
	}
	if err := mysqlStore.SeedAdminUser(context.Background(), "admin", cfg.Security.AdminPassword); err != nil {
		_ = db.Close()
		closeRuntimeClosers(stores.closers)
		return runtimeStores{}, fmt.Errorf("seed production admin user: %w", err)
	}
	stores.authStore = mysqlStore
	stores.parseStore = mysqlStore
	stores.taskStore = mysqlStore
	stores.adminStore = mysqlStore
	stores.closers = append(stores.closers, db)
	return stores, nil
}

func newRuntimeParseCache(cfg config.Config) (parseusecase.Cache, io.Closer, error) {
	fallback := sharedcache.NewMemory(512)
	if strings.TrimSpace(cfg.Redis.Addr) == "" {
		return &runtimeParseCache{store: sharedcache.NewTiered(nil, fallback)}, nil, nil
	}
	namespace := strings.TrimSpace(cfg.Redis.Namespace)
	if namespace == "" {
		namespace = firstNonEmptyRuntime(cfg.Environment, "development")
	}
	client := redis.NewClient(&redis.Options{
		Addr:     strings.TrimSpace(cfg.Redis.Addr),
		Username: strings.TrimSpace(cfg.Redis.Username),
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
	})
	redisStore, err := sharedcache.NewRedis(client, namespace)
	if err != nil {
		_ = client.Close()
		return nil, nil, err
	}
	return &runtimeParseCache{store: sharedcache.NewTiered(redisStore, fallback)}, client, nil
}

func runtimeWorkerID(cfg config.Config) string {
	parts := []string{"watermark-go"}
	for _, value := range []string{cfg.Gate.Role, cfg.Gate.DataStage, cfg.Gate.DeploymentRunID} {
		value = strings.TrimSpace(value)
		if value != "" {
			parts = append(parts, value)
		}
	}
	if len(parts) == 1 {
		if hostname, err := os.Hostname(); err == nil && strings.TrimSpace(hostname) != "" {
			parts = append(parts, strings.TrimSpace(hostname))
		}
	}
	return strings.Join(parts, ":")
}

func newRuntimeParseService(cfg config.Config, store runtimeParseResultStore, cache parseusecase.Cache) (*runtimeParseService, error) {
	fetcher, err := netguard.NewDefaultFetcher()
	if err != nil {
		return nil, fmt.Errorf("construct guarded fetcher: %w", err)
	}
	nativeService, err := native.NewService(coreparser.Dependencies{
		Fetcher:     fetcher,
		Clock:       time.Now,
		WeiboCookie: cfg.Parser.WeiboCookie,
		XiguaCookie: cfg.Parser.XiguaCookie,
		SohuToken:   cfg.Parser.SohuAPIToken,
	})
	if err != nil {
		return nil, fmt.Errorf("construct native parser: %w", err)
	}
	descriptors := native.Descriptors()
	resolver, err := parseusecase.NewRegistryResolver(descriptors)
	if err != nil {
		return nil, fmt.Errorf("construct parser resolver: %w", err)
	}
	nativeParser := nativeUsecaseParser{
		service:     nativeService,
		idPlatforms: supportedIDPlatforms(descriptors),
	}
	service := parseusecase.NewService(parseusecase.Dependencies{
		Parser: parseusecase.ParserChain{Parsers: []parseusecase.Parser{
			m3u8PassthroughParser{},
			nativeParser,
		}},
		IDParser: nativeParser,
		Resolver: chainResolver{
			resolvers: []parseusecase.Resolver{
				m3u8Resolver{},
				resolver,
			},
		},
		Cache:   cache,
		Store:   store,
		Entropy: rand.Reader,
	})
	return &runtimeParseService{service: service, cache: store}, nil
}

func newRuntimeDownloadFetcher() (*netguard.Fetcher, error) {
	validator, err := netguard.NewValidator(netguard.ValidatorOptions{})
	if err != nil {
		return nil, err
	}
	maxBytes, err := maxRuntimeDownloadBytes()
	if err != nil {
		return nil, err
	}
	return netguard.NewFetcher(netguard.FetcherOptions{
		Validator: validator,
		Limits: netguard.Limits{
			ResponseHeaderBytes: 64 << 10,
			WireBodyBytes:       maxBytes,
			DecodedBodyBytes:    maxBytes,
			Duration:            10 * time.Minute,
		},
	})
}

func maxRuntimeDownloadBytes() (int64, error) {
	mediaTypes := []download.MediaType{
		download.MediaTypeVideo,
		download.MediaTypeAudio,
		download.MediaTypeImage,
	}
	var maxBytes int64
	for _, mediaType := range mediaTypes {
		limit, err := download.MaxBytesForMediaType(mediaType)
		if err != nil {
			return 0, err
		}
		if limit > maxBytes {
			maxBytes = limit
		}
	}
	if maxBytes <= 0 {
		return 0, errors.New("download media limit must be positive")
	}
	return maxBytes, nil
}

type runtimeParseService struct {
	service *parseusecase.Service
	cache   parseusecase.CachedReader
}

func (service *runtimeParseService) Parse(ctx context.Context, request parseusecase.Request) (parseusecase.ParseOutput, error) {
	if service == nil || service.service == nil {
		return parseusecase.ParseOutput{}, parseusecase.NewError(parseusecase.ErrorInternal, parseusecase.StageParser, "", true)
	}
	return service.service.Parse(ctx, request)
}

func (service *runtimeParseService) ParseID(ctx context.Context, request parseusecase.IDRequest) (parseusecase.ParseOutput, error) {
	if service == nil || service.service == nil {
		return parseusecase.ParseOutput{}, parseusecase.NewError(parseusecase.ErrorUnsupported, parseusecase.StageInput, request.Source, false)
	}
	return service.service.ParseID(ctx, request)
}

func (service *runtimeParseService) GetCached(ctx context.Context, shareID string) (parseusecase.CompatData, bool, error) {
	if service == nil || service.cache == nil {
		return parseusecase.CompatData{}, false, nil
	}
	return service.cache.GetCached(ctx, shareID)
}

func authenticatedParse(service *runtimeParseService) httpapi.ParseFunc {
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

type guardedFetcher interface {
	Fetch(context.Context, netguard.FetchRequest) (*http.Response, error)
}

type runtimeDownloadService struct {
	inner       *download.Service
	fetcher     guardedFetcher
	idleTimeout time.Duration
}

func (service *runtimeDownloadService) CreateFallback(ctx context.Context, request download.CreateRequest) (download.TaskView, error) {
	if service == nil || service.inner == nil {
		return download.TaskView{}, errors.New("download service unavailable")
	}
	view, err := service.inner.CreateFallback(ctx, request)
	if err != nil {
		return download.TaskView{}, err
	}
	service.startFallbackTransfer(view.TaskID, request)
	return view, nil
}

func (service *runtimeDownloadService) startFallbackTransfer(taskID string, request download.CreateRequest) {
	go func() {
		ctx := context.Background()
		if err := service.completeFallbackTransfer(ctx, taskID, request); err != nil {
			_ = service.inner.MarkFailed(context.Background(), taskID)
		}
	}()
}

func (service *runtimeDownloadService) completeFallbackTransfer(ctx context.Context, taskID string, request download.CreateRequest) error {
	if service == nil || service.inner == nil || service.fetcher == nil {
		return errors.New("download transfer unavailable")
	}
	target, err := netguard.NewFetchURL(strings.TrimSpace(request.MediaURL))
	if err != nil {
		return errors.Join(download.ErrUnsafeTarget, err)
	}
	maxBytes, err := download.MaxBytesForMediaType(request.MediaType)
	if err != nil {
		return err
	}
	release, err := service.inner.AcquireTransfer(ctx, request.ClientID)
	if err != nil {
		return err
	}
	defer release()

	response, err := service.fetcher.Fetch(ctx, netguard.FetchRequest{
		Method:       http.MethodGet,
		URL:          target,
		Header:       make(http.Header),
		MaxRedirects: 3,
	})
	if err != nil {
		return err
	}
	defer func() {
		_ = response.Body.Close()
	}()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("download media fetch status %d", response.StatusCode)
	}

	var body bytes.Buffer
	idleTimeout := service.idleTimeout
	if idleTimeout <= 0 {
		idleTimeout = 15 * time.Second
	}
	if _, err := download.CopyWithIdleDeadline(ctx, &body, response.Body, download.StreamOptions{
		IdleTimeout: idleTimeout,
		MaxBytes:    maxBytes,
	}); err != nil {
		return err
	}
	return service.inner.WriteCompletedFile(ctx, taskID, body.Bytes(), response.Header.Get("Content-Type"))
}

func (service *runtimeDownloadService) GetFallback(ctx context.Context, taskID string, ticket string) (download.TaskView, bool, error) {
	if service == nil || service.inner == nil {
		return download.TaskView{}, false, errors.New("download service unavailable")
	}
	return service.inner.GetFallback(ctx, taskID, ticket)
}

func (service *runtimeDownloadService) CreateM3U8(ctx context.Context, request download.M3U8Request) (download.TaskView, error) {
	if service == nil || service.inner == nil {
		return download.TaskView{}, errors.New("download service unavailable")
	}
	return service.inner.CreateM3U8(ctx, request)
}

func (service *runtimeDownloadService) GetM3U8(ctx context.Context, taskID string) (download.TaskView, bool, error) {
	if service == nil || service.inner == nil {
		return download.TaskView{}, false, errors.New("download service unavailable")
	}
	return service.inner.GetM3U8(ctx, taskID)
}

func (service *runtimeDownloadService) ValidateDownloadTicket(ctx context.Context, taskID string, ticket string) error {
	if service == nil || service.inner == nil {
		return errors.New("download service unavailable")
	}
	return service.inner.ValidateDownloadTicket(ctx, taskID, ticket)
}

func (service *runtimeDownloadService) ValidateFileTicket(ctx context.Context, taskID string, ticket string) error {
	if service == nil || service.inner == nil {
		return errors.New("download service unavailable")
	}
	return service.inner.ValidateFileTicket(ctx, taskID, ticket)
}

func (service *runtimeDownloadService) ServeTaskFile(writer http.ResponseWriter, request *http.Request, taskID string) error {
	if service == nil || service.inner == nil {
		return errors.New("download service unavailable")
	}
	return service.inner.ServeTaskFile(writer, request, taskID)
}

func newRuntimeAdminService(cfg config.Config, userStore admin.UserStore) (*admin.Service, error) {
	if userStore == nil {
		seeded, err := newSeededAdminStore(cfg.Security.AdminPassword)
		if err != nil {
			return nil, fmt.Errorf("construct admin user store: %w", err)
		}
		userStore = seeded
	}
	service, err := admin.NewService(admin.ServiceOptions{
		Auth: admin.AuthOptions{
			CookieSigningKey: []byte(cfg.Security.AdminSessionSecret),
			UserStore:        userStore,
			Environment:      cfg.Environment,
			EnvUsername:      "admin",
			EnvPassword:      cfg.Security.AdminPassword,
			AllowedOrigins:   []string{},
			Entropy:          rand.Reader,
		},
		StartedAt: time.Now(),
	})
	if err != nil {
		return nil, fmt.Errorf("construct admin service: %w", err)
	}
	return service, nil
}

func runtimeTempRoot(cfg config.Config) string {
	if workDir := strings.TrimSpace(cfg.Runner.Universal.WorkDir); workDir != "" {
		return filepath.Join(workDir, "downloads")
	}
	return filepath.Join(os.TempDir(), "watermark-go-downloads")
}

type chainResolver struct {
	resolvers []parseusecase.Resolver
}

func (resolver chainResolver) ResolveURL(raw string) (parseusecase.Descriptor, error) {
	var lastErr error
	for _, candidate := range resolver.resolvers {
		if candidate == nil {
			continue
		}
		descriptor, err := candidate.ResolveURL(raw)
		if err == nil {
			return descriptor, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return parseusecase.Descriptor{}, lastErr
	}
	return parseusecase.Descriptor{}, parseusecase.NewError(parseusecase.ErrorUnsupported, parseusecase.StageInput, "", false)
}

type m3u8Resolver struct{}

func (m3u8Resolver) ResolveURL(raw string) (parseusecase.Descriptor, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil || parsed.Hostname() == "" || !isHTTP(parsed.Scheme) || !looksLikeM3U8(parsed.Path) {
		return parseusecase.Descriptor{}, parseusecase.NewError(parseusecase.ErrorUnsupported, parseusecase.StageInput, "", false)
	}
	if _, err := netguard.NewFetchURL(parsed.String()); err != nil {
		return parseusecase.Descriptor{}, parseusecase.NewError(parseusecase.ErrorInvalidInput, parseusecase.StageInput, "m3u8", false)
	}
	queryKeys := make([]string, 0, len(parsed.Query()))
	for key := range parsed.Query() {
		key = strings.ToLower(strings.TrimSpace(key))
		if key != "" {
			queryKeys = append(queryKeys, key)
		}
	}
	sort.Strings(queryKeys)
	return parseusecase.Descriptor{
		Platform:  "m3u8",
		QueryKeys: queryKeys,
		HostRules: []parseusecase.HostRule{{
			Host:              strings.ToLower(parsed.Hostname()),
			IncludeSubdomains: false,
		}},
	}, nil
}

type m3u8PassthroughParser struct{}

func (m3u8PassthroughParser) Parse(_ context.Context, request parseusecase.ParserRequest) (parseusecase.Result, error) {
	if request.Descriptor.Platform != "m3u8" {
		return parseusecase.Result{}, parseusecase.NewError(parseusecase.ErrorUnsupported, parseusecase.StageParser, request.Descriptor.Platform, false)
	}
	return parseusecase.Result{
		Platform:   "m3u8",
		Type:       "m3u8",
		Title:      "m3u8",
		M3U8URL:    request.Canonical.URL,
		PreviewURL: request.Canonical.URL,
	}, nil
}

type nativeUsecaseParser struct {
	service     *native.Service
	idPlatforms map[string]struct{}
}

func (parser nativeUsecaseParser) Parse(ctx context.Context, request parseusecase.ParserRequest) (parseusecase.Result, error) {
	if parser.service == nil {
		return parseusecase.Result{}, parseusecase.NewError(parseusecase.ErrorInternal, parseusecase.StageParser, request.Descriptor.Platform, true)
	}
	info, err := parser.service.ParseVideoShareURL(ctx, request.RawURL)
	if err != nil {
		return parseusecase.Result{}, err
	}
	return nativeInfoToUsecaseResult(request.Descriptor.Platform, info), nil
}

func (parser nativeUsecaseParser) ParseID(ctx context.Context, request parseusecase.IDParserRequest) (parseusecase.Result, error) {
	if parser.service == nil {
		return parseusecase.Result{}, parseusecase.NewError(parseusecase.ErrorInternal, parseusecase.StageParser, request.Source, true)
	}
	source := strings.ToLower(strings.TrimSpace(request.Source))
	if len(parser.idPlatforms) > 0 {
		if _, ok := parser.idPlatforms[source]; !ok {
			return parseusecase.Result{}, parseusecase.NewError(parseusecase.ErrorUnsupported, parseusecase.StageInput, source, false)
		}
	}
	info, err := parser.service.ParseVideoID(ctx, request.Source, request.VideoID)
	if err != nil {
		return parseusecase.Result{}, err
	}
	return nativeInfoToUsecaseResult(request.Source, info), nil
}

func supportedIDPlatforms(descriptors []coreparser.Descriptor) map[string]struct{} {
	platforms := make(map[string]struct{})
	for _, descriptor := range descriptors {
		if !descriptor.SupportsID {
			continue
		}
		if key := strings.ToLower(strings.TrimSpace(string(descriptor.Key))); key != "" {
			platforms[key] = struct{}{}
		}
		for _, alias := range descriptor.Aliases {
			if key := strings.ToLower(strings.TrimSpace(string(alias))); key != "" {
				platforms[key] = struct{}{}
			}
		}
	}
	return platforms
}

func nativeInfoToUsecaseResult(platform string, info *native.VideoParseInfo) parseusecase.Result {
	if info == nil {
		return parseusecase.Result{Platform: strings.TrimSpace(platform)}
	}
	result := parseusecase.Result{
		Platform:   strings.TrimSpace(platform),
		Type:       "video",
		Title:      info.Title,
		VideoURL:   strings.TrimSpace(info.VideoUrl),
		AudioURL:   strings.TrimSpace(info.MusicUrl),
		CoverURL:   strings.TrimSpace(info.CoverUrl),
		PreviewURL: strings.TrimSpace(info.PreviewUrl),
		Author: parseusecase.Author{
			UID:    info.Author.Uid,
			Name:   info.Author.Name,
			Avatar: info.Author.Avatar,
		},
		Images: make([]parseusecase.ImageAsset, 0, len(info.Images)),
	}
	if looksLikeM3U8(result.VideoURL) {
		result.M3U8URL = result.VideoURL
	}
	for _, image := range info.Images {
		result.Images = append(result.Images, parseusecase.ImageAsset{
			URL:          image.Url,
			LivePhotoURL: image.LivePhotoUrl,
		})
	}
	if len(result.Images) > 0 && result.VideoURL == "" {
		result.Type = "gallery"
	}
	return result
}

func looksLikeM3U8(raw string) bool {
	return strings.Contains(strings.ToLower(strings.TrimSpace(raw)), ".m3u8")
}

func isHTTP(scheme string) bool {
	return strings.EqualFold(scheme, "http") || strings.EqualFold(scheme, "https")
}

type memoryParseStore struct {
	mu      sync.RWMutex
	byShare map[string]parseusecase.CompatData
}

func newMemoryParseStore() *memoryParseStore {
	return &memoryParseStore{byShare: make(map[string]parseusecase.CompatData)}
}

func (store *memoryParseStore) SaveResult(_ context.Context, result parseusecase.StoredResult) error {
	if store == nil {
		return errors.New("nil parse store")
	}
	if strings.TrimSpace(result.ShareID) == "" {
		return nil
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.byShare[result.ShareID] = result.Data
	return nil
}

func (store *memoryParseStore) GetCached(_ context.Context, shareID string) (parseusecase.CompatData, bool, error) {
	if store == nil {
		return parseusecase.CompatData{}, false, errors.New("nil parse store")
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	data, ok := store.byShare[strings.TrimSpace(shareID)]
	return data, ok, nil
}

type seededAdminStore struct {
	mu      sync.Mutex
	user    admin.User
	audits  []admin.AuditRecord
	enabled bool
}

func newSeededAdminStore(password string) (*seededAdminStore, error) {
	password = strings.TrimSpace(password)
	store := &seededAdminStore{}
	if password == "" {
		return store, nil
	}
	hash, err := admin.HashPassword(password)
	if err != nil {
		return nil, err
	}
	store.enabled = true
	store.user = admin.User{Username: "admin", Role: admin.RoleOwner, PasswordHash: hash}
	return store, nil
}

func (store *seededAdminStore) FindUser(_ context.Context, username string) (admin.User, bool, error) {
	if store == nil || !store.enabled || strings.TrimSpace(username) != store.user.Username {
		return admin.User{}, false, nil
	}
	return store.user, true, nil
}

func (store *seededAdminStore) RecordAudit(_ context.Context, record admin.AuditRecord) error {
	if store == nil {
		return errors.New("nil admin store")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.audits = append(store.audits, record)
	return nil
}
