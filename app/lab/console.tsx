"use client";

import Link from "next/link";
import { useEffect, useState } from "react";

const API = process.env.NEXT_PUBLIC_API_URL || "http://localhost:8080/api/v1";
type Seat = { id:string; section:string; row:string; number:number; state:string };
type Result = { users:number; seatId:string; winners:number; conflicts:number; errors:number; databaseActiveClaims:number; durationMs:number; invariantPassed:boolean };

export function LabConsole() {
  const [users,setUsers]=useState(250);
  const [seats,setSeats]=useState<Seat[]>([]);
  const [seatId,setSeatId]=useState("");
  const [result,setResult]=useState<Result|null>(null);
  const [running,setRunning]=useState(false);
  const [message,setMessage]=useState("Loading live inventory…");
  useEffect(()=>{const controller=new AbortController();fetch(`${API}/events/aurora-2026/seats`,{signal:controller.signal}).then(r=>r.json()).then(data=>{const available=(data.seats||[]).filter((s:Seat)=>s.state==="available");setSeats(available);setSeatId(available[0]?.id||"");setMessage(available.length?"Ready to create a collision":"No available seats")}).catch(()=>setMessage("The API is not connected. Start the Docker stack and refresh."));return()=>controller.abort()},[]);
  async function run(){if(!seatId)return;setRunning(true);setResult(null);setMessage(`Releasing ${users.toLocaleString()} buyers at the same seat…`);try{const response=await fetch(`${API}/lab/contention`,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({eventId:"aurora-2026",seatId,users})});const data=await response.json();if(!response.ok)throw new Error(data.message||"Simulation failed");setResult(data);setMessage(data.invariantPassed?"Invariant held: one—and only one—buyer won.":"Invariant failed. Inspect the result below.")}catch(error){setMessage(error instanceof Error?error.message:"Simulation failed")}finally{setRunning(false)}}
  return <main className="lab-page">
    <header className="nav shell"><Link href="/" className="brand"><span>F</span> FLASHSEAT</Link><div className="edition">CONCURRENCY OBSERVATORY</div><nav><Link href="/">← BACK TO EVENT</Link></nav></header>
    <section className="lab-hero shell"><p className="eyebrow"><span className="live-dot"/>INTERACTIVE SYSTEM LAB</p><h1>Make them<br/><em>all click.</em></h1><p className="intro">Aim hundreds of buyers at one seat. Redis absorbs the rush; PostgreSQL decides the single winner.</p></section>
    <section className="simulator shell">
      <div className="sim-copy"><p className="kicker">LIVE CONTENTION TEST</p><h2>Start a controlled stampede.</h2><p>This runs real concurrent Go routines against the same Redis and PostgreSQL services powering the ticket page. The winning test hold is automatically released afterward.</p></div>
      <div className="sim-controls">
        <label><span>SIMULATED BUYERS</span><b>{users.toLocaleString()}</b><input type="range" min="2" max="1000" step="1" value={users} onChange={e=>setUsers(Number(e.target.value))}/></label>
        <label><span>TARGET SEAT</span><select value={seatId} onChange={e=>setSeatId(e.target.value)}>{seats.slice(0,40).map(s=><option key={s.id} value={s.id}>{s.section} · {s.row}{s.number}</option>)}</select></label>
        <button className="primary lab-run" disabled={running||!seatId} onClick={run}>{running?"RUNNING COLLISION…":"LAUNCH SIMULATION →"}</button>
        <p className="sim-message"><i className={result?.invariantPassed?"pass-dot":"soft-dot"}/>{message}</p>
      </div>
    </section>
    {result&&<section className={`result-strip ${result.invariantPassed?"passed":"failed"}`}>
      <div className="shell result-grid"><div><span>REQUESTS</span><b>{result.users.toLocaleString()}</b></div><div><span>WINNERS</span><b>{result.winners}</b></div><div><span>CONFLICTS</span><b>{result.conflicts.toLocaleString()}</b></div><div><span>DB CLAIMS</span><b>{result.databaseActiveClaims}</b></div><div><span>ERRORS</span><b>{result.errors}</b></div><div><span>WALL TIME</span><b>{result.durationMs} ms</b></div></div>
      <div className="shell verdict"><strong>{result.invariantPassed?"ZERO DOUBLE-BOOKINGS":"INVARIANT NEEDS ATTENTION"}</strong><span>{result.invariantPassed?`${result.users-1} losing requests were safely rejected.`:"The result did not satisfy the one-winner guarantee."}</span></div>
    </section>}
    <section className="architecture shell"><div><p className="kicker">WHAT JUST HAPPENED</p><h2>Speed at the edge.<br/>Truth at the core.</h2><p className="intro">The simulation is not an animation. Every buyer races through atomic Redis admission. The winner then commits through sorted database row locks and uniqueness constraints.</p></div><div className="flow"><div><span>01</span><b>Release</b><p>All goroutines begin at the same synchronization barrier.</p></div><div><span>02</span><b>Lease</b><p>Atomic Lua grants the requested seat key to one token.</p></div><div><span>03</span><b>Commit</b><p>PostgreSQL verifies one active claim at the source of truth.</p></div><div><span>04</span><b>Clean up</b><p>The test hold is released so you can run it again.</p></div></div></section>
    <footer className="shell"><span>POSTGRESQL IS AUTHORITATIVE</span><span>REDIS IS AN ACCELERATOR</span></footer>
  </main>;
}
