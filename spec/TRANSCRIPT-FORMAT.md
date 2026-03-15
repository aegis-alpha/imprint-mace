# Transcript Input Format

Standardized intermediate format for agent conversation transcripts. Platform adapters convert native JSONL to this format; Imprint reads one format.

Frontmatter parser: `internal/transcript/frontmatter.go`. Platform adapters: `tools/adapters/` (cursor, claude-code, openclaw).

---

## 1. Overview

Agent platforms (Cursor, Claude Code, OpenClaw, Craft Agents) store conversation transcripts as JSONL files with incompatible schemas. Imprint needs a single, stable input contract so that:

- Each platform requires only one small adapter (20-30 lines)
- Imprint's BatchAdapter processes one format, not four
- Adding a new platform means writing one adapter, not changing Imprint

The format is annotated markdown: YAML frontmatter for metadata, plain text body for conversation content. Files use the `.md` extension and are processed by the existing BatchAdapter (`internal/ingest/batch.go`).

---

## 2. File Format

A transcript file is a UTF-8 markdown file with two sections:

```
---
<YAML frontmatter>
---

<body: timestamped, role-prefixed messages>
```

The frontmatter block is delimited by `---` on its own line. Everything after the closing `---` is the body.

Files without frontmatter (plain `.txt` or `.md`) continue to work unchanged. Frontmatter is optional enrichment, not a requirement. See section 7 (Backward Compatibility).

---

## 3. Frontmatter Fields

| Field | Type | Required | Description | Maps to |
|-------|------|----------|-------------|---------|
| `source` | string | yes | Platform that produced the transcript | -- |
| `session` | string | yes | Session ID on the source platform | -- |
| `date` | string (ISO-8601) | no | Session start timestamp | `transcripts.date` |
| `participants` | string[] | no | Participant names or roles | `transcripts.participants` (JSON array) |
| `topic` | string | no | Session topic or title, if known | `transcripts.topic` |

### Field details

**source**

One of: `cursor`, `claude-code`, `openclaw`, `craft-agents`. Lowercase, kebab-case. New platforms add new values; existing values never change.

**session**

The platform's native session identifier. For Cursor and Claude Code this is a UUID. For OpenClaw this is the session key. The value is opaque to Imprint -- stored but not interpreted.

**date**

ISO-8601 timestamp of session start. Full precision preferred (`2026-03-15T14:30:00Z`), but date-only (`2026-03-15`) is accepted. If the platform does not record a session start time, omit the field.

**participants**

YAML sequence of strings. Typical values: `[user, assistant]`. Multi-agent sessions may include agent names: `[user, assistant, code-reviewer]`. If the platform does not distinguish participants, omit the field.

**topic**

Free-text string. Adapters may derive this from the first user message, a session title, or a platform-provided label. If no topic is available, omit the field.

### Example frontmatter

```yaml
---
source: cursor
session: a1b2c3d4-e5f6-7890-abcd-ef1234567890
date: 2026-03-15T14:30:00Z
participants: [user, assistant]
topic: Database configuration
---
```

---

## 4. Body Format

The body contains conversation messages in chronological order.

### Message format

```
[<timestamp>] <role>:
<content>
```

- **Timestamp**: ISO-8601, enclosed in square brackets. Optional -- if the platform does not provide per-message timestamps, use `[]` (empty brackets).
- **Role**: one of `user`, `assistant`, `system`, `tool`. Lowercase.
- **Content**: plain text, may span multiple lines. Preserves original formatting including code blocks, lists, and whitespace.
- **Separator**: one blank line between messages.

### Roles

| Role | Meaning |
|------|---------|
| `user` | Human user input |
| `assistant` | AI assistant response |
| `system` | System-level message (rare; most system messages are stripped by adapters) |
| `tool` | Tool output that the user saw (not internal tool calls) |

### Timestamps

When available, timestamps use full ISO-8601 with timezone: `2026-03-15T14:30:05Z`.

When the platform provides relative ordering but not absolute times, adapters may use the session start time plus a sequential offset, or omit timestamps entirely.

