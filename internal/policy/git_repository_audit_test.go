package policy_test

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

const (
	maxPolicyTotalObjectBytes  = 128 * 1024 * 1024
	maxPolicyGitMetadataBytes  = 16 * 1024 * 1024
	maxPolicyTreeMetadataBytes = 64 * 1024 * 1024
	maxPolicyTreeWalks         = 4096
)

type auditViolation struct {
	Kind         string
	Object       string
	LocationHash string
	Line         int
	Variable     string
}

type repositoryAudit struct {
	Violations        []auditViolation
	UniqueBlobsRead   int
	UniqueTreesWalked int
}

type repositoryAuditLimits struct {
	maxTreeWalks         int
	maxTreeMetadataBytes int64
	maxTotalObjectBytes  int64
}

type auditTreeBudget struct {
	limits        repositoryAuditLimits
	walks         int
	metadataBytes int64
}

type auditContentBudget struct {
	limit int64
	total int64
}

type auditCommit struct {
	oid  string
	tree string
}

type auditLocation struct {
	kind               string
	hash               string
	syntaxPath         string
	parserCookiePolicy bool
}

type auditObject struct {
	blobLocations []auditLocation
	messageKind   string
}

var (
	auditCredentialShapePattern = regexp.MustCompile(credentialShapePattern())
	auditObjectIDPattern        = regexp.MustCompile(`^[0-9a-f]{40,64}$`)
	auditOpenRegularFile        = func(path string) (*os.File, error) {
		return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	}
)

func auditGitRepository(root string) (repositoryAudit, error) {
	return auditGitRepositoryWithLimits(root, repositoryAuditLimits{
		maxTreeWalks: maxPolicyTreeWalks, maxTreeMetadataBytes: maxPolicyTreeMetadataBytes,
		maxTotalObjectBytes: maxPolicyTotalObjectBytes,
	})
}

func auditGitRepositoryWithLimits(root string, limits repositoryAuditLimits) (repositoryAudit, error) {
	if limits.maxTreeWalks <= 0 || limits.maxTreeMetadataBytes <= 0 || limits.maxTotalObjectBytes <= 0 {
		return repositoryAudit{}, fmt.Errorf("repository audit requires positive traversal limits")
	}
	audit := repositoryAudit{}
	objects := make(map[string]*auditObject)
	commitRoots := make(map[string]struct{})
	treeRoots := make(map[string]string)
	addTree := func(oid, locationKind string) {
		if _, exists := treeRoots[oid]; !exists {
			treeRoots[oid] = locationKind
		}
	}
	addBlob := func(oid string, location auditLocation) {
		entry := objects[oid]
		if entry == nil {
			entry = &auditObject{}
			objects[oid] = entry
		}
		entry.blobLocations = append(entry.blobLocations, location)
	}
	addMessage := func(oid, kind string) {
		entry := objects[oid]
		if entry == nil {
			entry = &auditObject{}
			objects[oid] = entry
		}
		if entry.messageKind == "" {
			entry.messageKind = kind
		}
	}

	refs, err := listAuditRefs(root)
	if err != nil {
		return repositoryAudit{}, err
	}
	for _, ref := range refs {
		refHash := auditMetadataHash("ref", ref.name)
		if err := appendAuditMetadataViolations(&audit, "ref", ref.name, ref.oid, refHash); err != nil {
			return repositoryAudit{}, err
		}
		switch ref.objectType {
		case "commit":
			commitRoots[ref.oid] = struct{}{}
		case "tag":
			addMessage(ref.oid, "tag-message")
		case "blob":
			addBlob(ref.oid, auditLocation{kind: "ref", hash: refHash, syntaxPath: "fixture"})
		case "tree":
			addTree(ref.oid, "ref-tree")
		default:
			return repositoryAudit{}, fmt.Errorf("repository audit encountered an unsupported ref object type")
		}
		switch ref.peeledType {
		case "":
		case "commit":
			commitRoots[ref.peeledOID] = struct{}{}
		case "blob":
			addBlob(ref.peeledOID, auditLocation{kind: "ref", hash: refHash, syntaxPath: "fixture"})
		case "tree":
			addTree(ref.peeledOID, "ref-tree")
		default:
			return repositoryAudit{}, fmt.Errorf("repository audit encountered an unsupported peeled object type")
		}
	}

	head, hasHead, err := auditHeadCommit(root)
	if err != nil {
		return repositoryAudit{}, err
	}
	if hasHead {
		commitRoots[head] = struct{}{}
	}
	commits, err := auditReachableCommits(root, commitRoots)
	if err != nil {
		return repositoryAudit{}, err
	}
	for _, commit := range commits {
		addMessage(commit.oid, "commit-message")
		addTree(commit.tree, "history")
	}
	treeIDs := make([]string, 0, len(treeRoots))
	for oid := range treeRoots {
		treeIDs = append(treeIDs, oid)
	}
	sort.Strings(treeIDs)
	treeBudget := auditTreeBudget{limits: limits}
	for _, treeID := range treeIDs {
		if err := collectAuditTree(root, treeID, treeRoots[treeID], &treeBudget, &audit, addBlob); err != nil {
			return repositoryAudit{}, err
		}
	}
	audit.UniqueTreesWalked = treeBudget.walks

	contentBudget := &auditContentBudget{limit: limits.maxTotalObjectBytes}
	if err := collectAuditIndexAndWorktree(root, objects, &audit, contentBudget, addBlob); err != nil {
		return repositoryAudit{}, err
	}
	if err := readAndScanAuditObjects(root, objects, &audit, contentBudget); err != nil {
		return repositoryAudit{}, err
	}
	audit.Violations = deduplicateAuditViolations(audit.Violations)
	return audit, nil
}

