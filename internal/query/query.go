// Package query implements the hybrid retrieval pipeline for answering
// natural language questions from the knowledge base.
//
// Pipeline: embed question -> parallel retrieval (cold + hot/cooldown layers) -> merge/rank
// -> optional rerank -> ReadContext enrichment -> LLM synthesis -> parse response.
package query

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
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
	// mergeStrategy: "" or "rrf" = Reciprocal Rank Fusion; "set-union" = dense order, then unseen sparse, then unseen graph.
	mergeStrategy string
	reranker      Reranker
	// rerankTopN: if > 0, only this many leading facts are reranked; rest keep merge order.
	rerankTopN int
}

// QuerierOption configures a Querier created with New.
type QuerierOption func(*Querier)

// WithMergeStrategy selects how fact lists are merged after retrieval.
// Use "rrf" (default) for reciprocal rank fusion, or "set-union" for dense-first
// positional ordering (see mergeSetUnion).
func WithMergeStrategy(s string) QuerierOption {
	s = strings.TrimSpace(strings.ToLower(s))
	return func(q *Querier) {
		q.mergeStrategy = s
	}
}

// New creates a Querier. Pass nil for embedder to disable vector search
// (falls back to FTS5 only).
func New(store db.Store, embedder provider.Embedder, sender Sender, transcriptDir string, logger *slog.Logger, opts ...QuerierOption) *Querier {
	q := &Querier{
		store:         store,
		embedder:      embedder,
		sender:        sender,
		transcriptDir: transcriptDir,
		logger:        logger,
	}
	for _, o := range opts {
		o(q)
	}
	return q
}

func (q *Querier) mergeRanked(r *retrievalResult) []rankedItem {
	switch q.mergeStrategy {
	case "set-union", "set_union":
		return q.mergeSetUnion(r)
	default:
		return q.mergeAndRank(r)
	}
}

// retrievalResult holds the raw output from all retrieval layers
// before merging and ranking.
type retrievalResult struct {
	factsByVector    []db.ScoredFact
	factsByText      []db.ScoredFact
	chunksByVector   []db.ScoredChunk
	chunksByText     []db.ScoredChunk
	graphFacts       []model.Fact
	hotByVector      []db.ScoredHotMessage
	hotByText        []db.ScoredHotMessage
	cooldownByVector []db.ScoredCooldownMessage
	cooldownByText   []db.ScoredCooldownMessage
}

const hotRetrievalLimit = 10

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

// Retrieve runs the retrieval pipeline (embed, parallel search, merge) without LLM synthesis.
// It returns ranked cold facts only (structured knowledge). Hot and cooldown raw messages are
// omitted so retrieval eval measures cold-path quality. A future RetrieveWithHot API may expose
// merged hot + cold rankings for callers that need them.
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
	ranked := q.mergeRanked(r)
	ranked = q.applyOptionalRerank(ctx, question, embedding, ranked)

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

	var out []RankedFact
	for i := range ranked {
		if ranked[i].fact == nil {
			continue
		}
		f := ranked[i].fact
		out = append(out, RankedFact{
			Fact:       *f,
			Score:      ranked[i].score,
			FromVector: vectorIDs[f.ID],
			FromText:   textIDs[f.ID],
			FromGraph:  graphIDs[f.ID],
		})
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

	ranked := q.mergeRanked(retrieved)
	ranked = q.applyOptionalRerank(ctx, question, embedding, ranked)

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
			countStructuredFacts(ranked), retrieved, 0, embedderAvailable, err.Error())
		return nil, fmt.Errorf("query LLM: %w", err)
	}

	result, err := parseResponse(resp.Content)
	synthesisMs := time.Since(synthesisStart).Milliseconds()
	if err != nil {
		totalMs := time.Since(totalStart).Milliseconds()
		q.writeQueryLog(ctx, "query", question, totalMs, retrievalMs, synthesisMs,
			countStructuredFacts(ranked), retrieved, 0, embedderAvailable, err.Error())
		return nil, fmt.Errorf("parse query response: %w", err)
	}

	result.FactsConsulted = countStructuredFacts(ranked)

	q.persistCitations(ctx, result.Citations)

	totalMs := time.Since(totalStart).Milliseconds()
	q.writeQueryLog(ctx, "query", question, totalMs, retrievalMs, synthesisMs,
		countStructuredFacts(ranked), retrieved, len(result.Citations), embedderAvailable, "")

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
		l.HotByVector = len(r.hotByVector)
		l.HotByText = len(r.hotByText)
		l.CooldownByVector = len(r.cooldownByVector)
		l.CooldownByText = len(r.cooldownByText)
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
		// HotMessageID citations are not persisted to fact_citations; no store API yet.
	}
}

