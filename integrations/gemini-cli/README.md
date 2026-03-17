# Imprint -- Gemini CLI Integration

Two integration layers: a **SessionStart hook** for deterministic context injection and an **MCP server** for on-demand tool access.

## What You Get

| Layer | Type | What it does |
|-------|------|--------------|
| Hook (SessionStart) | Deterministic | Injects Imprint context at the start of every session -- preferences, recent facts, active decisions |
| MCP server | Probabilistic | 7 tools (query, ingest, status, entities, graph, update_fact, supersede_fact) available on demand |
| GEMINI.md | Rules | Instructions for the agent on how and when to use Imprint tools |

The hook fires automatically. The MCP tools are called by the agent when it decides to. The GEMINI.md guides the agent's behavior. All three layers work together.

## Setup

### 1. Hook + MCP (combined config)

Gemini CLI uses a single `settings.json` for both hooks and MCP servers.

Copy the hook script to your project:

```bash
mkdir -p .gemini/hooks
cp integrations/gemini-cli/hooks/imprint-context.sh .gemini/hooks/
chmod +x .gemini/hooks/imprint-context.sh
```

Copy the settings template (or merge into your existing `.gemini/settings.json`):

```bash
cp integrations/gemini-cli/settings.json .gemini/settings.json
```

For user-level (all projects):

```bash
mkdir -p ~/.gemini/hooks
cp integrations/gemini-cli/hooks/imprint-context.sh ~/.gemini/hooks/
# Merge into ~/.gemini/settings.json
```

If `imprint` is not on your PATH, use the full path in the `command` field.

The `"trust": true` setting in `mcpServers` bypasses tool confirmation prompts for Imprint tools.

### 2. Rules (agent behavior)

Copy `integrations/gemini-cli/GEMINI.md` to your project root:

```bash
cp integrations/gemini-cli/GEMINI.md GEMINI.md
```

Or for global rules:

```bash
cp integrations/gemini-cli/GEMINI.md ~/.gemini/GEMINI.md
```

Gemini CLI loads GEMINI.md from `~/.gemini/GEMINI.md` (global), then ancestor directories up to the `.git` root.

## Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `IMPRINT_URL` | (auto-discovered) | Imprint server URL |
| `IMPRINT_QUERY_TIMEOUT` | `5` | Hook request timeout in seconds |

URL auto-discovery:

1. `IMPRINT_URL` environment variable
2. `~/.imprint/serve.json` (written by `imprint serve`)
3. Default: `http://localhost:8080`

### Environment variables in hooks

Gemini CLI provides these environment variables to hook scripts:

| Variable | Description |
|----------|-------------|
| `GEMINI_PROJECT_DIR` | Absolute path to the project root |
| `GEMINI_SESSION_ID` | Unique ID for the current session |
| `GEMINI_CWD` | Current working directory |

## Requirements

- `imprint serve` running (for the hook)
- `imprint` binary on PATH or full path in settings.json (for MCP)
- `curl` (for the hook script)
- `jq` (optional, for robust JSON escaping; falls back to sed)

## Hook Events

The SessionStart hook fires on:

| Source | When |
|--------|------|
| `startup` | New session |
| `resume` | Resumed session |
| `clear` | After `/clear` |

SessionStart is advisory only -- startup is never blocked.

## Known Limitations

- SessionStart cannot block session creation (advisory only)
- The hook has no access to the user's first message (session start, not prompt submit)
- For per-turn context injection, consider adding a `BeforeAgent` hook (fires before each agent turn, not just session start)