type auditRef struct {
	oid        string
	objectType string
	name       string
	peeledOID  string
	peeledType string
}

func listAuditRefs(root string) ([]auditRef, error) {
	output, err := auditGitOutput(root, nil, "for-each-ref", "--format=%(objectname)%00%(objecttype)%00%(refname)%00%(*objectname)%00%(*objecttype)")
	if err != nil {
		return nil, err
	}
	var refs []auditRef
	for _, record := range bytes.Split(bytes.TrimSuffix(output, []byte{'\n'}), []byte{'\n'}) {
		if len(record) == 0 {
			continue
		}
		fields := bytes.Split(record, []byte{0})
		if len(fields) != 5 {
			return nil, fmt.Errorf("repository audit could not parse ref metadata")
		}
		refs = append(refs, auditRef{
			oid: string(fields[0]), objectType: string(fields[1]), name: string(fields[2]),
			peeledOID: string(fields[3]), peeledType: string(fields[4]),
		})
	}
	return refs, nil
}

func auditHeadCommit(root string) (string, bool, error) {
	cmd := exec.Command("git", "-C", root, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	cmd.Stderr = io.Discard
	output, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return "", false, nil
		}
		return "", false, fmt.Errorf("repository audit could not resolve HEAD")
	}
	head := strings.TrimSpace(string(output))
	if !auditObjectIDPattern.MatchString(head) {
		return "", false, fmt.Errorf("repository audit received malformed HEAD metadata")
	}
	return head, true, nil
}

