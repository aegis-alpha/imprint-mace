package eval

import "math"

// --- Per-category scores ---

// CategoryScore holds precision, recall, and F1 for one extraction category.
type CategoryScore struct {
	Precision float64 `json:"precision"`
	Recall    float64 `json:"recall"`
	F1        float64 `json:"f1"`
	Support   int     `json:"support"`
}

func f1(p, r float64) float64 {
	if p+r == 0 {
		return 0
	}
	return 2 * p * r / (p + r)
}

// --- Fact scoring (CaRB-style asymmetric) ---

// ScoreFacts computes precision, recall, and F1 for fact extraction.
// Uses greedy best-match for precision and max-match for recall.
// matchThreshold is the minimum FactMatchScore to count as a match.
func ScoreFacts(system, gold []GoldenFact, matchThreshold float64) CategoryScore {
	if len(gold) == 0 && len(system) == 0 {
		return CategoryScore{Precision: 1, Recall: 1, F1: 1, Support: 0}
	}
	if len(gold) == 0 {
		return CategoryScore{Precision: 0, Recall: 1, F1: 0, Support: 0}
	}
	if len(system) == 0 {
		return CategoryScore{Precision: 1, Recall: 0, F1: 0, Support: len(gold)}
	}

	matrix := make([][]float64, len(system))
	for i := range system {
		matrix[i] = make([]float64, len(gold))
		for j := range gold {
			matrix[i][j] = FactMatchScore(system[i], gold[j])
		}
	}

	recall := maxMatchRecall(matrix, matchThreshold)
	precision := greedyMatchPrecision(matrix, matchThreshold)

	return CategoryScore{
		Precision: precision,
		Recall:    recall,
		F1:        f1(precision, recall),
		Support:   len(gold),
	}
}

// maxMatchRecall: for each gold item, take the max score across system items.
// A gold item is "recalled" if any system item scores above threshold.
func maxMatchRecall(matrix [][]float64, threshold float64) float64 {
	nGold := len(matrix[0])
	if nGold == 0 {
		return 1.0
	}
	matched := 0
	for j := 0; j < nGold; j++ {
		bestScore := 0.0
		for i := range matrix {
			if matrix[i][j] > bestScore {
				bestScore = matrix[i][j]
			}
		}
		if bestScore >= threshold {
			matched++
		}
	}
	return float64(matched) / float64(nGold)
}

// greedyMatchPrecision: match system items to gold items one-to-one.
// Greedy: pick the highest-scoring (sys, gold) pair, assign it, remove both,
// repeat. A system item is "precise" if it matched above threshold.
func greedyMatchPrecision(matrix [][]float64, threshold float64) float64 {
	nSys := len(matrix)
	if nSys == 0 {
		return 1.0
	}

	usedSys := make([]bool, nSys)
	usedGold := make([]bool, len(matrix[0]))
	matched := 0

	for k := 0; k < nSys; k++ {
		bestI, bestJ := -1, -1
		bestScore := 0.0
		for i := range matrix {
			if usedSys[i] {
				continue
			}
			for j := range matrix[i] {
				if usedGold[j] {
					continue
				}
				if matrix[i][j] > bestScore {
					bestScore = matrix[i][j]
					bestI = i
					bestJ = j
				}
			}
		}
		if bestI < 0 || bestScore < threshold {
			break
		}
		usedSys[bestI] = true
		usedGold[bestJ] = true
		matched++
	}

	return float64(matched) / float64(nSys)
}

// --- Entity scoring ---

// ScoreEntities computes precision, recall, and F1 for entity extraction.
func ScoreEntities(system, gold []GoldenEntity) CategoryScore {
	if len(gold) == 0 && len(system) == 0 {
		return CategoryScore{Precision: 1, Recall: 1, F1: 1, Support: 0}
	}
	if len(gold) == 0 {
		return CategoryScore{Precision: 0, Recall: 1, F1: 0, Support: 0}
	}
	if len(system) == 0 {
		return CategoryScore{Precision: 1, Recall: 0, F1: 0, Support: len(gold)}
	}

	goldMatched := make([]bool, len(gold))
	sysMatched := 0

	for i := range system {
		for j := range gold {
			if goldMatched[j] {
				continue
			}
			if EntityMatch(system[i], gold[j]) {
				goldMatched[j] = true
				sysMatched++
				break
			}
		}
	}

	recalled := 0
	for _, m := range goldMatched {
		if m {
			recalled++
		}
	}

	precision := float64(sysMatched) / float64(len(system))
	recall := float64(recalled) / float64(len(gold))

	return CategoryScore{
		Precision: precision,
		Recall:    recall,
		F1:        f1(precision, recall),
		Support:   len(gold),
	}
}

// --- Relationship scoring ---

