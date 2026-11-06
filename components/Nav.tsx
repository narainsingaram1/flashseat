import Link from "next/link";

export function Nav({ active, status }: { active: "live" | "runs" | "compare"; status?: React.ReactNode }) {
  return (
    <header className="nav">
      <Link href="/" className="brand" aria-label="FlashSeat home">
        <svg viewBox="0 0 32 32" width="22" height="22" aria-hidden="true"><rect width="32" height="32" rx="7" fill="#1a1f26" /><rect x="6" y="8" width="5" height="5" rx="1" fill="#2fbf71" /><rect x="13.5" y="8" width="5" height="5" rx="1" fill="#2fbf71" /><rect x="21" y="8" width="5" height="5" rx="1" fill="#f2a33a" /><rect x="6" y="15.5" width="5" height="5" rx="1" fill="#3a414c" /><rect x="13.5" y="15.5" width="5" height="5" rx="1" fill="#2fbf71" /><rect x="21" y="15.5" width="5" height="5" rx="1" fill="#3a414c" /><rect x="6" y="23" width="20" height="2" rx="1" fill="#e8eaed" /></svg>
        FlashSeat
      </Link>
      <nav>
        <Link href="/" className={active === "live" ? "on" : ""}>Live sale</Link>
        <Link href="/runs" className={active === "runs" ? "on" : ""}>Runs</Link>
        <Link href="/compare" className={active === "compare" ? "on" : ""}>Compare</Link>
      </nav>
      <div className="nav-status">{status}</div>
    </header>
  );
}

export function PhasePill({ phase, connected = true }: { phase: string; connected?: boolean }) {
  const label: Record<string, string> = { idle: "Idle", preparing: "Building venue", selling: "Sale live", settling: "Settling holds", done: "Finished", failed: "Failed" };
  return <span className={`pill phase-${connected ? phase : "offline"}`}><i />{connected ? label[phase] ?? phase : "Reconnecting"}</span>;
}
