package cooldown

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/aegis-alpha/imprint-mace/internal/config"
	"github.com/aegis-alpha/imprint-mace/internal/db"
	"github.com/aegis-alpha/imprint-mace/internal/imprint"
	"github.com/aegis-alpha/imprint-mace/internal/model"
)

// --- fakeIngester ---

type fakeIngester struct {
	mu      sync.Mutex
	calls   []ingestCall
	failFor map[string]error
}

type ingestCall struct {
	Text       string
	SourceFile string
}

func (f *fakeIngester) Ingest(_ context.Context, text, sourceFile string, _ ...imprint.IngestOption) (*imprint.IngestResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, ingestCall{Text: text, SourceFile: sourceFile})
	if err, ok := f.failFor[sourceFile]; ok {
		return nil, err
	}
	return &imprint.IngestResult{FactsCount: 1}, nil
}

func (f *fakeIngester) ingestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeIngester) lastCall() ingestCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[len(f.calls)-1]
}

// --- fakeStore ---

type fakeStore struct {
	db.Store // embed to satisfy interface; panics on unimplemented methods

	mu              sync.Mutex
	clusters        []db.CooldownCluster
	clusterMessages map[string][]model.CooldownMessage
	// claimedClusters simulates rows already claimed/processed for MarkClusterProcessed.
	claimedClusters map[string]bool
	markCalls       []string
	clearCalls      []string
	markErr         error
	clearErr        error
	listClustersErr error
	listMessagesErr error
	listSessions    []string
	listSessionsErr error
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		clusterMessages: make(map[string][]model.CooldownMessage),
		claimedClusters: make(map[string]bool),
	}
}

func (s *fakeStore) ListSessionsWithUnclusteredCooldown(_ context.Context) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listSessionsErr != nil {
		return nil, s.listSessionsErr
	}
	out := make([]string, len(s.listSessions))
	copy(out, s.listSessions)
	return out, nil
}

func (s *fakeStore) ClearClusterProcessed(_ context.Context, clusterID string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clearCalls = append(s.clearCalls, clusterID)
	if s.clearErr != nil {
		return 0, s.clearErr
	}
	if !s.claimedClusters[clusterID] {
		return 0, nil
	}
	delete(s.claimedClusters, clusterID)
	return int64(len(s.clusterMessages[clusterID])), nil
}

func (s *fakeStore) ListClustersReadyForExtraction(_ context.Context, _ int, _ int) ([]db.CooldownCluster, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listClustersErr != nil {
		return nil, s.listClustersErr
	}
	out := make([]db.CooldownCluster, len(s.clusters))
	copy(out, s.clusters)
	return out, nil
}

func (s *fakeStore) ListClusterMessages(_ context.Context, clusterID string) ([]model.CooldownMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listMessagesErr != nil {
		return nil, s.listMessagesErr
	}
	msgs, ok := s.clusterMessages[clusterID]
	if !ok {
		return nil, nil
	}
	out := make([]model.CooldownMessage, len(msgs))
	copy(out, msgs)
	return out, nil
}

func (s *fakeStore) MarkClusterProcessed(_ context.Context, clusterID string, _ time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.markCalls = append(s.markCalls, clusterID)
	if s.markErr != nil {
		return 0, s.markErr
	}
	if s.claimedClusters[clusterID] {
		return 0, nil
	}
	msgs := s.clusterMessages[clusterID]
	if len(msgs) == 0 {
		return 0, nil
	}
	s.claimedClusters[clusterID] = true
	return int64(len(msgs)), nil
}

func (s *fakeStore) addCluster(cl db.CooldownCluster, msgs []model.CooldownMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clusters = append(s.clusters, cl)
	s.clusterMessages[cl.ClusterID] = msgs
}

func (s *fakeStore) markCallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.markCalls)
}

func (s *fakeStore) clearCallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.clearCalls)
}

// --- helpers ---

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

func testCoolCfg() config.CoolConfig {
	enabled := true
	return config.CoolConfig{
		Enabled:        &enabled,
		TickSeconds:    1,
		SilenceHours:   8,
		MaxClusterSize: 50,
	}
}

