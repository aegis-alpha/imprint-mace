package query

import (
	"context"
	"fmt"
	"strings"

	"github.com/aegis-alpha/imprint-mace/internal/model"
)

// RankedItem is one merge-ranked fact passed to a post-retrieval reranker API.
// Hot/cooldown raw rows use [Reranker] only after conversion to structured text upstream;
// the querier reranks contiguous fact-only prefixes (see applyRerankToRankedItems).
type RankedItem struct {
	Fact  model.Fact
	Score float64
}

// Reranker reorders a slice of ranked items for a query. Implementations must
// return a permutation of the input (same length, same fact IDs multiset).
// topN is passed through to APIs such as Cohere's top_n (typically len(items)).
type Reranker interface {
	Rerank(ctx context.Context, query string, items []RankedItem, topN int) ([]RankedItem, error)
}

// WithReranker attaches an optional post-merge reranker. topN controls how many
// leading merged items are considered: only an all-fact prefix of that length is
// reranked; trailing items keep merge order. If topN is 0, the full slice is
// considered (still requires an all-fact prefix for the reranked segment).
func WithReranker(r Reranker, topN int) QuerierOption {
	return func(q *Querier) {
		q.reranker = r
		q.rerankTopN = topN
	}
}

// applyRerankToRankedItems reorders the first rerankTopN items when they are all
// structured facts (no hot/cooldown rows in that prefix). Otherwise returns items unchanged.
func (q *Querier) applyRerankToRankedItems(ctx context.Context, question string, items []rankedItem) []rankedItem {
	if q.reranker == nil || len(items) == 0 {
		return items
	}
	n := q.rerankTopN
	if n <= 0 || n > len(items) {
		n = len(items)
	}
	head := items[:n]
	tail := items[n:]
	for i := range head {
		if head[i].fact == nil {
			return items
		}
	}
	ri := make([]RankedItem, len(head))
	for i := range head {
		ri[i] = RankedItem{Fact: *head[i].fact, Score: head[i].score}
	}
	out, err := q.reranker.Rerank(ctx, question, ri, n)
	if err != nil {
		q.logger.Warn("rerank failed, using merge order", "error", err)
		return items
	}
	if len(out) != len(head) {
		q.logger.Warn("rerank returned wrong length, using merge order",
			"want", len(head), "got", len(out))
		return items
	}
	if !sameFactIDMultisetRankedItems(head, out) {
		q.logger.Warn("rerank changed fact set, using merge order")
		return items
	}
	newHead := make([]rankedItem, len(out))
	for i := range out {
		fp := new(model.Fact)
		*fp = out[i].Fact
		newHead[i] = rankedItem{fact: fp, score: out[i].Score}
	}
	return append(newHead, tail...)
}

func sameFactIDMultisetRankedItems(head []rankedItem, items []RankedItem) bool {
	if len(head) != len(items) {
		return false
	}
	want := make(map[string]int, len(head))
	for _, it := range head {
		if it.fact == nil {
			return false
		}
		want[it.fact.ID]++
	}
	for _, it := range items {
		id := it.Fact.ID
		if want[id] == 0 {
			return false
		}
		want[id]--
	}
	for _, c := range want {
		if c != 0 {
			return false
		}
	}
	return true
}

// factTextForRerank builds a single string per fact for cross-encoder APIs.
func factTextForRerank(f model.Fact) string {
	subj := strings.TrimSpace(f.Subject)
	content := strings.TrimSpace(f.Content)
	if subj != "" && content != "" {
		return subj + ": " + content
	}
	if content != "" {
		return content
	}
	return subj
}

// NewRerankerFromConfig builds a reranker from a single provider entry.
func NewRerankerFromConfig(cfg model.ProviderConfig) (Reranker, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.Name)) {
	case "cohere":
		return NewCohereReranker(cfg)
	default:
		return nil, fmt.Errorf("unsupported reranker provider %q", cfg.Name)
	}
}
