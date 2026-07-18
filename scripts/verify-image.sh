#!/usr/bin/env bash
set -Eeuo pipefail

readonly compose_project=watermark-go
readonly image_ref="${1:?image repository@sha256 digest required}"
readonly expected_source="${EXPECTED_SOURCE:-https://github.com/1136623363/watermark-go}"
readonly expected_revision="${EXPECTED_REVISION:-}"

cleanup() { :; }
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

fail() {
  printf 'FAIL %s\n' "$1" >&2
  exit 1
}

case "$image_ref" in
  *@sha256:????????????????????????????????????????????????????????????????) ;;
  *) fail "image must be repository@sha256 digest" ;;
esac

metadata="$(docker buildx imagetools inspect "$image_ref" --format '{{json .}}')" || fail "inspect image metadata failed"
printf '%s' "$metadata" | grep -q "$expected_source" || fail "source label missing"
if [ -n "$expected_revision" ]; then
  printf '%s' "$metadata" | grep -q "$expected_revision" || fail "revision label missing"
fi
printf 'PASS project=%s image=%s\n' "$compose_project" "$image_ref"
