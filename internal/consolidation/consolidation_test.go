package consolidation

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/aegis-alpha/imprint-mace/internal/config"
	"github.com/aegis-alpha/imprint-mace/internal/db"
	"github.com/aegis-alpha/imprint-mace/internal/model"
	"github.com/aegis-alpha/imprint-mace/internal/provider"
)

// --- mock senders ---

type mockSender struct {
	response *provider.Response
	err      error
}

func (m *mockSender) Send(_ context.Context, _ provider.Request) (*provider.Response, error) {
	return m.response, m.err
}

type queueSender struct {
	responses []*provider.Response
	idx       int
}

func (q *queueSender) Send(_ context.Context, _ provider.Request) (*provider.Response, error) {
	if q.idx >= len(q.responses) {
		return nil, fmt.Errorf("queueSender exhausted: called %d times but only %d responses", q.idx+1, len(q.responses))
	}
	resp := q.responses[q.idx]
	q.idx++
	return resp, nil
}

// --- helpers ---

func testStore(t *testing.T) db.Store {
	t.Helper()
	s, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func seedFacts(t *testing.T, store db.Store, n int) []model.Fact {
	t.Helper()
	now := time.Now()
	var facts []model.Fact
	for i := 0; i < n; i++ {
		f := model.Fact{
			ID:         db.NewID(),
			Source:     model.Source{TranscriptFile: "test.md"},
			FactType:   model.FactDecision,
			Subject:    fmt.Sprintf("subject-%d", i),
			Content:    fmt.Sprintf("Fact number %d about Acme project.", i),
			Confidence: 0.9,
			CreatedAt:  now.Add(time.Duration(i) * time.Second),
		}
		if err := store.CreateFact(context.Background(), &f); err != nil {
			t.Fatalf("seed fact %d: %v", i, err)
		}
		facts = append(facts, f)
	}
	return facts
}

func writePromptTemplate(t *testing.T) string {
	t.Helper()
	return "testdata/consolidation-prompt.md"
}

func testConsolidator(t *testing.T, store db.Store, resp *provider.Response, sendErr error) *Consolidator {
	t.Helper()
	return testConsolidatorWithThreshold(t, store, resp, sendErr, 0.40)
}

func testConsolidatorWithThreshold(t *testing.T, store db.Store, resp *provider.Response, sendErr error, threshold float64) *Consolidator {
	t.Helper()
	c, err := New(
		&mockSender{response: resp, err: sendErr},
		store,
		writePromptTemplate(t),
		config.DefaultTypes(),
		threshold,
		slog.Default(),
	)
	if err != nil {
		t.Fatalf("failed to create consolidator: %v", err)
	}
	return c
}

// validConsolidationJSON returns a mock LLM response with connections between
// the first two fact IDs provided.
func validConsolidationJSON(factIDs []string) string {
	factA := "fact-a"
	factB := "fact-b"
	if len(factIDs) >= 2 {
		factA = factIDs[0]
		factB = factIDs[1]
	}
	return fmt.Sprintf(`{
  "connections": [
    {
      "fact_a": %q,
      "fact_b": %q,
      "connection_type": "supports",
      "strength": 0.8
    }
  ],
  "summary": "Both facts relate to decisions about the Acme project.",
  "insight": "There is a consistent pattern of technical decisions favoring simplicity.",
  "importance": 0.7
}`, factA, factB)
}

// --- tests ---

func TestConsolidate_FindsConnections(t *testing.T) {
	store := testStore(t)
	facts := seedFacts(t, store, 3)

	c := testConsolidator(t, store, &provider.Response{
		Content:      validConsolidationJSON([]string{facts[0].ID, facts[1].ID}),
		ProviderName: "mock",
		Model:        "test-model",
		TokensUsed:   200,
	}, nil)

	results, err := c.Consolidate(context.Background(), 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least one result")
	}
	if len(results[0].FactConnections) != 1 {
		t.Fatalf("expected 1 connection, got %d", len(results[0].FactConnections))
	}

	fc := results[0].FactConnections[0]
	if fc.FactA != facts[0].ID {
		t.Errorf("expected fact_a %q, got %q", facts[0].ID, fc.FactA)
	}
	if fc.FactB != facts[1].ID {
		t.Errorf("expected fact_b %q, got %q", facts[1].ID, fc.FactB)
	}
	if fc.ConnectionType != model.ConnSupports {
		t.Errorf("expected connection_type 'supports', got %q", fc.ConnectionType)
	}
	if fc.Strength != 0.8 {
		t.Errorf("expected strength 0.8, got %f", fc.Strength)
	}
}

func TestConsolidate_GeneratesInsight(t *testing.T) {
	store := testStore(t)
	facts := seedFacts(t, store, 3)

	c := testConsolidator(t, store, &provider.Response{
		Content:      validConsolidationJSON([]string{facts[0].ID, facts[1].ID}),
		ProviderName: "mock",
		Model:        "test-model",
		TokensUsed:   200,
	}, nil)

	results, err := c.Consolidate(context.Background(), 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least one result")
	}

	cons := results[0].Consolidation
	if cons.Summary == "" {
		t.Error("expected non-empty summary")
	}
	if cons.Insight == "" {
		t.Error("expected non-empty insight")
	}
	if cons.Importance != 0.7 {
		t.Errorf("expected importance 0.7, got %f", cons.Importance)
	}
	if len(cons.SourceFactIDs) != 3 {
		t.Errorf("expected 3 source fact IDs, got %d", len(cons.SourceFactIDs))
	}
	if len(cons.ID) != 26 {
		t.Errorf("expected 26-char ULID, got %q (len %d)", cons.ID, len(cons.ID))
	}
	if cons.CreatedAt.IsZero() {
		t.Error("expected non-zero created_at")
	}
}

func TestConsolidate_HandlesEmptyDB(t *testing.T) {
	store := testStore(t)

	c := testConsolidator(t, store, &provider.Response{
		Content:      `{}`,
		ProviderName: "mock",
		Model:        "test-model",
	}, nil)

	results, err := c.Consolidate(context.Background(), 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected empty results for empty DB, got %d", len(results))
	}
}

func TestConsolidate_HandlesInvalidJSON(t *testing.T) {
	store := testStore(t)
	seedFacts(t, store, 3)

	c := testConsolidator(t, store, &provider.Response{
		Content:      "this is not json at all",
		ProviderName: "mock",
		Model:        "test-model",
	}, nil)

	_, err := c.Consolidate(context.Background(), 10)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "parse LLM response") {
		t.Errorf("expected parse error, got %q", err.Error())
	}
}

