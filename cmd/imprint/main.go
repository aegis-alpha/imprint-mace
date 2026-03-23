package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/aegis-alpha/imprint-mace/internal/api"
	"github.com/aegis-alpha/imprint-mace/internal/config"
	"github.com/aegis-alpha/imprint-mace/internal/consolidation"
	"github.com/aegis-alpha/imprint-mace/internal/quality"
	impctx "github.com/aegis-alpha/imprint-mace/internal/context"
	"github.com/aegis-alpha/imprint-mace/internal/db"
	impeval "github.com/aegis-alpha/imprint-mace/internal/eval"
	"github.com/aegis-alpha/imprint-mace/internal/extraction"
	"github.com/aegis-alpha/imprint-mace/internal/imprint"
	"github.com/aegis-alpha/imprint-mace/internal/ingest"
	impmcp "github.com/aegis-alpha/imprint-mace/internal/mcp"
	"github.com/aegis-alpha/imprint-mace/internal/model"
	"github.com/aegis-alpha/imprint-mace/internal/provider"
	"github.com/aegis-alpha/imprint-mace/internal/query"
	"github.com/aegis-alpha/imprint-mace/internal/transcript"
	"github.com/aegis-alpha/imprint-mace/internal/update"
	"github.com/aegis-alpha/imprint-mace/internal/watcher"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "version") {
		fmt.Printf("imprint %s\n", version)
		os.Exit(0)
	}

	go update.Check(version)

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfgPath := flag.String("config", "", "path to config.toml (default: config.toml, env: IMPRINT_CONFIG)")
	flag.Parse()

	if *cfgPath == "" {
		if env := os.Getenv("IMPRINT_CONFIG"); env != "" {
			*cfgPath = env
		} else {
			*cfgPath = "config.toml"
		}
	}

	args := flag.Args()
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: imprint [--config path] <command> [args]")
		fmt.Fprintln(os.Stderr, "Commands:")
		fmt.Fprintln(os.Stderr, "  ingest                    Read text from stdin and extract facts")
		fmt.Fprintln(os.Stderr, "  ingest-dir PATH           Process all .txt/.md files in a directory (cron-friendly)")
		fmt.Fprintln(os.Stderr, "  watch PATH                Watch a directory for new/changed files (long-running)")
		fmt.Fprintln(os.Stderr, "  embed-backfill [--model=X] [--chunks] Generate embeddings for facts/chunks without them")
		fmt.Fprintln(os.Stderr, "  consolidate               Run one consolidation pass")
		fmt.Fprintln(os.Stderr, "  status                    Show database statistics")
		fmt.Fprintln(os.Stderr, "  query QUESTION            Ask a question against the knowledge base")
		fmt.Fprintln(os.Stderr, "  serve [--host=H] [--port=P] [--watch=PATH] Start HTTP API server (default 127.0.0.1:8080)")
		fmt.Fprintln(os.Stderr, "  mcp                       Start MCP server (stdio transport)")
		fmt.Fprintln(os.Stderr, "  export [--format=json|csv] [--output=path] Export knowledge base")
		fmt.Fprintln(os.Stderr, "  context [HINT]            Build context snapshot for system prompt injection")
		fmt.Fprintln(os.Stderr, "  eval --golden=PATH [--format=json|table] [--save-baseline] [--check] [--threshold=N]")
		fmt.Fprintln(os.Stderr, "                                          Evaluate extraction quality against golden set")
		fmt.Fprintln(os.Stderr, "  eval generate [--output=PATH]           Generate built-in golden eval dataset (default: testdata/golden/)")
		fmt.Fprintln(os.Stderr, "  eval-retrieval [--format=json|table] [--no-embedder] [--save-baseline] [--check] [--threshold=N]")
		fmt.Fprintln(os.Stderr, "                                          Evaluate retrieval quality (Recall@10, MRR)")
		fmt.Fprintln(os.Stderr, "  eval-history [--type=extraction|retrieval] [--limit=N] Show eval score history")
		fmt.Fprintln(os.Stderr, "  optimize                  Run one prompt optimization cycle (Karpathy loop)")
		fmt.Fprintln(os.Stderr, "  gc                        Delete expired facts (valid_until < now - gc_after_days)")
		fmt.Fprintln(os.Stderr, "  version                   Print version and exit")
		os.Exit(1)
	}

	cmd := args[0]
	switch cmd {
	case "ingest":
		runIngest(logger, *cfgPath)
	case "ingest-dir":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: imprint ingest-dir [--consolidate] PATH")
			os.Exit(1)
		}
		consolidateFlag := false
		dirArg := args[1]
		if args[1] == "--consolidate" {
			consolidateFlag = true
			if len(args) < 3 {
				fmt.Fprintln(os.Stderr, "Usage: imprint ingest-dir [--consolidate] PATH")
				os.Exit(1)
			}
			dirArg = args[2]
		}
		runIngestDir(logger, *cfgPath, dirArg, consolidateFlag)
	case "watch":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: imprint watch PATH")
			os.Exit(1)
		}
		runWatch(logger, *cfgPath, args[1])
	case "consolidate":
		runConsolidateCmd(logger, *cfgPath)
	case "status":
		runStatus(logger, *cfgPath)
	case "embed-backfill":
		modelFilter := ""
		chunksFlag := false
		for _, a := range args[1:] {
			if len(a) > 8 && a[:8] == "--model=" {
				modelFilter = a[8:]
			}
			if a == "--chunks" {
				chunksFlag = true
			}
		}
		if chunksFlag {
			runEmbedBackfillChunks(logger, *cfgPath, modelFilter)
		} else {
			runEmbedBackfill(logger, *cfgPath, modelFilter)
		}
	case "query":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: imprint query QUESTION")
			os.Exit(1)
		}
		question := strings.Join(args[1:], " ")
		runQuery(logger, *cfgPath, question)
	case "serve":
		hostFlag, portFlag, watchFlag := "", 0, ""
		for _, a := range args[1:] {
			switch {
			case strings.HasPrefix(a, "--host="):
				hostFlag = a[7:]
			case strings.HasPrefix(a, "--port="):
				fmt.Sscanf(a[7:], "%d", &portFlag) //nolint:gosec // best-effort parse; zero portFlag triggers auto-port
			case strings.HasPrefix(a, "--watch="):
				watchFlag = a[8:]
			}
		}
		runServe(logger, *cfgPath, hostFlag, portFlag, watchFlag)
	case "mcp":
		runMCP(logger, *cfgPath)
	case "export":
		format, output := "json", ""
		for _, a := range args[1:] {
			if strings.HasPrefix(a, "--format=") {
				format = a[9:]
			} else if strings.HasPrefix(a, "--output=") {
				output = a[9:]
			}
		}
		runExport(logger, *cfgPath, format, output)
	case "context":
		hint := ""
		if len(args) > 1 {
			hint = strings.Join(args[1:], " ")
		}
		runContext(logger, *cfgPath, hint)
	case "eval":
		if len(args) > 1 && args[1] == "generate" {
			outputDir := "testdata/golden"
			for _, a := range args[2:] {
				if strings.HasPrefix(a, "--output=") {
					outputDir = a[9:]
				}
			}
			runEvalGenerate(logger, outputDir)
			break
		}
		goldenDir, format := "", "table"
		saveBaseline, check := false, false
		threshold := 0.05
		for _, a := range args[1:] {
			switch {
			case strings.HasPrefix(a, "--golden="):
				goldenDir = a[9:]
			case strings.HasPrefix(a, "--format="):
				format = a[9:]
			case a == "--save-baseline":
				saveBaseline = true
			case a == "--check":
				check = true
			case strings.HasPrefix(a, "--threshold="):
				fmt.Sscanf(a[12:], "%f", &threshold) //nolint:gosec
			}
		}
		if goldenDir == "" {
			fmt.Fprintln(os.Stderr, "Usage: imprint eval --golden=PATH [--format=json|table] [--save-baseline] [--check] [--threshold=N]")
			fmt.Fprintln(os.Stderr, "       imprint eval generate [--output=PATH]")
			os.Exit(1)
		}
		runEval(logger, *cfgPath, goldenDir, format, saveBaseline, check, threshold)
	case "eval-retrieval":
		format := "table"
		noEmbedder := false
		saveBaseline, check := false, false
		threshold := 0.05
		for _, a := range args[1:] {
			switch {
			case strings.HasPrefix(a, "--format="):
				format = a[9:]
			case a == "--no-embedder":
				noEmbedder = true
			case a == "--save-baseline":
				saveBaseline = true
			case a == "--check":
				check = true
			case strings.HasPrefix(a, "--threshold="):
				fmt.Sscanf(a[12:], "%f", &threshold) //nolint:gosec
			}
		}
		runEvalRetrieval(logger, *cfgPath, format, noEmbedder, saveBaseline, check, threshold)
	case "eval-history":
		evalType := ""
		limit := 10
		for _, a := range args[1:] {
			switch {
			case strings.HasPrefix(a, "--type="):
				evalType = a[7:]
			case strings.HasPrefix(a, "--limit="):
				fmt.Sscanf(a[8:], "%d", &limit) //nolint:gosec
			}
		}
		runEvalHistory(logger, *cfgPath, evalType, limit)
	case "optimize":
		runOptimizeCmd(logger, *cfgPath)
	case "gc":
		runGC(logger, *cfgPath)
	case "version":
		fmt.Printf("imprint %s\n", version)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		os.Exit(1)
	}
}

