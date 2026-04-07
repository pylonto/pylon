#!/bin/sh
set -eu

: "${PROMPT:?PROMPT env var is required}"
: "${JOB_ID:?JOB_ID env var is required}"
: "${CALLBACK_URL:?CALLBACK_URL env var is required}"

echo "[mock-agent] Job $JOB_ID starting"
echo "[mock-agent] Prompt: $PROMPT"
echo "[mock-agent] Files in /workspace:"
ls /workspace

# Simulate a short think
sleep 1

OUTPUT='{"type":"result","subtype":"success","result":"Mock agent received the prompt and saw the workspace."}'

payload=$(printf '{"job_id":"%s","status":"completed","output":%s}' "$JOB_ID" "$OUTPUT")

echo "[mock-agent] Sending results to $CALLBACK_URL"
curl -s -X POST "$CALLBACK_URL" \
    -H "Content-Type: application/json" \
    -d "$payload"

echo ""
echo "[mock-agent] Job $JOB_ID done"
