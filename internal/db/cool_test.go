package db

import (
	"context"
	"testing"
	"time"

	"github.com/aegis-alpha/imprint-mace/internal/model"
)

func TestMigration015_TranscriptPlatformSessionID(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()

	var colCount int
	rows, err := store.db.QueryContext(ctx,
		`PRAGMA table_info(transcripts)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull int
		var dfltValue *string
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dfltValue, &pk); err != nil {
			t.Fatal(err)
		}
		colCount++
		if name == "platform_session_id" {
			found = true
		}
	}
	if !found {
		t.Fatalf("platform_session_id column not found in transcripts (cols=%d)", colCount)
	}
}

func TestMigration015_TranscriptPlatformSessionIndex(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	var n int
	err := store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_transcripts_platform_session'`).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected idx_transcripts_platform_session index, got count %d", n)
	}
}

func TestTranscript_PlatformSessionIDPersistence(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()

	tr := &model.Transcript{
		ID:                NewID(),
		FilePath:          "test/session.md",
		PlatformSessionID: "sess-abc-123",
		ChunkCount:        1,
		CreatedAt:         time.Now().UTC(),
	}
	if err := store.CreateTranscript(ctx, tr); err != nil {
		t.Fatal(err)
	}

	got, err := store.GetTranscript(ctx, tr.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.PlatformSessionID != "sess-abc-123" {
		t.Errorf("expected platform_session_id 'sess-abc-123', got %q", got.PlatformSessionID)
	}

	gotByPath, err := store.GetTranscriptByPath(ctx, "test/session.md")
	if err != nil {
		t.Fatal(err)
	}
	if gotByPath == nil {
		t.Fatal("GetTranscriptByPath returned nil")
	}
	if gotByPath.PlatformSessionID != "sess-abc-123" {
		t.Errorf("by path: expected 'sess-abc-123', got %q", gotByPath.PlatformSessionID)
	}
}

func insertHotMsg(t *testing.T, store *SQLiteStore, id, speaker, content, platformSessionID string, ts time.Time) {
	t.Helper()
	ctx := context.Background()
	hm := &model.HotMessage{
		ID:                id,
		Speaker:           speaker,
		Content:           content,
		Timestamp:         ts,
		PlatformSessionID: platformSessionID,
		HasEmbedding:      false,
		CreatedAt:         ts,
	}
	if err := store.InsertHotMessage(ctx, hm, nil); err != nil {
		t.Fatalf("insert hot %s: %v", id, err)
	}
}

func moveAllToCooldown(t *testing.T, store *SQLiteStore) {
	t.Helper()
	ctx := context.Background()
	future := time.Now().UTC().Add(time.Hour)
	if _, err := store.MoveHotToCooldown(ctx, future, 10000); err != nil {
		t.Fatalf("move to cooldown: %v", err)
	}
}

func TestListCooldownUnclustered(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	insertHotMsg(t, store, "m1", "user", "hello", "sess1", now.Add(-3*time.Minute))
	insertHotMsg(t, store, "m2", "assistant", "world", "sess1", now.Add(-2*time.Minute))
	insertHotMsg(t, store, "m3", "user", "other session", "sess2", now.Add(-time.Minute))
	moveAllToCooldown(t, store)

	msgs, err := store.ListCooldownUnclustered(ctx, "sess1", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 unclustered for sess1, got %d", len(msgs))
	}
	if msgs[0].ID != "m1" || msgs[1].ID != "m2" {
		t.Errorf("unexpected order: %s, %s", msgs[0].ID, msgs[1].ID)
	}

	msgs2, err := store.ListCooldownUnclustered(ctx, "sess2", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs2) != 1 {
		t.Fatalf("expected 1 unclustered for sess2, got %d", len(msgs2))
	}
}

func TestListCooldownUnclustered_ExcludesClustered(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	insertHotMsg(t, store, "c1", "user", "clustered", "sessX", now.Add(-2*time.Minute))
	insertHotMsg(t, store, "c2", "user", "unclustered", "sessX", now.Add(-time.Minute))
	moveAllToCooldown(t, store)

	if err := store.AssignCooldownCluster(ctx, "cluster-A", []string{"c1"}); err != nil {
		t.Fatal(err)
	}

	msgs, err := store.ListCooldownUnclustered(ctx, "sessX", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 unclustered, got %d", len(msgs))
	}
	if msgs[0].ID != "c2" {
		t.Errorf("expected c2, got %s", msgs[0].ID)
	}
}

func TestAssignCooldownCluster(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	insertHotMsg(t, store, "a1", "user", "alpha", "sessA", now.Add(-3*time.Minute))
	insertHotMsg(t, store, "a2", "user", "beta", "sessA", now.Add(-2*time.Minute))
	insertHotMsg(t, store, "a3", "user", "gamma", "sessA", now.Add(-time.Minute))
	moveAllToCooldown(t, store)

	if err := store.AssignCooldownCluster(ctx, "clust-1", []string{"a1", "a2"}); err != nil {
		t.Fatal(err)
	}

	var clusterID string
	err := store.db.QueryRowContext(ctx,
		`SELECT cluster_id FROM cooldown_messages WHERE id = ?`, "a1").Scan(&clusterID)
	if err != nil {
		t.Fatal(err)
	}
	if clusterID != "clust-1" {
		t.Errorf("expected cluster_id 'clust-1', got %q", clusterID)
	}

	var nullCluster *string
	err = store.db.QueryRowContext(ctx,
		`SELECT cluster_id FROM cooldown_messages WHERE id = ?`, "a3").Scan(&nullCluster)
	if err != nil {
		t.Fatal(err)
	}
	if nullCluster != nil {
		t.Errorf("expected nil cluster_id for a3, got %v", *nullCluster)
	}
}

func TestAssignCooldownCluster_DoesNotOverwriteExistingCluster(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	insertHotMsg(t, store, "o1", "user", "one", "sessO", now.Add(-2*time.Minute))
	moveAllToCooldown(t, store)

	if err := store.AssignCooldownCluster(ctx, "first-cluster", []string{"o1"}); err != nil {
		t.Fatal(err)
	}
	if err := store.AssignCooldownCluster(ctx, "second-cluster", []string{"o1"}); err != nil {
		t.Fatal(err)
	}

	var cid string
	err := store.db.QueryRowContext(ctx,
		`SELECT cluster_id FROM cooldown_messages WHERE id = ?`, "o1").Scan(&cid)
	if err != nil {
		t.Fatal(err)
	}
	if cid != "first-cluster" {
		t.Errorf("expected cluster unchanged as first-cluster, got %q", cid)
	}
}

func TestMarkCooldownProcessedBySession_ParsesAsRFC3339(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	insertHotMsg(t, store, "mp1", "user", "x", "sessMP", now)
	moveAllToCooldown(t, store)
	if _, err := store.LinkCooldownToTranscript(ctx, "sessMP", "t.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkCooldownProcessedBySession(ctx, "sessMP"); err != nil {
		t.Fatal(err)
	}

	row := store.db.QueryRowContext(ctx, `
		SELECT id, speaker, content, timestamp, platform, platform_session_id,
			linker_ref, has_embedding,
			cluster_id, transcript_file, transcript_line, processed_at,
			moved_from_hot, created_at
		FROM cooldown_messages WHERE id = ?`, "mp1")
	m, err := scanCooldownMessage(row)
	if err != nil {
		t.Fatal(err)
	}
	if m.ProcessedAt == nil {
		t.Fatal("expected ProcessedAt to parse from RFC3339, got nil")
	}
}

func TestListSessionsWithUnclusteredCooldown(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	insertHotMsg(t, store, "su1", "user", "a", "sessU1", now.Add(-2*time.Minute))
	insertHotMsg(t, store, "su2", "user", "b", "sessU2", now.Add(-time.Minute))
	moveAllToCooldown(t, store)

	sessions, err := store.ListSessionsWithUnclusteredCooldown(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d: %v", len(sessions), sessions)
	}
}

func TestAssignCooldownCluster_EmptyInputs(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()

	if err := store.AssignCooldownCluster(ctx, "", []string{"x"}); err != nil {
		t.Errorf("empty clusterID should be no-op, got %v", err)
	}
	if err := store.AssignCooldownCluster(ctx, "c1", nil); err != nil {
		t.Errorf("nil messageIDs should be no-op, got %v", err)
	}
}

func TestMarkClusterProcessed(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	insertHotMsg(t, store, "p1", "user", "proc me", "sessP", now.Add(-2*time.Minute))
	insertHotMsg(t, store, "p2", "user", "proc me too", "sessP", now.Add(-time.Minute))
	moveAllToCooldown(t, store)

	if err := store.AssignCooldownCluster(ctx, "clust-p", []string{"p1", "p2"}); err != nil {
		t.Fatal(err)
	}

	processedAt := time.Now().UTC().Truncate(time.Second)
	affected, err := store.MarkClusterProcessed(ctx, "clust-p", processedAt)
	if err != nil {
		t.Fatal(err)
	}
	if affected != 2 {
		t.Fatalf("expected 2 rows affected, got %d", affected)
	}

	var pAt string
	err = store.db.QueryRowContext(ctx,
		`SELECT processed_at FROM cooldown_messages WHERE id = ?`, "p1").Scan(&pAt)
	if err != nil {
		t.Fatal(err)
	}
	if pAt == "" {
		t.Fatal("expected processed_at to be set")
	}
}

func TestLinkCooldownToTranscript(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	insertHotMsg(t, store, "l1", "user", "link me", "sessL", now.Add(-2*time.Minute))
	insertHotMsg(t, store, "l2", "user", "link me too", "sessL", now.Add(-time.Minute))
	moveAllToCooldown(t, store)

	n, err := store.LinkCooldownToTranscript(ctx, "sessL", "transcripts/sess.md")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("expected 2 rows updated, got %d", n)
	}

	var tf string
	err = store.db.QueryRowContext(ctx,
		`SELECT transcript_file FROM cooldown_messages WHERE id = ?`, "l1").Scan(&tf)
	if err != nil {
		t.Fatal(err)
	}
	if tf != "transcripts/sess.md" {
		t.Errorf("expected transcript_file 'transcripts/sess.md', got %q", tf)
	}

	var tl int
	err = store.db.QueryRowContext(ctx,
		`SELECT transcript_line FROM cooldown_messages WHERE id = ?`, "l1").Scan(&tl)
	if err != nil {
		t.Fatal(err)
	}
	if tl != 1 {
		t.Errorf("expected transcript_line 1, got %d", tl)
	}
}

