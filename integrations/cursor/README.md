# Imprint -- Cursor Integration

Two integration layers: a **sessionStart hook** for deterministic context injection and an **MCP server** for on-demand tool access.

## What You Get

| Layer | Type | What it does |
|-------|------|--------------|
| Hook (sessionStart) | Deterministic | Injects Imprint context at the start of every session -- preferences, recent facts, active decisions |
| MCP server | Probabilistic | 7 tools (query, ingest, status, entities, graph, update_fact, supersede_fact) available on demand |
| SKILL.md | Rules | Instructions for the agent on how and when to use Imprint tools |

The hook fires automatically. The MCP tools are called by the agent when it decides to. The SKILL.md guides the agent's behavior. All three layers work together.

## Setup

### 1. Hook (automatic context at session start)

Copy the hook script to your project:

```bash
mkdir -p .cursor/hooks
cp integrations/cursor/hooks/imprint-context.sh .cursor/hooks/
chmod +x .cursor/hooks/imprint-context.sh
```

Copy the hooks config (or merge into your existing `.cursor/hooks.json`):

```bash
cp integrations/cursor/hooks.json .cursor/hooks.json
```

The hook calls `GET /context` on the Imprint HTTP API. It requires `imprint serve` to be running.

### 2. MCP server (on-demand tools)

Copy the MCP config (or merge into your existing `.cursor/mcp.json`):

```bash
cp integrations/cursor/mcp.json .cursor/mcp.json
```

Edit `.cursor/mcp.json` and set the path to your `config.toml`:

```json
{
  "mcpServers": {
    "imprint": {
      "command": "imprint",
      "args": ["mcp"],
      "env": {
        "IMPRINT_CONFIG": "/path/to/config.toml"
      }
    }
  }
}
```

If `imprint` is not on your PATH, use the full path to the binary.

### 3. Rules (agent behavior)

Copy `integrations/cursor/SKILL.md` to `.cursor/rules/imprint.md` (or add it as a Cursor rule with `alwaysApply: true`).

## Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `IMPRINT_URL` | (auto-discovered) | Imprint server URL |
| `IMPRINT_QUERY_TIMEOUT` | `5` | Hook request timeout in seconds |
| `IMPRINT_CONFIG` | `config.toml` | Path to Imprint config file (for MCP server) |

URL auto-discovery:

1. `IMPRINT_URL` environment variable
2. `~/.imprint/serve.json` (written by `imprint serve`)
3. Default: `http://localhost:8080`

## Requirements

- `imprint serve` running (for the hook)
- `imprint` binary on PATH or full path in mcp.json (for MCP)
- `curl` (for the hook script)
- `jq` (for parsing the JSON response from Imprint)

## Known Limitations

- `sessionStart` is fire-and-forget -- if the hook is slow, the agent may start before context arrives
- Background Agents may not fire `sessionStart` hooks
- The hook has no access to the user's first message (session start, not prompt submit)
- `beforeSubmitPrompt` does not support context injection (Cursor feature request open)
