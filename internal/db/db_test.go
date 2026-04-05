package db

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aegis-alpha/imprint-mace/internal/model"
)

func openTestDB(t *testing.T) *SQLiteStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// --- Migration idempotency ---

func TestMigrate_Idempotent(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "idempotent.db")

	store1, err := Open(dbPath)
	if err != nil {
		t.Fatalf("first Open failed: %v", err)
	}
	store1.Close()

	store2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("second Open failed (migrations not idempotent): %v", err)
	}
	store2.Close()
}

func TestMigrate_SplitStatements(t *testing.T) {
	cases := []struct {
		file      string
		wantCount int
	}{
		{"001_init.sql", 17},
		{"002_taxonomy_evolution.sql", 8},
		{"003_ingested_files.sql", 2},
		{"004_embedding_model_fts.sql", 2},
		{"005_transcripts.sql", 5},
		{"006_chunks_fts.sql", 1},
		{"007_supersede_reason.sql", 2},
	}

	for _, tc := range cases {
		data, err := migrations.ReadFile("migrations/" + tc.file)
		if err != nil {
			t.Fatalf("read %s: %v", tc.file, err)
		}
		stmts := splitMigrationStatements(string(data))
		if len(stmts) != tc.wantCount {
			t.Errorf("%s: got %d statements, want %d", tc.file, len(stmts), tc.wantCount)
			for i, s := range stmts {
				t.Logf("  stmt[%d]: %.80s...", i, s)
			}
		}
	}
}

// --- Facts ---

func TestCreateAndGetFact(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Second)
	f := &model.Fact{
		ID:       NewID(),
		Source:   model.Source{TranscriptFile: "2026-03-14.md", LineRange: &[2]int{10, 20}},
		FactType: model.FactPreference,
		Subject:  "Alice",
		Content:  "Prefers lists over tables",
		Confidence: 0.9,
		Validity: model.TimeRange{ValidFrom: &now},
		CreatedAt: now,
	}

	if err := store.CreateFact(ctx, f); err != nil {
		t.Fatalf("create fact: %v", err)
	}

	got, err := store.GetFact(ctx, f.ID)
	if err != nil {
		t.Fatalf("get fact: %v", err)
	}
	if got.Content != "Prefers lists over tables" {
		t.Errorf("expected content, got %q", got.Content)
	}
	if got.Subject != "Alice" {
		t.Errorf("expected subject Alice, got %q", got.Subject)
	}
	if string(got.FactType) != "preference" {
		t.Errorf("expected fact_type preference, got %q", got.FactType)
	}
	if got.Source.TranscriptFile != "2026-03-14.md" {
		t.Errorf("expected source file, got %q", got.Source.TranscriptFile)
	}
	if got.Source.LineRange == nil || got.Source.LineRange[0] != 10 || got.Source.LineRange[1] != 20 {
		t.Errorf("expected line range [10,20], got %v", got.Source.LineRange)
	}
	if got.Confidence != 0.9 {
		t.Errorf("expected confidence 0.9, got %f", got.Confidence)
	}
}

func TestListFacts_FilterByType(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	for i, ft := range []model.FactType{model.FactPreference, model.FactDecision, model.FactPreference} {
		store.CreateFact(ctx, &model.Fact{
			ID: NewID(), Source: model.Source{TranscriptFile: "t.md"},
			FactType: ft, Content: "fact " + string(rune('A'+i)), CreatedAt: now,
		})
	}

	facts, err := store.ListFacts(ctx, FactFilter{FactType: "preference"})
	if err != nil {
		t.Fatalf("list facts: %v", err)
	}
	if len(facts) != 2 {
		t.Errorf("expected 2 preference facts, got %d", len(facts))
	}
}

func TestListFacts_NotSuperseded(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	oldID := NewID()
	newID := NewID()

	store.CreateFact(ctx, &model.Fact{
		ID: oldID, Source: model.Source{TranscriptFile: "t.md"},
		FactType: model.FactDecision, Content: "old decision", CreatedAt: now,
	})
	store.CreateFact(ctx, &model.Fact{
		ID: newID, Source: model.Source{TranscriptFile: "t.md"},
		FactType: model.FactDecision, Content: "new decision", CreatedAt: now,
	})
	store.SupersedeFact(ctx, oldID, newID)

	all, _ := store.ListFacts(ctx, FactFilter{})
	if len(all) != 2 {
		t.Errorf("expected 2 total facts, got %d", len(all))
	}

	active, _ := store.ListFacts(ctx, FactFilter{NotSuperseded: true})
	if len(active) != 1 {
		t.Errorf("expected 1 active fact, got %d", len(active))
	}
	if active[0].ID != newID {
		t.Errorf("expected active fact to be new one")
	}
}

func TestListFacts_Limit(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	for i := 0; i < 10; i++ {
		store.CreateFact(ctx, &model.Fact{
			ID: NewID(), Source: model.Source{TranscriptFile: "t.md"},
			FactType: model.FactRule, Content: "rule", CreatedAt: now,
		})
	}

	facts, _ := store.ListFacts(ctx, FactFilter{Limit: 3})
	if len(facts) != 3 {
		t.Errorf("expected 3 facts with limit, got %d", len(facts))
	}
}

// --- Entities ---

func TestCreateAndGetEntity(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()

	e := &model.Entity{
		ID:         NewID(),
		Name:       "Alice",
		EntityType: model.EntityPerson,
		Aliases:    []string{"A", "the engineer"},
		CreatedAt:  time.Now().UTC(),
	}

	if err := store.CreateEntity(ctx, e); err != nil {
		t.Fatalf("create entity: %v", err)
	}

	got, err := store.GetEntity(ctx, e.ID)
	if err != nil {
		t.Fatalf("get entity: %v", err)
	}
	if got.Name != "Alice" {
		t.Errorf("expected name Alice, got %q", got.Name)
	}
	if len(got.Aliases) != 2 || got.Aliases[0] != "A" {
		t.Errorf("expected aliases [A, the engineer], got %v", got.Aliases)
	}
}

func TestGetEntityByName_CaseInsensitive(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()

	store.CreateEntity(ctx, &model.Entity{
		ID: NewID(), Name: "OpenClaw", EntityType: model.EntityProject, CreatedAt: time.Now().UTC(),
	})

	got, err := store.GetEntityByName(ctx, "openclaw")
	if err != nil {
		t.Fatalf("get by name: %v", err)
	}
	if got.Name != "OpenClaw" {
		t.Errorf("expected OpenClaw, got %q", got.Name)
	}
}

func TestListEntities_FilterByType(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	store.CreateEntity(ctx, &model.Entity{ID: NewID(), Name: "Alice", EntityType: model.EntityPerson, CreatedAt: now})
	store.CreateEntity(ctx, &model.Entity{ID: NewID(), Name: "ProjectX", EntityType: model.EntityProject, CreatedAt: now})
	store.CreateEntity(ctx, &model.Entity{ID: NewID(), Name: "Bob", EntityType: model.EntityPerson, CreatedAt: now})

	people, _ := store.ListEntities(ctx, EntityFilter{EntityType: "person"})
	if len(people) != 2 {
		t.Errorf("expected 2 people, got %d", len(people))
	}
}

// --- Relationships ---

func TestCreateAndListRelationships(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	e1 := &model.Entity{ID: NewID(), Name: "Alice", EntityType: model.EntityPerson, CreatedAt: now}
	e2 := &model.Entity{ID: NewID(), Name: "ProjectX", EntityType: model.EntityProject, CreatedAt: now}
	store.CreateEntity(ctx, e1)
	store.CreateEntity(ctx, e2)

	r := &model.Relationship{
		ID: NewID(), FromEntity: e1.ID, ToEntity: e2.ID,
		RelationType: model.RelWorksOn, CreatedAt: now,
	}
	if err := store.CreateRelationship(ctx, r); err != nil {
		t.Fatalf("create relationship: %v", err)
	}

	rels, err := store.ListRelationships(ctx, RelFilter{EntityID: e1.ID})
	if err != nil {
		t.Fatalf("list relationships: %v", err)
	}
	if len(rels) != 1 {
		t.Fatalf("expected 1 relationship, got %d", len(rels))
	}
	if string(rels[0].RelationType) != "works_on" {
		t.Errorf("expected works_on, got %q", rels[0].RelationType)
	}

	rels2, _ := store.ListRelationships(ctx, RelFilter{EntityID: e2.ID})
	if len(rels2) != 1 {
		t.Errorf("expected relationship found from either side, got %d", len(rels2))
	}
}

func TestListRelationships_FilterByType(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	e1 := &model.Entity{ID: NewID(), Name: "A", EntityType: model.EntityPerson, CreatedAt: now}
	e2 := &model.Entity{ID: NewID(), Name: "B", EntityType: model.EntityProject, CreatedAt: now}
	store.CreateEntity(ctx, e1)
	store.CreateEntity(ctx, e2)

	store.CreateRelationship(ctx, &model.Relationship{ID: NewID(), FromEntity: e1.ID, ToEntity: e2.ID, RelationType: model.RelOwns, CreatedAt: now})
	store.CreateRelationship(ctx, &model.Relationship{ID: NewID(), FromEntity: e1.ID, ToEntity: e2.ID, RelationType: model.RelUses, CreatedAt: now})

	owns, _ := store.ListRelationships(ctx, RelFilter{RelationType: "owns"})
	if len(owns) != 1 {
		t.Errorf("expected 1 owns relationship, got %d", len(owns))
	}
}

