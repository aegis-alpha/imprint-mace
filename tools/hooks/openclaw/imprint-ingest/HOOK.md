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

- `IMPRINT_URL` environment variable (default: `http://localhost:8080`)

## Minimum content length

Messages shorter than 20 characters (after trimming) are skipped -- too short
to contain extractable knowledge.
