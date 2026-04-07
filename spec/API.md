# Imprint: API Reference

Three interfaces to the same core: HTTP REST API, MCP server (stdio), and CLI. All delegate to `Engine.Ingest()` and `Querier.Query()`.

---

## 1. HTTP REST API

Start with `imprint serve`. Default address: `127.0.0.1:8080`. Override with `--host` and `--port` flags or `[api]` config section.

All responses are JSON with `Content-Type: application/json`. Errors return `{"error": "<message>"}` with an appropriate HTTP status code.

If `[llm].base_url` is configured, Imprint runs in Prism mode: all chat/embedding/rerank backend calls route to one endpoint with task headers (`X-Prism-Task`, `X-Prism-App`) and direct `[[providers.*]]` chains are ignored.

Vector backend mode is configured via `[vector].mode`:
- `read-write` (default): processes that require vector writes must pass a startup write-path self-test.
- `read-only`: explicit operator choice; vector search stays available, vector writes are rejected centrally.
- `disabled`: no vector backend is attached.

### POST /ingest

Behavior depends on config `[hot].enabled` and optional request field `mode`.

#### Cold path (default, or hot disabled, or `mode: "extract"`)

Runs LLM extraction: facts, entities, and relationships.

**Request:**

```json
{
  "text": "Alice decided to use Go for Acme.",
  "source": "session-42",
  "mode": "extract"
}
```

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `text` | string | yes | -- | Text to extract knowledge from |
| `source` | string | no | `"api"` | Source identifier for provenance |
| `mode` | string | no | (omit) | Set to `"extract"` to force cold path even when `[hot]` is enabled |

**Response (200):**

```json
{
  "facts_count": 2,
  "entities_count": 2,
  "relationships_count": 1,
  "entity_collisions": 1,
  "entity_creations": 1,
  "fact_ids": ["01JFA..."],
  "entity_ids": ["01JFB..."],
  "relationship_ids": ["01JFC..."]
}
```

#### Hot path (`[hot].enabled = true` and `mode` is not `"extract"`)

Stores one raw message per request (no extraction LLM). Optional synchronous embedding when `len(text) >= embed_min_chars` and an embedder chain is configured.

**Request:** same JSON shape; `source` may use prefix `realtime:` so the remainder becomes `platform_session_id` (see `spec/HOT-PHASE-SPEC.md`).

**Response (200):**

```json
{
  "id": "01JQ...",
  "has_embedding": true,
  "hot": true
}
```

