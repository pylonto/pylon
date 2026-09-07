// Run only in the pinned image: network none, readonly root, no role mount.
import test from "node:test";
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { MAX_TOOL_ERROR_BYTES, toolErrorDetail, toolResultForModel } from "./tool.mjs";

const envelope = (detail) => ({ error: "sandbox_tool_failed", error_detail: detail });
function modelError(output, callback) {
  try { toolResultForModel(output, callback); }
  catch (error) { return error.message; }
  assert.fail("failure must throw so the actual SDK emits isError, never success content");
}

test("selected sandbox errors are identical with and without private observation", () => {
  const detail = toolErrorDetail({ message: "Before Git check\nfatal: not a git repository\nCommand exited with code 128", stack: "SDK_STACK_CANARY", headers: { authorization: "AUTH_CANARY" } });
  const seen = [];
  const off = modelError(envelope(detail));
  const on = modelError(envelope(detail), (value) => seen.push(value));
  assert.equal(off, detail.message);
  assert.equal(on, off);
  assert.deepEqual(seen, [detail]);
  assert.ok(!on.includes("CANARY"));
  assert.deepEqual(toolResultForModel({ content: [{ type: "text", text: "successful output" }] }), { content: [{ type: "text", text: "successful output" }], details: {} });
});

test("encoded error cap keeps preceding output and trailing failure with an explicit marker", () => {
  for (const middle of ["x", "\u0000日本語", "\ud800"]) {
    const detail = toolErrorDetail({ message: "First stdout\n" + middle.repeat(20000) + "\nCommand exited with code 7" });
    assert.equal(detail.truncated, true);
    assert.ok(detail.message.startsWith("First stdout"));
    assert.ok(detail.message.endsWith("Command exited with code 7"));
    assert.ok(detail.message.includes("[Sandbox tool error truncated]"));
    assert.ok(detail.message.isWellFormed());
    assert.ok(Buffer.byteLength(JSON.stringify(detail.message)) <= MAX_TOOL_ERROR_BYTES);
    assert.equal(modelError(envelope(detail)), detail.message);
  }
});

test("host/auth/transport categories and malformed failure envelopes cannot carry arbitrary detail", () => {
  const detail = toolErrorDetail({ message: "UNTRUSTED_CANARY" });
  for (const output of [null, [], {}, { error: "sandbox_tool_failed" },
    { error: "auth_unavailable", error_detail: detail },
    { error: "pi_exec_attach_failed", error_detail: detail },
    { error: "runtime_failed", error_detail: detail },
    { ...envelope(detail), provider: "UNTRUSTED_CANARY" },
    envelope({ ...detail, stack: "UNTRUSTED_CANARY" }),
    envelope({ ...detail, category: "UNTRUSTED_CANARY" }),
    envelope({ ...detail, truncated: null }),
    envelope({ ...detail, message: "x".repeat(MAX_TOOL_ERROR_BYTES + 1) }),
    { content: [{ type: "text", text: "safe", headers: "UNTRUSTED_CANARY" }] },
  ]) {
    let observed = false;
    assert.equal(modelError(output, () => { observed = true; }), "sandbox_tool_failed");
    assert.equal(observed, false);
  }
});

test("actual tool executable keeps parsing and dispatch failures categorical", () => {
  for (const input of ["INPUT_CANARY", JSON.stringify({ name: "UNKNOWN_TOOL_CANARY", args: {} })]) {
    const child = spawnSync(process.execPath, ["/opt/pylon/tool.mjs"], { input, encoding: "utf8", timeout: 5000, maxBuffer: 16384 });
    assert.equal(child.error, undefined);
    assert.equal(child.status, 0);
    assert.equal(child.stderr, "");
    assert.deepEqual(JSON.parse(child.stdout), { error: "sandbox_tool_failed" });
    assert.equal(modelError(JSON.parse(child.stdout)), "sandbox_tool_failed");
  }
});
