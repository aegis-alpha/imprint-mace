package db

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/aegis-alpha/imprint-mace/internal/model"
)

func TestMigration014_CreatesHotMessages(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	var n int
	err := store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='hot_messages'`).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected hot_messages table, got count %d", n)
	}
}

func TestMigration014_CreatesCooldownMessages(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	var n int
	err := store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='cooldown_messages'`).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected cooldown_messages table, got count %d", n)
	}
}

func TestMigration014_CreatesFTS5Tables(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	for _, name := range []string{"hot_messages_fts", "cooldown_messages_fts"} {
		var n int
		err := store.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&n)
		if err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("expected %s, got count %d", name, n)
		}
	}
}

func TestMigration014_QueryLogColumns(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	rows, err := store.db.QueryContext(ctx, `PRAGMA table_info(query_log)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull, dflt, pk any
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		cols[name] = true
	}
	for _, want := range []string{"hot_by_vector", "hot_by_text", "cooldown_by_vector", "cooldown_by_text"} {
		if !cols[want] {
			t.Fatalf("missing query_log column %q", want)
		}
	}
}

func TestMigration014_Idempotent(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/m14.db"
	s1, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s1.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	s2.Close()
}

func hotTestMsg(id string, ts time.Time, content string) *model.HotMessage {
	return &model.HotMessage{
		ID:                id,
		Speaker:           "user",
		Content:           content,
		Timestamp:         ts,
		Platform:          "p",
		PlatformSessionID: "sess1",
		HasEmbedding:      false,
		CreatedAt:         ts,
	}
}

func TestInsertHotMessage_Basic(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	id := NewID()
	msg := hotTestMsg(id, now, "hello hot path")
	if err := store.InsertHotMessage(ctx, msg, nil); err != nil {
		t.Fatal(err)
	}
	list, err := store.ListHotMessages(ctx, HotMessageFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Content != "hello hot path" {
		t.Fatalf("got %+v", list)
	}
}

func TestInsertHotMessage_FTS5Indexed(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	msg := hotTestMsg(NewID(), now, "uniquekeywordalpha hot fts")
	if err := store.InsertHotMessage(ctx, msg, nil); err != nil {
		t.Fatal(err)
	}
	hits, err := store.SearchHotByText(ctx, "uniquekeywordalpha", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || !strings.Contains(hits[0].Message.Content, "uniquekeywordalpha") {
		t.Fatalf("got %+v", hits)
	}
}

func TestInsertHotMessage_WithEmbedding(t *testing.T) {
	store := vecTestStore(t, 4)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	id := NewID()
	msg := hotTestMsg(id, now, strings.Repeat("x", 50))
	msg.HasEmbedding = true
	emb := []float32{0.1, 0.2, 0.3, 0.4}
	if err := store.InsertHotMessage(ctx, msg, emb); err != nil {
		t.Fatal(err)
	}
	var blob []byte
	err := store.db.QueryRowContext(ctx, `SELECT embedding FROM hot_messages WHERE id = ?`, id).Scan(&blob)
	if err != nil {
		t.Fatal(err)
	}
	if len(blob) == 0 {
		t.Fatal("expected embedding blob")
	}
	q := []float32{0.1, 0.2, 0.3, 0.4}
	res, err := store.SearchHotByVector(ctx, q, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Message.ID != id {
		t.Fatalf("vector search: %+v", res)
	}
}

func TestInsertHotMessage_NilEmbedding(t *testing.T) {
	store := vecTestStore(t, 4)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	id := NewID()
	msg := hotTestMsg(id, now, "short")
	if err := store.InsertHotMessage(ctx, msg, nil); err != nil {
		t.Fatal(err)
	}
	var hasEmb int64
	var emb sql.NullString
	err := store.db.QueryRowContext(ctx, `SELECT has_embedding, embedding FROM hot_messages WHERE id = ?`, id).Scan(&hasEmb, &emb)
	if err != nil {
		t.Fatal(err)
	}
	if hasEmb != 0 || emb.Valid {
		t.Fatalf("hasEmb=%d embValid=%v", hasEmb, emb.Valid)
	}
}

func TestInsertHotMessage_DuplicateID(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	id := NewID()
	msg := hotTestMsg(id, now, "one")
	if err := store.InsertHotMessage(ctx, msg, nil); err != nil {
		t.Fatal(err)
	}
	msg2 := hotTestMsg(id, now, "two")
	if err := store.InsertHotMessage(ctx, msg2, nil); err == nil {
		t.Fatal("expected error on duplicate id")
	}
}

func TestInsertHotMessage_NilFields(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	msg := &model.HotMessage{
		ID:        NewID(),
		Speaker:   "assistant",
		Content:   "no optional fields",
		Timestamp: now,
		CreatedAt: now,
	}
	if err := store.InsertHotMessage(ctx, msg, nil); err != nil {
		t.Fatal(err)
	}
	list, err := store.ListHotMessages(ctx, HotMessageFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("got %d", len(list))
	}
}

func TestListHotMessages_All(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	for i := 0; i < 3; i++ {
		msg := hotTestMsg(NewID(), now.Add(time.Duration(i)*time.Minute), "m")
		if err := store.InsertHotMessage(ctx, msg, nil); err != nil {
			t.Fatal(err)
		}
	}
	list, err := store.ListHotMessages(ctx, HotMessageFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("want 3 got %d", len(list))
	}
}

func TestListHotMessages_ByPlatformSession(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	a := hotTestMsg(NewID(), now, "a")
	a.PlatformSessionID = "alpha"
	b := hotTestMsg(NewID(), now, "b")
	b.PlatformSessionID = "beta"
	_ = store.InsertHotMessage(ctx, a, nil)
	_ = store.InsertHotMessage(ctx, b, nil)
	list, err := store.ListHotMessages(ctx, HotMessageFilter{PlatformSessionID: "alpha", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].PlatformSessionID != "alpha" {
		t.Fatalf("got %+v", list)
	}
}

func TestListHotMessages_ByTimeRange(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	old := hotTestMsg(NewID(), base, "old")
	newm := hotTestMsg(NewID(), base.Add(2*time.Hour), "new")
	_ = store.InsertHotMessage(ctx, old, nil)
	_ = store.InsertHotMessage(ctx, newm, nil)
	after := base.Add(1 * time.Hour)
	list, err := store.ListHotMessages(ctx, HotMessageFilter{After: &after, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Content != "new" {
		t.Fatalf("got %+v", list)
	}
}

func TestListHotMessages_Limit(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	for i := 0; i < 5; i++ {
		_ = store.InsertHotMessage(ctx, hotTestMsg(NewID(), now.Add(time.Duration(i)*time.Second), "x"), nil)
	}
	list, err := store.ListHotMessages(ctx, HotMessageFilter{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("want 2 got %d", len(list))
	}
}

func TestListHotMessages_Empty(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	list, err := store.ListHotMessages(ctx, HotMessageFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("want empty, got %d", len(list))
	}
}

func TestListHotMessages_OrderByTimestamp(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	t2 := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	t1 := t2.Add(-time.Hour)
	_ = store.InsertHotMessage(ctx, hotTestMsg(NewID(), t2, "second"), nil)
	_ = store.InsertHotMessage(ctx, hotTestMsg(NewID(), t1, "first"), nil)
	list, err := store.ListHotMessages(ctx, HotMessageFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Content != "first" || list[1].Content != "second" {
		t.Fatalf("order wrong: %+v", list)
	}
}

func TestSearchHotByText_Match(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	_ = store.InsertHotMessage(ctx, hotTestMsg(NewID(), now, "golang modules are nice"), nil)
	hits, err := store.SearchHotByText(ctx, "golang", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("want 1 hit, got %d", len(hits))
	}
}

func TestSearchHotByText_NoMatch(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	hits, err := store.SearchHotByText(ctx, "zzznomatchzzz", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("want 0, got %d", len(hits))
	}
}

func TestSearchHotByText_Ranking(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	_ = store.InsertHotMessage(ctx, hotTestMsg(NewID(), now, "rust rust rust programming"), nil)
	_ = store.InsertHotMessage(ctx, hotTestMsg(NewID(), now, "go go language"), nil)
	hits, err := store.SearchHotByText(ctx, "rust", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) < 1 {
		t.Fatal("expected hits")
	}
	if !strings.Contains(hits[0].Message.Content, "rust") {
		t.Fatalf("top hit should be rust-heavy: %q", hits[0].Message.Content)
	}
}

func TestSearchHotByText_Sanitization(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	_ = store.InsertHotMessage(ctx, hotTestMsg(NewID(), now, "plain word"), nil)
	hits, err := store.SearchHotByText(ctx, "word AND (bad", 5)
	if err != nil {
		t.Fatal(err)
	}
	// Sanitized query should not error; may be empty or partial match
	_ = hits
}

func TestSearchHotByText_EmptyQuery(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	hits, err := store.SearchHotByText(ctx, "   ", 5)
	if err != nil {
		t.Fatal(err)
	}
	if hits != nil && len(hits) != 0 {
		t.Fatalf("expected empty, got %d", len(hits))
	}
}

func TestSearchHotByVector_Match(t *testing.T) {
	store := vecTestStore(t, 4)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	msg := hotTestMsg(NewID(), now, strings.Repeat("a", 50))
	msg.HasEmbedding = true
	emb := []float32{0.9, 0.1, 0, 0}
	if err := store.InsertHotMessage(ctx, msg, emb); err != nil {
		t.Fatal(err)
	}
	res, err := store.SearchHotByVector(ctx, []float32{0.95, 0.05, 0, 0}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Message.ID != msg.ID {
		t.Fatalf("got %+v", res)
	}
}

func TestSearchHotByVector_NoIndex(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	res, err := store.SearchHotByVector(ctx, []float32{1, 0, 0, 0}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if res != nil && len(res) != 0 {
		t.Fatalf("expected empty without index, got %d", len(res))
	}
}

func TestSearchHotByVector_NilEmbedding(t *testing.T) {
	store := vecTestStore(t, 4)
	ctx := context.Background()
	res, err := store.SearchHotByVector(ctx, nil, 5)
	if err != nil {
		t.Fatal(err)
	}
	if res != nil && len(res) != 0 {
		t.Fatalf("expected empty, got %d", len(res))
	}
}

func TestSearchHotByVector_Limit(t *testing.T) {
	store := vecTestStore(t, 4)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	for i := 0; i < 4; i++ {
		msg := hotTestMsg(NewID(), now.Add(time.Duration(i)*time.Second), strings.Repeat("b", 50))
		msg.HasEmbedding = true
		emb := []float32{float32(i+1) * 0.1, 0.2, 0.3, 0.4}
		if err := store.InsertHotMessage(ctx, msg, emb); err != nil {
			t.Fatal(err)
		}
	}
	res, err := store.SearchHotByVector(ctx, []float32{0.4, 0.2, 0.3, 0.4}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 {
		t.Fatalf("want 2 results, got %d", len(res))
	}
}

func TestSearchCooldownByText_Match(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	old := now.Add(-2 * time.Hour)
	msg := hotTestMsg(NewID(), old, "cooldown keyword beta")
	if err := store.InsertHotMessage(ctx, msg, nil); err != nil {
		t.Fatal(err)
	}
	n, err := store.MoveHotToCooldown(ctx, now, 100)
	if err != nil || n != 1 {
		t.Fatalf("move: n=%d err=%v", n, err)
	}
	hits, err := store.SearchCooldownByText(ctx, "beta", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("want 1, got %d", len(hits))
	}
}

func TestSearchCooldownByText_NoMatch(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	hits, err := store.SearchCooldownByText(ctx, "nothinghere", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("want 0, got %d", len(hits))
	}
}

func TestSearchCooldownByText_EmptyTable(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	hits, err := store.SearchCooldownByText(ctx, "word", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("want 0, got %d", len(hits))
	}
}

func TestSearchCooldownByText_AfterMove(t *testing.T) {
	TestSearchCooldownByText_Match(t)
}

func TestSearchCooldownByVector_Match(t *testing.T) {
	store := vecTestStore(t, 4)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	old := now.Add(-2 * time.Hour)
	msg := hotTestMsg(NewID(), old, strings.Repeat("c", 50))
	msg.HasEmbedding = true
	emb := []float32{0.2, 0.8, 0, 0}
	if err := store.InsertHotMessage(ctx, msg, emb); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MoveHotToCooldown(ctx, now, 100); err != nil {
		t.Fatal(err)
	}
	res, err := store.SearchCooldownByVector(ctx, []float32{0.15, 0.85, 0, 0}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Message.ID != msg.ID {
		t.Fatalf("got %+v", res)
	}
}

func TestSearchCooldownByVector_PrefixFilter(t *testing.T) {
	store := vecTestStore(t, 4)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	old := now.Add(-2 * time.Hour)

	// Fact in vector index
	f := &model.Fact{
		ID: NewID(), Source: model.Source{TranscriptFile: "t.md"},
		FactType: model.FactPreference, Content: strings.Repeat("f", 50), CreatedAt: now,
	}
	if err := store.CreateFact(ctx, f); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateFactEmbedding(ctx, f.ID, []float32{0.9, 0.1, 0, 0}, "m"); err != nil {
		t.Fatal(err)
	}

	hmsg := hotTestMsg(NewID(), old, strings.Repeat("h", 50))
	hmsg.HasEmbedding = true
	if err := store.InsertHotMessage(ctx, hmsg, []float32{0.1, 0.9, 0, 0}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MoveHotToCooldown(ctx, now, 100); err != nil {
		t.Fatal(err)
	}

	res, err := store.SearchCooldownByVector(ctx, []float32{0.15, 0.85, 0, 0}, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		if r.Message.ID == f.ID {
			t.Fatalf("fact id should not appear in cooldown vector results")
		}
	}
	found := false
	for _, r := range res {
		if r.Message.ID == hmsg.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected cooldown message in results, got %+v", res)
	}
}

func TestSearchCooldownByVector_AfterMove(t *testing.T) {
	TestSearchCooldownByVector_Match(t)
}

func TestMoveHotToCooldown_Basic(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	old := now.Add(-time.Hour)
	if err := store.InsertHotMessage(ctx, hotTestMsg(NewID(), old, "oldmsg"), nil); err != nil {
		t.Fatal(err)
	}
	n, err := store.MoveHotToCooldown(ctx, now, 100)
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	cnt, _ := store.CountHotMessages(ctx)
	if cnt != 0 {
		t.Fatalf("hot should be empty, got %d", cnt)
	}
	var c int
	_ = store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM cooldown_messages`).Scan(&c)
	if c != 1 {
		t.Fatalf("cooldown count %d", c)
	}
}

