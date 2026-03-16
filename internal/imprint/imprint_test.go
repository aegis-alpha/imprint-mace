package imprint

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aegis-alpha/imprint-mace/internal/config"
	"github.com/aegis-alpha/imprint-mace/internal/db"
	"github.com/aegis-alpha/imprint-mace/internal/extraction"
	"github.com/aegis-alpha/imprint-mace/internal/provider"
)

type mockSender struct {
	response *provider.Response
	err      error
}

func (m *mockSender) Send(_ context.Context, _ provider.Request) (*provider.Response, error) {
	return m.response, m.err
}

func promptPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "prompt.md")
	os.WriteFile(path, []byte("Extract facts. Return JSON.\n## Fact Types ({{len .FactTypes}})\n"), 0644)
	return path
}

func testEngine(t *testing.T, sender *mockSender) (*Engine, db.Store) {
	t.Helper()
	store, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	ext, err := extraction.New(
		sender,
		promptPath(t),
		config.DefaultTypes(),
		slog.Default(),
	)
	if err != nil {
		t.Fatalf("create extractor: %v", err)
	}

	eng := New(ext, store, nil, 0, 0, slog.Default())
	return eng, store
}

var mockJSON = `{
  "facts": [
    {"fact_type": "decision", "subject": "Acme", "content": "Acme uses Go.", "confidence": 0.9, "validity": {"valid_from": null, "valid_until": null}},
    {"fact_type": "preference", "subject": "Alice", "content": "Alice prefers dark mode.", "confidence": 0.8, "validity": {"valid_from": null, "valid_until": null}}
  ],
  "entities": [
    {"name": "Alice", "entity_type": "person", "aliases": []},
    {"name": "Acme", "entity_type": "project", "aliases": ["acme-app"]}
  ],
  "relationships": [
    {"from_entity": "Alice", "to_entity": "Acme", "relation_type": "works_on"}
  ]
}`

func TestIngest_StoresAll(t *testing.T) {
	sender := &mockSender{response: &provider.Response{
		Content: mockJSON, ProviderName: "mock", Model: "test", TokensUsed: 100,
	}}
	eng, store := testEngine(t, sender)

	_, err := eng.Ingest(context.Background(), "Alice decided Acme uses Go.", "test.md")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ctx := context.Background()

	facts, err := store.ListFacts(ctx, db.FactFilter{Limit: 10})
	if err != nil {
		t.Fatalf("list facts: %v", err)
	}
	if len(facts) != 2 {
		t.Errorf("expected 2 facts in DB, got %d", len(facts))
	}

	entities, err := store.ListEntities(ctx, db.EntityFilter{Limit: 10})
	if err != nil {
		t.Fatalf("list entities: %v", err)
	}
	if len(entities) != 2 {
		t.Errorf("expected 2 entities in DB, got %d", len(entities))
	}

	rels, err := store.ListRelationships(ctx, db.RelFilter{Limit: 10})
	if err != nil {
		t.Fatalf("list relationships: %v", err)
	}
	if len(rels) != 1 {
		t.Errorf("expected 1 relationship in DB, got %d", len(rels))
	}
}

func TestIngest_ReturnsResult(t *testing.T) {
	sender := &mockSender{response: &provider.Response{
		Content: mockJSON, ProviderName: "mock", Model: "test", TokensUsed: 100,
	}}
	eng, _ := testEngine(t, sender)

	result, err := eng.Ingest(context.Background(), "Alice decided Acme uses Go.", "test.md")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.FactsCount != 2 {
		t.Errorf("expected FactsCount=2, got %d", result.FactsCount)
	}
	if result.EntitiesCount != 2 {
		t.Errorf("expected EntitiesCount=2, got %d", result.EntitiesCount)
	}
	if result.RelationshipsCount != 1 {
		t.Errorf("expected RelationshipsCount=1, got %d", result.RelationshipsCount)
	}
	if len(result.FactIDs) != 2 {
		t.Errorf("expected 2 FactIDs, got %d", len(result.FactIDs))
	}
	if len(result.EntityIDs) != 2 {
		t.Errorf("expected 2 EntityIDs, got %d", len(result.EntityIDs))
	}
	if len(result.RelationshipIDs) != 1 {
		t.Errorf("expected 1 RelationshipID, got %d", len(result.RelationshipIDs))
	}
}

