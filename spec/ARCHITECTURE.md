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

Text arrives via the HTTP API (`POST /ingest`) or MCP tool (`imprint_ingest`). The behavior depends on whether hot phase is enabled in config.

**When hot phase disabled (default):** Text goes directly to `Engine.Ingest()` (section 1.3). No chunking, no dedup, no transcript registration. Each call triggers LLM extraction (2-10 seconds depending on text length and provider).

**When hot phase enabled:** Text is stored in `hot_messages` table (section 1.2b). Zero LLM cost, instant search, no extraction. This is the hot path.

**OpenClaw integration:** Two deterministic integration paths:

1. **Ingest** (internal hook): The `imprint-ingest` hook listens for `message:preprocessed` events and sends the enriched message body to `POST /ingest` automatically. Every message is ingested without model intervention. This is a fire-and-forget hook -- the result is not needed by the gateway.
2. **Context delivery** (plugin): The `imprint-context` plugin registers a `before_prompt_build` hook and calls `GET /context?hint=<message>`. The result is returned as `{ prependContext }` which OpenClaw injects into the agent's prompt. This is the only way to deterministically inject context -- internal hooks (`message:preprocessed`) are fire-and-forget and cannot modify the prompt.

**Service discovery:** The server writes its actual listen address to `~/.imprint/serve.json` on startup. If the configured port is busy, it tries the next available port (up to +20). Hooks and other clients read this file to discover the server automatically. The file is removed on graceful shutdown.

### 1.2b Hot Ingest

When `[hot] enabled = true` in config, `POST /ingest` and `imprint_ingest` store raw messages in the `hot_messages` table instead of calling `Engine.Ingest()`. This is the hot path: zero LLM cost, instant search, no extraction.

**Flow:**

1. **Store raw.** Message stored with speaker, content, timestamp, platform metadata, and linker_ref (connects to reply target).
2. **Selective embedding.** Messages with `len(content) >= 50` get an embedding (E021 result: 22% cost saving, zero recall loss). Shorter messages get `has_embedding = 0`.
3. **FTS5 indexing.** Message content indexed in `hot_messages_fts` for keyword search.
4. **Vector indexing.** If embedding succeeds, stored in both: (a) `hot_messages.embedding` BLOB (source of truth), (b) USearch index with key `"hot:"+messageID` (cache for fast search).
5. **Queryable immediately.** Message is searchable via FTS5 and vector within milliseconds. No extraction latency.

**Override:** The `mode=extract` parameter forces LLM extraction even when hot is enabled. This is for manual/testing use. Normal callers (hooks, agents) never set it.

**Response:** Returns `{"id": "<ulid>", "has_embedding": true, "hot": true}` instead of the extraction result. The `hot: true` field helps callers identify which path was taken.

**TTL:** Messages move from hot to cooldown after configurable TTL (default 60 minutes). A background goroutine in `runServe` calls `store.MoveHotToCooldown()` every `tick_seconds` (default 60). Messages remain searchable in cooldown -- they never disappear from query results.

**When hot is disabled:** `POST /ingest` behaves identically to the current version (calls `Engine.Ingest()`, returns extraction result). The hot path is completely inactive when `[hot] enabled = false` (default).

### 1.3 Engine.Ingest() (Cold Path)

`internal/imprint.Engine` is the canonical path for extraction-based ingestion. When hot phase is enabled, this is the cold path -- it is bypassed for per-message realtime ingest, but still used for batch transcript processing.

