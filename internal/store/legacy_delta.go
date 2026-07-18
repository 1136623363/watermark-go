package store

import "strings"

const (
	MigrationModeReliableBinlog = "reliable_binlog_delta"
	MigrationModeFinalNoBinlog  = "final_full_no_binlog"
	MigrationModeUnsupported    = "unsupported"
)

type LegacyDiscovery struct {
	Engine       string
	Version      string
	LogBin       bool
	GTIDReliable bool
}

func ChooseMigrationMode(discovery LegacyDiscovery) string {
	engine := strings.ToLower(strings.TrimSpace(discovery.Engine))
	if engine == "" || strings.TrimSpace(discovery.Version) == "" {
		return MigrationModeUnsupported
	}
	if strings.Contains(engine, "mariadb") && (!discovery.LogBin || !discovery.GTIDReliable) {
		return MigrationModeFinalNoBinlog
	}
	if discovery.LogBin && discovery.GTIDReliable {
		return MigrationModeReliableBinlog
	}
	return MigrationModeFinalNoBinlog
}
