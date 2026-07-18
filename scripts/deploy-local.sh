#!/usr/bin/env bash
set -Eeuo pipefail

readonly compose_project=watermark-go
readonly compose_file="${COMPOSE_FILE:-deploy/compose.yml}"
readonly runtime_env="${RUNTIME_ENV:-/var/lib/watermark-go/runtime.env}"
readonly role="${DEPLOY_ROLE:-recovery}"
readonly attempt_id="${DEPLOY_ATTEMPT_ID:-deploy-$(date -u +%Y%m%dT%H%M%SZ)}"

cleanup() { :; }
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

fail() {
  printf 'FAIL %s\n' "$1" >&2
  exit 1
}

case "$role" in
  recovery|candidate) ;;
  *) fail "role must be recovery or candidate" ;;
esac

scripts/preflight.sh
docker compose --env-file "$runtime_env" -p "$compose_project" -f "$compose_file" --profile "$role" pull
docker compose --env-file "$runtime_env" -p "$compose_project" -f "$compose_file" --profile "$role" up --force-recreate --no-deps data-gate-"${role}"
docker compose --env-file "$runtime_env" -p "$compose_project" -f "$compose_file" --profile "$role" up -d --no-deps "parser-helper-${role}" "egress-proxy-${role}" "api-${role}"
scripts/smoke.sh "http://127.0.0.1:${API_PORT:-5001}" "$attempt_id"
printf 'PASS project=%s role=%s attempt=%s\n' "$compose_project" "$role" "$attempt_id"
