# Imprint: Architecture

Imprint is a persistent memory system for AI agents. It extracts structured knowledge (facts, entities, relationships) from conversation transcripts, stores them in a local SQLite knowledge graph, finds connections between facts, and evolves its own type taxonomy over time.

The system is library-first: the core is a set of Go packages (`internal/imprint`, `internal/extraction`, `internal/query`, etc.). Transport wrappers (HTTP API, MCP server, CLI) are thin layers that delegate to the same `Engine.Ingest()` and `Querier.Query()` calls.

Storage is two-layer: transcript files on disk are the source of truth; the SQLite database is a derived index with back-references (file path + line range) to the original text.

---

## 1. Ingest Pipeline

All text enters the system through `Engine.Ingest()`. There are three paths to reach it.

### 1.1 Batch Ingest

`internal/ingest.BatchAdapter` processes a directory of `.txt` and `.md` files.

1. **Discover files.** Read the directory (flat, non-recursive), select `.txt` and `.md` files. Subdirectories are skipped.
2. **Parse frontmatter.** If the file starts with `---\n`, extract YAML frontmatter (source, session, date, participants, topic). See `spec/TRANSCRIPT-FORMAT.md`.
3. **Dedup.** Compute SHA-256 of the file content. Compare against `ingested_files` table. If the hash matches, skip.
4. **Register transcript.** Create or update a `transcripts` row with metadata from frontmatter. On re-ingest, delete old `transcript_chunks` first.
5. **Chunk.** If the file exceeds 8000 bytes, split into ~4000-byte chunks at newline boundaries with 200-byte overlap. Each chunk carries its 1-based line range (start, end) in the original file.
6. **Register chunks.** Write a `transcript_chunks` row per chunk (transcript ID, line range, content hash).
7. **Ingest each chunk.** Call `Engine.Ingest(text, relPath, WithLineOffset(start, end))` for each chunk.
8. **Record.** Upsert `ingested_files` with path, hash, chunk count, fact count.

### 1.2 Realtime Ingest

Text arrives via the HTTP API (`POST /ingest`) or MCP tool (`imprint_ingest`). No chunking, no dedup, no transcript registration -- the text goes directly to `Engine.Ingest()`.

**Note:** The HTTP `POST /ingest` endpoint accepts only `text` and `source` fields. The `WithSessionID` ingest option exists in the Engine but is not exposed via the HTTP handler -- it is only reachable through direct Engine calls (e.g. from hooks).

**Performance note:** Each `Engine.Ingest()` call triggers one LLM extraction (2-10 seconds depending on text length and provider). For bulk operations, callers should process items sequentially with retry and backoff, not in parallel. Concurrent LLM calls risk rate limits and timeouts.

**OpenClaw integration:** Two deterministic integration paths:

1. **Ingest** (internal hook): The `imprint-ingest` hook listens for `message:preprocessed` events and sends the enriched message body to `POST /ingest` automatically. Every message is ingested without model intervention. This is a fire-and-forget hook -- the result is not needed by the gateway.
2. **Context delivery** (plugin): The `imprint-context` plugin registers a `before_prompt_build` hook and calls `GET /context?hint=<message>`. The result is returned as `{ prependContext }` which OpenClaw injects into the agent's prompt. This is the only way to deterministically inject context -- internal hooks (`message:preprocessed`) are fire-and-forget and cannot modify the prompt.

**Service discovery:** The server writes its actual listen address to `~/.imprint/serve.json` on startup. If the configured port is busy, it tries the next available port (up to +20). Hooks and other clients read this file to discover the server automatically. The file is removed on graceful shutdown.

### 1.3 Engine.Ingest()

`internal/imprint.Engine` is the single canonical path for all ingestion.

1. **Extract.** Send text to the LLM via `Extractor.Extract()`. Returns facts, entities, and relationships.
2. **Attach source.** If `WithLineOffset` was set, stamp every fact with the source file path and line range.
3. **Context TTL.** If `contextTTLDays > 0`, auto-set `valid_until` on context-type facts that don't already have an expiry. This makes situational facts (e.g. "node-2 is offline") expire automatically.
4. **Store entities.** Write each entity to the DB. Failures are logged and skipped (non-fatal).
5. **Embed + dedup.** If an `Embedder` is configured, generate a vector embedding for each fact. If `dedupThreshold > 0`, search for existing facts with cosine similarity above the threshold -- if found, skip the duplicate.
6. **Store facts.** Write each non-duplicate fact. Store its embedding alongside with the model name.
7. **Resolve relationships.** The LLM returns relationships with entity names. The engine maps names to entity IDs (from the entities just stored) and writes the relationships. Unknown entity references are logged and skipped.

