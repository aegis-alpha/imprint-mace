import { existsSync } from "fs";
import { spawn } from "child_process";
import { join, dirname } from "path";
import { homedir } from "os";

const ACTIONS = new Set(["new", "reset", "stop"]);

function getAdapterPath(): string | null {
  const envPath = process.env.IMPRINT_ADAPTER_PATH;
  if (envPath) {
    if (existsSync(envPath)) return envPath;
    console.error(
      `[imprint-transcript] IMPRINT_ADAPTER_PATH not found: ${envPath}`,
    );
    return null;
  }

  const relative = join(dirname(__dirname), "..", "adapter", "openclaw-adapter");
  if (existsSync(relative)) return relative;

  console.error(
    `[imprint-transcript] adapter not found at ${relative} -- ` +
      `set IMPRINT_ADAPTER_PATH or install the adapter`,
  );
  return null;
}

function getOutputDir(): string {
  return (
    process.env.IMPRINT_TRANSCRIPTS_DIR ||
    join(homedir(), ".openclaw", "workspace", "memory", "transcripts")
  );
}

function getSessionFile(event: any): string | null {
  const explicit = event.context?.sessionFile;
  if (explicit && typeof explicit === "string") return explicit;

  const sessionId =
    event.context?.sessionId || event.context?.sessionEntry?.id || event.sessionKey;
  if (sessionId) {
    return join(
      homedir(),
      ".openclaw",
      "agents",
      "main",
      "sessions",
      `${sessionId}.jsonl`,
    );
  }

  return null;
}

const handler = async (event: any) => {
  if (event.type !== "command") return;
  if (!ACTIONS.has(event.action)) return;

  const sessionFile = getSessionFile(event);
  if (!sessionFile) {
    console.error(
      "[imprint-transcript] no session file path in event context -- skipping",
    );
    return;
  }

  if (!existsSync(sessionFile)) return;

  const adapterPath = getAdapterPath();
  if (!adapterPath) return;

  const outputDir = getOutputDir();

  const sessionKey = event.sessionKey || "";

  try {
    const child = spawn("python3", [adapterPath, sessionFile, outputDir], {
      detached: true,
      stdio: "ignore",
      env: { ...process.env, IMPRINT_SESSION_KEY: sessionKey },
    });
    child.unref();
    console.log(
      `[imprint-transcript] converting ${sessionFile} -> ${outputDir}`,
    );
  } catch (err) {
    console.error("[imprint-transcript] failed to spawn adapter:", err);
  }
};

export default handler;
