package task

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"time"
)

type Status string

const (
	Pending   Status = "pending"
	Running   Status = "running"
	Completed Status = "completed"
	Failed    Status = "failed"
	Expired   Status = "expired"
	Queued    Status = "queued"
)

var ErrEntropyUnavailable = errors.New("task id entropy unavailable")

type Task struct {
	ID            string
	Type          string
	Status        Status
	Payload       []byte
	Result        []byte
	ErrorMessage  string
	Attempts      int
	MaxAttempts   int
	LockedBy      string
	LockedUntil   time.Time
	NextAttemptAt time.Time
	RequestID     string
	ClientID      string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	StartedAt     time.Time
	FinishedAt    time.Time
}

type Snapshot struct {
	TaskID       string
	Type         string
	Status       Status
	Progress     int
	Result       []byte
	ErrorMessage string
	RequestID    string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func NormalizeStatus(status Status) Status {
	switch status {
	case Queued:
		return Pending
	case Pending, Running, Completed, Failed, Expired:
		return status
	default:
		return Pending
	}
}

func (task Task) Snapshot() Snapshot {
	status := NormalizeStatus(task.Status)
	progress := 0
	switch status {
	case Running:
		progress = 50
	case Completed, Failed, Expired:
		progress = 100
	}
	return Snapshot{
		TaskID:       task.ID,
		Type:         task.Type,
		Status:       status,
		Progress:     progress,
		Result:       append([]byte(nil), task.Result...),
		ErrorMessage: task.ErrorMessage,
		RequestID:    task.RequestID,
		CreatedAt:    task.CreatedAt,
		UpdatedAt:    task.UpdatedAt,
	}
}

func GenerateID(reader io.Reader) (string, error) {
	if reader == nil {
		reader = rand.Reader
	}
	raw := make([]byte, 16)
	if _, err := io.ReadFull(reader, raw); err != nil {
		return "", ErrEntropyUnavailable
	}
	return "parse_" + base64.RawURLEncoding.EncodeToString(raw), nil
}
