import http from 'k6/http';
import { check } from 'k6';

const baseURL = __ENV.BASE_URL || 'http://192.168.86.1:8080';
const videoID = Number(__ENV.VIDEO_ID || 10020);
const vus = Number(__ENV.VUS || 500);

export const options = {
  scenarios: {
    get_detail_same_key_burst: {
      executor: 'per-vu-iterations',
      vus,
      iterations: 1,
      maxDuration: '30s',
      gracefulStop: '0s',
    },
  },
  summaryTrendStats: ['avg', 'min', 'med', 'max', 'p(90)', 'p(95)', 'p(99)'],
  thresholds: {
    http_req_failed: ['rate<0.01'],
    checks: ['rate>0.99'],
  },
};

const payload = JSON.stringify({ id: videoID });

export default function () {
  const response = http.post(`${baseURL}/video/getDetail`, payload, {
    headers: { 'Content-Type': 'application/json' },
    tags: { scenario: 'get_detail_same_key_burst' },
  });

  check(response, {
    'getDetail returns HTTP 200': (res) => res.status === 200,
    'getDetail returns requested video': (res) => res.status === 200 && Number(res.json('id')) === videoID,
  });
}