### 1.4 Extraction

`internal/extraction.Extractor` converts text to structured data.

- The system prompt is a Go template (`prompts/extraction-prompt.md`) rendered at startup with the current type taxonomy from config. When the taxonomy changes, the prompt updates on the next restart.
- The LLM returns JSON with arrays of facts, entities, and relationships. Each fact has a type, subject, content, confidence, and optional temporal validity. The response is parsed through a robust fallback chain: `stripMarkdownFences` removes `` ```json ... ``` `` wrapping, `extractJSON` locates the first `{...}` object in surrounding text, and a text refusal detector catches non-JSON responses (e.g. "I cannot extract..."). This handles providers that wrap JSON in markdown fences or prepend conversational text.
- The `Extractor` hydrates the raw LLM output: generates ULID IDs, sets timestamps, attaches source info.
- Every extraction call is logged to `extraction_log` (provider, model, tokens, duration, counts, errors) via `ExtractionLogger`.

### 1.5 Dual-Layer Memory (D27)

Realtime and batch ingest coexist for the same session. Realtime facts are fast but coarse; batch facts are richer (full file references, line ranges). When batch catches up, it supersedes the realtime facts.

**Realtime path:**

1. `WithSessionID(id)` is an `IngestOption` that stamps all extracted facts with `realtime:{sessionID}` in the `source_file` field.
2. The OpenClaw `imprint-ingest` hook calls `POST /ingest` on every message, passing the session ID. Facts are available immediately but lack file-level source references.

**Batch supersede:**

1. When `BatchAdapter.processFile()` processes a transcript, it reads the `session` field from YAML frontmatter.
2. After ingesting all chunks, if `session` is non-empty, it calls `store.SupersedeRealtimeBySession(sessionID)`.
3. This marks all facts where `source_file = 'realtime:{sessionID}'` as superseded (`superseded_by = 'batch-replaced'`).
4. The batch facts replace them with full file references and line ranges.

**Design constraint:** Cosine similarity is not used for cross-path supersede. Simulation (D002) showed a 73% miss rate due to granularity drift between realtime single-message facts and batch multi-message chunk facts. Session-boundary supersede is deterministic and reliable.

### 1.7 Hot-Cool-Cold Pipeline (D31-D35)

**Status: Designed, not yet implemented.** Replaces per-message extraction (28% capture rate) with streaming storage + delayed topic-scoped extraction.

Core insight: the original problem framing ("how to extract from a stream") was wrong. The right framing: "how to make raw messages queryable immediately, and extract with full context later."

```
Message arrives (from hook/API)
    |
    v
[HOT PHASE] -- raw store, TTL ~1h, zero LLM cost
    |  1. Store raw message (speaker, content, timestamp, platform metadata)
    |  2. Selective embed (messages >= 50 chars get embedding)
    |  3. FTS5 index
    |  4. Queryable immediately via FTS5 + embed
    |
    | (TTL expires, message copied to cooldown)
    v
[COOL PHASE] -- topic clustering + extraction
    |  1. Messages accumulate in cooldown_messages table
    |  2. Hybrid Union topic segmentation (TreeSeg + TT+Merge, D32)
    |  3. Cluster readiness: topic silence (8h) or cluster size (50 msgs)
    |  4. Ready cluster -> format as 0GMem-style annotated block (D33)
    |  5. Extraction LLM call with full topic context
    |  6. Results -> Engine.Ingest() -> cold phase
    |
    v
[COLD PHASE] -- permanent store (existing infrastructure)
    Facts + Entities + Relationships
    Transcripts on disk
    Consolidation, Karpathy loop, 5-layer retrieval
```

**Two tables (D35):** `hot_messages` (raw, fast for realtime query) and `cooldown_messages` (same base + cluster_id, transcript linking, processing status). Separate tables prevent SQLite single-writer contention between realtime query and background processing.

**Topic segmentation (D32):** Hybrid Union algorithm (TreeSeg + TT+Merge, boundary union). Determines WHEN to extract (cluster readiness) and WHICH messages form a block. Does NOT reorder messages -- extraction receives chronological order. Experiment showed topic-based reordering adds no extraction quality.

**Annotation format (D33):** `[N] [Speaker] (date) (time) (->ref): content`. Linker (->ref) connects confirmations ("yes") to the specific proposal they confirm. Known facts NOT included in extraction prompt (paradoxical priming effect measured in E020).

**Triggers (D34):** Topic silence (8 hours, configurable) + cluster size (50 messages, safety valve). Session end rejected -- technical event, not semantic.

