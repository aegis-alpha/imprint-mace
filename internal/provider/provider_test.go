package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aegis-alpha/imprint-mace/internal/model"
)

// --- OpenAI-compatible provider tests ---

func TestOpenAICompatible_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/chat/completions" {
			t.Errorf("expected /chat/completions, got %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("expected Bearer test-key, got %s", r.Header.Get("Authorization"))
		}

		body, _ := io.ReadAll(r.Body)
		var req chatRequest
		json.Unmarshal(body, &req)
		if req.Model != "gpt-test" {
			t.Errorf("expected model gpt-test, got %s", req.Model)
		}
		if len(req.Messages) != 2 {
			t.Errorf("expected 2 messages, got %d", len(req.Messages))
		}
		if req.Messages[0].Role != "system" {
			t.Errorf("expected system role, got %s", req.Messages[0].Role)
		}

		json.NewEncoder(w).Encode(chatResponse{
			Choices: []chatChoice{{Message: chatMessage{Content: "test response"}}},
			Usage:   chatUsage{TotalTokens: 42},
		})
	}))
	defer srv.Close()

	cfg := model.ProviderConfig{
		Name:    "openai",
		BaseURL: srv.URL,
		Model:   "gpt-test",
	}
	p := NewOpenAICompatible(cfg, "test-key")

	resp, err := p.Send(context.Background(), Request{
		SystemPrompt: "you are helpful",
		UserPrompt:   "hello",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "test response" {
		t.Errorf("expected 'test response', got %q", resp.Content)
	}
	if resp.TokensUsed != 42 {
		t.Errorf("expected 42 tokens, got %d", resp.TokensUsed)
	}
	if resp.ProviderName != "openai" {
		t.Errorf("expected provider name 'openai', got %q", resp.ProviderName)
	}
}

func TestOpenAICompatible_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error": "rate limited"}`))
	}))
	defer srv.Close()

	cfg := model.ProviderConfig{Name: "openai", BaseURL: srv.URL, Model: "gpt-test"}
	p := NewOpenAICompatible(cfg, "test-key")

	_, err := p.Send(context.Background(), Request{UserPrompt: "hello"})
	if err == nil {
		t.Fatal("expected error for 429 response")
	}
}

func TestOpenAICompatible_EmptyChoices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(chatResponse{Choices: []chatChoice{}})
	}))
	defer srv.Close()

	cfg := model.ProviderConfig{Name: "openai", BaseURL: srv.URL, Model: "gpt-test"}
	p := NewOpenAICompatible(cfg, "test-key")

	_, err := p.Send(context.Background(), Request{UserPrompt: "hello"})
	if err == nil {
		t.Fatal("expected error for empty choices")
	}
}

// --- Anthropic provider tests ---

func TestAnthropic_APIKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("expected /v1/messages, got %s", r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "anthropic-key" {
			t.Errorf("expected x-api-key anthropic-key, got %s", r.Header.Get("x-api-key"))
		}
		if r.Header.Get("Authorization") != "" {
			t.Error("should not have Authorization header when using API key")
		}
		if r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Errorf("expected anthropic-version header")
		}

		body, _ := io.ReadAll(r.Body)
		var req anthropicRequest
		json.Unmarshal(body, &req)
		if req.System == "" {
			t.Error("expected system prompt in top-level field")
		}
		if req.Model != "claude-test" {
			t.Errorf("expected model claude-test, got %s", req.Model)
		}

		json.NewEncoder(w).Encode(anthropicResponse{
			Content: []anthropicContentBlock{{Type: "text", Text: "claude says hi"}},
			Usage:   anthropicUsage{InputTokens: 10, OutputTokens: 5},
		})
	}))
	defer srv.Close()

	cfg := model.ProviderConfig{Name: "anthropic", BaseURL: srv.URL, Model: "claude-test"}
	p := NewAnthropic(cfg, "anthropic-key", false)

	resp, err := p.Send(context.Background(), Request{
		SystemPrompt: "be helpful",
		UserPrompt:   "hello",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "claude says hi" {
		t.Errorf("expected 'claude says hi', got %q", resp.Content)
	}
	if resp.TokensUsed != 15 {
		t.Errorf("expected 15 tokens, got %d", resp.TokensUsed)
	}
}

func TestAnthropic_BearerToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer my-oauth-token" {
			t.Errorf("expected Bearer token, got Authorization: %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("x-api-key") != "" {
			t.Error("should not have x-api-key header when using Bearer token")
		}

		json.NewEncoder(w).Encode(anthropicResponse{
			Content: []anthropicContentBlock{{Type: "text", Text: "token auth works"}},
			Usage:   anthropicUsage{InputTokens: 5, OutputTokens: 3},
		})
	}))
	defer srv.Close()

	cfg := model.ProviderConfig{Name: "anthropic", BaseURL: srv.URL, Model: "claude-test"}
	p := NewAnthropic(cfg, "my-oauth-token", true)

	resp, err := p.Send(context.Background(), Request{UserPrompt: "hello"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "token auth works" {
		t.Errorf("expected 'token auth works', got %q", resp.Content)
	}
}

func TestAnthropic_NoTextContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(anthropicResponse{
			Content: []anthropicContentBlock{},
		})
	}))
	defer srv.Close()

	cfg := model.ProviderConfig{Name: "anthropic", BaseURL: srv.URL, Model: "claude-test"}
	p := NewAnthropic(cfg, "key", false)

	_, err := p.Send(context.Background(), Request{UserPrompt: "hello"})
	if err == nil {
		t.Fatal("expected error for empty content")
	}
}

