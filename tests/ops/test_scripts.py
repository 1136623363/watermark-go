import importlib.util
import hashlib
import json
import os
import pathlib
import stat
import subprocess


ROOT = pathlib.Path(__file__).resolve().parents[2]
ACCEPTANCE_PATH = ROOT / "scripts" / "verify-acceptance.py"
WRITE_EVIDENCE_PATH = ROOT / "scripts" / "write-evidence.py"
CANONICAL_FIXTURE_SHA256 = "bb0f55ea17ddc613f64282a5786a7ab137a945a847b444fdd2f4bfb212bc5eba"
FIXTURE_PATH = ROOT / "tests" / "baseline" / "fixtures" / "platform-samples.json"


def load_module(path, name):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def write_json(path, payload):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(payload, ensure_ascii=False, indent=2), encoding="utf-8")


def media_test_manifest():
    return [
        {
            "name": "TestStructuredJSONGoldenMatrix",
            "package": "./internal/parser/native",
            "command": "go test ./internal/parser/native -run TestStructuredJSONGoldenMatrix -count=1",
        },
        {
            "name": "TestMediaCandidateOrderIsStable",
            "package": "./internal/parser",
            "command": "go test ./internal/parser -run TestMediaCandidateOrderIsStable -count=1",
        },
        {
            "name": "TestCanonicalURLQueryPolicyMatrix",
            "package": "./internal/parse",
            "command": "go test ./internal/parse -run TestCanonicalURLQueryPolicyMatrix -count=1",
        },
        {
            "name": "TestCacheVersionChangeMisses",
            "package": "./internal/cache",
            "command": "go test ./internal/cache -run TestCacheVersionChangeMisses -count=1",
        },
        {
            "name": "TestNegativeCachePolicyRejectsNonCacheableErrors",
            "package": "./internal/cache",
            "command": "go test ./internal/cache -run TestNegativeCachePolicyRejectsNonCacheableErrors -count=1",
        },
        {
            "name": "TestDASHCandidateOrderAndFallbackBudget",
            "package": "./internal/media",
            "command": "go test ./internal/media -run TestDASHCandidateOrderAndFallbackBudget -count=1",
        },
    ]


