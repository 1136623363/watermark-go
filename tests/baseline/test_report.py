import importlib.util
import copy
import hashlib
import json
import pathlib


ROOT = pathlib.Path(__file__).resolve().parents[2]
RUNNER_PATH = ROOT / "scripts" / "baseline" / "run.py"
CANONICAL_FIXTURE_SHA256 = "bb0f55ea17ddc613f64282a5786a7ab137a945a847b444fdd2f4bfb212bc5eba"
FIXTURE_PATH = ROOT / "tests" / "baseline" / "fixtures" / "platform-samples.json"


def load_runner():
    spec = importlib.util.spec_from_file_location("baseline_runner", RUNNER_PATH)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def canonical_full_report(**overrides):
    fixture = json.loads(FIXTURE_PATH.read_text(encoding="utf-8"))
    sample_keys = [item["platformKey"] for item in fixture if item["enabled"] and item["sampleURL"]]
    assert len(sample_keys) == 93
    records = []
    time_offset = overrides.get("time_offset", 0)
    invocation_prefix = overrides.get("invocation_prefix", "parse")
    for index, sample_key in enumerate(sample_keys):
        group = index // 3
        started = time_offset + group * 7000
        ended = started + 6000
        records.append(
            {
                "sampleKey": sample_key,
                "status": "completed",
                "ok": index < overrides.get("success", 62),
                "mediaSuccess": index < overrides.get("success", 62),
                "mediaUrls": [f"https://cdn.example/{index}.mp4"] if index < overrides.get("success", 62) else [],
                "startedMonotonicMs": started,
                "endedMonotonicMs": ended,
                "parserInvocationId": f"{invocation_prefix}-{index:02d}",
            }
        )
    record_set_sha256 = hashlib.sha256(
        json.dumps(records, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8")
    ).hexdigest()
    report = {
        "schemaVersion": 1,
        "passed": True,
        "runId": overrides.get("run_id", "baseline-run-a"),
        "sourceCommit": overrides.get("source_commit", "f" * 40),
        "imageDigest": overrides.get("image_digest", "ghcr.io/1136623363/watermark-go@sha256:" + "a" * 64),
        "canonicalFixtureSha256": CANONICAL_FIXTURE_SHA256,
        "records": records,
        "recordSetSha256": overrides.get("record_set_sha256", record_set_sha256),
        "completed": overrides.get("records", 93),
        "success": overrides.get("success", 62),
        "durationMs": overrides.get("wall_clock_ms", 216000),
        "concurrency": 3,
        "maxObservedConcurrency": overrides.get("max_observed_concurrency", 3),
        "nativeEnabled": True,
        "fallbackEnabled": True,
        "cacheBypass": True,
    }
    return report


def test_gate_recomputes_canonical_baseline():
    runner = load_runner()
    report = canonical_full_report(records=93, success=62, wall_clock_ms=216000, max_observed_concurrency=3)
    assert runner.evaluate(report).passed is True

    report["records"][-1]["endedMonotonicMs"] += 1
    report["durationMs"] = 216000
    result = runner.evaluate(report)
    assert result.passed is False
    assert "durationMs" in " ".join(result.reasons)


def test_gate_rejects_aggregate_lies_and_record_reuse():
    runner = load_runner()
    report = canonical_full_report(records=93, success=62, wall_clock_ms=216000, max_observed_concurrency=3)
    report["records"][1]["parserInvocationId"] = report["records"][0]["parserInvocationId"]
    report["success"] = 93
    report["passed"] = True
    result = runner.evaluate(report)
    assert result.passed is False
    assert "parserInvocationId" in " ".join(result.reasons)


def test_gate_recomputes_concurrency_half_open_intervals():
    runner = load_runner()
    report = canonical_full_report(records=93, success=62, wall_clock_ms=216000, max_observed_concurrency=3)
    report["records"][3]["startedMonotonicMs"] = report["records"][0]["startedMonotonicMs"]
    report["records"][3]["endedMonotonicMs"] = report["records"][0]["endedMonotonicMs"]
    report["maxObservedConcurrency"] = 3
    result = runner.evaluate(report)
    assert result.passed is False
    assert "concurrency" in " ".join(result.reasons)


def test_gate_requires_runtime_flags_and_exact_93_completed():
    runner = load_runner()
    for key, value in {
        "nativeEnabled": False,
        "fallbackEnabled": False,
        "cacheBypass": False,
        "completed": 92,
        "concurrency": 2,
    }.items():
        report = canonical_full_report(records=93, success=62, wall_clock_ms=216000, max_observed_concurrency=3)
        report[key] = value
        result = runner.evaluate(report)
        assert result.passed is False, key


def test_gate_requires_report_identity_metadata():
    runner = load_runner()
    invalid_cases = {
        "schemaVersion": "1",
        "runId": "",
        "sourceCommit": "short-sha",
        "imageDigest": "ghcr.io/1136623363/watermark-go:latest",
        "recordSetSha256": "not-a-sha256",
    }
    for key, value in invalid_cases.items():
        report = canonical_full_report()
        report[key] = value
        result = runner.evaluate(report)
        assert result.passed is False, key
        assert key in " ".join(result.reasons), result.reasons


def test_gate_rejects_serial_execution_even_when_aggregate_claims_three():
    runner = load_runner()
    report = canonical_full_report()
    for index, record in enumerate(report["records"]):
        record["startedMonotonicMs"] = index * 1000
        record["endedMonotonicMs"] = index * 1000 + 500
    report["durationMs"] = 92500
    report["maxObservedConcurrency"] = 3

    result = runner.evaluate(report)

    assert result.passed is False
    assert "concurrency" in " ".join(result.reasons)


def test_three_round_gate_requires_exactly_three_independent_runs():
    runner = load_runner()
    rounds = [
        canonical_full_report(run_id=f"baseline-run-{index}", time_offset=index * 300000, invocation_prefix=f"run{index}")
        for index in range(3)
    ]
    assert runner.evaluate_rounds(rounds).passed is True

    result = runner.evaluate_rounds(rounds[:2])
    assert result.passed is False
    assert "exactly 3" in " ".join(result.reasons)

    duplicate_run_id = [copy.deepcopy(report) for report in rounds]
    duplicate_run_id[1]["runId"] = duplicate_run_id[0]["runId"]
    result = runner.evaluate_rounds(duplicate_run_id)
    assert result.passed is False
    assert "runId" in " ".join(result.reasons)

    overlapping_window = [copy.deepcopy(report) for report in rounds]
    overlapping_window[1] = canonical_full_report(run_id="baseline-run-overlap", time_offset=1000, invocation_prefix="overlap")
    result = runner.evaluate_rounds(overlapping_window)
    assert result.passed is False
    assert "time windows" in " ".join(result.reasons)

    reused_invocations = [
        canonical_full_report(run_id="baseline-run-1", time_offset=0, invocation_prefix="reused"),
        canonical_full_report(run_id="baseline-run-2", time_offset=300000, invocation_prefix="reused"),
        canonical_full_report(run_id="baseline-run-3", time_offset=600000, invocation_prefix="unique"),
    ]
    result = runner.evaluate_rounds(reused_invocations)
    assert result.passed is False
    assert "parserInvocationId" in " ".join(result.reasons)
