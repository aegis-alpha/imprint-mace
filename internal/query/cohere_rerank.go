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

// CohereReranker calls Cohere's /v2/rerank API (document reranking).
type CohereReranker struct {
	cfg    model.ProviderConfig
	apiKey string
	client *http.Client
}

// NewCohereReranker builds a reranker from providers.reranker config (name = "cohere").
func NewCohereReranker(cfg model.ProviderConfig) (*CohereReranker, error) {
	key, err := rerankerCredential(cfg)
	if err != nil {
		return nil, err
	}
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout == 0 {
		timeout = 45 * time.Second
	}
	base := strings.TrimSpace(cfg.BaseURL)
	if base == "" {
		base = "https://api.cohere.com"
	}
	// Store normalized base on cfg copy for URL building
	cfg.BaseURL = strings.TrimSuffix(base, "/")
	if strings.TrimSpace(cfg.Model) == "" {
		cfg.Model = "rerank-english-v3.0"
	}
	return &CohereReranker{
		cfg:    cfg,
		apiKey: key,
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
	return "", fmt.Errorf("no API key for reranker (env %s / %s)", cfg.TokenEnv, cfg.APIKeyEnv)
}

type cohereRerankRequest struct {
	Model     string   `json:"model"`
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
	TopN      int      `json:"top_n"`
}

type cohereRerankResponse struct {
	Results []struct {
		Index            int     `json:"index"`
		RelevanceScore   float64 `json:"relevance_score"`
	} `json:"results"`
}

// Rerank implements [Reranker]. Reorders items using Cohere scores; output length
// matches input. topN selects API top_n (capped to len(items)).
func (c *CohereReranker) Rerank(ctx context.Context, query string, items []RankedItem, topN int) ([]RankedItem, error) {
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

	body := cohereRerankRequest{
		Model:     c.cfg.Model,
		Query:     query,
		Documents: docs,
		TopN:      topN,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal rerank request: %w", err)
	}

	url := c.cfg.BaseURL + "/v2/rerank"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("create rerank request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.client.Do(req)
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

	var parsed cohereRerankResponse
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
	// Append any missing indices in original order (partial API response).
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