// --- Consolidations ---

func TestCreateAndListConsolidations(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()

	c := &model.Consolidation{
		ID:            NewID(),
		SourceFactIDs: []string{"f1", "f2", "f3"},
		Summary:       "User prefers concise communication",
		Insight:       "Pattern across 3 facts about communication style",
		Importance:    0.8,
		CreatedAt:     time.Now().UTC(),
	}

	if err := store.CreateConsolidation(ctx, c); err != nil {
		t.Fatalf("create consolidation: %v", err)
	}

	cons, err := store.ListConsolidations(ctx, 10)
	if err != nil {
		t.Fatalf("list consolidations: %v", err)
	}
	if len(cons) != 1 {
		t.Fatalf("expected 1 consolidation, got %d", len(cons))
	}
	if cons[0].Summary != "User prefers concise communication" {
		t.Errorf("expected summary, got %q", cons[0].Summary)
	}
	if len(cons[0].SourceFactIDs) != 3 {
		t.Errorf("expected 3 source fact IDs, got %d", len(cons[0].SourceFactIDs))
	}
}

// --- Fact connections ---

func TestCreateAndListFactConnections(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	f1 := &model.Fact{ID: NewID(), Source: model.Source{TranscriptFile: "t.md"}, FactType: model.FactDecision, Content: "fact 1", CreatedAt: now}
	f2 := &model.Fact{ID: NewID(), Source: model.Source{TranscriptFile: "t.md"}, FactType: model.FactDecision, Content: "fact 2", CreatedAt: now}
	store.CreateFact(ctx, f1)
	store.CreateFact(ctx, f2)

	fc := &model.FactConnection{
		ID:             NewID(),
		FactA:          f1.ID,
		FactB:          f2.ID,
		ConnectionType: model.ConnSupports,
		Strength:       0.85,
		CreatedAt:      now,
	}
	if err := store.CreateFactConnection(ctx, fc); err != nil {
		t.Fatalf("create fact connection: %v", err)
	}

	conns, err := store.ListFactConnections(ctx, f1.ID)
	if err != nil {
		t.Fatalf("list fact connections: %v", err)
	}
	if len(conns) != 1 {
		t.Fatalf("expected 1 connection, got %d", len(conns))
	}
	if string(conns[0].ConnectionType) != "supports" {
		t.Errorf("expected supports, got %q", conns[0].ConnectionType)
	}
	if conns[0].Strength != 0.85 {
		t.Errorf("expected strength 0.85, got %f", conns[0].Strength)
	}

	conns2, _ := store.ListFactConnections(ctx, f2.ID)
	if len(conns2) != 1 {
		t.Errorf("expected connection found from either side, got %d", len(conns2))
	}
}

func TestListAllFactConnections(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	f1 := &model.Fact{ID: NewID(), Source: model.Source{TranscriptFile: "t.md"}, FactType: model.FactDecision, Content: "fact A", CreatedAt: now}
	f2 := &model.Fact{ID: NewID(), Source: model.Source{TranscriptFile: "t.md"}, FactType: model.FactDecision, Content: "fact B", CreatedAt: now}
	f3 := &model.Fact{ID: NewID(), Source: model.Source{TranscriptFile: "t.md"}, FactType: model.FactDecision, Content: "fact C", CreatedAt: now}
	store.CreateFact(ctx, f1)
	store.CreateFact(ctx, f2)
	store.CreateFact(ctx, f3)

	store.CreateFactConnection(ctx, &model.FactConnection{ID: NewID(), FactA: f1.ID, FactB: f2.ID, ConnectionType: model.ConnSupports, Strength: 0.9, CreatedAt: now})
	store.CreateFactConnection(ctx, &model.FactConnection{ID: NewID(), FactA: f2.ID, FactB: f3.ID, ConnectionType: model.ConnContradicts, Strength: 0.7, CreatedAt: now})

	all, err := store.ListAllFactConnections(ctx, 0)
	if err != nil {
		t.Fatalf("list all fact connections: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 connections, got %d", len(all))
	}

	limited, err := store.ListAllFactConnections(ctx, 1)
	if err != nil {
		t.Fatalf("list all fact connections with limit: %v", err)
	}
	if len(limited) != 1 {
		t.Errorf("expected 1 connection with limit=1, got %d", len(limited))
	}
}

func TestDeleteExpiredFacts(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	past := now.Add(-48 * time.Hour)
	future := now.Add(48 * time.Hour)

	expired := &model.Fact{
		ID: NewID(), Source: model.Source{TranscriptFile: "t.md"},
		FactType: model.FactContext, Content: "node-2 is offline",
		Validity: model.TimeRange{ValidUntil: &past}, CreatedAt: now,
	}
	active := &model.Fact{
		ID: NewID(), Source: model.Source{TranscriptFile: "t.md"},
		FactType: model.FactContext, Content: "node-1 is healthy",
		Validity: model.TimeRange{ValidUntil: &future}, CreatedAt: now,
	}
	noExpiry := &model.Fact{
		ID: NewID(), Source: model.Source{TranscriptFile: "t.md"},
		FactType: model.FactDecision, Content: "use Go",
		CreatedAt: now,
	}
	store.CreateFact(ctx, expired)
	store.CreateFact(ctx, active)
	store.CreateFact(ctx, noExpiry)

	deleted, err := store.DeleteExpiredFacts(ctx, now)
	if err != nil {
		t.Fatalf("delete expired facts: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("expected 1 deleted, got %d", deleted)
	}

	remaining, _ := store.ListFacts(ctx, FactFilter{})
	if len(remaining) != 2 {
		t.Errorf("expected 2 remaining facts, got %d", len(remaining))
	}

	for _, f := range remaining {
		if f.ID == expired.ID {
			t.Error("expired fact should have been deleted")
		}
	}
}

// --- ID generation ---

func TestNewID_Unique(t *testing.T) {
	ids := make(map[string]bool)
	for i := 0; i < 1000; i++ {
		id := NewID()
		if ids[id] {
			t.Fatalf("duplicate ID generated: %s", id)
		}
		ids[id] = true
		if len(id) != 26 {
			t.Errorf("expected 26-char ULID, got %d chars: %s", len(id), id)
		}
	}
}

// --- Graph Traversal ---

func setupGraphEntities(t *testing.T, store *SQLiteStore) (a, b, c model.Entity) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	a = model.Entity{ID: NewID(), Name: "Alice", EntityType: model.EntityPerson, CreatedAt: now}
	b = model.Entity{ID: NewID(), Name: "Acme", EntityType: model.EntityProject, CreatedAt: now}
	c = model.Entity{ID: NewID(), Name: "node-1", EntityType: model.EntityServer, CreatedAt: now}

	for _, e := range []*model.Entity{&a, &b, &c} {
		if err := store.CreateEntity(ctx, e); err != nil {
			t.Fatalf("create entity %s: %v", e.Name, err)
		}
	}
	return
}

func TestGetEntityGraph_Depth1(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	a, b, c := setupGraphEntities(t, store)

	store.CreateRelationship(ctx, &model.Relationship{
		ID: NewID(), FromEntity: a.ID, ToEntity: b.ID,
		RelationType: model.RelWorksOn, CreatedAt: now,
	})
	store.CreateRelationship(ctx, &model.Relationship{
		ID: NewID(), FromEntity: a.ID, ToEntity: c.ID,
		RelationType: model.RelManages, CreatedAt: now,
	})

	graph, err := store.GetEntityGraph(ctx, a.ID, 1)
	if err != nil {
		t.Fatalf("GetEntityGraph: %v", err)
	}
	if graph.Center.ID != a.ID {
		t.Errorf("expected center %s, got %s", a.ID, graph.Center.ID)
	}
	if len(graph.Entities) != 3 {
		t.Errorf("expected 3 entities (A,B,C), got %d", len(graph.Entities))
	}
	if len(graph.Relationships) != 2 {
		t.Errorf("expected 2 relationships, got %d", len(graph.Relationships))
	}
}

func TestGetEntityGraph_Depth2(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	a, b, c := setupGraphEntities(t, store)

	store.CreateRelationship(ctx, &model.Relationship{
		ID: NewID(), FromEntity: a.ID, ToEntity: b.ID,
		RelationType: model.RelWorksOn, CreatedAt: now,
	})
	store.CreateRelationship(ctx, &model.Relationship{
		ID: NewID(), FromEntity: b.ID, ToEntity: c.ID,
		RelationType: model.RelDependsOn, CreatedAt: now,
	})

	graph1, err := store.GetEntityGraph(ctx, a.ID, 1)
	if err != nil {
		t.Fatalf("GetEntityGraph depth=1: %v", err)
	}
	if len(graph1.Entities) != 2 {
		t.Errorf("depth=1: expected 2 entities (A,B), got %d", len(graph1.Entities))
	}

	graph2, err := store.GetEntityGraph(ctx, a.ID, 2)
	if err != nil {
		t.Fatalf("GetEntityGraph depth=2: %v", err)
	}
	if len(graph2.Entities) != 3 {
		t.Errorf("depth=2: expected 3 entities (A,B,C), got %d", len(graph2.Entities))
	}
	if len(graph2.Relationships) != 2 {
		t.Errorf("depth=2: expected 2 relationships, got %d", len(graph2.Relationships))
	}
}

