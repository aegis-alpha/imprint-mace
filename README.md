[![Agent](https://img.shields.io/badge/Agent-Claude_Code-blue)]()
[![Agent](https://img.shields.io/badge/Agent-OpenClaw-orange)]()
[![Agent](https://img.shields.io/badge/Agent-Cursor-black)]()
[![Agent](https://img.shields.io/badge/Agent-Craft_Agents-grey)]()
[![Channel](https://img.shields.io/badge/MCP-red)]()
[![Channel](https://img.shields.io/badge/API-blue)]()
[![License](https://img.shields.io/badge/Open_Source-MIT-green)]()



# **Imprint** MACE 


Memory And Context Engine (MACE) for AI agents. Imprint turns conversations into a structured knowledge graph and uses it to form the agent's working context -- what it knows, what was decided, what matters right now. Cursor, Claude Code, any MCP client. Single Go binary, single SQLite file. Experimental.

## The Problem

AI agents forget everything between sessions. Every conversation starts from zero. Context is lost, decisions are repeated, preferences are re-explained. Bolting a vector database onto an LLM gives you fuzzy recall at best -- not structured knowledge you can query, connect, and reason over.

## How Imprint Works

Imprint watches transcript files, extracts facts, entities, and relationships, discovers connections between them, and autonomously evolves its type taxonomy based on what it sees. Conversation transcripts on disk are the source of truth -- the database is a derived index. Every fact links back to the exact lines in the original file, so you can always verify, enrich, and cross-reference.

## Architecture

```
  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐
  │ Text files   │  │ Realtime API │  │ File watcher │
  │ (transcripts)│  │ (HTTP/MCP)   │  │ (cron/poll)  │
  └──────┬───────┘  └──────┬───────┘  └──────┬───────┘
         │                 │                  │
  ┌──────▼────────┐        │                  │
  │ Batch Adapter │        │                  │
  │ (chunk, dedup)│        │                  │
  └──────┬────────┘        │                  │
         └────────┬────────┘──────────────────┘
                  │
         ┌────────▼──────────┐
         │  Engine.Ingest()  │
         │                   │
         │  1. Extract (LLM) │──── Provider Chain (Google/Anthropic/Ollama)
         │  2. Embed         │──── Embedder Chain (OpenAI/Ollama)
         │  3. Store         │──── DB (SQLite + sqlite-vec)
         │  4. Log           │──── extraction_log
         └────────┬──────────┘
                  │
    ┌─────────────┼──────────────────┐
    │             │                  │
┌───▼──────┐ ┌────▼─────┐  ┌─────────▼──────────────┐
│Consoli-  │ │Taxonomy  │  │  Query                 │
│dation    │ │Evolution │  │  1. Vector (facts)     │
│(bg loop) │ │(bg loop) │  │  2. Vector (chunks)    │
└──────────┘ └──────────┘  │  3. FTS5 (chunks)      │
                           │  4. Graph traversal    │
                           │  5. ReadContext (disk) │
                           │  6. LLM synthesis      │
                           └──────────┬─────────────┘
                                      │
                  ┌───────────────────┼───────────────────┐
                  │                   │                   │
         ┌────────▼─────────┐ ┌───────▼───────┐  ┌────────▼────────┐
         │    DB Layer      │ │  Transcript   │  │  Transport      │
         │  SQLite + vec0   │ │  Files (disk) │  │  HTTP/MCP/CLI   │
         │                  │ │  (source of   │  │                 │
         │  facts, entities │ │   truth)      │  │                 │
         │  relationships   │ │               │  │                 │
         │  consolidations  │ └───────────────┘  └─────────────────┘
         │  transcript meta │
         │  taxonomy signals│
         │  extraction_log  │
         └──────────────────┘
```

**Library-first.** The core is a set of Go functions -- `ingest`, `query`, `consolidate`, `status`. Transport wrappers (HTTP API, MCP server, CLI) are thin layers on top. You can embed Imprint directly in your Go application or run it as a standalone service.

**Two-layer storage.** Transcript files on disk are the source of truth. The SQLite database is a derived index with back-references (file path + line range) to the original text. This means:
- Every fact traces back to the exact conversation that produced it
- Query can enrich answers with surrounding context from the original file
- Deleting the DB loses the index, not the data -- re-ingest rebuilds it

**Data flow:**
- **Ingest (batch):** transcript file -> batch adapter (chunk, dedup) -> Engine.Ingest() -> extract + embed + store (with back-references to source file + lines)
- **Ingest (realtime):** text via API -> Engine.Ingest() -> same path
- **Consolidation (background):** unconsolidated facts -> LLM grouping -> insights + fact connections
- **Taxonomy evolution (background):** signals from extraction log -> LLM review -> validated proposals -> auto-apply
- **Query:** question -> 5 parallel retrieval layers (vector facts, vector chunks, FTS5 facts, FTS5 chunks, graph traversal) -> RRF merge -> ReadContext enrichment from disk -> LLM synthesis -> answer with citations

## Quick Start

### Build from source

```bash
# Requires Go 1.26+ and a C compiler (for SQLite + FTS5)
git clone https://github.com/aegis-alpha/imprint-mace.git
cd imprint-mace
CGO_ENABLED=1 go build -tags sqlite_fts5 -o imprint ./cmd/imprint
```

### Configure

```bash
cp config.toml.example config.toml
```

Set your LLM provider API keys as environment variables:

```bash
export GOOGLE_API_KEY="your-key"
export OPENAI_API_KEY="your-key"
export ANTHROPIC_API_KEY="your-key"
```

The config file defines provider chains (ordered fallback), type taxonomy, and prompt paths. See `config.toml.example` for the full reference.

### Run

```bash
# Ingest text from stdin
echo "Alice decided to use Go for Acme." | ./imprint ingest

# Process a directory of transcripts (cron-friendly)
./imprint ingest-dir /path/to/transcripts

# Watch a directory for new/changed files (long-running)
./imprint watch /path/to/transcripts

# Run consolidation manually
./imprint consolidate

# Show database statistics
./imprint status

# Ask a question against the knowledge base
./imprint query "What language is Acme written in?"

# Start HTTP API server (default 127.0.0.1:8080)
./imprint serve

# Start MCP server (stdio transport, for Cursor/Claude Code)
./imprint mcp

# Generate embeddings for facts without them
./imprint embed-backfill
```

Use `--config` to specify a config file (default: `config.toml`, env: `IMPRINT_CONFIG`):

```bash
./imprint --config /etc/imprint/config.toml ingest-dir /data/transcripts
```

Schedule with cron for automatic ingestion:

```
*/15 * * * * /usr/local/bin/imprint ingest-dir /data/transcripts
```

## Deploy with Docker

```bash
cd deploy
cp .env.example .env   # fill in API keys
docker-compose up -d
```

Watchtower auto-updates the container on every push to main. See [deploy/README.md](deploy/README.md) for details.

## Use as a Library

Imprint is library-first. You can embed it directly in your Go application:

```go
store, _ := db.Open("memory.db")
defer store.Close()

eng := imprint.New(extractor, store, embedder, 0, 7, logger)
result, _ := eng.Ingest(ctx, "Alice prefers dark mode.", "session-42.md")
// result.FactsCount, result.EntitiesCount, result.RelationshipsCount
```

The CLI and MCP server are thin wrappers around the same `Engine.Ingest()` call.

## Use with AI Agents (MCP)

Imprint includes an MCP server for integration with Cursor, Claude Code, and other MCP-compatible agents. It runs over stdio -- no HTTP server or port needed.

### Available Tools

| Tool | Description |
|------|-------------|
| `imprint_ingest` | Extract facts, entities, and relationships from text |
| `imprint_query` | Ask a question against the knowledge base, get answer with citations |
| `imprint_status` | Show knowledge base statistics |
| `imprint_entities` | List entities, optionally filtered by type |
| `imprint_graph` | Get the subgraph around an entity |
| `imprint_update_fact` | Update metadata on an existing fact (confidence, expiry, subject) |
| `imprint_supersede_fact` | Replace a fact with updated content, marking the old one as superseded |

### Cursor

Add to `.cursor/mcp.json`:

```json
{
  "mcpServers": {
    "imprint": {
      "command": "/path/to/imprint",
      "args": ["mcp", "--config", "/path/to/config.toml"]
    }
  }
}
```

### Claude Code

Add to `.claude/settings.json`:

```json
{
  "mcpServers": {
    "imprint": {
      "command": "/path/to/imprint",
      "args": ["mcp", "--config", "/path/to/config.toml"]
    }
  }
}
```

### OpenClaw

OpenClaw integrates via hooks -- deterministic, fires on every message without relying on the model to call tools.

**Install hooks:**

```bash
cp -r tools/hooks/openclaw/imprint-ingest ~/.openclaw/hooks/
cp -r tools/hooks/openclaw/imprint-query ~/.openclaw/hooks/
```

**Configure:**

```bash
export IMPRINT_URL="http://your-imprint-server:8080"
```

**What the hooks do:**

- `imprint-ingest` -- sends every message to `POST /ingest` for knowledge extraction (fire-and-forget)
- `imprint-query` -- calls `GET /query` with the message content and injects the answer as context for the agent (5s timeout)

Both hooks listen for `message:preprocessed` events and use `bodyForAgent` (enriched body after media/link understanding), falling back to `body`.

## Data Model

Imprint extracts three core structures from conversations:

### Facts

Atomic pieces of knowledge with type, confidence, and temporal validity.

| Type | Description | Example |
|------|-------------|---------|
| preference | Personal taste, tool preference | "Alice prefers dark mode" |
| decision | A choice that was made | "Decided to use Go instead of Rust" |
| rule | Standing instruction or constraint | "Never deploy on Fridays" |
| project | Information about a project | "Acme uses SQLite for storage" |
| contact | A person's role or relation | "Bob leads the API team" |
| bio | Biographical information | "Alice is based in Berlin" |
| context | Situational state (may expire) | "node-1 is offline since 04:00 UTC" |
| lesson | Something learned from experience | "Local models hallucinate on consolidation" |
| workflow | Multi-step procedure | "To deploy: push, wait for CI, migrate, restart" |
| goal | Desired outcome | "Ship the MVP by end of Q2" |
| event | Time-bound occurrence | "node-2 outage on 2026-03-14" |
| skill | Capability of an entity | "Alice knows Rust, Go, and SQL" |

### Entities

Distinct things that facts are about: people, projects, tools, servers, concepts, organizations, locations, documents, agents. Each entity has a canonical name and optional aliases for deduplication.

### Relationships

Directed connections between entities: `owns`, `uses`, `works_on`, `depends_on`, `related_to`, `created_by`, `part_of`, `manages`, `located_at`.

### Consolidation Layer

During consolidation, Imprint discovers connections between facts (`supports`, `contradicts`, `elaborates`, `caused_by`, `supersedes`, `precedes`) and synthesizes higher-order insights from groups of related facts.

## Configuration

Imprint uses TOML for configuration with environment variables for secrets.

### Provider Chain

Providers are tried in priority order. If the first fails (timeout, rate limit, server error), the next is tried automatically.

```toml
[[providers.extraction]]
name = "google"
base_url = "https://generativelanguage.googleapis.com/v1beta/openai"
model = "gemini-2.5-flash-lite"
api_key_env = "GOOGLE_API_KEY"
timeout_seconds = 30
priority = 1

[[providers.extraction]]
name = "anthropic"
base_url = "https://api.anthropic.com"
model = "claude-haiku-4"
api_key_env = "ANTHROPIC_API_KEY"
timeout_seconds = 30
priority = 2

[[providers.extraction]]
name = "ollama"
base_url = "http://localhost:11434"
model = "qwen3.5:27b"
timeout_seconds = 120
priority = 3
```

Provider detection is automatic: `"anthropic"` routes to the Anthropic Messages API, `"ollama"` routes to the Ollama native API, everything else uses the OpenAI-compatible API (covers OpenAI, Google, Groq, Together, Fireworks, vLLM, llama.cpp, LM Studio, and any other OpenAI-compatible endpoint).

### Tested Models

**Extraction:** Gemini 2.5 Flash Lite, Claude Haiku 4, GPT-5 Nano, Qwen 3.5 27B (local via Ollama)

**Embedding:** OpenAI text-embedding-3-small, nomic-embed-text (Ollama)

### Type Taxonomy

All 36 types (12 fact, 9 entity, 9 relationship, 6 connection) are defined in `config.toml` and rendered into extraction prompts at runtime. To add a new type, add it to the config -- no code changes needed.

```toml
[[types.fact_types]]
name = "preference"
description = "Personal taste, style choice, tool preference"
example = "Alice prefers dark mode in all editors"
```

## Self-Evolving Taxonomy

This is the part that makes Imprint different from a static extraction pipeline.

The system collects signals about its own type system during normal operation -- no extra LLM calls, just SQL queries over extraction results:

- **Custom frequency:** an unnamed type keeps appearing in extractions
- **Type unused:** a defined type is never matched
- **Low confidence:** extractions consistently score low confidence for a type
- **Type overlap:** two types are used interchangeably

When signals accumulate past a threshold, an LLM review proposes taxonomy changes (add, remove, merge, rename). Proposals are validated in shadow mode -- the system runs sample extractions with the proposed taxonomy and compares results against the current one. If validation passes, the change is auto-applied. If it fails, the proposal is rejected with a reason.

The entire cycle is autonomous. No human intervention needed, though all proposals and their outcomes are logged for review. All proposal types (add, remove, merge, rename) are fully validated.

## Project Status

### What works

- Extract facts, entities, and relationships from any text via LLM
- Store in local SQLite knowledge graph with vector search (sqlite-vec) and full-text search (FTS5)
- Batch process directories of transcript files with SHA-256 dedup
- Watch directories for new/changed files (fsnotify)
- Consolidate facts into higher-order insights (background or manual)
- Self-evolving type taxonomy (signal collection, LLM review, auto-apply)
- Embedding support (OpenAI, Ollama) with model metadata tracking
- Transcripts as first-class objects: metadata in DB, text on disk, back-references from facts to source file + line range
- Frontmatter parser: YAML frontmatter -> transcript metadata (source, session, date, participants, topic)
- Standardized transcript input format: platform adapters convert native JSONL to annotated markdown
- ReadContext: load surrounding lines from transcript files on disk to enrich query answers
- Query layer: hybrid retrieval across 5 parallel layers (vector facts, vector chunks, FTS5 facts, FTS5 chunks, graph traversal), Reciprocal Rank Fusion merge, ReadContext enrichment, LLM synthesis with citations
- MCP server (stdio transport, 7 tools) for Cursor, Claude Code, and other MCP-compatible agents
- HTTP API: 8 REST endpoints (POST /ingest, GET /query, GET /status, GET /entities, GET /facts, GET /graph/{id}, PATCH /facts/{id}, POST /facts/{id}/supersede)
- Platform adapters: Cursor, Claude Code, OpenClaw (Python scripts in tools/adapters/)
- OpenClaw hooks: deterministic integration via message:preprocessed hooks (imprint-ingest for realtime knowledge extraction, imprint-query for automatic context retrieval)
- Self-editing memory: agents can update fact metadata or supersede facts with corrected content
- Semantic dedup during ingest: cosine similarity check skips near-duplicate facts (configurable threshold)
- Context TTL: context-type facts auto-expire after configurable days
- GC: delete expired facts past a retention window
- Export: dump entire knowledge base as JSON or CSV
- Agent integration skills: Cursor (SKILL.md) and Claude Code (AGENTS.md)
- 11 CLI subcommands: `ingest`, `ingest-dir`, `watch`, `consolidate`, `status`, `embed-backfill`, `query`, `serve`, `mcp`, `export`, `gc`
- 238 tests

## Benchmarks

Measured on commodity hardware (single-core SQLite, no tuning).

**Ingestion:**
- 18,000 facts/second insert throughput
- Graph traversal (depth 4, 200K relationships): 12ms
- Full-text search (2M records): 35ms

**Consolidation (156 facts, Gemini Flash-Lite):**
- Latency: 5.5 seconds
- Cost: $0.002
- Coverage: 100% (zero fact loss on standard workloads)

**At scale (2M facts, 80K entities, 200K relationships):**
- Graph queries: under 100ms
- FTS5 keyword search: under 100ms
- Database size: 16GB (fits on any VPS)


## Design Decisions

| Decision | Rationale |
|----------|-----------|
| Go, single binary | Cross-platform, no runtime deps, goroutines for concurrency |
| SQLite (single embedded file) | Graph via recursive CTE (12ms at 200K rels), vector via sqlite-vec, FTS via FTS5. No separate server, no runtime dependencies. |
| Library-first | Core is functions, not a server. Embed in your app or wrap with any transport. |
| ULID for IDs | Chronologically sortable, important for temporal ordering of facts |
| Config-driven taxonomy | Types in TOML, rendered into prompts at runtime. Change types without changing code. |
| Provider chain with fallback | No single point of failure. If one LLM is down, the next is tried automatically. |
| Transcripts as source of truth | DB is a derived index. Files on disk hold the full conversation. Facts back-reference file + line range. Query enriches from disk. |
| Embedding model metadata | Each embedding stored with model name. On provider switch: selective re-embedding or adapter -- no full re-embedding needed. |
| vec0 created programmatically | sqlite-vec virtual table created in Go code (not SQL migration) because dimensions come from config at runtime. |

## Contributing

Contributions are welcome. Please open an issue to discuss what you'd like to change before submitting a PR.

```bash
# Run tests (requires CGo for SQLite + FTS5)
CGO_ENABLED=1 go test -tags sqlite_fts5 ./...
```

## License

[MIT](LICENSE)

**There is NO WARRANTY for this software.**


GitHub: https://github.com/aegis-alpha/imprint-mace

Copyright 2026 Aegis Alpha SA