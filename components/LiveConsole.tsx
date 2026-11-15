"use client";

import Link from "next/link";
import { useCallback, useEffect, useRef, useState } from "react";
import { API, fmt, getJSON, presetName } from "@/lib/api";
import type { ChaosAction, ChaosMark, Frame, Invariants, Layout, Phase, Point, PresetOption, Queue, Run, RunInput, Sample, Snapshot } from "@/lib/api";
import { applyDeltas, countStates, createBoard, type Board } from "@/lib/board";
import { SeatLegend, SeatMap } from "./SeatMap";
import { ChartLegend, Feed, InvariantPanel, SeatBar, Stat, TrafficChart } from "./Panels";
import { EngineTag, Nav, PhasePill } from "./Nav";
import { RunSummary, verdict } from "./RunSummary";

const MODES = {
  safe: { title: "FlashSeat engine", body: "Redis claims the block atomically, then one PostgreSQL function locks rows in order and commits" },
  naive: { title: "Naive baseline", body: "Read what's free, then insert. No lock, no transaction, no unique constraint" },
};

/** The benchmark crowd: enough pressure, with no waiting room, for the naive engine to oversell. */
const RACE_CROWD = { preset: "arena", buyers: 6000, arrivalSeconds: 15, admitRate: 0, holdTtlSeconds: 10 };

type Live = {
  run: Run | null; phase: Phase; layout: Layout | null; series: Point[]; chaos: ChaosMark[]; invariants: Invariants | null;
  queue: Queue | null; totals: Record<string, number>; t0: number; tAt: number;
};

