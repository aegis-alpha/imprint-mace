# Hot Phase Implementation Specification (Phase 1, v0.5.0)

Phase 1 of the Hot-Cool-Cold Pipeline. Messages stored raw, queryable immediately via FTS5 + USearch HNSW (D36). TTL moves messages from hot to cooldown. Both hot and cooldown are searchable -- messages never disappear from query results. Cool phase extraction (topic segmentation + LLM) is Phase 2.

Linear tasks: BVP-352 (HCP-1), BVP-353 (HCP-2), BVP-358 partial (HCP-7).

Decisions D31-D36 are the design basis. Brainstorm Q7 Resolution is the schema source. D36: USearch replaces sqlite-vec; single cache file, SQLite is source of truth.

---

## 1. Migration 014

**File:** `internal/db/migrations/014_hot_cool_pipeline.sql`

Note: migration 013 is USearch migration (BVP-365, embedding BLOB on transcript_chunks). This migration is 014.

### 1.1 hot_messages

```sql
CREATE TABLE IF NOT EXISTS hot_messages (
    id              TEXT PRIMARY KEY,
    speaker         TEXT NOT NULL,
    content         TEXT NOT NULL,
    timestamp       TEXT NOT NULL,
    platform        TEXT,
    platform_session_id TEXT,
    linker_ref      TEXT,
    has_embedding   INTEGER NOT NULL DEFAULT 0,
    created_at      TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_hot_timestamp ON hot_messages(timestamp);
CREATE INDEX IF NOT EXISTS idx_hot_platform_session ON hot_messages(platform_session_id);
```

### 1.2 cooldown_messages

```sql
CREATE TABLE IF NOT EXISTS cooldown_messages (
    id              TEXT PRIMARY KEY,
    speaker         TEXT NOT NULL,
    content         TEXT NOT NULL,
    timestamp       TEXT NOT NULL,
    platform        TEXT,
    platform_session_id TEXT,
    linker_ref      TEXT,
    has_embedding   INTEGER NOT NULL DEFAULT 0,
    cluster_id      TEXT,
    transcript_file TEXT,
    transcript_line INTEGER,
    processed_at    TEXT,
    moved_from_hot  TEXT NOT NULL,
    created_at      TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_cooldown_cluster ON cooldown_messages(cluster_id);
CREATE INDEX IF NOT EXISTS idx_cooldown_processed ON cooldown_messages(processed_at);
CREATE INDEX IF NOT EXISTS idx_cooldown_timestamp ON cooldown_messages(timestamp);
CREATE INDEX IF NOT EXISTS idx_cooldown_platform_session ON cooldown_messages(platform_session_id);
```

### 1.3 FTS5 Virtual Tables

```sql
CREATE VIRTUAL TABLE IF NOT EXISTS hot_messages_fts USING fts5(
    content,
    message_id UNINDEXED
);

CREATE VIRTUAL TABLE IF NOT EXISTS cooldown_messages_fts USING fts5(
    content,
    message_id UNINDEXED
);
```

Pattern matches existing `facts_fts` (fact_id UNINDEXED) and `transcript_chunks_fts` (chunk_id UNINDEXED).

### 1.4 Vector Search (USearch, D36)

Vector search uses USearch HNSW, not sqlite-vec vec0 tables. There are no vec0 virtual tables in this migration.

USearch operates as a single cache file (`imprint.vecindex`) next to the SQLite database. SQLite `embedding` BLOB columns are the source of truth. The cache file is a derived acceleration structure (like FTS5) -- expendable and rebuildable.

All vector tables (facts, chunks, hot messages, cooldown messages) share one USearch index. Keys are prefixed to avoid collisions: `fact:<ulid>`, `chunk:<ulid>`, `hot:<ulid>`, `cool:<ulid>`. The uint64 key in USearch is FNV-64 hash of the prefixed string key.

**Hot message embeddings** are stored in a new SQLite column on `hot_messages`:

```sql
ALTER TABLE hot_messages ADD COLUMN embedding BLOB;
```

(This ALTER is included in migration 014, after the CREATE TABLE.)

When a hot message gets an embedding (content >= 50 chars), the embedding is stored in both:
1. `hot_messages.embedding` BLOB column (source of truth)
2. USearch index with key `"hot:" + messageID` (cache for fast search)

When messages move to cooldown, the embedding BLOB is copied to `cooldown_messages.embedding`:

```sql
ALTER TABLE cooldown_messages ADD COLUMN embedding BLOB;
```

And the USearch key is re-added with `"cool:"` prefix (old `"hot:"` key removed).

**Index lifecycle** (managed in `internal/vecindex/` package):

```go
func OpenVectorIndex(cachePath string, dims int, store db.Store) (*USearchIndex, error)
```

1. **Startup:** try `index.Load(cachePath)`. If success -- 75ms for 200K vectors. Done.
2. **Cache miss/corrupt:** rebuild from SQLite. Scan all embedding BLOBs from `facts.embedding`, `hot_messages.embedding`, `cooldown_messages.embedding` columns, plus chunk embeddings from the existing `chunks_vec` table (until chunks are migrated to BLOB storage). Insert into USearch with prefixed keys. Save cache. Slow (~11 min for 200K) but only on first run or recovery.
3. **Runtime:** `index.Add()` on each new embedding, `index.Search()` for queries.
4. **Shutdown:** `index.Save()` via atomic write (temp file + `os.Rename`). 316ms for 200K.

**Thread safety:** `index.ChangeThreadsSearch(16)` and `index.ChangeThreadsAdd(4)` at startup. Concurrent reads and writes are safe.

**Quantization:** f16 (half-precision). Recall@1 = 99.1% vs 99.2% for f32 (Deep1B benchmark). Memory: ~341 MB for 200K/768d vs ~585 MB for f32.

### 1.5 Design Notes

- Message ID is ULID (D14), generated once in hot, preserved in cooldown. Same ID across both tables.
- `linker_ref` references another message ULID regardless of which table the target is in.
- `has_embedding` = 1 only for messages with `len(content) >= 50` (E021 decision: zero recall loss, 22% cost saving).
- Two separate tables for performance: hot = realtime query (agent waits), cool = background processing. SQLite single-writer constraint means shared table would cause contention (D35).
- `cluster_id` is ULID (TEXT), not INTEGER. Clusters may be recreated during re-clustering; ULID prevents ID collisions.
- `moved_from_hot` is NOT NULL -- every cooldown message came from hot.
- `processed_at` is NULL until extraction completes in a future phase.
- Vector search uses USearch HNSW (D36), not sqlite-vec vec0. Single cache file, SQLite is source of truth.

---

## 2. Store Interface Extensions

All new methods are added to the `Store` interface in `internal/db/store.go`.

### 2.1 HotMessage Type

Used by multiple Store methods. Defined in section 3 but referenced here for signatures.

### 2.2 InsertHotMessage

```go
InsertHotMessage(ctx context.Context, msg *model.HotMessage, embedding []float32) error
```

The `embedding` parameter is the pre-computed vector (or nil if no embedding). This follows the pattern of `CreateTranscriptChunk(ctx, chunk, text)` which takes auxiliary data alongside the main struct.

**SQL:**

```sql
INSERT INTO hot_messages (id, speaker, content, timestamp, platform, platform_session_id,
    linker_ref, has_embedding, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);
```

Also inserts into `hot_messages_fts`:

```sql
INSERT INTO hot_messages_fts (content, message_id) VALUES (?, ?);
```

