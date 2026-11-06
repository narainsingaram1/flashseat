"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import type { Layout, Row } from "@/lib/api";
import type { Board } from "@/lib/board";

const COLORS = ["#262c36", "#f2a33a", "#2fbf71", "#4c8dff", "#ff4d4f"];
const STATE_NAMES = ["Open", "Held", "Sold", "Back on sale", "Double-booked"];
const FLASH_MS = 700;
const TOP_ROWS = 3; // stage + section labels above the first section

type Hover = { x: number; y: number; text: string } | null;

/** Draws every seat in the venue and redraws only when seats change. */
export function SeatMap({ layout, board, compact = false }: { layout: Layout; board: Board; compact?: boolean }) {
  const wrap = useRef<HTMLDivElement>(null);
  const canvas = useRef<HTMLCanvasElement>(null);
  const geom = useRef({ cell: 4, ox: 0, oy: 0, w: 0, h: 0 });
  const [hover, setHover] = useState<Hover>(null);

  const rowsByY = useMemo(() => {
    const m = new Map<number, Row[]>();
    for (const r of layout.rows) {
      const list = m.get(r.y) || [];
      list.push(r);
      m.set(r.y, list);
    }
    return m;
  }, [layout]);

  useEffect(() => {
    const el = wrap.current, cv = canvas.current;
    if (!el || !cv) return;
    const ctx = cv.getContext("2d");
    if (!ctx) return;
    let raf = 0, resized = true, lastFlash = 0;

    const resize = () => {
      const dpr = window.devicePixelRatio || 1;
      const w = el.clientWidth;
      const cell = Math.max(2, Math.floor(w / layout.width * 100) / 100);
      const h = Math.ceil(cell * (layout.height + TOP_ROWS) + 4);
      cv.width = Math.floor(w * dpr);
      cv.height = Math.floor(h * dpr);
      cv.style.height = `${h}px`;
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
      geom.current = { cell, ox: (w - cell * layout.width) / 2, oy: cell * TOP_ROWS, w, h };
      resized = true;
    };
    const ro = new ResizeObserver(resize);
    ro.observe(el);
    resize();

    const draw = (now: number) => {
      raf = requestAnimationFrame(draw);
      const flashing = now - lastFlash < FLASH_MS + 50;
      if (!board.dirty && !resized && !flashing) return;
      board.dirty = false;
      resized = false;
      const { cell, ox, oy, w, h } = geom.current;
      ctx.clearRect(0, 0, w, h);
      // Stage
      ctx.fillStyle = "#e8eaed";
      const stageW = Math.min(w * 0.36, cell * layout.width * 0.4);
      ctx.fillRect(w / 2 - stageW / 2, 2, stageW, Math.max(3, cell * 0.7));
      if (!compact) {
        ctx.fillStyle = "#8b93a1";
        ctx.font = `600 ${Math.max(9, Math.min(11, cell * 2.2))}px ui-sans-serif, system-ui`;
        ctx.textAlign = "center";
        for (const s of layout.sections) {
          ctx.fillText(s.name.toUpperCase(), ox + (s.x + s.width / 2) * cell, oy + s.y * cell - cell * 0.6);
        }
      }
      const gap = cell >= 4 ? 1 : cell >= 2.5 ? 0.6 : 0.3;
      const size = cell - gap;
      const st = board.states, changed = board.changedAt;
      let newest = 0;
      for (const r of layout.rows) {
        const y = oy + r.y * cell;
        for (let k = 0; k < r.len; k++) {
          const i = r.start + k;
          const x = ox + (r.x + k) * cell;
          ctx.fillStyle = COLORS[st[i]] || COLORS[0];
          ctx.fillRect(x, y, size, size);
          const age = now - changed[i];
          if (changed[i] > 0 && age < FLASH_MS) {
            newest = Math.max(newest, changed[i]);
            ctx.fillStyle = `rgba(255,255,255,${0.7 * (1 - age / FLASH_MS)})`;
            ctx.fillRect(x, y, size, size);
          }
        }
      }
      if (newest) lastFlash = Math.max(lastFlash, newest);
    };
    raf = requestAnimationFrame(draw);
    return () => {
      cancelAnimationFrame(raf);
      ro.disconnect();
    };
  }, [layout, board, compact]);

  const onMove = (e: React.MouseEvent) => {
    const rect = canvas.current!.getBoundingClientRect();
    const { cell, ox, oy } = geom.current;
    const gx = Math.floor((e.clientX - rect.left - ox) / cell);
    const gy = Math.floor((e.clientY - rect.top - oy) / cell);
    const row = rowsByY.get(gy)?.find((r) => gx >= r.x && gx < r.x + r.len);
    if (!row) return setHover(null);
    const i = row.start + gx - row.x;
    setHover({ x: e.clientX - rect.left, y: e.clientY - rect.top, text: `${row.section} · Row ${row.label} · Seat ${gx - row.x + 1} — ${STATE_NAMES[board.states[i]] ?? "Open"}` });
  };

  return (
    <div ref={wrap} className="seatmap" onMouseMove={onMove} onMouseLeave={() => setHover(null)}>
      <canvas ref={canvas} aria-label={`Seat map with ${layout.seats.toLocaleString()} seats`} role="img" />
      {hover && <div className="seat-tip" style={{ left: hover.x + 12, top: hover.y + 12 }}>{hover.text}</div>}
    </div>
  );
}

export function SeatLegend({ naive = false }: { naive?: boolean }) {
  const items = [0, 1, 2, 3, ...(naive ? [4] : [])];
  return (
    <div className="legend">
      {items.map((s) => (
        <span key={s}><i style={{ background: COLORS[s] }} />{STATE_NAMES[s]}</span>
      ))}
      {!naive && <span><i style={{ background: COLORS[4] }} />Double-booked (never expected)</span>}
    </div>
  );
}