1. **Extract.** Send text to the LLM via `Extractor.Extract()`. Returns facts, entities, and relationships.
2. **Attach source.** If `WithLineOffset` was set, stamp every fact with the source file path and line range.
3. **Context TTL.** If `contextTTLDays > 0`, auto-set `valid_until` on context-type facts that don't already have an expiry. This makes situational facts (e.g. "node-2 is offline") expire automatically.
4. **Store entities.** Write each entity to the DB. Failures are logged and skipped (non-fatal). When an entity name already exists, new aliases are unioned into the stored row (case-insensitive); type mismatches log a warning and keep the existing type.
5. **Embed + smart dedup.** If an `Embedder` is configured, generate a vector embedding for each fact. If `dedupThreshold > 0`, search for the nearest **active** fact (not already superseded). Skip only when similarity is above the threshold **and** word-level Jaccard similarity of `content` is high, **unless** opt-in dedup merge (BVP-320) is enabled: then one LLM call classifies the pair as skip, supersede, or merge. Skip discards the new fact. Supersede stores the extracted fact normally, then applies `SupersedeFactByContradiction` to retire the similar old row (`dedup:supersede:` reason prefix, same `valid_until` behavior as contradiction). Merge builds a new fact from LLM `merged_content` (same `fact_type` and `subject` as the old fact, source from the current ingest), stores and embeds it, retires the old row (`dedup:merge:` prefix), and does **not** store the pre-merge extracted fact. Classifier errors fall back to skip. When merge is disabled, behavior matches the original binary skip.
6. **Store facts.** Write each surviving fact and its embedding. Candidate neighbors for contradiction review are collected after the new row is indexed: vector search (top few hits above a score floor), excluding already-superseded rows, same-batch IDs, and pairs whose subjects do not overlap enough (word Jaccard).
7. **Contradiction batch (optional, BVP-316).** When enabled in config and a provider chain plus prompt are wired via `Engine` options, one LLM call reviews all `(new fact, candidates)` groups. The model returns which existing facts to soft-supersede. A code-level confidence guard blocks superseding a high-confidence stored fact with a low-confidence new fact unless the model output is still filtered by that rule (low new vs high old is rejected in Go). Applied supersedes set `superseded_by`, `supersede_reason` with a `contradiction:` prefix, and `valid_until` from the new fact's `created_at`. Runs are logged to `extraction_log` with `provider_name` `contradiction-check`. `FactsSuperseded` in the ingest result counts both immediate dedup supersedes (BVP-320) and contradiction batch supersedes.
8. **Resolve relationships.** The LLM returns relationships with entity names. The engine maps names to entity IDs (from the entities just stored) and writes the relationships. Unknown entity references are logged and skipped.

### 1.4 Extraction

`internal/extraction.Extractor` converts text to structured data.

- The system prompt is a Go template (`prompts/extraction-prompt.md`) rendered at startup with the current type taxonomy from config. When the taxonomy changes, the prompt updates on the next restart.
- The LLM returns JSON with arrays of facts, entities, and relationships. Each fact has a type, subject, content, confidence, and optional temporal validity. The response is parsed through a robust fallback chain: `stripMarkdownFences` removes `` ```json ... ``` `` wrapping, `extractJSON` locates the first `{...}` object in surrounding text, and a text refusal detector catches non-JSON responses (e.g. "I cannot extract..."). This handles providers that wrap JSON in markdown fences or prepend conversational text.
- The `Extractor` hydrates the raw LLM output: generates ULID IDs, sets timestamps, attaches source info.
- Every extraction call is logged to `extraction_log` (provider, model, tokens, duration, counts, errors) via `ExtractionLogger`.

### 1.5 Dual-Layer Memory (Superseded by Hot Phase)

**Note:** The D27 realtime path (`WithSessionID`, `SupersedeRealtimeBySession`) was removed in v0.5.0. Hot-Cool-Cold Pipeline (section 1.7) replaces it. Raw messages are stored in hot phase at zero LLM cost instead of extracted as temporary facts. No supersede needed -- messages never produce temporary facts.

**Legacy:** Existing `realtime:*` facts in the database (from pre-v0.5.0 deployments) remain as historical data. No new `realtime:*` facts are created.

### 1.7 Hot-Cool-Cold Pipeline (Phase 1: v0.5.0, Phase 2: v0.6.0)

**Status: Phase 1 + Phase 2 implemented (BVP-352, BVP-353, BVP-355, BVP-356, BVP-357, BVP-358).** Replaces per-message extraction (28% capture rate) with streaming storage + delayed topic-scoped extraction.

Core insight: the original problem framing ("how to extract from a stream") was wrong. The right framing: "how to make raw messages queryable immediately, and extract with full context later."

