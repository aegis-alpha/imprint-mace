package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aegis-alpha/imprint-mace/internal/model"
)

type ModelInfo struct {
	ID            string
	ContextWindow int
}

type ModelLister interface {
	ListModels(ctx context.Context) ([]ModelInfo, error)
	ProviderName() string
}

// --- OpenAI-compatible ModelLister ---

type OpenAIModelLister struct {
	baseURL string
	apiKey  string
	name    string
	client  *http.Client
}

func NewOpenAIModelLister(baseURL, apiKey string, client *http.Client) *OpenAIModelLister {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &OpenAIModelLister{baseURL: baseURL, apiKey: apiKey, name: "openai", client: client}
}

func (l *OpenAIModelLister) ProviderName() string { return l.name }

func (l *OpenAIModelLister) ListModels(ctx context.Context) ([]ModelInfo, error) {
	url := l.baseURL + "/models"
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+l.apiKey)

	resp, err := l.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, truncate(string(body), 200))
	}

	var openaiResp struct {
		Data []struct {
			ID            string `json:"id"`
			ContextWindow int    `json:"context_window"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &openaiResp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	if len(openaiResp.Data) > 0 {
		models := make([]ModelInfo, len(openaiResp.Data))
		for i, m := range openaiResp.Data {
			models[i] = ModelInfo{ID: m.ID, ContextWindow: m.ContextWindow}
		}
		return models, nil
	}

	var googleResp struct {
		Models []struct {
			Name            string `json:"name"`
			InputTokenLimit int    `json:"inputTokenLimit"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &googleResp); err != nil {
		return nil, fmt.Errorf("parse google response: %w", err)
	}
	if len(googleResp.Models) > 0 {
		models := make([]ModelInfo, len(googleResp.Models))
		for i, m := range googleResp.Models {
			id := m.Name
			if strings.HasPrefix(id, "models/") {
				id = strings.TrimPrefix(id, "models/")
			}
			models[i] = ModelInfo{ID: id, ContextWindow: m.InputTokenLimit}
		}
		return models, nil
	}

	return nil, nil
}

// --- Anthropic ModelLister ---

type AnthropicModelLister struct {
	baseURL string
	cred    string
	isToken bool
	name    string
	client  *http.Client
}

func NewAnthropicModelLister(baseURL, cred string, isToken bool, client *http.Client) *AnthropicModelLister {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &AnthropicModelLister{baseURL: baseURL, cred: cred, isToken: isToken, name: "anthropic", client: client}
}

func (l *AnthropicModelLister) ProviderName() string { return l.name }

func (l *AnthropicModelLister) ListModels(ctx context.Context) ([]ModelInfo, error) {
	url := l.baseURL + "/v1/models"
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	if l.isToken {
		req.Header.Set("Authorization", "Bearer "+l.cred)
	} else {
		req.Header.Set("x-api-key", l.cred)
	}
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := l.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, truncate(string(body), 200))
	}

	var anthropicResp struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &anthropicResp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	models := make([]ModelInfo, len(anthropicResp.Data))
	for i, m := range anthropicResp.Data {
		models[i] = ModelInfo{ID: m.ID}
	}
	return models, nil
}

// --- Ollama ModelLister ---

type OllamaModelLister struct {
	baseURL string
	name    string
	client  *http.Client
}

func NewOllamaModelLister(baseURL string, client *http.Client) *OllamaModelLister {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &OllamaModelLister{baseURL: baseURL, name: "ollama", client: client}
}

func (l *OllamaModelLister) ProviderName() string { return l.name }

func (l *OllamaModelLister) ListModels(ctx context.Context) ([]ModelInfo, error) {
	url := l.baseURL + "/api/tags"
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := l.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, truncate(string(body), 200))
	}

	var ollamaResp struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &ollamaResp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	models := make([]ModelInfo, len(ollamaResp.Models))
	for i, m := range ollamaResp.Models {
		models[i] = ModelInfo{ID: m.Name}
	}
	return models, nil
}

// --- Factory ---

// NewModelListersFromConfig builds ModelListers from provider configs grouped by task type.
// Deduplicates by (provider_name, base_url) so the same provider appearing in
// multiple task chains only produces one lister.
func NewModelListersFromConfig(taskConfigs map[string][]model.ProviderConfig) []ModelLister {
	type key struct{ name, baseURL string }
	seen := make(map[key]bool)
	var listers []ModelLister

	for _, configs := range taskConfigs {
		for _, cfg := range configs {
			k := key{cfg.Name, cfg.BaseURL}
			if seen[k] {
				continue
			}
			seen[k] = true

			switch cfg.Name {
			case "anthropic":
				cred, isToken, err := credentials(cfg)
				if err != nil {
					continue
				}
				l := NewAnthropicModelLister(cfg.BaseURL, cred, isToken, nil)
				l.name = cfg.Name
				listers = append(listers, l)
			case "ollama":
				l := NewOllamaModelLister(cfg.BaseURL, nil)
				l.name = cfg.Name
				listers = append(listers, l)
			default:
				cred, _, err := credentials(cfg)
				if err != nil {
					continue
				}
				l := NewOpenAIModelLister(cfg.BaseURL, cred, nil)
				l.name = cfg.Name
				listers = append(listers, l)
			}
		}
	}
	return listers
}
