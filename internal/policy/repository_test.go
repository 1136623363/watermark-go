package policy_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/goccy/go-yaml"
)

func TestRepositoryTrackedFilesExcludeSensitiveArtifactsAndRetiredPipelines(t *testing.T) {
	root := repositoryRoot(t)
	output, err := exec.Command("git", "-C", root, "ls-files", "-z").Output()
	if err != nil {
		t.Fatalf("list tracked files: %v", err)
	}

	var forbidden []string
	for _, path := range strings.Split(strings.TrimSuffix(string(output), "\x00"), "\x00") {
		if path == "" {
			continue
		}
		if reason := forbiddenTrackedFileReason(path); reason != "" {
			forbidden = append(forbidden, sanitizedLabel(path)+" ("+reason+")")
		}
	}

	if len(forbidden) != 0 {
		sort.Strings(forbidden)
		t.Fatalf("forbidden files are tracked:\n%s", strings.Join(forbidden, "\n"))
	}
}

func TestRepositoryUsesCanonicalModuleAndImportPrefix(t *testing.T) {
	root := repositoryRoot(t)
	moduleFile := readPolicyDocument(t, root, "go.mod")
	if !strings.HasPrefix(moduleFile, "module github.com/1136623363/watermark-go\n") {
		t.Fatal("go.mod does not use the canonical module path")
	}
	if !strings.Contains(moduleFile, "\ngo 1.24.0\n") || !strings.Contains(moduleFile, "\ntoolchain go1.26.5\n") {
		t.Fatal("go.mod does not pin the required language and preferred toolchain versions")
	}

	output, err := exec.Command(
		"git", "-C", root, "ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", "*.go",
	).Output()
	if err != nil {
		t.Fatalf("list tracked Go files: %v", err)
	}
	paths := make([]string, 0)
	for _, path := range strings.Split(strings.TrimSuffix(string(output), "\x00"), "\x00") {
		if path == "" {
			continue
		}
		paths = append(paths, path)
	}
	legacyFiles, err := legacyModuleImportFiles(root, paths)
	if err != nil {
		t.Fatalf("scan repository Go imports: %v", err)
	}
	if len(legacyFiles) != 0 {
		sanitized := make([]string, 0, len(legacyFiles))
		for _, path := range legacyFiles {
			sanitized = append(sanitized, sanitizedLabel(path))
		}
		t.Fatalf("repository retains legacy module imports: %s", strings.Join(sanitized, ", "))
	}
}

func legacyModuleImportFiles(root string, paths []string) ([]string, error) {
	legacyPrefix := "watermark-" + "backend/"
	legacyFiles := make([]string, 0)
	for index, path := range paths {
		absolutePath := filepath.Join(root, path)
		if _, err := os.Stat(absolutePath); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, fmt.Errorf("stat Go import candidate %d: %w", index, err)
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), absolutePath, nil, parser.ImportsOnly)
		if err != nil {
			return nil, fmt.Errorf("parse Go import candidate %d: %w", index, err)
		}
		for _, spec := range parsed.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return nil, fmt.Errorf("decode Go import candidate %d: %w", index, err)
			}
			if strings.HasPrefix(importPath, legacyPrefix) {
				legacyFiles = append(legacyFiles, path)
				break
			}
		}
	}
	return legacyFiles, nil
}

func TestRepositoryTrackedWorkflowsExcludeJenkins(t *testing.T) {
	root := repositoryRoot(t)
	output, err := exec.Command(
		"git", "-C", root, "grep", "-I", "-i", "-l", "-E", "-e", "jenkins", "--", ".github/workflows",
	).Output()
	if err == nil {
		t.Fatalf("tracked workflows contain Jenkins references:\n%s", formatSanitizedLines(output))
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return
	}
	t.Fatalf("scan tracked workflows for Jenkins references: %v", err)
}

func TestRepositoryReachableWorkflowHistoryExcludesJenkins(t *testing.T) {
	root := repositoryRoot(t)
	matches := make(map[string]struct{})
	for _, batch := range revisionBatches(reachableRevisions(t, root), 32) {
		args := []string{"-C", root, "grep", "-I", "-i", "-l", "-E", "-e", "jenkins"}
		args = append(args, batch...)
		args = append(args, "--", ".github/workflows")
		output, err := exec.Command("git", args...).Output()
		if err == nil {
			for _, match := range strings.Split(strings.TrimSpace(string(output)), "\n") {
				if match != "" {
					matches[sanitizedLabel(match)] = struct{}{}
				}
			}
			continue
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			continue
		}
		t.Fatalf("scan reachable workflow history for Jenkins references: %v", err)
	}

	if len(matches) != 0 {
		paths := make([]string, 0, len(matches))
		for match := range matches {
			paths = append(paths, match)
		}
		sort.Strings(paths)
		const reportLimit = 10
		if len(paths) > reportLimit {
			paths = append(paths[:reportLimit], fmt.Sprintf("... %d more matches", len(paths)-reportLimit))
		}
		t.Fatalf("reachable workflow history contains Jenkins references:\n%s", strings.Join(paths, "\n"))
	}
}

func forbiddenTrackedFileReason(path string) string {
	base := filepath.Base(path)
	lowerBase := strings.ToLower(base)
	ext := strings.ToLower(filepath.Ext(base))

	switch {
	case strings.EqualFold(base, "jenkinsfile"):
		return "Jenkins pipeline"
	case strings.EqualFold(base, "docker-compose.prod.worker.yml"):
		return "legacy worker deployment"
	case strings.HasPrefix(lowerBase, ".env"):
		return "environment secrets"
	case ext == ".pem" || ext == ".key" || ext == ".p12" ||
		ext == ".crt" || ext == ".cer" || ext == ".cert" || ext == ".der" || ext == ".pfx":
		return "private key material"
	case strings.Contains(base, "密码") || strings.Contains(base, "服务器配置"):
		return "sensitive configuration document"
	default:
		return ""
	}
}

func TestRepositorySecurityAuditCoversIndexWorktreeHistoryAndRefs(t *testing.T) {
	root := repositoryRoot(t)
	audit, err := auditGitRepository(root)
	if err != nil {
		t.Fatalf("repository security audit failed closed: %v", err)
	}
	if len(audit.Violations) != 0 {
		t.Fatalf("repository security audit found violations:\n%s", formatAuditViolations(audit.Violations))
	}
}

func TestFrontendProvenancePinsCleanSourceSnapshot(t *testing.T) {
	root := repositoryRoot(t)
	body, err := os.ReadFile(filepath.Join(root, "docs", "frontend-provenance.json"))
	if err != nil {
		t.Fatal("read frontend provenance")
	}
	var provenance struct {
		SchemaVersion       int      `json:"schemaVersion"`
		SourceRepository    string   `json:"sourceRepository"`
		SourcePathAtCapture string   `json:"sourcePathAtCapture"`
		Commit              string   `json:"commit"`
		Tree                string   `json:"tree"`
		WorkingTreeStatus   string   `json:"workingTreeStatus"`
		TrackedRoots        []string `json:"trackedRoots"`
		ManifestAlgorithm   string   `json:"manifestAlgorithm"`
		ManifestCommand     string   `json:"manifestCommand"`
		ManifestSHA256      string   `json:"manifestSha256"`
	}
	if err := json.Unmarshal(body, &provenance); err != nil {
		t.Fatal("decode frontend provenance")
	}
	if provenance.SchemaVersion != 1 ||
		provenance.SourceRepository != "https://github.com/1136623363/watermark" ||
		provenance.SourcePathAtCapture != "/srv/watermark" ||
		provenance.Commit != "5d72c4925017676b6183b907dfe11ec60a4885bf" ||
		provenance.Tree != "03c72a16532f51db76203967a3b982d49d4909d1" ||
		provenance.WorkingTreeStatus != "clean" ||
		strings.Join(provenance.TrackedRoots, ",") != "miniprogram,test,project.config.json" ||
		provenance.ManifestAlgorithm != "sha256(sha256sum(each tracked file in git ls-files -z order))" ||
		provenance.ManifestCommand != "git ls-files -z -- miniprogram test project.config.json | xargs -0 sha256sum | sha256sum" ||
		provenance.ManifestSHA256 != "3e3f172b90439252e3601892e15fef2d398747ac9630fbc148013304d8c776f8" {
		t.Fatal("frontend provenance does not match the approved clean snapshot")
	}
}

func TestBaselineProvenanceSeparatesExternalDocumentFromCommittedCatalog(t *testing.T) {
	root := repositoryRoot(t)
	body, err := os.ReadFile(filepath.Join(root, "docs", "baseline-provenance.json"))
	if err != nil {
		t.Fatal("read baseline provenance")
	}
	var provenance struct {
		SourceCommit                        string `json:"sourceCommit"`
		SourceTree                          string `json:"sourceTree"`
		BaselineDocumentSourcePathAtCapture string `json:"baselineDocumentSourcePathAtCapture"`
		BaselineDocumentTracked             bool   `json:"baselineDocumentTracked"`
		BaselineDocumentBinding             string `json:"baselineDocumentBinding"`
		BaselineDocumentSHA256              string `json:"baselineDocumentSha256"`
		CatalogManifestSHA256               string `json:"catalogManifestSha256"`
	}
	if err := json.Unmarshal(body, &provenance); err != nil {
		t.Fatal("decode baseline provenance")
	}
	if provenance.SourceCommit != "1d3dc9a6064f3f2e41af9ea92a29566885939175" ||
		provenance.SourceTree != "d1aac032059b5622fc9f0b5cd6ce321a77978a1e" ||
		provenance.BaselineDocumentSourcePathAtCapture != "/srv/watermark/watermark-"+"backend/测试结果基准.md" ||
		provenance.BaselineDocumentTracked ||
		provenance.BaselineDocumentBinding != "user-provided external acceptance baseline captured by content hash; not attributed to sourceCommit" ||
		provenance.BaselineDocumentSHA256 != "a470a87e64242e5e97ee1a03571c43198a6bd7036c0b756e8c69fd9b639df29a" ||
		provenance.CatalogManifestSHA256 != "05d832a7d59897d16cd4bd26a7d02d6f6bdf5ec6829c1a280e974579fa29bf6a" {
		t.Fatal("baseline provenance does not preserve the approved capture boundary")
	}
}

