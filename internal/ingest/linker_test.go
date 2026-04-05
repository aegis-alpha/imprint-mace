package ingest

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/aegis-alpha/imprint-mace/internal/db"
	"github.com/aegis-alpha/imprint-mace/internal/model"
)

func openLinkerTestDB(t *testing.T) *db.SQLiteStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "linker.db")
	store, err := db.Open(path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func hotMsg(id, speaker, sess string, ts time.Time, content string) *model.HotMessage {
	return &model.HotMessage{
		ID:                id,
		Speaker:           speaker,
		Content:           content,
		Timestamp:         ts,
		Platform:          "p",
		PlatformSessionID: sess,
		HasEmbedding:      false,
		CreatedAt:         ts,
	}
}

func TestResolveHotLinkerRef_Alternating(t *testing.T) {
	store := openLinkerTestDB(t)
	ctx := context.Background()
	sess := "rt-alt"
	base := time.Date(2026, 4, 4, 9, 0, 0, 0, time.UTC)
	u1 := db.NewID()
	a1 := db.NewID()
	u2 := db.NewID()
	_ = store.InsertHotMessage(ctx, hotMsg(u1, "user", sess, base, "u1"), nil)
	_ = store.InsertHotMessage(ctx, hotMsg(a1, "assistant", sess, base.Add(time.Second), "a1"), nil)

	ref, err := ResolveHotLinkerRef(ctx, store, sess, "user")
	if err != nil {
		t.Fatal(err)
	}
	if ref != a1 {
		t.Fatalf("user should link to last assistant, got %q want %q", ref, a1)
	}
	ref2, err := ResolveHotLinkerRef(ctx, store, sess, "assistant")
	if err != nil {
		t.Fatal(err)
	}
	if ref2 != u1 {
		t.Fatalf("assistant should link to first user, got %q want %q", ref2, u1)
	}

	_ = store.InsertHotMessage(ctx, hotMsg(u2, "user", sess, base.Add(2*time.Second), "u2"), nil)
	ref3, err := ResolveHotLinkerRef(ctx, store, sess, "assistant")
	if err != nil {
		t.Fatal(err)
	}
	if ref3 != u2 {
		t.Fatalf("after second user, assistant should link u2, got %q", ref3)
	}
}

func TestApplyHotLinkerRef_SameSpeakerBurst(t *testing.T) {
	store := openLinkerTestDB(t)
	ctx := context.Background()
	sess := "burst"
	base := time.Date(2026, 4, 4, 10, 0, 0, 0, time.UTC)
	a1 := db.NewID()
	u1 := db.NewID()
	u2 := db.NewID()
	u3 := db.NewID()
	_ = store.InsertHotMessage(ctx, hotMsg(a1, "assistant", sess, base, "a"), nil)
	_ = store.InsertHotMessage(ctx, hotMsg(u1, "user", sess, base.Add(time.Second), "u1"), nil)

	msg2 := hotMsg(u2, "user", sess, base.Add(2*time.Second), "u2")
	if err := ApplyHotLinkerRef(ctx, store, msg2); err != nil {
		t.Fatal(err)
	}
	if msg2.LinkerRef != a1 {
		t.Fatalf("second user message should link to last assistant %q, got %q", a1, msg2.LinkerRef)
	}
	if err := store.InsertHotMessage(ctx, msg2, nil); err != nil {
		t.Fatal(err)
	}

	msg3 := hotMsg(u3, "user", sess, base.Add(3*time.Second), "u3")
	if err := ApplyHotLinkerRef(ctx, store, msg3); err != nil {
		t.Fatal(err)
	}
	if msg3.LinkerRef != a1 {
		t.Fatalf("third user should still link assistant a1, got %q", msg3.LinkerRef)
	}
}

func TestApplyHotLinkerRef_FirstMessageEmptyRef(t *testing.T) {
	store := openLinkerTestDB(t)
	ctx := context.Background()
	msg := hotMsg(db.NewID(), "user", "s", time.Now().UTC(), "first")
	if err := ApplyHotLinkerRef(ctx, store, msg); err != nil {
		t.Fatal(err)
	}
	if msg.LinkerRef != "" {
		t.Fatalf("expected empty linker_ref, got %q", msg.LinkerRef)
	}
}

func TestApplyHotLinkerRef_PreservesExplicitRef(t *testing.T) {
	store := openLinkerTestDB(t)
	ctx := context.Background()
	sess := "explicit"
	_ = store.InsertHotMessage(ctx, hotMsg(db.NewID(), "assistant", sess, time.Now().UTC(), "a"), nil)
	msg := hotMsg(db.NewID(), "user", sess, time.Now().UTC(), "u")
	msg.LinkerRef = "custom-ref-ulid"
	if err := ApplyHotLinkerRef(ctx, store, msg); err != nil {
		t.Fatal(err)
	}
	if msg.LinkerRef != "custom-ref-ulid" {
		t.Fatalf("explicit ref overwritten: %q", msg.LinkerRef)
	}
}

func TestApplyHotLinkerRef_ChainInsertOrder(t *testing.T) {
	store := openLinkerTestDB(t)
	ctx := context.Background()
	sess := "chain"
	base := time.Date(2026, 4, 5, 11, 0, 0, 0, time.UTC)
	idC := db.NewID()
	idB := db.NewID()
	idA := db.NewID()

	mC := hotMsg(idC, "assistant", sess, base, "c")
	if err := ApplyHotLinkerRef(ctx, store, mC); err != nil {
		t.Fatal(err)
	}
	_ = store.InsertHotMessage(ctx, mC, nil)

	mB := hotMsg(idB, "user", sess, base.Add(time.Second), "b")
	if err := ApplyHotLinkerRef(ctx, store, mB); err != nil {
		t.Fatal(err)
	}
	if mB.LinkerRef != idC {
		t.Fatalf("B linker %q want %q", mB.LinkerRef, idC)
	}
	_ = store.InsertHotMessage(ctx, mB, nil)

	mA := hotMsg(idA, "assistant", sess, base.Add(2*time.Second), "a")
	if err := ApplyHotLinkerRef(ctx, store, mA); err != nil {
		t.Fatal(err)
	}
	if mA.LinkerRef != idB {
		t.Fatalf("A linker %q want %q", mA.LinkerRef, idB)
	}
	_ = store.InsertHotMessage(ctx, mA, nil)

	chain, err := store.GetLinkedMessages(ctx, idA)
	if err != nil {
		t.Fatal(err)
	}
	if len(chain) != 3 {
		t.Fatalf("chain len %d", len(chain))
	}
}

func TestOtherHotSpeaker(t *testing.T) {
	if OtherHotSpeaker("user") != "assistant" || OtherHotSpeaker("assistant") != "user" {
		t.Fatal("mapping")
	}
	if OtherHotSpeaker("  USER  ") != "assistant" {
		t.Fatal("trim/lower")
	}
	if OtherHotSpeaker("bot") != "" {
		t.Fatal("unknown should be empty")
	}
}
