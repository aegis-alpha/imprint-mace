package main

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/aegis-alpha/imprint-mace/internal/config"
	"github.com/aegis-alpha/imprint-mace/internal/db"
)

func skipIfUSearchBroken(t *testing.T) {
	t.Helper()
	if os.Getenv("IMPRINT_SKIP_USEARCH") != "" {
		t.Skip("IMPRINT_SKIP_USEARCH set -- USearch C library crashes on this platform")
	}
}

type ttlMoveSpy struct {
	*db.SQLiteStore
	mu        sync.Mutex
	cutoffs   []time.Time
	batches   []int
	callCount int
}

func (s *ttlMoveSpy) MoveHotToCooldown(ctx context.Context, olderThan time.Time, batchSize int) (int64, error) {
	s.mu.Lock()
	s.cutoffs = append(s.cutoffs, olderThan)
	s.batches = append(s.batches, batchSize)
	s.callCount++
	s.mu.Unlock()
	return s.SQLiteStore.MoveHotToCooldown(ctx, olderThan, batchSize)
}

func TestHotTTL_CallsMoveHotToCooldown(t *testing.T) {
	skipIfUSearchBroken(t)
	base, err := db.Open(t.TempDir() + "/ttl-spy.db")
	if err != nil {
		t.Fatal(err)
	}
	if err := base.AttachVectorIndex(4); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { base.Close() })

	spy := &ttlMoveSpy{SQLiteStore: base}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hotCfg := config.HotConfig{TTLMinutes: 60, TickSeconds: 1, BatchSize: 77}
	go startHotTTLGoroutine(ctx, spy, hotCfg, slog.Default())

	time.Sleep(2500 * time.Millisecond)
	cancel()
	time.Sleep(50 * time.Millisecond)

	spy.mu.Lock()
	n := spy.callCount
	lastBatch := 0
	if len(spy.batches) > 0 {
		lastBatch = spy.batches[len(spy.batches)-1]
	}
	spy.mu.Unlock()

	if n < 2 {
		t.Fatalf("expected at least 2 MoveHotToCooldown calls, got %d", n)
	}
	if lastBatch != 77 {
		t.Errorf("expected batch size 77, got %d", lastBatch)
	}
}

func TestHotTTL_GracefulShutdown(t *testing.T) {
	skipIfUSearchBroken(t)
	base, err := db.Open(t.TempDir() + "/ttl-shutdown.db")
	if err != nil {
		t.Fatal(err)
	}
	if err := base.AttachVectorIndex(4); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { base.Close() })

	spy := &ttlMoveSpy{SQLiteStore: base}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		startHotTTLGoroutine(ctx, spy, config.HotConfig{TTLMinutes: 60, TickSeconds: 60, BatchSize: 100}, slog.Default())
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("TTL goroutine did not exit after cancelled context")
	}
}
