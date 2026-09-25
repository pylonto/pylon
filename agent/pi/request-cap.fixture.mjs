// Network-none test helper, never a brief/config option or a live worker path.
// Shared by SDK regressions and the private native worker observation overlay.
import { createHash } from "node:crypto";
import { zstdDecompressSync } from "node:zlib";
import { InMemoryCredentialStore } from "@earendil-works/pi-ai";
import { MODEL, PROVIDER, MAX_REQUEST_BYTES, requireThinking } from "./meter.mjs";

const demand = (ok) => { if (!ok) throw new Error("request_cap_fixture_failed"); };
const bytes = (x) => Buffer.byteLength(JSON.stringify(x));
const digest = (x) => createHash("sha256").update(x).digest("hex");
const read = (path, extra = {}) => ({ name: "read", arguments: { path, ...extra } });
const plans = {
  tiny: [[read("AGENTS.md", { limit: 1 })]],
  full: [[read("AGENTS.md")], [read("docs/PROJECT_STATE.md")], [read("crates/ciao-host/src/codex_history.rs")]],
  chunks: [1, 21, 41].map((offset) => [read("docs/PROJECT_STATE.md", { offset, limit: 20 })]),
  // Six distinct legal results cross the OLD 256 KiB aggregate ceiling.
  // They now fit without any tool-size change, truncation or compaction.
  aggregate: [0, 2, 4].map((start) => [start, start + 1].map((n) => ({ name: "bash", arguments: {
    command: `node -e 'process.stdout.write("fixture-${n}:" + "x".repeat(48000))'`, timeout: 5,
  } }))),
  // Escaped control text is 32 KiB at the SDK tool and about 192 KiB encoded:
  // each result stays below the unchanged RPC cap; 44 accumulated results
  // exceed 8 MiB before HTTP 23. No repository mutation or larger tool bound.
  over: Array.from({ length: 22 }, (_, turn) => [turn * 2, turn * 2 + 1].map((n) => ({ name: "bash", arguments: {
    command: `node -e 'process.stdout.write("fixture-${n}:" + "\\u0001".repeat(32000))'`, timeout: 5,
  } }))),
};
export async function createRequestCapFixture(name, thinking, credential) {
  requireThinking(thinking);
  const plan = plans[name]; demand(Boolean(plan));
  const scheduled = plan.flat(), results = [], payloads = [];
  const credentials = new InMemoryCredentialStore();
  await credentials.modify(PROVIDER, () => credential);
  let requests = 0, issued = 0, unexpected = 0, stop = "", deltaBytes = 0;
  let sseBytes = 0, sseLine = 0;
  const fetch = async (url, options) => {
    if (String(url) !== "https://chatgpt.com/backend-api/codex/responses" || options.method !== "POST" || requests >= plan.length + 1) {
      unexpected++; throw new Error("request_cap_fixture_failed");
    }
    const raw = new Headers(options.headers).get("content-encoding") === "zstd" ?
      zstdDecompressSync(options.body, { maxOutputLength: MAX_REQUEST_BYTES }).toString() : options.body;
    const body = JSON.parse(raw), row = payloads.at(-1);
    demand(row && row.json_bytes === Buffer.byteLength(raw) && row.sha256 === digest(raw));
    demand(body.model === MODEL && body.reasoning?.effort === thinking && body.store === false && body.stream === true);
    row.http = true;
    const calls = plan[requests++] ?? [];
    const events = [];
    for (const call of calls) {
      const index = issued++, item = { type: "function_call", id: `cap-item-${index}`, call_id: `cap-call-${index}`, name: call.name, arguments: "" };
      const args = JSON.stringify(call.arguments);
      events.push({ type: "response.output_item.added", output_index: index, item },
        { type: "response.function_call_arguments.delta", output_index: index, item_id: item.id, delta: args },
        { type: "response.output_item.done", output_index: index, item: { ...item, arguments: args } });
    }
    events.push({ type: "response.completed", response: { id: "cap-response", status: "completed", usage: {
      input_tokens: 10, output_tokens: 5, total_tokens: 15, input_tokens_details: { cached_tokens: 2 },
    } } });
    const lines = events.map((e) => `data: ${JSON.stringify(e)}`);
    const response = lines.join("\n\n") + "\n\n";
    sseBytes = Math.max(sseBytes, Buffer.byteLength(response));
    sseLine = Math.max(sseLine, ...lines.map((x) => x.length));
    return new Response(response, { headers: { "content-type": "text/event-stream" } });
  };
  // Never retain a network-capable fallback, even for accidental auth/catalog IO.
  Object.defineProperty(globalThis, "fetch", { configurable: true, get: () => fetch, set: () => {} });
  return { mode: "session", credentials, fetch,
    observePayload(body, model, meter) {
      demand(payloads.length < plan.length + 1);
      const nonSize = model.provider === PROVIDER && model.id === MODEL && body.model === MODEL &&
        body.reasoning?.effort === thinking && body.store === false && body.stream === true && !body.background && !body.previous_response_id;
      demand(nonSize);
      const outputs = body.input.filter((x) => x.type === "function_call_output"), calls = body.input.filter((x) => x.type === "function_call");
      demand(outputs.length === results.length && calls.length === results.length);
      demand(new Set(calls.map((c) => c.call_id)).size === calls.length);
      for (let i = 0; i < results.length; i++) {
        demand(outputs[i].output === results[i].text && outputs[i].call_id === calls[i].call_id);
        demand(calls[i].name === scheduled[i].name && JSON.stringify(JSON.parse(calls[i].arguments)) === JSON.stringify(scheduled[i].arguments));
      }
      const raw = JSON.stringify(body);
      payloads.push({ json_bytes: Buffer.byteLength(raw), sha256: digest(raw), tool_results: results.length,
        included_exactly: true, non_size_passed: true, reserved_before: meter.reserved, requests_before: meter.requests,
        usage_before: { ...meter.result() }, http: false });
    },
    observe(event) {
      if (event.type === "message_update" && typeof event.assistantMessageEvent.delta === "string") deltaBytes += Buffer.byteLength(event.assistantMessageEvent.delta);
      if (event.type !== "message_end") return;
      if (event.message.role === "assistant") stop = event.message.stopReason;
      if (event.message.role !== "toolResult") return;
      const m = event.message;
      demand(results.length < scheduled.length && !m.isError && m.toolName === scheduled[results.length].name);
      demand(m.content.length > 0 && m.content.every((x) => x.type === "text"));
      const text = m.content.map((x) => x.text).join("\n"), jsonBytes = bytes({ content: m.content });
      demand(text.length > 0 && jsonBytes < 262144);
      if (name === "aggregate") demand(text === `fixture-${results.length}:` + "x".repeat(48000));
      if (name === "over") demand(text === `fixture-${results.length}:` + "\u0001".repeat(32000));
      results.push({ text, json_bytes: jsonBytes, text_bytes: Buffer.byteLength(text), sha256: digest(text) });
    },
    assertComplete(meter) {
      const over = name === "over", expectedRequests = over ? 22 : plan.length + 1;
      demand(MAX_REQUEST_BYTES === 8388608 && unexpected === 0 && issued === scheduled.length && results.length === scheduled.length);
      demand(requests === expectedRequests && meter.requests === requests && meter.reserved === requests * 400000 && meter.tokens === (over ? 20000000 : 1600000));
      demand(payloads.length === plan.length + 1 && payloads.length > 1 && new Set(results.map((r) => r.sha256)).size === results.length);
      for (let i = 0; i < payloads.length; i++) {
        const row = payloads[i], expectedResults = plan.slice(0, i).flat().length;
        demand(row.tool_results === expectedResults && row.reserved_before === i * 400000 && row.requests_before === i);
        demand(row.usage_before.input === i * 8 && row.usage_before.output === i * 5 && row.usage_before.cache_read === i * 2 && row.usage_before.cache_write === 0);
        demand(row.http === (i < expectedRequests));
        demand(i === 0 || row.json_bytes > payloads[i - 1].json_bytes);
        demand((row.json_bytes > 8388608) === (over && i === payloads.length - 1));
      }
      demand(stop === (over ? "error" : "stop") && meter.failure === (over ? "output_bound" : "") && meter.pause === "" && !meter.pending);
      const u = meter.result(); demand(u.input === requests * 8 && u.output === requests * 5 && u.cache_read === requests * 2 && u.cache_write === 0);
      demand(deltaBytes > 0 && deltaBytes < 2097152 && sseBytes > 0 && sseBytes < 16777216 && sseLine > 0 && sseLine < 2097152);
      if (name === "tiny" || name === "chunks") demand(payloads.at(-1).json_bytes < 65536);
      if (name === "full") demand(payloads.at(-1).json_bytes > 65536);
      if (name === "aggregate") demand(payloads.at(-1).json_bytes > 262144);
      return { case: name, thinking, assertions_passed: true, requests, tools: results.length, payloads,
        tool_results: results.map(({ text: _text, ...metadata }) => metadata), reserved: meter.reserved, usage: u,
        max_sse_bytes: sseBytes, max_sse_line_utf16: sseLine, content_delta_bytes: deltaBytes, failure: meter.failure };
    },
  };
}
