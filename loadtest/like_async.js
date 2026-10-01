import http from 'k6/http';
import { check } from 'k6';
import exec from 'k6/execution';

// 点赞异步链路：每个账户在 60 秒内对不同视频点赞，
// 避免触发每账户限流或 likes 联合唯一索引的重复关系。
const fixturePath = __ENV.FIXTURE || './like_hot_fixture.json';
const fixture = JSON.parse(open(fixturePath));
const poolName = __ENV.POOL;
const tokens = fixture.token_pools[poolName];
const videoIDs = fixture.video_ids;

if (!poolName || !Array.isArray(tokens) || tokens.length === 0) {
  throw new Error('POOL must identify a non-empty fixture token pool');
}
const maxRequestsPerUser = Number(__ENV.REQUESTS_PER_USER || 100);
if (videoIDs.length < maxRequestsPerUser) {
	throw new Error('like test requires at least REQUESTS_PER_USER video IDs');
}

const baseURL = __ENV.BASE_URL || 'http://192.168.86.1:8080';
const rate = Number(__ENV.RATE || 100);
const duration = __ENV.DURATION || '60s';

export const options = {
  scenarios: {
    like_async: {
      executor: 'constant-arrival-rate',
      rate,
      timeUnit: '1s',
      duration,
      preAllocatedVUs: Number(__ENV.PRE_ALLOCATED_VUS || rate),
      maxVUs: Number(__ENV.MAX_VUS || rate * 4),
    },
  },
  summaryTrendStats: ['avg', 'min', 'med', 'max', 'p(90)', 'p(95)', 'p(99)'],
  thresholds: {
    http_req_failed: ['rate<0.01'],
    checks: ['rate>0.99'],
  },
};

export default function () {
  const iteration = exec.scenario.iterationInTest;
  const tokenIndex = iteration % tokens.length;
  const round = Math.floor(iteration / tokens.length);
  // 用户偏移量与轮次步长都和视频总数互质：
  // 同一用户前 maxRequestsPerUser 次不会重复，同时同一时刻的请求会分散到更多视频。
  const videoIndex = (tokenIndex * 7919 + round * 4729) % videoIDs.length;
  const payload = JSON.stringify({ video_id: videoIDs[videoIndex] });
  const response = http.post(`${baseURL}/like/like`, payload, {
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${tokens[tokenIndex].token}`,
    },
    tags: { scenario: 'like_async', pool: poolName },
  });

  check(response, {
    'like returns HTTP 202': (res) => res.status === 202,
    'like command is accepted': (res) => res.status === 202 && res.json('status') === 'PENDING',
  });
}
