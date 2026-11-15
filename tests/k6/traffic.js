import http from 'k6/http';
import ws from 'k6/ws';
import { check, fail, sleep } from 'k6';
import { Counter, Gauge, Rate, Trend } from 'k6/metrics';

const baseURL = __ENV.METRIC_K6_URL || '';
const expectedEmail = __ENV.METRIC_K6_EXPECTED_EMAIL || '';
const profile = __ENV.METRIC_K6_PROFILE || 'smoke';
if (!/^http:\/\/127\.0\.0\.1:\d+$/.test(baseURL) || !expectedEmail.startsWith('metric-k6-')) {
  throw new Error('Use tests/k6/run.sh: the suite requires an isolated loopback API.');
}
if (!['smoke', 'load'].includes(profile)) {
  throw new Error('METRIC_K6_PROFILE must be smoke or load.');
}

function integerEnv(name, fallback, maximum) {
  const value = __ENV[name] ? Number(__ENV[name]) : fallback;
  if (!Number.isInteger(value) || value < 1 || value > maximum) {
    throw new Error(`${name} must be an integer between 1 and ${maximum}.`);
  }
  return value;
}
const loadVUs = integerEnv('METRIC_K6_LOAD_VUS', 15, 1000);
const wsVUs = integerEnv('METRIC_K6_WS_VUS', 2, 100);
const rampSeconds = integerEnv('METRIC_K6_RAMP_SECONDS', 5, 300);
const holdSeconds = integerEnv('METRIC_K6_HOLD_SECONDS', 20, 3600);
const batchSize = integerEnv('METRIC_K6_BATCH_SIZE', 5, 1000);
const sleepMs = integerEnv('METRIC_K6_SLEEP_MS', 200, 60000);

const ingestBatches = new Counter('metric_ingest_batches');
const ingestedRecords = new Counter('metric_ingested_records');
const ingestDuration = new Trend('metric_ingest_duration_ms', true);
const metricsDuration = new Trend('metric_metrics_duration_ms', true);
const wsOK = new Rate('metric_ws_ok');
const historyOK = new Rate('metric_history_ok');
const firstSnapshot = new Trend('metric_ws_first_snapshot_ms', true);
const detectionTime = new Trend('metric_incident_detection_ms', true);
const duplicates = new Gauge('metric_duplicate_incidents');

const scenarios = profile === 'smoke' ? {
  api: { executor: 'constant-vus', exec: 'apiTraffic', vus: 2, duration: '10s', gracefulStop: '5s' },
  websocket: { executor: 'constant-vus', exec: 'websocketTraffic', vus: 1, duration: '10s', gracefulStop: '5s' },
} : {
  api: { executor: 'ramping-vus', exec: 'apiTraffic', startVUs: 0, stages: [
    { duration: `${rampSeconds}s`, target: loadVUs },
    { duration: `${holdSeconds}s`, target: loadVUs },
    { duration: `${rampSeconds}s`, target: 0 },
  ], gracefulRampDown: '5s', gracefulStop: '5s' },
  websocket: { executor: 'constant-vus', exec: 'websocketTraffic', vus: wsVUs,
    duration: `${2 * rampSeconds + holdSeconds}s`, gracefulStop: '5s' },
};

const thresholds = {
  checks: ['rate==1'],
  http_req_failed: ['rate==0'],
  metric_ws_ok: ['rate==1'],
  metric_history_ok: ['rate==1'],
};
if (__ENV.METRIC_K6_P95_MS) {
  const maxP95 = Number(__ENV.METRIC_K6_P95_MS);
  if (!Number.isFinite(maxP95) || maxP95 <= 0) throw new Error('METRIC_K6_P95_MS must be a positive number.');
  thresholds['http_req_duration{name:ingest}'] = [`p(95)<${maxP95}`];
}

export const options = { scenarios, thresholds, summaryTrendStats: ['avg', 'med', 'p(95)', 'p(99)', 'max'] };

function jsonOrFail(response, context) {
  try { return response.json(); } catch (_) { fail(`${context} returned invalid JSON`); }
}

function requireResponse(response, status, name) {
  if (!check(response, { [name]: (r) => r && r.status === status })) {
    fail(`${name} failed with HTTP ${response ? response.status : 'no response'}`);
  }
}

