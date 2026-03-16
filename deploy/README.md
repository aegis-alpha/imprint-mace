# Deploying Imprint

## Prerequisites

- Docker and docker-compose
- API keys for at least one LLM provider (Google, OpenAI, or Anthropic)

## Quick start

1. Copy `.env.example` to `.env` and fill in your API keys
2. Adjust `config.toml` if needed (provider priorities, consolidation settings)
3. `docker-compose up -d`
4. Watchtower auto-updates the container when new commits are pushed to main

## Modes

**HTTP API (default):**

```
docker-compose up -d
```

**MCP (stdio transport):**

```
docker-compose -f docker-compose.yml -f docker-compose.mcp.yml up -d
```

## Volumes

| Mount | Container path | Purpose |
|-------|---------------|---------|
| `./data/` | `/data` | SQLite database (persistent) |
| `./config.toml` | `/etc/imprint/config.toml` | Configuration |
| `./transcripts/` | `/transcripts` | Transcript files for batch ingest |

Prompt files (extraction, consolidation, query) are baked into the image at `/etc/imprint/prompts/`.

## Auto-update

Watchtower checks ghcr.io every 5 minutes. The update cycle:

push to main -> CI builds Docker image -> push to ghcr.io:main -> Watchtower pulls -> container restart

## Migration / Bulk Ingest

Each `Engine.Ingest()` call triggers one LLM extraction request. Extraction takes 2-10 seconds per chunk depending on text length and provider speed. For large imports:

- Process in small batches (5-10 items at a time), not all at once
- Set `timeout_seconds = 120` for extraction providers (long texts may exceed 30s default)
- Implement retry with backoff in migration scripts -- transient API errors are normal
- Use `imprint ingest-dir` for file-based bulk import (handles chunking and dedup automatically)
- Monitor with `imprint status` between batches to verify progress

For 100+ facts, expect 5-15 minutes total processing time with cloud providers.

## Pin a version

Replace `:main` with a specific tag in `docker-compose.yml`:

```
image: ghcr.io/aegis-alpha/imprint-mace:v1.0.0
```

## OpenClaw Integration

When running Imprint in Docker alongside OpenClaw, set `IMPRINT_URL` to the Docker service name:

```bash
export IMPRINT_URL="http://imprint:8080"
```

Both services must be on the same Docker network. If using separate compose files, create a shared network.

The `imprint-ingest` hook fires on every message -- expect LLM extraction calls proportional to message volume. For high-traffic channels, consider increasing `timeout_seconds` on extraction providers and monitoring `imprint status` for queue depth.

In Docker, port conflicts don't apply (isolated network namespace), so auto-port fallback is not needed. The `IMPRINT_URL` env variable takes priority over `~/.imprint/serve.json` -- use it for Docker deployments.