func TestCoolPipelineStats(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	insertHotMsg(t, store, "s1", "user", "stat1", "sessS", now.Add(-3*time.Minute))
	insertHotMsg(t, store, "s2", "user", "stat2", "sessS", now.Add(-2*time.Minute))
	insertHotMsg(t, store, "s3", "user", "stat3", "sessS", now.Add(-time.Minute))
	moveAllToCooldown(t, store)

	if err := store.AssignCooldownCluster(ctx, "clust-s1", []string{"s1", "s2"}); err != nil {
		t.Fatal(err)
	}

	stats, err := store.CoolPipelineStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.ClustersPending != 1 {
		t.Errorf("expected 1 pending cluster, got %d", stats.ClustersPending)
	}
	if stats.ClustersExtracted != 0 {
		t.Errorf("expected 0 extracted clusters, got %d", stats.ClustersExtracted)
	}

	processedAt := time.Now().UTC()
	if _, err := store.MarkClusterProcessed(ctx, "clust-s1", processedAt); err != nil {
		t.Fatal(err)
	}

	stats2, err := store.CoolPipelineStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats2.ClustersPending != 0 {
		t.Errorf("expected 0 pending after processing, got %d", stats2.ClustersPending)
	}
	if stats2.ClustersExtracted != 1 {
		t.Errorf("expected 1 extracted, got %d", stats2.ClustersExtracted)
	}
	if stats2.MessagesProcessed != 2 {
		t.Errorf("expected 2 processed messages, got %d", stats2.MessagesProcessed)
	}
}

