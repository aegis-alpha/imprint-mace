package segment

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/aegis-alpha/imprint-mace/internal/db"
	"github.com/aegis-alpha/imprint-mace/internal/model"
)

// --- helpers ---

func makeMessages(n int, session string) []model.CooldownMessage {
	msgs := make([]model.CooldownMessage, n)
	base := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)
	for i := range msgs {
		msgs[i] = model.CooldownMessage{
			ID:                db.NewID(),
			Speaker:           "user",
			Content:           fmt.Sprintf("message %d about topic", i),
			Timestamp:         base.Add(time.Duration(i) * time.Minute),
			PlatformSessionID: session,
			CreatedAt:         base.Add(time.Duration(i) * time.Minute),
		}
	}
	return msgs
}

// makeTopicEmbeddings creates embeddings that cluster into distinct topics.
// Each topic gets a distinct direction vector; messages within a topic
// are identical embeddings so cosine similarity within a topic = 1.0
// and across topics < 1.0.
func makeTopicEmbeddings(topicSizes []int, dims int) [][]float32 {
	var embs [][]float32
	for ti, sz := range topicSizes {
		vec := make([]float32, dims)
		vec[ti%dims] = 1.0
		if ti+1 < dims {
			vec[ti+1] = 0.3
		}
		for i := 0; i < sz; i++ {
			cp := make([]float32, dims)
			copy(cp, vec)
			embs = append(embs, cp)
		}
	}
	return embs
}

func allMessageIDs(msgs []model.CooldownMessage) []string {
	ids := make([]string, len(msgs))
	for i, m := range msgs {
		ids[i] = m.ID
	}
	return ids
}

func totalMessages(result SegmentResult) int {
	total := 0
	for _, ids := range result.Clusters {
		total += len(ids)
	}
	return total
}

// --- Pure algorithm tests ---

func TestHybridUnion_EmptySession(t *testing.T) {
	result := HybridUnion(nil, nil, DefaultParams())
	if len(result.Clusters) != 0 {
		t.Errorf("expected 0 clusters for empty input, got %d", len(result.Clusters))
	}
	if result.Order != nil {
		t.Errorf("expected nil order for empty input, got %v", result.Order)
	}
}

func TestHybridUnion_SingleMessage(t *testing.T) {
	msgs := makeMessages(1, "session-1")
	result := HybridUnion(msgs, nil, DefaultParams())

	if len(result.Clusters) != 1 {
		t.Fatalf("expected 1 cluster for single message, got %d", len(result.Clusters))
	}
	if totalMessages(result) != 1 {
		t.Errorf("expected 1 total message, got %d", totalMessages(result))
	}
	if len(result.Order) != 1 {
		t.Errorf("expected 1 cluster in order, got %d", len(result.Order))
	}
}

func TestHybridUnion_HappyPath_MultipleTopics(t *testing.T) {
	// 3 topics: 5 messages, 4 messages, 6 messages = 15 total
	topicSizes := []int{5, 4, 6}
	totalN := 0
	for _, s := range topicSizes {
		totalN += s
	}

	msgs := makeMessages(totalN, "session-multi")
	embs := makeTopicEmbeddings(topicSizes, 32)

	result := HybridUnion(msgs, embs, DefaultParams())

	if totalMessages(result) != totalN {
		t.Errorf("expected %d total messages, got %d", totalN, totalMessages(result))
	}

	if len(result.Clusters) < 2 {
		t.Errorf("expected at least 2 clusters for 3-topic input, got %d", len(result.Clusters))
	}

	// Verify all message IDs are accounted for
	seen := make(map[string]bool)
	for _, ids := range result.Clusters {
		for _, id := range ids {
			if seen[id] {
				t.Errorf("duplicate message ID %s in clusters", id)
			}
			seen[id] = true
		}
	}
	if len(seen) != totalN {
		t.Errorf("expected %d unique messages, got %d", totalN, len(seen))
	}

	// Verify clusters are contiguous (messages within a cluster are adjacent)
	allIDs := allMessageIDs(msgs)
	idToIdx := make(map[string]int, len(allIDs))
	for i, id := range allIDs {
		idToIdx[id] = i
	}
	for cid, ids := range result.Clusters {
		for j := 1; j < len(ids); j++ {
			if idToIdx[ids[j]] != idToIdx[ids[j-1]]+1 {
				t.Errorf("cluster %s has non-contiguous messages at position %d", cid, j)
			}
		}
	}
}