func loadConfig(logger *slog.Logger, cfgPath string) *config.Config {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		logger.Error("failed to load config", "path", cfgPath, "error", err)
		os.Exit(1)
	}
	return cfg
}

func openStore(logger *slog.Logger, cfg *config.Config) *db.SQLiteStore {
	store, err := db.Open(cfg.DB.Path)
	if err != nil {
		logger.Error("failed to open database", "path", cfg.DB.Path, "error", err)
		os.Exit(1)
	}
	dims := cfg.EffectiveEmbeddingDims()
	if err := store.EnsureVecTable(context.Background(), dims); err != nil {
		logger.Error("failed to initialize vector table", "error", err)
		os.Exit(1)
	}
	if err := store.EnsureChunkVecTable(context.Background(), dims); err != nil {
		logger.Error("failed to initialize chunk vector table", "error", err)
		os.Exit(1)
	}
	return store
}

func createEngine(logger *slog.Logger, cfg *config.Config, store db.Store) *imprint.Engine {
	chain, err := provider.NewChain(cfg.Providers.Extraction)
	if err != nil {
		logger.Error("failed to create provider chain", "error", err)
		os.Exit(1)
	}

	types := cfg.EffectiveTypes()
	promptPath := cfg.Prompts.Extraction
	qcfg := cfg.EffectiveQualityConfig()
	if qcfg.OptimizedPromptPath != "" {
		if _, err := os.Stat(qcfg.OptimizedPromptPath); err == nil {
			logger.Info("using optimized extraction prompt", "path", qcfg.OptimizedPromptPath)
			promptPath = qcfg.OptimizedPromptPath
		}
	}

	extractor, err := extraction.New(chain, promptPath, types, logger)
	if err != nil {
		logger.Error("failed to create extractor", "error", err)
		os.Exit(1)
	}

	var embedder provider.Embedder
	embChain, err := provider.NewEmbedderChain(cfg.Providers.Embedding)
	if err != nil {
		logger.Warn("failed to create embedder chain", "error", err)
	}
	if embChain != nil {
		embedder = embChain
		logger.Info("embeddings enabled", "model", embChain.ModelName())
	}

	return imprint.New(extractor, store, embedder, cfg.Consolidation.DedupSimilarityThreshold, cfg.EffectiveContextTTLDays(), logger)
}

func createQuerier(logger *slog.Logger, cfg *config.Config, store db.Store) *query.Querier {
	queryProviders := cfg.Providers.Query
	if len(queryProviders) == 0 {
		queryProviders = cfg.Providers.Extraction
	}
	chain, err := provider.NewChain(queryProviders)
	if err != nil {
		logger.Error("failed to create query provider chain", "error", err)
		os.Exit(1)
	}

	var embedder provider.Embedder
	embChain, err := provider.NewEmbedderChain(cfg.Providers.Embedding)
	if err == nil && embChain != nil {
		embedder = embChain
	}

	transcriptDir := ""
	if cfg.Watcher.Path != "" {
		transcriptDir = cfg.Watcher.Path
	}

	return query.New(store, embedder, chain, transcriptDir, logger)
}

func createBuilder(cfg *config.Config, store db.Store, logger *slog.Logger) *impctx.Builder {
	var embedder provider.Embedder
	embChain, err := provider.NewEmbedderChain(cfg.Providers.Embedding)
	if err == nil && embChain != nil {
		embedder = embChain
	}

	transcriptDir := ""
	if cfg.Watcher.Path != "" {
		transcriptDir = cfg.Watcher.Path
	}

	ctxCfg := cfg.EffectiveContextConfig()
	return impctx.New(store, embedder, transcriptDir, impctx.BuilderConfig{
		RecentHours:        ctxCfg.RecentHours,
		MaxFacts:           ctxCfg.MaxFacts,
		IncludePreferences: ctxCfg.IncludePreferences != nil && *ctxCfg.IncludePreferences,
	}, logger)
}

func runContext(logger *slog.Logger, cfgPath, hint string) {
	cfg := loadConfig(logger, cfgPath)
	store := openStore(logger, cfg)
	defer store.Close()

	builder := createBuilder(cfg, store, logger)

	ctx := context.Background()
	result, err := builder.Build(ctx, hint)
	if err != nil {
		logger.Error("context build failed", "error", err)
		os.Exit(1)
	}

	fmt.Print(result)
}

func runConsolidation(ctx context.Context, logger *slog.Logger, cfg *config.Config, store db.Store) {
	if len(cfg.Providers.Consolidation) == 0 {
		return
	}
	consChain, err := provider.NewChain(cfg.Providers.Consolidation)
	if err != nil {
		logger.Warn("consolidation provider unavailable", "error", err)
		return
	}
	types := cfg.EffectiveTypes()
	threshold := cfg.EffectiveClusterSimilarityThreshold()
	cons, err := consolidation.New(consChain, store, cfg.Prompts.Consolidation, types, threshold, logger)
	if err != nil {
		logger.Warn("failed to create consolidator", "error", err)
		return
	}
	results, err := cons.Consolidate(ctx, cfg.Consolidation.MaxGroupSize)
	if err != nil {
		logger.Warn("consolidation failed", "error", err)
	} else if len(results) > 0 {
		totalConns := 0
		for i := range results {
			totalConns += len(results[i].FactConnections)
		}
		fmt.Printf("Consolidated: %d clusters, %d connections\n", len(results), totalConns)
	}
}

