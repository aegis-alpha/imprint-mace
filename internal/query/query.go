// Package query implements the hybrid retrieval pipeline for answering
// natural language questions from the knowledge base.
//
// Pipeline: embed question -> parallel retrieval (4 layers) -> merge/rank
// -> ReadContext enrichment -> LLM synthesis -> parse response.
package query

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/aegis-alpha/imprint-mace/internal/db"
	"github.com/aegis-alpha/imprint-mace/internal/fts"
	"github.com/aegis-alpha/imprint-mace/internal/model"
	"github.com/aegis-alpha/imprint-mace/internal/provider"
)

// Sender sends a request to an LLM. provider.Chain implements this.
type Sender interface {
	Send(ctx context.Context, req provider.Request) (*provider.Response, error)
}

// Querier answers natural language questions using hybrid retrieval.
type Querier struct {
	store         db.Store
	embedder      provider.Embedder
	sender        Sender
	transcriptDir string
	logger        *slog.Logger
}

// New creates a Querier. Pass nil for embedder to disable vector search
// (falls back to FTS5 only).
func New(store db.Store, embedder provider.Embedder, sender Sender, transcriptDir string, logger *slog.Logger) *Querier {
	return &Querier{
		store:         store,
		embedder:      embedder,
		sender:        sender,
		transcriptDir: transcriptDir,
		logger:        logger,
	}
}

// retrievalResult holds the raw output from all retrieval layers
// before merging and ranking.
type retrievalResult struct {
	factsByVector  []db.ScoredFact
	factsByText    []db.ScoredFact
	chunksByVector []db.ScoredChunk
	chunksByText   []db.ScoredChunk
	graphFacts     []model.Fact
}

// RetrievalResult holds ranked facts with per-layer attribution,
// suitable for eval without LLM synthesis.
type RetrievalResult struct {
	Ranked         []RankedFact
	FactsByVector  int
	FactsByText    int
	FactsByGraph   int
	ChunksByVector int
	ChunksByText   int
}

// RankedFact is a fact with its merged RRF score and layer provenance.
type RankedFact struct {
	Fact       model.Fact
	Score      float64
	FromVector bool
	FromText   bool
	FromGraph  bool
}

// Retrieve runs the retrieval pipeline (embed, parallel search, RRF merge)
// without LLM synthesis. Returns ranked facts with per-layer attribution.
func (q *Querier) Retrieve(ctx context.Context, question string) (*RetrievalResult, error) {
	var embedding []float32
	if q.embedder != nil {
		var err error
		embedding, err = q.embedder.Embed(ctx, question)
		if err != nil {
			q.logger.Warn("failed to embed question, falling back to text-only", "error", err)
		}
	}

	r := q.retrieve(ctx, question, embedding)
	ranked := q.mergeAndRank(r)

	vectorIDs := map[string]bool{}
	for i := range r.factsByVector {
		vectorIDs[r.factsByVector[i].Fact.ID] = true
	}
	textIDs := map[string]bool{}
	for i := range r.factsByText {
		textIDs[r.factsByText[i].Fact.ID] = true
	}
	graphIDs := map[string]bool{}
	for i := range r.graphFacts {
		graphIDs[r.graphFacts[i].ID] = true
	}

	out := make([]RankedFact, len(ranked))
	for i := range ranked {
		out[i] = RankedFact{
			Fact:       ranked[i].fact,
			Score:      ranked[i].score,
			FromVector: vectorIDs[ranked[i].fact.ID],
			FromText:   textIDs[ranked[i].fact.ID],
			FromGraph:  graphIDs[ranked[i].fact.ID],
		}
	}

	return &RetrievalResult{
		Ranked:         out,
		FactsByVector:  len(r.factsByVector),
		FactsByText:    len(r.factsByText),
		FactsByGraph:   len(r.graphFacts),
		ChunksByVector: len(r.chunksByVector),
		ChunksByText:   len(r.chunksByText),
	}, nil
}