If `embedding` is non-nil, also:
1. Stores embedding as BLOB in `hot_messages.embedding` column (source of truth)
2. Adds to USearch index: `vecIndex.Add("hot:"+msg.ID, embedding)`

**Error cases:**

- UNIQUE constraint violation (duplicate ID) -- return wrapped error
- Context cancelled -- return ctx.Err()

### 2.3 ListHotMessages

```go
ListHotMessages(ctx context.Context, filter HotMessageFilter) ([]model.HotMessage, error)
```

```go
type HotMessageFilter struct {
    PlatformSessionID string
    After             *time.Time // messages with timestamp > After
    Before            *time.Time // messages with timestamp < Before
    Limit             int        // default 100
}
```

**SQL:** `SELECT ... FROM hot_messages WHERE 1=1` with optional AND clauses for each filter field. ORDER BY timestamp ASC. LIMIT defaults to 100.

**Error cases:**

- Empty result -- return empty slice, nil error

### 2.4 SearchHotByText

```go
SearchHotByText(ctx context.Context, query string, limit int) ([]ScoredHotMessage, error)
```

```go
type ScoredHotMessage struct {
    Message model.HotMessage
    Score   float64
}
```

**SQL:**

```sql
SELECT hm.*, hf.rank
FROM hot_messages_fts hf
JOIN hot_messages hm ON hf.message_id = hm.id
WHERE hot_messages_fts MATCH ?
ORDER BY hf.rank
LIMIT ?
```

Query must be sanitized through `fts.SanitizeQuery()` before use (same as existing FTS5 searches).

**Error cases:**

- Empty/invalid query after sanitization -- return empty slice, nil error
- FTS5 syntax error -- log warning, return empty slice, nil error

### 2.5 SearchHotByVector

```go
SearchHotByVector(ctx context.Context, embedding []float32, limit int) ([]ScoredHotMessage, error)
```

**Implementation (USearch, D36):**

1. Call `vecIndex.Search(embedding, limit)` with `"hot:"` prefix filter (USearch `FilteredSearch` callback accepts only keys whose reverse-mapped string starts with `"hot:"`)
2. For each returned key, strip `"hot:"` prefix to get message ULID
3. Fetch full `HotMessage` from SQLite by ID
4. Return `[]ScoredHotMessage` with distance scores converted to similarity (1 - distance)

No SQL JOIN needed -- USearch returns IDs, Go code fetches metadata from SQLite.

**Error cases:**

- USearch index not loaded -- return empty slice, nil error (graceful degradation)
- Nil embedding -- return empty slice, nil error

### 2.6 SearchCooldownByText

```go
SearchCooldownByText(ctx context.Context, query string, limit int) ([]ScoredHotMessage, error)
```

Same as `SearchHotByText` but over `cooldown_messages_fts`. Reuses `ScoredHotMessage` type (the struct is identical -- a HotMessage + Score).

**SQL:**

```sql
SELECT cm.*, cf.rank
FROM cooldown_messages_fts cf
JOIN cooldown_messages cm ON cf.message_id = cm.id
WHERE cooldown_messages_fts MATCH ?
ORDER BY cf.rank
LIMIT ?
```

Query sanitized through `fts.SanitizeQuery()`.

**Error cases:** same as SearchHotByText.

### 2.7 SearchCooldownByVector

```go
SearchCooldownByVector(ctx context.Context, embedding []float32, limit int) ([]ScoredHotMessage, error)
```

Same as `SearchHotByVector` but uses `"cool:"` prefix filter in USearch.

1. Call `vecIndex.Search(embedding, limit)` with `"cool:"` prefix filter
2. Strip `"cool:"` prefix to get message ULID
3. Fetch full message from `cooldown_messages` by ID
4. Return `[]ScoredHotMessage`

**Error cases:** same as SearchHotByVector.

### 2.8 MoveHotToCooldown (was 2.6)

```go
MoveHotToCooldown(ctx context.Context, olderThan time.Time, batchSize int) (moved int64, err error)
```

Runs in a single SQLite transaction + USearch index updates:

**SQLite transaction:**

```sql
BEGIN;

INSERT INTO cooldown_messages (id, speaker, content, timestamp, platform,
    platform_session_id, linker_ref, has_embedding, embedding, cluster_id,
    transcript_file, transcript_line, processed_at, moved_from_hot, created_at)
SELECT id, speaker, content, timestamp, platform,
    platform_session_id, linker_ref, has_embedding, embedding, NULL, NULL,
    NULL, NULL, ?, created_at
FROM hot_messages
WHERE timestamp < ?
ORDER BY timestamp ASC
LIMIT ?;

INSERT INTO cooldown_messages_fts (content, message_id)
SELECT hf.content, hf.message_id
FROM hot_messages_fts hf
JOIN hot_messages hm ON hf.message_id = hm.id
WHERE hm.timestamp < ?
ORDER BY hm.timestamp ASC
LIMIT ?;

DELETE FROM hot_messages_fts WHERE message_id IN (
    SELECT id FROM hot_messages WHERE timestamp < ? ORDER BY timestamp ASC LIMIT ?
);

DELETE FROM hot_messages WHERE timestamp < ? ORDER BY timestamp ASC LIMIT ?;

COMMIT;
```

**USearch index updates (after SQLite commit):**

For each moved message with `has_embedding = 1`:
1. Read the embedding from the in-memory USearch entry (or from the just-inserted cooldown_messages.embedding BLOB)
2. `vecIndex.Remove("hot:" + id)` -- remove hot key
3. `vecIndex.Add("cool:" + id, embedding)` -- add cooldown key

These are in-memory operations on the USearch HNSW graph. The cache file is saved at shutdown, not per-move.

The `moved_from_hot` parameter is `time.Now().UTC().Format(time.RFC3339)`.

**Error cases:**

- Transaction failure -- rollback, return error
- No messages to move -- return 0, nil
- Partial failure (INSERT succeeds, DELETE fails) -- rollback entire transaction

### 2.7 DeleteExpiredHot

```go
DeleteExpiredHot(ctx context.Context, olderThan time.Time) (int64, error)
```

Safety cleanup for messages that somehow remain in hot past TTL (e.g. after crash during MoveHotToCooldown). Deletes from hot_messages and hot_messages_fts. Also removes corresponding `"hot:"` keys from USearch index.

```sql
DELETE FROM hot_messages_fts WHERE message_id IN (
    SELECT id FROM hot_messages WHERE timestamp < ?
);
DELETE FROM hot_messages WHERE timestamp < ?;
```

Also: `vecIndex.Remove("hot:" + id)` for each deleted message with `has_embedding = 1`.

**Error cases:**

- No messages to delete -- return 0, nil

### 2.8 CountHotMessages

```go
CountHotMessages(ctx context.Context) (int, error)
```

```sql
SELECT COUNT(*) FROM hot_messages;
```

Used by `Stats()` to include hot message counts in `GET /status`. The `DBStats` struct gains a new field:

```go
type DBStats struct {
    // ... existing fields ...
    HotMessages      int
    CooldownMessages int
}
```

The `Stats()` implementation adds two queries:

```sql
SELECT COUNT(*) FROM hot_messages;
SELECT COUNT(*) FROM cooldown_messages;
```

### 2.9 Vector Index Lifecycle (replaces EnsureHotVecTable / EnsureCooldownVecTable)

No per-table vec methods. The single USearch index is managed by `internal/vecindex/` package (see section 1.4). The Store interface does not expose vector index lifecycle -- it is handled at the application level in `main.go`:

