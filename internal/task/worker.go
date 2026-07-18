package task

import (
	"context"
	"errors"
	"time"
)

var ErrNoTask = errors.New("no task available")

type LeaseStore interface {
	ClaimNext(context.Context, string, time.Time, time.Duration, int) (Task, bool, error)
	RenewLease(context.Context, string, string, time.Time) error
	Complete(context.Context, string, string, []byte) error
	Fail(context.Context, string, string, string, time.Time) error
	RecoverExpired(context.Context, time.Time) (int, error)
}

type Executor interface {
	Execute(context.Context, Task) ([]byte, error)
}

type Worker struct {
	store         LeaseStore
	executor      Executor
	clock         func() time.Time
	workerID      string
	lease         time.Duration
	renewInterval time.Duration
	maxAttempts   int
	backoff       time.Duration
}

func NewWorker(store LeaseStore, executor Executor, optionList ...Option) *Worker {
	config := defaultOptions()
	for _, option := range optionList {
		if option != nil {
			option(&config)
		}
	}
	return &Worker{
		store:         store,
		executor:      executor,
		clock:         config.clock,
		workerID:      config.workerID,
		lease:         config.lease,
		renewInterval: config.renewInterval,
		maxAttempts:   config.maxAttempts,
		backoff:       config.backoff,
	}
}

func (worker *Worker) WorkOne(ctx context.Context) error {
	if worker == nil || worker.store == nil || worker.executor == nil {
		return errors.New("worker is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	task, ok, err := worker.store.ClaimNext(ctx, worker.workerID, worker.clock(), worker.lease, worker.maxAttempts)
	if err != nil || !ok {
		return err
	}
	if worker.renewInterval <= 0 {
		_ = worker.store.RenewLease(ctx, task.ID, worker.workerID, worker.clock().Add(worker.lease))
	}
	result, err := worker.executor.Execute(ctx, task)
	if err != nil {
		retryAt := worker.clock().Add(worker.retryBackoff(task.Attempts))
		return worker.store.Fail(ctx, task.ID, worker.workerID, err.Error(), retryAt)
	}
	return worker.store.Complete(ctx, task.ID, worker.workerID, result)
}

func (worker *Worker) RecoverExpired(ctx context.Context) error {
	if worker == nil || worker.store == nil {
		return errors.New("worker is not configured")
	}
	_, err := worker.store.RecoverExpired(ctx, worker.clock())
	return err
}

func (worker *Worker) retryBackoff(attempt int) time.Duration {
	if attempt <= 1 {
		return worker.backoff
	}
	backoff := worker.backoff
	for index := 1; index < attempt; index++ {
		backoff *= 2
	}
	return backoff
}
