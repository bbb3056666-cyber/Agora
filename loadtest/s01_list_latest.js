import http from 'k6/http';
import { check } from 'k6';

// S01：正常登录用户的最新 Feed 热路径。
// Access Token 不写入脚本；每轮测试前通过 ACCESS_TOKEN 环境变量传入。
const baseURL = __ENV.BASE_URL || 'http://192.168.86.1:8080';
const accessToken = __ENV.ACCESS_TOKEN;

if (!accessToken) {
  throw new Error('ACCESS_TOKEN is required for S01');
}

export const options = {
  scenarios: {
    s01_logged_hot_path: {
      executor: 'constant-arrival-rate',
      rate: Number(__ENV.RATE || 50),
      timeUnit: '1s',
      duration: __ENV.DURATION || '30s',
      preAllocatedVUs: Number(__ENV.PRE_ALLOCATED_VUS || 20),
      maxVUs: Number(__ENV.MAX_VUS || 100),
    },
  },
  thresholds: {
    http_req_failed: ['rate<0.01'],
  },
  summaryTrendStats: ['avg', 'min', 'med', 'max', 'p(90)', 'p(95)', 'p(99)'],
};

const payload = JSON.stringify({
  limit: 10,
  latest_time: 0,
});

const params = {
  headers: {
    'Content-Type': 'application/json',
    Authorization: `Bearer ${accessToken}`,
  },
  tags: {
    scenario: 'S01_logged_hot_path',
  },
};

export default function () {
  const response = http.post(`${baseURL}/feed/listLatest`, payload, params);

  check(response, {
    'listLatest returns HTTP 200': (res) => res.status === 200,
  });
}
