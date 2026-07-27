#!/usr/bin/env bash
set -Eeuo pipefail

readonly compose_project=watermark-go
readonly base_url="${1:-http://127.0.0.1:5001}"
readonly attempt_id="${2:-smoke-local}"

cleanup() { :; }
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

fail() {
  printf 'FAIL %s\n' "$1" >&2
  exit 1
}

health_body="$(curl -fsS --connect-timeout 3 --max-time 10 "${base_url%/}/healthz")" || fail "health endpoint failed"
case "$health_body" in
  *ok*|*healthy*) ;;
  *) fail "health body did not report ok" ;;
esac
printf 'PASS project=%s attempt=%s\n' "$compose_project" "$attempt_id"
