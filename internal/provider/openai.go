package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aegis-alpha/imprint-mace/internal/model"
)

// OpenAICompatible implements Provider for any OpenAI-compatible API.
// This covers: OpenAI, Google (via OpenAI-compat endpoint), Anthropic (via proxy),
// local models (Ollama, vLLM, llama.cpp server).
type OpenAICompatible struct {
	config model.ProviderConfig
	apiKey string
	client *http.Client
}

func NewOpenAICompatible(cfg model.ProviderConfig, apiKey string) *OpenAICompatible {
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	return &OpenAICompatible{
		config: cfg,
		apiKey: apiKey,
		client: &http.Client{Timeout: timeout},
	}
}

func (p *OpenAICompatible) Name() string {
	return p.config.Name
}

func (p *OpenAICompatible) Send(ctx context.Context, req Request) (*Response, error) {
	messages := []chatMessage{
		{Role: "system", Content: req.SystemPrompt},
		{Role: "user", Content: req.UserPrompt},
	}

	body := chatRequest{
		Model:               p.config.Model,
		Messages:            messages,
		MaxCompletionTokens: req.MaxTokens,
	}
	temp := req.Temperature
	if p.config.Temperature > 0 {
		temp = p.config.Temperature
	}
	if temp > 0 {
		body.Temperature = &temp
	}
	if body.MaxCompletionTokens == 0 {
		body.MaxCompletionTokens = 4096
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := p.config.BaseURL + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	if strings.Contains(p.config.BaseURL, "openrouter.ai") {
		httpReq.Header.Set("HTTP-Referer", "https://github.com/aegis-alpha/imprint-MACE")
		httpReq.Header.Set("X-Title", "Imprint MACE")
	}

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

	if len(respBody) == 0 {
		return nil, fmt.Errorf("empty response body (status %d)", httpResp.StatusCode)
	}

	var chatResp chatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return nil, fmt.Errorf("parse response (len=%d): %w", len(respBody), err)
	}

	if len(chatResp.Choices) == 0 {
		return nil, fmt.Errorf("empty choices in response")
	}

	return &Response{
		Content:      chatResp.Choices[0].Message.Content,
		ProviderName: p.config.Name,
		Model:        p.config.Model,
		TokensUsed:   chatResp.Usage.TotalTokens,
	}, nil
}

// OpenAI chat completions API types.

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model               string        `json:"model"`
	Messages            []chatMessage `json:"messages"`
	Temperature         *float64      `json:"temperature,omitempty"`
	MaxCompletionTokens int           `json:"max_completion_tokens,omitempty"`
}

type chatResponse struct {
	Choices []chatChoice `json:"choices"`
	Usage   chatUsage    `json:"usage"`
}

type chatChoice struct {
	Message chatMessage `json:"message"`
}

type chatUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
