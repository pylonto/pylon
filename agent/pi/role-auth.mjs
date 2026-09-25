import { open, constants } from "node:fs/promises";
import { PROVIDER } from "./meter.mjs";

// The runtime alone reads its existing role credential. Do not send this object
// to the host, tools, diagnostics or model. Bound the read itself, not just stat.
export async function readRoleCredential(path = "/role/auth.json") {
  const file = await open(path, constants.O_RDONLY | constants.O_NOFOLLOW | constants.O_NONBLOCK);
  try {
    const info = await file.stat();
    if (!info.isFile() || info.nlink !== 1 || info.size > 16384 || (info.mode & 0o077)) throw new Error("auth_unavailable");
    const raw = Buffer.alloc(16385);
    let bytes = 0;
    while (bytes < raw.length) {
      const part = await file.read(raw, bytes, raw.length - bytes, bytes);
      if (!part.bytesRead) break;
      bytes += part.bytesRead;
    }
    if (bytes > 16384) throw new Error("auth_unavailable");
    const auth = JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(raw.subarray(0, bytes)));
    if (!auth || Array.isArray(auth) || Object.keys(auth).length !== 1 || auth[PROVIDER]?.type !== "oauth" || auth[PROVIDER].env) throw new Error("auth_unavailable");
    return auth[PROVIDER];
  } finally { await file.close(); }
}
