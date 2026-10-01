#!/usr/bin/env bash
set -euo pipefail

rate="${1:?usage: run_s02_diagnostic.sh <rate> <duration_seconds> <run_name>}"
duration_seconds="${2:?usage: run_s02_diagnostic.sh <rate> <duration_seconds> <run_name>}"
run_name="${3:?usage: run_s02_diagnostic.sh <rate> <duration_seconds> <run_name>}"

base_dir="$HOME/agora-k6"
result_dir="$base_dir/results/s02/$run_name"
mkdir -p "$result_dir"

k6 run \
  --summary-export "$result_dir/k6_summary.json" \
  -e BASE_URL="http://192.168.86.1:8080" \
  -e RATE="$rate" \
  -e DURATION="${duration_seconds}s" \
  -e PRE_ALLOCATED_VUS=450 \
  -e MAX_VUS=1800 \
  "$base_dir/s02_list_latest_realistic.js" \
  >"$result_dir/k6_console.log" 2>&1 &
k6_pid=$!

(
  printf 'timestamp\tk6_cpu_percent\tk6_rss_kib\tload1\tload5\tload15\tmem_available_kib\n'
  while kill -0 "$k6_pid" 2>/dev/null; do
    timestamp="$(date -Iseconds)"
    proc="$(ps -p "$k6_pid" -o %cpu=,rss= 2>/dev/null | awk '{print $1 "\t" $2}')"
    load="$(awk '{print $1 "\t" $2 "\t" $3}' /proc/loadavg)"
    mem="$(awk '/MemAvailable:/ {print $2}' /proc/meminfo)"
    printf '%s\t%s\t%s\t%s\n' "$timestamp" "$proc" "$load" "$mem"
    sleep 1
  done
) >"$result_dir/vm_metrics_1s.tsv" &
metrics_pid=$!

wait "$k6_pid"
wait "$metrics_pid" || true
