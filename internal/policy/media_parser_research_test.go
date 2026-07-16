package policy_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMediaParserResearchPinsSourceLicenseAndAdoptionBoundary(t *testing.T) {
	root := repositoryRoot(t)
	body, err := os.ReadFile(filepath.Join(root, "docs", "research", "media-parser-provenance.json"))
	if err != nil {
		t.Fatalf("read media-parser provenance: %v", err)
	}

	var provenance struct {
		SchemaVersion    int      `json:"schemaVersion"`
		SourceRepository string   `json:"sourceRepository"`
		DefaultBranch    string   `json:"defaultBranch"`
		Commit           string   `json:"commit"`
		Tree             string   `json:"tree"`
		ReviewedPaths    []string `json:"reviewedPaths"`
		License          struct {
			SPDX   string `json:"spdx"`
			File   string `json:"file"`
			SHA256 string `json:"sha256"`
		} `json:"license"`
		Adoption struct {
			Mode                            string `json:"mode"`
			CodeCopied                      bool   `json:"codeCopied"`
			BaselineAuthority               bool   `json:"baselineAuthority"`
			AttributionRequiredIfCodeCopied bool   `json:"attributionRequiredIfCodeCopied"`
		} `json:"adoption"`
		SecurityDisposition string `json:"securityDisposition"`
	}
	if err := json.Unmarshal(body, &provenance); err != nil {
		t.Fatalf("decode media-parser provenance: %v", err)
	}

	wantPaths := []string{
		"README.md",
		"LICENSE",
		"app.py",
		"Dockerfile",
		"docker-compose.yml",
		"requirements.txt",
		"configs/business_config.json",
		"configs/general_constants.py",
		"configs/logging_config.py",
		"src/__init__.py",
		"src/parser_factory.py",
		"src/api/parse.py",
		"src/parsers/base_parser.py",
		"src/parsers/bilibili_parser.py",
		"src/parsers/douyin_parser.py",
		"src/parsers/kuaishou_parser.py",
		"src/parsers/xiaohongshu_parser.py",
		"utils/web_fetcher.py",
	}
	if provenance.SchemaVersion != 1 ||
		provenance.SourceRepository != "https://github.com/ucmao/media-parser" ||
		provenance.DefaultBranch != "starter" ||
		provenance.Commit != "033424b08ac6468c8c37b6fb0c98a0446bb09d9e" ||
		provenance.Tree != "56e556db619a296340fa8b00f3c726676cf32bcf" ||
		strings.Join(provenance.ReviewedPaths, "\n") != strings.Join(wantPaths, "\n") ||
		provenance.License.SPDX != "MIT" ||
		provenance.License.File != "LICENSE" ||
		provenance.License.SHA256 != "122b79645845bb6445da24e9c6eedd3597dce6259070e301b2f06bd49aa3c280" ||
		provenance.Adoption.Mode != "concepts-and-test-design-only" ||
		provenance.Adoption.CodeCopied ||
		provenance.Adoption.BaselineAuthority ||
		!provenance.Adoption.AttributionRequiredIfCodeCopied ||
		provenance.SecurityDisposition != "do-not-use-as-runtime-or-network-security-baseline" {
		t.Fatal("media-parser provenance does not match the approved research boundary")
	}
}

func TestMediaParserResearchMapsStrengthsAndExplicitlyRejectsUnsafePatterns(t *testing.T) {
	root := repositoryRoot(t)
	body, err := os.ReadFile(filepath.Join(root, "docs", "research", "media-parser-review.md"))
	if err != nil {
		t.Fatalf("read media-parser review: %v", err)
	}
	text := string(body)
	for _, required := range []string{
		"metadata-driven Descriptor registry",
		"50 个 domain alias",
		"ImageAsset",
		"LivePhotoURL",
		"MediaCandidate",
		"首次网络请求前完成 SSRF 校验",
		"不得关闭 TLS 校验",
		"不得硬编码会话或反爬材料",
		"不得作为 93 样本基线权威",
		"上游代码复制：无",
		"输入目录与出口 authority 分离",
		"TestEveryNativeFixedEndpointHasPolicyOwner",
		"Actions-only immutable image",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("media-parser review is missing required decision: %q", required)
		}
	}
}

