package server

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/1136623363/watermark-go/internal/config"
)

const defaultStartupCancelTimeout = 20 * time.Second

var errStartupCancellationTimeout = errors.New("server startup cancellation timed out")

type runServerFunc func(context.Context, config.Config, func(*http.Server) error) error

type Service struct {
	cfg                  config.Config
	run                  runServerFunc
	startupCancelTimeout time.Duration

	mu              sync.Mutex
	started         bool
	startupCanceled bool
	readyClosed     bool
	ready           chan struct{}
	terminal        chan struct{}
	doneEvents      chan error
	terminalErr     error
	cancel          context.CancelCauseFunc
	httpServer      *http.Server
}

func New(cfg config.Config) *Service {
	return newService(cfg, startHTTPServer, defaultStartupCancelTimeout)
}

func newService(cfg config.Config, run runServerFunc, startupCancelTimeout time.Duration) *Service {
	return &Service{
		cfg:                  cfg,
		run:                  run,
		startupCancelTimeout: startupCancelTimeout,
		ready:                make(chan struct{}),
		terminal:             make(chan struct{}),
		doneEvents:           make(chan error, 1),
	}
}

func (service *Service) Start(ctx context.Context) error {
	if service == nil {
		return errors.New("nil server service")
	}
	if ctx == nil {
		return errors.New("nil server context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if service.run == nil || service.startupCancelTimeout <= 0 {
		return errors.New("invalid server lifecycle configuration")
	}

	service.mu.Lock()
	if service.started {
		service.mu.Unlock()
		return errors.New("server service already started")
	}
	service.started = true
	runCtx, cancel := context.WithCancelCause(context.WithoutCancel(ctx))
	service.cancel = cancel
	service.mu.Unlock()

	go func() {
		err := service.run(runCtx, service.cfg, service.markReady)
		service.finish(err)
	}()

	select {
	case <-service.ready:
		if err := ctx.Err(); err != nil {
			return service.cancelStartup(err)
		}
		return nil
	case <-service.terminal:
		cancel(errors.New("server stopped during startup"))
		err := service.terminalError()
		if err == nil {
			return errors.New("server stopped before becoming ready")
		}
		return err
	case <-ctx.Done():
		return service.cancelStartup(ctx.Err())
	}
}

func (service *Service) Done() <-chan error {
	if service == nil {
		return nil
	}
	return service.doneEvents
}

func (service *Service) Stop(ctx context.Context) error {
	if service == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("nil server shutdown context")
	}

	service.mu.Lock()
	if !service.started {
		service.mu.Unlock()
		return nil
	}
	if !service.readyClosed {
		service.startupCanceled = true
	}
	server := service.httpServer
	cancel := service.cancel
	terminal := service.terminal
	service.mu.Unlock()

	if cancel != nil {
		cancel(context.Canceled)
	}
	var result error
	if server != nil {
		if err := server.Shutdown(ctx); err != nil {
			result = errors.Join(result, err)
			if closeErr := server.Close(); closeErr != nil && !errors.Is(closeErr, http.ErrServerClosed) {
				result = errors.Join(result, closeErr)
			}
		}
	}

	select {
	case <-terminal:
	case <-ctx.Done():
		if server != nil {
			if closeErr := server.Close(); closeErr != nil && !errors.Is(closeErr, http.ErrServerClosed) {
				result = errors.Join(result, closeErr)
			}
		}
		result = errors.Join(result, ctx.Err())
	}
	return result
}

func (service *Service) markReady(server *http.Server) error {
	service.mu.Lock()
	if service.startupCanceled || service.readyClosed || service.terminalClosedLocked() {
		service.mu.Unlock()
		if server != nil {
			_ = server.Close()
		}
		return context.Canceled
	}
	service.httpServer = server
	service.readyClosed = true
	close(service.ready)
	service.mu.Unlock()
	return nil
}

func (service *Service) cancelStartup(cause error) error {
	if cause == nil {
		cause = context.Canceled
	}
	service.mu.Lock()
	service.startupCanceled = true
	server := service.httpServer
	cancel := service.cancel
	terminal := service.terminal
	service.mu.Unlock()
	if cancel != nil {
		cancel(cause)
	}
	if server != nil {
		_ = server.Close()
	}

	timer := time.NewTimer(service.startupCancelTimeout)
	defer timer.Stop()
	select {
	case <-terminal:
		return cause
	case <-timer.C:
		return errors.Join(cause, errStartupCancellationTimeout)
	}
}

func (service *Service) finish(err error) {
	service.mu.Lock()
	service.terminalErr = err
	close(service.terminal)
	service.mu.Unlock()

	service.doneEvents <- err
	close(service.doneEvents)
}

func (service *Service) terminalError() error {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.terminalErr
}

func (service *Service) terminalClosedLocked() bool {
	select {
	case <-service.terminal:
		return true
	default:
		return false
	}
}
