"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useEffect, useState } from "react";
import type { Run } from "@/lib/api";
import { fmt, getJSON, presetName } from "@/lib/api";
import { Nav } from "./Nav";
import { verdict } from "./RunSummary";
import { loadRun, PlayerBar, ReplayStage, timelineEnd, usePlayer, type LoadedRun } from "./Replay";

export function Compare({ a: wantA, b: wantB }: { a: string; b: string }) {
  const router = useRouter();
  const [runs, setRuns] = useState<Run[] | null>(null);
  const [left, setLeft] = useState<LoadedRun | null>(null);
  const [right, setRight] = useState<LoadedRun | null>(null);
  const [error, setError] = useState("");

  useEffect(() => {
    getJSON<{ runs: Run[] }>("/control/simulations").then((r) => setRuns(r.runs.filter((x) => x.hasTimeline))).catch((e) => setError(e.message));
  }, []);

  // Default to the latest FlashSeat run against the latest naive run.
  const a = wantA || runs?.find((r) => r.mode === "safe")?.id || "";
  const b = wantB || runs?.find((r) => r.mode === "naive")?.id || runs?.find((r) => r.id !== a)?.id || "";

  useEffect(() => {
    if (!a) return;
    loadRun(a).then(setLeft).catch((e) => setError(e.message));
  }, [a]);
  useEffect(() => {
    if (!b) return;
    loadRun(b).then(setRight).catch((e) => setError(e.message));
  }, [b]);

  const player = usePlayer([left, right].filter(Boolean).map((l) => timelineEnd(l!.timeline)));
  const choose = (side: "a" | "b", id: string) => router.replace(`/compare?a=${side === "a" ? id : a}&b=${side === "b" ? id : b}`);
  const label = (r: Run) => `${r.mode === "safe" ? "FlashSeat" : "Naive"} · ${presetName[r.preset] ?? r.preset} · ${fmt.n(r.buyers)} buyers · ${new Date(r.createdAt).toLocaleString(undefined, { month: "short", day: "numeric", hour: "numeric", minute: "2-digit" })}`;

  return (
    <main className="app">
      <Nav active="compare" />
      <section className="hero">
        <div>
          <h1>Same crowd. Two engines.</h1>
          <p>Both replays run on one clock. The naive engine reads availability and then writes, with no lock between the two; under real concurrency, buyers slip into that gap and the same seat is sold twice. Red squares are seats PostgreSQL recorded as sold to more than one buyer.</p>
        </div>
      </section>
      {error && <p className="error panel">{error}</p>}
      {runs && runs.length < 2 && <div className="panel empty"><h2>Record two runs first</h2><p className="muted">Run the FlashSeat engine and the naive baseline with the same settings, then come back.</p><Link className="button" href="/">Go to the live sale</Link></div>}
      {runs && runs.length >= 2 && (
        <>
          <section className="panel sticky-player"><PlayerBar player={player} /></section>
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
