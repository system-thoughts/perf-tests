import exec from 'k6/execution';
import { textSummary, summaryTrendStats } from '../../lib/summary.js';

export { summaryTrendStats };

export function summaryOutputs(data, spec) {
  const envelope = {
    schema_version: 1, run_id: spec.run_id, worker_id: spec.worker_id,
    manifest_id: spec.manifest_id, plan_id: spec.plan_id,
    effective_options: exec.test.options,
    native_summary: data,
  };
  const json = JSON.stringify(envelope);
  const consumed = data.metrics.plan_consumed?.values?.count;
  const report = textSummary(data, {
    mode: 'fixed-pool', proxy: spec.proxy,
    target: `dataset ${spec.manifest_id}, Range: ${spec.range_bytes} bytes`,
    rate: spec.rate, preAllocatedVUs: spec.pre_allocated_vus, maxVUs: spec.max_vus,
    expectedStatus: 206,
    details: [
      ['Run ID', spec.run_id], ['Worker', String(spec.worker_id)], ['Plan', spec.plan_id],
      ['Consumed', `${Number.isFinite(consumed) ? consumed : 'n/a'} plan entries; shard ${spec.region_count} regions (repeats included)`],
    ],
  });
  return { stdout: report + `PROXY_BENCH_FIXED_POOL_K6_SUMMARY_V1 ${json}\n` };
}
