package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aegis-alpha/imprint-mace/internal/config"
	impctx "github.com/aegis-alpha/imprint-mace/internal/context"
	"github.com/aegis-alpha/imprint-mace/internal/db"
	"github.com/aegis-alpha/imprint-mace/internal/extraction"
	"github.com/aegis-alpha/imprint-mace/internal/imprint"
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

func skipIfUSearchBroken(t *testing.T) {
	t.Helper()
	if os.Getenv("IMPRINT_SKIP_USEARCH") != "" {
		t.Skip("IMPRINT_SKIP_USEARCH set -- USearch C library crashes on this platform")
	}
}

func testAPI(t *testing.T) (*Handler, db.Store) {
	t.Helper()
	skipIfUSearchBroken(t)
	store, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := store.AttachVectorIndex(4); err != nil {
		t.Fatalf("AttachVectorIndex: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	sender := &mockSender{response: &provider.Response{
		Content:      `{"answer": "test answer", "citations": [], "confidence": 0.5, "notes": ""}`,
		ProviderName: "mock", Model: "test", TokensUsed: 10,
	}}
	q := query.New(store, nil, sender, "", slog.Default())
	h := NewHandler(nil, store, q, "test", slog.Default())
	return h, store
}

func testAPIWithBuilder(t *testing.T) (*Handler, db.Store) {
	t.Helper()
	skipIfUSearchBroken(t)
	store, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := store.AttachVectorIndex(4); err != nil {
		t.Fatalf("AttachVectorIndex: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	sender := &mockSender{response: &provider.Response{
		Content:      `{"answer": "test answer", "citations": [], "confidence": 0.5, "notes": ""}`,
		ProviderName: "mock", Model: "test", TokensUsed: 10,
	}}
	q := query.New(store, nil, sender, "", slog.Default())
	builder := impctx.New(store, nil, "", impctx.BuilderConfig{
		RecentHours:        24,
		MaxFacts:           20,
		IncludePreferences: true,
	}, slog.Default())
	h := NewHandlerWithBuilder(nil, store, q, builder, "test", slog.Default())
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
	var resp statusResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse JSON: %v", err)
	}
	if resp.Stats.Entities != 1 {
		t.Errorf("expected 1 entity, got %d", resp.Stats.Entities)
	}
	if resp.Version != "test" {
		t.Errorf("expected version %q, got %q", "test", resp.Version)
	}
}

