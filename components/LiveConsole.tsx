"use client";

import Link from "next/link";
import { useCallback, useEffect, useRef, useState } from "react";
import { API, fmt, getJSON, presetName } from "@/lib/api";
import type { ChaosAction, ChaosMark, Frame, Invariants, Layout, Phase, Point, PresetOption, Queue, Run, RunInput, Sample, Snapshot } from "@/lib/api";
import { applyDeltas, countStates, createBoard, type Board } from "@/lib/board";
import { SeatLegend, SeatMap } from "./SeatMap";
import { ChartLegend, Feed, InvariantPanel, SeatBar, Stat, TrafficChart } from "./Panels";
import { Nav, PhasePill } from "./Nav";
import { RunSummary } from "./RunSummary";

const MODES = {
  safe: { title: "FlashSeat engine", body: "Redis Lua claims a block of seats atomically, then one PostgreSQL function locks the rows in order and commits. Constraints are the final guard." },
  naive: { title: "Naive baseline", body: "Read availability, pick seats, insert. No locks, no transaction, no unique constraint — how most first versions are written." },
};

type Live = {
  run: Run | null; phase: Phase; layout: Layout | null; series: Point[]; chaos: ChaosMark[]; invariants: Invariants | null;
  queue: Queue | null; totals: Record<string, number>; t0: number; tAt: number;
};

const sumPrefix = (o: Record<string, number>, p: string) => Object.entries(o).reduce((a, [k, v]) => (k.startsWith(p) ? a + v : a), 0);

