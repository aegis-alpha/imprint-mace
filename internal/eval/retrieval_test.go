package eval

import (
	"bytes"
	"context"
	"math"
	"testing"

	"github.com/aegis-alpha/imprint-mace/internal/model"
	"github.com/aegis-alpha/imprint-mace/internal/query"
)

// mockRetriever returns canned results keyed by question substring.
type mockRetriever struct {
	results map[string]*query.RetrievalResult
}

func (m *mockRetriever) Retrieve(_ context.Context, question string) (*query.RetrievalResult, error) {
	for key, result := range m.results {
		if question == key {
			return result, nil
		}
	}
	return &query.RetrievalResult{}, nil
}

func makeRanked(ids []string, fromVector, fromText, fromGraph bool) []query.RankedFact {
	out := make([]query.RankedFact, len(ids))
	for i, id := range ids {
		out[i] = query.RankedFact{
			Fact:       model.Fact{ID: id},
			Score:      1.0 / float64(i+1),
			FromVector: fromVector,
			FromText:   fromText,
			FromGraph:  fromGraph,
		}
	}
	return out
}

func TestReciprocalRank(t *testing.T) {
	ranked := []query.RankedFact{
		{Fact: model.Fact{ID: "a"}, Score: 1.0},
		{Fact: model.Fact{ID: "b"}, Score: 0.5},
		{Fact: model.Fact{ID: "c"}, Score: 0.3},
	}

	rr := reciprocalRank(ranked, []string{"a"})
	if math.Abs(rr-1.0) > 0.001 {
		t.Errorf("first position RR = %f, want 1.0", rr)
	}

	rr = reciprocalRank(ranked, []string{"b"})
	if math.Abs(rr-0.5) > 0.001 {
		t.Errorf("second position RR = %f, want 0.5", rr)
	}

	rr = reciprocalRank(ranked, []string{"c"})
	if math.Abs(rr-1.0/3.0) > 0.001 {
		t.Errorf("third position RR = %f, want 0.333", rr)
	}

	rr = reciprocalRank(ranked, []string{"missing"})
	if rr != 0 {
		t.Errorf("missing RR = %f, want 0", rr)
	}

	rr = reciprocalRank(ranked, []string{"c", "a"})
	if math.Abs(rr-1.0) > 0.001 {
		t.Errorf("multi-expected RR = %f, want 1.0 (first hit at rank 1)", rr)
	}
}

func TestRunRetrievalPerfect(t *testing.T) {
	retriever := &mockRetriever{
		results: map[string]*query.RetrievalResult{
			"What language?": {
				Ranked:        makeRanked([]string{"f-acme-go"}, true, true, false),
				FactsByVector: 1,
				FactsByText:   1,
			},
			"Who is Alice?": {
				Ranked:      makeRanked([]string{"f-alice-telegram"}, false, true, false),
				FactsByText: 1,
			},
		},
	}

	examples := []RetrievalGoldenExample{
		{
			Question:      "What language?",
			ExpectedFacts: []string{"f-acme-go"},
			Category:      "direct_lookup",
		},
		{
			Question:      "Who is Alice?",
			ExpectedFacts: []string{"f-alice-telegram"},
			Category:      "direct_lookup",
		},
	}

	report, err := RunRetrieval(context.Background(), retriever, examples)
	if err != nil {
		t.Fatalf("RunRetrieval: %v", err)
	}

	if math.Abs(report.RecallAt10-1.0) > 0.001 {
		t.Errorf("perfect Recall@10 = %f, want 1.0", report.RecallAt10)
	}
	if math.Abs(report.MRR-1.0) > 0.001 {
		t.Errorf("perfect MRR = %f, want 1.0", report.MRR)
	}
	if report.TotalQuestions != 2 {
		t.Errorf("TotalQuestions = %d, want 2", report.TotalQuestions)
	}
}

func TestRunRetrievalPartial(t *testing.T) {
	retriever := &mockRetriever{
		results: map[string]*query.RetrievalResult{
			"multi-hop question": {
				Ranked:      makeRanked([]string{"f-a", "f-b", "f-extra"}, false, true, false),
				FactsByText: 3,
			},
		},
	}

	examples := []RetrievalGoldenExample{
		{
			Question:      "multi-hop question",
			ExpectedFacts: []string{"f-a", "f-b", "f-c"},
			Category:      "multi_hop",
		},
	}

	report, err := RunRetrieval(context.Background(), retriever, examples)
	if err != nil {
		t.Fatalf("RunRetrieval: %v", err)
	}

	expectedRecall := 2.0 / 3.0
	if math.Abs(report.RecallAt10-expectedRecall) > 0.01 {
		t.Errorf("partial Recall@10 = %f, want %f", report.RecallAt10, expectedRecall)
	}
	if math.Abs(report.MRR-1.0) > 0.001 {
		t.Errorf("partial MRR = %f, want 1.0 (f-a at rank 1)", report.MRR)
	}
}