func TestMediaParserResearchIsIntegratedAcrossGoverningDocuments(t *testing.T) {
	root := repositoryRoot(t)
	documents := map[string][]string{
		"docs/superpowers/plans/2026-07-13-watermark-go-single-node-refactor.md": {
			"metadata-driven `Descriptor`",
			"ImageAsset{URL, LivePhotoURL}",
			"首次网络请求之前",
			"50 个 domain alias 只作候选目录",
			"SessionMaterialProvider",
			"mediaParserIntegration",
			"TestPurposeScopedOutboundAuthority",
			"TestParserAPIAuthorityCannotBeUsedAsInputRoute",
		},
		"docs/superpowers/specs/2026-07-13-watermark-go-single-node-refactor-design.md": {
			"033424b08ac6468c8c37b6fb0c98a0446bb09d9e",
			"metadata-driven `Descriptor`",
			"baselineAuthority=false",
			"SessionMaterialProvider",
			"mediaParserIntegration",
			"MetadataAPI",
		},
		"docs/requirements-traceability.md": {
			"## 外部解析研究融合",
			"不自动成为 production 支持项",
			"不盲信数组下标",
			"SessionMaterialProvider",
			"media-parser-integration.json",
			"TestCrossPurposeRedirectFailsClosed",
		},
		"约束文件.md": {
			"metadata-driven descriptor registry",
			"codeCopied=false",
			"首次请求后才校验 domain",
			"session_expired",
			"mediaParserIntegration",
		},
	}
	for path, required := range documents {
		body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			t.Fatalf("read governing document %s: %v", path, err)
		}
		for _, phrase := range required {
			if !strings.Contains(string(body), phrase) {
				t.Fatalf("governing document %s is missing media-parser integration: %q", path, phrase)
			}
		}
	}
}

func TestMediaParserResearchBenefitsHaveExecutableOwnersAndEvidence(t *testing.T) {
	root := repositoryRoot(t)
	plan := readPolicyDocument(t, root, "docs/superpowers/plans/2026-07-13-watermark-go-single-node-refactor.md")
	tasks := map[string]string{
		"3":  policyDocumentSection(t, plan, "## 任务 3：", "## 任务 4："),
		"4":  policyDocumentSection(t, plan, "## 任务 4：", "## 任务 5："),
		"5":  policyDocumentSection(t, plan, "## 任务 5：", "## 任务 6："),
		"6":  policyDocumentSection(t, plan, "## 任务 6：", "## 任务 7："),
		"7":  policyDocumentSection(t, plan, "## 任务 7：", "## 任务 8："),
		"9":  policyDocumentSection(t, plan, "## 任务 9：", "## 任务 10："),
		"10": policyDocumentSection(t, plan, "## 任务 10：", "## 任务 11："),
		"12": policyDocumentSection(t, plan, "## 任务 12：", "## 任务 13："),
		"14": policyDocumentSection(t, plan, "## 任务 14：", "## 任务 15："),
		"15": policyDocumentSection(t, plan, "## 任务 15：", "## 任务 16："),
		"18": policyDocumentSection(t, plan, "## 任务 18：", ""),
	}

	want := map[string][]string{
		"3": {
			"internal/parser/result_test.go",
			"internal/parser/native/structured_json_test.go",
			"internal/parser/session_material.go",
			"TestDescriptorCapabilitiesMatchResult",
			"TestRegistryRejectsUnknownHostWithTypedError",
			"TestStructuredJSONGoldenMatrix",
			"TestMediaCandidateOrderIsStable",
			"TestCandidateFallbackHonorsTotalBudget",
			"TestScopedSessionMaterialInvalidatesOnlyOnTypedExpiry",
			"vid", "id", "xsec_token", "modal_id", "v", "s", "pid",
		},
		"4": {
			"internal/netguard/authority.go",
			"TestPurposeScopedOutboundAuthority",
			"TestEveryNativeFixedEndpointHasPolicyOwner",
			"TestParserAPIAuthorityCannotBeUsedAsInputRoute",
			"TestSensitiveHeadersNeverReachDynamicMediaCandidateHost",
			"TestCrossPurposeRedirectFailsClosed",
		},
		"5": {
			"TestCacheKeyBindsPlatformResourceParserAndSchemaVersion",
			"TestCacheVersionChangeMisses",
			"TestCacheSingleflightCoalescesSameKey",
			"TestNegativeCachePolicyRejectsNonCacheableErrors",
			"TestRedisAndMemoryShareCacheSemantics",
		},
		"6": {
			"TestClientSessionSecretsNeverReachParserDependencies",
			"parser upstream session material",
		},
		"7": {
			"TestCanonicalURLQueryPolicyMatrix",
			"TestCanonicalURL",
		},
		"9": {
			"TestDASHCandidateOrderAndFallbackBudget",
			"统一总预算", "0700", "0600", "symlink", "未配对的 DASH",
		},
		"10": {
			"tests/research/media-parser/manifest.json",
			"productionEnabled=false",
			"coverage clue not adopted",
			"m.weibo.cn", "m.oasis.weibo.cn",
		},
		"12": {
			"TestMediaParserIntegrationContract",
			"mediaParserIntegration",
		},
		"14": {
			"mediaParserIntegration", "sourceCommit", "imageDigest", "ciRunId",
		},
		"15": {
			"media-parser focused suite",
			"mediaParserIntegration",
			"TestStructuredJSONGoldenMatrix",
		},
		"18": {
			"mediaParserIntegration", "evidenceMode", "live", "hermetic",
			"audio", "livePhoto", "sourceCommit", "imageDigest", "ciRunId",
		},
	}
	for task, required := range want {
		for _, phrase := range required {
			if !containsPolicyText(tasks[task], phrase) {
				t.Errorf("Task %s media-parser integration omits %q", task, phrase)
			}
		}
	}
}

