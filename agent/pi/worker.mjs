import { readFile, stat } from "node:fs/promises";
import http from "node:http";
import { createAgentSession, createCodingTools, createExtensionRuntime, ModelRuntime, SessionManager, SettingsManager } from "@earendil-works/pi-coding-agent";
import { AssistantMessageEventStream, InMemoryCredentialStore, InMemoryModelsStore } from "@earendil-works/pi-ai";
import { Meter, MODEL, PROVIDER, THINKING, zeroUsage } from "./meter.mjs";
import { installPiStream } from "./stream.mjs";

const socketPath = "/transport/pi.sock";
const bound = 256 * 1024;
function rpc(path, data, signal) {
  return new Promise((resolve, reject) => {
    const raw = data === undefined ? undefined : JSON.stringify(data);
    if (raw && Buffer.byteLength(raw) > bound) return reject(new Error("output_bound"));
    const req = http.request({ socketPath, path, method: raw === undefined ? "GET" : "POST", signal,
      headers: raw === undefined ? {} : { "content-type": "application/json", "content-length": Buffer.byteLength(raw) } }, (res) => {
      const chunks = [];
      let bytes = 0;
      res.on("data", (chunk) => {
        bytes += chunk.length;
        if (bytes > bound) res.destroy(new Error("output_bound"));
        else chunks.push(chunk);
      });
      res.on("error", reject);
      res.on("end", () => {
        try {
          if (res.statusCode !== 200) throw new Error("runtime_failed");
          resolve(JSON.parse(Buffer.concat(chunks).toString("utf8")));
        } catch { reject(new Error("runtime_failed")); }
      });
    });
    req.on("error", reject);
    req.end(raw);
  });
}

// No discovered settings, extensions, context files, packages, skills or commands.
// Repository instructions are data inside the signed brief and remote read tool.
const resources = {
  getExtensions: () => ({ extensions: [], errors: [], runtime: createExtensionRuntime() }),
  getSkills: () => ({ skills: [], diagnostics: [] }), getPrompts: () => ({ prompts: [], diagnostics: [] }),
  getThemes: () => ({ themes: [], diagnostics: [] }), getAgentsFiles: () => ({ agentsFiles: [] }),
  getSystemPrompt: () => "Repair only the signed maintenance brief in /workspace. All tools run in a disposable offline sandbox. No publication or host access is available. Produce a minimal text patch and preserve tests. Be concise.",
  getSystemPromptSource: () => undefined, getAppendSystemPrompt: () => [], getAppendSystemPromptSources: () => [],
  extendResources: () => {}, reload: async () => {},
};

