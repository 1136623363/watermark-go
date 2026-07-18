#!/usr/bin/env bash
set -Eeuo pipefail

if [ "$#" -ne 0 ]; then
  printf 'restore-clone accepts no arguments\n' >&2
  exit 64
fi

umask 077
readonly backup_dir=/backup
readonly dump_file="${backup_dir}/source.sql"
readonly manifest_file="${backup_dir}/source.sql.sha256"
readonly receipt_part="${backup_dir}/restore.passed.part"
readonly receipt="${backup_dir}/restore.passed"

test -d "$backup_dir"
test -f /run/secrets/clone.cnf
test -f "$dump_file"
test -f "$manifest_file"

(
  cd "$backup_dir"
  sha256sum -c source.sql.sha256
)

mysql --defaults-extra-file=/run/secrets/clone.cnf --host=mariadb-clone < "$dump_file"

dump_sha="$(sha256sum "$dump_file" | awk '{print $1}')"
printf '{"status":"passed","sourceDumpSha256":"%s"}\n' "$dump_sha" > "$receipt_part"
sync "$receipt_part"
mv -f "$receipt_part" "$receipt"
sync "$receipt" "$backup_dir"