```
Message arrives (from hook/API)
    |
    v
[HOT PHASE] -- raw store, TTL ~1h, zero LLM cost [IMPLEMENTED v0.5.0]
    |  1. Store raw message (speaker, content, timestamp, platform metadata, linker_ref)
    |  2. Selective embed (messages >= 50 chars get embedding)
    |  3. FTS5 index (hot_messages_fts)
    |  4. USearch HNSW index (key "hot:"+messageID)
    |  5. Queryable immediately via FTS5 + vector search
    |
    | (TTL expires, message copied to cooldown via background goroutine)
    v
[COOL PHASE] -- topic clustering + extraction [IMPLEMENTED v0.6.0]
    |  1. Messages accumulate in cooldown_messages table
    |  2. Hybrid Union topic segmentation (TreeSeg + TT+Merge, D32)
    |     Per-session clustering via internal/segment/ package
    |  3. Cluster readiness: topic silence (8h) or cluster size (50 msgs)
    |  4. Ready cluster -> format as [speaker, date time]: content
    |  5. Extraction via Engine.Ingest() with source "cooldown-cluster/<ulid>"
    |  6. Transcript linking: batch ingest links cooldown rows, marks processed
    |
    v
[COLD PHASE] -- permanent store (existing infrastructure)
    Facts + Entities + Relationships
    Transcripts on disk
    Consolidation, Karpathy loop, retrieval
```

**Two tables (D35):** `hot_messages` (raw, fast for realtime query) and `cooldown_messages` (same base + cluster_id, transcript linking, processing status). Separate tables prevent SQLite single-writer contention between realtime query and background processing. Both tables have embedding BLOB columns and FTS5 virtual tables.

**TTL goroutine:** Background loop in `runServe` checks hot messages every `tick_seconds` (default 60), moves messages older than `ttl_minutes` (default 60) to cooldown in batches (default 100). Embeddings and FTS5 entries are copied. USearch keys change from `"hot:"` to `"cool:"` prefix.

**Message linker (BVP-354):** `linker_ref` field connects messages to their reply targets. Set via `internal/ingest.ApplyHotLinkerRef` before insert. Heuristic: links to the latest prior hot row from the other speaker in the same `platform_session_id`. Used to connect confirmations ("yes") to the specific proposal they confirm.

**Topic segmentation (D32, implemented v0.6.0):** Hybrid Union algorithm (TreeSeg + TT+Merge, boundary union) in `internal/segment/`. Determines WHEN to extract (cluster readiness) and WHICH messages form a block. Does NOT reorder messages -- extraction receives chronological order. Experiment showed topic-based reordering adds no extraction quality. Clustering is per-session (`platform_session_id`); cross-session clustering is a correctness bug. Graceful degradation when embeddings unavailable (Jaccard text similarity fallback).

**Extraction format (implemented v0.6.0):** `[speaker, YYYY-MM-DD HH:MM]: content`. Known facts NOT included in extraction prompt (paradoxical priming effect measured in E020).

**Triggers (D34, implemented v0.6.0):** Topic silence (8 hours, configurable via `[cool] silence_hours`) + cluster size (50 messages, configurable via `[cool] max_cluster_size`, safety valve). Session end rejected -- technical event, not semantic. Background goroutine in `internal/cooldown/` polls every `[cool] tick_seconds` (default 300).

**Transcript linking (implemented v0.6.0):** Batch ingest links cooldown rows to transcripts by `platform_session_id`. When batch produces facts, linked cooldown rows are marked as processed (prevents duplicate extraction). v0.6.0 limitation: linking only works when batch runs after cooldown rows exist.

**Query path (Phase 1):** Nine-layer search. Four new layers (hot vector, hot FTS5, cooldown vector, cooldown FTS5) added to existing five (fact vector, fact FTS5, chunk vector, chunk FTS5, graph). Hot and cooldown raw messages merged via RRF alongside structured facts. LLM synthesis includes "Fresh Messages" section for raw messages. Citations support `hot_message_id` field.

**Platforms without hooks:** Transcript files go directly to cold phase via file watcher (batch path). No hot phase. Same extraction pipeline.

### 1.6 Embedding