func TestMediaParserResearchNeverEntersRuntimeOrBuildGraph(t *testing.T) {
	root := repositoryRoot(t)
	output, err := exec.Command(
		"git", "-C", root, "ls-files", "-z", "--cached", "--others", "--exclude-standard",
	).Output()
	if err != nil {
		t.Fatalf("list tracked files: %v", err)
	}

	for _, path := range strings.Split(string(output), "\x00") {
		path = filepath.ToSlash(path)
		if path == "" || !isMediaParserRuntimeOrBuildPath(path) {
			continue
		}
		lowerPath := strings.ToLower(path)
		if (strings.HasPrefix(lowerPath, "vendor/") || strings.HasPrefix(lowerPath, "third_party/")) &&
			strings.Contains(lowerPath, "media-parser") {
			t.Errorf("tracked runtime/build graph vendors media-parser at %s", path)
			continue
		}
		bodies := mediaParserPolicyBodies(t, root, path)
		for source, body := range bodies {
			lowerBody := strings.ToLower(string(body))
			for _, forbidden := range []string{
				"github.com/ucmao/media-parser",
				"ucmao/media-parser",
				"media-parser.git",
			} {
				if strings.Contains(lowerBody, forbidden) {
					t.Errorf("runtime/build graph %s copy of %s depends on, clones, or syncs media-parser via %q", source, path, forbidden)
				}
			}
		}
	}
}

func TestMediaParserRuntimeGraphScannerReadsIndexAndWorktree(t *testing.T) {
	root := t.TempDir()
	if output, err := exec.Command("git", "-C", root, "init", "--quiet").CombinedOutput(); err != nil {
		t.Fatalf("initialize fixture repository: %v: %s", err, output)
	}
	path := "Dockerfile"
	absolutePath := filepath.Join(root, path)
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(absolutePath, []byte(body), 0o600); err != nil {
			t.Fatalf("write fixture Dockerfile: %v", err)
		}
	}
	stage := func() {
		t.Helper()
		if output, err := exec.Command("git", "-C", root, "add", "--", path).CombinedOutput(); err != nil {
			t.Fatalf("stage fixture Dockerfile: %v: %s", err, output)
		}
	}

	unsafe := "RUN git clone https://github.com/ucmao/" + "media-parser.git /runtime-dependency\n"
	write("FROM scratch\n")
	stage()
	write(unsafe)
	bodies := mediaParserPolicyBodies(t, root, path)
	if !strings.Contains(string(bodies["worktree"]), "media-parser.git") || strings.Contains(string(bodies["index"]), "media-parser.git") {
		t.Fatal("scanner did not keep distinct safe index and unsafe worktree copies")
	}

	stage()
	write("FROM scratch\n")
	bodies = mediaParserPolicyBodies(t, root, path)
	if !strings.Contains(string(bodies["index"]), "media-parser.git") || strings.Contains(string(bodies["worktree"]), "media-parser.git") {
		t.Fatal("scanner did not keep distinct unsafe index and safe worktree copies")
	}
}

