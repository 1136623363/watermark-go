#!/usr/bin/env python3
"""Baseline report evaluator and lightweight offline report helper.

The deploy gates intentionally trust raw per-sample records, not aggregate
fields.  This module is importable by tests and executable for validating a
JSON report before it is promoted into release evidence.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import pathlib
import re
import sys
from typing import Any


REQUIRED_COMPLETED = 93
MIN_SUCCESS = 62
MAX_DURATION_MS = 216_000
REQUIRED_CONCURRENCY = 3
CANONICAL_FIXTURE_SHA256 = "bb0f55ea17ddc613f64282a5786a7ab137a945a847b444fdd2f4bfb212bc5eba"
ROOT = pathlib.Path(__file__).resolve().parents[2]
FIXTURE_PATH = ROOT / "tests" / "baseline" / "fixtures" / "platform-samples.json"
FULL_GIT_SHA_RE = re.compile(r"[a-f0-9]{40}")
IMAGE_DIGEST_RE = re.compile(r".+@sha256:[a-f0-9]{64}")
SHA256_RE = re.compile(r"[a-f0-9]{64}")


class GateResult:
    def __init__(self, passed: bool, reasons: list[str], computed: dict[str, Any] | None = None):
        self.passed = passed
        self.reasons = reasons
        self.computed = computed or {}

    def to_json(self) -> dict[str, Any]:
        return {"passed": self.passed, "reasons": self.reasons, "computed": self.computed}


def evaluate(report: dict[str, Any]) -> GateResult:
    reasons: list[str] = []
    records = report.get("records")
    if not isinstance(records, list):
        return GateResult(False, ["records must be a list"], {})

    if not isinstance(report.get("schemaVersion"), int):
        reasons.append("schemaVersion must be an integer")
    if not str(report.get("runId", "")).strip():
        reasons.append("runId must be non-empty")
    if not FULL_GIT_SHA_RE.fullmatch(str(report.get("sourceCommit", ""))):
        reasons.append("sourceCommit must be a full git SHA")
    if not IMAGE_DIGEST_RE.fullmatch(str(report.get("imageDigest", ""))):
        reasons.append("imageDigest must be repository@sha256")
    if report.get("canonicalFixtureSha256") != CANONICAL_FIXTURE_SHA256:
        reasons.append("canonicalFixtureSha256 does not match approved fixture")
    for flag in ("nativeEnabled", "fallbackEnabled", "cacheBypass"):
        if report.get(flag) is not True:
            reasons.append(f"{flag} must be true")
    if report.get("concurrency") != REQUIRED_CONCURRENCY:
        reasons.append("concurrency must be exactly 3")

    sample_keys: set[str] = set()
    invocation_ids: set[str] = set()
    intervals: list[tuple[int, int]] = []
    recomputed_success = 0
    completed = 0
    recomputed_record_set_sha256 = record_set_sha256(records)
    claimed_record_set_sha256 = str(report.get("recordSetSha256", "")).strip()
    if not SHA256_RE.fullmatch(claimed_record_set_sha256):
        reasons.append("recordSetSha256 must be sha256 hex")
    elif claimed_record_set_sha256 != recomputed_record_set_sha256:
        reasons.append("recordSetSha256 aggregate does not match raw records")
    for index, record in enumerate(records):
        if not isinstance(record, dict):
            reasons.append(f"record[{index}] must be an object")
            continue
        sample_key = str(record.get("sampleKey", "")).strip()
        if not sample_key:
            reasons.append(f"record[{index}] missing sampleKey")
        elif sample_key in sample_keys:
            reasons.append(f"duplicate sampleKey {sample_key}")
        else:
            sample_keys.add(sample_key)

        invocation_id = str(record.get("parserInvocationId", "")).strip()
        if not invocation_id:
            reasons.append(f"record[{index}] missing parserInvocationId")
        elif invocation_id in invocation_ids:
            reasons.append(f"duplicate parserInvocationId {invocation_id}")
        else:
            invocation_ids.add(invocation_id)

        if record.get("status") == "completed":
            completed += 1
        media_success = record.get("mediaSuccess")
        if media_success is None:
            media_success = bool(record.get("mediaUrls"))
        if record.get("ok") is True and media_success is True:
            recomputed_success += 1

        try:
            started = int(record["startedMonotonicMs"])
            ended = int(record["endedMonotonicMs"])
        except (KeyError, TypeError, ValueError):
            reasons.append(f"record[{index}] missing monotonic interval")
            continue
        if ended <= started:
            reasons.append(f"record[{index}] ended before it started")
        else:
            intervals.append((started, ended))

    if len(records) != REQUIRED_COMPLETED:
        reasons.append(f"records length must be {REQUIRED_COMPLETED}")
    if completed != REQUIRED_COMPLETED or report.get("completed") != REQUIRED_COMPLETED:
        reasons.append("completed must be recomputed as 93")
    if recomputed_success < MIN_SUCCESS:
        reasons.append("success must be at least 62 from raw records")
    if report.get("success") != recomputed_success:
        reasons.append("success aggregate does not match raw records")
    canonical_keys = canonical_enabled_sample_keys()
    if len(sample_keys) != REQUIRED_COMPLETED:
        reasons.append("93 unique sampleKey values are required")
    if canonical_keys and sample_keys != canonical_keys:
        reasons.append("sampleKey set must match the canonical enabled fixture exactly")
    if len(invocation_ids) != REQUIRED_COMPLETED:
        reasons.append("93 unique parserInvocationId values are required")

    recomputed_duration = 0
    recomputed_concurrency = 0
    window_start = 0
    window_end = 0
    if intervals:
        window_start = min(start for start, _ in intervals)
        window_end = max(end for _, end in intervals)
        recomputed_duration = window_end - window_start
        recomputed_concurrency = max_observed_concurrency(intervals)
    if recomputed_duration > MAX_DURATION_MS:
        reasons.append(f"durationMs recomputed from records exceeds {MAX_DURATION_MS}")
    if report.get("durationMs") != recomputed_duration:
        reasons.append("durationMs aggregate does not match raw monotonic intervals")
    if recomputed_concurrency != REQUIRED_CONCURRENCY:
        reasons.append("concurrency recomputed from half-open intervals must be exactly 3")
    if report.get("maxObservedConcurrency") != recomputed_concurrency:
        reasons.append("concurrency aggregate does not match raw intervals")

    computed = {
        "completed": completed,
        "success": recomputed_success,
        "durationMs": recomputed_duration,
        "maxObservedConcurrency": recomputed_concurrency,
        "uniqueSampleKeys": len(sample_keys),
        "uniqueParserInvocationIds": len(invocation_ids),
        "recordSetSha256": recomputed_record_set_sha256,
        "windowStartMonotonicMs": window_start,
        "windowEndMonotonicMs": window_end,
    }
    if reasons:
        if report.get("passed") is True:
            reasons.append("self-reported passed=true disagrees with recomputed result")
        return GateResult(False, reasons, computed)
    if report.get("passed") is not True:
        return GateResult(False, ["passed must be true and match recomputed result"], computed)
    return GateResult(True, [], computed)


def evaluate_rounds(reports: list[dict[str, Any]]) -> GateResult:
    """Evaluate the full required three-round baseline evidence set."""
    reasons: list[str] = []
    if not isinstance(reports, list):
        return GateResult(False, ["baseline rounds must be a list"], {})
    if len(reports) != 3:
        reasons.append("baseline requires exactly 3 round reports")

    run_ids: set[str] = set()
    record_hashes: set[str] = set()
    parser_invocations: set[str] = set()
    windows: list[tuple[int, int, str]] = []
    checked_rounds = 0

    for index, report in enumerate(reports):
        label = f"baseline round {index + 1}"
        if not isinstance(report, dict):
            reasons.append(f"{label}: report must be an object")
            continue
        result = evaluate(report)
        checked_rounds += 1
        reasons.extend(f"{label}: {reason}" for reason in result.reasons)

        run_id = str(report.get("runId", "")).strip()
        if run_id:
            if run_id in run_ids:
                reasons.append(f"{label}: duplicate runId {run_id}")
            run_ids.add(run_id)

        record_hash = str(report.get("recordSetSha256", "")).strip()
        if SHA256_RE.fullmatch(record_hash):
            if record_hash in record_hashes:
                reasons.append(f"{label}: duplicate recordSetSha256 {record_hash}")
            record_hashes.add(record_hash)

        records = report.get("records")
        if isinstance(records, list):
            for record_index, record in enumerate(records):
                if not isinstance(record, dict):
                    continue
                invocation_id = str(record.get("parserInvocationId", "")).strip()
                if not invocation_id:
                    continue
                if invocation_id in parser_invocations:
                    reasons.append(
                        f"{label}: parserInvocationId reused across baseline rounds at record[{record_index}]"
                    )
                    break
                parser_invocations.add(invocation_id)

        window_start = result.computed.get("windowStartMonotonicMs")
        window_end = result.computed.get("windowEndMonotonicMs")
        if isinstance(window_start, int) and isinstance(window_end, int) and window_end > window_start:
            windows.append((window_start, window_end, label))

    for (previous_start, previous_end, previous_label), (current_start, current_end, current_label) in zip(
        sorted(windows), sorted(windows)[1:]
    ):
        if current_start < previous_end:
            reasons.append(
                f"baseline time windows must not overlap: {previous_label} [{previous_start},{previous_end}) "
                f"overlaps {current_label} [{current_start},{current_end})"
            )

    computed = {
        "rounds": checked_rounds,
        "uniqueRunIds": len(run_ids),
        "uniqueRecordSetSha256": len(record_hashes),
        "uniqueParserInvocationIdsAcrossRounds": len(parser_invocations),
    }
    return GateResult(not reasons, reasons, computed)


def record_set_sha256(records: list[Any]) -> str:
    payload = json.dumps(records, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8")
    return hashlib.sha256(payload).hexdigest()


def max_observed_concurrency(intervals: list[tuple[int, int]]) -> int:
    events: list[tuple[int, int]] = []
    for started, ended in intervals:
        events.append((started, 1))
        events.append((ended, -1))
    active = 0
    maximum = 0
    for _, delta in sorted(events, key=lambda item: (item[0], item[1])):
        active += delta
        maximum = max(maximum, active)
    return maximum


def canonical_enabled_sample_keys() -> set[str]:
    if not FIXTURE_PATH.exists():
        return set()
    fixture = json.loads(FIXTURE_PATH.read_text(encoding="utf-8"))
    return {str(item["platformKey"]) for item in fixture if item.get("enabled") and item.get("sampleURL")}


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Validate a baseline report from raw records.")
    parser.add_argument("report", type=pathlib.Path)
    args = parser.parse_args(argv)
    report = json.loads(args.report.read_text(encoding="utf-8"))
    result = evaluate(report)
    if result.passed:
        print(json.dumps(result.to_json(), ensure_ascii=False, sort_keys=True))
        return 0
    print(json.dumps(result.to_json(), ensure_ascii=False, sort_keys=True), file=sys.stderr)
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