When timestamps are unknown, use empty brackets:

```
[] user:
How do I configure the database?
```

### Code in messages

Code blocks in message content are preserved as-is. The extraction LLM handles code interpretation; the adapter does not modify content.

### Example body

```
[2026-03-15T14:30:00Z] user:
How do I configure the database?

[2026-03-15T14:30:05Z] assistant:
You need to set the connection string in config.toml:

    [database]
    path = "data/imprint.db"

This creates a SQLite database at the specified path.

[2026-03-15T14:31:00Z] user:
What about the embedding dimensions?

[2026-03-15T14:31:10Z] assistant:
Add this to your config:

    [embedding]
    dimensions = 384
```

---

## 5. Platform Examples

### 5.1 Cursor

**Input** (JSONL, one line per entry):

```json
{"uuid":"msg-001","type":"human","text":"How do I configure the database?"}
{"uuid":"msg-002","type":"ai","text":"You need to set the connection string in config.toml..."}
{"uuid":"msg-003","type":"tool_use","name":"read_file","input":{"path":"config.toml"}}
{"uuid":"msg-004","type":"tool_result","content":"[database]\npath = \"data/imprint.db\""}
```

**Output** (annotated markdown):

```markdown
---
source: cursor
session: a1b2c3d4-e5f6-7890-abcd-ef1234567890
participants: [user, assistant]
---

[] user:
How do I configure the database?

[] assistant:
You need to set the connection string in config.toml...
```

Notes: Cursor JSONL does not include timestamps, so brackets are empty. Tool calls (`tool_use`, `tool_result`) are stripped. Types `human` and `ai` map to `user` and `assistant`.

### 5.2 Claude Code

**Input** (JSONL, one line per entry):

```json
{"type":"human","uuid":"h-001","sessionId":"sess-abc","timestamp":"2026-03-15T14:30:00Z","text":"How do I configure the database?"}
{"type":"assistant","uuid":"a-001","parentUuid":"h-001","sessionId":"sess-abc","timestamp":"2026-03-15T14:30:05Z","text":"You need to set the connection string...","thinking":"Let me check the config format..."}
{"type":"tool_use","uuid":"t-001","parentUuid":"a-001","name":"Read","input":{"file_path":"config.toml"}}
{"type":"tool_result","uuid":"tr-001","parentUuid":"t-001","content":"[database]\npath = ..."}
```

**Output** (annotated markdown):

```markdown
---
source: claude-code
session: sess-abc
date: 2026-03-15T14:30:00Z
participants: [user, assistant]
---

[2026-03-15T14:30:00Z] user:
How do I configure the database?

[2026-03-15T14:30:05Z] assistant:
You need to set the connection string...
```

Notes: Thinking blocks are stripped. Tool calls are stripped. Timestamps are preserved from the `timestamp` field. `human` maps to `user`.

### 5.3 OpenClaw

**Input** (JSONL, one line per entry):

```json
{"id":"oc-001","role":"user","content":"How do I configure the database?","timestamp":"2026-03-15T14:30:00Z","parentId":null}
{"id":"oc-002","role":"assistant","content":"You need to set the connection string...","timestamp":"2026-03-15T14:30:05Z","parentId":"oc-001"}
{"id":"oc-003","role":"tool","content":"[read_file] config.toml -> [database]\\npath = ...","timestamp":"2026-03-15T14:30:06Z","parentId":"oc-002"}
```

**Output** (annotated markdown):

```markdown
---
source: openclaw
session: session-key-xyz
date: 2026-03-15T14:30:00Z
participants: [user, assistant]
---

[2026-03-15T14:30:00Z] user:
How do I configure the database?

[2026-03-15T14:30:05Z] assistant:
You need to set the connection string...
```

Notes: OpenClaw uses tree structure (`parentId`); the adapter linearizes messages in chronological order. Tool messages are stripped. Roles map directly (`user` -> `user`, `assistant` -> `assistant`).

### 5.4 Craft Agents

**Input** (JSONL, same schema as Claude Code via shared SDK):

