package imprint

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/aegis-alpha/imprint-mace/internal/db"
	"github.com/aegis-alpha/imprint-mace/internal/extraction"
	"github.com/aegis-alpha/imprint-mace/internal/model"
	"github.com/aegis-alpha/imprint-mace/internal/provider"
)

const (
	contradictionSupersedeReasonPrefix = "contradiction:"
	contradictionCandidateMinScore     = 0.6
	contradictionSubjectMinJaccard     = 0.3
	dedupContentJaccardMin             = 0.9
)

type contradictionCandidate struct {
	Fact  model.Fact
	Score float64
}

type contradictionGroup struct {
	NewFact    model.Fact
	Candidates []contradictionCandidate
}

type rawContradictionBatch struct {
	Decisions []rawContradictionDecision `json:"decisions"`
}

type rawContradictionDecision struct {
	NewFactID  string                    `json:"new_fact_id"`
	Supersedes []rawContradictionReplace `json:"supersedes"`
}

type rawContradictionReplace struct {
	OldFactID       string `json:"old_fact_id"`
	ShouldSupersede bool   `json:"should_supersede"`
	Rationale       string `json:"rationale"`
}

type contradictionDecision struct {
	NewFactID string
	OldFactID string
	Rationale string
}

// runContradictionBatch calls the LLM once for all groups and returns supersede decisions.
func runContradictionBatch(ctx context.Context, sender extraction.Sender, systemPrompt string, groups []contradictionGroup, logger *slog.Logger) ([]contradictionDecision, string, int, int64, error) {
	if len(groups) == 0 {
		return nil, "", 0, 0, nil
	}
	user, err := buildContradictionUserPayload(groups)
	if err != nil {
		return nil, "", 0, 0, err
	}
	start := time.Now()
	resp, err := sender.Send(ctx, provider.Request{
		SystemPrompt: systemPrompt,
		UserPrompt:   user,
		MaxTokens:    4096,
		Temperature:  0,
	})
	if err != nil {
		return nil, "", 0, 0, fmt.Errorf("contradiction provider: %w", err)
	}
	durationMs := time.Since(start).Milliseconds()
	content := extractJSONObject(stripMarkdownFences(resp.Content))
	var raw rawContradictionBatch
	if err := json.Unmarshal([]byte(content), &raw); err != nil {
		return nil, resp.Model, resp.TokensUsed, durationMs, fmt.Errorf("parse contradiction JSON: %w", err)
	}
	decisions := make([]contradictionDecision, 0)
	allowed := map[string]map[string]bool{}
	for _, g := range groups {
		m := make(map[string]bool)
		for _, c := range g.Candidates {
			m[c.Fact.ID] = true
		}
		allowed[g.NewFact.ID] = m
	}
	for _, d := range raw.Decisions {
		am, ok := allowed[d.NewFactID]
		if !ok {
			continue
		}
		for _, s := range d.Supersedes {
			if !s.ShouldSupersede || s.OldFactID == "" {
				continue
			}
			if !am[s.OldFactID] {
				logger.Warn("contradiction: ignoring unknown old_fact_id for new_fact",
					"new_fact_id", d.NewFactID, "old_fact_id", s.OldFactID)
				continue
			}
			decisions = append(decisions, contradictionDecision{
				NewFactID: d.NewFactID,
				OldFactID: s.OldFactID,
				Rationale: strings.TrimSpace(s.Rationale),
			})
		}
	}
	return decisions, resp.Model, resp.TokensUsed, durationMs, nil
}

func buildContradictionUserPayload(groups []contradictionGroup) (string, error) {
	type candOut struct {
		ID         string  `json:"id"`
		Subject    string  `json:"subject"`
		Content    string  `json:"content"`
		Confidence float64 `json:"confidence"`
		Score      float64 `json:"vector_score"`
	}
	type groupOut struct {
		NewFact struct {
			ID         string  `json:"id"`
			Subject    string  `json:"subject"`
			Content    string  `json:"content"`
			Confidence float64 `json:"confidence"`
			FactType   string  `json:"fact_type"`
		} `json:"new_fact"`
		Candidates []candOut `json:"candidates"`
	}
	out := struct {
		Groups []groupOut `json:"groups"`
	}{}
	for _, g := range groups {
		var gob groupOut
		gob.NewFact.ID = g.NewFact.ID
		gob.NewFact.Subject = g.NewFact.Subject
		gob.NewFact.Content = g.NewFact.Content
		gob.NewFact.Confidence = g.NewFact.Confidence
		gob.NewFact.FactType = string(g.NewFact.FactType)
		for _, c := range g.Candidates {
			gob.Candidates = append(gob.Candidates, candOut{
				ID:         c.Fact.ID,
				Subject:    c.Fact.Subject,
				Content:    c.Fact.Content,
				Confidence: c.Fact.Confidence,
				Score:      c.Score,
			})
		}
		out.Groups = append(out.Groups, gob)
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal contradiction payload: %w", err)
	}
	return string(b), nil
}

func stripMarkdownFences(s string) string {
	s = strings.TrimSpace(s)
	for strings.HasPrefix(s, "```") {
		if idx := strings.Index(s, "\n"); idx != -1 {
			s = s[idx+1:]
		} else {
			s = strings.TrimPrefix(s, "```")
			s = strings.TrimPrefix(s, "json")
		}
		s = strings.TrimSpace(s)
	}
	for strings.HasSuffix(s, "```") {
		s = strings.TrimSuffix(s, "```")
		s = strings.TrimSpace(s)
	}
	return strings.TrimSpace(s)
}

func extractJSONObject(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 0 && s[0] == '{' {
		return s
	}
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start >= 0 && end > start {
		return s[start : end+1]
	}
	return s
}

func filterContradictionCandidates(hits []db.ScoredFact, newSubject string, batchIDs map[string]struct{}) []contradictionCandidate {
	var out []contradictionCandidate
	for _, h := range hits {
		if h.Score < contradictionCandidateMinScore {
			continue
		}
		// Active candidates only: skip rows already retired by either superseded_by or supersede_reason.
		if h.Fact.SupersededBy != "" || h.Fact.SupersedeReason != "" {
			continue
		}
		if _, inBatch := batchIDs[h.Fact.ID]; inBatch {
			continue
		}
		if !subjectMatch(newSubject, h.Fact.Subject, contradictionSubjectMinJaccard) {
			continue
		}
		out = append(out, contradictionCandidate{Fact: h.Fact, Score: h.Score})
	}
	return out
}

func confidenceAllowsSupersede(newFact, oldFact model.Fact) bool {
	if newFact.Confidence >= 0.5 {
		return true
	}
	if oldFact.Confidence <= 0.8 {
		return true
	}
	// Low-confidence new fact must not supersede a high-confidence old fact.
	return false
}

func formatContradictionReason(rationale string) string {
	r := strings.TrimSpace(rationale)
	if r == "" {
		return contradictionSupersedeReasonPrefix + "unspecified"
	}
	return contradictionSupersedeReasonPrefix + r
}