func TestRunRetrievalNoise(t *testing.T) {
	retriever := &mockRetriever{
		results: map[string]*query.RetrievalResult{
			"irrelevant question": {},
			"another noise":       {Ranked: makeRanked([]string{"f-spurious"}, false, true, false), FactsByText: 1},
		},
	}

	examples := []RetrievalGoldenExample{
		{
			Question:      "irrelevant question",
			ExpectedFacts: []string{},
			Category:      "noise",
		},
		{
			Question:      "another noise",
			ExpectedFacts: []string{},
			Category:      "noise",
		},
	}

	report, err := RunRetrieval(context.Background(), retriever, examples)
	if err != nil {
		t.Fatalf("RunRetrieval: %v", err)
	}

	if report.NoiseResult.Total != 2 {
		t.Errorf("noise total = %d, want 2", report.NoiseResult.Total)
	}
	if report.NoiseResult.ZeroResults != 1 {
		t.Errorf("noise zero results = %d, want 1", report.NoiseResult.ZeroResults)
	}
	if math.Abs(report.NoiseResult.RejectionRate-0.5) > 0.01 {
		t.Errorf("noise rejection rate = %f, want 0.5", report.NoiseResult.RejectionRate)
	}
}

func TestRunRetrievalLayerStats(t *testing.T) {
	retriever := &mockRetriever{
		results: map[string]*query.RetrievalResult{
			"vector+text": {
				Ranked: []query.RankedFact{
					{Fact: model.Fact{ID: "f-a"}, Score: 1.0, FromVector: true, FromText: true},
				},
				FactsByVector: 1,
				FactsByText:   1,
			},
			"graph only": {
				Ranked: []query.RankedFact{
					{Fact: model.Fact{ID: "f-b"}, Score: 1.0, FromGraph: true},
				},
				FactsByGraph: 1,
			},
		},
	}

	examples := []RetrievalGoldenExample{
		{Question: "vector+text", ExpectedFacts: []string{"f-a"}, Category: "direct_lookup"},
		{Question: "graph only", ExpectedFacts: []string{"f-b"}, Category: "graph_traversal"},
	}

	report, err := RunRetrieval(context.Background(), retriever, examples)
	if err != nil {
		t.Fatalf("RunRetrieval: %v", err)
	}

	if report.LayerStats.MultiLayer != 1 {
		t.Errorf("MultiLayer = %d, want 1", report.LayerStats.MultiLayer)
	}
	if report.LayerStats.GraphOnly != 1 {
		t.Errorf("GraphOnly = %d, want 1", report.LayerStats.GraphOnly)
	}
	if report.LayerStats.VectorHits != 1 {
		t.Errorf("VectorHits = %d, want 1", report.LayerStats.VectorHits)
	}
	if report.LayerStats.TextHits != 1 {
		t.Errorf("TextHits = %d, want 1", report.LayerStats.TextHits)
	}
	if report.LayerStats.GraphHits != 1 {
		t.Errorf("GraphHits = %d, want 1", report.LayerStats.GraphHits)
	}
}

func TestRunRetrievalCategoryScores(t *testing.T) {
	retriever := &mockRetriever{
		results: map[string]*query.RetrievalResult{
			"q1": {Ranked: makeRanked([]string{"f-a"}, false, true, false), FactsByText: 1},
			"q2": {Ranked: makeRanked([]string{"f-b"}, false, true, false), FactsByText: 1},
			"q3": {Ranked: makeRanked([]string{"f-c"}, false, false, true), FactsByGraph: 1},
		},
	}

	examples := []RetrievalGoldenExample{
		{Question: "q1", ExpectedFacts: []string{"f-a"}, Category: "direct_lookup"},
		{Question: "q2", ExpectedFacts: []string{"f-b"}, Category: "direct_lookup"},
		{Question: "q3", ExpectedFacts: []string{"f-c"}, Category: "graph_traversal"},
	}

	report, err := RunRetrieval(context.Background(), retriever, examples)
	if err != nil {
		t.Fatalf("RunRetrieval: %v", err)
	}

	dl, ok := report.CategoryScores["direct_lookup"]
	if !ok {
		t.Fatal("missing direct_lookup category")
	}
	if dl.Count != 2 {
		t.Errorf("direct_lookup count = %d, want 2", dl.Count)
	}
	if math.Abs(dl.Recall10-1.0) > 0.001 {
		t.Errorf("direct_lookup Recall@10 = %f, want 1.0", dl.Recall10)
	}

	gt, ok := report.CategoryScores["graph_traversal"]
	if !ok {
		t.Fatal("missing graph_traversal category")
	}
	if gt.Count != 1 {
		t.Errorf("graph_traversal count = %d, want 1", gt.Count)
	}
}

