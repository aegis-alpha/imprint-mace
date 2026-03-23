# Imprint Context Plugin for OpenClaw

Injects relevant context from Imprint into every agent prompt via `before_prompt_build` plugin hook.

## How it works

On every prompt build, the plugin calls Imprint's `GET /context?hint=<user_message>` endpoint (fast, no LLM synthesis, typically 50-200ms) and prepends the result to the agent's prompt via `prependContext`.

The agent sees relevant facts, preferences, and recent knowledge from Imprint before generating its response.

## Installation

1. Copy the plugin directory to your OpenClaw instance:

```bash
cp -r integrations/openclaw/plugin /home/ubuntu/imprint-context-plugin
```

2. Add to `openclaw.json` -- two sections required:

```json
{
  "plugins": {
    "load": {
      "paths": ["/home/ubuntu/imprint-context-plugin"]
    },
    "entries": {
      "imprint-context": {
        "enabled": true,
        "config": {
          "imprintUrl": "http://localhost:8890",
          "timeoutMs": 3000
        }
      }
    }
  }
}
```

- `plugins.load.paths` -- tells OpenClaw where to find the plugin (directory containing `openclaw.plugin.json`). Without this, OpenClaw won't discover the plugin.
- `plugins.entries.imprint-context` -- enables the plugin and passes config (accessible via `api.pluginConfig`). Without this, the plugin loads but won't know the Imprint URL.

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
