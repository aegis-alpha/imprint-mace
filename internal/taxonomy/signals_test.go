package taxonomy

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/aegis-alpha/imprint-mace/internal/config"
	"github.com/aegis-alpha/imprint-mace/internal/db"
	"github.com/aegis-alpha/imprint-mace/internal/model"
)

func openTestDB(t *testing.T) (db.Store, *sql.DB) {
	t.Helper()
	store, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store, store.RawDB()
}

func seedFactsWithType(t *testing.T, store db.Store, factType model.FactType, confidence float64, n int) {
	t.Helper()
	now := time.Now()
	for i := 0; i < n; i++ {
		f := model.Fact{
			ID:         db.NewID(),
			Source:     model.Source{TranscriptFile: "test.md"},
			FactType:   factType,
			Subject:    fmt.Sprintf("subject-%d", i),
			Content:    fmt.Sprintf("Fact %d of type %s.", i, factType),
			Confidence: confidence,
			CreatedAt:  now.Add(time.Duration(i) * time.Second),
		}
		if err := store.CreateFact(context.Background(), &f); err != nil {
			t.Fatalf("seed fact: %v", err)
		}
	}
}

func TestCollectCustomTypes(t *testing.T) {
	store, rawDB := openTestDB(t)

	seedFactsWithType(t, store, "decision", 0.9, 2)
	seedFactsWithType(t, store, "custom_metric", 0.8, 3)

	sc := NewSignalCollector(rawDB, store, config.DefaultTypes(), slog.Default())
	n, err := sc.CollectAll(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	signals, err := store.ListTaxonomySignals(context.Background(), false, 50)
	if err != nil {
		t.Fatalf("list signals: %v", err)
	}

	var customSignals []db.TaxonomySignal
	for _, s := range signals {
		if s.SignalType == "custom_frequency" {
			customSignals = append(customSignals, s)
		}
	}
	if len(customSignals) != 1 {
		t.Fatalf("expected 1 custom_frequency signal, got %d (total signals: %d)", len(customSignals), n)
	}
	if customSignals[0].TypeName != "custom_metric" {
		t.Errorf("expected type_name 'custom_metric', got %q", customSignals[0].TypeName)
	}
	if customSignals[0].Count != 3 {
		t.Errorf("expected count 3, got %d", customSignals[0].Count)
	}
}

func TestCollectUnusedTypes(t *testing.T) {
	store, rawDB := openTestDB(t)

	seedFactsWithType(t, store, "decision", 0.9, 2)

	sc := NewSignalCollector(rawDB, store, config.DefaultTypes(), slog.Default())
	_, err := sc.CollectAll(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	signals, err := store.ListTaxonomySignals(context.Background(), false, 50)
	if err != nil {
		t.Fatalf("list signals: %v", err)
	}

	var unusedSignals []db.TaxonomySignal
	for _, s := range signals {
		if s.SignalType == "type_unused" {
			unusedSignals = append(unusedSignals, s)
		}
	}
	// 12 fact types in DefaultTypes, 1 used ("decision"), so 11 unused
	if len(unusedSignals) != 11 {
		t.Errorf("expected 11 type_unused signals, got %d", len(unusedSignals))
		for _, s := range unusedSignals {
			t.Logf("  unused: %s", s.TypeName)
		}
	}
}

func TestCollectLowConfidence(t *testing.T) {
	store, rawDB := openTestDB(t)

	seedFactsWithType(t, store, "context", 0.3, 5)
	seedFactsWithType(t, store, "decision", 0.95, 3)

	sc := NewSignalCollector(rawDB, store, config.DefaultTypes(), slog.Default())
	_, err := sc.CollectAll(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	signals, err := store.ListTaxonomySignals(context.Background(), false, 50)
	if err != nil {
		t.Fatalf("list signals: %v", err)
	}

	var lowConfSignals []db.TaxonomySignal
	for _, s := range signals {
		if s.SignalType == "low_confidence" {
			lowConfSignals = append(lowConfSignals, s)
		}
	}
	if len(lowConfSignals) != 1 {
		t.Fatalf("expected 1 low_confidence signal, got %d", len(lowConfSignals))
	}
	if lowConfSignals[0].TypeName != "context" {
		t.Errorf("expected type_name 'context', got %q", lowConfSignals[0].TypeName)
	}
	if lowConfSignals[0].Count != 5 {
		t.Errorf("expected count 5, got %d", lowConfSignals[0].Count)
	}
}

func TestCollectEmptyDB(t *testing.T) {
	store, rawDB := openTestDB(t)

	sc := NewSignalCollector(rawDB, store, config.DefaultTypes(), slog.Default())
	n, err := sc.CollectAll(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// All 12 types are unused when DB is empty
	if n != 12 {
		t.Errorf("expected 12 signals (all types unused), got %d", n)
	}
}

// --- ShouldReview tests ---

func TestShouldReview_BelowThreshold(t *testing.T) {
	store, rawDB := openTestDB(t)
	ctx := context.Background()

	seedFactsWithType(t, store, "custom_metric", 0.8, 3)

	sc := NewSignalCollector(rawDB, store, config.DefaultTypes(), slog.Default())
	sc.CollectAll(ctx)

	got, err := sc.ShouldReview(ctx, ReviewThreshold{MinAbsolute: 10, PctThreshold: 0.05})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got {
		t.Error("expected false: 3 custom facts below absolute threshold of 10")
	}
}

func TestShouldReview_AbsoluteThreshold(t *testing.T) {
	store, rawDB := openTestDB(t)
	ctx := context.Background()

	seedFactsWithType(t, store, "custom_metric", 0.8, 12)

	sc := NewSignalCollector(rawDB, store, config.DefaultTypes(), slog.Default())
	sc.CollectAll(ctx)

	got, err := sc.ShouldReview(ctx, ReviewThreshold{MinAbsolute: 10, PctThreshold: 0.05})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Error("expected true: 12 custom facts >= absolute threshold of 10")
	}
}

func TestShouldReview_PercentThreshold(t *testing.T) {
	store, rawDB := openTestDB(t)
	ctx := context.Background()

	seedFactsWithType(t, store, "decision", 0.9, 180)
	seedFactsWithType(t, store, "custom_metric", 0.8, 5)
	seedFactsWithType(t, store, "context", 0.3, 15)

	sc := NewSignalCollector(rawDB, store, config.DefaultTypes(), slog.Default())
	sc.CollectAll(ctx)

	// total facts = 200, 5% = 10
	// actionable: custom_metric count=5 + low_confidence context count=15 = 20
	// 20 >= max(10, 10) = true
	got, err := sc.ShouldReview(ctx, ReviewThreshold{MinAbsolute: 10, PctThreshold: 0.05})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Error("expected true: actionable sum 20 >= 5% of 200 facts")
	}
}
