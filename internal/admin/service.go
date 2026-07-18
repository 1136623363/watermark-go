package admin

import (
	"context"
	"errors"
	"time"
)

var ErrUnsupported = errors.New("unsupported")

type ServiceOptions struct {
	Auth      AuthOptions
	StartedAt time.Time
	Samples   []BaselineSample
}

type Service struct {
	auth      *AuthService
	startedAt time.Time
	samples   []BaselineSample
}

type Summary struct {
	StartedAt       time.Time `json:"startedAt"`
	UptimeSeconds   int64     `json:"uptimeSeconds"`
	PlatformCount   int       `json:"platformCount"`
	TestSampleCount int       `json:"testSampleCount"`
	TestLinkCount   int       `json:"testLinkCount"`
}

func NewService(options ServiceOptions) (*Service, error) {
	auth, err := NewAuthService(options.Auth)
	if err != nil {
		return nil, err
	}
	startedAt := options.StartedAt
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	return &Service{auth: auth, startedAt: startedAt, samples: append([]BaselineSample(nil), options.Samples...)}, nil
}

func (service *Service) Auth() *AuthService {
	if service == nil {
		return nil
	}
	return service.auth
}

func (service *Service) Summary(_ context.Context) Summary {
	now := time.Now()
	testLinks := 0
	for _, sample := range service.samples {
		if sample.Enabled && sample.SampleURL != "" {
			testLinks++
		}
	}
	return Summary{
		StartedAt:       service.startedAt,
		UptimeSeconds:   int64(now.Sub(service.startedAt).Seconds()),
		PlatformCount:   len(service.samples),
		TestSampleCount: len(service.samples),
		TestLinkCount:   testLinks,
	}
}

func (service *Service) UpdateSettings(ctx context.Context, session Session, _ map[string]any) error {
	return service.auth.RecordAudit(ctx, session, "settings.update", "settings", "runtime", map[string]any{"changed": true})
}

func (service *Service) Profile(context.Context) error {
	return ErrUnsupported
}
