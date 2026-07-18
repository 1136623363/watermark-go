package task

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestWorkerRenewsLeaseAndDoesNotExecuteTwice(t *testing.T) {
	clock := newManualClock(time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC))
	store := NewMemoryStore(WithClock(clock.Now))
	id := store.InsertPending(context.Background(), "parse", []byte(`{"url":"https://example.com/v"}`))
	executor := &recordingExecutor{result: []byte(`{"ok":true}`)}
	workerOne := NewWorker(store, executor, WithClock(clock.Now), WithWorkerID("worker-one"), WithRenewInterval(0))
	workerTwo := NewWorker(store, executor, WithClock(clock.Now), WithWorkerID("worker-two"), WithRenewInterval(0))

	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		_ = workerOne.WorkOne(context.Background())
	}()
	go func() {
		defer wait.Done()
		_ = workerTwo.WorkOne(context.Background())
	}()
	wait.Wait()

	if got := store.Status(id); got != Completed {
		t.Fatalf("task status = %s, want completed", got)
	}
	if calls := executor.Calls(id); calls != 1 {
		t.Fatalf("executor calls = %d, want 1", calls)
	}
	if renewals := store.LeaseRenewCount(id); renewals == 0 {
		t.Fatal("worker did not renew the lease")
	}
}

func TestExpiredRunningTaskReturnsToPendingAfterRestart(t *testing.T) {
	clock := newManualClock(time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC))
	store := NewMemoryStore(WithClock(clock.Now))
	id := store.InsertRunningWithLease(context.Background(), "parse", clock.Now().Add(-time.Second))
	worker := NewWorker(store, &recordingExecutor{}, WithClock(clock.Now))

	if err := worker.RecoverExpired(context.Background()); err != nil {
		t.Fatalf("RecoverExpired() error = %v", err)
	}
	if got := store.Status(id); got != Pending {
		t.Fatalf("task status = %s, want pending", got)
	}
}

func TestWorkerRetriesWithBackoffAndThenFails(t *testing.T) {
	clock := newManualClock(time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC))
	store := NewMemoryStore(WithClock(clock.Now))
	id := store.InsertPending(context.Background(), "parse", []byte(`{"url":"https://example.com/v"}`))
	executor := &recordingExecutor{err: errors.New("temporary upstream failure")}
	worker := NewWorker(store, executor, WithClock(clock.Now), WithWorkerID("worker"), WithRenewInterval(0), WithMaxAttempts(2))

	if err := worker.WorkOne(context.Background()); err != nil {
		t.Fatalf("first WorkOne() error = %v", err)
	}
	if got := store.Status(id); got != Pending {
		t.Fatalf("status after first failure = %s, want pending", got)
	}
	if next := store.NextAttemptAt(id); !next.After(clock.Now()) {
		t.Fatalf("next attempt = %s, want exponential backoff after now", next)
	}
	clock.Advance(3 * time.Second)
	if err := worker.WorkOne(context.Background()); err != nil {
		t.Fatalf("second WorkOne() error = %v", err)
	}
	if got := store.Status(id); got != Failed {
		t.Fatalf("status after max attempts = %s, want failed", got)
	}
}

func TestQueuedStatusIsReadAsPendingButNeverWrittenAsCanonical(t *testing.T) {
	store := NewMemoryStore()
	id := store.InsertLegacyQueued(context.Background(), "parse")
	snapshot, ok := store.Snapshot(id)
	if !ok {
		t.Fatal("legacy queued task missing")
	}
	if snapshot.Status != Pending {
		t.Fatalf("snapshot status = %s, want pending", snapshot.Status)
	}
	if raw := store.RawStatus(id); raw != Queued {
		t.Fatalf("raw legacy status = %s, want queued fixture", raw)
	}
	created := store.InsertPending(context.Background(), "parse", []byte(`{}`))
	if raw := store.RawStatus(created); raw == Queued {
		t.Fatal("new task wrote queued as canonical status")
	}
}

type recordingExecutor struct {
	mu     sync.Mutex
	result []byte
	err    error
	calls  map[string]int
}

func (executor *recordingExecutor) Execute(_ context.Context, task Task) ([]byte, error) {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	if executor.calls == nil {
		executor.calls = make(map[string]int)
	}
	executor.calls[task.ID]++
	if executor.err != nil {
		return nil, executor.err
	}
	return append([]byte(nil), executor.result...), nil
}

func (executor *recordingExecutor) Calls(id string) int {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	return executor.calls[id]
}

type manualClock struct {
	mu  sync.Mutex
	now time.Time
}

func newManualClock(now time.Time) *manualClock {
	return &manualClock{now: now}
}

func (clock *manualClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *manualClock) Advance(delta time.Duration) {
	clock.mu.Lock()
	clock.now = clock.now.Add(delta)
	clock.mu.Unlock()
}