// Query answers a natural language question using hybrid retrieval.
func (q *Querier) Query(ctx context.Context, question string) (*model.QueryResult, error) {
	totalStart := time.Now()

	var embedding []float32
	embedderAvailable := q.embedder != nil
	if q.embedder != nil {
		var err error
		embedding, err = q.embedder.Embed(ctx, question)
		if err != nil {
			q.logger.Warn("failed to embed question, falling back to text-only", "error", err)
		}
	}

	retrievalStart := time.Now()
	retrieved := q.retrieve(ctx, question, embedding)
	retrievalMs := time.Since(retrievalStart).Milliseconds()

	ranked := q.mergeAndRank(retrieved)

	enriched := q.enrichWithContext(ctx, ranked, retrieved)

	prompt := q.buildPrompt(question, enriched)

	synthesisStart := time.Now()
	resp, err := q.sender.Send(ctx, provider.Request{
		SystemPrompt: prompt.system,
		UserPrompt:   prompt.user,
		MaxTokens:    2048,
	})
	if err != nil {
		totalMs := time.Since(totalStart).Milliseconds()
		q.writeQueryLog(ctx, "query", question, totalMs, retrievalMs, 0,
			len(ranked), retrieved, 0, embedderAvailable, err.Error())
		return nil, fmt.Errorf("query LLM: %w", err)
	}

	result, err := parseResponse(resp.Content)
	synthesisMs := time.Since(synthesisStart).Milliseconds()
	if err != nil {
		totalMs := time.Since(totalStart).Milliseconds()
		q.writeQueryLog(ctx, "query", question, totalMs, retrievalMs, synthesisMs,
			len(ranked), retrieved, 0, embedderAvailable, err.Error())
		return nil, fmt.Errorf("parse query response: %w", err)
	}

	result.FactsConsulted = len(ranked)

	q.persistCitations(ctx, result.Citations)

	totalMs := time.Since(totalStart).Milliseconds()
	q.writeQueryLog(ctx, "query", question, totalMs, retrievalMs, synthesisMs,
		len(ranked), retrieved, len(result.Citations), embedderAvailable, "")

	q.logger.Info("query complete",
		"question_len", len(question),
		"facts_consulted", result.FactsConsulted,
		"citations", len(result.Citations),
		"total_ms", totalMs,
		"retrieval_ms", retrievalMs,
		"synthesis_ms", synthesisMs,
	)

	return result, nil
}

func (q *Querier) writeQueryLog(ctx context.Context, endpoint, question string,
	totalMs, retrievalMs, synthesisMs int64, factsFound int,
	r *retrievalResult, citations int, embedderAvailable bool, errStr string) {
	l := &db.QueryLog{
		ID:                 db.NewID(),
		Endpoint:           endpoint,
		Question:           question,
		TotalLatencyMs:     totalMs,
		RetrievalLatencyMs: retrievalMs,
		SynthesisLatencyMs: synthesisMs,
		FactsFound:         factsFound,
		CitationsCount:     citations,
		EmbedderAvailable:  embedderAvailable,
		Error:              errStr,
		CreatedAt:          time.Now(),
	}
	if r != nil {
		l.FactsByVector = len(r.factsByVector)
		l.FactsByText = len(r.factsByText)
		l.FactsByGraph = len(r.graphFacts)
		l.ChunksByVector = len(r.chunksByVector)
		l.ChunksByText = len(r.chunksByText)
	}
	if err := q.store.CreateQueryLog(ctx, l); err != nil {
		q.logger.Warn("failed to write query log", "error", err)
	}
}

func (q *Querier) persistCitations(ctx context.Context, citations []model.Citation) {
	if len(citations) == 0 {
		return
	}
	queryID := db.NewID()
	for _, c := range citations {
		if c.FactID == "" {
			continue
		}
		if err := q.store.CreateFactCitation(ctx, c.FactID, queryID); err != nil {
			q.logger.Warn("failed to persist fact citation",
				"fact_id", c.FactID, "query_id", queryID, "error", err)
		}
	}
}

