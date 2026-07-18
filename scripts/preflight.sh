#!/usr/bin/env bash
set -Eeuo pipefail

readonly compose_project=watermark-go
readonly compose_file="${COMPOSE_FILE:-deploy/compose.yml}"
readonly runtime_env="${RUNTIME_ENV:-/var/lib/watermark-go/runtime.env}"

cleanup() { :; }
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

fail() {
  printf 'FAIL %s\n' "$1" >&2
  exit 1
}

test -f "$compose_file" || fail "missing compose file"
test -f "$runtime_env" || fail "missing runtime env"
mode="$(stat -c '%a' "$runtime_env")"
test "$mode" = "600" || fail "runtime env must be 0600"
grep -nE '(^|[[:space:]])build:' "$compose_file" && fail "compose contains disallowed local image creation"

rendered_dir="$(mktemp -d)"
trap 'rm -rf "$rendered_dir"' EXIT HUP INT TERM
rendered="$rendered_dir/compose.rendered.yml"
docker compose --env-file "$runtime_env" -p "$compose_project" -f "$compose_file" config > "$rendered"
grep -nE '(^|[[:space:]])build:' "$rendered" && fail "rendered compose contains disallowed local image creation"

if grep -nE '0\.0\.0\.0:|:80:|:443:|:15002:|:5002:' "$rendered"; then
  fail "rendered compose has a host bind outside 127.0.0.1:5001 or 127.0.0.1:15001"
fi
grep -q '127.0.0.1:5001:5001\|127.0.0.1:15001:5001' "$rendered" || fail "missing allowed API bind"
printf 'PASS project=%s\n' "$compose_project"