func mediaParserPolicyBodies(t *testing.T, root, path string) map[string][]byte {
	t.Helper()
	bodies := make(map[string][]byte, 2)

	tracked := exec.Command("git", "-C", root, "ls-files", "--error-unmatch", "--", path)
	if err := tracked.Run(); err == nil {
		body, err := exec.Command("git", "-C", root, "show", ":"+path).Output()
		if err != nil {
			t.Fatalf("read index runtime/build graph file %s: %v", path, err)
		}
		bodies["index"] = body
	}

	absolutePath := filepath.Join(root, filepath.FromSlash(path))
	info, err := os.Lstat(absolutePath)
	if os.IsNotExist(err) {
		return bodies
	}
	if err != nil {
		t.Fatalf("inspect worktree runtime/build graph file %s: %v", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(absolutePath)
		if err != nil {
			t.Fatalf("read worktree runtime/build graph symlink %s: %v", path, err)
		}
		bodies["worktree"] = []byte(target)
		return bodies
	}
	body, err := os.ReadFile(absolutePath)
	if err != nil {
		t.Fatalf("read worktree runtime/build graph file %s: %v", path, err)
	}
	bodies["worktree"] = body
	return bodies
}

func isMediaParserRuntimeOrBuildPath(path string) bool {
	if path == "go.mod" || path == "go.sum" || path == "Dockerfile" || path == "Makefile" ||
		path == ".gitmodules" || strings.HasPrefix(path, "requirements") {
		return true
	}
	for _, prefix := range []string{
		".github/workflows/", "cmd/", "internal/", "bridges/", "deploy/", "scripts/", "vendor/", "third_party/",
	} {
		if strings.HasPrefix(path, prefix) {
			return !strings.HasPrefix(path, "internal/policy/")
		}
	}
	return false
}

func TestParserMigrationHasAnAtomicNetguardDependency(t *testing.T) {
	root := repositoryRoot(t)
	plan := readPolicyDocument(t, root, "docs/superpowers/plans/2026-07-13-watermark-go-single-node-refactor.md")
	task2 := policyDocumentSection(t, plan, "## 任务 2：", "## 任务 3：")
	task3 := policyDocumentSection(t, plan, "## 任务 3：", "## 任务 4：")
	task4 := policyDocumentSection(t, plan, "## 任务 4：", "## 任务 5：")

	for _, required := range []string{"环境配置只来自 `ParserConfig`", "`Dependencies`", "Config"} {
		if !strings.Contains(task2, required) {
			t.Errorf("Task 2 parser dependency boundary omits %q", required)
		}
	}
	for _, required := range []string{
		"先实现 `internal/netguard` 的安全 core",
		"FetchURL", "SafeURL", "受控 `Fetcher`", "不能提交任何临时裸 HTTP",
		"NewRegistry(native.Descriptors())", "_, err := NewRegistry(descriptors)",
		"production import 精确字面量", ":!internal/policy/**", "legacy history scanner",
	} {
		if !strings.Contains(task3, required) {
			t.Errorf("Task 3 atomic netguard dependency omits %q", required)
		}
	}
	for _, required := range []string{"任务 3 已提供 netguard core", "全树出口", "subprocess"} {
		if !strings.Contains(task4, required) {
			t.Errorf("Task 4 netguard continuation omits %q", required)
		}
	}
}

func TestSubprocessFallbackRequiresNetworkIsolatedHelper(t *testing.T) {
	root := repositoryRoot(t)
	plan := readPolicyDocument(t, root, "docs/superpowers/plans/2026-07-13-watermark-go-single-node-refactor.md")
	task4 := policyDocumentSection(t, plan, "## 任务 4：", "## 任务 5：")
	task13 := policyDocumentSection(t, plan, "## 任务 13：", "## 任务 14：")
	constraints := readPolicyDocument(t, root, "约束文件.md")

	for _, required := range []string{
		"network-isolated `parser-helper`", "shared UDS", "raw socket", "production fail closed",
		"server/main.go", "server/ytdlp.go", "parser/universal/bridge.go", "server/universal_parser.go",
		"server/m3u8_task.go", "server/tool_updates.go", "server/infrastructure.go",
		"generated production", "全树出口", "精确符号 allowlist", "requestOverride",
		"bridges/universal/python/bridge.py", "go test ./... -count=1",
	} {
		if !strings.Contains(task4, required) {
			t.Errorf("Task 4 subprocess isolation/callsite contract omits %q", required)
		}
	}
	for _, required := range []string{
		"parser-helper", "egress-proxy", "internal: true", "RECOVERY_IMAGE", "CANDIDATE_IMAGE",
		"专属 UDS volume",
		"无公开端口", "不得连接 MySQL/Redis", "runtime inspect",
	} {
		if !strings.Contains(task13, required) {
			t.Errorf("Task 13 parser sandbox topology omits %q", required)
		}
	}
	for _, required := range []string{
		"parser-helper", "独立业务服务", "recovery 与 candidate", "专属 UDS", "raw socket",
		"该组同一不可变应用镜像 digest",
	} {
		if !strings.Contains(constraints, required) {
			t.Errorf("constraints parser sandbox boundary omits %q", required)
		}
	}
}
