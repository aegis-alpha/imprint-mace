package query

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aegis-alpha/imprint-mace/internal/db"
	"github.com/aegis-alpha/imprint-mace/internal/fts"
	"github.com/aegis-alpha/imprint-mace/internal/model"
	"github.com/aegis-alpha/imprint-mace/internal/provider"
)

// --- mocks ---

type mockSender struct {
	response *provider.Response
	err      error
}

func (m *mockSender) Send(_ context.Context, _ provider.Request) (*provider.Response, error) {
	return m.response, m.err
}

type mockEmbedder struct {
	vec []float32
	err error
}

func (m *mockEmbedder) Embed(_ context.Context, _ string) ([]float32, error) {
	return m.vec, m.err
}

func (m *mockEmbedder) ModelName() string { return "mock-embed" }

// --- helpers ---

func testQuerier(t *testing.T, sender *mockSender, embedder provider.Embedder) (*Querier, db.Store) {
	t.Helper()
	store, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	if embedder != nil {
		me := embedder.(*mockEmbedder)
		dims := len(me.vec)
		if dims > 0 {
			if err := store.EnsureVecTable(context.Background(), dims); err != nil {
				t.Fatalf("ensure vec table: %v", err)
			}
			if err := store.EnsureChunkVecTable(context.Background(), dims); err != nil {
				t.Fatalf("ensure chunk vec table: %v", err)
			}
		}
	}

	q := New(store, embedder, sender, "", slog.Default())
	return q, store
}

func seedFact(t *testing.T, store db.Store, id, subject, content string, embedder provider.Embedder) {
	t.Helper()
	now := time.Now()
	f := &model.Fact{
		ID:       id,
		Source:   model.Source{TranscriptFile: "test.md"},
		FactType: model.FactDecision,
		Subject:  subject,
		Content:  content,
		Confidence: 0.9,
		CreatedAt: now,
	}
	if err := store.CreateFact(context.Background(), f); err != nil {
		t.Fatalf("seed fact %s: %v", id, err)
	}
	if embedder != nil {
		vec, err := embedder.Embed(context.Background(), content)
		if err != nil {
			t.Fatalf("embed fact %s: %v", id, err)
		}
		if err := store.UpdateFactEmbedding(context.Background(), id, vec, embedder.ModelName()); err != nil {
			t.Fatalf("update embedding %s: %v", id, err)
		}
	}
}

func seedEntity(t *testing.T, store db.Store, id, name string) {
	t.Helper()
	e := &model.Entity{
		ID:         id,
		Name:       name,
		EntityType: model.EntityPerson,
		CreatedAt:  time.Now(),
	}
	if err := store.CreateEntity(context.Background(), e); err != nil {
		t.Fatalf("seed entity %s: %v", id, err)
	}
}

// --- tests ---

func TestQuery_ReturnsAnswer(t *testing.T) {
	vec := []float32{0.1, 0.2, 0.3, 0.4}
	embedder := &mockEmbedder{vec: vec}
	sender := &mockSender{response: &provider.Response{
		Content: `{
			"answer": "Acme uses Go for single-binary deployment.",
			"citations": [{"fact_id": "fact-001"}],
			"confidence": 0.95,
			"notes": ""
		}`,
		ProviderName: "mock",
		Model:        "test",
		TokensUsed:   50,
	}}

	q, store := testQuerier(t, sender, embedder)
	seedFact(t, store, "fact-001", "Acme", "Acme uses Go for single-binary deployment.", embedder)

	result, err := q.Query(context.Background(), "What language does Acme use?")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Answer == "" {
		t.Error("expected non-empty answer")
	}
	if len(result.Citations) == 0 {
		t.Error("expected at least one citation")
	}
	if result.Citations[0].FactID != "fact-001" {
		t.Errorf("expected citation fact_id 'fact-001', got %q", result.Citations[0].FactID)
	}
	if result.FactsConsulted == 0 {
		t.Error("expected facts_consulted > 0")
	}
}

func TestQuery_NoResults(t *testing.T) {
	vec := []float32{0.1, 0.2, 0.3, 0.4}
	embedder := &mockEmbedder{vec: vec}
	sender := &mockSender{response: &provider.Response{
		Content: `{
			"answer": "No information available.",
			"citations": [],
			"confidence": 0.1,
			"notes": "No relevant facts found."
		}`,
		ProviderName: "mock",
		Model:        "test",
		TokensUsed:   20,
	}}

	q, _ := testQuerier(t, sender, embedder)

	result, err := q.Query(context.Background(), "What is DataSync?")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Answer == "" {
		t.Error("expected non-empty answer even for no-results case")
	}
	if len(result.Citations) != 0 {
		t.Errorf("expected 0 citations for no-results, got %d", len(result.Citations))
	}
}