func TestSensitiveDefaultScannerRejectsLiteralAndAllowsPlaceholders(t *testing.T) {
	disallowed := "prod-" + "credential-material"
	variable := "APP_CLIENT_SIGNATURE_" + "KEY"
	unsafeFixtures := []string{
		variable + "=" + disallowed + "\n",
		"- " + variable + "=" + disallowed + "\n",
		variable + ": " + disallowed + "\n",
		"\"" + variable + "\": \"" + disallowed + "\"\n",
		variable + "=${" + variable + ":-" + disallowed + "}\n",
		"envOrDefault(\n  \"" + variable + "\",\n  \"" + disallowed + "\",\n)\n",
		"firstNonEmptyString(os.Getenv(\"" + variable + "\"), \"" + disallowed + "\")\n",
	}
	for _, fixture := range unsafeFixtures {
		got := scanSensitiveDefaults([]byte(fixture), "fixture.env", "working-tree")
		if len(got) != 1 || got[0].Variable != variable {
			t.Fatalf("scanner matches = %#v, want %s", got, variable)
		}
	}
	for _, name := range []string{
		"APP_SECRET_" + "VALUE",
		"ACCESS_TOKEN_" + "MATERIAL",
		"SESSION_COOKIE_" + "MATERIAL",
		"SIGNING_KEY_" + "MATERIAL",
	} {
		got := scanSensitiveDefaults([]byte(name+"="+disallowed+"\n"), "fixture.env", "working-tree")
		if len(got) != 1 || got[0].Variable != name {
			t.Fatalf("scanner matches = %#v, want %s", got, name)
		}
	}

	for _, allowed := range []string{
		`APP_CLIENT_SIGNATURE_KEY=""`,
		`APP_CLIENT_SIGNATURE_KEY=${APP_CLIENT_SIGNATURE_KEY}`,
		`APP_CLIENT_SIGNATURE_KEY=${APP_CLIENT_SIGNATURE_KEY:?required}`,
		`APP_CLIENT_SIGNATURE_KEY=${APP_CLIENT_SIGNATURE_KEY:-change-me}`,
		`APP_CLIENT_SIGNATURE_KEY=__SET_IN_PRODUCTION_ENV__`,
		`APP_CLIENT_SIGNATURE_KEY=invalid-for-test-only`,
		`ADMIN_PASSWORD=change-me`,
		`DOWNLOAD_TOKEN_SECRET=x`,
		`COOKIE_VALUE=example`,
		`value := os.Getenv("APP_CLIENT_SIGNATURE_KEY")`,
		`value, ok := os.LookupEnv("APP_CLIENT_SIGNATURE_KEY")`,
		`- APP_CLIENT_SIGNATURE_KEY=${APP_CLIENT_SIGNATURE_KEY:?required}`,
		`APP_CLIENT_SIGNATURE_KEY: ${APP_CLIENT_SIGNATURE_KEY}`,
		`"APP_CLIENT_SIGNATURE_KEY": "example"`,
		"`E2E_CLIENT_SIGNATURE_" + "KEY`：默认 `invalid-for-test-only`。",
		"APP_CLIENT_" + "TOKEN_TTL_SECONDS=2592000",
		"HTTP_" + "COOKIE_HEADER=Cookie",
		"CACHE_" + "KEY_NAME=parse-result",
	} {
		if matches := scanSensitiveDefaults([]byte(allowed+"\n"), "fixture.env", "working-tree"); len(matches) != 0 {
			t.Errorf("obvious placeholder was rejected for %s", matches[0].Variable)
		}
	}
	if report := formatSensitiveMatches(scanSensitiveDefaults([]byte(unsafeFixtures[0]), "fixture.env", "working-tree")); strings.Contains(report, disallowed) {
		t.Fatal("sanitized scanner report exposed a sensitive literal")
	}
	for _, allowed := range []string{
		`id-token: write`,
		`password: ${{ secrets.GITHUB_TOKEN }}`,
	} {
		if matches := scanSensitiveDefaults([]byte(allowed+"\n"), ".github/workflows/ci.yml", "working-tree"); len(matches) != 0 {
			t.Errorf("workflow placeholder was rejected for %s", matches[0].Variable)
		}
	}
}

type sensitiveMatch struct {
	Path     string
	Line     int
	Variable string
	Revision string
}

const (
	configIdentifierExpression = `[A-Za-z][A-Za-z0-9_.-]*`
	maxPolicyBlobBytes         = 8 * 1024 * 1024
)

var (
	sensitiveAssignmentPattern = regexp.MustCompile(
		`(^|[[:space:]{,(])["'` + "`" + `]?(` + configIdentifierExpression + `)["'` + "`" + `]?(?:[[:space:]]+[A-Za-z_][A-Za-z0-9_.*\[\]]*)?[[:space:]]*(:=|=|:)[[:space:]]*("[^"]*"|'[^']*'|` + "`[^`]*`" + `|\$\{\{[^}]*\}\}|\$\{(?:[^{}]|\$\{[^{}]*\})*\}[^[:space:],}]*|[^[:space:],}]+)`,
	)
	dockerEnvironmentPattern = regexp.MustCompile(
		`(?i)^[[:space:]]*ENV[[:space:]]+(` + configIdentifierExpression + `)(?:[[:space:]]+|[[:space:]]*=[[:space:]]*)(.*)$`,
	)
	kubernetesNamePattern = regexp.MustCompile(
		`^[[:space:]-]*name:[[:space:]]*["'` + "`" + `]?(` + configIdentifierExpression + `)["'` + "`" + `]?[[:space:]]*$`,
	)
	kubernetesValuePattern = regexp.MustCompile(
		`^[[:space:]]*value:[[:space:]]*(.*)$`,
	)
	multilineQuotedSensitiveDefaultPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?s)envOrDefault[[:space:]]*\([[:space:]]*["'` + "`" + `](` + configIdentifierExpression + `)["'` + "`" + `][[:space:]]*,[[:space:]]*["'` + "`" + `]([^"'` + "`" + `]*)["'` + "`" + `]`),
		regexp.MustCompile(`(?s)firstNonEmptyString[[:space:]]*\([[:space:]]*os\.(?:Getenv|LookupEnv)[[:space:]]*\([[:space:]]*["'` + "`" + `](` + configIdentifierExpression + `)["'` + "`" + `][[:space:]]*\)[[:space:]]*,[[:space:]]*["'` + "`" + `]([^"'` + "`" + `]*)["'` + "`" + `]`),
	}
	exactEnvironmentReferencePattern = regexp.MustCompile(`^\$\{[A-Za-z_][A-Za-z0-9_]*\}$`)
	exactRequiredEnvironmentPattern  = regexp.MustCompile(`^\$\{[A-Za-z_][A-Za-z0-9_]*:\?[^}]+\}$`)
	exactDefaultEnvironmentPattern   = regexp.MustCompile(`^\$\{[A-Za-z_][A-Za-z0-9_]*:-(.*)\}$`)
)

func scanSensitiveDefaults(contents []byte, path, revision string) []sensitiveMatch {
	matches, err := scanSensitiveDefaultsStrict(contents, path, revision)
	if err != nil {
		return []sensitiveMatch{{Path: sanitizedLabel(path), Variable: "UNSCANNABLE", Revision: sanitizedLabel(revision)}}
	}
	return matches
}

func scanSensitiveDefaultsStrict(contents []byte, path, revision string) ([]sensitiveMatch, error) {
	if len(contents) > maxPolicyBlobBytes {
		return nil, fmt.Errorf("policy blob exceeds the per-object scan limit")
	}
	if !utf8.Valid(contents) {
		return nil, fmt.Errorf("policy blob is not valid UTF-8 text")
	}
	for _, value := range string(contents) {
		if unicode.IsControl(value) && value != '\t' && value != '\r' && value != '\n' {
			return nil, fmt.Errorf("policy blob contains unsupported control bytes")
		}
	}
	contents = bytes.TrimPrefix(contents, []byte{0xef, 0xbb, 0xbf})
	if bytes.Contains(contents, []byte{0xef, 0xbb, 0xbf}) {
		return nil, fmt.Errorf("policy blob contains an embedded byte-order mark")
	}

	seen := make(map[string]struct{})
	var matches []sensitiveMatch
	add := func(variable string, line int) {
		variable = strings.ToUpper(variable)
		key := fmt.Sprintf("%s:%d:%s:%s", path, line, variable, revision)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		matches = append(matches, sensitiveMatch{Path: path, Line: line, Variable: variable, Revision: revision})
	}
	lines := strings.Split(string(contents), "\n")
	var kubernetesVariable string
	var kubernetesLine int
	for index, line := range lines {
		lineNumber := index + 1
		if groups := dockerEnvironmentPattern.FindStringSubmatch(line); len(groups) == 3 {
			if isSensitiveIdentifier(groups[1]) && !isAllowedSensitiveDefault(groups[2], groups[1]) {
				add(groups[1], lineNumber)
			}
		}
		if groups := kubernetesNamePattern.FindStringSubmatch(line); len(groups) == 2 {
			kubernetesVariable, kubernetesLine = groups[1], lineNumber
			continue
		}
		if kubernetesVariable != "" {
			if groups := kubernetesValuePattern.FindStringSubmatch(line); len(groups) == 2 {
				if isSensitiveIdentifier(kubernetesVariable) && !isAllowedSensitiveDefault(groups[1], kubernetesVariable) {
					add(kubernetesVariable, kubernetesLine)
				}
				kubernetesVariable = ""
			} else if strings.TrimSpace(line) != "" && !strings.HasPrefix(strings.TrimSpace(line), "#") {
				kubernetesVariable = ""
			}
		}

		for _, indices := range sensitiveAssignmentPattern.FindAllStringSubmatchIndex(line, -1) {
			if len(indices) < 10 || indices[4] < 0 || indices[6] < 0 || indices[8] < 0 || isNestedEnvironmentReference(line, indices[0]) {
				continue
			}
			variable := line[indices[4]:indices[5]]
			operator := line[indices[6]:indices[7]]
			candidate := line[indices[8]:indices[9]]
			if isSensitiveIdentifier(variable) && assignmentHasConfigurationContext(line, indices[4], indices[5]) && shouldInspectSensitiveCandidate(variable, operator, candidate, path) && !isAllowedSensitiveDefault(candidate, variable) {
				add(variable, lineNumber)
			}
		}
	}

	for _, pattern := range multilineQuotedSensitiveDefaultPatterns {
		for _, indices := range pattern.FindAllSubmatchIndex(contents, -1) {
			if len(indices) < 6 || indices[2] < 0 || indices[4] < 0 {
				continue
			}
			variable := string(contents[indices[2]:indices[3]])
			candidate := string(contents[indices[4]:indices[5]])
			if !isSensitiveIdentifier(variable) || isAllowedSensitiveDefault(candidate, variable) {
				continue
			}
			line := bytes.Count(contents[:indices[0]], []byte{'\n'}) + 1
			add(variable, line)
		}
	}
	return matches, nil
}

func assignmentHasConfigurationContext(line string, variableStart, variableEnd int) bool {
	if variableStart < 0 || variableEnd > len(line) {
		return false
	}
	if variableStart > 0 && variableEnd < len(line) {
		left, right := line[variableStart-1], line[variableEnd]
		if (left == '"' || left == '\'' || left == '`') && right == left {
			return true
		}
	}
	prefix := strings.TrimSpace(line[:variableStart])
	switch prefix {
	case "", "-", "const", "var", "export", "ENV":
		return true
	}
	fields := strings.Fields(prefix)
	if len(fields) != 0 {
		switch strings.ToLower(strings.Trim(fields[0], "{}(),")) {
		case "const", "let", "var", "export", "env":
			return true
		}
	}
	return strings.Contains(prefix, "{") && strings.HasSuffix(prefix, ",")
}

