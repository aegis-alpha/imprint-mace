package taxonomy

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aegis-alpha/imprint-mace/internal/config"
	"github.com/aegis-alpha/imprint-mace/internal/db"
	"github.com/aegis-alpha/imprint-mace/internal/model"
	"github.com/aegis-alpha/imprint-mace/internal/provider"
)

type mockSender struct {
	response *provider.Response
	err      error
	calls    int
}

func (m *mockSender) Send(_ context.Context, _ provider.Request) (*provider.Response, error) {
	m.calls++
	return m.response, m.err
}

func testPromptPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "prompt.md")
	os.WriteFile(path, []byte("Review signals.\n{{range .FactTypes}}- {{.Name}}\n{{end}}\n{{.SignalsText}}\nReturn JSON."), 0644)
	return path
}

var mockReviewJSON = `[
  {"action": "add", "type_category": "fact", "type_name": "metric", "definition": {"name": "metric", "description": "A measured value", "example": "CPU at 80%"}, "rationale": "LLM returned metric 50 times"},
  {"action": "remove", "type_category": "fact", "type_name": "skill", "definition": {}, "rationale": "Zero facts use this type"}
]`

func TestReviewTaxonomy_CreatesProposals(t *testing.T) {
	store, rawDB := openTestDB(t)
	ctx := context.Background()

	seedFactsWithType(t, store, "custom_metric", 0.8, 15)

	sc := NewSignalCollector(rawDB, store, config.DefaultTypes(), slog.Default())
	sc.CollectAll(ctx)

	sender := &mockSender{response: &provider.Response{
		Content: mockReviewJSON, ProviderName: "mock", Model: "test", TokensUsed: 200,
	}}

	evolver, err := NewEvolver(sender, store, rawDB, testPromptPath(t), config.DefaultTypes(), slog.Default())
	if err != nil {
		t.Fatalf("create evolver: %v", err)
	}

	proposals, err := evolver.ReviewTaxonomy(ctx)
	if err != nil {
		t.Fatalf("review taxonomy: %v", err)
	}

	if len(proposals) != 2 {
		t.Fatalf("expected 2 proposals, got %d", len(proposals))
	}
	if proposals[0].Action != "add" {
		t.Errorf("proposal[0]: expected action 'add', got %q", proposals[0].Action)
	}
	if proposals[0].TypeName != "metric" {
		t.Errorf("proposal[0]: expected type_name 'metric', got %q", proposals[0].TypeName)
	}
	if proposals[1].Action != "remove" {
		t.Errorf("proposal[1]: expected action 'remove', got %q", proposals[1].Action)
	}

	stored, err := store.ListTaxonomyProposals(ctx, "proposed", 10)
	if err != nil {
		t.Fatalf("list proposals: %v", err)
	}
	if len(stored) != 2 {
		t.Errorf("expected 2 proposals in DB, got %d", len(stored))
	}
}

func TestValidateProposals_AddValidated(t *testing.T) {
	store, rawDB := openTestDB(t)
	ctx := context.Background()

	seedFactsWithType(t, store, "custom_metric", 0.8, 15)

	sc := NewSignalCollector(rawDB, store, config.DefaultTypes(), slog.Default())
	sc.CollectAll(ctx)

	store.CreateTaxonomyProposal(ctx, &db.TaxonomyProposal{
		ID: db.NewID(), Action: "add", TypeCategory: "fact", TypeName: "custom_metric",
		Definition: `{"name":"custom_metric"}`, Rationale: "frequent custom type",
		Status: "proposed", ShadowResults: "{}", SignalIDs: "[]",
		CreatedAt: time.Now().UTC(),
	})

	sender := &mockSender{response: &provider.Response{Content: "[]"}}
	evolver, _ := NewEvolver(sender, store, rawDB, testPromptPath(t), config.DefaultTypes(), slog.Default())

	err := evolver.ValidateProposals(ctx)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}

	validated, _ := store.ListTaxonomyProposals(ctx, "validated", 10)
	if len(validated) != 1 {
		t.Fatalf("expected 1 validated proposal, got %d", len(validated))
	}
	if validated[0].TypeName != "custom_metric" {
		t.Errorf("expected type_name 'custom_metric', got %q", validated[0].TypeName)
	}
}

