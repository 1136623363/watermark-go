package app

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/1136623363/watermark-go/internal/config"
	"github.com/1136623363/watermark-go/internal/server"
)

const DefaultShutdownTimeout = 20 * time.Second

type Component interface {
	Start(context.Context) error
	Done() <-chan error
	Stop(context.Context) error
}

type Option func(*options) error

type options struct {
	components      []Component
	shutdownTimeout time.Duration
}

type App struct {
	components      []Component
	doneChannels    []<-chan error
	shutdownTimeout time.Duration
}

type startedComponent struct {
	index     int
	component Component
	done      <-chan error
}

type lifecycleEventKind uint8

const (
	lifecycleStartCompleted lifecycleEventKind = iota
	lifecycleComponentTerminated
	lifecycleProcessCanceled
)

type lifecycleEvent struct {
	kind           lifecycleEventKind
	componentIndex int
	err            error
	startCompleted bool
	startErr       error
}

func New(cfg config.Config, supplied ...Option) (*App, error) {
	settings := options{
		components:      []Component{server.New(cfg)},
		shutdownTimeout: DefaultShutdownTimeout,
	}
	for _, option := range supplied {
		if option == nil {
			return nil, errors.New("nil app option")
		}
		if err := option(&settings); err != nil {
			return nil, err
		}
	}
	if settings.shutdownTimeout <= 0 {
		return nil, errors.New("shutdown timeout must be positive")
	}
	doneChannels := make([]<-chan error, len(settings.components))
	for index, component := range settings.components {
		if component == nil {
			return nil, errors.New("nil app component")
		}
		doneChannels[index] = component.Done()
		if doneChannels[index] == nil {
			return nil, errors.New("app component has no terminal event channel")
		}
	}
	return &App{
		components:      append([]Component(nil), settings.components...),
		doneChannels:    doneChannels,
		shutdownTimeout: settings.shutdownTimeout,
	}, nil
}

func WithComponents(components ...Component) Option {
	return func(settings *options) error {
		settings.components = append([]Component(nil), components...)
		return nil
	}
}

func WithShutdownTimeout(timeout time.Duration) Option {
	return func(settings *options) error {
		if timeout <= 0 {
			return errors.New("shutdown timeout must be positive")
		}
		settings.shutdownTimeout = timeout
		return nil
	}
}

func (application *App) Run(ctx context.Context) error {
	if application == nil {
		return errors.New("nil app")
	}
	if ctx == nil {
		return errors.New("nil run context")
	}

	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()

	started := make([]startedComponent, 0, len(application.components))
	for index, component := range application.components {
		startResult := make(chan error, 1)
		go func(component Component) {
			startResult <- component.Start(runCtx)
		}(component)

		event := waitLifecycleEvent(ctx, startResult, started)
		current := startedComponent{index: index, component: component, done: application.doneChannels[index]}
		switch event.kind {
		case lifecycleStartCompleted:
			if event.err != nil {
				cancelRun()
				shutdownErr := application.stopWithNewBudget(started)
				return errors.Join(fmt.Errorf("start component %d: %w", index, event.err), shutdownErr)
			}
			started = append(started, current)
		case lifecycleComponentTerminated:
			cancelRun()
			shutdownErr := application.settleStartAndStop(started, current, startResult, event)
			return errors.Join(componentTerminalError(event), shutdownErr)
		case lifecycleProcessCanceled:
			cancelRun()
			return application.settleStartAndStop(started, current, startResult, event)
		default:
			cancelRun()
			return errors.New("invalid application lifecycle event")
		}
	}

	event := waitLifecycleEvent(ctx, nil, started)
	cancelRun()
	switch event.kind {
	case lifecycleComponentTerminated:
		return errors.Join(componentTerminalError(event), application.stopWithNewBudget(started))
	case lifecycleProcessCanceled:
		return application.stopWithNewBudget(started)
	default:
		return errors.New("invalid application lifecycle event")
	}
}