func TestHybridUnion_NoEmbeddings_GracefulDegradation(t *testing.T) {
	msgs := makeMessages(10, "session-no-emb")

	// No embeddings -- all similarities default to 0.5
	result := HybridUnion(msgs, nil, DefaultParams())

	if totalMessages(result) != 10 {
		t.Errorf("expected 10 total messages, got %d", totalMessages(result))
	}

	// With all similarities at 0.5 and threshold 0.5, the algorithm should
	// still produce at least 1 cluster without panicking.
	if len(result.Clusters) < 1 {
		t.Errorf("expected at least 1 cluster, got %d", len(result.Clusters))
	}
}

func TestHybridUnion_PartialEmbeddings(t *testing.T) {
	msgs := makeMessages(6, "session-partial")
	embs := make([][]float32, 6)
	// Only first 3 messages have embeddings
	for i := 0; i < 3; i++ {
		embs[i] = make([]float32, 16)
		embs[i][0] = 1.0
	}
	// Last 3 are nil (no embedding)

	result := HybridUnion(msgs, embs, DefaultParams())
	if totalMessages(result) != 6 {
		t.Errorf("expected 6 total messages, got %d", totalMessages(result))
	}
}

func TestHybridUnion_TwoMessages(t *testing.T) {
	msgs := makeMessages(2, "session-two")
	result := HybridUnion(msgs, nil, DefaultParams())

	if totalMessages(result) != 2 {
		t.Errorf("expected 2 total messages, got %d", totalMessages(result))
	}
}

func TestHybridUnion_LargerSession(t *testing.T) {
	// 4 topics of varying sizes
	topicSizes := []int{8, 12, 6, 10}
	totalN := 0
	for _, s := range topicSizes {
		totalN += s
	}

	msgs := makeMessages(totalN, "session-large")
	embs := makeTopicEmbeddings(topicSizes, 64)

	result := HybridUnion(msgs, embs, DefaultParams())

	if totalMessages(result) != totalN {
		t.Errorf("expected %d total messages, got %d", totalN, totalMessages(result))
	}
	if len(result.Clusters) < 2 {
		t.Errorf("expected multiple clusters for 4-topic input, got %d", len(result.Clusters))
	}
	if len(result.Order) != len(result.Clusters) {
		t.Errorf("order length %d != clusters count %d", len(result.Order), len(result.Clusters))
	}
}

// --- Component tests ---

func TestCosineSimilarity(t *testing.T) {
	tests := []struct {
		name string
		a, b []float32
		want float64
		tol  float64
	}{
		{"identical", []float32{1, 0, 0}, []float32{1, 0, 0}, 1.0, 0.001},
		{"orthogonal", []float32{1, 0, 0}, []float32{0, 1, 0}, 0.0, 0.001},
		{"opposite", []float32{1, 0, 0}, []float32{-1, 0, 0}, -1.0, 0.001},
		{"similar", []float32{1, 1, 0}, []float32{1, 0.9, 0}, 0.998, 0.01},
		{"empty_a", []float32{}, []float32{1, 0}, 0.5, 0.001},
		{"empty_b", []float32{1, 0}, []float32{}, 0.5, 0.001},
		{"mismatched_len", []float32{1, 0}, []float32{1, 0, 0}, 0.5, 0.001},
		{"zero_a", []float32{0, 0, 0}, []float32{1, 0, 0}, 0.5, 0.001},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cosineSimilarity(tt.a, tt.b)
			if math.Abs(got-tt.want) > tt.tol {
				t.Errorf("cosineSimilarity(%v, %v) = %f, want %f (tol %f)", tt.a, tt.b, got, tt.want, tt.tol)
			}
		})
	}
}

