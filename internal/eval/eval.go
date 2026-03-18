package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/aegis-alpha/imprint-mace/internal/extraction"
	"github.com/aegis-alpha/imprint-mace/internal/model"
)

// Config controls the eval run.
type Config struct {
	MatchThreshold float64 // minimum FactMatchScore to count as match (default 0.5)
}

// DefaultConfig returns sensible defaults.
func DefaultConfig() Config {
	return Config{
		MatchThreshold: 0.5,
	}
}

// Report is the full eval output.
type Report struct {
	GoldenCount   int            `json:"golden_count"`
	PositiveCount int            `json:"positive_count"`
	NoiseCount    int            `json:"noise_count"`
	Facts         CategoryScore  `json:"facts"`
	Entities      CategoryScore  `json:"entities"`
	Relationships CategoryScore  `json:"relationships"`
	Noise         NoiseResult    `json:"noise"`
	Calibration   CalibrationResult `json:"calibration"`
	Composite     float64        `json:"composite_score"`
	Examples      []ExampleResult `json:"examples,omitempty"`
}

// ExampleResult is the per-example detail.
type ExampleResult struct {
	Name          string        `json:"name"`
	IsNoise       bool          `json:"is_noise"`
	SystemFacts   int           `json:"system_facts"`
	GoldFacts     int           `json:"gold_facts"`
	SystemEnts    int           `json:"system_entities"`
	GoldEnts      int           `json:"gold_entities"`
	SystemRels    int           `json:"system_relationships"`
	GoldRels      int           `json:"gold_relationships"`
	FactScore     CategoryScore `json:"fact_score"`
	EntityScore   CategoryScore `json:"entity_score"`
	RelScore      CategoryScore `json:"relationship_score"`
	Error         string        `json:"error,omitempty"`
}

// Run executes the eval: loads golden set, runs extraction on each example,
// compares results, and returns the report.
func Run(ctx context.Context, extractor *extraction.Extractor, examples []GoldenExample, cfg Config) (*Report, error) {
	if cfg.MatchThreshold == 0 {
		cfg.MatchThreshold = 0.5
	}

	var (
		allSysFacts   []GoldenFact
		allGoldFacts  []GoldenFact
		allSysEnts    []GoldenEntity
		allGoldEnts   []GoldenEntity
		allSysRels    []GoldenRelationship
		allGoldRels   []GoldenRelationship
		noiseFacts    []int
		calSamples    []CalibrationSample
		positiveCount int
		noiseCount    int
		exResults     []ExampleResult
	)

	for _, ex := range examples {
		er := ExampleResult{
			Name:     ex.Name,
			IsNoise:  ex.Expected.IsNoise(),
			GoldFacts: len(ex.Expected.Facts),
			GoldEnts:  len(ex.Expected.Entities),
			GoldRels:  len(ex.Expected.Relationships),
		}

		result, err := extractor.Extract(ctx, ex.Text, "eval:"+ex.Name)
		if err != nil {
			er.Error = err.Error()
			exResults = append(exResults, er)
			if ex.Expected.IsNoise() {
				noiseCount++
				noiseFacts = append(noiseFacts, 0)
			} else {
				positiveCount++
			}
			continue
		}

		sysFacts := factsToGolden(result.Facts)
		sysEnts := entitiesToGolden(result.Entities)
		sysRels := relsToGolden(result.Relationships)

		er.SystemFacts = len(sysFacts)
		er.SystemEnts = len(sysEnts)
		er.SystemRels = len(sysRels)

		if ex.Expected.IsNoise() {
			noiseCount++
			noiseFacts = append(noiseFacts, len(sysFacts))
		} else {
			positiveCount++
			er.FactScore = ScoreFacts(sysFacts, ex.Expected.Facts, cfg.MatchThreshold)
			er.EntityScore = ScoreEntities(sysEnts, ex.Expected.Entities)
			er.RelScore = ScoreRelationships(sysRels, ex.Expected.Relationships, sysEnts, ex.Expected.Entities)

			allSysFacts = append(allSysFacts, sysFacts...)
			allGoldFacts = append(allGoldFacts, ex.Expected.Facts...)
			allSysEnts = append(allSysEnts, sysEnts...)
			allGoldEnts = append(allGoldEnts, ex.Expected.Entities...)
			allSysRels = append(allSysRels, sysRels...)
			allGoldRels = append(allGoldRels, ex.Expected.Relationships...)
		}

		for _, sf := range sysFacts {
			matched := false
			for _, gf := range ex.Expected.Facts {
				if FactMatchScore(sf, gf) >= cfg.MatchThreshold {
					matched = true
					break
				}
			}
			calSamples = append(calSamples, CalibrationSample{
				Confidence: sf.Confidence,
				Correct:    matched,
			})
		}

		exResults = append(exResults, er)
	}

	factScore := ScoreFacts(allSysFacts, allGoldFacts, cfg.MatchThreshold)
	entityScore := ScoreEntities(allSysEnts, allGoldEnts)
	relScore := ScoreRelationships(allSysRels, allGoldRels, allSysEnts, allGoldEnts)
	noiseResult := ScoreNoise(noiseFacts)
	calResult := ScoreCalibration(calSamples)

	composite := CompositeScore(factScore.F1, entityScore.F1, relScore.F1, calResult.ECE, noiseResult.NRR)

	return &Report{
		GoldenCount:   len(examples),
		PositiveCount: positiveCount,
		NoiseCount:    noiseCount,
		Facts:         factScore,
		Entities:      entityScore,
		Relationships: relScore,
		Noise:         noiseResult,
		Calibration:   calResult,
		Composite:     composite,
		Examples:      exResults,
	}, nil
}