func TestMoveHotToCooldown_PreservesID(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	id := NewID()
	old := now.Add(-time.Hour)
	if err := store.InsertHotMessage(ctx, hotTestMsg(id, old, "x"), nil); err != nil {
		t.Fatal(err)
	}
	_, _ = store.MoveHotToCooldown(ctx, now, 100)
	var cid string
	err := store.db.QueryRowContext(ctx, `SELECT id FROM cooldown_messages WHERE id = ?`, id).Scan(&cid)
	if err != nil {
		t.Fatal(err)
	}
	if cid != id {
		t.Fatalf("id mismatch")
	}
}

func TestMoveHotToCooldown_PreservesEmbedding(t *testing.T) {
	store := vecTestStore(t, 4)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	old := now.Add(-time.Hour)
	id := NewID()
	msg := hotTestMsg(id, old, strings.Repeat("e", 50))
	msg.HasEmbedding = true
	emb := []float32{0.25, 0.25, 0.25, 0.25}
	if err := store.InsertHotMessage(ctx, msg, emb); err != nil {
		t.Fatal(err)
	}
	_, err := store.MoveHotToCooldown(ctx, now, 100)
	if err != nil {
		t.Fatal(err)
	}
	var blob []byte
	err = store.db.QueryRowContext(ctx, `SELECT embedding FROM cooldown_messages WHERE id = ?`, id).Scan(&blob)
	if err != nil || len(blob) == 0 {
		t.Fatalf("embedding in cooldown: %v len=%d", err, len(blob))
	}
	res, err := store.SearchCooldownByVector(ctx, emb, 5)
	if err != nil || len(res) != 1 {
		t.Fatalf("cool vector: %+v err=%v", res, err)
	}
}

