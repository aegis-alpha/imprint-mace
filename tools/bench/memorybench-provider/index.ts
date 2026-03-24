import type {
  Provider,
  ProviderConfig,
  IngestOptions,
  IngestResult,
  SearchOptions,
  IndexingProgressCallback,
} from "../../types/provider"
import type { UnifiedSession } from "../../types/unified"

const DEFAULT_BASE_URL = "http://127.0.0.1:8181"
const INGEST_TIMEOUT_MS = 30_000
const QUERY_TIMEOUT_MS = 60_000

export class ImprintProvider implements Provider {
  name = "imprint"
  private baseUrl = DEFAULT_BASE_URL

  async initialize(config: ProviderConfig): Promise<void> {
    this.baseUrl = (config.baseUrl || DEFAULT_BASE_URL).replace(/\/+$/, "")

    const res = await fetch(`${this.baseUrl}/status`, {
      signal: AbortSignal.timeout(5000),
    })
    if (!res.ok) {
      throw new Error(`Imprint not reachable at ${this.baseUrl}: ${res.status}`)
    }
    console.log(`[imprint] connected to ${this.baseUrl}`)
  }

  async ingest(sessions: UnifiedSession[], options: IngestOptions): Promise<IngestResult> {
    const documentIds: string[] = []

    for (const session of sessions) {
      const parts: string[] = []
      const date =
        (session.metadata?.formattedDate as string) ||
        (session.metadata?.date as string) ||
        ""
      if (date) {
        parts.push(`[conversation: ${date}]`)
      }
      for (const msg of session.messages) {
        parts.push(`${msg.role}: ${msg.content}`)
      }
      const text = parts.join("\n\n")
      const source = `bench:${options.containerTag}:${session.sessionId}`

      const res = await fetch(`${this.baseUrl}/ingest`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ text, source }),
        signal: AbortSignal.timeout(INGEST_TIMEOUT_MS),
      })

      if (!res.ok) {
        const body = await res.text().catch(() => "")
        throw new Error(`Ingest failed for ${session.sessionId}: ${res.status} ${body}`)
      }

      const result = await res.json()
      const ids = result.fact_ids || []
      documentIds.push(...ids)
    }

    return { documentIds }
  }

  async awaitIndexing(
    result: IngestResult,
    _containerTag: string,
    onProgress?: IndexingProgressCallback,
  ): Promise<void> {
    onProgress?.({
      completedIds: result.documentIds,
      failedIds: [],
      total: result.documentIds.length,
    })
  }

  async search(query: string, options: SearchOptions): Promise<unknown[]> {
    const res = await fetch(
      `${this.baseUrl}/query?q=${encodeURIComponent(query)}`,
      { signal: AbortSignal.timeout(QUERY_TIMEOUT_MS) },
    )

    if (!res.ok) {
      const body = await res.text().catch(() => "")
      throw new Error(`Query failed: ${res.status} ${body}`)
    }

    const result = await res.json()
    return [
      {
        content: result.answer || "",
        score: 1.0,
        metadata: {
          facts_consulted: result.facts_consulted,
          citations: result.citations,
        },
      },
    ]
  }

  async clear(containerTag: string): Promise<void> {
    try {
      const res = await fetch(`${this.baseUrl}/admin/facts`, {
        method: "DELETE",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ source_pattern: `bench:${containerTag}:%` }),
        signal: AbortSignal.timeout(10_000),
      })
      if (!res.ok) {
        await fetch(`${this.baseUrl}/admin/reset`, {
          method: "POST",
          headers: { "X-Confirm-Reset": "yes" },
          signal: AbortSignal.timeout(10_000),
        })
      }
    } catch {
      console.warn(`[imprint] clear failed for ${containerTag}, trying full reset`)
      await fetch(`${this.baseUrl}/admin/reset`, {
        method: "POST",
        headers: { "X-Confirm-Reset": "yes" },
        signal: AbortSignal.timeout(10_000),
      })
    }
  }
}

export default ImprintProvider
