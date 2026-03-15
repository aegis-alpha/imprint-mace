package extraction

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/aegis-alpha/imprint-mace/internal/config"
	"github.com/aegis-alpha/imprint-mace/internal/db"
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

func testExtractor(t *testing.T, resp *provider.Response, err error) *Extractor {
	t.Helper()
	e, createErr := New(
		&mockSender{response: resp, err: err},
		"testdata/extraction-prompt.md",
		config.DefaultTypes(),
		slog.Default(),
	)
	if createErr != nil {
		t.Fatalf("failed to create extractor: %v", createErr)
	}
	return e
}

var validJSON = `{
  "facts": [
    {
      "fact_type": "decision",
      "subject": "Acme",
      "content": "Acme will be written in Go.",
      "confidence": 1.0,
      "validity": {"valid_from": null, "valid_until": null}
    },
    {
      "fact_type": "preference",
      "subject": "Alice",
      "content": "Alice prefers dark mode.",
      "confidence": 0.9,
      "validity": {"valid_from": null, "valid_until": null}
    }
  ],
  "entities": [
    {"name": "Alice", "entity_type": "person", "aliases": []},
    {"name": "Acme", "entity_type": "project", "aliases": ["acme-app"]}
  ],
  "relationships": [
    {"from_entity": "Alice", "to_entity": "Acme", "relation_type": "works_on"}
  ]
}`

// --- tests ---

func TestExtract_ParsesValidJSON(t *testing.T) {
	e := testExtractor(t, &provider.Response{
		Content:      validJSON,
		ProviderName: "mock",
		Model:        "test-model",
		TokensUsed:   100,
	}, nil)

	result, err := e.Extract(context.Background(), "some transcript text", "test.md")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Facts) != 2 {
		t.Fatalf("expected 2 facts, got %d", len(result.Facts))
	}
	if result.Facts[0].FactType != "decision" {
		t.Errorf("expected fact_type 'decision', got %q", result.Facts[0].FactType)
	}
	if result.Facts[0].Subject != "Acme" {
		t.Errorf("expected subject 'Acme', got %q", result.Facts[0].Subject)
	}
	if result.Facts[0].Content != "Acme will be written in Go." {
		t.Errorf("expected content, got %q", result.Facts[0].Content)
	}
	if result.Facts[0].Confidence != 1.0 {
		t.Errorf("expected confidence 1.0, got %f", result.Facts[0].Confidence)
	}
	if result.Facts[1].FactType != "preference" {
		t.Errorf("expected fact_type 'preference', got %q", result.Facts[1].FactType)
	}

	if len(result.Entities) != 2 {
		t.Fatalf("expected 2 entities, got %d", len(result.Entities))
	}
	if result.Entities[0].Name != "Alice" {
		t.Errorf("expected entity name 'Alice', got %q", result.Entities[0].Name)
	}
	if result.Entities[1].Aliases[0] != "acme-app" {
		t.Errorf("expected alias 'acme-app', got %v", result.Entities[1].Aliases)
	}

	if len(result.Relationships) != 1 {
		t.Fatalf("expected 1 relationship, got %d", len(result.Relationships))
	}
	if result.Relationships[0].RelationType != "works_on" {
		t.Errorf("expected relation_type 'works_on', got %q", result.Relationships[0].RelationType)
	}
}

func TestExtract_HandlesEmptyResult(t *testing.T) {
	e := testExtractor(t, &provider.Response{
		Content:      `{"facts": [], "entities": [], "relationships": []}`,
		ProviderName: "mock",
		Model:        "test-model",
	}, nil)

	result, err := e.Extract(context.Background(), "just some chatter", "test.md")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Facts) != 0 {
		t.Errorf("expected 0 facts, got %d", len(result.Facts))
	}
	if len(result.Entities) != 0 {
		t.Errorf("expected 0 entities, got %d", len(result.Entities))
	}
	if len(result.Relationships) != 0 {
		t.Errorf("expected 0 relationships, got %d", len(result.Relationships))
	}
}

