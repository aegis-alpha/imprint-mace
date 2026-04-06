package imprint

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"text/template"
	"time"

	"github.com/aegis-alpha/imprint-mace/internal/db"
	"github.com/aegis-alpha/imprint-mace/internal/extraction"
	"github.com/aegis-alpha/imprint-mace/internal/model"
	"github.com/aegis-alpha/imprint-mace/internal/provider"
)

const dedupSupersedeReasonPrefix = "dedup:"

// mergeLLMSystemPrompt is the fixed system line for dedup merge classification (user message is the rendered template).
const mergeLLMSystemPrompt = "You classify pairs of similar knowledge-base facts. Respond with one JSON object only, no markdown fences."

type mergePromptData struct {
	ExistingFact model.Fact
	NewFact      model.Fact
}

type rawDedupMergeDecision struct {
	Action        string `json:"action"`
	Reason        string `json:"reason"`
	MergedContent string `json:"merged_content"`
}

type dedupMergeDecision struct {
	Action        string
	Reason        string
	MergedContent string
}

func formatDedupReason(kind, rationale string) string {
	k := strings.TrimSpace(strings.ToLower(kind))
	r := strings.TrimSpace(rationale)
	if r == "" {
		return dedupSupersedeReasonPrefix + k + ":unspecified"
	}
	return dedupSupersedeReasonPrefix + k + ":" + r
}

func renderMergeUserPrompt(tmplText string, existing, newF model.Fact) (string, error) {
	tmpl, err := template.New("merge").Parse(tmplText)
	if err != nil {
		return "", fmt.Errorf("parse merge prompt template: %w", err)
	}
	var buf bytes.Buffer
	data := mergePromptData{ExistingFact: existing, NewFact: newF}
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("execute merge prompt template: %w", err)
	}
	return buf.String(), nil
}

// runDedupMergeClassify calls the LLM for skip | supersede | merge on one pair.
func runDedupMergeClassify(ctx context.Context, sender extraction.Sender, systemPrompt, templateText string, existing, newF model.Fact) (dedupMergeDecision, string, int, int64, error) {
	var zero dedupMergeDecision
	user, err := renderMergeUserPrompt(templateText, existing, newF)
	if err != nil {
		return zero, "", 0, 0, err
	}
	start := time.Now()
	resp, err := sender.Send(ctx, provider.Request{
		SystemPrompt: strings.TrimSpace(systemPrompt),
		UserPrompt:   user,
		MaxTokens:    1024,
		Temperature:  0,
	})
	if err != nil {
		return zero, "", 0, 0, err
	}
	durationMs := time.Since(start).Milliseconds()
	content := extractJSONObject(stripMarkdownFences(resp.Content))
	var raw rawDedupMergeDecision
	if err := json.Unmarshal([]byte(content), &raw); err != nil {
		return zero, resp.Model, resp.TokensUsed, durationMs, fmt.Errorf("parse dedup merge JSON: %w", err)
	}
	action := strings.ToLower(strings.TrimSpace(raw.Action))
	dec := dedupMergeDecision{
		Action:        action,
		Reason:        strings.TrimSpace(raw.Reason),
		MergedContent: strings.TrimSpace(raw.MergedContent),
	}
	switch dec.Action {
	case "skip", "supersede":
		return dec, resp.Model, resp.TokensUsed, durationMs, nil
	case "merge":
		if dec.MergedContent == "" {
			return zero, resp.Model, resp.TokensUsed, durationMs, fmt.Errorf("dedup merge: action merge requires merged_content")
		}
		return dec, resp.Model, resp.TokensUsed, durationMs, nil
	default:
		return zero, resp.Model, resp.TokensUsed, durationMs, fmt.Errorf("dedup merge: unknown action %q", raw.Action)
	}
}

func mergedFactFromLLM(old model.Fact, extracted model.Fact, mergedContent string) model.Fact {
	now := time.Now().UTC()
	return model.Fact{
		ID:         db.NewID(),
		Source:     extracted.Source,
		FactType:   old.FactType,
		Subject:    old.Subject,
		Content:    strings.TrimSpace(mergedContent),
		Confidence: extracted.Confidence,
		Validity:   extracted.Validity,
		CreatedAt:  now,
	}
}

func isActiveFactForDedup(f model.Fact) bool {
	return f.SupersededBy == "" && f.SupersedeReason == ""
}