func auditReachableCommits(root string, roots map[string]struct{}) ([]auditCommit, error) {
	if len(roots) == 0 {
		return nil, nil
	}
	ordered := make([]string, 0, len(roots))
	for oid := range roots {
		ordered = append(ordered, oid)
	}
	sort.Strings(ordered)
	input := []byte(strings.Join(ordered, "\n") + "\n")
	output, err := auditGitOutput(root, input, "log", "--no-decorate", "--format=%H%x00%T", "--stdin")
	if err != nil {
		return nil, err
	}
	var commits []auditCommit
	for _, line := range bytes.Split(bytes.TrimSpace(output), []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		fields := bytes.Split(line, []byte{0})
		if len(fields) != 2 {
			return nil, fmt.Errorf("repository audit could not parse commit tree metadata")
		}
		commit := auditCommit{oid: strings.TrimSpace(string(fields[0])), tree: strings.TrimSpace(string(fields[1]))}
		if !auditObjectIDPattern.MatchString(commit.oid) || !auditObjectIDPattern.MatchString(commit.tree) {
			return nil, fmt.Errorf("repository audit received malformed commit tree metadata")
		}
		commits = append(commits, commit)
	}
	sort.Slice(commits, func(i, j int) bool { return commits[i].oid < commits[j].oid })
	return commits, nil
}

func collectAuditTree(
	root, treeish, locationKind string,
	budget *auditTreeBudget,
	audit *repositoryAudit,
	addBlob func(string, auditLocation),
) error {
	if budget == nil || budget.walks >= budget.limits.maxTreeWalks {
		return fmt.Errorf("repository audit exceeds the unique tree traversal limit")
	}
	budget.walks++
	output, err := auditGitOutput(root, nil, "ls-tree", "-r", "-z", "--full-tree", treeish)
	if err != nil {
		return err
	}
	budget.metadataBytes += int64(len(output))
	if budget.metadataBytes > budget.limits.maxTreeMetadataBytes {
		return fmt.Errorf("repository audit exceeds the cumulative tree metadata limit")
	}
	for _, record := range bytes.Split(bytes.TrimSuffix(output, []byte{0}), []byte{0}) {
		if len(record) == 0 {
			continue
		}
		metadata, path, ok := bytes.Cut(record, []byte{'\t'})
		if !ok {
			return fmt.Errorf("repository audit could not parse tree metadata")
		}
		fields := bytes.Fields(metadata)
		if len(fields) != 3 {
			return fmt.Errorf("repository audit could not parse tree entry")
		}
		if string(fields[1]) != "blob" {
			continue
		}
		pathString := string(path)
		pathHash := auditMetadataHash("path", pathString)
		if err := appendAuditMetadataViolations(audit, "path", pathString, string(fields[2]), pathHash); err != nil {
			return err
		}
		addBlob(string(fields[2]), auditLocation{
			kind: locationKind, hash: pathHash, syntaxPath: auditSyntaxPath(pathString),
			parserCookiePolicy: isProductionParserSourcePath(pathString),
		})
	}
	return nil
}

func collectAuditIndexAndWorktree(
	root string,
	objects map[string]*auditObject,
	audit *repositoryAudit,
	contentBudget *auditContentBudget,
	addBlob func(string, auditLocation),
) error {
	output, err := auditGitOutput(root, nil, "ls-files", "--stage", "-z")
	if err != nil {
		return err
	}
	for _, record := range bytes.Split(bytes.TrimSuffix(output, []byte{0}), []byte{0}) {
		if len(record) == 0 {
			continue
		}
		metadata, path, ok := bytes.Cut(record, []byte{'\t'})
		if !ok {
			return fmt.Errorf("repository audit could not parse index metadata")
		}
		fields := bytes.Fields(metadata)
		if len(fields) != 3 || string(fields[2]) != "0" {
			return fmt.Errorf("repository audit requires a conflict-free index")
		}
		mode, oid, pathString := string(fields[0]), string(fields[1]), string(path)
		pathHash := auditMetadataHash("path", pathString)
		if err := appendAuditMetadataViolations(audit, "path", pathString, oid, pathHash); err != nil {
			return err
		}
		if strings.HasPrefix(mode, "100") || mode == "120000" {
			addBlob(oid, auditLocation{
				kind: "index", hash: pathHash, syntaxPath: auditSyntaxPath(pathString),
				parserCookiePolicy: isProductionParserSourcePath(pathString),
			})
		}
		worktreePath := filepath.Join(root, filepath.FromSlash(pathString))
		info, statErr := os.Lstat(worktreePath)
		if errors.Is(statErr, os.ErrNotExist) {
			continue
		}
		if statErr != nil {
			return fmt.Errorf("repository audit could not inspect a tracked worktree entry")
		}
		var contents []byte
		switch {
		case info.Mode().IsRegular():
			contents, err = readAuditRegularWorktreeFile(worktreePath, info)
			if err != nil {
				return err
			}
		case info.Mode()&os.ModeSymlink != 0:
			target, readErr := os.Readlink(worktreePath)
			if readErr != nil {
				return fmt.Errorf("repository audit could not read a tracked worktree symlink")
			}
			contents = []byte(target)
			if len(contents) > maxPolicyBlobBytes {
				return fmt.Errorf("repository audit worktree symlink exceeds the per-object scan limit")
			}
		default:
			continue
		}
		if err := contentBudget.consume(int64(len(contents))); err != nil {
			return err
		}
		if err := scanAuditContents(contents, auditLocation{
			kind: "worktree", hash: pathHash, syntaxPath: auditSyntaxPath(pathString),
			parserCookiePolicy: isProductionParserSourcePath(pathString),
		}, "", audit); err != nil {
			return err
		}
	}
	return nil
}

