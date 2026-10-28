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
proxy-bench-worker-support prepare-input --script-dir "$SCRIPT_DIR" --output "$WORKER_SPEC" &
child=$!
[ "$interrupted" -eq 0 ] || forward_signal
wait_child
prepare_code=$child_code
child=''
[ "$interrupted" -eq 0 ] || exit 130
[ "$prepare_code" -eq 0 ] || exit "$prepare_code"

# Derive the proxy from the validated target configuration, without eval.
IFS= read -r HTTP_PROXY < "$work_dir/proxy" || exit 3
# Go/k6's proxy transport must see the real environment, without bypasses.
unset http_proxy https_proxy HTTPS_PROXY all_proxy ALL_PROXY no_proxy NO_PROXY
export HTTP_PROXY
K6_NO_USAGE_REPORT=true
export K6_NO_USAGE_REPORT
started_at=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
[ "$interrupted" -eq 0 ] || exit 130
# Use native k6 option precedence; handleSummary records effective test options.
k6 run --no-color "$SCRIPT_DIR/benchmark.js" &
child=$!
[ "$interrupted" -eq 0 ] || forward_signal
wait_child
k6_code=$child_code
child=''
ended_at=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
# ReadEnvironment already validated this decimal; normalize leading zeroes for JSON.
worker_id=${WORKER_ID:-0}
while [ "${worker_id#0}" != "$worker_id" ]; do worker_id=${worker_id#0}; done
worker_id=${worker_id:-0}
reason=completed
[ "$k6_code" -eq 0 ] || reason=failed
if [ "$k6_code" -eq 126 ] || [ "$k6_code" -eq 127 ]; then reason=start_failed; fi
[ "$interrupted" -eq 0 ] || reason=interrupted
# JSON strings below are validated identifiers, UTC timestamps or fixed enums.
# Never interpolate free-form paths or error messages without a JSON encoder.
printf 'PROXY_BENCH_FIXED_POOL_WORKER_EXIT_V1 {"run_id":"%s","worker_id":%s,"process_started_at":"%s","process_ended_at":"%s","exit_code":%s,"termination_reason":"%s"}\n' \
  "$RUN_ID" "$worker_id" "$started_at" "$ended_at" "$k6_code" "$reason"
[ "$interrupted" -eq 0 ] || exit 130
exit "$k6_code"
