"use client";

import type { ChaosMark, Invariants, Point, Sample } from "@/lib/api";
import { fmt } from "@/lib/api";

const CHAOS_LABEL: Record<string, string> = {
  "redis-outage": "Redis cut", "redis-wipe": "Redis wiped", "postgres-slow": "Postgres slowed", "api-crash": "API replica killed",
};

/** Requests per second, stacked by outcome, with hold P99 latency overlaid. */
export function TrafficChart({ series, chaos, playhead, height = 150 }: { series: Point[]; chaos: ChaosMark[]; playhead?: number; height?: number }) {
  const W = 1000, H = height, pad = { l: 44, r: 44, t: 10, b: 20 };
  const end = Math.max(10000, ...series.map((p) => p.t), ...(playhead ? [playhead] : []));
  const peak = Math.max(10, ...series.map((p) => p.holds + p.conflicts + p.soldOut + p.failures + p.checkouts));
  const peakLat = Math.max(5, ...series.map((p) => p.holdP99));
  const x = (t: number) => pad.l + (t / end) * (W - pad.l - pad.r);
  const y = (v: number) => H - pad.b - (v / peak) * (H - pad.t - pad.b);
  const yl = (v: number) => H - pad.b - (v / peakLat) * (H - pad.t - pad.b);
  const bw = Math.max(1.5, (W - pad.l - pad.r) / Math.max(1, end / 1000) - 1.5);
  const stacks: [keyof Point, string][] = [["holds", "#34c77b"], ["checkouts", "#1f7f4f"], ["conflicts", "#f0a63c"], ["soldOut", "#4a4c54"], ["failures", "#f0524f"]];
  const line = series.filter((p) => p.holdP99 > 0).map((p, i) => `${i ? "L" : "M"}${x(p.t).toFixed(1)},${yl(p.holdP99).toFixed(1)}`).join("");
  return (
    <svg className="chart" viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none" role="img" aria-label="Requests per second and hold latency">
      {[0.5, 1].map((f) => (
        <g key={f}>
          <line x1={pad.l} x2={W - pad.r} y1={y(peak * f)} y2={y(peak * f)} className="grid" />
          <text x={pad.l - 6} y={y(peak * f) + 4} textAnchor="end" className="axis">{fmt.n(Math.round(peak * f))}</text>
          <text x={W - pad.r + 6} y={yl(peakLat * f) + 4} className="axis lat">{Math.round(peakLat * f)}ms</text>
        </g>
      ))}
      {series.map((p) => {
        let acc = 0;
        return stacks.map(([k, c]) => {
          const v = p[k] as number;
          if (!v) return null;
          const top = y(acc + v), bottom = y(acc);
          acc += v;
          return <rect key={`${p.t}-${k}`} x={x(p.t) - bw} y={top} width={bw} height={Math.max(0.5, bottom - top)} fill={c} />;
        });
      })}
      {line && <path d={line} className="latency" />}
      {chaos.filter((c) => c.event === "start").map((c, i) => (
        <g key={i}>
          <line x1={x(c.t)} x2={x(c.t)} y1={pad.t} y2={H - pad.b} className="chaos-line" />
          <text x={x(c.t) + 4} y={pad.t + 10} className="chaos-text">{CHAOS_LABEL[c.action] ?? c.action}</text>
        </g>
      ))}
      {playhead !== undefined && <line x1={x(playhead)} x2={x(playhead)} y1={pad.t} y2={H - pad.b} className="playhead" />}
      <text x={pad.l} y={H - 4} className="axis">0:00</text>
      <text x={W - pad.r} y={H - 4} textAnchor="end" className="axis">{fmt.clock(end)}</text>
    </svg>
  );
}

export function ChartLegend() {
  return (
    <div className="legend small">
      <span><i style={{ background: "#34c77b" }} />Holds won</span>
      <span><i style={{ background: "#1f7f4f" }} />Checkouts</span>
      <span><i style={{ background: "#f0a63c" }} />Lost race</span>
      <span><i style={{ background: "#4a4c54" }} />Sold out</span>
      <span><i style={{ background: "#f0524f" }} />Failed</span>
      <span><i className="line" />Hold P99</span>
    </div>
  );
}

