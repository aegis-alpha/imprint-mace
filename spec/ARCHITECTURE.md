# Imprint: Architecture

Imprint is a persistent memory system for AI agents. It extracts structured knowledge (facts, entities, relationships) from conversation transcripts, stores them in a local SQLite knowledge graph, finds connections between facts, and evolves its own type taxonomy over time.

The system is library-first: the core is a set of Go packages (`internal/imprint`, `internal/extraction`, `internal/query`, etc.). Transport wrappers (HTTP API, MCP server, CLI) are thin layers that delegate to the same `Engine.Ingest()` and `Querier.Query()` calls.

Storage is two-layer: transcript files on disk are the source of truth; the SQLite database is a derived index with back-references (file path + line range) to the original text.

---

## 1. Ingest Pipeline

All text enters the system through `Engine.Ingest()`. There are three paths to reach it.

### 1.1 Batch Ingest

`internal/ingest.BatchAdapter` processes a directory of `.txt` and `.md` files.

1. **Discover files.** Walk the directory, select `.txt` and `.md` files.
2. **Parse frontmatter.** If the file starts with `---\n`, extract YAML frontmatter (source, session, date, participants, topic). See `spec/TRANSCRIPT-FORMAT.md`.
3. **Dedup.** Compute SHA-256 of the file content. Compare against `ingested_files` table. If the hash matches, skip.
4. **Register transcript.** Create or update a `transcripts` row with metadata from frontmatter. On re-ingest, delete old `transcript_chunks` first.
5. **Chunk.** If the file exceeds 8000 bytes, split into ~4000-byte chunks at newline boundaries with 200-byte overlap. Each chunk carries its 1-based line range (start, end) in the original file.
6. **Register chunks.** Write a `transcript_chunks` row per chunk (transcript ID, line range, content hash).
7. **Ingest each chunk.** Call `Engine.Ingest(text, relPath, WithLineOffset(start, end))` for each chunk.
8. **Record.** Upsert `ingested_files` with path, hash, chunk count, fact count.

### 1.2 Realtime Ingest

Text arrives via the HTTP API (`POST /ingest`) or MCP tool (`imprint_ingest`). No chunking, no dedup, no transcript registration -- the text goes directly to `Engine.Ingest()`.

**Performance note:** Each `Engine.Ingest()` call triggers one LLM extraction (2-10 seconds depending on text length and provider). For bulk operations, callers should process items sequentially with retry and backoff, not in parallel. Concurrent LLM calls risk rate limits and timeouts.

**OpenClaw integration:** The `imprint-ingest` hook listens for `message:preprocessed` events and sends the enriched message body to `POST /ingest` automatically. This is a deterministic integration path -- every message is ingested without model intervention. The `imprint-query` hook calls `GET /query` on the same event and injects the answer as agent context.

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
- The LLM returns JSON with arrays of facts, entities, and relationships. Each fact has a type, subject, content, confidence, and optional temporal validity.
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

Four layers run concurrently:

| Layer | Source | Method | Limit |
|-------|--------|--------|-------|
| Vector facts | `facts_vec` | KNN cosine search on question embedding | 20 |
| Vector chunks | `chunks_vec` | KNN cosine search on question embedding | 10 |
| FTS5 | `facts_fts` | BM25 keyword search on sanitized question | 10 |
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

The LLM returns JSON with an answer, citations (fact IDs and/or consolidation IDs), a confidence score, and optional notes about contradictions or gaps.

---

## 3. Consolidation

`internal/consolidation` groups related facts, discovers connections between them, and generates higher-order insights.

### 3.1 Consolidator

`Consolidator.Consolidate(ctx, limit)`:

1. Fetch unconsolidated facts from the DB. A fact is unconsolidated if its ID does not appear in any consolidation's `source_fact_ids` array (checked via `json_each`).
2. If fewer than 2 facts, skip.
3. Format facts as a list (`[ID] (type) subject: content`) and send to the LLM with the consolidation prompt template.
4. The LLM returns JSON: connections (fact_a, fact_b, connection_type, strength), a summary, an insight, and an importance score.
5. Store a `Consolidation` record (source fact IDs, summary, insight, importance).
6. Store `FactConnection` records for each connection.

Connection types: `supports`, `contradicts`, `elaborates`, `caused_by`, `supersedes`, `precedes`.

### 3.2 Scheduler

`Scheduler` runs consolidation in a background loop:

1. Tick at a configurable interval (default: 60 minutes).
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

`Evolver.ValidateProposals()` runs post-hoc SQL validation on proposed changes:

| Action | Validation | Result |
|--------|-----------|--------|
| `add` | custom_frequency signal exists with count >= 1 | `validated` |
| `remove` | Type has 0 facts AND total facts >= 100 | `validated` |
| `merge` | Requires manual review | stays `proposed` |
| `rename` | Requires manual review | stays `proposed` |

Validated proposals can be applied (status -> `applied`). Rejected proposals are marked with a reason.

### 4.5 Runtime Merge

`EffectiveTypesWithProposals()` merges the base config taxonomy with all applied proposals:

- `add` proposals append new `TypeDef` entries.
- `remove` proposals filter out matching entries.

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
transcripts (id, file_path, date, participants, topic, chunk_count, created_at)
transcript_chunks (id, transcript_id, line_start, line_end, content_hash, embedding_model)
```

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

Credentials are resolved per provider: `token_env` (OAuth/Bearer token) is tried first; if empty, falls back to `api_key_env` (API key).

### 6.3 Task-Specific Chains

Each task type has its own provider chain configured independently:

| Chain | Config key | Used by |
|-------|-----------|---------|
| Extraction | `[[providers.extraction]]` | `Extractor` |
| Consolidation | `[[providers.consolidation]]` | `Consolidator` |
| Query | `[[providers.query]]` | `Querier` |
| Embedding | `[[providers.embedding]]` | `EmbedderChain` |

If no query providers are configured, the extraction chain is used as fallback.

---

## 7. Configuration

TOML config with environment variables for secrets. See `config.toml.example` for the full reference.

### 7.1 Sections

| Section | Purpose |
|---------|---------|
| `[db]` | Database path |
| `[api]` | HTTP server host and port |
| `[consolidation]` | Interval, min_facts threshold, max_group_size, dedup threshold |
| `[embedding]` | Dimensions, distance metric |
| `[watcher]` | Watch path, poll interval, debounce, consolidate-after-ingest flag |
| `[prompts]` | Paths to extraction, consolidation, and query prompt templates |
| `[[providers.*]]` | Provider chains for extraction, consolidation, query, embedding |
| `[[types.*]]` | Type taxonomy: fact_types, entity_types, relation_types, connection_types |

### 7.2 Defaults

- `EffectiveEmbeddingDims()`: returns configured dimensions, or 1536 if not set.
- `EffectiveAPIAddr()`: returns configured host:port, or `127.0.0.1:8080` if not set.
- `EffectiveTypes()`: returns configured types, falling back to built-in defaults (12 fact, 9 entity, 9 relation, 6 connection types) for any empty category.
- `EffectiveGCAfterDays()`: returns configured gc_after_days, or 30 if not set.
- `EffectiveContextTTLDays()`: returns configured context_ttl_days, or 0 (disabled) if not set.

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

`imprint eval generate` writes the built-in golden dataset to disk (default: `testdata/golden/`). The dataset contains 42 examples (30 positive, 12 noise) covering all 12 fact types, 9 entity types, 9 relationship types, and common noise patterns (cron output, stack traces, CI logs, meta-conversation, tool output).

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
