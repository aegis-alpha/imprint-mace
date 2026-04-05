[![Release](https://img.shields.io/github/v/release/aegis-alpha/imprint-mace)](https://github.com/aegis-alpha/imprint-MACE/releases)
[![Agent](https://img.shields.io/badge/Agent-Claude_Code-blue)]()
[![Agent](https://img.shields.io/badge/Agent-OpenClaw-orange)]()
[![Agent](https://img.shields.io/badge/Agent-Cursor-black)]()
[![Agent](https://img.shields.io/badge/Agent-Gemini_CLI-4285F4)]()
[![Agent](https://img.shields.io/badge/Agent-Craft_Agents-grey)]()
[![Channel](https://img.shields.io/badge/MCP-red)]()
[![Channel](https://img.shields.io/badge/API-blue)]()
[![License](https://img.shields.io/badge/Open_Source-MIT-green)](https://github.com/aegis-alpha/imprint-mace/blob/main/LICENSE)
[![Changelog](https://img.shields.io/badge/CHANGELOG-green)](https://github.com/aegis-alpha/imprint-mace/blob/main/CHANGELOG.md)



# **Imprint** MACE 


Memory And Context Engine (MACE) for AI agents. Imprint turns conversations into a structured knowledge graph and uses it to form the agent's working context -- what it knows, what was decided, what matters right now. Cursor, Claude Code, any MCP client. Single Go binary, single SQLite file.

> **Status: Experimental (see badge for version)**
> Imprint is functional and deployed, but the API, MCP tools, config format, and database schema may change between versions. 405 tests pass, dual-layer memory works for OpenClaw, but multi-platform integration and production hardening are in progress. Feedback and contributions welcome.


## The Problem

AI agents forget everything between sessions. Every conversation starts from zero. Context is lost, decisions are repeated, preferences are re-explained. Bolting a vector database onto an LLM gives you fuzzy recall at best -- not structured knowledge you can query, connect, and reason over.

## How Imprint Works

Imprint watches transcript files, extracts facts, entities, and relationships, discovers connections between them, and autonomously evolves its type taxonomy based on what it sees. Conversation transcripts on disk are the source of truth -- the database is a derived index. Every fact links back to the exact lines in the original file, so you can always verify, enrich, and cross-reference.

**Hot-Cool-Cold Pipeline (v0.5.0 + v0.6.0):** When hot phase is enabled, incoming messages are stored immediately for instant search -- no LLM extraction, no latency, no cost. Messages are queryable within milliseconds via FTS5 and USearch HNSW vector search. After a configurable TTL (default 60 minutes), messages move to cooldown where they remain searchable. The cool pipeline (v0.6.0) automatically clusters cooldown messages by topic using Hybrid Union segmentation (TreeSeg + TT+Merge), then extracts triggered clusters through the standard Engine.Ingest() path. Batch transcript linking prevents duplicate extraction. The query pipeline searches all three phases (hot, cooldown, cold) simultaneously -- knowledge never disappears.

## Architecture

```
  ┌──────────────┐  ┌──────────────────┐  ┌──────────────┐
  │ Text files   │  │ Realtime API     │  │ File watcher │
  │ (transcripts)│  │ (HTTP/MCP hooks) │  │ (cron/poll)  │
  └──────┬───────┘  └──────┬───────────┘  └──────┬───────┘
         │                 │                      │
  ┌──────▼────────┐   ┌────▼────────┐            │
  │ Batch Adapter │   │ Hot Ingest  │            │
  │ (chunk, dedup)│   │ (if enabled)│            │
  └──────┬────────┘   └────┬────────┘            │
         │                 │                      │
         │          ┌──────▼───────────┐          │
         │          │  hot_messages    │          │
         │          │  (raw, FTS5+vec) │          │
         │          │  TTL -> cooldown │          │
         │          └──────────────────┘          │
         │                                        │
         └────────┬──────────────────────────────┘
                  │
         ┌────────▼──────────┐
         │  Engine.Ingest()  │  (Cold Path)
         │                   │
         │  1. Extract (LLM) │──── Provider Chain (Google/Anthropic/Ollama)
         │  2. Embed         │──── Embedder Chain (OpenAI/Voyage/Ollama)
         │  3. Store         │──── DB (SQLite + USearch sidecar)
         │  4. Log           │──── extraction_log
         └────────┬──────────┘
                  │
    ┌─────────────┼──────────────────┐
    │             │                  │
┌───▼──────┐ ┌────▼─────┐  ┌─────────▼──────────────────────┐
│Consoli-  │ │Taxonomy  │  │  Query (9 layers)              │
│dation    │ │Evolution │  │  1. Hot vector   (USearch)     │
│(bg loop) │ │(bg loop) │  │  2. Hot FTS5                   │
└──────────┘ └──────────┘  │  3. Cooldown vector (USearch)  │
                           │  4. Cooldown FTS5              │
                           │  5. Fact vector    (USearch)   │
                           │  6. Fact FTS5                  │
                           │  7. Chunk vector   (USearch)   │
                           │  8. Chunk FTS5                 │
                           │  9. Graph traversal            │
                           │  -> RRF merge                  │
                           │  -> ReadContext (disk)         │
                           │  -> LLM synthesis              │
                           └──────────┬─────────────────────┘
                                      │
                  ┌───────────────────┼───────────────────────────┐
                  │                   │                           │
         ┌────────▼─────────┐ ┌───────▼───────┐  ┌──────────▼──────────┐
         │    DB Layer      │ │  Transcript   │  │  Transport          │
         │  SQLite + USearch│ │  Files (disk) │  │  HTTP/MCP/CLI       │
         │                  │ │  (source of   │  │                     │
         │  facts, entities │ │   truth)      │  │                     │
         │  relationships   │ │               │  │                     │
         │  consolidations  │ └───────────────┘  └─────────────────────┘
         │  hot_messages    │
         │  cooldown_msgs   │
         │  transcript meta │
         │  taxonomy signals│
         │  extraction_log  │
         └──────────────────┘
```

*Note: When hot phase is disabled (default), the diagram simplifies -- no hot_messages/cooldown_messages tables, query uses 5 layers instead of 9.*

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
- **Query:** question -> parallel retrieval (cold: vector facts, vector chunks, FTS5 facts, FTS5 chunks, graph; when enabled: hot + cooldown vector/text) -> RRF or set-union merge -> post-merge rerank (cosine default, provider reranker optional; fact prefix only) -> ReadContext enrichment from disk -> LLM synthesis -> answer with citations (`facts_consulted` counts structured facts only; raw messages may appear in Fresh Messages as `[hot:…]` / `[cool:…]`).
- **Hot phase (opt-in, `[hot]` in config):** HTTP/MCP ingest can store raw messages without extraction; TTL moves them to cooldown; query merge includes both layers. Use `mode: "extract"` on ingest to force the LLM extraction path while hot is enabled.

## Self-Improving Architecture

Imprint doesn't just store knowledge -- it has six closed-loop mechanisms that make it better over time without human intervention.

```
                    ┌─────────────────────────────────────────────┐
                    │              Extraction Prompt               │
                    │         (rendered from taxonomy)             │
                    └──────────┬──────────────────────┬───────────┘
                               │                      │
                    ┌──────────▼──────────┐  ┌────────▼──────────┐
                    │  Taxonomy Evolution  │  │   Karpathy Loop   │
                    │  signals -> LLM     │  │  mutate -> eval   │
                    │  review -> apply    │  │  -> gate -> save  │
                    └──────────┬──────────┘  └────────┬──────────┘
                               │                      │
                    ┌──────────▼──────────────────────▼───────────┐
                    │            Quality Signals                   │
                    │  supersede rate, confidence, collisions,     │
                    │  citations, extraction log                   │
                    └──────────┬──────────────────────┬───────────┘
                               │                      │
                    ┌──────────▼──────────┐  ┌────────▼──────────┐
                    │   Consolidation     │  │  Provider Healing  │
                    │  cluster -> LLM     │  │  classify -> skip  │
                    │  -> insights        │  │  -> retry -> sub   │
                    └─────────────────────┘  └────────────────────┘
```

| Mechanism | What it does | How it closes the loop |
|-----------|-------------|----------------------|
| **Taxonomy Evolution** | Detects when the type system doesn't fit the data (custom types appearing, configured types unused, low confidence). LLM proposes add/remove/merge/rename. Validated proposals auto-apply. | Extraction signals -> proposals -> applied -> updated prompt -> different extraction behavior |
| **Karpathy Loop** | Automatically mutates the extraction prompt, evaluates against a golden dataset, keeps improvements, discards regressions. Rate-limited (1/hour, 24h pause after 3 failures). | Quality signals -> prompt mutation -> eval gate -> better prompt -> better extraction |
| **Quality Signals** | Computes production metrics from real data: supersede rate per fact type, confidence calibration, entity collision rate, fact citation frequency. | Feeds both Taxonomy Evolution and Karpathy Loop with data-driven improvement signals |
| **Consolidation** | Pre-clusters facts by embedding similarity, then LLM discovers connections (supports, contradicts, elaborates, caused_by, supersedes, precedes) and generates higher-order insights. | Raw facts -> clustered groups -> connections + insights -> richer knowledge graph |
| **Auto-Healing Providers** | Classifies errors (transient/auth/model_not_found), skips broken providers, retries after cooldown, substitutes models via prefix matching when configured model disappears. | Provider fails -> classify -> skip/retry -> find substitute -> resume extraction |
| **Fact Dedup** | Embedding-based cosine similarity check before storing. Prevents the knowledge base from accumulating duplicates as the same topics are discussed across sessions. | New fact -> embed -> compare existing -> skip if duplicate -> clean KB |

These mechanisms run autonomously. No configuration changes needed -- they activate based on data volume thresholds and quality signal patterns.

## Quick Start

### Build from source

```bash
# Requires Go 1.26+, a C compiler (SQLite + FTS5), and the USearch C library.
# Install USearch: https://github.com/unum-cloud/USearch/releases (e.g. .deb on Linux, .zip on macOS).
# Link the C library when building (example when it is not on the default linker path):
#   export CGO_LDFLAGS="-L/usr/local/lib -lusearch_c"
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
export OPENROUTER_API_KEY="your-key"   # optional -- 29 free models
export VOYAGE_API_KEY="your-key"       # optional -- 200M free tokens/year
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
# If the port is busy, tries the next available (up to +20)
# Writes actual address to ~/.imprint/serve.json for auto-discovery
./imprint serve

# Start MCP server (stdio transport, for Cursor/Claude Code)
./imprint mcp

# Generate embeddings for facts without them
./imprint embed-backfill

# Build context snapshot (for system prompt injection, no LLM)
./imprint context "current project topic"

# Export knowledge base
./imprint export --format=json --output=backup.json

# Evaluate extraction quality against golden set
./imprint eval --golden=testdata/golden/

# Evaluate retrieval quality (Recall@10, MRR, per-layer stats)
./imprint eval-retrieval

# Evaluate retrieval without embedder (graceful degradation test)
./imprint eval-retrieval --no-embedder

# Compare merge strategies (default: rrf; alternative: set-union dense-first ordering)
./imprint eval-retrieval --merge-strategy=set-union

# Run one prompt optimization cycle (Karpathy loop)
./imprint optimize

# Delete expired facts
./imprint gc
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
| `imprint_relationships` | List relationships, optionally filtered by type or entity |
| `imprint_graph` | Get the subgraph around an entity |
| `imprint_update_fact` | Update metadata on an existing fact (confidence, expiry, subject) |
| `imprint_supersede_fact` | Replace a fact with updated content, marking the old one as superseded |

### Platform Setup

Each platform has a dedicated setup guide with MCP config, hook scripts, and rules files:

| Platform | Hook | MCP | Rules | Setup Guide |
|----------|------|-----|-------|-------------|
| Cursor | sessionStart | Yes | SKILL.md | [integrations/cursor/](integrations/cursor/) |
| Claude Code | SessionStart | Yes | AGENTS.md | [integrations/claude-code/](integrations/claude-code/) |
| Gemini CLI | SessionStart | Yes | GEMINI.md | [integrations/gemini-cli/](integrations/gemini-cli/) |
| OpenClaw | Plugin (context) + 2 hooks (ingest, transcript) | Yes | AGENTS.md + TOOLS.md | [integrations/openclaw/](integrations/openclaw/) |

Hooks call `GET /context` (retrieval-only, 50-200ms). Agents that need full LLM-synthesized answers use the `imprint_query` MCP tool directly.

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
model = "claude-haiku-4-5"
api_key_env = "ANTHROPIC_API_KEY"
timeout_seconds = 30
priority = 2

[[providers.extraction]]
name = "ollama"
base_url = "http://localhost:11434"
model = "qwen3.5:27b"
timeout_seconds = 120
priority = 3

# OpenRouter -- single API key, 300+ models, 29 free
[[providers.extraction]]
name = "openrouter"
base_url = "https://openrouter.ai/api/v1"
model = "qwen/qwen3-next-80b:free"
api_key_env = "OPENROUTER_API_KEY"
timeout_seconds = 120
priority = 4

# Voyage AI -- high-quality embeddings, 200M free tokens/year
[[providers.embedding]]
name = "voyage"
base_url = "https://api.voyageai.com/v1"
model = "voyage-4-lite"
api_key_env = "VOYAGE_API_KEY"
timeout_seconds = 15
priority = 1
```

Provider detection is automatic: `"anthropic"` routes to the Anthropic Messages API, `"ollama"` routes to the Ollama native API, everything else uses the OpenAI-compatible API (covers OpenAI, Google, OpenRouter, Voyage AI, Groq, Together, Fireworks, vLLM, llama.cpp, LM Studio, and any other OpenAI-compatible endpoint).

### Prism Mode (Single Endpoint)

If you run a routing proxy (for example Prism), you can route all tasks through one endpoint:

```toml
[llm]
base_url = "http://localhost:8089/v1"
```

When `[llm].base_url` is set:
- all chat/extraction/consolidation/query/embedding/rerank calls go to that endpoint
- Imprint sends task headers on every request: `X-Prism-Task` and `X-Prism-App`
- `[[providers.*]]` chains are ignored
- provider health checks are skipped (managed by the proxy)
- model name is fixed to `"auto"` and selected by the proxy

### Supported Providers

| Provider | API | Free tier | Auth env var |
|----------|-----|-----------|--------------|
| Google Gemini | OpenAI-compatible | Generous | `GOOGLE_API_KEY` |
| OpenAI | Native | None | `OPENAI_API_KEY` |
| Anthropic | Native (Messages API) | None | `ANTHROPIC_API_KEY` |
| Ollama | Native | Self-hosted | None |
| OpenRouter | OpenAI-compatible | 29 free models | `OPENROUTER_API_KEY` |
| Voyage AI | OpenAI-compatible (embed) | 200M tokens/year | `VOYAGE_API_KEY` |

### Tested Models

**Extraction:** Gemini 2.5 Flash Lite, Claude Haiku 4, GPT-5 Nano, Qwen 3.5 27B (Ollama), Qwen3 Next 80B (OpenRouter free)

**Embedding:** OpenAI text-embedding-3-small, Voyage voyage-4-lite, nomic-embed-text (Ollama)

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

When signals accumulate past a threshold, an LLM review proposes taxonomy changes (add, remove, merge, rename). Proposals are validated in shadow mode -- the system runs sample extractions with the proposed taxonomy and compares results against the current one. If validation passes, the change is auto-applied. If it fails, the proposal is rejected with a reason.

The entire cycle is autonomous. No human intervention needed, though all proposals and their outcomes are logged for review. All proposal types (add, remove, merge, rename) are fully validated.

## Self-Tuning Quality

Imprint monitors its own extraction quality and automatically optimizes the extraction prompt.

**Quality signals** are computed from production data after every ingest batch -- no extra LLM calls, just SQL queries:

- **Supersede rate** per fact type -- how often facts get replaced (high rate = extraction is unstable)
- **Citation rate** per fact type -- how often facts are cited in queries (low rate = extraction produces unused facts)
- **Volume anomaly** -- unusual spikes or drops in extraction volume
- **Entity collision rate** -- how often new entities collide with existing ones during dedup
- **Confidence calibration** -- are confidence scores accurate or inflated?
- **Confidence-citation calibration** -- do high-confidence facts get cited more often?

When signals exceed thresholds, the **Karpathy loop** kicks in:

1. Send the current extraction prompt + signal summary to an LLM
2. LLM produces a candidate prompt
3. Run the candidate against the golden eval dataset
4. If composite score improves -- keep; otherwise discard

The original prompt is never modified. Rate-limited to 1 attempt per hour; 3 consecutive failures pause for 24 hours. Cost: ~$0.004 per attempt.

Run manually: `imprint optimize`. Runs automatically after `ingest-dir`, `watch`, and `serve` when signals warrant it.

## Project Status

### What works

- **Knowledge extraction:** facts, entities, and relationships from any text via LLM, with semantic dedup and configurable type taxonomy
- **Hot-Cool-Cold Pipeline (Phase 1 v0.5.0, Phase 2 v0.6.0):** realtime messages stored raw for instant search (zero LLM cost). TTL moves messages from hot to cooldown. Query searches all three phases (9 layers: hot vector, hot FTS5, cooldown vector, cooldown FTS5, fact vector, fact FTS5, chunk vector, chunk FTS5, graph). Hot messages appear in results as "Fresh Messages" alongside structured facts. Phase 2: Hybrid Union topic segmentation clusters cooldown messages per session, background goroutine extracts triggered clusters via Engine.Ingest(), transcript linking connects cooldown rows to batch-ingested transcripts and prevents duplicate extraction.
- **Hybrid query:** 9 parallel retrieval layers when hot enabled (5 when disabled), RRF or set-union merge, post-merge reranking (cosine default plus provider-agnostic HTTP reranker option), ReadContext enrichment from source files, LLM synthesis with citations
- **Self-evolving taxonomy:** signal collection from extraction results, LLM review, validated proposals, auto-apply -- fully autonomous
- **Platform integrations:** deterministic hooks for OpenClaw, Cursor, Claude Code, Gemini CLI. MCP server (8 tools) for any MCP client. HTTP API (9 main + 3 admin endpoints).
- **Consolidation:** background grouping of related facts, connection discovery, higher-order insights
- **Transcript-first storage:** files on disk are the source of truth, DB is a derived index with back-references to file + line range
- **Self-editing memory:** agents can update fact metadata or supersede facts with corrected content via MCP tools or HTTP API
- **USearch HNSW vector index:** single `.vecindex` sidecar file, 247x faster than sqlite-vec (~1.1ms vs ~272ms at 200K scale), f16 quantization, SQLite embedding BLOBs as source of truth
- **Eval harness:** extraction eval (CaRB-style P/R/F1, NRR, ECE, composite score) + retrieval eval (Recall@10, MRR, per-layer contribution, graceful degradation delta). Built-in golden datasets for both.
- **Self-tuning quality:** 6 quality signal collectors (supersede rate, citation rate, volume anomaly, entity collision rate, confidence calibration, confidence-citation calibration), Karpathy loop for automatic prompt optimization, query_log instrumentation
- **17 CLI subcommands**, 405 tests, Docker deployment with Watchtower auto-update

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
| SQLite (single embedded file) | Graph via recursive CTE (12ms at 200K rels), vector similarity via USearch (sidecar index + embedding BLOBs), FTS via FTS5. No separate server, no runtime database service. |
| Library-first | Core is functions, not a server. Embed in your app or wrap with any transport. |
| ULID for IDs | Chronologically sortable, important for temporal ordering of facts |
| Config-driven taxonomy | Types in TOML, rendered into prompts at runtime. Change types without changing code. |
| Provider chain with auto-healing | No single point of failure. If one LLM is down, the next is tried automatically. Error classification (transient vs auth vs model-not-found) drives retry logic. Exhausted providers are flagged in the knowledge base. Model substitution via prefix matching when configured models disappear. |
| Transcripts as source of truth | DB is a derived index. Files on disk hold the full conversation. Facts back-reference file + line range. Query enriches from disk. |
| Embedding model metadata | Each embedding stored with model name. On provider switch: selective re-embedding or adapter -- no full re-embedding needed. |
| USearch sidecar index | Vector ANN lives in a `.vecindex` file next to the DB; dimensions come from config at runtime. Embeddings are also stored as BLOBs on rows for durability. 247x faster than sqlite-vec at 200K scale. |
| Hot-Cool Pipeline (opt-in) | Raw message storage with instant FTS5 + vector search (zero LLM cost). TTL moves messages to cooldown. Cool pipeline clusters by topic and extracts via Engine.Ingest(). Transcript linking prevents duplicate extraction. Query searches hot + cooldown + cold simultaneously. Messages never disappear. |

## Contributing

Contributions are welcome. Please open an issue to discuss what you'd like to change before submitting a PR.

```bash
# Run tests (requires CGO for SQLite + FTS5 + USearch C library)
CGO_ENABLED=1 go test -tags sqlite_fts5 ./...
```

## License

[MIT](LICENSE)

**There is NO WARRANTY for this software.**


GitHub: https://github.com/aegis-alpha/imprint-mace

Copyright 2026 Aegis Alpha SA