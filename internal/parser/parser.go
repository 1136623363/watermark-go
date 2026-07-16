package parser

import (
	"context"
	"net/http"
	"time"

	"github.com/1136623363/watermark-go/internal/netguard"
)

type Parser interface {
	Parse(context.Context, Request) (Result, error)
}

type HTTPClientFactory interface {
	HTTPClient(context.Context, int) *http.Client
	HTTPClientWithRedirect(context.Context, int, func(*http.Request, []*http.Request) error) *http.Client
}

type Secret interface {
	Configured() bool
	Use(func(string) error) error
}

// SessionLoader obtains short-lived parser material inside the same request
// budget as the upstream snapshot that will consume it. Implementations must
// not create a second time/request envelope when refreshing material.
type SessionLoader func(context.Context, SessionMaterialKey, *RequestBudget) (SensitiveMaterial, error)

type Dependencies struct {
	Fetcher       HTTPClientFactory
	Clock         func() time.Time
	Sessions      *SessionMaterialProvider
	SessionLoader SessionLoader
	WeiboCookie   string
	XiguaCookie   string
	SohuToken     Secret
	Probe         func()
}

type Request struct {
	URL      netguard.FetchURL
	ID       string
	Platform PlatformKey
}
