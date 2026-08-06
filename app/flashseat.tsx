"use client";

import { useCallback, useEffect, useState } from "react";
import Link from "next/link";

type SeatState = "available" | "held" | "sold";
type Seat = { id: string; section: string; row: string; number: number; priceCents: number; accessible: boolean; state: SeatState; mine?: boolean };
type Hold = { id: string; expiresAt: string; seats: Seat[]; totalCents: number };

const API = process.env.NEXT_PUBLIC_API_URL || "http://localhost:8080/api/v1";
const sections = ["ORCHESTRA", "MEZZANINE", "BALCONY"];
const demoSeats: Seat[] = sections.flatMap((section, si) =>
  Array.from({ length: 32 }, (_, i) => ({
    id: `${section.toLowerCase()}-${i + 1}`,
    section,
    row: String.fromCharCode(65 + Math.floor(i / 8)),
    number: (i % 8) + 1,
    priceCents: [18500, 12500, 7500][si],
    accessible: i === 7 || i === 24,
    state: i % 17 === 0 ? "sold" : i % 13 === 0 ? "held" : "available",
  }))
);

function money(cents: number) {
  return new Intl.NumberFormat("en-US", { style: "currency", currency: "USD", maximumFractionDigits: 0 }).format(cents / 100);
}

function useCountdown(expiresAt?: string) {
  const [now, setNow] = useState(0);
  useEffect(() => { const timer = setInterval(() => setNow(Date.now()), 250); return () => clearInterval(timer); }, []);
  const seconds = Math.max(0, Math.ceil(((expiresAt ? Date.parse(expiresAt) : 0) - now) / 1000));
  return `${String(Math.floor(seconds / 60)).padStart(2, "0")}:${String(seconds % 60).padStart(2, "0")}`;
}