func TestJaccardSimilarity(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want float64
		tol  float64
	}{
		{"identical", "hello world", "hello world", 1.0, 0.001},
		{"disjoint", "hello world", "foo bar", 0.0, 0.001},
		{"partial", "hello world foo", "hello bar foo", 0.5, 0.001},
		{"empty_both", "", "", 1.0, 0.001},
		{"empty_one", "hello", "", 0.0, 0.001},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := jaccardSimilarity(tt.a, tt.b)
			if math.Abs(got-tt.want) > tt.tol {
				t.Errorf("jaccardSimilarity(%q, %q) = %f, want %f", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestAdjacentSimilarities_AllNeutralWithoutEmbeddings(t *testing.T) {
	sims := adjacentSimilarities(nil, 5)
	if len(sims) != 4 {
		t.Fatalf("expected 4 similarities, got %d", len(sims))
	}
	for i, s := range sims {
		if s != 0.5 {
			t.Errorf("sims[%d] = %f, want 0.5 (neutral fallback)", i, s)
		}
	}
}

func TestTreeSeg_SingleMessage(t *testing.T) {
	boundaries := treeSeg(nil, 1, 0.001, 7)
	if len(boundaries) != 0 {
		t.Errorf("expected no boundaries for single message, got %v", boundaries)
	}
}

func TestTreeSeg_KGreaterThanN(t *testing.T) {
	sims := []float64{0.5, 0.5}
	boundaries := treeSeg(sims, 3, 0.001, 10)
	if len(boundaries) != 2 {
		t.Errorf("expected 2 boundaries (every message is a segment), got %d: %v", len(boundaries), boundaries)
	}
}

func TestTTMerge_EmptyInput(t *testing.T) {
	boundaries := ttMerge(nil, 0, 3, 5, 0.5)
	if len(boundaries) != 0 {
		t.Errorf("expected no boundaries for empty input, got %v", boundaries)
	}
}

func TestSegmentSizes(t *testing.T) {
	sizes := segmentSizes([]int{3, 7}, 10)
	want := []int{3, 4, 3}
	if len(sizes) != len(want) {
		t.Fatalf("expected %d sizes, got %d", len(want), len(sizes))
	}
	for i := range sizes {
		if sizes[i] != want[i] {
			t.Errorf("sizes[%d] = %d, want %d", i, sizes[i], want[i])
		}
	}
}

func TestUnionBoundaries(t *testing.T) {
	a := []int{3, 7, 15}
	b := []int{5, 7, 20}
	got := unionBoundaries(a, b)
	want := []int{3, 5, 7, 15, 20}
	if len(got) != len(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %d, want %d", i, got[i], want[i])
		}
	}
}

func TestBuildClusters_Contiguity(t *testing.T) {
	msgs := makeMessages(10, "session-build")
	boundaries := []int{3, 7}
	result := buildClusters(msgs, boundaries)

	if len(result.Clusters) != 3 {
		t.Fatalf("expected 3 clusters, got %d", len(result.Clusters))
	}
	if len(result.Order) != 3 {
		t.Fatalf("expected 3 in order, got %d", len(result.Order))
	}

	// Verify sizes: [0..2]=3, [3..6]=4, [7..9]=3
	wantSizes := []int{3, 4, 3}
	for i, cid := range result.Order {
		ids := result.Clusters[cid]
		if len(ids) != wantSizes[i] {
			t.Errorf("cluster %d: expected %d messages, got %d", i, wantSizes[i], len(ids))
		}
	}
}

// --- Integration adapter tests ---

// mockStore implements the subset of db.Store needed by ClusterUnclustered.
type mockStore struct {
	db.Store
	messages       []model.CooldownMessage
	assigned       map[string][]string
	listErr        error
	assignErr      error
	assignCallCount int
}

func newMockStore(msgs []model.CooldownMessage) *mockStore {
	return &mockStore{
		messages: msgs,
		assigned: make(map[string][]string),
	}
}

func (m *mockStore) ListCooldownUnclustered(_ context.Context, _ string, limit int) ([]model.CooldownMessage, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	if limit > 0 && limit < len(m.messages) {
		return m.messages[:limit], nil
	}
	return m.messages, nil
}

func (m *mockStore) AssignCooldownCluster(_ context.Context, clusterID string, messageIDs []string) error {
	m.assignCallCount++
	if m.assignErr != nil {
		return m.assignErr
	}
	m.assigned[clusterID] = messageIDs
	return nil
}

func TestClusterUnclustered_HappyPath(t *testing.T) {
	msgs := makeMessages(12, "session-adapter")
	topicEmbs := makeTopicEmbeddings([]int{6, 6}, 32)
	store := newMockStore(msgs)

	result, err := ClusterUnclustered(context.Background(), store, "session-adapter", 100, topicEmbs, DefaultParams())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if len(store.assigned) == 0 {
		t.Error("expected store.AssignCooldownCluster to be called")
	}

	// All messages should be assigned
	totalAssigned := 0
	for _, ids := range store.assigned {
		totalAssigned += len(ids)
	}
	if totalAssigned != 12 {
		t.Errorf("expected 12 messages assigned, got %d", totalAssigned)
	}
}

func TestClusterUnclustered_EmptySession(t *testing.T) {
	store := newMockStore(nil)

	result, err := ClusterUnclustered(context.Background(), store, "session-empty", 100, nil, DefaultParams())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil result for empty session, got %v", result)
	}
	if len(store.assigned) != 0 {
		t.Error("expected no assignments for empty session")
	}
}

func TestClusterUnclustered_AllAlreadyClustered(t *testing.T) {
	// ListCooldownUnclustered returns empty -- all messages already have cluster IDs
	store := newMockStore([]model.CooldownMessage{})

	result, err := ClusterUnclustered(context.Background(), store, "session-done", 100, nil, DefaultParams())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil result when all clustered, got %v", result)
	}
}

