package policy_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

var digestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
var pinnedActionPattern = regexp.MustCompile(`^[A-Za-z0-9_.\-/]+@[a-f0-9]{40}$`)

type lockedImage struct {
	Tag    string `json:"tag"`
	Digest string `json:"digest"`
}

type imageLock struct {
	SchemaVersion int                    `json:"schemaVersion"`
	Images        map[string]lockedImage `json:"images"`
	Tools         map[string]struct {
		Version string `json:"version"`
		Commit  string `json:"commit"`
		SHA256  string `json:"sha256"`
		Archive string `json:"archive"`
	} `json:"tools"`
	PythonRequirements []struct {
		Name    string   `json:"name"`
		Version string   `json:"version"`
		Hashes  []string `json:"hashes"`
	} `json:"pythonRequirements"`
	Reproducibility struct {
		BuildFlags              []string `json:"buildFlags"`
		NoCommitInBinary        bool     `json:"noCommitInBinary"`
		NoPythonBytecode        bool     `json:"noPythonBytecode"`
		PromotionMarkerExcluded bool     `json:"promotionMarkerExcluded"`
	} `json:"reproducibility"`
}

func TestDockerfileAndImageLockPolicy(t *testing.T) {
	root := repositoryRoot(t)
	lock := readImageLock(t, root)
	dockerfile := readPolicyDocument(t, root, "Dockerfile")
	dockerignore := readPolicyDocument(t, root, ".dockerignore")

	requiredImages := []string{"builder", "runtime", "mysql", "redis", "mariadbRecovery"}
	for _, name := range requiredImages {
		image := lock.Images[name]
		if image.Tag == "" || !digestPattern.MatchString(image.Digest) {
			t.Fatalf("image lock %s = %#v, want tag and sha256 digest", name, image)
		}
	}
	builder := lock.Images["builder"]
	runtime := lock.Images["runtime"]
	if !strings.Contains(builder.Tag, "golang:1.26.5") {
		t.Fatalf("builder image tag = %q, want Go 1.26.5", builder.Tag)
	}
	for _, expected := range []string{
		"FROM " + builder.Tag + "@" + builder.Digest + " AS builder",
		"FROM " + runtime.Tag + "@" + runtime.Digest + " AS runtime",
		"-trimpath",
		"-buildvcs=false",
		"-buildid=",
		"--require-hashes",
		"PYTHONDONTWRITEBYTECODE=1",
		"USER 10001:10001",
		"org.opencontainers.image.revision",
		"org.opencontainers.image.source=https://github.com/1136623363/watermark-go",
	} {
		if !strings.Contains(dockerfile, expected) {
			t.Fatalf("Dockerfile missing %q", expected)
		}
	}
	for _, forbidden := range []string{"git clone", "yt-dlp -U", "pip install yt-dlp", "HEALTHCHECK", "promotion-marker.txt"} {
		if strings.Contains(dockerfile, forbidden) {
			t.Fatalf("Dockerfile contains forbidden %q", forbidden)
		}
	}
	if !strings.Contains(dockerignore, "release/promotion-marker.txt") {
		t.Fatal(".dockerignore must exclude release/promotion-marker.txt")
	}
	if !lock.Reproducibility.NoCommitInBinary || !lock.Reproducibility.NoPythonBytecode || !lock.Reproducibility.PromotionMarkerExcluded {
		t.Fatalf("reproducibility lock is incomplete: %#v", lock.Reproducibility)
	}
	if len(lock.PythonRequirements) == 0 {
		t.Fatal("image lock must record Python requirements with hashes")
	}
	for _, requirement := range lock.PythonRequirements {
		if requirement.Name == "" || requirement.Version == "" || len(requirement.Hashes) == 0 {
			t.Fatalf("Python requirement is not locked: %#v", requirement)
		}
		for _, hash := range requirement.Hashes {
			if !strings.HasPrefix(hash, "sha256:") || !digestPattern.MatchString(hash) {
				t.Fatalf("Python requirement hash is not sha256: %#v", requirement)
			}
		}
	}
	for _, tool := range []string{"yt-dlp", "videodl", "musicdl", "ffmpeg"} {
		locked := lock.Tools[tool]
		if locked.Version == "" && locked.Commit == "" {
			t.Fatalf("tool %s is not version/commit locked: %#v", tool, locked)
		}
		if locked.SHA256 == "" || !digestPattern.MatchString("sha256:"+strings.TrimPrefix(locked.SHA256, "sha256:")) {
			t.Fatalf("tool %s has invalid sha256: %#v", tool, locked)
		}
		if repeatedHexPlaceholder(strings.TrimPrefix(locked.SHA256, "sha256:")) {
			t.Fatalf("tool %s uses a placeholder sha256: %#v", tool, locked)
		}
		if locked.Commit != "" {
			if !regexp.MustCompile(`^[a-f0-9]{40}$`).MatchString(locked.Commit) {
				t.Fatalf("tool %s commit is not a fixed lowercase git SHA: %#v", tool, locked)
			}
			if repeatedHexPlaceholder(locked.Commit) {
				t.Fatalf("tool %s uses a placeholder commit: %#v", tool, locked)
			}
		}
	}
}

