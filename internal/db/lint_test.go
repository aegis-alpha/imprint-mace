package db

import (
	"context"
	"testing"
	"time"

	"github.com/aegis-alpha/imprint-mace/internal/model"
)

func TestNormalizeLintChecks_AllDefault(t *testing.T) {
	got, err := NormalizeLintChecks("", AllLintChecks)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(got) != len(AllLintChecks) {
		t.Fatalf("len %d, want %d", len(got), len(AllLintChecks))
	}
}

func TestNormalizeLintChecks_FilterAndDedupe(t *testing.T) {
	got, err := NormalizeLintChecks("stale, orphans ,stale", AllLintChecks)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(got) != 2 || got[0] != "stale" || got[1] != "orphans" {
		t.Fatalf("got %v", got)
	}
}

func TestNormalizeLintChecks_Unknown(t *testing.T) {
	_, err := NormalizeLintChecks("nope", AllLintChecks)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestLint_EmptyDB(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()

	for _, fn := range []func(context.Context) error{
		func(c context.Context) error {
			r, e := store.LintStaleFacts(c)
			if e != nil || len(r) != 0 {
				t.Fatalf("LintStaleFacts: %v, n=%d", e, len(r))
			}
			return nil
		},
		func(c context.Context) error {
			r, e := store.LintOrphanEntities(c)
			if e != nil || len(r) != 0 {
				t.Fatalf("LintOrphanEntities: %v, n=%d", e, len(r))
			}
			return nil
		},
		func(c context.Context) error {
			r, e := store.LintBrokenSupersedeChains(c)
			if e != nil || len(r) != 0 {
				t.Fatalf("LintBrokenSupersedeChains: %v, n=%d", e, len(r))
			}
			return nil
		},
		func(c context.Context) error {
			r, e := store.LintEntityDedupExact(c)
			if e != nil || len(r) != 0 {
				t.Fatalf("LintEntityDedupExact: %v, n=%d", e, len(r))
			}
			return nil
		},
		func(c context.Context) error {
			r, e := store.LintEntityDedupSubstring(c)
			if e != nil || len(r) != 0 {
				t.Fatalf("LintEntityDedupSubstring: %v, n=%d", e, len(r))
			}
			return nil
		},
		func(c context.Context) error {
			r, e := store.LintFactsMissingEmbeddingsByType(c)
			if e != nil || len(r) != 0 {
				t.Fatalf("LintFactsMissingEmbeddingsByType: %v, n=%d", e, len(r))
			}
			return nil
		},
		func(c context.Context) error {
			r, e := store.LintDistinctNonEmptySourceFiles(c)
			if e != nil || len(r) != 0 {
				t.Fatalf("LintDistinctNonEmptySourceFiles: %v, n=%d", e, len(r))
			}
			return nil
		},
		func(c context.Context) error {
			n, e := store.LintUnconsolidatedActiveFactsCount(c)
			if e != nil || n != 0 {
				t.Fatalf("LintUnconsolidatedActiveFactsCount: %v, n=%d", e, n)
			}
			return nil
		},
	} {
		if err := fn(ctx); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLint_StaleFact(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	past := time.Now().UTC().Add(-48 * time.Hour)
	f := &model.Fact{
		ID:         NewID(),
		Source:     model.Source{TranscriptFile: "t.md"},
		FactType:   model.FactPreference,
		Subject:    "Alice",
		Content:    "likes tea",
		Confidence: 0.9,
		Validity:   model.TimeRange{ValidUntil: &past},
		CreatedAt:  time.Now().UTC().Truncate(time.Second),
	}
	if err := store.CreateFact(ctx, f); err != nil {
		t.Fatal(err)
	}
	rows, err := store.LintStaleFacts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != f.ID {
		t.Fatalf("got %+v", rows)
	}
}

func TestLint_OrphanEntity(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	e := &model.Entity{
		ID:         NewID(),
		Name:       "LonelyConcept",
		EntityType: model.EntityConcept,
		CreatedAt:  time.Now().UTC().Truncate(time.Second),
	}
	if err := store.CreateEntity(ctx, e); err != nil {
		t.Fatal(err)
	}
	rows, err := store.LintOrphanEntities(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != e.ID {
		t.Fatalf("got %+v", rows)
	}
}

func TestLint_BrokenSupersedeChain(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	raw := store.RawDB()
	if _, err := raw.ExecContext(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = raw.ExecContext(context.Background(), "PRAGMA foreign_keys = ON")
	})
	id := NewID()
	_, err := raw.ExecContext(ctx, `
		INSERT INTO facts (id, source_file, fact_type, subject, content, confidence, created_at, superseded_by)
		VALUES (?, 'x.md', 'preference', 's', 'c', 1.0, ?, 'missing-target-id')`,
		id, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		t.Fatal(err)
	}
	rows, err := store.LintBrokenSupersedeChains(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != id || rows[0].SupersededBy != "missing-target-id" {
		t.Fatalf("got %+v", rows)
	}
}

func TestLint_DedupExact(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	for _, name := range []struct{ id, n string }{
		{NewID(), "Config.toml"},
		{NewID(), "  config.toml "},
	} {
		e := &model.Entity{ID: name.id, Name: name.n, EntityType: model.EntityDocument, CreatedAt: now}
		if err := store.CreateEntity(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := store.LintEntityDedupExact(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 pair, got %d %+v", len(rows), rows)
	}
}

func TestLint_MissingEmbeddingsGrouped(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	for i := 0; i < 2; i++ {
		f := &model.Fact{
			ID:         NewID(),
			Source:     model.Source{TranscriptFile: "a.md"},
			FactType:   model.FactRule,
			Content:    "rule",
			Confidence: 1,
			CreatedAt:  now,
		}
		if err := store.CreateFact(ctx, f); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := store.LintFactsMissingEmbeddingsByType(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].FactType != "rule" || rows[0].Count != 2 {
		t.Fatalf("got %+v", rows)
	}
}

func TestLint_UnconsolidatedCount(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	f := &model.Fact{
		ID:         NewID(),
		Source:     model.Source{TranscriptFile: "a.md"},
		FactType:   model.FactBio,
		Content:    "bio",
		Confidence: 1,
		CreatedAt:  now,
	}
	if err := store.CreateFact(ctx, f); err != nil {
		t.Fatal(err)
	}
	n, err := store.LintUnconsolidatedActiveFactsCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("want 1, got %d", n)
	}
}