func TestRunRetrievalEmpty(t *testing.T) {
	retriever := &mockRetriever{results: map[string]*query.RetrievalResult{}}

	report, err := RunRetrieval(context.Background(), retriever, nil)
	if err != nil {
		t.Fatalf("RunRetrieval: %v", err)
	}
	if report.TotalQuestions != 0 {
		t.Errorf("empty TotalQuestions = %d, want 0", report.TotalQuestions)
	}
}

func TestWriteRetrievalTable(t *testing.T) {
	report := &RetrievalReport{
		TotalQuestions: 5,
		RecallAt10:     0.85,
		MRR:            0.72,
		CategoryScores: map[string]CategoryRecall{
			"direct_lookup": {Count: 3, Recall10: 0.9, MRR: 0.8},
		},
		LayerStats:  LayerStats{VectorHits: 3, TextHits: 2, GraphHits: 1},
		NoiseResult: NoiseRetrievalResult{Total: 2, ZeroResults: 1, RejectionRate: 0.5},
	}

	var buf bytes.Buffer
	WriteRetrievalTable(&buf, report)

	output := buf.String()
	if output == "" {
		t.Error("WriteRetrievalTable produced empty output")
	}
	if !bytes.Contains(buf.Bytes(), []byte("0.8500")) {
		t.Error("output should contain Recall@10 value")
	}
	if !bytes.Contains(buf.Bytes(), []byte("Noise Rejection")) {
		t.Error("output should contain noise section")
	}
}

func TestWriteRetrievalJSON(t *testing.T) {
	report := &RetrievalReport{
		TotalQuestions: 3,
		RecallAt10:     0.75,
		MRR:            0.60,
		Categories:     map[string]int{"direct_lookup": 3},
		CategoryScores: map[string]CategoryRecall{},
	}

	var buf bytes.Buffer
	if err := WriteRetrievalJSON(&buf, report); err != nil {
		t.Fatalf("WriteRetrievalJSON: %v", err)
	}
	if buf.Len() == 0 {
		t.Error("WriteRetrievalJSON produced empty output")
	}
}

func TestBuiltinRetrievalSeed(t *testing.T) {
	seed := BuiltinRetrievalSeed()
	if len(seed.Facts) < 20 {
		t.Errorf("seed facts = %d, want >= 20", len(seed.Facts))
	}
	if len(seed.Entities) < 20 {
		t.Errorf("seed entities = %d, want >= 20", len(seed.Entities))
	}
	if len(seed.Relationships) < 10 {
		t.Errorf("seed relationships = %d, want >= 10", len(seed.Relationships))
	}

	factIDs := map[string]bool{}
	for _, f := range seed.Facts {
		if factIDs[f.ID] {
			t.Errorf("duplicate fact ID: %s", f.ID)
		}
		factIDs[f.ID] = true
	}

	entityIDs := map[string]bool{}
	for _, e := range seed.Entities {
		if entityIDs[e.ID] {
			t.Errorf("duplicate entity ID: %s", e.ID)
		}
		entityIDs[e.ID] = true
	}
}

func TestBuiltinRetrievalExamples(t *testing.T) {
	examples := BuiltinRetrievalExamples()
	if len(examples) < 15 {
		t.Errorf("examples = %d, want >= 15", len(examples))
	}

	categories := map[string]int{}
	for _, ex := range examples {
		categories[ex.Category]++
		if ex.Question == "" {
			t.Error("example has empty question")
		}
	}

	for _, required := range []string{"direct_lookup", "graph_traversal", "temporal", "multi_hop", "noise"} {
		if categories[required] == 0 {
			t.Errorf("missing category: %s", required)
		}
	}
}

func TestBuiltinExamplesReferenceValidFacts(t *testing.T) {
	seed := BuiltinRetrievalSeed()
	factIDs := map[string]bool{}
	for _, f := range seed.Facts {
		factIDs[f.ID] = true
	}

	examples := BuiltinRetrievalExamples()
	for _, ex := range examples {
		for _, fid := range ex.ExpectedFacts {
			if !factIDs[fid] {
				t.Errorf("example %q references non-existent fact %q", ex.Question, fid)
			}
		}
	}
}

func TestMean(t *testing.T) {
	if m := mean(nil); m != 0 {
		t.Errorf("mean(nil) = %f, want 0", m)
	}
	if m := mean([]float64{1, 2, 3}); math.Abs(m-2.0) > 0.001 {
		t.Errorf("mean([1,2,3]) = %f, want 2.0", m)
	}
}