// retrieve runs all retrieval layers concurrently and collects results.
// Vector layers are skipped when embedding is nil.
func (q *Querier) retrieve(ctx context.Context, question string, embedding []float32) *retrievalResult {
	r := &retrievalResult{}
	var mu sync.Mutex
	var wg sync.WaitGroup

	if embedding != nil {
		wg.Add(2)
		go func() {
			defer wg.Done()
			facts, err := q.store.SearchByVector(ctx, embedding, 20)
			if err != nil {
				q.logger.Warn("vector fact search failed", "error", err)
				return
			}
			mu.Lock()
			r.factsByVector = facts
			mu.Unlock()
		}()
		go func() {
			defer wg.Done()
			chunks, err := q.store.SearchChunksByVector(ctx, embedding, 10)
			if err != nil {
				q.logger.Warn("vector chunk search failed", "error", err)
				return
			}
			mu.Lock()
			r.chunksByVector = chunks
			mu.Unlock()
		}()
	}

	wg.Add(3)
	go func() {
		defer wg.Done()
		sanitized := fts.SanitizeQuery(question)
		if sanitized == "" {
			return
		}
		facts, err := q.store.SearchByText(ctx, sanitized, 10)
		if err != nil {
			q.logger.Warn("text search failed", "error", err)
			return
		}
		mu.Lock()
		r.factsByText = facts
		mu.Unlock()
	}()
	go func() {
		defer wg.Done()
		sanitized := fts.SanitizeQuery(question)
		if sanitized == "" {
			return
		}
		chunks, err := q.store.SearchChunksByText(ctx, sanitized, 10)
		if err != nil {
			q.logger.Warn("chunk text search failed", "error", err)
			return
		}
		mu.Lock()
		r.chunksByText = chunks
		mu.Unlock()
	}()
	go func() {
		defer wg.Done()
		facts := q.retrieveByGraph(ctx, question)
		mu.Lock()
		r.graphFacts = facts
		mu.Unlock()
	}()

	wg.Wait()
	return r
}


// retrieveByGraph extracts entity names from the question by word matching,
// looks them up, and traverses the graph for connected facts.
func (q *Querier) retrieveByGraph(ctx context.Context, question string) []model.Fact {
	words := strings.Fields(question)
	seen := map[string]bool{}
	var facts []model.Fact

	for _, word := range words {
		clean := strings.Trim(word, "?.,!;:'\"")
		if clean == "" || len(clean) < 2 {
			continue
		}
		entity, err := q.store.GetEntityByName(ctx, clean)
		if err != nil || entity == nil {
			continue
		}
		if seen[entity.ID] {
			continue
		}
		seen[entity.ID] = true

		graph, err := q.store.GetEntityGraph(ctx, entity.ID, 1)
		if err != nil {
			q.logger.Warn("graph traversal failed", "entity", entity.Name, "error", err)
			continue
		}

		for _, rel := range graph.Relationships {
			if rel.SourceFact != "" {
				fact, err := q.store.GetFact(ctx, rel.SourceFact)
				if err == nil && fact != nil {
					facts = append(facts, *fact)
				}
			}
		}
	}

	return facts
}

// rankedFact is a fact with its merged score from all retrieval layers.
type rankedFact struct {
	fact  model.Fact
	score float64
}

// mergeAndRank deduplicates facts across all layers and ranks them
// using Reciprocal Rank Fusion (k=60).
func (q *Querier) mergeAndRank(r *retrievalResult) []rankedFact {
	const k = 60.0
	scores := map[string]float64{}
	facts := map[string]model.Fact{}

	for rank := range r.factsByVector {
		sf := &r.factsByVector[rank]
		scores[sf.Fact.ID] += 1.0 / (k + float64(rank+1))
		facts[sf.Fact.ID] = sf.Fact
	}

	for rank := range r.factsByText {
		sf := &r.factsByText[rank]
		scores[sf.Fact.ID] += 1.0 / (k + float64(rank+1))
		facts[sf.Fact.ID] = sf.Fact
	}

	for rank := range r.graphFacts {
		gf := &r.graphFacts[rank]
		scores[gf.ID] += 1.0 / (k + float64(rank+1))
		facts[gf.ID] = *gf
	}

	now := time.Now().UTC()
	ranked := make([]rankedFact, 0, len(facts))
	for id := range facts {
		f := facts[id]
		if f.Validity.ValidUntil != nil && f.Validity.ValidUntil.Before(now) {
			continue
		}
		ranked = append(ranked, rankedFact{fact: f, score: scores[id]})
	}

	sortByScore(ranked)
	return ranked
}

// sortByScore sorts ranked facts in descending order by score.
func sortByScore(ranked []rankedFact) {
	for i := 1; i < len(ranked); i++ {
		for j := i; j > 0 && ranked[j].score > ranked[j-1].score; j-- {
			ranked[j], ranked[j-1] = ranked[j-1], ranked[j]
		}
	}
}

