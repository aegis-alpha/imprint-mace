package main

import (
	"testing"

	"github.com/aegis-alpha/imprint-mace/internal/config"
	"github.com/aegis-alpha/imprint-mace/internal/model"
)

func TestProviderConfigsForTask_PrismMode(t *testing.T) {
	cfg := &config.Config{
		LLM: config.LLMConfig{BaseURL: "https://prism.example.com/v1"},
	}
	got := providerConfigsForTask(cfg, "query")
	if len(got) != 1 {
		t.Fatalf("expected 1 provider, got %d", len(got))
	}
	if got[0].Name != "prism" {
		t.Fatalf("expected provider name prism, got %q", got[0].Name)
	}
	if got[0].Model != "auto" {
		t.Fatalf("expected model auto, got %q", got[0].Model)
	}
	if got[0].BaseURL != "https://prism.example.com/v1" {
		t.Fatalf("unexpected base_url: %q", got[0].BaseURL)
	}
	if got[0].Headers["X-Prism-Task"] != "query" {
		t.Fatalf("expected task header query, got %q", got[0].Headers["X-Prism-Task"])
	}
	if got[0].Headers["X-Prism-App"] != "imprint" {
		t.Fatalf("expected app header imprint, got %q", got[0].Headers["X-Prism-App"])
	}
}

func TestProviderConfigsForTask_DirectModeQueryFallback(t *testing.T) {
	extraction := model.ProviderConfig{Name: "openai", BaseURL: "https://x", Model: "m"}
	cfg := &config.Config{
		Providers: config.ProviderChains{
			Extraction: []model.ProviderConfig{extraction},
		},
	}
	got := providerConfigsForTask(cfg, "query")
	if len(got) != 1 || got[0].Name != extraction.Name {
		t.Fatalf("expected query fallback to extraction providers")
	}
}
