# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/).

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

[0.3.0]: https://github.com/aegis-alpha/imprint-MACE/releases/tag/v0.3.0
[0.2.0]: https://github.com/aegis-alpha/imprint-MACE/releases/tag/v0.2.0
[0.1.0]: https://github.com/aegis-alpha/imprint-MACE/releases/tag/v0.1.0
