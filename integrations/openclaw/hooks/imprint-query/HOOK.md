---
name: imprint-query
description: "Retrieves relevant context from Imprint before the agent responds"
metadata: { "openclaw": { "emoji": "🔍", "events": ["message:preprocessed"] } }
---

# imprint-query

Fires on every `message:preprocessed` event. Sends the message body to Imprint's
`GET /context` endpoint (retrieval-only, no LLM synthesis) and pushes the result
into `event.messages` so the agent sees it as additional context.

Uses `/context` (fast, retrieval-only) instead of `/query` (slow, LLM synthesis).
The `/context` endpoint returns ranked facts from vector search, FTS, and graph
traversal without an LLM call -- typically 50-200ms vs 5-30s for full query.
Agents that need full LLM-synthesized answers should use the `imprint_query`
MCP tool directly.

## Timeout

Default: 5 seconds. Configurable via `IMPRINT_QUERY_TIMEOUT` environment
variable (in milliseconds). If the request takes longer or fails, the hook
logs a warning and continues silently -- message processing is not blocked.

## Reachability

On first invocation, the hook checks `GET /status` to verify Imprint is running.
If unreachable, it retries every 60 seconds instead of disabling permanently.
Once Imprint becomes available, the hook resumes automatically.

## Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `IMPRINT_URL` | (auto-discovered) | Imprint server URL |
| `IMPRINT_QUERY_TIMEOUT` | `5000` | Request timeout in milliseconds |

URL resolution priority:

1. `IMPRINT_URL` environment variable
2. `~/.imprint/serve.json` (written automatically by `imprint serve`)
3. Default: `http://localhost:8080`

## Minimum content length

Messages shorter than 20 characters (after trimming) are skipped.
