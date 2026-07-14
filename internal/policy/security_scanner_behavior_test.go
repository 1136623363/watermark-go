package policy_test

import (
	"bytes"
	"strings"
	"testing"
)

func TestSensitiveDefaultScannerCoversIdentifierAndSyntaxVariants(t *testing.T) {
	literal := "prod-" + "credential-material"
	fixtures := []struct {
		name     string
		variable string
		body     string
	}{
		{name: "lower camel password", variable: "adminPassword", body: `adminPassword = "` + literal + `"`},
		{name: "lower camel secret", variable: "clientSecret", body: `clientSecret := "` + literal + `"`},
		{name: "lower camel token", variable: "apiToken", body: `{"apiToken":"` + literal + `"}`},
		{name: "upper compact key", variable: "APIKEY", body: `APIKEY=` + literal},
		{name: "upper compact secret", variable: "CLIENTSECRET", body: `CLIENTSECRET: ` + literal},
		{name: "go raw string", variable: "clientSecret", body: "clientSecret := `" + literal + "`"},
		{name: "docker env spaced", variable: "API_TOKEN", body: `ENV API_TOKEN ` + literal},
		{name: "docker env equals", variable: "API_TOKEN", body: `ENV API_TOKEN=` + literal},
		{name: "kubernetes name value", variable: "API_TOKEN", body: "- name: API_TOKEN\n  value: " + literal},
		{name: "yaml block", variable: "clientSecret", body: "clientSecret: |\n  " + literal},
		{name: "json lowercase", variable: "client_secret", body: `{"client_secret":"` + literal + `"}`},
	}
	for _, tc := range fixtures {
		t.Run(tc.name, func(t *testing.T) {
			matches, err := scanSensitiveDefaultsStrict([]byte(tc.body+"\n"), "fixture", "working-tree")
			if err != nil {
				t.Fatalf("scan fixture: %v", err)
			}
			if len(matches) == 0 || !strings.EqualFold(matches[0].Variable, tc.variable) {
				t.Fatalf("matches = %#v, want variable %s", matches, tc.variable)
			}
		})
	}
}

func TestSensitiveDefaultScannerCoversMultipleAssignmentsAndCaseVariants(t *testing.T) {
	literal := "prod-" + "credential-material"
	fixtures := []string{
		`{"safe":"value","client` + `Secret":"` + literal + `"}`,
		`ENV SAFE=value ADMIN_PASSWORD=` + literal,
		`env safe=value client_secret=` + literal,
		`eNv SAFE=value AdminPassword=` + literal,
		`const safe="value", clientSecret="` + literal + `"`,
		`export SAFE=value CLIENT_SECRET=` + literal,
		`{safe: value, client` + `Secret: ` + literal + `}`,
		`clientsecret=` + literal,
		`adminpassword=` + literal,
	}
	for index, body := range fixtures {
		matches, err := scanSensitiveDefaultsStrict([]byte(body+"\n"), "fixture", "working-tree")
		if err != nil {
			t.Fatalf("scan case-variant fixture %d", index)
		}
		if len(matches) == 0 {
			t.Fatalf("case-variant fixture %d bypassed the scanner", index)
		}
	}
	prose := "documentation says safe, adminpassword=" + literal
	if matches, err := scanSensitiveDefaultsStrict([]byte(prose+"\n"), "fixture", "working-tree"); err != nil || len(matches) != 0 {
		t.Fatal("scanner treated prose without configuration context as an assignment")
	}
}

func TestSensitiveDefaultScannerCoversPunctuationTypedDeclarationsAndBOM(t *testing.T) {
	literal := "prod-" + "credential-material"
	fixtures := []string{
		"client" + "-secret=" + literal,
		"client" + ".secret=" + literal,
		"const client" + "Secret string = \"" + literal + "\"",
		"var admin" + "Password string = \"" + literal + "\"",
		"\ufeffclient" + "Secret=\"" + literal + "\"",
	}
	for index, body := range fixtures {
		matches, err := scanSensitiveDefaultsStrict([]byte(body+"\n"), "fixture", "working-tree")
		if err != nil {
			t.Fatalf("scan syntax fixture %d", index)
		}
		if len(matches) == 0 {
			t.Fatalf("syntax fixture %d bypassed the scanner", index)
		}
	}
}

func TestSensitiveDefaultScannerRejectsEmbeddedBOM(t *testing.T) {
	body := "safe=true \ufeffclient" + "Secret=\"prod-credential-material\"\n"
	if _, err := scanSensitiveDefaultsStrict([]byte(body), "fixture", "working-tree"); err == nil {
		t.Fatal("scanner accepted an embedded byte-order mark")
	}
}

func TestSensitiveDefaultScannerPlaceholderAndEnvironmentRulesAreStrict(t *testing.T) {
	allowed := []string{
		`clientSecret="example"`,
		`clientSecret="example-test"`,
		`clientSecret="invalid-for-test-only"`,
		`clientSecret=${CLIENT_SECRET}`,
		`clientSecret=${CLIENT_SECRET:?required}`,
		`clientSecret=${CLIENT_SECRET:-change-me}`,
		`clientSecret=${OUTER_SECRET:-${INNER_SECRET}}`,
	}
	for _, body := range allowed {
		matches, err := scanSensitiveDefaultsStrict([]byte(body+"\n"), "fixture", "working-tree")
		if err != nil || len(matches) != 0 {
			t.Fatalf("allowed fixture %q rejected: matches=%#v error=%v", body, matches, err)
		}
	}

	disallowed := []string{
		`clientSecret="notexampleproduction"`,
		`clientSecret="exampleproduction"`,
		`clientSecret=$()`,
		`clientSecret=${CLIENT_SECRET}suffix`,
		`clientSecret=${CLIENT_SECRET:-production-material}`,
	}
	for _, body := range disallowed {
		matches, err := scanSensitiveDefaultsStrict([]byte(body+"\n"), "fixture", "working-tree")
		if err != nil {
			t.Fatalf("scan disallowed fixture: %v", err)
		}
		if len(matches) == 0 {
			t.Fatalf("disallowed fixture was accepted")
		}
	}
}

func TestSensitiveDefaultScannerFailsClosedForBinaryAndOversizedBlobs(t *testing.T) {
	for _, contents := range [][]byte{
		{0, 1, 2},
		{0xff, 0xfe},
		[]byte("safe\x01text"),
		[]byte("safe\x7ftext"),
		[]byte("safe\u0080text"),
		bytes.Repeat([]byte("x"), maxPolicyBlobBytes+1),
	} {
		if _, err := scanSensitiveDefaultsStrict(contents, "fixture", "working-tree"); err == nil {
			t.Fatal("scanner accepted an unscannable blob")
		}
	}
}

func TestSensitiveDefaultScannerAllowsNormalTextControls(t *testing.T) {
	if _, err := scanSensitiveDefaultsStrict([]byte("first\tfield\r\nsecond\n"), "fixture", "working-tree"); err != nil {
		t.Fatal("scanner rejected tab, carriage return, or newline in normal UTF-8 text")
	}
}
