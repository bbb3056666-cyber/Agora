import http from 'k6/http';
import { check } from 'k6';
import exec from 'k6/execution';

// S02 使用独立 fixture：100 个登录用户轮换，避免覆盖 S01 的单用户轻量场景。
const fixture = JSON.parse(open('./s02_list_latest_fixture.json'));
const tokens = fixture.feed.viewer_tokens.map((actor) => actor.token);

if (tokens.length !== 100) {
  throw new Error(`S02 requires exactly 100 viewer tokens; got ${tokens.length}`);
}

const baseURL = __ENV.BASE_URL || 'http://192.168.86.1:8080';
const rate = Number(__ENV.RATE || 2000);
const duration = __ENV.DURATION || '60s';

export const options = {
  scenarios: {
    s02_realistic_logged_latest: {
      executor: 'constant-arrival-rate',
      rate,
      timeUnit: '1s',
      duration,
      preAllocatedVUs: Number(__ENV.PRE_ALLOCATED_VUS || 100),
      maxVUs: Number(__ENV.MAX_VUS || 1000),
    },
  },
  summaryTrendStats: ['avg', 'min', 'med', 'max', 'p(90)', 'p(95)', 'p(99)'],
  thresholds: {
    http_req_failed: ['rate<0.01'],
    checks: ['rate>0.99'],
  },
};

const payload = JSON.stringify({ limit: 20, latest_time: 0 });

export default function () {
  const token = tokens[exec.scenario.iterationInTest % tokens.length];
  const response = http.post(`${baseURL}/feed/listLatest`, payload, {
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${token}`,
    },
    tags: { scenario: 'S02_realistic_logged_latest' },
  });

  check(response, {
    'listLatest returns HTTP 200': (res) => res.status === 200,
    'listLatest returns 20 videos': (res) => {
      if (res.status !== 200) return false;
      const body = res.json();
      return Array.isArray(body.video_list) && body.video_list.length === 20;
    },
  });
}
