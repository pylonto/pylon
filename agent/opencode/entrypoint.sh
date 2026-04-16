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

# --- Hooks URL (set by runner, fall back to deriving from callback URL) ---
HOOKS_URL="${HOOKS_URL:-${CALLBACK_URL/callback/hooks}}"

# --- Run OpenCode ---
OPENCODE_ARGS=(run "$PROMPT" --format json)

# Resume previous session if SESSION_ID is set (follow-up messages).
if [ -n "${SESSION_ID:-}" ]; then
    log "resuming session $SESSION_ID"
    OPENCODE_ARGS+=(--session "$SESSION_ID" --continue)
fi

# Stream NDJSON to a file for capture, and to a processor that:
# 1. Logs tool use to stderr (visible in docker logs / /status)
# 2. POSTs tool events to the Pylon hooks endpoint
RAW_FILE=$(mktemp)
opencode "${OPENCODE_ARGS[@]}" 2>&1 | tee "$RAW_FILE" | node -e "
  const readline = require('readline');
  const rl = readline.createInterface({ input: process.stdin });
  rl.on('line', (line) => {
    try {
      const obj = JSON.parse(line);
      if (obj.type === 'text' && obj.part && obj.part.text) {
        const preview = obj.part.text.length > 200 ? obj.part.text.slice(0, 200) + '...' : obj.part.text;
        process.stderr.write('[agent] [${SHORT_ID}] ' + preview + '\n');
      }
      if (obj.type === 'tool_use' && obj.part) {
        const tool = obj.part.tool || 'unknown';
        const input = obj.part.state?.input || {};
        const title = obj.part.state?.title || '';
        const desc = title || JSON.stringify(input).slice(0, 100);
        process.stderr.write('[agent] [${SHORT_ID}] > ' + tool + ' ' + desc + '\n');
        // POST to hooks endpoint
        fetch('${HOOKS_URL}', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ tool_name: tool, tool_input: input }),
        }).catch(e => process.stderr.write('[agent] [${SHORT_ID}] hooks POST failed: ' + e.message + '\n'));
      }
    } catch {}
  });
" || true
EXIT_CODE=${PIPESTATUS[0]:-0}

if [ "$EXIT_CODE" -eq 0 ]; then
    log "completed"
    # OpenCode outputs NDJSON (one JSON object per line) with types: step_start,
    # text, tool_use, step_finish. Normalize to Pylon's expected format.
    # NOTE: If other agents also emit NDJSON, extract this into a shared script.
    output=$(node -e "
      const lines = require('fs').readFileSync('$RAW_FILE','utf8').trim().split('\n');
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
    raw_output=$(cat "$RAW_FILE")
    log "failed: $raw_output"
    escaped=$(echo "$raw_output" | sed 's/\\/\\\\/g; s/"/\\"/g' | tr '\n' ' ')
    payload=$(printf '{"job_id":"%s","status":"failed","error":"%s"}' "$JOB_ID" "$escaped")
fi
rm -f "$RAW_FILE"

# --- POST results to callback ---
log "posting results"
curl -s -o /dev/null -X POST "$CALLBACK_URL" \
    -H "Content-Type: application/json" \
    -d "$payload"
exit 0