func shouldInspectSensitiveCandidate(variable, operator, candidate, path string) bool {
	value := strings.TrimSpace(candidate)
	if isGitHubWorkflowPath(path) && operator == ":" && strings.EqualFold(variable, "id-token") {
		return false
	}
	if value == "" {
		return true
	}
	if strings.ContainsRune(`"'`+"`", rune(value[0])) || strings.HasPrefix(value, "$") || value == "|" || value == ">" || strings.HasPrefix(value, "|-") || strings.HasPrefix(value, ">-") {
		return true
	}
	if operator == ":" {
		if isSourceCodePath(path) && (strings.ContainsAny(value, "([") || regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*\.`).MatchString(value) || regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*[,}]?$`).MatchString(value)) {
			return false
		}
		return true
	}
	if !isSourceCodePath(path) {
		return true
	}
	return variable == strings.ToUpper(variable) && operator == "="
}

func isGitHubWorkflowPath(path string) bool {
	normalized := filepath.ToSlash(path)
	return strings.HasPrefix(normalized, ".github/workflows/") || strings.Contains(normalized, "/.github/workflows/")
}

func isSourceCodePath(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go", ".py", ".js", ".ts", ".tsx", ".jsx", ".html", ".vue", ".java", ".rs":
		return true
	default:
		return false
	}
}

func isSensitiveIdentifier(variable string) bool {
	parts := sensitiveIdentifierParts(variable)
	for index, part := range parts {
		switch part {
		case "PASSWORD", "SECRET":
			return true
		case "TOKEN":
			if isNonSecretMetadataTail(parts[index+1:]) {
				continue
			}
			return true
		case "COOKIE":
			if len(parts[index+1:]) == 0 || isNonSecretMetadataTail(parts[index+1:]) {
				continue
			}
			return true
		case "KEY":
			if !hasSensitiveKeyQualifier(parts[:index]) || isNonSecretMetadataTail(parts[index+1:]) {
				continue
			}
			return true
		}
	}
	return false
}

func hasSensitiveKeyQualifier(parts []string) bool {
	for _, part := range parts {
		switch part {
		case "CACHE", "CONTEXT", "CURRENT", "FIELD", "MAP", "SETTING", "SETTINGS":
			return false
		}
	}
	for _, part := range parts {
		switch part {
		case "API", "ACCESS", "AUTH", "CLIENT", "ENCRYPTION", "HMAC", "PRIVATE", "SESSION", "SIGNATURE", "SIGNING", "WECHAT":
			return true
		}
	}
	return false
}

func sensitiveIdentifierParts(variable string) []string {
	var words []string
	for _, segment := range strings.FieldsFunc(variable, func(r rune) bool {
		return r == '_' || r == '-' || r == '.'
	}) {
		start := 0
		runes := []rune(segment)
		for index := 1; index < len(runes); index++ {
			lowerToUpper := runes[index-1] >= 'a' && runes[index-1] <= 'z' && runes[index] >= 'A' && runes[index] <= 'Z'
			acronymBoundary := index+1 < len(runes) && runes[index-1] >= 'A' && runes[index-1] <= 'Z' && runes[index] >= 'A' && runes[index] <= 'Z' && runes[index+1] >= 'a' && runes[index+1] <= 'z'
			if lowerToUpper || acronymBoundary {
				words = append(words, strings.ToUpper(string(runes[start:index])))
				start = index
			}
		}
		if start < len(runes) {
			word := strings.ToUpper(string(runes[start:]))
			for _, suffix := range []string{"PASSWORD", "SECRET", "COOKIE", "TOKEN", "KEY"} {
				if len(word) > len(suffix) && strings.HasSuffix(word, suffix) {
					words = append(words, strings.TrimSuffix(word, suffix), suffix)
					word = ""
					break
				}
			}
			if word != "" {
				words = append(words, word)
			}
		}
	}
	return words
}

func isNonSecretMetadataTail(parts []string) bool {
	if len(parts) == 0 {
		return false
	}
	for _, part := range parts {
		switch part {
		case "TTL", "SECOND", "SECONDS", "MILLISECOND", "MILLISECONDS", "ENABLED", "REQUIRED", "HEADER", "NAME", "TYPE", "COUNT", "PATH", "URL", "MODE", "STATUS", "EXPIRY", "EXPIRES", "LIFETIME", "ID", "PATTERN":
		default:
			return false
		}
	}
	return true
}

func isNestedEnvironmentReference(line string, matchStart int) bool {
	if matchStart <= 0 {
		return false
	}
	if line[matchStart-1] == '$' {
		return true
	}
	return matchStart > 1 && line[matchStart-1] == '{' && line[matchStart-2] == '$'
}

func isAllowedSensitiveDefault(candidate, variable string) bool {
	value := normalizeSensitiveCandidate(candidate)
	if value == "" || strings.EqualFold(value, variable) {
		return true
	}
	if exactEnvironmentReferencePattern.MatchString(value) || exactRequiredEnvironmentPattern.MatchString(value) {
		return true
	}
	if strings.HasPrefix(value, "${{") && strings.HasSuffix(value, "}}") && strings.Contains(value, "secrets.") {
		return true
	}
	if groups := exactDefaultEnvironmentPattern.FindStringSubmatch(value); len(groups) == 2 {
		return isAllowedSensitiveDefault(groups[1], variable)
	}
	lower := strings.ToLower(value)
	if strings.EqualFold(variable, "id-token") && lower == "write" {
		return true
	}
	if lower == "x" || lower == "bad" || lower == "test" || lower == "definitely-wrong" {
		return true
	}
	for _, marker := range []string{"__set_in_production_env__", "invalid-for-test-only", "change-me", "change_me", "example", "placeholder", "dummy", "redacted", "your"} {
		if lower == marker || strings.HasPrefix(lower, marker+"-") || strings.HasPrefix(lower, marker+"_") ||
			strings.HasPrefix(lower, marker+".") || strings.HasPrefix(lower, marker+":") || strings.HasPrefix(lower, marker+"/") {
			return true
		}
	}
	return false
}

func normalizeSensitiveCandidate(candidate string) string {
	value := strings.TrimSpace(candidate)
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "\"") || strings.HasPrefix(value, "'") || strings.HasPrefix(value, "`") {
		quote := value[0]
		if end := strings.IndexByte(value[1:], quote); end >= 0 {
			return strings.TrimSpace(value[1 : end+1])
		}
	}
	if comment := strings.IndexByte(value, '#'); comment >= 0 {
		value = value[:comment]
	}
	value = strings.TrimSpace(strings.TrimRight(value, ","))
	if !strings.HasPrefix(value, "${") {
		if fields := strings.Fields(value); len(fields) != 0 {
			value = fields[0]
		}
	}
	return value
}

func sanitizedLabel(value string) string {
	if value == "" {
		return "empty"
	}
	return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(value)))
}

func trackedPaths(t *testing.T, root, revision string) []string {
	t.Helper()
	var output []byte
	var err error
	if revision == "HEAD" {
		output, err = exec.Command("git", "-C", root, "ls-files", "-z").Output()
	} else {
		output, err = exec.Command("git", "-C", root, "ls-tree", "-r", "--name-only", "-z", revision).Output()
	}
	if err != nil {
		t.Fatalf("list tracked paths for %s: %v", revision, err)
	}
	paths := strings.Split(strings.TrimSuffix(string(output), "\x00"), "\x00")
	sort.Strings(paths)
	return paths
}

func formatSensitiveMatches(matches []sensitiveMatch) string {
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Revision != matches[j].Revision {
			return matches[i].Revision < matches[j].Revision
		}
		if matches[i].Path != matches[j].Path {
			return matches[i].Path < matches[j].Path
		}
		if matches[i].Line != matches[j].Line {
			return matches[i].Line < matches[j].Line
		}
		return matches[i].Variable < matches[j].Variable
	})
	const reportLimit = 40
	lines := make([]string, 0, min(len(matches), reportLimit)+1)
	for _, match := range matches[:min(len(matches), reportLimit)] {
		lines = append(lines, fmt.Sprintf("location=%s line=%d variable=%s revision=%s", sanitizedLabel(match.Path), match.Line, match.Variable, sanitizedLabel(match.Revision)))
	}
	if len(matches) > reportLimit {
		lines = append(lines, fmt.Sprintf("... %d more sanitized matches", len(matches)-reportLimit))
	}
	return strings.Join(lines, "\n")
}

func shortRevision(revision string) string {
	if len(revision) > 12 {
		return revision[:12]
	}
	return revision
}

func reachableRevisions(t *testing.T, root string) []string {
	t.Helper()
	headOutput, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("resolve HEAD for history scan: %v", err)
	}
	commandArgs := append([]string{"-C", root}, reachableRevisionArgs()...)
	listOutput, err := exec.Command("git", commandArgs...).Output()
	if err != nil {
		t.Fatalf("list reachable Git history: %v", err)
	}

	seen := make(map[string]struct{})
	revisions := make([]string, 0)
	add := func(revision string) {
		revision = strings.TrimSpace(revision)
		if revision == "" {
			return
		}
		if _, ok := seen[revision]; ok {
			return
		}
		seen[revision] = struct{}{}
		revisions = append(revisions, revision)
	}
	add(string(headOutput))
	for _, revision := range strings.Fields(string(listOutput)) {
		add(revision)
	}
	return revisions
}

func reachableRevisionArgs() []string {
	return []string{"rev-list", "--all", "HEAD"}
}

func revisionBatches(revisions []string, batchSize int) [][]string {
	if batchSize <= 0 {
		panic("history scan batch size must be positive")
	}
	batches := make([][]string, 0, (len(revisions)+batchSize-1)/batchSize)
	for start := 0; start < len(revisions); start += batchSize {
		end := start + batchSize
		if end > len(revisions) {
			end = len(revisions)
		}
		batches = append(batches, revisions[start:end])
	}
	return batches
}

func TestRepositoryHistoryScanBatchesIncludeHeadOnce(t *testing.T) {
	root := repositoryRoot(t)
	revisions := reachableRevisions(t, root)
	headOutput, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("resolve HEAD: %v", err)
	}
	head := strings.TrimSpace(string(headOutput))

	count := 0
	for _, revision := range revisions {
		if revision == head {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("HEAD occurs %d times in deduplicated revisions, want 1", count)
	}

	const batchSize = 8
	total := 0
	for _, batch := range revisionBatches(revisions, batchSize) {
		if len(batch) == 0 || len(batch) > batchSize {
			t.Fatalf("history scan batch size = %d, want 1..%d", len(batch), batchSize)
		}
		total += len(batch)
	}
	if total != len(revisions) {
		t.Fatalf("batched revisions = %d, want %d", total, len(revisions))
	}
}

