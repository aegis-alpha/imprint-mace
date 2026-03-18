package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/aegis-alpha/imprint-mace/internal/config"
	impctx "github.com/aegis-alpha/imprint-mace/internal/context"
	"github.com/aegis-alpha/imprint-mace/internal/update"
	"github.com/aegis-alpha/imprint-mace/internal/consolidation"
	"github.com/aegis-alpha/imprint-mace/internal/db"
	"github.com/aegis-alpha/imprint-mace/internal/extraction"
	"github.com/aegis-alpha/imprint-mace/internal/imprint"
	"github.com/aegis-alpha/imprint-mace/internal/ingest"
	"github.com/aegis-alpha/imprint-mace/internal/api"
	impmcp "github.com/aegis-alpha/imprint-mace/internal/mcp"
	"github.com/aegis-alpha/imprint-mace/internal/model"
	"github.com/aegis-alpha/imprint-mace/internal/provider"
	"github.com/aegis-alpha/imprint-mace/internal/query"
	"github.com/aegis-alpha/imprint-mace/internal/transcript"
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
	extractor, err := extraction.New(chain, cfg.Prompts.Extraction, types, logger)
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
