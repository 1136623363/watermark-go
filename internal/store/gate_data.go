package store

import (
	"context"
	"errors"
	"time"
)

const (
	GateModeApply      = "apply"
	GateModeRevalidate = "revalidate"
)

type GateStore interface {
	Apply(context.Context) error
	Revalidate(context.Context) error
}

type GateRequest struct {
	Mode        string
	Receipt     GateReceipt
	Expectation GateExpectation
	Now         time.Time
}

func RunDataGate(ctx context.Context, request GateRequest, gateStore GateStore) (GateReceipt, error) {
	if ctx == nil {
		return GateReceipt{}, errors.New("data gate context is required")
	}
	if gateStore == nil {
		return GateReceipt{}, errors.New("data gate store is required")
	}
	now := request.Now
	if now.IsZero() {
		now = time.Now()
	}
	switch request.Mode {
	case GateModeApply:
		if err := gateStore.Apply(ctx); err != nil {
			return GateReceipt{}, err
		}
	case GateModeRevalidate:
		if err := gateStore.Revalidate(ctx); err != nil {
			return GateReceipt{}, err
		}
	default:
		return GateReceipt{}, errors.New("unknown data gate mode")
	}
	if err := request.Receipt.Validate(request.Expectation, now); err != nil {
		return GateReceipt{}, err
	}
	return request.Receipt, nil
}
