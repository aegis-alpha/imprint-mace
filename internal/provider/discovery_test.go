package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aegis-alpha/imprint-mace/internal/model"
)

func TestOpenAIModelLister_ParsesResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/models" {
			t.Errorf("expected /models, got %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("expected Bearer test-key, got %s", r.Header.Get("Authorization"))
		}
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"id": "gpt-5-nano", "context_window": 128000},
				{"id": "gpt-5-mini", "context_window": 64000},
			},
		})
	}))
	defer srv.Close()

	lister := NewOpenAIModelLister(srv.URL, "test-key", nil)
	models, err := lister.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}
	if models[0].ID != "gpt-5-nano" {
		t.Errorf("expected gpt-5-nano, got %s", models[0].ID)
	}
	if models[0].ContextWindow != 128000 {
		t.Errorf("expected context_window 128000, got %d", models[0].ContextWindow)
	}
	if lister.ProviderName() != "openai" {
		t.Errorf("expected provider name openai, got %s", lister.ProviderName())
	}
}

func TestOpenAIModelLister_ParsesGoogleFormat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"models": []map[string]any{
				{"name": "models/gemini-2.5-flash-lite", "inputTokenLimit": 1000000},
				{"name": "models/gemini-2.5-pro", "inputTokenLimit": 2000000},
			},
		})
	}))
	defer srv.Close()

	lister := NewOpenAIModelLister(srv.URL, "test-key", nil)
	models, err := lister.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}
	if models[0].ID != "gemini-2.5-flash-lite" {
		t.Errorf("expected gemini-2.5-flash-lite (prefix stripped), got %s", models[0].ID)
	}
	if models[0].ContextWindow != 1000000 {
		t.Errorf("expected context_window 1000000, got %d", models[0].ContextWindow)
	}
}

func TestOpenAIModelLister_HandlesError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error": "invalid api key"}`))
	}))
	defer srv.Close()

	lister := NewOpenAIModelLister(srv.URL, "bad-key", nil)
	_, err := lister.ListModels(context.Background())
	if err == nil {
		t.Fatal("expected error for 401 response")
	}
}

func TestAnthropicModelLister_ParsesResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("expected /v1/models, got %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"id": "claude-haiku-4-5-20251001"},
				{"id": "claude-sonnet-4-20250514"},
			},
		})
	}))
	defer srv.Close()

	lister := NewAnthropicModelLister(srv.URL, "test-key", false, nil)
	models, err := lister.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}
	if models[0].ID != "claude-haiku-4-5-20251001" {
		t.Errorf("expected claude-haiku-4-5-20251001, got %s", models[0].ID)
	}
}

func TestAnthropicModelLister_SendsCorrectHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "anthropic-key" {
			t.Errorf("expected x-api-key anthropic-key, got %s", r.Header.Get("x-api-key"))
		}
		if r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Errorf("expected anthropic-version 2023-06-01, got %s", r.Header.Get("anthropic-version"))
		}
		json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{}})
	}))
	defer srv.Close()

	lister := NewAnthropicModelLister(srv.URL, "anthropic-key", false, nil)
	_, err := lister.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
}

func TestAnthropicModelLister_BearerToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer my-token" {
			t.Errorf("expected Bearer my-token, got %s", r.Header.Get("Authorization"))
		}
		if r.Header.Get("x-api-key") != "" {
			t.Error("should not have x-api-key when using Bearer token")
		}
		json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{}})
	}))
	defer srv.Close()

	lister := NewAnthropicModelLister(srv.URL, "my-token", true, nil)
	_, err := lister.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
}

func TestOllamaModelLister_ParsesResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			t.Errorf("expected /api/tags, got %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "" {
			t.Error("ollama should not send Authorization header")
		}
		json.NewEncoder(w).Encode(map[string]any{
			"models": []map[string]any{
				{"name": "qwen:7b"},
				{"name": "llama3:8b"},
			},
		})
	}))
	defer srv.Close()

	lister := NewOllamaModelLister(srv.URL, nil)
	models, err := lister.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}
	if models[0].ID != "qwen:7b" {
		t.Errorf("expected qwen:7b, got %s", models[0].ID)
	}
}

func TestOllamaModelLister_HandlesUnreachable(t *testing.T) {
	lister := NewOllamaModelLister("http://127.0.0.1:1", nil)
	_, err := lister.ListModels(context.Background())
	if err == nil {
		t.Fatal("expected error for unreachable server")
	}
}

func TestNewModelListersFromConfig_DeduplicatesProviders(t *testing.T) {
	t.Setenv("TEST_KEY", "fake-key")

	configs := map[string][]model.ProviderConfig{
		"extraction": {
			{Name: "openai", BaseURL: "http://api.openai.com", Model: "gpt-5-nano", APIKeyEnv: "TEST_KEY"},
			{Name: "anthropic", BaseURL: "http://api.anthropic.com", Model: "claude-haiku", APIKeyEnv: "TEST_KEY"},
		},
		"query": {
			{Name: "openai", BaseURL: "http://api.openai.com", Model: "gpt-5-mini", APIKeyEnv: "TEST_KEY"},
		},
	}

	listers := NewModelListersFromConfig(configs)
	if len(listers) != 2 {
		t.Errorf("expected 2 listers (openai deduped), got %d", len(listers))
		for _, l := range listers {
			t.Logf("  lister: %s", l.ProviderName())
		}
	}
}
