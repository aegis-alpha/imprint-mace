package db

import (
	"fmt"
	"strings"
)

// Lint result row types for imprint lint (BVP-368).

// LintStaleFact is a fact past valid_until with no superseding fact.
type LintStaleFact struct {
	ID         string
	Subject    string
	Content    string
	ValidUntil string
}

// LintOrphanEntity has no relationships and no fact uses its name as subject.
type LintOrphanEntity struct {
	ID         string
	Name       string
	EntityType string
}

// LintBrokenSupersedeChain is a fact whose superseded_by target is missing.
type LintBrokenSupersedeChain struct {
	ID           string
	Subject      string
	SupersededBy string
}

// LintEntityDedupPair is a possible duplicate entity pair.
type LintEntityDedupPair struct {
	ID1   string
	Name1 string
	ID2   string
	Name2 string
	Kind  string // "exact" or "substring"
}

// LintMissingEmbeddingByType counts facts without usable embeddings per fact_type.
type LintMissingEmbeddingByType struct {
	FactType string
	Count    int
}

// AllLintChecks is the default ordered list of lint check names for --check filtering.
var AllLintChecks = []string{
	"chains",
	"stale",
	"dedup",
	"embeddings",
	"orphans",
	"sources",
	"consolidation",
}

// NormalizeLintChecks parses a comma-separated --check value against allowed names.
// Empty filter returns a copy of allChecks (typically AllLintChecks).
func NormalizeLintChecks(filter string, allChecks []string) ([]string, error) {
	if len(allChecks) == 0 {
		return nil, fmt.Errorf("lint: empty check list")
	}
	allowed := make(map[string]struct{}, len(allChecks))
	for _, n := range allChecks {
		allowed[n] = struct{}{}
	}
	if strings.TrimSpace(filter) == "" {
		out := make([]string, len(allChecks))
		copy(out, allChecks)
		return out, nil
	}
	var out []string
	for _, p := range strings.Split(filter, ",") {
		name := strings.TrimSpace(strings.ToLower(p))
		if name == "" {
			continue
		}
		if _, ok := allowed[name]; !ok {
			return nil, fmt.Errorf("lint: unknown check %q", name)
		}
		out = append(out, name)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("lint: no checks after parsing --check")
	}
	return dedupePreserveOrder(out), nil
}

func dedupePreserveOrder(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	var out []string
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
