// Package extraction converts transcript text into structured facts,
// entities, and relationships by sending text to an LLM with a
// type-aware prompt template (D18).
package extraction

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"text/template"
	"time"

	"github.com/aegis-alpha/imprint-mace/internal/config"
	"github.com/aegis-alpha/imprint-mace/internal/db"
	"github.com/aegis-alpha/imprint-mace/internal/model"
	"github.com/aegis-alpha/imprint-mace/internal/provider"
)

// Sender sends a request to an LLM. provider.Chain implements this.
type Sender interface {
	Send(ctx context.Context, req provider.Request) (*provider.Response, error)
}

// ExtractionLogger writes extraction results to persistent storage (D19).
// If nil, extraction logging is skipped.
type ExtractionLogger interface {
	CreateExtractionLog(ctx context.Context, l *db.ExtractionLog) error
}

// Extractor sends transcript text to an LLM and parses the structured response.
type Extractor struct {
	sender   Sender
	prompt   string
	logger   *slog.Logger
	extLog   ExtractionLogger
}

// New creates an Extractor. It reads the prompt template from disk and
// renders it with the given types (D18). The rendered prompt is reused
// for every Extract call.
func New(sender Sender, templatePath string, types config.TypesConfig, logger *slog.Logger) (*Extractor, error) {
	raw, err := os.ReadFile(templatePath) //nolint:gosec // template path from config, not user input
	if err != nil {
		return nil, fmt.Errorf("read prompt template %s: %w", templatePath, err)
	}

	tmpl, err := template.New("extraction").Parse(string(raw))
	if err != nil {
		return nil, fmt.Errorf("parse prompt template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, types); err != nil {
		return nil, fmt.Errorf("render prompt template: %w", err)
	}

	return &Extractor{
		sender: sender,
		prompt: buf.String(),
		logger: logger,
	}, nil
}

// SetExtractionLogger attaches a persistent log writer (D19).
// When set, every Extract call writes to extraction_log.
func (e *Extractor) SetExtractionLogger(l ExtractionLogger) {
	e.extLog = l
}

// RenderedPrompt returns the rendered system prompt (for testing/debugging).
func (e *Extractor) RenderedPrompt() string {
	return e.prompt
}

// rawExtractionResult is the JSON shape the LLM returns.
// Fields are semantic only -- no IDs, timestamps, or source info.
type rawExtractionResult struct {
	Facts         []rawFact         `json:"facts"`
	Entities      []rawEntity       `json:"entities"`
	Relationships []rawRelationship `json:"relationships"`
}

type rawFact struct {
	FactType   string     `json:"fact_type"`
	Subject    string     `json:"subject"`
	Content    string     `json:"content"`
	Confidence float64    `json:"confidence"`
	Validity   rawValidity `json:"validity"`
}

type rawValidity struct {
	ValidFrom  *string `json:"valid_from"`
	ValidUntil *string `json:"valid_until"`
}

type rawEntity struct {
	Name       string   `json:"name"`
	EntityType string   `json:"entity_type"`
	Aliases    []string `json:"aliases"`
}

type rawRelationship struct {
	FromEntity   string `json:"from_entity"`
	ToEntity     string `json:"to_entity"`
	RelationType string `json:"relation_type"`
}

// Extract sends text to the LLM and returns structured extraction results.
// It generates IDs (ULID), sets timestamps, and attaches source info.
func (e *Extractor) Extract(ctx context.Context, text string, sourceFile string) (*model.ExtractionResult, error) {
	start := time.Now()

	resp, err := e.sender.Send(ctx, provider.Request{
		SystemPrompt: e.prompt,
		UserPrompt:   text,
		MaxTokens:    4096,
	})
	if err != nil {
		duration := time.Since(start)
		e.logger.Error("extraction provider failed",
			"source", sourceFile,
			"duration_ms", duration.Milliseconds(),
			"error", err,
		)
		e.writeLog(ctx, "", "", len(text), 0, duration.Milliseconds(),
			false, 0, 0, 0, "provider_error", err.Error())
		return nil, fmt.Errorf("provider: %w", err)
	}

	duration := time.Since(start)
	content := stripMarkdownFences(resp.Content)

	var raw rawExtractionResult
	if err := json.Unmarshal([]byte(content), &raw); err != nil {
		e.logger.Error("extraction JSON parse failed",
			"source", sourceFile,
			"provider", resp.ProviderName,
			"model", resp.Model,
			"duration_ms", duration.Milliseconds(),
			"tokens", resp.TokensUsed,
			"raw_length", len(resp.Content),
			"error", err,
		)
		e.writeLog(ctx, resp.ProviderName, resp.Model, len(text), resp.TokensUsed,
			duration.Milliseconds(), false, 0, 0, 0, "parse_error", err.Error())
		return nil, fmt.Errorf("parse LLM response: %w", err)
	}

	now := time.Now()
	result := e.hydrate(raw, sourceFile, now)

	e.logger.Info("extraction complete",
		"source", sourceFile,
		"provider", resp.ProviderName,
		"model", resp.Model,
		"duration_ms", duration.Milliseconds(),
		"tokens", resp.TokensUsed,
		"facts", len(result.Facts),
		"entities", len(result.Entities),
		"relationships", len(result.Relationships),
	)
	logID := e.writeLog(ctx, resp.ProviderName, resp.Model, len(text), resp.TokensUsed,
		duration.Milliseconds(), true, len(result.Facts), len(result.Entities),
		len(result.Relationships), "", "")
	result.ExtractionLogID = logID

	return result, nil
}

func (e *Extractor) writeLog(ctx context.Context, providerName, modelName string,
	inputLen, tokens int, durationMs int64, success bool,
	facts, entities, rels int, errType, errMsg string) string {
	if e.extLog == nil {
		return ""
	}
	l := &db.ExtractionLog{
		ID:                 db.NewID(),
		ProviderName:       providerName,
		Model:              modelName,
		InputLength:        inputLen,
		TokensUsed:         tokens,
		DurationMs:         durationMs,
		Success:            success,
		FactsCount:         facts,
		EntitiesCount:      entities,
		RelationshipsCount: rels,
		ErrorType:          errType,
		ErrorMessage:       errMsg,
		CreatedAt:          time.Now(),
	}
	if err := e.extLog.CreateExtractionLog(ctx, l); err != nil {
		e.logger.Error("failed to write extraction log", "error", err)
		return ""
	}
	return l.ID
}

// hydrate converts raw LLM output into fully populated model types
// with generated IDs, timestamps, and source info.
func (e *Extractor) hydrate(raw rawExtractionResult, sourceFile string, now time.Time) *model.ExtractionResult {
	result := &model.ExtractionResult{}

	for _, rf := range raw.Facts {
		f := model.Fact{
			ID:         db.NewID(),
			Source:     model.Source{TranscriptFile: sourceFile},
			FactType:   model.FactType(rf.FactType),
			Subject:    rf.Subject,
			Content:    rf.Content,
			Confidence: rf.Confidence,
			CreatedAt:  now,
		}
		if rf.Validity.ValidFrom != nil {
			if t, err := time.Parse(time.RFC3339, *rf.Validity.ValidFrom); err == nil {
				f.Validity.ValidFrom = &t
			}
		}
		if rf.Validity.ValidUntil != nil {
			if t, err := time.Parse(time.RFC3339, *rf.Validity.ValidUntil); err == nil {
				f.Validity.ValidUntil = &t
			}
		}
		result.Facts = append(result.Facts, f)
	}

	for _, re := range raw.Entities {
		result.Entities = append(result.Entities, model.Entity{
			ID:         db.NewID(),
			Name:       re.Name,
			EntityType: model.EntityType(re.EntityType),
			Aliases:    re.Aliases,
			CreatedAt:  now,
		})
	}

	for _, rr := range raw.Relationships {
		result.Relationships = append(result.Relationships, model.Relationship{
			ID:           db.NewID(),
			FromEntity:   rr.FromEntity,
			ToEntity:     rr.ToEntity,
			RelationType: model.RelationType(rr.RelationType),
			CreatedAt:    now,
		})
	}

	return result
}

// stripMarkdownFences removes ```json ... ``` wrapping that some LLMs add
// despite being told not to.
func stripMarkdownFences(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		if idx := strings.Index(s, "\n"); idx != -1 {
			s = s[idx+1:]
		}
	}
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}
