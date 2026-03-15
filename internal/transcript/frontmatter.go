package transcript

import (
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Frontmatter struct {
	Source       string   `yaml:"source"`
	Session      string   `yaml:"session"`
	DateRaw      string   `yaml:"date"`
	Participants []string `yaml:"participants"`
	Topic        string   `yaml:"topic"`

	Date *time.Time `yaml:"-"`
}

// ParseFrontmatter extracts YAML frontmatter from annotated markdown content.
// Returns parsed metadata, body text (without frontmatter), and error.
// If no frontmatter is present, returns nil metadata and the original content.
func ParseFrontmatter(content string) (*Frontmatter, string, error) {
	if !strings.HasPrefix(content, "---\n") {
		return nil, content, nil
	}

	end := strings.Index(content[4:], "\n---")
	if end < 0 {
		return nil, content, nil
	}
	yamlBlock := content[4 : 4+end]
	// Skip past "\n---\n" (4 bytes) to get the body after the closing delimiter.
	bodyStart := 4 + end + 4
	if bodyStart < len(content) && content[bodyStart] == '\n' {
		bodyStart++
	}
	body := content[bodyStart:]

	var fm Frontmatter
	if err := yaml.Unmarshal([]byte(yamlBlock), &fm); err != nil {
		return nil, "", fmt.Errorf("parse frontmatter YAML: %w", err)
	}

	if fm.DateRaw != "" {
		for _, layout := range []string{
			time.RFC3339,
			"2006-01-02",
		} {
			if t, err := time.Parse(layout, fm.DateRaw); err == nil {
				fm.Date = &t
				break
			}
		}
	}

	return &fm, body, nil
}
