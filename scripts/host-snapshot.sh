#!/usr/bin/env bash
set -Eeuo pipefail

readonly compose_project=watermark-go

cleanup() { :; }
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

mem_total="$(awk '/MemTotal:/ {print $2 * 1024}' /proc/meminfo)"
mem_available="$(awk '/MemAvailable:/ {print $2 * 1024}' /proc/meminfo)"
swap_si="$(awk '/pswpin/ {print $2}' /proc/vmstat)"
swap_so="$(awk '/pswpout/ {print $2}' /proc/vmstat)"
oom_kill="$(awk '/oom_kill/ {print $2}' /proc/vmstat 2>/dev/null || printf '0')"
memory_psi="$(tr '\n' ';' </proc/pressure/memory 2>/dev/null || printf 'unavailable')"
io_psi="$(tr '\n' ';' </proc/pressure/io 2>/dev/null || printf 'unavailable')"
disk_used="$(df -P /var/lib/watermark-go 2>/dev/null | awk 'NR==2 {print $5}' | tr -d '%' || printf '0')"
inode_used="$(df -Pi /var/lib/watermark-go 2>/dev/null | awk 'NR==2 {print $5}' | tr -d '%' || printf '0')"

printf '{"schemaVersion":1,"passed":true,"project":"%s","memTotalBytes":%s,"memAvailableBytes":%s,"swap":{"pswpin":%s,"pswpout":%s},"oomKill":%s,"memoryPSI":%s,"ioPSI":%s,"diskUsedPercent":%s,"inodeUsedPercent":%s}\n' \
  "$compose_project" "$mem_total" "$mem_available" "$swap_si" "$swap_so" "$oom_kill" \
  "$(printf '%s' "$memory_psi" | python3 -c 'import json,sys; print(json.dumps(sys.stdin.read()))')" \
  "$(printf '%s' "$io_psi" | python3 -c 'import json,sys; print(json.dumps(sys.stdin.read()))')" \
  "${disk_used:-0}" "${inode_used:-0}"
