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

// This extra detail is private inspection input only. The worker still throws
// the same model-visible sandbox_tool_failed error and never trusts its metadata.
export function toolErrorDetail(error) {
  const raw = typeof error?.message === "string" ? error.message : "tool execution failed";
  let message = raw.slice(0, 8192);
  while (Buffer.byteLength(JSON.stringify(message)) > 8192) message = message.slice(0, Math.floor(message.length / 2));
  return { category: ["ENOENT", "EACCES", "EPERM", "EINVAL"].includes(error?.code) ? error.code : "execution_failed",
    message, truncated: message.length !== raw.length };
}

// This process lives ONLY in the network-disabled, credential-free tool container.
// Even a hostile bash command can affect only that disposable container.
async function main() {
  try {
    const { name, args } = JSON.parse(await readToolInput(process.stdin));
    const tool = createCodingTools("/workspace").find((t) => t.name === name);
    if (!tool) throw new Error("unknown_tool");
    const result = await tool.execute("remote", args, AbortSignal.timeout(30000));
    const output = JSON.stringify({ content: result.content });
    if (Buffer.byteLength(output) > 256 * 1024) throw new Error("output_bound");
    process.stdout.write(output);
  } catch (error) {
    process.stdout.write(JSON.stringify({ error: "sandbox_tool_failed", error_detail: toolErrorDetail(error) }));
  }
}
if (fileURLToPath(import.meta.url) === process.argv[1]) await main();
