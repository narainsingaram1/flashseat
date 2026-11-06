// A ticket on-sale, one iteration per buyer. Buyers arrive at a steady rate
// over the arrival window, wait in the virtual queue, ask the server for the
// best seats available (some insist on a price tier), retry when they lose a
// race, think, and then buy, change their mind, or walk away and let the hold
// expire. Every step is a real HTTP request through the gateway.
//
// Run standalone:
//   k6 run -e BASE_URL=http://localhost:8080/api/v1 -e EVENT_ID=arena \
//          -e BUYERS=500 -e ARRIVAL_SECONDS=10 -e RUN_ID=local onsale.js
import http from "k6/http";
import exec from "k6/execution";
import { sleep } from "k6";
import { Counter, Trend } from "k6/metrics";

const BASE = __ENV.BASE_URL;
const EVENT = __ENV.EVENT_ID || "arena";
const BUYERS = Number(__ENV.BUYERS || 500);
const ARRIVAL = Number(__ENV.ARRIVAL_SECONDS || 10);
const RUN = (__ENV.RUN_ID || "local").slice(0, 8);
const MAX_QUEUE_WAIT_MS = 180000;

const arrived = new Counter("buyers_arrived");
const purchased = new Counter("buyers_purchased");
const seatsPurchased = new Counter("seats_purchased");
const soldOut = new Counter("buyers_sold_out");
const gaveUp = new Counter("buyers_gave_up");
const abandoned = new Counter("buyers_abandoned");
const released = new Counter("buyers_released");
const checkoutExpired = new Counter("checkouts_expired");
const holdLost = new Counter("hold_races_lost");
const unavailable = new Counter("responses_503");
const unexpected = new Counter("responses_unexpected");
const queueWait = new Trend("queue_wait", true);
const holdTime = new Trend("hold_latency", true);
const checkoutTime = new Trend("checkout_latency", true);
const journey = new Trend("journey", true);

export const options = {
  scenarios: {
    onsale: {
      executor: "constant-arrival-rate",
      rate: BUYERS,
      timeUnit: `${ARRIVAL}s`,
      duration: `${ARRIVAL}s`,
      // Allocate up front: buyers spend several seconds queueing and thinking,
      // and k6 cannot create VUs fast enough mid-test without dropping arrivals.
      preAllocatedVUs: Math.min(BUYERS, Math.ceil((BUYERS / ARRIVAL) * 15)),
      maxVUs: BUYERS,
      gracefulStop: "240s",
    },
  },
  discardResponseBodies: true,
  summaryTrendStats: ["min", "med", "avg", "p(90)", "p(95)", "p(99)", "max"],
  setupTimeout: "30s",
};

http.setResponseCallback(http.expectedStatuses(200, 201, 202, 204, 409, 410, 503));

export function setup() {
  const layout = http.get(`${BASE}/events/${EVENT}/layout`, { responseType: "text" });
  if (layout.status !== 200) throw new Error(`layout unavailable: ${layout.status}`);
  const tiers = [...new Set(layout.json("sections").map((s) => s.tier))].sort();
  return { tiers };
}

function pick(weights) {
  let r = Math.random() * weights.reduce((a, [, w]) => a + w, 0);
  for (const [v, w] of weights) if ((r -= w) <= 0) return v;
  return weights[0][0];
}

function json(res) {
  try { return JSON.parse(res.body); } catch (_) { return {}; }
}

