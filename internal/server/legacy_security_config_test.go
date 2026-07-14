package server

import (
	"strings"
	"testing"
)

func TestValidateLegacyProductionConfigRejectsWeakSecrets(t *testing.T) {
	strongPassword := strings.Repeat("A7", 8)
	strongSecret := strings.Repeat("B8", 20)
	base := map[string]string{
		"APP_ENV":                        "production",
		"ADMIN_PASSWORD":                 strongPassword,
		"ADMIN_SESSION_SECRET":           strongSecret,
		"DOWNLOAD_FALLBACK_TOKEN_SECRET": strongSecret,
		"WECHAT_MINI_APP_ID":             "wx-production-id",
		"WECHAT_MINI_APP_SECRET":         strongSecret,
		"APP_CLIENT_SIGNATURE_REQUIRED":  "false",
	}

	tests := []struct {
		name  string
		field string
		value string
	}{
		{name: "admin password empty", field: "ADMIN_PASSWORD", value: ""},
		{name: "admin password short", field: "ADMIN_PASSWORD", value: "short"},
		{name: "admin password placeholder", field: "ADMIN_PASSWORD", value: "change-me"},
		{name: "admin session empty", field: "ADMIN_SESSION_SECRET", value: ""},
		{name: "admin session short", field: "ADMIN_SESSION_SECRET", value: "short"},
		{name: "admin session placeholder", field: "ADMIN_SESSION_SECRET", value: "invalid-for-test-only"},
		{name: "download secret empty", field: "DOWNLOAD_FALLBACK_TOKEN_SECRET", value: ""},
		{name: "download secret short", field: "DOWNLOAD_FALLBACK_TOKEN_SECRET", value: "short"},
		{name: "download secret placeholder", field: "DOWNLOAD_FALLBACK_TOKEN_SECRET", value: "example"},
		{name: "wechat app id empty", field: "WECHAT_MINI_APP_ID", value: ""},
		{name: "wechat secret empty", field: "WECHAT_MINI_APP_SECRET", value: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			environment := cloneEnvironmentMap(base)
			environment[tc.field] = tc.value
			if err := validateLegacyProductionConfig(mapEnvironment(environment)); err == nil {
				t.Fatalf("production config accepted invalid %s", tc.field)
			}
		})
	}
}

func TestValidateLegacyProductionConfigChecksOptionalAESKey(t *testing.T) {
	strongPassword := strings.Repeat("A7", 8)
	strongSecret := strings.Repeat("B8", 20)
	base := map[string]string{
		"APP_ENV":                        "production",
		"ADMIN_PASSWORD":                 strongPassword,
		"ADMIN_SESSION_SECRET":           strongSecret,
		"DOWNLOAD_FALLBACK_TOKEN_SECRET": strongSecret,
		"WECHAT_MINI_APP_ID":             "wx-production-id",
		"WECHAT_MINI_APP_SECRET":         strongSecret,
		"APP_CLIENT_SIGNATURE_REQUIRED":  "true",
	}
	for _, value := range []string{"", "short", "invalid-for-test-only"} {
		environment := cloneEnvironmentMap(base)
		environment["APP_CLIENT_SIGNATURE_KEY"] = value
		if err := validateLegacyProductionConfig(mapEnvironment(environment)); err == nil {
			t.Fatal("production config accepted an invalid optional AES key")
		}
	}
	environment := cloneEnvironmentMap(base)
	environment["APP_CLIENT_SIGNATURE_KEY"] = strings.Repeat("C9", 8)
	if err := validateLegacyProductionConfig(mapEnvironment(environment)); err != nil {
		t.Fatalf("valid production config rejected: %v", err)
	}
}

func TestValidateLegacyProductionConfigAllowsDevelopmentWithoutSecrets(t *testing.T) {
	if err := validateLegacyProductionConfig(mapEnvironment(map[string]string{"APP_ENV": "test"})); err != nil {
		t.Fatalf("test config rejected: %v", err)
	}
}

func mapEnvironment(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func cloneEnvironmentMap(input map[string]string) map[string]string {
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}
