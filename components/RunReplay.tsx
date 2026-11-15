"use client";

import Link from "next/link";
import { useEffect, useState } from "react";
import { Nav } from "./Nav";
import { RunSummary } from "./RunSummary";
import { loadRun, PlayerBar, ReplayStage, timelineEnd, usePlayer, type LoadedRun } from "./Replay";

export function RunReplay({ id }: { id: string }) {
  const [loaded, setLoaded] = useState<LoadedRun | null>(null);
  const [error, setError] = useState("");
  const player = usePlayer(loaded ? [timelineEnd(loaded.timeline)] : []);
  const { setPlaying } = player;
  useEffect(() => {
    loadRun(id).then((l) => { setLoaded(l); setPlaying(true); }).catch((e) => setError(e.message));
  }, [id, setPlaying]);

  return (
    <main className="app">
      <Nav active="runs" />
      {error && <div className="panel empty"><h2>Replay unavailable</h2><p className="error">{error}</p><Link href="/runs" className="button">Back to runs</Link></div>}
      {!loaded && !error && <div className="panel empty"><p className="muted">Loading replay…</p></div>}
      {loaded && (
        <>
          <RunSummary run={loaded.run} />
          <section className="panel">
            <PlayerBar player={player} />
            <ReplayStage loaded={loaded} t={player.t} />
          </section>
        </>
      )}
    </main>
  );
}