```go
vecIndex, err := vecindex.OpenVectorIndex(cachePath, dims, store)
```

The `vecIndex` is passed to the Store, Querier, Builder, and API handler as a dependency.

---

## 3. Hot Message Type

**File:** `internal/model/types.go`

### 3.1 Struct Definition

```go
type HotMessage struct {
    ID                string    `json:"id"`
    Speaker           string    `json:"speaker"`
    Content           string    `json:"content"`
    Timestamp         time.Time `json:"timestamp"`
    Platform          string    `json:"platform,omitempty"`
    PlatformSessionID string    `json:"platform_session_id,omitempty"`
    LinkerRef         string    `json:"linker_ref,omitempty"`
    HasEmbedding      bool      `json:"has_embedding"`
    CreatedAt         time.Time `json:"created_at"`
}
```

### 3.2 Relationship to Existing Types

- **Not a Fact.** HotMessage is raw text, not extracted structured knowledge. Facts are produced by extraction in the cool phase (future).
- **Not a TranscriptChunk.** TranscriptChunks are segments of transcript files on disk. HotMessages are individual messages from hooks/API, stored in the DB.
- **ID generation:** ULID via `db.NewID()`, same as all other IDs in the system (D14). Generated once at insert time, preserved when message moves to cooldown.
- **Speaker values:** `"user"` or `"assistant"`. Matches the convention used in transcript format (spec/TRANSCRIPT-FORMAT.md).
- **Timestamp:** The original message time from the platform, not the DB insert time. `CreatedAt` is the DB insert time.

### 3.3 CooldownMessage Type

Not needed for Phase 1. The cooldown_messages table exists but is only written to by `MoveHotToCooldown`. No code reads from it in Phase 1. When Phase 2 (cool phase extraction) is implemented, a `CooldownMessage` type will be added with the additional fields (cluster_id, transcript_file, transcript_line, processed_at, moved_from_hot).

---

## 4. Hot Phase Ingest API

### 4.1 Design Decision: Routing

**Decision: `POST /ingest` changes behavior when hot phase is enabled. No new endpoint.**

All existing callers (`POST /ingest` HTTP, `imprint_ingest` MCP tool, OpenClaw hook, Cursor SKILL.md, Claude Code AGENTS.md) continue using the same URL and tool name. When `[hot] enabled = true` in config, `POST /ingest` stores raw messages in hot_messages instead of running LLM extraction. When `[hot] enabled = false`, behavior is unchanged (LLM extraction).

Rationale: every caller that sends per-message text to `POST /ingest` is doing realtime ingest. Hot phase replaces realtime LLM extraction entirely. Creating a new endpoint would require updating every hook, every SKILL.md, every AGENTS.md. Reusing the existing endpoint means zero changes to callers.

Batch callers (CLI `imprint ingest`, `imprint ingest-dir`, file watcher) call `Engine.Ingest()` directly in Go -- they do not use the HTTP endpoint. They are not affected.

### 4.2 HTTP Endpoint

**`POST /ingest` (modified behavior when hot enabled)**

Request (one new optional field):

