# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/).

## [Unreleased]

### Added

- **BVP-320:** optional **LLM dedup merge** on `Engine.Ingest()`: when smart dedup would skip, classify skip / supersede / merge (`prompts/merge-prompt.md` template). Supersede and merge retire the similar fact via `SupersedeFactByContradiction` with `dedup:supersede:` / `dedup:merge:` reason prefixes (same `valid_until` behavior as BVP-316). Merge stores LLM-combined content only; the raw extracted near-duplicate is discarded. Entity name collisions now union aliases. Config: `[quality] merge_on_dedup` (default off), `merge_prompt_path`; Prism task `X-Prism-Task: merge`. Store: `UpdateEntityAliases`.
- **BVP-316:** optional batch **contradiction review** on `Engine.Ingest()`: smart content dedup (cosine + Jaccard), neighbor retrieval with subject overlap, one LLM call per ingest, soft supersede via `SupersedeFactByContradiction` (`valid_until` + `superseded_by` + `supersede_reason`), `extraction_log` rows with `contradiction-check`, quality signal `contradiction_supersede_rate`. Config: `[quality] contradiction_detection`, `contradiction_prompt_path`; Prism task `X-Prism-Task: contradiction` when Prism mode is on.
- **BVP-368:** `imprint lint`: SQL-only knowledge base integrity diagnostics (no LLM). Flags: `--format=table|json`, `--check=` comma-separated subset (`chains`, `stale`, `dedup`, `embeddings`, `orphans`, `sources`, `consolidation`). `sources` resolves `facts.source_file` against `[watcher].path` when that path is set. Store methods on `db.Store` for each query.

### Changed

- `imprint lint`: exit status 1 when any ERROR-level check has findings; `sources` also reports paths that exist but cannot be `stat`d (e.g. permission errors); stale-facts action text clarifies `gc_after_days` vs immediate `valid_until` expiry.
- Reranker is now provider-agnostic and always enabled in query flow: default local cosine reranking (zero-config, no API call) with optional HTTP provider rerank via `[[providers.reranker]]`. Generic client targets `/v1/rerank` with Cohere `/v2/rerank` compatibility. Cohere-specific client removed.
- Added Prism mode via `[llm].base_url`: single-endpoint routing for extraction/query/consolidation/embedding/rerank with task headers (`X-Prism-Task`, `X-Prism-App`), direct `[[providers.*]]` chains ignored while active, and provider health checks skipped in this mode.

## [0.6.1] - 2026-04-05

### Fixed

