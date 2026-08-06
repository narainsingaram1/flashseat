"use client";

import Link from "next/link";
import { useEffect, useRef, useState } from "react";

const API = process.env.NEXT_PUBLIC_API_URL || "http://localhost:8080/api/v1";
type Seat = { id:string; section:string; row:string; number:number; state:string };
type Result = { id:number; users:number; seatId:string; winners:number; conflicts:number; errors:number; databaseActiveClaims:number; durationMs:number; invariantPassed:boolean; createdAt:string };
const presets=[{users:100,label:"Quick test",note:"A fast correctness check"},{users:500,label:"Rush hour",note:"A heavy simultaneous surge"},{users:1000,label:"Flash drop",note:"Maximum in-app collision"}];

export function LabConsole() {
  const [users,setUsers]=useState(500),[seats,setSeats]=useState<Seat[]>([]),[seatId,setSeatId]=useState(""),[result,setResult]=useState<Result|null>(null),[running,setRunning]=useState(false),[phase,setPhase]=useState(0),[error,setError]=useState("");
  const resultRef=useRef<HTMLElement>(null);
  useEffect(()=>{const controller=new AbortController();fetch(`${API}/events/aurora-2026/seats`,{signal:controller.signal}).then(r=>r.json()).then(data=>{const available=(data.seats||[]).filter((s:Seat)=>s.state==="available");setSeats(available);setSeatId(available[0]?.id||"")}).catch(()=>setError("The reservation service is offline. Restart Docker, then refresh this page."));return()=>controller.abort()},[]);
  async function run(){if(!seatId)return;setRunning(true);setResult(null);setError("");setPhase(1);const phases=setInterval(()=>setPhase(p=>Math.min(p+1,3)),260);try{const [response]=await Promise.all([fetch(`${API}/lab/contention`,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({eventId:"aurora-2026",seatId,users})}),new Promise(resolve=>setTimeout(resolve,850))]);const data=await response.json();if(!response.ok)throw new Error(data.message||"Simulation failed");setResult(data);setPhase(4);setTimeout(()=>resultRef.current?.scrollIntoView({behavior:"smooth",block:"start"}),80)}catch(e){setError(e instanceof Error?e.message:"Simulation failed");setPhase(0)}finally{clearInterval(phases);setRunning(false)}}
  const target=seats.find(s=>s.id===seatId);
  return <main className="lab-page focused-lab">
    <header className="nav shell"><Link href="/" className="brand"><span>F</span> FLASHSEAT</Link><div className="edition">SIMULATION LAB</div><nav className="nav-links"><Link href="/results">ALL RESULTS</Link><Link href="/">TICKET DEMO</Link></nav></header>
    <section className="lab-intro shell"><p className="eyebrow"><span className="live-dot"/>LIVE DATABASE TEST</p><h1>Can 1,000 buyers<br/><em>claim one seat?</em></h1><p>Set the crowd size, press one button, and watch the reservation engine prove that only one buyer can win.</p></section>
    <section className="direction-bar"><div className="shell"><div><b>1</b><span><strong>Choose the crowd</strong>Select a test size below</span></div><i>→</i><div><b>2</b><span><strong>Launch the collision</strong>Everyone clicks together</span></div><i>→</i><div><b>3</b><span><strong>Read the proof</strong>One winner, zero duplicates</span></div></div></section>
    <section className="runner shell">
      <div className="runner-heading"><span>STEP 1</span><h2>How many buyers should compete?</h2></div>
      <div className="preset-grid">{presets.map(p=><button key={p.users} className={users===p.users?"active":""} onClick={()=>setUsers(p.users)} disabled={running}><span>{p.label}</span><b>{p.users.toLocaleString()}</b><small>{p.note}</small><i>{users===p.users?"SELECTED":"SELECT"}</i></button>)}</div>
      <div className="target-row"><div><span>TARGET SEAT</span><strong>{target?`${target.section} · ROW ${target.row} · SEAT ${target.number}`:"Finding an available seat…"}</strong><small>The lab chooses from live inventory and releases the winning test hold afterward.</small></div><label><span>Change seat</span><select value={seatId} onChange={e=>setSeatId(e.target.value)} disabled={running}>{seats.slice(0,40).map(s=><option key={s.id} value={s.id}>{s.section} · {s.row}{s.number}</option>)}</select></label></div>
      <div className="launch-zone"><div><span>STEP 2</span><h2>Ready to create the collision?</h2><p>{users.toLocaleString()} buyers will attempt <strong>{target?`${target.row}${target.number}`:"one seat"}</strong> at exactly the same time.</p></div><button className="launch-button" disabled={running||!seatId} onClick={run}>{running?<><span className="spinner"/>SIMULATION RUNNING</>:<>LAUNCH {users.toLocaleString()} BUYERS <b>→</b></>}</button></div>
      {(running||error)&&<div className="run-status" aria-live="polite">{error?<p className="error-message">{error}</p>:<><div className={phase>=1?"done":""}><b>01</b><span>Buyers released</span></div><div className={phase>=2?"done":""}><b>02</b><span>Redis resolves collision</span></div><div className={phase>=3?"done":""}><b>03</b><span>PostgreSQL verifies winner</span></div></>}</div>}
    </section>
    {result&&<section ref={resultRef} className={`proof ${result.invariantPassed?"passed":"failed"}`}>
      <div className="shell"><div className="proof-header"><p className="eyebrow">STEP 3 / THE RESULT</p><h2>{result.invariantPassed?"One winner. Everyone else stopped.":"The guarantee did not hold."}</h2><span className="proof-badge">{result.invariantPassed?"✓ ZERO DOUBLE-BOOKINGS":"! REVIEW REQUIRED"}</span></div>
        <div className="proof-numbers"><div className="winner-number"><span>SUCCESSFUL BUYERS</span><b>{result.winners}</b><small>allowed to hold the seat</small></div><div className="versus">VS</div><div><span>SAFELY REJECTED</span><b>{result.conflicts.toLocaleString()}</b><small>received a conflict response</small></div></div>
        <div className="proof-bar"><i style={{width:`${Math.max(1,(result.winners/result.users)*100)}%`}}/><span>{result.users.toLocaleString()} total simultaneous attempts</span></div>
        <div className="evidence-grid"><div><span>DATABASE CLAIMS</span><b>{result.databaseActiveClaims}</b><small>must equal one</small></div><div><span>INTERNAL ERRORS</span><b>{result.errors}</b><small>must equal zero</small></div><div><span>ENGINE TIME</span><b>{result.durationMs} ms</b><small>local container run</small></div><div><span>INVARIANT</span><b>{result.invariantPassed?"PASSED":"FAILED"}</b><small>verified after the race</small></div></div>
        <div className="proof-actions"><button onClick={()=>{setResult(null);setPhase(0);window.scrollTo({top:520,behavior:"smooth"})}}>RUN ANOTHER TEST</button><Link href="/results">VIEW ALL SAVED RESULTS →</Link></div>
      </div>
    </section>}
  </main>;
}
