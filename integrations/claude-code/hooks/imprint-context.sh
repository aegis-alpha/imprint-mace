#!/usr/bin/env bash
#
# Imprint context hook for Claude Code (SessionStart)
#
# Reads event JSON from stdin, calls GET /context on the Imprint HTTP API,
# and returns the result as additionalContext in Claude Code's JSON format.
#
# Requires: curl
# Optional: jq (for robust JSON escaping; falls back to sed)
#
# Environment variables:
#   IMPRINT_URL              -- Imprint server URL (overrides auto-discovery)
#   IMPRINT_QUERY_TIMEOUT    -- Request timeout in seconds (default: 5)

set -euo pipefail

# Drain stdin (Claude Code sends SessionStart JSON; we don't need it)
cat > /dev/null

TIMEOUT="${IMPRINT_QUERY_TIMEOUT:-5}"

discover_url() {
  if [ -n "${IMPRINT_URL:-}" ]; then
    echo "$IMPRINT_URL"
    return
  fi

  local serve_json="$HOME/.imprint/serve.json"
  if [ -f "$serve_json" ] && command -v jq > /dev/null 2>&1; then
    local url
    url=$(jq -r '.url // empty' "$serve_json" 2>/dev/null || true)
    if [ -n "$url" ]; then
      echo "$url"
      return
    fi
  fi

  echo "http://localhost:8080"
}

URL=$(discover_url)

if ! curl -sf --max-time 3 "${URL}/status" > /dev/null 2>&1; then
  echo '{}'
  exit 0
fi

CONTEXT=$(curl -sf --max-time "$TIMEOUT" "${URL}/context" 2>/dev/null || true)

if [ -z "$CONTEXT" ] || [ "$CONTEXT" = "null" ]; then
  echo '{}'
  exit 0
fi

if command -v jq > /dev/null 2>&1; then
  jq -n --arg ctx "$CONTEXT" '{
    "hookSpecificOutput": {
      "hookEventName": "SessionStart",
      "additionalContext": $ctx
    }
  }'
else
  ESCAPED=$(printf '%s' "$CONTEXT" | sed 's/\\/\\\\/g; s/"/\\"/g; s/	/\\t/g' | tr '\n' ' ')
  printf '{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"%s"}}\n' "$ESCAPED"
fi

exit 0