// enrichedFact is a ranked fact with optional transcript context.
type enrichedFact struct {
	rankedFact
	context       string
	chunkContexts []string
}

// enrichWithContext loads surrounding transcript lines for top-K facts
// that have source line references, and also loads context from top
// chunk retrieval results.
func (q *Querier) enrichWithContext(ctx context.Context, ranked []rankedFact, r *retrievalResult) []enrichedFact {
	const topK = 10
	limit := topK
	if len(ranked) < limit {
		limit = len(ranked)
	}

	enriched := make([]enrichedFact, len(ranked))
	for i := range ranked {
		enriched[i] = enrichedFact{rankedFact: ranked[i]}
	}

	if q.transcriptDir == "" {
		return enriched
	}

	for i := 0; i < limit; i++ {
		fact := enriched[i].fact
		if fact.Source.LineRange == nil || fact.Source.TranscriptFile == "" {
			continue
		}
		srcCtx, err := readSourceContext(fact, q.transcriptDir)
		if err != nil {
			q.logger.Debug("failed to read source context",
				"fact_id", fact.ID, "error", err)
			continue
		}
		enriched[i].context = srcCtx
	}

	chunkContexts := q.loadChunkContexts(ctx, r)
	if len(chunkContexts) > 0 && len(enriched) > 0 {
		enriched[0].chunkContexts = chunkContexts
	}

	return enriched
}

const maxChunkContexts = 5

// loadChunkContexts resolves top chunk results into transcript text.
func (q *Querier) loadChunkContexts(ctx context.Context, r *retrievalResult) []string {
	if r == nil {
		return nil
	}

	type scoredChunkID struct {
		chunk db.ScoredChunk
		score float64
	}
	seen := map[string]bool{}
	var candidates []scoredChunkID

	for i, sc := range r.chunksByVector {
		if !seen[sc.Chunk.ID] {
			seen[sc.Chunk.ID] = true
			candidates = append(candidates, scoredChunkID{chunk: sc, score: 1.0 / float64(i+1)})
		}
	}
	for i, sc := range r.chunksByText {
		if !seen[sc.Chunk.ID] {
			seen[sc.Chunk.ID] = true
			candidates = append(candidates, scoredChunkID{chunk: sc, score: 1.0 / float64(i+1)})
		}
	}

	if len(candidates) == 0 {
		return nil
	}

	limit := maxChunkContexts
	if len(candidates) < limit {
		limit = len(candidates)
	}

	var contexts []string
	for i := 0; i < limit; i++ {
		c := candidates[i].chunk.Chunk
		tr, err := q.store.GetTranscript(ctx, c.TranscriptID)
		if err != nil || tr == nil {
			q.logger.Debug("failed to get transcript for chunk",
				"chunk_id", c.ID, "transcript_id", c.TranscriptID, "error", err)
			continue
		}

		path := filepath.Join(q.transcriptDir, tr.FilePath)
		text, err := readContextLines(path, c.LineStart, c.LineEnd, 3)
		if err != nil {
			q.logger.Debug("failed to read chunk context",
				"chunk_id", c.ID, "path", tr.FilePath, "error", err)
			continue
		}
		if text == "" {
			continue
		}

		header := fmt.Sprintf("--- %s lines %d-%d (chunk) ---", tr.FilePath, c.LineStart, c.LineEnd)
		contexts = append(contexts, header+"\n"+text)
	}

	return contexts
}

type builtPrompt struct {
	system string
	user   string
}

