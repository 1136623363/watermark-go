#!/usr/bin/env bash
set -Eeuo pipefail

readonly compose_project=watermark-go
readonly run_id="${PROMOTION_RUN_ID:?set promotion run id}"
readonly recovery_digest="${RECOVERY_IMAGE:?set verified recovery digest}"
readonly marker_path="release/promotion-marker.txt"
readonly evidence_ref="refs/evidence/recovery-${run_id}"
tmp_dir=""
tmp_index=""

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

scripts/verify-acceptance.py --schema-of-present --deployment-run-id "$run_id" >/dev/null || fail "recovery-ready evidence failed"
case "$recovery_digest" in
  *@sha256:????????????????????????????????????????????????????????????????) ;;
  *) fail "RECOVERY_IMAGE must be immutable digest" ;;
esac

payload_hash="$(git rev-parse HEAD:artifacts 2>/dev/null || git rev-parse 'HEAD^{tree}')"
tmp_dir="$(mktemp -d)"
tmp_index="$tmp_dir/git-index"
promotion_json="$tmp_dir/promotion-evidence.json"
promotion_payload="$(
  python3 - "$run_id" "$recovery_digest" "$payload_hash" "$compose_project" <<'PY'
import json
import sys

run_id, recovery_digest, payload_hash, project = sys.argv[1:]
print(json.dumps({
    "schemaVersion": 1,
    "passed": True,
    "deploymentRunId": run_id,
    "role": "recovery",
    "recoveryImage": recovery_digest,
    "payloadTree": payload_hash,
    "project": project,
}, ensure_ascii=False, sort_keys=True, separators=(",", ":")))
PY
)"
scripts/write-evidence.py "$promotion_json" --payload "$promotion_payload" >/dev/null
GIT_INDEX_FILE="$tmp_index" git read-tree --empty
promotion_blob="$(GIT_INDEX_FILE="$tmp_index" git hash-object -w "$promotion_json")"
GIT_INDEX_FILE="$tmp_index" git update-index --add --cacheinfo "100644,$promotion_blob,evidence.json"
evidence_tree="$(GIT_INDEX_FILE="$tmp_index" git write-tree)"
evidence_commit="$(printf 'recovery evidence %s\n' "$run_id" | git commit-tree "$evidence_tree" -p HEAD)"
git update-ref "$evidence_ref" "$evidence_commit"

marker_payload="$(
  python3 - "$run_id" "$recovery_digest" "$evidence_commit" "$payload_hash" "$evidence_ref" "$compose_project" <<'PY'
import json
import sys

run_id, recovery_digest, evidence_commit, payload_hash, evidence_ref, project = sys.argv[1:]
print(json.dumps({
    "schemaVersion": 1,
    "passed": True,
    "deploymentRunId": run_id,
    "role": "recovery",
    "recoveryImage": recovery_digest,
    "evidenceCommit": evidence_commit,
    "evidenceRef": evidence_ref,
    "payloadTree": payload_hash,
    "project": project,
}, ensure_ascii=False, sort_keys=True, separators=(",", ":")))
PY
)"
scripts/write-evidence.py "$marker_path" --payload "$marker_payload" >/dev/null
printf 'PASS project=%s evidenceRef=%s\n' "$compose_project" "$evidence_ref"
