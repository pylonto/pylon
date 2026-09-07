// Internal, network-none fixtures only. No brief/config field enables this path.
import { zstdDecompressSync } from "node:zlib";
import { AssistantMessageEventStream, InMemoryCredentialStore } from "@earendil-works/pi-ai";
import { MODEL, PROVIDER, DEFAULT_THINKING, requireThinking } from "./meter.mjs";

export function fixtureStream(model, turn, fixtureCase) {
  const stream = new AssistantMessageEventStream();
  const content = turn === 0 ? [
    { type: "toolCall", id: "fixture-read", name: "read", arguments: { path: "src/repair.txt" } },
    { type: "toolCall", id: "fixture-edit", name: "edit", arguments: { path: "src/repair.txt", oldText: "broken", newText: "repaired" } },
    { type: "toolCall", id: "fixture-check", name: "bash", arguments: { command: "test ! -e /role && test ! -e /transport && test ! -e /var/run/docker.sock && test ! -e /workspace/.git && test \"$(cat src/repair.txt)\" = repaired && test \"$(ls /sys/class/net)\" = lo && test ! -w /opt/pylon/worker.mjs", timeout: 5 } },
  ] : [{ type: "text", text: "Fixture complete, not model evidence." }];
  if (turn === 0 && fixtureCase) {
    const commands = {
      hang: "node -e 'setInterval(() => {}, 1000)'",
      symlink: "rm src/repair.txt; ln -s /opt/pylon/worker.mjs src/repair.txt",
      mode: "chmod 0755 src/repair.txt",
      rename: "printf 'broken\\n' > src/repair.txt; mv src/repair.txt src/renamed.txt",
      oversized: "node -e 'require(\"fs\").writeFileSync(\"src/repair.txt\", \"x\".repeat(1048577))'",
    };
    if (!commands[fixtureCase]) throw new Error("runtime_failed");
    content[2].arguments = { command: commands[fixtureCase], timeout: 30 };
  }
  const message = { role: "assistant", api: model.api, provider: model.provider, model: model.id, content,
    stopReason: turn === 0 ? "toolUse" : "stop", timestamp: Date.now(),
    usage: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, totalTokens: 0, cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 } } };
  queueMicrotask(() => { stream.push({ type: "start", partial: message }); stream.push({ type: "done", reason: message.stopReason, message }); stream.end(); });
  return stream;
}

export const hiddenCanaries = ["CANARY_THINKING_PRIVATE", "CANARY_ENCRYPTED_PRIVATE", "CANARY_HEADER_PRIVATE", "CANARY_ENVELOPE_PRIVATE", "CANARY_ENVIRONMENT_PRIVATE"];
export function syntheticCredential(rotated = false) {
  // Unsigned and nonexistent. Not an OAuth credential or availability proof.
  const accountId = rotated ? "fixture-only-rotated" : "fixture-only";
  return { type: "oauth", accountId, access: "fixture." + Buffer.from(JSON.stringify({ "https://api.openai.com/auth": { chatgpt_account_id: accountId } })).toString("base64url") + ".fixture",
    refresh: rotated ? "CANARY_REFRESH_ROTATED" : "CANARY_REFRESH_INITIAL", expires: Date.now() + 3600000 };
}

