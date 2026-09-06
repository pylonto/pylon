FROM node:24.15.0-bookworm-slim@sha256:152aceace5c03e2597988763165ee33e3fd3633636db0fc983cd2e126b02cfde
RUN apt-get update && apt-get install -y --no-install-recommends git ca-certificates && rm -rf /var/lib/apt/lists/*
WORKDIR /opt/pylon
COPY --chmod=0644 package.json package-lock.json ./
RUN npm ci --ignore-scripts --omit=dev && npm cache clean --force
COPY --chmod=0644 meter.mjs stream.mjs catalog.mjs worker.mjs tool.mjs sandbox.mjs ./
ENV PI_SKIP_VERSION_CHECK=1 PI_TELEMETRY=0
RUN node catalog.mjs
USER 1000:1000
ENTRYPOINT ["/usr/local/bin/node", "/opt/pylon/worker.mjs"]
