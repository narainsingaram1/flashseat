import type { Frame, Layout } from "./api";

export const FREE = 0, HELD = 1, SOLD = 2, RETURNED = 3, CONFLICT = 4;

/** Mutable seat state shared between the data stream and the canvas. */
export type Board = {
  states: Uint8Array;
  changedAt: Float64Array;
  dirty: boolean;
};

export function createBoard(seats: number, states = ""): Board {
  const b: Board = { states: new Uint8Array(seats), changedAt: new Float64Array(seats), dirty: true };
  for (let i = 0; i < Math.min(seats, states.length); i++) b.states[i] = states.charCodeAt(i) - 48;
  return b;
}

/** Applies flat [index, state, index, state, …] deltas. */
export function applyDeltas(b: Board, d: number[] | undefined, now: number, flash = true) {
  if (!d) return;
  for (let k = 0; k + 1 < d.length; k += 2) {
    const i = d[k];
    if (i < 0 || i >= b.states.length) continue;
    b.states[i] = d[k + 1];
    if (flash) b.changedAt[i] = now;
  }
  if (d.length) b.dirty = true;
}

export function countStates(b: Board) {
  const c = { free: 0, held: 0, sold: 0, conflict: 0 };
  for (const s of b.states) {
    if (s === HELD) c.held++;
    else if (s === SOLD) c.sold++;
    else if (s === CONFLICT) c.conflict++;
    else c.free++;
  }
  return c;
}

/** Rebuilds the board at time t (ms) by replaying recorded frames. */
export function boardAt(layout: Layout, frames: Frame[], t: number): Board {
  const b = createBoard(layout.seats);
  for (const f of frames) {
    if (f.t > t) break;
    applyDeltas(b, f.d, 0, false);
  }
  return b;
}

/** Steps a board through recorded frames. Seeking forward applies only new
 * frames; seeking backward replays from the start. */
export class Replayer {
  readonly board: Board;
  private next = 0;
  private at = -1;

  constructor(private readonly frames: Frame[], seats: number) {
    this.board = createBoard(seats);
  }

  seek(t: number) {
    const b = this.board;
    if (t < this.at) {
      b.states.fill(0);
      b.changedAt.fill(0);
      b.dirty = true;
      this.next = 0;
    }
    const flash = t - this.at < 2000; // animate normal playback, not big jumps
    const now = performance.now();
    while (this.next < this.frames.length && this.frames[this.next].t <= t) applyDeltas(b, this.frames[this.next++].d, now, flash);
    this.at = t;
    return countStates(b);
  }
}
