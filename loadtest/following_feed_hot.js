import http from 'k6/http';
import { check } from 'k6';
import exec from 'k6/execution';

// 夹具中的测试用户都关注 1 名普通作者和 10 名拉模式作者。
// 令牌文件只在注释版的 loadtest/generated/ 中保存，不提交仓库。
const fixture = JSON.parse(open('./following_normal_viewer_fixture.json'));
const tokens = fixture.feed.viewer_tokens.map((viewer) => viewer.token);

if (tokens.length < 50) {
	throw new Error(`following-feed test requires at least 50 viewer tokens; got ${tokens.length}`);
}

const baseURL = __ENV.BASE_URL || 'http://192.168.86.1:8080';
const rate = Number(__ENV.RATE || 200);
const duration = __ENV.DURATION || '60s';

export const options = {
  scenarios: {
    following_feed_hot: {
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
  const response = http.post(`${baseURL}/feed/listByFollowing`, payload, {
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${token}`,
    },
    tags: { scenario: 'following_feed_hot' },
  });

  check(response, {
    'listByFollowing returns HTTP 200': (res) => res.status === 200,
    'listByFollowing returns 20 unique videos': (res) => {
      if (res.status !== 200) return false;
      const videos = res.json('video_list');
      if (!Array.isArray(videos) || videos.length !== 20) return false;
      return new Set(videos.map((video) => Number(video.id))).size === videos.length;
    },
  });
}