func TestExtract_HandlesInvalidJSON(t *testing.T) {
	e := testExtractor(t, &provider.Response{
		Content:      "this is not json at all",
		ProviderName: "mock",
		Model:        "test-model",
	}, nil)

	_, err := e.Extract(context.Background(), "some text", "test.md")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestExtract_SetsIDsAndTimestamps(t *testing.T) {
	e := testExtractor(t, &provider.Response{
		Content:      validJSON,
		ProviderName: "mock",
		Model:        "test-model",
	}, nil)

	result, err := e.Extract(context.Background(), "some text", "test.md")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for i, f := range result.Facts {
		if len(f.ID) != 26 {
			t.Errorf("fact[%d]: expected 26-char ULID, got %q (len %d)", i, f.ID, len(f.ID))
		}
		if f.CreatedAt.IsZero() {
			t.Errorf("fact[%d]: created_at is zero", i)
		}
		if f.Source.TranscriptFile != "test.md" {
			t.Errorf("fact[%d]: expected source 'test.md', got %q", i, f.Source.TranscriptFile)
		}
	}
	for i, ent := range result.Entities {
		if len(ent.ID) != 26 {
			t.Errorf("entity[%d]: expected 26-char ULID, got %q (len %d)", i, ent.ID, len(ent.ID))
		}
		if ent.CreatedAt.IsZero() {
			t.Errorf("entity[%d]: created_at is zero", i)
		}
	}
	for i, r := range result.Relationships {
		if len(r.ID) != 26 {
			t.Errorf("relationship[%d]: expected 26-char ULID, got %q (len %d)", i, r.ID, len(r.ID))
		}
		if r.CreatedAt.IsZero() {
			t.Errorf("relationship[%d]: created_at is zero", i)
		}
	}
}

func TestExtract_ProviderFails(t *testing.T) {
	e := testExtractor(t, nil, fmt.Errorf("connection refused"))

	_, err := e.Extract(context.Background(), "some text", "test.md")
	if err == nil {
		t.Fatal("expected error when provider fails")
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("expected error to contain 'connection refused', got %q", err.Error())
	}
}

func TestRenderPrompt(t *testing.T) {
	e := testExtractor(t, &provider.Response{
		Content:      `{"facts": [], "entities": [], "relationships": []}`,
		ProviderName: "mock",
		Model:        "test-model",
	}, nil)

	prompt := e.RenderedPrompt()

	for _, typeName := range []string{"preference", "decision", "workflow", "goal", "skill"} {
		if !strings.Contains(prompt, typeName) {
			t.Errorf("rendered prompt missing fact type %q", typeName)
		}
	}
	for _, typeName := range []string{"person", "location", "document", "agent"} {
		if !strings.Contains(prompt, typeName) {
			t.Errorf("rendered prompt missing entity type %q", typeName)
		}
	}
	for _, typeName := range []string{"owns", "created_by", "part_of", "located_at"} {
		if !strings.Contains(prompt, typeName) {
			t.Errorf("rendered prompt missing relation type %q", typeName)
		}
	}
	if !strings.Contains(prompt, "Fact Types (12)") {
		t.Errorf("expected 'Fact Types (12)' in rendered prompt")
	}
}

// --- extraction log tests (D19) ---

func testExtractorWithLog(t *testing.T, store *db.SQLiteStore, resp *provider.Response, sendErr error) *Extractor {
	t.Helper()
	e := testExtractor(t, resp, sendErr)
	e.SetExtractionLogger(store)
	return e
}

func TestExtract_LogsSuccess(t *testing.T) {
	store, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	e := testExtractorWithLog(t, store, &provider.Response{
		Content:      validJSON,
		ProviderName: "mock",
		Model:        "test-model",
		TokensUsed:   100,
	}, nil)

	_, err = e.Extract(context.Background(), "some transcript text", "test.md")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	logs, err := store.ListExtractionLogs(context.Background(), 10)
	if err != nil {
		t.Fatalf("list logs: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 log entry, got %d", len(logs))
	}
	l := logs[0]
	if !l.Success {
		t.Error("expected success=true")
	}
	if l.ProviderName != "mock" {
		t.Errorf("expected provider 'mock', got %q", l.ProviderName)
	}
	if l.FactsCount != 2 {
		t.Errorf("expected facts_count 2, got %d", l.FactsCount)
	}
	if l.EntitiesCount != 2 {
		t.Errorf("expected entities_count 2, got %d", l.EntitiesCount)
	}
	if l.RelationshipsCount != 1 {
		t.Errorf("expected relationships_count 1, got %d", l.RelationshipsCount)
	}
	if l.TokensUsed != 100 {
		t.Errorf("expected tokens 100, got %d", l.TokensUsed)
	}
	if l.InputLength != len("some transcript text") {
		t.Errorf("expected input_length %d, got %d", len("some transcript text"), l.InputLength)
	}
}

func TestExtract_LogsParseError(t *testing.T) {
	store, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	e := testExtractorWithLog(t, store, &provider.Response{
		Content:      "not json",
		ProviderName: "mock",
		Model:        "test-model",
		TokensUsed:   50,
	}, nil)

	_, _ = e.Extract(context.Background(), "some text", "test.md")

	logs, err := store.ListExtractionLogs(context.Background(), 10)
	if err != nil {
		t.Fatalf("list logs: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 log entry, got %d", len(logs))
	}
	l := logs[0]
	if l.Success {
		t.Error("expected success=false")
	}
	if l.ErrorType != "parse_error" {
		t.Errorf("expected error_type 'parse_error', got %q", l.ErrorType)
	}
}

func TestExtract_LogsProviderError(t *testing.T) {
	store, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	e := testExtractorWithLog(t, store, nil, fmt.Errorf("timeout"))

	_, _ = e.Extract(context.Background(), "some text", "test.md")

	logs, err := store.ListExtractionLogs(context.Background(), 10)
	if err != nil {
		t.Fatalf("list logs: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 log entry, got %d", len(logs))
	}
	l := logs[0]
	if l.Success {
		t.Error("expected success=false")
	}
	if l.ErrorType != "provider_error" {
		t.Errorf("expected error_type 'provider_error', got %q", l.ErrorType)
	}
	if !strings.Contains(l.ErrorMessage, "timeout") {
		t.Errorf("expected error message to contain 'timeout', got %q", l.ErrorMessage)
	}
}
