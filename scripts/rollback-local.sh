#!/usr/bin/env bash
set -Eeuo pipefail

readonly compose_project=watermark-go
readonly compose_file="${COMPOSE_FILE:-deploy/compose.yml}"
readonly runtime_env="${RUNTIME_ENV:-/var/lib/watermark-go/runtime.env}"
readonly state_file="${STATE_FILE:-artifacts/deploy/state-before.json}"
readonly rollbackMode=absent_two_stage
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

test -f "$state_file" || fail "missing state file"
test -n "${RECOVERY_IMAGE:-}" || fail "RECOVERY_IMAGE must point at verified A digest"
case "$RECOVERY_IMAGE" in
  *@sha256:????????????????????????????????????????????????????????????????) ;;
  *) fail "RECOVERY_IMAGE must be immutable digest" ;;
esac

wait_for_support_services() {
  local attempt
  for attempt in $(seq 1 "$support_wait_attempts"); do
    if docker compose --env-file "$runtime_env" -p "$compose_project" -f "$compose_file" --profile recovery exec -T mysql \
      sh -c 'mysqladmin ping -h 127.0.0.1 -u"$MYSQL_USER" -p"$MYSQL_PASSWORD" --silent' >/dev/null 2>&1; then
      break
    fi
    if [ "$attempt" = "$support_wait_attempts" ]; then
      fail "mysql not ready"
    fi
    sleep 2
  done

  for attempt in $(seq 1 "$support_wait_attempts"); do
    if [ "$(docker compose --env-file "$runtime_env" -p "$compose_project" -f "$compose_file" --profile recovery exec -T redis redis-cli ping 2>/dev/null || true)" = "PONG" ]; then
      break
    fi
    if [ "$attempt" = "$support_wait_attempts" ]; then
      fail "redis not ready"
    fi
    sleep 2
  done
}

prepare_gate_receipt_volume() {
  docker compose --env-file "$runtime_env" -p "$compose_project" -f "$compose_file" --profile recovery run --rm --no-deps \
    --entrypoint /bin/sh --user 0:0 data-gate-recovery \
    -c 'set -e; mkdir -p /run/watermark-gate; chown 10001:10001 /run/watermark-gate; chmod 0700 /run/watermark-gate'
}

scripts/preflight.sh
docker compose --env-file "$runtime_env" -p "$compose_project" -f "$compose_file" --profile candidate stop api-candidate parser-helper-candidate egress-proxy-candidate || true
docker compose --env-file "$runtime_env" -p "$compose_project" -f "$compose_file" --profile recovery pull
docker compose --env-file "$runtime_env" -p "$compose_project" -f "$compose_file" --profile recovery up -d mysql redis
wait_for_support_services
prepare_gate_receipt_volume
docker compose --env-file "$runtime_env" -p "$compose_project" -f "$compose_file" --profile recovery up --force-recreate --no-deps data-gate-recovery
docker compose --env-file "$runtime_env" -p "$compose_project" -f "$compose_file" --profile recovery up -d --no-deps parser-helper-recovery egress-proxy-recovery api-recovery
printf 'PASS rollbackMode=%s project=%s\n' "$rollbackMode" "$compose_project"
