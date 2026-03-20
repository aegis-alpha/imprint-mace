package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aegis-alpha/imprint-mace/internal/db"
	"github.com/aegis-alpha/imprint-mace/internal/query"
)

// Retriever runs retrieval without LLM synthesis.
type Retriever interface {
	Retrieve(ctx context.Context, question string) (*query.RetrievalResult, error)
}

// RetrievalReport is the full retrieval eval output.
type RetrievalReport struct {
	TotalQuestions int                    `json:"total_questions"`
	Categories     map[string]int         `json:"categories"`
	RecallAt10     float64                `json:"recall_at_10"`
	MRR            float64                `json:"mrr"`
	LayerStats     LayerStats             `json:"layer_stats"`
	CategoryScores map[string]CategoryRecall `json:"category_scores"`
	NoiseResult    NoiseRetrievalResult   `json:"noise"`
	Examples       []RetrievalExampleResult `json:"examples,omitempty"`
}

// LayerStats tracks which retrieval layers contributed to finding expected facts.
type LayerStats struct {
	VectorHits int `json:"vector_hits"`
	TextHits   int `json:"text_hits"`
	GraphHits  int `json:"graph_hits"`
	VectorOnly int `json:"vector_only"`
	TextOnly   int `json:"text_only"`
	GraphOnly  int `json:"graph_only"`
	MultiLayer int `json:"multi_layer"`
}

// CategoryRecall holds recall metrics per question category.
type CategoryRecall struct {
	Count    int     `json:"count"`
	Recall10 float64 `json:"recall_at_10"`
	MRR      float64 `json:"mrr"`
}

// NoiseRetrievalResult tracks how many noise questions returned zero facts.
type NoiseRetrievalResult struct {
	Total          int     `json:"total"`
	ZeroResults    int     `json:"zero_results"`
	RejectionRate  float64 `json:"rejection_rate"`
	AvgFactsFound  float64 `json:"avg_facts_found"`
}

// RetrievalExampleResult is the per-question detail.
type RetrievalExampleResult struct {
	Question       string   `json:"question"`
	Category       string   `json:"category"`
	ExpectedFacts  []string `json:"expected_facts"`
	FoundFacts     []string `json:"found_facts"`
	Recall         float64  `json:"recall"`
	ReciprocalRank float64  `json:"reciprocal_rank"`
	TotalRetrieved int      `json:"total_retrieved"`
	ByVector       int      `json:"by_vector"`
	ByText         int      `json:"by_text"`
	ByGraph        int      `json:"by_graph"`
	Error          string   `json:"error,omitempty"`
}

// SeedDB populates a store with the retrieval golden seed data.
// Returns the number of facts, entities, and relationships created.
func SeedDB(ctx context.Context, store db.Store, seed RetrievalSeedData) (int, int, int, error) {
	now := time.Now().UTC()

	for i := range seed.Facts {
		f := seed.Facts[i]
		f.Confidence = 0.9
		f.CreatedAt = now
		if err := store.CreateFact(ctx, &f); err != nil {
			return 0, 0, 0, fmt.Errorf("create fact %s: %w", f.ID, err)
		}
	}

	for i := range seed.Entities {
		e := seed.Entities[i]
		e.CreatedAt = now
		if err := store.CreateEntity(ctx, &e); err != nil {
			return 0, 0, 0, fmt.Errorf("create entity %s: %w", e.ID, err)
		}
	}

	for i := range seed.Relationships {
		r := seed.Relationships[i]
		r.CreatedAt = now
		if err := store.CreateRelationship(ctx, &r); err != nil {
			return 0, 0, 0, fmt.Errorf("create relationship %s: %w", r.ID, err)
		}
	}

	return len(seed.Facts), len(seed.Entities), len(seed.Relationships), nil
}