def manifest_sha256(manifest):
    return hashlib.sha256(
        json.dumps(manifest, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8")
    ).hexdigest()


def valid_media_parser_evidence():
    manifest = media_test_manifest()
    return {
        "schemaVersion": 1,
        "passed": True,
        "sourceCommit": "f" * 40,
        "imageDigest": "ghcr.io/1136623363/watermark-go@sha256:" + "a" * 64,
        "ciRunId": "ci-123",
        "testManifest": manifest,
        "testManifestSha256": manifest_sha256(manifest),
        "hermetic": True,
        "gates": {
            "registry": True,
            "structuredJSON": True,
            "queryPolicy": True,
            "candidateRankingBudget": True,
            "cacheSemantics": True,
            "richMedia": True,
            "unsafePatternScan": True,
        },
        "gateResults": {
            "registry": {"passed": True, "exitCode": 0, "command": "go test ./internal/parser/... -run TestRegistry -count=1"},
            "structuredJSON": {
                "passed": True,
                "exitCode": 0,
                "command": "go test ./internal/parser/native -run TestStructuredJSONGoldenMatrix -count=1",
            },
            "queryPolicy": {"passed": True, "exitCode": 0, "command": "go test ./internal/parse -run TestCanonicalURLQueryPolicyMatrix -count=1"},
            "candidateRankingBudget": {
                "passed": True,
                "exitCode": 0,
                "command": "go test ./internal/parser ./internal/media -run 'Test(MediaCandidate|DASH)' -count=1",
            },
            "cacheSemantics": {
                "passed": True,
                "exitCode": 0,
                "command": "go test ./internal/cache -run 'Test(CacheVersion|NegativeCache)' -count=1",
            },
            "richMedia": {"passed": True, "exitCode": 0, "command": "go test ./tests/contracts -run TestMediaParserIntegrationContract -count=1"},
            "unsafePatternScan": {"passed": True, "exitCode": 0, "command": "go test ./internal/policy -run TestMediaParser -count=1"},
        },
    }


def valid_migration_evidence(run_id="deploy-current", attempt_id="cutover-current"):
    return {
        "schemaVersion": 1,
        "passed": True,
        "deploymentRunId": run_id,
        "cutoverAttemptId": attempt_id,
        "chosenMigrationMode": "final_full_no_binlog",
        "finalSnapshot": True,
        "finalImport": True,
        "finalChecksum": True,
        "tableScopedNoWriter": True,
        "deltaPosition": {"state": "not_applicable"},
        "reverseReplay": {"state": "not_applicable"},
    }


def valid_observation_evidence(run_id="deploy-current", attempt_id="cutover-current"):
    started = 1000
    samples = []
    for seq in range(1, 61):
        samples.append(
            {
                "seq": seq,
                "monotonicMs": started + seq * 30000,
                "imageDigest": "ghcr.io/1136623363/watermark-go@sha256:" + "a" * 64,
                "health": "ok",
                "healthLatencyMs": 25,
                "restartCount": 0,
                "oomCount": 0,
                "ioErrors": 0,
                "memTotalBytes": 8 * 1024 * 1024 * 1024,
                "memAvailableBytes": 2 * 1024 * 1024 * 1024,
                "swapSiSo": 0,
                "memoryPSI": {"someAvg10": 0, "fullAvg10": 0},
                "ioPSI": {"someAvg10": 0, "fullAvg10": 0},
                "diskUsedPercent": 42,
                "inodeUsedPercent": 17,
            }
        )
    return {
        "schemaVersion": 1,
        "passed": True,
        "deploymentRunId": run_id,
        "cutoverAttemptId": attempt_id,
        "startedAtMonotonicMs": started,
        "endedAtMonotonicMs": started + 1800000,
        "samplesPassed": 60,
        "p95HealthLatencyMs": 25,
        "samples": samples,
    }


def valid_baseline_report(run_id="baseline-1", offset=0, invocation_prefix="baseline1"):
    fixture = json.loads(FIXTURE_PATH.read_text(encoding="utf-8"))
    sample_keys = [item["platformKey"] for item in fixture if item["enabled"] and item["sampleURL"]]
    assert len(sample_keys) == 93
    records = []
    for index, sample_key in enumerate(sample_keys):
        group = index // 3
        started = offset + group * 7000
        records.append(
            {
                "sampleKey": sample_key,
                "status": "completed",
                "ok": index < 62,
                "mediaSuccess": index < 62,
                "mediaUrls": [f"https://cdn.example/{index}.mp4"] if index < 62 else [],
                "startedMonotonicMs": started,
                "endedMonotonicMs": started + 6000,
                "parserInvocationId": f"{invocation_prefix}-{index:02d}",
            }
        )
    record_set_sha256 = hashlib.sha256(
        json.dumps(records, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8")
    ).hexdigest()
    return {
        "schemaVersion": 1,
        "passed": True,
        "runId": run_id,
        "sourceCommit": "f" * 40,
        "imageDigest": "ghcr.io/1136623363/watermark-go@sha256:" + "a" * 64,
        "canonicalFixtureSha256": CANONICAL_FIXTURE_SHA256,
        "recordSetSha256": record_set_sha256,
        "records": records,
        "completed": 93,
        "success": 62,
        "durationMs": 216000,
        "concurrency": 3,
        "maxObservedConcurrency": 3,
        "nativeEnabled": True,
        "fallbackEnabled": True,
        "cacheBypass": True,
    }


def valid_local_verification_markdown(source_commit="f" * 40, **overrides):
    fields = {
        "schemaVersion": 1,
        "passed": True,
        "runId": "task15-local",
        "sourceCommit": source_commit,
        "generatedAt": "2026-07-22T09:35:22Z",
        "gofmt": 0,
        "goVet": 0,
        "goRace": 0,
        "pythonBridgePolicy": 0,
        "baselineOpsPytest": 0,
        "frontendProvenance": 0,
        "miniProgramTests": 0,
        "mediaParserFocusedSuite": 0,
        "composeConfig": 0,
        "policy": 0,
        "gitleaks": 0,
    }
    fields.update(overrides)
    lines = ["---"]
    for key, value in fields.items():
        if isinstance(value, bool):
            value = "true" if value else "false"
        lines.append(f"{key}: {value}")
    lines.extend(
        [
            "---",
            "",
            "Task 15 local verification ran hermetic checks only; no image build, docker up, or service startup was performed.",
            "",
        ]
    )
    return "\n".join(lines)


def valid_secret_scan_evidence(source_commit="f" * 40, **overrides):
    payload = {
        "schemaVersion": 1,
        "passed": True,
        "runId": "task15-local",
        "sourceCommit": source_commit,
        "generatedAt": "2026-07-22T09:35:22Z",
        "version": "v8.30.1",
        "scope": "all-refs-history",
        "redacted": "true",
    }
    payload.update(overrides)
    return payload


def write_minimal_complete_evidence(root):
    media = valid_media_parser_evidence()
    media["deploymentRunId"] = "deploy-current"
    media["cutoverAttemptId"] = "cutover-current"
    write_json(root / "artifacts" / "verification" / "media-parser-integration.json", media)
    write_json(root / "artifacts" / "acceptance" / "media-parser-integration.json", media)
    write_json(root / "artifacts" / "migration" / "legacy-data-rehearsal.json", valid_migration_evidence())
    write_json(root / "artifacts" / "deploy" / "observation-30m.json", valid_observation_evidence())


def test_acceptance_validates_local_verification_and_secret_scan_artifacts(tmp_path):
    verifier = load_module(ACCEPTANCE_PATH, "verify_acceptance")
    source_commit = "f" * 40
    local_path = tmp_path / "artifacts" / "verification" / "local-verification.md"
    secret_path = tmp_path / "artifacts" / "verification" / "secret-scan.txt"
    local_path.parent.mkdir(parents=True, exist_ok=True)
    local_path.write_text(valid_local_verification_markdown(source_commit), encoding="utf-8")
    write_json(secret_path, valid_secret_scan_evidence(source_commit))

    result = verifier.verify_root(
        tmp_path,
        mode="schema-of-present",
        expected_source_commit=source_commit,
    )

    assert result.passed is True
    assert "artifacts/verification/local-verification.md" in result.checked
    assert "artifacts/verification/secret-scan.txt" in result.checked


def test_acceptance_rejects_broken_local_verification_artifact(tmp_path):
    verifier = load_module(ACCEPTANCE_PATH, "verify_acceptance")
    local_path = tmp_path / "artifacts" / "verification" / "local-verification.md"
    local_path.parent.mkdir(parents=True, exist_ok=True)
    local_path.write_text(
        valid_local_verification_markdown("f" * 40, gitleaks=1, sourceCommit="e" * 40),
        encoding="utf-8",
    )

    result = verifier.verify_root(
        tmp_path,
        mode="schema-of-present",
        expected_source_commit="f" * 40,
    )

    assert result.passed is False
    joined = " ".join(result.reasons)
    assert "sourceCommit" in joined
    assert "gitleaks" in joined


def test_acceptance_rejects_broken_secret_scan_artifact(tmp_path):
    verifier = load_module(ACCEPTANCE_PATH, "verify_acceptance")
    write_json(
        tmp_path / "artifacts" / "verification" / "secret-scan.txt",
        valid_secret_scan_evidence("f" * 40, passed=False, redacted="false"),
    )

    result = verifier.verify_root(
        tmp_path,
        mode="schema-of-present",
        expected_source_commit="f" * 40,
    )

    assert result.passed is False
    joined = " ".join(result.reasons)
    assert "passed" in joined
    assert "redacted" in joined


def test_acceptance_recomputes_media_parser_machine_evidence(tmp_path):
    verifier = load_module(ACCEPTANCE_PATH, "verify_acceptance")
    path = tmp_path / "artifacts" / "verification" / "media-parser-integration.json"
    evidence = valid_media_parser_evidence()
    write_json(path, evidence)

    result = verifier.verify_root(
        tmp_path,
        mode="schema-of-present",
        expected_source_commit=evidence["sourceCommit"],
        expected_image_digest=evidence["imageDigest"],
        expected_ci_run_id=evidence["ciRunId"],
    )
    assert result.passed is True

    evidence["gates"]["richMedia"] = False
    evidence["passed"] = True
    write_json(path, evidence)
    result = verifier.verify_root(
        tmp_path,
        mode="schema-of-present",
        expected_source_commit=evidence["sourceCommit"],
        expected_image_digest=evidence["imageDigest"],
        expected_ci_run_id=evidence["ciRunId"],
    )
    assert result.passed is False
    assert "richMedia" in " ".join(result.reasons)


def test_acceptance_allows_local_prebuild_media_parser_verification_evidence(tmp_path):
    verifier = load_module(ACCEPTANCE_PATH, "verify_acceptance")
    evidence = valid_media_parser_evidence()
    evidence["imageDigest"] = "notApplicablePreBuild"
    evidence["ciRunId"] = "notApplicableLocal"
    write_json(tmp_path / "artifacts" / "verification" / "media-parser-integration.json", evidence)

    result = verifier.verify_root(
        tmp_path,
        mode="schema-of-present",
        expected_source_commit=evidence["sourceCommit"],
    )
    assert result.passed is True

    result = verifier.verify_root(
        tmp_path,
        mode="schema-of-present",
        expected_source_commit=evidence["sourceCommit"],
        expected_image_digest="ghcr.io/1136623363/watermark-go@sha256:" + "a" * 64,
    )
    assert result.passed is False
    assert "imageDigest" in " ".join(result.reasons)

    write_json(tmp_path / "artifacts" / "acceptance" / "media-parser-integration.json", evidence)
    result = verifier.verify_root(tmp_path, mode="schema-of-present")
    assert result.passed is False
    assert "repository@sha256" in " ".join(result.reasons)


def test_acceptance_recomputes_media_parser_test_manifest_and_raw_commands(tmp_path):
    verifier = load_module(ACCEPTANCE_PATH, "verify_acceptance")
    path = tmp_path / "artifacts" / "verification" / "media-parser-integration.json"
    evidence = valid_media_parser_evidence()
    write_json(path, evidence)
    assert verifier.verify_root(tmp_path, mode="schema-of-present").passed is True

    evidence["testManifest"][0]["name"] = "TestRenamedAway"
    write_json(path, evidence)
    result = verifier.verify_root(tmp_path, mode="schema-of-present")
    assert result.passed is False
    joined = " ".join(result.reasons)
    assert "testManifestSha256" in joined
    assert "TestStructuredJSONGoldenMatrix" in joined

    evidence = valid_media_parser_evidence()
    evidence["gateResults"]["cacheSemantics"]["exitCode"] = 1
    evidence["gateResults"]["cacheSemantics"]["passed"] = True
    write_json(path, evidence)
    result = verifier.verify_root(tmp_path, mode="schema-of-present")
    assert result.passed is False
    assert "cacheSemantics" in " ".join(result.reasons)


def test_acceptance_rejects_stale_attempt_and_incomplete_final_full_migration(tmp_path):
    verifier = load_module(ACCEPTANCE_PATH, "verify_acceptance")
    write_json(
        tmp_path / "artifacts" / "migration" / "legacy-data-rehearsal.json",
        {
            "schemaVersion": 1,
            "passed": True,
            "deploymentRunId": "run-old",
            "chosenMigrationMode": "final_full_no_binlog",
            "finalSnapshot": True,
            "finalImport": True,
            "finalChecksum": True,
            "tableScopedNoWriter": False,
        },
    )
    result = verifier.verify_root(tmp_path, mode="schema-of-present", expected_deployment_run_id="run-new")
    assert result.passed is False
    joined = " ".join(result.reasons)
    assert "deploymentRunId" in joined
    assert "tableScopedNoWriter" in joined


def test_acceptance_requires_current_attempt_binding_when_expected(tmp_path):
    verifier = load_module(ACCEPTANCE_PATH, "verify_acceptance")
    evidence = valid_media_parser_evidence()
    write_json(tmp_path / "artifacts" / "verification" / "media-parser-integration.json", evidence)

    result = verifier.verify_root(tmp_path, mode="schema-of-present", expected_deployment_run_id="run-new")

    assert result.passed is False
    assert "deploymentRunId" in " ".join(result.reasons)


def test_acceptance_requires_not_applicable_delta_markers_for_final_full_migration(tmp_path):
    verifier = load_module(ACCEPTANCE_PATH, "verify_acceptance")
    evidence = valid_migration_evidence()
    del evidence["deltaPosition"]
    del evidence["reverseReplay"]
    write_json(tmp_path / "artifacts" / "migration" / "legacy-data-rehearsal.json", evidence)

    result = verifier.verify_root(tmp_path, mode="schema-of-present")

    assert result.passed is False
    joined = " ".join(result.reasons)
    assert "deltaPosition" in joined
    assert "reverseReplay" in joined


def test_acceptance_rejects_observation_aggregate_without_raw_samples(tmp_path):
    verifier = load_module(ACCEPTANCE_PATH, "verify_acceptance")
    write_json(
        tmp_path / "artifacts" / "deploy" / "observation-30m.json",
        {
            "schemaVersion": 1,
            "passed": True,
            "startedAtMonotonicMs": 0,
            "endedAtMonotonicMs": 1800000,
            "samplesPassed": 60,
            "p95HealthLatencyMs": 25,
            "samples": [],
        },
    )
    result = verifier.verify_root(tmp_path, mode="schema-of-present")
    assert result.passed is False
    assert "60 raw observation samples" in " ".join(result.reasons)


def test_acceptance_recomputes_observation_resource_stop_lines(tmp_path):
    verifier = load_module(ACCEPTANCE_PATH, "verify_acceptance")
    path = tmp_path / "artifacts" / "deploy" / "observation-30m.json"

    evidence = valid_observation_evidence()
    for sample in evidence["samples"]:
        sample["swapSiSo"] = 99
        sample["oomCount"] = 7
    write_json(path, evidence)
    assert verifier.verify_root(tmp_path, mode="schema-of-present").passed is True

    mutations = [
        ("disk", lambda item: item.__setitem__("diskUsedPercent", 85)),
        ("inode", lambda item: item.__setitem__("inodeUsedPercent", 85)),
        ("memory", lambda item: item.__setitem__("memAvailableBytes", 512 * 1024 * 1024)),
        ("memory", lambda item: (
            item.__setitem__("memTotalBytes", 32 * 1024 * 1024 * 1024),
            item.__setitem__("memAvailableBytes", 2 * 1024 * 1024 * 1024),
        )),
        ("memory PSI", lambda item: item.__setitem__("memoryPSI", {"someAvg10": 10.1, "fullAvg10": 0})),
        ("io PSI", lambda item: item.__setitem__("ioPSI", {"someAvg10": 0, "fullAvg10": 5.1})),
        ("OOM", lambda item: item.__setitem__("oomCount", 1)),
    ]
    for reason_fragment, mutate in mutations:
        evidence = valid_observation_evidence()
        mutate(evidence["samples"][-1])
        write_json(path, evidence)
        result = verifier.verify_root(tmp_path, mode="schema-of-present")
        assert result.passed is False, reason_fragment
        assert reason_fragment in " ".join(result.reasons)

    evidence = valid_observation_evidence()
    del evidence["samples"][-1]["memTotalBytes"]
    write_json(path, evidence)
    result = verifier.verify_root(tmp_path, mode="schema-of-present")
    assert result.passed is False
    assert "memTotalBytes" in " ".join(result.reasons)

    evidence = valid_observation_evidence()
    evidence["samples"][10]["swapSiSo"] = 1
    evidence["samples"][11]["swapSiSo"] = 2
    evidence["samples"][12]["swapSiSo"] = 3
    write_json(path, evidence)
    result = verifier.verify_root(tmp_path, mode="schema-of-present")
    assert result.passed is False
    assert "swap" in " ".join(result.reasons)


def test_acceptance_recomputes_observation_health_and_p95_from_raw_samples(tmp_path):
    verifier = load_module(ACCEPTANCE_PATH, "verify_acceptance")
    evidence = valid_observation_evidence()
    write_json(tmp_path / "artifacts" / "deploy" / "observation-30m.json", evidence)
    assert verifier.verify_root(tmp_path, mode="schema-of-present").passed is True

    evidence["samples"][0]["health"] = "down"
    evidence["samplesPassed"] = 60
    write_json(tmp_path / "artifacts" / "deploy" / "observation-30m.json", evidence)
    result = verifier.verify_root(tmp_path, mode="schema-of-present")
    assert result.passed is False
    assert "samplesPassed" in " ".join(result.reasons)

    evidence = valid_observation_evidence()
    evidence["p95HealthLatencyMs"] = 99
    write_json(tmp_path / "artifacts" / "deploy" / "observation-30m.json", evidence)
    result = verifier.verify_root(tmp_path, mode="schema-of-present")
    assert result.passed is False
    assert "p95HealthLatencyMs" in " ".join(result.reasons)


def test_acceptance_require_complete_requires_exactly_three_baseline_rounds(tmp_path):
    verifier = load_module(ACCEPTANCE_PATH, "verify_acceptance")
    write_minimal_complete_evidence(tmp_path)
    write_json(tmp_path / "artifacts" / "benchmark" / "baseline-round-1.json", valid_baseline_report())

    result = verifier.verify_root(tmp_path, mode="require-complete")
    assert result.passed is False
    assert "exactly three baseline" in " ".join(result.reasons)

    for index in range(2, 5):
        write_json(
            tmp_path / "artifacts" / "benchmark" / f"baseline-round-{index}.json",
            valid_baseline_report(
                run_id=f"baseline-{index}",
                offset=(index - 1) * 300000,
                invocation_prefix=f"baseline{index}",
            ),
        )
    result = verifier.verify_root(tmp_path, mode="require-complete")
    assert result.passed is False
    assert "exactly three baseline" in " ".join(result.reasons)


def test_acceptance_rejects_reused_baseline_round_records(tmp_path):
    verifier = load_module(ACCEPTANCE_PATH, "verify_acceptance")
    write_minimal_complete_evidence(tmp_path)
    for index, prefix in enumerate(["reused", "reused", "unique"], start=1):
        write_json(
            tmp_path / "artifacts" / "benchmark" / f"baseline-round-{index}.json",
            valid_baseline_report(
                run_id=f"baseline-{index}",
                offset=(index - 1) * 300000,
                invocation_prefix=prefix,
            ),
        )

    result = verifier.verify_root(tmp_path, mode="require-complete")
    assert result.passed is False
    assert "parserInvocationId" in " ".join(result.reasons)


def test_acceptance_binds_observation_and_baseline_to_expected_identity(tmp_path):
    verifier = load_module(ACCEPTANCE_PATH, "verify_acceptance")
    expected_digest = "ghcr.io/1136623363/watermark-go@sha256:" + "a" * 64
    other_digest = "ghcr.io/1136623363/watermark-go@sha256:" + "c" * 64
    write_json(tmp_path / "artifacts" / "deploy" / "observation-30m.json", valid_observation_evidence())
    assert verifier.verify_root(tmp_path, mode="schema-of-present", expected_image_digest=expected_digest).passed is True

    result = verifier.verify_root(tmp_path, mode="schema-of-present", expected_image_digest=other_digest)
    assert result.passed is False
    assert "imageDigest" in " ".join(result.reasons)

    baseline_root = tmp_path / "baseline-root"
    for index in range(1, 4):
        write_json(
            baseline_root / "artifacts" / "benchmark" / f"baseline-round-{index}.json",
            valid_baseline_report(
                run_id=f"baseline-{index}",
                offset=(index - 1) * 300000,
                invocation_prefix=f"baseline{index}",
            ),
        )
    assert (
        verifier.verify_root(
            baseline_root,
            mode="schema-of-present",
            expected_source_commit="f" * 40,
            expected_image_digest=expected_digest,
        ).passed
        is True
    )

    result = verifier.verify_root(
        baseline_root,
        mode="schema-of-present",
        expected_source_commit="e" * 40,
        expected_image_digest=expected_digest,
    )
    assert result.passed is False
    assert "sourceCommit" in " ".join(result.reasons)


def test_write_evidence_is_atomic_and_schema_guarded(tmp_path):
    writer = load_module(WRITE_EVIDENCE_PATH, "write_evidence")
    path = tmp_path / "artifact.json"
    payload = {"schemaVersion": 1, "passed": True, "deploymentRunId": "run-a"}
    writer.write_evidence(path, payload)
    mode = stat.S_IMODE(path.stat().st_mode)
    assert mode == 0o600
    assert json.loads(path.read_text(encoding="utf-8")) == payload

    try:
        writer.write_evidence(path, {"passed": True})
    except ValueError as exc:
        assert "schemaVersion" in str(exc)
    else:
        raise AssertionError("write_evidence accepted payload without schemaVersion")


def test_write_evidence_cli_builds_versioned_json_and_markdown(tmp_path):
    json_path = tmp_path / "secret-scan.txt"
    proc = subprocess.run(
        [
            "python3",
            str(WRITE_EVIDENCE_PATH),
            "--output",
            str(json_path),
            "--schema-version",
            "1",
            "--passed",
            "true",
            "--run-id",
            "task15-local",
            "--field",
            "version=v8.30.1",
            "--field",
            "scope=all-refs-history",
        ],
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    assert proc.returncode == 0, proc.stderr
    payload = json.loads(json_path.read_text(encoding="utf-8"))
    assert payload == {
        "schemaVersion": 1,
        "passed": True,
        "runId": "task15-local",
        "version": "v8.30.1",
        "scope": "all-refs-history",
    }
    assert stat.S_IMODE(json_path.stat().st_mode) == 0o600

    markdown_path = tmp_path / "local-verification.md"
    proc = subprocess.run(
        [
            "python3",
            str(WRITE_EVIDENCE_PATH),
            "--output",
            str(markdown_path),
            "--format",
            "markdown",
            "--schema-version",
            "1",
            "--passed",
            "true",
            "--run-id",
            "task15-local",
            "--source-commit",
            "f" * 40,
            "--field",
            "commands=pytest:0,go-test:0",
            "--summary",
            "Local verification passed with redacted command summaries.",
        ],
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    assert proc.returncode == 0, proc.stderr
    body = markdown_path.read_text(encoding="utf-8")
    assert body.startswith("---\n")
    assert "schemaVersion: 1\n" in body
    assert "passed: true\n" in body
    assert "sourceCommit: " + "f" * 40 in body
    assert "Local verification passed" in body
    assert stat.S_IMODE(markdown_path.stat().st_mode) == 0o600


def test_write_evidence_cli_rejects_invalid_field_keys(tmp_path):
    for index, field in enumerate(["bad:key=value", "1bad=value", "---=value", "bad-key=value"]):
        output = tmp_path / f"invalid-{index}.json"
        proc = subprocess.run(
            [
                "python3",
                str(WRITE_EVIDENCE_PATH),
                "--output",
                str(output),
                "--schema-version",
                "1",
                "--passed",
                "true",
                "--run-id",
                "task15-local",
                "--field",
                field,
            ],
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
        )
        assert proc.returncode != 0
        assert "field" in proc.stderr or "key" in proc.stderr
        assert not output.exists()


def test_acceptance_allows_local_prebuild_media_parser_evidence_only_in_verification_slot(tmp_path):
    verifier = load_module(ACCEPTANCE_PATH, "verify_acceptance")
    evidence = valid_media_parser_evidence()
    evidence["imageDigest"] = "notApplicablePreBuild"
    evidence["ciRunId"] = "notApplicableLocal"
    write_json(tmp_path / "artifacts" / "verification" / "media-parser-integration.json", evidence)

    assert verifier.verify_root(tmp_path, mode="schema-of-present").passed is True

    result = verifier.verify_root(
        tmp_path,
        mode="schema-of-present",
        expected_image_digest="ghcr.io/1136623363/watermark-go@sha256:" + "a" * 64,
    )
    assert result.passed is False
    assert "imageDigest" in " ".join(result.reasons)

    acceptance_root = tmp_path / "acceptance-root"
    write_json(acceptance_root / "artifacts" / "acceptance" / "media-parser-integration.json", evidence)
    result = verifier.verify_root(acceptance_root, mode="schema-of-present")
    assert result.passed is False
    assert "imageDigest" in " ".join(result.reasons)


def test_shell_scripts_are_guarded_and_never_build_images():
    required = [
        "smoke.sh",
        "deploy-local.sh",
        "rollback-local.sh",
        "preflight.sh",
        "observe.sh",
        "verify-image.sh",
        "host-snapshot.sh",
        "promote.sh",
    ]
    for name in required:
        path = ROOT / "scripts" / name
        body = path.read_text(encoding="utf-8")
        assert body.startswith("#!/usr/bin/env bash\nset -Eeuo pipefail\n"), name
        assert "watermark-go" in body, name
        assert "trap " in body, name
        assert "docker build " not in body
        assert "docker build\n" not in body
        assert "docker compose build" not in body
        assert "buildx build" not in body
        assert "docker load" not in body
        subprocess.run(["bash", "-n", str(path)], check=True)

    deploy = (ROOT / "scripts" / "deploy-local.sh").read_text(encoding="utf-8")
    assert "--force-recreate --no-deps data-gate-" in deploy
    assert " up -d mysql redis\n" in deploy
    assert "wait_for_support_services" in deploy
    assert "mysqladmin ping" in deploy
    assert "redis-cli ping" in deploy
    deploy_wait_call = "\nwait_for_support_services\n"
    assert deploy_wait_call in deploy
    assert deploy.index(" pull") < deploy.index(" up -d mysql redis")
    assert deploy.index(" up -d mysql redis") < deploy.index("--force-recreate --no-deps data-gate-")
    assert deploy.index(" up -d mysql redis") < deploy.index(deploy_wait_call)
    assert deploy.index(deploy_wait_call) < deploy.index("--force-recreate --no-deps data-gate-")
    assert "runtime_value()" in deploy
    assert 'runtime_value "RECOVERY_API_HOST_PORT" "5001"' in deploy
    assert 'runtime_value "CANDIDATE_API_HOST_PORT" "15001"' in deploy
    assert "docker compose" in deploy
    assert " pull\n" in deploy or " pull " in deploy
    assert "CANDIDATE_API_HOST_PORT:-15001" not in deploy
    assert "RECOVERY_API_HOST_PORT:-5001" not in deploy
    assert "API_PORT:-5001" not in deploy

    compose = (ROOT / "deploy" / "compose.yml").read_text(encoding="utf-8")
    env_example = (ROOT / "deploy" / "env.example").read_text(encoding="utf-8")
    assert "${CANDIDATE_API_HOST_PORT:-15001}:5001" in compose
    assert "CANDIDATE_API_HOST_PORT=15001" in env_example

    rollback = (ROOT / "scripts" / "rollback-local.sh").read_text(encoding="utf-8")
    assert "rollbackMode=absent_two_stage" in rollback
    assert "RECOVERY_IMAGE" in rollback
    assert " up -d mysql redis\n" in rollback
    assert "wait_for_support_services" in rollback
    assert "mysqladmin ping" in rollback
    assert "redis-cli ping" in rollback
    rollback_wait_call = "\nwait_for_support_services\n"
    assert rollback_wait_call in rollback
    assert rollback.index(" pull") < rollback.index(" up -d mysql redis")
    assert rollback.index(" up -d mysql redis") < rollback.index("--force-recreate --no-deps data-gate-recovery")
    assert rollback.index(" up -d mysql redis") < rollback.index(rollback_wait_call)
    assert rollback.index(rollback_wait_call) < rollback.index("--force-recreate --no-deps data-gate-recovery")

    observe = (ROOT / "scripts" / "observe.sh").read_text(encoding="utf-8")
    assert "startedAtMonotonicMs" in observe
    assert "samplesPassed" in observe
    assert "p95HealthLatencyMs" in observe
    assert "curl -fsS" in observe
    assert "IMAGE_DIGEST" in observe
    assert 'for key in ("restartCount", "ioErrors")' in observe
    assert '("restartCount", "oomCount", "ioErrors")' not in observe

    promote = (ROOT / "scripts" / "promote.sh").read_text(encoding="utf-8")
    assert "scripts/write-evidence.py" in promote
    assert "promotion-marker.txt" in promote
    assert "> /tmp/watermark-promotion-evidence.json" not in promote


def test_preflight_accepts_compose_v2_structured_allowed_ports(tmp_path):
    compose_file = tmp_path / "compose.yml"
    compose_file.write_text(
        "services:\n"
        "  api-recovery:\n"
        "    image: ghcr.io/1136623363/watermark-go@sha256:" + "a" * 64 + "\n",
        encoding="utf-8",
    )
    runtime_env = tmp_path / "runtime.env"
    runtime_env.write_text("RECOVERY_API_HOST_PORT=15001\n", encoding="utf-8")
    runtime_env.chmod(0o600)
    fake_bin = tmp_path / "bin"
    fake_bin.mkdir()
    fake_docker = fake_bin / "docker"
    fake_docker.write_text(
        "#!/usr/bin/env python3\n"
        "import json, sys\n"
        "if sys.argv[1:3] != ['compose', '--env-file'] or 'config' not in sys.argv:\n"
        "    raise SystemExit('unexpected docker invocation: ' + ' '.join(sys.argv))\n"
        "print(json.dumps({'services': {'api-recovery': {'ports': ["
        "{'host_ip': '127.0.0.1', 'published': '15001', 'target': 5001, 'protocol': 'tcp'}"
        "]}}}))\n",
        encoding="utf-8",
    )
    fake_docker.chmod(0o755)

    env = os.environ.copy()
    env["PATH"] = str(fake_bin) + os.pathsep + env["PATH"]
    env["COMPOSE_FILE"] = str(compose_file)
    env["RUNTIME_ENV"] = str(runtime_env)
    proc = subprocess.run(
        ["bash", str(ROOT / "scripts" / "preflight.sh")],
        env=env,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )

    assert proc.returncode == 0, proc.stderr + proc.stdout
    assert "PASS project=watermark-go" in proc.stdout


def test_verify_acceptance_cli_rejects_old_pass_artifact(tmp_path):
    write_json(
        tmp_path / "artifacts" / "verification" / "media-parser-integration.json",
        valid_media_parser_evidence() | {"deploymentRunId": "run-old"},
    )
    proc = subprocess.run(
        [
            "python3",
            str(ACCEPTANCE_PATH),
            "--root",
            str(tmp_path),
            "--schema-of-present",
            "--deployment-run-id",
            "run-new",
        ],
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    assert proc.returncode != 0
    assert "deploymentRunId" in proc.stderr


def test_ci_frontend_checkout_uses_cross_repo_secret():
    workflow = (ROOT / ".github" / "workflows" / "ci-image.yml").read_text(encoding="utf-8")
    assert "repository: 1136623363/watermark" in workflow
    assert "token: ${{ secrets.FRONTEND_REPO_TOKEN }}" in workflow


def test_ci_python_runtime_smoke_does_not_write_pycache_under_app():
    workflow = (ROOT / ".github" / "workflows" / "ci-image.yml").read_text(encoding="utf-8")
    assert "-m py_compile /app/bridges/universal/python/bridge.py" not in workflow
    assert "compile(pathlib.Path('/app/bridges/universal/python/bridge.py').read_text" in workflow


def test_ci_release_evidence_only_pushes_do_not_rebuild_images():
    workflow = (ROOT / ".github" / "workflows" / "ci-image.yml").read_text(encoding="utf-8")
    assert "paths-ignore:" in workflow
    assert '"artifacts/release/**"' in workflow
    assert '"artifacts/verification/**"' in workflow
