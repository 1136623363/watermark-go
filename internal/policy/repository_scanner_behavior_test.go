package policy_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepositoryAuditScansStagedBlobWhenWorktreeWasOverwritten(t *testing.T) {
	repo := newAuditRepository(t)
	path := filepath.Join(repo, "configuration.txt")
	writeAuditFixture(t, path, "safe=true\n")
	gitTestOutput(t, repo, "add", "configuration.txt")
	gitTestOutput(t, repo, "commit", "--quiet", "-m", "safe baseline")

	literal := "prod-" + "credential-material"
	writeAuditFixture(t, path, `adminPassword="`+literal+`"`+"\n")
	gitTestOutput(t, repo, "add", "configuration.txt")
	writeAuditFixture(t, path, "safe=true\n")

	audit, err := auditGitRepository(repo)
	if err != nil {
		t.Fatalf("audit repository: %v", err)
	}
	if !auditHasKind(audit, "sensitive-default") {
		t.Fatal("staged sensitive default was not detected")
	}
	report := formatAuditViolations(audit.Violations)
	if strings.Contains(report, literal) || strings.Contains(report, "configuration.txt") {
		t.Fatal("sanitized audit report exposed a literal or path")
	}
}

func TestRepositoryAuditDeduplicatesSameIndexAndWorktreeLocation(t *testing.T) {
	repo := newAuditRepository(t)
	path := filepath.Join(repo, "configuration.txt")
	writeAuditFixture(t, path, "admin"+"Password="+"prod-"+"credential-material\n")
	gitTestOutput(t, repo, "add", "configuration.txt")

	audit, err := auditGitRepository(repo)
	if err != nil {
		t.Fatalf("audit repository: %v", err)
	}
	count := 0
	for _, violation := range audit.Violations {
		if violation.Kind == "sensitive-default" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("sensitive violations at one location = %d, want 1", count)
	}
}

func TestRepositoryAuditScansOlderReachableHistory(t *testing.T) {
	repo := newAuditRepository(t)
	path := filepath.Join(repo, "settings.txt")
	writeAuditFixture(t, path, "clientSecret="+"prod-"+"credential-material\n")
	gitTestOutput(t, repo, "add", "settings.txt")
	gitTestOutput(t, repo, "commit", "--quiet", "-m", "first")
	writeAuditFixture(t, path, "safe=true\n")
	gitTestOutput(t, repo, "add", "settings.txt")
	gitTestOutput(t, repo, "commit", "--quiet", "-m", "second")

	audit, err := auditGitRepository(repo)
	if err != nil {
		t.Fatalf("audit repository: %v", err)
	}
	if !auditHasKind(audit, "sensitive-default") {
		t.Fatal("sensitive default in older reachable history was not detected")
	}
}

func TestRepositoryAuditCoversMessagesRefsAndCredentialShapedNames(t *testing.T) {
	repo := newAuditRepository(t)
	writeAuditFixture(t, filepath.Join(repo, "safe.txt"), "first\n")
	gitTestOutput(t, repo, "add", "safe.txt")
	gitTestOutput(t, repo, "commit", "--quiet", "-m", "safe baseline")

	writeAuditFixture(t, filepath.Join(repo, "safe.txt"), "second\n")
	gitTestOutput(t, repo, "add", "safe.txt")
	messagePath := filepath.Join(repo, "message.fixture")
	writeAuditFixture(t, messagePath, "adminPassword="+"prod-"+"credential-material\n")
	gitTestOutput(t, repo, "commit", "--quiet", "-F", messagePath)

	tagMessagePath := filepath.Join(repo, "tag-message.fixture")
	writeAuditFixture(t, tagMessagePath, "clientSecret="+"prod-"+"credential-material\n")
	gitTestOutput(t, repo, "tag", "-a", "audit-tag", "-F", tagMessagePath)
	if err := os.Remove(messagePath); err != nil {
		t.Fatalf("remove commit message fixture: %v", err)
	}
	if err := os.Remove(tagMessagePath); err != nil {
		t.Fatalf("remove tag message fixture: %v", err)
	}

	credentialShape := "ghp" + "_" + strings.Repeat("A", 24)
	directPath := filepath.Join(repo, "direct.fixture")
	writeAuditFixture(t, directPath, credentialShape+"\n")
	blobID := strings.TrimSpace(gitTestOutput(t, repo, "hash-object", "-w", directPath))
	if err := os.Remove(directPath); err != nil {
		t.Fatalf("remove direct blob fixture: %v", err)
	}
	gitTestOutput(t, repo, "update-ref", "refs/archive/direct-blob", blobID)
	head := strings.TrimSpace(gitTestOutput(t, repo, "rev-parse", "HEAD"))
	credentialRef := "refs/heads/" + credentialShape
	gitTestOutput(t, repo, "update-ref", credentialRef, head)
	credentialPath := credentialShape + ".txt"
	writeAuditFixture(t, filepath.Join(repo, credentialPath), "safe=true\n")
	gitTestOutput(t, repo, "add", credentialPath)
	configuredName := "admin" + "Password=" + "prod-" + "credential-material"
	configuredRef := "refs/heads/" + configuredName
	gitTestOutput(t, repo, "update-ref", configuredRef, head)
	configuredPath := configuredName + ".txt"
	writeAuditFixture(t, filepath.Join(repo, configuredPath), "safe=true\n")
	gitTestOutput(t, repo, "add", configuredPath)

	audit, err := auditGitRepository(repo)
	if err != nil {
		t.Fatalf("audit repository: %v", err)
	}
	for _, kind := range []string{
		"commit-message-sensitive-default",
		"tag-message-sensitive-default",
		"blob-credential-shape",
		"ref-name-credential-shape",
		"path-name-credential-shape",
		"ref-name-sensitive-default",
		"path-name-sensitive-default",
	} {
		if !auditHasKind(audit, kind) {
			t.Errorf("audit omitted %s", kind)
		}
	}
	report := formatAuditViolations(audit.Violations)
	for _, raw := range []string{credentialShape, credentialRef, credentialPath, configuredName, configuredRef, configuredPath, "prod-" + "credential-material"} {
		if strings.Contains(report, raw) {
			t.Fatal("sanitized audit report exposed sensitive metadata")
		}
	}
}

