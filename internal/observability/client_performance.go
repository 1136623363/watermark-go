package observability

import (
	"encoding/json"
	"io"
	"net/http"
	"sync/atomic"
	"time"
)

type PerformanceOptions struct {
	Capacity     int
	MaxBodyBytes int64
}

type PerformanceEvent struct {
	ReceivedAt time.Time       `json:"receivedAt"`
	Payload    json.RawMessage `json:"payload"`
}

type PerformanceCollector struct {
	events       chan PerformanceEvent
	maxBodyBytes int64
	dropped      atomic.Int64
}

func NewPerformanceCollector(options PerformanceOptions) *PerformanceCollector {
	capacity := options.Capacity
	if capacity <= 0 {
		capacity = 128
	}
	maxBodyBytes := options.MaxBodyBytes
	if maxBodyBytes <= 0 {
		maxBodyBytes = 16 << 10
	}
	return &PerformanceCollector{
		events:       make(chan PerformanceEvent, capacity),
		maxBodyBytes: maxBodyBytes,
	}
}

func (collector *PerformanceCollector) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if collector == nil {
		collector = NewPerformanceCollector(PerformanceOptions{})
	}
	if request.Method != http.MethodPost {
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, collector.maxBodyBytes+1))
	if err != nil {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	if int64(len(body)) > collector.maxBodyBytes {
		writer.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}
	event := PerformanceEvent{ReceivedAt: time.Now(), Payload: append([]byte(nil), body...)}
	select {
	case collector.events <- event:
	default:
		collector.dropped.Add(1)
	}
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write([]byte(`{"code":0,"msg":"ok"}`))
}

func (collector *PerformanceCollector) Dropped() int64 {
	if collector == nil {
		return 0
	}
	return collector.dropped.Load()
}
