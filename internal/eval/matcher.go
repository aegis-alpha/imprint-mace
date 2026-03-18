package eval

import (
	"math"
	"strings"
	"unicode"
)

// --- Name normalization ---

// normalizeName lowercases, collapses whitespace, and strips leading
// articles ("the", "a", "an") for entity name comparison.
func normalizeName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = collapseWhitespace(s)
	for _, prefix := range []string{"the ", "a ", "an "} {
		if strings.HasPrefix(s, prefix) {
			s = s[len(prefix):]
			break
		}
	}
	return s
}

func collapseWhitespace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prev := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			if !prev {
				b.WriteRune(' ')
			}
			prev = true
		} else {
			b.WriteRune(r)
			prev = false
		}
	}
	return strings.TrimSpace(b.String())
}

// --- Fact matching ---

// FactMatchScore returns a score in [0, 1] for how well system matches gold.
// Uses composite matching: type exact, subject fuzzy, content Jaccard.
func FactMatchScore(sys, gold GoldenFact) float64 {
	if strings.ToLower(sys.FactType) != strings.ToLower(gold.FactType) {
		return 0
	}

	subjectSim := subjectSimilarity(sys.Subject, gold.Subject)
	if subjectSim < 0.3 {
		return 0
	}

	contentSim := jaccardSimilarity(sys.Content, gold.Content)

	return 0.3*subjectSim + 0.7*contentSim
}

// FactMatchScoreWithEmbeddings uses embedding cosine similarity for content
// instead of Jaccard. Returns 0 if type doesn't match.
func FactMatchScoreWithEmbeddings(sys, gold GoldenFact, sysEmb, goldEmb []float32) float64 {
	if strings.ToLower(sys.FactType) != strings.ToLower(gold.FactType) {
		return 0
	}

	subjectSim := subjectSimilarity(sys.Subject, gold.Subject)
	if subjectSim < 0.3 {
		return 0
	}

	contentSim := CosineSimilarity(sysEmb, goldEmb)
	if contentSim < 0 {
		contentSim = 0
	}

	return 0.3*subjectSim + 0.7*contentSim
}

func subjectSimilarity(a, b string) float64 {
	na := normalizeName(a)
	nb := normalizeName(b)
	if na == nb {
		return 1.0
	}
	if strings.Contains(na, nb) || strings.Contains(nb, na) {
		return 0.8
	}
	return jaccardSimilarity(a, b)
}

// --- Entity matching ---

// EntityMatch returns true if the system entity matches the gold entity
// using alias-aware, case-insensitive name comparison.
func EntityMatch(sys, gold GoldenEntity) bool {
	sysName := normalizeName(sys.Name)
	goldName := normalizeName(gold.Name)

	if sysName == goldName {
		return true
	}

	for _, alias := range gold.Aliases {
		if normalizeName(alias) == sysName {
			return true
		}
	}

	for _, alias := range sys.Aliases {
		if normalizeName(alias) == goldName {
			return true
		}
	}

	return false
}

// EntityTypeMatch returns true if the entity types match (case-insensitive).
func EntityTypeMatch(sys, gold GoldenEntity) bool {
	return strings.EqualFold(sys.EntityType, gold.EntityType)
}

// --- Relationship matching ---

// RelationshipMatch returns true if the system relationship matches the gold
// relationship. Entity names are matched via alias-aware comparison against
// the provided entity lists.
func RelationshipMatch(sys, gold GoldenRelationship, sysEntities, goldEntities []GoldenEntity) bool {
	if !strings.EqualFold(sys.RelationType, gold.RelationType) {
		return false
	}

	fromMatch := entityNamesMatch(sys.FromEntity, gold.FromEntity, sysEntities, goldEntities)
	toMatch := entityNamesMatch(sys.ToEntity, gold.ToEntity, sysEntities, goldEntities)

	return fromMatch && toMatch
}

func entityNamesMatch(sysName, goldName string, sysEntities, goldEntities []GoldenEntity) bool {
	if normalizeName(sysName) == normalizeName(goldName) {
		return true
	}

	var sysEntity, goldEntity *GoldenEntity
	for i := range sysEntities {
		if normalizeName(sysEntities[i].Name) == normalizeName(sysName) {
			sysEntity = &sysEntities[i]
			break
		}
	}
	for i := range goldEntities {
		if normalizeName(goldEntities[i].Name) == normalizeName(goldName) {
			goldEntity = &goldEntities[i]
			break
		}
	}

	if sysEntity != nil && goldEntity != nil {
		return EntityMatch(*sysEntity, *goldEntity)
	}

	return false
}

// --- Text similarity ---

// jaccardSimilarity computes word-level Jaccard similarity between two strings.
func jaccardSimilarity(a, b string) float64 {
	wordsA := tokenize(a)
	wordsB := tokenize(b)

	if len(wordsA) == 0 && len(wordsB) == 0 {
		return 1.0
	}
	if len(wordsA) == 0 || len(wordsB) == 0 {
		return 0.0
	}

	setA := make(map[string]bool, len(wordsA))
	for _, w := range wordsA {
		setA[w] = true
	}
	setB := make(map[string]bool, len(wordsB))
	for _, w := range wordsB {
		setB[w] = true
	}

	intersection := 0
	for w := range setA {
		if setB[w] {
			intersection++
		}
	}

	union := len(setA) + len(setB) - intersection
	if union == 0 {
		return 1.0
	}

	return float64(intersection) / float64(union)
}

var stopwords = map[string]bool{
	"a": true, "an": true, "the": true, "is": true, "are": true,
	"was": true, "were": true, "be": true, "been": true, "being": true,
	"have": true, "has": true, "had": true, "do": true, "does": true,
	"did": true, "will": true, "would": true, "could": true, "should": true,
	"may": true, "might": true, "shall": true, "can": true,
	"for": true, "and": true, "but": true, "or": true, "nor": true,
	"not": true, "so": true, "yet": true, "to": true, "of": true,
	"in": true, "on": true, "at": true, "by": true, "with": true,
	"from": true, "as": true, "into": true, "that": true, "this": true,
	"it": true, "its": true,
}

func tokenize(s string) []string {
	s = strings.ToLower(s)
	words := strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' && r != '_'
	})
	var filtered []string
	for _, w := range words {
		if !stopwords[w] {
			filtered = append(filtered, w)
		}
	}
	return filtered
}

// --- Vector math ---

// CosineSimilarity computes cosine similarity between two vectors.
// Returns 0 if either vector is zero-length or they differ in length.
func CosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	denom := math.Sqrt(normA) * math.Sqrt(normB)
	if denom == 0 {
		return 0
	}
	return dot / denom
}
