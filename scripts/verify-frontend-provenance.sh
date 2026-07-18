#!/usr/bin/env bash
set -euo pipefail

expected_commit="5d72c4925017676b6183b907dfe11ec60a4885bf"
expected_tree="03c72a16532f51db76203967a3b982d49d4909d1"
expected_manifest="3e3f172b90439252e3601892e15fef2d398747ac9630fbc148013304d8c776f8"

repo="${FRONTEND_REPO:-/srv/watermark}"
if [[ ! -d "$repo/.git" ]]; then
  echo "FRONTEND_REPO is not a git repository: $repo" >&2
  exit 1
fi

status="$(git -C "$repo" status --short)"
if [[ -n "$status" ]]; then
  echo "frontend repository is not clean:" >&2
  echo "$status" >&2
  exit 1
fi

commit="$(git -C "$repo" rev-parse HEAD)"
if [[ "$commit" != "$expected_commit" ]]; then
  echo "frontend commit mismatch: got $commit want $expected_commit" >&2
  exit 1
fi

tree="$(git -C "$repo" rev-parse 'HEAD^{tree}')"
if [[ "$tree" != "$expected_tree" ]]; then
  echo "frontend tree mismatch: got $tree want $expected_tree" >&2
  exit 1
fi

manifest="$(
  cd "$repo"
  git ls-files -z -- miniprogram test project.config.json | xargs -0 sha256sum | sha256sum | awk '{print $1}'
)"
if [[ "$manifest" != "$expected_manifest" ]]; then
  echo "frontend tracked manifest mismatch: got $manifest want $expected_manifest" >&2
  exit 1
fi

echo "frontend provenance ok: $commit"
