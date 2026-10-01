import http from 'k6/http';
import { check } from 'k6';
import exec from 'k6/execution';

// 热门流正常热路径：登录用户读取首页，命中 Redis 热榜快照，
// 并让服务端实际执行 BatchGetLiked。
// Token 只保存在 loadtest/generated/ 夹具中，不提交仓库。
const fixture = JSON.parse(open('./popularity_hot_fixture.json'));
const tokens = fixture.viewer_tokens.map((viewer) => viewer.token);

if (tokens.length < 50) {
  throw new Error(`popularity test requires at least 50 viewer tokens; got ${tokens.length}`);
}

const baseURL = __ENV.BASE_URL || 'http://192.168.86.1:8080';
const rate = Number(__ENV.RATE || 200);
const duration = __ENV.DURATION || '60s';
// 正式压测传入预热请求返回的 as_of，固定同一份 5 分钟快照，
// 避免跨分钟时把 ZUNIONSTORE 快照构建成本混入正常读取路径。
const asOf = Number(__ENV.AS_OF || 0);

if (asOf <= 0) {
  throw new Error('AS_OF from the prewarmed popularity snapshot is required');
}

export const options = {
  scenarios: {
    popularity_hot: {
      executor: 'constant-arrival-rate',
      rate,
      timeUnit: '1s',
      duration,
      preAllocatedVUs: Number(__ENV.PRE_ALLOCATED_VUS || 100),
      maxVUs: Number(__ENV.MAX_VUS || 2000),
    },
  },
  summaryTrendStats: ['avg', 'min', 'med', 'max', 'p(90)', 'p(95)', 'p(99)'],
  thresholds: {
    http_req_failed: ['rate<0.01'],
    checks: ['rate>0.99'],
  },
};

const payload = JSON.stringify({ limit: 20, as_of: asOf, offset: 0 });

export default function () {
  const token = tokens[exec.scenario.iterationInTest % tokens.length];
  const response = http.post(`${baseURL}/feed/listByPopularity`, payload, {
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${token}`,
    },
    tags: { scenario: 'popularity_hot' },
  });

  check(response, {
    'listByPopularity returns HTTP 200': (res) => res.status === 200,
    'listByPopularity returns 20 unique videos with liked state': (res) => {
      if (res.status !== 200) return false;
      const videos = res.json('video_list');
      if (!Array.isArray(videos) || videos.length !== 20) return false;
      if (new Set(videos.map((video) => Number(video.id))).size !== videos.length) return false;
      return videos.every((video) => typeof video.is_liked === 'boolean');
    },
  });
}