// --- Ollama provider tests ---

func TestOllama_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Errorf("expected /api/chat, got %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "" {
			t.Error("ollama should not send Authorization header")
		}

		body, _ := io.ReadAll(r.Body)
		var req ollamaRequest
		json.Unmarshal(body, &req)
		if req.Stream {
			t.Error("expected stream=false")
		}
		if req.Model != "qwen:7b" {
			t.Errorf("expected model qwen:7b, got %s", req.Model)
		}

		json.NewEncoder(w).Encode(ollamaResponse{
			Message:         ollamaMessage{Role: "assistant", Content: "ollama response"},
			PromptEvalCount: 20,
			EvalCount:       10,
		})
	}))
	defer srv.Close()

	cfg := model.ProviderConfig{Name: "ollama", BaseURL: srv.URL, Model: "qwen:7b"}
	p := NewOllama(cfg)

	resp, err := p.Send(context.Background(), Request{
		SystemPrompt: "be helpful",
		UserPrompt:   "hello",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "ollama response" {
		t.Errorf("expected 'ollama response', got %q", resp.Content)
	}
	if resp.TokensUsed != 30 {
		t.Errorf("expected 30 tokens, got %d", resp.TokensUsed)
	}
}

// --- Chain tests ---

func TestChain_FirstProviderSucceeds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(chatResponse{
			Choices: []chatChoice{{Message: chatMessage{Content: "from first"}}},
		})
	}))
	defer srv.Close()

	chain := &Chain{
		providers: []Provider{
			NewOpenAICompatible(model.ProviderConfig{Name: "first", BaseURL: srv.URL, Model: "m"}, "k"),
			NewOpenAICompatible(model.ProviderConfig{Name: "second", BaseURL: "http://should-not-be-called", Model: "m"}, "k"),
		},
	}

	resp, err := chain.Send(context.Background(), Request{UserPrompt: "hello"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "from first" {
		t.Errorf("expected 'from first', got %q", resp.Content)
	}
	if resp.ProviderName != "first" {
		t.Errorf("expected provider 'first', got %q", resp.ProviderName)
	}
}

func TestChain_FallbackToSecond(t *testing.T) {
	failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("provider down"))
	}))
	defer failSrv.Close()

	okSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(chatResponse{
			Choices: []chatChoice{{Message: chatMessage{Content: "from second"}}},
		})
	}))
	defer okSrv.Close()

	chain := &Chain{
		providers: []Provider{
			NewOpenAICompatible(model.ProviderConfig{Name: "broken", BaseURL: failSrv.URL, Model: "m"}, "k"),
			NewOpenAICompatible(model.ProviderConfig{Name: "working", BaseURL: okSrv.URL, Model: "m"}, "k"),
		},
	}

	resp, err := chain.Send(context.Background(), Request{UserPrompt: "hello"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "from second" {
		t.Errorf("expected 'from second', got %q", resp.Content)
	}
	if resp.ProviderName != "working" {
		t.Errorf("expected provider 'working', got %q", resp.ProviderName)
	}
}

func TestChain_AllFail(t *testing.T) {
	failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer failSrv.Close()

	chain := &Chain{
		providers: []Provider{
			NewOpenAICompatible(model.ProviderConfig{Name: "a", BaseURL: failSrv.URL, Model: "m"}, "k"),
			NewOpenAICompatible(model.ProviderConfig{Name: "b", BaseURL: failSrv.URL, Model: "m"}, "k"),
		},
	}

	_, err := chain.Send(context.Background(), Request{UserPrompt: "hello"})
	if err == nil {
		t.Fatal("expected error when all providers fail")
	}
}

func TestNewChain_NoConfigs(t *testing.T) {
	_, err := NewChain(nil)
	if err == nil {
		t.Fatal("expected error for empty configs")
	}
}

func TestNewChain_SelectsCorrectProviderType(t *testing.T) {
	t.Setenv("TEST_KEY", "fake-key")

	configs := []model.ProviderConfig{
		{Name: "anthropic", BaseURL: "http://localhost", Model: "claude", APIKeyEnv: "TEST_KEY", Priority: 1},
		{Name: "ollama", BaseURL: "http://localhost:11434", Model: "qwen", Priority: 2},
		{Name: "openai", BaseURL: "http://localhost", Model: "gpt", APIKeyEnv: "TEST_KEY", Priority: 3},
	}

	chain, err := NewChain(configs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chain.providers) != 3 {
		t.Fatalf("expected 3 providers, got %d", len(chain.providers))
	}

	if _, ok := chain.providers[0].(*Anthropic); !ok {
		t.Errorf("expected first provider to be *Anthropic, got %T", chain.providers[0])
	}
	if _, ok := chain.providers[1].(*Ollama); !ok {
		t.Errorf("expected second provider to be *Ollama, got %T", chain.providers[1])
	}
	if _, ok := chain.providers[2].(*OpenAICompatible); !ok {
		t.Errorf("expected third provider to be *OpenAICompatible, got %T", chain.providers[2])
	}
}

func TestNewChain_SkipsProvidersWithoutKeys(t *testing.T) {
	configs := []model.ProviderConfig{
		{Name: "openai", BaseURL: "http://localhost", Model: "gpt", APIKeyEnv: "NONEXISTENT_KEY_12345", Priority: 1},
		{Name: "ollama", BaseURL: "http://localhost:11434", Model: "qwen", Priority: 2},
	}

	chain, err := NewChain(configs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chain.providers) != 1 {
		t.Fatalf("expected 1 provider (ollama only), got %d", len(chain.providers))
	}
	if chain.providers[0].Name() != "ollama" {
		t.Errorf("expected ollama, got %s", chain.providers[0].Name())
	}
}

func TestCredentials_TokenPriority(t *testing.T) {
	t.Setenv("MY_KEY", "the-api-key")
	t.Setenv("MY_TOKEN", "the-token")

	cfg := model.ProviderConfig{APIKeyEnv: "MY_KEY", TokenEnv: "MY_TOKEN", Name: "test"}
	cred, isToken, err := credentials(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isToken {
		t.Error("expected isToken=true when token_env is set")
	}
	if cred != "the-token" {
		t.Errorf("expected 'the-token', got %q", cred)
	}
}

func TestCredentials_FallbackToAPIKey(t *testing.T) {
	t.Setenv("MY_KEY", "the-api-key")

	cfg := model.ProviderConfig{APIKeyEnv: "MY_KEY", TokenEnv: "", Name: "test"}
	cred, isToken, err := credentials(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isToken {
		t.Error("expected isToken=false when only api_key_env is set")
	}
	if cred != "the-api-key" {
		t.Errorf("expected 'the-api-key', got %q", cred)
	}
}

func TestCredentials_TokenEnvSetButEmpty(t *testing.T) {
	t.Setenv("MY_KEY", "the-api-key")

	cfg := model.ProviderConfig{APIKeyEnv: "MY_KEY", TokenEnv: "EMPTY_TOKEN_VAR_12345", Name: "test"}
	cred, isToken, err := credentials(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isToken {
		t.Error("expected fallback to API key when token env var is empty")
	}
	if cred != "the-api-key" {
		t.Errorf("expected 'the-api-key', got %q", cred)
	}
}

func TestCredentials_NeitherSet(t *testing.T) {
	cfg := model.ProviderConfig{APIKeyEnv: "NOPE_12345", TokenEnv: "ALSO_NOPE_12345", Name: "test"}
	_, _, err := credentials(cfg)
	if err == nil {
		t.Fatal("expected error when neither credential is available")
	}
}

func TestNewChain_TokenAuth(t *testing.T) {
	t.Setenv("MY_TOKEN", "oauth-token")

	configs := []model.ProviderConfig{
		{Name: "anthropic", BaseURL: "http://localhost", Model: "claude", TokenEnv: "MY_TOKEN", Priority: 1},
	}

	chain, err := NewChain(configs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chain.providers) != 1 {
		t.Fatalf("expected 1 provider, got %d", len(chain.providers))
	}
	ap, ok := chain.providers[0].(*Anthropic)
	if !ok {
		t.Fatalf("expected *Anthropic, got %T", chain.providers[0])
	}
	if !ap.isToken {
		t.Error("expected isToken=true for token-based auth")
	}
}
