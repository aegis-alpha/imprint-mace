// Package eval scores Imprint extraction quality against a golden dataset.
//
// The golden dataset is a directory of paired files:
//
//	001-technical-decision.txt   (input transcript chunk)
//	001-technical-decision.json  (expected ExtractionResult)
//
// The eval harness runs extraction on each .txt, compares the result
// to the .json, and reports precision/recall/F1 per category plus
// noise rejection rate, confidence calibration (ECE), and a weighted
// composite score.
package eval

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// GoldenExample is one input-output pair from the golden dataset.
type GoldenExample struct {
	Name     string
	Text     string
	Expected GoldenExpected
}

// GoldenExpected is the expected extraction output for one example.
// Mirrors the LLM's raw JSON shape (no IDs, no timestamps).
type GoldenExpected struct {
	Facts         []GoldenFact         `json:"facts"`
	Entities      []GoldenEntity       `json:"entities"`
	Relationships []GoldenRelationship `json:"relationships"`
	Metadata      *GoldenMetadata      `json:"_metadata,omitempty"`
}

// GoldenFact is a single expected fact.
type GoldenFact struct {
	FactType   string  `json:"fact_type"`
	Subject    string  `json:"subject"`
	Content    string  `json:"content"`
	Confidence float64 `json:"confidence"`
}

// GoldenEntity is a single expected entity.
type GoldenEntity struct {
	Name       string   `json:"name"`
	EntityType string   `json:"entity_type"`
	Aliases    []string `json:"aliases"`
}

// GoldenRelationship is a single expected relationship.
type GoldenRelationship struct {
	FromEntity   string `json:"from_entity"`
	ToEntity     string `json:"to_entity"`
	RelationType string `json:"relation_type"`
}

// GoldenMetadata is optional annotation metadata.
type GoldenMetadata struct {
	Source     string `json:"source,omitempty"`
	Annotator string `json:"annotator,omitempty"`
	Category  string `json:"category,omitempty"`
}

// IsNoise returns true if this example expects empty extraction (no facts,
// no entities, no relationships). Noise examples are used to compute the
// Noise Rejection Rate.
func (e *GoldenExpected) IsNoise() bool {
	return len(e.Facts) == 0 && len(e.Entities) == 0 && len(e.Relationships) == 0
}

// LoadGoldenDir reads all paired .txt/.json files from dir.
// Files are paired by stem: "001-foo.txt" pairs with "001-foo.json".
// Returns an error if any .txt has no matching .json or vice versa.
func LoadGoldenDir(dir string) ([]GoldenExample, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read golden dir %s: %w", dir, err)
	}

	txtFiles := make(map[string]string)
	jsonFiles := make(map[string]string)

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		ext := filepath.Ext(name)
		stem := strings.TrimSuffix(name, ext)

		switch ext {
		case ".txt", ".md":
			txtFiles[stem] = filepath.Join(dir, name)
		case ".json":
			jsonFiles[stem] = filepath.Join(dir, name)
		}
	}

	var stems []string
	for stem := range txtFiles {
		if _, ok := jsonFiles[stem]; ok {
			stems = append(stems, stem)
		}
	}
	sort.Strings(stems)

	if len(stems) == 0 {
		return nil, fmt.Errorf("no paired .txt/.json files found in %s", dir)
	}

	examples := make([]GoldenExample, 0, len(stems))
	for _, stem := range stems {
		text, err := os.ReadFile(txtFiles[stem])
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", txtFiles[stem], err)
		}

		jsonData, err := os.ReadFile(jsonFiles[stem])
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", jsonFiles[stem], err)
		}

		var expected GoldenExpected
		if err := json.Unmarshal(jsonData, &expected); err != nil {
			return nil, fmt.Errorf("parse %s: %w", jsonFiles[stem], err)
		}

		examples = append(examples, GoldenExample{
			Name:     stem,
			Text:     string(text),
			Expected: expected,
		})
	}

	return examples, nil
}
