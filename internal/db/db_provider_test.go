package db

import (
	"context"
	"testing"
	"time"
)

func TestUpsertProviderModel_InsertAndUpdate(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()

	m := &ProviderModel{
		ProviderName:  "openai",
		ModelID:       "gpt-5-nano",
		ContextWindow: 128000,
		Available:     true,
		LastChecked:   time.Now().UTC().Truncate(time.Second),
	}
	if err := store.UpsertProviderModel(ctx, m); err != nil {
		t.Fatalf("insert: %v", err)
	}

	models, err := store.ListProviderModels(ctx, "openai")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(models))
	}
	if models[0].ModelID != "gpt-5-nano" {
		t.Errorf("expected gpt-5-nano, got %s", models[0].ModelID)
	}
	if models[0].ContextWindow != 128000 {
		t.Errorf("expected context_window 128000, got %d", models[0].ContextWindow)
	}

	m.Available = false
	if err := store.UpsertProviderModel(ctx, m); err != nil {
		t.Fatalf("update: %v", err)
	}

	models, err = store.ListProviderModels(ctx, "openai")
	if err != nil {
		t.Fatalf("list after update: %v", err)
	}
	if len(models) != 0 {
		t.Errorf("expected 0 available models after marking unavailable, got %d", len(models))
	}
}

func TestListProviderModels_FiltersByProvider(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	for _, m := range []ProviderModel{
		{ProviderName: "openai", ModelID: "gpt-5-nano", Available: true, LastChecked: now},
		{ProviderName: "openai", ModelID: "gpt-5-mini", Available: true, LastChecked: now},
		{ProviderName: "anthropic", ModelID: "claude-haiku", Available: true, LastChecked: now},
	} {
		m := m
		if err := store.UpsertProviderModel(ctx, &m); err != nil {
			t.Fatalf("insert %s/%s: %v", m.ProviderName, m.ModelID, err)
		}
	}

	openaiModels, err := store.ListProviderModels(ctx, "openai")
	if err != nil {
		t.Fatalf("list openai: %v", err)
	}
	if len(openaiModels) != 2 {
		t.Errorf("expected 2 openai models, got %d", len(openaiModels))
	}

	anthropicModels, err := store.ListProviderModels(ctx, "anthropic")
	if err != nil {
		t.Fatalf("list anthropic: %v", err)
	}
	if len(anthropicModels) != 1 {
		t.Errorf("expected 1 anthropic model, got %d", len(anthropicModels))
	}
	if anthropicModels[0].ModelID != "claude-haiku" {
		t.Errorf("expected claude-haiku, got %s", anthropicModels[0].ModelID)
	}
}

func TestUpsertProviderHealth_InsertAndUpdate(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	h := &ProviderHealth{
		ProviderName:    "openai",
		TaskType:        "extraction",
		ConfiguredModel: "gpt-5-nano",
		ActiveModel:     "gpt-5-nano",
		Status:          "ok",
		LastChecked:     now,
	}
	if err := store.UpsertProviderHealth(ctx, h); err != nil {
		t.Fatalf("insert: %v", err)
	}

	got, err := store.GetProviderHealth(ctx, "openai", "extraction")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != "ok" {
		t.Errorf("expected status ok, got %s", got.Status)
	}
	if got.ActiveModel != "gpt-5-nano" {
		t.Errorf("expected active_model gpt-5-nano, got %s", got.ActiveModel)
	}

	switchedAt := now.Add(time.Minute)
	h.Status = "degraded"
	h.ActiveModel = "gpt-5-mini"
	h.LastError = "model not found in provider model list"
	h.SwitchedAt = &switchedAt
	if err := store.UpsertProviderHealth(ctx, h); err != nil {
		t.Fatalf("update: %v", err)
	}

	got, err = store.GetProviderHealth(ctx, "openai", "extraction")
	if err != nil {
		t.Fatalf("get after update: %v", err)
	}
	if got.Status != "degraded" {
		t.Errorf("expected status degraded, got %s", got.Status)
	}
	if got.ActiveModel != "gpt-5-mini" {
		t.Errorf("expected active_model gpt-5-mini, got %s", got.ActiveModel)
	}
	if got.SwitchedAt == nil {
		t.Error("expected switched_at to be set")
	}
}

func TestListProviderHealth_ReturnsAll(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	entries := []ProviderHealth{
		{ProviderName: "openai", TaskType: "extraction", ConfiguredModel: "gpt-5-nano", ActiveModel: "gpt-5-nano", Status: "ok", LastChecked: now},
		{ProviderName: "openai", TaskType: "query", ConfiguredModel: "gpt-5-nano", ActiveModel: "gpt-5-nano", Status: "ok", LastChecked: now},
		{ProviderName: "anthropic", TaskType: "extraction", ConfiguredModel: "claude-haiku", ActiveModel: "", Status: "degraded", LastChecked: now},
	}
	for i := range entries {
		if err := store.UpsertProviderHealth(ctx, &entries[i]); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	all, err := store.ListProviderHealth(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("expected 3 entries, got %d", len(all))
	}
}

func TestGetProviderHealth_NotFound(t *testing.T) {
	store := openTestDB(t)
	ctx := context.Background()

	_, err := store.GetProviderHealth(ctx, "nonexistent", "extraction")
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}
