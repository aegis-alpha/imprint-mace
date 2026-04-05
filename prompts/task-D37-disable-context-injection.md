# Task: D37 -- Disable Context Injection on Production

## What

Disable Imprint context injection into OpenClaw agent prompts. Two mechanisms must be removed:

1. **Plugin `imprint-context`** -- calls `GET /context` on every prompt build, injects result via `prependContext`
2. **Hook `imprint-query`** -- calls `GET /context` on every message, pushes result into `event.messages`

Both inject 60+ records of mostly noise into every agent prompt. Decision D37: disabled until context builder is redesigned.

## What NOT to touch

- **`imprint-ingest` hook** -- sends messages to Imprint hot store. KEEP IT. It is needed for Phase 2.
- **`imprint-transcript` hook** -- batch transcript conversion. KEEP IT.
- **Imprint server** -- do not stop or reconfigure. `GET /context` endpoint stays in the codebase, it just won't be called.

## Steps

### 1. Find and remove the plugin

```bash
# Check if plugin exists as a directory
ls -la ~/.openclaw/plugins/imprint-context/ 2>/dev/null

# If it exists, remove it
rm -rf ~/.openclaw/plugins/imprint-context/
```

If the plugin is registered in an OpenClaw config file (e.g. `~/.openclaw/config.json` or `~/.openclaw/config.toml` or similar), find the entry referencing `imprint-context` and remove it.

```bash
# Search for plugin references in OpenClaw config
grep -r "imprint-context" ~/.openclaw/ --include="*.json" --include="*.toml" --include="*.yaml" --include="*.yml" 2>/dev/null
```

### 2. Find and remove the old hook

```bash
# Check if hook exists
ls -la ~/.openclaw/hooks/imprint-query/ 2>/dev/null

# If it exists, remove it
rm -rf ~/.openclaw/hooks/imprint-query/
```

### 3. Verify other hooks are intact

```bash
# These must still exist:
ls -la ~/.openclaw/hooks/imprint-ingest/
ls -la ~/.openclaw/hooks/imprint-transcript/
```

If either is missing, STOP and report. Do not proceed.

### 4. Restart OpenClaw

Restart the OpenClaw process so it reloads plugins and hooks without the removed ones.

```bash
# Find how OpenClaw runs (systemd, docker, pm2, etc.)
systemctl status openclaw 2>/dev/null || \
docker ps --filter name=openclaw 2>/dev/null || \
pm2 list 2>/dev/null

# Restart using whichever method is active
```

### 5. Verify

After restart, send a test message to the agent and check that:

1. No `## Relevant Context` / `## Preferences` / `## Recent` block appears in the agent's prompt
2. The agent still works normally (responds to messages)
3. `imprint-ingest` still works: check Imprint logs for incoming hot messages

```bash
# Check Imprint is still receiving messages (look for recent hot ingest logs)
docker logs imprint --tail 20 2>/dev/null || journalctl -u imprint --no-pager -n 20 2>/dev/null
```

## Report

When done, report:
- Plugin found: yes/no, where
- Hook found: yes/no, where
- Both removed: yes/no
- OpenClaw restarted: yes/no
- imprint-ingest still working: yes/no
- Context injection stopped: yes/no (check agent prompt for absence of Imprint context block)