func TestStatus_CoolStatsJSONUsesSnakeCase(t *testing.T) {
	h, store := testAPI(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	hm := &model.HotMessage{
		ID:                "cool-stat-1",
		Speaker:           "user",
		Content:           "hello",
		Timestamp:         now,
		PlatformSessionID: "sess-status",
		HasEmbedding:      false,
		CreatedAt:         now,
	}
	if err := store.InsertHotMessage(ctx, hm, nil); err != nil {
		t.Fatal(err)
	}
	future := now.Add(time.Hour)
	if _, err := store.MoveHotToCooldown(ctx, future, 1000); err != nil {
		t.Fatal(err)
	}
	if err := store.AssignCooldownCluster(ctx, "clust-status", []string{"cool-stat-1"}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/status", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `"clusters_pending"`) {
		t.Errorf("response should contain JSON key clusters_pending, body: %s", body)
	}
	if strings.Contains(body, `"ClustersPending"`) {
		t.Errorf("response should not contain PascalCase ClustersPending, body: %s", body)
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
	if err := json.Unmarshal(w.Body.Bytes(), &entities); err != nil {
		t.Fatalf("parse response JSON: %v", err)
	}
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
	if err := json.Unmarshal(w.Body.Bytes(), &facts); err != nil {
		t.Fatalf("parse response JSON: %v", err)
	}
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
	if err := json.Unmarshal(w.Body.Bytes(), &facts); err != nil {
		t.Fatalf("parse response JSON: %v", err)
	}
	if len(facts) != 1 {
		t.Errorf("expected 1 decision fact, got %d", len(facts))
	}
}

// --- GET /relationships ---

func TestRelationships_ListAll(t *testing.T) {
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

	req := httptest.NewRequest("GET", "/relationships", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var rels []model.Relationship
	if err := json.Unmarshal(w.Body.Bytes(), &rels); err != nil {
		t.Fatalf("parse JSON: %v", err)
	}
	if len(rels) != 1 {
		t.Errorf("expected 1 relationship, got %d", len(rels))
	}
}

func TestRelationships_FilterByType(t *testing.T) {
	h, store := testAPI(t)
	ctx := context.Background()

	store.CreateEntity(ctx, &model.Entity{
		ID: "e1", Name: "Alice", EntityType: model.EntityPerson, CreatedAt: time.Now(),
	})
	store.CreateEntity(ctx, &model.Entity{
		ID: "e2", Name: "Acme", EntityType: model.EntityProject, CreatedAt: time.Now(),
	})
	store.CreateEntity(ctx, &model.Entity{
		ID: "e3", Name: "Go", EntityType: model.EntityTool, CreatedAt: time.Now(),
	})
	store.CreateRelationship(ctx, &model.Relationship{
		ID: "r1", FromEntity: "e1", ToEntity: "e2", RelationType: model.RelWorksOn, CreatedAt: time.Now(),
	})
	store.CreateRelationship(ctx, &model.Relationship{
		ID: "r2", FromEntity: "e2", ToEntity: "e3", RelationType: model.RelUses, CreatedAt: time.Now(),
	})

	req := httptest.NewRequest("GET", "/relationships?type=works_on", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var rels []model.Relationship
	if err := json.Unmarshal(w.Body.Bytes(), &rels); err != nil {
		t.Fatalf("parse JSON: %v", err)
	}
	if len(rels) != 1 {
		t.Errorf("expected 1 works_on relationship, got %d", len(rels))
	}
}

func TestRelationships_FilterByEntity(t *testing.T) {
	h, store := testAPI(t)
	ctx := context.Background()

	store.CreateEntity(ctx, &model.Entity{
		ID: "e1", Name: "Alice", EntityType: model.EntityPerson, CreatedAt: time.Now(),
	})
	store.CreateEntity(ctx, &model.Entity{
		ID: "e2", Name: "Acme", EntityType: model.EntityProject, CreatedAt: time.Now(),
	})
	store.CreateEntity(ctx, &model.Entity{
		ID: "e3", Name: "Bob", EntityType: model.EntityPerson, CreatedAt: time.Now(),
	})
	store.CreateRelationship(ctx, &model.Relationship{
		ID: "r1", FromEntity: "e1", ToEntity: "e2", RelationType: model.RelWorksOn, CreatedAt: time.Now(),
	})
	store.CreateRelationship(ctx, &model.Relationship{
		ID: "r2", FromEntity: "e3", ToEntity: "e2", RelationType: model.RelWorksOn, CreatedAt: time.Now(),
	})

	req := httptest.NewRequest("GET", "/relationships?entity=e1", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var rels []model.Relationship
	if err := json.Unmarshal(w.Body.Bytes(), &rels); err != nil {
		t.Fatalf("parse JSON: %v", err)
	}
	if len(rels) != 1 {
		t.Errorf("expected 1 relationship for e1, got %d", len(rels))
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
	if err := json.Unmarshal(w.Body.Bytes(), &graph); err != nil {
		t.Fatalf("parse response JSON: %v", err)
	}
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
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("parse response JSON: %v", err)
	}
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

// --- POST /ingest hot mode ---

type ingestHotSpy struct {
	db.Store
	lastMsg   *model.HotMessage
	lastEmb   []float32
	insertErr error
}

func (s *ingestHotSpy) InsertHotMessage(ctx context.Context, msg *model.HotMessage, emb []float32) error {
	cp := *msg
	s.lastMsg = &cp
	s.lastEmb = nil
	if emb != nil {
		s.lastEmb = append([]float32(nil), emb...)
	}
	if s.insertErr != nil {
		return s.insertErr
	}
	return s.Store.InsertHotMessage(ctx, msg, emb)
}

type dimEmbedder struct {
	n int
}

func (d dimEmbedder) Embed(_ context.Context, _ string) ([]float32, error) {
	v := make([]float32, d.n)
	for i := range v {
		v[i] = 0.25
	}
	return v, nil
}

func (d dimEmbedder) ModelName() string { return "mock-dim" }

func apiPromptPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "prompt.md")
	if err := os.WriteFile(path, []byte("x\n## Fact Types ({{len .FactTypes}})\n"), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func newHotIngestHTTPHandler(t *testing.T, dim int) (*Handler, *ingestHotSpy) {
	t.Helper()
	skipIfUSearchBroken(t)
	base, err := db.Open(t.TempDir() + "/hot-ingest.db")
	if err != nil {
		t.Fatal(err)
	}
	if err := base.AttachVectorIndex(dim); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { base.Close() })
	spy := &ingestHotSpy{Store: base}
	sender := &mockSender{response: &provider.Response{
		Content:      `{"facts":[],"entities":[],"relationships":[]}`,
		ProviderName: "mock", Model: "test", TokensUsed: 1,
	}}
	q := query.New(spy, nil, sender, "", slog.Default())
	h := NewHandler(nil, spy, q, "test", slog.Default())
	h.SetHotEnabled(true)
	return h, spy
}

func TestIngestHot_Basic(t *testing.T) {
	h, spy := newHotIngestHTTPHandler(t, 8)
	h.SetHotEmbedMinChars(50)
	body := `{"text":"hello hot path minimum fifty charsxxxxxxxxxxxxxxxxxxxxxxxxxx","source":"realtime:sess-99"}`
	req := httptest.NewRequest("POST", "/ingest", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		ID           string `json:"id"`
		HasEmbedding bool   `json:"has_embedding"`
		Hot          bool   `json:"hot"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Hot || resp.ID == "" {
		t.Fatalf("response: %+v", resp)
	}
	if spy.lastMsg == nil || spy.lastMsg.PlatformSessionID != "sess-99" || spy.lastMsg.Platform != "unknown" {
		t.Fatalf("stored message metadata wrong: %#v", spy.lastMsg)
	}
}

func TestIngestHot_ShortContent(t *testing.T) {
	h, spy := newHotIngestHTTPHandler(t, 8)
	h.SetEmbedder(dimEmbedder{n: 8})
	h.SetHotEmbedMinChars(50)
	body := `{"text":"short"}`
	req := httptest.NewRequest("POST", "/ingest", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp struct {
		HasEmbedding bool `json:"has_embedding"`
		Hot          bool `json:"hot"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.HasEmbedding {
		t.Error("expected no embedding for short text")
	}
	if len(spy.lastEmb) != 0 {
		t.Error("expected nil embedding passed to store")
	}
}

func TestIngestHot_LongContent_WithEmbedder(t *testing.T) {
	h, spy := newHotIngestHTTPHandler(t, 8)
	h.SetEmbedder(dimEmbedder{n: 8})
	h.SetHotEmbedMinChars(50)
	body := `{"text":"` + strings.Repeat("x", 55) + `"}`
	req := httptest.NewRequest("POST", "/ingest", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp struct {
		HasEmbedding bool `json:"has_embedding"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if !resp.HasEmbedding {
		t.Error("expected has_embedding true")
	}
	if len(spy.lastEmb) != 8 {
		t.Errorf("expected 8-dim embedding, got %d", len(spy.lastEmb))
	}
}

func TestIngestHot_NoEmbedder(t *testing.T) {
	h, spy := newHotIngestHTTPHandler(t, 8)
	h.SetHotEmbedMinChars(50)
	body := `{"text":"` + strings.Repeat("y", 55) + `"}`
	req := httptest.NewRequest("POST", "/ingest", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp struct {
		HasEmbedding bool `json:"has_embedding"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.HasEmbedding {
		t.Error("expected has_embedding false without embedder")
	}
	if len(spy.lastEmb) != 0 {
		t.Error("expected no embedding blob")
	}
}

func TestIngestHot_MissingText(t *testing.T) {
	h, _ := newHotIngestHTTPHandler(t, 8)
	req := httptest.NewRequest("POST", "/ingest", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestIngestHot_Disabled_ColdPath(t *testing.T) {
	skipIfUSearchBroken(t)
	base, err := db.Open(t.TempDir() + "/cold-ingest.db")
	if err != nil {
		t.Fatal(err)
	}
	if err := base.AttachVectorIndex(8); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { base.Close() })
	spy := &ingestHotSpy{Store: base}
	sender := &mockSender{response: &provider.Response{
		Content:      `{"facts":[],"entities":[],"relationships":[]}`,
		ProviderName: "mock", Model: "test", TokensUsed: 1,
	}}
	ext, err := extraction.New(sender, apiPromptPath(t), config.DefaultTypes(), slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	ext.SetExtractionLogger(base)
	eng := imprint.New(ext, base, nil, 0, 0, slog.Default())
	q := query.New(spy, nil, sender, "", slog.Default())
	h := NewHandler(eng, spy, q, "test", slog.Default())
	h.SetHotEnabled(false)

	body := `{"text":"some text for extraction path"}`
	req := httptest.NewRequest("POST", "/ingest", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if spy.lastMsg != nil {
		t.Fatal("InsertHotMessage should not run on cold ingest path")
	}
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["hot"]; ok {
		t.Fatal("cold response should not include hot field")
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
	if err := json.Unmarshal(w.Body.Bytes(), &newFact); err != nil {
		t.Fatalf("parse response JSON: %v", err)
	}
	if newFact.Content != "Alice switched to Go." {
		t.Errorf("new content = %q", newFact.Content)
	}

	old, _ := store.GetFact(ctx, "ss-001")
	if old.SupersededBy == "" {
		t.Error("old fact should be superseded")
	}
}

// --- GET /context ---

func TestContext_ReturnsText(t *testing.T) {
	h, store := testAPIWithBuilder(t)
	ctx := context.Background()

	store.CreateFact(ctx, &model.Fact{
		ID: "ctx-1", FactType: model.FactPreference, Subject: "Alice",
		Content: "Alice prefers dark mode.", Confidence: 0.9,
		Source: model.Source{TranscriptFile: "test.md"}, CreatedAt: time.Now(),
	})

	req := httptest.NewRequest("GET", "/context?hint=dark+mode", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp contextResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response JSON: %v", err)
	}
	if resp.Context == "" {
		t.Error("expected non-empty context")
	}
}

func TestContext_EmptyHint(t *testing.T) {
	h, store := testAPIWithBuilder(t)
	ctx := context.Background()

	store.CreateFact(ctx, &model.Fact{
		ID: "ctx-2", FactType: model.FactPreference, Subject: "Bob",
		Content: "Bob prefers Go.", Confidence: 0.8,
		Source: model.Source{TranscriptFile: "test.md"}, CreatedAt: time.Now(),
	})

	req := httptest.NewRequest("GET", "/context", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp contextResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response JSON: %v", err)
	}
}

func TestContext_NoBuilder(t *testing.T) {
	h, _ := testAPI(t)

	req := httptest.NewRequest("GET", "/context", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 (route not registered without builder), got %d", w.Code)
	}
}

func TestContext_BuilderError(t *testing.T) {
	store, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	builder := impctx.New(&errStore{}, nil, "", impctx.BuilderConfig{
		RecentHours: 24, MaxFacts: 20, IncludePreferences: true,
	}, slog.Default())
	h := NewHandlerWithBuilder(nil, store, nil, builder, "test", slog.Default())

	req := httptest.NewRequest("GET", "/context?hint=test", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 (builder degrades gracefully), got %d: %s", w.Code, w.Body.String())
	}
	var resp contextResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response JSON: %v", err)
	}
	if resp.Context != "" {
		t.Errorf("expected empty context on store errors, got %q", resp.Context)
	}
}

type errStore struct{ db.Store }

func (e *errStore) SearchByVector(_ context.Context, _ []float32, _ int) ([]db.ScoredFact, error) {
	return nil, errors.New("forced error")
}

func (e *errStore) SearchByText(_ context.Context, _ string, _ int) ([]db.ScoredFact, error) {
	return nil, errors.New("forced error")
}

func (e *errStore) ListFacts(_ context.Context, _ db.FactFilter) ([]model.Fact, error) {
	return nil, errors.New("forced error")
}

func (e *errStore) CreateQueryLog(_ context.Context, _ *db.QueryLog) error {
	return nil
}

func (e *errStore) CreateEvalRun(_ context.Context, _ *db.EvalRun) error {
	return nil
}

func (e *errStore) LatestEvalRun(_ context.Context, _ string) (*db.EvalRun, error) {
	return nil, errors.New("no eval runs")
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