func readAuditRegularWorktreeFile(path string, before os.FileInfo) ([]byte, error) {
	file, err := auditOpenRegularFile(path)
	if err != nil {
		return nil, fmt.Errorf("repository audit could not securely open a tracked worktree blob")
	}
	defer file.Close()

	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return nil, fmt.Errorf("repository audit worktree blob changed before secure open")
	}
	if opened.Size() > maxPolicyBlobBytes {
		return nil, fmt.Errorf("repository audit worktree blob exceeds the per-object scan limit")
	}
	contents, err := io.ReadAll(io.LimitReader(file, maxPolicyBlobBytes+1))
	if err != nil {
		return nil, fmt.Errorf("repository audit could not read a tracked worktree blob")
	}
	if len(contents) > maxPolicyBlobBytes {
		return nil, fmt.Errorf("repository audit worktree blob exceeds the per-object scan limit")
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(opened, after) || opened.Size() != after.Size() || !opened.ModTime().Equal(after.ModTime()) {
		return nil, fmt.Errorf("repository audit worktree blob changed during scan")
	}
	return contents, nil
}

func readAndScanAuditObjects(root string, objects map[string]*auditObject, audit *repositoryAudit, contentBudget *auditContentBudget) error {
	oids := make([]string, 0, len(objects))
	for oid := range objects {
		oids = append(oids, oid)
	}
	sort.Strings(oids)
	if len(oids) == 0 {
		return nil
	}

	cmd := exec.Command("git", "-C", root, "cat-file", "--batch")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("repository audit could not open the object reader")
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("repository audit could not open the object stream")
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("repository audit could not start the object reader")
	}
	reader := bufio.NewReader(stdout)
	completed := false
	defer func() {
		_ = stdin.Close()
		if !completed && cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()

	for _, requestedOID := range oids {
		if _, err := fmt.Fprintln(stdin, requestedOID); err != nil {
			return fmt.Errorf("repository audit could not request an object")
		}
		header, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("repository audit received an incomplete object header")
		}
		fields := strings.Fields(header)
		if len(fields) != 3 || fields[0] != requestedOID {
			return fmt.Errorf("repository audit received malformed object metadata")
		}
		size, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil || size < 0 {
			return fmt.Errorf("repository audit received an invalid object size")
		}
		if size > maxPolicyBlobBytes {
			return fmt.Errorf("repository audit object exceeds the per-object scan limit")
		}
		if err := contentBudget.consume(size); err != nil {
			return err
		}
		contents := make([]byte, size)
		if _, err := io.ReadFull(reader, contents); err != nil {
			return fmt.Errorf("repository audit received incomplete object contents")
		}
		if delimiter, err := reader.ReadByte(); err != nil || delimiter != '\n' {
			return fmt.Errorf("repository audit received a malformed object delimiter")
		}

		object := objects[requestedOID]
		if len(object.blobLocations) != 0 {
			if fields[1] != "blob" {
				return fmt.Errorf("repository audit object type did not match a blob request")
			}
			audit.UniqueBlobsRead++
			for _, location := range deduplicateAuditLocations(object.blobLocations) {
				if err := scanAuditContents(contents, location, requestedOID, audit); err != nil {
					return err
				}
			}
		}
		if object.messageKind != "" {
			wantType := strings.TrimSuffix(object.messageKind, "-message")
			if fields[1] != wantType {
				return fmt.Errorf("repository audit object type did not match a message request")
			}
			message, ok := gitObjectMessage(contents)
			if !ok {
				return fmt.Errorf("repository audit could not parse an object message")
			}
			location := auditLocation{kind: object.messageKind, hash: auditMetadataHash(object.messageKind, requestedOID), syntaxPath: "message.txt"}
			if err := scanAuditContents(message, location, requestedOID, audit); err != nil {
				return err
			}
		}
	}
	if err := stdin.Close(); err != nil {
		return fmt.Errorf("repository audit could not close the object request stream")
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("repository audit object reader failed")
	}
	completed = true
	return nil
}