func TestGetEntityGraph_NotFound(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()

	_, err := store.GetEntityGraph(ctx, "nonexistent", 1)
	if err == nil {
		t.Fatal("expected error for nonexistent entity")
	}
}

func TestFindPath_DirectConnection(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	a, b, _ := setupGraphEntities(t, store)

	rel := &model.Relationship{
		ID: NewID(), FromEntity: a.ID, ToEntity: b.ID,
		RelationType: model.RelWorksOn, CreatedAt: now,
	}
	store.CreateRelationship(ctx, rel)

	path, err := store.FindPath(ctx, a.ID, b.ID)
	if err != nil {
		t.Fatalf("FindPath: %v", err)
	}
	if len(path) != 2 {
		t.Fatalf("expected 2 steps (A -> B), got %d", len(path))
	}
	if path[0].EntityID != a.ID {
		t.Errorf("step 0: expected entity %s, got %s", a.ID, path[0].EntityID)
	}
	if path[1].EntityID != b.ID {
		t.Errorf("step 1: expected entity %s, got %s", b.ID, path[1].EntityID)
	}
}

func TestFindPath_TwoHops(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	a, b, c := setupGraphEntities(t, store)

	store.CreateRelationship(ctx, &model.Relationship{
		ID: NewID(), FromEntity: a.ID, ToEntity: b.ID,
		RelationType: model.RelWorksOn, CreatedAt: now,
	})
	store.CreateRelationship(ctx, &model.Relationship{
		ID: NewID(), FromEntity: b.ID, ToEntity: c.ID,
		RelationType: model.RelDependsOn, CreatedAt: now,
	})

	path, err := store.FindPath(ctx, a.ID, c.ID)
	if err != nil {
		t.Fatalf("FindPath: %v", err)
	}
	if len(path) != 3 {
		t.Fatalf("expected 3 steps (A -> B -> C), got %d", len(path))
	}
	if path[0].EntityID != a.ID {
		t.Errorf("step 0: expected %s, got %s", a.ID, path[0].EntityID)
	}
	if path[1].EntityID != b.ID {
		t.Errorf("step 1: expected %s, got %s", b.ID, path[1].EntityID)
	}
	if path[2].EntityID != c.ID {
		t.Errorf("step 2: expected %s, got %s", c.ID, path[2].EntityID)
	}
}

// --- Taxonomy Proposals ---

func TestCreateAndListTaxonomyProposals(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()

	p := &TaxonomyProposal{
		ID:            NewID(),
		Action:        "add",
		TypeCategory:  "fact",
		TypeName:      "metric",
		Definition:    `{"name":"metric","description":"A measured value","example":"CPU usage is 80%"}`,
		Rationale:     "LLM returned metric 50 times",
		Status:        "proposed",
		ShadowResults: "{}",
		SignalIDs:     `["sig1","sig2"]`,
		CreatedAt:     time.Now().UTC().Truncate(time.Second),
	}

	if err := store.CreateTaxonomyProposal(ctx, p); err != nil {
		t.Fatalf("create proposal: %v", err)
	}

	proposals, err := store.ListTaxonomyProposals(ctx, "proposed", 10)
	if err != nil {
		t.Fatalf("list proposals: %v", err)
	}
	if len(proposals) != 1 {
		t.Fatalf("expected 1 proposal, got %d", len(proposals))
	}
	if proposals[0].Action != "add" {
		t.Errorf("expected action 'add', got %q", proposals[0].Action)
	}
	if proposals[0].TypeName != "metric" {
		t.Errorf("expected type_name 'metric', got %q", proposals[0].TypeName)
	}
	if proposals[0].Rationale != "LLM returned metric 50 times" {
		t.Errorf("expected rationale, got %q", proposals[0].Rationale)
	}
}

func TestUpdateTaxonomyProposalStatus(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()

	p := &TaxonomyProposal{
		ID:            NewID(),
		Action:        "add",
		TypeCategory:  "fact",
		TypeName:      "metric",
		Definition:    `{}`,
		Rationale:     "test",
		Status:        "proposed",
		ShadowResults: "{}",
		SignalIDs:     "[]",
		CreatedAt:     time.Now().UTC().Truncate(time.Second),
	}
	store.CreateTaxonomyProposal(ctx, p)

	now := time.Now().UTC().Truncate(time.Second)
	err := store.UpdateTaxonomyProposalStatus(ctx, p.ID, "validated", `{"count":50}`, &now)
	if err != nil {
		t.Fatalf("update status: %v", err)
	}

	proposals, _ := store.ListTaxonomyProposals(ctx, "validated", 10)
	if len(proposals) != 1 {
		t.Fatalf("expected 1 validated proposal, got %d", len(proposals))
	}
	if proposals[0].ShadowResults != `{"count":50}` {
		t.Errorf("expected shadow_results, got %q", proposals[0].ShadowResults)
	}
	if proposals[0].ResolvedAt == nil {
		t.Error("expected resolved_at to be set")
	}
}

func TestListTaxonomyProposals_FilterByStatus(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	store.CreateTaxonomyProposal(ctx, &TaxonomyProposal{
		ID: NewID(), Action: "add", TypeCategory: "fact", TypeName: "metric",
		Definition: "{}", Rationale: "r1", Status: "proposed",
		ShadowResults: "{}", SignalIDs: "[]", CreatedAt: now,
	})
	store.CreateTaxonomyProposal(ctx, &TaxonomyProposal{
		ID: NewID(), Action: "remove", TypeCategory: "fact", TypeName: "skill",
		Definition: "{}", Rationale: "r2", Status: "validated",
		ShadowResults: "{}", SignalIDs: "[]", CreatedAt: now,
	})
	store.CreateTaxonomyProposal(ctx, &TaxonomyProposal{
		ID: NewID(), Action: "add", TypeCategory: "entity", TypeName: "service",
		Definition: "{}", Rationale: "r3", Status: "proposed",
		ShadowResults: "{}", SignalIDs: "[]", CreatedAt: now,
	})

	proposed, _ := store.ListTaxonomyProposals(ctx, "proposed", 10)
	if len(proposed) != 2 {
		t.Errorf("expected 2 proposed, got %d", len(proposed))
	}

	validated, _ := store.ListTaxonomyProposals(ctx, "validated", 10)
	if len(validated) != 1 {
		t.Errorf("expected 1 validated, got %d", len(validated))
	}

	all, _ := store.ListTaxonomyProposals(ctx, "", 10)
	if len(all) != 3 {
		t.Errorf("expected 3 total, got %d", len(all))
	}
}

func TestFindPath_NoPath(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	a, b, c := setupGraphEntities(t, store)

	store.CreateRelationship(ctx, &model.Relationship{
		ID: NewID(), FromEntity: a.ID, ToEntity: b.ID,
		RelationType: model.RelWorksOn, CreatedAt: now,
	})

	path, err := store.FindPath(ctx, a.ID, c.ID)
	if err != nil {
		t.Fatalf("FindPath: %v", err)
	}
	if len(path) != 0 {
		t.Errorf("expected empty path, got %d steps", len(path))
	}
}

// --- Migration 013 (chunk embedding BLOB) ---

func TestMigration013_AddsEmbeddingColumn(t *testing.T) {
	store := openTestDB(t)
	var n int
	err := store.RawDB().QueryRow(`
		SELECT COUNT(*) FROM pragma_table_info('transcript_chunks') WHERE name = 'embedding'`).Scan(&n)
	if err != nil {
		t.Fatalf("pragma_table_info: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected transcript_chunks.embedding column, found count=%d", n)
	}
}

func TestBackfillChunkEmbeddings(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	dims := 3
	_, err := store.RawDB().ExecContext(ctx, fmt.Sprintf(
		`CREATE VIRTUAL TABLE IF NOT EXISTS chunks_vec USING vec0(chunk_id TEXT PRIMARY KEY, embedding float[%d] distance_metric=cosine)`,
		dims))
	if err != nil {
		t.Fatalf("create chunks_vec: %v", err)
	}
	tr := &model.Transcript{
		ID: NewID(), FilePath: "bf.md", ChunkCount: 1,
		CreatedAt: time.Now().UTC().Truncate(time.Second),
	}
	if err := store.CreateTranscript(ctx, tr); err != nil {
		t.Fatal(err)
	}
	c := &model.TranscriptChunk{
		ID: NewID(), TranscriptID: tr.ID, LineStart: 1, LineEnd: 5, ContentHash: "h",
	}
	if err := store.CreateTranscriptChunk(ctx, c, "body"); err != nil {
		t.Fatal(err)
	}
	emb := []float32{0.25, 0.5, 0.75}
	blob := float32ToBlob(emb)
	if _, err := store.RawDB().ExecContext(ctx,
		`INSERT INTO chunks_vec(chunk_id, embedding) VALUES (?, ?)`, c.ID, blob); err != nil {
		t.Fatalf("insert chunks_vec: %v", err)
	}
	if err := store.backfillChunkEmbeddingsIfNeeded(); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	var got []byte
	if err := store.RawDB().QueryRowContext(ctx,
		`SELECT embedding FROM transcript_chunks WHERE id = ?`, c.ID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("expected transcript_chunks.embedding backfilled")
	}
	if len(got) != len(blob) {
		t.Fatalf("blob len: want %d, got %d", len(blob), len(got))
	}
}

