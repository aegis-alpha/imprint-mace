const IMPRINT_URL = process.env.IMPRINT_URL || "http://localhost:8080";
const MIN_LENGTH = 20;
const TIMEOUT_MS = 5000;

const handler = async (event: any) => {
  const content = (event.context?.bodyForAgent || event.context?.body || "").trim();
  if (content.length < MIN_LENGTH) {
    return;
  }

  try {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), TIMEOUT_MS);

    const res = await fetch(
      `${IMPRINT_URL}/query?q=${encodeURIComponent(content)}`,
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
      if (!Array.isArray(event.messages)) {
        event.messages = [];
      }
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
