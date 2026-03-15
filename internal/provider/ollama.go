package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/aegis-alpha/imprint-mace/internal/model"
)

// Ollama implements Provider for the Ollama native API (/api/chat).
// While Ollama also supports OpenAI-compatible format on /v1/,
// the native API gives access to Ollama-specific features like
// structured output format enforcement.
type Ollama struct {
	config model.ProviderConfig
	client *http.Client
}

func NewOllama(cfg model.ProviderConfig) *Ollama {
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout == 0 {
		timeout = 120 * time.Second
	}
	return &Ollama{
		config: cfg,
		client: &http.Client{Timeout: timeout},
	}
}

func (p *Ollama) Name() string {
	return p.config.Name
}

func (p *Ollama) Send(ctx context.Context, req Request) (*Response, error) {
	messages := []ollamaMessage{
		{Role: "system", Content: req.SystemPrompt},
		{Role: "user", Content: req.UserPrompt},
	}

	body := ollamaRequest{
		Model:    p.config.Model,
		Messages: messages,
		Stream:   false,
		Options: ollamaOptions{
			NumPredict: req.MaxTokens,
		},
	}
	temp := req.Temperature
	if p.config.Temperature > 0 {
		temp = p.config.Temperature
	}
	if temp > 0 {
		body.Options.Temperature = &temp
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := p.config.BaseURL + "/api/chat"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d: %s", httpResp.StatusCode, truncate(string(respBody), 200))
	}

	var ollamaResp ollamaResponse
	if err := json.Unmarshal(respBody, &ollamaResp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	if ollamaResp.Message.Content == "" {
		return nil, fmt.Errorf("empty content in response")
	}

	return &Response{
		Content:      ollamaResp.Message.Content,
		ProviderName: p.config.Name,
		Model:        p.config.Model,
		TokensUsed:   ollamaResp.PromptEvalCount + ollamaResp.EvalCount,
	}, nil
}

// Ollama native API types.

type ollamaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ollamaOptions struct {
	Temperature *float64 `json:"temperature,omitempty"`
	NumPredict  int     `json:"num_predict,omitempty"`
}

type ollamaRequest struct {
	Model    string          `json:"model"`
	Messages []ollamaMessage `json:"messages"`
	Stream   bool            `json:"stream"`
	Options  ollamaOptions   `json:"options,omitempty"`
}

type ollamaResponse struct {
	Message         ollamaMessage `json:"message"`
	PromptEvalCount int           `json:"prompt_eval_count"`
	EvalCount       int           `json:"eval_count"`
}
