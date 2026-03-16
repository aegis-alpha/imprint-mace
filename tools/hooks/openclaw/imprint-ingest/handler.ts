import { readFileSync } from "fs";
import { join } from "path";
import { homedir } from "os";

const MIN_LENGTH = 20;
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
  console.error(
    `[imprint-ingest] Imprint not reachable at ${url} -- hook disabled. ` +
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

  const parts = [
    event.context?.channelId,
    event.context?.conversationId,
    event.timestamp instanceof Date
      ? event.timestamp.toISOString().slice(0, 10)
      : undefined,
  ].filter(Boolean);
  const source = parts.join("-");

  void (async () => {
    if (!(await ensureReachable(url))) return;
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
