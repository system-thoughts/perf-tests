#!/bin/sh
# Only manage this worker's child process. No Kubernetes API or remote control.
set -u
SCRIPT_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd) || exit 3
RESULT_DIR=${RESULT_DIR-/results}
if [ -n "$RESULT_DIR" ]; then
  case "$RESULT_DIR" in /*) ;; *) RESULT_DIR="$PWD/$RESULT_DIR" ;; esac
fi
export RESULT_DIR
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
proxy-bench-worker-support prepare-input --script-dir "$SCRIPT_DIR" &
child=$!
[ "$interrupted" -eq 0 ] || forward_signal
wait_child
prepare_code=$child_code
child=''
[ "$interrupted" -eq 0 ] || exit 130
[ "$prepare_code" -eq 0 ] || exit "$prepare_code"

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
reason=completed
[ "$k6_code" -eq 0 ] || reason=failed
if [ "$k6_code" -eq 126 ] || [ "$k6_code" -eq 127 ]; then reason=start_failed; fi
[ "$interrupted" -eq 0 ] || reason=interrupted
proxy-bench-worker-support write-report --started-at "$started_at" --ended-at "$ended_at" \
  --exit-code "$k6_code" --reason "$reason"
report_code=$?
[ "$interrupted" -eq 0 ] || exit 130
exit "$report_code"
