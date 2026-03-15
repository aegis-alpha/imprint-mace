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

// Query answers a natural language question using hybrid retrieval.
func (q *Querier) Query(ctx context.Context, question string) (*model.QueryResult, error) {
	var embedding []float32
	if q.embedder != nil {
		var err error
		embedding, err = q.embedder.Embed(ctx, question)
		if err != nil {
			q.logger.Warn("failed to embed question, falling back to text-only", "error", err)
		}
	}

	retrieved := q.retrieve(ctx, question, embedding)

	ranked := q.mergeAndRank(retrieved)

	enriched := q.enrichWithContext(ctx, ranked, retrieved)

	prompt := q.buildPrompt(question, enriched)

	resp, err := q.sender.Send(ctx, provider.Request{
		SystemPrompt: prompt.system,
		UserPrompt:   prompt.user,
		MaxTokens:    2048,
	})
	if err != nil {
		return nil, fmt.Errorf("query LLM: %w", err)
	}

	result, err := parseResponse(resp.Content)
	if err != nil {
		return nil, fmt.Errorf("parse query response: %w", err)
	}

	result.FactsConsulted = len(ranked)

	q.logger.Info("query complete",
		"question_len", len(question),
		"facts_consulted", result.FactsConsulted,
		"citations", len(result.Citations),
	)

	return result, nil
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
		sanitized := sanitizeFTS5Query(question)
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
		sanitized := sanitizeFTS5Query(question)
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

// sanitizeFTS5Query removes characters that are special in FTS5 syntax.
func sanitizeFTS5Query(q string) string {
	replacer := strings.NewReplacer(
		"?", "", "!", "", ".", "", ",", "", ";", "",
		":", "", "'", "", "\"", "", "(", "", ")", "",
		"*", "", "+", "", "-", "", "^", "",
		"{", "", "}", "", "[", "", "]", "",
	)
	cleaned := replacer.Replace(q)
	words := strings.Fields(cleaned)
	if len(words) == 0 {
		return ""
	}
	return strings.Join(words, " ")
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

	for rank, sf := range r.factsByVector {
		scores[sf.Fact.ID] += 1.0 / (k + float64(rank+1))
		facts[sf.Fact.ID] = sf.Fact
	}

	for rank, sf := range r.factsByText {
		scores[sf.Fact.ID] += 1.0 / (k + float64(rank+1))
		facts[sf.Fact.ID] = sf.Fact
	}

	for rank, gf := range r.graphFacts {
		scores[gf.ID] += 1.0 / (k + float64(rank+1))
		facts[gf.ID] = gf
	}

	now := time.Now().UTC()
	ranked := make([]rankedFact, 0, len(facts))
	for id, fact := range facts {
		if fact.Validity.ValidUntil != nil && fact.Validity.ValidUntil.Before(now) {
			continue
		}
		ranked = append(ranked, rankedFact{fact: fact, score: scores[id]})
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
	for i, rf := range ranked {
		enriched[i] = enrichedFact{rankedFact: rf}
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
		for _, ef := range facts {
			f := ef.fact
			date := f.CreatedAt.Format("2006-01-02")
			line := fmt.Sprintf("- [%s] (%s, confidence=%.2f, %s) %s: %s",
				f.ID, f.FactType, f.Confidence, date, f.Subject, f.Content)
			factLines = append(factLines, line)
		}
		userParts = append(userParts, "### Facts\n"+strings.Join(factLines, "\n"))
	}

	var contextParts []string
	for _, ef := range facts {
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
	if strings.HasSuffix(s, "```") {
		s = s[:len(s)-3]
	}
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
	data, err := os.ReadFile(filePath)
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