func TestRepositoryReachableRevisionsIncludeDetachedHeadAncestors(t *testing.T) {
	repo := t.TempDir()
	gitTestOutput(t, repo, "init", "--quiet")
	gitTestOutput(t, repo, "config", "user.name", "Repository Policy Test")
	gitTestOutput(t, repo, "config", "user.email", "repository-policy@example.invalid")
	gitTestOutput(t, repo, "config", "commit.gpgsign", "false")

	fixturePath := filepath.Join(repo, "fixture.txt")
	if err := os.WriteFile(fixturePath, []byte("first\n"), 0o600); err != nil {
		t.Fatalf("write first fixture: %v", err)
	}
	gitTestOutput(t, repo, "add", "fixture.txt")
	gitTestOutput(t, repo, "commit", "--quiet", "-m", "first")
	first := strings.TrimSpace(gitTestOutput(t, repo, "rev-parse", "HEAD"))

	if err := os.WriteFile(fixturePath, []byte("second\n"), 0o600); err != nil {
		t.Fatalf("write second fixture: %v", err)
	}
	gitTestOutput(t, repo, "add", "fixture.txt")
	gitTestOutput(t, repo, "commit", "--quiet", "-m", "second")
	second := strings.TrimSpace(gitTestOutput(t, repo, "rev-parse", "HEAD"))
	branch := strings.TrimSpace(gitTestOutput(t, repo, "symbolic-ref", "--short", "HEAD"))
	gitTestOutput(t, repo, "checkout", "--quiet", "--detach", "HEAD")
	gitTestOutput(t, repo, "branch", "--delete", "--force", branch)
	if refs := strings.TrimSpace(gitTestOutput(t, repo, "for-each-ref", "--format=%(refname)")); refs != "" {
		t.Fatalf("temporary detached repository still has refs: %s", refs)
	}

	revisions := reachableRevisions(t, repo)
	if len(revisions) != 2 {
		t.Fatalf("reachable revisions = %d, want 2", len(revisions))
	}
	seen := make(map[string]bool)
	for _, revision := range revisions {
		seen[revision] = true
	}
	if !seen[first] || !seen[second] {
		t.Fatalf("detached history omitted HEAD or its ancestor")
	}
}

func TestRepositoryReachableRevisionCommandExplicitlyIncludesHead(t *testing.T) {
	if got := strings.Join(reachableRevisionArgs(), " "); got != "rev-list --all HEAD" {
		t.Fatalf("reachable revision command = %q, want %q", got, "rev-list --all HEAD")
	}
}

func gitTestOutput(t *testing.T, root string, args ...string) string {
	t.Helper()
	commandArgs := append([]string{"-C", root}, args...)
	output, err := exec.Command("git", commandArgs...).CombinedOutput()
	if err != nil {
		t.Fatalf("sanitized Git fixture command failed: %v", err)
	}
	return string(output)
}

func formatSanitizedLines(output []byte) string {
	var labels []string
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if line != "" {
			labels = append(labels, sanitizedLabel(line))
		}
	}
	return strings.Join(labels, "\n")
}

func credentialShapePattern() string {
	return strings.Join([]string{
		`gh[pousr]` + `_[A-Za-z0-9]{20,}`,
		`github` + `_pat_[A-Za-z0-9_]{20,}`,
		`-----BEGIN [A-Z ]*PRIVATE` + ` KEY-----`,
		`Cookie:[[:space:]]*` + `SUB=`,
		`SUB` + `=[A-Za-z0-9%._-]{20,}`,
	}, "|")
}

func TestRepositoryCredentialShapePatternRecognizesGitHubTokens(t *testing.T) {
	pattern := regexp.MustCompile(credentialShapePattern())
	positivePrefixes := []struct {
		name   string
		prefix string
	}{
		{name: "classic_pat", prefix: "ghp" + "_"},
		{name: "oauth", prefix: "gho" + "_"},
		{name: "user_to_server", prefix: "ghu" + "_"},
		{name: "server_to_server", prefix: "ghs" + "_"},
		{name: "refresh", prefix: "ghr" + "_"},
		{name: "fine_grained", prefix: "github" + "_pat_"},
	}
	for _, tc := range positivePrefixes {
		t.Run("positive_"+tc.name, func(t *testing.T) {
			candidate := tc.prefix + strings.Repeat("Ab3", 10)
			if !pattern.MatchString(candidate) {
				t.Fatal("credential-shaped token was not detected")
			}
		})
	}

	negativeCases := []struct {
		name      string
		candidate string
	}{
		{name: "classic_too_short", candidate: "ghp" + "_" + strings.Repeat("A", 19)},
		{name: "fine_grained_too_short", candidate: "github" + "_pat_short"},
		{name: "unknown_prefix", candidate: "ghx" + "_" + strings.Repeat("A", 30)},
		{name: "invalid_characters", candidate: "ghp" + "_" + strings.Repeat("-", 30)},
	}
	for _, tc := range negativeCases {
		t.Run("negative_"+tc.name, func(t *testing.T) {
			if pattern.MatchString(tc.candidate) {
				t.Fatal("non-token text was classified as a credential")
			}
		})
	}
}

func TestRepositorySensitivePathPolicyCoversEnvironmentPrefixesAndCertificates(t *testing.T) {
	for _, path := range []string{
		".env", ".env.production", ".envrc", "config/.env-prod",
		"secrets/server.crt", "secrets/server.cer", "secrets/server.cert",
		"secrets/server.der", "secrets/client.pfx",
	} {
		if reason := forbiddenTrackedFileReason(path); reason == "" {
			t.Errorf("%q is not rejected", path)
		}
	}
}

func TestRepositoryIgnoreFilesCoverSensitiveExtensions(t *testing.T) {
	requiredPatterns := []string{
		".env*", "*.pem", "*.key", "*.p12",
		"*.crt", "*.cer", "*.cert", "*.der", "*.pfx",
	}
	for _, path := range []string{".gitignore", ".dockerignore"} {
		contents, err := os.ReadFile(filepath.Join(repositoryRoot(t), path))
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		lines := make(map[string]struct{})
		for _, line := range strings.Split(string(contents), "\n") {
			lines[strings.TrimSpace(line)] = struct{}{}
		}
		for _, pattern := range requiredPatterns {
			if _, ok := lines[pattern]; !ok {
				t.Errorf("%s does not exclude %q", path, pattern)
			}
		}
	}
}

func TestRepositoryGitIgnoreKeepsSourcePackagesTrackable(t *testing.T) {
	root := repositoryRoot(t)
	for _, path := range []string{"internal/cache/redis.go", "internal/download/service.go"} {
		ignored, err := gitCheckIgnore(root, path)
		if err != nil {
			t.Fatalf("check ignore policy for %s: %v", path, err)
		}
		if ignored {
			t.Errorf("source path %q must not be ignored", path)
		}
	}
}

func TestRepositoryGitIgnoreRootsRuntimeArtifacts(t *testing.T) {
	root := repositoryRoot(t)
	for _, path := range []string{
		"cache/item", "logs/app.log", "tmp/item", "data/mysql/ibdata1",
		"data/redis/dump.rdb", "downloads/video.mp4", "docker-export/image.tar",
		"runtime.db", "video.mp4", "image.tar",
	} {
		ignored, err := gitCheckIgnore(root, path)
		if err != nil {
			t.Fatalf("check ignore policy for %s: %v", path, err)
		}
		if !ignored {
			t.Errorf("root runtime artifact %q must be ignored", path)
		}
	}
}

func TestRepositoryDockerIgnoreRootsRuntimeArtifacts(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join(repositoryRoot(t), ".dockerignore"))
	if err != nil {
		t.Fatalf("read .dockerignore: %v", err)
	}
	lines := make(map[string]struct{})
	for _, line := range strings.Split(string(contents), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			lines[line] = struct{}{}
		}
	}

	for _, pattern := range []string{
		"/cache/**", "/.cache/**", "/logs/**", "/tmp/**", "/.tmp/**", "/.tmp-yt/**", "/reports/**", "/artifacts/**",
		"/.docker-data/**", "/data/mysql/**", "/data/redis/**", "/mysql-data/**", "/redis-data/**", "/appendonlydir/**",
		"/download/**", "/downloads/**", "/docker-export/**", "/docker-exports/**",
		"/*.db", "/*.rdb", "/*.aof", "/*.mp4", "/*.tar", "/*.oci",
	} {
		if _, ok := lines[pattern]; !ok {
			t.Errorf(".dockerignore does not contain rooted pattern %q", pattern)
		}
	}

	for _, pattern := range []string{
		"cache", ".cache", "logs", "tmp", ".tmp", ".tmp-yt", "reports",
		".docker-data", "data/mysql", "data/redis", "mysql-data", "redis-data", "appendonlydir",
		"download", "downloads", "docker-export", "docker-exports",
		"*.db", "*.rdb", "*.aof", "*.mp4", "*.tar", "*.oci",
	} {
		if _, ok := lines[pattern]; ok {
			t.Errorf(".dockerignore contains source-swallowing pattern %q", pattern)
		}
	}
}

func TestRepositoryTraceabilityUsesPlannedWorkflowPath(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join(repositoryRoot(t), "docs", "requirements-traceability.md"))
	if err != nil {
		t.Fatalf("read requirements traceability: %v", err)
	}
	if strings.Contains(string(contents), ".github/workflows/image.yml") {
		t.Fatal("requirements traceability references the obsolete image workflow path")
	}
	if !strings.Contains(string(contents), ".github/workflows/ci-image.yml") {
		t.Fatal("requirements traceability does not reference the planned CI image workflow path")
	}
}

