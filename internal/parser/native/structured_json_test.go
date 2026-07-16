package native

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	coreparser "github.com/1136623363/watermark-go/internal/parser"
)

func TestStructuredJSONGoldenMatrix(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		file     string
		wantCode coreparser.ErrorCode
	}{
		{name: "complete", file: "kuaishou_complete.html"},
		{name: "escaped", file: "kuaishou_escaped.html"},
		{name: "field-moved", file: "kuaishou_moved.html"},
		{name: "init-state-and-apollo", file: "kuaishou_multi_carrier.html"},
		{name: "truncated", file: "kuaishou_truncated.html", wantCode: coreparser.ErrorSchemaChanged},
		{name: "login", file: "kuaishou_login.html", wantCode: coreparser.ErrorCredentialRequired},
		{name: "risk-control", file: "kuaishou_risk.html", wantCode: coreparser.ErrorSecurityRejected},
		{name: "empty-core", file: "kuaishou_empty_core.html", wantCode: coreparser.ErrorSchemaChanged},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			document := readSyntheticFixture(t, test.file)
			payload, _, err := extractStructuredJSON(document, "window.INIT_STATE", "window.__APOLLO_STATE__")
			if err == nil {
				_, err = findKuaishouSnapshot(payload)
			}
			if test.wantCode == "" {
				if err != nil {
					t.Fatalf("fixture rejected: %v", err)
				}
				return
			}
			var typed *coreparser.ParseError
			if !errors.As(err, &typed) || typed.Code != test.wantCode {
				t.Fatalf("error = %#v, want code %q", err, test.wantCode)
			}
		})
	}

	bilibiliTests := []struct {
		name     string
		file     string
		wantCode coreparser.ErrorCode
	}{
		{name: "complete", file: "bilibili_complete.json"},
		{name: "escaped", file: "bilibili_escaped.json"},
		{name: "multi-carrier", file: "bilibili_multi_carrier.json"},
		{name: "truncated", file: "bilibili_truncated.json", wantCode: coreparser.ErrorSchemaChanged},
		{name: "field-moved", file: "bilibili_moved.json", wantCode: coreparser.ErrorSchemaChanged},
		{name: "login", file: "bilibili_login.json", wantCode: coreparser.ErrorCredentialRequired},
		{name: "risk-control", file: "bilibili_risk.json", wantCode: coreparser.ErrorSecurityRejected},
		{name: "empty-core", file: "bilibili_empty_core.json", wantCode: coreparser.ErrorSchemaChanged},
	}
	for _, test := range bilibiliTests {
		test := test
		t.Run("bilibili-"+test.name, func(t *testing.T) {
			t.Parallel()
			_, err := decodeBiliViewSnapshot(readSyntheticFixture(t, test.file))
			if test.wantCode == "" {
				if err != nil {
					t.Fatalf("fixture rejected: %v", err)
				}
				return
			}
			var typed *coreparser.ParseError
			if !errors.As(err, &typed) || typed.Code != test.wantCode {
				t.Fatalf("error = %#v, want code %q", err, test.wantCode)
			}
		})
	}

	t.Run("bilibili-play-empty-core", func(t *testing.T) {
		t.Parallel()
		_, err := decodeBiliPlaySnapshot(readSyntheticFixture(t, "bilibili_play_empty_core.json"))
		var typed *coreparser.ParseError
		if !errors.As(err, &typed) || typed.Code != coreparser.ErrorSchemaChanged {
			t.Fatalf("error = %#v, want code %q", err, coreparser.ErrorSchemaChanged)
		}
	})
}

func readSyntheticFixture(t *testing.T, name string) []byte {
	t.Helper()
	path := filepath.Join("testdata", "structured", name)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lower := strings.ToLower(string(content))
	if !strings.Contains(lower, "synthetic") {
		t.Fatalf("fixture %s lacks synthetic provenance", name)
	}
	for _, forbidden := range []string{"set-cookie", "authorization:", "sessionid=", "xsec_token="} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("fixture %s contains forbidden session material", name)
		}
	}
	return content
}
