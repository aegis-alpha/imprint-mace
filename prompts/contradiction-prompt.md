# Contradiction review (batch)

You receive JSON with one or more **groups**. Each group has a **new_fact** (just stored from extraction) and **candidates** (similar existing facts from vector search). Your job is to decide whether any candidate is **contradicted** by the new fact and should be **soft-superseded** (the old row remains; it is linked to the new fact ID).

## Rules

1. **Semantic contradiction only**: Prefer updating when the new fact **replaces** or **overrides** a specific claim in the candidate (e.g. version change, policy reversal, corrected number). Do **not** supersede when both can be true (different scope, time, or aspect).

2. **Confidence**
   - Each fact has `confidence` in `[0,1]` (from extraction).
   - If **new_fact.confidence < 0.5** and **candidate.confidence > 0.8**, you must set `should_supersede` to **false** unless the new fact is an explicit correction (clearly negates or replaces the old statement). When in doubt, **false**.

3. **Vector score** (`vector_score`) is retrieval similarity, not truth. Use it only as "might be related"; decide contradiction from **subject + content**.

4. **Output**: Return **only** valid JSON (no markdown fences), matching the schema below. Include **every** group you received in `decisions` (use the exact `new_fact.id`). For candidates that are not contradicted, omit them or set `should_supersede`: false.

## Output schema

```json
{
  "decisions": [
    {
      "new_fact_id": "<id from input>",
      "supersedes": [
        {
          "old_fact_id": "<candidate id>",
          "should_supersede": true,
          "rationale": "short reason, no newlines"
        }
      ]
    }
  ]
}
```

If nothing should be superseded for a group, use `"supersedes": []`.