// WriteTable writes a human-readable report to w.
func WriteTable(w io.Writer, r *Report) {
	fmt.Fprintf(w, "Imprint Extraction Eval\n")
	fmt.Fprintf(w, "========================\n")
	fmt.Fprintf(w, "Golden set: %d examples (%d positive, %d noise)\n\n",
		r.GoldenCount, r.PositiveCount, r.NoiseCount)

	fmt.Fprintf(w, "%-16s %10s %10s %10s %10s\n", "Category", "Precision", "Recall", "F1", "Support")
	fmt.Fprintf(w, "%s\n", strings.Repeat("-", 58))
	printRow(w, "Facts", r.Facts)
	printRow(w, "Entities", r.Entities)
	printRow(w, "Relationships", r.Relationships)
	fmt.Fprintf(w, "%s\n", strings.Repeat("-", 58))

	if r.NoiseCount > 0 {
		fmt.Fprintf(w, "Noise Rejection Rate:  %.2f (%d/%d)\n",
			r.Noise.NRR, r.Noise.Rejected, r.Noise.Total)
		fmt.Fprintf(w, "Spurious Fact Density: %.2f (%d facts from %d noise inputs)\n",
			r.Noise.SpuriousDensity, r.Noise.SpuriousFacts, r.Noise.Total)
	}

	fmt.Fprintf(w, "ECE (10-bin):          %.4f\n", r.Calibration.ECE)
	fmt.Fprintf(w, "Brier Score:           %.4f\n", r.Calibration.Brier)
	fmt.Fprintf(w, "%s\n", strings.Repeat("-", 58))
	fmt.Fprintf(w, "Composite Score:       %.4f\n", r.Composite)
	fmt.Fprintf(w, "  (0.4*fact_F1 + 0.2*entity_F1 + 0.2*rel_F1 + 0.1*(1-ECE) + 0.1*NRR)\n")
}

func printRow(w io.Writer, name string, s CategoryScore) {
	fmt.Fprintf(w, "%-16s %10.4f %10.4f %10.4f %10d\n",
		name, s.Precision, s.Recall, s.F1, s.Support)
}

// WriteJSON writes the report as JSON to w.
func WriteJSON(w io.Writer, r *Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// --- Conversion helpers ---

func factsToGolden(facts []model.Fact) []GoldenFact {
	out := make([]GoldenFact, len(facts))
	for i := range facts {
		out[i] = GoldenFact{
			FactType:   string(facts[i].FactType),
			Subject:    facts[i].Subject,
			Content:    facts[i].Content,
			Confidence: facts[i].Confidence,
		}
	}
	return out
}

func entitiesToGolden(entities []model.Entity) []GoldenEntity {
	out := make([]GoldenEntity, len(entities))
	for i, e := range entities {
		out[i] = GoldenEntity{
			Name:       e.Name,
			EntityType: string(e.EntityType),
			Aliases:    e.Aliases,
		}
	}
	return out
}

func relsToGolden(rels []model.Relationship) []GoldenRelationship {
	out := make([]GoldenRelationship, len(rels))
	for i, r := range rels {
		out[i] = GoldenRelationship{
			FromEntity:   r.FromEntity,
			ToEntity:     r.ToEntity,
			RelationType: string(r.RelationType),
		}
	}
	return out
}