func gitCheckIgnore(root, path string) (bool, error) {
	err := exec.Command("git", "-C", root, "check-ignore", "--no-index", "--quiet", "--", path).Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

func TestRepositoryComposeBuildKeyPolicyCoversQuotedKeys(t *testing.T) {
	for _, compose := range []string{
		"services:\n  api:\n    build: .\n",
		"services:\n  api:\n    \"build\": .\n",
		"services:\n  api:\n    'build': .\n",
		"services: {api: {build: .}}\n",
		"services:\n  api:\n    ? build\n    : .\n",
	} {
		services, err := composeServicesWithBuild([]byte(compose))
		if err != nil {
			t.Fatalf("parse Compose fixture: %v", err)
		}
		if len(services) == 0 {
			t.Errorf("build key not detected in %q", compose)
		}
	}

	services, err := composeServicesWithBuild([]byte("services:\n  api:\n    image: ghcr.io/example/build:latest\n"))
	if err != nil {
		t.Fatalf("parse safe Compose fixture: %v", err)
	}
	if len(services) != 0 {
		t.Fatal("image value containing build must not be treated as a build key")
	}
}

func TestRepositoryDeployComposeDoesNotBuildOnTarget(t *testing.T) {
	composePath := filepath.Join(repositoryRoot(t), "deploy", "compose.yml")
	contents, err := os.ReadFile(composePath)
	if errors.Is(err, os.ErrNotExist) {
		t.Skip("deploy/compose.yml is introduced by a later implementation task")
	}
	if err != nil {
		t.Fatalf("read deploy/compose.yml: %v", err)
	}

	services, err := composeServicesWithBuild(contents)
	if err != nil {
		t.Fatalf("parse deploy/compose.yml: %v", err)
	}
	if len(services) != 0 {
		t.Fatalf("deploy/compose.yml must pull prebuilt images; services with a build key: %s", strings.Join(services, ", "))
	}
}

func TestRepositoryAllTrackedYAMLDocumentsEnforceComposePolicy(t *testing.T) {
	root := repositoryRoot(t)
	var violations []string
	for _, path := range trackedPaths(t, root, "HEAD") {
		if !isYAMLPath(path) {
			continue
		}
		contents, err := exec.Command("git", "-C", root, "show", ":"+path).Output()
		if err != nil {
			t.Fatalf("read indexed YAML document %s: %v", sanitizedLabel(path), err)
		}
		documentViolations, err := composePolicyViolations(contents, path)
		if err != nil {
			t.Fatalf("parse indexed YAML document %s: %v", sanitizedLabel(path), err)
		}
		for _, violation := range documentViolations {
			violations = append(violations, fmt.Sprintf("location=%s kind=%s", sanitizedLabel(path), violation))
		}
	}
	if len(violations) != 0 {
		t.Fatalf("indexed YAML documents violate the Compose policy:\n%s", strings.Join(violations, "\n"))
	}
}

func isYAMLPath(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".yml" || ext == ".yaml"
}

func TestRepositorySourceProvenanceIsMachineReadableAndPinned(t *testing.T) {
	root := repositoryRoot(t)
	contents, err := os.ReadFile(filepath.Join(root, "docs", "source-provenance.json"))
	if err != nil {
		t.Fatalf("read source provenance: %v", err)
	}
	var provenance struct {
		OriginalRepo       string `json:"original_repo"`
		SourceCommit       string `json:"source_commit"`
		SourceTree         string `json:"source_tree"`
		CleanHistoryReason string `json:"clean_history_reason"`
		CreatedAt          string `json:"created_at"`
	}
	if err := json.Unmarshal(contents, &provenance); err != nil {
		t.Fatalf("parse source provenance: %v", err)
	}
	if provenance.OriginalRepo != "https://github.com/1136623363/watermark-backend" {
		t.Errorf("original_repo is not the pinned source repository")
	}
	if provenance.SourceCommit != "1d3dc9a6064f3f2e41af9ea92a29566885939175" {
		t.Errorf("source_commit is not pinned to the approved baseline")
	}
	if provenance.SourceTree != "d1aac032059b5622fc9f0b5cd6ce321a77978a1e" {
		t.Errorf("source_tree is not pinned to the approved source tree")
	}
	if _, err := time.Parse(time.RFC3339, provenance.CreatedAt); err != nil {
		t.Errorf("created_at is not RFC3339: %v", err)
	}
	for name, value := range map[string]string{
		"source_tree": provenance.SourceTree, "clean_history_reason": provenance.CleanHistoryReason, "created_at": provenance.CreatedAt,
	} {
		if strings.TrimSpace(value) == "" {
			t.Errorf("%s must not be empty", name)
		}
	}
}

func TestRepositoryPlanUsesCanonicalPathsAndBoundaries(t *testing.T) {
	root := repositoryRoot(t)
	body := readPolicyDocument(t, root, "docs/superpowers/plans/2026-07-13-watermark-go-single-node-refactor.md")
	assertCanonicalPlanOrTracePaths(t, body)
	for _, required := range []string{
		"tests/baseline/fixtures/platform-samples.json",
		"internal/admin/baseline.go",
		"internal/admin/baseline_test.go",
		"scripts/baseline/run.py",
		"tests/baseline/test_report.py",
		"tests/contracts/frontend_contract_test.go",
		"tests/e2e/test_frontend_flow.py",
		"internal/httpapi/client_handlers_test.go",
		"internal/httpapi/parse_contract_test.go",
		"internal/httpapi/parse_task_contract_test.go",
		"internal/httpapi/download_contract_test.go",
		"internal/observability/client_performance_test.go",
		"scripts/verify-image.sh",
		"scripts/smoke.sh",
		"go1.26.5",
		"golang:1.26.5",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("planning documents omit canonical path or version %q", required)
		}
	}
	assertUnambiguousRollbackDatabaseTarget(t, body)

	assertForbiddenPlanOrTracePaths(t, body)
}

func TestRepositoryTraceabilityUsesCanonicalPathsAndBoundaries(t *testing.T) {
	root := repositoryRoot(t)
	body := readPolicyDocument(t, root, "docs/requirements-traceability.md")
	assertCanonicalPlanOrTracePaths(t, body)
	assertForbiddenPlanOrTracePaths(t, body)
}

func TestRepositoryPlanUsesExternalCanonicalFixtureTrustAnchor(t *testing.T) {
	root := repositoryRoot(t)
	body := readPolicyDocument(t, root, "docs/superpowers/plans/2026-07-13-watermark-go-single-node-refactor.md")
	task := policyDocumentSection(t, body, "## 任务 10：", "## 任务 11：")
	for _, required := range []string{
		"canonicalFixturePath", "canonicalFixtureSha256", "完整 bytes", "禁止写入 fixture 自身",
		"Go/Python 固定字面量", "fixture/report 自报", "任务 10 生成并独立审查后",
	} {
		if !strings.Contains(task, required) {
			t.Errorf("baseline fixture trust contract omits %q", required)
		}
	}
	if strings.Contains(task, "同时固定在 manifest") {
		t.Fatal("canonical fixture hash remains self-referential")
	}
}

func TestRepositoryPlanDefinesSingleSafeInitialRollbackPath(t *testing.T) {
	root := repositoryRoot(t)
	body := readPolicyDocument(t, root, "docs/superpowers/plans/2026-07-13-watermark-go-single-node-refactor.md")
	task := policyDocumentSection(t, body, "## 任务 14：", "## 任务 15：")
	assertOrderedPolicyMarkers(t, task, []string{
		"首次部署没有 previous image", "栅栏/排空新服务", "outbox reverse replay",
		"唯一指定、实际承接回滚生产流量的隔离旧库克隆", "checksum",
		"原子切换旧服务 DSN", "验证连接身份", "恢复旧路由", "禁止用早期备份覆盖",
	})
	if strings.Contains(task, "MySQL 备份恢复流程") {
		t.Fatal("Task 14 still permits destructive rollback from an early backup")
	}
}

func TestRepositoryDocumentsPostCutoverFailureTrapAndWechatReadiness(t *testing.T) {
	root := repositoryRoot(t)
	plan := readPolicyDocument(t, root, "docs/superpowers/plans/2026-07-13-watermark-go-single-node-refactor.md")
	task := policyDocumentSection(t, plan, "## 任务 18：", "")
	readiness := strings.Index(task, "一次性 wx.login code readiness")
	cutover := strings.Index(task, "步骤 2：")
	if readiness < 0 || cutover < 0 || readiness >= cutover || !strings.Contains(task, "未就绪不得进入写栅栏或切流") {
		t.Fatal("Task 18 must prove WeChat runtime readiness before the write fence or cutover")
	}
	for _, required := range []string{
		"/var/lib/watermark-go/runtime.env", "--env-file /var/lib/watermark-go/runtime.env",
		"统一 post-cutover failure trap", "步骤 3、4、5、6 任一失败", "立即 fence/drain 新写",
		"自动调用已演练且适用的 rollback 分支", "禁止恢复旧路由", "不得让疑似坏版本继续接受写",
		"受控只读/隔离", "FAILED", "旧侧可恢复状态", "full/final/reverse 坐标",
		"checksum/route/DB identity/duration/result",
	} {
		if !containsPolicyText(task, required) {
			t.Errorf("Task 18 post-cutover contract omits %q", required)
		}
	}

	design := readPolicyDocument(t, root, "docs/superpowers/specs/2026-07-13-watermark-go-single-node-refactor-design.md")
	for _, required := range []string{
		"/var/lib/watermark-go/runtime.env", "一次性 wx.login code readiness", "未就绪不得进入写栅栏或切流",
		"统一 post-cutover failure trap", "步骤 3、4、5、6 任一失败", "立即 fence/drain 新写",
		"自动调用已演练且适用的 rollback 分支", "禁止恢复旧路由", "不得让疑似坏版本继续接受写",
		"受控只读/隔离", "FAILED", "旧侧可恢复状态", "full/final/reverse 坐标",
		"checksum/route/DB identity/duration/result",
	} {
		if !containsPolicyText(design, required) {
			t.Errorf("design post-cutover contract omits %q", required)
		}
	}

	trace := readPolicyDocument(t, root, "docs/requirements-traceability.md")
	for _, body := range []string{plan, design, trace} {
		if !containsPolicyText(body, "/var/lib/watermark-go/runtime.env") {
			t.Error("document omits the repository-external runtime file")
		}
		if containsPolicyText(body, "deploy/.env.runtime") {
			t.Error("document retains the repository-local runtime file")
		}
	}
}

func TestRepositoryPlanDefinesCompletePerRunAcceptanceGate(t *testing.T) {
	root := repositoryRoot(t)
	body := readPolicyDocument(t, root, "docs/superpowers/plans/2026-07-13-watermark-go-single-node-refactor.md")
	task := policyDocumentSection(t, body, "## 任务 14：", "## 任务 15：")
	for _, required := range []string{
		"unique runId", "canonicalFixtureSha256", "concurrency=3", "nativeEnabled=true",
		"fallbackEnabled=true", "cacheBypass=true", "completed=93", "success>=62",
		"durationMs<=216000", "93 个 enabled 样本", "parserInvocationId",
		"passed=true 不能绕过", "负向夹具逐项覆盖",
	} {
		if !containsPolicyText(task, required) {
			t.Errorf("per-run acceptance contract omits %q", required)
		}
	}
}

func TestRepositoryPlanPinsComposeBindAllowlistAndCISelfLock(t *testing.T) {
	root := repositoryRoot(t)
	plan := readPolicyDocument(t, root, "docs/superpowers/plans/2026-07-13-watermark-go-single-node-refactor.md")
	task13 := policyDocumentSection(t, plan, "## 任务 13：", "## 任务 14：")
	for _, required := range []string{
		"RECOVERY_API_HOST_PORT", "CANDIDATE_API_HOST_PORT", "严格 allowlist", "127.0.0.1:15001",
		"127.0.0.1:5001", "`0.0.0.0`", "--schema-of-present", "--require-complete",
		"仅 final evidence push", "unit/schema-of-present", "docs/artifacts-only",
	} {
		if !strings.Contains(task13, required) {
			t.Errorf("Compose or CI self-lock contract omits %q", required)
		}
	}
	task18 := policyDocumentSection(t, plan, "## 任务 18：", "")
	for _, required := range []string{
		"隐式固定 bind `127.0.0.1`", "CANDIDATE_API_HOST_PORT=5001", "Task 18 禁止启用 LAN override",
	} {
		if !strings.Contains(task18, required) {
			t.Errorf("final runtime bind contract omits %q", required)
		}
	}
	if !strings.Contains(task18, "verify-acceptance.py --require-complete") {
		t.Fatal("final evidence push does not require complete acceptance evidence")
	}
}

