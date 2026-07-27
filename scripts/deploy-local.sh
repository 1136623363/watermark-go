#!/usr/bin/env bash
set -Eeuo pipefail

readonly compose_project=watermark-go
readonly compose_file="${COMPOSE_FILE:-deploy/compose.yml}"
readonly runtime_env="${RUNTIME_ENV:-/var/lib/watermark-go/runtime.env}"
readonly role="${DEPLOY_ROLE:-recovery}"
readonly attempt_id="${DEPLOY_ATTEMPT_ID:-deploy-$(date -u +%Y%m%dT%H%M%SZ)}"
readonly support_wait_attempts="${SUPPORT_WAIT_ATTEMPTS:-60}"

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

test -f "$runtime_env" || fail "missing runtime env"

runtime_value() {
  local key="${1:?runtime key required}"
  local default_value="${2:-}"
  local value
  value="$(awk -F= -v key="$key" '$1 == key {print substr($0, length($1) + 2); found = 1; exit} END {if (!found) exit 1}' "$runtime_env" || true)"
  if [ -n "$value" ]; then
    printf '%s\n' "$value"
  else
    printf '%s\n' "$default_value"
  fi
}

case "$role" in
  recovery) readonly smoke_port="$(runtime_value "RECOVERY_API_HOST_PORT" "5001")" ;;
  candidate) readonly smoke_port="$(runtime_value "CANDIDATE_API_HOST_PORT" "15001")" ;;
esac

wait_for_support_services() {
  local attempt
  for attempt in $(seq 1 "$support_wait_attempts"); do
    if docker compose --env-file "$runtime_env" -p "$compose_project" -f "$compose_file" --profile "$role" exec -T mysql \
      sh -c 'mysqladmin ping -h 127.0.0.1 -u"$MYSQL_USER" -p"$MYSQL_PASSWORD" --silent' >/dev/null 2>&1; then
      break
    fi
    if [ "$attempt" = "$support_wait_attempts" ]; then
      fail "mysql not ready"
    fi
    sleep 2
  done

  for attempt in $(seq 1 "$support_wait_attempts"); do
    if [ "$(docker compose --env-file "$runtime_env" -p "$compose_project" -f "$compose_file" --profile "$role" exec -T redis redis-cli ping 2>/dev/null || true)" = "PONG" ]; then
      break
    fi
    if [ "$attempt" = "$support_wait_attempts" ]; then
      fail "redis not ready"
    fi
    sleep 2
  done
}

prepare_gate_receipt_volume() {
  docker compose --env-file "$runtime_env" -p "$compose_project" -f "$compose_file" --profile "$role" run --rm --no-deps \
    --entrypoint /bin/sh --user 0:0 data-gate-"${role}" \
    -c 'set -e; mkdir -p /run/watermark-gate; chown 10001:10001 /run/watermark-gate; chmod 0700 /run/watermark-gate'
}

scripts/preflight.sh
docker compose --env-file "$runtime_env" -p "$compose_project" -f "$compose_file" --profile "$role" pull
docker compose --env-file "$runtime_env" -p "$compose_project" -f "$compose_file" --profile "$role" up -d mysql redis
wait_for_support_services
prepare_gate_receipt_volume
docker compose --env-file "$runtime_env" -p "$compose_project" -f "$compose_file" --profile "$role" up --force-recreate --no-deps data-gate-"${role}"
docker compose --env-file "$runtime_env" -p "$compose_project" -f "$compose_file" --profile "$role" up -d --no-deps "parser-helper-${role}" "egress-proxy-${role}" "api-${role}"
scripts/smoke.sh "http://127.0.0.1:${smoke_port}" "$attempt_id"
printf 'PASS project=%s role=%s attempt=%s\n' "$compose_project" "$role" "$attempt_id"
