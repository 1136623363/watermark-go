package task

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

const defaultMaxAttempts = 2

const MySQLClaimNextStatement = `
SELECT id, task_id, task_type, payload_json, retry_count, max_attempts, request_id, client_id, created_at, updated_at
FROM parse_tasks
WHERE status = 'pending'
  AND (next_attempt_at IS NULL OR next_attempt_at <= ?)
  AND retry_count < max_attempts
ORDER BY created_at ASC
LIMIT 1
FOR UPDATE SKIP LOCKED`

type options struct {
	clock         func() time.Time
	workerID      string
	lease         time.Duration
	renewInterval time.Duration
	maxAttempts   int
	backoff       time.Duration
}

type Option func(*options)

func WithClock(clock func() time.Time) Option {
	return func(options *options) {
		if clock != nil {
			options.clock = clock
		}
	}
}

func WithWorkerID(id string) Option {
	return func(options *options) {
		if strings.TrimSpace(id) != "" {
			options.workerID = strings.TrimSpace(id)
		}
	}
}

func WithRenewInterval(interval time.Duration) Option {
	return func(options *options) {
		options.renewInterval = interval
	}
}

func WithMaxAttempts(maxAttempts int) Option {
	return func(options *options) {
		if maxAttempts > 0 {
			options.maxAttempts = maxAttempts
		}
	}
}

func defaultOptions() options {
	return options{
		clock:         time.Now,
		workerID:      "worker",
		lease:         15 * time.Second,
		renewInterval: 5 * time.Second,
		maxAttempts:   defaultMaxAttempts,
		backoff:       2 * time.Second,
	}
}

type CreateRequest struct {
	ID          string
	Type        string
	Payload     []byte
	RequestID   string
	ClientID    string
	MaxAttempts int
}

type MemoryStore struct {
	mu        sync.Mutex
	clock     func() time.Time
	sequence  int64
	tasks     map[string]Task
	order     []string
	idemIndex map[string]string
	renewals  map[string]int
}

func NewMemoryStore(optionList ...Option) *MemoryStore {
	config := defaultOptions()
	for _, option := range optionList {
		if option != nil {
			option(&config)
		}
	}
	return &MemoryStore{
		clock:     config.clock,
		tasks:     make(map[string]Task),
		idemIndex: make(map[string]string),
		renewals:  make(map[string]int),
	}
}

func (store *MemoryStore) Create(_ context.Context, request CreateRequest) (Task, error) {
	if store == nil {
		return Task{}, errors.New("nil task store")
	}
	request.ID = strings.TrimSpace(request.ID)
	request.Type = strings.TrimSpace(request.Type)
	if request.ID == "" || request.Type == "" {
		return Task{}, errors.New("task identity is incomplete")
	}
	maxAttempts := request.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = defaultMaxAttempts
	}
	now := store.clock()
	store.mu.Lock()
	defer store.mu.Unlock()
	if key := idempotencyKey(request.Type, request.RequestID, request.ClientID); key != "" {
		if existingID := store.idemIndex[key]; existingID != "" {
			return cloneTask(store.tasks[existingID]), nil
		}
	}
	task := Task{
		ID:            request.ID,
		Type:          request.Type,
		Status:        Pending,
		Payload:       append([]byte(nil), request.Payload...),
		MaxAttempts:   maxAttempts,
		NextAttemptAt: now,
		RequestID:     strings.TrimSpace(request.RequestID),
		ClientID:      strings.TrimSpace(request.ClientID),
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	store.tasks[task.ID] = task
	store.order = append(store.order, task.ID)
	if key := idempotencyKey(request.Type, request.RequestID, request.ClientID); key != "" {
		store.idemIndex[key] = task.ID
	}
	return cloneTask(task), nil
}

func (store *MemoryStore) InsertPending(ctx context.Context, taskType string, payload []byte) string {
	id := store.nextID("task")
	task, err := store.Create(ctx, CreateRequest{ID: id, Type: taskType, Payload: payload})
	if err != nil {
		return ""
	}
	return task.ID
}

func (store *MemoryStore) InsertRunningWithLease(ctx context.Context, taskType string, lockedUntil time.Time) string {
	id := store.InsertPending(ctx, taskType, []byte(`{}`))
	store.mu.Lock()
	defer store.mu.Unlock()
	task := store.tasks[id]
	task.Status = Running
	task.LockedBy = "previous-worker"
	task.LockedUntil = lockedUntil
	task.StartedAt = store.clock()
	task.UpdatedAt = task.StartedAt
	store.tasks[id] = task
	return id
}

func (store *MemoryStore) InsertLegacyQueued(ctx context.Context, taskType string) string {
	id := store.InsertPending(ctx, taskType, []byte(`{}`))
	store.mu.Lock()
	defer store.mu.Unlock()
	task := store.tasks[id]
	task.Status = Queued
	store.tasks[id] = task
	return id
}

