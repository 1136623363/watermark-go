package server

import (
	"fmt"
	"strings"
)

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
