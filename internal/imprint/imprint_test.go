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
	"github.com/aegis-alpha/imprint-mace/internal/model"
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
	ext.SetExtractionLogger(store)

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

func skipIfUSearchBroken(t *testing.T) {
	t.Helper()
	if os.Getenv("IMPRINT_SKIP_USEARCH") != "" {
		t.Skip("IMPRINT_SKIP_USEARCH set -- USearch C library crashes on this platform")
	}
}

func testEngineWithDedup(t *testing.T, sender *mockSender, embedder provider.Embedder, dims int, threshold float64) (*Engine, *db.SQLiteStore) {
	t.Helper()
	store, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	if dims > 0 {
		skipIfUSearchBroken(t)
		if err := store.AttachVectorIndex(dims); err != nil {
			t.Fatalf("AttachVectorIndex: %v", err)
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

func TestIngest_EntityDedup(t *testing.T) {
	aliceJSON := `{
  "facts": [
    {"fact_type": "bio", "subject": "Alice", "content": "Alice is an engineer.", "confidence": 0.9, "validity": {"valid_from": null, "valid_until": null}}
  ],
  "entities": [
    {"name": "Alice", "entity_type": "person", "aliases": []}
  ],
  "relationships": []
}`
	aliceWithRelJSON := `{
  "facts": [
    {"fact_type": "project", "subject": "Acme", "content": "Acme uses Go.", "confidence": 0.9, "validity": {"valid_from": null, "valid_until": null}}
  ],
  "entities": [
    {"name": "Alice", "entity_type": "person", "aliases": []},
    {"name": "Acme", "entity_type": "project", "aliases": []}
  ],
  "relationships": [
    {"from_entity": "Alice", "to_entity": "Acme", "relation_type": "works_on"}
  ]
}`

	sender := &mockSender{}
	eng, store := testEngine(t, sender)
	ctx := context.Background()

	sender.response = &provider.Response{
		Content: aliceJSON, ProviderName: "mock", Model: "test", TokensUsed: 50,
	}
	r1, err := eng.Ingest(ctx, "Alice is an engineer.", "test.md")
	if err != nil {
		t.Fatalf("first ingest: %v", err)
	}
	if r1.EntitiesCount != 1 {
		t.Fatalf("expected 1 entity on first ingest, got %d", r1.EntitiesCount)
	}
	firstAliceID := r1.EntityIDs[0]

	sender.response = &provider.Response{
		Content: aliceWithRelJSON, ProviderName: "mock", Model: "test", TokensUsed: 50,
	}
	r2, err := eng.Ingest(ctx, "Alice works on Acme which uses Go.", "test2.md")
	if err != nil {
		t.Fatalf("second ingest: %v", err)
	}
	if r2.EntitiesCount != 2 {
		t.Errorf("expected 2 entities on second ingest (1 reused + 1 new), got %d", r2.EntitiesCount)
	}

	entities, err := store.ListEntities(ctx, db.EntityFilter{Limit: 100})
	if err != nil {
		t.Fatalf("list entities: %v", err)
	}
	aliceCount := 0
	for _, e := range entities {
		if strings.EqualFold(e.Name, "Alice") {
			aliceCount++
		}
	}
	if aliceCount != 1 {
		t.Errorf("expected exactly 1 Alice entity in DB, got %d (total entities: %d)", aliceCount, len(entities))
	}

	var reusedAliceID string
	for _, id := range r2.EntityIDs {
		ent, err := store.GetEntity(ctx, id)
		if err != nil {
			continue
		}
		if strings.EqualFold(ent.Name, "Alice") {
			reusedAliceID = id
			break
		}
	}
	if reusedAliceID != firstAliceID {
		t.Errorf("second ingest should reuse original Alice ID %s, got %s", firstAliceID, reusedAliceID)
	}

	rels, err := store.ListRelationships(ctx, db.RelFilter{Limit: 10})
	if err != nil {
		t.Fatalf("list relationships: %v", err)
	}
	if len(rels) != 1 {
		t.Fatalf("expected 1 relationship, got %d", len(rels))
	}
	if rels[0].FromEntity != firstAliceID {
		t.Errorf("relationship from_entity should be original Alice ID %s, got %s", firstAliceID, rels[0].FromEntity)
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

func TestIngest_EntityCollisionCounters(t *testing.T) {
	aliceJSON := `{
  "facts": [],
  "entities": [{"name": "Alice", "entity_type": "person", "aliases": []}],
  "relationships": []
}`
	aliceAcmeJSON := `{
  "facts": [],
  "entities": [
    {"name": "Alice", "entity_type": "person", "aliases": []},
    {"name": "Acme", "entity_type": "project", "aliases": []}
  ],
  "relationships": []
}`

	sender := &mockSender{}
	eng, _ := testEngine(t, sender)
	ctx := context.Background()

	sender.response = &provider.Response{
		Content: aliceJSON, ProviderName: "mock", Model: "test", TokensUsed: 50,
	}
	r1, err := eng.Ingest(ctx, "Alice.", "test.md")
	if err != nil {
		t.Fatalf("first ingest: %v", err)
	}
	if r1.EntityCollisions != 0 {
		t.Errorf("first ingest: expected 0 collisions, got %d", r1.EntityCollisions)
	}
	if r1.EntityCreations != 1 {
		t.Errorf("first ingest: expected 1 creation, got %d", r1.EntityCreations)
	}

	sender.response = &provider.Response{
		Content: aliceAcmeJSON, ProviderName: "mock", Model: "test", TokensUsed: 50,
	}
	r2, err := eng.Ingest(ctx, "Alice works on Acme.", "test2.md")
	if err != nil {
		t.Fatalf("second ingest: %v", err)
	}
	if r2.EntityCollisions != 1 {
		t.Errorf("second ingest: expected 1 collision (Alice reused), got %d", r2.EntityCollisions)
	}
	if r2.EntityCreations != 1 {
		t.Errorf("second ingest: expected 1 creation (Acme new), got %d", r2.EntityCreations)
	}
}

func TestIngest_CollisionStatsInExtractionLog(t *testing.T) {
	aliceJSON := `{
  "facts": [],
  "entities": [{"name": "Alice", "entity_type": "person", "aliases": []}],
  "relationships": []
}`
	aliceAgainJSON := `{
  "facts": [],
  "entities": [
    {"name": "Alice", "entity_type": "person", "aliases": []},
    {"name": "Acme", "entity_type": "project", "aliases": []}
  ],
  "relationships": []
}`

	sender := &mockSender{}
	eng, store := testEngine(t, sender)
	ctx := context.Background()

	sender.response = &provider.Response{
		Content: aliceJSON, ProviderName: "mock", Model: "test", TokensUsed: 50,
	}
	_, err := eng.Ingest(ctx, "Alice.", "test.md")
	if err != nil {
		t.Fatalf("first ingest: %v", err)
	}

	sender.response = &provider.Response{
		Content: aliceAgainJSON, ProviderName: "mock", Model: "test", TokensUsed: 50,
	}
	_, err = eng.Ingest(ctx, "Alice works on Acme.", "test2.md")
	if err != nil {
		t.Fatalf("second ingest: %v", err)
	}

	logs, err := store.ListExtractionLogs(ctx, 10)
	if err != nil {
		t.Fatalf("list logs: %v", err)
	}
	if len(logs) < 2 {
		t.Fatalf("expected at least 2 log entries, got %d", len(logs))
	}

	latest := logs[0]
	if latest.EntityCollisions != 1 {
		t.Errorf("extraction_log: expected entity_collisions=1, got %d", latest.EntityCollisions)
	}
	if latest.EntityCreations != 1 {
		t.Errorf("extraction_log: expected entity_creations=1, got %d", latest.EntityCreations)
	}
}

// cbSender routes LLM calls by system prompt (extraction vs contradiction batch).
type cbSender struct {
	fn func(context.Context, provider.Request) (*provider.Response, error)
}

func (c *cbSender) Send(ctx context.Context, req provider.Request) (*provider.Response, error) {
	return c.fn(ctx, req)
}

const testContradictionSystemPrompt = "# Contradiction review (batch)\nReturn JSON only.\n"

func openStoreWithVec(t *testing.T, dims int) *db.SQLiteStore {
	t.Helper()
	store, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if dims > 0 {
		skipIfUSearchBroken(t)
		if err := store.AttachVectorIndex(dims); err != nil {
			t.Fatalf("AttachVectorIndex: %v", err)
		}
	}
	return store
}

func newTestEngineWithContradiction(t *testing.T, store *db.SQLiteStore, sender extraction.Sender, embedder provider.Embedder, threshold float64) *Engine {
	t.Helper()
	ext, err := extraction.New(
		sender,
		promptPath(t),
		config.DefaultTypes(),
		slog.Default(),
	)
	if err != nil {
		t.Fatalf("create extractor: %v", err)
	}
	return New(ext, store, embedder, threshold, 0, slog.Default(), WithContradiction(true, sender, testContradictionSystemPrompt))
}

func TestIngest_SmartDedup_AllowsDifferentContentAtHighCosine(t *testing.T) {
	vec := []float32{0.5, 0.5, 0.5, 0.5}
	embedder := &mockEmbedder{vec: vec}
	sender := &mockSender{}

	py39 := `{
  "facts": [
    {"fact_type": "project", "subject": "Acme", "content": "Acme uses Python 3.9.", "confidence": 0.9, "validity": {"valid_from": null, "valid_until": null}}
  ],
  "entities": [],
  "relationships": []
}`
	py312 := `{
  "facts": [
    {"fact_type": "project", "subject": "Acme", "content": "Acme uses Python 3.12.", "confidence": 0.9, "validity": {"valid_from": null, "valid_until": null}}
  ],
  "entities": [],
  "relationships": []
}`

	eng, store := testEngineWithDedup(t, sender, embedder, 4, 0.95)
	ctx := context.Background()

	sender.response = &provider.Response{Content: py39, ProviderName: "mock", Model: "test", TokensUsed: 50}
	if _, err := eng.Ingest(ctx, "x", "a.md"); err != nil {
		t.Fatalf("first ingest: %v", err)
	}
	sender.response = &provider.Response{Content: py312, ProviderName: "mock", Model: "test", TokensUsed: 50}
	if _, err := eng.Ingest(ctx, "y", "b.md"); err != nil {
		t.Fatalf("second ingest: %v", err)
	}

	facts, err := store.ListFacts(ctx, db.FactFilter{})
	if err != nil {
		t.Fatalf("list facts: %v", err)
	}
	if len(facts) != 2 {
		t.Fatalf("expected 2 facts (smart dedup allows different versions at high cosine), got %d", len(facts))
	}
}

func TestIngest_ContradictionSupersedesPriorFact(t *testing.T) {
	vec := []float32{0.2, 0.4, 0.6, 0.8}
	embedder := &mockEmbedder{vec: vec}
	store := openStoreWithVec(t, 4)

	goFact := `{
  "facts": [
    {"fact_type": "decision", "subject": "Stack", "content": "We use Go for the API.", "confidence": 0.9, "validity": {"valid_from": null, "valid_until": null}}
  ],
  "entities": [],
  "relationships": []
}`
	rustFact := `{
  "facts": [
    {"fact_type": "decision", "subject": "Stack", "content": "We use Rust for the API.", "confidence": 0.85, "validity": {"valid_from": null, "valid_until": null}}
  ],
  "entities": [],
  "relationships": []
}`

	var call int
	sender := &cbSender{fn: func(ctx context.Context, req provider.Request) (*provider.Response, error) {
		call++
		if strings.Contains(req.SystemPrompt, "Contradiction review") {
			facts, err := store.ListFacts(ctx, db.FactFilter{Limit: 20})
			if err != nil {
				return nil, err
			}
			var oldID, newID string
			for _, f := range facts {
				if strings.Contains(f.Content, "Go") {
					oldID = f.ID
				}
				if strings.Contains(f.Content, "Rust") {
					newID = f.ID
				}
			}
			if oldID == "" || newID == "" {
				t.Fatalf("could not resolve fact ids for contradiction mock (old=%q new=%q)", oldID, newID)
			}
			body := fmt.Sprintf(`{"decisions":[{"new_fact_id":"%s","supersedes":[{"old_fact_id":"%s","should_supersede":true,"rationale":"stack change"}]}]}`, newID, oldID)
			return &provider.Response{Content: body, ProviderName: "mock", Model: "c-test", TokensUsed: 12}, nil
		}
		if call == 1 {
			return &provider.Response{Content: goFact, ProviderName: "mock", Model: "test", TokensUsed: 50}, nil
		}
		return &provider.Response{Content: rustFact, ProviderName: "mock", Model: "test", TokensUsed: 50}, nil
	}}

	eng := newTestEngineWithContradiction(t, store, sender, embedder, 0)
	ctx := context.Background()

	if _, err := eng.Ingest(ctx, "x", "a.md"); err != nil {
		t.Fatalf("first ingest: %v", err)
	}
	res, err := eng.Ingest(ctx, "y", "b.md")
	if err != nil {
		t.Fatalf("second ingest: %v", err)
	}
	if !res.ContradictionChecked {
		t.Fatal("expected ContradictionChecked")
	}
	if res.FactsSuperseded != 1 {
		t.Fatalf("FactsSuperseded = %d, want 1", res.FactsSuperseded)
	}

	facts, err := store.ListFacts(ctx, db.FactFilter{Limit: 20})
	if err != nil {
		t.Fatalf("list facts: %v", err)
	}
	var goF *model.Fact
	for i := range facts {
		if strings.Contains(facts[i].Content, "Go") {
			goF = &facts[i]
			break
		}
	}
	if goF == nil {
		t.Fatal("Go fact not found")
	}
	if goF.SupersededBy == "" {
		t.Fatal("expected Go fact superseded")
	}
	if !strings.HasPrefix(goF.SupersedeReason, "contradiction:") {
		t.Errorf("supersede_reason = %q", goF.SupersedeReason)
	}

	logs, err := store.ListExtractionLogs(ctx, 20)
	if err != nil {
		t.Fatalf("list logs: %v", err)
	}
	var sawContra bool
	for _, l := range logs {
		if l.ProviderName == "contradiction-check" && l.Success {
			sawContra = true
			break
		}
	}
	if !sawContra {
		t.Error("expected extraction_log row for contradiction-check")
	}
}

func TestIngest_ContradictionConfidenceGuardBlocks(t *testing.T) {
	vec := []float32{0.1, 0.3, 0.5, 0.7}
	embedder := &mockEmbedder{vec: vec}
	store := openStoreWithVec(t, 4)

	highConf := `{
  "facts": [
    {"fact_type": "decision", "subject": "Stack", "content": "We use Go for the API.", "confidence": 0.95, "validity": {"valid_from": null, "valid_until": null}}
  ],
  "entities": [],
  "relationships": []
}`
	lowConf := `{
  "facts": [
    {"fact_type": "decision", "subject": "Stack", "content": "We use Rust for the API.", "confidence": 0.35, "validity": {"valid_from": null, "valid_until": null}}
  ],
  "entities": [],
  "relationships": []
}`

	var call int
	sender := &cbSender{fn: func(ctx context.Context, req provider.Request) (*provider.Response, error) {
		call++
		if strings.Contains(req.SystemPrompt, "Contradiction review") {
			facts, err := store.ListFacts(ctx, db.FactFilter{Limit: 20})
			if err != nil {
				return nil, err
			}
			var oldID, newID string
			for _, f := range facts {
				if strings.Contains(f.Content, "Go") {
					oldID = f.ID
				}
				if strings.Contains(f.Content, "Rust") {
					newID = f.ID
				}
			}
			body := fmt.Sprintf(`{"decisions":[{"new_fact_id":"%s","supersedes":[{"old_fact_id":"%s","should_supersede":true,"rationale":"try"}]}]}`, newID, oldID)
			return &provider.Response{Content: body, ProviderName: "mock", Model: "c-test", TokensUsed: 5}, nil
		}
		if call == 1 {
			return &provider.Response{Content: highConf, ProviderName: "mock", Model: "test", TokensUsed: 50}, nil
		}
		return &provider.Response{Content: lowConf, ProviderName: "mock", Model: "test", TokensUsed: 50}, nil
	}}

	eng := newTestEngineWithContradiction(t, store, sender, embedder, 0)
	ctx := context.Background()

	if _, err := eng.Ingest(ctx, "x", "a.md"); err != nil {
		t.Fatalf("first ingest: %v", err)
	}
	res, err := eng.Ingest(ctx, "y", "b.md")
	if err != nil {
		t.Fatalf("second ingest: %v", err)
	}
	if res.FactsSuperseded != 0 {
		t.Fatalf("expected confidence guard to block supersede, got FactsSuperseded=%d", res.FactsSuperseded)
	}

	facts, err := store.ListFacts(ctx, db.FactFilter{Limit: 20})
	if err != nil {
		t.Fatalf("list facts: %v", err)
	}
	for _, f := range facts {
		if strings.Contains(f.Content, "Go") && f.SupersededBy != "" {
			t.Errorf("Go fact should not be superseded by low-confidence new fact, got superseded_by=%q", f.SupersededBy)
		}
	}
}