func (store *MemoryStore) ClaimNext(_ context.Context, workerID string, now time.Time, lease time.Duration, maxAttempts int) (Task, bool, error) {
	if store == nil {
		return Task{}, false, errors.New("nil task store")
	}
	if now.IsZero() {
		now = store.clock()
	}
	if lease <= 0 {
		lease = 15 * time.Second
	}
	if maxAttempts <= 0 {
		maxAttempts = defaultMaxAttempts
	}
	workerID = strings.TrimSpace(workerID)
	if workerID == "" {
		workerID = "worker"
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, id := range store.order {
		task := store.tasks[id]
		if task.Status != Pending {
			continue
		}
		if !task.NextAttemptAt.IsZero() && task.NextAttemptAt.After(now) {
			continue
		}
		if task.MaxAttempts <= 0 {
			task.MaxAttempts = maxAttempts
		}
		if task.Attempts >= task.MaxAttempts {
			task.Status = Failed
			task.UpdatedAt = now
			store.tasks[id] = task
			continue
		}
		task.Status = Running
		task.Attempts++
		task.LockedBy = workerID
		task.LockedUntil = now.Add(lease)
		if task.StartedAt.IsZero() {
			task.StartedAt = now
		}
		task.UpdatedAt = now
		store.tasks[id] = task
		return cloneTask(task), true, nil
	}
	return Task{}, false, nil
}

func (store *MemoryStore) RenewLease(_ context.Context, id, workerID string, until time.Time) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	task, ok := store.tasks[id]
	if !ok || task.Status != Running || task.LockedBy != workerID {
		return errors.New("task lease not owned")
	}
	task.LockedUntil = until
	task.UpdatedAt = store.clock()
	store.tasks[id] = task
	store.renewals[id]++
	return nil
}

func (store *MemoryStore) Complete(_ context.Context, id, workerID string, result []byte) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	task, ok := store.tasks[id]
	if !ok || task.Status != Running || task.LockedBy != workerID {
		return errors.New("task lease not owned")
	}
	now := store.clock()
	task.Status = Completed
	task.Result = append([]byte(nil), result...)
	task.LockedBy = ""
	task.LockedUntil = time.Time{}
	task.FinishedAt = now
	task.UpdatedAt = now
	store.tasks[id] = task
	return nil
}

func (store *MemoryStore) Fail(_ context.Context, id, workerID, message string, retryAt time.Time) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	task, ok := store.tasks[id]
	if !ok || task.Status != Running || task.LockedBy != workerID {
		return errors.New("task lease not owned")
	}
	now := store.clock()
	task.ErrorMessage = strings.TrimSpace(message)
	task.LockedBy = ""
	task.LockedUntil = time.Time{}
	if task.Attempts >= task.MaxAttempts {
		task.Status = Failed
		task.FinishedAt = now
	} else {
		task.Status = Pending
		task.NextAttemptAt = retryAt
	}
	task.UpdatedAt = now
	store.tasks[id] = task
	return nil
}

func (store *MemoryStore) RecoverExpired(_ context.Context, now time.Time) (int, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	count := 0
	for id, task := range store.tasks {
		if task.Status != Running || task.LockedUntil.IsZero() || task.LockedUntil.After(now) {
			continue
		}
		task.Status = Pending
		task.LockedBy = ""
		task.LockedUntil = time.Time{}
		task.NextAttemptAt = now
		task.UpdatedAt = now
		store.tasks[id] = task
		count++
	}
	return count, nil
}

func (store *MemoryStore) Get(_ context.Context, id string) (Task, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	task, ok := store.tasks[strings.TrimSpace(id)]
	return cloneTask(task), ok, nil
}

func (store *MemoryStore) Status(id string) Status {
	store.mu.Lock()
	defer store.mu.Unlock()
	return NormalizeStatus(store.tasks[id].Status)
}

func (store *MemoryStore) RawStatus(id string) Status {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.tasks[id].Status
}

func (store *MemoryStore) Snapshot(id string) (Snapshot, bool) {
	task, ok, _ := store.Get(context.Background(), id)
	if !ok {
		return Snapshot{}, false
	}
	return task.Snapshot(), true
}

func (store *MemoryStore) NextAttemptAt(id string) time.Time {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.tasks[id].NextAttemptAt
}

func (store *MemoryStore) LeaseRenewCount(id string) int {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.renewals[id]
}

func (store *MemoryStore) Count() int {
	store.mu.Lock()
	defer store.mu.Unlock()
	return len(store.tasks)
}

func (store *MemoryStore) nextID(prefix string) string {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.sequence++
	return fmt.Sprintf("%s_%06d", prefix, store.sequence)
}

func idempotencyKey(taskType, requestID, clientID string) string {
	taskType = strings.TrimSpace(taskType)
	requestID = strings.TrimSpace(requestID)
	clientID = strings.TrimSpace(clientID)
	if taskType == "" || requestID == "" || clientID == "" {
		return ""
	}
	return taskType + "\x00" + requestID + "\x00" + clientID
}

func cloneTask(task Task) Task {
	task.Payload = append([]byte(nil), task.Payload...)
	task.Result = append([]byte(nil), task.Result...)
	return task
}
