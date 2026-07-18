package parse

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/1136623363/watermark-go/internal/task"
)

type TaskMeta struct {
	RequestID string
	ClientID  string
}

type TaskView struct {
	TaskID    string      `json:"taskId"`
	Status    string      `json:"status"`
	Progress  int         `json:"progress"`
	PollURL   string      `json:"pollUrl"`
	RequestID string      `json:"requestId"`
	Result    *CompatData `json:"result,omitempty"`
}

type AsyncTaskStore interface {
	Create(context.Context, task.CreateRequest) (task.Task, error)
	Get(context.Context, string) (task.Task, bool, error)
}

type AsyncTaskDependencies struct {
	Store       AsyncTaskStore
	Entropy     io.Reader
	Clock       func() time.Time
	PollBaseURL string
}

type AsyncTasks struct {
	store       AsyncTaskStore
	entropy     io.Reader
	clock       func() time.Time
	pollBaseURL string
}

func NewAsyncTasks(dependencies AsyncTaskDependencies) *AsyncTasks {
	clock := dependencies.Clock
	if clock == nil {
		clock = time.Now
	}
	pollBaseURL := strings.TrimRight(strings.TrimSpace(dependencies.PollBaseURL), "/")
	if pollBaseURL == "" {
		pollBaseURL = "/api/parse/task"
	}
	return &AsyncTasks{
		store:       dependencies.Store,
		entropy:     dependencies.Entropy,
		clock:       clock,
		pollBaseURL: pollBaseURL,
	}
}

func (tasks *AsyncTasks) Submit(ctx context.Context, request Request, meta TaskMeta) (TaskView, error) {
	if tasks == nil || tasks.store == nil {
		return TaskView{}, NewError(ErrorInternal, StageStore, "", true)
	}
	id, err := task.GenerateID(tasks.entropy)
	if err != nil {
		return TaskView{}, err
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return TaskView{}, NewError(ErrorInvalidInput, StageInput, "", false)
	}
	meta.RequestID = strings.TrimSpace(meta.RequestID)
	if meta.RequestID == "" {
		meta.RequestID = id
	}
	created, err := tasks.store.Create(ctx, task.CreateRequest{
		ID:        id,
		Type:      "parse",
		Payload:   payload,
		RequestID: meta.RequestID,
		ClientID:  strings.TrimSpace(meta.ClientID),
	})
	if err != nil {
		return TaskView{}, err
	}
	return tasks.view(created.Snapshot()), nil
}

func (tasks *AsyncTasks) Get(ctx context.Context, id string) (TaskView, bool, error) {
	if tasks == nil || tasks.store == nil {
		return TaskView{}, false, NewError(ErrorInternal, StageStore, "", true)
	}
	found, ok, err := tasks.store.Get(ctx, strings.TrimSpace(id))
	if err != nil || !ok {
		return TaskView{}, ok, err
	}
	return tasks.view(found.Snapshot()), true, nil
}

func (tasks *AsyncTasks) view(snapshot task.Snapshot) TaskView {
	view := TaskView{
		TaskID:    snapshot.TaskID,
		Status:    string(task.NormalizeStatus(snapshot.Status)),
		Progress:  snapshot.Progress,
		PollURL:   tasks.pollBaseURL + "/" + snapshot.TaskID,
		RequestID: snapshot.RequestID,
	}
	if view.RequestID == "" {
		view.RequestID = snapshot.TaskID
	}
	if task.NormalizeStatus(snapshot.Status) == task.Completed && len(snapshot.Result) > 0 {
		var data CompatData
		if err := json.Unmarshal(snapshot.Result, &data); err == nil {
			view.Result = &data
		}
	}
	return view
}

type TaskParser interface {
	Parse(context.Context, Request) (ParseOutput, error)
}

type TaskExecutor struct {
	Parser TaskParser
}

func (executor TaskExecutor) Execute(ctx context.Context, item task.Task) ([]byte, error) {
	if executor.Parser == nil {
		return nil, errors.New("parse executor is unavailable")
	}
	var request Request
	if err := json.Unmarshal(item.Payload, &request); err != nil {
		return nil, NewError(ErrorInvalidInput, StageInput, "", false)
	}
	output, err := executor.Parser.Parse(ctx, request)
	if err != nil {
		return nil, err
	}
	return json.Marshal(output.Data)
}
