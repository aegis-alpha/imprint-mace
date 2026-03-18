import { readFileSync } from "fs";
import { join } from "path";
import { homedir } from "os";

const MIN_LENGTH = 20;
const RECHECK_INTERVAL_MS = 60_000;
const LEARN_THRESHOLD = parseInt(process.env.IMPRINT_FILTER_THRESHOLD || "5", 10);
const SKIP_RATIO = parseFloat(process.env.IMPRINT_FILTER_SKIP_RATIO || "0.8");

let reachable = false;
let lastCheckAt = 0;

interface PatternStats {
  total: number;
  empty: number;
  loggedSkip: boolean;
}

const patternStats = new Map<string, PatternStats>();

function extractPattern(sessionKey: string): string {
  const parts = sessionKey.split(":");
  if (parts.length > 1) {
    const last = parts[parts.length - 1];
    if (last.length >= 8 && /^[a-f0-9-]+$/i.test(last)) {
      parts.pop();
    }
  }
  return parts.join(":");
}

function shouldSkip(pattern: string): boolean {
  const stats = patternStats.get(pattern);
  if (!stats || stats.total < LEARN_THRESHOLD) return false;
  return stats.empty / stats.total >= SKIP_RATIO;
}

function recordResult(pattern: string, factsCount: number): void {
  let stats = patternStats.get(pattern);
  if (!stats) {
    stats = { total: 0, empty: 0, loggedSkip: false };
    patternStats.set(pattern, stats);
  }
  stats.total++;
  if (factsCount === 0) stats.empty++;
}

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
  const pattern = extractPattern(sessionId);

  if (shouldSkip(pattern)) {
    const stats = patternStats.get(pattern)!;
    if (!stats.loggedSkip) {
      console.log(
        `[imprint-ingest] auto-skipping pattern '${pattern}' (${stats.empty}/${stats.total} empty)`,
      );
      stats.loggedSkip = true;
    }
    return;
  }

  void (async () => {
    if (!(await checkReachable(url))) return;
    try {
      const res = await fetch(`${url}/ingest`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ text: content, source }),
        signal: AbortSignal.timeout(10_000),
      });
      if (res.ok) {
        try {
          const body = await res.json();
          recordResult(pattern, body.facts_count ?? body.FactsCount ?? 0);
        } catch {
          recordResult(pattern, 0);
        }
      } else {
        recordResult(pattern, 0);
      }
    } catch (err: any) {
      if (err?.name !== "AbortError") {
        reachable = false;
      }
      console.error("[imprint-ingest] failed to send to Imprint:", err);
    }
  })();
};

export default handler;
