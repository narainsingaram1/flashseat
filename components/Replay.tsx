"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { Run, Timeline } from "@/lib/api";
import { fmt, getJSON } from "@/lib/api";
import { Replayer } from "@/lib/board";
import { SeatLegend, SeatMap } from "./SeatMap";
import { ChartLegend, InvariantPanel, SeatBar, TrafficChart } from "./Panels";
import { EngineTag } from "./Nav";

export type LoadedRun = { run: Run; timeline: Timeline };

export async function loadRun(id: string): Promise<LoadedRun> {
  const [run, timeline] = await Promise.all([getJSON<Run>(`/control/simulations/${id}`), getJSON<Timeline>(`/control/simulations/${id}/timeline`)]);
  // Go encodes an empty slice as null when a run had no faults injected.
  return { run: { ...run, chaos: run.chaos ?? [] }, timeline: { ...timeline, chaos: timeline.chaos ?? [], series: timeline.series ?? [], frames: timeline.frames ?? [] } };
}

/** Plays one or more recorded runs on a shared clock. */
export function usePlayer(durations: number[]) {
  const end = Math.max(0, ...durations);
  const [t, setT] = useState(0);
  const [playing, setPlaying] = useState(false);
  const [speed, setSpeed] = useState(2);
  const last = useRef(0);
  useEffect(() => {
    if (!playing) return;
    let raf = 0;
    last.current = performance.now();
    const step = (now: number) => {
      const dt = (now - last.current) * speed;
      last.current = now;
      setT((v) => {
        const next = Math.min(end, v + dt);
        if (next >= end) setPlaying(false);
        return next;
      });
      raf = requestAnimationFrame(step);
    };
    raf = requestAnimationFrame(step);
    return () => cancelAnimationFrame(raf);
  }, [playing, speed, end]);

  const toggle = useCallback(() => {
    if (!playing && t >= end) setT(0);
    setPlaying(!playing);
  }, [playing, t, end]);
  const restart = useCallback(() => { setT(0); setPlaying(true); }, []);
  const seekBy = useCallback((ms: number) => setT((v) => Math.max(0, Math.min(end, v + ms))), [end]);
  const seek = useCallback((ms: number) => setT(Math.max(0, Math.min(end, ms))), [end]);
  return { t, playing, setPlaying, speed, setSpeed, end, toggle, restart, seekBy, seek };
}

export type Player = ReturnType<typeof usePlayer>;

const SPEEDS = [1, 2, 4, 8];
const Icon = ({ d }: { d: string }) => <svg viewBox="0 0 16 16" width="16" height="16" aria-hidden="true" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round"><path d={d} /></svg>;

