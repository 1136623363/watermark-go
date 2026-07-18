package cache

import (
	"context"
	"errors"
	"sync"
	"time"
)

type Memory struct {
	mu       sync.Mutex
	items    map[string]memoryEntry
	order    []string
	capacity int
	now      func() time.Time
}

type memoryEntry struct {
	value     []byte
	expiresAt time.Time
}

func NewMemory(capacity int) *Memory {
	if capacity <= 0 {
		capacity = 128
	}
	return &Memory{
		items:    make(map[string]memoryEntry),
		capacity: capacity,
		now:      time.Now,
	}
}

func (memory *Memory) Get(ctx context.Context, key Key) ([]byte, bool, error) {
	if ctx == nil {
		return nil, false, errors.New("cache context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if memory == nil {
		return nil, false, errors.New("nil memory cache")
	}
	memory.mu.Lock()
	defer memory.mu.Unlock()
	entry, ok := memory.items[key.String()]
	if !ok {
		return nil, false, nil
	}
	if !entry.expiresAt.IsZero() && !memory.now().Before(entry.expiresAt) {
		delete(memory.items, key.String())
		return nil, false, nil
	}
	return append([]byte(nil), entry.value...), true, nil
}

func (memory *Memory) Set(ctx context.Context, key Key, value []byte, ttl time.Duration) error {
	if ctx == nil {
		return errors.New("cache context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if memory == nil {
		return errors.New("nil memory cache")
	}
	memory.mu.Lock()
	defer memory.mu.Unlock()
	if _, exists := memory.items[key.String()]; !exists {
		memory.order = append(memory.order, key.String())
	}
	var expiresAt time.Time
	if ttl > 0 {
		expiresAt = memory.now().Add(ttl)
	}
	memory.items[key.String()] = memoryEntry{value: append([]byte(nil), value...), expiresAt: expiresAt}
	memory.evictLocked()
	return nil
}

func (memory *Memory) Delete(ctx context.Context, key Key) error {
	if ctx == nil {
		return errors.New("cache context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if memory == nil {
		return errors.New("nil memory cache")
	}
	memory.mu.Lock()
	defer memory.mu.Unlock()
	delete(memory.items, key.String())
	return nil
}

func (memory *Memory) evictLocked() {
	for len(memory.items) > memory.capacity && len(memory.order) > 0 {
		victim := memory.order[0]
		memory.order = memory.order[1:]
		delete(memory.items, victim)
	}
}