func TestDockerfilePinsRuntimeToolchainSources(t *testing.T) {
	root := repositoryRoot(t)
	lock := readImageLock(t, root)
	dockerfile := readPolicyDocument(t, root, "Dockerfile")
	required := []string{
		"ADD --checksum=" + lock.Tools["videodl"].SHA256 + " https://github.com/CharlesPikachu/videodl/archive/" + lock.Tools["videodl"].Commit + ".tar.gz /tmp/videodl.tar.gz",
		"ADD --checksum=" + lock.Tools["musicdl"].SHA256 + " https://github.com/CharlesPikachu/musicdl/archive/" + lock.Tools["musicdl"].Commit + ".tar.gz /tmp/musicdl.tar.gz",
		"ADD --checksum=" + lock.Tools["ffmpeg"].SHA256 + " " + lock.Tools["ffmpeg"].Archive + " /tmp/ffmpeg.tar.xz",
		"FFMPEG_BINARY=/usr/local/bin/ffmpeg",
		"/usr/local/bin/ffmpeg",
		"/app/third_party/CharlesPikachu/videodl",
		"/app/third_party/CharlesPikachu/musicdl",
		"python -m pip install --no-cache-dir --require-hashes -r /tmp/requirements.lock",
	}
	for _, expected := range required {
		if !strings.Contains(dockerfile, expected) {
			t.Fatalf("Dockerfile runtime toolchain is not pinned by %q", expected)
		}
	}
	for _, forbidden := range []string{
		"apt-get install -y ffmpeg\n",
		"apt-get install -y --no-install-recommends ffmpeg\n",
		"snapshot.debian.org/archive/debian/",
		"github.com/CharlesPikachu/videodl.git",
		"github.com/CharlesPikachu/musicdl.git",
	} {
		if strings.Contains(dockerfile, forbidden) {
			t.Fatalf("Dockerfile contains unpinned runtime tool source %q", forbidden)
		}
	}
}

