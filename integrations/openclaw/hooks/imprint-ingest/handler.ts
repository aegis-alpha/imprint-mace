import { readFileSync } from "fs";
import { join } from "path";
import { homedir } from "os";

const MIN_LENGTH = 20;
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
  if (reachable) return true;
  if (now - lastCheckAt < RECHECK_INTERVAL_MS) return false;
  lastCheckAt = now;
  try {
    const res = await fetch(`${url}/status`, {
      signal: AbortSignal.timeout(3000),
    });
    if (res.ok) {
      reachable = true;
      return true;
    }
  } catch {}
  console.error(
    `[imprint-ingest] Imprint not reachable at ${url} -- will retry in ${RECHECK_INTERVAL_MS / 1000}s. ` +
      `Set IMPRINT_URL env or check that imprint serve is running.`,
  );
  return false;
}

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
      await fetch(`${url}/ingest`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ text: content, source }),
      });
    } catch (err) {
      console.error("[imprint-ingest] failed to send to Imprint:", err);
    }
  })();
};

export default handler;