export function LiveConsole() {
  const [live, setLive] = useState<Live>({ run: null, phase: "idle", layout: null, series: [], chaos: [], invariants: null, queue: null, totals: {}, t0: 0, tAt: 0 });
  const [board, setBoard] = useState<Board | null>(null);
  const boardRef = useRef<Board | null>(null);
  const [counts, setCounts] = useState({ free: 0, held: 0, sold: 0, conflict: 0 });
  const [feed, setFeed] = useState<Sample[]>([]);
  const [connected, setConnected] = useState(false);
  const [options, setOptions] = useState<{ presets: PresetOption[]; chaos: ChaosAction[] } | null>(null);
  const [form, setForm] = useState<RunInput | null>(null);
  const [error, setError] = useState("");
  const [starting, setStarting] = useState(false);
  const [now, setNow] = useState(0);
  const [finished, setFinished] = useState<Run | null>(null);

  useEffect(() => {
    getJSON<{ presets: PresetOption[]; chaos: ChaosAction[] }>("/control/options")
      .then((o) => { setOptions(o); setForm((f) => f ?? o.presets.find((p) => p.id === "arena")?.defaults ?? o.presets[0].defaults); })
      .catch(() => setError("Can't reach the control plane. Start the stack with docker compose up --build, then refresh."));
  }, []);

  const onSnapshot = useCallback((s: Snapshot) => {
    const b = s.layout ? createBoard(s.layout.seats, s.states) : null;
    boardRef.current = b;
    setBoard(b);
    if (b) setCounts(countStates(b));
    setLive({ run: s.run, phase: s.phase, layout: s.layout, series: s.series, chaos: s.chaos, invariants: s.invariants, queue: s.queue, totals: s.totals || {}, t0: s.t, tAt: performance.now() });
  }, []);

  const onFrame = useCallback((f: Frame) => {
    const b = boardRef.current;
    if (b && f.d) {
      applyDeltas(b, f.d, performance.now());
      setCounts(countStates(b));
    }
    if (f.f?.length) setFeed((old) => [...f.f!.slice().reverse(), ...old].slice(0, 60));
    setLive((l) => {
      const next = { ...l, t0: f.t, tAt: performance.now() };
      if (f.c) {
        const totals = { ...l.totals };
        for (const [k, v] of Object.entries(f.c)) totals[k] = (totals[k] || 0) + v;
        next.totals = totals;
      }
      if (f.p) next.series = [...l.series, f.p];
      if (f.i) next.invariants = f.i;
      if (f.x) next.chaos = [...l.chaos, ...f.x];
      if (f.q) next.queue = f.q;
      if (f.phase) next.phase = f.phase;
      return next;
    });
  }, []);

  useEffect(() => {
    const es = new EventSource(`${API}/control/stream`);
    es.addEventListener("snapshot", (e) => { setConnected(true); onSnapshot(JSON.parse((e as MessageEvent).data)); });
    es.addEventListener("frame", (e) => onFrame(JSON.parse((e as MessageEvent).data)));
    es.onerror = () => setConnected(false);
    return () => es.close();
  }, [onSnapshot, onFrame]);

  useEffect(() => {
    const id = setInterval(() => setNow(performance.now()), 250);
    return () => clearInterval(id);
  }, []);

  const runId = live.run?.id;
  useEffect(() => {
    if ((live.phase !== "done" && live.phase !== "failed") || !runId) return;
    let cancelled = false;
    const load = () => getJSON<Run>(`/control/simulations/${runId}`).then((r) => {
      if (cancelled) return;
      if (r.status === "running") setTimeout(load, 800);
      else setFinished(r);
    }).catch(() => {});
    load();
    return () => { cancelled = true; };
  }, [live.phase, runId]);

  const running = live.phase === "preparing" || live.phase === "selling" || live.phase === "settling";

  async function start() {
    if (!form) return;
    setStarting(true);
    setError("");
    setFeed([]);
    setFinished(null);
    try {
      await getJSON("/control/simulations", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(form) });
    } catch (e) {
      setError(e instanceof Error ? e.message : "Could not start the sale");
    } finally {
      setStarting(false);
    }
  }

  async function chaos(id: string) {
    setError("");
    try {
      await getJSON(`/control/chaos/${id}`, { method: "POST" });
    } catch (e) {
      setError(e instanceof Error ? e.message : "Fault injection failed");
    }
  }

  const choosePreset = (id: string) => {
    const p = options?.presets.find((x) => x.id === id);
    if (p && form) setForm({ ...p.defaults, mode: form.mode });
  };

  const last = live.series[live.series.length - 1];
  const elapsed = live.phase === "selling" || live.phase === "settling" ? live.t0 + (now - live.tAt) : live.t0;
  const mode = live.run?.mode ?? form?.mode ?? "safe";
  const total = live.layout?.seats ?? 0;
  const lost = live.totals["hold_409_seat_unavailable"] || 0;
  const failed = sumPrefix(live.totals, "hold_503") + sumPrefix(live.totals, "hold_500");
  const won = live.totals["hold_201"] || 0;
  const activeChaos = new Set(live.chaos.filter((c) => c.event === "start" && !live.chaos.some((e) => e.event === "end" && e.action === c.action && e.t >= c.t)).filter((c) => c.action === "redis-outage" || c.action === "postgres-slow").map((c) => c.action));

  return (
    <main className="app">
      <Nav active="live" status={<PhasePill phase={live.phase} connected={connected} />} />

      <section className="hero">
        <div>
          <h1>Watch thousands of buyers fight for every seat.</h1>
          <p>Each square is a real seat in PostgreSQL. A k6 swarm of independent HTTP buyers queues, grabs the best seats it can, loses races, pays, or walks away — and you can break the system while it happens.</p>
        </div>
        {live.run && (
          <div className="run-chip">
            <span className={`mode-tag ${live.run.mode}`}>{live.run.mode === "safe" ? "FlashSeat engine" : "Naive baseline"}</span>
            <span>{presetName[live.run.preset] ?? live.run.preset} · {fmt.n(live.run.buyers)} buyers</span>
            <b className="clock">{fmt.clock(elapsed)}</b>
          </div>
        )}
      </section>

      <div className="console">
        <aside className="panel controls">
          <h2>Configure the sale</h2>
          {form && options ? (
            <>
              <div className="seg" role="radiogroup" aria-label="Venue">
                {options.presets.map((p) => (
                  <button key={p.id} role="radio" aria-checked={form.preset === p.id} className={form.preset === p.id ? "on" : ""} disabled={running} onClick={() => choosePreset(p.id)}>
                    <b>{p.name.replace(/^The /, "")}</b><small>{fmt.n(p.seats)} seats</small>
                  </button>
                ))}
              </div>
              <div className="modes" role="radiogroup" aria-label="Reservation engine">
                {(["safe", "naive"] as const).map((m) => (
                  <button key={m} role="radio" aria-checked={form.mode === m} className={`mode ${m} ${form.mode === m ? "on" : ""}`} disabled={running} onClick={() => setForm({ ...form, mode: m })}>
                    <b>{MODES[m].title}</b><small>{MODES[m].body}</small>
                  </button>
                ))}
              </div>
              <Slider label="Buyers" value={form.buyers} min={100} max={12000} step={100} disabled={running} onChange={(v) => setForm({ ...form, buyers: v })} fmtv={fmt.n} />
              <Slider label="Arrival window" value={form.arrivalSeconds} min={2} max={60} step={1} disabled={running} onChange={(v) => setForm({ ...form, arrivalSeconds: v })} fmtv={(v) => `${v} s`} />
              <Slider label="Waiting room admits" value={form.admitRate} min={0} max={1500} step={10} disabled={running} onChange={(v) => setForm({ ...form, admitRate: v })} fmtv={(v) => (v === 0 ? "everyone" : `${v}/s`)} />
              <Slider label="Hold expires after" value={form.holdTtlSeconds} min={3} max={60} step={1} disabled={running} onChange={(v) => setForm({ ...form, holdTtlSeconds: v })} fmtv={(v) => `${v} s`} />
              <p className="hint">{fmt.n(Math.round(form.buyers / form.arrivalSeconds))} buyers arrive per second. About {fmt.n(Math.round(form.buyers * 2.5))} seats requested for {fmt.n(options.presets.find((p) => p.id === form.preset)?.seats ?? 0)} available.</p>
              <button className="primary" onClick={start} disabled={running || starting}>{running ? "Sale in progress…" : starting ? "Starting…" : "Open the sale"}</button>
            </>
          ) : <p className="muted">Loading options…</p>}

          {options && (
            <div className={`chaos ${live.phase === "selling" ? "" : "disabled"}`}>
              <h2>Break something</h2>
              <p className="muted">{live.phase === "selling" ? "Inject a real fault while buyers are active." : "Available while a sale is live."}</p>
              {options.chaos.map((c) => (
                <button key={c.id} className={`chaos-btn ${activeChaos.has(c.id) ? "active" : ""}`} disabled={live.phase !== "selling" || activeChaos.has(c.id)} onClick={() => chaos(c.id)} title={c.description}>
                  <b>{c.label}</b><small>{c.description}</small>
                </button>
              ))}
            </div>
          )}
          {error && <p className="error" role="alert">{error}</p>}
        </aside>

        <section className="panel stage">
          <div className="stage-head">
            <div>
              <h2>{live.run ? `${presetName[live.run.preset] ?? live.run.preset}` : "No sale yet"}</h2>
              <p className="muted">{total ? `${fmt.n(counts.sold)} sold · ${fmt.n(counts.held)} held · ${fmt.n(counts.free)} open${counts.conflict ? ` · ${fmt.n(counts.conflict)} double-booked` : ""}` : "Open a sale to build the venue."}</p>
            </div>
            {total > 0 && <b className="sold-pct">{fmt.pct((counts.sold / total) * 100)}<small>sold</small></b>}
          </div>
          {total > 0 && <SeatBar sold={counts.sold} held={counts.held} conflict={counts.conflict} total={total} />}
          {live.layout && board ? <SeatMap layout={live.layout} board={board} /> : <div className="map-empty">The venue appears here.</div>}
          <SeatLegend naive={mode === "naive"} />
          <div className="chart-wrap">
            <TrafficChart series={live.series} chaos={live.chaos} />
            <ChartLegend />
          </div>
        </section>

        <aside className="side">
          <div className="panel">
            <div className="stats">
              <Stat label="Requests / s" value={fmt.n(last?.requests)} sub={`peak ${fmt.n(Math.max(0, ...live.series.map((p) => p.requests)))}`} />
              <Stat label="Hold P99" value={fmt.ms(last?.holdP99)} sub={`p50 ${fmt.ms(last?.holdP50)}`} />
              <Stat label="In waiting room" value={fmt.n(live.queue?.waiting)} sub={`${fmt.n(live.queue?.admitted)} admitted`} />
              <Stat label="Holds won" value={fmt.n(won)} sub={`${fmt.n(lost)} lost races`} />
              <Stat label="Failed closed" value={fmt.n(failed)} sub="503 instead of unsafe write" tone={failed ? "warn" : undefined} />
              <Stat label="Revenue" value={fmt.usd(live.invariants?.revenueCents)} sub={`${fmt.n(live.invariants?.orders)} orders`} />
            </div>
          </div>
          <div className="panel">
            <h2>Correctness, live from PostgreSQL</h2>
            <InvariantPanel inv={live.invariants} mode={mode} />
          </div>
          <div className="panel feed-panel">
            <h2>Buyer activity</h2>
            <Feed items={feed} />
          </div>
        </aside>
      </div>

      {finished && <RunSummary run={finished} />}
      {finished && (
        <div className="after-actions">
          <Link href={`/runs/${finished.id}`} className="button">Watch the replay</Link>
          <Link href="/compare" className="button ghost">Compare engines side by side</Link>
        </div>
      )}
    </main>
  );
}

function Slider({ label, value, min, max, step, disabled, onChange, fmtv }: { label: string; value: number; min: number; max: number; step: number; disabled?: boolean; onChange: (v: number) => void; fmtv: (v: number) => string }) {
  return (
    <label className="slider">
      <span>{label}<b>{fmtv(value)}</b></span>
      <input type="range" min={min} max={max} step={step} value={value} disabled={disabled} onChange={(e) => onChange(Number(e.target.value))} />
    </label>
  );
}
