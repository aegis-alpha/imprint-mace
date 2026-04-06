package quality

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/aegis-alpha/imprint-mace/internal/config"
	"github.com/aegis-alpha/imprint-mace/internal/db"

	_ "github.com/mattn/go-sqlite3"
)

func testConfig() config.QualityConfig {
	enabled := true
	return config.QualityConfig{
		Enabled:              &enabled,
		CollectionThreshold:  50,
		SupersedeRateWarning: 0.30,
		WindowDays:           30,
		DecayHalfLifeDays:    14,
	}
}

var sentinelFactID string

func setupTestDB(t *testing.T) (*db.SQLiteStore, *sql.DB) {
	t.Helper()
	tmpFile := fmt.Sprintf("%s/quality_test_%d.db", t.TempDir(), time.Now().UnixNano())
	store, err := db.Open(tmpFile)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	sqlDB := store.RawDB()
	sentinelFactID = db.NewID()
	_, err = sqlDB.Exec(`
		INSERT INTO facts (id, source_file, fact_type, content, confidence, created_at)
		VALUES (?, 'sentinel.md', 'rule', 'sentinel', 1.0, datetime('now'))`, sentinelFactID)
	if err != nil {
		t.Fatalf("insert sentinel fact: %v", err)
	}
	return store, sqlDB
}

type testFact struct {
	ID           string
	FactType     string
	Content      string
	Confidence   float64
	SupersededBy string
	CreatedAt    time.Time
}

func insertTestFact(t *testing.T, sqlDB *sql.DB, f *testFact) {
	t.Helper()
	var supersededBy interface{}
	if f.SupersededBy != "" {
		supersededBy = sentinelFactID
	}
	_, err := sqlDB.Exec(`
		INSERT INTO facts (id, source_file, fact_type, content, confidence, superseded_by, created_at)
		VALUES (?, 'test.md', ?, ?, ?, ?, ?)`,
		f.ID, f.FactType, f.Content, f.Confidence, supersededBy, f.CreatedAt.Format(time.RFC3339))
	if err != nil {
		t.Fatalf("insert test fact: %v", err)
	}
}

func insertExtractionLog(t *testing.T, sqlDB *sql.DB, collisions, creations int, createdAt time.Time) {
	t.Helper()
	_, err := sqlDB.Exec(`
		INSERT INTO extraction_log (id, provider_name, model, input_length, tokens_used,
			duration_ms, success, facts_count, entities_count, relationships_count,
			entity_collisions, entity_creations, created_at)
		VALUES (?, 'test', 'test-model', 100, 50, 100, 1, 5, 2, 1, ?, ?, ?)`,
		db.NewID(), collisions, creations, createdAt.Format(time.RFC3339))
	if err != nil {
		t.Fatalf("insert extraction log: %v", err)
	}
}

func insertFactCitation(t *testing.T, sqlDB *sql.DB, factID, queryID string) {
	t.Helper()
	_, err := sqlDB.Exec(`
		INSERT INTO fact_citations (fact_id, query_id, cited_at) VALUES (?, ?, datetime('now'))`,
		factID, queryID)
	if err != nil {
		t.Fatalf("insert fact citation: %v", err)
	}
}