export function FlashSeat() {
  const [seats, setSeats] = useState<Seat[]>(demoSeats);
  const [selected, setSelected] = useState<string[]>([]);
  const [section, setSection] = useState("ORCHESTRA");
  const [hold, setHold] = useState<Hold | null>(null);
  const [order, setOrder] = useState<string | null>(null);
  const [status, setStatus] = useState("Connecting to live inventory…");
  const [busy, setBusy] = useState(false);
  const countdown = useCountdown(hold?.expiresAt);

  const bootstrap = useCallback(async () => {
    try {
      await fetch(`${API}/sessions/demo`, { method: "POST", credentials: "include" });
      const response = await fetch(`${API}/events/aurora-2026/seats`, { credentials: "include" });
      if (!response.ok) throw new Error();
      const data = await response.json();
      setSeats(data.seats);
      setStatus("Live inventory");
    } catch {
      setStatus("Preview inventory · start the API for live mode");
    }
  }, []);

  useEffect(() => { const timer = setTimeout(bootstrap, 0); return () => clearTimeout(timer); }, [bootstrap]);
  useEffect(() => {
    const stream = new EventSource(`${API}/events/aurora-2026/stream`, { withCredentials: true });
    stream.addEventListener("inventory", () => bootstrap());
    stream.onerror = () => stream.close();
    return () => stream.close();
  }, [bootstrap]);

  const visible = seats.filter((seat) => seat.section === section);
  const chosen = seats.filter((seat) => selected.includes(seat.id));
  const subtotal = chosen.reduce((sum, seat) => sum + seat.priceCents, 0);

  function toggleSeat(seat: Seat) {
    if (seat.state !== "available" || hold) return;
    setSelected((current) => current.includes(seat.id) ? current.filter((id) => id !== seat.id) : current.length < 8 ? [...current, seat.id] : current);
  }

  async function createHold() {
    if (!selected.length) return;
    setBusy(true);
    try {
      const response = await fetch(`${API}/holds`, {
        method: "POST", credentials: "include", headers: { "Content-Type": "application/json", "Idempotency-Key": crypto.randomUUID() },
        body: JSON.stringify({ eventId: "aurora-2026", seatIds: selected }),
      });
      const data = await response.json();
      if (!response.ok) throw new Error(data.message || "Those seats were just claimed.");
      setHold(data);
      setSeats((all) => all.map((seat) => selected.includes(seat.id) ? { ...seat, state: "held", mine: true } : seat));
      setStatus("Seats secured — finish checkout before time expires");
    } catch (error) {
      setStatus(error instanceof Error ? error.message : "Unable to hold seats");
      await bootstrap();
      setSelected([]);
    } finally { setBusy(false); }
  }

  async function checkout() {
    if (!hold) return;
    setBusy(true);
    try {
      const response = await fetch(`${API}/holds/${hold.id}/checkout`, { method: "POST", credentials: "include", headers: { "Idempotency-Key": crypto.randomUUID() } });
      const data = await response.json();
      if (!response.ok) throw new Error(data.message || "Checkout could not be completed.");
      setOrder(data.reference || data.id);
      setSeats((all) => all.map((seat) => selected.includes(seat.id) ? { ...seat, state: "sold", mine: false } : seat));
      setStatus("Reservation confirmed");
    } catch (error) { setStatus(error instanceof Error ? error.message : "Checkout failed"); }
    finally { setBusy(false); }
  }

  async function release() {
    if (hold) await fetch(`${API}/holds/${hold.id}`, { method: "DELETE", credentials: "include" }).catch(() => null);
    setSeats((all) => all.map((seat) => selected.includes(seat.id) && seat.mine ? { ...seat, state: "available", mine: false } : seat));
    setSelected([]); setHold(null); setStatus("Hold released");
  }

  return (
    <main>
      <header className="nav shell">
        <Link href="/" className="brand" aria-label="FlashSeat home"><span>F</span> FLASHSEAT</Link>
        <div className="edition">EDITION 01 / LIVE INVENTORY</div>
        <nav className="nav-links"><Link href="/lab">RUN SIMULATION</Link><Link href="/results">RESULTS</Link></nav>
      </header>

      <section className="hero shell">
        <div className="eyebrow"><span className="live-dot" /> ONE NIGHT ONLY · BROOKLYN</div>
        <h1>Aurora<br/><em>After Dark</em></h1>
        <div className="hero-meta">
          <p>SYMPHONIC ELECTRONICA<br/>IN THE ROUND</p>
          <p>OCTOBER 17, 2026<br/>DOORS 7:00 PM</p>
          <p>THE HALCYON<br/>BROOKLYN, NY</p>
        </div>
        <div className="ticket-mark" aria-hidden="true"><b>17</b><span>OCT<br/>2026</span></div>
      </section>

      <section className="booking shell">
        <aside className="details">
          <p className="kicker">SELECT YOUR VIEW</p>
          <h2>Every seat.<br/>One decisive moment.</h2>
          <p className="intro">Choose up to eight seats. Your selection becomes a protected ten-minute hold only after confirmation.</p>
          <div className="status-line"><span className={status.startsWith("Live") ? "live-dot" : "soft-dot"}/>{status}</div>
          <div className="legend"><span><i className="available"/>Available</span><span><i className="selected"/>Selected</span><span><i className="unavailable"/>Unavailable</span><span><i className="accessible">◇</i>Accessible</span></div>
          <ol className="booking-steps"><li><b>01</b><span>Choose a section</span></li><li><b>02</b><span>Click available circles</span></li><li><b>03</b><span>Hold, then checkout</span></li></ol>
          <Link href="/lab" className="lab-cta">Want to see 1,000 buyers collide? <b>Open System Lab →</b></Link>
        </aside>

        <div className="venue-card">
          <div className="stage"><span>THE STAGE</span><div/></div>
          <div className="section-tabs" role="tablist">
            {sections.map((name) => <button role="tab" aria-selected={section === name} onClick={() => setSection(name)} key={name}>{name}</button>)}
          </div>
          <div className="seat-map" role="group" aria-label={`${section} seating map`}>
            {visible.map((seat) => {
              const active = selected.includes(seat.id);
              return <button key={seat.id} aria-label={`${seat.section}, row ${seat.row}, seat ${seat.number}, ${money(seat.priceCents)}, ${seat.state}`} aria-pressed={active} disabled={seat.state !== "available" || !!hold} className={`seat ${seat.state} ${active ? "is-selected" : ""}`} onClick={() => toggleSeat(seat)}><span>{seat.number}</span>{seat.accessible && <b>◇</b>}</button>;
            })}
          </div>
          <div className="map-footer"><span>{section} · ROWS A—D</span><span>{visible.filter((s) => s.state === "available").length} AVAILABLE</span></div>
        </div>
      </section>

      <section className="selection shell" aria-live="polite">
        <div><p className="kicker">YOUR SELECTION</p><h3>{chosen.length ? `${chosen.length} seat${chosen.length > 1 ? "s" : ""} selected` : "Awaiting your choice"}</h3><p>{chosen.map((seat) => `${seat.row}${seat.number}`).join(" · ") || "Select a seat from the map to begin."}</p></div>
        {hold && !order && <div className="countdown"><span>HOLD EXPIRES IN</span><b>{countdown}</b></div>}
        <div className="total"><span>SUBTOTAL</span><b>{money(subtotal)}</b></div>
        {!hold && <button className="primary" disabled={!selected.length || busy} onClick={createHold}>{busy ? "SECURING…" : "HOLD THESE SEATS →"}</button>}
        {hold && !order && <><button className="text-button" onClick={release}>Release</button><button className="primary" disabled={busy} onClick={checkout}>{busy ? "PROCESSING…" : "COMPLETE RESERVATION →"}</button></>}
        {order && <div className="receipt"><span>CONFIRMED</span><b>{order}</b></div>}
      </section>

      <footer className="shell"><span>FLASHSEAT / ENGINEERED FOR THE RUSH</span><span>POSTGRESQL × REDIS × GO</span></footer>
    </main>
  );
}