func TestRepositoryPlanDefinesAtomicEvidenceAndFinalContainerDiff(t *testing.T) {
	root := repositoryRoot(t)
	plan := readPolicyDocument(t, root, "docs/superpowers/plans/2026-07-13-watermark-go-single-node-refactor.md")
	for _, tc := range []struct {
		start, end, artifact string
	}{
		{"## 任务 15：", "## 任务 16：", "artifacts/verification/local-verification.md"},
		{"## 任务 17：", "## 任务 18：", "artifacts/acceptance/shadow-e2e.json"},
		{"## 任务 18：", "", "artifacts/deploy/public-cutover.json"},
	} {
		section := policyDocumentSection(t, plan, tc.start, tc.end)
		for _, required := range []string{
			tc.artifact, "同目录 0600 临时文件", "fsync", "原子 rename", "schemaVersion", "passed", "脱敏",
		} {
			if !strings.Contains(section, required) {
				t.Errorf("%s evidence contract omits %q", tc.artifact, required)
			}
		}
	}
	task18 := policyDocumentSection(t, plan, "## 任务 18：", "")
	for _, required := range []string{
		"观察结束再次写最终 after/diff", "与任务 17 的 before 比较", "无关容器变化必须为零",
		"verify-acceptance.py 强制校验",
	} {
		if !strings.Contains(task18, required) {
			t.Errorf("final container inventory contract omits %q", required)
		}
	}
}

func TestRepositoryPlanDefinesFixedFrontendDomainMatrix(t *testing.T) {
	root := repositoryRoot(t)
	plan := readPolicyDocument(t, root, "docs/superpowers/plans/2026-07-13-watermark-go-single-node-refactor.md")
	task := policyDocumentSection(t, plan, "## 任务 18：", "")
	for _, required := range []string{
		"session", "syncParse", "asyncSubmit", "asyncPoll", "cacheRestore", "performance",
		"fallbackCreate", "fallbackPoll", "fallbackDownload", "m3u8Create", "m3u8Poll",
		"m3u8Download", "video", "gallery", "requestId", "passed",
	} {
		if !strings.Contains(task, required) {
			t.Errorf("frontend domain evidence matrix omits %q", required)
		}
	}
}

func TestRepositoryPlanKeepsRollbackRehearsalIsolatedAndClosesBothBranches(t *testing.T) {
	root := repositoryRoot(t)
	plan := readPolicyDocument(t, root, "docs/superpowers/plans/2026-07-13-watermark-go-single-node-refactor.md")
	task := policyDocumentSection(t, plan, "## 任务 17：", "## 任务 18：")
	for _, required := range []string{
		"隔离 old-service clone", "隔离 DB clone", "影子路由", "不得修改在线旧服务 DSN",
		"不得切换在线写流", "实际切换只允许任务 18", "当前适用分支真实演练",
		"隔离等价演练", "durationSeconds<=300", "healthPassed=true",
		"dataPassed=true", "branches.previousDigest", "branches.initialDeployment.applicable=false",
		"result=not_applicable_no_verified_legacy_service", "isolatedCompatibilityRehearsal",
		"不得计入实际 rollback/pass",
	} {
		if !containsPolicyText(task, required) {
			t.Errorf("rollback rehearsal contract omits %q", required)
		}
	}
	design := readPolicyDocument(t, root, "docs/superpowers/specs/2026-07-13-watermark-go-single-node-refactor-design.md")
	for _, required := range []string{
		"隔离 old-service clone", "隔离 DB clone", "不得修改在线旧服务 DSN", "不得切换在线写流",
		"当前适用分支真实演练", "隔离等价演练", "durationSeconds<=300",
		"healthPassed=true", "dataPassed=true",
	} {
		if !containsPolicyText(design, required) {
			t.Errorf("design rollback rehearsal contract omits %q", required)
		}
	}
}

func TestRepositoryPlanDefinesASTNetworkAndCookiePolicyCommitBoundary(t *testing.T) {
	root := repositoryRoot(t)
	plan := readPolicyDocument(t, root, "docs/superpowers/plans/2026-07-13-watermark-go-single-node-refactor.md")
	task3 := policyDocumentSection(t, plan, "## 任务 3：", "## 任务 4：")
	for _, required := range []string{
		"const", "VarSpec", ":=", "scope-correct", "shadowing",
		"git add -A -- internal/parsers internal/parser", "internal/policy/parser_egress_test.go",
	} {
		if !strings.Contains(task3, required) {
			t.Errorf("parser Cookie gate or commit boundary omits %q", required)
		}
	}
	task4 := policyDocumentSection(t, plan, "## 任务 4：", "## 任务 5：")
	for _, required := range []string{
		"internal/policy/network_egress_test.go", "AST/go-types", "&http.Client", "http.Transport",
		"http.DefaultTransport", "net.Dial*", "自定义 RoundTripper", "import alias",
		"dot import", "resty.New", "netguard transport", "os/exec.Command*", "yt-dlp/universal/ffmpeg",
		"结构化 argv builder", "sh/bash", "curl/wget/nc", "动态可执行名",
		"internal/runtimecfg/settings.go", "internal/server/client_auth.go",
		"internal/server/download_fallback.go", "internal/server/cluster.go",
		"internal/server/cluster_platform_tests.go", "git diff --name-only",
		"全部 staged", "cmd/parser-helper", "cmd/netguard-proxy", "internal/parser/sandbox",
	} {
		if !strings.Contains(task4, required) {
			t.Errorf("network egress gate or commit boundary omits %q", required)
		}
	}
}

func TestRepositoryPlanSeparatesReviewFixAndEvidenceCommits(t *testing.T) {
	root := repositoryRoot(t)
	plan := readPolicyDocument(t, root, "docs/superpowers/plans/2026-07-13-watermark-go-single-node-refactor.md")
	task := policyDocumentSection(t, plan, "## 任务 15：", "## 任务 16：")
	for _, required := range []string{
		"must-fix 先写失败测试", "精确 implementation/test 路径", "禁止 `git add -A`",
		"fix: address pre-release review findings", "重跑步骤 1–2", "独立 evidence commit",
		"test: record local verification evidence", "tests/policy/test_python_bridge_security.py",
		"scripts/verify-frontend-provenance.sh", "test_miniprogram_*.js",
		"go test ./internal/policy", "git diff --cached --check", "scripts/verify-gitleaks.sh",
	} {
		if !strings.Contains(task, required) {
			t.Errorf("review-fix/evidence commit contract omits %q", required)
		}
	}
}

func TestRepositoryPlanDefinesRouteAuthCompatibilityInventory(t *testing.T) {
	root := repositoryRoot(t)
	plan := readPolicyDocument(t, root, "docs/superpowers/plans/2026-07-13-watermark-go-single-node-refactor.md")
	for _, tc := range []struct {
		start, end string
		required   []string
	}{
		{"## 任务 7：", "## 任务 8：", []string{"crypto-random", "至少 128 位", "bearer capability", "legacy shareId", "只读恢复", "熵失败"}},
		{"## 任务 8：", "## 任务 9：", []string{"parse task ID", "crypto-random", "至少 128 位", "bearer capability", "公开轮询不要求 token"}},
		{"## 任务 9：", "## 任务 10：", []string{"fallback create", "attempt/limit/SSRF", "cache shareId", "parse poll", "fallback poll/download", "签名 ticket URL", "m3u8 create", "/api/task/:id", "最终 file URL", "token/Bearer"}},
		{"## 任务 11：", "## 任务 12：", []string{"cache/parse poll", "random >=128-bit ID bearer", "fallback poll/download", "签名 ticket URL", "m3u8 create/task poll", "/api/task/:id", "file download 才要求签名 URL", "token/Bearer"}},
		{"## 任务 12：", "## 任务 13：", []string{"cache/parse poll", "随机 >=128-bit ID bearer", "fallback", "签名 poll/download URL", "m3u8 task poll", "最终 file URL 才签名", "token/Bearer"}},
	} {
		section := policyDocumentSection(t, plan, tc.start, tc.end)
		for _, required := range tc.required {
			if !containsPolicyText(section, required) {
				t.Errorf("%s route-auth inventory omits %q", tc.start, required)
			}
		}
	}
}

func TestRepositoryDocumentsRecordVerifiedCleanRootAndR4AsRuntimeValidated(t *testing.T) {
	root := repositoryRoot(t)
	plan := readPolicyDocument(t, root, "docs/superpowers/plans/2026-07-13-watermark-go-single-node-refactor.md")
	task1 := policyDocumentSection(t, plan, "## 任务 1：", "## 任务 2：")
	for _, required := range []string{
		"- [x] **步骤 7：审查批准后重建根并复验**",
		"rootCommit=5a1dd14aa38c63091d8d7139fd0024718b79bdbb",
		"rootTree=525e9fb72308c4af5478ffcc1705d8af73c82c1e",
		"无 parent", "reflog 为空", "fsck 无 unreachable", "v8.30.1", "full scan PASS",
	} {
		if !strings.Contains(task1, required) {
			t.Errorf("Task 1 clean-root completion evidence omits %q", required)
		}
	}
	trace := readPolicyDocument(t, root, "docs/requirements-traceability.md")
	if !strings.Contains(trace, "任务 1 已建立仓库门禁与经验证的唯一干净根历史") {
		t.Fatal("traceability does not record the verified clean root history")
	}
	for _, required := range []string{"任务 12 契约验证", "任务 17 运行验证"} {
		if !strings.Contains(trace, required) {
			t.Errorf("R4 traceability status omits %q", required)
		}
	}
}

func TestRepositoryPlanRejectsForgedObservationAndBenchmarkAggregates(t *testing.T) {
	root := repositoryRoot(t)
	plan := readPolicyDocument(t, root, "docs/superpowers/plans/2026-07-13-watermark-go-single-node-refactor.md")
	task14 := policyDocumentSection(t, plan, "## 任务 14：", "## 任务 15：")
	for _, required := range []string{
		"endedAt-startedAt>=1800s", "第一个样本在 startedAt 后约 30 秒", "第 60 个样本不早于 startedAt+1800s",
		"60 个唯一且严格递增", "25–35 秒", "imageDigest", "healthLatencyMs", "restartCount",
		"oomCount", "ioErrors", "memoryPSI", "ioPSI", "从 60 个原始样本重算 P95", "不信任 60/60 聚合",
		"93 个唯一 sampleKey", "canonical enabled set", "从 records 重算 completed/success/wall-clock",
		"三轮时间窗不重叠", "records 不复用", "media success", "parserInvocationId", "不信任 aggregate",
	} {
		if !containsPolicyText(task14, required) {
			t.Errorf("anti-forgery verifier contract omits %q", required)
		}
	}
}

func TestRepositoryPlanPinsReproducibleRuntimeAndImageSourceBinding(t *testing.T) {
	root := repositoryRoot(t)
	plan := readPolicyDocument(t, root, "docs/superpowers/plans/2026-07-13-watermark-go-single-node-refactor.md")
	task13 := policyDocumentSection(t, plan, "## 任务 13：", "## 任务 14：")
	for _, required := range []string{
		"ffmpeg 精确包版本", "reproducible snapshot", "固定制品 SHA-256", "--require-hashes",
		"每个 Python 依赖", "org.opencontainers.image.revision", "org.opencontainers.image.source",
		"provenance attestation subject", "完整 40 位 source commit", "实际 RepoDigest",
		"tag 仅作索引", "scripts/verify-image.sh", "runtime inspect", "release/promotion-marker.txt",
		"不得 COPY 进 rootfs", "-trimpath -buildvcs=false", "清空 buildid", "禁止用 ldflags",
		"禁止生成 `.pyc`", "canonical rootfs inventory", "两个模拟 revision",
	} {
		if !containsPolicyText(task13, required) {
			t.Errorf("reproducible runtime or image binding contract omits %q", required)
		}
	}
	task16And17 := policyDocumentSection(t, plan, "## 任务 16：", "## 任务 18：")
	for _, required := range []string{
		"sbom-recovery.spdx.json", "sbom-final.spdx.json", "repository-and-image.txt", "按 A/B role",
		"B promotion push 前", "fixed Gitleaks full-history",
	} {
		if !containsPolicyText(task16And17, required) {
			t.Errorf("release evidence role contract omits %q", required)
		}
	}
}

