#!/usr/bin/env python3
"""Verify deployment/acceptance evidence from raw machine-readable fields."""

from __future__ import annotations

import argparse
import hashlib
import importlib.util
import json
import pathlib
import re
import sys
from typing import Any


MEDIA_GATES = (
    "registry",
    "structuredJSON",
    "queryPolicy",
    "candidateRankingBudget",
    "cacheSemantics",
    "richMedia",
    "unsafePatternScan",
)
REQUIRED_MEDIA_TESTS = (
    "TestStructuredJSONGoldenMatrix",
    "TestMediaCandidateOrderIsStable",
    "TestCanonicalURLQueryPolicyMatrix",
    "TestCacheVersionChangeMisses",
    "TestNegativeCachePolicyRejectsNonCacheableErrors",
    "TestDASHCandidateOrderAndFallbackBudget",
)
FULL_GIT_SHA_RE = re.compile(r"[a-f0-9]{40}")
IMAGE_DIGEST_RE = re.compile(r".+@sha256:[a-f0-9]{64}")
SHA256_RE = re.compile(r"[a-f0-9]{64}")

MEDIA_EVIDENCE_PATHS = (
    pathlib.Path("artifacts/verification/media-parser-integration.json"),
    pathlib.Path("artifacts/acceptance/media-parser-integration.json"),
)
MIGRATION_PATH = pathlib.Path("artifacts/migration/legacy-data-rehearsal.json")
OBSERVATION_PATH = pathlib.Path("artifacts/deploy/observation-30m.json")
BASELINE_GLOBS = (
    "artifacts/benchmark/baseline-round-*.json",
    "artifacts/benchmark/run-*.json",
)


class VerificationResult:
    def __init__(self, passed: bool, reasons: list[str], checked: list[str] | None = None):
        self.passed = passed
        self.reasons = reasons
        self.checked = checked or []

    def to_json(self) -> dict[str, Any]:
        return {"passed": self.passed, "reasons": self.reasons, "checked": self.checked}


def verify_root(
    root: str | pathlib.Path,
    *,
    mode: str,
    expected_deployment_run_id: str | None = None,
    expected_cutover_attempt_id: str | None = None,
    expected_source_commit: str | None = None,
    expected_image_digest: str | None = None,
    expected_ci_run_id: str | None = None,
) -> VerificationResult:
    root = pathlib.Path(root)
    reasons: list[str] = []
    checked: list[str] = []
    present = False

    for relative in MEDIA_EVIDENCE_PATHS:
        path = root / relative
        if path.exists():
            present = True
            checked.append(str(relative))
            reasons.extend(
                validate_media_parser_evidence(
                    load_json(path),
                    str(relative),
                    expected_source_commit=expected_source_commit,
                    expected_image_digest=expected_image_digest,
                    expected_ci_run_id=expected_ci_run_id,
                    expected_deployment_run_id=expected_deployment_run_id,
                    expected_cutover_attempt_id=expected_cutover_attempt_id,
                )
            )

    migration = root / MIGRATION_PATH
    if migration.exists():
        present = True
        checked.append(str(MIGRATION_PATH))
        reasons.extend(
            validate_migration_evidence(
                load_json(migration),
                expected_deployment_run_id=expected_deployment_run_id,
                expected_cutover_attempt_id=expected_cutover_attempt_id,
            )
        )

    observation = root / OBSERVATION_PATH
    if observation.exists():
        present = True
        checked.append(str(OBSERVATION_PATH))
        reasons.extend(
            validate_observation_evidence(
                load_json(observation),
                expected_deployment_run_id=expected_deployment_run_id,
                expected_cutover_attempt_id=expected_cutover_attempt_id,
                expected_image_digest=expected_image_digest,
            )
        )

    baseline_paths = find_baseline_paths(root)
    baseline_reports: list[dict[str, Any]] = []
    baseline_labels: list[str] = []
    for baseline in baseline_paths:
        present = True
        label = str(baseline.relative_to(root))
        checked.append(label)
        report = load_json(baseline)
        baseline_reports.append(report)
        baseline_labels.append(label)
        reasons.extend(
            validate_baseline_report(
                report,
                label,
                expected_source_commit=expected_source_commit,
                expected_image_digest=expected_image_digest,
            )
        )

    if baseline_reports:
        if len(baseline_reports) == 3:
            reasons.extend(validate_baseline_round_set(baseline_reports, baseline_labels))
        elif mode == "schema-of-present":
            reasons.append("baseline evidence must include exactly three baseline round reports when any round is present")

    if mode == "require-complete":
        required = set(str(path) for path in MEDIA_EVIDENCE_PATHS)
        required.update({str(MIGRATION_PATH), str(OBSERVATION_PATH)})
        missing = sorted(path for path in required if not (root / path).exists())
        for path in missing:
            reasons.append(f"complete acceptance missing {path}")
        if len(baseline_reports) != 3:
            reasons.append("complete acceptance requires exactly three baseline round reports")
    elif mode == "schema-of-present" and not present:
        checked.append("no-present-artifacts")

    return VerificationResult(not reasons, reasons, checked)