func TestComposeDeploymentPolicy(t *testing.T) {
	root := repositoryRoot(t)
	body := []byte(readPolicyDocument(t, root, "deploy/compose.yml"))
	violations, err := composePolicyViolations(body, "deploy/compose.yml")
	if err != nil {
		t.Fatalf("parse compose: %v", err)
	}
	if len(violations) != 0 {
		t.Fatalf("compose policy violations: %v", violations)
	}
	document, err := parseComposeYAML(body)
	if err != nil {
		t.Fatalf("parse compose document: %v", err)
	}
	required := []string{
		"data-gate-recovery", "api-recovery", "parser-helper-recovery", "egress-proxy-recovery",
		"data-gate-candidate", "api-candidate", "parser-helper-candidate", "egress-proxy-candidate",
	}
	for _, serviceName := range required {
		if _, ok := document.services[serviceName]; !ok {
			t.Fatalf("compose missing service %s", serviceName)
		}
	}
	for _, role := range []string{"recovery", "candidate"} {
		imageExpression := "${" + strings.ToUpper(role) + "_IMAGE:?same verified " + map[string]string{"recovery": "A", "candidate": "B"}[role] + " registry digest}"
		for _, serviceName := range []string{"api-" + role, "parser-helper-" + role, "egress-proxy-" + role, "data-gate-" + role} {
			service := document.services[serviceName]
			if service["image"] != imageExpression {
				t.Fatalf("%s image = %#v, want %s", serviceName, service["image"], imageExpression)
			}
			if _, ok := service["build"]; ok {
				t.Fatalf("%s must not build locally", serviceName)
			}
			if service["network_mode"] == "host" {
				t.Fatalf("%s must not use host network", serviceName)
			}
		}
		gate := document.services["data-gate-"+role]
		assertStringSliceEqual(t, serviceStringSlice(gate["networks"]), []string{"data"})
		gateEnv := envMap(gate)
		imageKey := strings.ToUpper(role) + "_IMAGE"
		requiredGateEnv := map[string]string{
			"GATE_RECEIPT_PATH":        "/run/watermark-gate/receipt.json",
			"GATE_ROLE":                role,
			"GATE_DATA_STAGE":          "${" + strings.ToUpper(role) + "_DATA_STAGE:?shadow or final}",
			"IMAGE_DIGEST":             "${" + imageKey + ":?same verified " + map[string]string{"recovery": "A", "candidate": "B"}[role] + " registry digest}",
			"GATE_SCHEMA_STATE":        "${" + strings.ToUpper(role) + "_GATE_SCHEMA_STATE:?schema checksum/version}",
			"GATE_TARGET_DB_IDENTITY":  "${" + strings.ToUpper(role) + "_GATE_TARGET_DB_IDENTITY:?target db identity}",
			"GATE_REDIS_IDENTITY":      "${" + strings.ToUpper(role) + "_GATE_REDIS_IDENTITY:?redis identity}",
			"GATE_OUTBOX_IDENTITY":     "${" + strings.ToUpper(role) + "_GATE_OUTBOX_IDENTITY:?outbox identity}",
			"GATE_INPUT_SNAPSHOT_HASH": "${" + strings.ToUpper(role) + "_GATE_INPUT_SNAPSHOT_HASH:?input snapshot hash}",
			"GATE_CONFIG_HASH":         "${" + strings.ToUpper(role) + "_GATE_CONFIG_HASH:?redacted config hash}",
		}
		for key, expected := range requiredGateEnv {
			if gateEnv[key] != expected {
				t.Fatalf("data gate %s env %s = %#v, want %s", role, key, gateEnv[key], expected)
			}
		}
		if _, ok := envMap(gate)["ADMIN_SESSION_SECRET"]; ok {
			t.Fatalf("data gate %s received API secret", role)
		}
		if _, ok := gate["ports"]; ok {
			t.Fatalf("data gate %s exposes ports", role)
		}
		if health, ok := gate["healthcheck"].(map[string]any); !ok || health["disable"] != true {
			t.Fatalf("data gate %s healthcheck = %#v", role, gate["healthcheck"])
		}
		apiEnv := envMap(document.services["api-"+role])
		for key, expected := range requiredGateEnv {
			if apiEnv[key] != expected {
				t.Fatalf("api %s env %s = %#v, want %s", role, key, apiEnv[key], expected)
			}
		}

		helper := document.services["parser-helper-"+role]
		if slices.Contains(serviceStringSlice(helper["networks"]), "data") || slices.Contains(serviceStringSlice(helper["networks"]), "egress") {
			t.Fatalf("helper %s crossed network boundary: %#v", role, helper["networks"])
		}
		if _, ok := helper["ports"]; ok {
			t.Fatalf("helper %s exposes ports", role)
		}

		proxy := document.services["egress-proxy-"+role]
		if slices.Contains(serviceStringSlice(proxy["networks"]), "data") {
			t.Fatalf("proxy %s joined data network", role)
		}
		if !slices.Contains(serviceStringSlice(proxy["networks"]), "egress") || !slices.Contains(serviceStringSlice(proxy["networks"]), "parser-sandbox-"+role) {
			t.Fatalf("proxy %s networks = %#v", role, proxy["networks"])
		}
	}
	networks, ok := document.topLevel["networks"].(map[string]any)
	if !ok {
		t.Fatal("compose missing networks")
	}
	for _, name := range []string{"parser-sandbox-recovery", "parser-sandbox-candidate"} {
		network, ok := networks[name].(map[string]any)
		if !ok || network["internal"] != true {
			t.Fatalf("%s must be internal: %#v", name, networks[name])
		}
	}
}

func TestWorkflowUsesPinnedActionsAndBuildAttestations(t *testing.T) {
	root := repositoryRoot(t)
	workflow := readPolicyDocument(t, root, ".github/workflows/ci-image.yml")
	lower := strings.ToLower(workflow)
	for _, forbidden := range []string{"pull_request_target", "jenkins", "persist-credentials: true"} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("workflow contains forbidden %q", forbidden)
		}
	}
	for _, required := range []string{
		"permissions:",
		"contents: read",
		"packages: write",
		"id-token: write",
		"actions: read",
		"go test ./...",
		"scripts/verify-gitleaks.sh",
		"docker/build-push-action",
		"steps.build.outputs.digest",
		"Runtime tool smoke",
		"/usr/local/bin/ffmpeg",
		"compile(pathlib.Path('/app/bridges/universal/python/bridge.py').read_text",
		"provenance: true",
		"sbom: true",
		"push: true",
		"ghcr.io/1136623363/watermark-go",
	} {
		if !strings.Contains(workflow, required) {
			t.Fatalf("workflow missing %q", required)
		}
	}
	for _, line := range strings.Split(workflow, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "uses: ") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(trimmed, "uses: "))
		action := strings.Fields(value)[0]
		if !pinnedActionPattern.MatchString(action) {
			t.Fatalf("workflow action is not pinned by 40-char SHA: %s", trimmed)
		}
		if !strings.Contains(value, "#") {
			t.Fatalf("workflow action pin lacks version comment: %s", trimmed)
		}
	}
}