/** One click runs the same crowd through both engines, one after the other. */
type Race = { input: RunInput; stage: "safe" | "naive" | "done"; safeId?: string; naiveId?: string; safe?: Run; naive?: Run };

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
  const [tuning, setTuning] = useState(false);
  const [error, setError] = useState("");
  const [starting, setStarting] = useState(false);
  const [now, setNow] = useState(0);
  const [finished, setFinished] = useState<Run | null>(null);
  const [history, setHistory] = useState<Run[] | null>(null);
  const [race, setRace] = useState<Race | null>(null);
  const stageRef = useRef<HTMLElement>(null);

  useEffect(() => {
    getJSON<{ presets: PresetOption[]; chaos: ChaosAction[] }>("/control/options")
      .then((o) => { setOptions(o); setForm((f) => f ?? o.presets.find((p) => p.id === "club")?.defaults ?? o.presets[0].defaults); })
      .catch(() => setError("Can't reach the control plane. Start the stack with docker compose up --build, then refresh"));
  }, []);

  const loadHistory = useCallback(() => {
    getJSON<{ runs: Run[] }>("/control/simulations").then((r) => setHistory(r.runs)).catch(() => {});
  }, []);
  useEffect(loadHistory, [loadHistory]);

  const onSnapshot = useCallback((s: Snapshot) => {
    const b = s.layout ? createBoard(s.layout.seats, s.states) : null;
    boardRef.current = b;
    setBoard(b);
    if (b) setCounts(countStates(b));
    setLive({ run: s.run, phase: s.phase, layout: s.layout, series: s.series ?? [], chaos: s.chaos ?? [], invariants: s.invariants, queue: s.queue, totals: s.totals || {}, t0: s.t, tAt: performance.now() });
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

  const launch = useCallback(async (input: RunInput) => {
    setStarting(true);
    setError("");
    setFeed([]);
    setFinished(null);
    try {
      const run = await getJSON<Run>("/control/simulations", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(input) });
      stageRef.current?.scrollIntoView({ behavior: "smooth", block: "start" });
      return run;
    } catch (e) {
      setError(e instanceof Error ? e.message : "Could not start the sale");
      return null;
    } finally {
      setStarting(false);
    }
  }, []);

  // The race lives in a ref as well as state, so the run loader below always
  // sees the latest leg without re-subscribing.
  const raceRef = useRef<Race | null>(null);
  const updateRace = useCallback((next: Race | null) => {
    raceRef.current = next;
    setRace(next);
  }, []);

  const onRunDone = useCallback((r: Run) => {
    const cur = raceRef.current;
    if (!cur) return;
    if (cur.stage === "safe" && r.id === cur.safeId) {
      const next: Race = { ...cur, stage: "naive", safe: r };
      updateRace(next);
      launch({ ...cur.input, mode: "naive" }).then((run) => {
        const latest = raceRef.current ?? next;
        updateRace(run ? { ...latest, naiveId: run.id } : { ...latest, stage: "done" });
      });
    } else if (cur.stage === "naive" && r.id === cur.naiveId) {
      updateRace({ ...cur, stage: "done", naive: r });
    }
  }, [launch, updateRace]);

  const runId = live.run?.id;
  useEffect(() => {
    if ((live.phase !== "done" && live.phase !== "failed") || !runId) return;
    let cancelled = false;
    const load = () => getJSON<Run>(`/control/simulations/${runId}`).then((r) => {
      if (cancelled) return;
      if (r.status === "running") setTimeout(load, 800);
      else { setFinished(r); loadHistory(); onRunDone(r); }
    }).catch(() => {});
    load();
    return () => { cancelled = true; };
  }, [live.phase, runId, loadHistory, onRunDone]);

  const running = live.phase === "preparing" || live.phase === "selling" || live.phase === "settling";
  const racing = !!race && race.stage !== "done";

  async function start() {
    if (!form) return;
    updateRace(null);
    await launch(form);
  }

  async function startRace() {
    if (!form) return;
    const input: RunInput = { ...RACE_CROWD, mode: "safe" };
    updateRace({ input, stage: "safe" });
    const run = await launch(input);
    const cur = raceRef.current;
    updateRace(run && cur ? { ...cur, safeId: run.id } : null);
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
  const venueSeats = options?.presets.find((p) => p.id === form?.preset)?.seats ?? 0;
  const busy = running || starting || racing;

  return (
    <main className="app">
      <Nav active="live" status={<PhasePill phase={live.phase} connected={connected} />} />

      <section className="intro">
        <div className="intro-copy">
          <h1>A ticket on-sale you can watch, and break</h1>
          <p>Thousands of real HTTP buyers race for the same seats. A Go engine on Redis and PostgreSQL has to sell each seat exactly once, even while you cut Redis or kill a server mid-sale.</p>
          <ol className="steps">
            <li><span className="step-n">1</span>Pick an engine and a venue</li>
            <li><span className="step-n">2</span>Open the sale and watch the seats fill</li>
            <li><span className="step-n">3</span>Break something while buyers are active</li>
          </ol>
        </div>
        <Proof runs={history} />
      </section>

      <section className="panel command" aria-label="Configure the sale">
        {form && options ? (
          <>
            <div className="command-row">
              <div className="field engine-field">
                <span className="field-label">Engine</span>
                <div className="engines" role="radiogroup" aria-label="Reservation engine">
                  {(["safe", "naive"] as const).map((m) => (
                    <button key={m} role="radio" aria-checked={form.mode === m} className={`engine ${m} ${form.mode === m ? "on" : ""}`} disabled={busy} onClick={() => setForm({ ...form, mode: m })}>
                      <span className="engine-title"><i className="sq" />{MODES[m].title}</span>
                      <small>{MODES[m].body}</small>
                    </button>
                  ))}
                </div>
              </div>
              <div className="field">
                <span className="field-label">Venue</span>
                <div className="seg" role="radiogroup" aria-label="Venue">
                  {options.presets.map((p) => (
                    <button key={p.id} role="radio" aria-checked={form.preset === p.id} className={form.preset === p.id ? "on" : ""} disabled={busy} onClick={() => choosePreset(p.id)}>
                      <span>{p.name.replace(/^The /, "")}</span><small>{fmt.n(p.seats)} seats</small>
                    </button>
                  ))}
                </div>
              </div>
              <div className="field crowd-field">
                <span className="field-label">Crowd</span>
                <p className="crowd">
                  <b>{fmt.n(form.buyers)}</b> buyers over <b>{form.arrivalSeconds} s</b>, {form.admitRate ? <>queue admits <b>{form.admitRate}/s</b></> : <b>no queue</b>}, <b>{form.holdTtlSeconds} s</b> holds
                </p>
                <button className="link" onClick={() => setTuning(!tuning)} aria-expanded={tuning}>{tuning ? "Done" : "Adjust"}</button>
              </div>
              <div className="actions">
                <button className="primary" onClick={start} disabled={busy}>{running ? "Sale in progress" : starting ? "Starting" : "Open the sale"}</button>
                <button className="secondary" onClick={startRace} disabled={busy} title="Runs the benchmark crowd (6,000 buyers, no waiting room) through the FlashSeat engine, then the naive baseline. About 90 seconds">Race both engines</button>
              </div>
            </div>
            {tuning && (
              <div className="tuning">
                <Slider label="Buyers" value={form.buyers} min={100} max={12000} step={100} disabled={busy} onChange={(v) => setForm({ ...form, buyers: v })} fmtv={fmt.n} />
                <Slider label="Arrival window" value={form.arrivalSeconds} min={2} max={60} step={1} disabled={busy} onChange={(v) => setForm({ ...form, arrivalSeconds: v })} fmtv={(v) => `${v} s`} />
                <Slider label="Waiting room admits" value={form.admitRate} min={0} max={1500} step={10} disabled={busy} onChange={(v) => setForm({ ...form, admitRate: v })} fmtv={(v) => (v === 0 ? "everyone" : `${v}/s`)} />
                <Slider label="Hold expires after" value={form.holdTtlSeconds} min={3} max={60} step={1} disabled={busy} onChange={(v) => setForm({ ...form, holdTtlSeconds: v })} fmtv={(v) => `${v} s`} />
                <p className="hint">{fmt.n(Math.round(form.buyers / form.arrivalSeconds))} buyers arrive per second, asking for about {fmt.n(Math.round(form.buyers * 2.5))} seats against {fmt.n(venueSeats)} available</p>
              </div>
            )}
          </>
        ) : <p className="muted">{error ? "" : "Loading options"}</p>}
        {error && <p className="error" role="alert">{error}</p>}
      </section>

      {race && <RaceStrip race={race} />}

      <div className="console">
        <section className="panel stage" ref={stageRef}>
          <div className="stage-head">
            <div className="stage-title">
              <h2>{live.run ? presetName[live.run.preset] ?? live.run.preset : "No sale yet"}</h2>
              {live.run && <EngineTag mode={live.run.mode} />}
              {live.run && <span className="meta">{fmt.n(live.run.buyers)} buyers</span>}
              {live.run && <span className="clock">{fmt.clock(elapsed)}</span>}
            </div>
            {total > 0 && <b className="sold-pct">{fmt.pct((counts.sold / total) * 100)}<small>sold</small></b>}
          </div>

          {options && (
            <div className={`faults ${live.phase === "selling" ? "armed" : ""}`}>
              <div className="faults-head">
                <span>Break something</span>
                <small>{live.phase === "selling" ? "Inject a real fault while buyers are active" : "Available once a sale is live"}</small>
              </div>
              <div className="fault-list">
                {options.chaos.map((c) => (
                  <button key={c.id} className={`fault ${activeChaos.has(c.id) ? "active" : ""}`} disabled={live.phase !== "selling" || activeChaos.has(c.id)} onClick={() => chaos(c.id)} title={c.description}>
                    <span>{c.label}</span>
                    <small>{c.description}</small>
                  </button>
                ))}
              </div>
            </div>
          )}

          {total > 0 && <SeatBar sold={counts.sold} held={counts.held} conflict={counts.conflict} total={total} />}
          {total > 0 && <p className="map-meta">{fmt.n(counts.sold)} sold · {fmt.n(counts.held)} held · {fmt.n(counts.free)} open{counts.conflict ? <span className="bad"> · {fmt.n(counts.conflict)} double-booked</span> : null}</p>}
          {live.layout && board ? <SeatMap layout={live.layout} board={board} /> : (
            <div className="map-empty">
              <p>Every square here is a real seat row in PostgreSQL</p>
              <small>Open a sale to build the venue</small>
            </div>
          )}
          <SeatLegend naive={mode === "naive"} />
          <div className="chart-wrap">
            <div className="chart-head"><span>Traffic by outcome</span><ChartLegend /></div>
            <TrafficChart series={live.series} chaos={live.chaos} />
          </div>
        </section>

        <aside className="side">
          <div className="panel">
            <h2>Correctness, live from PostgreSQL</h2>
            <InvariantPanel inv={live.invariants} mode={mode} />
          </div>
          <div className="panel">
            <h2>Under load</h2>
            <div className="stats">
              <Stat label="Requests per second" value={fmt.n(last?.requests)} sub={`peak ${fmt.n(Math.max(0, ...live.series.map((p) => p.requests)))}`} />
              <Stat label="Hold latency P99" value={fmt.ms(last?.holdP99)} sub={`p50 ${fmt.ms(last?.holdP50)}`} />
              <Stat label="In waiting room" value={fmt.n(live.queue?.waiting)} sub={`${fmt.n(live.queue?.admitted)} admitted`} />
              <Stat label="Holds won" value={fmt.n(won)} sub={`${fmt.n(lost)} lost races`} />
              <Stat label="Failed closed" value={fmt.n(failed)} sub="503 instead of an unsafe write" tone={failed ? "warn" : undefined} />
              <Stat label="Revenue" value={fmt.usd(live.invariants?.revenueCents)} sub={`${fmt.n(live.invariants?.orders)} orders`} />
            </div>
          </div>
          <div className="panel feed-panel">
            <h2>Buyer activity</h2>
            <Feed items={feed} />
          </div>
        </aside>
      </div>

      {finished && !racing && (
        <>
          <RunSummary run={finished} />
          <div className="after-actions">
            <Link href={`/runs/${finished.id}`} className="button">Watch the replay</Link>
            <Link href="/compare" className="button ghost">Compare engines side by side</Link>
          </div>
        </>
      )}
    </main>
  );
}

/** Headline result, totalled over every recorded run of each engine. */
function Proof({ runs }: { runs: Run[] | null }) {
  const done = runs?.filter((r) => r.status === "succeeded" && r.invariants?.final) ?? [];
  const row = (mode: "safe" | "naive") => {
    const mine = done.filter((r) => r.mode === mode);
    const dup = mine.reduce((a, r) => a + (r.invariants!.final!.doubleSold ?? 0), 0);
    const sold = mine.reduce((a, r) => a + (r.invariants!.final!.seatsSold ?? 0), 0);
    return (
      <div className={`proof-row ${mode}`}>
        <EngineTag mode={mode} />
        {mine.length ? (
          <>
            <b className={dup ? "bad" : "good"}>{fmt.n(dup)}</b>
            <span>seats sold twice, across {fmt.n(mine.length)} {mine.length === 1 ? "run" : "runs"} and {fmt.n(sold)} seats sold</span>
          </>
        ) : <span className="muted">No recorded run yet</span>}
      </div>
    );
  };
  const hasNaive = done.some((r) => r.mode === "naive");
  return (
    <aside className="proof" aria-label="Recorded results">
      <span className="proof-label">Every recorded run, checked in PostgreSQL</span>
      {row("safe")}
      {row("naive")}
      {hasNaive ? <Link href="/runs" className="link">See every run</Link> : <span className="proof-hint">Race both engines to fill this in</span>}
    </aside>
  );
}

function RaceStrip({ race }: { race: Race }) {
  const leg = (mode: "safe" | "naive", run?: Run) => {
    const state = run ? "done" : race.stage === mode ? "live" : "waiting";
    return (
      <div className={`race-leg ${state}`}>
        <EngineTag mode={mode} />
        <span>{run ? verdict(run) : state === "live" ? "Selling now" : "Up next, same crowd"}</span>
      </div>
    );
  };
  return (
    <section className="panel race" aria-label="Engine race">
      <div className="race-head">
        <div>
          <h2>Race: same crowd, two engines</h2>
          <p className="meta">{presetName[race.input.preset]} · {fmt.n(race.input.buyers)} buyers over {race.input.arrivalSeconds} s · no waiting room · {race.input.holdTtlSeconds} s holds</p>
        </div>
        {race.stage === "done" && race.safe && race.naive && <Link href={`/compare?a=${race.safe.id}&b=${race.naive.id}`} className="button">Replay side by side</Link>}
      </div>
      <div className="race-legs">
        {leg("safe", race.safe)}
        {leg("naive", race.naive)}
      </div>
    </section>
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