func TestClusterUnclustered_StoreListError(t *testing.T) {
	store := newMockStore(nil)
	store.listErr = fmt.Errorf("db connection lost")

	_, err := ClusterUnclustered(context.Background(), store, "session-err", 100, nil, DefaultParams())
	if err == nil {
		t.Fatal("expected error from store.ListCooldownUnclustered")
	}
}

func TestClusterUnclustered_StoreAssignError_NoPartialState(t *testing.T) {
	msgs := makeMessages(10, "session-assign-err")
	store := newMockStore(msgs)
	store.assignErr = fmt.Errorf("write failed")

	_, err := ClusterUnclustered(context.Background(), store, "session-assign-err", 100, nil, DefaultParams())
	if err == nil {
		t.Fatal("expected error from store.AssignCooldownCluster")
	}
	// The first call to AssignCooldownCluster should fail and return immediately.
	// No partial cluster IDs should be written (the error prevents further calls).
	if store.assignCallCount > 1 {
		t.Errorf("expected at most 1 assign call before error, got %d", store.assignCallCount)
	}
}

// --- Per-session invariant test ---

func TestClusterUnclustered_PerSessionInvariant(t *testing.T) {
	// Two sessions with different messages
	msgsA := makeMessages(5, "session-A")
	msgsB := makeMessages(5, "session-B")

	storeA := newMockStore(msgsA)
	storeB := newMockStore(msgsB)

	resultA, err := ClusterUnclustered(context.Background(), storeA, "session-A", 100, nil, DefaultParams())
	if err != nil {
		t.Fatalf("session-A error: %v", err)
	}
	resultB, err := ClusterUnclustered(context.Background(), storeB, "session-B", 100, nil, DefaultParams())
	if err != nil {
		t.Fatalf("session-B error: %v", err)
	}

	// Cluster IDs must not overlap between sessions
	clusterIDsA := make(map[string]bool)
	if resultA != nil {
		for cid := range resultA.Clusters {
			clusterIDsA[cid] = true
		}
	}
	if resultB != nil {
		for cid := range resultB.Clusters {
			if clusterIDsA[cid] {
				t.Errorf("cluster ID %s appears in both session-A and session-B", cid)
			}
		}
	}

	// Message IDs must not overlap between sessions
	msgIDsA := make(map[string]bool)
	for _, ids := range storeA.assigned {
		for _, id := range ids {
			msgIDsA[id] = true
		}
	}
	for _, ids := range storeB.assigned {
		for _, id := range ids {
			if msgIDsA[id] {
				t.Errorf("message ID %s assigned in both sessions -- cross-session clustering bug", id)
			}
		}
	}
}

// --- Variance / mean utility tests ---

func TestVariance(t *testing.T) {
	tests := []struct {
		name string
		vals []float64
		want float64
		tol  float64
	}{
		{"empty", nil, 0, 0.001},
		{"single", []float64{5.0}, 0, 0.001},
		{"uniform", []float64{3, 3, 3}, 0, 0.001},
		{"simple", []float64{1, 2, 3}, 0.6667, 0.001},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := variance(tt.vals)
			if math.Abs(got-tt.want) > tt.tol {
				t.Errorf("variance(%v) = %f, want %f", tt.vals, got, tt.want)
			}
		})
	}
}

func TestMean(t *testing.T) {
	tests := []struct {
		vals []float64
		want float64
	}{
		{nil, 0},
		{[]float64{2, 4, 6}, 4},
		{[]float64{1}, 1},
	}
	for _, tt := range tests {
		got := mean(tt.vals)
		if got != tt.want {
			t.Errorf("mean(%v) = %f, want %f", tt.vals, got, tt.want)
		}
	}
}
