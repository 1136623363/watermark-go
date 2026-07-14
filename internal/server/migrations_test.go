package server

import (
	"strings"
	"testing"
)

func TestLoadMigrationsIncludesCoreTables(t *testing.T) {
	records, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations failed: %v", err)
	}
	if len(records) == 0 {
		t.Fatal("expected embedded migrations")
	}
	core := records[0]
	if core.Version != "001" {
		t.Fatalf("first migration version = %q, want 001", core.Version)
	}
	for _, table := range []string{"parse_results", "parse_tasks", "admin_users", "runtime_settings"} {
		if !strings.Contains(core.SQL, "CREATE TABLE IF NOT EXISTS "+table) {
			t.Fatalf("core migration should create %s", table)
		}
	}
}

func TestLoadMigrationsIncludesGrowthSchema(t *testing.T) {
	records, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations failed: %v", err)
	}
	var growth string
	for _, record := range records {
		if record.Version == "002" {
			growth = record.SQL
			break
		}
	}
	if growth == "" {
		t.Fatal("expected growth schema migration")
	}
	for _, table := range []string{"app_users", "user_quota_accounts", "parse_quota_ledger", "ad_reward_events", "platforms"} {
		if !strings.Contains(growth, "CREATE TABLE IF NOT EXISTS "+table) {
			t.Fatalf("growth migration should create %s", table)
		}
	}
}

func TestLoadMigrationsIncludesPlatformTestSamples(t *testing.T) {
	records, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations failed: %v", err)
	}
	var samples string
	for _, record := range records {
		if record.Version == "003" {
			samples = record.SQL
			break
		}
	}
	if samples == "" {
		t.Fatal("expected platform test samples migration")
	}
	if !strings.Contains(samples, "CREATE TABLE IF NOT EXISTS platform_test_samples") {
		t.Fatal("samples migration should create platform_test_samples")
	}
}

func TestLoadMigrationsIncludesParseAttemptClassification(t *testing.T) {
	records, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations failed: %v", err)
	}
	var classification string
	for _, record := range records {
		if record.Version == "004" {
			classification = record.SQL
			break
		}
	}
	if classification == "" {
		t.Fatal("expected parse attempt classification migration")
	}
	for _, fragment := range []string{"ADD COLUMN raw_input", "ADD COLUMN host", "ADD COLUMN classification", "idx_classification_created"} {
		if !strings.Contains(classification, fragment) {
			t.Fatalf("classification migration should contain %s", fragment)
		}
	}
}

func TestLoadMigrationsIncludesWechatDownloadDomains(t *testing.T) {
	records, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations failed: %v", err)
	}
	var domains string
	for _, record := range records {
		if record.Version == "005" {
			domains = record.SQL
			break
		}
	}
	if domains == "" {
		t.Fatal("expected wechat download domains migration")
	}
	for _, fragment := range []string{"CREATE TABLE IF NOT EXISTS wechat_download_domains", "CREATE TABLE IF NOT EXISTS wechat_download_domain_observations", "uk_origin", "uk_url_field"} {
		if !strings.Contains(domains, fragment) {
			t.Fatalf("wechat download domains migration should contain %s", fragment)
		}
	}
}

func TestLoadMigrationsIncludesPlatformTestItemNodeTimes(t *testing.T) {
	records, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations failed: %v", err)
	}
	var nodeTimes string
	for _, record := range records {
		if record.Version == "008" {
			nodeTimes = record.SQL
			break
		}
	}
	if nodeTimes == "" {
		t.Fatal("expected platform test item node time migration")
	}
	for _, fragment := range []string{"ADD COLUMN started_at", "ADD COLUMN responded_at", "ADD COLUMN node_id", "ADD COLUMN node_name", "ADD COLUMN node_role"} {
		if !strings.Contains(nodeTimes, fragment) {
			t.Fatalf("platform test node time migration should contain %s", fragment)
		}
	}
}

func TestLoadMigrationsIncludesAppClientSessions(t *testing.T) {
	records, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations failed: %v", err)
	}
	var sessions string
	for _, record := range records {
		if record.Version == "009" {
			sessions = record.SQL
			break
		}
	}
	if sessions == "" {
		t.Fatal("expected app client sessions migration")
	}
	for _, fragment := range []string{"CREATE TABLE IF NOT EXISTS app_client_sessions", "token_hash", "idx_expires_at"} {
		if !strings.Contains(sessions, fragment) {
			t.Fatalf("app client sessions migration should contain %s", fragment)
		}
	}
}

func TestSplitSQLStatementsRemovesCommentsAndEmptyStatements(t *testing.T) {
	statements := splitSQLStatements(`
-- leading comment
CREATE TABLE one (id INT);

-- middle comment
CREATE TABLE two (id INT);
`)
	if len(statements) != 3 {
		t.Fatalf("statement parts = %d, want 3 including trailing empty part", len(statements))
	}
	if strings.Contains(statements[0], "--") || strings.Contains(statements[1], "--") {
		t.Fatalf("comments should be removed: %#v", statements)
	}
	if strings.TrimSpace(statements[0]) != "CREATE TABLE one (id INT)" {
		t.Fatalf("first statement = %q", statements[0])
	}
	if strings.TrimSpace(statements[1]) != "CREATE TABLE two (id INT)" {
		t.Fatalf("second statement = %q", statements[1])
	}
}