func TestRepositoryAuditFailsClosedForUnscannableIndexBlobs(t *testing.T) {
	for _, fixture := range []struct {
		name string
		body []byte
	}{
		{name: "binary", body: []byte{0, 1, 2}},
		{name: "oversized", body: make([]byte, maxPolicyBlobBytes+1)},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			repo := newAuditRepository(t)
			writeAuditFixtureBytes(t, filepath.Join(repo, "fixture.bin"), fixture.body)
			gitTestOutput(t, repo, "add", "fixture.bin")
			if _, err := auditGitRepository(repo); err == nil {
				t.Fatal("audit accepted an unscannable index blob")
			}
		})
	}
}

func TestRepositoryAuditScansTrackedWorktreeSymlinkTargetTextWithoutFollowing(t *testing.T) {
	repo := newAuditRepository(t)
	linkPath := filepath.Join(repo, "configuration.txt")
	if err := os.Symlink("safe-target", linkPath); err != nil {
		t.Fatalf("create safe symlink: %v", err)
	}
	gitTestOutput(t, repo, "add", "configuration.txt")
	if err := os.Remove(linkPath); err != nil {
		t.Fatalf("remove safe symlink: %v", err)
	}
	if err := os.Symlink("admin"+"Password="+"prod-"+"credential-material", linkPath); err != nil {
		t.Fatalf("replace worktree symlink: %v", err)
	}

	audit, err := auditGitRepository(repo)
	if err != nil {
		t.Fatal("audit tracked worktree symlink")
	}
	if !auditHasKind(audit, "sensitive-default") {
		t.Fatal("tracked worktree symlink target text bypassed the audit")
	}
}

func TestRepositoryAuditNeverFollowsTrackedWorktreeSymlink(t *testing.T) {
	repo := newAuditRepository(t)
	writeAuditFixtureBytes(t, filepath.Join(repo, "untracked.bin"), []byte{0, 1, 2})
	if err := os.Symlink("untracked.bin", filepath.Join(repo, "safe-link")); err != nil {
		t.Fatalf("create symlink: %v", err)
	}
	gitTestOutput(t, repo, "add", "safe-link")
	if _, err := auditGitRepository(repo); err != nil {
		t.Fatal("audit followed a tracked symlink instead of scanning only its target text")
	}
}

func TestRepositoryAuditFailsClosedWhenRegularWorktreeFileBecomesSymlinkBeforeOpen(t *testing.T) {
	repo := newAuditRepository(t)
	trackedPath := filepath.Join(repo, "safe.txt")
	writeAuditFixture(t, trackedPath, "safe\n")
	gitTestOutput(t, repo, "add", "safe.txt")
	writeAuditFixtureBytes(t, filepath.Join(repo, "untracked.bin"), []byte{0, 1, 2})

	originalOpen := auditOpenRegularFile
	replaced := false
	auditOpenRegularFile = func(path string) (*os.File, error) {
		if !replaced && path == trackedPath {
			replaced = true
			if err := os.Remove(path); err != nil {
				return nil, err
			}
			if err := os.Symlink("untracked.bin", path); err != nil {
				return nil, err
			}
		}
		return originalOpen(path)
	}
	t.Cleanup(func() { auditOpenRegularFile = originalOpen })

	if _, err := auditGitRepository(repo); err == nil {
		t.Fatal("audit followed a symlink substituted after the worktree lstat")
	}
}

