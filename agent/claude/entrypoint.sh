#!/bin/bash
set -euo pipefail

# --- Auth check ---
if [ -n "${ANTHROPIC_API_KEY:-}" ]; then
    echo "[agent] Using API key authentication"
elif [ -d "/home/pylon/.claude" ] && [ "$(ls -A /home/pylon/.claude 2>/dev/null)" ]; then
    echo "[agent] Using mounted OAuth session from /home/pylon/.claude"
else
    echo "Error: No authentication found. Set ANTHROPIC_API_KEY or mount your ~/.claude directory with -v ~/.claude:/home/pylon/.claude"
    exit 1
fi

# --- Validate required env vars ---
: "${PROMPT:?PROMPT env var is required}"
: "${JOB_ID:?JOB_ID env var is required}"
: "${CALLBACK_URL:?CALLBACK_URL env var is required}"

echo "[agent] Job $JOB_ID starting"
echo "[agent] Prompt: $PROMPT"
echo "[agent] Callback: $CALLBACK_URL"

cd /workspace

# --- Run Claude Code ---
# --print: non-interactive, prints result and exits
# --output-format json: structured output we can parse
# -p: pass the prompt directly
if output=$(claude --print --output-format json -p "$PROMPT" 2>&1); then
    echo "[agent] Claude Code completed successfully"
    # Build success payload — use jq-free approach with a temp file to handle escaping
    payload=$(printf '{"job_id":"%s","status":"completed","output":%s}' "$JOB_ID" "$output")
else
    echo "[agent] Claude Code failed: $output"
    # Escape the error for JSON: replace backslashes, quotes, newlines
    escaped=$(echo "$output" | sed 's/\\/\\\\/g; s/"/\\"/g' | tr '\n' ' ')
    payload=$(printf '{"job_id":"%s","status":"failed","error":"%s"}' "$JOB_ID" "$escaped")
fi

# --- POST results to callback ---
echo "[agent] Sending results to $CALLBACK_URL"
curl -s -X POST "$CALLBACK_URL" \
    -H "Content-Type: application/json" \
    -d "$payload"

echo ""
echo "[agent] Job $JOB_ID done"
exit 0
