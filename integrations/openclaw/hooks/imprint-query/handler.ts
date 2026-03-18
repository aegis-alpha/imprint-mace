import { getImprintURL, checkReachable, setUnreachable } from "../shared/imprint-client";

const MIN_LENGTH = 20;

function getTimeoutMs(): number {
  const env = process.env.IMPRINT_QUERY_TIMEOUT;
  if (env) {
    const parsed = parseInt(env, 10);
    if (parsed > 0) return parsed;
  }
  return 5000;
}

const handler = async (event: any) => {
  const url = getImprintURL();

  const content = (
    event.context?.bodyForAgent ||
    event.context?.body ||
    ""
  ).trim();
  if (content.length < MIN_LENGTH) return;

  if (!(await checkReachable(url, "imprint-query"))) return;

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
      setUnreachable();
    }
  }
};

export default handler;