func TestRepositoryAuditReadsRepeatedBlobsOnlyOnce(t *testing.T) {
	repo := newAuditRepository(t)
	writeAuditFixture(t, filepath.Join(repo, "one.txt"), "same\n")
	writeAuditFixture(t, filepath.Join(repo, "two.txt"), "same\n")
	gitTestOutput(t, repo, "add", "one.txt", "two.txt")
	gitTestOutput(t, repo, "commit", "--quiet", "-m", "same blobs")

	audit, err := auditGitRepository(repo)
	if err != nil {
		t.Fatalf("audit repository: %v", err)
	}
	if audit.UniqueBlobsRead != 1 {
		t.Fatalf("unique blobs read = %d, want 1", audit.UniqueBlobsRead)
	}
}

func TestRepositoryAuditWalksRepeatedRootTreeOnlyOnce(t *testing.T) {
	repo := newAuditRepository(t)
	writeAuditFixture(t, filepath.Join(repo, "same.txt"), "same\n")
	gitTestOutput(t, repo, "add", "same.txt")
	gitTestOutput(t, repo, "commit", "--quiet", "-m", "first tree")
	gitTestOutput(t, repo, "commit", "--quiet", "--allow-empty", "-m", "same tree")

	audit, err := auditGitRepositoryWithLimits(repo, repositoryAuditLimits{
		maxTreeWalks: 1, maxTreeMetadataBytes: maxPolicyTreeMetadataBytes,
		maxTotalObjectBytes: maxPolicyTotalObjectBytes,
	})
	if err != nil {
		t.Fatalf("audit repeated root tree: %v", err)
	}
	if audit.UniqueTreesWalked != 1 {
		t.Fatalf("unique trees walked = %d, want 1", audit.UniqueTreesWalked)
	}
}

func TestRepositoryAuditFailsClosedAtCumulativeTreeMetadataBudget(t *testing.T) {
	repo := newAuditRepository(t)
	writeAuditFixture(t, filepath.Join(repo, "safe.txt"), "safe\n")
	gitTestOutput(t, repo, "add", "safe.txt")
	gitTestOutput(t, repo, "commit", "--quiet", "-m", "tree metadata")

	if _, err := auditGitRepositoryWithLimits(repo, repositoryAuditLimits{
		maxTreeWalks: maxPolicyTreeWalks, maxTreeMetadataBytes: 1,
		maxTotalObjectBytes: maxPolicyTotalObjectBytes,
	}); err == nil {
		t.Fatal("audit accepted tree metadata beyond the cumulative budget")
	}
}

func TestRepositoryAuditFailsClosedAtCumulativeWorktreeBudget(t *testing.T) {
	repo := newAuditRepository(t)
	writeAuditFixture(t, filepath.Join(repo, "one.txt"), "one\n")
	writeAuditFixture(t, filepath.Join(repo, "two.txt"), "two\n")
	gitTestOutput(t, repo, "add", "one.txt", "two.txt")

	if _, err := auditGitRepositoryWithLimits(repo, repositoryAuditLimits{
		maxTreeWalks: maxPolicyTreeWalks, maxTreeMetadataBytes: maxPolicyTreeMetadataBytes,
		maxTotalObjectBytes: 7,
	}); err == nil {
		t.Fatal("audit accepted tracked worktree bytes beyond the cumulative budget")
	}
}

func TestRepositoryAuditSharesCumulativeBudgetAcrossWorktreeAndGitObjects(t *testing.T) {
	repo := newAuditRepository(t)
	writeAuditFixture(t, filepath.Join(repo, "safe.txt"), "safe\n")
	gitTestOutput(t, repo, "add", "safe.txt")

	if _, err := auditGitRepositoryWithLimits(repo, repositoryAuditLimits{
		maxTreeWalks: maxPolicyTreeWalks, maxTreeMetadataBytes: maxPolicyTreeMetadataBytes,
		maxTotalObjectBytes: 5,
	}); err == nil {
		t.Fatal("audit did not share the cumulative byte budget between worktree and Git objects")
	}
}

func newAuditRepository(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	gitTestOutput(t, repo, "init", "--quiet")
	gitTestOutput(t, repo, "config", "user.name", "Repository Policy Test")
	gitTestOutput(t, repo, "config", "user.email", "repository-policy@example.invalid")
	gitTestOutput(t, repo, "config", "commit.gpgsign", "false")
	return repo
}

func writeAuditFixture(t *testing.T, path, contents string) {
	t.Helper()
	writeAuditFixtureBytes(t, path, []byte(contents))
}

func writeAuditFixtureBytes(t *testing.T, path string, contents []byte) {
	t.Helper()
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatalf("write audit fixture: %v", err)
	}
}

func auditHasKind(audit repositoryAudit, kind string) bool {
	for _, violation := range audit.Violations {
		if violation.Kind == kind {
			return true
		}
	}
	return false
}
