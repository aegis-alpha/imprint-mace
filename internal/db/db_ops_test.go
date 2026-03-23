package db

import (
	"context"
	"testing"
	"time"
)

func TestUpsertProviderOps_InsertAndUpdate(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	ops := &ProviderOps{
		ProviderName: "openai",
		Status:       "ok",
		RetryCount:   0,
		MaxRetries:   5,
		LastSuccess:  &now,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := store.UpsertProviderOps(ctx, ops); err != nil {
		t.Fatalf("insert: %v", err)
	}

	got, err := store.GetProviderOps(ctx, "openai")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != "ok" {
		t.Errorf("expected status ok, got %s", got.Status)
	}
	if got.RetryCount != 0 {
		t.Errorf("expected retry_count 0, got %d", got.RetryCount)
	}

	nextCheck := now.Add(time.Hour)
	ops.Status = "transient_error"
	ops.RetryCount = 1
	ops.LastError = "connection refused"
	ops.ErrorType = "connection_refused"
	ops.NextCheckAt = &nextCheck
	ops.UpdatedAt = now.Add(time.Second)
	if err := store.UpsertProviderOps(ctx, ops); err != nil {
		t.Fatalf("update: %v", err)
	}

	got, err = store.GetProviderOps(ctx, "openai")
	if err != nil {
		t.Fatalf("get after update: %v", err)
	}
	if got.Status != "transient_error" {
		t.Errorf("expected status transient_error, got %s", got.Status)
	}
	if got.RetryCount != 1 {
		t.Errorf("expected retry_count 1, got %d", got.RetryCount)
	}
	if got.LastError != "connection refused" {
		t.Errorf("expected last_error 'connection refused', got %s", got.LastError)
	}
	if got.ErrorType != "connection_refused" {
		t.Errorf("expected error_type connection_refused, got %s", got.ErrorType)
	}
	if got.NextCheckAt == nil {
		t.Error("expected next_check_at to be set")
	}
}

func TestGetProviderOps_NotFound(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()

	_, err := store.GetProviderOps(ctx, "nonexistent")
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestListProviderOps_ReturnsAll(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	for _, name := range []string{"openai", "anthropic", "ollama"} {
		if err := store.UpsertProviderOps(ctx, &ProviderOps{
			ProviderName: name,
			Status:       "ok",
			MaxRetries:   5,
			CreatedAt:    now,
			UpdatedAt:    now,
		}); err != nil {
			t.Fatalf("insert %s: %v", name, err)
		}
	}

	all, err := store.ListProviderOps(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("expected 3 entries, got %d", len(all))
	}
}

func TestEnqueueRetry_And_Dequeue(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()

	entry := &RetryEntry{
		ID:        NewID(),
		TaskType:  "extraction",
		Payload:   `{"system_prompt":"test","user_prompt":"hello"}`,
		CreatedAt: time.Now().UTC().Truncate(time.Second),
		Status:    "pending",
	}
	if err := store.EnqueueRetry(ctx, entry); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	entries, err := store.DequeueRetries(ctx, 10)
	if err != nil {
		t.Fatalf("dequeue: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].TaskType != "extraction" {
		t.Errorf("expected task_type extraction, got %s", entries[0].TaskType)
	}
	if entries[0].Status != "processing" {
		t.Errorf("expected status processing after dequeue, got %s", entries[0].Status)
	}

	entries2, err := store.DequeueRetries(ctx, 10)
	if err != nil {
		t.Fatalf("dequeue again: %v", err)
	}
	if len(entries2) != 0 {
		t.Errorf("expected 0 entries on second dequeue, got %d", len(entries2))
	}
}

func TestUpdateRetryStatus(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()

	entry := &RetryEntry{
		ID:        NewID(),
		TaskType:  "query",
		Payload:   `{}`,
		CreatedAt: time.Now().UTC().Truncate(time.Second),
		Status:    "pending",
	}
	if err := store.EnqueueRetry(ctx, entry); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	if err := store.UpdateRetryStatus(ctx, entry.ID, "completed", ""); err != nil {
		t.Fatalf("update status: %v", err)
	}

	entries, err := store.DequeueRetries(ctx, 10)
	if err != nil {
		t.Fatalf("dequeue: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 pending entries after completion, got %d", len(entries))
	}
}

func TestExpireOldRetries(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()

	old := &RetryEntry{
		ID:        NewID(),
		TaskType:  "extraction",
		Payload:   `{}`,
		CreatedAt: time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second),
		Status:    "pending",
	}
	fresh := &RetryEntry{
		ID:        NewID(),
		TaskType:  "extraction",
		Payload:   `{}`,
		CreatedAt: time.Now().UTC().Truncate(time.Second),
		Status:    "pending",
	}
	if err := store.EnqueueRetry(ctx, old); err != nil {
		t.Fatalf("enqueue old: %v", err)
	}
	if err := store.EnqueueRetry(ctx, fresh); err != nil {
		t.Fatalf("enqueue fresh: %v", err)
	}

	cutoff := time.Now().UTC().Add(-24 * time.Hour)
	expired, err := store.ExpireOldRetries(ctx, cutoff)
	if err != nil {
		t.Fatalf("expire: %v", err)
	}
	if expired != 1 {
		t.Errorf("expected 1 expired, got %d", expired)
	}

	entries, err := store.DequeueRetries(ctx, 10)
	if err != nil {
		t.Fatalf("dequeue: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("expected 1 remaining entry, got %d", len(entries))
	}
}

func TestRetryQueueDepth(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()

	depth, err := store.RetryQueueDepth(ctx)
	if err != nil {
		t.Fatalf("depth empty: %v", err)
	}
	if depth != 0 {
		t.Errorf("expected 0 depth on empty queue, got %d", depth)
	}

	for i := 0; i < 3; i++ {
		if err := store.EnqueueRetry(ctx, &RetryEntry{
			ID:        NewID(),
			TaskType:  "extraction",
			Payload:   `{}`,
			CreatedAt: time.Now().UTC().Truncate(time.Second),
			Status:    "pending",
		}); err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
	}

	depth, err = store.RetryQueueDepth(ctx)
	if err != nil {
		t.Fatalf("depth after enqueue: %v", err)
	}
	if depth != 3 {
		t.Errorf("expected depth 3, got %d", depth)
	}

	if err := store.UpdateRetryStatus(ctx, "", "completed", ""); err == nil {
		_, _ = store.DequeueRetries(ctx, 1)
	}

	depth, err = store.RetryQueueDepth(ctx)
	if err != nil {
		t.Fatalf("depth after dequeue: %v", err)
	}
	if depth != 3 {
		t.Errorf("expected depth 3 (1 processing + 2 pending), got %d", depth)
	}
}
