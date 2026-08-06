"use client";

import Link from "next/link";
import { useEffect, useState } from "react";

const API = process.env.NEXT_PUBLIC_API_URL || "http://localhost:8080/api/v1";
type Job = { id:string;status:string;virtualUsers:number;requests:number;winners:number;conflicts:number;unexpectedErrors:number;barrierMisses:number;databaseActiveClaims:number;doubleBookings:number;throughputRps:number;p95Ms:number;p99Ms:number;invariantPassed:boolean;k6Version:string;targetUrl:string;failureMessage?:string;createdAt:string;startedAt?:string;completedAt?:string };
type Results = { totalRuns:number;totalRequests:number;totalConflicts:number;totalErrors:number;passedRuns:number;doubleBookings:number;averageP99Ms:number;peakThroughputRps:number;maxVirtualUsers:number;recent:Job[] };

function wallDuration(run: Job) {
  if (!run.startedAt || !run.completedAt) return "pending";
  const seconds = Math.round((new Date(run.completedAt).getTime() - new Date(run.startedAt).getTime()) / 1000);
  return seconds < 60 ? `${seconds}s` : `${Math.floor(seconds / 60)}m ${seconds % 60}s`;
}

export function ResultsDashboard() {
  const [data,setData]=useState<Results|null>(null),[error,setError]=useState("");
  useEffect(()=>{fetch(`${API}/benchmarks/results`).then(r=>r.json()).then(v=>{if(v.code)throw new Error(v.message);setData(v)}).catch(e=>setError(e.message))},[]);
  const completed=data?.recent.filter(r=>r.status==="succeeded"||r.status==="failed").length||0;
  const passRate=completed&&data?Math.round(data.passedRuns/completed*100):0;
  return <main className="results-page">
    <header className="nav shell"><Link href="/" className="brand"><span>F</span> FLASHSEAT</Link><div className="edition">MEASURED EVIDENCE</div><nav className="nav-links"><Link href="/">RUN HTTP TEST</Link></nav></header>
    <section className="results-hero shell"><p className="eyebrow"><span className="live-dot"/>PERSISTED K6 + DATABASE RESULTS</p><h1>Claims require<br/><em>receipts.</em></h1><p>Every accepted row below came from an external K6 process calling the real REST API. Latency comes from K6; correctness comes from PostgreSQL queried before cleanup.</p></section>
    {error&&<div className="shell results-error">{error}</div>}
    {!data&&!error&&<div className="shell results-loading">Loading HTTP benchmark evidence…</div>}
    {data&&<>
      <section className="scoreboard"><div className="shell score-grid"><div className="hero-score"><span>DOUBLE-BOOKINGS</span><b>{data.doubleBookings}</b><small>database-observed duplicates</small></div><div><span>ACCEPTED RUNS</span><b>{data.totalRuns.toLocaleString()}</b></div><div><span>SEAT ATTEMPTS</span><b>{data.totalRequests.toLocaleString()}</b></div><div><span>PASS RATE</span><b>{passRate}%</b></div><div><span>LARGEST CROWD</span><b>{data.maxVirtualUsers.toLocaleString()}</b></div><div><span>PEAK BURST RATE</span><b>{data.peakThroughputRps.toFixed(0)} rps</b></div></div></section>
      {data.totalRuns===0?<section className="empty-results shell"><b>NO HTTP RUNS YET</b><h2>Create the first real benchmark.</h2><p>The previous in-process simulations are intentionally excluded from this evidence page.</p><Link href="/">RUN FIRST HTTP TEST →</Link></section>:
      <section className="history shell"><div className="history-head"><div><p className="kicker">AUDITABLE JOB HISTORY</p><h2>External HTTP runs</h2></div><Link href="/">+ NEW BENCHMARK</Link></div>
        <div className="run-table benchmark-table"><div className="table-row table-header"><span>STATUS</span><span>CLIENTS</span><span>WIN / 409</span><span>P95 / P99</span><span>BURST RATE</span><span>DB CLAIMS</span><span>WHEN</span></div>
          {data.recent.map(run=><details className="job-row" key={run.id}><summary className="table-row"><span><i className={run.invariantPassed?"run-pass":"run-fail"}/>{run.status.toUpperCase()}</span><strong>{run.virtualUsers.toLocaleString()}</strong><span>{run.winners} / {run.conflicts.toLocaleString()}</span><span>{run.p95Ms.toFixed(1)} / {run.p99Ms.toFixed(1)} ms</span><span>{run.throughputRps.toFixed(0)} rps</span><span>{run.databaseActiveClaims} ({run.doubleBookings} dupes)</span><span>{new Date(run.createdAt).toLocaleString(undefined,{month:"short",day:"numeric",hour:"numeric",minute:"2-digit"})}</span></summary><div className="job-detail"><span><b>JOB ID</b>{run.id}</span><span><b>RUNNER</b>{run.k6Version||"pending"}</span><span><b>TARGET</b>{run.targetUrl||"pending"}</span><span><b>JOB WALL TIME</b>{wallDuration(run)}</span><span><b>BARRIER MISSES</b>{run.barrierMisses}</span><span><b>UNEXPECTED ERRORS</b>{run.unexpectedErrors}</span>{run.failureMessage&&<p>{run.failureMessage}</p>}</div></details>)}
        </div>
        <div className="resume-proof"><span>EVIDENCE-BASED SUMMARY</span><p>Measured <b>{data.totalRequests.toLocaleString()} real seat-hold HTTP attempts</b> with a largest synchronized crowd of <b>{data.maxVirtualUsers.toLocaleString()}</b>, peak observed burst completion rate of <b>{data.peakThroughputRps.toFixed(0)} attempts/sec</b>, and <b>{data.doubleBookings} database-observed double-bookings</b>.</p></div>
        <p className="honesty-note">Burst rate is attempts divided by the slowest hold request in the synchronized window. Expand a row to inspect total job time. Local-machine results are not production capacity guarantees.</p>
      </section>}
    </>}
  </main>;
}
