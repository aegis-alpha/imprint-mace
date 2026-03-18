import { readFileSync } from "fs";
import { join } from "path";
import { homedir } from "os";

export const RECHECK_INTERVAL_MS = 60_000;

let reachable = false;
let lastCheckAt = 0;

export function getImprintURL(): string {
  const envURL = process.env.IMPRINT_URL;
  if (envURL) return envURL;

  try {
    const infoPath = join(homedir(), ".imprint", "serve.json");
    const data = JSON.parse(readFileSync(infoPath, "utf-8"));
    if (data.url) return data.url;
  } catch {}

  return "http://localhost:8080";
}

export async function checkReachable(url: string, tag: string): Promise<boolean> {
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
    `[${tag}] Imprint not reachable at ${url} -- will retry in ${RECHECK_INTERVAL_MS / 1000}s. ` +
      `Set IMPRINT_URL env or check that imprint serve is running.`,
  );
  return false;
}

export function setUnreachable(): void {
  reachable = false;
  lastCheckAt = 0;
}

export function isReachable(): boolean {
  return reachable;
}
