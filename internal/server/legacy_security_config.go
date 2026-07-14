package server

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

func validateCurrentLegacyProductionConfig() error {
	return validateLegacyProductionConfig(os.Getenv)
}

func validateLegacyProductionConfig(getenv func(string) string) error {
	if getenv == nil {
		return errors.New("environment reader is required")
	}
	if !strings.EqualFold(strings.TrimSpace(getenv("APP_ENV")), "production") {
		return nil
	}

	checks := []struct {
		name     string
		minimum  int
		allowAES bool
		required bool
	}{
		{name: "ADMIN_PASSWORD", minimum: 12, required: true},
		{name: "ADMIN_SESSION_SECRET", minimum: 32, required: true},
		{name: "DOWNLOAD_FALLBACK_TOKEN_SECRET", minimum: 32, required: true},
		{name: "WECHAT_MINI_APP_SECRET", minimum: 16, required: true},
	}
	for _, check := range checks {
		if err := validateConfiguredSecret(check.name, getenv(check.name), check.minimum, check.allowAES, check.required); err != nil {
			return err
		}
	}
	if value := strings.TrimSpace(getenv("WECHAT_MINI_APP_ID")); value == "" || isObviousSecretPlaceholder(value) {
		return errors.New("invalid production configuration: WECHAT_MINI_APP_ID")
	}
	if parseEnvironmentBool(getenv("APP_CLIENT_SIGNATURE_REQUIRED"), false) {
		if err := validateConfiguredSecret("APP_CLIENT_SIGNATURE_KEY", getenv("APP_CLIENT_SIGNATURE_KEY"), 16, true, true); err != nil {
			return err
		}
	}
	return nil
}

func validateConfiguredSecret(name, raw string, minimum int, aesLength, required bool) error {
	value := strings.TrimSpace(raw)
	if value == "" {
		if !required {
			return nil
		}
		return fmt.Errorf("invalid production configuration: %s", name)
	}
	if isObviousSecretPlaceholder(value) {
		return fmt.Errorf("invalid production configuration: %s", name)
	}
	if aesLength {
		switch len(value) {
		case 16, 24, 32:
			return nil
		default:
			return fmt.Errorf("invalid production configuration: %s", name)
		}
	}
	if len(value) < minimum {
		return fmt.Errorf("invalid production configuration: %s", name)
	}
	return nil
}

func isObviousSecretPlaceholder(raw string) bool {
	value := strings.ToLower(strings.TrimSpace(raw))
	markers := []string{
		"change-me", "change_me", "changeme", "example", "placeholder", "dummy",
		"invalid-for-test-only", "redacted", "sample", "admin123456", "password", "secret",
	}
	for _, marker := range markers {
		if value == marker {
			return true
		}
		for _, separator := range []string{"-", "_", ".", ":", "/"} {
			if strings.HasPrefix(value, marker+separator) {
				return true
			}
		}
	}
	return false
}

func parseEnvironmentBool(raw string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "y", "on":
		return true
	case "0", "false", "no", "n", "off":
		return false
	default:
		return fallback
	}
}