// retrieve runs all retrieval layers concurrently and collects results (9 layers when embedding is set).
// Vector layers are skipped when embedding is nil.
func (q *Querier) retrieve(ctx context.Context, question string, embedding []float32) *retrievalResult {
	r := &retrievalResult{}
	var mu sync.Mutex
	var wg sync.WaitGroup

	if embedding != nil {
		wg.Add(4)
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
		go func() {
			defer wg.Done()
			msgs, err := q.store.SearchHotByVector(ctx, embedding, hotRetrievalLimit)
			if err != nil {
				q.logger.Warn("hot vector search failed", "error", err)
				return
			}
			mu.Lock()
			r.hotByVector = msgs
			mu.Unlock()
		}()
		go func() {
			defer wg.Done()
			msgs, err := q.store.SearchCooldownByVector(ctx, embedding, hotRetrievalLimit)
			if err != nil {
				q.logger.Warn("cooldown vector search failed", "error", err)
				return
			}
			mu.Lock()
			r.cooldownByVector = msgs
			mu.Unlock()
		}()
	}

	wg.Add(5)
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
	go func() {
		defer wg.Done()
		sanitized := fts.SanitizeQuery(question)
		if sanitized == "" {
			return
		}
		msgs, err := q.store.SearchHotByText(ctx, sanitized, hotRetrievalLimit)
		if err != nil {
			q.logger.Warn("hot text search failed", "error", err)
			return
		}
		mu.Lock()
		r.hotByText = msgs
		mu.Unlock()
	}()
	go func() {
		defer wg.Done()
		sanitized := fts.SanitizeQuery(question)
		if sanitized == "" {
			return
		}
		msgs, err := q.store.SearchCooldownByText(ctx, sanitized, hotRetrievalLimit)
		if err != nil {
			q.logger.Warn("cooldown text search failed", "error", err)
			return
		}
		mu.Lock()
		r.cooldownByText = msgs
		mu.Unlock()
	}()

	wg.Wait()
	return r
}

