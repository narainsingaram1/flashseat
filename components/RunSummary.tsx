import type { Run } from "@/lib/api";
import { fmt, presetName } from "@/lib/api";
import { Stat } from "./Panels";
import { EngineTag } from "./Nav";

export function verdict(run: Run) {
  const m = run.metrics, inv = run.invariants?.final;
  const sold = inv?.seatsSold ?? m.seatsSold ?? 0;
  const dup = inv?.doubleSold ?? m.doubleSold ?? 0;
  if (run.status === "failed" && !sold) return run.failureMessage || "The run failed before any seats sold.";
  const when = m.soldOutMs !== undefined ? `sold out in ${fmt.secs(m.soldOutMs)}` : `sold (${fmt.pct(m.sellThroughPct)})`;
  return `${fmt.n(sold)} of ${fmt.n(run.seats)} seats ${when}, ${dup === 0 ? "no seat sold twice" : `${fmt.n(dup)} sold to two buyers`}`;
}

export function RunSummary({ run }: { run: Run }) {
  const m = run.metrics, k = run.k6Summary || {}, inv = run.invariants?.final;
  const dup = inv?.doubleSold ?? 0;
  const overlap = inv?.overlappingClaims ?? 0;
  const backstop = inv?.backstopHits ?? 0;
  return (
    <section className={`panel summary ${run.invariants?.passed ? "passed" : "failed"}`}>
      <div className="summary-head">
        <EngineTag mode={run.mode} />
        <span className="meta">{presetName[run.preset] ?? run.preset} · {fmt.n(run.buyers)} buyers over {run.arrivalSeconds} s · {run.admitRate ? `queue admits ${run.admitRate}/s` : "no queue"} · {run.holdTtlSeconds} s holds</span>
      </div>
      <h2 className="verdict">{verdict(run)}</h2>
      {run.failureMessage && <p className="error">{run.failureMessage}</p>}
      <div className="stats wide">
        <Stat label="Seats sold twice" value={fmt.n(dup)} tone={dup ? "bad" : "good"} sub={overlap ? `${fmt.n(overlap)} overlapping claims` : run.mode === "safe" ? (backstop ? `${fmt.n(backstop)} caught only by a unique index` : "engine checks held; no unique index fired") : "checked in PostgreSQL"} />
        <Stat label="Revenue" value={fmt.usd(inv?.revenueCents ?? m.revenueCents)} sub={`${fmt.n(inv?.orders ?? m.orders)} orders`} />
        <Stat label="Peak load" value={`${fmt.n(m.peakRequestsPerSec)} req/s`} sub={`${fmt.n(k.httpRequests)} requests total`} />
        <Stat label="Hold latency P99" value={fmt.ms(k.holdP99Ms ?? m.holdP99Ms)} sub={`p50 ${fmt.ms(k.holdP50Ms ?? m.holdP50Ms)}, client-side`} />
        <Stat label="Checkout P99" value={fmt.ms(k.checkoutP99Ms ?? m.checkoutP99Ms)} />
        <Stat label="Queue wait P95" value={fmt.secs(k.queueWaitP95Ms)} sub={`max ${fmt.secs(k.queueWaitMaxMs)}`} />
        <Stat label="Seats resold" value={fmt.n(m.resoldSeats)} sub={`${fmt.n(m.holdsExpired)} holds expired`} />
        <Stat label="Buyers" value={`${fmt.n(k.buyersPurchased)} bought`} sub={`${fmt.n(k.buyersSoldOut)} sold out · ${fmt.n(k.buyersAbandoned)} walked away`} />
      </div>
    </section>
  );
}