func TestIngest_ProviderFails(t *testing.T) {
	sender := &mockSender{err: fmt.Errorf("connection refused")}
	eng, _ := testEngine(t, sender)

	_, err := eng.Ingest(context.Background(), "some text", "test.md")
	if err == nil {
		t.Fatal("expected error when provider fails")
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("expected error to contain 'connection refused', got %q", err.Error())
	}
}

func TestIngest_EmptyResult(t *testing.T) {
	sender := &mockSender{response: &provider.Response{
		Content:      `{"facts": [], "entities": [], "relationships": []}`,
		ProviderName: "mock", Model: "test", TokensUsed: 10,
	}}
	eng, _ := testEngine(t, sender)

	result, err := eng.Ingest(context.Background(), "just some chatter", "test.md")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.FactsCount != 0 {
		t.Errorf("expected 0 facts, got %d", result.FactsCount)
	}
	if result.EntitiesCount != 0 {
		t.Errorf("expected 0 entities, got %d", result.EntitiesCount)
	}
	if result.RelationshipsCount != 0 {
		t.Errorf("expected 0 relationships, got %d", result.RelationshipsCount)
	}
}

// --- Dedup tests (BVP-215) ---

type mockEmbedder struct {
	vec []float32
}

func (m *mockEmbedder) Embed(_ context.Context, _ string) ([]float32, error) {
	return m.vec, nil
}

func (m *mockEmbedder) ModelName() string { return "mock-embed" }

func testEngineWithDedup(t *testing.T, sender *mockSender, embedder provider.Embedder, dims int, threshold float64) (*Engine, *db.SQLiteStore) {
	t.Helper()
	store, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	if dims > 0 {
		if err := store.EnsureVecTable(context.Background(), dims); err != nil {
			t.Fatalf("ensure vec table: %v", err)
		}
	}

	ext, err := extraction.New(
		sender,
		promptPath(t),
		config.DefaultTypes(),
		slog.Default(),
	)
	if err != nil {
		t.Fatalf("create extractor: %v", err)
	}

	eng := New(ext, store, embedder, threshold, 0, slog.Default())
	return eng, store
}

var singleFactJSON = `{
  "facts": [
    {"fact_type": "decision", "subject": "Acme", "content": "Acme uses Go.", "confidence": 0.9, "validity": {"valid_from": null, "valid_until": null}}
  ],
  "entities": [],
  "relationships": []
}`

func TestIngest_SkipsDuplicateFact(t *testing.T) {
	vec := []float32{0.1, 0.2, 0.3, 0.4}
	embedder := &mockEmbedder{vec: vec}
	sender := &mockSender{response: &provider.Response{
		Content: singleFactJSON, ProviderName: "mock", Model: "test", TokensUsed: 50,
	}}

	eng, store := testEngineWithDedup(t, sender, embedder, 4, 0.95)
	ctx := context.Background()

	r1, err := eng.Ingest(ctx, "Acme uses Go.", "test.md")
	if err != nil {
		t.Fatalf("first ingest: %v", err)
	}
	if r1.FactsCount != 1 {
		t.Fatalf("expected 1 fact on first ingest, got %d", r1.FactsCount)
	}

	r2, err := eng.Ingest(ctx, "Acme uses Go.", "test.md")
	if err != nil {
		t.Fatalf("second ingest: %v", err)
	}
	if r2.FactsCount != 0 {
		t.Errorf("expected 0 facts on second ingest (dedup), got %d", r2.FactsCount)
	}

	facts, _ := store.ListFacts(ctx, db.FactFilter{})
	if len(facts) != 1 {
		t.Errorf("expected 1 fact in DB total, got %d", len(facts))
	}
}

