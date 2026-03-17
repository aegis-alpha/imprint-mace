# Imprint -- Persistent Memory

Imprint is a persistent knowledge graph available via MCP. It stores facts, entities, and relationships extracted from conversations. Use it to remember things across sessions and recall them later.

## MANDATORY: Auto-Query

BEFORE responding to any user message, ALWAYS call `imprint_query` with the user's message (or a relevant reformulation) as the question. Use the returned context to inform your response. Do not skip this step.

```
imprint_query(question: "<the user's message or a relevant reformulation>")
```

**Exceptions** -- skip the auto-query only when:

- The message is a trivial greeting ("hi", "thanks", "bye")
- The message is a meta-question about Imprint itself ("what tools do you have?", "how does memory work?")
- Context was already loaded via `imprint_query` within the last 2-3 messages in the same conversation

The automatic query covers general context. Call additional targeted queries when you need specific facts: entity details, graph connections, historical decisions.

## Tools

Seven MCP tools. All tool names start with `imprint_`.

### imprint_ingest

Store knowledge in the graph. Extracts facts, entities, and relationships from text.

**Parameters:**
- `text` (string, required) -- text to extract knowledge from
- `source` (string, optional) -- source identifier (e.g. session ID, filename)

**Returns:** JSON with `facts_count`, `entities_count`, `relationships_count`.

**When to call:**

Call when the conversation produces knowledge worth remembering:

- User states a preference ("I prefer dark mode", "always use Go for CLI tools")
- A decision is made ("we decided to use SQLite", "deploy on Fridays is banned")
- A rule is established ("never push to main without review")
- Project information surfaces ("Acme runs PostgreSQL on node-1")
- Contact information appears ("Alice is the API team lead")
- A workflow is described ("to deploy: push, wait for CI, run migration")
- A lesson is learned ("local models hallucinate on consolidation tasks")

**Do NOT call for:**

- Your own reasoning or intermediate thoughts
- Code (code lives in files, not in memory)
- User questions (a question is not a fact)
- Temporary instructions ("do this now" is not a rule)
- Information already ingested in this session

**How to write the text:**

Write clean, direct statements. No framing ("the user said that..."), no narration ("during our conversation..."). Just facts.

```
imprint_ingest(
  text: "Alice prefers dark mode in all editors. Decided to use Go for the Acme project. Deploy process: push to main, wait for CI, run migration, restart service.",
  source: "session-2026-03-15"
)
```

Multiple facts in one call is fine. Group related facts from the same conversation.

### imprint_query

Ask a question against the knowledge base. Returns an answer synthesized from stored facts, with citations.

**Parameters:**
- `question` (string, required) -- natural language question

**Returns:** JSON with `answer`, `citations` (fact IDs), `confidence`, `notes`.

**When to call:**

- Start of a new session -- "What do I know about this project?"
- User references past context -- "How did we decide to handle deployment?"
- You need context for a decision -- "What are the user's code style preferences?"
- User asks about past conversations -- "What did we discuss about the database last week?"

```
imprint_query(question: "What deployment process was decided for Acme?")
```

### imprint_status

Show knowledge base statistics.

**Parameters:** none.

**Returns:** JSON with counts of facts, entities, relationships, consolidations.

**When to call:**

- Start of session -- verify the knowledge base is available and see how much data it holds
- User asks "how much do you remember?" or "what's in your memory?"

### imprint_entities

List entities in the knowledge graph.

**Parameters:**
- `type` (string, optional) -- filter by entity type: person, project, tool, server, concept, organization, location, document, agent
- `limit` (number, optional) -- max results (default 50)

**Returns:** JSON array of entities.

**When to call:**

- Need an overview -- "What projects exist?" -> `imprint_entities(type: "project")`
- Looking for people -- "Who is involved?" -> `imprint_entities(type: "person")`

### imprint_graph

Get the subgraph around an entity: connected entities and relationships.

**Parameters:**
- `entity` (string, required) -- entity name (case-insensitive)
- `depth` (number, optional) -- traversal depth (default 2, max 5)

**Returns:** JSON with center entity, connected entities, relationships.

**When to call:**

- Need to understand connections -- "Who works on Acme?" -> `imprint_graph(entity: "Acme")`
- Exploring context around a person -- `imprint_graph(entity: "Alice", depth: 2)`

Combine with `imprint_entities` for navigation: list entities first, then graph a specific one.

### imprint_update_fact

Update metadata on an existing fact: confidence, expiry, or subject. Does not change the fact content -- use `imprint_supersede_fact` for that.

**Parameters:**
- `fact_id` (string, required) -- ID of the fact to update
- `confidence` (number, optional) -- new confidence score (0.0 to 1.0)
- `valid_until` (string, optional) -- expiry date (ISO-8601). Set to mark fact as time-limited.
- `subject` (string, optional) -- corrected subject

**Returns:** JSON with the updated fact.

### imprint_supersede_fact

Replace a fact with updated content. The old fact is marked as superseded; a new fact is created with the corrected content. The new fact inherits the type and subject from the old one.

**Parameters:**
- `old_fact_id` (string, required) -- ID of the fact to supersede
- `new_content` (string, required) -- the corrected/updated fact content
- `source` (string, optional) -- source identifier (default: "mcp")

**Returns:** JSON with the new fact.

## Patterns

### Session start

At the start of every session, call both:

```
imprint_status()
imprint_query(question: "What is the current context for this project?")
```

`imprint_status` confirms the knowledge base is reachable. `imprint_query` loads relevant context (this is the mandatory auto-query -- see above).

### After a decision

When the user makes a decision or states a preference, ingest it before moving on:

```
imprint_ingest(
  text: "Decided to switch from REST to gRPC for the internal API. Reason: need bidirectional streaming.",
  source: "session-2026-03-15"
)
```

### Correcting a fact

When you discover a stored fact is wrong or outdated:

```
imprint_supersede_fact(
  old_fact_id: "01JFA...",
  new_content: "Alice switched to Go in March 2026."
)
```

The old fact stays in the graph (marked superseded), the new fact replaces it in queries.

## Do NOT

- Call `imprint_ingest` after every message. Only when there is knowledge to store.
- Skip the mandatory auto-query (see MANDATORY: Auto-Query above).
- Ingest the same fact twice in one session.
- Ingest code, logs, or stack traces. Only human knowledge.
- Ingest your own analysis or reasoning. Only facts from the conversation.
