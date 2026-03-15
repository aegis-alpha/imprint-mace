// Package taxonomy collects signals about the type system (D17).
// All signal collection is SQL-only -- no LLM cost.
package taxonomy

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/aegis-alpha/imprint-mace/internal/config"
	"github.com/aegis-alpha/imprint-mace/internal/db"
)

// SignalCollector analyzes facts and writes taxonomy signals.
type SignalCollector struct {
	sqlDB  *sql.DB
	store  db.Store
	types  config.TypesConfig
	logger *slog.Logger
}

// NewSignalCollector creates a SignalCollector. It needs raw *sql.DB access
// for aggregate queries that aren't part of the Store interface.
func NewSignalCollector(sqlDB *sql.DB, store db.Store, types config.TypesConfig, logger *slog.Logger) *SignalCollector {
	return &SignalCollector{
		sqlDB:  sqlDB,
		store:  store,
		types:  types,
		logger: logger,
	}
}

// CollectAll runs all signal collectors and writes results to taxonomy_signals.
// Returns the number of signals written.
func (sc *SignalCollector) CollectAll(ctx context.Context) (int, error) {
	var total int

	n, err := sc.collectCustomTypes(ctx)
	if err != nil {
		return total, fmt.Errorf("custom types: %w", err)
	}
	total += n

	n, err = sc.collectUnusedTypes(ctx)
	if err != nil {
		return total, fmt.Errorf("unused types: %w", err)
	}
	total += n

	n, err = sc.collectLowConfidence(ctx)
	if err != nil {
		return total, fmt.Errorf("low confidence: %w", err)
	}
	total += n

	sc.logger.Info("taxonomy signal collection complete", "signals_written", total)
	return total, nil
}

// ReviewThreshold configures when LLM taxonomy review should trigger.
type ReviewThreshold struct {
	MinAbsolute  int     // absolute minimum actionable count (default 10)
	PctThreshold float64 // fraction of total facts (default 0.05)
}

// ShouldReview checks whether enough actionable signals have accumulated
// to justify an LLM taxonomy review. Actionable signals are
// custom_frequency and low_confidence (not type_unused).
// Threshold: SUM(count) >= max(MinAbsolute, total_facts * PctThreshold).
func (sc *SignalCollector) ShouldReview(ctx context.Context, threshold ReviewThreshold) (bool, error) {
	var actionableSum int
	row := sc.sqlDB.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(count), 0) FROM taxonomy_signals
		WHERE resolved_by IS NULL
		AND signal_type IN ('custom_frequency', 'low_confidence')`)
	if err := row.Scan(&actionableSum); err != nil {
		return false, fmt.Errorf("sum actionable signals: %w", err)
	}

	var totalFacts int
	row = sc.sqlDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM facts")
	if err := row.Scan(&totalFacts); err != nil {
		return false, fmt.Errorf("count facts: %w", err)
	}

	pctMin := int(float64(totalFacts) * threshold.PctThreshold)
	effectiveThreshold := threshold.MinAbsolute
	if pctMin > effectiveThreshold {
		effectiveThreshold = pctMin
	}

	return actionableSum >= effectiveThreshold, nil
}

// collectCustomTypes finds fact_type values not in the known taxonomy.
func (sc *SignalCollector) collectCustomTypes(ctx context.Context) (int, error) {
	known := make(map[string]bool)
	for _, t := range sc.types.FactTypes {
		known[t.Name] = true
	}

	rows, err := sc.sqlDB.QueryContext(ctx,
		"SELECT fact_type, COUNT(*) as cnt FROM facts GROUP BY fact_type ORDER BY cnt DESC")
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	var count int
	now := time.Now()
	for rows.Next() {
		var typeName string
		var cnt int
		if err := rows.Scan(&typeName, &cnt); err != nil {
			return count, err
		}
		if !known[typeName] {
			sig := &db.TaxonomySignal{
				ID:           db.NewID(),
				SignalType:   "custom_frequency",
				TypeCategory: "fact",
				TypeName:     typeName,
				Count:        cnt,
				Details:      fmt.Sprintf(`{"frequency":%d}`, cnt),
				CreatedAt:    now,
			}
			if err := sc.store.CreateTaxonomySignal(ctx, sig); err != nil {
				return count, err
			}
			count++
		}
	}
	return count, rows.Err()
}

// collectUnusedTypes finds known types with zero facts.
func (sc *SignalCollector) collectUnusedTypes(ctx context.Context) (int, error) {
	used := make(map[string]bool)
	rows, err := sc.sqlDB.QueryContext(ctx, "SELECT DISTINCT fact_type FROM facts")
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return 0, err
		}
		used[t] = true
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	var count int
	now := time.Now()
	for _, td := range sc.types.FactTypes {
		if !used[td.Name] {
			sig := &db.TaxonomySignal{
				ID:           db.NewID(),
				SignalType:   "type_unused",
				TypeCategory: "fact",
				TypeName:     td.Name,
				Count:        0,
				Details:      `{}`,
				CreatedAt:    now,
			}
			if err := sc.store.CreateTaxonomySignal(ctx, sig); err != nil {
				return count, err
			}
			count++
		}
	}
	return count, nil
}

// collectLowConfidence finds types where average confidence is below 0.5.
func (sc *SignalCollector) collectLowConfidence(ctx context.Context) (int, error) {
	rows, err := sc.sqlDB.QueryContext(ctx,
		"SELECT fact_type, AVG(confidence) as avg_conf, COUNT(*) as cnt FROM facts GROUP BY fact_type HAVING avg_conf < 0.5")
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	var count int
	now := time.Now()
	for rows.Next() {
		var typeName string
		var avgConf float64
		var cnt int
		if err := rows.Scan(&typeName, &avgConf, &cnt); err != nil {
			return count, err
		}
		sig := &db.TaxonomySignal{
			ID:           db.NewID(),
			SignalType:   "low_confidence",
			TypeCategory: "fact",
			TypeName:     typeName,
			Count:        cnt,
			Details:      fmt.Sprintf(`{"avg_confidence":%.3f,"count":%d}`, avgConf, cnt),
			CreatedAt:    now,
		}
		if err := sc.store.CreateTaxonomySignal(ctx, sig); err != nil {
			return count, err
		}
		count++
	}
	return count, rows.Err()
}
