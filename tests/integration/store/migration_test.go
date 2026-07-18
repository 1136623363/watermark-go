//go:build integration

package store_integration_test

import (
	"os"
	"testing"
)

func TestStoreMigrationIntegrationRequiresPinnedServices(t *testing.T) {
	if os.Getenv("STORE_INTEGRATION_READY") != "true" {
		t.Fatal("STORE_INTEGRATION_READY=true with pinned MariaDB/MySQL services is required; integration tests must not skip silently")
	}
}
