package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	mcplib "github.com/mark3labs/mcp-go/mcp"

	"github.com/aegis-alpha/imprint-mace/internal/config"
	"github.com/aegis-alpha/imprint-mace/internal/db"
	"github.com/aegis-alpha/imprint-mace/internal/extraction"
	"github.com/aegis-alpha/imprint-mace/internal/imprint"
	"github.com/aegis-alpha/imprint-mace/internal/model"
	"github.com/aegis-alpha/imprint-mace/internal/provider"
	"github.com/aegis-alpha/imprint-mace/internal/query"
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

func testServer(t *testing.T, sender *mockSender) *Server {
	t.Helper()
	store, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := store.EnsureVecTable(context.Background(), 8); err != nil {
		t.Fatalf("vec table: %v", err)
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

	eng := imprint.New(ext, store, nil, 0, 0, slog.Default())
	return New(eng, store, nil, slog.Default())
}

func testStore(t *testing.T) (*Server, db.Store) {
	t.Helper()
	store, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := store.EnsureVecTable(context.Background(), 8); err != nil {
		t.Fatalf("vec table: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	srv := New(nil, store, nil, slog.Default())
	return srv, store
}

func callTool(t *testing.T, name string, args map[string]any) mcplib.CallToolRequest {
	t.Helper()
	return mcplib.CallToolRequest{
		Params: mcplib.CallToolParams{
			Name:      name,
			Arguments: args,
		},
	}
}

func resultText(t *testing.T, result *mcplib.CallToolResult) string {
	t.Helper()
	if result == nil {
		t.Fatal("nil result")
	}
	if len(result.Content) == 0 {
		t.Fatal("empty content")
	}
	tc, ok := result.Content[0].(mcplib.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", result.Content[0])
	}
	return tc.Text
}

// --- imprint_ingest ---

var mockJSON = `{
  "facts": [
    {"fact_type": "decision", "subject": "Acme", "content": "Acme uses Go.", "confidence": 0.9, "validity": {}}
  ],
  "entities": [
    {"name": "Acme", "entity_type": "project", "aliases": []}
  ],
  "relationships": []
}`

func TestIngestTool_ExtractsAndStores(t *testing.T) {
	sender := &mockSender{response: &provider.Response{
		Content: mockJSON, ProviderName: "mock", Model: "test", TokensUsed: 100,
	}}
	srv := testServer(t, sender)

	req := callTool(t, "imprint_ingest", map[string]any{
		"text":   "Acme uses Go for everything.",
		"source": "test-session",
	})

	result, err := srv.handleIngest(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	text := resultText(t, result)
	var ir imprint.IngestResult
	if err := json.Unmarshal([]byte(text), &ir); err != nil {
		t.Fatalf("failed to parse result JSON: %v", err)
	}
	if ir.FactsCount != 1 {
		t.Errorf("expected 1 fact, got %d", ir.FactsCount)
	}
	if ir.EntitiesCount != 1 {
		t.Errorf("expected 1 entity, got %d", ir.EntitiesCount)
	}
}

func TestIngestTool_MissingText(t *testing.T) {
	sender := &mockSender{response: &provider.Response{
		Content: mockJSON, ProviderName: "mock", Model: "test", TokensUsed: 100,
	}}
	srv := testServer(t, sender)

	req := callTool(t, "imprint_ingest", map[string]any{})

	result, err := srv.handleIngest(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing text")
	}
}

func TestIngestTool_ProviderFails(t *testing.T) {
	sender := &mockSender{err: fmt.Errorf("connection refused")}
	srv := testServer(t, sender)

	req := callTool(t, "imprint_ingest", map[string]any{
		"text": "some text",
	})

	result, err := srv.handleIngest(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result when provider fails")
	}
}

// --- imprint_status ---

func TestStatusTool_ReturnsStats(t *testing.T) {
	srv, store := testStore(t)
	ctx := context.Background()

	store.CreateEntity(ctx, &model.Entity{
		ID: "e1", Name: "Alice", EntityType: model.EntityPerson,
	})
	store.CreateEntity(ctx, &model.Entity{
		ID: "e2", Name: "Acme", EntityType: model.EntityProject,
	})

	req := callTool(t, "imprint_status", map[string]any{})
	result, err := srv.handleStatus(ctx, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	text := resultText(t, result)
	var stats db.DBStats
	if err := json.Unmarshal([]byte(text), &stats); err != nil {
		t.Fatalf("failed to parse stats JSON: %v", err)
	}
	if stats.Entities != 2 {
		t.Errorf("expected 2 entities, got %d", stats.Entities)
	}
}

// --- imprint_entities ---

func TestEntitiesTool_FiltersByType(t *testing.T) {
	srv, store := testStore(t)
	ctx := context.Background()

	store.CreateEntity(ctx, &model.Entity{
		ID: "e1", Name: "Alice", EntityType: model.EntityPerson,
	})
	store.CreateEntity(ctx, &model.Entity{
		ID: "e2", Name: "Acme", EntityType: model.EntityProject,
	})
	store.CreateEntity(ctx, &model.Entity{
		ID: "e3", Name: "Bob", EntityType: model.EntityPerson,
	})

	req := callTool(t, "imprint_entities", map[string]any{
		"type": "person",
	})
	result, err := srv.handleEntities(ctx, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	text := resultText(t, result)
	var entities []model.Entity
	if err := json.Unmarshal([]byte(text), &entities); err != nil {
		t.Fatalf("failed to parse entities JSON: %v", err)
	}
	if len(entities) != 2 {
		t.Errorf("expected 2 person entities, got %d", len(entities))
	}
}

func TestEntitiesTool_DefaultLimit(t *testing.T) {
	srv, store := testStore(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		store.CreateEntity(ctx, &model.Entity{
			ID: fmt.Sprintf("e%d", i), Name: fmt.Sprintf("Entity%d", i), EntityType: model.EntityConcept,
		})
	}

	req := callTool(t, "imprint_entities", map[string]any{})
	result, err := srv.handleEntities(ctx, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	text := resultText(t, result)
	var entities []model.Entity
	if err := json.Unmarshal([]byte(text), &entities); err != nil {
		t.Fatalf("failed to parse entities JSON: %v", err)
	}
	if len(entities) != 3 {
		t.Errorf("expected 3 entities, got %d", len(entities))
	}
}

// --- imprint_graph ---

func TestGraphTool_ReturnsSubgraph(t *testing.T) {
	srv, store := testStore(t)
	ctx := context.Background()

	store.CreateEntity(ctx, &model.Entity{
		ID: "e1", Name: "Alice", EntityType: model.EntityPerson,
	})
	store.CreateEntity(ctx, &model.Entity{
		ID: "e2", Name: "Acme", EntityType: model.EntityProject,
	})
	store.CreateRelationship(ctx, &model.Relationship{
		ID: "r1", FromEntity: "e1", ToEntity: "e2", RelationType: model.RelWorksOn,
	})

	req := callTool(t, "imprint_graph", map[string]any{
		"entity": "Alice",
	})
	result, err := srv.handleGraph(ctx, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	text := resultText(t, result)
	var graph db.EntityGraph
	if err := json.Unmarshal([]byte(text), &graph); err != nil {
		t.Fatalf("failed to parse graph JSON: %v", err)
	}
	if graph.Center.Name != "Alice" {
		t.Errorf("expected center=Alice, got %s", graph.Center.Name)
	}
	if len(graph.Entities) < 2 {
		t.Errorf("expected at least 2 entities in graph, got %d", len(graph.Entities))
	}
	if len(graph.Relationships) != 1 {
		t.Errorf("expected 1 relationship, got %d", len(graph.Relationships))
	}
}

func TestGraphTool_EntityNotFound(t *testing.T) {
	srv, _ := testStore(t)

	req := callTool(t, "imprint_graph", map[string]any{
		"entity": "NonExistent",
	})
	result, err := srv.handleGraph(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for non-existent entity")
	}
}

func TestGraphTool_MissingEntity(t *testing.T) {
	srv, _ := testStore(t)

	req := callTool(t, "imprint_graph", map[string]any{})
	result, err := srv.handleGraph(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing entity param")
	}
}

func TestGraphTool_DepthClamp(t *testing.T) {
	srv, store := testStore(t)
	ctx := context.Background()

	store.CreateEntity(ctx, &model.Entity{
		ID: "e1", Name: "Alice", EntityType: model.EntityPerson,
	})

	req := callTool(t, "imprint_graph", map[string]any{
		"entity": "Alice",
		"depth":  float64(10),
	})
	result, err := srv.handleGraph(ctx, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Error("expected success even with clamped depth")
	}
}

// --- imprint_query ---

type mockQuerySender struct {
	response *provider.Response
}

func (m *mockQuerySender) Send(_ context.Context, _ provider.Request) (*provider.Response, error) {
	return m.response, nil
}

func TestQueryTool_ReturnsAnswer(t *testing.T) {
	store, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := store.EnsureVecTable(context.Background(), 4); err != nil {
		t.Fatalf("vec table: %v", err)
	}
	if err := store.EnsureChunkVecTable(context.Background(), 4); err != nil {
		t.Fatalf("chunk vec table: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	ctx := context.Background()
	store.CreateFact(ctx, &model.Fact{
		ID: "qf-001", FactType: "decision", Subject: "Acme",
		Content: "Acme uses Go.", Confidence: 0.9,
		Source: model.Source{TranscriptFile: "test.md"},
	})

	querySender := &mockQuerySender{response: &provider.Response{
		Content: `{"answer": "Acme uses Go.", "citations": [{"fact_id": "qf-001"}], "confidence": 0.9, "notes": ""}`,
		ProviderName: "mock", Model: "test", TokensUsed: 30,
	}}

	q := query.New(store, nil, querySender, "", slog.Default())
	srv := New(nil, store, q, slog.Default())

	req := callTool(t, "imprint_query", map[string]any{
		"question": "What language does Acme use?",
	})
	result, err := srv.handleQuery(ctx, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatal("expected success result")
	}

	text := resultText(t, result)
	var qr model.QueryResult
	if err := json.Unmarshal([]byte(text), &qr); err != nil {
		t.Fatalf("failed to parse query result: %v", err)
	}
	if qr.Answer == "" {
		t.Error("expected non-empty answer")
	}
	if len(qr.Citations) == 0 {
		t.Error("expected at least one citation")
	}
}

// --- imprint_update_fact ---

func TestUpdateFactTool_UpdatesConfidence(t *testing.T) {
	srv, store := testStore(t)
	ctx := context.Background()

	store.CreateFact(ctx, &model.Fact{
		ID: "uf-001", FactType: model.FactDecision, Subject: "Acme",
		Content: "Acme uses Go.", Confidence: 0.5,
		Source: model.Source{TranscriptFile: "test.md"},
	})

	req := callTool(t, "imprint_update_fact", map[string]any{
		"fact_id":    "uf-001",
		"confidence": float64(0.95),
	})
	result, err := srv.handleUpdateFact(ctx, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error: %s", resultText(t, result))
	}

	got, _ := store.GetFact(ctx, "uf-001")
	if got.Confidence != 0.95 {
		t.Errorf("expected confidence 0.95, got %f", got.Confidence)
	}
}

// --- imprint_supersede_fact ---

func TestSupersedeFactTool_CreatesNewFact(t *testing.T) {
	srv, store := testStore(t)
	ctx := context.Background()

	store.CreateFact(ctx, &model.Fact{
		ID: "sf-001", FactType: model.FactSkill, Subject: "Alice",
		Content: "Alice uses Rust.", Confidence: 0.9,
		Source: model.Source{TranscriptFile: "test.md"},
	})

	req := callTool(t, "imprint_supersede_fact", map[string]any{
		"old_fact_id": "sf-001",
		"new_content": "Alice switched to Go.",
	})
	result, err := srv.handleSupersedeFact(ctx, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error: %s", resultText(t, result))
	}

	var newFact model.Fact
	if err := json.Unmarshal([]byte(resultText(t, result)), &newFact); err != nil {
		t.Fatalf("parse result: %v", err)
	}
	if newFact.Content != "Alice switched to Go." {
		t.Errorf("new content = %q", newFact.Content)
	}

	old, _ := store.GetFact(ctx, "sf-001")
	if old.SupersededBy == "" {
		t.Error("old fact should be superseded")
	}
}

func TestSupersedeFactTool_OldFactNotFound(t *testing.T) {
	srv, _ := testStore(t)

	req := callTool(t, "imprint_supersede_fact", map[string]any{
		"old_fact_id": "nonexistent",
		"new_content": "something",
	})
	result, err := srv.handleSupersedeFact(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for non-existent fact")
	}
}

func TestQueryTool_MissingQuestion(t *testing.T) {
	srv := New(nil, nil, nil, slog.Default())

	req := callTool(t, "imprint_query", map[string]any{})
	result, err := srv.handleQuery(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing question")
	}
}
