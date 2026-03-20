package provider

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/aegis-alpha/imprint-mace/internal/db"
)

type mockProvider struct {
	name    string
	sendFn  func(ctx context.Context, req Request) (*Response, error)
}

func (m *mockProvider) Name() string { return m.name }
func (m *mockProvider) Send(ctx context.Context, req Request) (*Response, error) {
	return m.sendFn(ctx, req)
}

func newTestStore(t *testing.T) db.Store {
	t.Helper()
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestResolvedProvider_OverridesModel(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	if err := store.UpsertProviderHealth(ctx, &db.ProviderHealth{
		ProviderName:    "openai",
		TaskType:        "extraction",
		ConfiguredModel: "gpt-5-nano",
		ActiveModel:     "gpt-5-mini",
		Status:          "degraded",
		LastChecked:     now,
	}); err != nil {
		t.Fatalf("setup health: %v", err)
	}

	var capturedModel string
	inner := &mockProvider{
		name: "openai",
		sendFn: func(ctx context.Context, req Request) (*Response, error) {
			capturedModel = req.UserPrompt
			return &Response{Content: "ok", ProviderName: "openai", Model: "gpt-5-mini"}, nil
		},
	}

	rp := NewResolvedProvider(inner, store, "openai", "extraction")
	_, err := rp.Send(ctx, Request{UserPrompt: "hello"})
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	if rp.ActiveModel() != "gpt-5-mini" {
		t.Errorf("expected active model gpt-5-mini, got %s", rp.ActiveModel())
	}
	_ = capturedModel
}

func TestResolvedProvider_SkipsAuthError(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	if err := store.UpsertProviderOps(ctx, &db.ProviderOps{
		ProviderName: "openai",
		Status:       "auth_error",
		LastError:    "invalid key",
		ErrorType:    "http_401",
		MaxRetries:   5,
		CreatedAt:    now,
		UpdatedAt:    now,
	}); err != nil {
		t.Fatalf("setup ops: %v", err)
	}

	called := false
	inner := &mockProvider{
		name: "openai",
		sendFn: func(ctx context.Context, req Request) (*Response, error) {
			called = true
			return &Response{Content: "ok"}, nil
		},
	}

	rp := NewResolvedProvider(inner, store, "openai", "extraction")
	_, err := rp.Send(ctx, Request{UserPrompt: "hello"})
	if err == nil {
		t.Fatal("expected error for auth_error status")
	}
	if called {
		t.Error("inner provider should not have been called")
	}
}

func TestResolvedProvider_SkipsBeforeNextCheck(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	future := now.Add(time.Hour)

	if err := store.UpsertProviderOps(ctx, &db.ProviderOps{
		ProviderName: "openai",
		Status:       "transient_error",
		RetryCount:   2,
		MaxRetries:   5,
		NextCheckAt:  &future,
		CreatedAt:    now,
		UpdatedAt:    now,
	}); err != nil {
		t.Fatalf("setup ops: %v", err)
	}

	called := false
	inner := &mockProvider{
		name: "openai",
		sendFn: func(ctx context.Context, req Request) (*Response, error) {
			called = true
			return &Response{Content: "ok"}, nil
		},
	}

	rp := NewResolvedProvider(inner, store, "openai", "extraction")
	_, err := rp.Send(ctx, Request{UserPrompt: "hello"})
	if err == nil {
		t.Fatal("expected error when next_check_at is in the future")
	}
	if called {
		t.Error("inner provider should not have been called before next_check_at")
	}
}

func TestResolvedProvider_RetriesAfterNextCheck(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	past := now.Add(-time.Hour)

	if err := store.UpsertProviderOps(ctx, &db.ProviderOps{
		ProviderName: "openai",
		Status:       "transient_error",
		RetryCount:   2,
		MaxRetries:   5,
		NextCheckAt:  &past,
		CreatedAt:    now,
		UpdatedAt:    now,
	}); err != nil {
		t.Fatalf("setup ops: %v", err)
	}

	inner := &mockProvider{
		name: "openai",
		sendFn: func(ctx context.Context, req Request) (*Response, error) {
			return &Response{Content: "recovered", ProviderName: "openai", Model: "gpt-5-nano"}, nil
		},
	}

	rp := NewResolvedProvider(inner, store, "openai", "extraction")
	resp, err := rp.Send(ctx, Request{UserPrompt: "hello"})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if resp.Content != "recovered" {
		t.Errorf("expected 'recovered', got %q", resp.Content)
	}

	ops, err := store.GetProviderOps(ctx, "openai")
	if err != nil {
		t.Fatalf("get ops: %v", err)
	}
	if ops.Status != "ok" {
		t.Errorf("expected status reset to ok, got %s", ops.Status)
	}
	if ops.RetryCount != 0 {
		t.Errorf("expected retry_count reset to 0, got %d", ops.RetryCount)
	}
}

func TestResolvedProvider_ResetsOnSuccess(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	if err := store.UpsertProviderOps(ctx, &db.ProviderOps{
		ProviderName: "openai",
		Status:       "ok",
		MaxRetries:   5,
		CreatedAt:    now,
		UpdatedAt:    now,
	}); err != nil {
		t.Fatalf("setup ops: %v", err)
	}

	inner := &mockProvider{
		name: "openai",
		sendFn: func(ctx context.Context, req Request) (*Response, error) {
			return &Response{Content: "ok", ProviderName: "openai"}, nil
		},
	}

	rp := NewResolvedProvider(inner, store, "openai", "extraction")
	_, err := rp.Send(ctx, Request{UserPrompt: "hello"})
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	ops, err := store.GetProviderOps(ctx, "openai")
	if err != nil {
		t.Fatalf("get ops: %v", err)
	}
	if ops.Status != "ok" {
		t.Errorf("expected status ok, got %s", ops.Status)
	}
	if ops.LastSuccess == nil {
		t.Error("expected last_success to be set")
	}
}

func TestResolvedProvider_ClassifiesAndRecordsError(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	inner := &mockProvider{
		name: "openai",
		sendFn: func(ctx context.Context, req Request) (*Response, error) {
			return nil, fmt.Errorf("status 503: service unavailable")
		},
	}

	rp := NewResolvedProvider(inner, store, "openai", "extraction")
	_, err := rp.Send(ctx, Request{UserPrompt: "hello"})
	if err == nil {
		t.Fatal("expected error")
	}

	ops, err := store.GetProviderOps(ctx, "openai")
	if err != nil {
		t.Fatalf("get ops: %v", err)
	}
	if ops.Status != "transient_error" {
		t.Errorf("expected status transient_error, got %s", ops.Status)
	}
	if ops.RetryCount != 1 {
		t.Errorf("expected retry_count 1, got %d", ops.RetryCount)
	}
	if ops.NextCheckAt == nil {
		t.Error("expected next_check_at to be set")
	}
}

func TestResolvedProvider_AuthErrorNeverRetries(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	inner := &mockProvider{
		name: "openai",
		sendFn: func(ctx context.Context, req Request) (*Response, error) {
			return nil, fmt.Errorf("status 401: unauthorized")
		},
	}

	rp := NewResolvedProvider(inner, store, "openai", "extraction")
	_, err := rp.Send(ctx, Request{UserPrompt: "hello"})
	if err == nil {
		t.Fatal("expected error")
	}

	ops, err := store.GetProviderOps(ctx, "openai")
	if err != nil {
		t.Fatalf("get ops: %v", err)
	}
	if ops.Status != "auth_error" {
		t.Errorf("expected status auth_error, got %s", ops.Status)
	}
}

func TestChainWithResolvedProviders_FallbackOnTransientError(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	primary := &mockProvider{
		name: "primary",
		sendFn: func(ctx context.Context, req Request) (*Response, error) {
			return nil, fmt.Errorf("status 503: service unavailable")
		},
	}
	fallback := &mockProvider{
		name: "fallback",
		sendFn: func(ctx context.Context, req Request) (*Response, error) {
			return &Response{Content: "from fallback", ProviderName: "fallback", Model: "model-b"}, nil
		},
	}

	rp1 := NewResolvedProvider(primary, store, "primary", "extraction")
	rp2 := NewResolvedProvider(fallback, store, "fallback", "extraction")

	chain := &Chain{providers: []Provider{rp1, rp2}}
	resp, err := chain.Send(ctx, Request{UserPrompt: "hello"})
	if err != nil {
		t.Fatalf("chain send: %v", err)
	}
	if resp.Content != "from fallback" {
		t.Errorf("expected 'from fallback', got %q", resp.Content)
	}
	if resp.ProviderName != "fallback" {
		t.Errorf("expected provider 'fallback', got %q", resp.ProviderName)
	}

	ops, err := store.GetProviderOps(ctx, "primary")
	if err != nil {
		t.Fatalf("get primary ops: %v", err)
	}
	if ops.Status != "transient_error" {
		t.Errorf("expected primary status transient_error, got %s", ops.Status)
	}
}
