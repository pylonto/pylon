#!/bin/bash
set -euo pipefail

# --- Validate required env vars ---
: "${PROMPT:?PROMPT env var is required}"
: "${JOB_ID:?JOB_ID env var is required}"
: "${CALLBACK_URL:?CALLBACK_URL env var is required}"

SHORT_ID="${JOB_ID:0:8}"
log() { echo "$(date '+%Y/%m/%d %H:%M:%S') [agent] [$SHORT_ID] $*"; }

# --- Auth check ---
if [ -n "${ANTHROPIC_API_KEY:-}" ]; then
    log "using API key auth"
elif [ -d "/home/pylon/.claude" ] && [ "$(ls -A /home/pylon/.claude 2>/dev/null)" ]; then
    log "using OAuth session"
else
    log "no authentication found"
    exit 1
fi

log "prompt: $PROMPT"

cd /workspace

# --- Run Claude Code ---
CLAUDE_ARGS=(--print --output-format json -p "$PROMPT")

# Resume previous session if SESSION_ID is set (follow-up messages).
if [ -n "${SESSION_ID:-}" ]; then
    log "resuming session $SESSION_ID"
    CLAUDE_ARGS+=(--resume "$SESSION_ID")
fi

if output=$(claude "${CLAUDE_ARGS[@]}" 2>&1); then
    log "completed"
    payload=$(printf '{"job_id":"%s","status":"completed","output":%s}' "$JOB_ID" "$output")
else
    log "failed: $output"
    escaped=$(echo "$output" | sed 's/\\/\\\\/g; s/"/\\"/g' | tr '\n' ' ')
    payload=$(printf '{"job_id":"%s","status":"failed","error":"%s"}' "$JOB_ID" "$escaped")
fi

# --- POST results to callback ---
log "posting results"
curl -s -o /dev/null -X POST "$CALLBACK_URL" \
    -H "Content-Type: application/json" \
    -d "$payload"
exit 0
