// Fixed-pool execution consumes prepared metadata only. No preparation hooks.
import http from 'k6/http';
import exec from 'k6/execution';
import { SharedArray } from 'k6/data';
import { Counter } from 'k6/metrics';
import { regionRequest } from './lib/plan.js';
import { summaryOutputs, summaryTrendStats } from './lib/summary.js';

const loadedAt = Date.now();
const spec = JSON.parse(open(__ENV.WORKER_SPEC));
const objects = new SharedArray('fixed-pool-objects', () => JSON.parse(open(spec.objects_file.path)));
const shard = new SharedArray('fixed-pool-shard', () => JSON.parse(open(spec.shard.path)));
if (shard.length !== spec.region_count) throw new Error('shard length mismatch');
if (__VU === 0) console.log(`Plan loaded: shard_regions=${shard.length} objects=${objects.length} elapsed_ms=${Date.now() - loadedAt}`);
const consumed = new Counter('plan_consumed');
http.setResponseCallback(http.expectedStatuses(206));

export const options = {
  scenarios: {
    fixed_pool: {
      executor: 'constant-arrival-rate', rate: spec.rate, timeUnit: '1s',
      duration: `${spec.duration_ms}ms`, gracefulStop: `${spec.graceful_stop_ms}ms`,
      preAllocatedVUs: spec.pre_allocated_vus, maxVUs: spec.max_vus,
    },
  },
  discardResponseBodies: true,
  noConnectionReuse: false,
  noVUConnectionReuse: false,
  maxRedirects: 0,
  summaryTrendStats,
};

export default function () {
  let request;
  try {
    request = regionRequest(spec, objects, shard, exec.scenario.iterationInInstance);
  } catch (error) {
    exec.test.abort(`fixed-pool mapping failed: ${error.message}`);
    return;
  }
  consumed.add(1);
  // One business call per iteration. With connection reuse the native HTTP
  // transport can resend an idempotent GET; this does not consume another item.
  http.get(request.url, {
    headers: { ...spec.headers, Range: request.range },
    timeout: `${spec.timeout_ms}ms`, redirects: 0, responseType: 'none',
    tags: { name: 'fixed-pool' },
  });
}

export function handleSummary(data) { return summaryOutputs(data, spec); }