func TestQuery_NoEmbedder(t *testing.T) {
	sender := &mockSender{response: &provider.Response{
		Content: `{
			"answer": "Acme uses Go.",
			"citations": [{"fact_id": "fact-002"}],
			"confidence": 0.8,
			"notes": ""
		}`,
		ProviderName: "mock",
		Model:        "test",
		TokensUsed:   30,
	}}

	q, store := testQuerier(t, sender, nil)

	now := time.Now()
	f := &model.Fact{
		ID:         "fact-002",
		Source:     model.Source{TranscriptFile: "test.md"},
		FactType:   model.FactDecision,
		Subject:    "Acme",
		Content:    "Acme uses Go.",
		Confidence: 0.9,
		CreatedAt:  now,
	}
	if err := store.CreateFact(context.Background(), f); err != nil {
		t.Fatalf("seed fact: %v", err)
	}

	result, err := q.Query(context.Background(), "Acme Go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Answer == "" {
		t.Error("expected non-empty answer with FTS5-only fallback")
	}
	if result.FactsConsulted == 0 {
		t.Error("expected facts_consulted > 0 from FTS5 search")
	}
}

// --- prompt building + response parsing tests ---

func TestBuildPrompt_FormatsFactsCorrectly(t *testing.T) {
	q := New(nil, nil, nil, "", slog.Default())

	lr := [2]int{10, 15}
	facts := []enrichedFact{
		{
			rankedFact: rankedFact{
				fact: model.Fact{
					ID:         "bp-001",
					FactType:   model.FactDecision,
					Subject:    "Acme",
					Content:    "Acme uses Go.",
					Confidence: 0.95,
					CreatedAt:  time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC),
					Source:     model.Source{TranscriptFile: "chat.md", LineRange: &lr},
				},
				score: 1.0,
			},
			context: "line 10\nline 11\nline 12\n",
		},
		{
			rankedFact: rankedFact{
				fact: model.Fact{
					ID:         "bp-002",
					FactType:   model.FactPreference,
					Subject:    "Alice",
					Content:    "Alice prefers dark mode.",
					Confidence: 0.8,
					CreatedAt:  time.Date(2026, 3, 14, 0, 0, 0, 0, time.UTC),
				},
				score: 0.5,
			},
		},
	}

	prompt := q.buildPrompt("What does Acme use?", facts)

	if !strings.Contains(prompt.user, "### Question") {
		t.Error("expected user prompt to contain '### Question'")
	}
	if !strings.Contains(prompt.user, "What does Acme use?") {
		t.Error("expected user prompt to contain the question")
	}
	if !strings.Contains(prompt.user, "[bp-001]") {
		t.Error("expected user prompt to contain fact ID bp-001")
	}
	if !strings.Contains(prompt.user, "(decision, confidence=0.95, 2026-03-15)") {
		t.Error("expected user prompt to contain formatted fact metadata")
	}
	if !strings.Contains(prompt.user, "Acme: Acme uses Go.") {
		t.Error("expected user prompt to contain fact subject and content")
	}
	if !strings.Contains(prompt.user, "### Transcript Context") {
		t.Error("expected user prompt to contain transcript context section")
	}
	if !strings.Contains(prompt.user, "line 10") {
		t.Error("expected user prompt to contain transcript context lines")
	}
	if prompt.system == "" {
		t.Error("expected non-empty system prompt")
	}
}

func TestBuildPrompt_FactWithoutLineRange_NoPanic(t *testing.T) {
	q := New(nil, nil, nil, "", slog.Default())

	facts := []enrichedFact{
		{
			rankedFact: rankedFact{
				fact: model.Fact{
					ID:         "nlr-001",
					FactType:   model.FactDecision,
					Subject:    "Acme",
					Content:    "Acme uses Go.",
					Confidence: 0.9,
					CreatedAt:  time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC),
					Source:     model.Source{TranscriptFile: "chat.md"},
				},
				score: 1.0,
			},
			context: "some transcript context\n",
		},
	}

	prompt := q.buildPrompt("What does Acme use?", facts)

	if !strings.Contains(prompt.user, "### Transcript Context") {
		t.Error("expected transcript context section")
	}
	if !strings.Contains(prompt.user, "--- chat.md ---") {
		t.Error("expected header without line range")
	}
	if strings.Contains(prompt.user, "lines") {
		t.Error("header should not contain 'lines' when LineRange is nil")
	}
}