func (budget *auditContentBudget) consume(size int64) error {
	if budget == nil || size < 0 || budget.limit <= 0 || size > budget.limit-budget.total {
		return fmt.Errorf("repository audit exceeds the cumulative content scan limit")
	}
	budget.total += size
	return nil
}

func scanAuditContents(contents []byte, location auditLocation, oid string, audit *repositoryAudit) error {
	syntaxPath := location.syntaxPath
	if syntaxPath == "" {
		syntaxPath = "fixture"
	}
	matches, err := scanSensitiveDefaultsStrict(contents, syntaxPath, location.kind)
	if err != nil {
		return fmt.Errorf("repository audit rejected an unscannable %s object", sanitizedAuditKind(location.kind))
	}
	for _, match := range matches {
		kind := "sensitive-default"
		if location.kind == "commit-message" || location.kind == "tag-message" {
			kind = location.kind + "-sensitive-default"
		}
		audit.Violations = append(audit.Violations, auditViolation{
			Kind: kind, Object: shortRevision(oid), LocationHash: location.hash,
			Line: match.Line, Variable: match.Variable,
		})
	}
	if auditCredentialShapePattern.Match(contents) {
		kind := "blob-credential-shape"
		if location.kind == "commit-message" || location.kind == "tag-message" {
			kind = location.kind + "-credential-shape"
		}
		audit.Violations = append(audit.Violations, auditViolation{Kind: kind, Object: shortRevision(oid), LocationHash: location.hash})
	}
	if location.parserCookiePolicy {
		violation, err := scanParserCookiePolicy(contents)
		if err != nil {
			return fmt.Errorf("repository audit rejected an unscannable parser source object")
		}
		if violation {
			audit.Violations = append(audit.Violations, auditViolation{
				Kind: "parser-cookie-literal", Object: shortRevision(oid), LocationHash: location.hash,
			})
		}
	}
	return nil
}

func appendAuditMetadataViolations(audit *repositoryAudit, metadataKind, value, oid, locationHash string) error {
	if auditCredentialShapePattern.MatchString(value) {
		audit.Violations = append(audit.Violations, auditViolation{
			Kind: metadataKind + "-name-credential-shape", Object: shortRevision(oid), LocationHash: locationHash,
		})
	}
	components := strings.NewReplacer("/", "\n", "\\", "\n").Replace(value)
	matches, err := scanSensitiveDefaultsStrict([]byte(components+"\n"), "metadata.txt", metadataKind+"-name")
	if err != nil {
		return fmt.Errorf("repository audit rejected unscannable metadata")
	}
	for _, match := range matches {
		audit.Violations = append(audit.Violations, auditViolation{
			Kind: metadataKind + "-name-sensitive-default", Object: shortRevision(oid),
			LocationHash: locationHash, Line: match.Line, Variable: match.Variable,
		})
	}
	return nil
}

