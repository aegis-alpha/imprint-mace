package eval

// CheckRegression compares current score against baseline.
// Returns passed=true if the score did not regress beyond threshold.
// A zero baseline is treated as "no meaningful baseline" and always passes.
func CheckRegression(current, baseline, threshold float64) (passed bool, delta float64) {
	delta = current - baseline
	if baseline == 0 {
		return true, delta
	}
	return delta >= -threshold, delta
}