// ScoreRelationships computes precision, recall, and F1 for relationship extraction.
func ScoreRelationships(system, gold []GoldenRelationship, sysEntities, goldEntities []GoldenEntity) CategoryScore {
	if len(gold) == 0 && len(system) == 0 {
		return CategoryScore{Precision: 1, Recall: 1, F1: 1, Support: 0}
	}
	if len(gold) == 0 {
		return CategoryScore{Precision: 0, Recall: 1, F1: 0, Support: 0}
	}
	if len(system) == 0 {
		return CategoryScore{Precision: 1, Recall: 0, F1: 0, Support: len(gold)}
	}

	goldMatched := make([]bool, len(gold))
	sysMatched := 0

	for i := range system {
		for j := range gold {
			if goldMatched[j] {
				continue
			}
			if RelationshipMatch(system[i], gold[j], sysEntities, goldEntities) {
				goldMatched[j] = true
				sysMatched++
				break
			}
		}
	}

	recalled := 0
	for _, m := range goldMatched {
		if m {
			recalled++
		}
	}

	precision := float64(sysMatched) / float64(len(system))
	recall := float64(recalled) / float64(len(gold))

	return CategoryScore{
		Precision: precision,
		Recall:    recall,
		F1:        f1(precision, recall),
		Support:   len(gold),
	}
}

// --- Noise Rejection Rate ---

// NoiseResult holds noise rejection metrics.
type NoiseResult struct {
	Total             int     `json:"total"`
	Rejected          int     `json:"rejected"`
	NRR               float64 `json:"nrr"`
	SpuriousFacts     int     `json:"spurious_facts"`
	SpuriousDensity   float64 `json:"spurious_density"`
}

// ScoreNoise computes the Noise Rejection Rate.
// noiseResults is a slice of extraction results for noise-only examples.
// Each element is the number of facts extracted from that noise input.
func ScoreNoise(factsPerNoiseInput []int) NoiseResult {
	if len(factsPerNoiseInput) == 0 {
		return NoiseResult{NRR: 1.0}
	}

	rejected := 0
	totalSpurious := 0
	for _, n := range factsPerNoiseInput {
		if n == 0 {
			rejected++
		}
		totalSpurious += n
	}

	total := len(factsPerNoiseInput)
	return NoiseResult{
		Total:           total,
		Rejected:        rejected,
		NRR:             float64(rejected) / float64(total),
		SpuriousFacts:   totalSpurious,
		SpuriousDensity: float64(totalSpurious) / float64(total),
	}
}

// --- Expected Calibration Error ---

// CalibrationResult holds confidence calibration metrics.
type CalibrationResult struct {
	ECE        float64          `json:"ece"`
	Brier      float64          `json:"brier"`
	BinCount   int              `json:"bin_count"`
	Bins       []CalibrationBin `json:"bins,omitempty"`
}

// CalibrationBin is one bin of the reliability diagram.
type CalibrationBin struct {
	LowerBound float64 `json:"lower_bound"`
	UpperBound float64 `json:"upper_bound"`
	Count      int     `json:"count"`
	Accuracy   float64 `json:"accuracy"`
	AvgConf    float64 `json:"avg_confidence"`
}

// CalibrationSample is one fact with its confidence and whether it was correct.
type CalibrationSample struct {
	Confidence float64
	Correct    bool
}

// ScoreCalibration computes ECE and Brier score from a set of samples.
// Uses 10 equal-width bins.
func ScoreCalibration(samples []CalibrationSample) CalibrationResult {
	if len(samples) == 0 {
		return CalibrationResult{BinCount: 10}
	}

	const nBins = 10
	bins := make([]CalibrationBin, nBins)
	for i := range bins {
		bins[i].LowerBound = float64(i) / float64(nBins)
		bins[i].UpperBound = float64(i+1) / float64(nBins)
	}

	for _, s := range samples {
		idx := int(s.Confidence * float64(nBins))
		if idx >= nBins {
			idx = nBins - 1
		}
		if idx < 0 {
			idx = 0
		}
		bins[idx].Count++
		bins[idx].AvgConf += s.Confidence
		if s.Correct {
			bins[idx].Accuracy++
		}
	}

	var ece float64
	n := float64(len(samples))
	for i := range bins {
		if bins[i].Count == 0 {
			continue
		}
		cnt := float64(bins[i].Count)
		bins[i].AvgConf /= cnt
		bins[i].Accuracy /= cnt
		ece += (cnt / n) * math.Abs(bins[i].Accuracy-bins[i].AvgConf)
	}

	var brier float64
	for _, s := range samples {
		correct := 0.0
		if s.Correct {
			correct = 1.0
		}
		brier += (s.Confidence - correct) * (s.Confidence - correct)
	}
	brier /= n

	return CalibrationResult{
		ECE:      ece,
		Brier:    brier,
		BinCount: nBins,
		Bins:     bins,
	}
}

// --- Composite score ---

// CompositeScore computes the weighted composite score.
// Weights: 0.4*factF1 + 0.2*entityF1 + 0.2*relF1 + 0.1*(1-ECE) + 0.1*NRR
func CompositeScore(factF1, entityF1, relF1, ece, nrr float64) float64 {
	return 0.4*factF1 + 0.2*entityF1 + 0.2*relF1 + 0.1*(1-ece) + 0.1*nrr
}
