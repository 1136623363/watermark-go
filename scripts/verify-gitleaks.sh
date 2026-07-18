#!/usr/bin/env bash
set -euo pipefail

GITLEAKS_VERSION="8.30.1"
GITLEAKS_ARCHIVE_SHA256="551f6fc83ea457d62a0d98237cbad105af8d557003051f41f3e7ca7b3f2470eb"

tmpdir="$(mktemp -d)"
cleanup() {
  rm -rf "$tmpdir"
}
trap cleanup EXIT HUP INT TERM
chmod 700 "$tmpdir"
umask 077

archive="$tmpdir/gitleaks.tar.gz"
checksums="$tmpdir/checksums.txt"
scan_log="$tmpdir/scan.log"
url="https://github.com/gitleaks/gitleaks/releases/download/v${GITLEAKS_VERSION}/gitleaks_${GITLEAKS_VERSION}_linux_x64.tar.gz"

: > "$scan_log"
chmod 600 "$scan_log"

verify_all() {
  curl --fail --show-error --silent --location \
    --proto '=https' --proto-redir '=https' \
    --max-redirs 5 --connect-timeout 10 --max-time 120 \
    --output "$archive" "$url" || return $?
  curl --fail --show-error --silent --location \
    --proto '=https' --proto-redir '=https' \
    --max-redirs 5 --connect-timeout 10 --max-time 120 \
    --output "$checksums" "https://github.com/gitleaks/gitleaks/releases/download/v${GITLEAKS_VERSION}/gitleaks_${GITLEAKS_VERSION}_checksums.txt" || return $?
  grep -F "${GITLEAKS_ARCHIVE_SHA256}  gitleaks_${GITLEAKS_VERSION}_linux_x64.tar.gz" "$checksums" >/dev/null || return $?
  printf '%s  %s\n' "$GITLEAKS_ARCHIVE_SHA256" "$archive" | sha256sum -c - >/dev/null || return $?
  tar -xzf "$archive" -C "$tmpdir" gitleaks >/dev/null || return $?
  chmod 700 "$tmpdir/gitleaks" || return $?
  "$tmpdir/gitleaks" git --no-banner --redact --log-opts=--all . || return $?
}

if verify_all >"$scan_log" 2>&1; then
  printf 'PASS\n'
else
  status=$?
  printf 'FAIL exit=%d\n' "$status" >&2
  exit "$status"
fi
