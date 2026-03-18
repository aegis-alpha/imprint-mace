package transcript

import (
	"fmt"
	"os"
	"strings"
)

// ReadContext reads lines [lineStart, lineEnd] from filePath (1-based, inclusive)
// and includes contextLines extra lines before and after.
func ReadContext(filePath string, lineStart, lineEnd, contextLines int) (string, error) {
	data, err := os.ReadFile(filePath) //nolint:gosec // path from trusted DB, not user input
	if err != nil {
		return "", fmt.Errorf("read transcript: %w", err)
	}

	lines := strings.Split(string(data), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	start := lineStart - 1 - contextLines
	if start < 0 {
		start = 0
	}
	end := lineEnd + contextLines
	if end > len(lines) {
		end = len(lines)
	}
	if start >= len(lines) {
		return "", nil
	}

	return strings.Join(lines[start:end], "\n") + "\n", nil
}
