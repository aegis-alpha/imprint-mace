// Package quality collects self-tuning signals from production data (BVP-279).
// All signal collection is SQL-only -- no LLM cost. Signals measure extraction
// quality via downstream usage patterns and confidence calibration.
package quality

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/aegis-alpha/imprint-mace/internal/config"
	"github.com/aegis-alpha/imprint-mace/internal/db"
)

const (
	SignalSupersedeRate                = "supersede_rate"
	SignalCitationRate                 = "citation_rate"
	SignalVolumeAnomaly                = "volume_anomaly"
	SignalEntityCollisionRate          = "entity_collision_rate"
	SignalConfidenceCalibration        = "confidence_calibration"
	SignalConfidenceCitationCalibration = "confidence_citation_calibration"
)

// Optimizer is the interface for the future Karpathy loop (D-Q4).
// When data is sufficient, the loop generates prompt candidates,
// evaluates them against the golden set, and keeps improvements.
type Optimizer interface {
	ShouldOptimize(signals []db.QualitySignal) bool
}

// Collector computes quality signals from production data and stores them.
type Collector struct {
	sqlDB  *sql.DB
	store  db.Store
	cfg    config.QualityConfig
	logger *slog.Logger
}

// NewCollector creates a quality signal collector.
func NewCollector(sqlDB *sql.DB, store db.Store, cfg config.QualityConfig, logger *slog.Logger) *Collector {
	return &Collector{
		sqlDB:  sqlDB,
		store:  store,
		cfg:    cfg,
		logger: logger,
	}
}

// CollectAll runs all signal collectors and writes results.
// Returns the number of signals written.
func (c *Collector) CollectAll(ctx context.Context) (int, error) {
	if c.cfg.Enabled != nil && !*c.cfg.Enabled {
		return 0, nil
	}

	var total int

	collectors := []struct {
		name string
		fn   func(context.Context) (int, error)
	}{
		{"supersede_rate", c.collectSupersedeRates},
		{"citation_rate", c.collectCitationRates},
		{"volume_anomaly", c.collectVolumeAnomalies},
		{"entity_collision_rate", c.collectEntityCollisionRate},
		{"confidence_calibration", c.collectConfidenceCalibration},
		{"confidence_citation_calibration", c.collectConfidenceCitationCalibration},
	}

	for _, col := range collectors {
		n, err := col.fn(ctx)
		if err != nil {
			c.logger.Warn("quality signal collection failed",
				"collector", col.name, "error", err)
			continue
		}
		total += n
	}

	c.logger.Info("quality signal collection complete", "signals_written", total)
	return total, nil
}

// decayWeight computes exp(-ln(2) * ageDays / halfLife) for temporal weighting.
func decayWeight(ageDays float64, halfLifeDays int) float64 {
	if halfLifeDays <= 0 {
		return 1.0
	}
	return math.Exp(-math.Ln2 * ageDays / float64(halfLifeDays))
}

