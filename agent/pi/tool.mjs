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
  } catch {
    process.stdout.write(JSON.stringify({ error: "sandbox_tool_failed" }));
  }
}
if (fileURLToPath(import.meta.url) === process.argv[1]) await main();