`internal/provider.EmbedderChain` generates vector embeddings for fact content, transcript chunks, and hot/cooldown messages.

- Embedders are tried in priority order (same fallback pattern as LLM providers).
- Each fact's embedding is stored in the `facts.embedding` BLOB column (source of truth) and indexed in USearch HNSW (cache for fast search, key prefix `"fact:"+factID`).
- The `embedding_model` column on the fact records which model produced the vector. This enables selective re-embedding when switching providers.
- Embedder types: OpenAI-compatible (covers OpenAI, Google, Voyage AI, etc.) and Ollama.

**Recommended production embedder:** EmbeddingGemma 300M (Google, 768d) served via llama-server. Benchmark on 4,390 real transcript chunks / 50 questions showed Recall@10 = 0.600, MRR = 0.586 -- within 4% of text-embedding-3-small (0.640 / 0.610) at zero cost. Matryoshka truncation to 512 dims produces no measurable quality loss. Backend latency via llama-server: p50 = 7.2ms per query (vs 65ms via Ollama -- 9x overhead from Ollama runtime, not model). This is a recommendation, not a requirement -- Imprint works with any OpenAI-compatible embedder.

**USearch vector index (D36, BVP-365):** Single `.vecindex` sidecar file next to the SQLite database. All vector tables (facts, chunks, hot messages, cooldown messages) share one HNSW index. Keys are prefixed to avoid collisions: `fact:<ulid>`, `chunk:<ulid>`, `hot:<ulid>`, `cool:<ulid>`. The uint64 key in USearch is FNV-64 hash of the prefixed string key. SQLite embedding BLOB columns are the source of truth; the cache file is expendable and rebuildable. Load time: ~75ms for 200K vectors. Search time: ~1.1ms at 200K scale (247x faster than sqlite-vec brute-force).

**Write-path invariant:** `internal/vecindex.USearchIndex` owns write safety centrally. Callers do not reserve capacity manually. Before every native `Add()`, the wrapper ensures the fresh or grown index has reserved capacity for the next write. This protects the first write on a fresh index and repeated growth writes behind one internal contract instead of caller discipline.

---

## 2. Query Pipeline

`internal/query.Querier` answers natural language questions using hybrid retrieval.

### 2.1 Retrieval Layers

Nine layers run concurrently (when hot phase and embeddings are enabled):

| Layer | Source | Method | Limit |
|-------|--------|--------|-------|
| Hot vector | USearch index (`"hot:"` prefix) | HNSW cosine search on question embedding | 10 |
| Hot FTS5 | `hot_messages_fts` | BM25 keyword search on sanitized question | 10 |
| Cooldown vector | USearch index (`"cool:"` prefix) | HNSW cosine search on question embedding | 10 |
| Cooldown FTS5 | `cooldown_messages_fts` | BM25 keyword search on sanitized question | 10 |
| Vector facts | USearch index (`"fact:"` prefix) | HNSW cosine search on question embedding | 20 |
| Vector chunks | USearch index (`"chunk:"` prefix) | HNSW cosine search on question embedding | 10 |
| FTS5 facts | `facts_fts` | BM25 keyword search on sanitized question | 10 |
| FTS5 chunks | `transcript_chunks_fts` | BM25 keyword search on sanitized question | 10 |
| Graph | `entities` + `relationships` | Word-match entity lookup, 1-hop traversal, collect source facts | unbounded |

**Graceful degradation:**
- Vector layers are skipped when no embedder is configured (falls back to FTS5 + graph)
- Hot/cooldown layers return empty when hot phase is disabled or tables are empty (zero overhead)
- When hot_messages and cooldown_messages are both empty, the pipeline behaves identically to the 5-layer cold-only version

### 2.2 Merge and Rank

Results from all layers are deduplicated and scored using Reciprocal Rank Fusion (k=60):

```
score(item) = sum over layers: 1 / (k + rank_in_layer)
```

Items that appear in multiple layers get higher scores.

**Unified type:** The `rankedItem` struct holds either a structured fact or a raw hot/cooldown message:

```go
type rankedItem struct {
    fact       *model.Fact
    hotMessage *model.HotMessage
    score      float64
    isHot      bool  // true for hot and cooldown messages
}
```