- USearch SIGSEGV on AMD EPYC-Rome (Gitea #1): replaced pre-built USearch `.deb` package with CMake source build in Dockerfile, CI, and release workflows. The pre-built binary used SIMD instructions (likely AVX-512) unsupported by EPYC-Rome. Source build compiles with the CPU features available on the build machine, producing a compatible binary on any architecture.
- Dockerfile: new multi-stage `usearch-builder` stage builds USearch v2.24.0 from source. Builder and runtime stages COPY only the artifacts (`libusearch_c.so`, `usearch.h`). Eliminates `.deb` download and `dpkg` from both stages.
- CI (`ci.yml`): all three jobs (test, security, integration) now build USearch from source instead of installing `.deb`. Added missing explicit `CGO_CFLAGS`/`CGO_LDFLAGS` env vars to `govulncheck` and integration test steps (previously set via `GITHUB_ENV` which was removed with the `.deb` step).
- Release (`release.yml`): replaced `.deb` install with source build, aligned with CI and Dockerfile.

## [0.6.0] - 2026-04-05

### Added

- Cool Pipeline Phase 2: automatic extraction from cooldown messages via topic segmentation and background goroutine (BVP-355, BVP-356, BVP-357)
- Hybrid Union topic segmentation: TreeSeg + TT+Merge boundary union algorithm in new `internal/segment/` package. Clusters cooldown messages per session into topically coherent groups. Parameters: TreeSeg lambda=0.001 K=7, TT+Merge window=3 threshold=0.5 min_size=5. Graceful degradation when embeddings unavailable (Jaccard text similarity fallback).
- Cool extraction goroutine: background worker in new `internal/cooldown/` package. Polls for clusters ready for extraction on configurable tick interval. Two triggers: silence (8h, no new messages while other clusters active) and size (50 messages, safety valve). Formats cluster messages as `[speaker, YYYY-MM-DD HH:MM]: content` and calls `Engine.Ingest()`. Concurrency guard prevents double extraction.
- Transcript linking: batch ingest now links cooldown rows to transcripts by `platform_session_id`. When batch produces facts, linked cooldown rows are marked as processed (prevents duplicate extraction by cool goroutine).
- `CooldownMessage` type in `internal/model/types.go` with 14 fields (cluster_id, transcript_file, transcript_line, processed_at, moved_from_hot, etc.)
- `ScoredCooldownMessage` type for cooldown search results (replaces `ScoredHotMessage` for cooldown queries)
- Config: `[cool]` section (enabled, tick_seconds, silence_hours, max_cluster_size). Defaults: disabled, 300 sec tick, 8h silence, 50 max cluster. Validation: `[cool] enabled` requires `[hot] enabled`.
- Migration 015: `platform_session_id` column + index on `transcripts` table
- Store interface: 8 new methods (`ListCooldownUnclustered`, `AssignCooldownCluster`, `ListClustersReadyForExtraction`, `MarkClusterProcessed`, `LinkCooldownToTranscript`, `CoolPipelineStats`, `ListClusterMessages`, `MarkCooldownProcessedBySession`)
- `CooldownCluster` and `CoolStats` types in Store
- `GET /status` extended: `cool_stats` field with clusters_pending, clusters_extracted, messages_processed
- `Transcript.PlatformSessionID` field persisted from frontmatter `session` key
- 53 new tests across 5 packages (segment: 23, cooldown: 11, db: 12, config: 3, ingest: 4)
- D37: context delivery disabled by default. `[context] enabled` is now a `*bool` (nil = disabled). `ContextEnabled()` returns false unless explicitly set to `true`. `createBuilder()` returns nil when disabled, skipping context injection in serve, watch, and MCP modes.
- USearch SIGSEGV test guard: `IMPRINT_SKIP_USEARCH=1` env var skips USearch-dependent tests on platforms where the C library crashes (Apple Silicon M4 Max). CI (Linux) runs all tests.

### Fixed

- Hot FTS5 search: `SearchHotByText` used FTS5 alias `hf MATCH ?` which SQLite does not support. Fixed to `hot_messages_fts MATCH ?`.

### Changed

- `SearchCooldownByText` and `SearchCooldownByVector` now return `[]ScoredCooldownMessage` (was `[]ScoredHotMessage`). Cooldown search results now include cluster_id, transcript_file, transcript_line, processed_at fields.
- `MarkClusterProcessed` returns `(int64, error)` (was `error`) for concurrency guard row count check
- Query merge converts `CooldownMessage` to `HotMessage` via `cooldownToHot` helper for unified RRF ranking

### Known Limitations (v0.6.0)

- Transcript linking is one-directional: cooldown rows are linked only when batch ingest runs AFTER cooldown rows exist. Retroactive linking deferred to v0.6.1.
- Segmentation without embeddings uses Jaccard text similarity as fallback (similarity 0.5 neutral for messages without embeddings)
- Cool goroutine segmenter passes nil embeddings in current wiring (text-based segmentation only)

## [0.5.1] - 2026-04-05

### Fixed

- DB-vector index consistency: vector index updates now happen after database commits. If vector index update fails, database transaction has already succeeded and the error message indicates index is out of sync. Vector index is treated as a cache that can be rebuilt from database embeddings (source of truth).
- `InsertHotMessage`: vector index Add is best-effort after commit (won't block ingestion if index fails)
- `MoveHotToCooldown`: vector index re-prefix after commit, returns `nMoved` on index errors (preserves count of successfully moved messages)
- `UpdateFactEmbedding`, `UpdateChunkEmbedding`: vector index Add after DB update with descriptive errors
- `newEmbedderChainOptional`: nil config guard to prevent panic
- Release (goreleaser): set `CGO_CFLAGS=-I/usr/local/include` and full `CGO_LDFLAGS` with `-L/usr/local/lib` so cgo finds `usearch.h` (quoted `#include` in upstream binding; same as CI)
- `.github/workflows/release.yml`: install `gcc`, `libsqlite3-dev`, then USearch `.deb` before goreleaser (download to `/tmp` and remove so GoReleaser does not see a dirty git tree)
- `.github/workflows/ci.yml`: add CGO_CFLAGS to all build steps, disable macOS tests (CGO pointer panics), disable lint temporarily

## [0.5.0] - 2026-04-05

### Added

- Hot-Cool Pipeline Phase 1: raw message storage with instant FTS5 + USearch HNSW search, zero LLM cost (BVP-352, BVP-353)
- Three-phase query: 9 retrieval layers (hot vector, hot FTS5, cooldown vector, cooldown FTS5, fact vector, fact FTS5, chunk vector, chunk FTS5, graph). Hot and cooldown messages participate in RRF merge alongside structured facts (BVP-358)
- TTL goroutine: automatic hot -> cooldown migration with configurable interval and batch size. Background loop in `runServe` moves messages older than `ttl_minutes` (default 60) every `tick_seconds` (default 60).
- Message linker: `linker_ref` connects messages to their reply targets for context-dependent retrieval (BVP-354). Heuristic: links to latest prior hot row from other speaker in same `platform_session_id`.
- Optional post-retrieval reranker via cross-encoder API: Cohere `/v2/rerank` support in `query` package. Configured via `[[providers.reranker]]` and `[rerank]` sections. Reranks fact-only prefix; skips when hot/cooldown rows lead the merged list (BVP-317).
- Migration 014: `hot_messages`, `cooldown_messages` tables with FTS5 virtual tables, embedding BLOB columns, and four new `query_log` columns (`hot_by_vector`, `hot_by_text`, `cooldown_by_vector`, `cooldown_by_text`)
- Config: `[hot]` section (enabled, ttl_minutes, tick_seconds, batch_size, embed_min_chars). Defaults: disabled, 60 min TTL, 60 sec tick, 100 batch, 50 char threshold.
- Config: `[rerank]` section (top_n). Default: 0 (rerank full fact prefix).
- POST /ingest dual behavior: hot store (when enabled) or LLM extraction (default), with `mode=extract` override to force extraction when hot enabled
- "Fresh Messages" section in LLM synthesis prompt for hot/cooldown results. Format: `[hot:<id>] (<speaker>, <date> <time>): <content>`. Sorted newest-first. `linker_ref` shown as `(->ref)`.
- `HotMessageID` in `Citation` struct for hot message provenance: `{"hot_message_id": "hot:01JQ..."}` or `{"hot_message_id": "cool:01JR..."}`
- `DBStats` extended: `hot_messages` and `cooldown_messages` count fields
- Store interface: 9 new methods (`InsertHotMessage`, `ListHotMessages`, `SearchHotByText`, `SearchHotByVector`, `SearchCooldownByText`, `SearchCooldownByVector`, `MoveHotToCooldown`, `DeleteExpiredHot`, `CountHotMessages`) plus 2 for linker support (`GetRecentHotMessages`, `GetLinkedMessages`)
- Package `internal/ingest`: `linker.go` with `ApplyHotLinkerRef` for reply-to linking
- Retrieval merge experiments: optional `set-union` ordering (dense list, then FTS5-only, then graph-only) vs default RRF; `imprint eval-retrieval --merge-strategy`, `query.WithMergeStrategy` (E023)
- Migration 013: `transcript_chunks.embedding` BLOB column (BVP-365)
- Package `internal/vecindex`: load/save index, filtered search by key prefix, rebuild from stored embeddings
- USearch HNSW: single `.vecindex` sidecar file, 247x faster than sqlite-vec (~1.1ms vs ~272ms at 200K scale), f16 quantization, shared index for all vector tables with prefixed keys (`fact:`, `chunk:`, `hot:`, `cool:`)

### Changed

- Vector search uses USearch (C library + Go bindings) instead of sqlite-vec. Embeddings stored as BLOB columns on facts, chunks, hot_messages, cooldown_messages. Legacy sqlite-vec remains in module graph for migration.
- `GET /query` and MCP `imprint_query`: `facts_consulted` counts structured facts in the merged list only (excludes hot/cooldown raw rows).
- `imprint serve`: SIGINT/SIGTERM cancels the hot TTL context before exit so the TTL goroutine stops cleanly.
- `Querier.Retrieve()` filters out hot/cooldown messages, returns cold facts only (for retrieval eval). Full results with hot messages available via `Query()`.

### Removed

- D27 realtime path: `WithSessionID` IngestOption, `SupersedeRealtimeBySession` Store method. Hot phase replaces this mechanism. Existing `realtime:*` facts remain as historical data.

## [0.4.0] - 2026-03-20

### Added

- Extraction eval harness: CaRB-style P/R/F1, NRR, ECE, composite score, built-in golden dataset (42 examples)
- Retrieval eval harness: Recall@10, MRR, per-layer contribution, noise rejection, graceful degradation delta
- Built-in retrieval golden dataset: 32 facts, 33 entities, 21 questions across 5 categories
- Quality signal collection: supersede rate, confidence mean, entity collision rate per fact type
- Karpathy loop: automatic extraction prompt optimization (LLM mutation + eval gate, hourly rate limit)
- query_log table: per-query metrics (latency breakdown, per-layer counts, embedder availability)
- GET /relationships endpoint and imprint_relationships MCP tool (BVP-265)
- Entity deduplication: case-insensitive merge with relationship re-pointing
- Self-learning ingest filter: tracks rejected inputs for false-negative analysis
- Admin API: POST /admin/reset, DELETE /admin/facts, POST /admin/deduplicate-entities
- GET /status extended: quality signals + query stats (30-day aggregate)
- CLI: eval, eval generate, eval-retrieval, optimize subcommands
- Querier.Retrieve(): exported retrieval without LLM synthesis (for eval and programmatic use)
- Config: [quality] section (enabled, thresholds, optimized_prompt_path, mutation_prompt_path)

### Fixed

- 22 code review findings (S037): supersede_reason column, FTS sanitizer refactor, lint fixes
- Hook reliability: inline shared client (D29), periodic reachability recheck, timeout handling
- Consolidation: validate fact IDs exist before creating connections
- Deploy: Dockcheck replaces Watchtower, config.toml.example added
- Entity name normalization and dedup error handling
- Anthropic model name correction (claude-haiku-4 -> claude-haiku-4-5)

### Changed

- 345 tests (up from 253)
- 15 CLI subcommands (up from 12)
- MCP server: 8 tools (added imprint_relationships)
- HTTP API: 12 endpoints (added /relationships, /admin/*, /status extended)

## [0.3.0] - 2026-03-17

### Added

- GET /context HTTP endpoint: retrieval-only context (no LLM synthesis, 50-200ms) for hook integrations
- Context builder package (internal/context/): vector search + FTS5 + preferences + recent facts, structured markdown output
- MCP Resources: imprint://context/relevant, imprint://context/preferences, imprint://context/recent, imprint://context/entities/{name}
- Cursor hook integration: sessionStart hook, hooks.json + mcp.json templates, README
- Claude Code hook integration: SessionStart hook, settings.json + mcp.json templates, README
- Gemini CLI hook integration: SessionStart hook, settings.json template, GEMINI.md rules file, README
- Consolidation pre-clustering: group facts by embedding similarity before LLM call (BVP-229)
- Config: [context] section (recent_hours, max_facts, include_preferences) with defaults
- Config: [consolidation] pre_cluster, similarity_threshold, max_cluster_size settings
- CLI: `imprint context [HINT]` subcommand for hook integration
- DB: FactFilter.CreatedAfter for time-based fact queries

### Fixed

- Config: include_preferences=false was ignored when no other context fields were set

## [0.2.0] - 2026-03-17

### Added

- Version update check: background check against GitHub Releases API (once/day, non-blocking)
- CI pipeline: golangci-lint (8 linters), govulncheck, trufflehog secret scan, coverage reporting
- Integration tests: full pipeline test with real LLM (build tag gated, runs on main only)
- Pre-commit script: local lint + short tests
- Dual-layer session supersede: realtime facts replaced when transcript is batch-ingested
- Release script with conventional commits version bump detection
- Docker advertise URL for service discovery
- OpenClaw transcript hook for batch ingest path
- OpenRouter provider support (extraction, embedding, query) -- 300+ models, 29 free
- Voyage AI embedding provider -- 200M free tokens/year
- OpenRouter app identification headers (HTTP-Referer, X-Title)

### Fixed

- 26 lint issues in existing code (TrimSuffix, rangeValCopy, ifElseChain, errcheck)
- golangci-lint CI compatibility with Go 1.26.1 (action v7 + lint v2)
- Integration test provider config (BaseURL required for OpenAI)
- OpenClaw hooks: improved error handling, retry logic, timeout config

### Changed

- Platform files migrated to integrations/{platform}/ structure
- OpenClaw hooks: URL discovery, reachability check, port fallback
- Docs synced: README, API.md, ARCHITECTURE.md, TRANSCRIPT-FORMAT.md
- 253+ tests (up from 232)

## [0.1.0] - 2026-03-15

Initial release.

### Added

- Knowledge extraction from text via LLM (Google, Anthropic, Ollama provider chain with fallback)
- SQLite knowledge graph: facts, entities, relationships, consolidations, fact connections
- Vector search via sqlite-vec (KNN over facts and transcript chunks)
- Full-text search via FTS5 (facts and transcript chunks)
- Graph traversal via recursive CTE (depth-limited subgraph queries)
- Batch ingestion: process directories of transcript files with SHA-256 dedup
- File watcher: fsnotify-based directory monitoring (long-running)
- Consolidation: LLM-driven grouping of facts into insights and connections
- Self-evolving type taxonomy: signal collection, LLM review, shadow validation, auto-apply
- Embedding support: OpenAI and Ollama embedders with model metadata tracking
- Transcripts as first-class objects: metadata in DB, text on disk, back-references from facts
- Frontmatter parser: YAML frontmatter to transcript metadata
- ReadContext: load surrounding lines from transcript files for query enrichment
- Query layer: 5 parallel retrieval layers, Reciprocal Rank Fusion merge, LLM synthesis with citations
- Semantic dedup during ingest (cosine similarity, configurable threshold)
- Self-editing memory: update fact metadata or supersede facts with corrected content
- Context TTL: context-type facts auto-expire after configurable days
- GC: delete expired facts past a retention window
- Export: dump knowledge base as JSON or CSV
- MCP server: 7 tools (ingest, query, status, entities, graph, update_fact, supersede_fact), stdio transport
- HTTP API: 8 REST endpoints (ingest, query, status, entities, facts, graph, patch fact, supersede fact)
- CLI: 12 subcommands (ingest, ingest-dir, watch, consolidate, status, embed-backfill, query, serve, mcp, export, gc, version)
- Platform adapters: Cursor, Claude Code, OpenClaw (convert native JSONL to annotated markdown)
- OpenClaw hooks: deterministic integration via message:preprocessed hooks
- Agent integration skills: Cursor (SKILL.md) and Claude Code (AGENTS.md)
- Docker image: ghcr.io/aegis-alpha/imprint-mace
- CI: GitHub Actions (test on Ubuntu + macOS, Docker build on main, goreleaser on tag)
- 232+ tests

[0.6.0]: https://github.com/aegis-alpha/imprint-MACE/releases/tag/v0.6.0
[0.5.1]: https://github.com/aegis-alpha/imprint-MACE/releases/tag/v0.5.1
[0.5.0]: https://github.com/aegis-alpha/imprint-MACE/releases/tag/v0.5.0
[0.4.0]: https://github.com/aegis-alpha/imprint-MACE/releases/tag/v0.4.0
[0.3.0]: https://github.com/aegis-alpha/imprint-MACE/releases/tag/v0.3.0
[0.2.0]: https://github.com/aegis-alpha/imprint-MACE/releases/tag/v0.2.0
[0.1.0]: https://github.com/aegis-alpha/imprint-MACE/releases/tag/v0.1.0