func TestConsolidate_ProviderFails(t *testing.T) {
	store := testStore(t)
	seedFacts(t, store, 3)

	c := testConsolidator(t, store, nil, fmt.Errorf("connection refused"))

	_, err := c.Consolidate(context.Background(), 10)
	if err == nil {
		t.Fatal("expected error when provider fails")
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("expected error to contain 'connection refused', got %q", err.Error())
	}
}

func TestConsolidate_StoresResults(t *testing.T) {
	store := testStore(t)
	facts := seedFacts(t, store, 3)

	c := testConsolidator(t, store, &provider.Response{
		Content:      validConsolidationJSON([]string{facts[0].ID, facts[1].ID}),
		ProviderName: "mock",
		Model:        "test-model",
		TokensUsed:   200,
	}, nil)

	results, err := c.Consolidate(context.Background(), 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least one result")
	}

	// Verify consolidation is in DB
	cons, err := store.ListConsolidations(context.Background(), 10)
	if err != nil {
		t.Fatalf("list consolidations: %v", err)
	}
	if len(cons) != 1 {
		t.Fatalf("expected 1 consolidation in DB, got %d", len(cons))
	}
	if cons[0].ID != results[0].Consolidation.ID {
		t.Errorf("consolidation ID mismatch: DB %q vs result %q", cons[0].ID, results[0].Consolidation.ID)
	}
	if cons[0].Summary != "Both facts relate to decisions about the Acme project." {
		t.Errorf("unexpected summary in DB: %q", cons[0].Summary)
	}

	// Verify fact connections are in DB
	fcs, err := store.ListFactConnections(context.Background(), facts[0].ID)
	if err != nil {
		t.Fatalf("list fact connections: %v", err)
	}
	if len(fcs) != 1 {
		t.Fatalf("expected 1 fact connection in DB, got %d", len(fcs))
	}
	if fcs[0].ConsolidationID != results[0].Consolidation.ID {
		t.Errorf("fact connection consolidation_id mismatch")
	}

	// Verify facts are now consolidated (not returned by ListUnconsolidatedFacts)
	unconsolidated, err := store.ListUnconsolidatedFacts(context.Background(), 10)
	if err != nil {
		t.Fatalf("list unconsolidated: %v", err)
	}
	if len(unconsolidated) != 0 {
		t.Errorf("expected 0 unconsolidated facts after consolidation, got %d", len(unconsolidated))
	}
}

