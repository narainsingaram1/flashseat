"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useEffect, useState } from "react";
import type { Run } from "@/lib/api";
import { fmt, getJSON, presetName } from "@/lib/api";
import { EngineTag, Nav } from "./Nav";

export function RunList() {
  const [runs, setRuns] = useState<Run[] | null>(null);
  const [error, setError] = useState("");
  const [picked, setPicked] = useState<string[]>([]);
  const router = useRouter();

  useEffect(() => {
    getJSON<{ runs: Run[] }>("/control/simulations").then((r) => setRuns(r.runs)).catch((e) => setError(e.message));
  }, []);

  const toggle = (id: string) => setPicked((p) => (p.includes(id) ? p.filter((x) => x !== id) : [...p.slice(-1), id]));
  const done = runs?.filter((r) => r.status === "succeeded") ?? [];
  const safe = done.filter((r) => r.mode === "safe");
  const totalSold = safe.reduce((a, r) => a + (r.invariants?.final?.seatsSold ?? 0), 0);
  const totalDup = safe.reduce((a, r) => a + (r.invariants?.final?.doubleSold ?? 0), 0);
  const totalBuyers = done.reduce((a, r) => a + r.buyers, 0);

  return (
    <main className="app">
      <Nav active="runs" />
      <section className="intro">
        <div className="intro-copy">
          <h1>Every sale, recorded</h1>
          <p>Each run keeps its full seat-by-seat timeline, server telemetry, the k6 client summary, and the final PostgreSQL invariant check. Tick two to replay them side by side.</p>
        </div>
        {runs && (
          <div className="totals">
            <div><b>{fmt.n(done.length)}</b><span>completed runs</span></div>
            <div><b>{fmt.n(totalBuyers)}</b><span>simulated buyers</span></div>
            <div><b>{fmt.n(totalSold)}</b><span>seats sold by FlashSeat</span></div>
            <div className={totalDup ? "bad" : "good"}><b>{fmt.n(totalDup)}</b><span>FlashSeat double-sales</span></div>
          </div>
        )}
      </section>
      {error && <p className="error panel">{error}</p>}
      {runs && runs.length === 0 && <div className="panel empty"><h2>No runs yet</h2><p className="muted">Open a sale from the live page to record the first one.</p><Link className="button" href="/">Go to the live sale</Link></div>}
      {runs && runs.length > 0 && (
        <section className="panel">
          <div className="table-head">
            <h2>Runs</h2>
            <button className="primary" disabled={picked.length !== 2} onClick={() => router.push(`/compare?a=${picked[0]}&b=${picked[1]}`)}>
              {picked.length === 2 ? "Compare these two" : `Select ${2 - picked.length} more to compare`}
            </button>
          </div>
          <div className="table-scroll">
            <table className="runs">
              <thead>
                <tr><th /><th>When</th><th>Engine</th><th>Venue</th><th>Buyers</th><th>Sold</th><th>Sold out in</th><th>Sold twice</th><th>Peak req/s</th><th>Hold P99</th><th /></tr>
              </thead>
              <tbody>
                {runs.map((r) => {
                  const inv = r.invariants?.final, m = r.metrics, k = r.k6Summary || {};
                  return (
                    <tr key={r.id} className={picked.includes(r.id) ? "picked" : ""}>
                      <td><input type="checkbox" aria-label="Select for comparison" disabled={!r.hasTimeline} checked={picked.includes(r.id)} onChange={() => toggle(r.id)} /></td>
                      <td>{new Date(r.createdAt).toLocaleString(undefined, { month: "short", day: "numeric", hour: "numeric", minute: "2-digit" })}</td>
                      <td><EngineTag mode={r.mode} short />{r.status !== "succeeded" && <span className="status">{r.status}</span>}</td>
                      <td>{presetName[r.preset] ?? r.preset}</td>
                      <td>{fmt.n(r.buyers)}</td>
                      <td>{inv ? `${fmt.n(inv.seatsSold)} / ${fmt.n(r.seats)}` : "—"}</td>
                      <td>{m.soldOutMs !== undefined ? fmt.secs(m.soldOutMs) : "—"}</td>
                      <td className={inv && inv.doubleSold > 0 ? "bad" : "good"}>{inv ? fmt.n(inv.doubleSold) : "—"}</td>
                      <td>{fmt.n(m.peakRequestsPerSec)}</td>
                      <td>{fmt.ms(k.holdP99Ms ?? m.holdP99Ms)}</td>
                      <td>{r.hasTimeline ? <Link href={`/runs/${r.id}`} className="link">Replay</Link> : <span className="muted">{r.failureMessage ? "failed" : "—"}</span>}</td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        </section>
      )}
    </main>
  );
}
