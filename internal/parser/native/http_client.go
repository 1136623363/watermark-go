package native

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/go-resty/resty/v2"

	"github.com/1136623363/watermark-go/internal/netguard"
	coreparser "github.com/1136623363/watermark-go/internal/parser"
)

// legacyHTTPClients is the only adapter from the legacy parser methods to the
// guarded HTTP boundary. Production constructors always supply a Fetcher; a
// zero value is fail-closed for pure legacy unit tests.
type legacyHTTPClients struct {
	ctx          context.Context
	fetcher      coreparser.HTTPClientFactory
	maxRedirects int
	weiboCookie  string
	xiguaCookie  string
	sohuToken    coreparser.Secret
	budget       *coreparser.RequestBudget
}

func (clients legacyHTTPClients) newRestyClient() *resty.Client {
	return resty.NewWithClient(clients.newHTTPClient())
}

func (clients legacyHTTPClients) newRestyClientNoRedirect() *resty.Client {
	return resty.NewWithClient(clients.newHTTPClientWithCheckRedirect(func(*http.Request, []*http.Request) error {
		return resty.ErrAutoRedirectDisabled
	}))
}

func (clients legacyHTTPClients) newRestyClientWithCheckRedirect(fn func(*http.Request, []*http.Request) error) *resty.Client {
	return resty.NewWithClient(clients.newHTTPClientWithCheckRedirect(fn))
}

func (clients legacyHTTPClients) newHTTPClient() *http.Client {
	if clients.fetcher == nil {
		return disabledHTTPClient()
	}
	return clients.withBudget(clients.fetcher.HTTPClient(clients.requestContext(), clients.redirectLimit()))
}

func (clients legacyHTTPClients) newHTTPClientWithCheckRedirect(fn func(*http.Request, []*http.Request) error) *http.Client {
	if clients.fetcher == nil {
		return disabledHTTPClient()
	}
	return clients.withBudget(clients.fetcher.HTTPClientWithRedirect(clients.requestContext(), clients.redirectLimit(), fn))
}

func (clients legacyHTTPClients) requestContext() context.Context {
	if clients.ctx == nil {
		return context.Background()
	}
	return clients.ctx
}

func (clients legacyHTTPClients) redirectLimit() int {
	if clients.maxRedirects <= 0 {
		return 3
	}
	return clients.maxRedirects
}

func (clients legacyHTTPClients) withBudget(base *http.Client) *http.Client {
	if clients.budget == nil || base == nil {
		return base
	}
	guarded := *base
	guarded.Transport = budgetRoundTripper{next: base.Transport, budget: clients.budget}
	checkRedirect := base.CheckRedirect
	guarded.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		// Count every observed redirect before parser-specific hooks can stop
		// automatic following. Short-link adapters intentionally return
		// ErrUseLastResponse and then inspect Location themselves; those hops
		// still belong to the one Parse-wide redirect budget.
		if err := clients.budget.AllowRedirect(); err != nil {
			return err
		}
		if checkRedirect != nil {
			if err := checkRedirect(request, via); err != nil {
				return err
			}
		}
		return nil
	}
	return &guarded
}

type budgetRoundTripper struct {
	next   http.RoundTripper
	budget *coreparser.RequestBudget
}

func (transport budgetRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if request == nil || request.URL == nil || transport.next == nil || transport.budget == nil {
		return nil, errors.New("invalid budgeted parser request")
	}
	target, err := netguard.NewFetchURL(request.URL.String())
	if err != nil {
		return nil, coreparser.NewParseError(coreparser.ErrorSecurityRejected, errors.New("parser request URL rejected"))
	}
	if err := transport.budget.AllowFetch(target); err != nil {
		return nil, fmt.Errorf("guarded parser request rejected: %w", err)
	}
	return transport.next.RoundTrip(request)
}

func disabledHTTPClient() *http.Client {
	return &http.Client{Transport: disabledRoundTripper{}}
}

type disabledRoundTripper struct{}

func (disabledRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("native parser HTTP dependency is not configured")
}