func TestCollectSupersedeRates(t *testing.T) {
	store, sqlDB := setupTestDB(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	for i := 0; i < 40; i++ {
		f := &testFact{
			ID:        db.NewID(),
			FactType:  "decision",
			Content:   fmt.Sprintf("decision %d", i),
			Confidence: 0.8,
			CreatedAt: time.Now().UTC().Add(-time.Duration(i) * time.Hour),
		}
		if i < 16 {
			f.SupersededBy = "newer"
		}
		insertTestFact(t, sqlDB, f)
	}

	c := NewCollector(sqlDB, store, testConfig(), logger)
	n, err := c.collectSupersedeRates(ctx)
	if err != nil {
		t.Fatalf("collectSupersedeRates: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 signal, got %d", n)
	}

	signals, err := store.ListQualitySignals(ctx, SignalSupersedeRate, 10)
	if err != nil {
		t.Fatalf("ListQualitySignals: %v", err)
	}
	if len(signals) != 1 {
		t.Fatalf("expected 1 signal, got %d", len(signals))
	}
	if signals[0].Category != "decision" {
		t.Errorf("expected category 'decision', got %q", signals[0].Category)
	}
	if signals[0].Value < 0.3 || signals[0].Value > 0.5 {
		t.Errorf("expected supersede rate ~0.4 (16/40 with decay), got %.3f", signals[0].Value)
	}
}

func TestCollectSupersedeRates_ColdStart(t *testing.T) {
	store, sqlDB := setupTestDB(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	for i := 0; i < 10; i++ {
		insertTestFact(t, sqlDB, &testFact{
			ID:        db.NewID(),
			FactType:  "decision",
			Content:   fmt.Sprintf("decision %d", i),
			Confidence: 0.8,
			CreatedAt: time.Now().UTC(),
		})
	}

	c := NewCollector(sqlDB, store, testConfig(), logger)
	n, err := c.collectSupersedeRates(ctx)
	if err != nil {
		t.Fatalf("collectSupersedeRates: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 signals (cold start, <30 facts), got %d", n)
	}
}

func TestCollectContradictionSupersedeRates(t *testing.T) {
	store, sqlDB := setupTestDB(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	now := time.Now().UTC()
	// 40 decision facts total: 10 contradiction-superseded, 6 superseded for other reasons.
	for i := 0; i < 40; i++ {
		factID := db.NewID()
		var supersededBy any
		var supersedeReason any
		switch {
		case i < 10:
			supersededBy = sentinelFactID
			supersedeReason = "contradiction:model-judged-update"
		case i < 16:
			supersededBy = sentinelFactID
			supersedeReason = "batch-replaced"
		}
		_, err := sqlDB.Exec(`
			INSERT INTO facts (id, source_file, fact_type, content, confidence, superseded_by, supersede_reason, created_at)
			VALUES (?, 'test.md', 'decision', ?, 0.8, ?, ?, ?)`,
			factID, fmt.Sprintf("decision %d", i), supersededBy, supersedeReason, now.Add(-time.Duration(i)*time.Hour).Format(time.RFC3339))
		if err != nil {
			t.Fatalf("insert test fact: %v", err)
		}
	}

	c := NewCollector(sqlDB, store, testConfig(), logger)
	n, err := c.collectContradictionSupersedeRates(ctx)
	if err != nil {
		t.Fatalf("collectContradictionSupersedeRates: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 contradiction supersede signal, got %d", n)
	}

	signals, err := store.ListQualitySignals(ctx, SignalContradictionSupersedeRate, 10)
	if err != nil {
		t.Fatalf("ListQualitySignals: %v", err)
	}
	if len(signals) != 1 {
		t.Fatalf("expected 1 signal, got %d", len(signals))
	}
	if signals[0].Category != "decision" {
		t.Errorf("expected category 'decision', got %q", signals[0].Category)
	}
	// Expect ~10/40 = 0.25 (decay may shift slightly).
	if signals[0].Value < 0.18 || signals[0].Value > 0.32 {
		t.Errorf("expected contradiction supersede rate near 0.25, got %.3f", signals[0].Value)
	}
}

func TestCollectEntityCollisionRate(t *testing.T) {
	store, sqlDB := setupTestDB(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	now := time.Now().UTC()
	for i := 0; i < 10; i++ {
		insertExtractionLog(t, sqlDB, 7, 3, now.Add(-time.Duration(i)*time.Hour))
	}

	c := NewCollector(sqlDB, store, testConfig(), logger)
	n, err := c.collectEntityCollisionRate(ctx)
	if err != nil {
		t.Fatalf("collectEntityCollisionRate: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 signal, got %d", n)
	}

	signals, err := store.ListQualitySignals(ctx, SignalEntityCollisionRate, 10)
	if err != nil {
		t.Fatalf("ListQualitySignals: %v", err)
	}
	if len(signals) != 1 {
		t.Fatalf("expected 1 signal, got %d", len(signals))
	}
	if signals[0].Value < 0.69 || signals[0].Value > 0.71 {
		t.Errorf("expected collision rate ~0.70, got %.3f", signals[0].Value)
	}
}

func TestCollectEntityCollisionRate_ColdStart(t *testing.T) {
	store, sqlDB := setupTestDB(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	insertExtractionLog(t, sqlDB, 3, 2, time.Now().UTC())

	c := NewCollector(sqlDB, store, testConfig(), logger)
	n, err := c.collectEntityCollisionRate(ctx)
	if err != nil {
		t.Fatalf("collectEntityCollisionRate: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 signals (cold start, <50 attempts), got %d", n)
	}
}

func TestCollectConfidenceCalibration(t *testing.T) {
	store, sqlDB := setupTestDB(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	for i := 0; i < 250; i++ {
		conf := float64(i%10) / 10.0
		superseded := ""
		if conf < 0.3 && i%2 == 0 {
			superseded = "newer"
		}
		insertTestFact(t, sqlDB, &testFact{
			ID:           db.NewID(),
			FactType:     "decision",
			Content:      fmt.Sprintf("fact %d", i),
			Confidence:   conf,
			SupersededBy: superseded,
			CreatedAt:    time.Now().UTC().Add(-time.Duration(i) * time.Minute),
		})
	}

	c := NewCollector(sqlDB, store, testConfig(), logger)
	n, err := c.collectConfidenceCalibration(ctx)
	if err != nil {
		t.Fatalf("collectConfidenceCalibration: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 signal, got %d", n)
	}

	signals, err := store.ListQualitySignals(ctx, SignalConfidenceCalibration, 10)
	if err != nil {
		t.Fatalf("ListQualitySignals: %v", err)
	}
	if len(signals) != 1 {
		t.Fatalf("expected 1 signal, got %d", len(signals))
	}
	if signals[0].Value < 0 || signals[0].Value > 1 {
		t.Errorf("ECE should be in [0,1], got %.3f", signals[0].Value)
	}
}

func TestCollectConfidenceCalibration_ColdStart(t *testing.T) {
	store, sqlDB := setupTestDB(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	for i := 0; i < 50; i++ {
		insertTestFact(t, sqlDB, &testFact{
			ID:        db.NewID(),
			FactType:  "decision",
			Content:   fmt.Sprintf("fact %d", i),
			Confidence: 0.8,
			CreatedAt: time.Now().UTC(),
		})
	}

	c := NewCollector(sqlDB, store, testConfig(), logger)
	n, err := c.collectConfidenceCalibration(ctx)
	if err != nil {
		t.Fatalf("collectConfidenceCalibration: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 signals (cold start, <200 facts), got %d", n)
	}
}

func TestCollectCitationRates_ColdStart(t *testing.T) {
	store, sqlDB := setupTestDB(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	for i := 0; i < 40; i++ {
		insertTestFact(t, sqlDB, &testFact{
			ID:        db.NewID(),
			FactType:  "decision",
			Content:   fmt.Sprintf("fact %d", i),
			Confidence: 0.8,
			CreatedAt: time.Now().UTC(),
		})
	}
	for i := 0; i < 10; i++ {
		insertFactCitation(t, sqlDB, db.NewID(), db.NewID())
	}

	c := NewCollector(sqlDB, store, testConfig(), logger)
	n, err := c.collectCitationRates(ctx)
	if err != nil {
		t.Fatalf("collectCitationRates: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 signals (cold start, <50 queries), got %d", n)
	}
}

func TestCollectVolumeAnomalies_ColdStart(t *testing.T) {
	store, sqlDB := setupTestDB(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	for i := 0; i < 3; i++ {
		insertExtractionLog(t, sqlDB, 0, 5, time.Now().UTC().Add(-time.Duration(i)*24*time.Hour))
	}

	c := NewCollector(sqlDB, store, testConfig(), logger)
	n, err := c.collectVolumeAnomalies(ctx)
	if err != nil {
		t.Fatalf("collectVolumeAnomalies: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 signals (cold start, <7 days), got %d", n)
	}
}

func TestCollectAll(t *testing.T) {
	store, sqlDB := setupTestDB(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	for i := 0; i < 250; i++ {
		conf := 0.5 + float64(i%5)*0.1
		superseded := ""
		if i%4 == 0 {
			superseded = "newer"
		}
		insertTestFact(t, sqlDB, &testFact{
			ID:           db.NewID(),
			FactType:     "decision",
			Content:      fmt.Sprintf("fact %d", i),
			Confidence:   conf,
			SupersededBy: superseded,
			CreatedAt:    time.Now().UTC().Add(-time.Duration(i) * time.Minute),
		})
	}

	now := time.Now().UTC()
	for i := 0; i < 10; i++ {
		insertExtractionLog(t, sqlDB, 7, 3, now.Add(-time.Duration(i)*24*time.Hour))
	}

	c := NewCollector(sqlDB, store, testConfig(), logger)
	n, err := c.CollectAll(ctx)
	if err != nil {
		t.Fatalf("CollectAll: %v", err)
	}

	// Should have: supersede_rate (1 for "decision"), entity_collision_rate (1),
	// confidence_calibration (1), volume_anomaly (1).
	// Citation signals skipped (no queries). Confidence-citation skipped (no queries).
	if n < 3 {
		t.Errorf("expected at least 3 signals from CollectAll, got %d", n)
	}
}

func TestDecayWeight(t *testing.T) {
	w0 := decayWeight(0, 14)
	if w0 != 1.0 {
		t.Errorf("age=0 should give weight 1.0, got %f", w0)
	}

	w14 := decayWeight(14, 14)
	if w14 < 0.49 || w14 > 0.51 {
		t.Errorf("age=half_life should give weight ~0.5, got %f", w14)
	}

	w28 := decayWeight(28, 14)
	if w28 < 0.24 || w28 > 0.26 {
		t.Errorf("age=2*half_life should give weight ~0.25, got %f", w28)
	}

	wNoDecay := decayWeight(100, 0)
	if wNoDecay != 1.0 {
		t.Errorf("half_life=0 should give weight 1.0 (no decay), got %f", wNoDecay)
	}
}