**Query path:** Three-phase search. Hot (FTS5 + embed over raw messages, freshest data), Cool (annotated messages not yet extracted), Cold (structured facts, existing 5-layer pipeline). Results merged via RRF, LLM synthesis handles mixed formats.

**Platforms without hooks:** Transcript files go directly to cool phase via file watcher. No hot phase. Same extraction pipeline.

### 1.6 Embedding

`internal/provider.EmbedderChain` generates vector embeddings for fact content.

- Embedders are tried in priority order (same fallback pattern as LLM providers).
- Each fact's embedding is stored in the `facts_vec` virtual table (sqlite-vec) alongside the fact ID.
- The `embedding_model` column on the fact records which model produced the vector. This enables selective re-embedding when switching providers.
- Embedder types: OpenAI-compatible (covers OpenAI, Google, etc.) and Ollama.

---

## 2. Query Pipeline

`internal/query.Querier` answers natural language questions using hybrid retrieval.

### 2.1 Retrieval Layers

Five layers run concurrently:

| Layer | Source | Method | Limit |
|-------|--------|--------|-------|
| Vector facts | `facts_vec` | KNN cosine search on question embedding | 20 |
| Vector chunks | `chunks_vec` | KNN cosine search on question embedding | 10 |
| FTS5 facts | `facts_fts` | BM25 keyword search on sanitized question | 10 |
| FTS5 chunks | `transcript_chunks_fts` | BM25 keyword search on sanitized question | 10 |
| Graph | `entities` + `relationships` | Word-match entity lookup, 1-hop traversal, collect source facts | unbounded |

Vector layers are skipped when no embedder is configured (graceful degradation to FTS5 + graph).

### 2.2 Merge and Rank

Results from all layers are deduplicated by fact ID and scored using Reciprocal Rank Fusion (k=60):

```
score(fact) = sum over layers: 1 / (k + rank_in_layer)
```

Facts that appear in multiple layers get higher scores.

### 2.3 ReadContext Enrichment

For the top 10 ranked facts that have source line references, the system loads surrounding lines (3 lines before and after) from the original transcript file on disk. This provides the LLM with conversational context around each fact.

### 2.4 LLM Synthesis

The system builds a prompt with three sections:

1. **Question** -- the user's question.
2. **Facts** -- ranked facts with ID, type, confidence, date, subject, content.
3. **Transcript Context** -- raw lines from disk for facts that have source references.

The LLM returns JSON with an answer, citations (fact IDs and/or consolidation IDs), a confidence score, and optional notes about contradictions or gaps. The `rawQueryResponse` struct parses all four fields, but `parseResponse` only passes `answer` and `citations` into `QueryResult` -- the `confidence` and `notes` fields are parsed and discarded.

---

## 3. Consolidation

`internal/consolidation` groups related facts, discovers connections between them, and generates higher-order insights.

### 3.1 Consolidator

`Consolidator.Consolidate(ctx, limit)` returns `[]ConsolidateResult`:

1. Fetch unconsolidated facts from the DB (up to `limit`). A fact is unconsolidated if its ID does not appear in any consolidation's `source_fact_ids` array (checked via `json_each`).
2. If fewer than 2 facts, skip.
3. **Pre-cluster** facts via `clusterFacts()` (see below). Each cluster is consolidated separately.
4. For each cluster: format facts as a list (`[ID] (type) subject: content`) and send to the LLM with the consolidation prompt template.
5. The LLM returns JSON: connections (fact_a, fact_b, connection_type, strength), a summary, an insight, and an importance score.
6. Store a `Consolidation` record per cluster (source fact IDs, summary, insight, importance).
7. Store `FactConnection` records for each connection.

Connection types: `supports`, `contradicts`, `elaborates`, `caused_by`, `supersedes`, `precedes`.

**Pre-clustering (`clusterFacts`):** Groups facts by embedding cosine similarity (configurable threshold via `cluster_similarity_threshold`, default 0.40). Facts without embeddings fall back to case-insensitive subject grouping. Remaining ungrouped facts go into a miscellaneous cluster. Clusters with fewer than 2 facts are dropped.

### 3.2 Scheduler

`Scheduler` runs consolidation in a background loop:

1. Tick at a configurable interval (set by the caller, e.g. `main.go`).
2. On each tick, count unconsolidated facts.
3. If count >= `min_facts` threshold, run `Consolidator.Consolidate()` with `max_group_size` as the limit.
4. Graceful shutdown via context cancellation.

The scheduler is used by the `watch` command. The `consolidate` CLI command runs a single pass without the scheduler.

---

## 4. Taxonomy Evolution

`internal/taxonomy` enables the type system to evolve autonomously based on extraction results.