func TestWorkflowSeparatesTestAndImagePermissions(t *testing.T) {
	root := repositoryRoot(t)
	workflow := readPolicyDocument(t, root, ".github/workflows/ci-image.yml")
	testJob := workflowJobBlock(t, workflow, "test")
	imageJob := workflowJobBlock(t, workflow, "image")
	if !strings.Contains(testJob, "contents: read") {
		t.Fatal("test job must have read-only contents permission")
	}
	for _, forbidden := range []string{
		"packages: write",
		"id-token: write",
		"attestations: write",
		"docker/login-action",
		"docker/build-push-action",
		"push: true",
	} {
		if strings.Contains(testJob, forbidden) {
			t.Fatalf("test job contains image-only permission or build step %q", forbidden)
		}
	}
	for _, expected := range []string{
		"needs: test",
		"packages: write",
		"id-token: write",
		"attestations: write",
		"docker/build-push-action",
		"push: true",
	} {
		if !strings.Contains(imageJob, expected) {
			t.Fatalf("image job missing %q", expected)
		}
	}
}

func TestComposeMigrationToolsPolicy(t *testing.T) {
	root := repositoryRoot(t)
	body := readPolicyDocument(t, root, "deploy/compose.yml")
	document, err := parseComposeYAML([]byte(body))
	if err != nil {
		t.Fatalf("parse compose document: %v", err)
	}
	for _, serviceName := range []string{"source-mariadb-dump", "mariadb-clone", "restore-mariadb-clone", "mysql", "redis"} {
		if _, ok := document.services[serviceName]; !ok {
			t.Fatalf("compose missing migration/support service %s", serviceName)
		}
	}
	sourceDump := document.services["source-mariadb-dump"]
	if sourceDump["image"] != "${MARIADB_RECOVERY_IMAGE:?set pinned recovery-tool digest}" {
		t.Fatalf("source dump image = %#v", sourceDump["image"])
	}
	if sourceDump["network_mode"] != "none" {
		t.Fatalf("source dump network_mode = %#v, want none", sourceDump["network_mode"])
	}
	if _, ok := sourceDump["ports"]; ok {
		t.Fatal("source dump must not expose ports")
	}
	restoreClone := document.services["restore-mariadb-clone"]
	if !slices.Contains(serviceStringSlice(restoreClone["networks"]), "data") {
		t.Fatalf("restore clone networks = %#v, want data", restoreClone["networks"])
	}
	for _, serviceName := range []string{"mariadb-clone", "mysql", "redis"} {
		if _, ok := document.services[serviceName]["ports"]; ok {
			t.Fatalf("%s must not expose host ports", serviceName)
		}
	}
	for _, expected := range []string{
		"source: /run/mysqld/mysqld.sock",
		"target: /run/source/mariadb.sock",
		"source: ${SOURCE_MARIADB_DEFAULTS_FILE:?0600 temporary file}",
		"source: ${MARIADB_CLONE_DEFAULTS_FILE:?0600 temporary file}",
		"source: ./deploy/migration/source-dump.sh",
		"source: ./deploy/migration/restore-clone.sh",
		"bind: {create_host_path: false}",
		"migration-backup:",
		"mariadb-clone-data:",
		"image: ${MYSQL_IMAGE:?set pinned mysql registry digest}",
		"image: ${REDIS_IMAGE:?set pinned redis registry digest}",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("compose migration policy missing %q", expected)
		}
	}
}

