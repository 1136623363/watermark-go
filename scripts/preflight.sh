#!/usr/bin/env bash
set -Eeuo pipefail

readonly compose_project=watermark-go
readonly compose_file="${COMPOSE_FILE:-deploy/compose.yml}"
readonly runtime_env="${RUNTIME_ENV:-/var/lib/watermark-go/runtime.env}"
readonly role="${DEPLOY_ROLE:-recovery}"

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
case "$role" in
  recovery|candidate) ;;
  *) fail "role must be recovery or candidate" ;;
esac
mode="$(stat -c '%a' "$runtime_env")"
test "$mode" = "600" || fail "runtime env must be 0600"
grep -nE '(^|[[:space:]])build:' "$compose_file" && fail "compose contains disallowed local image creation"

rendered_dir="$(mktemp -d)"
trap 'rm -rf "$rendered_dir"' EXIT HUP INT TERM
rendered="$rendered_dir/compose.rendered.json"
docker compose --env-file "$runtime_env" -p "$compose_project" -f "$compose_file" --profile "$role" config --format json > "$rendered"
python3 - "$rendered" <<'PY' || fail "rendered compose policy rejected"
import json
import sys

document = json.load(open(sys.argv[1], encoding="utf-8"))
allowed_published = {"5001", "15001"}
allowed_seen = False
for name, service in document.get("services", {}).items():
    if "build" in service:
        raise SystemExit(f"{name}: build is disallowed")
    for port in service.get("ports", []) or []:
        host_ip = str(port.get("host_ip", ""))
        published = str(port.get("published", ""))
        target = str(port.get("target", ""))
        protocol = str(port.get("protocol", "tcp"))
        if host_ip != "127.0.0.1" or target != "5001" or published not in allowed_published or protocol != "tcp":
            raise SystemExit(f"{name}: host bind is outside allowlist")
        allowed_seen = True
if not allowed_seen:
    raise SystemExit("missing allowed API bind")
PY
printf 'PASS project=%s\n' "$compose_project"
