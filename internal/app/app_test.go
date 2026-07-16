package app

import (
	"context"
	"errors"
	"reflect"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/1136623363/watermark-go/internal/config"
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