func TestParseResponse_ValidJSON(t *testing.T) {
	input := `{
		"answer": "Acme uses Go.",
		"citations": [{"fact_id": "f-001"}, {"consolidation_id": "c-001"}],
		"confidence": 0.95,
		"notes": "No contradictions."
	}`

	result, err := parseResponse(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Answer != "Acme uses Go." {
		t.Errorf("expected answer 'Acme uses Go.', got %q", result.Answer)
	}
	if len(result.Citations) != 2 {
		t.Fatalf("expected 2 citations, got %d", len(result.Citations))
	}
	if result.Citations[0].FactID != "f-001" {
		t.Errorf("expected first citation fact_id 'f-001', got %q", result.Citations[0].FactID)
	}
	if result.Citations[1].ConsolidationID != "c-001" {
		t.Errorf("expected second citation consolidation_id 'c-001', got %q", result.Citations[1].ConsolidationID)
	}
}

func TestParseResponse_WithMarkdownFences(t *testing.T) {
	input := "```json\n{\"answer\": \"test\", \"citations\": [], \"confidence\": 0.5, \"notes\": \"\"}\n```"

	result, err := parseResponse(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Answer != "test" {
		t.Errorf("expected answer 'test', got %q", result.Answer)
	}
}

func TestParseResponse_InvalidJSON_ReturnsError(t *testing.T) {
	_, err := parseResponse("this is not json at all")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestMergeResults_DeduplicatesByFactID(t *testing.T) {
	q := New(nil, nil, nil, "", slog.Default())

	r := &retrievalResult{
		factsByVector: []db.ScoredFact{
			{Fact: model.Fact{ID: "dup-001", Content: "fact A"}, Score: 0.9},
			{Fact: model.Fact{ID: "dup-002", Content: "fact B"}, Score: 0.8},
		},
		factsByText: []db.ScoredFact{
			{Fact: model.Fact{ID: "dup-001", Content: "fact A"}, Score: 0.7},
			{Fact: model.Fact{ID: "dup-003", Content: "fact C"}, Score: 0.6},
		},
	}

	ranked := q.mergeAndRank(r)

	ids := map[string]bool{}
	for _, rf := range ranked {
		if ids[rf.fact.ID] {
			t.Errorf("duplicate fact ID %q in ranked results", rf.fact.ID)
		}
		ids[rf.fact.ID] = true
	}
	if len(ranked) != 3 {
		t.Errorf("expected 3 unique facts, got %d", len(ranked))
	}
}

func TestMergeResults_FiltersExpiredFacts(t *testing.T) {
	q := New(nil, nil, nil, "", slog.Default())

	past := time.Now().UTC().Add(-24 * time.Hour)
	future := time.Now().UTC().Add(24 * time.Hour)

	r := &retrievalResult{
		factsByVector: []db.ScoredFact{
			{Fact: model.Fact{ID: "exp-001", Content: "expired", Validity: model.TimeRange{ValidUntil: &past}}, Score: 0.9},
			{Fact: model.Fact{ID: "exp-002", Content: "active", Validity: model.TimeRange{ValidUntil: &future}}, Score: 0.8},
			{Fact: model.Fact{ID: "exp-003", Content: "no expiry"}, Score: 0.7},
		},
	}

	ranked := q.mergeAndRank(r)
	if len(ranked) != 2 {
		t.Fatalf("expected 2 facts (expired filtered out), got %d", len(ranked))
	}
	for _, rf := range ranked {
		if rf.fact.ID == "exp-001" {
			t.Error("expired fact should have been filtered out")
		}
	}
}

func TestMergeResults_RRFRanking(t *testing.T) {
	q := New(nil, nil, nil, "", slog.Default())

	r := &retrievalResult{
		factsByVector: []db.ScoredFact{
			{Fact: model.Fact{ID: "rrf-001"}, Score: 0.9},
			{Fact: model.Fact{ID: "rrf-002"}, Score: 0.8},
		},
		factsByText: []db.ScoredFact{
			{Fact: model.Fact{ID: "rrf-001"}, Score: 0.7},
		},
	}

	ranked := q.mergeAndRank(r)
	if len(ranked) < 2 {
		t.Fatalf("expected at least 2 ranked facts, got %d", len(ranked))
	}
	if ranked[0].fact.ID != "rrf-001" {
		t.Errorf("expected rrf-001 to rank first (appears in both layers), got %q", ranked[0].fact.ID)
	}
	if ranked[0].score <= ranked[1].score {
		t.Error("expected first fact to have higher RRF score than second")
	}
}

// --- end-to-end tests ---

func TestQueryEndToEnd_AnswersFromSeededFacts(t *testing.T) {
	vec := []float32{0.1, 0.2, 0.3, 0.4}
	embedder := &mockEmbedder{vec: vec}

	sender := &mockSender{response: &provider.Response{
		Content: `{
			"answer": "Acme is written in Go. Alice leads the project and prefers dark mode for the IDE.",
			"citations": [
				{"fact_id": "e2e-fact-001"},
				{"fact_id": "e2e-fact-002"},
				{"fact_id": "e2e-fact-003"}
			],
			"confidence": 0.92,
			"notes": ""
		}`,
		ProviderName: "mock",
		Model:        "test",
		TokensUsed:   80,
	}}

	q, store := testQuerier(t, sender, embedder)

	seedFact(t, store, "e2e-fact-001", "Acme", "Acme is written in Go for single-binary deployment.", embedder)
	seedFact(t, store, "e2e-fact-002", "Alice", "Alice leads the Acme project.", embedder)
	seedFact(t, store, "e2e-fact-003", "Alice", "Alice prefers dark mode for the IDE.", embedder)

	seedEntity(t, store, "e2e-ent-alice", "Alice")
	seedEntity(t, store, "e2e-ent-acme", "Acme")

	rel := &model.Relationship{
		ID:           "e2e-rel-001",
		FromEntity:   "e2e-ent-alice",
		ToEntity:     "e2e-ent-acme",
		RelationType: model.RelWorksOn,
		SourceFact:   "e2e-fact-002",
		CreatedAt:    time.Now(),
	}
	if err := store.CreateRelationship(context.Background(), rel); err != nil {
		t.Fatalf("seed relationship: %v", err)
	}

	result, err := q.Query(context.Background(), "What does Alice work on and what language does Acme use?")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Answer == "" {
		t.Error("expected non-empty answer")
	}
	if !strings.Contains(result.Answer, "Go") {
		t.Error("expected answer to mention Go")
	}
	if result.FactsConsulted < 3 {
		t.Errorf("expected at least 3 facts consulted, got %d", result.FactsConsulted)
	}
}

func TestQueryEndToEnd_CitesCorrectFacts(t *testing.T) {
	vec := []float32{0.5, 0.5, 0.5, 0.5}
	embedder := &mockEmbedder{vec: vec}

	sender := &mockSender{response: &provider.Response{
		Content: `{
			"answer": "Bob manages the production servers.",
			"citations": [{"fact_id": "cite-fact-001"}],
			"confidence": 0.88,
			"notes": ""
		}`,
		ProviderName: "mock",
		Model:        "test",
		TokensUsed:   40,
	}}

	q, store := testQuerier(t, sender, embedder)

	seedFact(t, store, "cite-fact-001", "Bob", "Bob manages the production servers.", embedder)
	seedFact(t, store, "cite-fact-002", "Alice", "Alice prefers Python.", embedder)

	result, err := q.Query(context.Background(), "Who manages the production servers?")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Citations) == 0 {
		t.Fatal("expected at least one citation")
	}

	citedIDs := map[string]bool{}
	for _, c := range result.Citations {
		if c.FactID != "" {
			citedIDs[c.FactID] = true
		}
	}
	if !citedIDs["cite-fact-001"] {
		t.Error("expected citation to reference cite-fact-001")
	}
}

// --- enrichment tests ---

func TestEnrichWithContext_LoadsLines(t *testing.T) {
	dir := t.TempDir()
	transcript := "line 1\nline 2\nline 3\nline 4\nline 5\nline 6\nline 7\nline 8\nline 9\nline 10\n"
	if err := writeTestFile(t, dir, "chat.md", transcript); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	q := New(nil, nil, nil, dir, slog.Default())

	lr := [2]int{4, 6}
	ranked := []rankedFact{{
		fact: model.Fact{
			ID:      "enr-001",
			Source:  model.Source{TranscriptFile: "chat.md", LineRange: &lr},
			Content: "some fact",
		},
		score: 1.0,
	}}

	enriched := q.enrichWithContext(context.Background(), ranked, nil)
	if len(enriched) != 1 {
		t.Fatalf("expected 1 enriched fact, got %d", len(enriched))
	}
	if enriched[0].context == "" {
		t.Fatal("expected non-empty context from transcript file")
	}
	if !containsLine(enriched[0].context, "line 4") {
		t.Error("expected context to contain 'line 4'")
	}
}

func TestEnrichWithContext_SkipsIfNoLineRange(t *testing.T) {
	q := New(nil, nil, nil, t.TempDir(), slog.Default())

	ranked := []rankedFact{{
		fact: model.Fact{
			ID:      "enr-002",
			Source:  model.Source{TranscriptFile: "chat.md"},
			Content: "some fact",
		},
		score: 1.0,
	}}

	enriched := q.enrichWithContext(context.Background(), ranked, nil)
	if enriched[0].context != "" {
		t.Error("expected empty context when fact has no line range")
	}
}

func TestEnrichWithContext_IncludesChunkContext(t *testing.T) {
	dir := t.TempDir()
	transcript := "line 1\nline 2\nline 3\nline 4\nline 5\nline 6\nline 7\nline 8\nline 9\nline 10\n"
	if err := writeTestFile(t, dir, "meeting.md", transcript); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	store, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	ctx := context.Background()
	tr := &model.Transcript{
		ID:         "tr-chunk-001",
		FilePath:   "meeting.md",
		ChunkCount: 1,
		CreatedAt:  time.Now().UTC(),
	}
	if err := store.CreateTranscript(ctx, tr); err != nil {
		t.Fatalf("create transcript: %v", err)
	}

	chunk := &model.TranscriptChunk{
		ID:           "chunk-001",
		TranscriptID: "tr-chunk-001",
		LineStart:    3,
		LineEnd:      5,
		ContentHash:  "abc",
	}
	if err := store.CreateTranscriptChunk(ctx, chunk, "line 3\nline 4\nline 5\n"); err != nil {
		t.Fatalf("create chunk: %v", err)
	}

	q := New(store, nil, nil, dir, slog.Default())

	ranked := []rankedFact{{
		fact:  model.Fact{ID: "f-001", Content: "some fact"},
		score: 1.0,
	}}

	r := &retrievalResult{
		chunksByVector: []db.ScoredChunk{
			{Chunk: *chunk, Score: 0.9},
		},
	}

	enriched := q.enrichWithContext(ctx, ranked, r)
	if len(enriched) != 1 {
		t.Fatalf("expected 1 enriched fact, got %d", len(enriched))
	}
	if len(enriched[0].chunkContexts) == 0 {
		t.Fatal("expected chunk contexts to be populated")
	}
	if !strings.Contains(enriched[0].chunkContexts[0], "line 3") {
		t.Error("expected chunk context to contain 'line 3'")
	}
	if !strings.Contains(enriched[0].chunkContexts[0], "(chunk)") {
		t.Error("expected chunk context header to contain '(chunk)' marker")
	}
}

func writeTestFile(t *testing.T, dir, name, content string) error {
	t.Helper()
	path := dir + "/" + name
	return os.WriteFile(path, []byte(content), 0644)
}

func containsLine(text, line string) bool {
	for _, l := range splitLines(text) {
		if l == line {
			return true
		}
	}
	return false
}

func splitLines(s string) []string {
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		if l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

// --- retrieval layer tests ---

func TestRetrieveByFactVector_ReturnsScoredFacts(t *testing.T) {
	vec := []float32{0.1, 0.2, 0.3, 0.4}
	embedder := &mockEmbedder{vec: vec}
	q, store := testQuerier(t, &mockSender{}, embedder)

	seedFact(t, store, "fv-001", "Acme", "Acme uses Go.", embedder)
	seedFact(t, store, "fv-002", "Acme", "Acme deploys to Linux.", embedder)

	r := q.retrieve(context.Background(), "What does Acme use?", vec)
	if len(r.factsByVector) == 0 {
		t.Fatal("expected vector search to return facts")
	}
	if len(r.factsByVector) > 2 {
		t.Errorf("expected at most 2 facts, got %d", len(r.factsByVector))
	}
}

func TestRetrieveByText_ReturnsScoredFacts(t *testing.T) {
	q, store := testQuerier(t, &mockSender{}, nil)

	seedFact(t, store, "ft-001", "Acme", "Acme uses Go for deployment.", nil)
	seedFact(t, store, "ft-002", "Bob", "Bob prefers Python.", nil)

	r := q.retrieve(context.Background(), "Acme Go deployment", nil)
	if len(r.factsByText) == 0 {
		t.Fatal("expected FTS5 search to return facts")
	}
	found := false
	for _, sf := range r.factsByText {
		if sf.Fact.ID == "ft-001" {
			found = true
		}
	}
	if !found {
		t.Error("expected FTS5 to find fact ft-001 about Acme Go")
	}
}

func TestRetrieveByText_SanitizesSpecialChars(t *testing.T) {
	q, store := testQuerier(t, &mockSender{}, nil)

	seedFact(t, store, "ft-san-001", "Acme", "Acme uses Go.", nil)

	r := q.retrieve(context.Background(), "Acme uses Go?", nil)
	if len(r.factsByText) == 0 {
		t.Fatal("expected FTS5 to work after sanitizing '?' from query")
	}
}

func TestSanitizeFTS5Query(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"What does Acme use?", "What does Acme use"},
		{"Alice's project", "Alices project"},
		{"node-1 status", "node1 status"},
		{"(test)", "test"},
		{"", ""},
		{"???", ""},
		{"path/to/file", "pathtofile"},
		{"~user home", "user home"},
		{"key=val&other=2", "keyvalother2"},
		{"/usr/local/bin", "usrlocalbin"},
	}
	for _, tt := range tests {
		got := fts.SanitizeQuery(tt.input)
		if got != tt.want {
			t.Errorf("fts.SanitizeQuery(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestQuery_FTS5Only_FindsRelevantFacts(t *testing.T) {
	sender := &mockSender{response: &provider.Response{
		Content: `{
			"answer": "Acme uses SQLite for storage.",
			"citations": [{"fact_id": "fts-001"}, {"fact_id": "fts-003"}],
			"confidence": 0.85,
			"notes": ""
		}`,
		ProviderName: "mock",
		Model:        "test",
		TokensUsed:   40,
	}}

	q, store := testQuerier(t, sender, nil)

	seedFact(t, store, "fts-001", "Acme", "Acme uses SQLite for persistent storage.", nil)
	seedFact(t, store, "fts-002", "Bob", "Bob prefers Python for scripting.", nil)
	seedFact(t, store, "fts-003", "Acme", "Acme SQLite database runs on node-1.", nil)

	r := q.retrieve(context.Background(), "Acme SQLite", nil)

	if len(r.factsByVector) != 0 {
		t.Errorf("expected 0 vector results without embedder, got %d", len(r.factsByVector))
	}
	if len(r.factsByText) == 0 {
		t.Fatal("expected FTS5 to return facts for 'Acme SQLite'")
	}

	foundIDs := map[string]bool{}
	for _, sf := range r.factsByText {
		foundIDs[sf.Fact.ID] = true
	}
	if !foundIDs["fts-001"] {
		t.Error("expected FTS5 to find fts-001 (contains 'Acme' and 'SQLite')")
	}
	if !foundIDs["fts-003"] {
		t.Error("expected FTS5 to find fts-003 (contains 'Acme' and 'SQLite')")
	}

	result, err := q.Query(context.Background(), "Acme SQLite")
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if result.Answer == "" {
		t.Error("expected non-empty answer")
	}
	if result.FactsConsulted == 0 {
		t.Error("expected facts_consulted > 0 from FTS5-only retrieval")
	}
}

func TestRetrieveByGraph_FindsConnectedFacts(t *testing.T) {
	q, store := testQuerier(t, &mockSender{}, nil)

	seedEntity(t, store, "ent-alice", "Alice")
	seedEntity(t, store, "ent-acme", "Acme")
	seedFact(t, store, "gf-001", "Alice", "Alice works on Acme.", nil)

	rel := &model.Relationship{
		ID:           "rel-001",
		FromEntity:   "ent-alice",
		ToEntity:     "ent-acme",
		RelationType: model.RelWorksOn,
		SourceFact:   "gf-001",
		CreatedAt:    time.Now(),
	}
	if err := store.CreateRelationship(context.Background(), rel); err != nil {
		t.Fatalf("seed relationship: %v", err)
	}

	r := q.retrieve(context.Background(), "What does Alice work on?", nil)
	if len(r.graphFacts) == 0 {
		t.Fatal("expected graph traversal to find facts via Alice entity")
	}
	found := false
	for _, f := range r.graphFacts {
		if f.ID == "gf-001" {
			found = true
		}
	}
	if !found {
		t.Error("expected graph to find fact gf-001 via Alice -> Acme relationship")
	}
}