```json
{
    "text": "We're going with Go for the backend",
    "source": "realtime:main",
    "mode": "extract"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `text` | string | yes | Message text |
| `source` | string | no | Source identifier. Defaults to `"api"`. |
| `mode` | string | no | `"extract"` forces LLM extraction even when hot enabled. Omit or any other value = default behavior. |

**Routing logic:**

- `[hot] enabled = false` -> always LLM extraction (old behavior), `mode` ignored
- `[hot] enabled = true` AND `mode` absent or not `"extract"` -> hot store
- `[hot] enabled = true` AND `mode = "extract"` -> LLM extraction (override)

The `mode` parameter is for manual/testing use. Normal callers (hooks, agents) never set it.

**New behavior when hot path is taken:**

Instead of calling `Engine.Ingest()` (LLM extraction), the handler:

1. Parses `text` and `source` from JSON body
2. Generates ULID via `db.NewID()`
3. Extracts `speaker` from context: default `"user"` (hooks send user messages; assistant messages come from batch transcripts)
4. Extracts `platform` and `platform_session_id` from `source` field: if source starts with `"realtime:"`, the remainder is the platform_session_id. Platform is inferred from the session ID format or defaults to `"unknown"`.
5. Determines `has_embedding`: `len(text) >= 50` AND embedder is non-nil
6. If `has_embedding`, generates embedding synchronously (see section 6)
7. Calls `store.InsertHotMessage(ctx, msg, embedding)`
8. Returns response

Response (200 OK, changed):

```json
{
    "id": "01JQXYZ...",
    "has_embedding": true,
    "hot": true
}
```

| Field | Type | Description |
|-------|------|-------------|
| `id` | string | ULID of the stored message |
| `has_embedding` | bool | Whether an embedding was generated |
| `hot` | bool | `true` when message was stored in hot phase (helps callers know which path was taken) |

When `[hot] enabled = false`, the handler behaves exactly as before (calls `Engine.Ingest()`, returns `IngestResult` with facts_count, entities_count, etc.). The `hot` field is absent or false.

Error responses (unchanged):

| Status | Condition |
|--------|-----------|
| 400 | Missing `text` |
| 500 | DB write failure |

### 4.3 Handler Implementation

In `internal/api/api.go`, the existing `handleIngest` method gains a branch:

```go
func (h *Handler) handleIngest(w http.ResponseWriter, r *http.Request) {
    // ... existing JSON parsing ...
    if h.hotEnabled {
        h.handleHotIngest(w, r, req)
        return
    }
    // ... existing Engine.Ingest() path ...
}
```

The `Handler` struct gets two new fields:

```go
type Handler struct {
    // ... existing fields ...
    embedder   provider.Embedder // nil = hot embedding disabled
    hotEnabled bool
}
```

Set via setter methods:

```go
func (h *Handler) SetEmbedder(e provider.Embedder)
func (h *Handler) SetHotEnabled(enabled bool)
```

### 4.4 MCP Tool

**`imprint_ingest` changes behavior when hot enabled. No new tool.**

Same principle as HTTP. The existing `imprint_ingest` tool handler gains the same branch:

```go
func (s *Server) handleIngest(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
    if s.hotEnabled {
        return s.handleHotIngest(ctx, req)
    }
    // ... existing Engine.Ingest() path ...
}
```

The `Server` struct gets new fields:

```go
type Server struct {
    // ... existing fields ...
    embedder   provider.Embedder
    hotEnabled bool
}
```

Set via setter methods:

```go
func (s *Server) SetEmbedder(e provider.Embedder)
func (s *Server) SetHotEnabled(enabled bool)
```

Tool description updated to reflect dual behavior:

```go
mcp.WithDescription("Store knowledge from text. When hot phase is enabled, stores raw message for immediate search (zero LLM cost). When disabled, extracts facts, entities, and relationships via LLM.")
```

### 4.5 Hook Update

**No hook changes needed.** The OpenClaw hook (`integrations/openclaw/hooks/imprint-ingest/handler.ts`) continues to send `POST /ingest` with `{"text": content, "source": "realtime:" + sessionId}`. The server-side behavior changes based on config.

Cursor SKILL.md and Claude Code AGENTS.md continue to instruct agents to call `imprint_ingest`. No documentation changes needed.

### 4.6 Backward Compatibility

- `POST /ingest` URL is unchanged. All callers work without modification.
- `imprint_ingest` MCP tool name is unchanged. All agent instructions work without modification.
- When `[hot] enabled = false` (default), behavior is identical to current. Zero risk for existing deployments.
- When `[hot] enabled = true`, per-message callers get hot behavior automatically. Batch callers (Engine.Ingest() direct) are not affected.
- Response schema changes: `hot: true` field added. Callers that don't check this field are unaffected.

---

## 5. TTL Goroutine

### 5.1 Who Starts It

The TTL goroutine runs in `runServe` (the `serve` command). It does NOT run in `watch` or other commands -- hot phase requires the HTTP server to be running (hooks send messages to the API).

### 5.2 Lifecycle

```go
func startHotTTLGoroutine(ctx context.Context, store db.Store, cfg HotConfig, logger *slog.Logger)
```

Called from `runServe` after store is opened and USearch index is loaded:

```go
hotCfg := cfg.EffectiveHotConfig()
if hotCfg.Enabled != nil && *hotCfg.Enabled {
    hotCtx, hotCancel := context.WithCancel(context.Background())
    defer hotCancel()
    go startHotTTLGoroutine(hotCtx, store, hotCfg, logger)
    logger.Info("hot phase TTL goroutine started",
        "ttl_minutes", hotCfg.TTLMinutes,
        "tick_seconds", hotCfg.TickSeconds,
        "batch_size", hotCfg.BatchSize)
}
```

Pattern matches the existing health refresh goroutine and consolidation scheduler in `runServe`.

### 5.3 Tick Logic

```go
func startHotTTLGoroutine(ctx context.Context, store db.Store, cfg HotConfig, logger *slog.Logger) {
    ticker := time.NewTicker(time.Duration(cfg.TickSeconds) * time.Second)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            cutoff := time.Now().UTC().Add(-time.Duration(cfg.TTLMinutes) * time.Minute)
            moved, err := store.MoveHotToCooldown(ctx, cutoff, cfg.BatchSize)
            if err != nil {
                logger.Error("hot TTL move failed", "error", err)
                continue
            }
            if moved > 0 {
                logger.Info("hot TTL moved messages to cooldown", "count", moved)
            }
        }
    }
}
```

### 5.4 Batch Size

Default: 100 messages per tick. Configurable via `[hot] batch_size`.

Rationale: at 1000 messages/day and 60-second tick interval, each tick moves at most ~1 message (messages arrive spread over the day). Batch size of 100 handles bursts and catch-up after downtime. The transaction (INSERT + DELETE) is fast for 100 rows in SQLite.

### 5.5 Failure Handling


| Failure                         | Behavior                                                                                                                                                        |
| ------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| cooldown INSERT fails           | Transaction rolls back. No data lost. Message stays in hot. Retried on next tick.                                                                               |
| hot DELETE fails                | Transaction rolls back. Message stays in hot. Retried on next tick.                                                                                             |
| Context cancelled               | Goroutine exits cleanly. Messages remain in hot until next server start.                                                                                        |
| Messages arrive during transfer | Safe. The transfer uses `WHERE timestamp < cutoff` with a cutoff computed before the query. New messages have timestamps after the cutoff and are not affected. |


### 5.6 Graceful Shutdown

The goroutine's context is derived from `runServe`'s lifecycle. When the server receives SIGINT/SIGTERM, the deferred `hotCancel()` fires, the goroutine exits on the next `select` iteration. No in-flight transaction is interrupted -- the current tick completes (or the DB connection closes, rolling back any open transaction).

---

## 6. Selective Embedding

### 6.1 Threshold

Messages with `len(content) >= 50` characters get an embedding. Messages shorter than 50 characters do not.

Source: E021 experiment (30 transcripts, 1492 messages, 25 queries). Result: embed-by-length = embed-all (identical Recall@10 and MRR). 22% cost saving, zero recall loss. Short messages ("da", "ok", "yes") have generic embeddings that never rank in top-10 results.

### 6.2 Which Embedder

The existing `EmbedderChain` from config (`[[providers.embedding]]`). Same embedder used for fact and chunk embeddings.

No separate "hot embedder" or local-only embedder in Phase 1. The embedding strategy decision (S067: local EmbeddingGemma as primary, cloud as fallback) is a future optimization. Phase 1 uses whatever embedder is configured.

If no embedder is configured (nil), hot messages are stored without embeddings. `has_embedding` = 0 for all messages. FTS5 search still works. This is the same graceful degradation pattern used by the query pipeline.

### 6.3 Timing: Synchronous

**Decision: Synchronous embedding during ingest.**

Options considered:


| Option          | Pros                                                                                      | Cons                                                                                                    |
| --------------- | ----------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------- |
| Sync            | Simple. Message is fully indexed when response returns. No race between ingest and query. | Adds ~5-100ms latency to hot ingest (local: 5ms, API: 100-200ms).                                       |
| Async goroutine | Zero latency for ingest response.                                                         | Race: query may find message via FTS5 but not via vector. Complexity: need to track pending embeddings. |


**Choice: Synchronous.** Rationale: hot ingest is called from hooks. The hook is fire-and-forget -- the agent does not wait for the response. Adding 5-100ms to a fire-and-forget call has zero user impact. The simplicity benefit (no async tracking, no races) outweighs the latency cost that nobody observes.

### 6.4 has_embedding Flag

Set at insert time based on `len(content) >= 50` AND embedder is non-nil AND embedding succeeds.

If embedding fails (provider error, timeout), the message is still stored with `has_embedding = 0`. The message is searchable via FTS5 but not via vector. This matches the graceful degradation pattern.

### 6.5 Vector Storage

When embedding succeeds, the vector is passed to `InsertHotMessage(ctx, msg, embedding)`. The store method:
1. Stores the embedding as BLOB in `hot_messages.embedding` column (source of truth, D36)
2. Adds to USearch index: `vecIndex.Add("hot:"+msg.ID, embedding)` (cache for fast search)

---

## 7. Three-Phase Query (Hot + Cold)

This is the most complex section. In Phase 1, cool phase is empty, so query searches hot + cold only.

### 7.1 Design Decision: Query Integration

**Decision: Hot search as new layers in the existing Querier, not a separate pre-step.**

Options considered:


| Option                | Pros                                                                | Cons                                                                                              |
| --------------------- | ------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------- |
| New layers in Querier | Unified RRF merge. Single code path. Consistent latency accounting. | Querier becomes more complex.                                                                     |
| Separate pre-step     | Querier unchanged. Hot search is independent.                       | Two separate search calls. Need custom merge logic outside Querier. Latency not tracked together. |


**Choice: New layers in Querier.** Rationale: the Querier already runs 5 layers concurrently and merges via RRF. Adding 4 more layers (hot FTS5, hot vector, cooldown FTS5, cooldown vector) is the same pattern. A separate pre-step would duplicate the merge logic and break the unified latency tracking in query_log.

### 7.2 New Retrieval Layers

Four new layers added to the existing 5, running concurrently (9 total):


| Layer           | Source                              | Method                                    | Limit |
| --------------- | ----------------------------------- | ----------------------------------------- | ----- |
| Hot vector      | USearch index (`"hot:"` prefix)     | HNSW cosine search on question embedding  | 10    |
| Hot FTS5        | `hot_messages_fts`                  | BM25 keyword search on sanitized question | 10    |
| Cooldown vector | USearch index (`"cool:"` prefix)    | HNSW cosine search on question embedding  | 10    |
| Cooldown FTS5   | `cooldown_messages_fts`             | BM25 keyword search on sanitized question | 10    |


The Querier's `retrieve()` method adds two more goroutines:

```go
// Hot vector search
if embedding != nil {
    wg.Add(1)
    go func() {
        defer wg.Done()
        msgs, err := q.store.SearchHotByVector(ctx, embedding, 10)
        if err != nil {
            q.logger.Warn("hot vector search failed", "error", err)
            return
        }
        mu.Lock()
        r.hotByVector = msgs
        mu.Unlock()
    }()
}

