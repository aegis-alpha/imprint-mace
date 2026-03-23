import { readFileSync } from "fs";
import { join } from "path";
import { homedir } from "os";

function discoverUrl(configUrl?: string): string {
  if (configUrl) return configUrl;
  const envUrl = process.env.IMPRINT_URL;
  if (envUrl) return envUrl;
  try {
    const data = JSON.parse(
      readFileSync(join(homedir(), ".imprint", "serve.json"), "utf-8"),
    );
    if (data.url) return data.url;
  } catch {}
  return "http://localhost:8080";
}

export default function (api: any) {
  const url = discoverUrl(api.pluginConfig?.imprintUrl);
  const timeoutMs = api.pluginConfig?.timeoutMs ?? 3000;

  let reachable = true;
  let lastCheck = 0;
  const RECHECK_MS = 60_000;

  async function ensureReachable(): Promise<boolean> {
    const now = Date.now();
    if (now - lastCheck < RECHECK_MS) return reachable;
    lastCheck = now;
    try {
      const res = await fetch(`${url}/status`, {
        signal: AbortSignal.timeout(2000),
      });
      reachable = res.ok;
    } catch {
      reachable = false;
    }
    if (!reachable) {
      api.logger?.warn?.(
        `[imprint] not reachable at ${url}, retry in ${RECHECK_MS / 1000}s`,
      );
    }
    return reachable;
  }

  api.on("before_prompt_build", async (event: any) => {
    if (!(await ensureReachable())) return;

    const hint = (event.prompt || "").trim();
    if (hint.length < 20) return;

    try {
      const res = await fetch(
        `${url}/context?hint=${encodeURIComponent(hint)}`,
        { signal: AbortSignal.timeout(timeoutMs) },
      );
      if (!res.ok) {
        api.logger?.warn?.(`[imprint] /context returned ${res.status}`);
        return;
      }
      const text = await res.text();
      if (text?.trim()) {
        api.logger?.info?.(
          `[imprint] injected ${text.trim().length} chars of context`,
        );
        return { prependContext: text.trim() };
      }
      api.logger?.debug?.("[imprint] /context returned empty");
    } catch (err: any) {
      if (err?.name === "AbortError") {
        api.logger?.warn?.(`[imprint] /context timed out after ${timeoutMs}ms`);
      } else {
        api.logger?.warn?.(`[imprint] /context failed: ${err}`);
        reachable = false;
        lastCheck = 0;
      }
    }
  });

  api.logger?.info?.(`[imprint] plugin loaded, url=${url}, timeout=${timeoutMs}ms`);
}
