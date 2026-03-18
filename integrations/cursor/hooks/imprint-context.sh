#!/usr/bin/env bash
#
# Imprint context hook for Cursor (sessionStart)
#
# Calls GET /context on the Imprint HTTP API and returns the result
# as additional_context for the agent's initial system context.
#
# Requires: curl, jq
#
# Environment variables:
#   IMPRINT_URL              -- Imprint server URL (overrides auto-discovery)
#   IMPRINT_QUERY_TIMEOUT    -- Request timeout in seconds (default: 5)

set -euo pipefail

# Drain stdin (Cursor sends session JSON on stdin; we don't need it)
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

if ! curl -sf --max-time 3 "${URL}/status" > /dev/null 2>&1; then
  echo '{}'
  exit 0
fi

RESPONSE=$(curl -sf --max-time "$TIMEOUT" "${URL}/context" 2>/dev/null || true)

if [ -z "$RESPONSE" ]; then
  echo '{}'
  exit 0
fi

if command -v jq > /dev/null 2>&1; then
  CONTEXT=$(echo "$RESPONSE" | jq -r '.context // empty')
  if [ -z "$CONTEXT" ]; then
    echo '{}'
    exit 0
  fi
  jq -n --arg ctx "$CONTEXT" '{"additional_context": $ctx}'
else
  echo '{}'
fi

exit 0
