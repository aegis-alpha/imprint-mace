import { readFileSync } from "fs";
import { join } from "path";
import { homedir } from "os";

const MIN_LENGTH = 20;
const RECHECK_INTERVAL_MS = 60_000;

function getTimeoutMs(): number {
  const env = process.env.IMPRINT_QUERY_TIMEOUT;
  if (env) {
    const parsed = parseInt(env, 10);
    if (parsed > 0) return parsed;
  }
  return 5000;
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

let reachable = false;
let lastCheckAt = 0;

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
  console.warn(
    `[imprint-query] Imprint not reachable at ${url} -- will retry in ${RECHECK_INTERVAL_MS / 1000}s. ` +
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

  if (!(await checkReachable(url))) return;

  const timeoutMs = getTimeoutMs();

  try {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), timeoutMs);

    const res = await fetch(
      `${url}/context?hint=${encodeURIComponent(content)}`,
      { signal: controller.signal },
    );
    clearTimeout(timer);

    if (!res.ok) {
      console.warn(`[imprint-query] /context returned ${res.status}`);
      return;
    }

    const text = await res.text();
    if (text && text.trim().length > 0) {
      event.messages.push(`[Imprint context]\n${text.trim()}`);
    }
  } catch (err: any) {
    if (err?.name === "AbortError") {
      console.warn(`[imprint-query] /context timed out after ${timeoutMs}ms`);
    } else {
      console.warn("[imprint-query] /context failed:", err);
      reachable = false;
    }
  }
};

export default handler;