const STATUS_CLASS = (s: number) => (s >= 500 ? "bad" : s === 409 || s === 410 ? "warn" : s >= 400 ? "bad" : "ok");

export function Feed({ items }: { items: Sample[] }) {
  return (
    <ol className="feed" aria-live="off">
      {items.length === 0 && <li className="feed-empty">Buyer activity appears here once the sale opens.</li>}
      {items.map((s, i) => (
        <li key={`${s.t}-${i}`}>
          <span className="who">{s.a}</span>
          <span className={`code ${STATUS_CLASS(s.s)}`}>{s.k === "queue" ? "queue" : s.s}</span>
          <span className="what">{describe(s)}</span>
        </li>
      ))}
    </ol>
  );
}

function describe(s: Sample) {
  if (s.k === "hold") {
    if (s.s === 201) return `held ${s.d}`;
    if (s.d.includes("adjacent")) return `${s.d}`;
    if (s.s === 409) return "lost the race, retrying";
    if (s.s === 503) return "failed closed: " + s.d;
    return s.d;
  }
  if (s.k === "checkout") return s.s === 200 ? s.d : s.s === 410 ? "hold expired before paying" : s.d;
  if (s.k === "release") return "changed their mind, released seats";
  return s.d;
}

type Check = { label: string; value: number; ok: boolean; hint: string };

export function InvariantPanel({ inv, mode }: { inv: Invariants | null; mode: string }) {
  const checks: Check[] = inv ? [
    { label: "Overlapping claims", value: inv.overlappingClaims, ok: inv.overlappingClaims === 0, hint: "two live holds, or a hold on a sold seat" },
    { label: "Order totals wrong", value: inv.orderTotalErrors, ok: inv.orderTotalErrors === 0, hint: "order total ≠ sum of line items" },
    ...(mode === "safe" ? [
      { label: "Caught only by the database", value: inv.backstopHits ?? 0, ok: (inv.backstopHits ?? 0) === 0, hint: "a seat unique index rejected a write the engine's own checks let through" },
      { label: "Stale holds", value: inv.staleHolds, ok: inv.staleHolds === 0, hint: "active > 5 s past expiry" },
      { label: "Redis ↔ Postgres drift", value: inv.redisDrift, ok: inv.redisDrift === 0, hint: inv.redisDrift < 0 ? "Redis unreachable or rebuilding" : "taken in Postgres, free in Redis" },
    ] : []),
  ] : [];
  const dup = inv?.doubleSold ?? 0;
  return (
    <div className="checks">
      <div className={`headline-check ${!inv ? "idle" : dup ? "fail" : "pass"}`}>
        <b>{inv ? fmt.n(dup) : "—"}</b>
        <div>
          <span>Seats sold twice</span>
          <small>{!inv ? "Checked against PostgreSQL every second during a sale" : dup ? "Order rows say these seats belong to two buyers" : mode === "safe" ? "Every seat has exactly one owner" : "None yet. Add buyers or shorten the arrival window"}</small>
        </div>
      </div>
      {checks.map((c) => (
        <div key={c.label} className={`check ${c.ok ? "pass" : c.value < 0 ? "unknown" : "fail"}`} title={c.hint}>
          <i className="sq" />
          <span className="label">{c.label}</span>
          <b>{c.value < 0 ? "?" : fmt.n(c.value)}</b>
        </div>
      ))}
    </div>
  );
}

export function Stat({ label, value, sub, tone }: { label: string; value: string; sub?: string; tone?: "good" | "bad" | "warn" }) {
  return (
    <div className={`stat ${tone ?? ""}`}>
      <span>{label}</span>
      <b>{value}</b>
      {sub && <small>{sub}</small>}
    </div>
  );
}

export function SeatBar({ sold, held, conflict, total }: { sold: number; held: number; conflict: number; total: number }) {
  const w = (v: number) => `${total ? (v / total) * 100 : 0}%`;
  return (
    <div className="seatbar" aria-label={`${sold} sold, ${held} held of ${total}`}>
      <i className="sold" style={{ width: w(sold) }} />
      <i className="conflict" style={{ width: w(conflict) }} />
      <i className="held" style={{ width: w(held) }} />
    </div>
  );
}