func runQualityCollection(ctx context.Context, logger *slog.Logger, store *db.SQLiteStore, qcfg config.QualityConfig) {
	collector := quality.NewCollector(store.RawDB(), store, qcfg, logger)
	n, err := collector.CollectAll(ctx)
	if err != nil {
		logger.Warn("quality signal collection failed", "error", err)
		return
	}
	if n > 0 {
		fmt.Printf("Quality signals collected: %d\n", n)
	}
}

func runOptimization(ctx context.Context, logger *slog.Logger, cfg *config.Config, store *db.SQLiteStore) {
	qcfg := cfg.EffectiveQualityConfig()

	signals, err := store.ListQualitySignals(ctx, "", 50)
	if err != nil || len(signals) == 0 {
		return
	}

	extractionProviders := cfg.Providers.Extraction
	chain, err := provider.NewChain(extractionProviders)
	if err != nil {
		logger.Warn("optimization: no provider chain", "error", err)
		return
	}

	mutationPrompt, err := os.ReadFile(qcfg.MutationPromptPath)
	if err != nil {
		logger.Warn("optimization: no mutation prompt", "path", qcfg.MutationPromptPath, "error", err)
		return
	}

	opt := quality.NewOptimizer(quality.OptimizerConfig{
		Sender:         chain,
		Store:          store,
		QualityCfg:     qcfg,
		PromptPath:     cfg.Prompts.Extraction,
		OptimizedPath:  qcfg.OptimizedPromptPath,
		MutationPrompt: string(mutationPrompt),
		GoldenDir:      qcfg.GoldenDir,
		Types:          cfg.EffectiveTypes(),
		Logger:         logger,
	})

	if !opt.ShouldOptimize(signals) {
		return
	}

	result := opt.Optimize(ctx)
	if result.Skipped != "" {
		logger.Info("optimization skipped", "reason", result.Skipped)
		return
	}
	if result.Error != nil {
		logger.Warn("optimization failed", "error", result.Error)
		return
	}
	if result.Kept {
		fmt.Printf("Optimization: prompt improved (%.4f -> %.4f), saved to %s\n",
			result.BaselineScore, result.CandidateScore, qcfg.OptimizedPromptPath)
	} else {
		fmt.Printf("Optimization: candidate discarded (%.4f <= %.4f)\n",
			result.CandidateScore, result.BaselineScore)
	}
}

func runIngest(logger *slog.Logger, cfgPath string) {
	cfg := loadConfig(logger, cfgPath)
	store := openStore(logger, cfg)
	defer store.Close()

	eng := createEngine(logger, cfg, store)

	text, err := io.ReadAll(os.Stdin)
	if err != nil {
		logger.Error("failed to read stdin", "error", err)
		os.Exit(1)
	}
	if len(text) == 0 {
		logger.Error("no input text (pipe text to stdin)")
		os.Exit(1)
	}

	ctx := context.Background()
	result, err := eng.Ingest(ctx, string(text), "stdin")
	if err != nil {
		logger.Error("ingest failed", "error", err)
		os.Exit(1)
	}

	fmt.Printf("Ingested: %d facts, %d entities, %d relationships\n",
		result.FactsCount, result.EntitiesCount, result.RelationshipsCount)

	if cfg.Consolidation.IntervalMinutes > 0 {
		runConsolidation(ctx, logger, cfg, store)
	}
}

func runIngestDir(logger *slog.Logger, cfgPath, dir string, consolidate bool) {
	cfg := loadConfig(logger, cfgPath)
	store := openStore(logger, cfg)
	defer store.Close()

	eng := createEngine(logger, cfg, store)
	adapter := ingest.NewBatchAdapter(eng, store, logger)

	ctx := context.Background()
	result, err := adapter.ProcessDir(ctx, dir)
	if err != nil {
		logger.Error("ingest-dir failed", "error", err)
		os.Exit(1)
	}

	fmt.Printf("Processed: %d files, %d skipped, %d facts, %d entities, %d relationships, %d errors\n",
		result.FilesProcessed, result.FilesSkipped, result.FactsTotal,
		result.EntitiesTotal, result.RelsTotal, len(result.Errors))

	if len(result.Errors) > 0 {
		for _, fe := range result.Errors {
			fmt.Fprintf(os.Stderr, "  error: %s: %v\n", fe.Path, fe.Err)
		}
	}

	if consolidate || cfg.Watcher.ConsolidateAfterIngest {
		runConsolidation(ctx, logger, cfg, store)
	}

	qcfg := cfg.EffectiveQualityConfig()
	if qcfg.Enabled != nil && *qcfg.Enabled && result.FactsTotal >= qcfg.CollectionThreshold {
		runQualityCollection(ctx, logger, store, qcfg)
		runOptimization(ctx, logger, cfg, store)
	}
}