// Hot FTS5 search
wg.Add(1)
go func() {
    defer wg.Done()
    sanitized := fts.SanitizeQuery(question)
    if sanitized == "" {
        return
    }
    msgs, err := q.store.SearchHotByText(ctx, sanitized, 10)
    if err != nil {
        q.logger.Warn("hot text search failed", "error", err)
        return
    }
    mu.Lock()
    r.hotByText = msgs
    mu.Unlock()
}()

// Cooldown vector search
if embedding != nil {
    wg.Add(1)
    go func() {
        defer wg.Done()
        msgs, err := q.store.SearchCooldownByVector(ctx, embedding, 10)
        if err != nil {
            q.logger.Warn("cooldown vector search failed", "error", err)
            return
        }
        mu.Lock()
        r.cooldownByVector = msgs
        mu.Unlock()
    }()
}

// Cooldown FTS5 search
wg.Add(1)
go func() {
    defer wg.Done()
    sanitized := fts.SanitizeQuery(question)
    if sanitized == "" {
        return
    }
    msgs, err := q.store.SearchCooldownByText(ctx, sanitized, 10)
    if err != nil {
        q.logger.Warn("cooldown text search failed", "error", err)
        return
    }
    mu.Lock()
    r.cooldownByText = msgs
    mu.Unlock()
}()
```

### 7.3 retrievalResult Extension

```go
type retrievalResult struct {
    factsByVector    []db.ScoredFact
    factsByText      []db.ScoredFact
    chunksByVector   []db.ScoredChunk
    chunksByText     []db.ScoredChunk
    graphFacts       []model.Fact
    hotByVector      []db.ScoredHotMessage  // NEW
    hotByText        []db.ScoredHotMessage  // NEW
    cooldownByVector []db.ScoredHotMessage  // NEW
    cooldownByText   []db.ScoredHotMessage  // NEW
}
```

### 7.4 RRF Merge: Raw Messages + Structured Facts

Hot messages participate in RRF alongside facts. Each hot message gets a synthetic "fact-like" entry for scoring purposes.

In `mergeAndRank`, after processing facts:

```go
for rank := range r.hotByVector {
    sm := &r.hotByVector[rank]
    id := "hot:" + sm.Message.ID
    scores[id] += 1.0 / (k + float64(rank+1))
    hotMessages[id] = sm.Message
}

for rank := range r.hotByText {
    sm := &r.hotByText[rank]
    id := "hot:" + sm.Message.ID
    scores[id] += 1.0 / (k + float64(rank+1))
    hotMessages[id] = sm.Message
}

for rank := range r.cooldownByVector {
    sm := &r.cooldownByVector[rank]
    id := "cool:" + sm.Message.ID
    scores[id] += 1.0 / (k + float64(rank+1))
    hotMessages[id] = sm.Message  // reuse same map -- HotMessage struct is identical
}

for rank := range r.cooldownByText {
    sm := &r.cooldownByText[rank]
    id := "cool:" + sm.Message.ID
    scores[id] += 1.0 / (k + float64(rank+1))
    hotMessages[id] = sm.Message
}
```

The `"hot:"` and `"cool:"` prefixes distinguish message IDs from fact IDs and from each other in the score map. Messages from both hot and cooldown are treated identically in ranking -- they are all raw messages, the only difference is age.

The merged output uses a unified type that can hold either a fact or a raw message:

```go
type rankedItem struct {
    fact       *model.Fact       // non-nil for cold results
    hotMessage *model.HotMessage // non-nil for hot/cooldown results
    score      float64
    isHot      bool              // true for both hot and cooldown messages
}
```

The `mergeAndRank` method signature does not change its return arity -- it returns a single slice:

```go
func (q *Querier) mergeAndRank(r *retrievalResult) []rankedItem
```

The existing `rankedFact` type is replaced by `rankedItem`. The `Retrieve()` public method (used by eval) continues to work: it filters `rankedItem` entries where `isHot == false` to build `[]RankedFact`. Hot messages are excluded from `RetrievalResult` since the eval pipeline only measures cold retrieval quality. A future `RetrieveWithHot()` method can expose hot results when needed.

### 7.5 LLM Synthesis Prompt

The synthesis prompt gains a new section for hot messages. The prompt structure becomes:

```
### Question
<user's question>

### Fresh Messages (recent, unverified)
- [hot:01JQXYZ...] (user, 2026-04-04 14:30): We're going with Go for the backend
- [hot:01JQXYZ...] (assistant, 2026-04-04 14:31): Should I use standard cmd/internal layout?
- [hot:01JQXYZ...] (user, 2026-04-04 14:31) (->01JQXYZ...): yes

### Facts
- [01ABC...] (decision, confidence=0.95, 2026-03-15) Language: Decided to use Go for the backend
...

### Data Quality
...

### Transcript Context
...
```

The "Fresh Messages" section is included when hot or cooldown results exist. Both are shown in the same section -- the user does not need to know which table the message came from. Messages are sorted by timestamp (newest first). Formatted as:

```
- [hot:<id>] (<speaker>, <date> <time>): <content>
```

If the message has a `linker_ref`, it is shown as `(->ref)` after the time.

### 7.6 Updated System Prompt

The `querySystemPrompt` constant gains additional rules for handling mixed results:

```
10. Fresh messages: A "Fresh Messages" section may be included. These are raw, unverified messages from recent conversations. They are the most current information but have not been verified through extraction.
    - When fresh messages confirm or update a structured fact, prefer the fresh message (it is newer).
    - When fresh messages contain a proposal or question (not a confirmed decision), mention it as "currently under discussion" -- do not present it as a decided fact.
    - When fresh messages contradict a high-confidence fact, show both and note the potential change.
    - You cannot cite fresh messages by fact_id. Instead, reference them by their hot: prefix ID in the citations array.