// buildPrompt constructs the system and user prompts for LLM synthesis.
func (q *Querier) buildPrompt(question string, facts []enrichedFact) builtPrompt {
	var userParts []string
	userParts = append(userParts, "### Question\n"+question)

	if len(facts) > 0 {
		var factLines []string
		for i := range facts {
			f := &facts[i].fact
			date := f.CreatedAt.Format("2006-01-02")
			line := fmt.Sprintf("- [%s] (%s, confidence=%.2f, %s) %s: %s",
				f.ID, f.FactType, f.Confidence, date, f.Subject, f.Content)
			factLines = append(factLines, line)
		}
		userParts = append(userParts, "### Facts\n"+strings.Join(factLines, "\n"))
	}

	var contextParts []string
	for i := range facts {
		ef := &facts[i]
		if ef.context != "" {
			var header string
			if ef.fact.Source.LineRange != nil {
				header = fmt.Sprintf("--- %s lines %d-%d ---",
					ef.fact.Source.TranscriptFile,
					ef.fact.Source.LineRange[0], ef.fact.Source.LineRange[1])
			} else {
				header = fmt.Sprintf("--- %s ---", ef.fact.Source.TranscriptFile)
			}
			contextParts = append(contextParts, header+"\n"+ef.context)
		}
		contextParts = append(contextParts, ef.chunkContexts...)
	}
	if len(contextParts) > 0 {
		userParts = append(userParts, "### Transcript Context\n"+strings.Join(contextParts, "\n\n"))
	}

	return builtPrompt{
		system: querySystemPrompt,
		user:   strings.Join(userParts, "\n\n"),
	}
}

// rawQueryResponse is the JSON shape the LLM returns for queries.
type rawQueryResponse struct {
	Answer     string          `json:"answer"`
	Citations  []model.Citation `json:"citations"`
	Confidence float64         `json:"confidence"`
	Notes      string          `json:"notes"`
}

// parseResponse extracts a QueryResult from the LLM's JSON response.
func parseResponse(content string) (*model.QueryResult, error) {
	content = stripMarkdownFences(content)

	var raw rawQueryResponse
	if err := json.Unmarshal([]byte(content), &raw); err != nil {
		return nil, fmt.Errorf("parse JSON: %w", err)
	}

	return &model.QueryResult{
		Answer:    raw.Answer,
		Citations: raw.Citations,
	}, nil
}

// stripMarkdownFences removes ```json ... ``` wrapping.
func stripMarkdownFences(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		if idx := strings.Index(s, "\n"); idx != -1 {
			s = s[idx+1:]
		}
	}
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}

// readSourceContext loads transcript lines for a fact's source reference.
func readSourceContext(fact model.Fact, transcriptDir string) (string, error) {
	if fact.Source.LineRange == nil {
		return "", nil
	}
	path := filepath.Join(transcriptDir, fact.Source.TranscriptFile)
	return readContextLines(path, fact.Source.LineRange[0], fact.Source.LineRange[1], 3)
}

// readContextLines reads lines [start, end] (1-based inclusive) from a file
// with contextLines extra lines before and after.
func readContextLines(filePath string, start, end, contextLines int) (string, error) {
	data, err := os.ReadFile(filePath) //nolint:gosec // path from trusted DB, not user input
	if err != nil {
		return "", fmt.Errorf("read file: %w", err)
	}

	lines := strings.Split(string(data), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	from := start - 1 - contextLines
	if from < 0 {
		from = 0
	}
	to := end + contextLines
	if to > len(lines) {
		to = len(lines)
	}
	if from >= len(lines) {
		return "", nil
	}

	return strings.Join(lines[from:to], "\n") + "\n", nil
}

const querySystemPrompt = `You are a knowledge retrieval system. You receive a question and a set of relevant facts, consolidations, and transcript context retrieved from a knowledge base. Your job is to answer the question accurately using ONLY the provided information. Return valid JSON only -- no commentary, no markdown fences, no explanation.

Return a single JSON object with this exact structure:

{"answer": "<your answer, 1-5 sentences>", "citations": [{"fact_id": "<ID of fact used>"}, {"consolidation_id": "<ID of consolidation used>"}], "confidence": <0.0 to 1.0>, "notes": "<optional: contradictions, gaps, or caveats>"}

Rules:
1. Use ONLY the provided facts, consolidations, and transcript context. Do not use external knowledge.
2. Cite every fact or consolidation that contributed to your answer.
3. If multiple facts contradict each other, mention the contradiction in "notes" and base the answer on the most recent or highest-confidence fact.
4. If a fact has been superseded, prefer the newer fact.
5. If the provided information is insufficient, say so clearly and set confidence below 0.3.
6. Keep the answer concise -- 1-5 sentences.
7. If transcript context is provided, use it to enrich the answer but cite the structured facts.
8. Temporal awareness: prefer recent facts over old ones.`
