# Imprint: API Reference

Three interfaces to the same core: HTTP REST API, MCP server (stdio), and CLI. All delegate to `Engine.Ingest()` and `Querier.Query()`.

---

## 1. HTTP REST API

Start with `imprint serve`. Default address: `127.0.0.1:8080`. Override with `--host` and `--port` flags or `[api]` config section.

All responses are JSON with `Content-Type: application/json`. Errors return `{"error": "<message>"}` with an appropriate HTTP status code.

### POST /ingest

Extract facts, entities, and relationships from text.

**Request:**

```json
{
  "text": "Alice decided to use Go for Acme.",
  "source": "session-42"
}
```

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `text` | string | yes | -- | Text to extract knowledge from |
| `source` | string | no | `"api"` | Source identifier for provenance |

**Response (200):**

```json
{
  "facts_count": 2,
  "entities_count": 2,
  "relationships_count": 1,
  "fact_ids": ["01JFA..."],
  "entity_ids": ["01JFB..."],
  "relationship_ids": ["01JFC..."]
}
```

### GET /query?q=...

Ask a question against the knowledge base.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `q` | string | yes | Natural language question |

**Response (200):**

```json
{
  "answer": "Acme is written in Go.",
  "citations": [
    {"fact_id": "01JFA..."},
    {"consolidation_id": "01JFD..."}
  ],
  "facts_consulted": 12
}
```

### GET /status

Database statistics, wrapped with version info.

**Response (200):**

```json
{
  "version": "0.1.0",
  "stats": {
    "facts": 142,
    "entities": 38,
    "relationships": 67,
    "consolidations": 5,
    "ingested_files": 12
  }
}
```

### GET /entities

List entities, optionally filtered by type.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `type` | string | no | -- | Filter by entity type |
| `limit` | int | no | 50 | Max results |

Entity types: `person`, `project`, `tool`, `server`, `concept`, `organization`, `location`, `document`, `agent`.

**Response (200):**

```json
[
  {
    "id": "01JFB...",
    "name": "Alice",
    "entity_type": "person",
    "aliases": ["alice"],
    "created_at": "2026-03-15T14:30:00Z"
  }
]
```

### GET /facts

List facts, optionally filtered by type and subject.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `type` | string | no | -- | Filter by fact type |
| `subject` | string | no | -- | Filter by subject |
| `limit` | int | no | 50 | Max results |

Fact types: `preference`, `decision`, `rule`, `project`, `contact`, `bio`, `context`, `lesson`, `workflow`, `goal`, `event`, `skill`.

**Response (200):**

```json
[
  {
    "id": "01JFA...",
    "source": {"transcript_file": "session.md", "line_range": [10, 15]},
    "fact_type": "decision",
    "subject": "Acme",
    "content": "Acme will be written in Go.",
    "confidence": 0.95,
    "validity": {"valid_from": "2026-03-15T00:00:00Z"},
    "created_at": "2026-03-15T14:30:00Z"
  }
]
```

### PATCH /facts/{id}

Update metadata on an existing fact (confidence, expiry, subject). Does not change the fact content -- use POST /facts/{id}/supersede for that.

**Request:**

```json
{
  "confidence": 0.95,
  "valid_until": "2026-04-01T00:00:00Z",
  "subject": "Acme"
}
```

All fields are optional. Only non-null fields are updated.

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `confidence` | number | no | New confidence score (0.0 to 1.0) |
| `valid_until` | string | no | Expiry date (ISO-8601) |
| `subject` | string | no | Corrected subject |

**Response (200):** The updated fact (same schema as GET /facts items).

**Errors:**
- 404: fact not found
- 400: invalid JSON or invalid date format

### POST /facts/{id}/supersede

Replace a fact with updated content. The old fact is marked as superseded; a new fact is created with the corrected content. The new fact inherits the type and subject from the old one.

**Request:**

```json
{
  "new_content": "Alice switched to Go in March 2026.",
  "source": "manual"
}
```

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `new_content` | string | yes | -- | The corrected/updated fact content |
| `source` | string | no | `"api"` | Source identifier for provenance |

**Response (200):** The new fact (same schema as GET /facts items). The old fact's `superseded_by` field now points to the new fact's ID.

**Errors:**
- 404: old fact not found
- 400: missing `new_content` or invalid JSON

### GET /graph/{id}

Get the subgraph around an entity: connected entities and relationships up to a given depth.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `id` (path) | string | yes | -- | Entity ID |
| `depth` | int | no | 2 | Traversal depth (max 5) |

**Response (200):**

```json
{
  "center": {"id": "01JFB...", "name": "Alice", "entity_type": "person"},
  "entities": [...],
  "relationships": [
    {
      "id": "01JFC...",
      "from_entity": "01JFB...",
      "to_entity": "01JFE...",
      "relation_type": "works_on",
      "source_fact": "01JFA...",
      "created_at": "2026-03-15T14:30:00Z"
    }
  ]
}
```