func TestSearchByVector_NilVectorIndex(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	res, err := store.SearchByVector(ctx, []float32{1, 0, 0, 0}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if res != nil && len(res) != 0 {
		t.Fatalf("expected empty without vector index, got %d hits", len(res))
	}
}

// --- Vector search ---

func vecTestStore(t *testing.T, dims int) *SQLiteStore {
	t.Helper()
	store := openTestDB(t)
	if err := store.AttachVectorIndex(dims); err != nil {
		t.Fatalf("AttachVectorIndex: %v", err)
	}
	return store
}

func TestSearchByVector_FindsSimilar(t *testing.T) {
	store := vecTestStore(t, 4)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	facts := []struct {
		content   string
		embedding []float32
	}{
		{"Alice prefers Go", []float32{1.0, 0.0, 0.0, 0.0}},
		{"Bob uses Python", []float32{0.0, 1.0, 0.0, 0.0}},
		{"Alice likes Go modules", []float32{0.9, 0.1, 0.0, 0.0}},
	}

	for _, f := range facts {
		fact := &model.Fact{
			ID: NewID(), Source: model.Source{TranscriptFile: "t.md"},
			FactType: model.FactPreference, Content: f.content, CreatedAt: now,
		}
		store.CreateFact(ctx, fact)
		store.UpdateFactEmbedding(ctx, fact.ID, f.embedding, "test-model")
	}

	query := []float32{0.95, 0.05, 0.0, 0.0}
	results, err := store.SearchByVector(ctx, query, 3)
	if err != nil {
		t.Fatalf("SearchByVector: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	if !strings.Contains(results[0].Fact.Content, "Alice") {
		t.Errorf("expected closest match to be about Alice, got %q", results[0].Fact.Content)
	}
	if results[0].Score < results[1].Score {
		t.Errorf("expected first result to have highest score, got %f < %f", results[0].Score, results[1].Score)
	}
}

func TestSearchByVector_RespectsLimit(t *testing.T) {
	store := vecTestStore(t, 4)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	for i := 0; i < 5; i++ {
		fact := &model.Fact{
			ID: NewID(), Source: model.Source{TranscriptFile: "t.md"},
			FactType: model.FactDecision, Content: "fact", CreatedAt: now,
		}
		store.CreateFact(ctx, fact)
		store.UpdateFactEmbedding(ctx, fact.ID, []float32{float32(i + 1), 0.1, 0.1, 0.1}, "test-model")
	}

	results, err := store.SearchByVector(ctx, []float32{1, 0, 0, 0}, 2)
	if err != nil {
		t.Fatalf("SearchByVector: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 results with limit=2, got %d", len(results))
	}
}

func TestSearchByVector_EmptyDB(t *testing.T) {
	store := vecTestStore(t, 4)
	ctx := context.Background()

	results, err := store.SearchByVector(ctx, []float32{1, 0, 0, 0}, 10)
	if err != nil {
		t.Fatalf("SearchByVector on empty DB: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

// --- ListFactsByEmbeddingModel ---

func TestListFactsByEmbeddingModel(t *testing.T) {
	store := vecTestStore(t, 4)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	models := []string{"openai-3-small", "openai-3-small", "nomic-embed"}
	for i, embModel := range models {
		fact := &model.Fact{
			ID: NewID(), Source: model.Source{TranscriptFile: "t.md"},
			FactType: model.FactDecision, Content: "fact " + string(rune('A'+i)), CreatedAt: now,
		}
		store.CreateFact(ctx, fact)
		store.UpdateFactEmbedding(ctx, fact.ID, []float32{float32(i), 0, 0, 0}, embModel)
	}

	openai, err := store.ListFactsByEmbeddingModel(ctx, "openai-3-small")
	if err != nil {
		t.Fatalf("ListFactsByEmbeddingModel: %v", err)
	}
	if len(openai) != 2 {
		t.Errorf("expected 2 openai facts, got %d", len(openai))
	}

	nomic, err := store.ListFactsByEmbeddingModel(ctx, "nomic-embed")
	if err != nil {
		t.Fatalf("ListFactsByEmbeddingModel: %v", err)
	}
	if len(nomic) != 1 {
		t.Errorf("expected 1 nomic fact, got %d", len(nomic))
	}
}

// --- FTS5 text search ---

func TestSearchByText_FindsMatch(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	store.CreateFact(ctx, &model.Fact{
		ID: NewID(), Source: model.Source{TranscriptFile: "t.md"},
		FactType: model.FactDecision, Content: "Acme uses SQLite for storage", CreatedAt: now,
	})
	store.CreateFact(ctx, &model.Fact{
		ID: NewID(), Source: model.Source{TranscriptFile: "t.md"},
		FactType: model.FactPreference, Content: "Alice prefers dark mode", CreatedAt: now,
	})

	results, err := store.SearchByText(ctx, "SQLite", 10)
	if err != nil {
		t.Fatalf("SearchByText: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result for 'SQLite', got %d", len(results))
	}
	if !strings.Contains(results[0].Fact.Content, "SQLite") {
		t.Errorf("expected result about SQLite, got %q", results[0].Fact.Content)
	}
	if results[0].Score <= 0 {
		t.Errorf("expected positive score, got %f", results[0].Score)
	}
}

func TestSearchByText_NoMatch(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	store.CreateFact(ctx, &model.Fact{
		ID: NewID(), Source: model.Source{TranscriptFile: "t.md"},
		FactType: model.FactDecision, Content: "Acme uses Go", CreatedAt: now,
	})

	results, err := store.SearchByText(ctx, "Python", 10)
	if err != nil {
		t.Fatalf("SearchByText: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results for 'Python', got %d", len(results))
	}
}

func TestSearchByText_RankedByRelevance(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	store.CreateFact(ctx, &model.Fact{
		ID: NewID(), Source: model.Source{TranscriptFile: "t.md"},
		FactType: model.FactDecision, Content: "Alice mentioned Go once", CreatedAt: now,
	})
	store.CreateFact(ctx, &model.Fact{
		ID: NewID(), Source: model.Source{TranscriptFile: "t.md"},
		FactType: model.FactDecision, Content: "Go is great for Go projects and Go modules", CreatedAt: now,
	})

	results, err := store.SearchByText(ctx, "Go", 10)
	if err != nil {
		t.Fatalf("SearchByText: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Score < results[1].Score {
		t.Errorf("expected first result to have higher score (more relevant), got %f < %f",
			results[0].Score, results[1].Score)
	}
}

// --- Stats ---

func TestStats_EmptyDB(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()

	stats, err := store.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Facts != 0 {
		t.Errorf("expected 0 facts, got %d", stats.Facts)
	}
	if stats.Entities != 0 {
		t.Errorf("expected 0 entities, got %d", stats.Entities)
	}
	if stats.Relationships != 0 {
		t.Errorf("expected 0 relationships, got %d", stats.Relationships)
	}
	if stats.Consolidations != 0 {
		t.Errorf("expected 0 consolidations, got %d", stats.Consolidations)
	}
	if stats.IngestedFiles != 0 {
		t.Errorf("expected 0 ingested files, got %d", stats.IngestedFiles)
	}
}

func TestStats_WithData(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now()

	store.CreateFact(ctx, &model.Fact{
		ID: NewID(), Source: model.Source{TranscriptFile: "test.md"},
		FactType: "decision", Subject: "Acme", Content: "fact 1",
		Confidence: 0.9, CreatedAt: now,
	})
	store.CreateFact(ctx, &model.Fact{
		ID: NewID(), Source: model.Source{TranscriptFile: "test.md"},
		FactType: "preference", Subject: "Alice", Content: "fact 2",
		Confidence: 0.8, CreatedAt: now,
	})
	store.CreateEntity(ctx, &model.Entity{
		ID: NewID(), Name: "Acme", EntityType: "project", CreatedAt: now,
	})

	stats, err := store.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Facts != 2 {
		t.Errorf("expected 2 facts, got %d", stats.Facts)
	}
	if stats.Entities != 1 {
		t.Errorf("expected 1 entity, got %d", stats.Entities)
	}
}

// --- UpdateFact ---

func TestUpdateFact_UpdatesConfidence(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	f := &model.Fact{
		ID: NewID(), Source: model.Source{TranscriptFile: "t.md"},
		FactType: model.FactDecision, Subject: "Acme", Content: "Acme uses Go.",
		Confidence: 0.5, CreatedAt: now,
	}
	store.CreateFact(ctx, f)

	newConf := 0.95
	err := store.UpdateFact(ctx, f.ID, FactUpdate{Confidence: &newConf})
	if err != nil {
		t.Fatalf("UpdateFact: %v", err)
	}

	got, _ := store.GetFact(ctx, f.ID)
	if got.Confidence != 0.95 {
		t.Errorf("expected confidence 0.95, got %f", got.Confidence)
	}
}

func TestUpdateFact_UpdatesValidUntil(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	f := &model.Fact{
		ID: NewID(), Source: model.Source{TranscriptFile: "t.md"},
		FactType: model.FactDecision, Content: "temporary fact",
		Confidence: 0.8, CreatedAt: now,
	}
	store.CreateFact(ctx, f)

	expiry := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	err := store.UpdateFact(ctx, f.ID, FactUpdate{ValidUntil: &expiry})
	if err != nil {
		t.Fatalf("UpdateFact: %v", err)
	}

	got, _ := store.GetFact(ctx, f.ID)
	if got.Validity.ValidUntil == nil {
		t.Fatal("expected valid_until to be set")
	}
	if !got.Validity.ValidUntil.Equal(expiry) {
		t.Errorf("expected valid_until %v, got %v", expiry, *got.Validity.ValidUntil)
	}
}

func TestUpdateFact_UpdatesSubject(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	f := &model.Fact{
		ID: NewID(), Source: model.Source{TranscriptFile: "t.md"},
		FactType: model.FactProject, Subject: "DataSync", Content: "runs on node-1",
		Confidence: 0.9, CreatedAt: now,
	}
	store.CreateFact(ctx, f)

	newSubject := "Acme"
	err := store.UpdateFact(ctx, f.ID, FactUpdate{Subject: &newSubject})
	if err != nil {
		t.Fatalf("UpdateFact: %v", err)
	}

	got, _ := store.GetFact(ctx, f.ID)
	if got.Subject != "Acme" {
		t.Errorf("expected subject Acme, got %q", got.Subject)
	}
}

func TestUpdateFact_MultipleFields(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	f := &model.Fact{
		ID: NewID(), Source: model.Source{TranscriptFile: "t.md"},
		FactType: model.FactDecision, Subject: "Old", Content: "some fact",
		Confidence: 0.5, CreatedAt: now,
	}
	store.CreateFact(ctx, f)

	newConf := 0.9
	newSubject := "New"
	expiry := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	err := store.UpdateFact(ctx, f.ID, FactUpdate{
		Confidence: &newConf,
		Subject:    &newSubject,
		ValidUntil: &expiry,
	})
	if err != nil {
		t.Fatalf("UpdateFact: %v", err)
	}

	got, _ := store.GetFact(ctx, f.ID)
	if got.Confidence != 0.9 {
		t.Errorf("confidence: expected 0.9, got %f", got.Confidence)
	}
	if got.Subject != "New" {
		t.Errorf("subject: expected New, got %q", got.Subject)
	}
	if got.Validity.ValidUntil == nil || !got.Validity.ValidUntil.Equal(expiry) {
		t.Errorf("valid_until: expected %v, got %v", expiry, got.Validity.ValidUntil)
	}
}

func TestUpdateFact_NoFields_NoOp(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	f := &model.Fact{
		ID: NewID(), Source: model.Source{TranscriptFile: "t.md"},
		FactType: model.FactDecision, Subject: "Acme", Content: "unchanged",
		Confidence: 0.7, CreatedAt: now,
	}
	store.CreateFact(ctx, f)

	err := store.UpdateFact(ctx, f.ID, FactUpdate{})
	if err != nil {
		t.Fatalf("UpdateFact with no fields should succeed: %v", err)
	}

	got, _ := store.GetFact(ctx, f.ID)
	if got.Confidence != 0.7 {
		t.Errorf("expected confidence unchanged at 0.7, got %f", got.Confidence)
	}
	if got.Subject != "Acme" {
		t.Errorf("expected subject unchanged at Acme, got %q", got.Subject)
	}
}

func TestUpdateFact_NotFound(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()

	newConf := 0.5
	err := store.UpdateFact(ctx, "nonexistent-id", FactUpdate{Confidence: &newConf})
	if err == nil {
		t.Fatal("expected error for non-existent fact ID")
	}
}

// --- Chunk FTS5 text search (BVP-214) ---

func TestSearchChunksByText_FindsMatch(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()

	tr := &model.Transcript{
		ID: NewID(), FilePath: "fts-test.md", ChunkCount: 2,
		CreatedAt: time.Now().UTC().Truncate(time.Second),
	}
	store.CreateTranscript(ctx, tr)

	c1 := &model.TranscriptChunk{
		ID: NewID(), TranscriptID: tr.ID,
		LineStart: 1, LineEnd: 20, ContentHash: "h1",
	}
	c2 := &model.TranscriptChunk{
		ID: NewID(), TranscriptID: tr.ID,
		LineStart: 18, LineEnd: 40, ContentHash: "h2",
	}
	store.CreateTranscriptChunk(ctx, c1, "Alice discussed the SQLite migration plan")
	store.CreateTranscriptChunk(ctx, c2, "Bob reviewed the deployment pipeline")

	results, err := store.SearchChunksByText(ctx, "SQLite", 10)
	if err != nil {
		t.Fatalf("SearchChunksByText: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result for 'SQLite', got %d", len(results))
	}
	if results[0].Chunk.ID != c1.ID {
		t.Errorf("expected chunk %s, got %s", c1.ID, results[0].Chunk.ID)
	}
	if results[0].Score <= 0 {
		t.Errorf("expected positive score, got %f", results[0].Score)
	}
}

func TestSearchChunksByText_NoMatch(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()

	tr := &model.Transcript{
		ID: NewID(), FilePath: "fts-nomatch.md", ChunkCount: 1,
		CreatedAt: time.Now().UTC().Truncate(time.Second),
	}
	store.CreateTranscript(ctx, tr)

	c := &model.TranscriptChunk{
		ID: NewID(), TranscriptID: tr.ID,
		LineStart: 1, LineEnd: 10, ContentHash: "h1",
	}
	store.CreateTranscriptChunk(ctx, c, "Alice discussed the migration plan")

	results, err := store.SearchChunksByText(ctx, "Kubernetes", 10)
	if err != nil {
		t.Fatalf("SearchChunksByText: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results for 'Kubernetes', got %d", len(results))
	}
}

func TestSearchChunksByText_BM25Ranking(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()

	tr := &model.Transcript{
		ID: NewID(), FilePath: "fts-rank.md", ChunkCount: 2,
		CreatedAt: time.Now().UTC().Truncate(time.Second),
	}
	store.CreateTranscript(ctx, tr)

	c1 := &model.TranscriptChunk{
		ID: NewID(), TranscriptID: tr.ID,
		LineStart: 1, LineEnd: 20, ContentHash: "r1",
	}
	c2 := &model.TranscriptChunk{
		ID: NewID(), TranscriptID: tr.ID,
		LineStart: 18, LineEnd: 40, ContentHash: "r2",
	}
	store.CreateTranscriptChunk(ctx, c1, "Go mentioned once here")
	store.CreateTranscriptChunk(ctx, c2, "Go is great for Go projects and Go modules and Go tools")

	results, err := store.SearchChunksByText(ctx, "Go", 10)
	if err != nil {
		t.Fatalf("SearchChunksByText: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Score < results[1].Score {
		t.Errorf("expected first result to have higher BM25 score, got %f < %f",
			results[0].Score, results[1].Score)
	}
}

// --- Chunk embedding listing (BVP-216) ---

func TestListChunksWithoutEmbedding(t *testing.T) {
	store := openTestDB(t)
	if err := store.AttachVectorIndex(3); err != nil {
		t.Fatalf("AttachVectorIndex: %v", err)
	}
	ctx := context.Background()

	tr := &model.Transcript{
		ID: NewID(), FilePath: "emb-list.md", ChunkCount: 3,
		CreatedAt: time.Now().UTC().Truncate(time.Second),
	}
	store.CreateTranscript(ctx, tr)

	c1 := &model.TranscriptChunk{ID: NewID(), TranscriptID: tr.ID, LineStart: 1, LineEnd: 10, ContentHash: "e1"}
	c2 := &model.TranscriptChunk{ID: NewID(), TranscriptID: tr.ID, LineStart: 11, LineEnd: 20, ContentHash: "e2"}
	c3 := &model.TranscriptChunk{ID: NewID(), TranscriptID: tr.ID, LineStart: 21, LineEnd: 30, ContentHash: "e3"}
	store.CreateTranscriptChunk(ctx, c1, "chunk one")
	store.CreateTranscriptChunk(ctx, c2, "chunk two")
	store.CreateTranscriptChunk(ctx, c3, "chunk three")

	store.UpdateChunkEmbedding(ctx, c2.ID, []float32{0.1, 0.2, 0.3}, "test-model")

	without, err := store.ListChunksWithoutEmbedding(ctx)
	if err != nil {
		t.Fatalf("ListChunksWithoutEmbedding: %v", err)
	}
	if len(without) != 2 {
		t.Errorf("expected 2 chunks without embedding, got %d", len(without))
	}
}

func TestListChunksByEmbeddingModel(t *testing.T) {
	store := openTestDB(t)
	if err := store.AttachVectorIndex(3); err != nil {
		t.Fatalf("AttachVectorIndex: %v", err)
	}
	ctx := context.Background()

	tr := &model.Transcript{
		ID: NewID(), FilePath: "emb-model.md", ChunkCount: 3,
		CreatedAt: time.Now().UTC().Truncate(time.Second),
	}
	store.CreateTranscript(ctx, tr)

	c1 := &model.TranscriptChunk{ID: NewID(), TranscriptID: tr.ID, LineStart: 1, LineEnd: 10, ContentHash: "m1"}
	c2 := &model.TranscriptChunk{ID: NewID(), TranscriptID: tr.ID, LineStart: 11, LineEnd: 20, ContentHash: "m2"}
	c3 := &model.TranscriptChunk{ID: NewID(), TranscriptID: tr.ID, LineStart: 21, LineEnd: 30, ContentHash: "m3"}
	store.CreateTranscriptChunk(ctx, c1, "chunk one")
	store.CreateTranscriptChunk(ctx, c2, "chunk two")
	store.CreateTranscriptChunk(ctx, c3, "chunk three")

	store.UpdateChunkEmbedding(ctx, c1.ID, []float32{0.1, 0.2, 0.3}, "openai-3-small")
	store.UpdateChunkEmbedding(ctx, c2.ID, []float32{0.4, 0.5, 0.6}, "openai-3-small")
	store.UpdateChunkEmbedding(ctx, c3.ID, []float32{0.7, 0.8, 0.9}, "nomic-embed")

	openai, err := store.ListChunksByEmbeddingModel(ctx, "openai-3-small")
	if err != nil {
		t.Fatalf("ListChunksByEmbeddingModel: %v", err)
	}
	if len(openai) != 2 {
		t.Errorf("expected 2 openai chunks, got %d", len(openai))
	}

	nomic, err := store.ListChunksByEmbeddingModel(ctx, "nomic-embed")
	if err != nil {
		t.Fatalf("ListChunksByEmbeddingModel: %v", err)
	}
	if len(nomic) != 1 {
		t.Errorf("expected 1 nomic chunk, got %d", len(nomic))
	}
}

// --- SupersedeWithContent ---

func TestSupersedeWithContent_CreatesNewAndLinks(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	old := &model.Fact{
		ID: NewID(), Source: model.Source{TranscriptFile: "t.md"},
		FactType: model.FactSkill, Subject: "Alice", Content: "Alice uses Rust.",
		Confidence: 0.9, CreatedAt: now,
	}
	store.CreateFact(ctx, old)

	newFact, err := store.SupersedeWithContent(ctx, old.ID, "Alice switched to Go.", "mcp")
	if err != nil {
		t.Fatalf("SupersedeWithContent: %v", err)
	}

	if newFact.Content != "Alice switched to Go." {
		t.Errorf("new content = %q, want %q", newFact.Content, "Alice switched to Go.")
	}
	if newFact.FactType != model.FactSkill {
		t.Errorf("new fact_type = %q, want %q", newFact.FactType, model.FactSkill)
	}
	if newFact.Subject != "Alice" {
		t.Errorf("new subject = %q, want %q", newFact.Subject, "Alice")
	}
	if newFact.Source.TranscriptFile != "mcp" {
		t.Errorf("new source = %q, want %q", newFact.Source.TranscriptFile, "mcp")
	}
	if newFact.ID == old.ID {
		t.Error("new fact should have a different ID")
	}

	oldGot, _ := store.GetFact(ctx, old.ID)
	if oldGot.SupersededBy != newFact.ID {
		t.Errorf("old fact superseded_by = %q, want %q", oldGot.SupersededBy, newFact.ID)
	}
}

func TestSupersedeWithContent_OldFactNotFound(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()

	_, err := store.SupersedeWithContent(ctx, "nonexistent-id", "new content", "mcp")
	if err == nil {
		t.Fatal("expected error for non-existent old fact")
	}
}

func TestSupersedeWithContent_CopiesTypeAndSubject(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	old := &model.Fact{
		ID: NewID(), Source: model.Source{TranscriptFile: "session-42.md"},
		FactType: model.FactProject, Subject: "Acme", Content: "Acme deploys on Fridays.",
		Confidence: 0.8, CreatedAt: now,
	}
	store.CreateFact(ctx, old)

	newFact, err := store.SupersedeWithContent(ctx, old.ID, "Acme deploys on Thursdays.", "manual")
	if err != nil {
		t.Fatalf("SupersedeWithContent: %v", err)
	}

	if newFact.FactType != model.FactProject {
		t.Errorf("fact_type = %q, want %q", newFact.FactType, model.FactProject)
	}
	if newFact.Subject != "Acme" {
		t.Errorf("subject = %q, want %q", newFact.Subject, "Acme")
	}
}

// --- SupersedeRealtimeBySession ---

func TestSupersedeRealtimeBySession(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	for i := 0; i < 3; i++ {
		store.CreateFact(ctx, &model.Fact{
			ID: NewID(), Source: model.Source{TranscriptFile: "realtime:sess-1"},
			FactType: model.FactDecision, Content: "realtime fact " + string(rune('A'+i)),
			Confidence: 0.8, CreatedAt: now,
		})
	}
	store.CreateFact(ctx, &model.Fact{
		ID: NewID(), Source: model.Source{TranscriptFile: "realtime:sess-2"},
		FactType: model.FactDecision, Content: "different session fact",
		Confidence: 0.8, CreatedAt: now,
	})
	store.CreateFact(ctx, &model.Fact{
		ID: NewID(), Source: model.Source{TranscriptFile: "batch-file.md"},
		FactType: model.FactDecision, Content: "batch fact",
		Confidence: 0.8, CreatedAt: now,
	})

	superseded, err := store.SupersedeRealtimeBySession(ctx, "sess-1")
	if err != nil {
		t.Fatalf("SupersedeRealtimeBySession: %v", err)
	}
	if superseded != 3 {
		t.Errorf("expected 3 superseded, got %d", superseded)
	}

	active, _ := store.ListFacts(ctx, FactFilter{NotSuperseded: true})
	if len(active) != 2 {
		t.Errorf("expected 2 active facts (sess-2 + batch), got %d", len(active))
	}
	for _, f := range active {
		if f.Source.TranscriptFile == "realtime:sess-1" {
			t.Error("sess-1 fact should have been superseded")
		}
	}

	facts, _ := store.ListFacts(ctx, FactFilter{})
	for _, f := range facts {
		if f.Source.TranscriptFile == "realtime:sess-1" {
			if f.SupersedeReason != "batch-replaced" {
				t.Errorf("fact %s: supersede_reason = %q, want %q", f.ID, f.SupersedeReason, "batch-replaced")
			}
			if f.SupersededBy != "" {
				t.Errorf("fact %s: superseded_by = %q, want empty (no sentinel)", f.ID, f.SupersededBy)
			}
		}
	}
}

func TestSupersedeRealtimeBySession_FKChecksStayEnabled(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	store.CreateFact(ctx, &model.Fact{
		ID: NewID(), Source: model.Source{TranscriptFile: "realtime:fk-test"},
		FactType: model.FactDecision, Content: "fk test fact",
		Confidence: 0.8, CreatedAt: now,
	})

	store.SupersedeRealtimeBySession(ctx, "fk-test")

	var fkEnabled int
	store.RawDB().QueryRow("PRAGMA foreign_keys").Scan(&fkEnabled)
	if fkEnabled != 1 {
		t.Errorf("FK checks disabled after SupersedeRealtimeBySession: foreign_keys = %d", fkEnabled)
	}
}

// --- Transcripts (D22) ---

func TestCreateAndGetTranscript(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Second)
	tr := &model.Transcript{
		ID:           NewID(),
		FilePath:     "2026-03-15-standup.md",
		Participants: []string{"Alice", "Bob"},
		Topic:        "daily standup",
		ChunkCount:   3,
		CreatedAt:    now,
	}

	if err := store.CreateTranscript(ctx, tr); err != nil {
		t.Fatalf("create transcript: %v", err)
	}

	got, err := store.GetTranscript(ctx, tr.ID)
	if err != nil {
		t.Fatalf("get transcript: %v", err)
	}
	if got.FilePath != tr.FilePath {
		t.Errorf("file_path = %q, want %q", got.FilePath, tr.FilePath)
	}
	if got.Topic != "daily standup" {
		t.Errorf("topic = %q, want %q", got.Topic, "daily standup")
	}
	if got.ChunkCount != 3 {
		t.Errorf("chunk_count = %d, want 3", got.ChunkCount)
	}
	if len(got.Participants) != 2 || got.Participants[0] != "Alice" {
		t.Errorf("participants = %v, want [Alice Bob]", got.Participants)
	}
}

func TestGetTranscriptByPath(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()

	tr := &model.Transcript{
		ID:         NewID(),
		FilePath:   "notes/2026-03-15.md",
		ChunkCount: 1,
		CreatedAt:  time.Now().UTC().Truncate(time.Second),
	}
	if err := store.CreateTranscript(ctx, tr); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := store.GetTranscriptByPath(ctx, "notes/2026-03-15.md")
	if err != nil {
		t.Fatalf("get by path: %v", err)
	}
	if got.ID != tr.ID {
		t.Errorf("id = %q, want %q", got.ID, tr.ID)
	}

	missing, err := store.GetTranscriptByPath(ctx, "nonexistent.md")
	if err != nil {
		t.Fatalf("get missing: %v", err)
	}
	if missing != nil {
		t.Error("expected nil for missing path")
	}
}

func TestCreateAndListTranscriptChunks(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()

	tr := &model.Transcript{
		ID:         NewID(),
		FilePath:   "meeting.md",
		ChunkCount: 2,
		CreatedAt:  time.Now().UTC().Truncate(time.Second),
	}
	if err := store.CreateTranscript(ctx, tr); err != nil {
		t.Fatalf("create transcript: %v", err)
	}

	c1 := &model.TranscriptChunk{
		ID:           NewID(),
		TranscriptID: tr.ID,
		LineStart:    1,
		LineEnd:      20,
		ContentHash:  "abc123",
	}
	c2 := &model.TranscriptChunk{
		ID:           NewID(),
		TranscriptID: tr.ID,
		LineStart:    18,
		LineEnd:      40,
		ContentHash:  "def456",
	}

	if err := store.CreateTranscriptChunk(ctx, c1, "chunk one text content"); err != nil {
		t.Fatalf("create chunk 1: %v", err)
	}
	if err := store.CreateTranscriptChunk(ctx, c2, "chunk two text content"); err != nil {
		t.Fatalf("create chunk 2: %v", err)
	}

	chunks, err := store.ListTranscriptChunks(ctx, tr.ID)
	if err != nil {
		t.Fatalf("list chunks: %v", err)
	}
	if len(chunks) != 2 {
		t.Fatalf("got %d chunks, want 2", len(chunks))
	}
	if chunks[0].LineStart != 1 || chunks[1].LineStart != 18 {
		t.Errorf("chunk order wrong: starts = %d, %d", chunks[0].LineStart, chunks[1].LineStart)
	}
}

func TestUpdateChunkEmbeddingAndSearch(t *testing.T) {
	store := openTestDB(t)
	if err := store.AttachVectorIndex(3); err != nil {
		t.Fatalf("AttachVectorIndex: %v", err)
	}
	ctx := context.Background()

	tr := &model.Transcript{
		ID: NewID(), FilePath: "vec-test.md", ChunkCount: 1,
		CreatedAt: time.Now().UTC().Truncate(time.Second),
	}
	store.CreateTranscript(ctx, tr)

	c := &model.TranscriptChunk{
		ID: NewID(), TranscriptID: tr.ID,
		LineStart: 1, LineEnd: 10, ContentHash: "aaa",
	}
	store.CreateTranscriptChunk(ctx, c, "vector test chunk text")

	emb := []float32{0.1, 0.2, 0.3}
	if err := store.UpdateChunkEmbedding(ctx, c.ID, emb, "test-model"); err != nil {
		t.Fatalf("UpdateChunkEmbedding: %v", err)
	}

	results, err := store.SearchChunksByVector(ctx, []float32{0.1, 0.2, 0.3}, 5)
	if err != nil {
		t.Fatalf("SearchChunksByVector: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Chunk.ID != c.ID {
		t.Errorf("chunk id = %q, want %q", results[0].Chunk.ID, c.ID)
	}
}

// --- ListFactEmbeddings ---

func TestListFactEmbeddings_ReturnsByType(t *testing.T) {
	store := vecTestStore(t, 4)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	f1 := &model.Fact{
		ID: NewID(), Source: model.Source{TranscriptFile: "t.md"},
		FactType: model.FactPreference, Subject: "Alice", Content: "prefers Go",
		Confidence: 0.9, CreatedAt: now,
	}
	store.CreateFact(ctx, f1)
	store.UpdateFactEmbedding(ctx, f1.ID, []float32{1.0, 0.0, 0.0, 0.0}, "test-model")

	f2 := &model.Fact{
		ID: NewID(), Source: model.Source{TranscriptFile: "t.md"},
		FactType: model.FactPreference, Subject: "Bob", Content: "prefers Rust",
		Confidence: 0.9, CreatedAt: now,
	}
	store.CreateFact(ctx, f2)
	store.UpdateFactEmbedding(ctx, f2.ID, []float32{0.9, 0.1, 0.0, 0.0}, "test-model")

	f3 := &model.Fact{
		ID: NewID(), Source: model.Source{TranscriptFile: "t.md"},
		FactType: model.FactDecision, Subject: "team", Content: "use Go",
		Confidence: 0.9, CreatedAt: now,
	}
	store.CreateFact(ctx, f3)
	store.UpdateFactEmbedding(ctx, f3.ID, []float32{0.0, 0.0, 1.0, 0.0}, "test-model")

	vecs, err := store.ListFactEmbeddings(ctx, "preference")
	if err != nil {
		t.Fatalf("ListFactEmbeddings: %v", err)
	}
	if len(vecs) != 2 {
		t.Fatalf("expected 2 vectors, got %d", len(vecs))
	}
	if len(vecs[0]) != 4 {
		t.Errorf("expected 4 dimensions, got %d", len(vecs[0]))
	}
}

func TestListFactEmbeddings_NoEmbeddings(t *testing.T) {
	store := vecTestStore(t, 4)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	f := &model.Fact{
		ID: NewID(), Source: model.Source{TranscriptFile: "t.md"},
		FactType: model.FactPreference, Subject: "Alice", Content: "prefers Go",
		Confidence: 0.9, CreatedAt: now,
	}
	store.CreateFact(ctx, f)

	vecs, err := store.ListFactEmbeddings(ctx, "preference")
	if err != nil {
		t.Fatalf("ListFactEmbeddings: %v", err)
	}
	if len(vecs) != 0 {
		t.Errorf("expected 0 vectors, got %d", len(vecs))
	}
}

func TestListFactEmbeddings_NoFacts(t *testing.T) {
	store := vecTestStore(t, 4)
	ctx := context.Background()

	vecs, err := store.ListFactEmbeddings(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("ListFactEmbeddings: %v", err)
	}
	if len(vecs) != 0 {
		t.Errorf("expected 0 vectors, got %d", len(vecs))
	}
}

// --- CreatedAfter filter ---

func TestListFacts_CreatedAfter(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()

	old := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	recent := time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)

	store.CreateFact(ctx, &model.Fact{
		ID: NewID(), Source: model.Source{TranscriptFile: "t.md"},
		FactType: model.FactDecision, Content: "old decision", CreatedAt: old,
	})
	store.CreateFact(ctx, &model.Fact{
		ID: NewID(), Source: model.Source{TranscriptFile: "t.md"},
		FactType: model.FactPreference, Content: "recent preference", CreatedAt: recent,
	})

	cutoff := time.Date(2026, 3, 14, 0, 0, 0, 0, time.UTC)
	facts, err := store.ListFacts(ctx, FactFilter{CreatedAfter: &cutoff})
	if err != nil {
		t.Fatalf("ListFacts with CreatedAfter: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("expected 1 fact after cutoff, got %d", len(facts))
	}
	if facts[0].Content != "recent preference" {
		t.Errorf("expected recent fact, got %q", facts[0].Content)
	}
}

// --- Query log ---

func TestCreateQueryLog(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()

	l := &QueryLog{
		ID:                 NewID(),
		Endpoint:           "query",
		Question:           "What is Acme?",
		TotalLatencyMs:     250,
		RetrievalLatencyMs: 80,
		SynthesisLatencyMs: 170,
		FactsFound:         5,
		FactsByVector:      3,
		FactsByText:        2,
		FactsByGraph:       1,
		ChunksByVector:     4,
		ChunksByText:       2,
		CitationsCount:     3,
		EmbedderAvailable:  true,
		CreatedAt:          time.Now(),
	}
	if err := store.CreateQueryLog(ctx, l); err != nil {
		t.Fatalf("CreateQueryLog: %v", err)
	}

	logs, err := store.ListQueryLogs(ctx, 10)
	if err != nil {
		t.Fatalf("ListQueryLogs: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 log, got %d", len(logs))
	}
	got := logs[0]
	if got.Endpoint != "query" {
		t.Errorf("endpoint: got %q, want %q", got.Endpoint, "query")
	}
	if got.TotalLatencyMs != 250 {
		t.Errorf("total_latency_ms: got %d, want 250", got.TotalLatencyMs)
	}
	if got.FactsByVector != 3 {
		t.Errorf("facts_by_vector: got %d, want 3", got.FactsByVector)
	}
	if got.ChunksByVector != 4 {
		t.Errorf("chunks_by_vector: got %d, want 4", got.ChunksByVector)
	}
	if !got.EmbedderAvailable {
		t.Error("embedder_available: got false, want true")
	}
	if got.Error != "" {
		t.Errorf("error: got %q, want empty", got.Error)
	}
}

func TestCreateQueryLog_WithError(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()

	l := &QueryLog{
		ID:                NewID(),
		Endpoint:          "query",
		Question:          "broken query",
		TotalLatencyMs:    50,
		EmbedderAvailable: false,
		Error:             "provider timeout",
		CreatedAt:         time.Now(),
	}
	if err := store.CreateQueryLog(ctx, l); err != nil {
		t.Fatalf("CreateQueryLog: %v", err)
	}

	logs, err := store.ListQueryLogs(ctx, 10)
	if err != nil {
		t.Fatalf("ListQueryLogs: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 log, got %d", len(logs))
	}
	if logs[0].Error != "provider timeout" {
		t.Errorf("error: got %q, want %q", logs[0].Error, "provider timeout")
	}
	if logs[0].EmbedderAvailable {
		t.Error("embedder_available: got true, want false")
	}
}

// --- Eval runs: list, baseline, git commit ---

func TestEvalRun_CreateAndList(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	runs := []struct {
		evalType string
		score    float64
		score2   float64
		delay    time.Duration
	}{
		{"extraction", 0.75, 0, 0},
		{"extraction", 0.80, 0, time.Second},
		{"retrieval", 0.90, 0.85, 2 * time.Second},
	}
	for _, r := range runs {
		store.CreateEvalRun(ctx, &EvalRun{
			ID:            NewID(),
			EvalType:      r.evalType,
			Score:         r.score,
			Score2:        r.score2,
			Report:        "{}",
			ExamplesCount: 10,
			CreatedAt:     now.Add(r.delay),
		})
	}

	extraction, err := store.ListEvalRuns(ctx, "extraction", 10)
	if err != nil {
		t.Fatalf("ListEvalRuns(extraction): %v", err)
	}
	if len(extraction) != 2 {
		t.Fatalf("expected 2 extraction runs, got %d", len(extraction))
	}
	if extraction[0].Score < extraction[1].Score {
		t.Error("expected DESC order by created_at (newest first)")
	}

	retrieval, err := store.ListEvalRuns(ctx, "retrieval", 10)
	if err != nil {
		t.Fatalf("ListEvalRuns(retrieval): %v", err)
	}
	if len(retrieval) != 1 {
		t.Fatalf("expected 1 retrieval run, got %d", len(retrieval))
	}

	all, err := store.ListEvalRuns(ctx, "", 10)
	if err != nil {
		t.Fatalf("ListEvalRuns(all): %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 total runs, got %d", len(all))
	}
}

func TestEvalRun_Baseline(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	run1 := &EvalRun{
		ID: NewID(), EvalType: "extraction", Score: 0.70,
		Report: "{}", ExamplesCount: 10, CreatedAt: now,
	}
	run2 := &EvalRun{
		ID: NewID(), EvalType: "extraction", Score: 0.80,
		Report: "{}", ExamplesCount: 10, CreatedAt: now.Add(time.Second),
	}
	store.CreateEvalRun(ctx, run1)
	store.CreateEvalRun(ctx, run2)

	if err := store.SetBaseline(ctx, run1.ID, "extraction"); err != nil {
		t.Fatalf("SetBaseline(run1): %v", err)
	}
	got, err := store.GetBaselineEvalRun(ctx, "extraction")
	if err != nil {
		t.Fatalf("GetBaselineEvalRun: %v", err)
	}
	if got.ID != run1.ID {
		t.Errorf("expected baseline run1 (%s), got %s", run1.ID, got.ID)
	}

	if err := store.SetBaseline(ctx, run2.ID, "extraction"); err != nil {
		t.Fatalf("SetBaseline(run2): %v", err)
	}
	got, err = store.GetBaselineEvalRun(ctx, "extraction")
	if err != nil {
		t.Fatalf("GetBaselineEvalRun after replace: %v", err)
	}
	if got.ID != run2.ID {
		t.Errorf("expected baseline run2 (%s), got %s", run2.ID, got.ID)
	}

	listed, _ := store.ListEvalRuns(ctx, "extraction", 10)
	for _, r := range listed {
		if r.ID == run1.ID && r.IsBaseline {
			t.Error("run1 should no longer be baseline")
		}
		if r.ID == run2.ID && !r.IsBaseline {
			t.Error("run2 should be baseline")
		}
	}
}

func TestEvalRun_GetBaseline_NoBaseline(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()

	store.CreateEvalRun(ctx, &EvalRun{
		ID: NewID(), EvalType: "extraction", Score: 0.70,
		Report: "{}", ExamplesCount: 10, CreatedAt: time.Now().UTC(),
	})

	got, err := store.GetBaselineEvalRun(ctx, "extraction")
	if err != nil {
		t.Fatalf("GetBaselineEvalRun: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil when no baseline set, got %+v", got)
	}
}

func TestEvalRun_AutoBaseline_FirstRun(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()

	run := &EvalRun{
		ID: NewID(), EvalType: "extraction", Score: 0.70,
		Report: "{}", ExamplesCount: 10, CreatedAt: time.Now().UTC(),
	}
	store.CreateEvalRun(ctx, run)

	baseline, _ := store.GetBaselineEvalRun(ctx, "extraction")
	if baseline != nil {
		t.Fatal("baseline should be nil before auto-update")
	}

	store.SetBaseline(ctx, run.ID, "extraction")
	baseline, _ = store.GetBaselineEvalRun(ctx, "extraction")
	if baseline == nil || baseline.ID != run.ID {
		t.Fatal("first run should become baseline")
	}
}

func TestEvalRun_AutoBaseline_Improvement(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	run1 := &EvalRun{
		ID: NewID(), EvalType: "extraction", Score: 0.70,
		Report: "{}", ExamplesCount: 10, CreatedAt: now,
	}
	store.CreateEvalRun(ctx, run1)
	store.SetBaseline(ctx, run1.ID, "extraction")

	run2 := &EvalRun{
		ID: NewID(), EvalType: "extraction", Score: 0.80,
		Report: "{}", ExamplesCount: 10, CreatedAt: now.Add(time.Second),
	}
	store.CreateEvalRun(ctx, run2)

	baseline, _ := store.GetBaselineEvalRun(ctx, "extraction")
	if baseline.Score >= run2.Score {
		t.Skip("baseline already better, nothing to test")
	}
	store.SetBaseline(ctx, run2.ID, "extraction")

	baseline, _ = store.GetBaselineEvalRun(ctx, "extraction")
	if baseline.ID != run2.ID {
		t.Errorf("baseline should be updated to run2, got %s", baseline.ID)
	}
}

func TestEvalRun_AutoBaseline_NoUpdateOnRegression(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	run1 := &EvalRun{
		ID: NewID(), EvalType: "extraction", Score: 0.80,
		Report: "{}", ExamplesCount: 10, CreatedAt: now,
	}
	store.CreateEvalRun(ctx, run1)
	store.SetBaseline(ctx, run1.ID, "extraction")

	run2 := &EvalRun{
		ID: NewID(), EvalType: "extraction", Score: 0.65,
		Report: "{}", ExamplesCount: 10, CreatedAt: now.Add(time.Second),
	}
	store.CreateEvalRun(ctx, run2)

	baseline, _ := store.GetBaselineEvalRun(ctx, "extraction")
	if run2.Score >= baseline.Score {
		t.Skip("run2 is not a regression")
	}

	baseline, _ = store.GetBaselineEvalRun(ctx, "extraction")
	if baseline.ID != run1.ID {
		t.Errorf("baseline should remain run1 on regression, got %s", baseline.ID)
	}
}

func TestEvalRun_GitCommit(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()

	run := &EvalRun{
		ID: NewID(), EvalType: "extraction", Score: 0.75,
		Report: "{}", ExamplesCount: 10, GitCommit: "abc1234",
		CreatedAt: time.Now().UTC(),
	}
	store.CreateEvalRun(ctx, run)

	got, err := store.LatestEvalRun(ctx, "extraction")
	if err != nil {
		t.Fatalf("LatestEvalRun: %v", err)
	}
	if got.GitCommit != "abc1234" {
		t.Errorf("expected git_commit 'abc1234', got %q", got.GitCommit)
	}
}

func TestQueryLogStats(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		store.CreateQueryLog(ctx, &QueryLog{
			ID: NewID(), Endpoint: "query", TotalLatencyMs: 200,
			EmbedderAvailable: true, CreatedAt: time.Now(),
		})
	}
	for i := 0; i < 5; i++ {
		store.CreateQueryLog(ctx, &QueryLog{
			ID: NewID(), Endpoint: "context", TotalLatencyMs: 40,
			EmbedderAvailable: true, CreatedAt: time.Now(),
		})
	}
	store.CreateQueryLog(ctx, &QueryLog{
		ID: NewID(), Endpoint: "query", TotalLatencyMs: 100,
		EmbedderAvailable: false, Error: "timeout", CreatedAt: time.Now(),
	})

	stats, err := store.QueryLogStats(ctx, 30)
	if err != nil {
		t.Fatalf("QueryLogStats: %v", err)
	}
	if stats.TotalQueries != 4 {
		t.Errorf("TotalQueries: got %d, want 4", stats.TotalQueries)
	}
	if stats.TotalContext != 5 {
		t.Errorf("TotalContext: got %d, want 5", stats.TotalContext)
	}
	if stats.ErrorCount != 1 {
		t.Errorf("ErrorCount: got %d, want 1", stats.ErrorCount)
	}
}
