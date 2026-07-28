package app

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/1136623363/watermark-go/internal/task"
)

type runtimeTaskWorkerOptions struct {
	PollInterval time.Duration
	Concurrency  int
}

type runtimeTaskWorkerComponent struct {
	worker      *task.Worker
	options     runtimeTaskWorkerOptions
	done        chan error
	cancel      context.CancelFunc
	startOnce   sync.Once
	stopOnce    sync.Once
	workersDone sync.WaitGroup
	mu          sync.Mutex
	err         error
}

func newRuntimeTaskWorkerComponent(worker *task.Worker, options runtimeTaskWorkerOptions) *runtimeTaskWorkerComponent {
	if options.PollInterval <= 0 {
		options.PollInterval = 500 * time.Millisecond
	}
	if options.Concurrency <= 0 {
		options.Concurrency = 1
	}
	return &runtimeTaskWorkerComponent{
		worker:  worker,
		options: options,
		done:    make(chan error, 1),
	}
}

func (component *runtimeTaskWorkerComponent) Start(ctx context.Context) error {
	if component == nil || component.worker == nil {
		return errors.New("runtime task worker is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var startErr error
	component.startOnce.Do(func() {
		workerCtx, cancel := context.WithCancel(ctx)
		component.cancel = cancel
		component.workersDone.Add(component.options.Concurrency)
		for index := 0; index < component.options.Concurrency; index++ {
			go component.loop(workerCtx)
		}
		go component.finishWhenWorkersExit()
	})
	return startErr
}

func (component *runtimeTaskWorkerComponent) Done() <-chan error {
	if component == nil {
		done := make(chan error)
		close(done)
		return done
	}
	return component.done
}

func (component *runtimeTaskWorkerComponent) Stop(ctx context.Context) error {
	if component == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	component.stopOnce.Do(func() {
		if component.cancel != nil {
			component.cancel()
		}
	})
	select {
	case err := <-component.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (component *runtimeTaskWorkerComponent) loop(ctx context.Context) {
	defer component.workersDone.Done()
	ticker := time.NewTicker(component.options.PollInterval)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return
		}
		if err := component.worker.RecoverExpired(ctx); err != nil {
			component.fail(err)
			return
		}
		if err := component.worker.WorkOne(ctx); err != nil {
			component.fail(err)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (component *runtimeTaskWorkerComponent) fail(err error) {
	if err == nil {
		return
	}
	component.mu.Lock()
	if component.err == nil {
		component.err = err
		if component.cancel != nil {
			component.cancel()
		}
	}
	component.mu.Unlock()
}

func (component *runtimeTaskWorkerComponent) finishWhenWorkersExit() {
	component.workersDone.Wait()
	component.mu.Lock()
	err := component.err
	component.mu.Unlock()
	component.done <- err
	close(component.done)
}
