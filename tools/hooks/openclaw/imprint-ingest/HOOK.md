---
name: imprint-ingest
description: "Sends every received message to Imprint for knowledge extraction"
metadata: { "openclaw": { "emoji": "🧠", "events": ["message:preprocessed"] } }
---

# imprint-ingest

Fires on every `message:preprocessed` event. Extracts the message body and sends
it to Imprint's `POST /ingest` endpoint for knowledge extraction.

Fire-and-forget -- does not block message processing. Errors are logged to
console.error with `[imprint-ingest]` prefix.

## Source construction

The `source` field sent to Imprint is built from:
- `event.context.channelId`
- `event.context.conversationId`
- date (YYYY-MM-DD from `event.timestamp`)

Joined by `-`, empty values filtered out.

## Configuration

URL resolution priority:

1. `IMPRINT_URL` environment variable (set in hook env config)
2. `~/.imprint/serve.json` (written automatically by `imprint serve`)
3. Default: `http://localhost:8080`

On first message, the hook checks `GET /status` to verify Imprint is reachable.
If not -- the hook disables itself and logs a clear error message.

## Minimum content length

Messages shorter than 20 characters (after trimming) are skipped -- too short
to contain extractable knowledge.
