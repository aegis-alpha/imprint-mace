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

// Anthropic implements Provider for the Anthropic Messages API (/v1/messages).
// This is required because Anthropic uses a different format than OpenAI:
// - system prompt is a top-level field, not a message
// - different request/response structure
// - uses x-api-key header instead of Authorization Bearer
type Anthropic struct {
	config  model.ProviderConfig
	cred    string
	isToken bool
	client  *http.Client
}

func NewAnthropic(cfg model.ProviderConfig, cred string, isToken bool) *Anthropic {
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	return &Anthropic{
		config:  cfg,
		cred:    cred,
		isToken: isToken,
		client:  &http.Client{Timeout: timeout},
	}
}

func (p *Anthropic) Name() string {
	return p.config.Name
}

func (p *Anthropic) Send(ctx context.Context, req Request) (*Response, error) {
	messages := []anthropicMessage{
		{Role: "user", Content: req.UserPrompt},
	}

	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = 4096
	}

	body := anthropicRequest{
		Model:     p.config.Model,
		MaxTokens: maxTokens,
		System:    req.SystemPrompt,
		Messages:  messages,
	}
	temp := req.Temperature
	if p.config.Temperature > 0 {
		temp = p.config.Temperature
	}
	if temp > 0 {
		body.Temperature = &temp
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := p.config.BaseURL + "/v1/messages"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.isToken {
		httpReq.Header.Set("Authorization", "Bearer "+p.cred)
	} else {
		httpReq.Header.Set("x-api-key", p.cred)
	}
	httpReq.Header.Set("anthropic-version", "2023-06-01")

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

	var anthropicResp anthropicResponse
	if err := json.Unmarshal(respBody, &anthropicResp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	var content string
	for _, block := range anthropicResp.Content {
		if block.Type == "text" {
			content += block.Text
		}
	}
	if content == "" {
		return nil, fmt.Errorf("no text content in response")
	}

	return &Response{
		Content:      content,
		ProviderName: p.config.Name,
		Model:        p.config.Model,
		TokensUsed:   anthropicResp.Usage.InputTokens + anthropicResp.Usage.OutputTokens,
	}, nil
}

// Anthropic Messages API types.

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicRequest struct {
	Model       string             `json:"model"`
	MaxTokens   int                `json:"max_tokens"`
	System      string             `json:"system,omitempty"`
	Messages    []anthropicMessage `json:"messages"`
	Temperature *float64           `json:"temperature,omitempty"`
}

type anthropicResponse struct {
	Content []anthropicContentBlock `json:"content"`
	Usage   anthropicUsage          `json:"usage"`
}

type anthropicContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type anthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}