const cases = {
  read: { name: "read", arguments: { path: "src/repair.txt" } },
  read_missing: { name: "read", arguments: { path: "src/missing.txt" } },
  edit: { name: "edit", arguments: { path: "src/repair.txt", oldText: "broken", newText: "repaired" } },
  edit_mismatch: { name: "edit", arguments: { path: "src/repair.txt", oldText: "not_present", newText: "repaired" } },
  edit_array: { name: "edit", arguments: { path: "src/repair.txt", edits: [{ oldText: "broken", newText: "repaired" }] } },
  edit_array_mismatch: { name: "edit", arguments: { path: "src/repair.txt", edits: [{ oldText: "not_present", newText: "repaired" }] } },
  write: { name: "write", arguments: { path: "src/repair.txt", content: "repaired\n" } },
  bash: { name: "bash", arguments: { command: "printf 'repaired\\n' > src/repair.txt", timeout: 5 } },
  hang: { name: "bash", arguments: { command: "node -e 'setInterval(() => {}, 1000)'", timeout: 30 } },
};
// Exercise an actual negative Node test, not a shell error mistaken for a test.
const negativeTest = `import test from "node:test"; import assert from "node:assert/strict"; import { readFileSync } from "node:fs"; test("requires repaired text", () => assert.equal(readFileSync("src/repair.txt", "utf8"), "repaired\\n"));`;
const runNegativeTest = `node --test-reporter=tap --input-type=module -e '${negativeTest}'`;
const failureRehearsals = {
  git_failure: {
    call: { name: "bash", arguments: { command: "test ! -e /workspace/.git && test ! -e /role && test ! -e /transport && printf 'Before Git check\\n' && git status --short && printf 'UNREACHED_AFTER_FAILURE\\n'", timeout: 5 } },
    expected: ["Before Git check", "fatal: not a git repository", "Command exited with code 128"],
    repair: cases.edit_array,
  },
  test_failure: {
    call: { name: "bash", arguments: { command: `printf 'Before negative test\\n' && ${runNegativeTest} && printf 'UNREACHED_AFTER_FAILURE\\n'`, timeout: 5 } },
    expected: ["Before negative test", "not ok 1 - requires repaired text", "Command exited with code 1"],
    repair: { name: "bash", arguments: { command: `printf 'repaired\\n' > src/repair.txt && ${runNegativeTest}`, timeout: 5 } },
  },
};
function textEvents(index, text, split = false) {
  const item = { type: "message", id: `synthetic-message-${index}`, role: "assistant", content: [] };
  const chunks = split ? [text.slice(0, 31), text.slice(31, 75), text.slice(75)] : [text];
  return [
    { type: "response.output_item.added", output_index: index, item },
    { type: "response.content_part.added", output_index: index, content_index: 0, part: { type: "output_text", text: "", annotations: [] } },
    ...chunks.map((delta) => ({ type: "response.output_text.delta", output_index: index, content_index: 0, delta })),
    { type: "response.output_item.done", output_index: index, item: { ...item, content: [{ type: "output_text", text, annotations: [] }] } },
  ];
}
function callEvents(index, tool) {
  const item = { type: "function_call", id: `synthetic-item-${index}`, call_id: `synthetic-call-${index}`, name: tool.name, arguments: "" };
  const args = JSON.stringify(tool.arguments);
  return [{ type: "response.output_item.added", output_index: index, item },
    { type: "response.function_call_arguments.delta", output_index: index, item_id: item.id, delta: args },
    { type: "response.output_item.done", output_index: index, item: { ...item, arguments: args } }];
}

