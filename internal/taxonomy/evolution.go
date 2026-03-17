package taxonomy

import (
	"bytes"
	"context"
	"database/sql"
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

// Sender sends a request to an LLM.
type Sender interface {
	Send(ctx context.Context, req provider.Request) (*provider.Response, error)
}

type promptData struct {
	config.TypesConfig
	SignalsText string
}

// Evolver sends accumulated taxonomy signals to an LLM for review
// and stores the resulting proposals.
type Evolver struct {
	sender Sender
	store  db.Store
	sqlDB  *sql.DB
	prompt string
	types  config.TypesConfig
	logger *slog.Logger
}

// NewEvolver creates an Evolver. It reads and renders the prompt template.
func NewEvolver(sender Sender, store db.Store, sqlDB *sql.DB, templatePath string, types config.TypesConfig, logger *slog.Logger) (*Evolver, error) {
	raw, err := os.ReadFile(templatePath)
	if err != nil {
		return nil, fmt.Errorf("read taxonomy review prompt %s: %w", templatePath, err)
	}

	if _, err := template.New("review").Parse(string(raw)); err != nil {
		return nil, fmt.Errorf("parse taxonomy review prompt: %w", err)
	}

	return &Evolver{
		sender: sender,
		store:  store,
		sqlDB:  sqlDB,
		prompt: string(raw),
		types:  types,
		logger: logger,
	}, nil
}

type rawProposal struct {
	Action       string          `json:"action"`
	TypeCategory string          `json:"type_category"`
	TypeName     string          `json:"type_name"`
	Definition   json.RawMessage `json:"definition"`
	Rationale    string          `json:"rationale"`
}

// ReviewTaxonomy collects unresolved actionable signals, sends them to
// the LLM for review, and stores resulting proposals in the DB.
// Returns empty slice if no actionable signals exist.
func (e *Evolver) ReviewTaxonomy(ctx context.Context) ([]db.TaxonomyProposal, error) {
	signals, err := e.store.ListTaxonomySignals(ctx, false, 100)
	if err != nil {
		return nil, fmt.Errorf("list signals: %w", err)
	}

	var actionable []db.TaxonomySignal
	for i := range signals {
		if signals[i].SignalType == "custom_frequency" || signals[i].SignalType == "low_confidence" {
			actionable = append(actionable, signals[i])
		}
	}

	if len(actionable) == 0 {
		return nil, nil
	}

	signalsText := formatSignals(actionable)

	tmpl, err := template.New("review").Parse(e.prompt)
	if err != nil {
		return nil, fmt.Errorf("parse prompt: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, promptData{TypesConfig: e.types, SignalsText: signalsText}); err != nil {
		return nil, fmt.Errorf("render prompt: %w", err)
	}

	resp, err := e.sender.Send(ctx, provider.Request{
		SystemPrompt: buf.String(),
		UserPrompt:   "Review the signals and propose taxonomy changes.",
		MaxTokens:    2048,
	})
	if err != nil {
		return nil, fmt.Errorf("LLM review: %w", err)
	}

	content := stripMarkdownFences(resp.Content)

	var rawProposals []rawProposal
	if err := json.Unmarshal([]byte(content), &rawProposals); err != nil {
		e.logger.Warn("taxonomy review: LLM returned invalid JSON, skipping",
			"error", err, "signals", len(actionable))
		return nil, nil
	}

	signalIDsJSON, _ := json.Marshal(collectSignalIDs(actionable))
	now := time.Now()

	var proposals []db.TaxonomyProposal
	for _, rp := range rawProposals {
		defStr := "{}"
		if len(rp.Definition) > 0 {
			defStr = string(rp.Definition)
		}

		p := db.TaxonomyProposal{
			ID:            db.NewID(),
			Action:        rp.Action,
			TypeCategory:  rp.TypeCategory,
			TypeName:      rp.TypeName,
			Definition:    defStr,
			Rationale:     rp.Rationale,
			Status:        "proposed",
			ShadowResults: "{}",
			SignalIDs:     string(signalIDsJSON),
			CreatedAt:     now,
		}

		if err := e.store.CreateTaxonomyProposal(ctx, &p); err != nil {
			e.logger.Error("failed to store proposal", "type_name", p.TypeName, "error", err)
			continue
		}
		proposals = append(proposals, p)
	}

	e.logger.Info("taxonomy review complete",
		"signals_reviewed", len(actionable),
		"proposals_created", len(proposals),
	)

	return proposals, nil
}

const minFactsForRemove = 100

// ValidateProposals runs validation on all "proposed" proposals.
// - add: SQL check for custom_frequency signal with count >= 1
// - remove: SQL check for 0 facts AND total facts >= minFactsForRemove
// - merge: embedding pre-filter + LLM confirmation
// - rename: LLM confirmation
func (e *Evolver) ValidateProposals(ctx context.Context) error {
	proposals, err := e.store.ListTaxonomyProposals(ctx, "proposed", 100)
	if err != nil {
		return fmt.Errorf("list proposed: %w", err)
	}

	for i := range proposals {
		p := &proposals[i]
		switch p.Action {
		case "add":
			if err := e.validateAdd(ctx, *p); err != nil {
				e.logger.Error("validate add failed", "type_name", p.TypeName, "error", err)
			}
		case "remove":
			if err := e.validateRemove(ctx, *p); err != nil {
				e.logger.Error("validate remove failed", "type_name", p.TypeName, "error", err)
			}
		case "merge":
			if err := e.validateMerge(ctx, *p); err != nil {
				e.logger.Error("validate merge failed", "type_name", p.TypeName, "error", err)
			}
		case "rename":
			if err := e.validateRename(ctx, *p); err != nil {
				e.logger.Error("validate rename failed", "type_name", p.TypeName, "error", err)
			}
		default:
			e.logger.Info("skipping validation for unknown action",
				"action", p.Action, "type_name", p.TypeName)
		}
	}
	return nil
}

func (e *Evolver) validateAdd(ctx context.Context, p db.TaxonomyProposal) error {
	var count int
	err := e.sqlDB.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(count), 0) FROM taxonomy_signals
		WHERE resolved_by IS NULL AND signal_type = 'custom_frequency'
		AND type_name = ?`, p.TypeName).Scan(&count)
	if err != nil {
		return err
	}

	if count > 0 {
		now := time.Now()
		shadow := fmt.Sprintf(`{"custom_frequency_count":%d}`, count)
		return e.store.UpdateTaxonomyProposalStatus(ctx, p.ID, "validated", shadow, &now)
	}
	return nil
}

func (e *Evolver) validateRemove(ctx context.Context, p db.TaxonomyProposal) error {
	var factCount int
	err := e.sqlDB.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM facts WHERE fact_type = ?", p.TypeName).Scan(&factCount)
	if err != nil {
		return err
	}

	var totalFacts int
	err = e.sqlDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM facts").Scan(&totalFacts)
	if err != nil {
		return err
	}

	if factCount == 0 && totalFacts >= minFactsForRemove {
		now := time.Now()
		shadow := fmt.Sprintf(`{"fact_count":0,"total_facts":%d}`, totalFacts)
		return e.store.UpdateTaxonomyProposalStatus(ctx, p.ID, "validated", shadow, &now)
	}
	return nil
}

const mergeSimilarityThreshold = 0.7
const maxSampleFacts = 10

func (e *Evolver) validateMerge(ctx context.Context, p db.TaxonomyProposal) error {
	var mergeTarget struct {
		MergeInto string `json:"merge_into"`
	}
	if err := json.Unmarshal([]byte(p.Definition), &mergeTarget); err != nil || mergeTarget.MergeInto == "" {
		now := time.Now()
		return e.store.UpdateTaxonomyProposalStatus(ctx, p.ID, "rejected",
			`{"reason":"invalid merge definition: missing merge_into"}`, &now)
	}

	sourceType := p.TypeName
	targetType := mergeTarget.MergeInto

	sourceCentroid, sourceCount, err := e.computeTypeCentroid(ctx, sourceType)
	if err != nil {
		return fmt.Errorf("centroid for %s: %w", sourceType, err)
	}
	targetCentroid, targetCount, err := e.computeTypeCentroid(ctx, targetType)
	if err != nil {
		return fmt.Errorf("centroid for %s: %w", targetType, err)
	}

	if sourceCentroid != nil && targetCentroid != nil {
		sim := cosineSimilarity(sourceCentroid, targetCentroid)
		e.logger.Info("merge embedding check",
			"source", sourceType, "target", targetType,
			"similarity", sim, "threshold", mergeSimilarityThreshold,
			"source_vecs", sourceCount, "target_vecs", targetCount)

		if sim < mergeSimilarityThreshold {
			now := time.Now()
			shadow := fmt.Sprintf(`{"cosine_similarity":%.4f,"threshold":%.1f,"source_vecs":%d,"target_vecs":%d}`,
				sim, mergeSimilarityThreshold, sourceCount, targetCount)
			return e.store.UpdateTaxonomyProposalStatus(ctx, p.ID, "rejected", shadow, &now)
		}
	}

	sourceFacts, err := e.store.ListFacts(ctx, db.FactFilter{FactType: sourceType, Limit: maxSampleFacts})
	if err != nil {
		return fmt.Errorf("list source facts: %w", err)
	}
	targetFacts, err := e.store.ListFacts(ctx, db.FactFilter{FactType: targetType, Limit: maxSampleFacts})
	if err != nil {
		return fmt.Errorf("list target facts: %w", err)
	}

	prompt := buildMergePrompt(sourceType, targetType, sourceFacts, targetFacts)
	resp, err := e.sender.Send(ctx, provider.Request{
		SystemPrompt: "You are a taxonomy analyst. Answer strictly in JSON.",
		UserPrompt:   prompt,
		MaxTokens:    512,
	})
	if err != nil {
		return fmt.Errorf("LLM merge confirmation: %w", err)
	}

	var decision struct {
		ShouldMerge bool   `json:"should_merge"`
		Reason      string `json:"reason"`
	}
	content := stripMarkdownFences(resp.Content)
	if err := json.Unmarshal([]byte(content), &decision); err != nil {
		return fmt.Errorf("parse LLM merge response: %w", err)
	}

	now := time.Now()
	if decision.ShouldMerge {
		shadow := fmt.Sprintf(`{"llm_confirmed":true,"reason":%q}`, decision.Reason)
		return e.store.UpdateTaxonomyProposalStatus(ctx, p.ID, "validated", shadow, &now)
	}
	shadow := fmt.Sprintf(`{"llm_confirmed":false,"reason":%q}`, decision.Reason)
	return e.store.UpdateTaxonomyProposalStatus(ctx, p.ID, "rejected", shadow, &now)
}

func (e *Evolver) validateRename(ctx context.Context, p db.TaxonomyProposal) error {
	var renameTarget struct {
		RenameTo string `json:"rename_to"`
	}
	if err := json.Unmarshal([]byte(p.Definition), &renameTarget); err != nil || renameTarget.RenameTo == "" {
		now := time.Now()
		return e.store.UpdateTaxonomyProposalStatus(ctx, p.ID, "rejected",
			`{"reason":"invalid rename definition: missing rename_to"}`, &now)
	}

	facts, err := e.store.ListFacts(ctx, db.FactFilter{FactType: p.TypeName, Limit: maxSampleFacts})
	if err != nil {
		return fmt.Errorf("list facts for rename: %w", err)
	}
	if len(facts) == 0 {
		now := time.Now()
		return e.store.UpdateTaxonomyProposalStatus(ctx, p.ID, "rejected",
			`{"reason":"no facts of this type to evaluate rename"}`, &now)
	}

	prompt := buildRenamePrompt(p.TypeName, renameTarget.RenameTo, facts)
	resp, err := e.sender.Send(ctx, provider.Request{
		SystemPrompt: "You are a taxonomy analyst. Answer strictly in JSON.",
		UserPrompt:   prompt,
		MaxTokens:    512,
	})
	if err != nil {
		return fmt.Errorf("LLM rename confirmation: %w", err)
	}

	var decision struct {
		ShouldRename bool   `json:"should_rename"`
		Reason       string `json:"reason"`
	}
	content := stripMarkdownFences(resp.Content)
	if err := json.Unmarshal([]byte(content), &decision); err != nil {
		return fmt.Errorf("parse LLM rename response: %w", err)
	}

	now := time.Now()
	if decision.ShouldRename {
		shadow := fmt.Sprintf(`{"llm_confirmed":true,"reason":%q}`, decision.Reason)
		return e.store.UpdateTaxonomyProposalStatus(ctx, p.ID, "validated", shadow, &now)
	}
	shadow := fmt.Sprintf(`{"llm_confirmed":false,"reason":%q}`, decision.Reason)
	return e.store.UpdateTaxonomyProposalStatus(ctx, p.ID, "rejected", shadow, &now)
}

func buildMergePrompt(sourceType, targetType string, sourceFacts, targetFacts []model.Fact) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Here are facts of type %q:\n", sourceType)
	for i := range sourceFacts {
		fmt.Fprintf(&b, "- [%s] %s\n", sourceFacts[i].Subject, sourceFacts[i].Content)
	}
	fmt.Fprintf(&b, "\nHere are facts of type %q:\n", targetType)
	for i := range targetFacts {
		fmt.Fprintf(&b, "- [%s] %s\n", targetFacts[i].Subject, targetFacts[i].Content)
	}
	fmt.Fprintf(&b, "\nQuestion: Are these two types semantically equivalent? Would merging %q into %q lose any meaningful distinction?\n", sourceType, targetType)
	b.WriteString("\nAnswer as JSON: {\"should_merge\": true/false, \"reason\": \"...\"}")
	return b.String()
}

func buildRenamePrompt(oldName, newName string, facts []model.Fact) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Here are facts of type %q:\n", oldName)
	for i := range facts {
		fmt.Fprintf(&b, "- [%s] %s\n", facts[i].Subject, facts[i].Content)
	}
	fmt.Fprintf(&b, "\nThe proposed new name is %q.\n", newName)
	fmt.Fprintf(&b, "\nQuestion: Does %q better describe these facts than %q? Is the rename justified?\n", newName, oldName)
	b.WriteString("\nAnswer as JSON: {\"should_rename\": true/false, \"reason\": \"...\"}")
	return b.String()
}

// EffectiveTypesWithProposals merges the base TypesConfig with applied
// proposals from the DB. Add proposals append new types; remove proposals
// filter out existing types. This is the runtime source of truth (D18 + D17).
func (e *Evolver) EffectiveTypesWithProposals(ctx context.Context, base config.TypesConfig) (config.TypesConfig, error) {
	applied, err := e.store.ListTaxonomyProposals(ctx, "applied", 500)
	if err != nil {
		return base, fmt.Errorf("list applied proposals: %w", err)
	}

	if len(applied) == 0 {
		return base, nil
	}

	result := config.TypesConfig{
		FactTypes:       append([]config.TypeDef{}, base.FactTypes...),
		EntityTypes:     append([]config.TypeDef{}, base.EntityTypes...),
		RelationTypes:   append([]config.TypeDef{}, base.RelationTypes...),
		ConnectionTypes: append([]config.TypeDef{}, base.ConnectionTypes...),
	}

	for i := range applied {
		p := &applied[i]
		switch p.Action {
		case "add":
			td := parseDefinition(p.Definition)
			if td.Name == "" {
				td.Name = p.TypeName
			}
			appendType(&result, p.TypeCategory, td)
		case "remove":
			removeType(&result, p.TypeCategory, p.TypeName)
		case "merge":
			removeType(&result, p.TypeCategory, p.TypeName)
		case "rename":
			var rt struct {
				RenameTo string `json:"rename_to"`
			}
			json.Unmarshal([]byte(p.Definition), &rt)
			if rt.RenameTo != "" {
				renameType(&result, p.TypeCategory, p.TypeName, rt.RenameTo)
			}
		}
	}

	return result, nil
}

func parseDefinition(defJSON string) config.TypeDef {
	var td config.TypeDef
	json.Unmarshal([]byte(defJSON), &td)
	return td
}

func appendType(tc *config.TypesConfig, category string, td config.TypeDef) {
	switch category {
	case "fact":
		tc.FactTypes = append(tc.FactTypes, td)
	case "entity":
		tc.EntityTypes = append(tc.EntityTypes, td)
	case "relation":
		tc.RelationTypes = append(tc.RelationTypes, td)
	case "connection":
		tc.ConnectionTypes = append(tc.ConnectionTypes, td)
	}
}

func removeType(tc *config.TypesConfig, category, name string) {
	filter := func(types []config.TypeDef, name string) []config.TypeDef {
		var out []config.TypeDef
		for _, t := range types {
			if t.Name != name {
				out = append(out, t)
			}
		}
		return out
	}
	switch category {
	case "fact":
		tc.FactTypes = filter(tc.FactTypes, name)
	case "entity":
		tc.EntityTypes = filter(tc.EntityTypes, name)
	case "relation":
		tc.RelationTypes = filter(tc.RelationTypes, name)
	case "connection":
		tc.ConnectionTypes = filter(tc.ConnectionTypes, name)
	}
}

func renameType(tc *config.TypesConfig, category, oldName, newName string) {
	rename := func(types []config.TypeDef) []config.TypeDef {
		for i := range types {
			if types[i].Name == oldName {
				types[i].Name = newName
				break
			}
		}
		return types
	}
	switch category {
	case "fact":
		tc.FactTypes = rename(tc.FactTypes)
	case "entity":
		tc.EntityTypes = rename(tc.EntityTypes)
	case "relation":
		tc.RelationTypes = rename(tc.RelationTypes)
	case "connection":
		tc.ConnectionTypes = rename(tc.ConnectionTypes)
	}
}

// computeTypeCentroid returns the average embedding vector for all facts
// of the given type that have embeddings in facts_vec. Returns nil if no
// embeddings exist.
func (e *Evolver) computeTypeCentroid(ctx context.Context, factType string) ([]float32, int, error) {
	vecs, err := e.store.ListFactEmbeddings(ctx, factType)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return nil, 0, nil
		}
		return nil, 0, fmt.Errorf("list embeddings for %s: %w", factType, err)
	}
	if len(vecs) == 0 {
		return nil, 0, nil
	}

	dims := len(vecs[0])
	centroid := make([]float32, dims)
	for _, v := range vecs {
		for j, val := range v {
			centroid[j] += val
		}
	}
	n := float32(len(vecs))
	for j := range centroid {
		centroid[j] /= n
	}
	return centroid, len(vecs), nil
}

func cosineSimilarity(a, b []float32) float64 {
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

func formatSignals(signals []db.TaxonomySignal) string {
	var b strings.Builder
	for i := range signals {
		s := &signals[i]
		fmt.Fprintf(&b, "- [%s] %s/%s: count=%d details=%s\n",
			s.SignalType, s.TypeCategory, s.TypeName, s.Count, s.Details)
	}
	return b.String()
}

func collectSignalIDs(signals []db.TaxonomySignal) []string {
	ids := make([]string, len(signals))
	for i := range signals {
		ids[i] = signals[i].ID
	}
	return ids
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