def validate_common(
    evidence: dict[str, Any],
    label: str,
    *,
    expected_deployment_run_id: str | None,
    expected_cutover_attempt_id: str | None,
) -> list[str]:
    reasons: list[str] = []
    if not isinstance(evidence.get("schemaVersion"), int):
        reasons.append(f"{label}: schemaVersion must be an integer")
    if evidence.get("passed") is not True:
        reasons.append(f"{label}: passed must be true and match recomputed result")
    if expected_deployment_run_id and evidence.get("deploymentRunId") != expected_deployment_run_id:
        reasons.append(f"{label}: deploymentRunId does not match current attempt")
    if expected_cutover_attempt_id and evidence.get("cutoverAttemptId") != expected_cutover_attempt_id:
        reasons.append(f"{label}: cutoverAttemptId does not match current attempt")
    return reasons


def validate_media_parser_evidence(
    evidence: dict[str, Any],
    label: str,
    *,
    expected_source_commit: str | None,
    expected_image_digest: str | None,
    expected_ci_run_id: str | None,
    expected_deployment_run_id: str | None,
    expected_cutover_attempt_id: str | None,
) -> list[str]:
    reasons = validate_common(
        evidence,
        label,
        expected_deployment_run_id=expected_deployment_run_id,
        expected_cutover_attempt_id=expected_cutover_attempt_id,
    )
    if expected_source_commit and evidence.get("sourceCommit") != expected_source_commit:
        reasons.append(f"{label}: sourceCommit does not match")
    if expected_image_digest and evidence.get("imageDigest") != expected_image_digest:
        reasons.append(f"{label}: imageDigest does not match")
    if expected_ci_run_id and evidence.get("ciRunId") != expected_ci_run_id:
        reasons.append(f"{label}: ciRunId does not match")
    if not FULL_GIT_SHA_RE.fullmatch(str(evidence.get("sourceCommit", ""))):
        reasons.append(f"{label}: sourceCommit must be a full git SHA")

    image_digest = str(evidence.get("imageDigest", ""))
    ci_run_id = str(evidence.get("ciRunId", ""))
    local_prebuild = (
        label == str(MEDIA_EVIDENCE_PATHS[0])
        and image_digest == "notApplicablePreBuild"
        and ci_run_id == "notApplicableLocal"
        and expected_image_digest is None
        and expected_ci_run_id is None
    )
    if not local_prebuild and not IMAGE_DIGEST_RE.fullmatch(image_digest):
        reasons.append(f"{label}: imageDigest must be repository@sha256")
    if not ci_run_id.strip():
        reasons.append(f"{label}: ciRunId must be non-empty")
    if ci_run_id == "notApplicableLocal" and not local_prebuild:
        reasons.append(f"{label}: ciRunId must identify a trusted CI run outside local prebuild verification")

    test_manifest = evidence.get("testManifest")
    if not isinstance(test_manifest, list) or not test_manifest:
        reasons.append(f"{label}: testManifest must be a non-empty raw test manifest")
        test_manifest = []
    manifest_hash = stable_sha256(test_manifest)
    if not SHA256_RE.fullmatch(str(evidence.get("testManifestSha256", ""))):
        reasons.append(f"{label}: testManifestSha256 must be sha256 hex")
    elif evidence.get("testManifestSha256") != manifest_hash:
        reasons.append(f"{label}: testManifestSha256 must be recomputed from testManifest")
    manifest_names = manifest_test_names(test_manifest)
    for required_test in REQUIRED_MEDIA_TESTS:
        if required_test not in manifest_names:
            reasons.append(f"{label}: testManifest missing focused suite test {required_test}")

    if evidence.get("hermetic") is not True or evidence.get("liveAggregateOnly") is True:
        reasons.append(f"{label}: mediaParserIntegration must use hermetic raw test evidence")
    gates = evidence.get("gates")
    if not isinstance(gates, dict):
        reasons.append(f"{label}: gates must be an object")
        gates = {}
    gate_results = evidence.get("gateResults")
    if not isinstance(gate_results, dict):
        reasons.append(f"{label}: gateResults must contain raw command results")
        gate_results = {}
    for gate in MEDIA_GATES:
        if gates.get(gate) is not True:
            reasons.append(f"{label}: mediaParserIntegration gate {gate} is not true")
        gate_result = gate_results.get(gate)
        if not isinstance(gate_result, dict):
            reasons.append(f"{label}: mediaParserIntegration gate {gate} missing raw command result")
            continue
        if gate_result.get("passed") is not True:
            reasons.append(f"{label}: mediaParserIntegration gate {gate} raw result is not passed")
        if gate_result.get("exitCode") != 0:
            reasons.append(f"{label}: mediaParserIntegration gate {gate} raw exitCode is not 0")
        command = gate_result.get("command")
        if not isinstance(command, str) or not command.strip():
            reasons.append(f"{label}: mediaParserIntegration gate {gate} command must be recorded")
    return reasons


