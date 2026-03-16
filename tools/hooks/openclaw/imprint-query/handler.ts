import { readFileSync } from "fs";
import { join } from "path";
import { homedir } from "os";

const MIN_LENGTH = 20;
const TIMEOUT_MS = 5000;
let verified = false;
let disabled = false;

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

async function ensureReachable(url: string): Promise<boolean> {
  if (disabled) return false;
  if (verified) return true;
  try {
    const res = await fetch(`${url}/status`, {
      signal: AbortSignal.timeout(3000),
    });
    if (res.ok) {
      verified = true;
      return true;
    }
  } catch {}
  disabled = true;
  console.warn(
    `[imprint-query] Imprint not reachable at ${url} -- hook disabled. ` +
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

  if (!(await ensureReachable(url))) return;

  try {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), TIMEOUT_MS);

    const res = await fetch(
      `${url}/query?q=${encodeURIComponent(content)}`,
      { signal: controller.signal },
    );
    clearTimeout(timer);

    if (!res.ok) {
      console.warn(`[imprint-query] query returned ${res.status}`);
      return;
    }

    const data = await res.json();
    const answer = data?.answer;
    if (answer && typeof answer === "string" && answer.trim().length > 0) {
      event.messages.push(`[Imprint context] ${answer}`);
    }
  } catch (err: any) {
    if (err?.name === "AbortError") {
      console.warn("[imprint-query] query timed out after 5s");
    } else {
      console.warn("[imprint-query] query failed:", err);
    }
  }
};

export default handler;
