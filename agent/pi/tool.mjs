import { createCodingTools } from "@earendil-works/pi-coding-agent";
import { fileURLToPath } from "node:url";

// Keep UTF-8 framing independent of Docker/pipe chunk boundaries. Buffer.toString
// on each chunk would silently corrupt a multibyte character split across reads.
export async function readToolInput(source) {
  let raw = "", bytes = 0;
  const decoder = new TextDecoder("utf-8", { fatal: true });
  for await (const chunk of source) {
    bytes += chunk.byteLength;
    if (bytes > 256 * 1024) throw new Error("input_bound");
    raw += decoder.decode(chunk, { stream: true });
  }
  return raw + decoder.decode();
}

export const MAX_TOOL_ERROR_BYTES = 8192;
const errorCategories = ["ENOENT", "EACCES", "EPERM", "EINVAL", "execution_failed"];

// Select only the credential-free SDK execute error's message. Keep both early
// command output and the trailing failure status when the encoded text is capped.
export function toolErrorDetail(error) {
  const raw = typeof error?.message === "string" && error.message ? error.message : "tool execution failed";
  let width = Math.min(raw.length, MAX_TOOL_ERROR_BYTES / 2);
  let truncated = raw.length > width * 2, message;
  for (;;) {
    message = (truncated ? raw.slice(0, width) + "\n[Sandbox tool error truncated]\n" + raw.slice(-width) : raw).toWellFormed();
    if (Buffer.byteLength(JSON.stringify(message)) <= MAX_TOOL_ERROR_BYTES) break;
    width = Math.floor(width / 2);
    truncated = true;
  }
  return { category: errorCategories.includes(error?.code) ? error.code : "execution_failed", message, truncated };
}

// The socket transports untrusted tool bytes, not exception objects or authority.
// Only an exact bounded SDK-execute failure envelope may become model text.
// Preflight/transport/auth failures have no such detail and remain categorical.
export function toolResultForModel(output, onToolError) {
  const failure = () => new Error("sandbox_tool_failed");
  if (!output || typeof output !== "object" || Array.isArray(output)) throw failure();
  if (output.error) {
    const detail = output.error_detail;
    if (output.error !== "sandbox_tool_failed" || Object.keys(output).sort().join() !== "error,error_detail" ||
      !detail || Object.keys(detail).sort().join() !== "category,message,truncated" ||
      !errorCategories.includes(detail.category) || typeof detail.message !== "string" || !detail.message ||
      detail.message.length > MAX_TOOL_ERROR_BYTES || Buffer.byteLength(JSON.stringify(detail.message)) > MAX_TOOL_ERROR_BYTES ||
      typeof detail.truncated !== "boolean") throw failure();
    onToolError?.(detail);
    // Throwing preserves the SDK's isError toolResult and ordinary recovery loop.
    throw new Error(detail.message);
  }
  if (Object.keys(output).join() !== "content" || !Array.isArray(output.content) ||
    output.content.some((c) => !c || c.type !== "text" || typeof c.text !== "string" || Object.keys(c).sort().join() !== "text,type")) throw failure();
  return { content: output.content, details: {} };
}

// This process lives ONLY in the network-disabled, credential-free tool container.
// Even a hostile bash command can affect only that disposable container.
async function main() {
  try {
    const { name, args } = JSON.parse(await readToolInput(process.stdin));
    const tool = createCodingTools("/workspace").find((t) => t.name === name);
    if (!tool) throw new Error("unknown_tool");
    let result;
    try { result = await tool.execute("remote", args, AbortSignal.timeout(30000)); }
    catch (error) {
      process.stdout.write(JSON.stringify({ error: "sandbox_tool_failed", error_detail: toolErrorDetail(error) }));
      return;
    }
    const output = JSON.stringify({ content: result.content });
    if (Buffer.byteLength(output) > 256 * 1024) throw new Error("output_bound");
    process.stdout.write(output);
  } catch {
    process.stdout.write(JSON.stringify({ error: "sandbox_tool_failed" }));
  }
}
if (fileURLToPath(import.meta.url) === process.argv[1]) await main();