def validate_migration_evidence(
    evidence: dict[str, Any],
    *,
    expected_deployment_run_id: str | None,
    expected_cutover_attempt_id: str | None,
) -> list[str]:
    reasons = validate_common(
        evidence,
        str(MIGRATION_PATH),
        expected_deployment_run_id=expected_deployment_run_id,
        expected_cutover_attempt_id=expected_cutover_attempt_id,
    )
    mode = evidence.get("chosenMigrationMode")
    if mode == "final_full_no_binlog":
        for key in ("finalSnapshot", "finalImport", "finalChecksum", "tableScopedNoWriter"):
            if evidence.get(key) is not True:
                reasons.append(f"{MIGRATION_PATH}: {key} required for final_full_no_binlog")
        for key in ("deltaPosition", "reverseReplay"):
            value = evidence.get(key)
            if not isinstance(value, dict) or value.get("state") != "not_applicable":
                reasons.append(f"{MIGRATION_PATH}: {key} must be not_applicable for final_full_no_binlog")
    elif mode == "binlog_delta":
        if not evidence.get("deltaPosition"):
            reasons.append(f"{MIGRATION_PATH}: binlog_delta requires deltaPosition")
    else:
        reasons.append(f"{MIGRATION_PATH}: unsupported chosenMigrationMode {mode!r}")
    return reasons


def validate_observation_evidence(
    evidence: dict[str, Any],
    *,
    expected_deployment_run_id: str | None,
    expected_cutover_attempt_id: str | None,
    expected_image_digest: str | None,
) -> list[str]:
    reasons = validate_common(
        evidence,
        str(OBSERVATION_PATH),
        expected_deployment_run_id=expected_deployment_run_id,
        expected_cutover_attempt_id=expected_cutover_attempt_id,
    )
    samples = evidence.get("samples")
    if not isinstance(samples, list) or len(samples) != 60:
        return reasons + [f"{OBSERVATION_PATH}: requires exactly 60 raw observation samples"]
    try:
        started = int(evidence["startedAtMonotonicMs"])
        ended = int(evidence["endedAtMonotonicMs"])
    except (KeyError, TypeError, ValueError):
        reasons.append(f"{OBSERVATION_PATH}: missing monotonic started/ended window")
        started = ended = 0
    if ended - started < 1_800_000:
        reasons.append(f"{OBSERVATION_PATH}: observation window must be at least 1800s")

    previous_seq = 0
    previous_time: int | None = None
    latencies: list[float] = []
    recomputed_samples_passed = 0
    first_image_digest: str | None = None
    swap_counters: list[int] = []
    oom_counters: list[int] = []
    required_keys = {
        "imageDigest",
        "health",
        "healthLatencyMs",
        "restartCount",
        "oomCount",
        "ioErrors",
        "memTotalBytes",
        "memAvailableBytes",
        "swapSiSo",
        "memoryPSI",
        "ioPSI",
        "diskUsedPercent",
        "inodeUsedPercent",
    }
    for index, sample in enumerate(samples):
        if not isinstance(sample, dict):
            reasons.append(f"{OBSERVATION_PATH}: sample[{index}] must be object")
            continue
        missing = sorted(required_keys - set(sample))
        if missing:
            reasons.append(f"{OBSERVATION_PATH}: sample[{index}] missing {','.join(missing)}")
        sample_image_digest = str(sample.get("imageDigest", ""))
        if not IMAGE_DIGEST_RE.fullmatch(sample_image_digest):
            reasons.append(f"{OBSERVATION_PATH}: sample[{index}] imageDigest must be repository@sha256")
        if expected_image_digest and sample_image_digest != expected_image_digest:
            reasons.append(f"{OBSERVATION_PATH}: sample[{index}] imageDigest does not match")
        if first_image_digest is None:
            first_image_digest = sample_image_digest
        elif sample_image_digest != first_image_digest:
            reasons.append(f"{OBSERVATION_PATH}: sample imageDigest values must be identical")
        seq = int(sample.get("seq", 0))
        if seq != previous_seq + 1:
            reasons.append(f"{OBSERVATION_PATH}: sample sequence must be unique and strictly increasing")
        previous_seq = seq
        timestamp = int(sample.get("monotonicMs", 0))
        if index == 0 and not (25_000 <= timestamp - started <= 35_000):
            reasons.append(f"{OBSERVATION_PATH}: first sample must be around +30s")
        if previous_time is not None and not (25_000 <= timestamp - previous_time <= 35_000):
            reasons.append(f"{OBSERVATION_PATH}: adjacent samples must be 25-35s apart")
        previous_time = timestamp
        try:
            latencies.append(float(sample["healthLatencyMs"]))
        except (KeyError, TypeError, ValueError):
            pass
        evaluate_resource_stop_lines(sample, index, reasons, swap_counters, oom_counters)
        if sample_health_passed(sample):
            recomputed_samples_passed += 1
        else:
            reasons.append(f"{OBSERVATION_PATH}: sample[{index}] health did not pass")
    if previous_time is not None and previous_time < started + 1_800_000:
        reasons.append(f"{OBSERVATION_PATH}: 60th sample must be no earlier than +1800s")
    if evidence.get("samplesPassed") != recomputed_samples_passed:
        reasons.append(f"{OBSERVATION_PATH}: samplesPassed must be recomputed from raw samples")
    if latencies and evidence.get("p95HealthLatencyMs") != percentile_95(latencies):
        reasons.append(f"{OBSERVATION_PATH}: p95HealthLatencyMs must be recomputed from raw samples")
    if has_three_consecutive_swap_increases(swap_counters):
        reasons.append(f"{OBSERVATION_PATH}: swap si/so increased for 3 consecutive samples")
    if has_any_counter_increase(oom_counters):
        reasons.append(f"{OBSERVATION_PATH}: OOM count increased during observation")
    return reasons


