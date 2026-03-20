# Imprint as OpenClaw Memory Backend -- Test Deployment

Test deployment for BVP-277. Validates Imprint as OpenClaw memory backend
before touching production Kevin on Hetzner.

## Architecture

```
+-------------------+       +-------------------+
|   OpenClaw agent  | stdio |   imprint-mcp     |
|   (test, :3100)   |------>|   (MCP server)    |
+-------------------+       +--------+----------+
                                     |
                              shared /data volume
                                     |
                            +--------+----------+
                            |   imprint         |
                            |   (HTTP, :8180)   |
                            +-------------------+
```

Separate Docker network, separate data directory. No shared state
with production.

## Quick Start

```bash
cp .env.example .env        # fill in API keys
cp config.toml.example config.toml
docker-compose up -d
```

Verify Imprint is running:

```bash
curl http://127.0.0.1:8180/status
```

## Testing Phases

### Phase 1: Parallel (both memory_search and Imprint)

Copy the parallel config:

```bash
cp openclaw.json.parallel openclaw.json
```

Uncomment the `openclaw` service in `docker-compose.yml`, then:

```bash
docker-compose up -d openclaw
```

Both memory_search and Imprint MCP tools are available. Compare results.

### Phase 2: Replace (Imprint only, memory_search disabled)

Copy the replace config:

```bash
cp openclaw.json.replace openclaw.json
docker-compose restart openclaw
```

memory_search and memory_get are denied. Only Imprint tools available.

## Verification Checklist

- [ ] Agent discovers imprint_query tool
- [ ] Agent can query Imprint and get results
- [ ] Ingest hook works (facts appear after conversation)
- [ ] Context injection works (GET /context returns data)
- [ ] (Phase 2) Agent does not use memory_search
- [ ] (Phase 2) No errors in logs after 24h

## Ports

| Service       | Internal | External         |
|---------------|----------|------------------|
| Imprint HTTP  | 8080     | 127.0.0.1:8180   |
| OpenClaw      | 3000     | 127.0.0.1:3100   |

Production Imprint uses 8080, production OpenClaw uses 3000.
Test ports are offset to avoid conflicts.

## Data Isolation

All data lives in `./data/` -- completely separate from production.
Safe to delete the entire directory to start fresh:

```bash
docker-compose down
rm -rf ./data
```

## Production Migration

Only after Phase 2 passes all checklist items. See BVP-277 in Linear
for the full migration plan.

## Files

| File                      | Purpose                                      |
|---------------------------|----------------------------------------------|
| docker-compose.yml        | Docker services (Imprint + MCP + OpenClaw)   |
| config.toml.example       | Imprint config (parallel mode)               |
| openclaw.json.parallel    | OpenClaw config: both tools available         |
| openclaw.json.replace     | OpenClaw config: memory_search disabled       |
| .env.example              | API key template                             |
