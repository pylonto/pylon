import test from "node:test";
import assert from "node:assert/strict";
import { Meter, MODEL, PROVIDER } from "./meter.mjs";
import * as limits from "./meter.mjs";

// Synthetic metadata is a unit fixture, NEVER provider/model availability proof.
const model = { provider: PROVIDER, id: MODEL, api: "openai-codex-responses", baseUrl: "https://chatgpt.com/backend-api", contextWindow: 272000, maxTokens: 128000, thinkingLevelMap: { max: "max", medium: "medium" } };
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
  for (const payload of [{ model: MODEL, reasoning: { effort: "high" } }, { model: MODEL, reasoning: { effort: "max" }, store: true, stream: true }, { model: MODEL, reasoning: { effort: "max" }, store: false, stream: true, input: "x".repeat(262144) }]) {
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

// These numbers deliberately do not derive from the implementation's constant:
// restoring 64 KiB, widening the budget or changing > to >= must turn tests red.
test("aggregate request ceiling has its own explicit owner", () => {
  assert.equal(limits.MAX_REQUEST_BYTES, 262144);
});
const requestBody = (effort = "max") => ({ model: MODEL, reasoning: { effort }, store: false, stream: true,
  instructions: "fixed", tools: [{ type: "function", name: "read", description: "fixed" }],
  input: [{ role: "user", content: "" }] });
function exactRequest(bytes, unit, effort) {
  const body = requestBody(effort);
  const remaining = bytes - Buffer.byteLength(JSON.stringify(body));
  const width = Buffer.byteLength(JSON.stringify(unit)) - 2;
  body.input[0].content = unit.repeat(Math.floor(remaining / width)) + "x".repeat(remaining % width);
  assert.equal(Buffer.byteLength(JSON.stringify(body)), bytes);
  return body;
}
for (const [label, unit] of Object.entries({ ascii: "a", multibyte: "é中", astral: "🧪", escaped: "\"\\\n\r\t\u0000\u0001" })) {
  for (const bytes of [262143, 262144, 262145]) for (const thinking of ["medium", "max"]) for (const prior of [0, 1]) {
    test(`aggregate UTF-8 JSON ${label} bytes=${bytes} thinking=${thinking} prior=${prior}`, async () => {
      let calls = 0;
      const meter = new Meter(1600000, model, async () => { calls++; return sse(usage); }, thinking);
      if (prior) {
        meter.payload(requestBody(thinking), model);
        await (await meter.fetch(url, opts)).text();
        await meter.settle();
      }
      const previous = structuredClone(meter.result());
      const body = exactRequest(bytes, unit, thinking);
      const before = JSON.stringify(body);
      if (bytes > 262144) {
        assert.throws(() => meter.payload(body, model), /^Error: output_bound$/);
        assert.equal(meter.failure, "output_bound");
        await assert.rejects(meter.fetch(url, opts), /^Error: output_bound$/);
      } else {
        assert.doesNotThrow(() => meter.payload(body, model));
        assert.equal(meter.failure, "");
      }
      // onPayload is before HTTP/reservation, not before adapter auth resolution.
      assert.equal(calls, prior);
      assert.equal(meter.requests, prior);
      assert.equal(meter.reserved, prior * 400000);
      assert.equal(meter.tokens, 1600000);
      assert.equal(meter.pending, false);
      assert.equal(meter.pause, "");
      assert.deepEqual(meter.result(), previous);
      assert.equal(JSON.stringify(body) === before, true, "no body rewriting or truncation");
      if (bytes <= 262144) {
        await (await meter.fetch(url, opts)).text();
        await meter.settle();
        assert.equal(calls, prior + 1);
        assert.equal(meter.requests, prior + 1);
        assert.equal(meter.reserved, (prior + 1) * 400000);
        assert.deepEqual(meter.result(), { input: 60 * (prior + 1), output: 20 * (prior + 1), cache_read: 30 * (prior + 1), cache_write: 10 * (prior + 1) });
      }
    });
  }
}
for (const field of ["provider", "model", "body_model", "effort", "store", "stream", "background", "previous_response"]) {
  test(`aggregate cap retains non-size payload predicate ${field}`, () => {
    const meter = new Meter(1600000, model), chosen = { ...model }, body = requestBody();
    if (field === "provider") chosen.provider = "wrong";
    if (field === "model") chosen.id = "wrong";
    if (field === "body_model") body.model = "wrong";
    if (field === "effort") body.reasoning.effort = "medium";
    if (field === "store") body.store = true;
    if (field === "stream") body.stream = false;
    if (field === "background") body.background = true;
    if (field === "previous_response") body.previous_response_id = "fixture";
    assert.throws(() => meter.payload(body, chosen), /^Error: output_bound$/);
    assert.equal(meter.requests, 0);
    assert.equal(meter.reserved, 0);
    assert.equal(meter.pending, false);
  });
}

test("SSE quota error keeps unknown usage AND carries pause", async () => {
  const meter = new Meter(400000, model, async () => new Response('data: {"type":"error","code":"usage_limit_reached"}\n\n'));
  await assert.rejects((await meter.fetch(url, opts)).text());
  assert.equal(meter.pause, "quota_exhausted");
  assert.equal(meter.result(), null);
});
