const IMPRINT_URL = process.env.IMPRINT_URL || "http://localhost:8080";
const MIN_LENGTH = 20;

const handler = async (event: any) => {
  const content = (event.context?.bodyForAgent || event.context?.body || "").trim();
  if (content.length < MIN_LENGTH) {
    return;
  }

  const parts = [
    event.context?.channelId,
    event.context?.conversationId,
    event.timestamp instanceof Date
      ? event.timestamp.toISOString().slice(0, 10)
      : undefined,
  ].filter(Boolean);
  const source = parts.join("-");

  void (async () => {
    try {
      await fetch(`${IMPRINT_URL}/ingest`, {
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
