package server

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/1136623363/watermark-go/internal/config"
	coreparser "github.com/1136623363/watermark-go/internal/parser"
)

type applicationSessionFetcher struct{}

func (applicationSessionFetcher) HTTPClient(context.Context, int) *http.Client { return &http.Client{} }

func (applicationSessionFetcher) HTTPClientWithRedirect(context.Context, int, func(*http.Request, []*http.Request) error) *http.Client {
	return &http.Client{}
}

func TestApplicationNativeParserUsesScopedSessionProvider(t *testing.T) {
	t.Parallel()
	const token = "application-session-token"
	loaded, err := config.LoadWith(func(key string) string {
		if key == "APP_ENV" {
			return config.EnvironmentTest
		}
		if key == "SOHU_API_KEY" {
			return token
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	dependencies, err := newApplicationNativeDependencies(loaded.Parser, applicationSessionFetcher{})
	if err != nil {
		t.Fatal(err)
	}
	if dependencies.Sessions == nil || dependencies.SessionLoader == nil {
		t.Fatal("application parser did not receive the scoped session provider and loader")
	}
	if dependencies.SohuToken != nil {
		t.Fatal("application parser bypassed the provider with a direct Sohu secret")
	}
	if applicationParserSessionTTL <= 0 || applicationParserSessionCapacity <= 0 {
		t.Fatal("application session provider has invalid bounds")
	}
	budget, err := coreparser.NewRequestBudget(coreparser.BudgetOptions{
		MaxRequests: 2, MaxRedirects: 1, Duration: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	key := coreparser.SessionMaterialKey{Platform: "sohu", Host: "api.tv.sohu.com"}
	material, err := dependencies.Sessions.Get(t.Context(), key, func(ctx context.Context) (coreparser.SensitiveMaterial, error) {
		return dependencies.SessionLoader(ctx, key, budget)
	})
	if err != nil {
		t.Fatal(err)
	}
	if !material.Configured() {
		t.Fatal("configured token did not reach the provider")
	}
	if err := material.Use(func(value string) error {
		if value != token {
			t.Fatalf("provider value mismatch")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := dependencies.SessionLoader(t.Context(), coreparser.SessionMaterialKey{
		Platform: "sohu", Host: "API.TV.SOHU.COM.",
	}, budget); err != nil {
		t.Fatalf("canonical host variant was rejected: %v", err)
	}
}

func TestApplicationSessionLoaderFailsClosedWithoutCredential(t *testing.T) {
	t.Parallel()
	dependencies, err := newApplicationNativeDependencies(config.ParserConfig{}, applicationSessionFetcher{})
	if err != nil {
		t.Fatal(err)
	}
	budget, err := coreparser.NewRequestBudget(coreparser.BudgetOptions{
		MaxRequests: 1, MaxRedirects: 0, Duration: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = dependencies.SessionLoader(t.Context(), coreparser.SessionMaterialKey{
		Platform: "sohu", Host: "api.tv.sohu.com",
	}, budget)
	var typed *coreparser.ParseError
	if !errors.As(err, &typed) || typed.Code != coreparser.ErrorCredentialRequired {
		t.Fatalf("missing application credential error = %#v", err)
	}
	configured, err := config.LoadWith(func(key string) string {
		switch key {
		case "APP_ENV":
			return config.EnvironmentTest
		case "SOHU_API_KEY":
			return "scope-sentinel"
		default:
			return ""
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	dependencies, err = newApplicationNativeDependencies(configured.Parser, applicationSessionFetcher{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = dependencies.SessionLoader(t.Context(), coreparser.SessionMaterialKey{
		Platform: "sohu", Host: "my.tv.sohu.com",
	}, budget)
	if !errors.As(err, &typed) || typed.Code != coreparser.ErrorSecurityRejected {
		t.Fatalf("unapproved application session scope error = %#v", err)
	}
}