def validate_baseline_report(
    report: dict[str, Any],
    label: str,
    *,
    expected_source_commit: str | None,
    expected_image_digest: str | None,
) -> list[str]:
    runner = load_baseline_runner()
    result = runner.evaluate(report)
    reasons = [f"{label}: {reason}" for reason in result.reasons]
    if expected_source_commit and report.get("sourceCommit") != expected_source_commit:
        reasons.append(f"{label}: sourceCommit does not match")
    if expected_image_digest and report.get("imageDigest") != expected_image_digest:
        reasons.append(f"{label}: imageDigest does not match")
    return reasons


def validate_baseline_round_set(reports: list[dict[str, Any]], labels: list[str]) -> list[str]:
    runner = load_baseline_runner()
    result = runner.evaluate_rounds(reports)
    if result.passed:
        return []
    label_text = ",".join(labels) if labels else "baseline rounds"
    return [f"{label_text}: {reason}" for reason in result.reasons]


def sample_health_passed(sample: dict[str, Any]) -> bool:
    health = sample.get("health")
    if isinstance(health, bool):
        health_ok = health is True
    else:
        health_ok = str(health).strip().lower() in {"ok", "healthy", "pass", "passing"}
    if not health_ok:
        return False
    for key in ("restartCount", "ioErrors"):
        try:
            if int(sample.get(key, 0)) != 0:
                return False
        except (TypeError, ValueError):
            return False
    return True


