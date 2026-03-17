# Imprint -- Claude Code Integration

Two integration layers: a **SessionStart hook** for deterministic context injection and an **MCP server** for on-demand tool access.

## What You Get

| Layer | Type | What it does |
|-------|------|--------------|
| Hook (SessionStart) | Deterministic | Injects Imprint context at the start of every session -- preferences, recent facts, active decisions |
| MCP server | Probabilistic | 7 tools (query, ingest, status, entities, graph, update_fact, supersede_fact) available on demand |
| AGENTS.md | Rules | Instructions for the agent on how and when to use Imprint tools (copy into your CLAUDE.md) |

The hook fires automatically on session start, resume, clear, and compact. The MCP tools are called by the agent when it decides to. The AGENTS.md provides behavioral guidance. All three layers work together.

## Setup

### 1. Hook (automatic context at session start)

Copy the hook script to your project:

```bash
mkdir -p .claude/hooks
cp integrations/claude-code/hooks/imprint-context.sh .claude/hooks/
chmod +x .claude/hooks/imprint-context.sh
```

Add the hook to your settings. Either merge into your existing `.claude/settings.json` or copy the template:

```bash
cp integrations/claude-code/settings.json .claude/settings.json
```

For user-level (all projects):

```bash
mkdir -p ~/.claude/hooks
cp integrations/claude-code/hooks/imprint-context.sh ~/.claude/hooks/
# Add the hook to ~/.claude/settings.json
```

The hook calls `GET /context` on the Imprint HTTP API. It requires `imprint serve` to be running.

### 2. MCP server (on-demand tools)

Copy the MCP config (or merge into your existing `.mcp.json` at the project root):

```bash
cp integrations/claude-code/mcp.json .mcp.json
```

Edit `.mcp.json` and set the path to your `config.toml`:

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

Claude Code reads `CLAUDE.md`, not `AGENTS.md`. Copy the Imprint instructions into your project's `CLAUDE.md`:

```bash
cat integrations/claude-code/AGENTS.md >> CLAUDE.md
```

Or reference it with the import syntax in your `CLAUDE.md`:

```
@integrations/claude-code/AGENTS.md
```

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
- `imprint` binary on PATH or full path in .mcp.json (for MCP)
- `curl` (for the hook script)
- `jq` (for parsing the JSON response from Imprint)

## Hook Events

The SessionStart hook fires on:

| Matcher | When |
|---------|------|
| `startup` | New session |
| `resume` | `--resume`, `--continue`, or `/resume` |
| `clear` | `/clear` |
| `compact` | Auto or manual compaction |

## Known Limitations

- Claude Code reads `CLAUDE.md`, not `AGENTS.md` -- the `integrations/claude-code/AGENTS.md` file is for users to copy into their own `CLAUDE.md`
- SessionStart cannot block session creation (advisory only)
- The hook has no access to the user's first message (session start, not prompt submit)
