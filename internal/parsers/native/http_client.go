package parser

import (
	"net/http"

	"github.com/go-resty/resty/v2"

	"github.com/1136623363/watermark-go/internal/runtimecfg"
)

func newRestyClient() *resty.Client {
	return runtimecfg.NewRestyClient()
}

func newHTTPClient() *http.Client {
	return runtimecfg.NewHTTPClient()
}

func newHTTPClientWithCheckRedirect(fn func(req *http.Request, via []*http.Request) error) *http.Client {
	client := runtimecfg.NewHTTPClient()
	client.CheckRedirect = fn
	return client
}
