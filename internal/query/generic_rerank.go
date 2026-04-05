package query

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/aegis-alpha/imprint-mace/internal/model"
)

// GenericReranker calls a provider rerank endpoint using OpenAI-style /v1/rerank.
// Cohere compatibility: when base_url contains "cohere.com", /v2/rerank is used.
type GenericReranker struct {
	cfg    model.ProviderConfig
	token  string
	client *http.Client
}

func NewGenericReranker(cfg model.ProviderConfig) (*GenericReranker, error) {
	token, err := rerankerCredential(cfg)
	if err != nil {
		return nil, err
	}
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout == 0 {
		timeout = 45 * time.Second
	}
	base := strings.TrimSpace(cfg.BaseURL)
	if base == "" {
		return nil, fmt.Errorf("reranker base_url is required")
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return nil, fmt.Errorf("reranker model is required")
	}
	cfg.BaseURL = strings.TrimSuffix(base, "/")
	return &GenericReranker{
		cfg:    cfg,
		token:  token,
		client: &http.Client{Timeout: timeout},
	}, nil
}

func rerankerCredential(cfg model.ProviderConfig) (string, error) {
	if cfg.TokenEnv != "" {
		if t := os.Getenv(cfg.TokenEnv); t != "" {
			return t, nil
		}
	}
	if cfg.APIKeyEnv != "" {
		if k := os.Getenv(cfg.APIKeyEnv); k != "" {
			return k, nil
		}
	}
	if len(cfg.Headers) > 0 {
		return "", nil
	}
	return "", fmt.Errorf("no API key for reranker (env %s / %s)", cfg.TokenEnv, cfg.APIKeyEnv)
}

type genericRerankRequest struct {
	Model     string   `json:"model"`
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
	TopN      int      `json:"top_n"`
}

type genericRerankResponse struct {
	Results []struct {
		Index          int     `json:"index"`
		RelevanceScore float64 `json:"relevance_score"`
	} `json:"results"`
}

func (g *GenericReranker) rerankPath() string {
	if strings.EqualFold(strings.TrimSpace(g.cfg.Name), "cohere") ||
		strings.Contains(strings.ToLower(g.cfg.BaseURL), "cohere.com") {
		return "/v2/rerank"
	}
	return "/v1/rerank"
}

func (g *GenericReranker) Rerank(ctx context.Context, query string, items []RankedItem, topN int) ([]RankedItem, error) {
	if len(items) == 0 {
		return nil, nil
	}
	if topN <= 0 || topN > len(items) {
		topN = len(items)
	}
	docs := make([]string, len(items))
	for i := range items {
		docs[i] = factTextForRerank(items[i].Fact)
		if docs[i] == "" {
			docs[i] = items[i].Fact.ID
		}
	}
	reqBody := genericRerankRequest{
		Model:     g.cfg.Model,
		Query:     query,
		Documents: docs,
		TopN:      topN,
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal rerank request: %w", err)
	}

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		g.cfg.BaseURL+g.rerankPath(),
		bytes.NewReader(payload),
	)
	if err != nil {
		return nil, fmt.Errorf("create rerank request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if g.token != "" {
		req.Header.Set("Authorization", "Bearer "+g.token)
	}
	for k, v := range g.cfg.Headers {
		req.Header.Set(k, v)
	}

	resp, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("rerank http: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read rerank response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("rerank status %d: %s", resp.StatusCode, truncateRerankErr(respBody))
	}

	var parsed genericRerankResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("parse rerank response: %w", err)
	}

	out := make([]RankedItem, 0, len(items))
	used := make([]bool, len(items))
	for _, r := range parsed.Results {
		if r.Index < 0 || r.Index >= len(items) || used[r.Index] {
			continue
		}
		used[r.Index] = true
		item := items[r.Index]
		item.Score = r.RelevanceScore
		out = append(out, item)
	}
	for i := range items {
		if !used[i] {
			out = append(out, items[i])
		}
	}
	if len(out) != len(items) {
		return nil, fmt.Errorf("rerank internal: length mismatch %d vs %d", len(out), len(items))
	}
	return out, nil
}

func truncateRerankErr(b []byte) string {
	s := string(b)
	if len(s) > 240 {
		return s[:240] + "..."
	}
	return s
}
