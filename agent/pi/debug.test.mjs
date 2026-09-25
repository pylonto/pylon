import test from "node:test";
import assert from "node:assert/strict";
import { readFile, mkdtemp, writeFile, chmod, symlink, link, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { DebugCapture } from "./debug.mjs";
import { readRoleCredential } from "./role-auth.mjs";
import { toolErrorDetail } from "./tool.mjs";
import { PROVIDER } from "./meter.mjs";

const limits = JSON.parse(await readFile(new URL("./debug-limits.fixture.json", import.meta.url), "utf8"));
function capture() {
  const frames = [];
  return { frames, debug: new DebugCapture(limits, async (frame) => { frames.push(structuredClone(frame)); }) };
}
const assistant = (text, extra = {}) => ({ type: "message_end", message: { role: "assistant", stopReason: "stop", content: [{ type: "text", text }], ...extra } });
const initial = "ACCESS_PRIVATE_CANARY_abcdefghij0123456789";
const refreshed = "REFRESH_PRIVATE_CANARY_zyxwvutsrq9876543210";

test("disabled capture visits no data, starts no requests and creates no pending work", async () => {
  let calls = 0;
  const debug = new DebugCapture(undefined, () => { calls++; });
  const hostile = { get type() { throw new Error("must not inspect"); } };
  debug.observe(hostile); debug.emit(hostile); debug.remember([initial]);
  assert.equal(await debug.close(), undefined); assert.equal(calls, 0); assert.equal(debug.pendingBytes, 0);
});

test("selected SDK text and all tool families survive; credentials and excluded structures do not", async () => {
  const { debug, frames } = capture();
  debug.rememberCredential({ access: initial, refresh: refreshed, accountId: "ACCOUNT_PRIVATE_CANARY" });
  const excluded = "STRUCTURE_PRIVATE_CANARY";
  debug.observe(assistant(`Visible permitted ${initial} tail.`, { content: [
    { type: "thinking", thinking: excluded, thinkingSignature: excluded },
    { type: "text", text: `Visible permitted ${initial} tail.`, signature: excluded },
    { type: "image", data: excluded },
  ], errorMessage: excluded, headers: { authorization: excluded }, env: { SECRET: excluded } }));
  await debug.pumping;
  for (const toolName of ["read", "write", "edit", "bash"]) {
    debug.observe({ type: "tool_execution_start", toolCallId: toolName, toolName,
      args: { path: "src/repair.txt", content: `Permitted argument ${refreshed}`, env: { SECRET: excluded }, thinking: excluded } });
    debug.observe({ type: "tool_execution_end", toolCallId: toolName, toolName, isError: false,
      result: { content: [{ type: "text", text: `Permitted result ${initial}` }], details: { authorization: excluded } } });
    await debug.pumping;
  }
  debug.toolError("edit", "edit", { message: `Permitted tool error ${refreshed}`, category: "ENOENT" });
  assert.equal(await debug.close(), true);
  const raw = JSON.stringify(frames);
  for (const secret of [initial, refreshed, excluded, "ACCOUNT_PRIVATE_CANARY"]) assert.ok(!raw.includes(secret));
  assert.ok(raw.includes("Visible permitted")); assert.ok(raw.includes("Permitted argument")); assert.ok(raw.includes("Permitted result")); assert.ok(raw.includes("Permitted tool error"));
  assert.equal(frames.filter((f) => f.event?.kind === "tool_start").length, 4);
  assert.equal(frames.filter((f) => f.event?.kind === "tool_end").length, 4);
  assert.equal(frames.at(-1).close.incomplete, false);
});

test("initial and refreshed values cannot escape through deltas, record edges or byte caps", async () => {
  const { debug, frames } = capture();
  debug.remember([initial]);
  debug.observe({ type: "message_update", assistantMessageEvent: { type: "text_delta", delta: initial.slice(0, 20) } });
  debug.observe({ type: "message_update", assistantMessageEvent: { type: "text_delta", delta: initial.slice(20) } });
  assert.equal(debug.pendingEvents, 0);
  debug.observe(assistant(`Visible ${initial}`));
  debug.remember([refreshed]);
  debug.emit({ kind: "assistant_text", text: "Allowed prefix " + refreshed.slice(0, 20) });
  debug.emit({ kind: "assistant_text", text: refreshed.slice(20) + " allowed suffix" });
  debug.emit({ kind: "assistant_text", text: "x".repeat(limits.event_bytes - 4096 - 4) + refreshed + " trailing" });
  assert.equal(await debug.close(), true);
  const raw = JSON.stringify(frames);
  for (const secret of [initial, refreshed, refreshed.slice(0, 20), refreshed.slice(20)]) assert.ok(!raw.includes(secret));
  assert.ok(raw.includes("Allowed prefix")); assert.ok(raw.includes("allowed suffix"));
  assert.ok(frames.some((f) => f.event?.truncated));
});

test("encoded Unicode/control bounds precede serialization of oversized and nested inputs", async () => {
  const { debug, frames } = capture();
  const huge = "日本語\n\"\u0000".repeat(100000);
  debug.observe(assistant("", { content: [{ type: "text", text: huge }, { get type() { throw new Error("visited beyond cap"); } }] }));
  debug.emit({ kind: "tool_start", tool: "edit", tool_index: 1, arguments: { path: "src/repair.txt", edits: Array.from({ length: 10000 }, () => ({ oldText: huge, newText: huge })) } });
  let nested = "end";
  for (let i = 0; i < 100; i++) nested = { content: nested };
  debug.emit({ kind: "tool_start", tool: "write", tool_index: 2, arguments: nested });
  assert.equal(await debug.close(), true);
  for (const frame of frames) assert.ok(Buffer.byteLength(JSON.stringify(frame)) + 128 <= limits.event_bytes);
  assert.ok(frames.filter((f) => f.event).every((f) => f.event.truncated));
  assert.ok(debug.peakBytes <= limits.queue_bytes); assert.ok(debug.peakEvents <= limits.queue_events);
});

test("one stalled sender bounds queued/inflight work and accounts drops at closure", async () => {
  let release, started, active = 0, peakActive = 0;
  const firstStarted = new Promise((resolve) => { started = resolve; });
  const gate = new Promise((resolve) => { release = resolve; });
  const frames = [];
  const debug = new DebugCapture(limits, async (frame) => {
    active++; peakActive = Math.max(peakActive, active); started();
    await gate; frames.push(frame); active--;
  });
  debug.emit({ kind: "assistant_text", text: "First known event" }); await firstStarted;
  for (let i = 0; i < 1000; i++) debug.emit({ kind: "assistant_text", text: "x".repeat(12000) });
  assert.ok(debug.pendingEvents <= limits.queue_events); assert.ok(debug.pendingBytes <= limits.queue_bytes);
  release(); assert.equal(await debug.close(), true);
  assert.equal(peakActive, 1); assert.ok(frames.at(-1).close.dropped_events > 0); assert.equal(frames.at(-1).close.truncated, true);
  assert.equal(debug.pendingBytes, 0); assert.equal(debug.pendingEvents, 0);
});

test("drain is bounded even if a fixture receiver ignores cancellation", async () => {
  const debug = new DebugCapture(limits, () => new Promise(() => {}));
  debug.emit({ kind: "assistant_text", text: "Known queued event" });
  // The referenced test deadline keeps Node alive; AbortSignal.timeout is unref'd.
  const guard = setTimeout(() => {}, limits.drain_timeout_ms + 1000);
  const start = performance.now();
  try {
    assert.equal(await debug.close(), false); assert.equal(debug.incomplete, true);
    assert.ok(performance.now() - start < limits.drain_timeout_ms + 500);
    assert.ok(debug.pendingEvents <= limits.queue_events);
  } finally { clearTimeout(guard); }
});

test("lost close acknowledgement and redaction exhaustion cannot claim complete capture", async () => {
  const debug = new DebugCapture(limits, async (frame) => { if (frame.close) throw new Error("lost acknowledgement"); });
  debug.emit({ kind: "assistant_text", text: "Known accepted event" });
  assert.equal(await debug.close(), false);
  const exhausted = capture();
  exhausted.debug.remember(Array.from({ length: limits.secret_values + 1 }, (_, i) => `SECRET_VALUE_${i}`));
  exhausted.debug.emit({ kind: "assistant_text", text: "SECRET_VALUE_32" });
  assert.equal(await exhausted.debug.close(), false);
  assert.equal(exhausted.frames.filter((f) => f.event).length, 0);
  assert.equal(exhausted.frames.at(-1).close.incomplete, true);
});

test("event count is bounded independently of a fast queue", async () => {
  const { debug, frames } = capture();
  for (let i = 0; i < limits.events + 1; i++) { debug.emit({ kind: "assistant_text", text: `Visible event ${i}` }); await debug.pumping; }
  assert.equal(await debug.close(), true);
  assert.equal(frames.filter((f) => f.event).length, limits.events);
  assert.equal(frames.at(-1).close.dropped_events, 1);
});

test("bounded tool errors keep useful details without SDK stacks or arbitrary metadata", () => {
  const detail = toolErrorDetail({ message: "Useful missing path", code: "ENOENT", stack: "STACK_PRIVATE_CANARY", headers: "HEADER_PRIVATE_CANARY" });
  assert.deepEqual(detail, { category: "ENOENT", message: "Useful missing path", truncated: false });
  const large = toolErrorDetail({ message: "\u0000日本語".repeat(20000), code: "PRIVATE_CODE_CANARY" });
  assert.equal(large.category, "execution_failed"); assert.equal(large.truncated, true); assert.ok(Buffer.byteLength(JSON.stringify(large.message)) <= 8192);
});

test("runtime credential read is bounded/no-follow and stays restricted to synthetic role OAuth", async () => {
  const root = await mkdtemp(join(tmpdir(), "pi-debug-auth-test-"));
  const path = join(root, "auth.json");
  const value = { type: "oauth", access: initial, refresh: refreshed, expires: Date.now() + 100000 };
  try {
    await writeFile(path, JSON.stringify({ [PROVIDER]: value }), { mode: 0o600 });
    assert.deepEqual(await readRoleCredential(path), value);
    await chmod(path, 0o644); await assert.rejects(readRoleCredential(path)); await chmod(path, 0o600);
    await link(path, join(root, "hardlink")); await assert.rejects(readRoleCredential(path)); await rm(join(root, "hardlink"));
    await symlink(path, join(root, "symlink")); await assert.rejects(readRoleCredential(join(root, "symlink")));
    await writeFile(path, "x".repeat(16385)); await assert.rejects(readRoleCredential(path));
    await writeFile(path, JSON.stringify({ [PROVIDER]: { type: "api_key", key: initial } })); await assert.rejects(readRoleCredential(path));
  } finally { await rm(root, { recursive: true, force: true }); }
});
