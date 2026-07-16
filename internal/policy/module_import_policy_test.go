package policy_test

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func TestLegacyModuleImportScannerRejectsQuotedAndRawImports(t *testing.T) {
	root := t.TempDir()
	legacyPrefix := "watermark-" + "backend/"
	files := map[string]string{
		"canonical.go": "package fixture\nimport \"github.com/1136623363/watermark-go/internal/config\"\n",
		"quoted.go":    "package fixture\nimport old \"" + legacyPrefix + "internal/server\"\n",
		"raw.go":       "package fixture\nimport `" + legacyPrefix + "internal/parsers/native`\n",
	}
	paths := make([]string, 0, len(files))
	for path, body := range files {
		if err := os.WriteFile(filepath.Join(root, path), []byte(body), 0o600); err != nil {
			t.Fatalf("write fixture %s: %v", path, err)
		}
		paths = append(paths, path)
	}

	got, err := legacyModuleImportFiles(root, paths)
	if err != nil {
		t.Fatalf("legacyModuleImportFiles() error = %v", err)
	}
	sort.Strings(got)
	want := []string{"quoted.go", "raw.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("legacy module imports = %#v, want %#v", got, want)
	}
}