func runWatch(logger *slog.Logger, cfgPath, dir string) {
	cfg := loadConfig(logger, cfgPath)
	store := openStore(logger, cfg)
	defer store.Close()

	eng := createEngine(logger, cfg, store)
	adapter := ingest.NewBatchAdapter(eng, store, logger)

	debounce := time.Duration(cfg.Watcher.DebounceSeconds) * time.Second
	if debounce == 0 {
		debounce = 2 * time.Second
	}

	qcfg := cfg.EffectiveQualityConfig()
	process := func(ctx context.Context, watchDir string) error {
		result, err := adapter.ProcessDir(ctx, watchDir)
		if err != nil {
			return err
		}
		if result.FilesProcessed > 0 {
			fmt.Printf("Processed: %d files, %d facts\n", result.FilesProcessed, result.FactsTotal)
			if cfg.Watcher.ConsolidateAfterIngest {
				runConsolidation(ctx, logger, cfg, store)
			}
			if qcfg.Enabled != nil && *qcfg.Enabled && result.FactsTotal >= qcfg.CollectionThreshold {
				runQualityCollection(ctx, logger, store, qcfg)
				runOptimization(ctx, logger, cfg, store)
			}
		}
		return nil
	}

	w, err := watcher.New(dir, watcher.Config{Debounce: debounce}, process)
	if err != nil {
		logger.Error("failed to create watcher", "error", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigCh
		logger.Info("received signal, shutting down", "signal", sig.String())
		cancel()
	}()

	w.Run(ctx)
}

func runConsolidateCmd(logger *slog.Logger, cfgPath string) {
	cfg := loadConfig(logger, cfgPath)
	store := openStore(logger, cfg)
	defer store.Close()

	if len(cfg.Providers.Consolidation) == 0 {
		logger.Error("no consolidation providers configured")
		os.Exit(1)
	}

	ctx := context.Background()
	runConsolidation(ctx, logger, cfg, store)
}

func runStatus(logger *slog.Logger, cfgPath string) {
	cfg := loadConfig(logger, cfgPath)
	store := openStore(logger, cfg)
	defer store.Close()

	ctx := context.Background()
	stats, err := store.Stats(ctx)
	if err != nil {
		logger.Error("failed to get stats", "error", err)
		os.Exit(1)
	}

	fmt.Printf("Database: %s\n", cfg.DB.Path)
	fmt.Printf("Facts:          %d\n", stats.Facts)
	fmt.Printf("Entities:       %d\n", stats.Entities)
	fmt.Printf("Relationships:  %d\n", stats.Relationships)
	fmt.Printf("Consolidations: %d\n", stats.Consolidations)
	fmt.Printf("Ingested files: %d\n", stats.IngestedFiles)

	printQualitySignals(ctx, store, cfg.EffectiveQualityConfig())
	printQueryStats(ctx, store)
	printEvalScores(ctx, store)
}

func printQualitySignals(ctx context.Context, store *db.SQLiteStore, qcfg config.QualityConfig) {
	signals, err := store.ListQualitySignals(ctx, "", 0)
	if err != nil || len(signals) == 0 {
		return
	}

	type key struct{ signalType, category string }
	seen := map[key]bool{}
	var latest []db.QualitySignal
	for _, s := range signals {
		k := key{s.SignalType, s.Category}
		if seen[k] {
			continue
		}
		seen[k] = true
		latest = append(latest, s)
	}

	temporalTypes := map[string]bool{"context": true, "event": true}

	fmt.Printf("\nQuality Signals:\n")
	fmt.Printf("  %-28s %-16s %8s\n", "Signal", "Category", "Value")
	fmt.Printf("  %s\n", strings.Repeat("-", 56))
	for _, s := range latest {
		warning := ""
		if s.SignalType == quality.SignalSupersedeRate &&
			!temporalTypes[s.Category] &&
			s.Value > qcfg.SupersedeRateWarning {
			warning = "  !! high"
		}
		fmt.Printf("  %-28s %-16s %8.4f%s\n", s.SignalType, s.Category, s.Value, warning)
	}
}

func printQueryStats(ctx context.Context, store *db.SQLiteStore) {
	stats, err := store.QueryLogStats(ctx, 30)
	if err != nil || (stats.TotalQueries == 0 && stats.TotalContext == 0) {
		return
	}

	total := stats.TotalQueries + stats.TotalContext
	errorRate := 0.0
	if total > 0 {
		errorRate = float64(stats.ErrorCount) / float64(total) * 100
	}

	fmt.Printf("\nQuery Stats (last 30 days):\n")
	fmt.Printf("  Total queries:    %d\n", stats.TotalQueries)
	fmt.Printf("  Total context:    %d\n", stats.TotalContext)
	fmt.Printf("  Avg latency:      %.0fms (query), %.0fms (context)\n",
		stats.AvgQueryLatency, stats.AvgContextLatency)
	fmt.Printf("  Error rate:       %.1f%%\n", errorRate)
	fmt.Printf("  Embedder avail:   %.1f%%\n", stats.EmbedderAvailPct)
}

func printEvalScores(ctx context.Context, store *db.SQLiteStore) {
	extraction, exErr := store.LatestEvalRun(ctx, "extraction")
	retrieval, retErr := store.LatestEvalRun(ctx, "retrieval")
	if exErr != nil && retErr != nil {
		return
	}

	fmt.Printf("\nEval Scores:\n")
	if extraction != nil {
		fmt.Printf("  Extraction: composite=%.4f  (%d examples, %s)\n",
			extraction.Score, extraction.ExamplesCount, extraction.CreatedAt.Format("2006-01-02 15:04"))
	}
	if retrieval != nil {
		fmt.Printf("  Retrieval:  recall@10=%.4f  mrr=%.4f  (%d questions, %s)\n",
			retrieval.Score, retrieval.Score2, retrieval.ExamplesCount, retrieval.CreatedAt.Format("2006-01-02 15:04"))
	}
}

func runEmbedBackfill(logger *slog.Logger, cfgPath, modelFilter string) {
	cfg := loadConfig(logger, cfgPath)
	store := openStore(logger, cfg)
	defer store.Close()

	embChain, err := provider.NewEmbedderChain(cfg.Providers.Embedding)
	if err != nil || embChain == nil {
		logger.Error("no embedding providers configured")
		os.Exit(1)
	}

	ctx := context.Background()

	var facts []model.Fact
	if modelFilter != "" {
		facts, err = store.ListFactsByEmbeddingModel(ctx, modelFilter)
		if err != nil {
			logger.Error("failed to list facts by model", "model", modelFilter, "error", err)
			os.Exit(1)
		}
		fmt.Printf("Re-embedding %d facts from model %q\n", len(facts), modelFilter)
	} else {
		facts, err = store.ListFactsWithoutEmbedding(ctx)
		if err != nil {
			logger.Error("failed to list facts without embedding", "error", err)
			os.Exit(1)
		}
		fmt.Printf("Backfilling %d facts without embeddings\n", len(facts))
	}

	if len(facts) == 0 {
		fmt.Println("Nothing to do.")
		return
	}

	success, failed := 0, 0
	for i := range facts {
		f := &facts[i]
		vec, err := embChain.Embed(ctx, f.Content)
		if err != nil {
			logger.Warn("embed failed", "fact_id", f.ID, "error", err)
			failed++
			continue
		}
		if err := store.UpdateFactEmbedding(ctx, f.ID, vec, embChain.ModelName()); err != nil {
			logger.Warn("store embedding failed", "fact_id", f.ID, "error", err)
			failed++
			continue
		}
		success++
	}

	fmt.Printf("Done: %d embedded, %d failed\n", success, failed)
}

func runEmbedBackfillChunks(logger *slog.Logger, cfgPath, modelFilter string) {
	cfg := loadConfig(logger, cfgPath)
	store := openStore(logger, cfg)
	defer store.Close()

	embChain, err := provider.NewEmbedderChain(cfg.Providers.Embedding)
	if err != nil || embChain == nil {
		logger.Error("no embedding providers configured")
		os.Exit(1)
	}

	transcriptDir := cfg.Watcher.Path
	if transcriptDir == "" {
		logger.Error("watcher.path not configured (needed to read chunk text from disk)")
		os.Exit(1)
	}

	ctx := context.Background()

	var chunks []model.TranscriptChunk
	if modelFilter != "" {
		chunks, err = store.ListChunksByEmbeddingModel(ctx, modelFilter)
		if err != nil {
			logger.Error("failed to list chunks by model", "model", modelFilter, "error", err)
			os.Exit(1)
		}
		fmt.Printf("Re-embedding %d chunks from model %q\n", len(chunks), modelFilter)
	} else {
		chunks, err = store.ListChunksWithoutEmbedding(ctx)
		if err != nil {
			logger.Error("failed to list chunks without embedding", "error", err)
			os.Exit(1)
		}
		fmt.Printf("Backfilling %d chunks without embeddings\n", len(chunks))
	}

	if len(chunks) == 0 {
		fmt.Println("Nothing to do.")
		return
	}

	success, failed := 0, 0
	for _, c := range chunks {
		tr, err := store.GetTranscript(ctx, c.TranscriptID)
		if err != nil || tr == nil {
			logger.Warn("transcript not found for chunk", "chunk_id", c.ID, "transcript_id", c.TranscriptID)
			failed++
			continue
		}

		text, err := transcript.ReadContext(
			filepath.Join(transcriptDir, tr.FilePath),
			c.LineStart, c.LineEnd, 0)
		if err != nil {
			logger.Warn("failed to read chunk text", "chunk_id", c.ID, "error", err)
			failed++
			continue
		}

		vec, err := embChain.Embed(ctx, text)
		if err != nil {
			logger.Warn("embed failed", "chunk_id", c.ID, "error", err)
			failed++
			continue
		}
		if err := store.UpdateChunkEmbedding(ctx, c.ID, vec, embChain.ModelName()); err != nil {
			logger.Warn("store chunk embedding failed", "chunk_id", c.ID, "error", err)
			failed++
			continue
		}
		success++
	}

	fmt.Printf("Done: %d embedded, %d failed\n", success, failed)
}

func runQuery(logger *slog.Logger, cfgPath, question string) {
	cfg := loadConfig(logger, cfgPath)
	store := openStore(logger, cfg)
	defer store.Close()

	q := createQuerier(logger, cfg, store)

	ctx := context.Background()
	result, err := q.Query(ctx, question)
	if err != nil {
		logger.Error("query failed", "error", err)
		os.Exit(1)
	}

	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		logger.Error("failed to marshal result", "error", err)
		os.Exit(1)
	}
	fmt.Println(string(data))
}