func TestMoveHotToCooldown_PreservesFTS5(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	old := now.Add(-time.Hour)
	if err := store.InsertHotMessage(ctx, hotTestMsg(NewID(), old, "ftskeep uniquegamma"), nil); err != nil {
		t.Fatal(err)
	}
	_, _ = store.MoveHotToCooldown(ctx, now, 100)
	hits, err := store.SearchCooldownByText(ctx, "uniquegamma", 5)
	if err != nil || len(hits) != 1 {
		t.Fatalf("hits=%+v err=%v", hits, err)
	}
}

func TestMoveHotToCooldown_SetsMovedFromHot(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	old := now.Add(-time.Hour)
	if err := store.InsertHotMessage(ctx, hotTestMsg(NewID(), old, "z"), nil); err != nil {
		t.Fatal(err)
	}
	_, _ = store.MoveHotToCooldown(ctx, now, 100)
	var mf string
	err := store.db.QueryRowContext(ctx, `SELECT moved_from_hot FROM cooldown_messages LIMIT 1`).Scan(&mf)
	if err != nil || mf == "" {
		t.Fatalf("moved_from_hot=%q err=%v", mf, err)
	}
	if _, err := time.Parse(time.RFC3339, mf); err != nil {
		t.Fatalf("moved_from_hot not RFC3339: %v", err)
	}
}