```

### 7.7 Citation Extension

The `Citation` struct gains a `HotMessageID` field:

```go
type Citation struct {
    FactID          string `json:"fact_id,omitempty"`
    ConsolidationID string `json:"consolidation_id,omitempty"`
    HotMessageID    string `json:"hot_message_id,omitempty"`
}
```

### 7.8 GET /context Update -- DEFERRED (requires compaction signal)

**Status:** Not implemented in Phase 1. Open design question for Phase 2.

**The problem:** `GET /context` is called on every agent turn (OpenClaw plugin `imprint-context` calls it via `before_prompt_build`). If context builder injects hot messages into every response, it duplicates what the agent already has in its context window. The agent sees the same messages twice -- once in its own context, once injected by Imprint. This wastes tokens on every LLM call.

Hot message injection makes sense only when the agent has **lost** context:
- After compaction (agent's context window was compressed, middle of conversation lost)
- At session start (agent begins with empty context)

During normal operation (no compaction, same session), the agent already has all recent messages in its context window. Injecting them again is pure duplication.

**Measured impact:** Kevin's production config shows `GET /context` injects ~60 records per call. Adding hot messages would increase this further. At $0.15-5.00/M input tokens (depending on model), unnecessary duplication across hundreds of daily calls adds up.

**Prior art research (S070):**

Three production systems solve this problem:

**1. JoelClaw ADR-0203 (most relevant).** Three-zone compaction recovery pipeline. Pi agent framework provides `ctx.getContextUsage()` API (returns % of context window filled) and lifecycle hooks (`session_before_compact`, `session_compact`, `turn_end`). The extension monitors context fill level:
- 40% (warm zone): start async recall queries, cache results
- 60% (hot zone): write task context to durable memory
- After compaction: inject pointer message (~150-300 tokens) with top-3 recalled memories + validated recall queries

Key insight: **inject pointers, not content.** "Here's what you were doing, here are the queries to run if you need more." Agent decides whether to fetch full context on demand. ~150-300 tokens vs thousands.

Production data: 42 compactions per session average. Each compaction discards ~63K tokens, replaces with ~2K summary. 77% of named entities lost in standard compaction.

Reference: https://joelclaw.com/adrs/0203-compaction-recovery-pipeline (no explicit license, personal project, public documentation).

**2. OpenClaw memory-context plugin (PR #17129, MIT).** Smart-trim architecture: archives messages at compaction, recalls via hybrid BM25 + vector search with time-decay and MMR re-ranking. Compaction bridge handles archive-before-compact + recall-after-compact lifecycle.

**3. xMemory (MIT, academic).** Hierarchical organization (Messages -> Episodes -> Semantics -> Themes). Reduces tokens from 9000 to 4700 per query. Solves "retrieval collapse" (flooding with near-duplicate snippets). Heavier architecture, more suited for long-term memory than compaction recovery.

**What Imprint lacks to implement this:**

1. **Compaction signal.** Imprint does not know when compaction happened. Options:
   - **(a)** Platform hook sends `"context_reset": true` in `POST /ingest`. Requires hook change per platform. OpenClaw has `compaction` event in config (Kevin uses `mode: "safeguard"` with `memoryFlush`). Other platforms -- unknown.
   - **(b)** `GET /context?after_compaction=true` parameter. Plugin/hook passes the flag when it knows compaction occurred. Same dependency on platform signal.
   - **(c)** Heuristic: if no messages from this `platform_session_id` for >N minutes, assume context reset. Unreliable -- could be a pause, not compaction.
   - **(d)** Count messages: Imprint knows how many messages it received from this session. If the agent's context window is ~200K tokens and average message is ~500 tokens, compaction happens around ~300-400 messages. Imprint can estimate. Fragile -- depends on model, system prompt size, tool call overhead.

2. **Context usage %.** JoelClaw uses `ctx.getContextUsage()` to know fill level. Imprint has no equivalent. Would need the platform to report it.

3. **Pointer format.** What to inject after compaction. JoelClaw's format: task summary (200 chars) + top 5 modified files + top 3 recalled memories (150 chars each) + 2 validated recall queries. Total ~150-300 tokens. Imprint equivalent: task summary from recent hot messages + top 3 relevant facts + recall query suggestions.

**Decision for Phase 1:** `GET /context` does NOT include hot messages. Context builder works with cold facts only (unchanged from current behavior). Hot messages are available through `GET /query` for direct questions.

**What to decide for Phase 2:**
- Which compaction signal mechanism (a/b/c/d above)
- Pointer format for post-compaction injection
- Whether to implement warm/hot zones (JoelClaw pattern) or simple binary (compacted / not compacted)
- Per-platform research: does each platform expose compaction events?

### 7.9 GET /query Update

No separate update needed. The Querier changes (sections 7.2-7.6) automatically affect `GET /query` because the handler calls `q.querier.Query()`.

### 7.10 Latency Budget

Hot search must not increase `GET /context` p95 beyond the existing target.

Hot FTS5 search: <1ms (SQLite FTS5 on a small table -- hot messages are TTL'd to ~1 hour of data, typically <200 messages).

Hot vector search: ~1ms (USearch HNSW, measured in PoC: avg 1.098ms at 200K vectors, 768 dims, f16. Hot table has <200 vectors -- even faster).

Cooldown FTS5 search: <5ms (cooldown table grows over time -- ~1000 msgs/day, but FTS5 scales well).

Cooldown vector search: ~1ms (USearch HNSW, same index as hot, prefix-filtered).

All 4 new layers run concurrently with existing 5 layers. Total added latency: ~0ms (parallel execution, all new layers are faster than the slowest existing layer).

**Comparison with sqlite-vec:** USearch is 247x faster at 200K scale (1.1ms vs estimated 272ms). At hot table scale (<200 vectors), both would be sub-millisecond, but USearch scales to the full index without degradation.

### 7.11 query_log Update

The `QueryLog` struct and `query_log` table gain two new columns:

```go
type QueryLog struct {
    // ... existing fields ...
    HotByVector      int
    HotByText        int
    CooldownByVector int
    CooldownByText   int
}
```

Migration 014 adds:

```sql
ALTER TABLE query_log ADD COLUMN hot_by_vector INTEGER NOT NULL DEFAULT 0;
ALTER TABLE query_log ADD COLUMN hot_by_text INTEGER NOT NULL DEFAULT 0;
ALTER TABLE query_log ADD COLUMN cooldown_by_vector INTEGER NOT NULL DEFAULT 0;
ALTER TABLE query_log ADD COLUMN cooldown_by_text INTEGER NOT NULL DEFAULT 0;
```

(These ALTER statements are included in the 014 migration file, after the table CREATE statements.)

---

## 8. Backward Compatibility

### 8.1 Old Realtime Path (D27: WithSessionID, supersede)

**Decision: Remove. Hot phase replaces D27 realtime path entirely.**

The old realtime path (`WithSessionID` -> `realtime:{sessionID}` in source_file -> `SupersedeRealtimeBySession` on batch) is deleted in Phase 1. Hot phase solves the same problem better: messages are stored raw (zero LLM cost), queryable immediately, and extracted with full context in cool phase (future). There are no temporary facts to supersede.

**Code to remove:**

| Component | What to remove |
|-----------|---------------|
| `internal/imprint/imprint.go` | `WithSessionID` IngestOption, `ingestOpts.sessionID` field, the `if o.sessionID != ""` block |
| `internal/db/store.go` | `SupersedeRealtimeBySession` method from Store interface |
| `internal/db/sqlite.go` | `SupersedeRealtimeBySession` implementation |
| `internal/ingest/batch.go` | The call to `SupersedeRealtimeBySession` after batch processing a transcript with `session` frontmatter |

No migration needed -- existing `realtime:*` facts in the DB are not affected. They remain as historical data. No new `realtime:*` facts will be created.

### 8.2 Old POST /ingest

Unchanged. No modifications to the handler, the Engine, or the extraction pipeline.

### 8.3 Old Query Pipeline

When hot_messages table is empty, the two new retrieval layers return empty results. RRF merge skips them. The query pipeline behaves identically to the current version. Zero overhead when hot phase is not in use.

### 8.4 Migration: Existing Deployments

Migration 014 creates new tables. It does not modify existing tables (except adding four columns to query_log). Existing data is untouched. The migration is safe for existing deployments:

- New tables are created with `IF NOT EXISTS`
- New indexes are created with `IF NOT EXISTS`
- ALTER TABLE uses `ADD COLUMN ... DEFAULT` (safe for existing rows)
- No data migration needed

---

## 9. Configuration

**New section in config.toml: `[hot]`**


| Key               | Type | Default | Description                                                                                                  |
| ----------------- | ---- | ------- | ------------------------------------------------------------------------------------------------------------ |
| `enabled`         | bool | `false` | Enable hot phase. When false, `POST /ingest` uses LLM extraction (old behavior), TTL goroutine does not start, query skips hot layers. |
| `ttl_minutes`     | int  | `60`    | How long messages stay in hot before moving to cooldown.                                                     |
| `tick_seconds`    | int  | `60`    | How often the TTL goroutine checks for expired messages.                                                     |
| `batch_size`      | int  | `100`   | Max messages moved per tick.                                                                                 |
| `embed_min_chars` | int  | `50`    | Minimum content length for embedding. Messages shorter than this get `has_embedding=0`.                      |


### 9.1 Config Struct

In `internal/config/config.go`:

```go
type HotConfig struct {
    Enabled       *bool `toml:"enabled"`
    TTLMinutes    int   `toml:"ttl_minutes"`
    TickSeconds   int   `toml:"tick_seconds"`
    BatchSize     int   `toml:"batch_size"`
    EmbedMinChars int   `toml:"embed_min_chars"`
}
```

Added to `Config`:

```go
type Config struct {
    // ... existing fields ...
    Hot HotConfig `toml:"hot"`
}
```

### 9.2 Effective Defaults

```go
func (c *Config) EffectiveHotConfig() HotConfig {
    h := c.Hot
    if h.Enabled == nil {
        enabled := false
        h.Enabled = &enabled
    }
    if h.TTLMinutes == 0 {
        h.TTLMinutes = 60
    }
    if h.TickSeconds == 0 {
        h.TickSeconds = 60
    }
    if h.BatchSize == 0 {
        h.BatchSize = 100
    }
    if h.EmbedMinChars == 0 {
        h.EmbedMinChars = 50
    }
    return h
}
```

### 9.3 Validation

No additional validation needed. All fields have safe defaults. `TTLMinutes = 0` would mean "move immediately" which is valid (though unusual). `Enabled = false` disables the entire hot phase.

### 9.4 config.toml.example Addition

```toml
# Hot phase (streaming message storage, Phase 1 of Hot-Cool-Cold Pipeline)
# [hot]
# enabled = true
# ttl_minutes = 60          # How long messages stay in hot before moving to cooldown
# tick_seconds = 60          # TTL check interval
# batch_size = 100           # Max messages moved per TTL tick
# embed_min_chars = 50       # Min content length for embedding (E021: 50 chars)
```

---

## 10. Tests

### 10.1 Migration 014


| Test                                       | Verifies                                                 |
| ------------------------------------------ | -------------------------------------------------------- |
| `TestMigration014_CreatesHotMessages`      | hot_messages table exists after migration                |
| `TestMigration014_CreatesCooldownMessages` | cooldown_messages table exists after migration           |
| `TestMigration014_CreatesFTS5Tables`       | hot_messages_fts and cooldown_messages_fts exist         |
| `TestMigration014_QueryLogColumns`         | hot_by_vector, hot_by_text, cooldown_by_vector, cooldown_by_text columns exist in query_log |
| `TestMigration014_Idempotent`              | Running migration twice does not error (IF NOT EXISTS)   |


### 10.2 Store: InsertHotMessage


| Test                                 | Verifies                                                                 |
| ------------------------------------ | ------------------------------------------------------------------------ |
| `TestInsertHotMessage_Basic`         | Message stored and retrievable                                           |
| `TestInsertHotMessage_FTS5Indexed`   | Message searchable via hot_messages_fts                                  |
| `TestInsertHotMessage_WithEmbedding` | Message with non-nil embedding stored in hot_messages.embedding BLOB + USearch index |
| `TestInsertHotMessage_NilEmbedding`  | Message with nil embedding stored without USearch entry, has_embedding=0  |
| `TestInsertHotMessage_DuplicateID`   | Returns error on duplicate ULID                                          |
| `TestInsertHotMessage_NilFields`     | Optional fields (platform, platform_session_id, linker_ref) can be empty |


### 10.3 Store: ListHotMessages


| Test                                    | Verifies                             |
| --------------------------------------- | ------------------------------------ |
| `TestListHotMessages_All`               | Returns all messages when no filter  |
| `TestListHotMessages_ByPlatformSession` | Filters by platform_session_id       |
| `TestListHotMessages_ByTimeRange`       | Filters by After/Before timestamps   |
| `TestListHotMessages_Limit`             | Respects limit parameter             |
| `TestListHotMessages_Empty`             | Returns empty slice when no messages |
| `TestListHotMessages_OrderByTimestamp`  | Results ordered by timestamp ASC     |


### 10.4 Store: SearchHotByText


| Test                               | Verifies                                       |
| ---------------------------------- | ---------------------------------------------- |
| `TestSearchHotByText_Match`        | Finds message by keyword                       |
| `TestSearchHotByText_NoMatch`      | Returns empty slice for unmatched query        |
| `TestSearchHotByText_Ranking`      | More relevant messages rank higher (BM25)      |
| `TestSearchHotByText_Sanitization` | FTS5 special characters are stripped           |
| `TestSearchHotByText_EmptyQuery`   | Returns empty slice for empty/whitespace query |


### 10.5 Store: SearchHotByVector


| Test                                 | Verifies                                                                 |
| ------------------------------------ | ------------------------------------------------------------------------ |
| `TestSearchHotByVector_Match`        | Finds message by embedding similarity via USearch                        |
| `TestSearchHotByVector_NoIndex`      | Returns empty slice when USearch index not loaded (graceful degradation) |
| `TestSearchHotByVector_NilEmbedding` | Returns empty slice for nil embedding                                    |
| `TestSearchHotByVector_Limit`        | Respects limit parameter                                                 |


### 10.6 Store: SearchCooldownByText


| Test                                    | Verifies                                            |
| --------------------------------------- | --------------------------------------------------- |
| `TestSearchCooldownByText_Match`        | Finds cooldown message by keyword                   |
| `TestSearchCooldownByText_NoMatch`      | Returns empty slice for unmatched query              |
| `TestSearchCooldownByText_EmptyTable`   | Returns empty slice when cooldown is empty           |
| `TestSearchCooldownByText_AfterMove`    | Message searchable in cooldown after MoveHotToCooldown |


### 10.7 Store: SearchCooldownByVector


| Test                                      | Verifies                                                 |
| ----------------------------------------- | -------------------------------------------------------- |
| `TestSearchCooldownByVector_Match`        | Finds cooldown message by embedding via USearch           |
| `TestSearchCooldownByVector_PrefixFilter` | Returns only "cool:" results, not "hot:" or "fact:"      |
| `TestSearchCooldownByVector_AfterMove`    | Message searchable in cooldown after MoveHotToCooldown   |


### 10.8 Store: MoveHotToCooldown


| Test                                          | Verifies                                                       |
| --------------------------------------------- | -------------------------------------------------------------- |
| `TestMoveHotToCooldown_Basic`                 | Messages older than cutoff moved to cooldown, deleted from hot |
| `TestMoveHotToCooldown_PreservesID`           | Same ULID in cooldown as was in hot                            |
| `TestMoveHotToCooldown_PreservesEmbedding`    | Embedding BLOB copied to cooldown_messages.embedding, USearch key re-prefixed |
| `TestMoveHotToCooldown_PreservesFTS5`         | FTS5 entry copied to cooldown_messages_fts                     |
| `TestMoveHotToCooldown_SetsMovedFromHot`      | moved_from_hot timestamp is set                                |
| `TestMoveHotToCooldown_BatchSize`             | Only moves up to batch_size messages per call                  |
| `TestMoveHotToCooldown_NewMessagesUnaffected` | Messages with timestamp after cutoff remain in hot             |
| `TestMoveHotToCooldown_EmptyHot`              | Returns 0 when no messages to move                             |
| `TestMoveHotToCooldown_Transaction`           | Failure during move rolls back -- no partial state             |


### 10.7 Vector Index (USearch, D36)

| Test | Verifies |
|------|----------|
| `TestVectorIndex_OpenCreate` | Creates new index when cache file missing |
| `TestVectorIndex_LoadExisting` | Loads existing cache file in <200ms |
| `TestVectorIndex_AddAndSearch` | Add vector with "hot:" prefix, search returns it |
| `TestVectorIndex_PrefixFilter` | Search with "hot:" prefix returns only hot messages, not facts |
| `TestVectorIndex_Remove` | Removed vector no longer appears in search |
| `TestVectorIndex_SaveAndReload` | Save, destroy, reload -- same search results |
| `TestVectorIndex_RebuildFromSQLite` | Rebuild from SQLite embeddings when cache file missing |
| `TestVectorIndex_ConcurrentReadWrite` | Concurrent Add + Search does not panic or corrupt |


### 10.8 API: POST /ingest (hot mode)

| Test | Verifies |
|------|----------|
| `TestIngestHot_Basic` | Returns 200 with id, has_embedding, hot=true when hot enabled |
| `TestIngestHot_MissingText` | Returns 400 (same as before) |
| `TestIngestHot_ShortContent` | has_embedding=false for content < 50 chars |
| `TestIngestHot_LongContent` | has_embedding=true for content >= 50 chars (when embedder available) |
| `TestIngestHot_NoEmbedder` | has_embedding=false when embedder is nil |
| `TestIngestHot_Disabled` | When hot.enabled=false, uses old LLM extraction path, returns IngestResult |
| `TestIngestHot_SourceParsing` | Extracts platform_session_id from "realtime:sessionId" source field |

### 10.9 MCP: imprint_ingest (hot mode)

| Test | Verifies |
|------|----------|
| `TestMCPIngestHot_Basic` | Returns message ID when hot enabled |
| `TestMCPIngestHot_MissingText` | Returns error |
| `TestMCPIngestHot_Disabled` | Uses old LLM extraction path when hot disabled |


### 10.10 Query: Hot Layers


| Test                                  | Verifies                                                          |
| ------------------------------------- | ----------------------------------------------------------------- |
| `TestQuery_HotLayersIncluded`         | Hot messages appear in query results                              |
| `TestQuery_CooldownLayersIncluded`    | Cooldown messages appear in query results                         |
| `TestQuery_HotCooldownColdMerged`     | Hot + cooldown + cold facts all merged via RRF                    |
| `TestQuery_HotOnlyWhenColdEmpty`      | Query works with only hot results (no facts in DB)                |
| `TestQuery_ColdOnlyWhenHotEmpty`      | Query works with only cold results (no hot/cooldown messages)     |
| `TestQuery_CooldownOnlyAfterTTL`      | After TTL moves messages to cooldown, they still appear in query  |
| `TestQuery_HotMessagesCited`          | Hot messages appear in citations with hot: prefix                 |
| `TestQuery_CooldownMessagesCited`     | Cooldown messages appear in citations with cool: prefix           |
| `TestQuery_EmptyHotAndCooldownTables` | Zero overhead when both hot and cooldown are empty                |


### 10.11 Context Builder

No hot search tests in Phase 1. Context builder is unchanged -- works with cold facts only. See section 7.8 for the deferred compaction-aware recall design.


### 10.12 TTL Goroutine


| Test                              | Verifies                                      |
| --------------------------------- | --------------------------------------------- |
| `TestHotTTL_MovesExpiredMessages` | Messages older than TTL are moved to cooldown |
| `TestHotTTL_KeepsFreshMessages`   | Messages younger than TTL remain in hot       |
| `TestHotTTL_GracefulShutdown`     | Goroutine exits when context is cancelled     |


### 10.13 Config


| Test                              | Verifies                                    |
| --------------------------------- | ------------------------------------------- |
| `TestHotConfig_Defaults`          | EffectiveHotConfig returns correct defaults |
| `TestHotConfig_Override`          | Config values override defaults             |
| `TestHotConfig_DisabledByDefault` | enabled defaults to false                   |


---

## Design Decisions Summary


| #   | Decision           | Choice                                              | Rationale                                                                                                                                            |
| --- | ------------------ | --------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------- |
| 1   | Hot ingest routing | Reuse `POST /ingest` and `imprint_ingest`, switch behavior by config | Every per-message caller already uses `/ingest`. New endpoint would require updating every hook, SKILL.md, AGENTS.md. Config flag `[hot] enabled` switches behavior server-side. Zero caller changes. |
| 2   | Embedding timing   | Synchronous                                         | Hook calls are fire-and-forget. 5-100ms latency is invisible to the user. Simplicity (no async tracking, no races) outweighs invisible latency cost. |
| 3   | Query integration  | Hot + cooldown as new layers in existing Querier    | Unified RRF merge. Single code path. Consistent latency accounting. Adding 4 layers (hot FTS5, hot vector, cooldown FTS5, cooldown vector) to existing 5 is the same pattern. Messages never disappear from search -- they move from hot layers to cooldown layers. |
| 4   | D27 replacement    | Hot phase replaces realtime path. Delete WithSessionID + SupersedeRealtimeBySession. | Hot phase solves the same problem (fresh data available before batch). Old mechanism produced temporary facts with 28% capture rate. Hot stores raw text at zero LLM cost. No temporary facts = no supersede needed. Dead code removed. |
| 5   | Context format     | "Recent Messages" section before "Relevant Context" | Hot data is freshest. Separate section with different format (raw messages vs structured facts) makes the source clear.                              |
| 6   | Vector search (D36) | USearch HNSW, single cache file, SQLite source of truth | sqlite-vec brute-force ~272ms at 200K -- over budget. USearch 1.1ms measured. One expendable cache file vs 4 vec0 tables. SQLite embedding column is source of truth. |


---

## Files Created / Modified

| File | Action | What changes |
|------|--------|-------------|
| `internal/db/migrations/014_hot_cool_pipeline.sql` | CREATE | New migration file (hot_messages, cooldown_messages, FTS5, embedding BLOB columns) |
| `internal/vecindex/` | CREATE | New package: VectorIndex interface, USearchIndex implementation, FNV-64 key mapping |
| `internal/model/types.go` | MODIFY | Add `HotMessage` struct |
| `internal/db/store.go` | MODIFY | Add 10 new methods + 2 new types to Store interface (incl. cooldown search) |
| `internal/db/sqlite.go` | MODIFY | Implement 10 new Store methods |
| `internal/api/api.go` | MODIFY | Add hot branch in handleIngest, `SetEmbedder`, `SetHotEnabled` |
| `internal/mcp/server.go` | MODIFY | Add hot branch in handleIngest, `SetEmbedder`, `SetHotEnabled` |
| `internal/query/query.go` | MODIFY | Add hot retrieval layers, extend RRF merge, update prompt |
| `internal/context/builder.go` | NO CHANGE | Hot search deferred to Phase 2 (section 7.8) |
| `internal/config/config.go` | MODIFY | Add `HotConfig` struct, `EffectiveHotConfig()` |
| `cmd/imprint/main.go` | MODIFY | Add TTL goroutine, open USearch index, pass to components |
| `config.toml.example` | MODIFY | Add `[hot]` section |
| `integrations/openclaw/hooks/imprint-ingest/handler.ts` | NO CHANGE | Hook continues to use `POST /ingest` -- behavior changes server-side |


