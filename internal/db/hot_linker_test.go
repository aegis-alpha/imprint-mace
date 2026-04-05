package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aegis-alpha/imprint-mace/internal/model"
)

func TestGetRecentHotMessages_NewestFirst(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	sess := "ps-1"
	base := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	id1 := NewID()
	id2 := NewID()
	id3 := NewID()
	m1 := hotTestMsg(id1, base.Add(time.Second), "a")
	m1.PlatformSessionID = sess
	m2 := hotTestMsg(id2, base.Add(3*time.Second), "b")
	m2.PlatformSessionID = sess
	m3 := hotTestMsg(id3, base.Add(2*time.Second), "c")
	m3.PlatformSessionID = sess
	_ = store.InsertHotMessage(ctx, m1, nil)
	_ = store.InsertHotMessage(ctx, m2, nil)
	_ = store.InsertHotMessage(ctx, m3, nil)

	got, err := store.GetRecentHotMessages(ctx, sess, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("len %d", len(got))
	}
	// Newest by timestamp: b (t+3s), c (t+2s), a (t+1s)
	if got[0].ID != id2 || got[1].ID != id3 || got[2].ID != id1 {
		t.Fatalf("order: %+v", idsOf(got))
	}
}

func TestGetRecentHotMessages_EmptySessionID(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	got, err := store.GetRecentHotMessages(ctx, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil && len(got) != 0 {
		t.Fatalf("want empty, got %d", len(got))
	}
}

func TestGetLinkedMessages_ChainABC(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	sess := "chain"
	base := time.Date(2026, 4, 2, 10, 0, 0, 0, time.UTC)
	idC := NewID()
	idB := NewID()
	idA := NewID()
	mC := hotTestMsg(idC, base, "c")
	mC.PlatformSessionID = sess
	mB := hotTestMsg(idB, base.Add(time.Second), "b")
	mB.PlatformSessionID = sess
	mB.LinkerRef = idC
	mA := hotTestMsg(idA, base.Add(2*time.Second), "a")
	mA.PlatformSessionID = sess
	mA.LinkerRef = idB
	_ = store.InsertHotMessage(ctx, mC, nil)
	_ = store.InsertHotMessage(ctx, mB, nil)
	_ = store.InsertHotMessage(ctx, mA, nil)

	chain, err := store.GetLinkedMessages(ctx, idA)
	if err != nil {
		t.Fatal(err)
	}
	if len(chain) != 3 {
		t.Fatalf("len %d", len(chain))
	}
	if chain[0].ID != idC || chain[1].ID != idB || chain[2].ID != idA {
		t.Fatalf("order %v", idsOf(chain))
	}
	if chain[0].LinkerRef != "" || chain[1].LinkerRef != idC || chain[2].LinkerRef != idB {
		t.Fatalf("refs: %+v %+v %+v", chain[0].LinkerRef, chain[1].LinkerRef, chain[2].LinkerRef)
	}
}

func TestGetLinkedMessages_FirstMessageNoRef(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	id := NewID()
	msg := hotTestMsg(id, time.Now().UTC(), "solo")
	if err := store.InsertHotMessage(ctx, msg, nil); err != nil {
		t.Fatal(err)
	}
	chain, err := store.GetLinkedMessages(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(chain) != 1 || chain[0].ID != id || chain[0].LinkerRef != "" {
		t.Fatalf("got %+v", chain)
	}
}

func TestGetLinkedMessages_ThroughCooldown(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	sess := "cool"
	base := time.Date(2026, 4, 3, 8, 0, 0, 0, time.UTC)
	idOld := NewID()
	idNew := NewID()
	_, err := store.RawDB().ExecContext(ctx, `
		INSERT INTO cooldown_messages (id, speaker, content, timestamp, platform, platform_session_id,
			linker_ref, has_embedding, cluster_id, transcript_file, transcript_line, processed_at, moved_from_hot, created_at)
		VALUES (?, 'assistant', 'old', ?, 'p', ?, '', 0, NULL, NULL, NULL, NULL, 'test', ?)`,
		idOld, base.Format(time.RFC3339), sess, base.Format(time.RFC3339))
	if err != nil {
		t.Fatal(err)
	}
	mNew := hotTestMsg(idNew, base.Add(time.Minute), "new")
	mNew.Speaker = "user"
	mNew.PlatformSessionID = sess
	mNew.LinkerRef = idOld
	if err := store.InsertHotMessage(ctx, mNew, nil); err != nil {
		t.Fatal(err)
	}
	chain, err := store.GetLinkedMessages(ctx, idNew)
	if err != nil {
		t.Fatal(err)
	}
	if len(chain) != 2 || chain[0].ID != idOld || chain[1].ID != idNew {
		t.Fatalf("got %+v", idsOf(chain))
	}
}

func TestGetLinkedMessages_BrokenRef(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	id := NewID()
	msg := hotTestMsg(id, time.Now().UTC(), "x")
	msg.LinkerRef = "missing-ulid-xxxxxxxx"
	if err := store.InsertHotMessage(ctx, msg, nil); err != nil {
		t.Fatal(err)
	}
	_, err := store.GetLinkedMessages(ctx, id)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestGetLinkedMessages_NotFound(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	_, err := store.GetLinkedMessages(ctx, "01ZZZZZZZZZZZZZZZZZZZZZZZ")
	if err == nil {
		t.Fatal("expected ErrNotFound")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}

func idsOf(msgs []model.HotMessage) []string {
	out := make([]string, len(msgs))
	for i := range msgs {
		out[i] = msgs[i].ID
	}
	return out
}