func TestMoveHotToCooldown_BatchSize(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	old := now.Add(-time.Hour)
	for i := 0; i < 5; i++ {
		_ = store.InsertHotMessage(ctx, hotTestMsg(NewID(), old.Add(time.Duration(i)*time.Second), "b"), nil)
	}
	n, err := store.MoveHotToCooldown(ctx, now, 2)
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	cnt, _ := store.CountHotMessages(ctx)
	if cnt != 3 {
		t.Fatalf("hot left=%d want 3", cnt)
	}
}

func TestMoveHotToCooldown_NewMessagesUnaffected(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	old := now.Add(-time.Hour)
	newTs := now.Add(time.Hour)
	if err := store.InsertHotMessage(ctx, hotTestMsg(NewID(), old, "old"), nil); err != nil {
		t.Fatal(err)
	}
	newID := NewID()
	if err := store.InsertHotMessage(ctx, hotTestMsg(newID, newTs, "new"), nil); err != nil {
		t.Fatal(err)
	}
	_, _ = store.MoveHotToCooldown(ctx, now, 100)
	cnt, _ := store.CountHotMessages(ctx)
	if cnt != 1 {
		t.Fatalf("hot count %d", cnt)
	}
	list, _ := store.ListHotMessages(ctx, HotMessageFilter{Limit: 5})
	if len(list) != 1 || list[0].ID != newID {
		t.Fatalf("wrong hot left: %+v", list)
	}
}

