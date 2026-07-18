package observability

import (
	"encoding/json"
	"io"
	"sync"
	"time"
)

type Event struct {
	RequestID string
	TaskID    string
	Platform  string
	Parser    string
	Stage     string
	Attempt   int
	Cache     string
	Fallback  bool
	ErrorKind string
	Duration  time.Duration

	RawURL  string
	Headers map[string]string
	Error   string
}

type JSONLogger struct {
	mu     sync.Mutex
	writer io.Writer
}

func NewJSONLogger(writer io.Writer) *JSONLogger {
	return &JSONLogger{writer: writer}
}

func (logger *JSONLogger) Log(event Event) {
	if logger == nil || logger.writer == nil {
		return
	}
	record := map[string]any{
		"requestId":  event.RequestID,
		"taskId":     event.TaskID,
		"platform":   event.Platform,
		"parser":     event.Parser,
		"stage":      event.Stage,
		"attempt":    event.Attempt,
		"cache":      event.Cache,
		"fallback":   event.Fallback,
		"errorKind":  event.ErrorKind,
		"durationMs": event.Duration.Milliseconds(),
	}
	logger.mu.Lock()
	defer logger.mu.Unlock()
	_ = json.NewEncoder(logger.writer).Encode(record)
}