func runServe(logger *slog.Logger, cfgPath, hostFlag string, portFlag int, watchDir string) {
	cfg := loadConfig(logger, cfgPath)
	store := openStore(logger, cfg)
	defer store.Close()

	runHealthCheckAtStartup(logger, cfg, store)

	eng := createEngine(logger, cfg, store)
	q := createQuerier(logger, cfg, store)
	builder := createBuilder(cfg, store, logger)

	if watchDir == "" {
		watchDir = cfg.Watcher.Path
	}
	if watchDir != "" {
		adapter := ingest.NewBatchAdapter(eng, store, logger)
		debounce := time.Duration(cfg.Watcher.DebounceSeconds) * time.Second
		if debounce == 0 {
			debounce = 2 * time.Second
		}
		serveQcfg := cfg.EffectiveQualityConfig()
		process := func(ctx context.Context, dir string) error {
			result, err := adapter.ProcessDir(ctx, dir)
			if err != nil {
				return err
			}
			if result.FilesProcessed > 0 {
				logger.Info("watcher ingested files",
					"files", result.FilesProcessed, "facts", result.FactsTotal)
				if cfg.Watcher.ConsolidateAfterIngest {
					runConsolidation(ctx, logger, cfg, store)
				}
				if serveQcfg.Enabled != nil && *serveQcfg.Enabled && result.FactsTotal >= serveQcfg.CollectionThreshold {
					runQualityCollection(ctx, logger, store, serveQcfg)
					runOptimization(ctx, logger, cfg, store)
				}
			}
			return nil
		}
		w, err := watcher.New(watchDir, watcher.Config{Debounce: debounce}, process)
		if err != nil {
			logger.Error("failed to create watcher", "error", err)
			os.Exit(1)
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go w.Run(ctx)
		logger.Info("file watcher started", "path", watchDir)
	}

	healthCfg := cfg.EffectiveHealthConfig()
	if healthCfg.CatalogRefreshDays > 0 {
		refreshCtx, refreshCancel := context.WithCancel(context.Background())
		defer refreshCancel()
		go func() {
			interval := time.Duration(healthCfg.CatalogRefreshDays) * 24 * time.Hour
			for {
				select {
				case <-refreshCtx.Done():
					return
				case <-time.After(interval):
					runHealthCheckAtStartup(logger, cfg, store)
				}
			}
		}()
		logger.Info("catalog refresh goroutine started", "interval_days", healthCfg.CatalogRefreshDays)
	}

	if cfg.Consolidation.IntervalMinutes > 0 && len(cfg.Providers.Consolidation) > 0 {
		consChain, err := provider.NewChain(cfg.Providers.Consolidation)
		if err != nil {
			logger.Warn("consolidation provider unavailable in serve mode", "error", err)
		} else {
			types := cfg.EffectiveTypes()
			threshold := cfg.EffectiveClusterSimilarityThreshold()
			cons, err := consolidation.New(consChain, store, cfg.Prompts.Consolidation, types, threshold, logger)
			if err != nil {
				logger.Warn("failed to create consolidator for serve mode", "error", err)
			} else {
				interval := time.Duration(cfg.Consolidation.IntervalMinutes) * time.Minute
				minFacts := cfg.Consolidation.MinFacts
				if minFacts == 0 {
					minFacts = 10
				}
				sched := consolidation.NewScheduler(cons, store, interval, minFacts, cfg.Consolidation.MaxGroupSize, logger)
				schedCtx, schedCancel := context.WithCancel(context.Background())
				defer schedCancel()
				go sched.Run(schedCtx)
				logger.Info("consolidation scheduler started",
					"interval_minutes", cfg.Consolidation.IntervalMinutes,
					"min_facts", minFacts)
			}
		}
	}

	addr := cfg.EffectiveAPIAddr()
	if hostFlag != "" || portFlag != 0 {
		host := cfg.API.Host
		if host == "" {
			host = "127.0.0.1"
		}
		port := cfg.API.Port
		if port == 0 {
			port = 8080
		}
		if hostFlag != "" {
			host = hostFlag
		}
		if portFlag != 0 {
			port = portFlag
		}
		addr = fmt.Sprintf("%s:%d", host, port)
	}

	handler := api.NewHandlerWithBuilder(eng, store, q, builder, version, logger)

	ln, actualAddr, err := listenWithFallback(addr, 20, logger)
	if err != nil {
		logger.Error("no available port found", "base_addr", addr, "error", err)
		os.Exit(1)
	}
	if err := writeServeInfo(actualAddr); err != nil {
		logger.Warn("failed to write serve info", "error", err)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		logger.Info("shutting down HTTP API server")
		removeServeInfo()
		ln.Close() //nolint:gosec // shutdown path; process exits immediately after
		os.Exit(0)
	}()

	logger.Info("starting HTTP API server", "addr", actualAddr)
	if err := http.Serve(ln, handler); err != nil {
		logger.Error("server failed", "error", err)
		removeServeInfo()
		os.Exit(1)
	}
}

func runMCP(logger *slog.Logger, cfgPath string) {
	cfg := loadConfig(logger, cfgPath)
	store := openStore(logger, cfg)
	defer store.Close()

	eng := createEngine(logger, cfg, store)
	q := createQuerier(logger, cfg, store)
	builder := createBuilder(cfg, store, logger)

	srv := impmcp.NewWithBuilder(eng, store, q, builder, version, logger)
	if err := srv.Run(context.Background()); err != nil {
		logger.Error("mcp server failed", "error", err)
		os.Exit(1)
	}
}

func runHealthCheckAtStartup(logger *slog.Logger, cfg *config.Config, store db.Store) {
	taskConfigs := map[string][]model.ProviderConfig{
		"extraction":    cfg.Providers.Extraction,
		"consolidation": cfg.Providers.Consolidation,
		"query":         cfg.Providers.Query,
		"embedding":     cfg.Providers.Embedding,
	}
	listers := provider.NewModelListersFromConfig(taskConfigs)
	if len(listers) == 0 {
		return
	}

	configs := make(map[string]map[string]string)
	for task, providerCfgs := range taskConfigs {
		for _, pc := range providerCfgs {
			if configs[pc.Name] == nil {
				configs[pc.Name] = make(map[string]string)
			}
			configs[pc.Name][task] = pc.Model
		}
	}

	hc := provider.NewHealthChecker(store, listers, configs, logger)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := hc.CheckAll(ctx); err != nil {
		logger.Warn("startup health check failed", "error", err)
	} else {
		logger.Info("startup health check complete")
	}
}

func runExport(logger *slog.Logger, cfgPath, format, outputPath string) {
	cfg := loadConfig(logger, cfgPath)
	store := openStore(logger, cfg)
	defer store.Close()

	ctx := context.Background()

	facts, err := store.ListFacts(ctx, db.FactFilter{})
	if err != nil {
		logger.Error("export facts failed", "error", err)
		os.Exit(1)
	}
	entities, err := store.ListEntities(ctx, db.EntityFilter{})
	if err != nil {
		logger.Error("export entities failed", "error", err)
		os.Exit(1)
	}
	rels, err := store.ListRelationships(ctx, db.RelFilter{})
	if err != nil {
		logger.Error("export relationships failed", "error", err)
		os.Exit(1)
	}
	cons, err := store.ListConsolidations(ctx, 0)
	if err != nil {
		logger.Error("export consolidations failed", "error", err)
		os.Exit(1)
	}
	fcs, err := store.ListAllFactConnections(ctx, 0)
	if err != nil {
		logger.Error("export fact connections failed", "error", err)
		os.Exit(1)
	}
	stats, err := store.Stats(ctx)
	if err != nil {
		logger.Error("export stats failed", "error", err)
		os.Exit(1)
	}

	switch format {
	case "json":
		exportJSON(logger, outputPath, facts, entities, rels, cons, fcs, stats)
	case "csv":
		exportCSV(logger, outputPath, facts, entities, rels, cons, fcs)
	default:
		fmt.Fprintf(os.Stderr, "unknown format: %s (use json or csv)\n", format)
		os.Exit(1)
	}
}

type exportData struct {
	Facts           []model.Fact           `json:"facts"`
	Entities        []model.Entity         `json:"entities"`
	Relationships   []model.Relationship   `json:"relationships"`
	Consolidations  []model.Consolidation  `json:"consolidations"`
	FactConnections []model.FactConnection `json:"fact_connections"`
	Stats           *db.DBStats            `json:"stats"`
}

func exportJSON(logger *slog.Logger, outputPath string, facts []model.Fact, entities []model.Entity, rels []model.Relationship, cons []model.Consolidation, fcs []model.FactConnection, stats *db.DBStats) {
	data := exportData{
		Facts:           facts,
		Entities:        entities,
		Relationships:   rels,
		Consolidations:  cons,
		FactConnections: fcs,
		Stats:           stats,
	}

	out, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		logger.Error("marshal export failed", "error", err)
		os.Exit(1)
	}

	if outputPath == "" {
		fmt.Println(string(out))
		return
	}

	if err := os.WriteFile(outputPath, out, 0644); err != nil {
		logger.Error("write export file failed", "path", outputPath, "error", err)
		os.Exit(1)
	}
	fmt.Printf("Exported to %s (%d facts, %d entities, %d relationships, %d consolidations, %d connections)\n",
		outputPath, len(facts), len(entities), len(rels), len(cons), len(fcs))
}

