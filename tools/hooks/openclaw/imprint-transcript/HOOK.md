---
name: imprint-transcript
description: "Converts session transcript to Imprint format on session end"
metadata: { "openclaw": { "emoji": "📄", "events": ["command:new", "command:reset", "command:stop"] } }
---

# imprint-transcript

Fires when a session ends. Converts the full session JSONL to Imprint's
annotated markdown format (D25) and writes it to the transcripts directory
where Imprint's file watcher or `ingest-dir` can pick it up.

Three trigger events -- all mean "current session is done, transcript is ready":

- `command:new` -- user starts a new session (previous session is complete)
- `command:reset` -- user resets the session
- `command:stop` -- user stops the agent

Excluded: `session:compact:after` -- compaction compresses history mid-session.
Ingesting a compacted transcript would produce a lossy version.

## How it works

1. Reads the session JSONL path from `event.context.sessionFile`, falling back
   to `~/.openclaw/agents/main/sessions/{sessionId}.jsonl`
2. Spawns `python3 openclaw-adapter <jsonl> <output-dir>` as a detached process
3. The adapter writes `{session_id}.md` to the output directory
4. Imprint's watcher picks up the new file on its next poll

The child process is fully detached (fire-and-forget). The hook returns
immediately without waiting for conversion to complete.

## Requirements

- Python 3 (for the openclaw-adapter script)
- The adapter script at `tools/adapters/openclaw-adapter` (relative to the hook)
  or at the path specified by `IMPRINT_ADAPTER_PATH`

## Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `IMPRINT_TRANSCRIPTS_DIR` | `~/.openclaw/workspace/memory/transcripts` | Directory where converted markdown files are written |
| `IMPRINT_ADAPTER_PATH` | `../../adapters/openclaw-adapter` (relative to hook dir) | Path to the openclaw-adapter Python script |

## Installation

```bash
cp -r tools/hooks/openclaw/imprint-transcript ~/.openclaw/hooks/
```

## Session identity note

The adapter uses the JSONL filename stem as the `session` field in frontmatter.
The realtime `imprint-ingest` hook constructs its source as
`channelId-conversationId-date`. These identifiers may not match. Session ID
alignment is tracked in BVP-232 and will be resolved when D27 (dual-layer
supersede) is implemented in Imprint's Go code.
