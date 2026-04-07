#!/bin/sh
set -eu

: "${PROMPT:?PROMPT env var is required}"
: "${JOB_ID:?JOB_ID env var is required}"
: "${CALLBACK_URL:?CALLBACK_URL env var is required}"

SHORT_ID="${JOB_ID%%-*}"
log() { echo "[mock-agent] [$SHORT_ID] $*"; }

log "starting"
log "prompt: $PROMPT"
log "files in /workspace:"
ls /workspace

# Simulate a short think
sleep 1

OUTPUT='{"type":"result","subtype":"success","result":"Mock agent received the prompt and saw the workspace."}'

payload=$(printf '{"job_id":"%s","status":"completed","output":%s}' "$JOB_ID" "$OUTPUT")

log "posting results"
curl -s -X POST "$CALLBACK_URL" \
    -H "Content-Type: application/json" \
    -d "$payload"

log "done"