func TestScanCooldownMessage(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	insertHotMsg(t, store, "scan1", "user", "scan test", "sessScan", now)
	moveAllToCooldown(t, store)

	if err := store.AssignCooldownCluster(ctx, "clust-scan", []string{"scan1"}); err != nil {
		t.Fatal(err)
	}

	row := store.db.QueryRowContext(ctx, `
		SELECT id, speaker, content, timestamp, platform, platform_session_id,
			linker_ref, has_embedding,
			cluster_id, transcript_file, transcript_line, processed_at,
			moved_from_hot, created_at
		FROM cooldown_messages WHERE id = ?`, "scan1")
	m, err := scanCooldownMessage(row)
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != "scan1" {
		t.Errorf("expected ID 'scan1', got %q", m.ID)
	}
	if m.Speaker != "user" {
		t.Errorf("expected speaker 'user', got %q", m.Speaker)
	}
	if m.ClusterID != "clust-scan" {
		t.Errorf("expected cluster_id 'clust-scan', got %q", m.ClusterID)
	}
	if m.PlatformSessionID != "sessScan" {
		t.Errorf("expected platform_session_id 'sessScan', got %q", m.PlatformSessionID)
	}
}

func TestSearchCooldownByText_ReturnsScoredCooldownMessage(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	hm := &model.HotMessage{
		ID:                "fts-cool-1",
		Speaker:           "user",
		Content:           "uniquecoolsearchterm alpha",
		Timestamp:         now.Add(-time.Minute),
		PlatformSessionID: "sessFTS",
		HasEmbedding:      false,
		CreatedAt:         now.Add(-time.Minute),
	}
	if err := store.InsertHotMessage(ctx, hm, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MoveHotToCooldown(ctx, now, 100); err != nil {
		t.Fatal(err)
	}

	hits, err := store.SearchCooldownByText(ctx, "uniquecoolsearchterm", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("expected 1 hit, got %d", len(hits))
	}
	if hits[0].Message.ID != "fts-cool-1" {
		t.Errorf("expected ID 'fts-cool-1', got %q", hits[0].Message.ID)
	}
	if hits[0].Message.PlatformSessionID != "sessFTS" {
		t.Errorf("expected platform_session_id 'sessFTS', got %q", hits[0].Message.PlatformSessionID)
	}
}