func TestValidateProposals_RemoveValidated(t *testing.T) {
	store, rawDB := openTestDB(t)
	ctx := context.Background()

	seedFactsWithType(t, store, "decision", 0.9, 120)

	sc := NewSignalCollector(rawDB, store, config.DefaultTypes(), slog.Default())
	sc.CollectAll(ctx)

	store.CreateTaxonomyProposal(ctx, &db.TaxonomyProposal{
		ID: db.NewID(), Action: "remove", TypeCategory: "fact", TypeName: "skill",
		Definition: "{}", Rationale: "unused type",
		Status: "proposed", ShadowResults: "{}", SignalIDs: "[]",
		CreatedAt: time.Now().UTC(),
	})

	sender := &mockSender{response: &provider.Response{Content: "[]"}}
	evolver, _ := NewEvolver(sender, store, rawDB, testPromptPath(t), config.DefaultTypes(), slog.Default())

	err := evolver.ValidateProposals(ctx)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}

	validated, _ := store.ListTaxonomyProposals(ctx, "validated", 10)
	if len(validated) != 1 {
		t.Fatalf("expected 1 validated proposal, got %d", len(validated))
	}
}

func TestValidateProposals_RemoveRejectedSmallDB(t *testing.T) {
	store, rawDB := openTestDB(t)
	ctx := context.Background()

	seedFactsWithType(t, store, "decision", 0.9, 10)

	store.CreateTaxonomyProposal(ctx, &db.TaxonomyProposal{
		ID: db.NewID(), Action: "remove", TypeCategory: "fact", TypeName: "skill",
		Definition: "{}", Rationale: "unused type",
		Status: "proposed", ShadowResults: "{}", SignalIDs: "[]",
		CreatedAt: time.Now().UTC(),
	})

	sender := &mockSender{response: &provider.Response{Content: "[]"}}
	evolver, _ := NewEvolver(sender, store, rawDB, testPromptPath(t), config.DefaultTypes(), slog.Default())

	evolver.ValidateProposals(ctx)

	proposed, _ := store.ListTaxonomyProposals(ctx, "proposed", 10)
	if len(proposed) != 1 {
		t.Errorf("expected remove proposal to stay 'proposed' on small DB, got %d proposed", len(proposed))
	}
}

func TestValidateProposals_MergeValidated(t *testing.T) {
	store, rawDB := openTestDB(t)
	ctx := context.Background()

	seedFactsWithType(t, store, "project_info", 0.9, 5)
	seedFactsWithType(t, store, "project", 0.9, 5)

	store.CreateTaxonomyProposal(ctx, &db.TaxonomyProposal{
		ID: db.NewID(), Action: "merge", TypeCategory: "fact", TypeName: "project_info",
		Definition: `{"merge_into":"project"}`, Rationale: "overlap with project",
		Status: "proposed", ShadowResults: "{}", SignalIDs: "[]",
		CreatedAt: time.Now().UTC(),
	})

	sender := &mockSender{response: &provider.Response{
		Content: `{"should_merge": true, "reason": "semantically equivalent"}`,
	}}
	evolver, _ := NewEvolver(sender, store, rawDB, testPromptPath(t), config.DefaultTypes(), slog.Default())

	err := evolver.ValidateProposals(ctx)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}

	validated, _ := store.ListTaxonomyProposals(ctx, "validated", 10)
	if len(validated) != 1 {
		t.Fatalf("expected 1 validated merge proposal, got %d", len(validated))
	}
}

func TestEffectiveTypesWithProposals_AddApplied(t *testing.T) {
	store, rawDB := openTestDB(t)
	ctx := context.Background()

	now := time.Now().UTC()
	store.CreateTaxonomyProposal(ctx, &db.TaxonomyProposal{
		ID: db.NewID(), Action: "add", TypeCategory: "fact", TypeName: "metric",
		Definition:    `{"name":"metric","description":"A measured value","example":"CPU at 80%"}`,
		Rationale:     "frequent custom type", Status: "applied",
		ShadowResults: "{}", SignalIDs: "[]", CreatedAt: now, ResolvedAt: &now,
	})

	base := config.DefaultTypes()
	sender := &mockSender{response: &provider.Response{Content: "[]"}}
	evolver, _ := NewEvolver(sender, store, rawDB, testPromptPath(t), base, slog.Default())

	merged, err := evolver.EffectiveTypesWithProposals(ctx, base)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}

	found := false
	for _, ft := range merged.FactTypes {
		if ft.Name == "metric" {
			found = true
			if ft.Description != "A measured value" {
				t.Errorf("expected description 'A measured value', got %q", ft.Description)
			}
			break
		}
	}
	if !found {
		t.Error("expected 'metric' in merged FactTypes")
	}

	if len(merged.FactTypes) != len(base.FactTypes)+1 {
		t.Errorf("expected %d fact types, got %d", len(base.FactTypes)+1, len(merged.FactTypes))
	}
}

