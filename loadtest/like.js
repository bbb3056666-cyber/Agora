import http from "k6/http";
import { SharedArray } from "k6/data";
import exec from "k6/execution";
import { Counter, Rate, Trend } from "k6/metrics";

const accounts = new SharedArray("like-test-accounts", () => JSON.parse(open("./generated/like-tokens.json")));
const baseURL = (__ENV.BASE_URL || "http://192.168.86.155").replace(/\/$/, "");
const qps = Number(__ENV.QPS || 20);
const duration = __ENV.DURATION || "30s";
const vus = Number(__ENV.VUS || Math.max(qps, 100));
const firstContentID = Number(__ENV.CONTENT_ID || 262183);
const contentCount = Number(__ENV.CONTENT_COUNT || 20);
const unlikePercent = Number(__ENV.UNLIKE_PERCENT || 0);
const expectedStatus = Number(__ENV.EXPECTED_STATUS || 202);

if (!accounts.length || !Number.isInteger(qps) || qps < 1 || !Number.isInteger(vus) || vus < 1 ||
    !Number.isInteger(firstContentID) || firstContentID < 1 || !Number.isInteger(contentCount) || contentCount < 1 ||
    unlikePercent < 0 || unlikePercent > 100) {
    throw new Error("invalid like test configuration");
}

const accepted = new Counter("like_accepted_total");
const rejected = new Counter("like_rejected_total");
const businessErrors = new Rate("like_business_errors");
const latency = new Trend("like_request_duration_ms", true);

export const options = {
    scenarios: {
        like: {
            executor: "constant-arrival-rate",
            rate: qps,
            timeUnit: "1s",
            duration,
            preAllocatedVUs: vus,
            maxVUs: vus,
            gracefulStop: "15s",
        },
    },
    thresholds: {
        like_business_errors: ["rate<0.01"],
        http_req_failed: ["rate<0.01"],
    },
    summaryTrendStats: ["avg", "min", "med", "p(90)", "p(95)", "p(99)", "max"],
};

export default function () {
    const iteration = exec.scenario.iterationInTest;
    if (__ENV.PACE_LOG === "1" && iteration % qps === 0) {
        console.log(`pace ${new Date().toISOString()} iteration=${iteration}`);
    }
    const account = accounts[iteration % accounts.length];
    // Spread writes across contents while keeping each account/content pair new across account cycles.
    const contentID = firstContentID + (iteration % contentCount + Math.floor(iteration / accounts.length)) % contentCount;
    const unlike = Math.random() * 100 < unlikePercent;
    const path = unlike ? "/like/unlike" : "/like/like";
    const response = http.post(`${baseURL}${path}`, JSON.stringify({ content_id: contentID }), {
        headers: {
            Authorization: `Bearer ${account.token}`,
            "Content-Type": "application/json",
        },
        timeout: "5s",
        tags: { name: `POST ${path}`, action: unlike ? "unlike" : "like" },
    });
    latency.add(response.timings.duration, { action: unlike ? "unlike" : "like" });
    const ok = response.status === expectedStatus;
    businessErrors.add(!ok);
    if (ok) accepted.add(1, { action: unlike ? "unlike" : "like" });
    else rejected.add(1, { status: String(response.status) });
}