```json
{"type":"human","uuid":"h-001","sessionId":"craft-sess-001","timestamp":"2026-03-15T14:30:00Z","text":"How do I configure the database?"}
{"type":"assistant","uuid":"a-001","parentUuid":"h-001","sessionId":"craft-sess-001","timestamp":"2026-03-15T14:30:05Z","text":"You need to set the connection string...","thinking":"Checking config..."}
```

**Output** (annotated markdown):

```markdown
---
source: craft-agents
session: craft-sess-001
date: 2026-03-15T14:30:00Z
participants: [user, assistant]
---

[2026-03-15T14:30:00Z] user:
How do I configure the database?

[2026-03-15T14:30:05Z] assistant:
You need to set the connection string...
```

Notes: Craft Agents uses the Claude Agent SDK, so the JSONL schema is identical to Claude Code. The adapter differs only in the `source` field value and the session directory path.

---

## 6. Rules for Adapters

### What an adapter MUST do

1. Read the platform's native JSONL file
2. Extract `role`, `content`, and `timestamp` for each message
3. Map platform-specific roles to standard roles (`human`/`ai` -> `user`/`assistant`)
4. Write YAML frontmatter with `source` and `session` (required), plus `date`, `participants`, `topic` if available
5. Write body in the format `[timestamp] role:` followed by content
6. Strip tool calls, tool results, thinking blocks, and internal system messages -- keep only user-visible content (user + assistant messages)
7. Linearize tree-structured conversations into chronological order
8. Save as a `.md` file

### What an adapter MUST NOT do

1. **Do not call Imprint.** The adapter is a converter, not an ingest pipeline. It writes a file; Imprint reads it separately.
2. **Do not modify content.** No summarization, no cleaning, no translation, no reformatting. The content must be byte-identical to the platform's original text (minus stripped elements).
3. **Do not deduplicate.** Deduplication is the BatchAdapter's responsibility (SHA-256 content hash).
4. **Do not chunk.** Chunking is the BatchAdapter's responsibility.
5. **Do not add metadata that the platform does not provide.** If there is no topic, omit `topic`. Do not invent one.

### Adapter location

Adapters live in `tools/adapters/`, outside Imprint's core packages. Each adapter is a standalone Python script. Naming: `<platform>-adapter`.

Implemented adapters:

| Adapter | Platform | Timestamps | Notes |
|---------|----------|-----------|-------|
| `cursor-adapter` | Cursor | No (empty brackets) | Reads `content[].text` blocks |
| `claude-code-adapter` | Claude Code | Yes (per message) | Strips thinking blocks |
| `openclaw-adapter` | OpenClaw | Yes (per message) | Filters `[cron:` messages, handles string and array content |

---

## 7. Backward Compatibility

Files without YAML frontmatter continue to work. The BatchAdapter already processes plain `.txt` and `.md` files by reading them as raw text, chunking, and passing to extraction.

When frontmatter is present, the frontmatter parser extracts metadata and populates the `Transcript` struct fields (`date`, `participants`, `topic`). When frontmatter is absent, these fields remain null/empty.

The body format (timestamped, role-prefixed messages) is valid plain text. Even without a frontmatter parser, the extraction LLM can process the body as ordinary conversation text. The format degrades gracefully.

### Migration path

No migration is needed. Existing plain text files in the ingest directory are unaffected. New adapter-produced files with frontmatter are processed alongside them. Both formats coexist indefinitely.

---

## 8. Data Flow

```
Platform JSONL  -->  Adapter  -->  Annotated .md  -->  BatchAdapter  -->  Engine.Ingest()
                                       |
                                       v
                              Frontmatter parser
                                       |
                                       v
                              Transcript metadata
                              (date, participants, topic)
```

The adapter runs outside Imprint (a hook, a cron job, or a manual invocation). It produces `.md` files in the ingest directory. The BatchAdapter picks them up on the next run, chunks them, checks dedup, and delegates to `Engine.Ingest()`. The frontmatter parser runs as part of the BatchAdapter's file processing, before chunking.
