#!/usr/bin/env bash
#
# Imprint context hook for Gemini CLI (SessionStart)
#
# Reads event JSON from stdin, calls GET /context on the Imprint HTTP API,
# and returns the result as additionalContext in Gemini CLI's JSON format.
#
# Requires: curl, jq
#
# Environment variables:
#   IMPRINT_URL              -- Imprint server URL (overrides auto-discovery)
#   IMPRINT_QUERY_TIMEOUT    -- Request timeout in seconds (default: 5)
#   IMPRINT_DEBUG            -- Set to "1" for stderr debug logging

set -euo pipefail

debug() { [ "${IMPRINT_DEBUG:-}" = "1" ] && echo "[imprint-hook] $*" >&2 || true; }

# Drain stdin (Gemini CLI sends SessionStart JSON; we don't need it)
timeout 1 cat > /dev/null 2>/dev/null || true

if ! command -v jq > /dev/null 2>&1; then
  echo "[imprint-hook] jq not found -- context hook requires jq for JSON parsing" >&2
  echo '{}'
  exit 0
fi

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
debug "using URL: $URL"

if ! curl -sf --max-time 3 "${URL}/status" > /dev/null 2>&1; then
  debug "Imprint not reachable at $URL"
  echo '{}'
  exit 0
fi

debug "calling /context"
RESPONSE=$(curl -sf --max-time "$TIMEOUT" "${URL}/context" 2>/dev/null || true)

if [ -z "$RESPONSE" ]; then
  debug "empty response from /context"
  echo '{}'
  exit 0
fi

debug "got response (${#RESPONSE} bytes)"
if command -v jq > /dev/null 2>&1; then
  CONTEXT=$(echo "$RESPONSE" | jq -r '.context // empty')
  if [ -z "$CONTEXT" ]; then
    echo '{}'
    exit 0
  fi
  jq -n --arg ctx "$CONTEXT" '{
    "hookSpecificOutput": {
      "additionalContext": $ctx
    }
  }'
else
  echo '{}'
fi

exit 0