// --- clustering tests ---

func TestClusterFacts_GroupsBySimilarity(t *testing.T) {
	// Two similar vectors and one dissimilar; threshold 0.90 should group the pair.
	facts := []model.Fact{
		{ID: "a", Embedding: []float32{1, 0, 0}},
		{ID: "b", Embedding: []float32{0.99, 0.1, 0}},
		{ID: "c", Embedding: []float32{0, 0, 1}},
	}
	clusters := clusterFacts(facts, 0.90)
	if len(clusters) != 1 {
		t.Fatalf("expected 1 cluster (the similar pair), got %d", len(clusters))
	}
	if len(clusters[0]) != 2 {
		t.Errorf("expected cluster of 2, got %d", len(clusters[0]))
	}
	ids := map[string]bool{clusters[0][0].ID: true, clusters[0][1].ID: true}
	if !ids["a"] || !ids["b"] {
		t.Errorf("expected cluster to contain a and b, got %v", ids)
	}
}

func TestClusterFacts_SeparatesDissimilar(t *testing.T) {
	// Four facts: two pairs of similar vectors, threshold 0.80.
	facts := []model.Fact{
		{ID: "a1", Embedding: []float32{1, 0, 0}},
		{ID: "a2", Embedding: []float32{0.95, 0.1, 0}},
		{ID: "b1", Embedding: []float32{0, 1, 0}},
		{ID: "b2", Embedding: []float32{0, 0.95, 0.1}},
	}
	clusters := clusterFacts(facts, 0.80)
	if len(clusters) != 2 {
		t.Fatalf("expected 2 clusters, got %d", len(clusters))
	}
	for i, cl := range clusters {
		if len(cl) != 2 {
			t.Errorf("cluster %d: expected 2 facts, got %d", i, len(cl))
		}
	}
}

func TestClusterFacts_FallbackToSubject(t *testing.T) {
	// No embeddings, but two facts share a subject.
	facts := []model.Fact{
		{ID: "a", Subject: "Acme"},
		{ID: "b", Subject: "acme"},
		{ID: "c", Subject: "Zeta"},
	}
	clusters := clusterFacts(facts, 0.40)
	if len(clusters) != 1 {
		t.Fatalf("expected 1 cluster (Acme pair), got %d", len(clusters))
	}
	if len(clusters[0]) != 2 {
		t.Errorf("expected cluster of 2, got %d", len(clusters[0]))
	}
}

func TestClusterFacts_SingleFactSkipped(t *testing.T) {
	// Each fact is unique -- no cluster of >= 2 can form.
	facts := []model.Fact{
		{ID: "a", Embedding: []float32{1, 0, 0}},
	}
	clusters := clusterFacts(facts, 0.40)
	if len(clusters) != 0 {
		t.Errorf("expected 0 clusters for single fact, got %d", len(clusters))
	}
}

func TestClusterFacts_EmptyInput(t *testing.T) {
	clusters := clusterFacts(nil, 0.40)
	if len(clusters) != 0 {
		t.Errorf("expected 0 clusters for nil input, got %d", len(clusters))
	}
	clusters = clusterFacts([]model.Fact{}, 0.40)
	if len(clusters) != 0 {
		t.Errorf("expected 0 clusters for empty input, got %d", len(clusters))
	}
}

