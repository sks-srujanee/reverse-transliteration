import http from 'k6/http';
import { check } from 'k6';
import { Counter, Rate, Trend } from 'k6/metrics';

const phase = __ENV.PHASE || 'baseline';
const base = __ENV.BASE_URL;
if (!base || !__ENV.API_KEY) throw new Error('Set BASE_URL and API_KEY');
if (!/^http:\/\/127\.0\.0\.1:\d{1,5}$/.test(base)) throw new Error('This test is restricted to local loopback');

const successful = new Counter('conversions');
const busy = new Counter('busy_responses');
const limited = new Counter('rate_limited_responses');
const successRate = new Rate('conversion_success_rate');
const incorrect = new Rate('incorrect_results');
const unexpected = new Rate('unexpected_status');
const successLatency = new Trend('conversion_latency_ms', true);
const rejectionLatency = new Trend('rejection_latency_ms', true);
const isBaseline = phase === 'baseline';
export const options = {
  scenarios: {
    requests: isBaseline
      ? { executor: 'shared-iterations', vus: 1, iterations: 15, maxDuration: '2m' }
      : { executor: 'constant-arrival-rate', rate: Number(__ENV.RATE || 10), timeUnit: '1s', duration: __ENV.DURATION || '30s', preAllocatedVUs: 32, maxVUs: 32, gracefulStop: '20s' },
  },
  thresholds: {
    checks: ['rate==1'],
    incorrect_results: ['rate==0'],
    unexpected_status: ['rate==0'],
    conversion_success_rate: [isBaseline ? 'rate==1' : 'rate>0'],
    ...(!isBaseline ? { dropped_iterations: ['count==0'] } : {}),
  },
  summaryTrendStats: ['avg', 'min', 'med', 'max', 'p(95)', 'p(99)'],
};

export default function () {
  const response = http.post(`${base}/v1/reverse`, JSON.stringify({ text: 'ગુજરાત मेरा नाम राहुल है தமிழ் తెలుగు বাংলা ଓଡ଼ିଆ' }), {
    headers: { Authorization: `Bearer ${__ENV.API_KEY}`, 'Content-Type': 'application/json' },
    timeout: '20s',
  });
  const ok = response.status === 200;
  let valid = false;
  if (ok) {
    try { valid = response.json('result') === 'gujarat mera naam rahul hai tamil telugu bangla oriya'; } catch (_) { /* Recorded as an incorrect result below. */ }
    check(response, { 'successful conversion is correct': () => valid });
    successLatency.add(response.timings.duration);
  } else {
    rejectionLatency.add(response.timings.duration);
  }
  const allowed = [200, 503, 429].includes(response.status);
  check(response, { 'status is success or controlled overload': () => allowed });
  successful.add(ok && valid ? 1 : 0);
  busy.add(response.status === 503 ? 1 : 0);
  limited.add(response.status === 429 ? 1 : 0);
  successRate.add(ok && valid);
  incorrect.add(ok && !valid);
  unexpected.add(!allowed);
}

export function handleSummary(data) {
  return { [__ENV.SUMMARY_PATH || 'k6-summary.json']: JSON.stringify(data, null, 2) };
}
