"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import type { Run, Timeline } from "@/lib/api";
import { fmt, getJSON } from "@/lib/api";
import { Replayer } from "@/lib/board";
import { SeatLegend, SeatMap } from "./SeatMap";
import { ChartLegend, InvariantPanel, SeatBar, TrafficChart } from "./Panels";

export type LoadedRun = { run: Run; timeline: Timeline };

export async function loadRun(id: string): Promise<LoadedRun> {
  const [run, timeline] = await Promise.all([getJSON<Run>(`/control/simulations/${id}`), getJSON<Timeline>(`/control/simulations/${id}/timeline`)]);
  return { run, timeline };
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
  return { t, setT, playing, setPlaying, speed, setSpeed, end };
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

export function PlayerBar({ player }: { player: ReturnType<typeof usePlayer> }) {
  const { t, setT, playing, setPlaying, speed, setSpeed, end } = player;
  return (
    <div className="player">
      <button className="play" onClick={() => { if (t >= end) setT(0); setPlaying(!playing); }} aria-label={playing ? "Pause" : "Play"}>{playing ? "❚❚" : "▶"}</button>
      <input type="range" min={0} max={Math.max(1, end)} step={50} value={t} onChange={(e) => { setPlaying(false); setT(Number(e.target.value)); }} aria-label="Replay position" />
      <span className="clock">{fmt.clock(t)} / {fmt.clock(end)}</span>
      <div className="speeds">
        {[1, 2, 4, 8].map((s) => <button key={s} className={speed === s ? "on" : ""} onClick={() => setSpeed(s)}>{s}×</button>)}
      </div>
    </div>
  );
}

export function ReplayStage({ loaded, t, compact = false }: { loaded: LoadedRun; t: number; compact?: boolean }) {
  const { board, counts } = useReplayBoard(loaded.timeline, t);
  const inv = useMemo(() => invariantsAt(loaded.timeline, t), [loaded.timeline, t]);
  const total = loaded.timeline.layout.seats;
  return (
    <div className="replay-stage">
      <div className="stage-head">
        <div>
          <span className={`mode-tag ${loaded.run.mode}`}>{loaded.run.mode === "safe" ? "FlashSeat engine" : "Naive baseline"}</span>
          <p className="muted">{fmt.n(counts.sold)} sold · {fmt.n(counts.held)} held{counts.conflict ? ` · ${fmt.n(counts.conflict)} double-booked` : ""}</p>
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
