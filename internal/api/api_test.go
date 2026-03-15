package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aegis-alpha/imprint-mace/internal/db"
	"github.com/aegis-alpha/imprint-mace/internal/model"
	"github.com/aegis-alpha/imprint-mace/internal/provider"
	"github.com/aegis-alpha/imprint-mace/internal/query"
)

type mockSender struct {
	response *provider.Response
}

func (m *mockSender) Send(_ context.Context, _ provider.Request) (*provider.Response, error) {
	return m.response, nil
}

func testAPI(t *testing.T) (*Handler, db.Store) {
	t.Helper()
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

	sender := &mockSender{response: &provider.Response{
		Content:      `{"answer": "test answer", "citations": [], "confidence": 0.5, "notes": ""}`,
		ProviderName: "mock", Model: "test", TokensUsed: 10,
	}}
	q := query.New(store, nil, sender, "", slog.Default())
	h := NewHandler(nil, store, q, slog.Default())
	return h, store
}

// --- GET /status ---

func TestStatus_ReturnsStats(t *testing.T) {
	h, store := testAPI(t)
	ctx := context.Background()

	store.CreateEntity(ctx, &model.Entity{
		ID: "e1", Name: "Alice", EntityType: model.EntityPerson, CreatedAt: time.Now(),
	})

	req := httptest.NewRequest("GET", "/status", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var stats db.DBStats
	if err := json.Unmarshal(w.Body.Bytes(), &stats); err != nil {
		t.Fatalf("parse JSON: %v", err)
	}
	if stats.Entities != 1 {
		t.Errorf("expected 1 entity, got %d", stats.Entities)
	}
}

// --- GET /entities ---

func TestEntities_ListAll(t *testing.T) {
	h, store := testAPI(t)
	ctx := context.Background()

	store.CreateEntity(ctx, &model.Entity{
		ID: "e1", Name: "Alice", EntityType: model.EntityPerson, CreatedAt: time.Now(),
	})
	store.CreateEntity(ctx, &model.Entity{
		ID: "e2", Name: "Acme", EntityType: model.EntityProject, CreatedAt: time.Now(),
	})

	req := httptest.NewRequest("GET", "/entities", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var entities []model.Entity
	if err := json.Unmarshal(w.Body.Bytes(), &entities); err != nil {
		t.Fatalf("parse JSON: %v", err)
	}
	if len(entities) != 2 {
		t.Errorf("expected 2 entities, got %d", len(entities))
	}
}

func TestEntities_FilterByType(t *testing.T) {
	h, store := testAPI(t)
	ctx := context.Background()

	store.CreateEntity(ctx, &model.Entity{
		ID: "e1", Name: "Alice", EntityType: model.EntityPerson, CreatedAt: time.Now(),
	})
	store.CreateEntity(ctx, &model.Entity{
		ID: "e2", Name: "Acme", EntityType: model.EntityProject, CreatedAt: time.Now(),
	})

	req := httptest.NewRequest("GET", "/entities?type=person", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var entities []model.Entity
	json.Unmarshal(w.Body.Bytes(), &entities)
	if len(entities) != 1 {
		t.Errorf("expected 1 person entity, got %d", len(entities))
	}
}

// --- GET /facts ---

func TestFacts_ListAll(t *testing.T) {
	h, store := testAPI(t)
	ctx := context.Background()

	store.CreateFact(ctx, &model.Fact{
		ID: "f1", FactType: model.FactDecision, Subject: "Acme",
		Content: "Acme uses Go.", Confidence: 0.9,
		Source: model.Source{TranscriptFile: "test.md"}, CreatedAt: time.Now(),
	})

	req := httptest.NewRequest("GET", "/facts", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var facts []model.Fact
	json.Unmarshal(w.Body.Bytes(), &facts)
	if len(facts) != 1 {
		t.Errorf("expected 1 fact, got %d", len(facts))
	}
}

func TestFacts_FilterByType(t *testing.T) {
	h, store := testAPI(t)
	ctx := context.Background()

	store.CreateFact(ctx, &model.Fact{
		ID: "f1", FactType: model.FactDecision, Subject: "Acme",
		Content: "Acme uses Go.", Confidence: 0.9,
		Source: model.Source{TranscriptFile: "test.md"}, CreatedAt: time.Now(),
	})
	store.CreateFact(ctx, &model.Fact{
		ID: "f2", FactType: model.FactPreference, Subject: "Alice",
		Content: "Alice prefers dark mode.", Confidence: 0.8,
		Source: model.Source{TranscriptFile: "test.md"}, CreatedAt: time.Now(),
	})

	req := httptest.NewRequest("GET", "/facts?type=decision", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var facts []model.Fact
	json.Unmarshal(w.Body.Bytes(), &facts)
	if len(facts) != 1 {
		t.Errorf("expected 1 decision fact, got %d", len(facts))
	}
}

// --- GET /graph ---

func TestGraph_ReturnsSubgraph(t *testing.T) {
	h, store := testAPI(t)
	ctx := context.Background()

	store.CreateEntity(ctx, &model.Entity{
		ID: "e1", Name: "Alice", EntityType: model.EntityPerson, CreatedAt: time.Now(),
	})
	store.CreateEntity(ctx, &model.Entity{
		ID: "e2", Name: "Acme", EntityType: model.EntityProject, CreatedAt: time.Now(),
	})
	store.CreateRelationship(ctx, &model.Relationship{
		ID: "r1", FromEntity: "e1", ToEntity: "e2", RelationType: model.RelWorksOn, CreatedAt: time.Now(),
	})

	req := httptest.NewRequest("GET", "/graph/e1", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var graph db.EntityGraph
	json.Unmarshal(w.Body.Bytes(), &graph)
	if graph.Center.Name != "Alice" {
		t.Errorf("expected center=Alice, got %s", graph.Center.Name)
	}
	if len(graph.Relationships) != 1 {
		t.Errorf("expected 1 relationship, got %d", len(graph.Relationships))
	}
}

func TestGraph_NotFound(t *testing.T) {
	h, _ := testAPI(t)

	req := httptest.NewRequest("GET", "/graph/nonexistent", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

// --- GET /query ---

func TestQuery_ReturnsAnswer(t *testing.T) {
	h, store := testAPI(t)
	ctx := context.Background()

	store.CreateFact(ctx, &model.Fact{
		ID: "qf-1", FactType: model.FactDecision, Subject: "Acme",
		Content: "Acme uses Go.", Confidence: 0.9,
		Source: model.Source{TranscriptFile: "test.md"}, CreatedAt: time.Now(),
	})

	req := httptest.NewRequest("GET", "/query?q=What+language+does+Acme+use", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var result model.QueryResult
	json.Unmarshal(w.Body.Bytes(), &result)
	if result.Answer == "" {
		t.Error("expected non-empty answer")
	}
}

func TestQuery_MissingQ(t *testing.T) {
	h, _ := testAPI(t)

	req := httptest.NewRequest("GET", "/query", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

// --- POST /ingest ---

func TestIngest_MissingText(t *testing.T) {
	h, _ := testAPI(t)

	req := httptest.NewRequest("POST", "/ingest", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

// --- PATCH /facts/{id} ---

func TestPatchFact_UpdatesConfidence(t *testing.T) {
	h, store := testAPI(t)
	ctx := context.Background()

	store.CreateFact(ctx, &model.Fact{
		ID: "pf-001", FactType: model.FactDecision, Subject: "Acme",
		Content: "Acme uses Go.", Confidence: 0.5,
		Source: model.Source{TranscriptFile: "test.md"}, CreatedAt: time.Now(),
	})

	body := `{"confidence": 0.95}`
	req := httptest.NewRequest("PATCH", "/facts/pf-001", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	got, _ := store.GetFact(ctx, "pf-001")
	if got.Confidence != 0.95 {
		t.Errorf("expected confidence 0.95, got %f", got.Confidence)
	}
}

func TestPatchFact_NotFound(t *testing.T) {
	h, _ := testAPI(t)

	body := `{"confidence": 0.5}`
	req := httptest.NewRequest("PATCH", "/facts/nonexistent", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

// --- POST /facts/{id}/supersede ---

func TestPostSupersede_CreatesNewFact(t *testing.T) {
	h, store := testAPI(t)
	ctx := context.Background()

	store.CreateFact(ctx, &model.Fact{
		ID: "ss-001", FactType: model.FactSkill, Subject: "Alice",
		Content: "Alice uses Rust.", Confidence: 0.9,
		Source: model.Source{TranscriptFile: "test.md"}, CreatedAt: time.Now(),
	})

	body := `{"new_content": "Alice switched to Go.", "source": "manual"}`
	req := httptest.NewRequest("POST", "/facts/ss-001/supersede", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var newFact model.Fact
	json.Unmarshal(w.Body.Bytes(), &newFact)
	if newFact.Content != "Alice switched to Go." {
		t.Errorf("new content = %q", newFact.Content)
	}

	old, _ := store.GetFact(ctx, "ss-001")
	if old.SupersededBy == "" {
		t.Error("old fact should be superseded")
	}
}

// --- method not allowed ---

func TestMethodNotAllowed(t *testing.T) {
	h, _ := testAPI(t)

	req := httptest.NewRequest("DELETE", "/status", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}