**Hot/cooldown prefixes:** Hot and cooldown messages use prefixed keys (`"hot:"+messageID`, `"cool:"+messageID`) in the score map to distinguish them from fact IDs and from each other. Both participate in RRF merge alongside structured facts.

**Set-union alternative:** `--merge-strategy=set-union` uses dense-first ordering: vector hits keep similarity order, then FTS5-only hits, then graph-only hits, then hot/cooldown hits. No RRF scoring. Programmatic: `query.WithMergeStrategy("set-union")`.

**Post-merge rerank:** The querier always has a reranker. Default is local cosine reranking (no network call): query embedding vs `Fact.Embedding`, with rows missing embeddings appended after reranked rows. When `[[providers.reranker]]` is configured, the first provider entry is used for HTTP reranking and cosine is used as fallback if client init fails. Provider rerank is API-compatible (`/v1/rerank`) for any OpenAI-style endpoint, with Cohere compatibility on `/v2/rerank`. `[rerank]` `top_n` limits how many leading items are considered. Reranking applies only when that prefix consists entirely of structured facts (not hot/cooldown raw rows); it reorders within the prefix and leaves the tail unchanged. On API failure or an invalid response, merge order is kept.

### 2.3 ReadContext Enrichment

For the top 10 ranked facts that have source line references, the system loads surrounding lines (3 lines before and after) from the original transcript file on disk. This provides the LLM with conversational context around each fact.

### 2.4 LLM Synthesis

The system builds a prompt with four sections (when hot/cooldown results exist):

1. **Question** -- the user's question.
2. **Fresh Messages** (optional) -- raw hot/cooldown messages sorted newest-first. Format: `[hot:<id>] (<speaker>, <date> <time>): <content>`. If message has `linker_ref`, shown as `(->ref)` after time. This section appears only when hot or cooldown layers returned results.
3. **Facts** -- ranked structured facts with ID, type, confidence, date, subject, content.
4. **Data Quality** -- computed metrics from the fact set (average confidence, superseded count, age spread, source diversity).
5. **Transcript Context** -- raw lines from disk for facts that have source references, plus chunk contexts from top chunk hits.

The LLM returns JSON with an answer, citations (fact IDs, consolidation IDs, and/or hot_message_ids), a confidence score, and optional notes about contradictions or gaps. The `rawQueryResponse` struct parses all four fields, but `parseResponse` only passes `answer` and `citations` into `QueryResult` -- the `confidence` and `notes` fields are parsed and discarded.

**Citation extension:** The `Citation` struct now includes `HotMessageID` field for citing raw messages: `{"hot_message_id": "hot:01JQ..."}`. The LLM uses this to reference fresh messages that contributed to the answer.

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

**Transcript chunk embedding BLOB (migration 013, BVP-365):**

```sql
ALTER TABLE transcript_chunks ADD COLUMN embedding BLOB;
```

Source of truth for chunk vectors for USearch rebuild; `chunks_vec` remains until a later migration removes it.

**Hot-Cool Pipeline (migration 014, BVP-352, D35, D36):**

```sql
hot_messages (id, speaker, content, timestamp, platform, platform_session_id,
              linker_ref, has_embedding, created_at, embedding BLOB)
cooldown_messages (id, speaker, content, timestamp, platform, platform_session_id,
                   linker_ref, has_embedding, cluster_id, transcript_file,
                   transcript_line, processed_at, moved_from_hot, created_at, embedding BLOB)
CREATE VIRTUAL TABLE hot_messages_fts USING fts5(content, message_id UNINDEXED);
CREATE VIRTUAL TABLE cooldown_messages_fts USING fts5(content, message_id UNINDEXED);
ALTER TABLE query_log ADD COLUMN hot_by_vector INTEGER NOT NULL DEFAULT 0;
ALTER TABLE query_log ADD COLUMN hot_by_text INTEGER NOT NULL DEFAULT 0;
ALTER TABLE query_log ADD COLUMN cooldown_by_vector INTEGER NOT NULL DEFAULT 0;
ALTER TABLE query_log ADD COLUMN cooldown_by_text INTEGER NOT NULL DEFAULT 0;
```

