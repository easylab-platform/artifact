#!/usr/bin/env bash
# Shared bounded-parallelism driver for the e2e matrices. Sourced, not executed.
#
# run_pool <fn> <proto...> runs "<fn> <proto>" for every proto, at most JOBS at
# a time. Each worker's stdout/stderr is captured to its own log file; the
# worker must write its single-line result (PASS/FAIL/SKIP <proto> ...) to
# "$RESULT_DIR/<proto>". When all workers finish the logs are printed in
# protocol order (deterministic, unlike interleaved serial output) and the
# results are aggregated into one summary. Returns non-zero if any FAIL.
#
# Workers must be self-contained: they own their pod/ConfigMap names and must
# not touch shared shell state (each runs in its own subshell).
#
# Env:
#   JOBS        max concurrent workers (default 6; JOBS=1 is serial)
#   LOG_DIR     override the per-worker log directory
#   RESULT_DIR  override the per-worker result directory

# emit_result <proto> <line> records a worker's verdict for the summary.
emit_result() {
  printf '%s\n' "$2" >"${RESULT_DIR:?RESULT_DIR not set}/$1"
}

run_pool() {
  local fn="$1"; shift
  local protos=("$@")
  local jobs="${JOBS:-6}"
  case "$jobs" in
    ''|*[!0-9]*) jobs=6 ;;
  esac
  [ "$jobs" -ge 1 ] || jobs=1

  local base
  base="$(mktemp -d "${TMPDIR:-/tmp}/e2e-pool.XXXXXX")"
  LOG_DIR="${LOG_DIR:-$base/logs}"
  RESULT_DIR="${RESULT_DIR:-$base/results}"
  mkdir -p "$LOG_DIR" "$RESULT_DIR"

  local p
  for p in "${protos[@]}"; do
    : >"$LOG_DIR/$p.log"
    "$fn" "$p" >"$LOG_DIR/$p.log" 2>&1 &
    # Throttle: block until a slot frees up. `jobs -rp` works without job
    # control in scripts; `wait -n` reaps exactly one finished worker.
    while [ "$(jobs -rp | wc -l)" -ge "$jobs" ]; do
      wait -n 2>/dev/null || true
    done
  done
  wait

  local pass=0 fail=0 skip=0 line
  local results=()
  for p in "${protos[@]}"; do
    echo "===== $p ====="
    sed 's/^/  /' "$LOG_DIR/$p.log" 2>/dev/null
    line="$(cat "$RESULT_DIR/$p" 2>/dev/null || true)"
    [ -n "$line" ] || line="FAIL $p (no result)"
    results+=("$line")
    case "$line" in
      PASS*) pass=$((pass+1)) ;;
      SKIP*) skip=$((skip+1)) ;;
      *)     fail=$((fail+1)) ;;
    esac
  done

  echo "====================================="
  printf '%s\n' "${results[@]}"
  echo "TOTAL: $pass pass, $fail fail${skip:+, $skip skip}"
  [ "$fail" -eq 0 ]
}
