package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/1136623363/watermark-go/internal/config"
)

func TestServiceReportsPostReadyTerminalFailure(t *testing.T) {
	terminalErr := errors.New("serve failed after ready")
	fail := make(chan struct{})
	service := newService(
		config.Config{},
		func(_ context.Context, _ config.Config, ready func(*http.Server) error) error {
			if err := ready(nil); err != nil {
				return err
			}
			<-fail
			return terminalErr
		},
		50*time.Millisecond,
	)
	if err := service.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	close(fail)
	select {
	case err := <-service.Done():
		if !errors.Is(err, terminalErr) {
			t.Fatalf("Done() error = %v, want terminal failure", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("service did not publish its post-ready terminal failure")
	}
}

func TestServiceStartupCancellationBeforeReadyIsBounded(t *testing.T) {
	entered := make(chan struct{})
	service := newService(
		config.Config{},
		func(ctx context.Context, _ config.Config, _ func(*http.Server) error) error {
			close(entered)
			<-ctx.Done()
			return ctx.Err()
		},
		50*time.Millisecond,
	)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- service.Start(ctx) }()
	<-entered
	started := time.Now()
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Start() error = %v, want cancellation", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Start() waited without bound after startup cancellation")
	}
	if elapsed := time.Since(started); elapsed > 150*time.Millisecond {
		t.Fatalf("startup cancellation took %s", elapsed)
	}
}

func TestServiceDoesNotLaunchRunnerForAlreadyCanceledStartup(t *testing.T) {
	invoked := make(chan struct{}, 1)
	service := newService(
		config.Config{},
		func(context.Context, config.Config, func(*http.Server) error) error {
			invoked <- struct{}{}
			return nil
		},
		50*time.Millisecond,
	)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := service.Start(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Start() error = %v, want cancellation", err)
	}
	select {
	case <-invoked:
		t.Fatal("already-canceled startup launched a runner that could bind a listener")
	default:
	}
}

func TestServiceRejectsLateReadyAfterBoundedStartupCancellation(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	readyResult := make(chan error, 1)
	service := newService(
		config.Config{},
		func(_ context.Context, _ config.Config, ready func(*http.Server) error) error {
			close(entered)
			<-release
			err := ready(nil)
			readyResult <- err
			return err
		},
		20*time.Millisecond,
	)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- service.Start(ctx) }()
	<-entered
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Start() error = %v, want cancellation", err)
		}
	case <-time.After(150 * time.Millisecond):
		t.Fatal("Start() did not honor its bounded startup-cancel budget")
	}
	close(release)
	select {
	case err := <-readyResult:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("late ready error = %v, want cancellation", err)
		}
	case <-time.After(150 * time.Millisecond):
		t.Fatal("late ready callback was not rejected")
	}
}

func TestServiceProcessCancellationAfterReadyRequiresAndHonorsStop(t *testing.T) {
	listenerAddress := make(chan string, 1)
	service := newService(
		config.Config{},
		func(ctx context.Context, _ config.Config, ready func(*http.Server) error) error {
			listener, err := listenHTTP(ctx, "127.0.0.1:0")
			if err != nil {
				return err
			}
			defer listener.Close()
			server := &http.Server{Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})}
			listenerAddress <- listener.Addr().String()
			if err := ready(server); err != nil {
				return err
			}
			if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return nil
		},
		50*time.Millisecond,
	)
	processCtx, cancelProcess := context.WithCancel(context.Background())
	if err := service.Start(processCtx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	address := <-listenerAddress
	cancelProcess()
	connection, err := net.DialTimeout("tcp", address, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("process cancellation bypassed app shutdown ordering: %v", err)
	}
	_ = connection.Close()
	stopCtx, cancelStop := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancelStop()
	if err := service.Stop(stopCtx); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if connection, err := net.DialTimeout("tcp", address, 20*time.Millisecond); err == nil {
		_ = connection.Close()
		t.Fatal("Stop() left the post-ready listener bound")
	}
}

func TestServiceStopIsIdempotent(t *testing.T) {
	service := newService(
		config.Config{},
		func(ctx context.Context, _ config.Config, ready func(*http.Server) error) error {
			if err := ready(nil); err != nil {
				return err
			}
			<-ctx.Done()
			return ctx.Err()
		},
		50*time.Millisecond,
	)
	if err := service.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		stopCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		err := service.Stop(stopCtx)
		cancel()
		if err != nil {
			t.Fatalf("Stop() attempt %d error = %v", attempt+1, err)
		}
	}
}

func TestServiceCanceledStartupCannotBindAListenerLater(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	bound := make(chan bool, 1)
	service := newService(
		config.Config{},
		func(ctx context.Context, _ config.Config, _ func(*http.Server) error) error {
			close(entered)
			<-release
			listener, err := listenHTTP(ctx, "127.0.0.1:0")
			if err != nil {
				bound <- false
				return err
			}
			bound <- true
			_ = listener.Close()
			return nil
		},
		20*time.Millisecond,
	)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- service.Start(ctx) }()
	<-entered
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Start() error = %v, want cancellation", err)
		}
	case <-time.After(150 * time.Millisecond):
		t.Fatal("Start() did not return within its cancellation budget")
	}
	close(release)
	select {
	case didBind := <-bound:
		if didBind {
			t.Fatal("canceled startup bound a listener after Start returned")
		}
	case <-time.After(150 * time.Millisecond):
		t.Fatal("canceled startup runner did not terminate after release")
	}
}
