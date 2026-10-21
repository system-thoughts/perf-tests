import exec from 'k6/execution';

export function summaryOutputs(data, spec) {
  const envelope = {
    schema_version: 1, run_id: spec.run_id, worker_id: spec.worker_id,
    manifest_id: spec.manifest_id, plan_id: spec.plan_id,
    effective_options: exec.test.options,
    native_summary: data,
  };
  const json = JSON.stringify(envelope);
  return {
    [`${__ENV.RESULT_DIR}/.k6-summary.json.pending`]: `${json}\n`,
    stdout: `PROXY_BENCH_FIXED_POOL_K6_SUMMARY_V1 ${json}\n`,
  };
}