func TestEffectiveTypesWithProposals_RemoveApplied(t *testing.T) {
	store, rawDB := openTestDB(t)
	ctx := context.Background()

	now := time.Now().UTC()
	store.CreateTaxonomyProposal(ctx, &db.TaxonomyProposal{
		ID: db.NewID(), Action: "remove", TypeCategory: "fact", TypeName: "skill",
		Definition: "{}", Rationale: "unused", Status: "applied",
		ShadowResults: "{}", SignalIDs: "[]", CreatedAt: now, ResolvedAt: &now,
	})

	base := config.DefaultTypes()
	sender := &mockSender{response: &provider.Response{Content: "[]"}}
	evolver, _ := NewEvolver(sender, store, rawDB, testPromptPath(t), base, slog.Default())

	merged, err := evolver.EffectiveTypesWithProposals(ctx, base)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}

	for _, ft := range merged.FactTypes {
		if ft.Name == "skill" {
			t.Error("expected 'skill' to be removed from merged FactTypes")
		}
	}

	if len(merged.FactTypes) != len(base.FactTypes)-1 {
		t.Errorf("expected %d fact types, got %d", len(base.FactTypes)-1, len(merged.FactTypes))
	}
}

func TestEffectiveTypesWithProposals_NoApplied(t *testing.T) {
	store, rawDB := openTestDB(t)
	ctx := context.Background()

	base := config.DefaultTypes()
	sender := &mockSender{response: &provider.Response{Content: "[]"}}
	evolver, _ := NewEvolver(sender, store, rawDB, testPromptPath(t), base, slog.Default())

	merged, err := evolver.EffectiveTypesWithProposals(ctx, base)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}

	if len(merged.FactTypes) != len(base.FactTypes) {
		t.Errorf("expected %d fact types unchanged, got %d", len(base.FactTypes), len(merged.FactTypes))
	}
}

func TestReviewTaxonomy_NoSignals(t *testing.T) {
	store, rawDB := openTestDB(t)
	ctx := context.Background()

	sender := &mockSender{response: &provider.Response{
		Content: "[]", ProviderName: "mock", Model: "test",
	}}

	evolver, err := NewEvolver(sender, store, rawDB, testPromptPath(t), config.DefaultTypes(), slog.Default())
	if err != nil {
		t.Fatalf("create evolver: %v", err)
	}

	proposals, err := evolver.ReviewTaxonomy(ctx)
	if err != nil {
		t.Fatalf("review taxonomy: %v", err)
	}

	if len(proposals) != 0 {
		t.Errorf("expected 0 proposals with no signals, got %d", len(proposals))
	}
	if sender.calls != 0 {
		t.Errorf("expected 0 LLM calls with no signals, got %d", sender.calls)
	}
}

func TestReviewTaxonomy_InvalidJSON_ReturnsNilNotError(t *testing.T) {
	store, rawDB := openTestDB(t)
	ctx := context.Background()

	seedFactsWithType(t, store, "custom_metric", 0.8, 15)

	sc := NewSignalCollector(rawDB, store, config.DefaultTypes(), slog.Default())
	sc.CollectAll(ctx)

	sender := &mockSender{response: &provider.Response{
		Content: "this is not valid JSON at all", ProviderName: "mock", Model: "test",
	}}

	evolver, err := NewEvolver(sender, store, rawDB, testPromptPath(t), config.DefaultTypes(), slog.Default())
	if err != nil {
		t.Fatalf("create evolver: %v", err)
	}

	proposals, err := evolver.ReviewTaxonomy(ctx)
	if err != nil {
		t.Fatalf("expected nil error on invalid JSON, got: %v", err)
	}
	if proposals != nil {
		t.Errorf("expected nil proposals on invalid JSON, got %d", len(proposals))
	}
}

// --- computeTypeCentroid ---

