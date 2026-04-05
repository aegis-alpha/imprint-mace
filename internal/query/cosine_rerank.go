package query

import (
	"context"
	"math"
	"sort"
)

// CosineReranker reorders facts by cosine similarity to the query embedding.
type CosineReranker struct {
	queryEmbedding []float32
}

func NewCosineReranker() *CosineReranker {
	return &CosineReranker{}
}

func (c *CosineReranker) SetQueryEmbedding(emb []float32) {
	if len(emb) == 0 {
		c.queryEmbedding = nil
		return
	}
	c.queryEmbedding = make([]float32, len(emb))
	copy(c.queryEmbedding, emb)
}

func (c *CosineReranker) Rerank(_ context.Context, _ string, items []RankedItem, _ int) ([]RankedItem, error) {
	if len(items) == 0 {
		return nil, nil
	}
	if len(c.queryEmbedding) == 0 {
		out := make([]RankedItem, len(items))
		copy(out, items)
		return out, nil
	}

	type scored struct {
		item  RankedItem
		score float64
	}
	withEmb := make([]scored, 0, len(items))
	withoutEmb := make([]RankedItem, 0, len(items))

	for _, item := range items {
		if len(item.Fact.Embedding) == 0 {
			withoutEmb = append(withoutEmb, item)
			continue
		}
		withEmb = append(withEmb, scored{
			item:  item,
			score: cosineSimilarity(c.queryEmbedding, item.Fact.Embedding),
		})
	}
	if len(withEmb) == 0 {
		out := make([]RankedItem, len(items))
		copy(out, items)
		return out, nil
	}

	sort.SliceStable(withEmb, func(i, j int) bool {
		return withEmb[i].score > withEmb[j].score
	})

	out := make([]RankedItem, 0, len(items))
	for _, s := range withEmb {
		s.item.Score = s.score
		out = append(out, s.item)
	}
	out = append(out, withoutEmb...)
	return out, nil
}

func cosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	denom := math.Sqrt(normA) * math.Sqrt(normB)
	if denom == 0 {
		return 0
	}
	return dot / denom
}