func exportCSV(logger *slog.Logger, outputDir string, facts []model.Fact, entities []model.Entity, rels []model.Relationship, cons []model.Consolidation, fcs []model.FactConnection) {
	if outputDir == "" {
		outputDir = "export"
	}
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		logger.Error("create export directory failed", "path", outputDir, "error", err)
		os.Exit(1)
	}

	writeCSV(logger, filepath.Join(outputDir, "facts.csv"), factsToCSV(facts))
	writeCSV(logger, filepath.Join(outputDir, "entities.csv"), entitiesToCSV(entities))
	writeCSV(logger, filepath.Join(outputDir, "relationships.csv"), relsToCSV(rels))
	writeCSV(logger, filepath.Join(outputDir, "consolidations.csv"), consToCSV(cons))
	writeCSV(logger, filepath.Join(outputDir, "fact_connections.csv"), fcsToCSV(fcs))

	fmt.Printf("Exported CSV to %s/ (%d facts, %d entities, %d relationships, %d consolidations, %d connections)\n",
		outputDir, len(facts), len(entities), len(rels), len(cons), len(fcs))
}

func writeCSV(logger *slog.Logger, path string, lines []string) {
	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		logger.Error("write CSV failed", "path", path, "error", err)
		os.Exit(1)
	}
}

func factsToCSV(facts []model.Fact) []string {
	lines := []string{"id,fact_type,subject,content,confidence,source_file,valid_from,valid_until,superseded_by,created_at"}
	for i := range facts {
		f := &facts[i]
		vf, vu := "", ""
		if f.Validity.ValidFrom != nil {
			vf = f.Validity.ValidFrom.Format(time.RFC3339)
		}
		if f.Validity.ValidUntil != nil {
			vu = f.Validity.ValidUntil.Format(time.RFC3339)
		}
		lines = append(lines, fmt.Sprintf("%s,%s,%s,%s,%.2f,%s,%s,%s,%s,%s",
			f.ID, f.FactType, csvEscape(f.Subject), csvEscape(f.Content), f.Confidence,
			csvEscape(f.Source.TranscriptFile), vf, vu, f.SupersededBy, f.CreatedAt.Format(time.RFC3339)))
	}
	return lines
}

func entitiesToCSV(entities []model.Entity) []string {
	lines := []string{"id,name,entity_type,aliases,created_at"}
	for _, e := range entities {
		lines = append(lines, fmt.Sprintf("%s,%s,%s,%s,%s",
			e.ID, csvEscape(e.Name), e.EntityType, csvEscape(strings.Join(e.Aliases, ";")), e.CreatedAt.Format(time.RFC3339)))
	}
	return lines
}

func relsToCSV(rels []model.Relationship) []string {
	lines := []string{"id,from_entity,to_entity,relation_type,source_fact,created_at"}
	for _, r := range rels {
		lines = append(lines, fmt.Sprintf("%s,%s,%s,%s,%s,%s",
			r.ID, r.FromEntity, r.ToEntity, r.RelationType, r.SourceFact, r.CreatedAt.Format(time.RFC3339)))
	}
	return lines
}

func consToCSV(cons []model.Consolidation) []string {
	lines := []string{"id,source_fact_ids,summary,insight,importance,created_at"}
	for _, c := range cons {
		lines = append(lines, fmt.Sprintf("%s,%s,%s,%s,%.2f,%s",
			c.ID, csvEscape(strings.Join(c.SourceFactIDs, ";")), csvEscape(c.Summary), csvEscape(c.Insight), c.Importance, c.CreatedAt.Format(time.RFC3339)))
	}
	return lines
}

func fcsToCSV(fcs []model.FactConnection) []string {
	lines := []string{"id,fact_a,fact_b,connection_type,strength,consolidation_id,created_at"}
	for _, fc := range fcs {
		lines = append(lines, fmt.Sprintf("%s,%s,%s,%s,%.2f,%s,%s",
			fc.ID, fc.FactA, fc.FactB, fc.ConnectionType, fc.Strength, fc.ConsolidationID, fc.CreatedAt.Format(time.RFC3339)))
	}
	return lines
}

func csvEscape(s string) string {
	if strings.ContainsAny(s, ",\"\n\r") {
		return "\"" + strings.ReplaceAll(s, "\"", "\"\"") + "\""
	}
	return s
}

func listenWithFallback(baseAddr string, maxAttempts int, logger *slog.Logger) (net.Listener, string, error) {
	host, portStr, err := net.SplitHostPort(baseAddr)
	if err != nil {
		return nil, "", fmt.Errorf("invalid address %q: %w", baseAddr, err)
	}
	port, _ := strconv.Atoi(portStr)
	for i := 0; i < maxAttempts; i++ {
		addr := fmt.Sprintf("%s:%d", host, port+i)
		ln, err := net.Listen("tcp", addr)
		if err == nil {
			if i > 0 {
				logger.Warn("configured port busy, using fallback",
					"configured", baseAddr, "actual", addr)
			}
			return ln, addr, nil
		}
	}
	return nil, "", fmt.Errorf("ports %d-%d all busy", port, port+maxAttempts-1)
}

func serveInfoPath() string {
	return filepath.Join(os.Getenv("HOME"), ".imprint", "serve.json")
}

func writeServeInfo(addr string) error {
	dir := filepath.Dir(serveInfoPath())
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	url := "http://" + addr
	if advertise := os.Getenv("IMPRINT_ADVERTISE_URL"); advertise != "" {
		url = advertise
	}
	info := map[string]string{"url": url}
	data, _ := json.Marshal(info)
	return os.WriteFile(serveInfoPath(), data, 0644)
}

