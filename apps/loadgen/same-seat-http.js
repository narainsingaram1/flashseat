import http from "k6/http";
import exec from "k6/execution";
import { Counter, Trend } from "k6/metrics";
import { sleep } from "k6";

const users = Number(__ENV.USERS);
const winners = new Counter("benchmark_winners");
const conflicts = new Counter("benchmark_conflicts");
const unexpected = new Counter("benchmark_unexpected_errors");
const attempts = new Counter("benchmark_attempts");
const holdDuration = new Trend("benchmark_hold_duration", true);
const barrierMisses = new Counter("benchmark_barrier_misses");

export const options = {
  scenarios: {
    collision: {
      executor: "per-vu-iterations",
      vus: users,
      iterations: 1,
      maxDuration: "2m",
      gracefulStop: "0s",
    },
  },
  thresholds: {
    benchmark_unexpected_errors: ["count==0"],
    benchmark_barrier_misses: ["count==0"],
  },
  discardResponseBodies: true,
  summaryTrendStats: ["min", "med", "avg", "p(90)", "p(95)", "p(99)", "max"],
};

http.setResponseCallback(http.expectedStatuses(200, 201, 409));

export default function () {
  const session = http.post(`${__ENV.BASE_URL}/sessions/demo`, null, { tags: { operation: "session" } });
  if (session.status !== 200) {
    unexpected.add(1);
    return;
  }
  const waitSeconds = (Number(__ENV.START_AT_MS) - Date.now()) / 1000;
  if (waitSeconds <= 0) {
    barrierMisses.add(1);
    unexpected.add(1);
    return;
  }
  sleep(waitSeconds);
  const response = http.post(`${__ENV.BASE_URL}/holds`, JSON.stringify({ eventId: __ENV.EVENT_ID, seatIds: [__ENV.SEAT_ID] }), {
    headers: { "Content-Type": "application/json", "Idempotency-Key": `bench-${__ENV.JOB_ID}-${exec.vu.idInTest}` },
    tags: { operation: "hold_collision" },
  });
  attempts.add(1);
  holdDuration.add(response.timings.duration);
  if (response.status === 201) winners.add(1);
  else if (response.status === 409) conflicts.add(1);
  else unexpected.add(1);
}

function value(data, metric, field) {
  return data.metrics[metric]?.values?.[field] || 0;
}

export function handleSummary(data) {
  const attemptsCount = value(data, "benchmark_attempts", "count");
  const longestHoldMs = value(data, "benchmark_hold_duration", "max");
  const summary = {
    requests: attemptsCount,
    winners: value(data, "benchmark_winners", "count"),
    conflicts: value(data, "benchmark_conflicts", "count"),
    unexpectedErrors: value(data, "benchmark_unexpected_errors", "count"),
    barrierMisses: value(data, "benchmark_barrier_misses", "count"),
    throughputRps: longestHoldMs > 0 ? attemptsCount / (longestHoldMs / 1000) : 0,
    p50Ms: value(data, "benchmark_hold_duration", "med"),
    p95Ms: value(data, "benchmark_hold_duration", "p(95)"),
    p99Ms: value(data, "benchmark_hold_duration", "p(99)"),
  };
  return { stdout: `FLASHSEAT_RESULT=${JSON.stringify(summary)}\n` };
}
