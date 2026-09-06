import { MODEL, PROVIDER, THINKING } from "./meter.mjs";

// One owner for the public SDK hook and the no-retry, metered SSE options. Tests
// run the actual pinned adapter here with synthetic HTTP, never subscription auth.
export function installPiStream(session, { runtime, meter, auth, signal, timeoutMs, fixtureStream }) {
  if (session.model?.id !== MODEL || session.model.provider !== PROVIDER || session.thinkingLevel !== THINKING ||
      typeof session.agent.streamFunction !== "function") throw new Error("model_unavailable");
  // The constructor option is streamFn; the public instance slot is different.
  session.agent.streamFunction = fixtureStream ?? (async (model, context, options) => {
    await meter.settle();
    return runtime.getProvider(PROVIDER).streamSimple(model, context, { ...options, apiKey: await auth(),
      signal: AbortSignal.any([signal, options?.signal].filter(Boolean)),
      transport: "sse", maxRetries: 0, timeoutMs, fetch: meter.fetch, onPayload: meter.payload });
  });
}
