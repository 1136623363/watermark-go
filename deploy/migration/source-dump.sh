#!/usr/bin/env bash
set -Eeuo pipefail

if [ "$#" -ne 0 ]; then
  printf 'source-dump accepts no arguments\n' >&2
  exit 64
fi

umask 077
readonly backup_dir=/backup
readonly database=watermark
readonly final_name=source.sql
readonly part="${backup_dir}/source.sql.part"
readonly manifest_part="${backup_dir}/source.sql.sha256.part"
readonly final="${backup_dir}/${final_name}"
readonly manifest="${backup_dir}/source.sql.sha256"

test -d "$backup_dir"
test -f /run/secrets/source.cnf
test -S /run/source/mariadb.sock

mysqldump \
  --defaults-extra-file=/run/secrets/source.cnf \
  --socket=/run/source/mariadb.sock \
  --single-transaction \
  --quick \
  --skip-lock-tables \
  --hex-blob \
  --databases "$database" > "$part"

dump_sha="$(sha256sum "$part" | awk '{print $1}')"
printf '%s  %s\n' "$dump_sha" "$final_name" > "$manifest_part"

sync "$part" "$manifest_part"
mv -f "$part" "$final"
mv -f "$manifest_part" "$manifest"
sync "$final" "$manifest" "$backup_dir"
