// Private inspection only. No discovery, SDK dumps, provider hooks or output logs.
// Session.subscribe is observational and not awaited; one bounded pump owns I/O.
const toolNames = new Set(["read", "write", "edit", "bash"]);
const argumentNames = new Set(["path", "command", "timeout", "offset", "limit", "content", "edits", "oldText", "newText"]);
const errorNames = new Set(["ENOENT", "EACCES", "EPERM", "EINVAL", "execution_failed", "validation_failed", "rpc_failed", "aborted"]);

function prefixTable(text) {
  const table = new Int32Array(text.length);
  for (let i = 1, j = 0; i < text.length; i++) {
    while (j && text[i] !== text[j]) j = table[j - 1];
    if (text[i] === text[j]) j++;
    table[i] = j;
  }
  return table;
}
function scan(text, secret, table, hit) {
  let j = 0;
  for (let i = 0; i < text.length; i++) {
    while (j && text[i] !== secret[j]) j = table[j - 1];
    if (text[i] === secret[j]) j++;
    if (j === secret.length) { hit?.(i + 1 - j, i + 1); j = table[j - 1]; }
  }
  return j;
}

export class DebugCapture {
  constructor(limits, send) {
    this.limits = limits;
    this.send = send;
    this.queue = [];
    this.pendingBytes = 0;
    this.pendingEvents = 0;
    this.peakBytes = 0;
    this.peakEvents = 0;
    this.sequence = 0;
    this.dropped = 0;
    this.truncated = false;
    this.incomplete = false;
    this.closed = false;
    this.secrets = [];
    this.secretBytes = 0;
    this.longestSecret = 0;
    this.tools = new Map();
    this.abort = new AbortController();
  }

  // Credentials stay solely in this immutable credentialed process. Learn both
  // initial and refreshed values before capture; never send the redaction set.
  remember(values) {
    if (!this.limits || this.redactionFailed) return;
    for (const value of values) {
      if (typeof value !== "string" || !value || this.secrets.some((s) => s.text === value)) continue;
      const bytes = Buffer.byteLength(value);
      if (value.length > this.limits.event_bytes || this.secrets.length >= this.limits.secret_values || bytes > this.limits.secret_bytes - this.secretBytes) {
        this.redactionFailed = this.incomplete = true;
        return;
      }
      const reverse = value.split("").reverse().join("");
      this.secrets.push({ text: value, table: prefixTable(value), reverse, reverseTable: prefixTable(reverse) });
      this.secretBytes += bytes;
      this.longestSecret = Math.max(this.longestSecret, value.length);
    }
  }
  redactionUnavailable() { this.redactionFailed = this.incomplete = true; }
  rememberCredential(credential) {
    this.remember([credential?.access, credential?.refresh, credential?.accountId, credential?.id_token]);
  }

  // Match before truncation, with bounded look-ahead. Conservatively redact
  // secret-prefix/suffix fragments at record boundaries as well as full values.
  // This sacrifices ambiguous edge characters rather than persisting fragments
  // that adjacent records could reassemble. No unbounded streaming carry exists.
  redact(input, maximum) {
    const raw = input.slice(0, maximum + this.longestSecret);
    const ranges = new Int32Array(raw.length + 1);
    const reverse = raw.split("").reverse().join("");
    const mark = (start, end) => { if (end > start) { ranges[start]++; ranges[end]--; } };
    for (const s of this.secrets) {
      const trailing = scan(raw, s.text, s.table, mark);
      if (raw.length === input.length && trailing) mark(raw.length - trailing, raw.length);
      const leading = scan(reverse, s.reverse, s.reverseTable);
      if (leading) mark(0, leading);
    }
    let result = "", masking = false, active = 0, redacted = false;
    for (let i = 0; i < raw.length; i++) {
      active += ranges[i];
      if (active) {
        if (!masking) result += "[redacted]";
        masking = redacted = true;
      } else { masking = false; result += raw[i]; }
    }
    return { text: result, redacted, shortened: raw.length < input.length };
  }

  text(value, state) {
    if (typeof value !== "string") return "";
    const filtered = this.redact(value, this.limits.event_bytes);
    state.redacted ||= filtered.redacted;
    let output = "";
    // Charge encoded JSON bytes (including escaping), not JS string length.
    for (const char of filtered.text) {
      const bytes = Buffer.byteLength(JSON.stringify(char)) - 2;
      if (bytes > state.remaining) { state.truncated = true; break; }
      output += char; state.remaining -= bytes;
    }
    state.truncated ||= filtered.shortened;
    return output;
  }

  arguments(value, state, depth = 0) {
    if (state.items-- <= 0 || depth > this.limits.argument_depth) { state.truncated = true; return "[truncated]"; }
    if (typeof value === "string") return this.text(value, state);
    if (typeof value === "number") return Number.isFinite(value) ? value : null;
    if (value === null || typeof value === "boolean") return value;
    if (Array.isArray(value)) {
      const out = [];
      for (let i = 0; i < value.length; i++) {
        if (state.items <= 0) { state.truncated = true; break; }
        out.push(this.arguments(value[i], state, depth + 1));
      }
      return out;
    }
    if (value && typeof value === "object") {
      const out = {};
      // Built-in argument vocabulary only. Never copy environment/auth objects,
      // unknown keys, SDK details, provider fields or arbitrary prototypes.
      for (const key of argumentNames) {
        if (!Object.hasOwn(value, key)) continue;
        if (state.items <= 0) { state.truncated = true; break; }
        out[key] = this.arguments(value[key], state, depth + 1);
      }
      return out;
    }
    return null;
  }