func gitObjectMessage(contents []byte) ([]byte, bool) {
	separator := bytes.Index(contents, []byte("\n\n"))
	if separator < 0 {
		return nil, false
	}
	return contents[separator+2:], true
}

func deduplicateAuditLocations(locations []auditLocation) []auditLocation {
	seen := make(map[string]struct{})
	result := make([]auditLocation, 0, len(locations))
	for _, location := range locations {
		key := location.kind + ":" + location.hash
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, location)
	}
	return result
}

func deduplicateAuditViolations(violations []auditViolation) []auditViolation {
	seen := make(map[string]int)
	result := make([]auditViolation, 0, len(violations))
	for _, violation := range violations {
		key := fmt.Sprintf("%s\x00%s\x00%d\x00%s", violation.Kind, violation.LocationHash, violation.Line, violation.Variable)
		if index, ok := seen[key]; ok {
			if result[index].Object == "" && violation.Object != "" {
				result[index].Object = violation.Object
			}
			continue
		}
		seen[key] = len(result)
		result = append(result, violation)
	}
	return result
}

func auditMetadataHash(kind, value string) string {
	return sanitizedLabel(kind + "\x00" + value)
}

func auditSyntaxPath(path string) string {
	if strings.EqualFold(filepath.Base(path), "Dockerfile") {
		return "Dockerfile"
	}
	return "fixture" + strings.ToLower(filepath.Ext(path))
}

func isProductionParserSourcePath(path string) bool {
	path = strings.ToLower(filepath.ToSlash(filepath.Clean(path)))
	return strings.HasSuffix(path, ".go") &&
		!strings.HasSuffix(path, "_test.go") &&
		(strings.HasPrefix(path, "internal/parsers/") || strings.HasPrefix(path, "internal/parser/"))
}

func sanitizedAuditKind(kind string) string {
	switch kind {
	case "index", "worktree", "history", "ref", "ref-tree", "commit-message", "tag-message":
		return kind
	default:
		return "repository"
	}
}

func formatAuditViolations(violations []auditViolation) string {
	ordered := append([]auditViolation(nil), violations...)
	sort.Slice(ordered, func(i, j int) bool {
		left := ordered[i].Kind + ordered[i].Object + ordered[i].LocationHash + ordered[i].Variable
		right := ordered[j].Kind + ordered[j].Object + ordered[j].LocationHash + ordered[j].Variable
		return left < right
	})
	const reportLimit = 40
	lines := make([]string, 0, min(len(ordered), reportLimit)+1)
	for _, violation := range ordered[:min(len(ordered), reportLimit)] {
		lines = append(lines, fmt.Sprintf("kind=%s object=%s location=%s line=%d variable=%s",
			violation.Kind, violation.Object, violation.LocationHash, violation.Line, violation.Variable))
	}
	if len(ordered) > reportLimit {
		lines = append(lines, fmt.Sprintf("... %d more sanitized violations", len(ordered)-reportLimit))
	}
	return strings.Join(lines, "\n")
}

func auditGitOutput(root string, input []byte, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	if input != nil {
		cmd.Stdin = bytes.NewReader(input)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("repository audit could not create a Git output stream")
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("repository audit could not start a Git metadata command")
	}
	output, readErr := io.ReadAll(io.LimitReader(stdout, maxPolicyGitMetadataBytes+1))
	if readErr != nil {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
		return nil, fmt.Errorf("repository audit could not read Git metadata")
	}
	if len(output) > maxPolicyGitMetadataBytes {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
		return nil, fmt.Errorf("repository audit Git metadata exceeds the scan limit")
	}
	waitErr := cmd.Wait()
	if waitErr != nil {
		return nil, fmt.Errorf("repository audit Git metadata command failed")
	}
	return output, nil
}
