//go:build integration

package internal_test

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/aegis-alpha/imprint-mace/internal/config"
	"github.com/aegis-alpha/imprint-mace/internal/consolidation"
	"github.com/aegis-alpha/imprint-mace/internal/db"
	"github.com/aegis-alpha/imprint-mace/internal/extraction"
	"github.com/aegis-alpha/imprint-mace/internal/imprint"
	"github.com/aegis-alpha/imprint-mace/internal/model"
	"github.com/aegis-alpha/imprint-mace/internal/provider"
	"github.com/aegis-alpha/imprint-mace/internal/query"
)

func requiredEnv(t *testing.T, key string) string {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		t.Skipf("%s not set, skipping integration test", key)
	}
	return v
}

func TestIntegration_FullPipeline(t *testing.T) {
	requiredEnv(t, "OPENAI_API_KEY")

	ctx := context.Background()
	logger := slog.Default()
	types := config.DefaultTypes()

	dbPath := t.TempDir() + "/integration.db"
	store, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	llmCfg := []model.ProviderConfig{{
		Name:      "openai",
		BaseURL:   "https://api.openai.com/v1",
		Model:     "gpt-4.1-mini",
		APIKeyEnv: "OPENAI_API_KEY",
	}}
	chain, err := provider.NewChain(llmCfg)
	if err != nil {
		t.Fatalf("create provider chain: %v", err)
	}

	promptDir := t.TempDir()
	promptPath := promptDir + "/extraction-prompt.md"
	promptData, err := os.ReadFile("extraction/testdata/extraction-prompt.md")
	if err != nil {
		promptData, err = os.ReadFile("../prompts/extraction-prompt.md")
		if err != nil {
			t.Fatalf("read extraction prompt: %v", err)
		}
	}
	if err := os.WriteFile(promptPath, promptData, 0600); err != nil {
		t.Fatalf("write prompt: %v", err)
	}

	ext, err := extraction.New(chain, promptPath, types, logger)
	if err != nil {
		t.Fatalf("create extractor: %v", err)
	}

	eng := imprint.New(ext, store, nil, 0, 0, logger)

	// Step 1: Ingest 3 messages
	messages := []struct {
		text   string
		source string
	}{
		{"We decided to use PostgreSQL for the Acme project. It handles our scale better than SQLite.", "meeting-2026-03-15.md"},
		{"Alice will lead the backend team. She prefers Go for all new services.", "standup-2026-03-16.md"},
		{"The deployment window is every Thursday 2-4pm UTC. No deploys on Fridays.", "ops-policy.md"},
	}

	for _, m := range messages {
		result, err := eng.Ingest(ctx, m.text, m.source)
		if err != nil {
			maybeSkipProviderQuota(t, "ingest "+m.source, err)
			t.Fatalf("ingest %q: %v", m.source, err)
		}
		t.Logf("ingested %s: %d facts, %d entities", m.source, result.FactsCount, result.EntitiesCount)
	}

	// Step 2: Verify facts were created
	facts, err := store.ListFacts(ctx, db.FactFilter{Limit: 100})
	if err != nil {
		t.Fatalf("list facts: %v", err)
	}
	if len(facts) == 0 {
		t.Fatal("expected facts > 0 after ingesting 3 messages")
	}
	t.Logf("total facts in DB: %d", len(facts))

	// Step 3: Query
	querier := query.New(store, nil, chain, "", logger)
	qResult, err := querier.Query(ctx, "What database does Acme use?")
	if err != nil {
		maybeSkipProviderQuota(t, "query", err)
		t.Fatalf("query: %v", err)
	}
	if qResult.Answer == "" {
		t.Fatal("expected non-empty answer from query")
	}
	t.Logf("query answer: %s", qResult.Answer)
	t.Logf("citations: %d, facts consulted: %d", len(qResult.Citations), qResult.FactsConsulted)

	// Step 4: Consolidation
	consolPromptData, err := os.ReadFile("consolidation/testdata/consolidation-prompt.md")
	if err != nil {
		consolPromptData, err = os.ReadFile("../prompts/consolidation-prompt.md")
		if err != nil {
			t.Fatalf("read consolidation prompt: %v", err)
		}
	}
	consolPromptPath := promptDir + "/consolidation-prompt.md"
	if err := os.WriteFile(consolPromptPath, consolPromptData, 0600); err != nil {
		t.Fatalf("write consolidation prompt: %v", err)
	}

	consol, err := consolidation.New(chain, store, consolPromptPath, types, 0.40, logger)
	if err != nil {
		t.Fatalf("create consolidator: %v", err)
	}

	cResults, err := consol.Consolidate(ctx, 50)
	if err != nil {
		maybeSkipProviderQuota(t, "consolidate", err)
		t.Fatalf("consolidate: %v", err)
	}
	if len(cResults) == 0 {
		t.Log("consolidation returned no clusters -- skipping connection check")
		return
	}
	totalConns := 0
	for i := range cResults {
		totalConns += len(cResults[i].FactConnections)
	}
	t.Logf("consolidation: %d clusters, %d connections, importance=%.2f",
		len(cResults), totalConns, cResults[0].Consolidation.Importance)

	// Step 5: Verify fact connections via one of the source facts
	if len(cResults[0].Consolidation.SourceFactIDs) > 0 {
		connections, err := store.ListFactConnections(ctx, cResults[0].Consolidation.SourceFactIDs[0])
		if err != nil {
			t.Fatalf("list connections: %v", err)
		}
		t.Logf("fact connections for first source fact: %d", len(connections))
	}
}
