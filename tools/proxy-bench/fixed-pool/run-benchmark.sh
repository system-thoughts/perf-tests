#!/bin/sh
# Only manage this worker's child process. No Kubernetes API or remote control.
set -u
SCRIPT_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd) || exit 3
work_dir=$(mktemp -d "${TMPDIR:-/tmp}/proxy-bench-fixed-pool.XXXXXXXX") || exit 3
trap 'rm -rf -- "$work_dir"' 0
WORKER_SPEC=$work_dir/worker-spec.json
export WORKER_SPEC
interrupted=0
child=''
forward_signal() {
  interrupted=1
  if [ -n "$child" ]; then kill -TERM "$child" 2>/dev/null || :; fi
}
wait_child() {
  wait "$child"
  child_code=$?
  # A trapped signal can interrupt wait while the child is still alive.
  if [ "$interrupted" -eq 1 ]; then
    wait "$child"
    next_code=$?
    if [ "$next_code" -ne 127 ]; then child_code=$next_code; fi
  fi
}
trap forward_signal INT TERM
proxy-bench-worker-support --output "$WORKER_SPEC" &
child=$!
[ "$interrupted" -eq 0 ] || forward_signal
wait_child
prepare_code=$child_code
child=''
[ "$interrupted" -eq 0 ] || exit 130
[ "$prepare_code" -eq 0 ] || exit "$prepare_code"

# Derive the proxy from the selected successful preparation, without eval.
IFS= read -r HTTP_PROXY < "$work_dir/proxy" || exit 3
# Go/k6's proxy transport must see the real environment, without bypasses.
unset http_proxy https_proxy HTTPS_PROXY all_proxy ALL_PROXY no_proxy NO_PROXY
export HTTP_PROXY
K6_NO_USAGE_REPORT=true
export K6_NO_USAGE_REPORT
started_at=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
[ "$interrupted" -eq 0 ] || exit 130
# Use native k6 option precedence; handleSummary records effective test options.
if [ -n "${RANGE:-}" ]; then
  printf 'fixed-pool: RANGE is ignored; the actual Range size comes from PLAN (see startup inputs).\n' >&2
fi
k6 run --no-color "$SCRIPT_DIR/benchmark.js" "$@" &
child=$!
[ "$interrupted" -eq 0 ] || forward_signal
wait_child
k6_code=$child_code
child=''
ended_at=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
interrupted_json=false
[ "$interrupted" -eq 0 ] || interrupted_json=true
# Only fixed-format timestamps, numbers and a boolean are interpolated.
# Free-form text requires a JSON encoder.
printf 'PROXY_BENCH_FIXED_POOL_WORKER_EXIT_V1 {"process_started_at":"%s","process_ended_at":"%s","exit_code":%s,"interrupted":%s}\n' \
  "$started_at" "$ended_at" "$k6_code" "$interrupted_json"
[ "$interrupted" -eq 0 ] || exit 130
exit "$k6_code"
