package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// --- OpenAI Embedder ---

func TestOpenAIEmbedder_ParsesResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("expected Bearer test-key, got %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"embedding":[0.1,0.2,0.3]}],"model":"text-embedding-3-small","usage":{"total_tokens":5}}`)
	}))
	defer srv.Close()

	emb := NewOpenAIEmbedder(srv.URL, "text-embedding-3-small", "test-key", nil)
	vec, err := emb.Embed(context.Background(), "hello world")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vec) != 3 {
		t.Fatalf("expected 3 dims, got %d", len(vec))
	}
	if vec[0] < 0.09 || vec[0] > 0.11 {
		t.Errorf("expected ~0.1, got %f", vec[0])
	}
	if emb.ModelName() != "text-embedding-3-small" {
		t.Errorf("expected model name, got %q", emb.ModelName())
	}
}

func TestOpenAIEmbedder_HandlesError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"message":"rate limited"}}`)
	}))
	defer srv.Close()

	emb := NewOpenAIEmbedder(srv.URL, "text-embedding-3-small", "test-key", nil)
	_, err := emb.Embed(context.Background(), "hello")
	if err == nil {
		t.Fatal("expected error on 429")
	}
}

// --- Ollama Embedder ---

func TestOllamaEmbedder_ParsesResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			t.Errorf("expected /api/embed, got %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"model":"nomic-embed-text","embeddings":[[0.4,0.5,0.6,0.7]]}`)
	}))
	defer srv.Close()

	emb := NewOllamaEmbedder(srv.URL, "nomic-embed-text")
	vec, err := emb.Embed(context.Background(), "hello world")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vec) != 4 {
		t.Fatalf("expected 4 dims, got %d", len(vec))
	}
	if vec[0] < 0.39 || vec[0] > 0.41 {
		t.Errorf("expected ~0.4, got %f", vec[0])
	}
	if emb.ModelName() != "nomic-embed-text" {
		t.Errorf("expected model name, got %q", emb.ModelName())
	}
}

func TestOllamaEmbedder_HandlesError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"error":"model not found"}`)
	}))
	defer srv.Close()

	emb := NewOllamaEmbedder(srv.URL, "nomic-embed-text")
	_, err := emb.Embed(context.Background(), "hello")
	if err == nil {
		t.Fatal("expected error on 500")
	}
}

// --- EmbedderChain ---

func TestEmbedderChain_Fallback(t *testing.T) {
	failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer failSrv.Close()

	okSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"embedding":[0.1,0.2]}]}`)
	}))
	defer okSrv.Close()

	chain := &EmbedderChain{
		embedders: []Embedder{
			NewOpenAIEmbedder(failSrv.URL, "fail-model", "key", nil),
			NewOpenAIEmbedder(okSrv.URL, "ok-model", "key", nil),
		},
	}

	vec, err := chain.Embed(context.Background(), "test")
	if err != nil {
		t.Fatalf("chain.Embed: %v", err)
	}
	if len(vec) != 2 {
		t.Errorf("expected 2 dims, got %d", len(vec))
	}
}

func TestEmbedderChain_ModelName(t *testing.T) {
	chain := &EmbedderChain{
		embedders: []Embedder{
			NewOpenAIEmbedder("http://x", "primary-model", "key", nil),
			NewOllamaEmbedder("http://y", "fallback-model"),
		},
	}
	if chain.ModelName() != "primary-model" {
		t.Errorf("expected primary-model, got %q", chain.ModelName())
	}
}

func TestOpenAIEmbedder_CustomHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Prism-Task"); got != "embedding" {
			t.Fatalf("expected X-Prism-Task embedding, got %q", got)
		}
		if got := r.Header.Get("X-Prism-App"); got != "imprint" {
			t.Fatalf("expected X-Prism-App imprint, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"embedding":[0.1,0.2]}]}`)
	}))
	defer srv.Close()

	emb := NewOpenAIEmbedder(srv.URL, "auto", "", map[string]string{
		"X-Prism-Task": "embedding",
		"X-Prism-App":  "imprint",
	})
	if _, err := emb.Embed(context.Background(), "hello"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNewEmbedderChain_EmptyReturnsNil(t *testing.T) {
	chain, err := NewEmbedderChain(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if chain != nil {
		t.Error("expected nil chain for empty config")
	}
}
