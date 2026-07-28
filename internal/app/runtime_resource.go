package app

import (
	"context"
	"errors"
	"io"
	"sync"
)

type runtimeResourceComponent struct {
	closers []io.Closer
	done    chan error
	once    sync.Once
}

func newRuntimeResourceComponent(closers ...io.Closer) *runtimeResourceComponent {
	filtered := make([]io.Closer, 0, len(closers))
	for _, closer := range closers {
		if closer != nil {
			filtered = append(filtered, closer)
		}
	}
	return &runtimeResourceComponent{
		closers: filtered,
		done:    make(chan error, 1),
	}
}

func (component *runtimeResourceComponent) Start(context.Context) error {
	if component == nil {
		return errors.New("nil runtime resource component")
	}
	return nil
}

func (component *runtimeResourceComponent) Done() <-chan error {
	if component == nil {
		done := make(chan error)
		close(done)
		return done
	}
	return component.done
}

func (component *runtimeResourceComponent) Stop(context.Context) error {
	if component == nil {
		return nil
	}
	var result error
	component.once.Do(func() {
		for index := len(component.closers) - 1; index >= 0; index-- {
			if err := component.closers[index].Close(); err != nil && result == nil {
				result = err
			}
		}
		component.done <- result
		close(component.done)
	})
	return result
}

func closeRuntimeClosers(closers []io.Closer) {
	for index := len(closers) - 1; index >= 0; index-- {
		if closers[index] != nil {
			_ = closers[index].Close()
		}
	}
}
