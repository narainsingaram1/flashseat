"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useCallback, useEffect, useRef, useState } from "react";
import type { Run } from "@/lib/api";
import { fmt, getJSON, presetName } from "@/lib/api";
import { Nav } from "./Nav";
import { verdict } from "./RunSummary";
import { loadRun, useReplayBoard, PlayerBar, ReplayStage, timelineEnd, usePlayer, type LoadedRun } from "./Replay";
import { EngineTag } from "./Nav";

export function Compare({ a: wantA, b: wantB }: { a: string; b: string }) {
  const router = useRouter();
  const [runs, setRuns] = useState<Run[] | null>(null);
  const [left, setLeft] = useState<LoadedRun | null>(null);
  const [right, setRight] = useState<LoadedRun | null>(null);
  const [error, setError] = useState("");

  useEffect(() => {
    getJSON<{ runs: Run[] }>("/control/simulations").then((r) => setRuns(r.runs.filter((x) => x.hasTimeline))).catch((e) => setError(e.message));
  }, []);

  // Default to a fair fight: the latest naive run against the latest FlashSeat
  // run that faced the same crowd, falling back to the latest of each engine.
  const pair = defaultPair(runs ?? []);
  const a = wantA || pair[0];
  const b = wantB || pair[1];

  const player = usePlayer([left, right].filter(Boolean).map((l) => timelineEnd(l!.timeline)));
  const { setPlaying, seek } = player;
  // Start playing once both sides have loaded, so the page opens in motion.
  const started = useRef("");
  const loadedIds = useRef<{ a?: string; b?: string }>({});
  const ready = useCallback((side: "a" | "b", l: LoadedRun) => {
    (side === "a" ? setLeft : setRight)(l);
    loadedIds.current[side] = l.run.id;
    const key = `${loadedIds.current.a}|${loadedIds.current.b}`;
    if (loadedIds.current.a === a && loadedIds.current.b === b && started.current !== key) {
      started.current = key;
      seek(0);
      setPlaying(true);
    }
  }, [a, b, seek, setPlaying]);
  useEffect(() => {
    if (!a) return;
    loadRun(a).then((l) => ready("a", l)).catch((e) => setError(e.message));
  }, [a, ready]);
  useEffect(() => {
    if (!b) return;
    loadRun(b).then((l) => ready("b", l)).catch((e) => setError(e.message));
  }, [b, ready]);

  const choose = (side: "a" | "b", id: string) => router.replace(`/compare?a=${side === "a" ? id : a}&b=${side === "b" ? id : b}`);
  const label = (r: Run) => `${r.mode === "safe" ? "FlashSeat" : "Naive"} · ${presetName[r.preset] ?? r.preset} · ${fmt.n(r.buyers)} buyers · ${new Date(r.createdAt).toLocaleString(undefined, { month: "short", day: "numeric", hour: "numeric", minute: "2-digit" })}`;

  return (
    <main className="app">
      <Nav active="compare" />
      <section className="intro">
        <div className="intro-copy">
          <h1>Same crowd, two engines</h1>
          <p>Both replays run on one clock. The naive engine reads availability and then writes, with no lock between the two; under real concurrency, buyers slip into that gap and the same seat is sold twice. Red squares are seats PostgreSQL recorded as sold to more than one buyer.</p>
          <p className="muted small">Fastest way to fill this page: Race both engines on the live sale.</p>
        </div>
      </section>
      {error && <p className="error panel">{error}</p>}
      {runs && runs.length < 2 && <div className="panel empty"><h2>Record two runs first</h2><p className="muted">Run the FlashSeat engine and the naive baseline with the same settings, then come back.</p><Link className="button" href="/">Go to the live sale</Link></div>}
      {runs && runs.length >= 2 && (
        <>
          <section className="panel sticky-player">
            <PlayerBar player={player} />
            <div className="scoreboard">
              {[left?.run.id === a ? left : null, right?.run.id === b ? right : null].map((l, i) => <Score key={i} loaded={l} t={player.t} />)}
            </div>
          </section>
          {left && right && left.run.id === a && right.run.id === b && !sameCrowd(left.run, right.run) && (
            <p className="crowd-warn small">These two runs faced different crowds, so the comparison is not like for like. Race both engines on the live sale for a fair pair.</p>
          )}
          <div className="compare">
            {([["a", a, left], ["b", b, right]] as const).map(([side, id, loaded]) => (
              <section key={side} className="panel">
                <select value={id} onChange={(e) => choose(side, e.target.value)} aria-label={`Run ${side.toUpperCase()}`}>
                  {runs.map((r) => <option key={r.id} value={r.id}>{label(r)}</option>)}
                </select>
                {loaded && loaded.run.id === id ? (
                  <>
                    <p className="verdict small">{verdict(loaded.run)}</p>
                    <ReplayStage loaded={loaded} t={player.t} compact />
                  </>
                ) : <p className="muted">Loading…</p>}
              </section>
            ))}
          </div>
        </>
      )}
    </main>
  );
}

function sameCrowd(x: Run, y: Run) {
  return x.preset === y.preset && x.buyers === y.buyers && x.arrivalSeconds === y.arrivalSeconds && x.admitRate === y.admitRate && x.holdTtlSeconds === y.holdTtlSeconds;
}

function defaultPair(runs: Run[]): [string, string] {
  const done = runs.filter((r) => r.status !== "running" && r.status !== "queued");
  for (const n of done.filter((r) => r.mode === "naive")) {
    const s = done.find((r) => r.mode === "safe" && sameCrowd(r, n));
    if (s) return [s.id, n.id];
  }
  const s = done.find((r) => r.mode === "safe") ?? done[0];
  const n = done.find((r) => r.mode === "naive" && r.id !== s?.id) ?? done.find((r) => r.id !== s?.id);
  return [s?.id ?? "", n?.id ?? ""];
}

/** One side of the head-to-head line in the sticky bar, at the replay clock. */
function Score({ loaded, t }: { loaded: LoadedRun | null; t: number }) {
  const { counts } = useReplayBoard(loaded?.timeline ?? null, t);
  if (!loaded) return <div className="score muted small">Loading…</div>;
  return (
    <div className="score">
      <EngineTag mode={loaded.run.mode} short />
      <span><b>{fmt.n(counts.sold)}</b> sold</span>
      <span className={counts.conflict ? "bad" : "good"}><b>{fmt.n(counts.conflict)}</b> double-booked</span>
    </div>
  );
}