Two separate tables for performance: hot = realtime query (agent waits), cool = background processing. Message ID (ULID) generated once in hot, preserved in cooldown. Vector search for hot and cooldown uses the shared USearch cache (`*.vecindex`) with prefixed keys (`hot:`, `cool:`), not sqlite-vec vec0 tables.

Hot ingest linker (BVP-354): before insert, `internal/ingest.ApplyHotLinkerRef` can set `linker_ref` to the latest prior hot row from the other speaker (`user` / `assistant`) in the same `platform_session_id`. `Store` exposes `GetRecentHotMessages` and `GetLinkedMessages` for that heuristic and for walking `linker_ref` across hot and cooldown rows.

**Cool Pipeline foundation (migration 015, BVP-355/356/357):**

```sql
ALTER TABLE transcripts ADD COLUMN platform_session_id TEXT;
CREATE INDEX IF NOT EXISTS idx_transcripts_platform_session ON transcripts(platform_session_id);
```

Enables transcript linking: batch ingest persists `platform_session_id` from frontmatter `session` key, then `LinkCooldownToTranscript` matches cooldown rows by session.

### 5.2 Vector Index (USearch HNSW, D36)

Vector search uses USearch (C library + Go bindings) instead of sqlite-vec. Single `.vecindex` sidecar file next to the SQLite database.

**Architecture:**
- **Source of truth:** Embedding BLOB columns on tables (`facts.embedding`, `transcript_chunks.embedding`, `hot_messages.embedding`, `cooldown_messages.embedding`)
- **Cache for fast search:** USearch HNSW index in `<dbname>.vecindex` file
- **Key mapping:** String keys (`fact:<ulid>`, `chunk:<ulid>`, `hot:<ulid>`, `cool:<ulid>`) mapped to uint64 via FNV-64 hash
- **Quantization:** f16 (half-precision). Recall@1 = 99.1% vs 99.2% for f32. Memory: ~341 MB for 200K/768d vs ~585 MB for f32.
- **Thread safety:** Concurrent reads and writes are safe (`ChangeThreadsSearch(16)`, `ChangeThreadsAdd(4)` at startup)

**Lifecycle:**
1. **Startup:** Try `index.Load(cachePath)`. If success: ~75ms for 200K vectors. Done.
2. **Cache miss/corrupt:** Rebuild from SQLite embedding BLOBs. Slow (~11 min for 200K) but only on first run or recovery.
3. **Runtime:** `index.Add()` on each new embedding, `index.Search()` for queries.
4. **Shutdown:** `index.Save()` via atomic write (temp file + `os.Rename`). ~316ms for 200K.

**Capability model:** Runtime code carries one explicit vector backend capability state:
- `healthy` + `mode=read-write`: read available, write safe
- `read_only` + `mode=read-only`: read available, write blocked centrally
- `disabled` + `mode=disabled`: no vector backend attached
- `unhealthy`: self-test or attach failed; write-required startup aborts before serving traffic

The state is stored on `SQLiteStore` and exposed via `/status` as `vector_backend`.

**Startup self-test:** Commands and runtimes that require vector writes do not trust in-process native probing, because a failing `usearch_add` can segfault the process. Instead, Imprint runs a subprocess self-test against a scratch index:
- fresh first `Add()`
- repeated `Add()`
- save/load
- search after reload

If `[vector].mode = "read-write"` and that self-test fails, startup is fatal. Logs state:
- which backend failed
- which self-test failed
- that read/search may still be alive
- that startup is fatal because memory writes cannot be guaranteed

**Performance:** ~1.1ms search at 200K scale, 247x faster than sqlite-vec brute-force (~272ms estimated).

**Legacy sqlite-vec:** The `facts_vec` and `chunks_vec` virtual tables remain in the module graph for migration and optional backfill from older deployments. New code uses USearch exclusively.

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

### 6.2 Prism Mode (single endpoint)

When `[llm].base_url` is configured, Imprint switches to Prism mode:

- All LLM-related tasks (extraction, query, consolidation, embedding, rerank) use one synthetic provider config (`name="prism"`, `model="auto"`, `base_url=[llm].base_url`).
- Task routing is done by headers on every HTTP call:
  - `X-Prism-Task`: `extraction`, `query`, `consolidation`, `embedding`, or `rerank`
  - `X-Prism-App`: `imprint`
- In this mode, `[[providers.*]]` chains are ignored.
- Provider health checks and model catalog refresh are skipped (managed by Prism/proxy).

### 6.3 Task-Specific Chains

In direct mode (`[llm]` disabled), each task type has its own provider chain configured independently:

| Chain | Config key | Used by |
|-------|-----------|---------|
| Extraction | `[[providers.extraction]]` | `Extractor` |
| Consolidation | `[[providers.consolidation]]` | `Consolidator` |
| Query | `[[providers.query]]` | `Querier` |
| Embedding | `[[providers.embedding]]` | `EmbedderChain` |
| Reranker | `[[providers.reranker]]` | Optional HTTP rerank provider (`/v1/rerank`, Cohere `/v2` compatibility) |

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
| `[llm]` | Prism mode base URL (single-endpoint routing for all LLM tasks) |
| `[consolidation]` | Interval, min_facts threshold, max_group_size, dedup threshold, cluster_similarity_threshold (default 0.40) |
| `[embedding]` | Dimensions, distance metric |
| `[vector]` | Vector backend mode: `read-write` (default), `read-only`, or `disabled` |
| `[watcher]` | Watch path, poll interval, debounce, consolidate-after-ingest flag |
| `[prompts]` | Paths to extraction, consolidation, and query prompt templates |
| `[[providers.*]]` | Provider chains for extraction, consolidation, query, embedding, reranker |
| `[[types.*]]` | Type taxonomy: fact_types, entity_types, relation_types, connection_types |
| `[context]` | Context builder settings: recent_hours, max_facts, include_preferences |
| `[quality]` | Quality signal collection + Karpathy loop: enabled, thresholds, prompt paths, golden_dir |
| `[health]` | Provider auto-healing: catalog_refresh_days, max_retries, retry_interval_hours, queue_ttl_hours |
| `[openclaw]` | OpenClaw memory backend mode (off/parallel/replace) |
| `[hot]` | Hot phase (v0.5.0): enabled, ttl_minutes, tick_seconds, batch_size, embed_min_chars |
| `[cool]` | Cool pipeline (v0.6.0): enabled, tick_seconds, silence_hours, max_cluster_size. Requires `[hot] enabled`. |
| `[rerank]` | Post-merge reranking controls: top_n (works for both cosine default and provider rerank) |

### 7.2 Defaults

- `EffectiveEmbeddingDims()`: returns configured dimensions, or 1536 if not set.
- `EffectiveVectorConfig()`: defaults `vector.mode` to `read-write`.
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

Eval does not depend on production USearch ANN. When embeddings are enabled, the temp DB uses a pure-Go exact vector backend:
- deterministic ordering
- no cgo/native ANN dependency
- suitable for small eval datasets

This keeps production on USearch for speed, while the harness optimizes for correctness, portability, and reproducibility.

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

**Merge strategy (experiments):** `--merge-strategy=rrf` (default) uses reciprocal rank fusion. `--merge-strategy=set-union` uses dense-first ordering: vector hits keep similarity order, then FTS5 hits not already listed, then graph hits not already listed. Programmatic equivalent: `query.WithMergeStrategy`. Findings: `research/RETRIEVAL-OPTIMIZATION-RESEARCH.md` (E023).

The `Querier.Retrieve()` method runs the full retrieval pipeline (embed, parallel search, merge step defaulting to RRF) and returns ranked facts with per-fact layer provenance (`FromVector`, `FromText`, `FromGraph`) without calling the LLM for synthesis.

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

`query_log` table records per-query metrics: total/retrieval/synthesis latency, facts found per layer (vector, text, graph, hot, cooldown), citations count, embedder availability. Four new columns added in migration 014: `hot_by_vector`, `hot_by_text`, `cooldown_by_vector`, `cooldown_by_text`. Aggregated in `GET /status` and `imprint status`.