function batch(size) {
  const ts = new Date().toISOString();
  return JSON.stringify({ logs: Array.from({ length: size }, () => ({
    ts, endpoint: '/k6/checkout', method: 'GET', status: 500,
    latency_ms: 40, ip: '192.0.2.1', payload_bytes: 100,
  })) });
}

export function setup() {
  const identity = http.get(`${baseURL}/api/auth/me`, { tags: { name: 'identity' } });
  requireResponse(identity, 200, 'isolated identity');
  if (jsonOrFail(identity, 'identity').email !== expectedEmail) fail('Unexpected API identity; refusing to run.');

  const created = http.post(`${baseURL}/api/projects`, JSON.stringify({ name: 'k6 disposable project' }), {
    headers: { 'Content-Type': 'application/json' }, tags: { name: 'create_project' },
  });
  requireResponse(created, 201, 'project created');
  const project = jsonOrFail(created, 'create project');
  if (!project.project_id || !project.api_key) fail('Project response is missing credentials.');

  const thresholdsResponse = http.put(`${baseURL}/api/projects/${project.project_id}/thresholds`, JSON.stringify({
    p95_ms: 0, error_rate: 10, rps_max: 0, req_per_ip_max: 0,
  }), { headers: { 'Content-Type': 'application/json' }, tags: { name: 'set_thresholds' } });
  requireResponse(thresholdsResponse, 200, 'thresholds set');

  const startedAt = Date.now();
  const seeded = http.post(`${baseURL}/ingest/${project.project_id}`, batch(20), {
    headers: { 'Content-Type': 'application/json', 'X-API-Key': project.api_key }, tags: { name: 'seed_ingest' },
  });
  requireResponse(seeded, 202, 'seed ingested');
  return { id: project.project_id, key: project.api_key, startedAt };
}

export function apiTraffic(project) {
  const response = http.post(`${baseURL}/ingest/${project.id}`, batch(batchSize), {
    headers: { 'Content-Type': 'application/json', 'X-API-Key': project.key }, tags: { name: 'ingest' },
  });
  ingestBatches.add(1);
  ingestDuration.add(response.timings.duration);
  if (response.status === 202) ingestedRecords.add(batchSize);
  check(response, { 'ingest accepted': (r) => r.status === 202 });
  const metrics = http.get(`${baseURL}/api/projects/${project.id}/metrics`, { tags: { name: 'metrics' } });
  metricsDuration.add(metrics.timings.duration);
  check(metrics, { 'metrics available': (r) => r.status === 200 });
  sleep(sleepMs / 1000);
}

export function websocketTraffic(project) {
  let validSnapshot = false;
  let socketError = false;
  const started = Date.now();
  let response;
  try {
    response = ws.connect(`${baseURL.replace('http:', 'ws:')}/ws/${project.id}`, {}, (socket) => {
      socket.on('message', (message) => {
        try {
          const event = JSON.parse(message);
          if (!validSnapshot && event.event_type === 'metric_snapshot' && event.project_id === project.id) {
            validSnapshot = true;
            firstSnapshot.add(Date.now() - started);
          }
        } catch (_) { socketError = true; }
      });
      socket.on('error', () => { socketError = true; });
      socket.setTimeout(() => socket.close(), 2000);
    });
  } catch (_) { socketError = true; }
  const ok = !!response && response.status === 101 && validSnapshot && !socketError;
  wsOK.add(ok);
  check(ok, { 'websocket received snapshot': (value) => value });
}

export function teardown(project) {
  if (!project) return;
  const response = http.get(`${baseURL}/api/projects/${project.id}/anomalies`, { tags: { name: 'anomalies' } });
  let incidents = [];
  if (response.status === 200) {
    try { incidents = response.json(); } catch (_) { /* reported by the check below */ }
  }
  const valid = Array.isArray(incidents) && incidents.length === 1 && incidents[0].type === 'high_error_rate';
  historyOK.add(valid);
  check(valid, { 'exactly one persisted error incident': (value) => value });
  if (Array.isArray(incidents)) {
    duplicates.add(Math.max(0, incidents.length - 1));
    if (incidents.length && incidents[0].detected_at) {
      const elapsed = Date.parse(incidents[0].detected_at) - project.startedAt;
      if (Number.isFinite(elapsed) && elapsed >= 0) detectionTime.add(elapsed);
    }
  }
}
