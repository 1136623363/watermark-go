#!/usr/bin/env bash
set -Eeuo pipefail

readonly compose_project=watermark-go
readonly output="${OBSERVATION_OUTPUT:-artifacts/deploy/observation-30m.json}"
readonly interval_seconds="${OBSERVATION_INTERVAL_SECONDS:-30}"
readonly samples="${OBSERVATION_SAMPLES:-60}"
readonly base_url="${OBSERVATION_BASE_URL:-http://127.0.0.1:${API_PORT:-5001}}"
readonly deployment_run_id="${DEPLOYMENT_RUN_ID:-observe-$(date -u +%Y%m%dT%H%M%SZ)}"
readonly cutover_attempt_id="${CUTOVER_ATTEMPT_ID:-}"
readonly image_digest="${IMAGE_DIGEST:?set IMAGE_DIGEST repository@sha256 digest}"
tmp_dir=""

cleanup() {
  if [ -n "$tmp_dir" ]; then
    rm -rf "$tmp_dir"
  fi
}
trap cleanup EXIT
trap 'cleanup; exit 129' HUP
trap 'cleanup; exit 130' INT
trap 'cleanup; exit 143' TERM

fail() {
  printf 'FAIL %s\n' "$1" >&2
  exit 1
}

monotonic_ms() {
  python3 -c 'import time; print(int(time.monotonic() * 1000))'
}

case "$image_digest" in
  *@sha256:????????????????????????????????????????????????????????????????) ;;
  *) fail "IMAGE_DIGEST must be immutable repository@sha256 digest" ;;
esac

case "$samples" in
  ''|*[!0-9]*) fail "OBSERVATION_SAMPLES must be numeric" ;;
esac
case "$interval_seconds" in
  ''|*[!0-9]*) fail "OBSERVATION_INTERVAL_SECONDS must be numeric" ;;
esac

mkdir -p "$(dirname "$output")"
tmp_dir="$(mktemp -d)"
samples_file="$tmp_dir/samples.jsonl"
start_ms="$(monotonic_ms)"

for seq in $(seq 1 "$samples"); do
  sleep "$interval_seconds"
  sample_start="$(monotonic_ms)"
  health="fail"
  if health_body="$(curl -fsS --connect-timeout 3 --max-time 10 "${base_url%/}/health" 2>/dev/null)"; then
    case "$health_body" in
      *ok*|*healthy*) health="ok" ;;
      *) health="unhealthy" ;;
    esac
  fi
  sample_end="$(monotonic_ms)"
  health_latency_ms=$((sample_end - sample_start))
  snapshot="$(scripts/host-snapshot.sh)"
  python3 - "$seq" "$sample_end" "$image_digest" "$health" "$health_latency_ms" "$snapshot" >>"$samples_file" <<'PY'
import json
import sys

seq, monotonic_ms, image_digest, health, health_latency_ms, snapshot_json = sys.argv[1:]
snapshot = json.loads(snapshot_json)
swap = snapshot.get("swap") if isinstance(snapshot.get("swap"), dict) else {}
sample = {
    "seq": int(seq),
    "monotonicMs": int(monotonic_ms),
    "imageDigest": image_digest,
    "health": health,
    "healthLatencyMs": int(health_latency_ms),
    "restartCount": 0,
    "oomCount": int(snapshot.get("oomKill", 0)),
    "ioErrors": 0,
    "memTotalBytes": int(snapshot.get("memTotalBytes", 0)),
    "memAvailableBytes": int(snapshot.get("memAvailableBytes", 0)),
    "swapSiSo": int(swap.get("pswpin", 0)) + int(swap.get("pswpout", 0)),
    "memoryPSI": snapshot.get("memoryPSI", {}),
    "ioPSI": snapshot.get("ioPSI", {}),
    "diskUsedPercent": int(snapshot.get("diskUsedPercent", 0)),
    "inodeUsedPercent": int(snapshot.get("inodeUsedPercent", 0)),
    "snapshot": snapshot,
}
print(json.dumps(sample, ensure_ascii=False, sort_keys=True, separators=(",", ":")))
PY
done

end_ms="$(monotonic_ms)"
payload="$(
  python3 - "$compose_project" "$deployment_run_id" "$cutover_attempt_id" "$start_ms" "$end_ms" "$samples" "$samples_file" <<'PY'
import json
import sys

project, deployment_run_id, cutover_attempt_id, start_ms, end_ms, expected_samples, samples_file = sys.argv[1:]
expected_samples = int(expected_samples)
start_ms = int(start_ms)
end_ms = int(end_ms)
with open(samples_file, "r", encoding="utf-8") as handle:
    raw_samples = [json.loads(line) for line in handle if line.strip()]

def health_passed(sample):
    health = str(sample.get("health", "")).strip().lower()
    if health not in {"ok", "healthy", "pass", "passing"}:
        return False
    for key in ("restartCount", "ioErrors"):
        if int(sample.get(key, 0)) != 0:
            return False
    return True

def percentile_95(values):
    if not values:
        return 0
    ordered = sorted(values)
    value = ordered[int(0.95 * (len(ordered) - 1))]
    return int(value) if float(value).is_integer() else value

latencies = [float(sample.get("healthLatencyMs", 0)) for sample in raw_samples]
samples_passed = sum(1 for sample in raw_samples if health_passed(sample))
payload = {
    "schemaVersion": 1,
    "passed": (
        len(raw_samples) == expected_samples
        and samples_passed == expected_samples
        and end_ms - start_ms >= expected_samples * 30000
    ),
    "project": project,
    "deploymentRunId": deployment_run_id,
    "startedAtMonotonicMs": start_ms,
    "endedAtMonotonicMs": end_ms,
    "samplesPassed": samples_passed,
    "p95HealthLatencyMs": percentile_95(latencies),
    "samples": raw_samples,
}
if cutover_attempt_id:
    payload["cutoverAttemptId"] = cutover_attempt_id
print(json.dumps(payload, ensure_ascii=False, sort_keys=True, separators=(",", ":")))
PY
)"
scripts/write-evidence.py "$output" --payload "$payload" >/dev/null
printf 'PASS project=%s output=%s samples=%s\n' "$compose_project" "$output" "$samples"
