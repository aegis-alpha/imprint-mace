package context

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aegis-alpha/imprint-mace/internal/db"
	"github.com/aegis-alpha/imprint-mace/internal/model"
)

func skipIfUSearchBroken(t *testing.T) {
	t.Helper()
	if os.Getenv("IMPRINT_SKIP_USEARCH") != "" {
		t.Skip("IMPRINT_SKIP_USEARCH set -- USearch C library crashes on this platform")
	}
}

type mockEmbedder struct {
	vec []float32
	err error
}

func (m *mockEmbedder) Embed(_ context.Context, _ string) ([]float32, error) {
	return m.vec, m.err
}

func (m *mockEmbedder) ModelName() string { return "mock-embed" }

func openTestStore(t *testing.T) *db.SQLiteStore {
	t.Helper()
	skipIfUSearchBroken(t)
	path := filepath.Join(t.TempDir(), "test.db")
	store, err := db.Open(path)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := store.AttachVectorIndex(4); err != nil {
		t.Fatalf("AttachVectorIndex: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func seedFact(t *testing.T, store *db.SQLiteStore, factType model.FactType, subject, content string, createdAt time.Time) string {
	t.Helper()
	id := db.NewID()
	err := store.CreateFact(context.Background(), &model.Fact{
		ID: id, Source: model.Source{TranscriptFile: "t.md"},
		FactType: factType, Subject: subject, Content: content,
		Confidence: 0.9, CreatedAt: createdAt,
	})
	if err != nil {
		t.Fatalf("seed fact: %v", err)
	}
	return id
}

func seedFactWithEmbedding(t *testing.T, store *db.SQLiteStore, factType model.FactType, subject, content string, createdAt time.Time, emb []float32) string {
	t.Helper()
	id := seedFact(t, store, factType, subject, content, createdAt)
	if err := store.UpdateFactEmbedding(context.Background(), id, emb, "mock-embed"); err != nil {
		t.Fatalf("seed embedding: %v", err)
	}
	return id
}

func TestBuild_WithHint(t *testing.T) {
	store := openTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)

	seedFactWithEmbedding(t, store, model.FactDecision, "Acme", "Acme uses Go", now, []float32{0.9, 0.1, 0.0, 0.0})
	seedFactWithEmbedding(t, store, model.FactPreference, "Alice", "Alice prefers dark mode", now, []float32{0.0, 0.0, 0.9, 0.1})

	emb := &mockEmbedder{vec: []float32{0.9, 0.1, 0.0, 0.0}}
	b := New(store, emb, "", BuilderConfig{RecentHours: 24, MaxFacts: 20, IncludePreferences: true}, slog.Default())

	result, err := b.Build(context.Background(), "What language does Acme use?")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if result == "" {
		t.Fatal("expected non-empty result")
	}
	if !strings.Contains(result, "Acme uses Go") {
		t.Errorf("expected result to contain 'Acme uses Go', got:\n%s", result)
	}
}

func TestBuild_EmptyHint(t *testing.T) {
	store := openTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)

	seedFact(t, store, model.FactPreference, "Alice", "Alice prefers dark mode", now)
	seedFact(t, store, model.FactDecision, "Acme", "Acme uses Go", now)

	b := New(store, nil, "", BuilderConfig{RecentHours: 24, MaxFacts: 20, IncludePreferences: true}, slog.Default())

	result, err := b.Build(context.Background(), "")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !strings.Contains(result, "Preferences") {
		t.Errorf("expected Preferences section for empty hint, got:\n%s", result)
	}
	if !strings.Contains(result, "Recent") {
		t.Errorf("expected Recent section for empty hint, got:\n%s", result)
	}
}

func TestBuild_NoEmbedder(t *testing.T) {
	store := openTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)

	seedFact(t, store, model.FactDecision, "Acme", "Acme uses Go", now)

	b := New(store, nil, "", BuilderConfig{RecentHours: 24, MaxFacts: 20, IncludePreferences: true}, slog.Default())

	result, err := b.Build(context.Background(), "Acme")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !strings.Contains(result, "Acme uses Go") {
		t.Errorf("expected text search to find 'Acme uses Go', got:\n%s", result)
	}
}

func TestBuild_NoFacts(t *testing.T) {
	store := openTestStore(t)

	b := New(store, nil, "", BuilderConfig{RecentHours: 24, MaxFacts: 20, IncludePreferences: true}, slog.Default())

	result, err := b.Build(context.Background(), "anything")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if result != "" {
		t.Errorf("expected empty string for empty DB, got %q", result)
	}
}

func TestBuild_PreferencesOnly(t *testing.T) {
	store := openTestStore(t)
	old := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second)

	seedFact(t, store, model.FactPreference, "Alice", "Alice prefers dark mode", old)
	seedFact(t, store, model.FactPreference, "Bob", "Bob prefers vim", old)

	b := New(store, nil, "", BuilderConfig{RecentHours: 1, MaxFacts: 20, IncludePreferences: true}, slog.Default())

	result, err := b.Build(context.Background(), "")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !strings.Contains(result, "Preferences") {
		t.Errorf("expected Preferences section, got:\n%s", result)
	}
	if strings.Contains(result, "Recent") {
		t.Errorf("expected no Recent section (facts are old), got:\n%s", result)
	}
}

func TestBuild_RecentFacts(t *testing.T) {
	store := openTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)

	seedFact(t, store, model.FactDecision, "Acme", "Acme uses Go", now)
	seedFact(t, store, model.FactEvent, "node-1", "node-1 restarted", now.Add(-1*time.Hour))

	b := New(store, nil, "", BuilderConfig{RecentHours: 24, MaxFacts: 20, IncludePreferences: false}, slog.Default())

	result, err := b.Build(context.Background(), "")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !strings.Contains(result, "Recent") {
		t.Errorf("expected Recent section, got:\n%s", result)
	}
	if !strings.Contains(result, "Acme uses Go") {
		t.Errorf("expected recent fact in output, got:\n%s", result)
	}
}

func TestBuild_Dedup(t *testing.T) {
	store := openTestStore(t)
	now := time.Now().UTC().Truncate(time.Second)

	seedFactWithEmbedding(t, store, model.FactDecision, "Acme", "Acme uses Go", now, []float32{0.9, 0.1, 0.0, 0.0})

	emb := &mockEmbedder{vec: []float32{0.9, 0.1, 0.0, 0.0}}
	b := New(store, emb, "", BuilderConfig{RecentHours: 24, MaxFacts: 20, IncludePreferences: true}, slog.Default())

	result, err := b.Build(context.Background(), "Acme Go")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	count := strings.Count(result, "Acme uses Go")
	if count > 1 {
		t.Errorf("expected fact to appear once (dedup), but appeared %d times in:\n%s", count, result)
	}
}
