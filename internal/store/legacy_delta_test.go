package store

import "testing"

func TestChooseMigrationModeUsesFinalFullWhenMariaDBBinlogUnavailable(t *testing.T) {
	mode := ChooseMigrationMode(LegacyDiscovery{Engine: "MariaDB", Version: "11.8.6", LogBin: false, GTIDReliable: false})
	if mode != MigrationModeFinalNoBinlog {
		t.Fatalf("mode = %q", mode)
	}
}

func TestChooseMigrationModeUsesReliableBinlogOnlyWhenCapabilityVerified(t *testing.T) {
	mode := ChooseMigrationMode(LegacyDiscovery{Engine: "MySQL", Version: "8.4", LogBin: true, GTIDReliable: true})
	if mode != MigrationModeReliableBinlog {
		t.Fatalf("mode = %q", mode)
	}
	if got := ChooseMigrationMode(LegacyDiscovery{}); got != MigrationModeUnsupported {
		t.Fatalf("empty discovery mode = %q", got)
	}
}
