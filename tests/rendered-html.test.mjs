import assert from "node:assert/strict";
import test from "node:test";

async function render(path = "/") {
  const workerUrl = new URL("../dist/server/index.js", import.meta.url);
  workerUrl.searchParams.set("test", `${process.pid}-${Date.now()}`);
  const { default: worker } = await import(workerUrl.href);
  return worker.fetch(new Request(`http://localhost${path}`, { headers: { accept: "text/html" } }), { ASSETS: { fetch: async () => new Response("Not found", { status: 404 }) } }, { waitUntil() {}, passThroughOnException() {} });
}

test("server-renders the FlashSeat event", async () => {
  const response = await render();
  assert.equal(response.status, 200);
  const html = await response.text();
  assert.match(html, /FlashSeat/);
  assert.match(html, /Aurora/);
  assert.match(html, /Engineered for the rush/i);
  assert.doesNotMatch(html, /codex-preview/);
});

test("server-renders the System Lab", async () => {
  const response = await render("/lab");
  assert.equal(response.status, 200);
  const html = await response.text();
  assert.match(html, /SIMULATION LAB/);
  assert.match(html, /LAUNCH/);
  assert.match(html, /Choose the crowd/i);
});

test("server-renders the saved results dashboard", async () => {
  const response = await render("/results");
  assert.equal(response.status, 200);
  const html = await response.text();
  assert.match(html, /PROOF ARCHIVE/);
  assert.match(html, /SAVED TEST HISTORY/);
});