func TestIngest_AllowsDifferentFacts(t *testing.T) {
	embedder := &mockEmbedder{vec: []float32{1.0, 0.0, 0.0, 0.0}}
	sender := &mockSender{}

	differentFactJSON := `{
  "facts": [
    {"fact_type": "preference", "subject": "Bob", "content": "Bob prefers Python.", "confidence": 0.8, "validity": {"valid_from": null, "valid_until": null}}
  ],
  "entities": [],
  "relationships": []
}`

	eng, store := testEngineWithDedup(t, sender, embedder, 4, 0.95)
	ctx := context.Background()

	sender.response = &provider.Response{
		Content: singleFactJSON, ProviderName: "mock", Model: "test", TokensUsed: 50,
	}
	r1, err := eng.Ingest(ctx, "Acme uses Go.", "test.md")
	if err != nil {
		t.Fatalf("first ingest: %v", err)
	}
	if r1.FactsCount != 1 {
		t.Fatalf("expected 1 fact, got %d", r1.FactsCount)
	}

	embedder.vec = []float32{0.0, 1.0, 0.0, 0.0}
	sender.response = &provider.Response{
		Content: differentFactJSON, ProviderName: "mock", Model: "test", TokensUsed: 50,
	}
	r2, err := eng.Ingest(ctx, "Bob prefers Python.", "test.md")
	if err != nil {
		t.Fatalf("second ingest: %v", err)
	}
	if r2.FactsCount != 1 {
		t.Errorf("expected 1 fact on second ingest (different content), got %d", r2.FactsCount)
	}

	facts, _ := store.ListFacts(ctx, db.FactFilter{})
	if len(facts) != 2 {
		t.Errorf("expected 2 facts in DB total, got %d", len(facts))
	}
}

func TestIngest_DedupDisabledWithoutEmbedder(t *testing.T) {
	sender := &mockSender{response: &provider.Response{
		Content: singleFactJSON, ProviderName: "mock", Model: "test", TokensUsed: 50,
	}}

	eng, store := testEngineWithDedup(t, sender, nil, 0, 0.95)
	ctx := context.Background()

	eng.Ingest(ctx, "Acme uses Go.", "test.md")
	eng.Ingest(ctx, "Acme uses Go.", "test.md")

	facts, _ := store.ListFacts(ctx, db.FactFilter{})
	if len(facts) != 2 {
		t.Errorf("expected 2 facts (no dedup without embedder), got %d", len(facts))
	}
}

func TestIngest_WithSessionID(t *testing.T) {
	sender := &mockSender{response: &provider.Response{
		Content: mockJSON, ProviderName: "mock", Model: "test", TokensUsed: 100,
	}}
	eng, store := testEngine(t, sender)
	ctx := context.Background()

	result, err := eng.Ingest(ctx, "Alice decided Acme uses Go.", "test.md", WithSessionID("sess-abc"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.FactsCount != 2 {
		t.Fatalf("expected 2 facts, got %d", result.FactsCount)
	}

	facts, err := store.ListFacts(ctx, db.FactFilter{Limit: 10})
	if err != nil {
		t.Fatalf("list facts: %v", err)
	}
	for i, f := range facts {
		if f.Source.TranscriptFile != "realtime:sess-abc" {
			t.Errorf("fact[%d]: source_file = %q, want %q", i, f.Source.TranscriptFile, "realtime:sess-abc")
		}
	}
}

func TestIngest_DedupThresholdZero_Disabled(t *testing.T) {
	vec := []float32{0.1, 0.2, 0.3, 0.4}
	embedder := &mockEmbedder{vec: vec}
	sender := &mockSender{response: &provider.Response{
		Content: singleFactJSON, ProviderName: "mock", Model: "test", TokensUsed: 50,
	}}

	eng, store := testEngineWithDedup(t, sender, embedder, 4, 0)
	ctx := context.Background()

	eng.Ingest(ctx, "Acme uses Go.", "test.md")
	eng.Ingest(ctx, "Acme uses Go.", "test.md")

	facts, _ := store.ListFacts(ctx, db.FactFilter{})
	if len(facts) != 2 {
		t.Errorf("expected 2 facts (dedup disabled with threshold=0), got %d", len(facts))
	}
}
