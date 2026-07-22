#!/usr/bin/env bash
set -Eeuo pipefail

readonly compose_project=watermark-go
readonly compose_file="${COMPOSE_FILE:-deploy/compose.yml}"
readonly runtime_env="${RUNTIME_ENV:-/var/lib/watermark-go/runtime.env}"
readonly state_file="${STATE_FILE:-artifacts/deploy/state-before.json}"
readonly rollbackMode=absent_two_stage

cleanup() { :; }
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

fail() {
  printf 'FAIL %s\n' "$1" >&2
  exit 1
}

test -f "$state_file" || fail "missing state file"
test -n "${RECOVERY_IMAGE:-}" || fail "RECOVERY_IMAGE must point at verified A digest"
case "$RECOVERY_IMAGE" in
  *@sha256:????????????????????????????????????????????????????????????????) ;;
  *) fail "RECOVERY_IMAGE must be immutable digest" ;;
esac

scripts/preflight.sh
docker compose --env-file "$runtime_env" -p "$compose_project" -f "$compose_file" --profile candidate stop api-candidate parser-helper-candidate egress-proxy-candidate || true
docker compose --env-file "$runtime_env" -p "$compose_project" -f "$compose_file" --profile recovery pull
docker compose --env-file "$runtime_env" -p "$compose_project" -f "$compose_file" --profile recovery up -d mysql redis
docker compose --env-file "$runtime_env" -p "$compose_project" -f "$compose_file" --profile recovery up --force-recreate --no-deps data-gate-recovery
docker compose --env-file "$runtime_env" -p "$compose_project" -f "$compose_file" --profile recovery up -d --no-deps parser-helper-recovery egress-proxy-recovery api-recovery
printf 'PASS rollbackMode=%s project=%s\n' "$rollbackMode" "$compose_project"
