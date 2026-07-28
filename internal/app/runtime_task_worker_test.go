package app

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	parseusecase "github.com/1136623363/watermark-go/internal/parse"
	"github.com/1136623363/watermark-go/internal/task"
)

func TestRuntimeTaskWorkerComponentCompletesPendingParseTask(t *testing.T) {
	store := task.NewMemoryStore()
	payload, err := json.Marshal(parseusecase.Request{URL: "https://example.com/video"})
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.Create(context.Background(), task.CreateRequest{
		ID:      "parse_worker_component",
		Type:    "parse",
		Payload: payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	worker := task.NewWorker(store, parseusecase.TaskExecutor{Parser: runtimeWorkerParserStub{}}, task.WithWorkerID("runtime-worker-test"))
	component := newRuntimeTaskWorkerComponent(worker, runtimeTaskWorkerOptions{
		PollInterval: time.Millisecond,
		Concurrency:  1,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := component.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer component.Stop(context.Background())

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		snapshot, ok := store.Snapshot(created.ID)
		if ok && snapshot.Status == task.Completed && len(snapshot.Result) > 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("task was not completed by runtime worker; status=%s", store.Status(created.ID))
}

type runtimeWorkerParserStub struct{}

func (runtimeWorkerParserStub) Parse(context.Context, parseusecase.Request) (parseusecase.ParseOutput, error) {
	return parseusecase.ParseOutput{
		Data: parseusecase.CompatData{
			Platform: "stub",
			Type:     "video",
			Title:    "completed by runtime worker",
			PlayAddr: "https://media.example/video.mp4",
		},
	}, nil
}
