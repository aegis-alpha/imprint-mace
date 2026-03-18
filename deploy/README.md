# Deploying Imprint

## Prerequisites

- Docker and docker-compose
- API keys for at least one LLM provider (Google, OpenAI, or Anthropic)

## Quick start

```bash
cd deploy/
cp .env.example .env          # fill in your API keys
cp config.toml.example config.toml  # edit provider priorities, models, etc.
docker-compose up -d
```

1. **`.env`** -- API keys (GOOGLE_API_KEY, OPENAI_API_KEY, ANTHROPIC_API_KEY). At least one is required.
2. **`config.toml`** -- provider chains, consolidation settings, embedding config. The example uses Google Gemini as primary with OpenAI and Anthropic as fallbacks. Edit to match your API keys.
3. **`docker-compose up -d`** -- starts Imprint + Watchtower (auto-updates from ghcr.io)

Both `config.toml` and `.env` must exist before running docker-compose. If `config.toml` is missing, Docker creates an empty directory instead of a file and Imprint will fail to start.

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

The docker-compose mounts `~/.imprint` from the host so that `serve.json` (written by Imprint on startup) is visible to hooks running outside the container.

`IMPRINT_ADVERTISE_URL` controls what URL is written to `serve.json`. Set it to the host-side address (matching the `ports:` mapping). If you change the port mapping, update both:

```yaml
ports:
  - "127.0.0.1:8890:8080"
environment:
  - IMPRINT_ADVERTISE_URL=http://127.0.0.1:8890
```
