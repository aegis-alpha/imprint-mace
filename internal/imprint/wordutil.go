package imprint

import "strings"

// jaccardWords returns word-level Jaccard similarity between two strings (lowercased, fields split).
func jaccardWords(a, b string) float64 {
	setA := wordSet(a)
	setB := wordSet(b)
	if len(setA) == 0 && len(setB) == 0 {
		return 0
	}
	inter := 0
	for w := range setA {
		if setB[w] {
			inter++
		}
	}
	union := len(setA) + len(setB) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

func wordSet(s string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(strings.ToLower(s)) {
		m[w] = true
	}
	return m
}

// subjectMatch reports whether two subject strings overlap enough for contradiction pairing.
func subjectMatch(a, b string, minJaccard float64) bool {
	return jaccardWords(a, b) >= minJaccard
}
