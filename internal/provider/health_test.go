package provider

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/aegis-alpha/imprint-mace/internal/db"
)

type mockModelLister struct {
	name   string
	models []ModelInfo
	err    error
}

func (m *mockModelLister) ListModels(_ context.Context) ([]ModelInfo, error) {
	return m.models, m.err
}

func (m *mockModelLister) ProviderName() string {
	return m.name
}

func openTestStore(t *testing.T) db.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	store, err := db.Open(path)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestHealthChecker_AllModelsAvailable(t *testing.T) {
	store := openTestStore(t)
	lister := &mockModelLister{
		name: "openai",
		models: []ModelInfo{
			{ID: "gpt-5-nano", ContextWindow: 128000},
			{ID: "gpt-5-mini", ContextWindow: 64000},
		},
	}
	configs := map[string]map[string]string{
		"openai": {"extraction": "gpt-5-nano"},
	}
	hc := NewHealthChecker(store, []ModelLister{lister}, configs, slog.Default())

	if err := hc.CheckAll(context.Background()); err != nil {
		t.Fatalf("CheckAll: %v", err)
	}

	h, err := store.GetProviderHealth(context.Background(), "openai", "extraction")
	if err != nil {
		t.Fatalf("GetProviderHealth: %v", err)
	}
	if h.Status != "ok" {
		t.Errorf("expected status ok, got %s", h.Status)
	}
	if h.ActiveModel != "gpt-5-nano" {
		t.Errorf("expected active_model gpt-5-nano, got %s", h.ActiveModel)
	}
}

func TestHealthChecker_ConfiguredModelMissing(t *testing.T) {
	store := openTestStore(t)
	lister := &mockModelLister{
		name: "openai",
		models: []ModelInfo{
			{ID: "gpt-5-mini", ContextWindow: 64000},
		},
	}
	configs := map[string]map[string]string{
		"openai": {"extraction": "gpt-5-nano"},
	}
	hc := NewHealthChecker(store, []ModelLister{lister}, configs, slog.Default())

	if err := hc.CheckAll(context.Background()); err != nil {
		t.Fatalf("CheckAll: %v", err)
	}

	h, err := store.GetProviderHealth(context.Background(), "openai", "extraction")
	if err != nil {
		t.Fatalf("GetProviderHealth: %v", err)
	}
	if h.Status != "degraded" {
		t.Errorf("expected status degraded, got %s", h.Status)
	}
	if h.ActiveModel != "gpt-5-mini" {
		t.Errorf("expected prefix-matched active_model gpt-5-mini, got %s", h.ActiveModel)
	}
	if h.LastError == "" {
		t.Error("expected last_error to be set")
	}
}

func TestHealthChecker_ProviderUnreachable(t *testing.T) {
	store := openTestStore(t)
	lister := &mockModelLister{
		name: "openai",
		err:  fmt.Errorf("connection refused"),
	}
	configs := map[string]map[string]string{
		"openai": {"extraction": "gpt-5-nano"},
	}
	hc := NewHealthChecker(store, []ModelLister{lister}, configs, slog.Default())

	if err := hc.CheckAll(context.Background()); err != nil {
		t.Fatalf("CheckAll: %v", err)
	}

	h, err := store.GetProviderHealth(context.Background(), "openai", "extraction")
	if err != nil {
		t.Fatalf("GetProviderHealth: %v", err)
	}
	if h.Status != "unavailable" {
		t.Errorf("expected status unavailable, got %s", h.Status)
	}
	if h.LastError == "" {
		t.Error("expected last_error to be set")
	}
}

func TestHealthChecker_PrefixMatchSubstitution(t *testing.T) {
	store := openTestStore(t)
	lister := &mockModelLister{
		name: "openai",
		models: []ModelInfo{
			{ID: "gpt-5-mini", ContextWindow: 64000},
			{ID: "gpt-4o", ContextWindow: 128000},
		},
	}
	configs := map[string]map[string]string{
		"openai": {"extraction": "gpt-5-nano"},
	}
	hc := NewHealthChecker(store, []ModelLister{lister}, configs, slog.Default())

	if err := hc.CheckAll(context.Background()); err != nil {
		t.Fatalf("CheckAll: %v", err)
	}

	h, err := store.GetProviderHealth(context.Background(), "openai", "extraction")
	if err != nil {
		t.Fatalf("GetProviderHealth: %v", err)
	}
	if h.Status != "degraded" {
		t.Errorf("expected status degraded, got %s", h.Status)
	}
	if h.ActiveModel != "gpt-5-mini" {
		t.Errorf("expected prefix-matched active_model gpt-5-mini, got %s", h.ActiveModel)
	}
}

func TestHealthChecker_PrefixMatchFallsBackToAny(t *testing.T) {
	store := openTestStore(t)
	lister := &mockModelLister{
		name: "openai",
		models: []ModelInfo{
			{ID: "gpt-4o", ContextWindow: 128000},
		},
	}
	configs := map[string]map[string]string{
		"openai": {"extraction": "gpt-5-nano"},
	}
	hc := NewHealthChecker(store, []ModelLister{lister}, configs, slog.Default())

	if err := hc.CheckAll(context.Background()); err != nil {
		t.Fatalf("CheckAll: %v", err)
	}

	h, err := store.GetProviderHealth(context.Background(), "openai", "extraction")
	if err != nil {
		t.Fatalf("GetProviderHealth: %v", err)
	}
	if h.Status != "degraded" {
		t.Errorf("expected status degraded, got %s", h.Status)
	}
	if h.ActiveModel != "gpt-4o" {
		t.Errorf("expected fallback active_model gpt-4o, got %s", h.ActiveModel)
	}
}

func TestHealthChecker_MultipleProviders(t *testing.T) {
	store := openTestStore(t)
	openaiLister := &mockModelLister{
		name: "openai",
		models: []ModelInfo{
			{ID: "gpt-5-nano"},
		},
	}
	anthropicLister := &mockModelLister{
		name: "anthropic",
		models: []ModelInfo{
			{ID: "claude-sonnet"},
		},
	}
	configs := map[string]map[string]string{
		"openai":    {"extraction": "gpt-5-nano"},
		"anthropic": {"extraction": "claude-haiku"},
	}
	hc := NewHealthChecker(store, []ModelLister{openaiLister, anthropicLister}, configs, slog.Default())

	if err := hc.CheckAll(context.Background()); err != nil {
		t.Fatalf("CheckAll: %v", err)
	}

	openaiH, err := store.GetProviderHealth(context.Background(), "openai", "extraction")
	if err != nil {
		t.Fatalf("GetProviderHealth openai: %v", err)
	}
	if openaiH.Status != "ok" {
		t.Errorf("openai: expected status ok, got %s", openaiH.Status)
	}

	anthropicH, err := store.GetProviderHealth(context.Background(), "anthropic", "extraction")
	if err != nil {
		t.Fatalf("GetProviderHealth anthropic: %v", err)
	}
	if anthropicH.Status != "degraded" {
		t.Errorf("anthropic: expected status degraded, got %s", anthropicH.Status)
	}
}
