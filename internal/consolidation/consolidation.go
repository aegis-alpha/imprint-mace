// Package consolidation groups related facts, finds connections between them,
// and generates higher-order insights by sending fact batches to an LLM
// with a type-aware prompt template.
package consolidation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
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

// Consolidator queries unconsolidated facts, sends them to an LLM for
// grouping and insight generation, and stores the results.
type Consolidator struct {
	sender           Sender
	store            db.Store
	prompt           string
	clusterThreshold float64
	logger           *slog.Logger
}

// New creates a Consolidator. It reads the prompt template from disk and
// renders it with the given types (connection types for consolidation).
// clusterThreshold controls the cosine similarity threshold for grouping
// facts into clusters before sending them to the LLM.
func New(sender Sender, store db.Store, templatePath string, types config.TypesConfig, clusterThreshold float64, logger *slog.Logger) (*Consolidator, error) {
	raw, err := os.ReadFile(templatePath) //nolint:gosec // template path from config, not user input
	if err != nil {
		return nil, fmt.Errorf("read prompt template %s: %w", templatePath, err)
	}

	tmpl, err := template.New("consolidation").Parse(string(raw))
	if err != nil {
		return nil, fmt.Errorf("parse prompt template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, types); err != nil {
		return nil, fmt.Errorf("render prompt template: %w", err)
	}

	return &Consolidator{
		sender:           sender,
		store:            store,
		prompt:           buf.String(),
		clusterThreshold: clusterThreshold,
		logger:           logger,
	}, nil
}

// rawConsolidationResult is the JSON shape the LLM returns.
type rawConsolidationResult struct {
	Connections []rawConnection `json:"connections"`
	Summary     string          `json:"summary"`
	Insight     string          `json:"insight"`
	Importance  float64         `json:"importance"`
}

type rawConnection struct {
	FactA          string  `json:"fact_a"`
	FactB          string  `json:"fact_b"`
	ConnectionType string  `json:"connection_type"`
	Strength       float64 `json:"strength"`
}

// ConsolidateResult holds what one consolidation pass produced.
type ConsolidateResult struct {
	Consolidation   *model.Consolidation
	FactConnections []model.FactConnection
}

// Consolidate fetches unconsolidated facts, clusters them by similarity,
// and sends each cluster to the LLM separately for consolidation.
// Returns nil (no error) when there are no viable clusters to consolidate.
func (c *Consolidator) Consolidate(ctx context.Context, limit int) ([]ConsolidateResult, error) {
	facts, err := c.store.ListUnconsolidatedFacts(ctx, limit)
	if err != nil {
		return nil, fmt.Errorf("list unconsolidated facts: %w", err)
	}
	if len(facts) < 2 {
		c.logger.Info("consolidation skipped: not enough unconsolidated facts", "count", len(facts))
		return nil, nil
	}

	clusters := clusterFacts(facts, c.clusterThreshold)
	if len(clusters) == 0 {
		c.logger.Info("consolidation skipped: no clusters with >= 2 facts")
		return nil, nil
	}

	c.logger.Info("facts clustered for consolidation",
		"total_facts", len(facts),
		"clusters", len(clusters),
	)

	var results []ConsolidateResult
	for i, cluster := range clusters {
		result, err := c.consolidateCluster(ctx, cluster, i, len(clusters))
		if err != nil {
			return results, fmt.Errorf("cluster %d/%d: %w", i+1, len(clusters), err)
		}
		results = append(results, *result)
	}

	return results, nil
}

func (c *Consolidator) consolidateCluster(ctx context.Context, facts []model.Fact, idx, total int) (*ConsolidateResult, error) {
	userPrompt := formatFactsForLLM(facts)

	start := time.Now()
	resp, err := c.sender.Send(ctx, provider.Request{
		SystemPrompt: c.prompt,
		UserPrompt:   userPrompt,
		MaxTokens:    4096,
	})
	if err != nil {
		duration := time.Since(start)
		c.logger.Error("consolidation provider failed",
			"cluster", fmt.Sprintf("%d/%d", idx+1, total),
			"duration_ms", duration.Milliseconds(),
			"fact_count", len(facts),
			"error", err,
		)
		return nil, fmt.Errorf("provider: %w", err)
	}

	duration := time.Since(start)
	content := stripMarkdownFences(resp.Content)

	var raw rawConsolidationResult
	if err := json.Unmarshal([]byte(content), &raw); err != nil {
		c.logger.Error("consolidation JSON parse failed",
			"cluster", fmt.Sprintf("%d/%d", idx+1, total),
			"provider", resp.ProviderName,
			"model", resp.Model,
			"duration_ms", duration.Milliseconds(),
			"tokens", resp.TokensUsed,
			"raw_length", len(resp.Content),
			"error", err,
		)
		return nil, fmt.Errorf("parse LLM response: %w", err)
	}

	now := time.Now()
	consolidationID := db.NewID()

	factIDs := make([]string, len(facts))
	for i := range facts {
		factIDs[i] = facts[i].ID
	}

	cons := &model.Consolidation{
		ID:            consolidationID,
		SourceFactIDs: factIDs,
		Summary:       raw.Summary,
		Insight:       raw.Insight,
		Importance:    raw.Importance,
		CreatedAt:     now,
	}

	if err := c.store.CreateConsolidation(ctx, cons); err != nil {
		return nil, fmt.Errorf("store consolidation: %w", err)
	}

	var fcs []model.FactConnection
	for _, rc := range raw.Connections {
		fc := model.FactConnection{
			ID:              db.NewID(),
			FactA:           rc.FactA,
			FactB:           rc.FactB,
			ConnectionType:  model.ConnectionType(rc.ConnectionType),
			Strength:        rc.Strength,
			ConsolidationID: consolidationID,
			CreatedAt:       now,
		}
		if err := c.store.CreateFactConnection(ctx, &fc); err != nil {
			return nil, fmt.Errorf("store fact connection: %w", err)
		}
		fcs = append(fcs, fc)
	}

	c.logger.Info("cluster consolidation complete",
		"cluster", fmt.Sprintf("%d/%d", idx+1, total),
		"provider", resp.ProviderName,
		"model", resp.Model,
		"duration_ms", duration.Milliseconds(),
		"tokens", resp.TokensUsed,
		"facts_in", len(facts),
		"connections_out", len(fcs),
		"importance", raw.Importance,
	)

	return &ConsolidateResult{
		Consolidation:   cons,
		FactConnections: fcs,
	}, nil
}

// clusterFacts groups facts by cosine similarity of their embeddings.
// Facts without embeddings fall back to case-insensitive subject grouping.
// Remaining ungrouped facts go into a misc cluster.
// Clusters with fewer than 2 facts are dropped.
func clusterFacts(facts []model.Fact, threshold float64) [][]model.Fact {
	var withEmb, withoutEmb []model.Fact
	for i := range facts {
		if len(facts[i].Embedding) > 0 {
			withEmb = append(withEmb, facts[i])
		} else {
			withoutEmb = append(withoutEmb, facts[i])
		}
	}

	var clusters [][]model.Fact

	assigned := make([]bool, len(withEmb))
	for i := range withEmb {
		if assigned[i] {
			continue
		}
		cluster := []model.Fact{withEmb[i]}
		assigned[i] = true
		for j := i + 1; j < len(withEmb); j++ {
			if assigned[j] {
				continue
			}
			if cosineSimilarity(withEmb[i].Embedding, withEmb[j].Embedding) >= threshold {
				cluster = append(cluster, withEmb[j])
				assigned[j] = true
			}
		}
		if len(cluster) >= 2 {
			clusters = append(clusters, cluster)
		}
	}

	subjectGroups := make(map[string][]model.Fact)
	var noSubject []model.Fact
	for i := range withoutEmb {
		subj := strings.TrimSpace(strings.ToLower(withoutEmb[i].Subject))
		if subj == "" {
			noSubject = append(noSubject, withoutEmb[i])
		} else {
			subjectGroups[subj] = append(subjectGroups[subj], withoutEmb[i])
		}
	}
	for _, group := range subjectGroups {
		if len(group) >= 2 {
			clusters = append(clusters, group)
		} else {
			noSubject = append(noSubject, group...)
		}
	}

	if len(noSubject) >= 2 {
		clusters = append(clusters, noSubject)
	}

	return clusters
}

func cosineSimilarity(a, b []float32) float64 {
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

func formatFactsForLLM(facts []model.Fact) string {
	var b strings.Builder
	for i := range facts {
		f := &facts[i]
		fmt.Fprintf(&b, "- [%s] (%s) %s: %s\n", f.ID, f.FactType, f.Subject, f.Content)
	}
	return b.String()
}

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
