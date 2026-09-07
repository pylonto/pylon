import test from "node:test";
import assert from "node:assert/strict";
import { Meter, MODEL, PROVIDER } from "./meter.mjs";

// Synthetic metadata is a unit fixture, NEVER provider/model availability proof.
const model = { provider: PROVIDER, id: MODEL, api: "openai-codex-responses", baseUrl: "https://chatgpt.com/backend-api", contextWindow: 272000, maxTokens: 128000, thinkingLevelMap: { max: "max" } };
const url = model.baseUrl + "/codex/responses";
const opts = { method: "POST" };
const sse = (usage, type = "response.completed") => new Response(`data: ${JSON.stringify({ type, response: { usage } })}\n\n`, { status: 200 });
const usage = { input_tokens: 100, output_tokens: 20, total_tokens: 120, input_tokens_details: { cached_tokens: 30, cache_write_tokens: 10 } };

test("reserves full envelope before each request, counts cache, refuses the next before fetch", async () => {
  let calls = 0;
  const meter = new Meter(799999, model, async (_url, options) => { calls++; assert.equal(options.redirect, "error"); return sse(usage); });
  assert.equal(meter.result().input, 0);
  const response = await meter.fetch(url, opts);
  assert.equal(meter.result(), null, "in-flight is not zero usage");
  await response.text();
  assert.deepEqual(meter.result(), { input: 60, output: 20, cache_read: 30, cache_write: 10 });
  await assert.rejects(meter.fetch(url, opts), /request_budget_exhausted/);
  assert.equal(calls, 1);
  assert.equal(meter.requests, 1);
});

test("unknown stream, missing/negative/over-limit usage never refund or admit another request", async () => {
  for (const value of [undefined, { ...usage, total_tokens: 0 }, { ...usage, output_tokens: -1 }, { ...usage, input_tokens: 300000, total_tokens: 300020 }, { ...usage, input_tokens_details: { cached_tokens: 101 } }]) {
    let calls = 0;
    const meter = new Meter(800000, model, async () => { calls++; return sse(value); });
    await assert.rejects((await meter.fetch(url, opts)).text());
    assert.equal(meter.result(), null);
    await assert.rejects(meter.fetch(url, opts));
    assert.equal(calls, 1);
  }
});

test("auth and quota rejection pause without retry; network ambiguity retains the request", async () => {
  for (const status of [401, 403, 429, 500]) {
    let calls = 0;
    const meter = new Meter(800000, model, async () => { calls++; return new Response("never publish provider text", { status }); });
    if (status === 500) { await assert.rejects(meter.fetch(url, opts)); assert.equal(meter.result(), null); }
    else {
      const response = await meter.fetch(url, opts);
      assert.equal(response.status, status);
      assert.equal(meter.pause, status === 429 ? "quota_exhausted" : "auth_unavailable");
      assert.equal(meter.result().input, 0);
      assert.ok(!(await response.text()).includes("never publish"));
    }
    await assert.rejects(meter.fetch(url, opts));
    assert.equal(calls, 1);
  }
  const meter = new Meter(400000, model, async () => { throw new Error("private transport diagnostic"); });
  await assert.rejects(meter.fetch(url, opts), /usage_unknown/);
  assert.equal(meter.result(), null);
});

test("concurrent attempts, wrong provider/model/thinking/endpoint and oversized payload refuse", async () => {
  assert.throws(() => new Meter(400000, { ...model, id: "lower-model" }), /model_unavailable/);
  assert.throws(() => new Meter(400000, { ...model, thinkingLevelMap: {} }), /model_unavailable/);
  const meter = new Meter(800000, model, async () => sse(usage));
  const first = await meter.fetch(url, opts);
  await assert.rejects(meter.fetch(url, opts));
  await first.text();
  assert.equal(meter.requests, 1);
  for (const payload of [{ model: MODEL, reasoning: { effort: "high" } }, { model: MODEL, reasoning: { effort: "max" }, store: true, stream: true }, { model: MODEL, reasoning: { effort: "max" }, store: false, stream: true, input: "x".repeat(65536) }]) {
    assert.throws(() => new Meter(400000, model).payload(payload, model));
  }
  const wrong = new Meter(400000, model, () => assert.fail("wrong endpoint reached fetch"));
  await assert.rejects(wrong.fetch("https://example.com/", opts));
  assert.equal(wrong.requests, 0);
});

test("raw response bound and terminal duplication are not known usage", async () => {
  const meter = new Meter(400000, model, async () => new Response("data: " + "x".repeat(2 * 1024 * 1024 + 1)));
  await assert.rejects((await meter.fetch(url, opts)).text());
  assert.equal(meter.failure, "output_bound");
  assert.equal(meter.result(), null);
  const duplicate = new Meter(400000, model, async () => new Response((await sse(usage).text()).repeat(2)));
  await assert.rejects((await duplicate.fetch(url, opts)).text());
  assert.equal(duplicate.result(), null);
});

test("accounting outlives an SDK reader stopping at terminal and rejects late duplicate usage", async () => {
  for (const duplicate of [false, true]) {
    let source;
    const body = new ReadableStream({ start(controller) { source = controller; } });
    const meter = new Meter(400000, model, async () => new Response(body));
    const response = await meter.fetch(url, opts);
    const reader = response.body.getReader();
    source.enqueue(new TextEncoder().encode(await sse(usage).text()));
    assert.equal((await reader.read()).done, false);
    const cancelled = reader.cancel();
    assert.equal(meter.result(), null, "reader cancellation cannot settle an open stream");
    if (duplicate) source.enqueue(new TextEncoder().encode(await sse(usage).text()));
    source.close();
    await Promise.all([cancelled, meter.settle()]);
    assert.equal(meter.result() === null, duplicate);
    if (duplicate) assert.equal(meter.failure, "usage_unknown");
    else assert.equal(meter.result().input, 60);
  }
});

test("SSE quota error keeps unknown usage AND carries pause", async () => {
  const meter = new Meter(400000, model, async () => new Response('data: {"type":"error","code":"usage_limit_reached"}\n\n'));
  await assert.rejects((await meter.fetch(url, opts)).text());
  assert.equal(meter.pause, "quota_exhausted");
  assert.equal(meter.result(), null);
});