/** Transport for a replay: restart, step, play/pause, scrub, speed, plus keyboard shortcuts. */
export function PlayerBar({ player }: { player: Player }) {
  const { t, playing, speed, setSpeed, end, toggle, restart, seekBy, seek, setPlaying } = player;
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const el = e.target as HTMLElement | null;
      if (e.metaKey || e.ctrlKey || e.altKey || el?.closest("input:not([type=range]), select, textarea")) return;
      if (e.key === " " || e.key === "k") { e.preventDefault(); toggle(); }
      else if (e.key === "ArrowLeft" || e.key === "j") { e.preventDefault(); seekBy(-5000); }
      else if (e.key === "ArrowRight" || e.key === "l") { e.preventDefault(); seekBy(5000); }
      else if (e.key === "r" || e.key === "Home") { e.preventDefault(); restart(); }
      else if (e.key === "End") { e.preventDefault(); setPlaying(false); seek(end); }
      else if (e.key >= "1" && e.key <= "4") setSpeed(SPEEDS[Number(e.key) - 1]);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [toggle, seekBy, restart, seek, setSpeed, setPlaying, end]);

  const pct = end ? (t / end) * 100 : 0;
  return (
    <div className="player">
      <div className="transport">
        <button className="tbtn" onClick={restart} aria-label="Restart" title="Restart (R)"><Icon d="M3 8a5 5 0 1 0 1.5-3.5M3 2.5V5h2.5" /></button>
        <button className="tbtn" onClick={() => seekBy(-5000)} aria-label="Back 5 seconds" title="Back 5s (←)"><Icon d="M7.5 4 3.5 8l4 4M12.5 4l-4 4 4 4" /></button>
        <button className="play" onClick={toggle} aria-label={playing ? "Pause" : t >= end && end > 0 ? "Replay from start" : "Play"} title="Play / pause (Space)">
          {playing
            ? <svg viewBox="0 0 16 16" width="16" height="16" aria-hidden="true"><rect x="3.5" y="3" width="3" height="10" rx="1" fill="currentColor" /><rect x="9.5" y="3" width="3" height="10" rx="1" fill="currentColor" /></svg>
            : <svg viewBox="0 0 16 16" width="16" height="16" aria-hidden="true"><path d="M5 3.2v9.6a.6.6 0 0 0 .9.5l7.6-4.8a.6.6 0 0 0 0-1L5.9 2.7a.6.6 0 0 0-.9.5Z" fill="currentColor" /></svg>}
        </button>
        <button className="tbtn" onClick={() => seekBy(5000)} aria-label="Forward 5 seconds" title="Forward 5s (→)"><Icon d="M3.5 4l4 4-4 4M8.5 4l4 4-4 4" /></button>
      </div>
      <input type="range" min={0} max={Math.max(1, end)} step={50} value={t} style={{ "--p": `${pct}%` } as React.CSSProperties}
        onChange={(e) => { setPlaying(false); seek(Number(e.target.value)); }} aria-label="Replay position" />
      <span className="clock">{fmt.clock(t)} / {fmt.clock(end)}</span>
      <div className="speeds" role="group" aria-label="Playback speed">
        {SPEEDS.map((s) => <button key={s} className={speed === s ? "on" : ""} onClick={() => setSpeed(s)} aria-pressed={speed === s}>{s}×</button>)}
      </div>
      <p className="keys" aria-hidden="true"><span><kbd>Space</kbd> play or pause</span><span><kbd>←</kbd><kbd>→</kbd> 5 seconds</span><span><kbd>R</kbd> restart</span><span><kbd>1</kbd>–<kbd>4</kbd> speed</span></p>
    </div>
  );
}

/** Keeps a board in sync with a replay clock. */
export function useReplayBoard(timeline: Timeline | null, t: number) {
  const replayer = useMemo(() => (timeline ? new Replayer(timeline.frames, timeline.layout.seats) : null), [timeline]);
  const counts = useMemo(() => replayer?.seek(t) ?? { free: 0, held: 0, sold: 0, conflict: 0 }, [replayer, t]);
  return { board: replayer?.board ?? null, counts };
}

export function invariantsAt(timeline: Timeline, t: number) {
  let inv = null;
  for (const f of timeline.frames) {
    if (f.t > t) break;
    if (f.i) inv = f.i;
  }
  return inv;
}

export function ReplayStage({ loaded, t, compact = false }: { loaded: LoadedRun; t: number; compact?: boolean }) {
  const { board, counts } = useReplayBoard(loaded.timeline, t);
  const inv = useMemo(() => invariantsAt(loaded.timeline, t), [loaded.timeline, t]);
  const total = loaded.timeline.layout.seats;
  return (
    <div className="replay-stage">
      <div className="stage-head">
        <div>
          <EngineTag mode={loaded.run.mode} />
          <p className="map-meta">{fmt.n(counts.sold)} sold · {fmt.n(counts.held)} held{counts.conflict ? ` · ${fmt.n(counts.conflict)} double-booked` : ""}</p>
        </div>
        <b className="sold-pct">{fmt.pct((counts.sold / total) * 100)}<small>sold</small></b>
      </div>
      <SeatBar sold={counts.sold} held={counts.held} conflict={counts.conflict} total={total} />
      {board && <SeatMap layout={loaded.timeline.layout} board={board} compact={compact} />}
      <SeatLegend naive={loaded.run.mode === "naive"} />
      <TrafficChart series={loaded.timeline.series} chaos={loaded.timeline.chaos} playhead={t} height={compact ? 110 : 150} />
      {!compact && <ChartLegend />}
      <InvariantPanel inv={inv} mode={loaded.run.mode} />
    </div>
  );
}

export function timelineEnd(tl: Timeline) {
  return Math.max(tl.frames.length ? tl.frames[tl.frames.length - 1].t : 0, tl.series.length ? tl.series[tl.series.length - 1].t : 0);
}
