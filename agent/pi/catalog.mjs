import { writeFile } from "node:fs/promises";
import { ModelRuntime } from "@earendil-works/pi-coding-agent";
import { InMemoryCredentialStore, InMemoryModelsStore } from "@earendil-works/pi-ai";
import { Meter, MODEL, PROVIDER } from "./meter.mjs";

// ModelRuntime.refresh intentionally skips unconfigured providers. Image builds
// have NO credentials, so use the public Provider.refreshModels/ModelsStore seam
// for the public pi.dev catalog, then let ModelRuntime restore that publication.
// This proves catalogue presence only, never subscription/model-call availability.
const modelsStore = new InMemoryModelsStore();
const runtime = await ModelRuntime.create({ credentials: new InMemoryCredentialStore(), modelsPath: null, modelsStore });
const provider = runtime.getProvider(PROVIDER);
const signal = AbortSignal.timeout(15000);
await provider.refreshModels({ credential: undefined, stored: undefined, allowNetwork: true, force: true, signal,
  publish: async ({ update, persist }) => {
    signal.throwIfAborted();
    if (persist) await modelsStore.write(PROVIDER, persist, { signal });
    update?.();
    return true;
  },
});
await runtime.refresh({ providers: [PROVIDER], allowNetwork: false, signal });
const model = runtime.getModel(PROVIDER, MODEL);
new Meter(400000, model ?? {});
await writeFile("/opt/pylon/models-entry.json", JSON.stringify(await modelsStore.read(PROVIDER)), { mode: 0o644 });
process.stdout.write(JSON.stringify({ catalog: true, authenticated: false, provider: model.provider, model: model.id,
  thinking: model.thinkingLevelMap.max, context: model.contextWindow, output: model.maxTokens }) + "\n");
