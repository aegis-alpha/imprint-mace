package transcript

import (
	"errors"
	"path/filepath"

	"github.com/aegis-alpha/imprint-mace/internal/model"
)

// GetSourceContext reads the original transcript file and returns the
// lines that a fact was extracted from, based on its Source.LineRange.
func GetSourceContext(fact model.Fact, transcriptDir string) (string, error) {
	if fact.Source.LineRange == nil {
		return "", errors.New("no line reference")
	}
	path := filepath.Join(transcriptDir, fact.Source.TranscriptFile)
	return ReadContext(path, fact.Source.LineRange[0], fact.Source.LineRange[1], 0)
}
