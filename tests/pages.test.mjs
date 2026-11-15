// Starts the production build and checks that every route server-renders.
// Run `npm run build` first.
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { after, before, test } from "node:test";

const PORT = 3107;
let server;

before(async () => {
  server = spawn("npx", ["next", "start", "-p", String(PORT)], { stdio: "ignore", env: { ...process.env, NODE_ENV: "production" } });
  for (let i = 0; i < 60; i++) {
    try {
      if ((await fetch(`http://localhost:${PORT}/`)).ok) return;
    } catch {}
    await new Promise((r) => setTimeout(r, 500));
  }
  throw new Error("next start did not come up");
});

after(() => server?.kill());

const page = async (path) => {
  const res = await fetch(`http://localhost:${PORT}${path}`);
  return { status: res.status, html: await res.text() };
};
const has = (html, text) => assert.ok(html.includes(text), `expected page to contain "${text}"`);

test("live sale page renders controls and correctness panel", async () => {
  const { status, html } = await page("/");
  assert.equal(status, 200);
  has(html, "A ticket on-sale you can watch, and break");
  has(html, "Correctness, live from PostgreSQL");
  has(html, "Break something while buyers are active");
});

test("runs and compare pages render", async () => {
  has((await page("/runs")).html, "Every sale, recorded");
  has((await page("/compare")).html, "Same crowd, two engines");
});

test("replay route renders for any run id", async () => {
  assert.equal((await page("/runs/00000000-0000-0000-0000-000000000000")).status, 200);
});