// collectSupersedeRates computes weighted supersede rate per fact type.
// Cold-start: skips types with fewer than 30 facts in the window.
func (c *Collector) collectSupersedeRates(ctx context.Context) (int, error) {
	rows, err := c.sqlDB.QueryContext(ctx, `
		SELECT fact_type, superseded_by,
			julianday('now') - julianday(created_at) AS age_days
		FROM facts
		WHERE created_at > datetime('now', '-' || ? || ' days')`,
		c.cfg.WindowDays)
	if err != nil {
		return 0, fmt.Errorf("query facts: %w", err)
	}
	defer rows.Close()

	type accumulator struct {
		weightedSuperseded float64
		weightedTotal      float64
		count              int
	}
	byType := map[string]*accumulator{}

	for rows.Next() {
		var factType string
		var supersededBy sql.NullString
		var ageDays float64
		if err := rows.Scan(&factType, &supersededBy, &ageDays); err != nil {
			return 0, err
		}
		acc, ok := byType[factType]
		if !ok {
			acc = &accumulator{}
			byType[factType] = acc
		}
		w := decayWeight(ageDays, c.cfg.DecayHalfLifeDays)
		acc.weightedTotal += w
		if supersededBy.Valid && supersededBy.String != "" {
			acc.weightedSuperseded += w
		}
		acc.count++
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	var written int
	now := time.Now()
	for factType, acc := range byType {
		if acc.count < 30 {
			continue
		}
		rate := acc.weightedSuperseded / acc.weightedTotal
		sig := &db.QualitySignal{
			ID:        db.NewID(),
			SignalType: SignalSupersedeRate,
			Category:  factType,
			Value:     rate,
			Details:   fmt.Sprintf(`{"sample_size":%d,"window_days":%d}`, acc.count, c.cfg.WindowDays),
			CreatedAt: now,
		}
		if err := c.store.CreateQualitySignal(ctx, sig); err != nil {
			return written, err
		}
		written++
	}
	return written, nil
}

// collectCitationRates computes weighted citation rate per fact type.
// Cold-start: 30 facts/type + 50 total queries.
func (c *Collector) collectCitationRates(ctx context.Context) (int, error) {
	var queryCount int
	if err := c.sqlDB.QueryRowContext(ctx,
		"SELECT COUNT(DISTINCT query_id) FROM fact_citations").Scan(&queryCount); err != nil {
		return 0, fmt.Errorf("count queries: %w", err)
	}
	if queryCount < 50 {
		return 0, nil
	}

	rows, err := c.sqlDB.QueryContext(ctx, `
		SELECT f.fact_type, f.id,
			julianday('now') - julianday(f.created_at) AS age_days,
			CASE WHEN fc.fact_id IS NOT NULL THEN 1 ELSE 0 END AS cited
		FROM facts f
		LEFT JOIN (SELECT DISTINCT fact_id FROM fact_citations) fc ON f.id = fc.fact_id
		WHERE f.created_at > datetime('now', '-' || ? || ' days')
		AND (f.superseded_by IS NULL OR f.superseded_by = '')`,
		c.cfg.WindowDays)
	if err != nil {
		return 0, fmt.Errorf("query citation data: %w", err)
	}
	defer rows.Close()

	type accumulator struct {
		weightedCited float64
		weightedTotal float64
		count         int
	}
	byType := map[string]*accumulator{}

	for rows.Next() {
		var factType, factID string
		var ageDays float64
		var cited int
		if err := rows.Scan(&factType, &factID, &ageDays, &cited); err != nil {
			return 0, err
		}
		acc, ok := byType[factType]
		if !ok {
			acc = &accumulator{}
			byType[factType] = acc
		}
		w := decayWeight(ageDays, c.cfg.DecayHalfLifeDays)
		acc.weightedTotal += w
		if cited == 1 {
			acc.weightedCited += w
		}
		acc.count++
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	var written int
	now := time.Now()
	for factType, acc := range byType {
		if acc.count < 30 {
			continue
		}
		rate := acc.weightedCited / acc.weightedTotal
		sig := &db.QualitySignal{
			ID:        db.NewID(),
			SignalType: SignalCitationRate,
			Category:  factType,
			Value:     rate,
			Details:   fmt.Sprintf(`{"sample_size":%d,"total_queries":%d}`, acc.count, queryCount),
			CreatedAt: now,
		}
		if err := c.store.CreateQualitySignal(ctx, sig); err != nil {
			return written, err
		}
		written++
	}
	return written, nil
}

// collectVolumeAnomalies detects extraction volume deviations.
// Cold-start: 7 days of extraction_log data.
func (c *Collector) collectVolumeAnomalies(ctx context.Context) (int, error) {
	var dayCount int
	if err := c.sqlDB.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT DATE(created_at)) FROM extraction_log
		WHERE created_at > datetime('now', '-' || ? || ' days')`,
		c.cfg.WindowDays).Scan(&dayCount); err != nil {
		return 0, fmt.Errorf("count log days: %w", err)
	}
	if dayCount < 7 {
		return 0, nil
	}

	rows, err := c.sqlDB.QueryContext(ctx, `
		SELECT DATE(created_at) AS day,
			AVG(facts_count) AS avg_facts,
			MAX(facts_count) AS max_facts,
			SUM(CASE WHEN facts_count = 0 THEN 1 ELSE 0 END) AS zero_count,
			SUM(CASE WHEN facts_count > 10 THEN 1 ELSE 0 END) AS high_count,
			COUNT(*) AS total_calls
		FROM extraction_log
		WHERE success = 1
		AND created_at > datetime('now', '-' || ? || ' days')
		GROUP BY DATE(created_at)
		ORDER BY day DESC`,
		c.cfg.WindowDays)
	if err != nil {
		return 0, fmt.Errorf("query volume: %w", err)
	}
	defer rows.Close()

	var totalAvg, totalCalls float64
	var days int
	for rows.Next() {
		var day string
		var avgFacts, maxFacts float64
		var zeroCount, highCount, callCount int
		if err := rows.Scan(&day, &avgFacts, &maxFacts, &zeroCount, &highCount, &callCount); err != nil {
			return 0, err
		}
		totalAvg += avgFacts
		totalCalls += float64(callCount)
		days++
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if days == 0 {
		return 0, nil
	}

	overallAvg := totalAvg / float64(days)
	sig := &db.QualitySignal{
		ID:        db.NewID(),
		SignalType: SignalVolumeAnomaly,
		Category:  "global",
		Value:     overallAvg,
		Details:   fmt.Sprintf(`{"days":%d,"total_calls":%.0f}`, days, totalCalls),
		CreatedAt: time.Now(),
	}
	if err := c.store.CreateQualitySignal(ctx, sig); err != nil {
		return 0, err
	}
	return 1, nil
}

// collectEntityCollisionRate computes entity name collision rate from extraction_log.
// Cold-start: 50 total entity creation attempts.
func (c *Collector) collectEntityCollisionRate(ctx context.Context) (int, error) {
	var totalCollisions, totalCreations int
	if err := c.sqlDB.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(entity_collisions), 0), COALESCE(SUM(entity_creations), 0)
		FROM extraction_log
		WHERE created_at > datetime('now', '-' || ? || ' days')`,
		c.cfg.WindowDays).Scan(&totalCollisions, &totalCreations); err != nil {
		return 0, fmt.Errorf("query collision stats: %w", err)
	}

	totalAttempts := totalCollisions + totalCreations
	if totalAttempts < 50 {
		return 0, nil
	}

	rate := float64(totalCollisions) / float64(totalAttempts)
	sig := &db.QualitySignal{
		ID:        db.NewID(),
		SignalType: SignalEntityCollisionRate,
		Category:  "global",
		Value:     rate,
		Details:   fmt.Sprintf(`{"collisions":%d,"creations":%d,"total":%d}`, totalCollisions, totalCreations, totalAttempts),
		CreatedAt: time.Now(),
	}
	if err := c.store.CreateQualitySignal(ctx, sig); err != nil {
		return 0, err
	}
	return 1, nil
}

