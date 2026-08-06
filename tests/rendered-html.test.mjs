import assert from "node:assert/strict";
import test from "node:test";

async function render(path = "/") {
  const workerUrl = new URL("../dist/server/index.js", import.meta.url);
  workerUrl.searchParams.set("test", `${process.pid}-${Date.now()}`);
  const { default: worker } = await import(workerUrl.href);
  return worker.fetch(new Request(`http://localhost${path}`, { headers: { accept: "text/html" } }), { ASSETS: { fetch: async () => new Response("Not found", { status: 404 }) } }, { waitUntil() {}, passThroughOnException() {} });
}

test("server-renders the external HTTP contention lab at the root", async () => {
  const response = await render();
  assert.equal(response.status, 200);
  const html = await response.text();
  assert.match(html, /HTTP CONTENTION LAB/);
  assert.match(html, /REAL K6/);
  assert.match(html, /RUN HTTP BENCHMARK/);
  assert.doesNotMatch(html, /codex-preview/);
});

test("server-renders the saved results dashboard", async () => {
  const response = await render("/results");
  assert.equal(response.status, 200);
  const html = await response.text();
  assert.match(html, /MEASURED EVIDENCE/);
  assert.match(html, /PERSISTED K6/);
});