export async function createSDKFixture(name, thinking = DEFAULT_THINKING) {
  requireThinking(thinking);
  const match = /^sdk_(session|agent)_(.+)$/.exec(name ?? "");
  if (!match) return undefined;
  const mode = match[1], selected = match[2];
  const failureRehearsal = failureRehearsals[selected];
  if (!cases[selected] && !failureRehearsal && !["privacy", "recover", "workspace_prompt"].includes(selected)) throw new Error("runtime_failed");
  const requestLimit = selected === "recover" || failureRehearsal ? 3 : 2;
  let modelError, recovered = false;
  const credentials = new InMemoryCredentialStore();
  await credentials.modify(PROVIDER, () => syntheticCredential());
  let requests = 0, unexpected = 0, sawThinking = false, sawSignature = false;
  const fetch = async (url, options) => {
    if (String(url) !== "https://chatgpt.com/backend-api/codex/responses" || ++requests > requestLimit) { unexpected++; throw new Error("runtime_failed"); }
    const raw = new Headers(options.headers).get("content-encoding") === "zstd" ? zstdDecompressSync(options.body, { maxOutputLength: 65536 }).toString() : options.body;
    const body = JSON.parse(raw);
    const tools = new Map(body.tools.map((t) => [t.name, t]));
    const expected = { read: ["limit", "offset", "path"], write: ["content", "path"], bash: ["command", "timeout"], edit: ["edits", "path"] };
    if (body.model !== MODEL || body.reasoning?.effort !== thinking || body.stream !== true || tools.size !== 4) throw new Error("runtime_failed");
    for (const [name, keys] of Object.entries(expected)) if (JSON.stringify(Object.keys(tools.get(name)?.parameters.properties ?? {}).sort()) !== JSON.stringify(keys)) throw new Error("runtime_failed");
    if (JSON.stringify(Object.keys(tools.get("edit").parameters.properties.edits.items.properties).sort()) !== '["newText","oldText"]') throw new Error("runtime_failed");
    if (selected === "workspace_prompt" && !["plain-files snapshot without .git", "controller exports the patch", "filesystem edits"].every((s) => body.instructions?.includes(s))) throw new Error("runtime_failed");
    if (failureRehearsal && requests > 1) {
      const outputs = body.input.filter((item) => item.type === "function_call_output");
      // Both the SDK's model-context toolResult and the actual adapter payload
      // must contain the error. Private debug text alone is not this evidence.
      if (typeof modelError !== "string" || !failureRehearsal.expected.every((s) => modelError.includes(s)) || modelError.includes("UNREACHED_AFTER_FAILURE") || outputs[0]?.output !== modelError) throw new Error("runtime_failed");
      if (requests === 3 && (!recovered || selected === "test_failure" && !outputs.at(-1)?.output.includes("ok 1 - requires repaired text"))) throw new Error("runtime_failed");
    }
    const events = [];
    if (selected === "privacy") {
      const credential = syntheticCredential(requests > 1);
      const text = `Visible permitted text ${credential.access} ${credential.refresh} ${credential.accountId} retained.`;
      const item = { type: "reasoning", id: "synthetic-thinking", summary: [], encrypted_content: hiddenCanaries[1] };
      events.push({ type: "response.output_item.added", output_index: 0, item },
        { type: "response.reasoning_summary_text.delta", output_index: 0, delta: hiddenCanaries[0] },
        { type: "response.output_item.done", output_index: 0, item: { ...item, summary: [{ type: "summary_text", text: hiddenCanaries[0] }] } },
        ...textEvents(1, text, true));
      if (requests === 1) {
        events.push(...callEvents(2, { name: "bash", arguments: { command: `printf 'Permitted tool result ${credential.access} ${credential.refresh}'`, timeout: 5 } }),
          ...callEvents(3, { name: "read", arguments: { path: `src/Permitted tool error ${credential.refresh}.txt` } }));
        await credentials.modify(PROVIDER, () => syntheticCredential(true));
      }
    } else if (requests === 1 || (selected === "recover" || failureRehearsal) && requests === 2) {
      events.push(...textEvents(0, "Visible synthetic tool inspection."));
      const chosen = failureRehearsal ? (requests === 1 ? failureRehearsal.call : failureRehearsal.repair) :
        selected === "recover" ? (requests === 1 ? cases.edit_array_mismatch : cases.edit_array) : selected === "workspace_prompt" ? cases.read : cases[selected];
      // Distinct call identity across recovery turns without changing arguments.
      events.push(...callEvents(requests, chosen));
    } else { events.push(...textEvents(0, "Visible synthetic completion, not model evidence.")); }
    events.push({ type: "response.completed", response: { id: "synthetic-response", status: "completed", usage: { input_tokens: 10, output_tokens: 5, total_tokens: 15, input_tokens_details: { cached_tokens: 2 } }, ignored: hiddenCanaries[3] } });
    return new Response(events.map((e) => `data: ${JSON.stringify(e)}\n\n`).join(""), { headers: { "content-type": "text/event-stream", "x-fixture-private": hiddenCanaries[2] } });
  };
  // Do not retain a network-capable fetch in a fixture. All accidental catalog,
  // auth or unmetered provider attempts fail or violate the final count fence.
  Object.defineProperty(globalThis, "fetch", { configurable: true, get: () => fetch, set: () => {} });
  return { mode, credentials, fetch,
    observe(event) {
      if (failureRehearsal && event.type === "message_end" && event.message?.role === "toolResult") {
        if (event.message.isError) modelError = event.message.content.filter((c) => c.type === "text").map((c) => c.text).join("\n");
        else recovered = true;
      }
      if (event.type !== "message_end" || event.message?.role !== "assistant") return;
      for (const block of event.message.content) if (block.type === "thinking") {
        sawThinking ||= block.thinking.includes(hiddenCanaries[0]);
        sawSignature ||= block.thinkingSignature?.includes(hiddenCanaries[1]);
      }
    },
    assertComplete(meter) {
      if (unexpected || requests !== requestLimit || requests !== meter.requests || selected === "privacy" && (!sawThinking || !sawSignature)) throw new Error("runtime_failed");
    },
  };
}