func TestMigrationWrapperPolicy(t *testing.T) {
	root := repositoryRoot(t)
	sourceDump := readPolicyDocument(t, root, "deploy/migration/source-dump.sh")
	restoreClone := readPolicyDocument(t, root, "deploy/migration/restore-clone.sh")
	sourceRequired := []string{
		"set -Eeuo pipefail",
		"umask 077",
		`if [ "$#" -ne 0 ]; then`,
		"--defaults-extra-file=/run/secrets/source.cnf",
		"--socket=/run/source/mariadb.sock",
		"--single-transaction",
		"--quick",
		"--skip-lock-tables",
		"--hex-blob",
		"source.sql.part",
		"source.sql.sha256.part",
		"sha256sum",
		"mv -f",
		"sync",
	}
	for _, expected := range sourceRequired {
		if !strings.Contains(sourceDump, expected) {
			t.Fatalf("source dump wrapper missing %q", expected)
		}
	}
	restoreRequired := []string{
		"set -Eeuo pipefail",
		"umask 077",
		`if [ "$#" -ne 0 ]; then`,
		"sha256sum -c",
		"--defaults-extra-file=/run/secrets/clone.cnf",
		"--host=mariadb-clone",
		"restore.passed.part",
		"restore.passed",
		"mv -f",
		"sync",
	}
	for _, expected := range restoreRequired {
		if !strings.Contains(restoreClone, expected) {
			t.Fatalf("restore clone wrapper missing %q", expected)
		}
	}
	for _, forbidden := range []string{
		"MIGRATION_SOURCE_DSN",
		"MIGRATION_TARGET_DSN",
		"MIGRATION_DUMP_PATH",
		"eval ",
	} {
		if strings.Contains(sourceDump, forbidden) || strings.Contains(restoreClone, forbidden) {
			t.Fatalf("migration wrapper contains forbidden %q", forbidden)
		}
	}
}

func TestGitleaksVerifierPolicy(t *testing.T) {
	root := repositoryRoot(t)
	script := readPolicyDocument(t, root, "scripts/verify-gitleaks.sh")
	for _, required := range []string{
		"set -euo pipefail",
		"GITLEAKS_VERSION=",
		"GITLEAKS_ARCHIVE_SHA256=",
		"https://github.com/gitleaks/gitleaks/releases/download/",
		"mktemp -d",
		"chmod 700",
		"umask 077",
		"trap cleanup EXIT HUP INT TERM",
		"--log-opts=--all",
		"--redact",
		"PASS",
		"FAIL",
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("gitleaks verifier missing %q", required)
		}
	}
	for _, forbidden := range []string{"cat \"$report\"", "grep \"$report\"", "tee \"$report\""} {
		if strings.Contains(script, forbidden) {
			t.Fatalf("gitleaks verifier may print findings via %q", forbidden)
		}
	}
}

func readImageLock(t *testing.T, root string) imageLock {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, "deploy", "image-lock.json"))
	if err != nil {
		t.Fatalf("read deploy/image-lock.json: %v", err)
	}
	var lock imageLock
	if err := json.Unmarshal(body, &lock); err != nil {
		t.Fatalf("decode image lock: %v", err)
	}
	if lock.SchemaVersion != 1 {
		t.Fatalf("image lock schemaVersion = %d, want 1", lock.SchemaVersion)
	}
	return lock
}

func serviceStringSlice(value any) []string {
	switch typed := value.(type) {
	case []any:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			result = append(result, fmt.Sprint(item))
		}
		return result
	case []string:
		return append([]string(nil), typed...)
	default:
		return nil
	}
}

func envMap(service map[string]any) map[string]string {
	result := make(map[string]string)
	switch environment := service["environment"].(type) {
	case []any:
		for _, item := range environment {
			parts := strings.SplitN(fmt.Sprint(item), "=", 2)
			if len(parts) == 2 {
				result[parts[0]] = parts[1]
			}
		}
	case map[string]any:
		for key, value := range environment {
			result[key] = fmt.Sprint(value)
		}
	}
	return result
}

func repeatedHexPlaceholder(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	first := value[0]
	if !strings.ContainsRune("abcdef0123456789", rune(first)) {
		return false
	}
	for index := 1; index < len(value); index++ {
		if value[index] != first {
			return false
		}
	}
	return true
}

func workflowJobBlock(t *testing.T, workflow string, job string) string {
	t.Helper()
	normalized := strings.ReplaceAll(workflow, "\r\n", "\n")
	pattern := regexp.MustCompile(`(?ms)^  ` + regexp.QuoteMeta(job) + `:\n(.*?)(?:^  [A-Za-z0-9_-]+:\n|\z)`)
	matches := pattern.FindStringSubmatch(normalized)
	if len(matches) != 2 {
		t.Fatalf("workflow missing job %s", job)
	}
	return matches[1]
}

func assertStringSliceEqual(t *testing.T, got []string, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("slice = %#v, want %#v", got, want)
	}
}
