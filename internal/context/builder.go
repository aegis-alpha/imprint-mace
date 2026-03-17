package context

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/aegis-alpha/imprint-mace/internal/db"
	"github.com/aegis-alpha/imprint-mace/internal/model"
	"github.com/aegis-alpha/imprint-mace/internal/provider"
)

type BuilderConfig struct {
	RecentHours        int
	MaxFacts           int
	IncludePreferences bool
}

type Builder struct {
	store         db.Store
	embedder      provider.Embedder
	transcriptDir string
	logger        *slog.Logger
	config        BuilderConfig
}

func New(store db.Store, embedder provider.Embedder, transcriptDir string, cfg BuilderConfig, logger *slog.Logger) *Builder {
	return &Builder{
		store:         store,
		embedder:      embedder,
		transcriptDir: transcriptDir,
		logger:        logger,
		config:        cfg,
	}
}

// Build assembles relevant context from the knowledge base without calling an LLM.
// If hint is non-empty, vector and text search are used to find relevant facts.
// If hint is empty, only preferences and recent facts are returned.
// Returns empty string when no facts are found (not an error).
func (b *Builder) Build(ctx context.Context, hint string) (string, error) {
	seen := map[string]bool{}
	var relevant []model.Fact
	var preferences []model.Fact
	var recent []model.Fact

	if b.embedder != nil && hint != "" {
		embedding, err := b.embedder.Embed(ctx, hint)
		if err != nil {
			b.logger.Warn("embed hint failed, skipping vector search", "error", err)
		} else {
			results, err := b.store.SearchByVector(ctx, embedding, b.config.MaxFacts)
			if err != nil {
				b.logger.Warn("vector search failed", "error", err)
			} else {
				for i := range results {
					if !seen[results[i].Fact.ID] {
						seen[results[i].Fact.ID] = true
						relevant = append(relevant, results[i].Fact)
					}
				}
			}
		}
	}

	if hint != "" {
		sanitized := sanitizeFTS5Query(hint)
		if sanitized != "" {
			results, err := b.store.SearchByText(ctx, sanitized, 10)
			if err != nil {
				b.logger.Warn("text search failed", "error", err)
			} else {
				for i := range results {
					if !seen[results[i].Fact.ID] {
						seen[results[i].Fact.ID] = true
						relevant = append(relevant, results[i].Fact)
					}
				}
			}
		}
	}

	if b.config.IncludePreferences {
		prefs, err := b.store.ListFacts(ctx, db.FactFilter{FactType: "preference"})
		if err != nil {
			b.logger.Warn("list preferences failed", "error", err)
		} else {
			preferences = prefs
		}
	}

	cutoff := time.Now().UTC().Add(-time.Duration(b.config.RecentHours) * time.Hour)
	recentFacts, err := b.store.ListFacts(ctx, db.FactFilter{CreatedAfter: &cutoff})
	if err != nil {
		b.logger.Warn("list recent facts failed", "error", err)
	} else {
		recent = recentFacts
	}

	return formatContext(relevant, preferences, recent, seen), nil
}

func formatContext(relevant, preferences, recent []model.Fact, seen map[string]bool) string {
	var sections []string

	if len(relevant) > 0 {
		var lines []string
		for i := range relevant {
			lines = append(lines, formatFact(&relevant[i], true))
		}
		sections = append(sections, "## Relevant Context\n"+strings.Join(lines, "\n"))
	}

	if len(preferences) > 0 {
		var lines []string
		for i := range preferences {
			lines = append(lines, fmt.Sprintf("- %s: %s", preferences[i].Subject, preferences[i].Content))
		}
		sections = append(sections, "## Preferences\n"+strings.Join(lines, "\n"))
	}

	if len(recent) > 0 {
		dedupRecent := dedupAgainst(recent, seen)
		if len(dedupRecent) > 0 {
			var lines []string
			for i := range dedupRecent {
				lines = append(lines, formatFact(&dedupRecent[i], false))
			}
			sections = append(sections, "## Recent\n"+strings.Join(lines, "\n"))
		}
	}

	return strings.Join(sections, "\n\n")
}

func formatFact(f *model.Fact, withConfidence bool) string {
	date := f.CreatedAt.Format("2006-01-02")
	if withConfidence {
		return fmt.Sprintf("- [%s] %s: %s (confidence=%.2f, %s)", f.FactType, f.Subject, f.Content, f.Confidence, date)
	}
	return fmt.Sprintf("- [%s] %s: %s (%s)", f.FactType, f.Subject, f.Content, date)
}

func dedupAgainst(facts []model.Fact, seen map[string]bool) []model.Fact {
	var result []model.Fact
	for i := range facts {
		if !seen[facts[i].ID] {
			seen[facts[i].ID] = true
			result = append(result, facts[i])
		}
	}
	return result
}

// sanitizeFTS5Query removes characters that are special in FTS5 syntax.
// Duplicated from internal/query/ to avoid coupling between packages.
func sanitizeFTS5Query(q string) string {
	replacer := strings.NewReplacer(
		"?", "", "!", "", ".", "", ",", "", ";", "",
		":", "", "'", "", "\"", "", "(", "", ")", "",
		"*", "", "+", "", "-", "", "^", "",
		"{", "", "}", "", "[", "", "]", "",
	)
	cleaned := replacer.Replace(q)
	words := strings.Fields(cleaned)
	if len(words) == 0 {
		return ""
	}
	return strings.Join(words, " ")
}