export default function (data) {
  const started = Date.now();
  const n = exec.scenario.iterationInTest + 1;
  const buyer = `#${n}`;
  const jar = new http.CookieJar();
  const req = (method, path, body, opts = {}) => http.request(method, `${BASE}${path}`, body === null ? null : JSON.stringify(body), {
    jar,
    headers: Object.assign({ "Content-Type": "application/json", "X-Buyer": buyer }, opts.headers || {}),
    responseType: opts.read ? "text" : "none",
    timeout: opts.timeout || "20s",
    tags: { step: opts.step || "other" },
  });
  arrived.add(1);

  if (req("POST", "/sessions/demo", null, { step: "session" }).status !== 200) { unexpected.add(1); return; }

  // Waiting room: join, then long-poll until admitted.
  let ticket;
  for (let i = 0; i < 5 && !ticket; i++) {
    const r = req("POST", `/events/${EVENT}/queue`, null, { read: true, step: "queue" });
    if (r.status === 201) ticket = json(r);
    else { unavailable.add(r.status === 503 ? 1 : 0); sleep(1); }
  }
  if (!ticket) { gaveUp.add(1); return; }
  const queuedAt = Date.now();
  while (!ticket.admitted) {
    if (Date.now() - queuedAt > MAX_QUEUE_WAIT_MS) { gaveUp.add(1); return; }
    const r = req("GET", `/events/${EVENT}/queue/${ticket.ticket}?sig=${ticket.sig}&wait=10`, null, { read: true, timeout: "15s", step: "queue" });
    if (r.status === 200) ticket = Object.assign(ticket, json(r));
    else sleep(1);
  }
  queueWait.add(Date.now() - queuedAt);

  // Ask for seats. Most buyers take the best available; some want a tier.
  let quantity = pick([[1, 15], [2, 45], [3, 15], [4, 25]]);
  let tier = Math.random() < 0.45 ? data.tiers[Math.floor(Math.random() * data.tiers.length)] : 0;
  let hold;
  let keyN = 0; // a new key only after a definite answer; a 503 retry reuses the key
  for (let attempt = 0; attempt < 8 && !hold; attempt++) {
    const r = req("POST", "/holds", { eventId: EVENT, quantity, tier }, {
      read: true, step: "hold",
      headers: { "Idempotency-Key": `${RUN}-${n}-hold-${keyN}`, "X-Admission-Token": ticket.pass },
    });
    if (r.status !== 503) keyN++;
    holdTime.add(r.timings.duration);
    const body = json(r);
    if (r.status === 201 || r.status === 200) { hold = body; break; }
    if (r.status === 409 && body.code === "sold_out") {
      if (tier !== 0) { tier = 0; continue; }          // flexible on price
      if (quantity > 1) { quantity -= 1; continue; }   // flexible on group size
      soldOut.add(1);
      journey.add(Date.now() - started);
      return;
    }
    if (r.status === 409) { holdLost.add(1); sleep(0.1 + Math.random() * 0.3); continue; }
    if (r.status === 503) { unavailable.add(1); sleep(1 + Math.random()); continue; }
    unexpected.add(1);
    return;
  }
  if (!hold) { gaveUp.add(1); return; }

  // Think, then decide.
  sleep(1 + Math.random() * 3);
  const choice = Math.random();
  if (choice < 0.72) {
    // A 503 means the outcome is unknown: the order may have committed. Retry
    // with the same key until the server gives a definite answer.
    for (let attempt = 0; attempt < 8; attempt++) {
      const r = req("POST", `/holds/${hold.id}/checkout`, null, { read: true, step: "checkout", headers: { "Idempotency-Key": `${RUN}-${n}-checkout` } });
      checkoutTime.add(r.timings.duration);
      if (r.status === 200) { purchased.add(1); seatsPurchased.add(hold.seats.length); break; }
      if (r.status === 410) { checkoutExpired.add(1); break; }
      if (r.status === 503) { unavailable.add(1); sleep(Math.min(4, 0.5 * 2 ** attempt)); continue; }
      unexpected.add(1);
      break;
    }
  } else if (choice < 0.82) {
    const r = req("DELETE", `/holds/${hold.id}`, null, { step: "release" });
    if (r.status === 204) released.add(1); else unexpected.add(1);
  } else {
    abandoned.add(1); // walks away; the hold expires
  }
  journey.add(Date.now() - started);
}

const val = (d, metric, field) => (d.metrics[metric] && d.metrics[metric].values[field]) || 0;

export function handleSummary(d) {
  const summary = {
    buyersArrived: val(d, "buyers_arrived", "count"),
    buyersPurchased: val(d, "buyers_purchased", "count"),
    seatsPurchased: val(d, "seats_purchased", "count"),
    buyersSoldOut: val(d, "buyers_sold_out", "count"),
    buyersGaveUp: val(d, "buyers_gave_up", "count"),
    buyersAbandoned: val(d, "buyers_abandoned", "count"),
    buyersReleased: val(d, "buyers_released", "count"),
    checkoutsExpired: val(d, "checkouts_expired", "count"),
    holdRacesLost: val(d, "hold_races_lost", "count"),
    responses503: val(d, "responses_503", "count"),
    responsesUnexpected: val(d, "responses_unexpected", "count"),
    droppedBuyers: val(d, "dropped_iterations", "count"),
    httpRequests: val(d, "http_reqs", "count"),
    httpRequestsPerSec: val(d, "http_reqs", "rate"),
    httpFailedRate: val(d, "http_req_failed", "rate"),
    maxVUs: val(d, "vus_max", "max"),
    queueWaitP50Ms: val(d, "queue_wait", "med"),
    queueWaitP95Ms: val(d, "queue_wait", "p(95)"),
    queueWaitMaxMs: val(d, "queue_wait", "max"),
    holdP50Ms: val(d, "hold_latency", "med"),
    holdP95Ms: val(d, "hold_latency", "p(95)"),
    holdP99Ms: val(d, "hold_latency", "p(99)"),
    checkoutP50Ms: val(d, "checkout_latency", "med"),
    checkoutP99Ms: val(d, "checkout_latency", "p(99)"),
    journeyP50Ms: val(d, "journey", "med"),
    testDurationMs: d.state.testRunDurationMs,
  };
  return { stdout: `FLASHSEAT_RESULT=${JSON.stringify(summary)}\n` };
}
