package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/aegis-alpha/imprint-mace/internal/db"
)

func runLint(logger *slog.Logger, cfgPath string, args []string) {
	format := "table"
	checkFilter := ""
	for _, a := range args {
		switch {
		case strings.HasPrefix(a, "--format="):
			format = strings.TrimPrefix(a, "--format=")
		case strings.HasPrefix(a, "--check="):
			checkFilter = strings.TrimPrefix(a, "--check=")
		}
	}
	if format != "table" && format != "json" {
		fmt.Fprintf(os.Stderr, "lint: unknown --format=%q (use table or json)\n", format)
		os.Exit(1)
	}

	checks, err := db.NormalizeLintChecks(checkFilter, db.AllLintChecks)
	if err != nil {
		fmt.Fprintf(os.Stderr, "lint: %v\n", err)
		os.Exit(1)
	}

	cfg := loadConfig(logger, cfgPath)
	store := openStore(logger, cfg)
	defer store.Close()

	ctx := context.Background()
	stats, err := store.Stats(ctx)
	if err != nil {
		logger.Error("lint: stats failed", "error", err)
		os.Exit(1)
	}

	type checkOut struct {
		name     string
		severity string
		count    int
		lines    []string
		findings any
	}

	var outs []checkOut
	var nErr, nWarn, nInfo int

	for _, checkName := range checks {
		switch checkName {
		case "chains":
			rows, err := store.LintBrokenSupersedeChains(ctx)
			if err != nil {
				logger.Error("lint: broken chains", "error", err)
				os.Exit(1)
			}
			if len(rows) > 0 {
				var lines []string
				var fj []map[string]string
				for _, r := range rows {
					subj := r.Subject
					if subj == "" {
						subj = "(no subject)"
					}
					lines = append(lines, fmt.Sprintf("  - fact %s (subject: %q) -> missing %s", r.ID, subj, r.SupersededBy))
					fj = append(fj, map[string]string{
						"fact_id": r.ID, "subject": r.Subject, "superseded_by": r.SupersededBy,
					})
				}
				outs = append(outs, checkOut{name: "broken_supersede_chains", severity: "error", count: len(rows), lines: lines, findings: fj})
				nErr++
			}

		case "stale":
			rows, err := store.LintStaleFacts(ctx)
			if err != nil {
				logger.Error("lint: stale facts", "error", err)
				os.Exit(1)
			}
			if len(rows) > 0 {
				oldest := rows[0].ValidUntil
				for _, r := range rows[1:] {
					if r.ValidUntil < oldest {
						oldest = r.ValidUntil
					}
				}
				line := fmt.Sprintf("  - %d facts expired but not superseded (oldest: %s)", len(rows), oldest)
				var fj []map[string]string
				for _, r := range rows {
					fj = append(fj, map[string]string{
						"id": r.ID, "subject": r.Subject, "valid_until": r.ValidUntil,
					})
				}
				outs = append(outs, checkOut{
					name: "stale_facts", severity: "warning", count: len(rows),
					lines: []string{line}, findings: fj,
				})
				nWarn++
			}

		case "dedup":
			ex, err := store.LintEntityDedupExact(ctx)
			if err != nil {
				logger.Error("lint: dedup exact", "error", err)
				os.Exit(1)
			}
			sub, err := store.LintEntityDedupSubstring(ctx)
			if err != nil {
				logger.Error("lint: dedup substring", "error", err)
				os.Exit(1)
			}
			pairs := append(append([]db.LintEntityDedupPair(nil), ex...), sub...)
			if len(pairs) > 0 {
				var lines []string
				var fj []map[string]string
				for _, p := range pairs {
					kind := "exact case/trim match"
					if p.Kind == "substring" {
						kind = "substring match"
					}
					lines = append(lines, fmt.Sprintf("  - %q and %q (%s)", p.Name1, p.Name2, kind))
					fj = append(fj, map[string]string{
						"entity_id_1": p.ID1, "name_1": p.Name1,
						"entity_id_2": p.ID2, "name_2": p.Name2,
						"kind": p.Kind,
					})
				}
				outs = append(outs, checkOut{name: "entity_dedup_candidates", severity: "warning", count: len(pairs), lines: lines, findings: fj})
				nWarn++
			}

		case "embeddings":
			rows, err := store.LintFactsMissingEmbeddingsByType(ctx)
			if err != nil {
				logger.Error("lint: embeddings", "error", err)
				os.Exit(1)
			}
			missing := 0
			for _, r := range rows {
				missing += r.Count
			}
			if missing > 0 {
				total := stats.Facts
				pct := 0.0
				if total > 0 {
					pct = float64(missing) / float64(total) * 100
				}
				sev := "info"
				if total > 0 && float64(missing)/float64(total) > 0.10 {
					sev = "warning"
				}
				line := fmt.Sprintf("  - %d facts without embeddings (%.0f%% of %d total)", missing, pct, total)
				var fj []map[string]any
				for _, r := range rows {
					fj = append(fj, map[string]any{"fact_type": r.FactType, "count": r.Count})
				}
				outs = append(outs, checkOut{
					name: "facts_without_embeddings", severity: sev, count: missing,
					lines: []string{line}, findings: fj,
				})
				if sev == "warning" {
					nWarn++
				} else {
					nInfo++
				}
			}

		case "orphans":
			rows, err := store.LintOrphanEntities(ctx)
			if err != nil {
				logger.Error("lint: orphans", "error", err)
				os.Exit(1)
			}
			if len(rows) > 0 {
				var lines []string
				var fj []map[string]string
				for _, r := range rows {
					lines = append(lines, fmt.Sprintf("  - %s %q (%s)", r.ID, r.Name, r.EntityType))
					fj = append(fj, map[string]string{"id": r.ID, "name": r.Name, "entity_type": r.EntityType})
				}
				outs = append(outs, checkOut{name: "orphan_entities", severity: "info", count: len(rows), lines: lines, findings: fj})
				nInfo++
			}

		case "sources":
			base := strings.TrimSpace(cfg.Watcher.Path)
			if base != "" {
				if !filepath.IsAbs(base) {
					if wd, err := os.Getwd(); err == nil {
						base = filepath.Join(wd, base)
					}
				}
				base = filepath.Clean(base)
				paths, err := store.LintDistinctNonEmptySourceFiles(ctx)
				if err != nil {
					logger.Error("lint: source files", "error", err)
					os.Exit(1)
				}
				var missing []string
				var unreadable []struct {
					sourceFile string
					resolved   string
					errMsg     string
				}
				for _, p := range paths {
					checkPath := p
					if !filepath.IsAbs(p) {
						checkPath = filepath.Join(base, p)
					}
					checkPath = filepath.Clean(checkPath)
					_, err := os.Stat(checkPath)
					if err != nil {
						if os.IsNotExist(err) {
							missing = append(missing, p)
						} else {
							unreadable = append(unreadable, struct {
								sourceFile string
								resolved   string
								errMsg     string
							}{p, checkPath, err.Error()})
						}
					}
				}
				if len(missing) > 0 {
					sort.Strings(missing)
					var lines []string
					var fj []map[string]string
					for _, p := range missing {
						lines = append(lines, fmt.Sprintf("  - %s", p))
						fj = append(fj, map[string]string{"source_file": p})
					}
					outs = append(outs, checkOut{name: "missing_source_files", severity: "info", count: len(missing), lines: lines, findings: fj})
					nInfo++
				}
				if len(unreadable) > 0 {
					sort.Slice(unreadable, func(i, j int) bool {
						return unreadable[i].sourceFile < unreadable[j].sourceFile
					})
					var lines []string
					var fj []map[string]string
					for _, u := range unreadable {
						lines = append(lines, fmt.Sprintf("  - %s -> %s (%s)", u.sourceFile, u.resolved, u.errMsg))
						fj = append(fj, map[string]string{
							"source_file": u.sourceFile,
							"resolved":    u.resolved,
							"error":       u.errMsg,
						})
					}
					outs = append(outs, checkOut{
						name: "source_file_unreadable", severity: "warning", count: len(unreadable),
						lines: lines, findings: fj,
					})
					nWarn++
				}
			}

		case "consolidation":
			n, err := store.LintUnconsolidatedActiveFactsCount(ctx)
			if err != nil {
				logger.Error("lint: consolidation", "error", err)
				os.Exit(1)
			}
			if n > 0 {
				total := stats.Facts
				pct := 0.0
				if total > 0 {
					pct = float64(n) / float64(total) * 100
				}
				sev := "info"
				if total > 0 && float64(n)/float64(total) >= 0.50 {
					sev = "warning"
				}
				line := fmt.Sprintf("  - %d facts never consolidated (%.0f%% of %d total)", n, pct, total)
				outs = append(outs, checkOut{
					name: "unconsolidated_facts", severity: sev, count: n,
					lines: []string{line},
					findings: []map[string]any{
						{"unconsolidated": n, "total_facts": total, "percent": pct},
					},
				})
				if sev == "warning" {
					nWarn++
				} else {
					nInfo++
				}
			}
		}
	}

	if format == "json" {
		type jcheck struct {
			Name     string `json:"name"`
			Severity string `json:"severity"`
			Count    int    `json:"count"`
			Findings any    `json:"findings"`
		}
		type jsummary struct {
			Errors        int `json:"errors"`
			Warnings      int `json:"warnings"`
			Info          int `json:"info"`
			TotalFacts    int `json:"total_facts"`
			TotalEntities int `json:"total_entities"`
		}
		jc := make([]jcheck, 0, len(outs))
		for _, o := range outs {
			jc = append(jc, jcheck{Name: o.name, Severity: o.severity, Count: o.count, Findings: o.findings})
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(map[string]any{
			"checks": jc,
			"summary": jsummary{
				Errors: nErr, Warnings: nWarn, Info: nInfo,
				TotalFacts: stats.Facts, TotalEntities: stats.Entities,
			},
		}); err != nil {
			logger.Error("lint: json encode", "error", err)
			os.Exit(1)
		}
		if nErr > 0 {
			os.Exit(1)
		}
		return
	}

	fmt.Println("KB Integrity Report")
	fmt.Println("===================")
	fmt.Println()
	if len(outs) == 0 {
		fmt.Println("No issues found.")
		fmt.Println()
		fmt.Printf("Summary: %d errors, %d warnings, %d info\n", nErr, nWarn, nInfo)
		if nErr > 0 {
			os.Exit(1)
		}
		return
	}

	for _, o := range outs {
		tag := strings.ToUpper(o.severity)
		title := strings.ReplaceAll(o.name, "_", " ")
		fmt.Printf("[%s] %s: %d\n", tag, title, o.count)
		for _, ln := range o.lines {
			fmt.Println(ln)
		}
		action := lintAction(o.name)
		if action != "" {
			fmt.Printf("  Action: %s\n", action)
		}
		fmt.Println()
	}
	fmt.Printf("Summary: %d errors, %d warnings, %d info\n", nErr, nWarn, nInfo)
	if nErr > 0 {
		os.Exit(1)
	}
}

func lintAction(checkName string) string {
	switch checkName {
	case "broken_supersede_chains":
		return "Supersede chain broken -- target fact was deleted. Repair DB or supersede manually."
	case "stale_facts":
		return "Past valid_until but not superseded. `imprint gc` deletes only when valid_until is older than gc_after_days; supersede manually or wait until that window, or change retention in config."
	case "entity_dedup_candidates":
		return "Possible duplicate entities -- consider merging."
	case "facts_without_embeddings":
		return "Run `imprint embed-backfill`."
	case "orphan_entities":
		return "Consider deleting orphan entities."
	case "missing_source_files":
		return "Source transcript file missing -- fact provenance may be lost."
	case "source_file_unreadable":
		return "Could not read the resolved path (permissions, I/O). Fix access or paths; provenance could not be verified."
	case "unconsolidated_facts":
		return "Run `imprint consolidate`."
	default:
		return ""
	}
}
