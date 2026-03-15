package consolidation

import (
	"context"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aegis-alpha/imprint-mace/internal/db"
	"github.com/aegis-alpha/imprint-mace/internal/model"
)

type countingConsolidator struct {
	calls atomic.Int32
}

func (c *countingConsolidator) Consolidate(ctx context.Context, limit int) (*ConsolidateResult, error) {
	c.calls.Add(1)
	return nil, nil
}

func TestScheduler_RunsConsolidation(t *testing.T) {
	store, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	now := time.Now()
	for i := 0; i < 5; i++ {
		store.CreateFact(ctx, &model.Fact{
			ID:         db.NewID(),
			Source:     model.Source{TranscriptFile: "test.md"},
			FactType:   "decision",
			Subject:    "Acme",
			Content:    "fact content",
			Confidence: 0.9,
			CreatedAt:  now,
		})
	}

	cc := &countingConsolidator{}
	sched := NewScheduler(cc, store, 50*time.Millisecond, 2, 10, slog.Default())

	ctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()

	sched.Run(ctx)

	if cc.calls.Load() == 0 {
		t.Error("expected at least 1 consolidation call")
	}
}

func TestScheduler_StopsOnContextCancel(t *testing.T) {
	store, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	cc := &countingConsolidator{}
	sched := NewScheduler(cc, store, time.Hour, 0, 10, slog.Default())

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		sched.Run(ctx)
		close(done)
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("scheduler did not stop after context cancel")
	}
}

func TestScheduler_SkipsBelowThreshold(t *testing.T) {
	store, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	store.CreateFact(ctx, &model.Fact{
		ID:         db.NewID(),
		Source:     model.Source{TranscriptFile: "test.md"},
		FactType:   "decision",
		Subject:    "Acme",
		Content:    "only one fact",
		Confidence: 0.9,
		CreatedAt:  time.Now(),
	})

	cc := &countingConsolidator{}
	sched := NewScheduler(cc, store, 50*time.Millisecond, 100, 10, slog.Default())

	ctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()

	sched.Run(ctx)

	if cc.calls.Load() != 0 {
		t.Errorf("expected 0 consolidation calls (below threshold), got %d", cc.calls.Load())
	}
}
