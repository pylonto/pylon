import { readFile } from "node:fs/promises";
import http from "node:http";
import { createAgentSession, createCodingTools, createExtensionRuntime, ModelRuntime, SessionManager, SettingsManager } from "@earendil-works/pi-coding-agent";
import { InMemoryCredentialStore, InMemoryModelsStore } from "@earendil-works/pi-ai";
import { Meter, MODEL, PROVIDER, THINKING, zeroUsage } from "./meter.mjs";
import { installPiStream } from "./stream.mjs";
import { DebugCapture } from "./debug.mjs";
import { readRoleCredential } from "./role-auth.mjs";
import { createSDKFixture, fixtureStream } from "./fixture.mjs";
import { toolResultForModel } from "./tool.mjs";

const socketPath = "/transport/pi.sock";
const bound = 256 * 1024;
function rpc(path, data, signal, captureClosed) {
  return new Promise((resolve, reject) => {
    const raw = data === undefined ? undefined : JSON.stringify(data);
    if (raw && Buffer.byteLength(raw) > bound) return reject(new Error("output_bound"));
    const headers = raw === undefined ? {} : { "content-type": "application/json", "content-length": Buffer.byteLength(raw) };
    if (captureClosed !== undefined) headers["X-Pylon-Pi-Capture"] = captureClosed ? "closed" : "incomplete";
    const req = http.request({ socketPath, path, method: raw === undefined ? "GET" : "POST", signal, headers }, (res) => {
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
  getSystemPrompt: () => "Edit the files in /workspace to repair only the signed maintenance brief. /workspace is a plain-files snapshot without .git; Git commands will not work there. The controller exports the patch from your filesystem edits after execution; an unapplied patch in assistant text is not an export. All tools run in a disposable offline sandbox. No publication or host access is available. Preserve tests and keep changes minimal. Be concise.",
  getSystemPromptSource: () => undefined, getAppendSystemPrompt: () => [], getAppendSystemPromptSources: () => [],
  extendResources: () => {}, reload: async () => {},
};

async function main() {
  const job = await rpc("/job", undefined, AbortSignal.timeout(5000));
  const millis = job.deadline * 1000 - Date.now();
  if (millis <= 0 || millis > 3600000) throw new Error("deadline");
  const signal = AbortSignal.timeout(millis);
  // Independent in-container watchdog also covers a disappeared host supervisor.
  const watchdog = setTimeout(() => process.exit(124), millis);
  let session, meter, synthetic;
  const debug = new DebugCapture(job.debug, (frame, signal) => rpc("/debug", frame, signal));
  let tools = 0;
  const result = { outcome: "executor_failed", usage: zeroUsage(), pause: "", failure: "runtime_failed", requests: 0, tools: 0,
    provider: PROVIDER, model: MODEL, thinking: THINKING, fixture: job.fixture };
  try {
    synthetic = job.fixture ? await createSDKFixture(job.fixture_case) : undefined;
    if (!job.fixture) debug.rememberCredential(await readRoleCredential());
    else if (synthetic) debug.rememberCredential(await synthetic.credentials.read(PROVIDER));
    const modelsStore = new InMemoryModelsStore();
    await modelsStore.write(PROVIDER, JSON.parse(await readFile("/opt/pylon/models-entry.json", "utf8")));
    const runtime = await ModelRuntime.create({ modelsPath: null, modelsStore, signal,
      ...(job.fixture ? { credentials: synthetic?.credentials ?? new InMemoryCredentialStore() } : { authPath: "/role/auth.json" }) });
    if (!job.fixture && !runtime.isUsingOAuth(PROVIDER)) throw new Error("auth_unavailable");
    const model = runtime.getModel(PROVIDER, MODEL);
    meter = new Meter(job.tokens, model ?? {}, synthetic?.fetch);
    const auth = async () => {
      try {
        const resolved = await runtime.getAuth(PROVIDER, { signal });
        if (!resolved?.auth?.apiKey) throw new Error();
        if (job.debug) {
          debug.remember([resolved.auth.apiKey]);
          try { debug.rememberCredential(synthetic ? await synthetic.credentials.read(PROVIDER) : await readRoleCredential()); }
          catch { debug.redactionUnavailable(); }
        }
        return resolved.auth.apiKey;
      } catch {
        if (signal.aborted) throw new Error("deadline");
        meter.pause = meter.failure = "auth_unavailable";
        throw new Error("auth_unavailable");
      }
    };
    if (!job.fixture || synthetic) await auth();
    const customTools = createCodingTools("/workspace").map((tool) => ({ ...tool, executionMode: "sequential",
      execute: async (_id, args, callSignal) => {
        tools++;
        let output;
        try { output = await rpc("/tool", { name: tool.name, args }, AbortSignal.any([signal, callSignal].filter(Boolean))); }
        catch { throw new Error("sandbox_tool_failed"); } // Never expose host/socket errors as sandbox output.
        return toolResultForModel(output, (detail) => debug.toolError(_id, tool.name, detail));
      },
    }));
    ({ session } = await createAgentSession({ cwd: "/tmp", agentDir: "/tmp/pi", modelRuntime: runtime, model, thinkingLevel: THINKING,
      resourceLoader: resources, customTools, tools: ["read", "write", "edit", "bash"],
      sessionManager: SessionManager.inMemory("/tmp"), settingsManager: SettingsManager.inMemory({ retry: { enabled: false }, compaction: { enabled: false } }) }));
    session.agent.toolExecution = "sequential";
    let turn = 0;
    installPiStream(session, { runtime, meter, auth, signal, timeoutMs: millis,
      fixtureStream: job.fixture && !synthetic ? ((m) => fixtureStream(m, turn++, job.fixture_case)) : undefined });
    let contentBytes = 0;
    let toolFailed = false;
    session.subscribe((event) => {
      if (event.type === "message_update") {
        const delta = event.assistantMessageEvent.delta;
        if (typeof delta === "string") contentBytes += Buffer.byteLength(delta);
        if (contentBytes > 2 * 1024 * 1024) { meter.failure = "output_bound"; session.agent.abort(); }
      }
      if (event.type === "tool_execution_end" && event.isError) toolFailed = true;
      debug.observe(event);
      synthetic?.observe(event);
    });
    signal.addEventListener("abort", () => session.agent.abort(), { once: true });
    // Legacy fixtures use Agent.prompt. The SDK fixture supplies explicitly
    // synthetic in-memory auth and HTTP to exercise Session.prompt too, offline.
    const useAgent = job.fixture && (!synthetic || synthetic.mode === "agent");
    debug.emit({ kind: "lifecycle", outcome: useAgent ? "prompt_agent" : "prompt_session" });
    if (useAgent) await session.agent.prompt(JSON.stringify(job.brief));
    else await session.prompt(JSON.stringify(job.brief), { expandPromptTemplates: false });
    await meter.settle();
    synthetic?.assertComplete(meter);
    const last = session.messages.filter((m) => m.role === "assistant").at(-1);
    if (signal.aborted) throw new Error("deadline");
    if (meter.failure || last?.stopReason !== "stop" || (job.fixture && !synthetic && toolFailed)) throw new Error(meter.failure || "provider_failed");
    result.outcome = "executor_returned";
    result.failure = "";
  } catch (error) {
    const allowed = ["auth_unavailable", "quota_exhausted", "model_unavailable", "request_budget_exhausted", "provider_failed", "usage_unknown", "runtime_failed", "output_bound", "deadline"];
    result.failure = meter?.failure || (allowed.includes(error?.message) ? error.message : "runtime_failed");
    if (result.failure === "auth_unavailable" || result.failure === "quota_exhausted") result.pause = result.failure;
  } finally {
    session?.dispose();
    await meter?.settle();
    const captureClosed = await debug.close();
    result.tools = tools;
    if (meter && !job.fixture) { result.usage = meter.result(); result.requests = meter.requests; result.pause ||= meter.pause; }
    await rpc("/result", result, AbortSignal.timeout(2000), captureClosed);
    clearTimeout(watchdog);
  }
}

main().then(() => process.exit(0), () => process.exit(1));