func removeServeInfo() {
	os.Remove(serveInfoPath()) //nolint:gosec // best-effort cleanup of serve.json
}

func runEvalGenerate(logger *slog.Logger, outputDir string) {
	result, err := impeval.Generate(outputDir)
	if err != nil {
		logger.Error("failed to generate golden set", "error", err)
		os.Exit(1)
	}
	fmt.Printf("Generated %d golden examples (%d positive, %d noise) in %s/\n",
		result.Total, result.Positive, result.Noise, result.Dir)
}

func runEval(logger *slog.Logger, cfgPath, goldenDir, format string, saveBaseline, check bool, threshold float64) {
	cfg := loadConfig(logger, cfgPath)

	chain, err := provider.NewChain(cfg.Providers.Extraction)
	if err != nil {
		logger.Error("failed to create provider chain", "error", err)
		os.Exit(1)
	}

	types := cfg.EffectiveTypes()
	evalPromptPath := cfg.Prompts.Extraction
	evalQcfg := cfg.EffectiveQualityConfig()
	if evalQcfg.OptimizedPromptPath != "" {
		if _, err := os.Stat(evalQcfg.OptimizedPromptPath); err == nil {
			logger.Info("eval using optimized extraction prompt", "path", evalQcfg.OptimizedPromptPath)
			evalPromptPath = evalQcfg.OptimizedPromptPath
		}
	}

	extractor, err := extraction.New(chain, evalPromptPath, types, logger)
	if err != nil {
		logger.Error("failed to create extractor", "error", err)
		os.Exit(1)
	}

	examples, err := impeval.LoadGoldenDir(goldenDir)
	if err != nil {
		logger.Error("failed to load golden set", "path", goldenDir, "error", err)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "Loaded %d golden examples from %s\n", len(examples), goldenDir)

	ctx := context.Background()
	report, err := impeval.Run(ctx, extractor, examples, impeval.DefaultConfig())
	if err != nil {
		logger.Error("eval failed", "error", err)
		os.Exit(1)
	}

	switch format {
	case "json":
		if err := impeval.WriteJSON(os.Stdout, report); err != nil {
			logger.Error("failed to write JSON report", "error", err)
			os.Exit(1)
		}
	default:
		impeval.WriteTable(os.Stdout, report)
	}

	runID := persistExtractionEval(ctx, logger, cfg, report, evalPromptPath)

	store := openStore(logger, cfg)
	defer store.Close()

	if saveBaseline && runID != "" {
		if err := store.SetBaseline(ctx, runID, "extraction"); err != nil {
			logger.Error("failed to set baseline", "error", err)
		} else {
			fmt.Fprintln(os.Stderr, "Baseline saved for extraction eval")
		}
	}

	if check {
		runRegressionCheck(ctx, logger, store, "extraction", report.Composite, threshold)
	}
}