---

## 2. MCP Tools

Start with `imprint mcp`. Runs over stdio (JSON-RPC). Compatible with Cursor, Claude Code, and other MCP clients. The MCP server advertises the binary version in its server info (`imprint`, version string).

### imprint_ingest

Extract facts, entities, and relationships from text.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `text` | string | yes | Text to extract knowledge from |
| `source` | string | no | Source identifier (default: `"mcp"`) |

Returns JSON with `facts_count`, `entities_count`, `relationships_count`, and ID arrays.

### imprint_query

Ask a question against the knowledge base.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `question` | string | yes | Natural language question |

Returns JSON with `answer`, `citations`, and `facts_consulted`.

### imprint_status

Show knowledge base statistics. No parameters.

Returns JSON with `facts`, `entities`, `relationships`, `consolidations`, `ingested_files`.

### imprint_entities

List entities in the knowledge graph.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `type` | string | no | Filter by entity type |
| `limit` | number | no | Max results (default 50) |

Returns JSON array of entities.

### imprint_graph

Get the subgraph around an entity.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `entity` | string | yes | Entity name (case-insensitive lookup) |
| `depth` | number | no | Traversal depth (default 2, max 5) |

Returns JSON with `center`, `entities`, and `relationships`.

### imprint_update_fact

Update metadata on an existing fact (confidence, expiry, subject). Does not change the fact content.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `fact_id` | string | yes | ID of the fact to update |
| `confidence` | number | no | New confidence score (0.0 to 1.0) |
| `valid_until` | string | no | Expiry date (ISO-8601) |
| `subject` | string | no | Corrected subject |

Returns JSON with the updated fact.

### imprint_supersede_fact

Replace a fact with updated content. Old fact is marked as superseded; new fact is created.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `old_fact_id` | string | yes | ID of the fact to supersede |
| `new_content` | string | yes | The corrected/updated fact content |
| `source` | string | no | Source identifier (default: `"mcp"`) |

Returns JSON with the new fact.

---

## 3. CLI

```
imprint [--config path] <command> [args]
```

Global flag: `--config` sets the config file path. Default: `config.toml`. Environment variable: `IMPRINT_CONFIG`.

### Commands

| Command | Arguments | Description |
|---------|-----------|-------------|
| `ingest` | -- | Read text from stdin, extract facts |
| `ingest-dir` | `[--consolidate] PATH` | Process all `.txt`/`.md` files in a directory |
| `watch` | `PATH` | Watch a directory for new/changed files (long-running) |
| `consolidate` | -- | Run one consolidation pass |
| `status` | -- | Show database statistics |
| `query` | `QUESTION` | Ask a question against the knowledge base |
| `embed-backfill` | `[--model=X] [--chunks]` | Generate embeddings for facts (or chunks with `--chunks`) without them, or re-embed by model |
| `serve` | `[--host=H] [--port=P] [--watch=PATH]` | Start HTTP API server. `--watch` starts a file watcher alongside the server. |
| `mcp` | -- | Start MCP server (stdio transport) |
| `export` | `[--format=json\|csv] [--output=path]` | Export entire knowledge base |
| `gc` | -- | Delete expired facts (valid_until < now - gc_after_days) |
| `version` | -- | Print version and exit |

The `--version` flag (before any command) also prints version and exits.

**Service discovery:** The `serve` command writes its actual listen address to `~/.imprint/serve.json` on startup. If the configured port is busy, it tries the next available port (up to +20). Set `IMPRINT_ADVERTISE_URL` to override the URL written to `serve.json` (useful when behind a proxy or on a remote server).

### Examples

```bash
# Pipe text to ingest
echo "Alice prefers dark mode." | imprint ingest

# Process a transcript directory
imprint ingest-dir /data/transcripts

# Process and consolidate in one pass
imprint ingest-dir --consolidate /data/transcripts

# Watch for new files
imprint watch /data/transcripts

# Ask a question
imprint query "What language is Acme written in?"

# Backfill embeddings
imprint embed-backfill

# Re-embed facts from a specific model
imprint embed-backfill --model=text-embedding-3-small

# Start HTTP server on custom port
imprint serve --port=9090

# Start HTTP server with file watcher
imprint serve --watch=/data/transcripts

# Start MCP server
imprint mcp

# Export as JSON
imprint export --format=json --output=backup.json

# Export as CSV (creates export/ directory)
imprint export --format=csv

# Backfill chunk embeddings
imprint embed-backfill --chunks

# Delete expired facts
imprint gc

# Print version
imprint version
imprint --version
```
