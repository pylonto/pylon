// Trusted runtime only. No tool or repository code executes in this process.
export const PROVIDER = "openai-codex";
export const MODEL = "gpt-6-astra";
export const DEFAULT_THINKING = "max";
export function requireThinking(value) {
  if (value !== "medium" && value !== "max") throw new Error("model_unavailable");
  return value;
}
const RESPONSE_BYTES = 16 * 1024 * 1024;
const LINE_BYTES = 2 * 1024 * 1024;
export const zeroUsage = () => ({ input: 0, output: 0, cache_read: 0, cache_write: 0 });
const integer = (n) => Number.isSafeInteger(n) && n >= 0;

export class Meter {
  constructor(tokens, model, fetchImpl = globalThis.fetch, thinking = DEFAULT_THINKING) {
    this.thinking = requireThinking(thinking);
    if (!integer(tokens) || tokens === 0 || model.provider !== PROVIDER || model.id !== MODEL ||
        model.api !== "openai-codex-responses" || model.baseUrl !== "https://chatgpt.com/backend-api" ||
        model.contextWindow !== 272000 || model.maxTokens !== 128000 || model.thinkingLevelMap?.max !== "max" ||
        model.thinkingLevelMap?.[this.thinking] !== this.thinking) {
      throw new Error("model_unavailable");
    }
    this.envelope = model.contextWindow + model.maxTokens;
    this.tokens = tokens;
    this.fetchImpl = fetchImpl;
    this.requests = 0;
    this.reserved = 0;
    this.usage = zeroUsage();
    this.pending = false;
    this.failure = "";
    this.pause = "";
    this.settling = Promise.resolve();
  }

  payload = (body, model) => {
    if (model.provider !== PROVIDER || model.id !== MODEL || body.model !== MODEL || body.reasoning?.effort !== this.thinking ||
        body.store !== false || body.stream !== true || body.background || body.previous_response_id ||
        Buffer.byteLength(JSON.stringify(body)) > 65536) {
      this.failure = "output_bound";
      throw new Error(this.failure);
    }
  };

  // This fetch is injected into the pinned SSE adapter, not global monkey-patching.
  // Every transport attempt reserves the FULL published input+output envelope.
  // maxTokens is deliberately not used: the Codex adapter does not forward it.
  fetch = async (url, options) => {
    if (this.failure || this.pending || this.reserved > this.tokens - this.envelope) {
      this.failure ||= "request_budget_exhausted";
      throw new Error(this.failure);
    }
    if (url !== "https://chatgpt.com/backend-api/codex/responses" || options.method !== "POST") {
      this.failure = "provider_failed";
      throw new Error(this.failure);
    }
    this.reserved += this.envelope;
    this.requests++;
    this.pending = true;
    let response;
    try {
      response = await this.fetchImpl(url, { ...options, redirect: "error" });
    } catch {
      this.failure = "usage_unknown";
      throw new Error(this.failure);
    }
    if ([401, 403, 429].includes(response.status)) {
      this.pending = false; // HTTP rejection before inference, not a lost stream.
      this.pause = response.status === 429 ? "quota_exhausted" : "auth_unavailable";
      this.failure = this.pause;
      await response.body?.cancel();
      return new Response(JSON.stringify({ error: { message: this.failure } }), { status: response.status });
    }
    if (!response.ok || !response.body) {
      this.failure = "usage_unknown";
      await response.body?.cancel();
      throw new Error(this.failure);
    }
    let bytes = 0;
    let line = "";
    let terminal = false;
    const decoder = new TextDecoder("utf-8", { fatal: true });
    const parse = (text) => {
      if (!text.startsWith("data:")) return;
      const raw = text.slice(5).trim();
      if (raw === "[DONE]") return;
      const event = JSON.parse(raw);
      const code = event.error?.code ?? event.response?.error?.code ?? event.code;
      if (["usage_limit_reached", "rate_limit_exceeded", "insufficient_quota"].includes(code)) this.pause = "quota_exhausted";
      if (["invalid_api_key", "token_expired", "invalid_authentication"].includes(code)) this.pause = "auth_unavailable";
      if (event.type !== "response.completed" && event.type !== "response.incomplete") return;
      if (terminal) throw new Error("usage_unknown");
      const u = event.response?.usage;
      const input = u?.input_tokens;
      const output = u?.output_tokens;
      const read = u?.input_tokens_details?.cached_tokens ?? 0;
      const write = u?.input_tokens_details?.cache_write_tokens ?? 0;
      if (![input, output, read, write, u?.total_tokens].every(integer) || read + write > input ||
          input > 272000 || output > 128000 || input + output !== u.total_tokens || input + output > this.envelope) {
        throw new Error("usage_unknown");
      }
      this.usage.input += input - read - write;
      this.usage.output += output;
      this.usage.cache_read += read;
      this.usage.cache_write += write;
      terminal = true;
    };
    const meter = this;
    const tap = new TransformStream({
      transform(chunk, controller) {
        try {
          bytes += chunk.byteLength;
          if (bytes > RESPONSE_BYTES) throw new Error("output_bound");
          line += decoder.decode(chunk, { stream: true });
          let at;
          while ((at = line.indexOf("\n")) !== -1) {
            if (at > LINE_BYTES) throw new Error("output_bound");
            parse(line.slice(0, at));
            line = line.slice(at + 1);
          }
          if (line.length > LINE_BYTES) throw new Error("output_bound");
          controller.enqueue(chunk);
        } catch (error) {
          meter.failure = error.message === "output_bound" ? "output_bound" : "usage_unknown";
          controller.error(new Error(meter.failure));
        }
      },
      flush() {
        try {
          line += decoder.decode();
          if (line.trim()) parse(line);
          if (!terminal) throw new Error("usage_unknown");
          meter.pending = false;
        } catch {
          meter.failure = "usage_unknown";
          throw new Error(meter.failure);
        }
      },
    });
    // The pinned adapter stops reading at response.completed, before EOF. Its
    // cancellation cannot own accounting: drain a second branch through the
    // SAME bounded tap so missing/trailing/duplicate usage remains observable.
    // Tee buffering cannot exceed RESPONSE_BYTES; no transcript is persisted.
    const [sdk, accounting] = response.body.pipeThrough(tap).tee();
    this.settling = (async () => {
      const reader = accounting.getReader();
      try { while (!(await reader.read()).done) { /* bounded by tap */ } }
      catch { this.pending = true; this.failure ||= "usage_unknown"; }
      finally { reader.releaseLock(); }
    })();
    return new Response(sdk, { status: response.status, headers: { "content-type": "text/event-stream" } });
  };

  async settle() { await this.settling; }

  result() {
    return this.pending ? null : this.usage;
  }
}
