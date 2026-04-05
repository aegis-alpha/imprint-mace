package provider

import (
	"context"
	"fmt"
	"os"

	"github.com/aegis-alpha/imprint-mace/internal/model"
)

type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
	ModelName() string
}

// EmbedderChain tries embedders in order until one succeeds.
type EmbedderChain struct {
	embedders []Embedder
}

func NewEmbedderChain(configs []model.ProviderConfig) (*EmbedderChain, error) {
	if len(configs) == 0 {
		return nil, nil
	}
	var embedders []Embedder
	for _, cfg := range configs {
		e, err := newEmbedder(cfg)
		if err != nil {
			continue
		}
		embedders = append(embedders, e)
	}
	if len(embedders) == 0 {
		return nil, nil
	}
	return &EmbedderChain{embedders: embedders}, nil
}

func newEmbedder(cfg model.ProviderConfig) (Embedder, error) {
	switch cfg.Name {
	case "ollama":
		return NewOllamaEmbedder(cfg.BaseURL, cfg.Model), nil
	default:
		key := ""
		if cfg.APIKeyEnv != "" {
			key = os.Getenv(cfg.APIKeyEnv)
		}
		if key == "" && len(cfg.Headers) == 0 {
			return nil, fmt.Errorf("no API key for embedder %s (env: %s)", cfg.Name, cfg.APIKeyEnv)
		}
		return NewOpenAIEmbedder(cfg.BaseURL, cfg.Model, key, cfg.Headers), nil
	}
}

func (c *EmbedderChain) Embed(ctx context.Context, text string) ([]float32, error) {
	var lastErr error
	for _, e := range c.embedders {
		vec, err := e.Embed(ctx, text)
		if err == nil {
			return vec, nil
		}
		lastErr = fmt.Errorf("embedder %s: %w", e.ModelName(), err)
	}
	return nil, fmt.Errorf("all embedders failed, last error: %w", lastErr)
}

func (c *EmbedderChain) ModelName() string {
	if len(c.embedders) > 0 {
		return c.embedders[0].ModelName()
	}
	return ""
}