func TestConsolidate_MultipleGroups(t *testing.T) {
	store := testStore(t)
	now := time.Now()

	// Create two pairs of facts with similar embeddings, dissimilar between pairs.
	factsData := []model.Fact{
		{ID: db.NewID(), Source: model.Source{TranscriptFile: "t.md"}, FactType: model.FactDecision, Subject: "X", Content: "X1", Confidence: 0.9, CreatedAt: now, Embedding: []float32{1, 0, 0}},
		{ID: db.NewID(), Source: model.Source{TranscriptFile: "t.md"}, FactType: model.FactDecision, Subject: "X", Content: "X2", Confidence: 0.9, CreatedAt: now.Add(time.Second), Embedding: []float32{0.99, 0.05, 0}},
		{ID: db.NewID(), Source: model.Source{TranscriptFile: "t.md"}, FactType: model.FactDecision, Subject: "Y", Content: "Y1", Confidence: 0.9, CreatedAt: now.Add(2 * time.Second), Embedding: []float32{0, 1, 0}},
		{ID: db.NewID(), Source: model.Source{TranscriptFile: "t.md"}, FactType: model.FactDecision, Subject: "Y", Content: "Y2", Confidence: 0.9, CreatedAt: now.Add(3 * time.Second), Embedding: []float32{0, 0.99, 0.05}},
	}
	for i := range factsData {
		if err := store.CreateFact(context.Background(), &factsData[i]); err != nil {
			t.Fatalf("seed fact: %v", err)
		}
	}

	qs := &queueSender{responses: []*provider.Response{
		{Content: validConsolidationJSON([]string{factsData[0].ID, factsData[1].ID}), ProviderName: "mock", Model: "m", TokensUsed: 100},
		{Content: validConsolidationJSON([]string{factsData[2].ID, factsData[3].ID}), ProviderName: "mock", Model: "m", TokensUsed: 100},
	}}

	c, err := New(qs, store, writePromptTemplate(t), config.DefaultTypes(), 0.80, slog.Default())
	if err != nil {
		t.Fatalf("create consolidator: %v", err)
	}

	results, err := c.Consolidate(context.Background(), 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results (one per cluster), got %d", len(results))
	}
	if qs.idx != 2 {
		t.Errorf("expected sender called 2 times, got %d", qs.idx)
	}
}

func TestConsolidate_NoEmbeddings_SubjectFallback(t *testing.T) {
	store := testStore(t)
	now := time.Now()

	// Three facts, two share subject "Acme", one is "Zeta" (alone).
	factsData := []model.Fact{
		{ID: db.NewID(), Source: model.Source{TranscriptFile: "t.md"}, FactType: model.FactDecision, Subject: "Acme", Content: "A1", Confidence: 0.9, CreatedAt: now},
		{ID: db.NewID(), Source: model.Source{TranscriptFile: "t.md"}, FactType: model.FactDecision, Subject: "Acme", Content: "A2", Confidence: 0.9, CreatedAt: now.Add(time.Second)},
		{ID: db.NewID(), Source: model.Source{TranscriptFile: "t.md"}, FactType: model.FactDecision, Subject: "Zeta", Content: "Z1", Confidence: 0.9, CreatedAt: now.Add(2 * time.Second)},
	}
	for i := range factsData {
		if err := store.CreateFact(context.Background(), &factsData[i]); err != nil {
			t.Fatalf("seed fact: %v", err)
		}
	}

	c := testConsolidator(t, store, &provider.Response{
		Content:      validConsolidationJSON([]string{factsData[0].ID, factsData[1].ID}),
		ProviderName: "mock",
		Model:        "test-model",
		TokensUsed:   100,
	}, nil)

	results, err := c.Consolidate(context.Background(), 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result (Acme cluster), got %d", len(results))
	}
	if len(results[0].Consolidation.SourceFactIDs) != 2 {
		t.Errorf("expected 2 source facts in cluster, got %d", len(results[0].Consolidation.SourceFactIDs))
	}
}
