#!/bin/bash
set -euo pipefail

# --- Validate required env vars ---
: "${PROMPT:?PROMPT env var is required}"
: "${JOB_ID:?JOB_ID env var is required}"
: "${CALLBACK_URL:?CALLBACK_URL env var is required}"

SHORT_ID="${JOB_ID:0:8}"
log() { echo "$(date '+%Y/%m/%d %H:%M:%S') [agent] [$SHORT_ID] $*"; }

# --- Auth check (optional -- OpenCode has built-in access via Zen) ---
log "using ${OPENCODE_PROVIDER:-default} provider"
log "prompt: $PROMPT"

cd /workspace

# --- Run OpenCode ---
OPENCODE_ARGS=(run "$PROMPT" --format json)

# Resume previous session if SESSION_ID is set (follow-up messages).
if [ -n "${SESSION_ID:-}" ]; then
    log "resuming session $SESSION_ID"
    OPENCODE_ARGS+=(--session "$SESSION_ID" --continue)
fi

if raw_output=$(opencode "${OPENCODE_ARGS[@]}" 2>&1); then
    log "completed"
    # OpenCode outputs NDJSON (one JSON object per line) with types: step_start,
    # text, step_finish. We normalize to a single JSON object matching Pylon's
    # expected format: {"result":"...", "session_id":"..."}
    # NOTE: If other agents also emit NDJSON, extract this into a shared script.
    output=$(echo "$raw_output" | node -e "
      const lines = require('fs').readFileSync('/dev/stdin','utf8').trim().split('\n');
      let text = '', sessionID = '';
      for (const line of lines) {
        try {
          const obj = JSON.parse(line);
          if (obj.sessionID && !sessionID) sessionID = obj.sessionID;
          if (obj.type === 'text' && obj.part && obj.part.text) text += obj.part.text;
        } catch {}
      }
      process.stdout.write(JSON.stringify({result: text, session_id: sessionID}));
    ")
    payload=$(printf '{"job_id":"%s","status":"completed","output":%s}' "$JOB_ID" "$output")
else
    log "failed: $raw_output"
    escaped=$(echo "$raw_output" | sed 's/\\/\\\\/g; s/"/\\"/g' | tr '\n' ' ')
    payload=$(printf '{"job_id":"%s","status":"failed","error":"%s"}' "$JOB_ID" "$escaped")
fi

# --- POST results to callback ---
log "posting results"
curl -s -o /dev/null -X POST "$CALLBACK_URL" \
    -H "Content-Type: application/json" \
    -d "$payload"
exit 0