### 4.1 Signal Collection

`SignalCollector.CollectAll()` runs three SQL-only collectors (no LLM cost):

| Signal | What it detects | SQL logic |
|--------|----------------|-----------|
| `custom_frequency` | Fact types not in the configured taxonomy | `GROUP BY fact_type`, filter against known types |
| `type_unused` | Configured types with zero facts | Cross-reference config types against `DISTINCT fact_type` |
| `low_confidence` | Types where average confidence < 0.5 | `AVG(confidence)` grouped by `fact_type` |

Signals are written to `taxonomy_signals` with type, category, count, and details.

### 4.2 Review Threshold

`ShouldReview()` checks whether enough actionable signals (custom_frequency + low_confidence) have accumulated:

```
threshold = max(MinAbsolute, total_facts * PctThreshold)
trigger when: SUM(signal.count) >= threshold
```

### 4.3 LLM Review

`Evolver.ReviewTaxonomy()`:

1. Collect unresolved actionable signals.
2. Render the taxonomy review prompt template with current types and signal text.
3. Send to LLM. Response is a JSON array of proposals, each with: action (add/remove/merge/rename), type_category, type_name, definition, rationale.
4. Store proposals in `taxonomy_proposals` with status `proposed`.

### 4.4 Validation

`Evolver.ValidateProposals()` runs automated validation on proposed changes:

| Action | Validation | Result |
|--------|-----------|--------|
| `add` | custom_frequency signal exists with count >= 1 | `validated` |
| `remove` | Type has 0 facts AND total facts >= 100 | `validated` |
| `merge` | Embedding centroid pre-filter (cosine >= 0.7 between source and target type centroids). Below threshold = rejected. Above threshold = LLM confirmation with sample facts from both types. | `validated` or `rejected` |
| `rename` | LLM confirmation with sample facts of the type. LLM evaluates whether the new name better describes the existing facts. | `validated` or `rejected` |

Validated proposals can be applied (status -> `applied`). Rejected proposals are marked with a reason.

### 4.5 Runtime Merge

`EffectiveTypesWithProposals()` merges the base config taxonomy with all applied proposals:

- `add` proposals append new `TypeDef` entries.
- `remove` proposals filter out matching entries.
- `merge` proposals remove the source type (the merged-from type disappears from the taxonomy).
- `rename` proposals rename the type in-place (parses `rename_to` from the proposal's definition JSON).

The merged result is the runtime taxonomy used by extraction prompts. This closes the loop: signals from extraction -> proposals -> applied changes -> updated prompts -> different extraction behavior.

---

## 5. Storage

### 5.1 Schema

All tables use TEXT primary keys (ULID). Timestamps are RFC 3339 strings. JSON is stored as TEXT.

**Core tables (migration 001):**

```sql
entities (id, name, entity_type, aliases, created_at)
facts (id, source_file, source_lines, source_ts, fact_type, subject,
       content, confidence, valid_from, valid_until, superseded_by, created_at, embedding)
relationships (id, from_entity, to_entity, relation_type, properties, source_fact, created_at)
consolidations (id, source_fact_ids, summary, insight, importance, created_at)
fact_connections (id, fact_a, fact_b, connection_type, strength, consolidation_id, created_at)
schema_migrations (version, applied_at)
```

**Taxonomy evolution (migration 002):**

```sql
taxonomy_signals (id, signal_type, type_category, type_name, count, details, created_at, resolved_by)
taxonomy_proposals (id, action, type_category, type_name, definition, rationale, status,
                    shadow_results, signal_ids, created_at, resolved_at)
extraction_log (id, provider_name, model, input_length, tokens_used, duration_ms, success,
                facts_count, entities_count, relationships_count, error_type, error_message, created_at)
```

**Batch tracking (migration 003):**

```sql
ingested_files (path, content_hash, chunks, facts_count, processed_at)
```

**Embedding metadata + FTS (migration 004):**

```sql
ALTER TABLE facts ADD COLUMN embedding_model TEXT;
CREATE VIRTUAL TABLE facts_fts USING fts5(content, fact_id UNINDEXED);
```

**Transcripts (migration 005):**

```sql
transcripts (id, file_path TEXT NOT NULL UNIQUE, date, participants, topic, chunk_count, created_at)
transcript_chunks (id, transcript_id, line_start, line_end, content_hash, embedding_model)
```

**FTS5 over transcript chunks (migration 006):**

```sql
CREATE VIRTUAL TABLE transcript_chunks_fts USING fts5(content, chunk_id UNINDEXED);
```

**Supersede reason (migration 007):**

```sql
ALTER TABLE facts ADD COLUMN supersede_reason TEXT;
```

Replaces the `'batch-replaced'` sentinel in `superseded_by` with a proper column, restoring FK integrity.

**Quality signals + fact citations (migration 008):**

```sql
quality_signals (id, signal_type, category, value, details, created_at)
fact_citations (fact_id, query_id, cited_at)
ALTER TABLE extraction_log ADD COLUMN entity_collisions INTEGER NOT NULL DEFAULT 0;
ALTER TABLE extraction_log ADD COLUMN entity_creations INTEGER NOT NULL DEFAULT 0;
```

**Query log (migration 009):**

```sql
query_log (id, endpoint, question, total_latency_ms, retrieval_latency_ms, synthesis_latency_ms,
           facts_found, facts_by_vector, facts_by_text, facts_by_graph, chunks_by_vector,
           chunks_by_text, citations_count, embedder_available, error, created_at)
```

**Eval runs (migration 010a):**

```sql
eval_runs (id, eval_type, score, score2, report, prompt_hash, examples_count, created_at)
```

**Provider models + health (migration 010b):**

```sql
provider_models (provider_name, model_id, context_window, available, last_checked)
    PRIMARY KEY (provider_name, model_id)
provider_health (provider_name, task_type, configured_model, active_model, status,
                 last_error, last_checked, switched_at)
    PRIMARY KEY (provider_name, task_type)
```

Note: migrations 010a and 010b share the same number prefix due to a numbering collision between S045 and S048. Both are applied.

**Provider ops + retry queue (migration 011):**

```sql
provider_ops (provider_name, status, retry_count, max_retries, last_error,
              error_type, next_check_at, last_success, created_at, updated_at)
    PRIMARY KEY (provider_name)
retry_queue (id, task_type, payload, created_at, retry_count, last_error, status)
```

**Eval runs baseline + git commit (migration 012):**

```sql
ALTER TABLE eval_runs ADD COLUMN git_commit TEXT;
ALTER TABLE eval_runs ADD COLUMN is_baseline INTEGER NOT NULL DEFAULT 0;
```

**Hot-Cool Pipeline tables (migration 013, D35) -- not yet implemented:**

```sql
hot_messages (id, speaker, content, timestamp, platform, platform_session_id,
              linker_ref, has_embedding, created_at)
    -- FTS5 virtual table over content
    -- vec0 table created programmatically (D23 pattern)

cooldown_messages (id, speaker, content, timestamp, platform, platform_session_id,
                   linker_ref, has_embedding, cluster_id, transcript_file,
                   transcript_line, processed_at, moved_from_hot, created_at)
    -- FTS5 virtual table over content
    -- vec0 table created programmatically
```

Two separate tables for performance: hot = realtime query (agent waits), cool = background processing. Message ID (ULID) generated once in hot, preserved in cooldown. Selective embedding: has_embedding=1 only for messages >= 50 chars.

### 5.2 Vector Tables

sqlite-vec virtual tables are created programmatically (not in SQL migrations) because dimensions come from config at runtime.

```sql
CREATE VIRTUAL TABLE facts_vec USING vec0(
    fact_id TEXT PRIMARY KEY,
    embedding float[<dims>] distance_metric=cosine
);
```

`EnsureVecTable(dims)` checks if the table exists with the correct dimensions. If dimensions changed, it drops and recreates the table.

A separate `chunks_vec` table stores transcript chunk embeddings with the same structure.

### 5.3 FTS5

`facts_fts` is a full-text search index over fact content. BM25 scoring is used for ranking. The query layer sanitizes input by stripping FTS5 special characters before searching.

### 5.4 Graph Traversal

Entity subgraphs are computed with a recursive CTE:

```sql
WITH RECURSIVE reachable(id, depth) AS (
    SELECT ?, 0
    UNION
    SELECT CASE
        WHEN r.from_entity = reachable.id THEN r.to_entity
        ELSE r.from_entity
    END, reachable.depth + 1
    FROM relationships r
    JOIN reachable ON r.from_entity = reachable.id OR r.to_entity = reachable.id
    WHERE reachable.depth < ?
)
SELECT DISTINCT e.* FROM entities e JOIN reachable ON e.id = reachable.id
```

This traverses the graph bidirectionally up to a configurable depth (max 5). `FindPath` uses a similar CTE to find the shortest path between two entities.

---

## 6. Provider Chain

### 6.1 Provider Types

Provider type is determined by the `name` field in config:

| Name | API | Auth |
|------|-----|------|
| `anthropic` | Anthropic Messages API (`/v1/messages`) | `x-api-key` header |
| `ollama` | Ollama native API (`/api/chat`) | None |
| anything else | OpenAI-compatible (`/v1/chat/completions`) | `Bearer` token |

The OpenAI-compatible provider covers OpenAI, Google Gemini, OpenRouter, Voyage AI, Groq, Together, Fireworks, DeepInfra, vLLM, llama.cpp, and LM Studio. OpenRouter sends additional `HTTP-Referer` and `X-Title` headers for app identification when `base_url` contains `openrouter.ai`.

### 6.2 Fallback

`Chain.Send()` tries providers in priority order (lower number = tried first). The first successful response is returned. If all fail, the last error is returned.

Credentials are resolved per provider: `token_env` (OAuth/Bearer token) is tried first; if empty, falls back to `api_key_env` (API key). **Exception:** Embedders (`newEmbedder`) only check `api_key_env`, skipping `token_env` entirely.

### 6.3 Task-Specific Chains

Each task type has its own provider chain configured independently:

| Chain | Config key | Used by |
|-------|-----------|---------|
| Extraction | `[[providers.extraction]]` | `Extractor` |
| Consolidation | `[[providers.consolidation]]` | `Consolidator` |
| Query | `[[providers.query]]` | `Querier` |
| Embedding | `[[providers.embedding]]` | `EmbedderChain` |

If no query providers are configured, the extraction chain is used as fallback.

### 6.4 Model Discovery

`internal/provider.ModelLister` queries provider APIs for available models. Three implementations:

| Implementation | API endpoint | Auth |
|----------------|-------------|------|
| `OpenAIModelLister` | `GET {base_url}/models` | Bearer token |
| `AnthropicModelLister` | `GET {base_url}/v1/models` | `x-api-key` or Bearer (token_env) |
| `OllamaModelLister` | `GET {base_url}/api/tags` | None |

The OpenAI lister also handles Google's response format (array under `models` key with `inputTokenLimit` instead of `context_window`).

`NewModelListersFromConfig(taskConfigs)` builds listers from all provider configs across task types. Deduplication by `(provider_name, base_url)` ensures the same provider appearing in multiple task chains produces only one lister.

### 6.5 Health Checking

`internal/provider.HealthChecker` compares configured models against available models.

`CheckAll(ctx)`:

1. For each `ModelLister`, call `ListModels()`. On failure, mark all tasks for that provider as `unavailable` in `provider_health`.
2. For each returned model, upsert a row in `provider_models` (name, model ID, context window, available=true, last_checked).
3. For each configured (provider, task, model) triple, check whether the configured model appears in the available set:
   - Found: status = `ok`, active_model = configured_model
   - Not found: status = `degraded`, active_model = result of `findSubstitute()`, last_error describes the substitution
4. Write results to `provider_health`.

**Model substitution** (`findSubstitute`): when the configured model is missing, tries prefix matching with progressively shorter prefixes. For example, `gpt-5-nano` missing tries `gpt-5-*`, then `gpt-*`. If no prefix match, picks any available model from that provider. If no models at all, returns empty.

### 6.6 Provider Models Table

Populated by health checking. One row per (provider, model) pair.

```sql
provider_models (provider_name, model_id, context_window, available, last_checked)
    PRIMARY KEY (provider_name, model_id)
```

### 6.7 Provider Health Table

One row per (provider, task) pair. Tracks whether the configured model is available and what the active model is.

```sql
provider_health (provider_name, task_type, configured_model, active_model, status,
                 last_error, last_checked, switched_at)
    PRIMARY KEY (provider_name, task_type)
```

Status values: `ok` (configured model available), `degraded` (model not found, substitute may be active), `unavailable` (provider unreachable).

### 6.8 Error Classification

`internal/provider/errors.go` classifies errors from provider Send() calls into four categories:

| Category | Triggers | DB error_type values |
|----------|----------|---------------------|
| `ErrorTransient` | timeout, connection refused, HTTP 429/502/503/529 | `timeout`, `connection_refused`, `http_502`, `http_503`, `http_529` |
| `ErrorAuth` | HTTP 401/403, invalid API key | `http_401`, `http_403`, `invalid_key` |
| `ErrorModelNotFound` | "model not found" in error message | `model_not_found` |
| `ErrorOther` | anything else | `unknown` |

Classification parses HTTP status codes from error strings (providers wrap status in messages like `"status 503: ..."`), detects timeout/connection errors from the `net` package, and unwraps error chains.

### 6.9 Provider Ops Table

Tracks operational status per provider. One row per provider name.

```sql
provider_ops (provider_name, status, retry_count, max_retries, last_error,
              error_type, next_check_at, last_success, created_at, updated_at)
    PRIMARY KEY (provider_name)
```

Status values: `ok`, `transient_error`, `auth_error`, `exhausted` (max retries reached).

### 6.10 Retry Queue

Stores failed requests when all providers are down, for later processing when a provider recovers.

```sql
retry_queue (id, task_type, payload, created_at, retry_count, last_error, status)
    PRIMARY KEY (id)
```

Status values: `pending`, `processing`, `completed`, `expired`. Entries older than `queue_ttl_hours` (default 24) are expired.

### 6.11 ResolvedProvider Wrapper

`internal/provider.ResolvedProvider` wraps a `Provider` with health-aware logic. `ResolvedEmbedder` does the same for `Embedder`.

**Send() flow:**

1. Read `provider_ops` for this provider.
2. If status is `auth_error`, return error immediately (no call to inner provider).
3. If status is `transient_error` and `next_check_at` is in the future, return error (skip until retry window).
4. Call `inner.Send()`.
5. On success: reset `provider_ops` to `ok`, record `last_success`.
6. On failure: classify error, update `provider_ops` (increment retry_count, set next_check_at = now + retry_interval_hours).
7. If status becomes `exhausted` or `auth_error`: write a context-type fact to Imprint DB with 24h TTL, so any agent querying Imprint sees the provider issue.

**Catalog refresh triggers:**

- At server start (`runServe` calls `runHealthCheckAtStartup`)
- Every `catalog_refresh_days` (default 3) via a background goroutine
- On `ErrorModelNotFound` (planned for BVP-306)

**Configuration** (`[health]` section in config.toml):

| Key | Default | Purpose |
|-----|---------|---------|
| `catalog_refresh_days` | 3 | How often to re-check provider model catalogs |
| `max_retries` | 5 | Retries before marking provider as `exhausted` |
| `retry_interval_hours` | 1 | Hours between retry attempts for transient errors |
| `queue_ttl_hours` | 24 | Hours before pending retry queue entries expire |

---

## 7. Configuration

TOML config with environment variables for secrets. See `config.toml.example` for the full reference.

### 7.1 Sections

| Section | Purpose |
|---------|---------|
| `[db]` | Database path |
| `[api]` | HTTP server host and port |
| `[consolidation]` | Interval, min_facts threshold, max_group_size, dedup threshold, cluster_similarity_threshold (default 0.40) |
| `[embedding]` | Dimensions, distance metric |
| `[watcher]` | Watch path, poll interval, debounce, consolidate-after-ingest flag |
| `[prompts]` | Paths to extraction, consolidation, and query prompt templates |
| `[[providers.*]]` | Provider chains for extraction, consolidation, query, embedding |
| `[[types.*]]` | Type taxonomy: fact_types, entity_types, relation_types, connection_types |
| `[context]` | Context builder settings: recent_hours, max_facts, include_preferences |
| `[quality]` | Quality signal collection + Karpathy loop: enabled, thresholds, prompt paths, golden_dir |
| `[health]` | Provider auto-healing: catalog_refresh_days, max_retries, retry_interval_hours, queue_ttl_hours |
| `[openclaw]` | OpenClaw memory backend mode (off/parallel/replace) |

### 7.2 Defaults

- `EffectiveEmbeddingDims()`: returns configured dimensions, or 1536 if not set.
- `EffectiveAPIAddr()`: returns configured host:port, or `127.0.0.1:8080` if not set.
- `EffectiveTypes()`: returns configured types, falling back to built-in defaults (12 fact, 9 entity, 9 relation, 6 connection types) for any empty category.
- `EffectiveGCAfterDays()`: returns configured gc_after_days, or 30 if not set.
- `EffectiveContextTTLDays()`: returns configured context_ttl_days, or 7 if not set.

---

## 8. Garbage Collection

The `gc` command deletes expired facts where `valid_until < now - gc_after_days`. This primarily targets context-type facts that were auto-expired by the context TTL feature (section 1.3, step 3).

The cutoff is configurable via `[consolidation] gc_after_days` (default: 30). Facts are hard-deleted from the `facts` table and the `facts_fts` index.

---

## 9. Export

The `export` command dumps the entire knowledge base for backup or analysis.

| Format | Output | Contents |
|--------|--------|----------|
| `json` | Single file (stdout or `--output=path`) | facts, entities, relationships, consolidations, fact_connections, stats |
| `csv` | Directory (default: `export/`) | One CSV per table: facts.csv, entities.csv, relationships.csv, consolidations.csv, fact_connections.csv |

---

## 10. Eval Harness

`internal/eval` scores extraction quality against a golden dataset. The `eval` CLI subcommand runs extraction on each golden example, compares the result to the expected output, and reports metrics.

### 10.1 Golden Dataset

`imprint eval generate` writes the built-in golden dataset to disk (default: `testdata/golden/`). The dataset contains 38 examples covering all 12 fact types, 9 entity types, 9 relationship types, and common noise patterns (cron output, stack traces, CI logs, meta-conversation, tool output).

Paired files in a directory: `001-foo.txt` (input) + `001-foo.json` (expected output). The JSON follows the same schema as `ExtractionResult` (facts, entities, relationships). Examples with all-empty arrays are noise examples. Users can add their own examples to the same directory for domain-specific evaluation.

### 10.2 Matching

| Category | Strategy |
|----------|----------|
| Facts | Composite: `fact_type` exact, `subject` normalized fuzzy, `content` Jaccard similarity (threshold 0.5) |
| Entities | Alias-aware, case-insensitive name comparison |
| Relationships | Entity names via entity matcher + `relation_type` exact |

### 10.3 Metrics

| Metric | Description |
|--------|-------------|
| Fact F1 | CaRB-style asymmetric P/R/F1 (greedy precision, max-match recall) |
| Entity F1 | Set-based P/R/F1 with alias-aware matching |
| Relationship F1 | Set-based P/R/F1 with entity name resolution |
| NRR | Noise Rejection Rate: fraction of noise inputs producing zero extractions |
| ECE | Expected Calibration Error (10-bin): measures confidence score accuracy |
| Brier | Mean squared error of confidence vs correctness |
| Composite | `0.4*fact_F1 + 0.2*entity_F1 + 0.2*rel_F1 + 0.1*(1-ECE) + 0.1*NRR` |

### 10.4 Output

Table (default) or JSON (`--format=json`). The composite score is a single number (0-1) suitable as an optimization target for automated prompt tuning.

### 10.5 Retrieval Eval

`imprint eval-retrieval` evaluates the retrieval pipeline (without LLM synthesis). It seeds a temporary database with a built-in golden dataset (32 facts, 33 entities, 17 relationships from a consistent test universe), then runs 21 golden questions across 5 categories.

**Categories:**

| Category | What it tests | Count |
|----------|--------------|-------|
| direct_lookup | Single fact, keyword match | 9 |
| graph_traversal | Following entity relationships | 5 |
| temporal | Time-sensitive questions | 2 |
| multi_hop | Combining facts from multiple sources | 3 |
| noise | Questions with no answer in KB | 2 |

**Metrics:**

| Metric | Description |
|--------|-------------|
| Recall@10 | Fraction of expected facts in top 10 retrieved |
| MRR | Mean Reciprocal Rank: 1/rank of first relevant fact |
| Per-layer contribution | Which layers (vector, FTS5, graph) found expected facts |
| Noise rejection | Fraction of noise questions returning zero results |

**Graceful degradation:** Run with `--no-embedder` to measure quality without vector search. The delta between full and text+graph-only modes quantifies embedder importance.

The `Querier.Retrieve()` method runs the full retrieval pipeline (embed, parallel search, RRF merge) and returns ranked facts with per-fact layer provenance (`FromVector`, `FromText`, `FromGraph`) without calling the LLM for synthesis.

---

## 11. Quality Signals and Prompt Optimization

### 11.1 Quality Signal Collection

`internal/quality.Collector` computes quality metrics from production data (SQL queries over extraction_log, facts, entities). Signals are stored in the `quality_signals` table.

| Signal | What it measures |
|--------|-----------------|
| supersede_rate | Fraction of facts superseded per fact type |
| citation_rate | Fraction of facts cited in queries per fact type |
| volume_anomaly | Unusual spikes or drops in extraction volume |
| entity_collision_rate | Fraction of entity creations that were dedup collisions |
| confidence_calibration | Calibration of extraction confidence scores vs actual quality |
| confidence_citation_calibration | Calibration of confidence scores vs citation frequency |

### 11.2 Karpathy Loop (Prompt Optimization)

`internal/quality.LoopOptimizer` automatically improves the extraction prompt:

1. **Trigger:** quality signals exceed thresholds after ingest
2. **Mutate:** send current prompt + signal summary to LLM via mutation prompt
3. **Eval:** run candidate prompt against the extraction golden set
4. **Gate:** if composite score improves, write to `prompts/extraction-prompt-optimized.md`; otherwise discard

Rate-limited to 1 attempt per hour. Three consecutive failures pause for 24 hours. The original prompt is never modified.

### 11.3 Query Log

`query_log` table records per-query metrics: total/retrieval/synthesis latency, facts found per layer (vector, text, graph), citations count, embedder availability. Aggregated in `GET /status` and `imprint status`.
