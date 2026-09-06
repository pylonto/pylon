// Run inside the immutable image with --network none and no role mount. This
// exercises the REAL pinned SDK/adapter with synthetic HTTP, not a model call.
import test from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { zstdDecompressSync } from "node:zlib";
import { createAgentSession, ModelRuntime, SessionManager, SettingsManager } from "@earendil-works/pi-coding-agent";
import { InMemoryCredentialStore, InMemoryModelsStore } from "@earendil-works/pi-ai";
import { Meter, MODEL, PROVIDER, THINKING } from "./meter.mjs";
import { installPiStream } from "./stream.mjs";
import { readToolInput } from "./tool.mjs";

// Deliberately unsigned, nonexistent account. The adapter needs the payload
// shape to build its header; this is NOT an OAuth credential or availability test.
const synthetic = "fixture." + Buffer.from(JSON.stringify({ "https://api.openai.com/auth": { chatgpt_account_id: "fixture-only" } })).toString("base64url") + ".fixture";
async function setup(fetch, budget = 400000) {
  const modelsStore = new InMemoryModelsStore();
  await modelsStore.write(PROVIDER, JSON.parse(await readFile("/opt/pylon/models-entry.json", "utf8")));
  const runtime = await ModelRuntime.create({ credentials: new InMemoryCredentialStore(), modelsPath: null, modelsStore });
  const model = runtime.getModel(PROVIDER, MODEL);
  const meter = new Meter(budget, model, fetch);
  const { session } = await createAgentSession({ cwd: "/tmp", agentDir: "/tmp/test-role", modelRuntime: runtime, model, thinkingLevel: THINKING, tools: [],
    sessionManager: SessionManager.inMemory(), settingsManager: SettingsManager.inMemory({ retry: { enabled: false }, compaction: { enabled: false } }) });
  installPiStream(session, { runtime, meter, auth: async () => synthetic, signal: AbortSignal.timeout(10000), timeoutMs: 5000 });
  return { session, meter };
}
const terminal = { type: "response.completed", response: { id: "fixture", status: "completed", usage: { input_tokens: 10, output_tokens: 5, total_tokens: 15, input_tokens_details: { cached_tokens: 2 } } } };

test("actual SDK hook sends exact max SSE through the meter and parses trusted usage", async () => {
  let calls = 0, request;
  const { session, meter } = await setup(async (url, options) => {
    calls++;
    const raw = new Headers(options.headers).get("content-encoding") === "zstd" ? zstdDecompressSync(options.body).toString() : options.body;
    request = { url, body: JSON.parse(raw), redirect: options.redirect };
    const item = { type: "message", id: "fixture-message", role: "assistant", content: [] };
    const events = [
      { type: "response.output_item.added", output_index: 0, item },
      { type: "response.content_part.added", output_index: 0, content_index: 0, part: { type: "output_text", text: "", annotations: [] } },
      { type: "response.output_text.delta", output_index: 0, content_index: 0, delta: "fixture" },
      { type: "response.output_item.done", output_index: 0, item: { ...item, content: [{ type: "output_text", text: "fixture", annotations: [] }] } },
      terminal,
    ];
    return new Response(events.map((event) => `data: ${JSON.stringify(event)}\n\n`).join(""), { headers: { "content-type": "text/event-stream" } });
  });
  const deltas = [];
  session.subscribe((event) => {
    if (event.type === "message_update" && typeof event.assistantMessageEvent.delta === "string") deltas.push(event.assistantMessageEvent.delta);
  });
  try {
    await session.agent.prompt("Synthetic adapter fixture only.");
    assert.ok(deltas.includes("fixture"), "the worker's actual SDK delta hook must execute");
    assert.equal(calls, 1);
    assert.equal(request.url, "https://chatgpt.com/backend-api/codex/responses");
    assert.equal(request.body.model, MODEL);
    assert.equal(request.body.reasoning?.effort, THINKING);
    assert.equal(request.body.stream, true);
    assert.equal(request.body.store, false);
    assert.equal(request.redirect, "error");
    assert.equal(session.messages.at(-1).stopReason, "stop", session.messages.at(-1).errorMessage);
    assert.deepEqual(meter.result(), { input: 8, output: 5, cache_read: 2, cache_write: 0 });
    await session.agent.prompt("The next request must fail BEFORE HTTP.");
    assert.equal(calls, 1);
    assert.equal(meter.failure, "request_budget_exhausted");
  } finally { session.dispose(); }
});

test("sandbox stdin preserves split UTF-8 and rejects malformed or oversized input", async () => {
  const input = JSON.stringify({ name: "write", args: { path: "src/text.txt", content: "naïve 日本語" } });
  async function* split() { for (const byte of Buffer.from(input)) yield Uint8Array.of(byte); }
  assert.equal(await readToolInput(split()), input);
  await assert.rejects(readToolInput([Uint8Array.of(0xff)]));
  await assert.rejects(readToolInput([new Uint8Array(256 * 1024 + 1)]), /input_bound/);
});

test("actual adapter enforces the payload ceiling before HTTP", async () => {
  let calls = 0;
  const { session, meter } = await setup(async () => { calls++; throw new Error("must not fetch"); });
  try {
    await session.agent.prompt("x".repeat(65536));
    assert.equal(calls, 0);
    assert.equal(meter.requests, 0);
    assert.equal(meter.failure, "output_bound");
  } finally { session.dispose(); }
});

test("actual adapter cannot retry HTTP quota or missing usage", async () => {
  for (const status of [429, 200]) {
    let calls = 0;
    const { session, meter } = await setup(async () => {
      calls++;
      return status === 429 ? new Response('{"error":{"message":"fixture quota"}}', { status }) :
        new Response('data: {"type":"response.completed","response":{"status":"completed"}}\n\n');
    }, 800000);
    try {
      await session.agent.prompt("Synthetic refusal fixture.");
      assert.equal(calls, 1);
      assert.equal(meter.requests, 1);
      assert.equal(meter.failure, status === 429 ? "quota_exhausted" : "usage_unknown");
      if (status === 200) assert.equal(meter.result(), null);
      else assert.equal(meter.pause, "quota_exhausted");
    } finally { session.dispose(); }
  }
});