// RunRetrieval executes the retrieval eval: for each golden question,
// runs retrieval and checks if expected facts appear in top-K results.
func RunRetrieval(ctx context.Context, retriever Retriever, examples []RetrievalGoldenExample) (*RetrievalReport, error) {
	const topK = 10

	report := &RetrievalReport{
		TotalQuestions: len(examples),
		Categories:     map[string]int{},
		CategoryScores: map[string]CategoryRecall{},
	}

	var (
		totalRecall    float64
		totalMRR       float64
		positiveCount  int
		layerStats     LayerStats
		noiseTotal     int
		noiseZero      int
		noiseTotalFacts int
		exResults      []RetrievalExampleResult
	)

	catRecalls := map[string][]float64{}
	catMRRs := map[string][]float64{}

	for _, ex := range examples {
		report.Categories[ex.Category]++

		er := RetrievalExampleResult{
			Question:      ex.Question,
			Category:      ex.Category,
			ExpectedFacts: ex.ExpectedFacts,
		}

		result, err := retriever.Retrieve(ctx, ex.Question)
		if err != nil {
			er.Error = err.Error()
			exResults = append(exResults, er)
			continue
		}

		er.TotalRetrieved = len(result.Ranked)
		er.ByVector = result.FactsByVector
		er.ByText = result.FactsByText
		er.ByGraph = result.FactsByGraph

		if ex.Category == "noise" {
			noiseTotal++
			if len(result.Ranked) == 0 {
				noiseZero++
			}
			noiseTotalFacts += len(result.Ranked)
			exResults = append(exResults, er)
			continue
		}

		positiveCount++

		topFacts := result.Ranked
		if len(topFacts) > topK {
			topFacts = topFacts[:topK]
		}

		retrievedIDs := map[string]bool{}
		for _, rf := range topFacts {
			retrievedIDs[rf.Fact.ID] = true
			er.FoundFacts = append(er.FoundFacts, rf.Fact.ID)
		}

		found := 0
		for _, expectedID := range ex.ExpectedFacts {
			if retrievedIDs[expectedID] {
				found++
			}
		}

		recall := 0.0
		if len(ex.ExpectedFacts) > 0 {
			recall = float64(found) / float64(len(ex.ExpectedFacts))
		} else {
			recall = 1.0
		}
		er.Recall = recall
		totalRecall += recall

		rr := reciprocalRank(topFacts, ex.ExpectedFacts)
		er.ReciprocalRank = rr
		totalMRR += rr

		catRecalls[ex.Category] = append(catRecalls[ex.Category], recall)
		catMRRs[ex.Category] = append(catMRRs[ex.Category], rr)

		for _, rf := range topFacts {
			if !isExpected(rf.Fact.ID, ex.ExpectedFacts) {
				continue
			}
			if rf.FromVector {
				layerStats.VectorHits++
			}
			if rf.FromText {
				layerStats.TextHits++
			}
			if rf.FromGraph {
				layerStats.GraphHits++
			}

			layers := 0
			if rf.FromVector {
				layers++
			}
			if rf.FromText {
				layers++
			}
			if rf.FromGraph {
				layers++
			}
			if layers > 1 {
				layerStats.MultiLayer++
			} else if rf.FromVector {
				layerStats.VectorOnly++
			} else if rf.FromText {
				layerStats.TextOnly++
			} else if rf.FromGraph {
				layerStats.GraphOnly++
			}
		}

		exResults = append(exResults, er)
	}

	if positiveCount > 0 {
		report.RecallAt10 = totalRecall / float64(positiveCount)
		report.MRR = totalMRR / float64(positiveCount)
	}

	for cat, recalls := range catRecalls {
		avg := mean(recalls)
		avgMRR := mean(catMRRs[cat])
		report.CategoryScores[cat] = CategoryRecall{
			Count:    len(recalls),
			Recall10: avg,
			MRR:      avgMRR,
		}
	}

	report.LayerStats = layerStats

	if noiseTotal > 0 {
		report.NoiseResult = NoiseRetrievalResult{
			Total:         noiseTotal,
			ZeroResults:   noiseZero,
			RejectionRate: float64(noiseZero) / float64(noiseTotal),
			AvgFactsFound: float64(noiseTotalFacts) / float64(noiseTotal),
		}
	}

	report.Examples = exResults
	return report, nil
}

// DegradationDelta runs retrieval eval twice (with and without embedder)
// and returns the quality difference.
type DegradationResult struct {
	WithEmbedder    float64 `json:"with_embedder"`
	WithoutEmbedder float64 `json:"without_embedder"`
	Delta           float64 `json:"delta"`
}

func reciprocalRank(ranked []query.RankedFact, expectedIDs []string) float64 {
	expected := map[string]bool{}
	for _, id := range expectedIDs {
		expected[id] = true
	}
	for i, rf := range ranked {
		if expected[rf.Fact.ID] {
			return 1.0 / float64(i+1)
		}
	}
	return 0.0
}

func isExpected(id string, expectedIDs []string) bool {
	for _, eid := range expectedIDs {
		if id == eid {
			return true
		}
	}
	return false
}

func mean(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range vals {
		sum += v
	}
	return sum / float64(len(vals))
}

// WriteRetrievalTable writes a human-readable retrieval report to w.
func WriteRetrievalTable(w io.Writer, r *RetrievalReport) {
	fmt.Fprintf(w, "Imprint Retrieval Eval\n")
	fmt.Fprintf(w, "========================\n")
	fmt.Fprintf(w, "Questions: %d\n\n", r.TotalQuestions)

	fmt.Fprintf(w, "%-16s %10s %10s %10s\n", "Category", "Count", "Recall@10", "MRR")
	fmt.Fprintf(w, "%s\n", strings.Repeat("-", 48))
	for cat, cs := range r.CategoryScores {
		fmt.Fprintf(w, "%-16s %10d %10.4f %10.4f\n", cat, cs.Count, cs.Recall10, cs.MRR)
	}
	fmt.Fprintf(w, "%s\n", strings.Repeat("-", 48))
	fmt.Fprintf(w, "%-16s %10s %10.4f %10.4f\n", "OVERALL", "", r.RecallAt10, r.MRR)

	fmt.Fprintf(w, "\nLayer Contribution (expected facts found by):\n")
	fmt.Fprintf(w, "  Vector: %d  Text: %d  Graph: %d\n",
		r.LayerStats.VectorHits, r.LayerStats.TextHits, r.LayerStats.GraphHits)
	fmt.Fprintf(w, "  Vector-only: %d  Text-only: %d  Graph-only: %d  Multi-layer: %d\n",
		r.LayerStats.VectorOnly, r.LayerStats.TextOnly, r.LayerStats.GraphOnly, r.LayerStats.MultiLayer)

	if r.NoiseResult.Total > 0 {
		fmt.Fprintf(w, "\nNoise Rejection:\n")
		fmt.Fprintf(w, "  Total noise questions: %d\n", r.NoiseResult.Total)
		fmt.Fprintf(w, "  Zero results:          %d (%.0f%%)\n",
			r.NoiseResult.ZeroResults, r.NoiseResult.RejectionRate*100)
		fmt.Fprintf(w, "  Avg facts found:       %.1f\n", r.NoiseResult.AvgFactsFound)
	}
}

// WriteRetrievalJSON writes the retrieval report as JSON to w.
func WriteRetrievalJSON(w io.Writer, r *RetrievalReport) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}