// collectConfidenceCalibration computes ECE (Expected Calibration Error)
// from confidence vs supersede data.
// Cold-start: 200 facts total in the window.
func (c *Collector) collectConfidenceCalibration(ctx context.Context) (int, error) {
	rows, err := c.sqlDB.QueryContext(ctx, `
		SELECT confidence, CASE WHEN superseded_by IS NOT NULL AND superseded_by != '' THEN 1 ELSE 0 END AS superseded
		FROM facts
		WHERE created_at > datetime('now', '-' || ? || ' days')`,
		c.cfg.WindowDays)
	if err != nil {
		return 0, fmt.Errorf("query confidence data: %w", err)
	}
	defer rows.Close()

	type bin struct {
		totalConf  float64
		superseded int
		count      int
	}
	bins := make([]bin, 10)

	var totalFacts int
	for rows.Next() {
		var conf float64
		var superseded int
		if err := rows.Scan(&conf, &superseded); err != nil {
			return 0, err
		}
		idx := int(conf * 10)
		if idx >= 10 {
			idx = 9
		}
		if idx < 0 {
			idx = 0
		}
		b := &bins[idx] //nolint:gosec // idx is bounds-checked above
		b.totalConf += conf
		b.superseded += superseded
		b.count++
		totalFacts++
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if totalFacts < 200 {
		return 0, nil
	}

	// ECE: weighted average of |accuracy - mean_confidence| per bin.
	// accuracy = 1 - supersede_rate (a non-superseded fact is "correct").
	var ece float64
	for _, b := range bins {
		if b.count == 0 {
			continue
		}
		accuracy := 1.0 - float64(b.superseded)/float64(b.count)
		meanConf := b.totalConf / float64(b.count)
		ece += float64(b.count) / float64(totalFacts) * math.Abs(accuracy-meanConf)
	}

	sig := &db.QualitySignal{
		ID:        db.NewID(),
		SignalType: SignalConfidenceCalibration,
		Category:  "global",
		Value:     ece,
		Details:   fmt.Sprintf(`{"total_facts":%d,"bins":10}`, totalFacts),
		CreatedAt: time.Now(),
	}
	if err := c.store.CreateQualitySignal(ctx, sig); err != nil {
		return 0, err
	}
	return 1, nil
}

// collectConfidenceCitationCalibration computes ECE from confidence vs citation data.
// Cold-start: 200 facts + 100 queries.
func (c *Collector) collectConfidenceCitationCalibration(ctx context.Context) (int, error) {
	var queryCount int
	if err := c.sqlDB.QueryRowContext(ctx,
		"SELECT COUNT(DISTINCT query_id) FROM fact_citations").Scan(&queryCount); err != nil {
		return 0, fmt.Errorf("count queries: %w", err)
	}
	if queryCount < 100 {
		return 0, nil
	}

	rows, err := c.sqlDB.QueryContext(ctx, `
		SELECT f.confidence,
			CASE WHEN fc.fact_id IS NOT NULL THEN 1 ELSE 0 END AS cited
		FROM facts f
		LEFT JOIN (SELECT DISTINCT fact_id FROM fact_citations) fc ON f.id = fc.fact_id
		WHERE f.created_at > datetime('now', '-' || ? || ' days')
		AND (f.superseded_by IS NULL OR f.superseded_by = '')`,
		c.cfg.WindowDays)
	if err != nil {
		return 0, fmt.Errorf("query confidence-citation data: %w", err)
	}
	defer rows.Close()

	type bin struct {
		totalConf float64
		cited     int
		count     int
	}
	bins := make([]bin, 10)

	var totalFacts int
	for rows.Next() {
		var conf float64
		var cited int
		if err := rows.Scan(&conf, &cited); err != nil {
			return 0, err
		}
		idx := int(conf * 10)
		if idx >= 10 {
			idx = 9
		}
		if idx < 0 {
			idx = 0
		}
		b := &bins[idx] //nolint:gosec // idx is bounds-checked above
		b.totalConf += conf
		b.cited += cited
		b.count++
		totalFacts++
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if totalFacts < 200 {
		return 0, nil
	}

	var ece float64
	for _, b := range bins {
		if b.count == 0 {
			continue
		}
		citationRate := float64(b.cited) / float64(b.count)
		meanConf := b.totalConf / float64(b.count)
		ece += float64(b.count) / float64(totalFacts) * math.Abs(citationRate-meanConf)
	}

	sig := &db.QualitySignal{
		ID:        db.NewID(),
		SignalType: SignalConfidenceCitationCalibration,
		Category:  "global",
		Value:     ece,
		Details:   fmt.Sprintf(`{"total_facts":%d,"total_queries":%d,"bins":10}`, totalFacts, queryCount),
		CreatedAt: time.Now(),
	}
	if err := c.store.CreateQualitySignal(ctx, sig); err != nil {
		return 0, err
	}
	return 1, nil
}
