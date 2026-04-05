package ingest

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/aegis-alpha/imprint-mace/internal/db"
	"github.com/aegis-alpha/imprint-mace/internal/model"
	"github.com/aegis-alpha/imprint-mace/internal/provider"
)

// insertCooldownRow inserts a hot message and moves it to cooldown for testing.
func insertCooldownRow(t *testing.T, store *db.SQLiteStore, id, content, platformSessionID string, ts time.Time) {
	t.Helper()
	ctx := context.Background()
	hm := &model.HotMessage{
		ID:                id,
		Speaker:           "user",
		Content:           content,
		Timestamp:         ts,
		PlatformSessionID: platformSessionID,
		HasEmbedding:      false,
		CreatedAt:         ts,
	}
	if err := store.InsertHotMessage(ctx, hm, nil); err != nil {
		t.Fatalf("insert hot %s: %v", id, err)
	}
	future := ts.Add(time.Hour)
	if _, err := store.MoveHotToCooldown(ctx, future, 10000); err != nil {
		t.Fatalf("move to cooldown: %v", err)
	}
}

func openTestStore(t *testing.T) *db.SQLiteStore {
	t.Helper()
	store, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestBatchIngest_LinksCooldownToTranscript(t *testing.T) {
	sender := &mockSender{response: &provider.Response{
		Content: mockJSON, ProviderName: "mock", Model: "test", TokensUsed: 50,
	}}
	adapter, store, dir := testSetup(t, sender)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	sqlStore := store.(*db.SQLiteStore)

	insertCooldownRow(t, sqlStore, "cool-link-1", "hello from session", "sess-link", now.Add(-3*time.Minute))
	insertCooldownRow(t, sqlStore, "cool-link-2", "more from session", "sess-link", now.Add(-2*time.Minute))

	content := "---\nsource: openclaw\nsession: sess-link\ndate: 2026-03-16\nparticipants: [user, assistant]\n---\n\nAlice decided to use Go for Acme.\n"
	writeFile(t, dir, "transcript.md", content)

	result, err := adapter.ProcessDir(ctx, dir)
	if err != nil {
		t.Fatalf("ProcessDir: %v", err)
	}
	if result.FactsTotal < 1 {
		t.Fatalf("expected at least 1 fact, got %d", result.FactsTotal)
	}

	rawDB := sqlStore.RawDB()

	var tf sql.NullString
	err = rawDB.QueryRowContext(ctx,
		`SELECT transcript_file FROM cooldown_messages WHERE id = ?`, "cool-link-1").Scan(&tf)
	if err != nil {
		t.Fatal(err)
	}
	if !tf.Valid || tf.String != "transcript.md" {
		t.Errorf("expected transcript_file 'transcript.md', got %q (valid=%v)", tf.String, tf.Valid)
	}

	var pa sql.NullString
	err = rawDB.QueryRowContext(ctx,
		`SELECT processed_at FROM cooldown_messages WHERE id = ?`, "cool-link-1").Scan(&pa)
	if err != nil {
		t.Fatal(err)
	}
	if !pa.Valid || pa.String == "" {
		t.Error("expected processed_at to be set (batch produced facts), but it is NULL")
	}

	var pa2 sql.NullString
	err = rawDB.QueryRowContext(ctx,
		`SELECT processed_at FROM cooldown_messages WHERE id = ?`, "cool-link-2").Scan(&pa2)
	if err != nil {
		t.Fatal(err)
	}
	if !pa2.Valid || pa2.String == "" {
		t.Error("expected processed_at on cool-link-2 to be set, but it is NULL")
	}
}

func TestBatchIngest_NoCooldownRows_NoError(t *testing.T) {
	sender := &mockSender{response: &provider.Response{
		Content: mockJSON, ProviderName: "mock", Model: "test", TokensUsed: 50,
	}}
	adapter, _, dir := testSetup(t, sender)
	ctx := context.Background()

	content := "---\nsource: openclaw\nsession: sess-empty\ndate: 2026-03-16\nparticipants: [user]\n---\n\nAlice decided to use Go for Acme.\n"
	writeFile(t, dir, "transcript.md", content)

	result, err := adapter.ProcessDir(ctx, dir)
	if err != nil {
		t.Fatalf("ProcessDir: %v", err)
	}
	if result.FilesProcessed != 1 {
		t.Errorf("expected 1 file processed, got %d", result.FilesProcessed)
	}
	if len(result.Errors) != 0 {
		t.Errorf("expected 0 errors, got %d: %v", len(result.Errors), result.Errors)
	}
}

func TestBatchIngest_NoFacts_TranscriptFileSetButNotProcessed(t *testing.T) {
	emptyJSON := `{"facts": [], "entities": [], "relationships": []}`
	sender := &mockSender{response: &provider.Response{
		Content: emptyJSON, ProviderName: "mock", Model: "test", TokensUsed: 10,
	}}
	adapter, store, dir := testSetup(t, sender)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	sqlStore := store.(*db.SQLiteStore)

	insertCooldownRow(t, sqlStore, "cool-nofact-1", "hello no facts", "sess-nofact", now.Add(-2*time.Minute))

	content := "---\nsource: openclaw\nsession: sess-nofact\ndate: 2026-03-16\nparticipants: [user]\n---\n\nSome text with no extractable facts.\n"
	writeFile(t, dir, "transcript.md", content)

	result, err := adapter.ProcessDir(ctx, dir)
	if err != nil {
		t.Fatalf("ProcessDir: %v", err)
	}
	if result.FactsTotal != 0 {
		t.Fatalf("expected 0 facts, got %d", result.FactsTotal)
	}

	rawDB := sqlStore.RawDB()

	var tf sql.NullString
	err = rawDB.QueryRowContext(ctx,
		`SELECT transcript_file FROM cooldown_messages WHERE id = ?`, "cool-nofact-1").Scan(&tf)
	if err != nil {
		t.Fatal(err)
	}
	if !tf.Valid || tf.String != "transcript.md" {
		t.Errorf("expected transcript_file 'transcript.md', got %q (valid=%v)", tf.String, tf.Valid)
	}

	var pa sql.NullString
	err = rawDB.QueryRowContext(ctx,
		`SELECT processed_at FROM cooldown_messages WHERE id = ?`, "cool-nofact-1").Scan(&pa)
	if err != nil {
		t.Fatal(err)
	}
	if pa.Valid {
		t.Errorf("expected processed_at to be NULL (no facts extracted), got %q", pa.String)
	}
}

func TestBatchIngest_MultipleSessions_OnlyMatchingLinked(t *testing.T) {
	sender := &mockSender{response: &provider.Response{
		Content: mockJSON, ProviderName: "mock", Model: "test", TokensUsed: 50,
	}}
	adapter, store, dir := testSetup(t, sender)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	sqlStore := store.(*db.SQLiteStore)

	insertCooldownRow(t, sqlStore, "cool-multi-1", "session A msg", "sess-A", now.Add(-3*time.Minute))
	insertCooldownRow(t, sqlStore, "cool-multi-2", "session B msg", "sess-B", now.Add(-2*time.Minute))

	content := fmt.Sprintf("---\nsource: openclaw\nsession: sess-A\ndate: 2026-03-16\nparticipants: [user]\n---\n\nAlice decided to use Go for Acme.\n")
	writeFile(t, dir, "transcript-a.md", content)

	_, err := adapter.ProcessDir(ctx, dir)
	if err != nil {
		t.Fatalf("ProcessDir: %v", err)
	}

	rawDB := sqlStore.RawDB()

	var tfA sql.NullString
	err = rawDB.QueryRowContext(ctx,
		`SELECT transcript_file FROM cooldown_messages WHERE id = ?`, "cool-multi-1").Scan(&tfA)
	if err != nil {
		t.Fatal(err)
	}
	if !tfA.Valid || tfA.String != "transcript-a.md" {
		t.Errorf("sess-A row: expected transcript_file 'transcript-a.md', got %q", tfA.String)
	}

	var tfB sql.NullString
	err = rawDB.QueryRowContext(ctx,
		`SELECT transcript_file FROM cooldown_messages WHERE id = ?`, "cool-multi-2").Scan(&tfB)
	if err != nil {
		t.Fatal(err)
	}
	if tfB.Valid {
		t.Errorf("sess-B row: expected transcript_file NULL (different session), got %q", tfB.String)
	}

	var paB sql.NullString
	err = rawDB.QueryRowContext(ctx,
		`SELECT processed_at FROM cooldown_messages WHERE id = ?`, "cool-multi-2").Scan(&paB)
	if err != nil {
		t.Fatal(err)
	}
	if paB.Valid {
		t.Errorf("sess-B row: expected processed_at NULL, got %q", paB.String)
	}
}

func TestBatchIngest_UnchangedFileHashStillLinksAndMarksCooldown(t *testing.T) {
	sender := &mockSender{response: &provider.Response{
		Content: mockJSON, ProviderName: "mock", Model: "test", TokensUsed: 50,
	}}
	adapter, store, dir := testSetup(t, sender)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	sqlStore := store.(*db.SQLiteStore)

	content := "---\nsource: openclaw\nsession: sess-hash-dedup\ndate: 2026-03-16\nparticipants: [user]\n---\n\nAlice decided to use Go for Acme.\n"
	writeFile(t, dir, "transcript.md", content)

	result1, err := adapter.ProcessDir(ctx, dir)
	if err != nil {
		t.Fatalf("first ProcessDir: %v", err)
	}
	if result1.FilesProcessed != 1 {
		t.Fatalf("first run: expected 1 file processed, got %d", result1.FilesProcessed)
	}

	insertCooldownRow(t, sqlStore, "cool-after-hash", "late cooldown", "sess-hash-dedup", now.Add(-time.Minute))

	result2, err := adapter.ProcessDir(ctx, dir)
	if err != nil {
		t.Fatalf("second ProcessDir: %v", err)
	}
	if result2.FilesProcessed != 0 {
		t.Errorf("second run: expected 0 files processed (hash unchanged), got %d", result2.FilesProcessed)
	}
	if result2.FilesSkipped != 1 {
		t.Errorf("second run: expected 1 file skipped, got %d", result2.FilesSkipped)
	}

	rawDB := sqlStore.RawDB()
	var tf sql.NullString
	err = rawDB.QueryRowContext(ctx,
		`SELECT transcript_file FROM cooldown_messages WHERE id = ?`, "cool-after-hash").Scan(&tf)
	if err != nil {
		t.Fatal(err)
	}
	if !tf.Valid || tf.String != "transcript.md" {
		t.Errorf("expected transcript_file linked to transcript.md, got %q valid=%v", tf.String, tf.Valid)
	}

	var pa sql.NullString
	err = rawDB.QueryRowContext(ctx,
		`SELECT processed_at FROM cooldown_messages WHERE id = ?`, "cool-after-hash").Scan(&pa)
	if err != nil {
		t.Fatal(err)
	}
	if !pa.Valid || pa.String == "" {
		t.Error("expected processed_at set after dedup path (batch had facts)")
	}
}
