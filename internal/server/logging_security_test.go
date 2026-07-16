package server

import (
	"strings"
	"testing"
)

func TestTargetForLogNeverReturnsPathQueryUserinfoOrMalformedInput(t *testing.T) {
	t.Parallel()
	sentinel := "query-material-must-not-cross"
	tests := []struct {
		raw  string
		want string
	}{
		{raw: "", want: "[empty-target]"},
		{raw: "%gh?xsec_token=" + sentinel, want: "[invalid-target]"},
		{raw: "?xsec_token=" + sentinel, want: "[invalid-target]"},
		{raw: "https://user:" + sentinel + "@example.com/watch", want: "[invalid-target]"},
		{raw: "https://example.com/private/" + sentinel + "?xsec_token=" + sentinel, want: "https://example.com"},
	}
	for _, test := range tests {
		got := targetForLog(test.raw)
		if got != test.want {
			t.Fatalf("targetForLog returned an unexpected safe classification: got=%q want=%q", got, test.want)
		}
		if strings.Contains(got, sentinel) || strings.Contains(got, "xsec_token") {
			t.Fatal("targetForLog exposed path or query material")
		}
	}
}