func waitLifecycleEvent(ctx context.Context, startResult <-chan error, started []startedComponent) lifecycleEvent {
	if event, ok := pollTerminalEvent(started); ok {
		return event
	}
	if err := ctx.Err(); err != nil {
		if event, ok := pollTerminalEvent(started); ok {
			return event
		}
		return lifecycleEvent{kind: lifecycleProcessCanceled, err: err}
	}
	if startResult != nil {
		select {
		case err := <-startResult:
			if event, ok := pollTerminalEvent(started); ok {
				event.startCompleted = true
				event.startErr = err
				return event
			}
			if processErr := ctx.Err(); processErr != nil {
				return lifecycleEvent{
					kind:           lifecycleProcessCanceled,
					err:            processErr,
					startCompleted: true,
					startErr:       err,
				}
			}
			return lifecycleEvent{kind: lifecycleStartCompleted, err: err, startCompleted: true, startErr: err}
		default:
		}
	}

	cases := make([]reflect.SelectCase, 0, len(started)+2)
	for _, component := range started {
		cases = append(cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(component.done)})
	}
	startCase := -1
	if startResult != nil {
		startCase = len(cases)
		cases = append(cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(startResult)})
	}
	processCase := len(cases)
	cases = append(cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(ctx.Done())})

	chosen, received, open := reflect.Select(cases)
	if chosen < len(started) {
		return lifecycleEvent{
			kind:           lifecycleComponentTerminated,
			componentIndex: started[chosen].index,
			err:            reflectedError(received, open),
		}
	}
	if chosen == startCase {
		startErr := reflectedError(received, open)
		if event, ok := pollTerminalEvent(started); ok {
			event.startCompleted = true
			event.startErr = startErr
			return event
		}
		if processErr := ctx.Err(); processErr != nil {
			return lifecycleEvent{
				kind:           lifecycleProcessCanceled,
				err:            processErr,
				startCompleted: true,
				startErr:       startErr,
			}
		}
		return lifecycleEvent{kind: lifecycleStartCompleted, err: startErr, startCompleted: true, startErr: startErr}
	}
	if chosen == processCase {
		if event, ok := pollTerminalEvent(started); ok {
			return event
		}
		return lifecycleEvent{kind: lifecycleProcessCanceled, err: ctx.Err()}
	}
	return lifecycleEvent{kind: lifecycleProcessCanceled, err: errors.New("invalid lifecycle selection")}
}

func pollTerminalEvent(started []startedComponent) (lifecycleEvent, bool) {
	if len(started) == 0 {
		return lifecycleEvent{}, false
	}
	cases := make([]reflect.SelectCase, 0, len(started)+1)
	for _, component := range started {
		cases = append(cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(component.done)})
	}
	cases = append(cases, reflect.SelectCase{Dir: reflect.SelectDefault})
	chosen, received, open := reflect.Select(cases)
	if chosen == len(started) {
		return lifecycleEvent{}, false
	}
	return lifecycleEvent{
		kind:           lifecycleComponentTerminated,
		componentIndex: started[chosen].index,
		err:            reflectedError(received, open),
	}, true
}

func reflectedError(value reflect.Value, open bool) error {
	if !open || !value.IsValid() || value.IsNil() {
		return nil
	}
	return value.Interface().(error)
}

func componentTerminalError(event lifecycleEvent) error {
	terminalErr := event.err
	if terminalErr == nil {
		terminalErr = errors.New("component stopped unexpectedly")
	}
	return fmt.Errorf("component %d terminated: %w", event.componentIndex, terminalErr)
}

func (application *App) settleStartAndStop(
	started []startedComponent,
	current startedComponent,
	startResult <-chan error,
	event lifecycleEvent,
) error {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), application.shutdownTimeout)
	defer cancel()

	startCompleted := event.startCompleted
	startErr := event.startErr
	var result error
	if !startCompleted {
		select {
		case startErr = <-startResult:
			startCompleted = true
		case <-shutdownCtx.Done():
			result = errors.Join(result, fmt.Errorf("cancel start component %d: %w", current.index, shutdownCtx.Err()))
		}
	}
	if startCompleted && startErr == nil {
		started = append(started, current)
		if terminalEvent, ok := pollTerminalEvent([]startedComponent{current}); ok {
			result = errors.Join(result, componentTerminalError(terminalEvent))
		}
	}
	return errors.Join(result, application.stop(started, shutdownCtx))
}

func (application *App) stopWithNewBudget(started []startedComponent) error {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), application.shutdownTimeout)
	defer cancel()
	return application.stop(started, shutdownCtx)
}

func (application *App) stop(started []startedComponent, shutdownCtx context.Context) error {
	var result error
	for index := len(started) - 1; index >= 0; index-- {
		if err := started[index].component.Stop(shutdownCtx); err != nil {
			result = errors.Join(result, fmt.Errorf("stop component %d: %w", started[index].index, err))
		}
	}
	return result
}
