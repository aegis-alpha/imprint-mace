package cooldown

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/aegis-alpha/imprint-mace/internal/config"
	"github.com/aegis-alpha/imprint-mace/internal/db"
	"github.com/aegis-alpha/imprint-mace/internal/model"
	"github.com/aegis-alpha/imprint-mace/internal/segment"
)

func TestWorkerIntegration_SegmentAndExtractSizeTrigger(t *testing.T) {
	store, err := db.Open(t.TempDir() + "/cool-e2e.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	sess := "sess-worker-e2e"

	for i, id := range []string{"e2e-1", "e2e-2"} {
		ts := now.Add(time.Duration(i) * time.Minute)
		hm := &model.HotMessage{
			ID:                id,
			Speaker:           "user",
			Content:           "msg content",
			Timestamp:         ts,
			PlatformSessionID: sess,
			HasEmbedding:      false,
			CreatedAt:         ts,
		}
		if err := store.InsertHotMessage(ctx, hm, nil); err != nil {
			t.Fatal(err)
		}
	}
	future := now.Add(2 * time.Hour)
	if _, err := store.MoveHotToCooldown(ctx, future, 1000); err != nil {
		t.Fatal(err)
	}

	enabled := true
	cfg := config.CoolConfig{
		Enabled:        &enabled,
		TickSeconds:    1,
		SilenceHours:   999,
		MaxClusterSize: 2,
	}
	ing := &fakeIngester{}
	segParams := segment.DefaultParams()
	segParams.TreeK = 1 // n=2 messages: default TreeK>=n forces a split; single cluster needed for size trigger
	segmenter := func(ctx context.Context, sessionID string) error {
		_, err := segment.ClusterUnclustered(ctx, store, sessionID, 1000, nil, segParams)
		return err
	}
	w := NewWorker(store, ing, segmenter, cfg, testLogger())
	w.tick(ctx)

	if ing.ingestCount() != 1 {
		t.Fatalf("expected 1 ingest, got %d", ing.ingestCount())
	}

	var c1, c2 string
	if err := store.RawDB().QueryRowContext(ctx,
		`SELECT cluster_id FROM cooldown_messages WHERE id = ?`, "e2e-1").Scan(&c1); err != nil {
		t.Fatal(err)
	}
	if err := store.RawDB().QueryRowContext(ctx,
		`SELECT cluster_id FROM cooldown_messages WHERE id = ?`, "e2e-2").Scan(&c2); err != nil {
		t.Fatal(err)
	}
	if c1 == "" || c2 == "" || c1 != c2 {
		t.Fatalf("expected same non-empty cluster_id, got %q and %q", c1, c2)
	}
}

func TestWorker_ConcurrentProcessCluster_SingleIngest(t *testing.T) {
	store, err := db.Open(t.TempDir() + "/cool-conc.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	sess := "sess-conc"

	for i, id := range []string{"conc-1", "conc-2"} {
		ts := now.Add(time.Duration(i) * time.Minute)
		hm := &model.HotMessage{
			ID:                id,
			Speaker:           "user",
			Content:           "x",
			Timestamp:         ts,
			PlatformSessionID: sess,
			HasEmbedding:      false,
			CreatedAt:         ts,
		}
		if err := store.InsertHotMessage(ctx, hm, nil); err != nil {
			t.Fatal(err)
		}
	}
	future := now.Add(2 * time.Hour)
	if _, err := store.MoveHotToCooldown(ctx, future, 1000); err != nil {
		t.Fatal(err)
	}
	if _, err := segment.ClusterUnclustered(ctx, store, sess, 1000, nil, segment.DefaultParams()); err != nil {
		t.Fatal(err)
	}

	var clusterID string
	if err := store.RawDB().QueryRowContext(ctx,
		`SELECT cluster_id FROM cooldown_messages WHERE id = ?`, "conc-1").Scan(&clusterID); err != nil {
		t.Fatal(err)
	}
	if clusterID == "" {
		t.Fatal("expected cluster_id")
	}

	enabled := true
	cfg := config.CoolConfig{
		Enabled:        &enabled,
		TickSeconds:    1,
		SilenceHours:   999,
		MaxClusterSize: 2,
	}
	ing := &fakeIngester{}
	w := NewWorker(store, ing, nil, cfg, testLogger())

	cl := db.CooldownCluster{
		ClusterID:    clusterID,
		TriggerKind:  "size",
		MessageCount: 2,
	}

	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			w.processCluster(ctx, cl)
		}()
	}
	wg.Wait()

	if ing.ingestCount() != 1 {
		t.Fatalf("expected exactly 1 ingest under concurrent processCluster, got %d", ing.ingestCount())
	}
}
