import { readFileSync } from "fs";
import { join } from "path";
import { homedir } from "os";

// Inlined from shared/imprint-client.ts (D29: hooks must be self-contained,
// OpenClaw hook loader does not resolve cross-directory imports)
const RECHECK_INTERVAL_MS = 60_000;
let reachable = false;
let lastCheckAt = 0;

function getImprintURL(): string {
  const envURL = process.env.IMPRINT_URL;
  if (envURL) return envURL;
  try {
    const infoPath = join(homedir(), ".imprint", "serve.json");
    const data = JSON.parse(readFileSync(infoPath, "utf-8"));
    if (data.url) return data.url;
  } catch {}
  return "http://localhost:8080";
}

async function checkReachable(url: string): Promise<boolean> {
  const now = Date.now();
  if (reachable && now - lastCheckAt < RECHECK_INTERVAL_MS) return true;
  if (!reachable && now - lastCheckAt < RECHECK_INTERVAL_MS) return false;
  lastCheckAt = now;
  try {
    const res = await fetch(`${url}/status`, {
      signal: AbortSignal.timeout(3000),
    });
    if (res.ok) { reachable = true; return true; }
  } catch {}
  reachable = false;
  console.warn(
    `[imprint-ingest] Imprint not reachable at ${url} -- will retry in ${RECHECK_INTERVAL_MS / 1000}s. ` +
      `Set IMPRINT_URL env or check that imprint serve is running.`,
  );
  return false;
}

function setUnreachable(): void {
  reachable = false;
  lastCheckAt = 0;
}

const MIN_LENGTH = 20;

const handler = async (event: any) => {
  const url = getImprintURL();

  const content = (
    event.context?.bodyForAgent ||
    event.context?.body ||
    ""
  ).trim();
  if (content.length < MIN_LENGTH) return;

  const sessionId = event.sessionKey || [
    event.context?.channelId,
    event.context?.conversationId,
  ].filter(Boolean).join(":");
  const source = `realtime:${sessionId}`;

  void (async () => {
    if (!(await checkReachable(url))) return;
    try {
      const res = await fetch(`${url}/ingest`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ text: content, source }),
        signal: AbortSignal.timeout(10_000),
      });
      if (!res.ok) {
        console.warn(`[imprint-ingest] ingest returned ${res.status}`);
      }
    } catch (err: any) {
      if (err?.name !== "AbortError") {
        setUnreachable();
      }
      console.error("[imprint-ingest] failed to send to Imprint:", err);
    }
  })();
};

export default handler;
