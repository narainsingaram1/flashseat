import Link from "next/link";

export function Nav({ active, status }: { active: "live" | "runs" | "compare"; status?: React.ReactNode }) {
  return (
    <header className="nav">
      <Link href="/" className="brand" aria-label="FlashSeat home">
        <svg viewBox="0 0 20 20" width="20" height="20" aria-hidden="true">
          <rect x="1" y="2" width="5" height="5" rx="1" fill="#34c77b" /><rect x="7.5" y="2" width="5" height="5" rx="1" fill="#34c77b" /><rect x="14" y="2" width="5" height="5" rx="1" fill="#f0a63c" />
          <rect x="1" y="8.5" width="5" height="5" rx="1" fill="#3a3c43" /><rect x="7.5" y="8.5" width="5" height="5" rx="1" fill="#34c77b" /><rect x="14" y="8.5" width="5" height="5" rx="1" fill="#3a3c43" />
          <rect x="1" y="15.5" width="18" height="2" rx="1" fill="#ececee" />
        </svg>
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
  return <span className={`phase phase-${connected ? phase : "offline"}`}><i className="sq" />{connected ? label[phase] ?? phase : "Reconnecting"}</span>;
}

export function EngineTag({ mode, short = false }: { mode: string; short?: boolean }) {
  const name = mode === "safe" ? "FlashSeat" : "Naive";
  return <span className={`engine-tag ${mode}`}><i className="sq" />{short ? name : mode === "safe" ? "FlashSeat engine" : "Naive baseline"}</span>;
}