func makeMsgs(clusterID string, n int) []model.CooldownMessage {
	msgs := make([]model.CooldownMessage, n)
	base := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)
	for i := range msgs {
		msgs[i] = model.CooldownMessage{
			ID:        fmt.Sprintf("msg-%s-%d", clusterID, i),
			Speaker:   "user",
			Content:   fmt.Sprintf("message %d", i),
			Timestamp: base.Add(time.Duration(i) * time.Minute),
			ClusterID: clusterID,
		}
	}
	return msgs
}

// --- tests ---

func TestWorker_HappyPath_SizeTrigger(t *testing.T) {
	store := newFakeStore()
	msgs := makeMsgs("clust-1", 3)
	store.addCluster(db.CooldownCluster{
		ClusterID:   "clust-1",
		TriggerKind: "size",
		MessageCount: 3,
	}, msgs)

	ing := &fakeIngester{}
	w := NewWorker(store, ing, nil, testCoolCfg(), testLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.tick(ctx)

	if ing.ingestCount() != 1 {
		t.Fatalf("expected 1 ingest call, got %d", ing.ingestCount())
	}
	call := ing.lastCall()
	if call.SourceFile != "cooldown-cluster/clust-1" {
		t.Errorf("expected source 'cooldown-cluster/clust-1', got %q", call.SourceFile)
	}
	if store.markCallCount() != 1 {
		t.Fatalf("expected 1 mark call, got %d", store.markCallCount())
	}
}

func TestWorker_SilenceTrigger(t *testing.T) {
	store := newFakeStore()
	msgs := makeMsgs("clust-silence", 2)
	store.addCluster(db.CooldownCluster{
		ClusterID:   "clust-silence",
		TriggerKind: "silence",
		MessageCount: 2,
	}, msgs)

	ing := &fakeIngester{}
	w := NewWorker(store, ing, nil, testCoolCfg(), testLogger())

	ctx := context.Background()
	w.tick(ctx)

	if ing.ingestCount() != 1 {
		t.Fatalf("expected 1 ingest, got %d", ing.ingestCount())
	}
}

func TestWorker_ConcurrencyGuard_ZeroAffected(t *testing.T) {
	store := newFakeStore()
	msgs := makeMsgs("clust-dup", 2)
	store.addCluster(db.CooldownCluster{
		ClusterID:   "clust-dup",
		TriggerKind: "size",
		MessageCount: 2,
	}, msgs)
	store.claimedClusters["clust-dup"] = true // another tick already claimed

	ing := &fakeIngester{}
	w := NewWorker(store, ing, nil, testCoolCfg(), testLogger())

	ctx := context.Background()
	w.tick(ctx)

	if ing.ingestCount() != 0 {
		t.Fatalf("ingest should not run when claim returns 0, got %d", ing.ingestCount())
	}
	if store.markCallCount() != 1 {
		t.Fatalf("mark should be called once, got %d", store.markCallCount())
	}
}

func TestWorker_IngestFails_NotMarkedProcessed(t *testing.T) {
	store := newFakeStore()
	msgs := makeMsgs("clust-fail", 2)
	store.addCluster(db.CooldownCluster{
		ClusterID:   "clust-fail",
		TriggerKind: "size",
		MessageCount: 2,
	}, msgs)

	ing := &fakeIngester{
		failFor: map[string]error{
			"cooldown-cluster/clust-fail": errors.New("provider timeout"),
		},
	}
	w := NewWorker(store, ing, nil, testCoolCfg(), testLogger())

	ctx := context.Background()
	w.tick(ctx)

	if ing.ingestCount() != 1 {
		t.Fatalf("expected 1 ingest attempt, got %d", ing.ingestCount())
	}
	if store.markCallCount() != 1 {
		t.Fatalf("expected 1 claim (mark) before ingest, got %d", store.markCallCount())
	}
	if store.clearCallCount() != 1 {
		t.Fatalf("expected 1 clear after ingest failure, got %d", store.clearCallCount())
	}
	if store.claimedClusters["clust-fail"] {
		t.Fatal("cluster should be unclaimed after rollback")
	}
}

func TestWorker_EmptyCluster_Skipped(t *testing.T) {
	store := newFakeStore()
	store.addCluster(db.CooldownCluster{
		ClusterID:   "clust-empty",
		TriggerKind: "silence",
		MessageCount: 0,
	}, nil) // no messages

	ing := &fakeIngester{}
	w := NewWorker(store, ing, nil, testCoolCfg(), testLogger())

	ctx := context.Background()
	w.tick(ctx)

	if ing.ingestCount() != 0 {
		t.Fatalf("ingest should not be called for empty cluster, got %d", ing.ingestCount())
	}
}

func TestWorker_NoClusters(t *testing.T) {
	store := newFakeStore()
	ing := &fakeIngester{}
	w := NewWorker(store, ing, nil, testCoolCfg(), testLogger())

	ctx := context.Background()
	w.tick(ctx)

	if ing.ingestCount() != 0 {
		t.Fatalf("no clusters means no ingest, got %d", ing.ingestCount())
	}
}

func TestWorker_SegmenterRunsForEachSessionWithUnclustered(t *testing.T) {
	var mu sync.Mutex
	var sessions []string
	segmenter := func(_ context.Context, sessionID string) error {
		mu.Lock()
		sessions = append(sessions, sessionID)
		mu.Unlock()
		return nil
	}
	store := newFakeStore()
	store.listSessions = []string{"sess-a", "sess-b"}
	ing := &fakeIngester{}
	w := NewWorker(store, ing, segmenter, testCoolCfg(), testLogger())
	w.tick(context.Background())

	if len(sessions) != 2 {
		t.Fatalf("expected 2 segmenter calls, got %d (%v)", len(sessions), sessions)
	}
}

func TestWorker_ListClustersError(t *testing.T) {
	store := newFakeStore()
	store.listClustersErr = errors.New("db locked")
	ing := &fakeIngester{}
	w := NewWorker(store, ing, nil, testCoolCfg(), testLogger())

	ctx := context.Background()
	w.tick(ctx)

	if ing.ingestCount() != 0 {
		t.Fatalf("should not ingest on list error, got %d", ing.ingestCount())
	}
}

func TestWorker_ListMessagesError(t *testing.T) {
	store := newFakeStore()
	store.listMessagesErr = errors.New("io error")
	store.addCluster(db.CooldownCluster{
		ClusterID:   "clust-err",
		TriggerKind: "size",
		MessageCount: 5,
	}, nil)

	ing := &fakeIngester{}
	w := NewWorker(store, ing, nil, testCoolCfg(), testLogger())

	ctx := context.Background()
	w.tick(ctx)

	if ing.ingestCount() != 0 {
		t.Fatalf("should not ingest on list messages error, got %d", ing.ingestCount())
	}
}

func TestWorker_MultipleClusters(t *testing.T) {
	store := newFakeStore()
	for i := 0; i < 3; i++ {
		id := fmt.Sprintf("multi-%d", i)
		store.addCluster(db.CooldownCluster{
			ClusterID:   id,
			TriggerKind: "size",
			MessageCount: 2,
		}, makeMsgs(id, 2))
	}

	ing := &fakeIngester{}
	w := NewWorker(store, ing, nil, testCoolCfg(), testLogger())

	ctx := context.Background()
	w.tick(ctx)

	if ing.ingestCount() != 3 {
		t.Fatalf("expected 3 ingest calls, got %d", ing.ingestCount())
	}
	if store.markCallCount() != 3 {
		t.Fatalf("expected 3 mark calls, got %d", store.markCallCount())
	}
}

func TestWorker_RunStopsOnCancel(t *testing.T) {
	store := newFakeStore()
	ing := &fakeIngester{}
	cfg := testCoolCfg()
	cfg.TickSeconds = 1
	w := NewWorker(store, ing, nil, cfg, testLogger())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not stop within 3s after cancel")
	}
}

func TestBuildClusterText(t *testing.T) {
	msgs := []model.CooldownMessage{
		{
			Speaker:   "user",
			Content:   "hello world",
			Timestamp: time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
		},
		{
			Speaker:   "assistant",
			Content:   "hi there",
			Timestamp: time.Date(2026, 4, 1, 10, 5, 0, 0, time.UTC),
		},
	}

	got := buildClusterText(msgs)
	expected := "[user, 2026-04-01 10:00]: hello world\n[assistant, 2026-04-01 10:05]: hi there\n"
	if got != expected {
		t.Errorf("buildClusterText mismatch:\ngot:  %q\nwant: %q", got, expected)
	}
}
