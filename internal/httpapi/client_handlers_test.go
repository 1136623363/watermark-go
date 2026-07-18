package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/1136623363/watermark-go/internal/auth"
)

func TestInvalidTokenUsesFrontendRefreshContract(t *testing.T) {
	router := newClientRouter(t, &auth.ServiceOptions{
		Environment: "test",
		Entropy:     &sequenceReader{},
	})

	res := postJSON(t, router, "/api/parse", `{"url":"https://example.com/v"}`, header("token", "bad"))
	if res.Code != http.StatusOK {
		t.Fatalf("HTTP status = %d, want 200", res.Code)
	}
	assertJSONEq(t, `{"code":1008,"msg":"登录状态已失效，请重试"}`, res.Body.String())
}

func TestClientLoginSanitizesWechatUpstreamErrors(t *testing.T) {
	rawLoginCode := "dummy-raw-login-code"
	rawAppMaterial := "dummy-raw-application-material"
	rawURL := "https://api.weixin.qq.com/sns/jscode2session?appid=wx-test&secret=" + rawAppMaterial + "&js_code=" + rawLoginCode
	logger := &recordingLogger{}
	router := newClientRouter(t, &auth.ServiceOptions{
		Environment:     "production",
		Entropy:         &sequenceReader{},
		WeChat:          auth.WeChatConfig{AppID: "wx-test", AppSecret: rawAppMaterial},
		WeChatExchanger: fakeWeChatExchanger{err: auth.NewWeChatError(auth.WeChatTransportError, errors.New(rawURL))},
		Logger:          logger,
	})

	res := postJSON(t, router, "/api/client/session", `{"code":"`+rawLoginCode+`","programType":12}`, header("Content-Type", "application/json"))
	if res.Code != http.StatusOK {
		t.Fatalf("HTTP status = %d, want 200", res.Code)
	}
	assertJSONEq(t, `{"code":1008,"msg":"客户端登录暂不可用，请重试"}`, res.Body.String())
	combined := res.Body.String() + "\n" + strings.Join(logger.categories, "\n")
	for _, forbidden := range []string{rawLoginCode, rawAppMaterial, rawURL} {
		if strings.Contains(combined, forbidden) {
			t.Fatalf("login response/log exposed %q in %q", forbidden, combined)
		}
	}
	if !logger.hasCategory("wechat_transport") {
		t.Fatalf("logger categories = %v, want wechat_transport", logger.categories)
	}
}

func TestClientSessionHandlerIssuesTokenUsableByParseTokenAndBearerHeaders(t *testing.T) {
	router := newClientRouter(t, &auth.ServiceOptions{
		Environment: "test",
		Entropy:     &sequenceReader{},
	})
	login := postJSON(t, router, "/api/client/session", `{"clientId":"frontend-client","programType":12}`, header("Content-Type", "application/json"))
	if login.Code != http.StatusOK {
		t.Fatalf("HTTP status = %d, want 200", login.Code)
	}
	var envelope struct {
		Code int `json:"code"`
		Data struct {
			Token string `json:"token"`
			UID   string `json:"uid"`
		} `json:"data"`
	}
	if err := json.Unmarshal(login.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode login response: %v", err)
	}
	if envelope.Code != 0 || envelope.Data.Token == "" || envelope.Data.UID != "30000001" {
		t.Fatalf("login envelope = %#v", envelope)
	}

	for _, requestHeader := range []http.Header{
		header("token", envelope.Data.Token),
		header("Authorization", "Bearer "+envelope.Data.Token),
	} {
		res := postJSON(t, router, "/api/parse", `{"url":"https://example.com/v"}`, requestHeader)
		if res.Code != http.StatusOK {
			t.Fatalf("HTTP status = %d, want 200", res.Code)
		}
		var parsed struct {
			Code int `json:"code"`
			Data struct {
				UID string `json:"uid"`
			} `json:"data"`
		}
		if err := json.Unmarshal(res.Body.Bytes(), &parsed); err != nil {
			t.Fatalf("decode parse response: %v", err)
		}
		if parsed.Code != 0 || parsed.Data.UID != "30000001" {
			t.Fatalf("parse response = %#v", parsed)
		}
	}
}

func newClientRouter(t *testing.T, options *auth.ServiceOptions) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	if options.Store == nil {
		options.Store = auth.NewMemoryStore()
	}
	service, err := auth.NewService(*options)
	if err != nil {
		t.Fatalf("new auth service: %v", err)
	}
	router := gin.New()
	handlers := ClientHandlers{
		Auth: service,
		Parse: func(_ context.Context, client auth.AuthenticatedClient, _ ParseRequest) (any, error) {
			return map[string]any{"uid": client.UID}, nil
		},
	}
	handlers.Register(router)
	return router
}

func postJSON(t *testing.T, router *gin.Engine, path string, body string, headers http.Header) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	for key, values := range headers {
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}
	router.ServeHTTP(recorder, request)
	return recorder
}

func header(key, value string) http.Header {
	headers := make(http.Header)
	headers.Set(key, value)
	return headers
}

func assertJSONEq(t *testing.T, want, got string) {
	t.Helper()
	var wantValue any
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("decode want JSON: %v", err)
	}
	var gotValue any
	if err := json.Unmarshal([]byte(got), &gotValue); err != nil {
		t.Fatalf("decode got JSON: %v body=%s", err, got)
	}
	if !reflect.DeepEqual(wantValue, gotValue) {
		t.Fatalf("JSON = %s, want %s", got, want)
	}
}

type sequenceReader struct{ next byte }

func (reader *sequenceReader) Read(p []byte) (int, error) {
	for index := range p {
		reader.next++
		p[index] = reader.next
	}
	return len(p), nil
}

type fakeWeChatExchanger struct {
	err error
}

func (exchanger fakeWeChatExchanger) Exchange(context.Context, auth.WeChatExchangeRequest) (auth.WeChatSession, error) {
	if exchanger.err != nil {
		return auth.WeChatSession{}, exchanger.err
	}
	return auth.WeChatSession{OpenID: "openid-for-test"}, nil
}

type recordingLogger struct {
	categories []string
}

func (logger *recordingLogger) ClientAuthEvent(event auth.Event) {
	logger.categories = append(logger.categories, event.Category)
}

func (logger *recordingLogger) hasCategory(category string) bool {
	for _, existing := range logger.categories {
		if existing == category {
			return true
		}
	}
	return false
}