  tool(id) {
    if (typeof id !== "string" || id.length > 256) return 0;
    if (!this.tools.has(id) && this.tools.size < 64) this.tools.set(id, this.tools.size + 1);
    return this.tools.get(id) ?? 0;
  }
  drop() { this.truncated = true; this.dropped = Math.min(1000000, this.dropped + 1); }

  emit(event) {
    if (!this.limits || this.closed) return;
    if (this.redactionFailed || this.sequence + this.pendingEvents >= this.limits.events) { this.drop(); return; }
    // Reserve space for JSON structure, frame and host sequence/source metadata.
    const state = { remaining: this.limits.event_bytes - 4096, items: this.limits.argument_items, truncated: false, redacted: false };
    const safe = { kind: event.kind };
    for (const key of ["tool", "tool_index", "outcome", "error_category", "partial"]) if (event[key] !== undefined) safe[key] = event[key];
    for (const key of ["text", "result", "error"]) if (event[key] !== undefined) safe[key] = this.text(event[key], state);
    if (event.arguments !== undefined) safe.arguments = this.arguments(event.arguments, state);
    safe.truncated = state.truncated || event.truncated === true;
    safe.redacted = state.redacted;
    const bytes = Buffer.byteLength(JSON.stringify(safe)) + 128;
    if (bytes > this.limits.event_bytes || this.pendingEvents >= this.limits.queue_events || bytes > this.limits.queue_bytes - this.pendingBytes) { this.drop(); return; }
    this.truncated ||= safe.truncated;
    this.queue.push({ event: safe, bytes });
    this.pendingBytes += bytes; this.pendingEvents++;
    this.peakBytes = Math.max(this.peakBytes, this.pendingBytes);
    this.peakEvents = Math.max(this.peakEvents, this.pendingEvents);
    if (!this.pumping) this.pumping = Promise.resolve().then(() => this.pump());
  }

  async pump() {
    while (this.queue.length && !this.abort.signal.aborted) {
      const entry = this.queue.shift();
      try {
        await this.send({ sequence: ++this.sequence, event: entry.event }, AbortSignal.any([this.abort.signal, AbortSignal.timeout(this.limits.rpc_timeout_ms)]));
      } catch { this.incomplete = true; this.abort.abort(); }
      finally { this.pendingBytes -= entry.bytes; this.pendingEvents--; }
    }
    while (this.queue.length) { const e = this.queue.shift(); this.pendingBytes -= e.bytes; this.pendingEvents--; this.drop(); }
    this.pumping = undefined;
  }

  visible(content) {
    let text = "", truncated = false, scanned = 0;
    const maximum = this.limits.event_bytes + this.longestSecret;
    if (!Array.isArray(content)) return { text, truncated };
    for (const block of content) {
      if (++scanned > this.limits.argument_items) { truncated = true; break; }
      if (block?.type !== "text" || typeof block.text !== "string") continue;
      const part = block.text.slice(0, maximum - text.length);
      text += part;
      if (part.length !== block.text.length) { truncated = true; break; }
    }
    return { text, truncated };
  }

  observe(event) {
    if (!this.limits || this.closed) return;
    if (["agent_start", "agent_end", "turn_start", "turn_end"].includes(event.type)) {
      this.emit({ kind: "lifecycle", outcome: event.type });
    } else if (event.type === "message_end" && event.message?.role === "assistant") {
      // Final text blocks, not deltas: split transport chunks cannot expose half
      // a credential before its remaining bytes arrive. Reasoning/signatures and
      // provider errorMessage never enter the serializer.
      const visible = this.visible(event.message.content);
      if (visible.text) this.emit({ kind: "assistant_text", ...visible, partial: ["aborted", "error"].includes(event.message.stopReason) });
    } else if (event.type === "tool_execution_start") {
      const tool = toolNames.has(event.toolName) ? event.toolName : "unknown";
      this.emit({ kind: "tool_start", tool, tool_index: this.tool(event.toolCallId), arguments: tool === "unknown" ? undefined : event.args });
    } else if (event.type === "tool_execution_end") {
      const visible = this.visible(event.result?.content);
      this.emit({ kind: "tool_end", tool: toolNames.has(event.toolName) ? event.toolName : "unknown", tool_index: this.tool(event.toolCallId), outcome: event.isError ? "failed" : "returned", [event.isError ? "error" : "result"]: visible.text, truncated: visible.truncated, error_category: event.isError ? "execution_failed" : "" });
    }
  }

  toolError(id, name, detail) {
    if (!detail || typeof detail.message !== "string") return;
    this.emit({ kind: "tool_error", tool: toolNames.has(name) ? name : "unknown", tool_index: this.tool(id), outcome: "failed", error: detail.message, error_category: errorNames.has(detail.category) ? detail.category : "unknown", truncated: detail.truncated === true });
  }

  async close() {
    if (!this.limits || this.closed) return;
    this.closed = true;
    const deadline = AbortSignal.timeout(this.limits.drain_timeout_ms);
    let release;
    const stopped = new Promise((resolve) => { release = resolve; });
    const stop = () => { this.incomplete = true; this.abort.abort(); release(); };
    deadline.addEventListener("abort", stop, { once: true });
    try {
      await Promise.race([this.pumping, stopped]);
      if (deadline.aborted || this.abort.signal.aborted) return false;
      await Promise.race([this.send({ sequence: this.sequence + 1, close: { events: this.sequence, dropped_events: this.dropped, truncated: this.truncated, incomplete: this.incomplete } }, AbortSignal.any([deadline, AbortSignal.timeout(this.limits.rpc_timeout_ms)])), stopped]);
      return !this.incomplete && !deadline.aborted;
    } catch { this.incomplete = true; return false; }
    finally { deadline.removeEventListener("abort", stop); this.abort.abort(); }
  }
}