| Field | Type | Description |
|-------|------|-------------|
| `id` | string | New hot message ULID |
| `has_embedding` | bool | Whether a vector was stored for this message |
| `hot` | bool | Always `true` for this path |

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
    {"consolidation_id": "01JFD..."},
    {"hot_message_id": "hot:01JQ..."},
    {"hot_message_id": "cool:01JR..."}
  ],
  "facts_consulted": 8
}
```

| Field | Type | Description |
|-------|------|-------------|
| `answer` | string | Synthesized answer |
| `citations` | array | Each object may include `fact_id`, `consolidation_id`, and/or `hot_message_id` |
| `hot_message_id` | string | Raw message citation: `hot:<ULID>` or `cool:<ULID>` (matches Fresh Messages tags in the synthesis prompt) |
| `facts_consulted` | number | Count of **structured facts** in the merged retrieval list passed to synthesis (excludes hot/cooldown raw rows) |

**Reranking behavior:** Query always applies post-merge reranking on the structured-fact prefix selected by `[rerank].top_n`. Default reranker is local cosine rescoring (query embedding vs fact embedding, zero external calls). If `[[providers.reranker]]` is configured, the first entry is used as an HTTP rerank endpoint (`/v1/rerank`, with Cohere `/v2/rerank` compatibility); cosine remains the startup fallback when provider init fails.

### GET /status

Database statistics, wrapped with version info.

**Response (200):**

```json
{
  "version": "v0.7.1-dev+abc1234",
  "stats": {
    "facts": 142,
    "entities": 38,
    "relationships": 67,
    "consolidations": 5,
    "ingested_files": 12,
    "hot_messages": 23,
    "cooldown_messages": 156
  },
  "vector_backend": {
    "backend": "usearch",
    "mode": "read-write",
    "status": "healthy",
    "read_available": true,
    "write_safe": true,
    "last_self_test": "fresh_add_repeated_add_save_load_search",
    "detail": "USearch fresh-index write path passed first add, repeated add, save/load, and search checks"
  },
  "quality_signals": [
    {
      "signal_type": "supersede_rate",
      "category": "decision",
      "value": 0.12,
      "details": "{\"sample_size\": 50}",
      "created_at": "2026-03-20T10:00:00Z"
    }
  ],
  "query_stats": {
    "total_queries": 84,
    "total_context": 210,
    "avg_query_latency": 1250,
    "avg_context_latency": 180,
    "error_count": 2,
    "embedder_avail_pct": 100
  },
  "eval_scores": {
    "extraction": {"score": 0.72, "examples": 38, "date": "2026-03-20T09:00:00Z"},
    "retrieval": {"score": 0.85, "score2": 0.91, "examples": 21, "date": "2026-03-20T09:05:00Z"}
  },
  "providers": [
    {
      "provider_name": "google",
      "task_type": "extraction",
      "configured_model": "gemini-2.5-flash",
      "active_model": "gemini-2.5-flash",
      "status": "ok"
    },
    {
      "provider_name": "anthropic",
      "task_type": "extraction",
      "configured_model": "claude-sonnet-4",
      "active_model": "",
      "status": "auth_error",
      "last_error": "status 401: unauthorized",
      "retry_count": 1
    }
  ],
  "retry_queue_depth": 0,
  "cool_stats": {
    "clusters_pending": 3,
    "clusters_extracted": 12,
    "messages_processed": 187
  }
}
```

Optional fields (omitted when empty):

| Field | Type | Present when |
|-------|------|-------------|
| `vector_backend` | object | Always for `SQLiteStore`-backed status responses. Fields: `backend`, `mode`, `status`, `read_available`, `write_safe`, `last_self_test` (if one ran), `detail`. |
| `quality_signals` | array | Quality signals have been collected (deduplicated: latest per signal_type + category) |
| `query_stats` | object | At least one query or context request in the last 30 days |
| `eval_scores` | object | At least one eval run recorded. `extraction`: composite score. `retrieval`: score = Recall@10, score2 = MRR. |
| `providers` | array | Provider health entries exist. Each entry: provider_name, task_type, configured_model, active_model, status, last_error (if any), retry_count (if > 0). Status merged from provider_health + provider_ops. |
| `retry_queue_depth` | int | Pending + processing entries in retry queue (omitted when 0). |
| `cool_stats` | object | Cool pipeline stats when `[cool] enabled`: `clusters_pending` (unprocessed clusters), `clusters_extracted` (processed clusters), `messages_processed` (cooldown messages with processed_at set). Omitted when disabled or all counts are zero. |

### GET /context

Retrieve relevant context from the knowledge base without LLM synthesis. Returns structured text with preferences, recent facts, and (if a hint is provided) semantically relevant facts. Designed for hook integrations where fast, deterministic context injection is needed (50-200ms typical).

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `hint` | string | no | Search hint for semantic relevance (e.g. user's message). If empty, returns only preferences and recent facts. |

**Response (200):**

```json
{
  "context": "## Relevant Context\n- [decision] Acme: Decided to use Go for the Acme project (confidence=0.95, 2026-03-15)\n\n## Preferences\n- Alice: Prefers dark mode in all editors\n\n## Recent\n- [project] Acme: Acme uses SQLite for storage (2026-03-15)"
}
```

The `context` field contains structured markdown text with sections (Relevant Context, Preferences, Recent). Sections are omitted when empty. Returns `{"context": ""}` when no facts are found.

**Comparison with GET /query:**

| | GET /context | GET /query |
|---|---|---|
| LLM call | No | Yes |
| Latency | 50-200ms | 5-30s |
| Output | Structured text (sections) | Synthesized answer with citations |
| Use case | Hook context injection | Agent tool (on-demand) |

---

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

### GET /relationships

List relationships in the knowledge graph.

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `type` | string | no | -- | Filter by relation type |
| `entity` | string | no | -- | Filter by entity ID (matches from_entity or to_entity) |
| `limit` | int | no | 50 | Max results |

Relation types: `owns`, `uses`, `works_on`, `depends_on`, `related_to`, `created_by`, `part_of`, `manages`, `located_at`.

**Response (200):**

```json
[
  {
    "id": "01JFC...",
    "from_entity": "01JFB...",
    "to_entity": "01JFE...",
    "relation_type": "works_on",
    "source_fact": "01JFA...",
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

Same branching as HTTP `POST /ingest`: cold extraction by default; hot raw ingest when `[hot].enabled` is true unless `mode` is `"extract"`.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `text` | string | yes | Text to ingest |
| `source` | string | no | Source identifier (default: `"mcp"`); `realtime:<session>` sets platform session id |
| `mode` | string | no | `"extract"` forces LLM extraction even when hot is enabled |

**Cold response:** JSON with `facts_count`, `entities_count`, `relationships_count`, and ID arrays.

**Hot response:** JSON with `id`, `has_embedding`, `hot` (see HTTP hot path above).

### imprint_query

Ask a question against the knowledge base.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `question` | string | yes | Natural language question |

Returns JSON with `answer`, `citations`, and `facts_consulted`. Citations may include `hot_message_id` (`hot:` or `cool:` prefix). `facts_consulted` counts structured facts only, not raw hot/cooldown rows.

**Note:** The LLM also returns `confidence` and `notes` fields, but these are parsed and discarded by `parseResponse` -- they do not appear in the response. See ARCHITECTURE.md section 2.4.

### imprint_status

Show knowledge base statistics. No parameters.

Returns JSON with `facts`, `entities`, `relationships`, `consolidations`, `ingested_files`, `hot_messages`, `cooldown_messages`.

### imprint_entities

List entities in the knowledge graph.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `type` | string | no | Filter by entity type |
| `limit` | number | no | Max results (default 50) |

Returns JSON array of entities.

### imprint_relationships

List relationships in the knowledge graph.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `type` | string | no | Filter by relation type |
| `entity` | string | no | Filter by entity ID (matches from_entity or to_entity) |
| `limit` | number | no | Max results (default 50) |

Returns JSON array of relationships.

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

### Conditional Registration

Not all tools and resources are registered unconditionally:

- `imprint_query` is registered only when a `Querier` is configured (i.e. `querier != nil`). Without query providers, the tool is absent from the MCP tool list.
- MCP Resources (below) are registered only when a context `Builder` is configured (i.e. `builder != nil`).

### MCP Resources

When a context builder is configured, the MCP server exposes read-only resources for context retrieval without LLM synthesis.

**Static resources:**

| URI | Name | Description |
|-----|------|-------------|
| `imprint://context/relevant` | Relevant Context | Relevant facts, preferences, and recent knowledge |
| `imprint://context/preferences` | User Preferences | All stored user preferences |
| `imprint://context/recent` | Recent Facts | Facts created in the last 24 hours |

**Resource templates:**

| URI Template | Name | Description |
|--------------|------|-------------|
| `imprint://context/entities/{name}` | Entity Context | Context about a specific entity (subgraph, related facts) |

All resources return `text/plain` MIME type.

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
| `eval` | `--golden=PATH [--format=json\|table] [--save-baseline] [--check] [--threshold=N]` | Evaluate extraction quality against a golden dataset. Baseline is managed automatically: first run becomes baseline, subsequent runs update it on improvement. `--save-baseline` forces a manual override. `--check` compares with baseline and exits non-zero on regression. Default threshold: 0.05. |
| `eval generate` | `[--output=PATH]` | Generate built-in golden eval dataset (default: `testdata/golden/`) |
| `eval-retrieval` | `[--format=json\|table] [--no-embedder] [--merge-strategy=rrf\|set-union] [--save-baseline] [--check] [--threshold=N]` | Evaluate retrieval quality (Recall@10, MRR) against the built-in corpus. `--no-embedder` runs text+graph only. When embeddings are enabled, eval uses a pure-Go exact vector backend rather than production USearch, so harness quality stays deterministic and portable. `--merge-strategy` selects RRF (default) or set-union dense-first ordering. Baselines and history are tracked under `retrieval`. |
| `eval-history` | `[--type=extraction\|retrieval] [--limit=N]` | Show eval score history with deltas and baseline markers. Default limit: 10. |
| `optimize` | -- | Run one prompt optimization cycle (Karpathy loop). After optimization, automatically runs both extraction and retrieval evals, persists results, and updates baselines. This is the primary way eval history accumulates. |
| `gc` | -- | Delete expired facts (valid_until < now - gc_after_days) |
| `lint` | `[--format=table\|json] [--check=a,b,...]` | SQL-only knowledge base integrity checks (stale facts, orphans, broken supersede chains, entity dedup hints, missing embeddings, missing or unreadable source files when `[watcher].path` is set, consolidation coverage). No LLM. `--check` limits which checks run (comma-separated): `chains`, `stale`, `dedup`, `embeddings`, `orphans`, `sources`, `consolidation`. Exits with status **1** if any ERROR-severity finding is reported (e.g. broken supersede chains). |
| `version` | -- | Print version and exit |

The `--version` flag (before any command) also prints version and exits.

Version strings use one of two forms:
- release build: `vX.Y.Z`
- identifiable dev build: `vX.Y.Z-dev+<shortsha>` when stamped at build time, or `dev+<shortsha>` when only Go VCS build metadata is available

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

# Generate the built-in golden eval dataset
imprint eval generate
imprint eval generate --output=./my-eval-data/

# Evaluate extraction quality against golden set
imprint eval --golden=testdata/golden/

# Evaluate and output JSON report
imprint eval --golden=testdata/golden/ --format=json

# Evaluate and save as baseline
imprint eval --golden=testdata/golden/ --save-baseline

# Evaluate and check against baseline (exit 1 on regression)
imprint eval --golden=testdata/golden/ --check

# Build context snapshot (no LLM, for system prompt injection)
imprint context "current project topic"

# Evaluate retrieval quality against the built-in corpus
imprint eval-retrieval

# Evaluate retrieval without embedder (graceful degradation)
imprint eval-retrieval --no-embedder

# Evaluate retrieval with JSON output
imprint eval-retrieval --format=json

# Evaluate retrieval with set-union merge strategy
imprint eval-retrieval --merge-strategy=set-union

# Evaluate retrieval and save as baseline
imprint eval-retrieval --no-embedder --save-baseline

# Check retrieval against baseline with custom threshold
imprint eval-retrieval --no-embedder --check --threshold=0.03

# Show eval history (all types)
imprint eval-history

# Show extraction eval history only
imprint eval-history --type=extraction --limit=20

# Run one prompt optimization cycle (Karpathy loop)
imprint optimize

# Delete expired facts
imprint gc

# KB integrity (SQL diagnostics)
imprint lint
imprint lint --format=json
imprint lint --check=stale,orphans,chains

# Print version
imprint version
imprint --version
```

---

## 4. Admin API

Destructive operations for data management. Available via HTTP only.

### POST /admin/reset

Wipe all data and recreate the database schema from scratch. This is destructive and irreversible.

**Request:**

- Header: `X-Confirm-Reset: yes` (required, prevents accidental calls)
- No body

**Response (200):**

```json
{"status": "reset complete"}
```

**Errors:**
- 400: missing `X-Confirm-Reset: yes` header

### DELETE /admin/facts

Delete facts whose `source_file` matches a SQL LIKE pattern. Also removes corresponding FTS and vector entries.

**Request:**

```json
{
  "source_pattern": "realtime:agent:main:cron:%"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `source_pattern` | string | yes | SQL LIKE pattern matched against `source_file` column |

**Response (200):**

```json
{"deleted": 142}
```

**Errors:**
- 400: missing `source_pattern` or invalid JSON

### POST /admin/deduplicate-entities

Find entity names that appear more than once (case-insensitive comparison), keep the oldest record, and merge the rest. Relationships referencing removed entities are re-pointed to the kept entity.

**Request:** No body.

**Response (200):**

```json
{
  "merged_groups": 7,
  "entities_removed": 12
}
```

| Field | Type | Description |
|-------|------|-------------|
| `merged_groups` | int | Number of duplicate name groups found and merged |
| `entities_removed` | int | Total entity records deleted (re-pointed first) |
