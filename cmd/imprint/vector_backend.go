package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/aegis-alpha/imprint-mace/internal/config"
	"github.com/aegis-alpha/imprint-mace/internal/db"
	"github.com/aegis-alpha/imprint-mace/internal/vecindex"
)

type vectorAccessMode int

const (
	vectorAccessReadOnly vectorAccessMode = iota
	vectorAccessWriteRequired
)

type vectorWriteSelfTestReport struct {
	Backend string `json:"backend"`
	Check   string `json:"check"`
	Detail  string `json:"detail"`
}

func configuredVectorMode(cfg *config.Config) vecindex.Mode {
	switch cfg.EffectiveVectorConfig().Mode {
	case "read-only":
		return vecindex.ModeReadOnly
	case "disabled":
		return vecindex.ModeDisabled
	default:
		return vecindex.ModeReadWrite
	}
}

func configureVectorBackend(logger *slog.Logger, cfg *config.Config, store *db.SQLiteStore, access vectorAccessMode) {
	mode := configuredVectorMode(cfg)
	dims := cfg.EffectiveEmbeddingDims()

	if mode == vecindex.ModeDisabled || dims <= 0 {
		store.SetVectorIndex(nil)
		store.SetVectorCapability(vecindex.DisabledCapability("vector backend disabled by configuration"))
		return
	}

	if err := store.AttachVectorIndex(dims); err != nil {
		logger.Error("failed to open vector backend", "backend", "usearch", "error", err)
		os.Exit(1)
	}

	capability := store.VectorCapability()
	capability.Backend = "usearch"
	capability.Mode = mode
	capability.ReadAvailable = true
	capability.WriteSafe = false
	capability.Status = vecindex.HealthReadOnly
	capability.Detail = "vector write path not enabled for this process"

	switch mode {
	case vecindex.ModeReadOnly:
		capability.Status = vecindex.HealthReadOnly
		capability.Detail = "operator selected explicit read-only mode"
	case vecindex.ModeReadWrite:
		if access == vectorAccessWriteRequired {
			report, err := runVectorWriteSelfTestSubprocess("usearch", dims)
			if err != nil {
				logger.Error("vector backend write-path self-test failed",
					"backend", "usearch",
					"self_test", "fresh_add_repeated_add_save_load_search",
					"read_path_note", "vector search/read path may still be alive, but write path is unsafe",
					"fatal_reason", "configured mode requires vector writes and Imprint cannot guarantee memory writes",
					"detail", err.Error(),
				)
				os.Exit(1)
			}
			capability.Status = vecindex.HealthHealthy
			capability.WriteSafe = true
			capability.LastSelfTest = report.Check
			capability.Detail = report.Detail
		}
	}

	store.SetVectorCapability(capability)
}

func runVectorWriteSelfTestSubprocess(backend string, dims int) (*vectorWriteSelfTestReport, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve executable: %w", err)
	}
	cmd := exec.Command(exe,
		"vector-self-test",
		"--backend="+backend,
		"--dims="+strconv.Itoa(dims),
	)
	cmd.Env = os.Environ()
	output, err := cmd.CombinedOutput()
	if err != nil {
		trimmed := strings.TrimSpace(string(output))
		if trimmed == "" {
			trimmed = err.Error()
		}
		return nil, fmt.Errorf("%w: %s", err, trimmed)
	}

	var report vectorWriteSelfTestReport
	if err := json.Unmarshal(output, &report); err != nil {
		return nil, fmt.Errorf("parse self-test output: %w", err)
	}
	return &report, nil
}

func runVectorSelfTestCommand(logger *slog.Logger, args []string) {
	backend := "usearch"
	dims := 1536
	for _, arg := range args {
		switch {
		case strings.HasPrefix(arg, "--backend="):
			backend = strings.TrimSpace(strings.ToLower(strings.TrimPrefix(arg, "--backend=")))
		case strings.HasPrefix(arg, "--dims="):
			if parsed, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(arg, "--dims="))); err == nil {
				dims = parsed
			}
		}
	}

	var (
		report *vectorWriteSelfTestReport
		err    error
	)
	switch backend {
	case "usearch":
		report, err = runUSearchWriteSelfTest(dims)
	default:
		err = fmt.Errorf("unsupported backend %q", backend)
	}

	if err != nil {
		writeVectorSelfTestOutput(vectorWriteSelfTestReport{
			Backend: backend,
			Check:   "fresh_add_repeated_add_save_load_search",
			Detail:  err.Error(),
		})
		logger.Error("vector self-test failed", "backend", backend, "error", err)
		os.Exit(1)
	}
	writeVectorSelfTestOutput(*report)
}

func writeVectorSelfTestOutput(report vectorWriteSelfTestReport) {
	data, err := json.Marshal(report)
	if err != nil {
		fmt.Fprintln(os.Stdout, `{"backend":"unknown","check":"marshal","detail":"failed to marshal self-test report"}`)
		return
	}
	fmt.Fprintln(os.Stdout, string(data))
}

func runUSearchWriteSelfTest(dims int) (*vectorWriteSelfTestReport, error) {
	if dims <= 0 {
		return nil, errors.New("dimensions must be positive")
	}
	tempDir, err := os.MkdirTemp("", "imprint-usearch-self-test-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tempDir)

	cachePath := filepath.Join(tempDir, "selftest.vecindex")
	idx, err := vecindex.OpenVectorIndex(cachePath, dims, func() (map[string][]float32, error) {
		return nil, nil
	})
	if err != nil {
		return nil, fmt.Errorf("open fresh index: %w", err)
	}

	first := makeSelfTestVector(dims, 0)
	second := makeSelfTestVector(dims, 1)
	if err := idx.Add("fact:selftest-1", first); err != nil {
		return nil, fmt.Errorf("fresh first add failed: %w", err)
	}
	if err := idx.Add("fact:selftest-2", second); err != nil {
		return nil, fmt.Errorf("repeated add failed: %w", err)
	}
	if _, err := idx.SearchWithPrefix(first, 2, "fact:"); err != nil {
		return nil, fmt.Errorf("search after write failed: %w", err)
	}
	if err := idx.Close(); err != nil {
		return nil, fmt.Errorf("close after save failed: %w", err)
	}

	reloaded, err := vecindex.OpenVectorIndex(cachePath, dims, func() (map[string][]float32, error) {
		return nil, errors.New("unexpected rebuild during self-test reload")
	})
	if err != nil {
		return nil, fmt.Errorf("reload saved index failed: %w", err)
	}
	defer reloaded.Close() //nolint:errcheck

	hits, err := reloaded.SearchWithPrefix(first, 2, "fact:")
	if err != nil {
		return nil, fmt.Errorf("search after reload failed: %w", err)
	}
	if len(hits) == 0 {
		return nil, errors.New("search after reload returned zero hits")
	}

	return &vectorWriteSelfTestReport{
		Backend: "usearch",
		Check:   "fresh_add_repeated_add_save_load_search",
		Detail:  "USearch fresh-index write path passed first add, repeated add, save/load, and search checks",
	}, nil
}

func makeSelfTestVector(dims int, hot int) []float32 {
	vec := make([]float32, dims)
	if dims == 0 {
		return vec
	}
	vec[hot%dims] = 1
	return vec
}
