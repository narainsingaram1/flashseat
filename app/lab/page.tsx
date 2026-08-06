import Link from "next/link";

export default function Lab() {
  return <main className="lab-page">
    <header className="nav shell"><Link href="/" className="brand"><span>F</span> FLASHSEAT</Link><div className="edition">CONCURRENCY OBSERVATORY</div><nav><Link href="/">← BACK TO EVENT</Link></nav></header>
    <section className="lab-hero shell"><p className="eyebrow"><span className="live-dot"/>SYSTEM LAB / LIVE ARCHITECTURE</p><h1>Built for<br/><em>the rush.</em></h1><p className="intro">A reservation system where thousands can compete—and only one transaction wins.</p></section>
    <section className="lab-grid"><div className="metric"><span>CONSISTENCY</span><b>Zero</b><p>oversold seats by database invariant</p></div><div className="metric"><span>HOLD WINDOW</span><b>10:00</b><p>authoritative PostgreSQL expiry</p></div><div className="metric"><span>LOCK ORDER</span><b>Sorted</b><p>deadlock-resistant seat acquisition</p></div><div className="metric"><span>DELIVERY</span><b>SSE</b><p>live inventory invalidations</p></div></section>
    <section className="architecture shell"><div><p className="kicker">THE CRITICAL PATH</p><h2>Speed at the edge.<br/>Truth at the core.</h2><p className="intro">Redis rejects obvious collisions quickly. PostgreSQL row locks and uniqueness constraints make the final decision. A reconciliation worker ensures expired inventory always returns.</p></div><div className="flow"><div><span>01</span><b>Request</b><p>Validate session, event, idempotency key, and seat set.</p></div><div><span>02</span><b>Lease</b><p>Atomic Lua acquires every Redis key—or none of them.</p></div><div><span>03</span><b>Commit</b><p>Sorted row locks serialize contenders at the source of truth.</p></div><div><span>04</span><b>Broadcast</b><p>SSE tells every open venue map which seats changed.</p></div></div></section>
    <footer className="shell"><span>POSTGRESQL IS AUTHORITATIVE</span><span>REDIS IS AN ACCELERATOR</span></footer>
  </main>;
}