func openTestDBWithVec(t *testing.T, dims int) (db.Store, *sql.DB) {
	t.Helper()
	store, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := store.EnsureVecTable(context.Background(), dims); err != nil {
		t.Fatalf("ensure vec table: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store, store.RawDB()
}

// --- cosineSimilarity ---

func TestCosineSimilarity_IdenticalVectors_Returns1(t *testing.T) {
	a := []float32{1.0, 0.0, 0.0}
	b := []float32{1.0, 0.0, 0.0}
	got := cosineSimilarity(a, b)
	if got < 0.999 || got > 1.001 {
		t.Errorf("cosineSimilarity(identical) = %f, want ~1.0", got)
	}
}

func TestCosineSimilarity_OrthogonalVectors_Returns0(t *testing.T) {
	a := []float32{1.0, 0.0, 0.0}
	b := []float32{0.0, 1.0, 0.0}
	got := cosineSimilarity(a, b)
	if got < -0.001 || got > 0.001 {
		t.Errorf("cosineSimilarity(orthogonal) = %f, want ~0.0", got)
	}
}

// --- test helpers ---

func seedFactsWithEmbedding(t *testing.T, store db.Store, factType model.FactType, embeddings [][]float32) {
	t.Helper()
	now := time.Now()
	for i, emb := range embeddings {
		f := model.Fact{
			ID:         db.NewID(),
			Source:     model.Source{TranscriptFile: "test.md"},
			FactType:   factType,
			Subject:    fmt.Sprintf("subject-%d", i),
			Content:    fmt.Sprintf("Fact %d of type %s.", i, factType),
			Confidence: 0.9,
			CreatedAt:  now.Add(time.Duration(i) * time.Second),
		}
		if err := store.CreateFact(context.Background(), &f); err != nil {
			t.Fatalf("seed fact: %v", err)
		}
		if err := store.UpdateFactEmbedding(context.Background(), f.ID, emb, "test-model"); err != nil {
			t.Fatalf("seed embedding: %v", err)
		}
	}
}

func TestComputeTypeCentroid_ReturnsAverage(t *testing.T) {
	store, rawDB := openTestDBWithVec(t, 4)
	ctx := context.Background()

	seedFactsWithEmbedding(t, store, "project", [][]float32{
		{1.0, 0.0, 0.0, 0.0},
		{0.0, 1.0, 0.0, 0.0},
	})

	sender := &mockSender{response: &provider.Response{Content: "[]"}}
	evolver, _ := NewEvolver(sender, store, rawDB, testPromptPath(t), config.DefaultTypes(), slog.Default())

	centroid, count, err := evolver.computeTypeCentroid(ctx, "project")
	if err != nil {
		t.Fatalf("computeTypeCentroid: %v", err)
	}
	if count != 2 {
		t.Errorf("expected count 2, got %d", count)
	}
	if len(centroid) != 4 {
		t.Fatalf("expected 4 dimensions, got %d", len(centroid))
	}
	if centroid[0] < 0.49 || centroid[0] > 0.51 {
		t.Errorf("centroid[0] = %f, want ~0.5", centroid[0])
	}
	if centroid[1] < 0.49 || centroid[1] > 0.51 {
		t.Errorf("centroid[1] = %f, want ~0.5", centroid[1])
	}
}

func TestComputeTypeCentroid_NoEmbeddings_ReturnsNil(t *testing.T) {
	store, rawDB := openTestDBWithVec(t, 4)
	ctx := context.Background()

	seedFactsWithType(t, store, "project", 0.9, 3)

	sender := &mockSender{response: &provider.Response{Content: "[]"}}
	evolver, _ := NewEvolver(sender, store, rawDB, testPromptPath(t), config.DefaultTypes(), slog.Default())

	centroid, count, err := evolver.computeTypeCentroid(ctx, "project")
	if err != nil {
		t.Fatalf("computeTypeCentroid: %v", err)
	}
	if centroid != nil {
		t.Errorf("expected nil centroid, got %v", centroid)
	}
	if count != 0 {
		t.Errorf("expected count 0, got %d", count)
	}
}

func TestComputeTypeCentroid_NoFacts_ReturnsNil(t *testing.T) {
	store, rawDB := openTestDBWithVec(t, 4)
	ctx := context.Background()

	sender := &mockSender{response: &provider.Response{Content: "[]"}}
	evolver, _ := NewEvolver(sender, store, rawDB, testPromptPath(t), config.DefaultTypes(), slog.Default())

	centroid, count, err := evolver.computeTypeCentroid(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("computeTypeCentroid: %v", err)
	}
	if centroid != nil {
		t.Errorf("expected nil centroid, got %v", centroid)
	}
	if count != 0 {
		t.Errorf("expected count 0, got %d", count)
	}
}

// --- validateMerge ---

func TestValidateMerge_SimilarTypes_Validated(t *testing.T) {
	store, rawDB := openTestDBWithVec(t, 4)
	ctx := context.Background()

	seedFactsWithEmbedding(t, store, "project_info", [][]float32{
		{0.9, 0.1, 0.0, 0.0},
		{0.8, 0.2, 0.0, 0.0},
	})
	seedFactsWithEmbedding(t, store, "project", [][]float32{
		{0.85, 0.15, 0.0, 0.0},
		{0.95, 0.05, 0.0, 0.0},
	})

	sender := &mockSender{response: &provider.Response{
		Content: `{"should_merge": true, "reason": "types are semantically equivalent"}`,
	}}
	evolver, _ := NewEvolver(sender, store, rawDB, testPromptPath(t), config.DefaultTypes(), slog.Default())

	p := db.TaxonomyProposal{
		ID: db.NewID(), Action: "merge", TypeCategory: "fact", TypeName: "project_info",
		Definition: `{"merge_into":"project"}`, Rationale: "overlap",
		Status: "proposed", ShadowResults: "{}", SignalIDs: "[]",
		CreatedAt: time.Now().UTC(),
	}
	store.CreateTaxonomyProposal(ctx, &p)

	if err := evolver.validateMerge(ctx, p); err != nil {
		t.Fatalf("validateMerge: %v", err)
	}

	validated, _ := store.ListTaxonomyProposals(ctx, "validated", 10)
	if len(validated) != 1 {
		t.Fatalf("expected 1 validated, got %d", len(validated))
	}
	if sender.calls != 1 {
		t.Errorf("expected 1 LLM call, got %d", sender.calls)
	}
}

func TestValidateMerge_DissimilarTypes_Rejected(t *testing.T) {
	store, rawDB := openTestDBWithVec(t, 4)
	ctx := context.Background()

	seedFactsWithEmbedding(t, store, "project_info", [][]float32{
		{1.0, 0.0, 0.0, 0.0},
	})
	seedFactsWithEmbedding(t, store, "context", [][]float32{
		{0.0, 0.0, 0.0, 1.0},
	})

	sender := &mockSender{response: &provider.Response{Content: `{}`}}
	evolver, _ := NewEvolver(sender, store, rawDB, testPromptPath(t), config.DefaultTypes(), slog.Default())

	p := db.TaxonomyProposal{
		ID: db.NewID(), Action: "merge", TypeCategory: "fact", TypeName: "project_info",
		Definition: `{"merge_into":"context"}`, Rationale: "overlap",
		Status: "proposed", ShadowResults: "{}", SignalIDs: "[]",
		CreatedAt: time.Now().UTC(),
	}
	store.CreateTaxonomyProposal(ctx, &p)

	if err := evolver.validateMerge(ctx, p); err != nil {
		t.Fatalf("validateMerge: %v", err)
	}

	rejected, _ := store.ListTaxonomyProposals(ctx, "rejected", 10)
	if len(rejected) != 1 {
		t.Fatalf("expected 1 rejected, got %d", len(rejected))
	}
	if sender.calls != 0 {
		t.Errorf("expected 0 LLM calls (rejected by embedding), got %d", sender.calls)
	}
}

func TestValidateMerge_NoEmbeddings_FallsBackToLLM(t *testing.T) {
	store, rawDB := openTestDB(t)
	ctx := context.Background()

	seedFactsWithType(t, store, "project_info", 0.9, 3)
	seedFactsWithType(t, store, "project", 0.9, 3)

	sender := &mockSender{response: &provider.Response{
		Content: `{"should_merge": true, "reason": "same concept"}`,
	}}
	evolver, _ := NewEvolver(sender, store, rawDB, testPromptPath(t), config.DefaultTypes(), slog.Default())

	p := db.TaxonomyProposal{
		ID: db.NewID(), Action: "merge", TypeCategory: "fact", TypeName: "project_info",
		Definition: `{"merge_into":"project"}`, Rationale: "overlap",
		Status: "proposed", ShadowResults: "{}", SignalIDs: "[]",
		CreatedAt: time.Now().UTC(),
	}
	store.CreateTaxonomyProposal(ctx, &p)

	if err := evolver.validateMerge(ctx, p); err != nil {
		t.Fatalf("validateMerge: %v", err)
	}

	validated, _ := store.ListTaxonomyProposals(ctx, "validated", 10)
	if len(validated) != 1 {
		t.Fatalf("expected 1 validated (LLM fallback), got %d", len(validated))
	}
	if sender.calls != 1 {
		t.Errorf("expected 1 LLM call, got %d", sender.calls)
	}
}

func TestValidateMerge_LLMRejects(t *testing.T) {
	store, rawDB := openTestDBWithVec(t, 4)
	ctx := context.Background()

	seedFactsWithEmbedding(t, store, "project_info", [][]float32{
		{0.9, 0.1, 0.0, 0.0},
	})
	seedFactsWithEmbedding(t, store, "project", [][]float32{
		{0.85, 0.15, 0.0, 0.0},
	})

	sender := &mockSender{response: &provider.Response{
		Content: `{"should_merge": false, "reason": "project_info has distinct scope"}`,
	}}
	evolver, _ := NewEvolver(sender, store, rawDB, testPromptPath(t), config.DefaultTypes(), slog.Default())

	p := db.TaxonomyProposal{
		ID: db.NewID(), Action: "merge", TypeCategory: "fact", TypeName: "project_info",
		Definition: `{"merge_into":"project"}`, Rationale: "overlap",
		Status: "proposed", ShadowResults: "{}", SignalIDs: "[]",
		CreatedAt: time.Now().UTC(),
	}
	store.CreateTaxonomyProposal(ctx, &p)

	if err := evolver.validateMerge(ctx, p); err != nil {
		t.Fatalf("validateMerge: %v", err)
	}

	rejected, _ := store.ListTaxonomyProposals(ctx, "rejected", 10)
	if len(rejected) != 1 {
		t.Fatalf("expected 1 rejected (LLM said no), got %d", len(rejected))
	}
}

// --- validateRename ---

func TestValidateRename_LLMConfirms_Validated(t *testing.T) {
	store, rawDB := openTestDB(t)
	ctx := context.Background()

	seedFactsWithType(t, store, "bio", 0.9, 5)

	sender := &mockSender{response: &provider.Response{
		Content: `{"should_rename": true, "reason": "biography is more descriptive"}`,
	}}
	evolver, _ := NewEvolver(sender, store, rawDB, testPromptPath(t), config.DefaultTypes(), slog.Default())

	p := db.TaxonomyProposal{
		ID: db.NewID(), Action: "rename", TypeCategory: "fact", TypeName: "bio",
		Definition: `{"rename_to":"biography"}`, Rationale: "more descriptive name",
		Status: "proposed", ShadowResults: "{}", SignalIDs: "[]",
		CreatedAt: time.Now().UTC(),
	}
	store.CreateTaxonomyProposal(ctx, &p)

	if err := evolver.validateRename(ctx, p); err != nil {
		t.Fatalf("validateRename: %v", err)
	}

	validated, _ := store.ListTaxonomyProposals(ctx, "validated", 10)
	if len(validated) != 1 {
		t.Fatalf("expected 1 validated, got %d", len(validated))
	}
	if sender.calls != 1 {
		t.Errorf("expected 1 LLM call, got %d", sender.calls)
	}
}

func TestValidateRename_LLMRejects_Rejected(t *testing.T) {
	store, rawDB := openTestDB(t)
	ctx := context.Background()

	seedFactsWithType(t, store, "bio", 0.9, 5)

	sender := &mockSender{response: &provider.Response{
		Content: `{"should_rename": false, "reason": "bio is already clear"}`,
	}}
	evolver, _ := NewEvolver(sender, store, rawDB, testPromptPath(t), config.DefaultTypes(), slog.Default())

	p := db.TaxonomyProposal{
		ID: db.NewID(), Action: "rename", TypeCategory: "fact", TypeName: "bio",
		Definition: `{"rename_to":"biography"}`, Rationale: "more descriptive name",
		Status: "proposed", ShadowResults: "{}", SignalIDs: "[]",
		CreatedAt: time.Now().UTC(),
	}
	store.CreateTaxonomyProposal(ctx, &p)

	if err := evolver.validateRename(ctx, p); err != nil {
		t.Fatalf("validateRename: %v", err)
	}

	rejected, _ := store.ListTaxonomyProposals(ctx, "rejected", 10)
	if len(rejected) != 1 {
		t.Fatalf("expected 1 rejected, got %d", len(rejected))
	}
}

func TestValidateRename_NoFacts_Rejected(t *testing.T) {
	store, rawDB := openTestDB(t)
	ctx := context.Background()

	sender := &mockSender{response: &provider.Response{Content: `{}`}}
	evolver, _ := NewEvolver(sender, store, rawDB, testPromptPath(t), config.DefaultTypes(), slog.Default())

	p := db.TaxonomyProposal{
		ID: db.NewID(), Action: "rename", TypeCategory: "fact", TypeName: "bio",
		Definition: `{"rename_to":"biography"}`, Rationale: "more descriptive name",
		Status: "proposed", ShadowResults: "{}", SignalIDs: "[]",
		CreatedAt: time.Now().UTC(),
	}
	store.CreateTaxonomyProposal(ctx, &p)

	if err := evolver.validateRename(ctx, p); err != nil {
		t.Fatalf("validateRename: %v", err)
	}

	rejected, _ := store.ListTaxonomyProposals(ctx, "rejected", 10)
	if len(rejected) != 1 {
		t.Fatalf("expected 1 rejected (no facts), got %d", len(rejected))
	}
	if sender.calls != 0 {
		t.Errorf("expected 0 LLM calls (no facts), got %d", sender.calls)
	}
}

// --- EffectiveTypesWithProposals: merge/rename ---

func TestEffectiveTypes_MergeApplied_SourceRemoved(t *testing.T) {
	store, rawDB := openTestDB(t)
	ctx := context.Background()

	now := time.Now().UTC()
	store.CreateTaxonomyProposal(ctx, &db.TaxonomyProposal{
		ID: db.NewID(), Action: "merge", TypeCategory: "fact", TypeName: "bio",
		Definition: `{"merge_into":"contact"}`, Rationale: "overlap",
		Status: "applied", ShadowResults: "{}", SignalIDs: "[]",
		CreatedAt: now, ResolvedAt: &now,
	})

	base := config.DefaultTypes()
	sender := &mockSender{response: &provider.Response{Content: "[]"}}
	evolver, _ := NewEvolver(sender, store, rawDB, testPromptPath(t), base, slog.Default())

	merged, err := evolver.EffectiveTypesWithProposals(ctx, base)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}

	for _, ft := range merged.FactTypes {
		if ft.Name == "bio" {
			t.Error("expected 'bio' (source) to be removed from merged FactTypes")
		}
	}

	foundTarget := false
	for _, ft := range merged.FactTypes {
		if ft.Name == "contact" {
			foundTarget = true
			break
		}
	}
	if !foundTarget {
		t.Error("expected 'contact' (target) to remain in merged FactTypes")
	}

	if len(merged.FactTypes) != len(base.FactTypes)-1 {
		t.Errorf("expected %d fact types, got %d", len(base.FactTypes)-1, len(merged.FactTypes))
	}
}

func TestEffectiveTypes_RenameApplied_NameChanged(t *testing.T) {
	store, rawDB := openTestDB(t)
	ctx := context.Background()

	now := time.Now().UTC()
	store.CreateTaxonomyProposal(ctx, &db.TaxonomyProposal{
		ID: db.NewID(), Action: "rename", TypeCategory: "fact", TypeName: "bio",
		Definition:    `{"rename_to":"biography","name":"bio","description":"Biographical information"}`,
		Rationale:     "more descriptive", Status: "applied",
		ShadowResults: "{}", SignalIDs: "[]",
		CreatedAt: now, ResolvedAt: &now,
	})

	base := config.DefaultTypes()
	sender := &mockSender{response: &provider.Response{Content: "[]"}}
	evolver, _ := NewEvolver(sender, store, rawDB, testPromptPath(t), base, slog.Default())

	merged, err := evolver.EffectiveTypesWithProposals(ctx, base)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}

	for _, ft := range merged.FactTypes {
		if ft.Name == "bio" {
			t.Error("expected 'bio' to be renamed, but it still exists")
		}
	}

	found := false
	for _, ft := range merged.FactTypes {
		if ft.Name == "biography" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected 'biography' in merged FactTypes after rename")
	}

	if len(merged.FactTypes) != len(base.FactTypes) {
		t.Errorf("expected same count %d after rename, got %d", len(base.FactTypes), len(merged.FactTypes))
	}
}
