# Imprint Context Plugin for OpenClaw

Injects relevant context from Imprint into every agent prompt via `before_prompt_build` plugin hook.

## How it works

On every prompt build, the plugin calls Imprint's `GET /context?hint=<user_message>` endpoint (fast, no LLM synthesis, typically 50-200ms) and prepends the result to the agent's prompt via `prependContext`.

The agent sees relevant facts, preferences, and recent knowledge from Imprint before generating its response.

## Installation

1. Copy the plugin directory to your OpenClaw instance:

```bash
cp -r integrations/openclaw/plugin /path/to/imprint-context-plugin
```

2. Add to `openclaw.json`:

```json
{
  "plugins": {
    "entries": {
      "imprint-context": {
        "path": "/path/to/imprint-context-plugin",
        "config": {
          "imprintUrl": "http://localhost:8890",
          "timeoutMs": 3000
        }
      }
    }
  }
}
```

3. Restart OpenClaw.

## Configuration

| Field | Default | Description |
|-------|---------|-------------|
| `imprintUrl` | Auto-discovered | Imprint server URL. Checked in order: config, `IMPRINT_URL` env, `~/.imprint/serve.json`, `localhost:8080` |
| `timeoutMs` | 3000 | Request timeout. If exceeded, prompt builds without Imprint context |

## Replaces imprint-query hook

This plugin replaces the `imprint-query` internal hook (`~/.openclaw/hooks/imprint-query/`). The internal hook used `message:preprocessed` which is fire-and-forget -- the context never reached the agent. This plugin uses `before_prompt_build` which returns `{ prependContext }` that OpenClaw injects into the prompt.

After installing this plugin, remove the old hook:

```bash
rm -rf ~/.openclaw/hooks/imprint-query
```

## Note on imprint-ingest hook

The `imprint-ingest` internal hook (`message:preprocessed`) still works correctly for ingestion -- it sends messages to Imprint for fact extraction. Fire-and-forget is fine for ingest (we don't need the result back). Only the query/context delivery needed the plugin approach.