func TestRepositoryPlanKeepsModuleAndParserMovesBuildable(t *testing.T) {
	root := repositoryRoot(t)
	plan := readPolicyDocument(t, root, "docs/superpowers/plans/2026-07-13-watermark-go-single-node-refactor.md")
	for _, tc := range []struct {
		start, end string
		required   []string
	}{
		{"## 任务 2：", "## 任务 3：", []string{"git grep -l", "watermark-" + "backend/", "机械替换全部 tracked Go", "go mod tidy", "git grep -n", "go test ./...", "精确清单"}},
		{"## 任务 3：", "## 任务 4：", []string{"production import 精确字面量", ":!internal/policy/**", "internal/server", "go test ./...", "broken tree", "精确 server callsite"}},
	} {
		section := policyDocumentSection(t, plan, tc.start, tc.end)
		for _, required := range tc.required {
			if !containsPolicyText(section, required) {
				t.Errorf("%s buildability contract omits %q", tc.start, required)
			}
		}
	}
}

func TestRepositoryPlanSeparatesHermeticStoreTestsFromPinnedIntegration(t *testing.T) {
	root := repositoryRoot(t)
	plan := readPolicyDocument(t, root, "docs/superpowers/plans/2026-07-13-watermark-go-single-node-refactor.md")
	task5 := policyDocumentSection(t, plan, "## 任务 5：", "## 任务 6：")
	for _, required := range []string{
		"sqlmock/golden/纯 typed importer", "禁止 `startTestMySQL`", "testcontainers", "连接宿主 MariaDB",
		"tests/integration/store", "MariaDB 11.8", "MySQL 8.4", "CI service 未 ready 必须 FAIL", "不能 skip",
	} {
		if !containsPolicyText(task5, required) {
			t.Errorf("Task 5 hermetic/integration boundary omits %q", required)
		}
	}
	task13 := policyDocumentSection(t, plan, "## 任务 13：", "## 任务 14：")
	for _, required := range []string{"go test -tags=integration ./tests/integration/store", "未执行或 skip", "目标宿主 Task 17 前"} {
		if !containsPolicyText(task13, required) {
			t.Errorf("Task 13 store integration CI gate omits %q", required)
		}
	}
}

func TestRepositoryPlanBindsAtomicEvidenceToCurrentCutoverAttempt(t *testing.T) {
	root := repositoryRoot(t)
	plan := readPolicyDocument(t, root, "docs/superpowers/plans/2026-07-13-watermark-go-single-node-refactor.md")
	task14 := policyDocumentSection(t, plan, "## 任务 14：", "## 任务 15：")
	for _, required := range []string{
		"scripts/write-evidence.py", "schemaVersion", "passed", "同目录 0600 temp", "file fsync",
		"directory fsync", "passed=false tombstone", "禁止 `printf >`", "deploymentRunId", "cutoverAttemptId",
		"旧 `passed=true` artifact", "set -Eeuo pipefail", "ERR/EXIT/HUP/INT/TERM", "in_progress",
		"runtime inspect", "reconcile", "A bootstrap", "B cutover", "pull-and-up.txt", "B final up",
		"localBuild=false", "localLoad=false",
	} {
		if !containsPolicyText(task14, required) {
			t.Errorf("Task 14 atomic/current-attempt evidence gate omits %q", required)
		}
	}
	task18 := policyDocumentSection(t, plan, "## 任务 18：", "")
	for _, required := range []string{
		"deploymentRunId", "cutoverAttemptId", "state-before.json", "frontend-domain-e2e.json",
		"observation-30m.json", "final-acceptance.md", "running-digest.txt", "B final up event",
		"当前 attempt", "passed=false tombstone", "旧 `passed=true`", "时间窗", "in_progress",
		"ERR/EXIT/HUP/INT/TERM", "回退成功后", "实际 A identity",
	} {
		if !containsPolicyText(task18, required) {
			t.Errorf("Task 18 current-attempt state machine omits %q", required)
		}
	}
}

func TestRepositoryPlanAvoidsEvidenceCommitSelfReferenceAndLocksFinalBArtifacts(t *testing.T) {
	root := repositoryRoot(t)
	plan := readPolicyDocument(t, root, "docs/superpowers/plans/2026-07-13-watermark-go-single-node-refactor.md")
	for _, required := range []string{
		"evidenceParentCommit", "evidencePayloadTreeSha256", "verifiedEvidenceCommit", "GITHUB_SHA",
		"git rev-parse HEAD", "docs/artifacts-only diff", "不得回写 tracked report", "final-shadow-e2e.json",
		"canonical `artifacts/release/image-digest.txt`", "B attestation", "B shadow", "原子生成",
	} {
		if !containsPolicyText(plan, required) {
			t.Errorf("final evidence/B artifact binding omits %q", required)
		}
	}
	if containsPolicyText(plan, "最终报告另记录 `evidenceCommit`") {
		t.Fatal("tracked final report still requires a self-referential current evidenceCommit")
	}
}

func TestRepositoryPlanRecomputesConcurrencyAndRedactsCapabilities(t *testing.T) {
	root := repositoryRoot(t)
	plan := readPolicyDocument(t, root, "docs/superpowers/plans/2026-07-13-watermark-go-single-node-refactor.md")
	task14 := policyDocumentSection(t, plan, "## 任务 14：", "## 任务 15：")
	for _, required := range []string{
		"maxObservedConcurrency", "started/ended", "half-open", "实际达到 3", "四条重叠", "全程串行",
	} {
		if !containsPolicyText(task14, required) {
			t.Errorf("benchmark concurrency anti-forgery gate omits %q", required)
		}
	}
	for _, required := range []string{
		"share/task capability ID", "签名 ticket", "完整 path/query", "route template", "same-origin/HTTPS",
		"字节数/内容 hash", "不可逆 hash", "sentinel",
	} {
		if !containsPolicyText(plan, required) {
			t.Errorf("capability evidence redaction contract omits %q", required)
		}
	}
}

func TestRepositoryDocumentsIsolateShadowWritesFromFinalProductionState(t *testing.T) {
	root := repositoryRoot(t)
	plan := readPolicyDocument(t, root, "docs/superpowers/plans/2026-07-13-watermark-go-single-node-refactor.md")
	for _, required := range []string{
		"Compose profiles", "shadow DB identity != final DB identity", "独立 DB/schema", "独立 Redis namespace",
		"shadow outbox", "不得进入 production outbox", "不采用清理 shadow 写入", "final production DB",
		"只接收 initial+delta", "scrub 后的生产数据", "无本轮 shadow/A-B acceptance",
		"legacy snapshot 中原有合法历史", "source checksum 保留",
		"verify-acceptance.py 强制证明",
	} {
		if !containsPolicyText(plan, required) {
			t.Errorf("shadow/final state isolation contract omits %q", required)
		}
	}
	design := readPolicyDocument(t, root, "docs/superpowers/specs/2026-07-13-watermark-go-single-node-refactor-design.md")
	trace := readPolicyDocument(t, root, "docs/requirements-traceability.md")
	for _, body := range []string{design, trace} {
		for _, required := range []string{
			"shadow DB identity != final DB identity", "独立 Redis namespace", "无本轮 shadow/A-B acceptance",
			"source checksum 保留",
		} {
			if !containsPolicyText(body, required) {
				t.Errorf("design/trace shadow isolation contract omits %q", required)
			}
		}
	}
}

func TestRepositoryDocumentsHostDiscoveryRecoveryDigestTunnelAndMariaDB(t *testing.T) {
	root := repositoryRoot(t)
	plan := readPolicyDocument(t, root, "docs/superpowers/plans/2026-07-13-watermark-go-single-node-refactor.md")
	for _, required := range []string{
		"oldServicePresent=false", "5001/15001 均空闲", "watermark.bxsn.cn 当前 502", "MariaDB 11.8.6",
		"@@log_bin=0", "binlog_format=MIXED", "gtid_strict_mode=0", "chosenMigrationMode=final_full_no_binlog",
		"table-scoped fence", "禁止全实例 read lock/read_only", "重复 hash 证明无 writer",
		"oldServiceIdentity", "routeIdentity", "dbWriterIdentity", "host-before 精确 identity/hash allowlist",
		"不存在则 fail closed", "recoveryDigest != finalDigest", "A 成为 recovery 后才创建 B",
		"B shadow 隔离全验", "schemaCompatibleWithRecovery=true", "schemaCompatibleWithFinal=true",
		"同一兼容 final DB", "legacy reverse 分支", "oldServicePresent=true", "运行 tunnel/dashboard",
		"安全 API 查询", "真实 HTTPS 探测", "/etc/cloudflared/config.yml 不是权威", "不编辑/重建 token tunnel",
		"MariaDB GTID/binlog", "静态无 writer 证明", "MariaDB recovery image", "官方 digest",
		"deploy/image-lock.json", "typed importer", "MySQL 8.4", "禁止裸 docker run",
	} {
		if !containsPolicyText(plan, required) {
			t.Errorf("host discovery/recovery/tunnel/MariaDB contract omits %q", required)
		}
	}
	design := readPolicyDocument(t, root, "docs/superpowers/specs/2026-07-13-watermark-go-single-node-refactor-design.md")
	trace := readPolicyDocument(t, root, "docs/requirements-traceability.md")
	constraints := readPolicyDocument(t, root, "约束文件.md")
	for _, body := range []string{design, trace, constraints} {
		for _, required := range []string{
			"oldServicePresent", "recovery digest", "final digest", "MariaDB 11.8.6",
			"chosenMigrationMode=final_full_no_binlog", "运行 tunnel/dashboard",
			"host-before 精确 identity/hash allowlist",
		} {
			if !containsPolicyText(body, required) {
				t.Errorf("design/trace/constraints environment contract omits %q", required)
			}
		}
	}
}

