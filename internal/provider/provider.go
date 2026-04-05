// Package provider implements multi-provider LLM client with fallback chain.
//
// Each task type (extraction, consolidation, query) has its own ordered
// list of providers. If the first provider fails, the next is tried.
package provider

import (
	"context"
	"fmt"
	"os"

	"github.com/aegis-alpha/imprint-mace/internal/model"
)

// Request is what we send to an LLM provider.
type Request struct {
	SystemPrompt string
	UserPrompt   string
	Temperature  float64
	MaxTokens    int
}

// Response is what we get back.
type Response struct {
	Content      string
	ProviderName string
	Model        string
	TokensUsed   int
}

// Provider sends a request to an LLM and returns the response.
type Provider interface {
	Name() string
	Send(ctx context.Context, req Request) (*Response, error)
}

// Chain tries providers in order until one succeeds.
type Chain struct {
	providers []Provider
}

// NewChain creates a fallback chain from config.
// Providers are tried in config order (first config entry = tried first).
// The provider type is determined by the "name" field in config:
//   - "anthropic" -> Anthropic Messages API
//   - "ollama"    -> Ollama native API (no API key needed)
//   - anything else -> OpenAI-compatible API (covers OpenAI, Google, Groq, Together, vLLM, etc.)
func NewChain(configs []model.ProviderConfig) (*Chain, error) {
	if len(configs) == 0 {
		return nil, fmt.Errorf("at least one provider required")
	}
	providers := make([]Provider, 0, len(configs))
	for _, cfg := range configs {
		p, err := newProvider(cfg)
		if err != nil {
			continue
		}
		providers = append(providers, p)
	}
	if len(providers) == 0 {
		return nil, fmt.Errorf("no providers have API keys configured (check env vars)")
	}
	return &Chain{providers: providers}, nil
}

// credentials resolves auth from config. token_env takes priority over api_key_env.
// Returns (credential, isToken). isToken=true means use Bearer token auth.
func credentials(cfg model.ProviderConfig) (string, bool, error) {
	if cfg.TokenEnv != "" {
		token := os.Getenv(cfg.TokenEnv)
		if token != "" {
			return token, true, nil
		}
	}
	if cfg.APIKeyEnv != "" {
		key := os.Getenv(cfg.APIKeyEnv)
		if key != "" {
			return key, false, nil
		}
	}
	return "", false, fmt.Errorf("no credentials for %s (checked env: %s, %s)", cfg.Name, cfg.TokenEnv, cfg.APIKeyEnv)
}

func newProvider(cfg model.ProviderConfig) (Provider, error) {
	switch cfg.Name {
	case "anthropic":
		cred, isToken, err := credentials(cfg)
		if err != nil {
			return nil, err
		}
		return NewAnthropic(cfg, cred, isToken), nil

	case "ollama":
		return NewOllama(cfg), nil

	default:
		cred, _, err := credentials(cfg)
		if err != nil {
			if len(cfg.Headers) == 0 {
				return nil, err
			}
			cred = ""
		}
		return NewOpenAICompatible(cfg, cred), nil
	}
}

// Send tries each provider in order. Returns the first successful response.
// If all fail, returns the last error.
func (c *Chain) Send(ctx context.Context, req Request) (*Response, error) {
	var lastErr error
	for _, p := range c.providers {
		resp, err := p.Send(ctx, req)
		if err == nil {
			return resp, nil
		}
		lastErr = fmt.Errorf("provider %s: %w", p.Name(), err)
	}
	return nil, fmt.Errorf("all providers failed, last error: %w", lastErr)
}