function fixtureStream(model, turn, fixtureCase) {
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

async function main() {
  const job = await rpc("/job", undefined, AbortSignal.timeout(5000));
  const millis = job.deadline * 1000 - Date.now();
  if (millis <= 0 || millis > 3600000) throw new Error("deadline");
  const signal = AbortSignal.timeout(millis);
  // Independent in-container watchdog also covers a disappeared host supervisor.
  const watchdog = setTimeout(() => process.exit(124), millis);
  let session, meter;
  let tools = 0;
  const result = { outcome: "executor_failed", usage: zeroUsage(), pause: "", failure: "runtime_failed", requests: 0, tools: 0,
    provider: PROVIDER, model: MODEL, thinking: THINKING, fixture: job.fixture };
  try {
    if (!job.fixture) {
      const info = await stat("/role/auth.json");
      if (!info.isFile() || info.size > 16384 || (info.mode & 0o077)) throw new Error("auth_unavailable");
      const auth = JSON.parse(await readFile("/role/auth.json", "utf8"));
      if (Object.keys(auth).length !== 1 || auth[PROVIDER]?.type !== "oauth" || auth[PROVIDER].env) throw new Error("auth_unavailable");
    }
    const modelsStore = new InMemoryModelsStore();
    await modelsStore.write(PROVIDER, JSON.parse(await readFile("/opt/pylon/models-entry.json", "utf8")));
    const runtime = await ModelRuntime.create({ modelsPath: null, modelsStore, signal,
      ...(job.fixture ? { credentials: new InMemoryCredentialStore() } : { authPath: "/role/auth.json" }) });
    if (!job.fixture && !runtime.isUsingOAuth(PROVIDER)) throw new Error("auth_unavailable");
    const model = runtime.getModel(PROVIDER, MODEL);
    meter = new Meter(job.tokens, model ?? {});
    const auth = async () => {
      try {
        const resolved = await runtime.getAuth(PROVIDER, { signal });
        if (!resolved?.auth?.apiKey) throw new Error();
        return resolved.auth.apiKey;
      } catch {
        if (signal.aborted) throw new Error("deadline");
        meter.pause = meter.failure = "auth_unavailable";
        throw new Error("auth_unavailable");
      }
    };
    if (!job.fixture) await auth();
    const customTools = createCodingTools("/workspace").map((tool) => ({ ...tool, executionMode: "sequential",
      execute: async (_id, args, callSignal) => {
        tools++;
        const output = await rpc("/tool", { name: tool.name, args }, AbortSignal.any([signal, callSignal].filter(Boolean)));
        if (output.error) throw new Error("sandbox_tool_failed");
        // Only bounded text reaches the model. No untrusted tool usage, commands,
        // provider config, callbacks or runtime settings are accepted as metadata.
        if (!Array.isArray(output.content) || output.content.some((c) => c.type !== "text" || typeof c.text !== "string")) throw new Error("sandbox_tool_failed");
        return { content: output.content, details: {} };
      },
    }));
    ({ session } = await createAgentSession({ cwd: "/tmp", agentDir: "/tmp/pi", modelRuntime: runtime, model, thinkingLevel: THINKING,
      resourceLoader: resources, customTools, tools: ["read", "write", "edit", "bash"],
      sessionManager: SessionManager.inMemory("/tmp"), settingsManager: SettingsManager.inMemory({ retry: { enabled: false }, compaction: { enabled: false } }) }));
    session.agent.toolExecution = "sequential";
    let turn = 0;
    installPiStream(session, { runtime, meter, auth, signal, timeoutMs: millis,
      fixtureStream: job.fixture ? ((m) => fixtureStream(m, turn++, job.fixture_case)) : undefined });
    let contentBytes = 0;
    let toolFailed = false;
    session.subscribe((event) => {
      if (event.type === "message_update") {
        const delta = event.assistantMessageEvent.delta;
        if (typeof delta === "string") contentBytes += Buffer.byteLength(delta);
        if (contentBytes > 2 * 1024 * 1024) { meter.failure = "output_bound"; session.agent.abort(); }
      }
      if (event.type === "tool_execution_end" && event.isError) toolFailed = true;
    });
    signal.addEventListener("abort", () => session.agent.abort(), { once: true });
    // A fixture supplies its own stream, so use the public Agent API without
    // Session.prompt's real-auth preflight. Never invent a credential to pass it.
    if (job.fixture) await session.agent.prompt(JSON.stringify(job.brief));
    else await session.prompt(JSON.stringify(job.brief), { expandPromptTemplates: false });
    await meter.settle();
    const last = session.messages.filter((m) => m.role === "assistant").at(-1);
    if (signal.aborted) throw new Error("deadline");
    if (meter.failure || last?.stopReason !== "stop" || (job.fixture && toolFailed)) throw new Error(meter.failure || "provider_failed");
    result.outcome = "executor_returned";
    result.failure = "";
  } catch (error) {
    const allowed = ["auth_unavailable", "quota_exhausted", "model_unavailable", "request_budget_exhausted", "provider_failed", "usage_unknown", "runtime_failed", "output_bound", "deadline"];
    result.failure = meter?.failure || (allowed.includes(error?.message) ? error.message : "runtime_failed");
    if (result.failure === "auth_unavailable" || result.failure === "quota_exhausted") result.pause = result.failure;
  } finally {
    session?.dispose();
    await meter?.settle();
    result.tools = tools;
    if (meter && !job.fixture) { result.usage = meter.result(); result.requests = meter.requests; result.pause ||= meter.pause; }
    await rpc("/result", result, AbortSignal.timeout(2000));
    clearTimeout(watchdog);
  }
}

main().then(() => process.exit(0), () => process.exit(1));