func TestRepositoryDocumentsDefineAbsentTwoStageRecoveryMode(t *testing.T) {
	root := repositoryRoot(t)
	plan := readPolicyDocument(t, root, "docs/superpowers/plans/2026-07-13-watermark-go-single-node-refactor.md")
	for _, required := range []string{
		"rollbackMode=absent_two_stage", "baselineHTTPS=502", "恢复原 502 路由", "不得声称 5 分钟健康回滚",
		"A shadow 隔离全验", "A 真实域名", "真微信", "A observation>=1800s", "才成为 recoveryDigest",
		"A→B source diff allowlist", "release/promotion-marker.txt", "禁止 Go/依赖/Dockerfile/执行/config/migration/schema",
		"其他 tracked 文件变化",
		"rootfs/app binary/tool versions/schema", "仅 OCI label 白名单差异", "B shadow 隔离全验",
		"state-before.json 锁定 A/B digest+attestation+config/DB identity", "B→A 真实 drill",
		"durationSeconds<=300", "rollbackMode", "不要求或伪造 legacy reverse",
	} {
		if !containsPolicyText(plan, required) {
			t.Errorf("absent two-stage recovery contract omits %q", required)
		}
	}
	design := readPolicyDocument(t, root, "docs/superpowers/specs/2026-07-13-watermark-go-single-node-refactor-design.md")
	trace := readPolicyDocument(t, root, "docs/requirements-traceability.md")
	for _, body := range []string{design, trace} {
		for _, required := range []string{
			"rollbackMode=absent_two_stage", "A observation>=1800s", "A→B source diff allowlist", "B→A 真实 drill",
		} {
			if !containsPolicyText(body, required) {
				t.Errorf("design/trace absent recovery contract omits %q", required)
			}
		}
	}
}

func TestRepositoryDesignTraceAndConstraintsMirrorActualDeploymentBranch(t *testing.T) {
	root := repositoryRoot(t)
	documents := map[string]string{
		"design":      readPolicyDocument(t, root, "docs/superpowers/specs/2026-07-13-watermark-go-single-node-refactor-design.md"),
		"trace":       readPolicyDocument(t, root, "docs/requirements-traceability.md"),
		"constraints": readPolicyDocument(t, root, "约束文件.md"),
	}
	for name, body := range documents {
		for _, required := range []string{
			"oldServicePresent=false", "MariaDB 11.8.6", "chosenMigrationMode=final_full_no_binlog",
			"rollbackMode=absent_two_stage", "shadow DB identity != final DB identity", "独立 Redis namespace",
			"无本轮 shadow/A-B acceptance", "source checksum 保留", "recovery digest", "final digest", "A observation>=1800s",
			"A→B source diff allowlist", "B→A 真实 drill", "host-before 精确 identity/hash allowlist",
			"运行 tunnel/dashboard", "/etc/cloudflared/config.yml 不是权威",
			"统一 post-cutover failure trap", "步骤 3、4、5、6 任一失败",
			"https://watermark.bxsn.cn/api/health",
		} {
			if !containsPolicyText(body, required) {
				t.Errorf("%s does not mirror actual deployment branch: missing %q", name, required)
			}
		}
		for _, forbidden := range []string{
			"部署采用 initial full + final delta",
			"再创建旧 MySQL 一致性备份",
			"最终实际启用旧服务短写栅栏",
			"previous digest 分支与首次部署分支的回滚演练都能",
		} {
			if containsPolicyText(body, forbidden) {
				t.Errorf("%s retains superseded deployment default %q", name, forbidden)
			}
		}
	}
}

func TestRepositoryDesignKeepsDeploymentArchitectureHostManaged(t *testing.T) {
	root := repositoryRoot(t)
	body := readPolicyDocument(t, root, "docs/superpowers/specs/2026-07-13-watermark-go-single-node-refactor-design.md")
	for _, required := range []string{
		"deploy/compose.yml", "deploy/env.example", "宿主机管理", "仓库不保存 Nginx/Caddy 配置",
		"artifacts/deploy/state-before.json", "artifacts/deploy/running-digest.txt",
		"artifacts/deploy/rollback-drill.txt", "artifacts/deploy/observation-30m.json",
		"artifacts/acceptance/frontend-domain-e2e.json",
	} {
		if !containsPolicyText(body, required) {
			t.Errorf("design document omits architecture boundary or evidence %q", required)
		}
	}
	if containsPolicyText(body, "deploy/                       单机 Compose、Caddy/Nginx") {
		t.Fatal("design document still assigns proxy configuration to deploy/")
	}
	deployment := policyDocumentSection(t, body, "## 10. 单机部署与资源保护", "## 11. 验收标准")
	assertOrderedPolicyMarkers(t, deployment, []string{
		"保存宿主机与现网快照", "用固定官方 digest 的 MariaDB recovery image",
		"才以 `127.0.0.1:15001` 启动 A shadow", "可选 LAN", "A observation>=1800s",
		"B→A 真实 drill", "state-before.json 锁定", "Task 18 才 fence/drain A",
	})
	for _, required := range []string{
		"previous digest 分支", "首次部署分支", "durationSeconds<=300", "healthPassed=true",
		"dataPassed=true", "同一兼容 final DB",
	} {
		if !containsPolicyText(body, required) {
			t.Errorf("design rollback contract omits %q", required)
		}
	}
	assertUnambiguousRollbackDatabaseTarget(t, body)
}

func assertUnambiguousRollbackDatabaseTarget(t *testing.T, body string) {
	t.Helper()
	for _, required := range []string{"唯一指定", "旧服务 DSN", "同一克隆", "验证连接身份"} {
		if !containsPolicyText(body, required) {
			t.Errorf("rollback database contract omits %q", required)
		}
	}
	for _, forbidden := range []string{"（或安全更新原旧库）", "（或安全重放原旧库）"} {
		if containsPolicyText(body, forbidden) {
			t.Errorf("rollback database contract remains ambiguous at %q", forbidden)
		}
	}
}

func containsPolicyText(body, required string) bool {
	normalize := func(value string) string {
		return strings.Join(strings.Fields(strings.ReplaceAll(value, "`", "")), " ")
	}
	return strings.Contains(normalize(body), normalize(required))
}

func policyDocumentSection(t *testing.T, body, start, end string) string {
	t.Helper()
	startIndex := strings.Index(body, start)
	if startIndex < 0 {
		t.Fatalf("document section start is missing: %q", start)
	}
	section := body[startIndex:]
	if end == "" {
		return section
	}
	endIndex := strings.Index(section[len(start):], end)
	if endIndex < 0 {
		t.Fatalf("document section end is missing after %q: %q", start, end)
	}
	return section[:len(start)+endIndex]
}

func assertOrderedPolicyMarkers(t *testing.T, body string, markers []string) {
	t.Helper()
	body = strings.Join(strings.Fields(strings.ReplaceAll(body, "`", "")), " ")
	previous := -1
	for _, marker := range markers {
		marker = strings.Join(strings.Fields(strings.ReplaceAll(marker, "`", "")), " ")
		relative := strings.Index(body[previous+1:], marker)
		if relative < 0 {
			t.Errorf("document marker is missing or out of order: %q", marker)
			continue
		}
		index := previous + 1 + relative
		previous = index
	}
}

func readPolicyDocument(t *testing.T, root, path string) string {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
	if err != nil {
		t.Fatalf("read policy document %s: %v", path, err)
	}
	return string(contents)
}

func assertCanonicalPlanOrTracePaths(t *testing.T, body string) {
	t.Helper()
	for _, required := range []string{
		"deploy/compose.yml", "deploy/env.example", "deploy/image-lock.json", ".github/workflows/ci-image.yml",
		"docs/frontend-provenance.json", "docs/baseline-provenance.json",
		"scripts/deploy-local.sh", "scripts/rollback-local.sh", "scripts/preflight.sh",
		"scripts/observe.sh", "scripts/host-snapshot.sh", "scripts/verify-gitleaks.sh",
		"scripts/verify-frontend-provenance.sh", "scripts/verify-acceptance.py", "tests/ops/test_scripts.py",
		"artifacts/deploy/state-before.json", "artifacts/deploy/running-digest.txt",
		"artifacts/deploy/rollback-drill.txt", "artifacts/deploy/observation-30m.json",
		"artifacts/deploy/pull-and-up.txt", "artifacts/deploy/before-after-containers.json",
		"artifacts/acceptance/frontend-domain-e2e.json", "artifacts/acceptance/admin-and-baseline.json",
		"artifacts/acceptance/redis-degraded.json", "artifacts/verification/secret-scan.txt",
		"artifacts/release/repository-and-image.txt",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("document omits canonical path %q", required)
		}
	}
}

func assertForbiddenPlanOrTracePaths(t *testing.T, body string) {
	t.Helper()
	for _, forbidden := range []string{
		"reports/local-verification.md",
		"reports/deployment/",
		"deploy/.env.example",
		"internal/benchmark/",
		"testdata/benchmark/",
		"`scripts/deploy.sh`",
		"`scripts/rollback.sh`",
		"git branch --unset-upstream",
		"`upstream` 仍只读",
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("document retains conflicting path or remote expectation %q", forbidden)
		}
	}
}

func composeServicesWithBuild(contents []byte) ([]string, error) {
	document, err := parseComposeYAML(contents)
	if err != nil {
		return nil, err
	}
	services := make([]string, 0)
	for name, service := range document.services {
		if _, ok := service["build"]; ok {
			services = append(services, name)
		}
	}
	sort.Strings(services)
	return services, nil
}

type parsedComposeYAML struct {
	topLevel map[string]any
	services map[string]map[string]any
}

func parseComposeYAML(contents []byte) (parsedComposeYAML, error) {
	var topLevel map[string]any
	decoder := yaml.NewDecoder(bytes.NewReader(contents))
	if err := decoder.Decode(&topLevel); err != nil {
		return parsedComposeYAML{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return parsedComposeYAML{}, fmt.Errorf("multiple YAML documents are not allowed")
	} else if !errors.Is(err, io.EOF) {
		return parsedComposeYAML{}, err
	}
	document := parsedComposeYAML{topLevel: topLevel, services: make(map[string]map[string]any)}
	servicesValue, exists := topLevel["services"]
	if !exists {
		return document, nil
	}
	services, ok := servicesValue.(map[string]any)
	if !ok {
		return parsedComposeYAML{}, fmt.Errorf("top-level services must be a mapping")
	}
	for name, rawService := range services {
		service, ok := rawService.(map[string]any)
		if !ok {
			return parsedComposeYAML{}, fmt.Errorf("Compose service must be a mapping")
		}
		document.services[name] = service
	}
	return document, nil
}

func composePolicyViolations(contents []byte, path string) ([]string, error) {
	document, err := parseComposeYAML(contents)
	if err != nil {
		return nil, err
	}
	_, hasServices := document.topLevel["services"]
	_, hasInclude := document.topLevel["include"]
	if !hasServices && !hasInclude {
		return nil, nil
	}
	seen := make(map[string]struct{})
	var violations []string
	add := func(kind string) {
		if _, ok := seen[kind]; ok {
			return
		}
		seen[kind] = struct{}{}
		violations = append(violations, kind)
	}
	if filepath.ToSlash(filepath.Clean(path)) != "deploy/compose.yml" {
		add("noncanonical-compose-path")
	}
	if hasInclude {
		add("top-level-include")
	}
	for _, service := range document.services {
		if _, ok := service["build"]; ok {
			add("service-build")
		}
		if _, ok := service["extends"]; ok {
			add("service-extends")
		}
	}
	sort.Strings(violations)
	return violations, nil
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	output, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("locate repository root: %v", err)
	}
	return strings.TrimSpace(string(output))
}
