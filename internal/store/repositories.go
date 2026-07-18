package store

import (
	"context"
	"time"
)

type ParseResult struct {
	ShareID       string
	URLHash       string
	SourceURLHash string
	Platform      string
	ResultType    string
	ResultJSON    []byte
	UpdatedAt     time.Time
}

type ParseResultRepository interface {
	UpsertParseResult(context.Context, ParseResult) error
	FindParseResultByShareID(context.Context, string) (ParseResult, bool, error)
}

type TaskLease struct {
	TaskID      string
	LockedBy    string
	LockedUntil time.Time
}

type TaskRepository interface {
	AcquireDueTask(context.Context, string, time.Time) (TaskLease, bool, error)
	ReleaseTask(context.Context, TaskLease) error
}
