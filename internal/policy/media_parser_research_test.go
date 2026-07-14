package policy_test

import (
	"encoding/json"
	"os"
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
		},
		"docs/superpowers/specs/2026-07-13-watermark-go-single-node-refactor-design.md": {
			"033424b08ac6468c8c37b6fb0c98a0446bb09d9e",
			"metadata-driven `Descriptor`",
			"baselineAuthority=false",
		},
		"docs/requirements-traceability.md": {
			"## 外部解析研究融合",
			"不自动成为 production 支持项",
			"不盲信数组下标",
		},
		"约束文件.md": {
			"metadata-driven descriptor registry",
			"codeCopied=false",
			"首次请求后才校验 domain",
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