// applyOptionalRerank runs post-merge reranking with per-request state.
func (q *Querier) applyOptionalRerank(ctx context.Context, question string, embedding []float32, items []rankedItem) []rankedItem {
	if cr, ok := q.reranker.(*CosineReranker); ok {
		local := *cr
		local.SetQueryEmbedding(embedding)
		return q.applyRerankWith(ctx, question, items, &local)
	}
	return q.applyRerankWith(ctx, question, items, q.reranker)
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

// rankedItem is a merged retrieval hit: structured fact or raw hot/cooldown message (HOT-PHASE-SPEC 7.4).
type rankedItem struct {
	fact       *model.Fact
	hotMessage *model.HotMessage
	score      float64
	// isRawMessage is true for rows from hot_messages or cooldown_messages (not structured facts).
	isRawMessage bool
	// messageCitationPrefix is "hot:" or "cool:" when isRawMessage; empty for facts. Matches RRF merge keys.
	messageCitationPrefix string
}

func countStructuredFacts(ranked []rankedItem) int {
	n := 0
	for i := range ranked {
		if ranked[i].fact != nil {
			n++
		}
	}
	return n
}

func citationPrefixForMergeID(id string) string {
	if strings.HasPrefix(id, "cool:") {
		return "cool:"
	}
	return "hot:"
}

// rankedFact is a fact with score for data-quality metrics (structured facts only).
type rankedFact struct {
	fact  model.Fact
	score float64
}

type dataQualityMetrics struct {
	FactCount       int
	AvgConfidence   float64
	MinConfidence   float64
	MaxConfidence   float64
	SupersededCount int
	NearDuplicates  int
	AgeSpreadDays   float64
	SourceCount     int
	OldestFactDays  float64
	NewestFactDays  float64
}

func computeDataQuality(ranked []rankedFact) dataQualityMetrics {
	if len(ranked) == 0 {
		return dataQualityMetrics{}
	}

	now := time.Now()
	m := dataQualityMetrics{
		FactCount:     len(ranked),
		MinConfidence: ranked[0].fact.Confidence,
		MaxConfidence: ranked[0].fact.Confidence,
	}

	var oldest, newest time.Time
	sources := map[string]bool{}
	var totalConf float64

	for i := range ranked {
		f := &ranked[i].fact
		totalConf += f.Confidence
		if f.Confidence < m.MinConfidence {
			m.MinConfidence = f.Confidence
		}
		if f.Confidence > m.MaxConfidence {
			m.MaxConfidence = f.Confidence
		}
		if f.SupersededBy != "" {
			m.SupersededCount++
		}
		if f.Source.TranscriptFile != "" {
			sources[f.Source.TranscriptFile] = true
		}
		if oldest.IsZero() || f.CreatedAt.Before(oldest) {
			oldest = f.CreatedAt
		}
		if newest.IsZero() || f.CreatedAt.After(newest) {
			newest = f.CreatedAt
		}
	}

	m.AvgConfidence = totalConf / float64(len(ranked))
	m.SourceCount = len(sources)

	if !oldest.IsZero() && !newest.IsZero() {
		m.AgeSpreadDays = newest.Sub(oldest).Hours() / 24.0
		m.OldestFactDays = now.Sub(oldest).Hours() / 24.0
		m.NewestFactDays = now.Sub(newest).Hours() / 24.0
	}

	for i := 0; i < len(ranked); i++ {
		for j := i + 1; j < len(ranked); j++ {
			a, b := &ranked[i].fact, &ranked[j].fact
			if a.Subject != b.Subject || a.Subject == "" {
				continue
			}
			if jaccardWords(a.Content, b.Content) > 0.7 {
				m.NearDuplicates++
			}
		}
	}

	return m
}

func jaccardWords(a, b string) float64 {
	setA := wordSet(a)
	setB := wordSet(b)
	if len(setA) == 0 && len(setB) == 0 {
		return 0
	}
	inter := 0
	for w := range setA {
		if setB[w] {
			inter++
		}
	}
	union := len(setA) + len(setB) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

func wordSet(s string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(strings.ToLower(s)) {
		m[w] = true
	}
	return m
}

// mergeAndRank deduplicates facts and raw messages across all layers using RRF (k=60).
// Hot and cooldown hits use prefixed keys "hot:" and "cool:" so they do not collide with fact IDs.
func (q *Querier) mergeAndRank(r *retrievalResult) []rankedItem {
	const k = 60.0
	scores := map[string]float64{}
	facts := map[string]model.Fact{}
	hotMsgs := map[string]model.HotMessage{}

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

	for rank := range r.hotByVector {
		sm := &r.hotByVector[rank]
		id := "hot:" + sm.Message.ID
		scores[id] += 1.0 / (k + float64(rank+1))
		hotMsgs[id] = sm.Message
	}
	for rank := range r.hotByText {
		sm := &r.hotByText[rank]
		id := "hot:" + sm.Message.ID
		scores[id] += 1.0 / (k + float64(rank+1))
		hotMsgs[id] = sm.Message
	}
	for rank := range r.cooldownByVector {
		sm := &r.cooldownByVector[rank]
		id := "cool:" + sm.Message.ID
		scores[id] += 1.0 / (k + float64(rank+1))
		hotMsgs[id] = cooldownToHot(&sm.Message)
	}
	for rank := range r.cooldownByText {
		sm := &r.cooldownByText[rank]
		id := "cool:" + sm.Message.ID
		scores[id] += 1.0 / (k + float64(rank+1))
		hotMsgs[id] = cooldownToHot(&sm.Message)
	}

	now := time.Now().UTC()
	ranked := make([]rankedItem, 0, len(scores))
	for id, sc := range scores {
		if f, ok := facts[id]; ok {
			if f.Validity.ValidUntil != nil && f.Validity.ValidUntil.Before(now) {
				continue
			}
			fc := new(model.Fact)
			*fc = f
			ranked = append(ranked, rankedItem{fact: fc, score: sc})
			continue
		}
		if hm, ok := hotMsgs[id]; ok {
			hmc := new(model.HotMessage)
			*hmc = hm
			ranked = append(ranked, rankedItem{
				hotMessage:            hmc,
				score:                 sc,
				isRawMessage:          true,
				messageCitationPrefix: citationPrefixForMergeID(id),
			})
		}
	}

	sortRankedItemsByScore(ranked)
	return ranked
}

// mergeSetUnion orders facts like Omni-SimpleMem-style fusion: keep vector hits
// in similarity order, append FTS5-only hits, then graph-only hits, then hot/cooldown
// vector and text layers (prefixed keys), with no RRF score. Expired facts are dropped.
// Positional scores decrease by 0.01 per rank for downstream compatibility.
func (q *Querier) mergeSetUnion(r *retrievalResult) []rankedItem {
	now := time.Now().UTC()
	seen := make(map[string]struct{},
		len(r.factsByVector)+len(r.factsByText)+len(r.graphFacts)+
			len(r.hotByVector)+len(r.hotByText)+len(r.cooldownByVector)+len(r.cooldownByText))
	var out []rankedItem

	tryAddFact := func(f model.Fact) {
		if f.Validity.ValidUntil != nil && f.Validity.ValidUntil.Before(now) {
			return
		}
		if _, ok := seen[f.ID]; ok {
			return
		}
		seen[f.ID] = struct{}{}
		pos := len(out)
		fc := new(model.Fact)
		*fc = f
		out = append(out, rankedItem{
			fact:  fc,
			score: 1.0 - float64(pos)*0.01,
		})
	}

	tryAddHot := func(m model.HotMessage, prefix string) {
		key := prefix + m.ID
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		pos := len(out)
		mc := new(model.HotMessage)
		*mc = m
		out = append(out, rankedItem{
			hotMessage:            mc,
			score:                 1.0 - float64(pos)*0.01,
			isRawMessage:          true,
			messageCitationPrefix: prefix,
		})
	}

	for i := range r.factsByVector {
		tryAddFact(r.factsByVector[i].Fact)
	}
	for i := range r.factsByText {
		tryAddFact(r.factsByText[i].Fact)
	}
	for i := range r.graphFacts {
		tryAddFact(r.graphFacts[i])
	}
	for i := range r.hotByVector {
		tryAddHot(r.hotByVector[i].Message, "hot:")
	}
	for i := range r.hotByText {
		tryAddHot(r.hotByText[i].Message, "hot:")
	}
	for i := range r.cooldownByVector {
		tryAddHot(cooldownToHot(&r.cooldownByVector[i].Message), "cool:")
	}
	for i := range r.cooldownByText {
		tryAddHot(cooldownToHot(&r.cooldownByText[i].Message), "cool:")
	}
	return out
}

// cooldownToHot converts a CooldownMessage to a HotMessage for merge/synthesis compatibility.
func cooldownToHot(cm *model.CooldownMessage) model.HotMessage {
	return model.HotMessage{
		ID:                cm.ID,
		Speaker:           cm.Speaker,
		Content:           cm.Content,
		Timestamp:         cm.Timestamp,
		Platform:          cm.Platform,
		PlatformSessionID: cm.PlatformSessionID,
		LinkerRef:         cm.LinkerRef,
		HasEmbedding:      cm.HasEmbedding,
		CreatedAt:         cm.CreatedAt,
	}
}

// sortRankedItemsByScore sorts ranked items in descending order by score (insertion sort, stable enough for typical N).
func sortRankedItemsByScore(ranked []rankedItem) {
	for i := 1; i < len(ranked); i++ {
		for j := i; j > 0 && ranked[j].score > ranked[j-1].score; j-- {
			ranked[j], ranked[j-1] = ranked[j-1], ranked[j]
		}
	}
}

// enrichedFact is a ranked item with optional transcript context (facts only).
type enrichedFact struct {
	rankedItem
	context       string
	chunkContexts []string
}

// enrichWithContext loads surrounding transcript lines for top-K facts
// that have source line references, and also loads context from top
// chunk retrieval results.
func (q *Querier) enrichWithContext(ctx context.Context, ranked []rankedItem, r *retrievalResult) []enrichedFact {
	const topK = 10
	limit := topK
	if len(ranked) < limit {
		limit = len(ranked)
	}

	enriched := make([]enrichedFact, len(ranked))
	for i := range ranked {
		enriched[i] = enrichedFact{rankedItem: ranked[i]}
	}

	if q.transcriptDir == "" {
		return enriched
	}

	for i := 0; i < limit; i++ {
		if enriched[i].fact == nil {
			continue
		}
		fact := enriched[i].fact
		if fact.Source.LineRange == nil || fact.Source.TranscriptFile == "" {
			continue
		}
		srcCtx, err := readSourceContext(*fact, q.transcriptDir)
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

	fresh := collectFreshMessagesSorted(facts)
	if len(fresh) > 0 {
		lines := make([]string, len(fresh))
		for i := range fresh {
			lines[i] = formatFreshMessageLine(fresh[i].msg, fresh[i].citationPrefix)
		}
		userParts = append(userParts, "### Fresh Messages (recent, unverified)\n"+strings.Join(lines, "\n"))
	}

	var factLines []string
	var dqInput []rankedFact
	for i := range facts {
		if facts[i].fact == nil {
			continue
		}
		f := facts[i].fact
		date := f.CreatedAt.Format("2006-01-02")
		line := fmt.Sprintf("- [%s] (%s, confidence=%.2f, %s) %s: %s",
			f.ID, f.FactType, f.Confidence, date, f.Subject, f.Content)
		factLines = append(factLines, line)
		dqInput = append(dqInput, rankedFact{fact: *f, score: facts[i].score})
	}
	if len(factLines) > 0 {
		userParts = append(userParts, "### Facts\n"+strings.Join(factLines, "\n"))
		dq := computeDataQuality(dqInput)
		userParts = append(userParts, formatDataQuality(dq))
	}

	var contextParts []string
	for i := range facts {
		ef := &facts[i]
		if ef.fact == nil || ef.context == "" {
			contextParts = append(contextParts, ef.chunkContexts...)
			continue
		}
		var header string
		if ef.fact.Source.LineRange != nil {
			header = fmt.Sprintf("--- %s lines %d-%d ---",
				ef.fact.Source.TranscriptFile,
				ef.fact.Source.LineRange[0], ef.fact.Source.LineRange[1])
		} else {
			header = fmt.Sprintf("--- %s ---", ef.fact.Source.TranscriptFile)
		}
		contextParts = append(contextParts, header+"\n"+ef.context)
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

// freshMessageLine is one row for the synthesis prompt (hot or cooldown table).
type freshMessageLine struct {
	msg            model.HotMessage
	citationPrefix string // "hot:" or "cool:" (matches merge / RRF keys)
}

func collectFreshMessagesSorted(facts []enrichedFact) []freshMessageLine {
	seen := map[string]struct{}{}
	var out []freshMessageLine
	for i := range facts {
		if facts[i].hotMessage == nil {
			continue
		}
		m := facts[i].hotMessage
		if _, ok := seen[m.ID]; ok {
			continue
		}
		seen[m.ID] = struct{}{}
		prefix := facts[i].messageCitationPrefix
		if prefix == "" {
			prefix = "hot:"
		}
		out = append(out, freshMessageLine{msg: *m, citationPrefix: prefix})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].msg.Timestamp.After(out[j].msg.Timestamp)
	})
	return out
}

func formatFreshMessageLine(m model.HotMessage, citationPrefix string) string {
	ts := m.Timestamp.Format("2006-01-02 15:04")
	suffix := ""
	if m.LinkerRef != "" {
		suffix = fmt.Sprintf(" (->%s)", m.LinkerRef)
	}
	tag := strings.TrimSuffix(citationPrefix, ":")
	if tag == "" {
		tag = "hot"
	}
	return fmt.Sprintf("- [%s:%s] (%s, %s%s): %s", tag, m.ID, m.Speaker, ts, suffix, m.Content)
}

func formatDataQuality(dq dataQualityMetrics) string {
	return fmt.Sprintf(`### Data Quality
- Facts retrieved: %d
- Average confidence: %.2f
- Confidence range: %.2f - %.2f
- Superseded facts included: %d
- Near-duplicate pairs: %d
- Source diversity: %d distinct files
- Age spread: %.1f days (oldest: %.1f days ago, newest: %.1f days ago)`,
		dq.FactCount, dq.AvgConfidence,
		dq.MinConfidence, dq.MaxConfidence,
		dq.SupersededCount, dq.NearDuplicates,
		dq.SourceCount,
		dq.AgeSpreadDays, dq.OldestFactDays, dq.NewestFactDays)
}

// rawQueryResponse is the JSON shape the LLM returns for queries.
type rawQueryResponse struct {
	Answer     string           `json:"answer"`
	Citations  []model.Citation `json:"citations"`
	Confidence float64          `json:"confidence"`
	Notes      string           `json:"notes"`
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

{"answer": "<your answer, 1-5 sentences>", "citations": [{"fact_id": "<ID of fact used>"}, {"consolidation_id": "<ID of consolidation used>"}, {"hot_message_id": "hot:<ULID> or cool:<ULID>"}], "confidence": <0.0 to 1.0>, "notes": "<optional: contradictions, gaps, or caveats>"}

Rules:
1. Use ONLY the provided facts, consolidations, transcript context, and any Fresh Messages section. Do not use external knowledge.
2. Cite every fact or consolidation that contributed to your answer.
3. If multiple facts contradict each other, mention the contradiction in "notes" and base the answer on the most recent or highest-confidence fact.
4. If a fact has been superseded, prefer the newer fact.
5. If the provided information is insufficient, say so clearly and set confidence below 0.3.
6. Keep the answer concise -- 1-5 sentences.
7. If transcript context is provided, use it to enrich the answer but cite the structured facts.
8. Temporal awareness: prefer recent facts over old ones.
9. Data quality awareness: A "Data Quality" section may be included in the input. Use it to calibrate your confidence:
   - If average confidence < 0.6 or fewer than 3 facts found, set your confidence below 0.5 and add a caveat (e.g. "Based on limited/low-confidence data: ...").
   - If superseded facts are present, note this and prefer non-superseded facts.
   - If age spread > 30 days, consider that older facts may be outdated.
   - If source diversity = 1, note that all information comes from a single source.
10. Fresh messages: A "Fresh Messages" section may be included. These are raw, unverified messages from recent conversations. They are the most current information but have not been verified through extraction.
    - When fresh messages confirm or update a structured fact, prefer the fresh message (it is newer).
    - When fresh messages contain a proposal or question (not a confirmed decision), mention it as "currently under discussion" -- do not present it as a decided fact.
    - When fresh messages contradict a high-confidence fact, show both and note the potential change.
    - You cannot cite fresh messages by fact_id. Use hot_message_id with the same prefix as in Fresh Messages: "hot:<ULID>" for hot-layer rows or "cool:<ULID>" for cooldown-layer rows (match the bracket tag shown in the prompt).`
