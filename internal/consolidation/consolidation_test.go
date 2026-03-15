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

// --- mock sender ---

type mockSender struct {
	response *provider.Response
	err      error
}

func (m *mockSender) Send(_ context.Context, _ provider.Request) (*provider.Response, error) {
	return m.response, m.err
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
	c, err := New(
		&mockSender{response: resp, err: sendErr},
		store,
		writePromptTemplate(t),
		config.DefaultTypes(),
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

	result, err := c.Consolidate(context.Background(), 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if len(result.FactConnections) != 1 {
		t.Fatalf("expected 1 connection, got %d", len(result.FactConnections))
	}

	fc := result.FactConnections[0]
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

	result, err := c.Consolidate(context.Background(), 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	cons := result.Consolidation
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

	result, err := c.Consolidate(context.Background(), 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil result for empty DB, got %+v", result)
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

	result, err := c.Consolidate(context.Background(), 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify consolidation is in DB
	cons, err := store.ListConsolidations(context.Background(), 10)
	if err != nil {
		t.Fatalf("list consolidations: %v", err)
	}
	if len(cons) != 1 {
		t.Fatalf("expected 1 consolidation in DB, got %d", len(cons))
	}
	if cons[0].ID != result.Consolidation.ID {
		t.Errorf("consolidation ID mismatch: DB %q vs result %q", cons[0].ID, result.Consolidation.ID)
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
	if fcs[0].ConsolidationID != result.Consolidation.ID {
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
