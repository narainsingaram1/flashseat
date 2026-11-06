export const API = process.env.NEXT_PUBLIC_API_URL || "http://localhost:8080/api/v1";

export type Section = { id: string; name: string; tier: number; x: number; y: number; rows: number; width: number };
export type Row = { section: string; tier: number; label: string; start: number; len: number; x: number; y: number };
export type Layout = { seats: number; width: number; height: number; sections: Section[]; rows: Row[] };

export type Sample = { t: number; a: string; k: string; s: number; d: string };
export type Point = {
  t: number; holds: number; conflicts: number; soldOut: number; failures: number; checkouts: number;
  requests: number; holdP50: number; holdP99: number; sold: number; held: number; waiting: number;
};
export type Invariants = {
  checkedAt: string; doubleSold: number; overlappingClaims: number; staleHolds: number; orderTotalErrors: number;
  seatsSold: number; seatsHeld: number; orders: number; revenueCents: number; redisDrift: number;
};
export type ChaosMark = { t: number; action: string; event: "start" | "end"; detail: string };
export type Queue = { issued: number; admitted: number; waiting: number };

export type Run = {
  id: string; status: "queued" | "running" | "succeeded" | "failed"; mode: "safe" | "naive"; preset: string;
  seats: number; buyers: number; arrivalSeconds: number; admitRate: number; holdTtlSeconds: number;
  metrics: Metrics; invariants: { final?: Invariants; passed?: boolean }; k6Summary?: K6Summary; chaos: ChaosMark[];
  hasTimeline: boolean; failureMessage?: string; createdAt: string; startedAt?: string; completedAt?: string;
};

export type Metrics = Partial<{
  seats: number; buyers: number; mode: string; seatsSold: number; orders: number; revenueCents: number; sellThroughPct: number;
  doubleSold: number; overlapping: number; holdsCreated: number; holdsExpired: number; holdsReleased: number; resoldSeats: number;
  serverRequests: number; peakRequestsPerSec: number; peakHoldAttemptsPerSec: number; holdP50Ms: number; holdP95Ms: number;
  holdP99Ms: number; checkoutP99Ms: number; responses: Record<string, number>; clientDurationMs: number; lastSaleMs: number;
  soldOutMs: number; k6Version: string;
}>;

export type K6Summary = Partial<{
  buyersArrived: number; buyersPurchased: number; seatsPurchased: number; buyersSoldOut: number; buyersGaveUp: number;
  buyersAbandoned: number; buyersReleased: number; checkoutsExpired: number; holdRacesLost: number; responses503: number;
  responsesUnexpected: number; droppedBuyers: number; httpRequests: number; httpRequestsPerSec: number; httpFailedRate: number;
  maxVUs: number; queueWaitP50Ms: number; queueWaitP95Ms: number; queueWaitMaxMs: number; holdP50Ms: number; holdP95Ms: number;
  holdP99Ms: number; checkoutP50Ms: number; checkoutP99Ms: number; journeyP50Ms: number; testDurationMs: number;
}>;

export type Snapshot = {
  run: Run | null; phase: Phase; t: number; layout: Layout | null; states: string; totals: Record<string, number>;
  series: Point[]; chaos: ChaosMark[]; invariants: Invariants | null; queue: Queue | null;
};

export type Frame = {
  t: number; d?: number[]; c?: Record<string, number>; f?: Sample[]; p?: Point; q?: Queue; i?: Invariants; x?: ChaosMark[]; phase?: Phase;
};

export type Timeline = { version: number; layout: Layout; mode: string; frames: Frame[]; series: Point[]; chaos: ChaosMark[]; final: Invariants | null };

export type Phase = "idle" | "preparing" | "selling" | "settling" | "done" | "failed";

export type RunInput = { mode: "safe" | "naive"; preset: string; buyers: number; arrivalSeconds: number; admitRate: number; holdTtlSeconds: number };
export type PresetOption = { id: string; name: string; seats: number; defaults: RunInput };
export type ChaosAction = { id: string; label: string; description: string; seconds: number };

export async function getJSON<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(`${API}${path}`, { cache: "no-store", ...init });
  const body = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(body.message || `Request failed (${res.status})`);
  return body as T;
}

export const fmt = {
  n: (v: number | undefined) => (v ?? 0).toLocaleString("en-US"),
  ms: (v: number | undefined) => (v === undefined ? "—" : v >= 100 ? `${Math.round(v)} ms` : `${v.toFixed(1)} ms`),
  secs: (ms: number | undefined) => (ms === undefined ? "—" : `${(ms / 1000).toFixed(1)} s`),
  pct: (v: number | undefined) => `${(v ?? 0).toFixed(1)}%`,
  usd: (cents: number | undefined) => `$${Math.round((cents ?? 0) / 100).toLocaleString("en-US")}`,
  clock: (ms: number) => {
    const s = Math.max(0, Math.floor(ms / 1000));
    return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;
  },
};

export const presetName: Record<string, string> = { club: "Basement Club", arena: "Meridian Arena" };
