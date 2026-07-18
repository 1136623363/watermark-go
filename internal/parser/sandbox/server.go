package sandbox

import (
	"context"
	"errors"
)

type Server struct {
	identity Identity
}

func NewServer(identity Identity, expectedRole string) (*Server, error) {
	if _, err := identity.Validate(expectedRole); err != nil {
		return nil, err
	}
	return &Server{identity: identity}, nil
}

func (server *Server) Healthcheck(ctx context.Context) error {
	if ctx == nil {
		return errors.New("parser sandbox healthcheck context is required")
	}
	if server == nil {
		return ErrSandboxUnverified
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func Healthcheck(ctx context.Context, identity Identity, expectedRole string) error {
	server, err := NewServer(identity, expectedRole)
	if err != nil {
		return err
	}
	return server.Healthcheck(ctx)
}