func TestMoveHotToCooldown_EmptyHot(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	n, err := store.MoveHotToCooldown(ctx, time.Now(), 10)
	if err != nil || n != 0 {
		t.Fatalf("n=%d err=%v", n, err)
	}
}

func TestMoveHotToCooldown_Transaction(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	old := now.Add(-time.Hour)
	id := NewID()
	if err := store.InsertHotMessage(ctx, hotTestMsg(id, old, "conflict"), nil); err != nil {
		t.Fatal(err)
	}
	// Pre-insert same id into cooldown so batch INSERT fails with UNIQUE.
	_, err := store.db.ExecContext(ctx, `
		INSERT INTO cooldown_messages (id, speaker, content, timestamp, moved_from_hot, created_at)
		VALUES (?, 'user', 'pre', ?, 'pre-move', ?)`,
		id, old.UTC().Format(time.RFC3339), old.UTC().Format(time.RFC3339))
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.MoveHotToCooldown(ctx, now, 10)
	if err == nil {
		t.Fatal("expected error on unique constraint")
	}
	var hotCount int
	_ = store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM hot_messages WHERE id = ?`, id).Scan(&hotCount)
	if hotCount != 1 {
		t.Fatalf("hot row should remain after rollback, count=%d", hotCount)
	}
}

func TestCountHotMessages(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	n, err := store.CountHotMessages(ctx)
	if err != nil || n != 0 {
		t.Fatalf("empty: %d %v", n, err)
	}
	_ = store.InsertHotMessage(ctx, hotTestMsg(NewID(), time.Now().UTC(), "x"), nil)
	n, err = store.CountHotMessages(ctx)
	if err != nil || n != 1 {
		t.Fatalf("want 1 got %d %v", n, err)
	}
}

func TestStats_IncludesHotAndCooldown(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	old := now.Add(-time.Hour)
	_ = store.InsertHotMessage(ctx, hotTestMsg(NewID(), old, "a"), nil)
	_ = store.InsertHotMessage(ctx, hotTestMsg(NewID(), now, "b"), nil)
	_, _ = store.MoveHotToCooldown(ctx, now, 10)
	st, err := store.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.HotMessages != 1 || st.CooldownMessages != 1 {
		t.Fatalf("stats hot=%d cool=%d", st.HotMessages, st.CooldownMessages)
	}
}