def evaluate_resource_stop_lines(
    sample: dict[str, Any],
    index: int,
    reasons: list[str],
    swap_counters: list[int],
    oom_counters: list[int],
) -> None:
    mem_available = numeric(sample.get("memAvailableBytes"))
    mem_total = numeric(sample.get("memTotalBytes"))
    if mem_total <= 0:
        reasons.append(f"{OBSERVATION_PATH}: sample[{index}] memTotalBytes is required for memory safety line")
    elif mem_available < max(1_073_741_824, int(mem_total * 0.15)):
        reasons.append(f"{OBSERVATION_PATH}: sample[{index}] memory MemAvailable below safety line")
    disk_used = numeric(sample.get("diskUsedPercent"))
    if disk_used >= 85:
        reasons.append(f"{OBSERVATION_PATH}: sample[{index}] disk used percent reached safety line")
    inode_used = numeric(sample.get("inodeUsedPercent"))
    if inode_used >= 85:
        reasons.append(f"{OBSERVATION_PATH}: sample[{index}] inode used percent reached safety line")
    restart_count = numeric(sample.get("restartCount"))
    if restart_count > 0:
        reasons.append(f"{OBSERVATION_PATH}: sample[{index}] restart count increased")
    io_errors = numeric(sample.get("ioErrors"))
    if io_errors > 0:
        reasons.append(f"{OBSERVATION_PATH}: sample[{index}] io error count increased")
    memory_some = psi_avg(sample.get("memoryPSI"), "some")
    if memory_some > 10:
        reasons.append(f"{OBSERVATION_PATH}: sample[{index}] memory PSI some avg10 exceeded safety line")
    io_full = psi_avg(sample.get("ioPSI"), "full")
    if io_full > 5:
        reasons.append(f"{OBSERVATION_PATH}: sample[{index}] io PSI full avg10 exceeded safety line")
    swap_counters.append(int(numeric(sample.get("swapSiSo"))))
    oom_counters.append(int(numeric(sample.get("oomCount"))))


def has_three_consecutive_swap_increases(values: list[int]) -> bool:
    previous: int | None = None
    increases = 0
    for value in values:
        if previous is not None and value > previous:
            increases += 1
        else:
            increases = 0
        if increases >= 3:
            return True
        previous = value
    return False


def has_any_counter_increase(values: list[int]) -> bool:
    previous: int | None = None
    for value in values:
        if previous is not None and value > previous:
            return True
        previous = value
    return False


def numeric(value: Any) -> float:
    try:
        return float(value)
    except (TypeError, ValueError):
        return 0


def psi_avg(value: Any, pressure_type: str) -> float:
    if isinstance(value, dict):
        camel_key = f"{pressure_type}Avg10"
        if camel_key in value:
            return numeric(value.get(camel_key))
        nested = value.get(pressure_type)
        if isinstance(nested, dict):
            return numeric(nested.get("avg10"))
    text = str(value)
    match = re.search(rf"{re.escape(pressure_type)}\s+avg10=([0-9.]+)", text)
    if match:
        return numeric(match.group(1))
    return 0


def percentile_95(values: list[float]) -> float:
    ordered = sorted(values)
    if not ordered:
        return 0
    index = int(0.95 * (len(ordered) - 1))
    value = ordered[index]
    return int(value) if value.is_integer() else value


def stable_sha256(value: Any) -> str:
    payload = json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8")
    return hashlib.sha256(payload).hexdigest()


def manifest_test_names(manifest: list[Any]) -> set[str]:
    names: set[str] = set()
    for entry in manifest:
        if isinstance(entry, str):
            names.add(entry)
        elif isinstance(entry, dict):
            name = str(entry.get("name", "")).strip()
            if name:
                names.add(name)
    return names


def load_baseline_runner():
    path = pathlib.Path(__file__).resolve().parent / "baseline" / "run.py"
    spec = importlib.util.spec_from_file_location("baseline_runner_for_acceptance", path)
    module = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(module)
    return module


def find_baseline_paths(root: pathlib.Path) -> list[pathlib.Path]:
    paths: set[pathlib.Path] = set()
    for pattern in BASELINE_GLOBS:
        paths.update(root.glob(pattern))
    return sorted(paths)


def load_json(path: pathlib.Path) -> dict[str, Any]:
    with path.open("r", encoding="utf-8") as handle:
        value = json.load(handle)
    if not isinstance(value, dict):
        raise ValueError(f"{path} must contain a JSON object")
    return value


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Verify acceptance evidence.")
    parser.add_argument("--root", default=".", type=pathlib.Path)
    mode = parser.add_mutually_exclusive_group(required=True)
    mode.add_argument("--schema-of-present", action="store_true")
    mode.add_argument("--require-complete", action="store_true")
    parser.add_argument("--deployment-run-id")
    parser.add_argument("--cutover-attempt-id")
    parser.add_argument("--source-commit")
    parser.add_argument("--image-digest")
    parser.add_argument("--ci-run-id")
    args = parser.parse_args(argv)
    result = verify_root(
        args.root,
        mode="require-complete" if args.require_complete else "schema-of-present",
        expected_deployment_run_id=args.deployment_run_id,
        expected_cutover_attempt_id=args.cutover_attempt_id,
        expected_source_commit=args.source_commit,
        expected_image_digest=args.image_digest,
        expected_ci_run_id=args.ci_run_id,
    )
    payload = json.dumps(result.to_json(), ensure_ascii=False, sort_keys=True)
    if result.passed:
        print(payload)
        return 0
    print(payload, file=sys.stderr)
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