func runEvalRetrieval(logger *slog.Logger, cfgPath, format string, noEmbedder, saveBaseline, check bool, threshold float64) {
	cfg := loadConfig(logger, cfgPath)

	tmpDir, err := os.MkdirTemp("", "imprint-eval-retrieval-*")
	if err != nil {
		logger.Error("failed to create temp dir", "error", err)
		os.Exit(1)
	}
	defer os.RemoveAll(tmpDir)

	tmpDBPath := filepath.Join(tmpDir, "eval.db")
	tmpStore, err := db.Open(tmpDBPath)
	if err != nil {
		logger.Error("failed to open temp database", "error", err)
		os.Exit(1)
	}
	defer tmpStore.Close()

	ctx := context.Background()

	seed := impeval.BuiltinRetrievalSeed()
	nf, ne, nr, err := impeval.SeedDB(ctx, tmpStore, seed)
	if err != nil {
		logger.Error("failed to seed eval database", "error", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "Seeded eval DB: %d facts, %d entities, %d relationships\n", nf, ne, nr)

	var embedder provider.Embedder
	if !noEmbedder {
		embChain, embErr := provider.NewEmbedderChain(cfg.Providers.Embedding)
		if embErr == nil && embChain != nil {
			embedder = embChain

			dims := cfg.EffectiveEmbeddingDims()
			if err := tmpStore.EnsureVecTable(ctx, dims); err != nil {
				logger.Error("failed to initialize vector table", "error", err)
				os.Exit(1)
			}

			fmt.Fprintf(os.Stderr, "Embedding %d seed facts...\n", nf)
			for i := range seed.Facts {
				f := &seed.Facts[i]
				vec, err := embChain.Embed(ctx, f.Content)
				if err != nil {
					logger.Warn("embed seed fact failed", "fact_id", f.ID, "error", err)
					continue
				}
				if err := tmpStore.UpdateFactEmbedding(ctx, f.ID, vec, embChain.ModelName()); err != nil {
					logger.Warn("store seed embedding failed", "fact_id", f.ID, "error", err)
				}
			}
		} else {
			fmt.Fprintln(os.Stderr, "No embedding provider configured, running text+graph only")
		}
	} else {
		fmt.Fprintln(os.Stderr, "Embedder disabled (--no-embedder)")
	}

	queryProviders := cfg.Providers.Query
	if len(queryProviders) == 0 {
		queryProviders = cfg.Providers.Extraction
	}
	chain, err := provider.NewChain(queryProviders)
	if err != nil {
		logger.Error("failed to create query provider chain", "error", err)
		os.Exit(1)
	}

	q := query.New(tmpStore, embedder, chain, "", logger)

	examples := impeval.BuiltinRetrievalExamples()
	fmt.Fprintf(os.Stderr, "Running retrieval eval: %d questions\n", len(examples))

	report, err := impeval.RunRetrieval(ctx, q, examples)
	if err != nil {
		logger.Error("retrieval eval failed", "error", err)
		os.Exit(1)
	}

	switch format {
	case "json":
		if err := impeval.WriteRetrievalJSON(os.Stdout, report); err != nil {
			logger.Error("failed to write JSON report", "error", err)
			os.Exit(1)
		}
	default:
		impeval.WriteRetrievalTable(os.Stdout, report)
	}

	runID := persistRetrievalEval(ctx, logger, cfg, report)

	mainStore := openStore(logger, cfg)
	defer mainStore.Close()

	if saveBaseline && runID != "" {
		if err := mainStore.SetBaseline(ctx, runID, "retrieval"); err != nil {
			logger.Error("failed to set baseline", "error", err)
		} else {
			fmt.Fprintln(os.Stderr, "Baseline saved for retrieval eval")
		}
	}

	if check {
		runRegressionCheck(ctx, logger, mainStore, "retrieval", report.RecallAt10, threshold)
	}
}

func persistExtractionEval(ctx context.Context, logger *slog.Logger, cfg *config.Config, report *impeval.Report, promptPath string) string {
	store := openStore(logger, cfg)
	defer store.Close()

	reportJSON, err := json.Marshal(report)
	if err != nil {
		logger.Warn("failed to marshal eval report for persistence", "error", err)
		return ""
	}

	promptData, _ := os.ReadFile(promptPath) //nolint:gosec // path from config, not user input
	promptHash := fmt.Sprintf("%x", sha256.Sum256(promptData))

	run := &db.EvalRun{
		ID:            db.NewID(),
		EvalType:      "extraction",
		Score:         report.Composite,
		ExamplesCount: report.GoldenCount,
		Report:        string(reportJSON),
		PromptHash:    promptHash,
		GitCommit:     gitCommitShort(),
		CreatedAt:     time.Now().UTC(),
	}
	if err := store.CreateEvalRun(ctx, run); err != nil {
		logger.Warn("failed to persist eval run", "error", err)
		return ""
	}
	fmt.Fprintf(os.Stderr, "Eval run saved (composite=%.4f, id=%s)\n", run.Score, run.ID)
	autoUpdateBaseline(ctx, logger, store, run)
	return run.ID
}

func persistRetrievalEval(ctx context.Context, logger *slog.Logger, cfg *config.Config, report *impeval.RetrievalReport) string {
	store := openStore(logger, cfg)
	defer store.Close()

	reportJSON, err := json.Marshal(report)
	if err != nil {
		logger.Warn("failed to marshal retrieval report for persistence", "error", err)
		return ""
	}

	run := &db.EvalRun{
		ID:            db.NewID(),
		EvalType:      "retrieval",
		Score:         report.RecallAt10,
		Score2:        report.MRR,
		ExamplesCount: report.TotalQuestions,
		Report:        string(reportJSON),
		GitCommit:     gitCommitShort(),
		CreatedAt:     time.Now().UTC(),
	}
	if err := store.CreateEvalRun(ctx, run); err != nil {
		logger.Warn("failed to persist retrieval eval run", "error", err)
		return ""
	}
	fmt.Fprintf(os.Stderr, "Retrieval eval run saved (recall@10=%.4f, mrr=%.4f, id=%s)\n", run.Score, run.Score2, run.ID)
	autoUpdateBaseline(ctx, logger, store, run)
	return run.ID
}

func autoUpdateBaseline(ctx context.Context, logger *slog.Logger, store *db.SQLiteStore, run *db.EvalRun) {
	current, err := store.GetBaselineEvalRun(ctx, run.EvalType)
	if err != nil {
		logger.Warn("failed to check baseline", "error", err)
		return
	}
	if current == nil || run.Score >= current.Score {
		if err := store.SetBaseline(ctx, run.ID, run.EvalType); err != nil {
			logger.Warn("failed to auto-update baseline", "error", err)
			return
		}
		if current == nil {
			fmt.Fprintf(os.Stderr, "Baseline set (first %s eval run)\n", run.EvalType)
		} else {
			fmt.Fprintf(os.Stderr, "Baseline updated (%.4f -> %.4f)\n", current.Score, run.Score)
		}
	}
}

func gitCommitShort() string {
	out, err := exec.Command("git", "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func runEvalHistory(logger *slog.Logger, cfgPath, evalType string, limit int) {
	cfg := loadConfig(logger, cfgPath)
	store := openStore(logger, cfg)
	defer store.Close()

	ctx := context.Background()
	runs, err := store.ListEvalRuns(ctx, evalType, limit)
	if err != nil {
		logger.Error("failed to list eval runs", "error", err)
		os.Exit(1)
	}

	if len(runs) == 0 {
		fmt.Println("No eval runs found.")
		return
	}

	fmt.Printf("%-20s %-12s %-8s %10s %10s %8s %8s %s\n",
		"Date", "Type", "Commit", "Score", "Score2", "N", "Delta", "Baseline")
	fmt.Println(strings.Repeat("-", 90))

	for i, r := range runs {
		commit := r.GitCommit
		if commit == "" {
			commit = "-"
		}
		baseline := ""
		if r.IsBaseline {
			baseline = " *"
		}

		delta := ""
		if i+1 < len(runs) && runs[i+1].EvalType == r.EvalType {
			d := r.Score - runs[i+1].Score
			if d >= 0 {
				delta = fmt.Sprintf("+%.4f", d)
			} else {
				delta = fmt.Sprintf("%.4f", d)
			}
		}

		score2 := ""
		if r.Score2 != 0 {
			score2 = fmt.Sprintf("%.4f", r.Score2)
		}

		fmt.Printf("%-20s %-12s %-8s %10.4f %10s %8d %8s%s\n",
			r.CreatedAt.Format("2006-01-02 15:04:05"),
			r.EvalType, commit, r.Score, score2,
			r.ExamplesCount, delta, baseline)
	}
}

func runRegressionCheck(ctx context.Context, logger *slog.Logger, store *db.SQLiteStore, evalType string, currentScore, threshold float64) {
	baseline, err := store.GetBaselineEvalRun(ctx, evalType)
	if err != nil {
		logger.Error("failed to get baseline", "error", err)
		os.Exit(1)
	}
	if baseline == nil {
		fmt.Fprintf(os.Stderr, "No baseline set for %s -- skipping regression check\n", evalType)
		return
	}

	passed, delta := impeval.CheckRegression(currentScore, baseline.Score, threshold)
	fmt.Fprintf(os.Stderr, "Regression check (%s): current=%.4f baseline=%.4f delta=%+.4f threshold=%.4f",
		evalType, currentScore, baseline.Score, delta, threshold)
	if passed {
		fmt.Fprintln(os.Stderr, " PASSED")
	} else {
		fmt.Fprintln(os.Stderr, " FAILED")
		os.Exit(1)
	}
}

func runOptimizeCmd(logger *slog.Logger, cfgPath string) {
	cfg := loadConfig(logger, cfgPath)
	store := openStore(logger, cfg)
	defer store.Close()

	ctx := context.Background()
	qcfg := cfg.EffectiveQualityConfig()

	chain, err := provider.NewChain(cfg.Providers.Extraction)
	if err != nil {
		logger.Error("failed to create provider chain", "error", err)
		os.Exit(1)
	}

	mutationPrompt, err := os.ReadFile(qcfg.MutationPromptPath)
	if err != nil {
		logger.Error("failed to read mutation prompt", "path", qcfg.MutationPromptPath, "error", err)
		os.Exit(1)
	}

	opt := quality.NewOptimizer(quality.OptimizerConfig{
		Sender:         chain,
		Store:          store,
		QualityCfg:     qcfg,
		PromptPath:     cfg.Prompts.Extraction,
		OptimizedPath:  qcfg.OptimizedPromptPath,
		MutationPrompt: string(mutationPrompt),
		GoldenDir:      qcfg.GoldenDir,
		Types:          cfg.EffectiveTypes(),
		Logger:         logger,
	})

	fmt.Println("Running optimization cycle...")
	result := opt.Optimize(ctx)

	if result.Skipped != "" {
		fmt.Printf("Skipped: %s\n", result.Skipped)
		return
	}
	if result.Error != nil {
		fmt.Fprintf(os.Stderr, "Optimization failed: %v\n", result.Error)
		os.Exit(1)
	}
	if result.Kept {
		fmt.Printf("Prompt improved: %.4f -> %.4f\nSaved to: %s\n",
			result.BaselineScore, result.CandidateScore, qcfg.OptimizedPromptPath)
	} else {
		fmt.Printf("Candidate discarded: %.4f <= baseline %.4f\n",
			result.CandidateScore, result.BaselineScore)
	}
}

func runGC(logger *slog.Logger, cfgPath string) {
	cfg := loadConfig(logger, cfgPath)
	store := openStore(logger, cfg)
	defer store.Close()

	gcDays := cfg.EffectiveGCAfterDays()
	cutoff := time.Now().UTC().Add(-time.Duration(gcDays) * 24 * time.Hour)

	ctx := context.Background()
	deleted, err := store.DeleteExpiredFacts(ctx, cutoff)
	if err != nil {
		logger.Error("gc failed", "error", err)
		os.Exit(1)
	}

	fmt.Printf("GC: deleted %d expired facts (valid_until < %s, gc_after_days=%d)\n",
		deleted, cutoff.Format("2006-01-02"), gcDays)
}
