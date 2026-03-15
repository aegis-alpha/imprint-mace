---
name: imprint-query
description: "Queries Imprint for relevant context before the agent responds"
metadata: { "openclaw": { "emoji": "🔍", "events": ["message:preprocessed"] } }
---

# imprint-query

Fires on every `message:preprocessed` event. Sends the message body to Imprint's
`GET /query` endpoint and pushes the answer into `event.messages` so the agent
sees it as additional context. If `event.messages` is not present, the hook
initializes it as an empty array before pushing.

This is the deterministic auto-query -- it replaces the probabilistic "ALWAYS
call imprint_query" instruction in agent skills.

## Timeout

5 seconds. If the query takes longer or fails, the hook logs a warning and
continues silently -- message processing is not blocked.

## Configuration

- `IMPRINT_URL` environment variable (default: `http://localhost:8080`)

## Minimum content length

Messages shorter than 20 characters (after trimming) are skipped.
