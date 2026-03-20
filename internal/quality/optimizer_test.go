package quality

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/aegis-alpha/imprint-mace/internal/config"
	"github.com/aegis-alpha/imprint-mace/internal/db"
	"github.com/aegis-alpha/imprint-mace/internal/eval"
	"github.com/aegis-alpha/imprint-mace/internal/provider"
)

type mockSender struct {
	response *provider.Response
	err      error
	calls    int
}

func (m *mockSender) Send(_ context.Context, req provider.Request) (*provider.Response, error) {
	m.calls++
	return m.response, m.err
}

func testQualityConfig() config.QualityConfig {
	enabled := true
	return config.QualityConfig{
		Enabled:              &enabled,
		CollectionThreshold:  50,
		SupersedeRateWarning: 0.30,
		WindowDays:           30,
		DecayHalfLifeDays:    14,
	}
}

func TestShouldOptimize_HighSupersedeRate(t *testing.T) {
	opt := &LoopOptimizer{cfg: testQualityConfig()}
	signals := []db.QualitySignal{
		{SignalType: SignalSupersedeRate, Category: "decision", Value: 0.45},
	}
	if !opt.ShouldOptimize(signals) {
		t.Error("expected ShouldOptimize=true for high supersede rate on non-temporal type")
	}
}

func TestShouldOptimize_TemporalTypeIgnored(t *testing.T) {
	opt := &LoopOptimizer{cfg: testQualityConfig()}
	signals := []db.QualitySignal{
		{SignalType: SignalSupersedeRate, Category: "context", Value: 0.80},
	}
	if opt.ShouldOptimize(signals) {
		t.Error("expected ShouldOptimize=false for temporal type (context)")
	}
}

func TestShouldOptimize_LowSignals(t *testing.T) {
	opt := &LoopOptimizer{cfg: testQualityConfig()}
	signals := []db.QualitySignal{
		{SignalType: SignalSupersedeRate, Category: "decision", Value: 0.10},
		{SignalType: SignalConfidenceCalibration, Category: "global", Value: 0.05},
	}
	if opt.ShouldOptimize(signals) {
		t.Error("expected ShouldOptimize=false for low signals")
	}
}

func TestShouldOptimize_HighECE(t *testing.T) {
	opt := &LoopOptimizer{cfg: testQualityConfig()}
	signals := []db.QualitySignal{
		{SignalType: SignalConfidenceCalibration, Category: "global", Value: 0.25},
	}
	if !opt.ShouldOptimize(signals) {
		t.Error("expected ShouldOptimize=true for high ECE")
	}
}

func TestOptimize_RateLimit(t *testing.T) {
	opt := &LoopOptimizer{cfg: testQualityConfig(), logger: slog.Default()}

	store, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()
	opt.store = store

	promptDir := t.TempDir()
	promptPath := filepath.Join(promptDir, "prompt.md")
	os.WriteFile(promptPath, []byte("Extract facts. Return JSON.\n## Fact Types ({{len .FactTypes}})\n"), 0644)
	opt.promptPath = promptPath

	goldenDir := t.TempDir()
	eval.Generate(goldenDir)
	opt.goldenDir = goldenDir
	opt.extractionTypes = config.DefaultTypes()

	sender := &mockSender{
		response: &provider.Response{
			Content:      "short",
			ProviderName: "mock",
			Model:        "test",
		},
	}
	opt.sender = sender

	r1 := opt.Optimize(context.Background())
	if !r1.Attempted {
		t.Fatal("first attempt should not be skipped")
	}

	r2 := opt.Optimize(context.Background())
	if r2.Skipped == "" {
		t.Error("second attempt should be rate-limited")
	}
}

func TestOptimize_CandidateTooShort(t *testing.T) {
	store, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer store.Close()

	promptDir := t.TempDir()
	promptPath := filepath.Join(promptDir, "prompt.md")
	os.WriteFile(promptPath, []byte("Extract facts. Return JSON.\n## Fact Types ({{len .FactTypes}})\n"), 0644)

	sender := &mockSender{
		response: &provider.Response{
			Content:      "too short",
			ProviderName: "mock",
			Model:        "test",
		},
	}

	opt := NewOptimizer(OptimizerConfig{
		Sender:         sender,
		Store:          store,
		QualityCfg:     testQualityConfig(),
		PromptPath:     promptPath,
		OptimizedPath:  filepath.Join(promptDir, "optimized.md"),
		MutationPrompt: "improve this prompt",
		GoldenDir:      "",
		Types:          config.DefaultTypes(),
		Logger:         slog.Default(),
	})

	result := opt.Optimize(context.Background())
	if result.Error == nil {
		t.Error("expected error for too-short candidate")
	}
}

func TestOptimize_ConsecutiveFailuresPause(t *testing.T) {
	opt := &LoopOptimizer{cfg: testQualityConfig(), logger: slog.Default()}

	for i := 0; i < defaultMaxConsecFailures; i++ {
		opt.recordFailure()
	}

	if opt.pauseUntil.IsZero() {
		t.Error("expected pauseUntil to be set after max consecutive failures")
	}

	opt.resetFailures()
	if !opt.pauseUntil.IsZero() {
		t.Error("expected pauseUntil to be reset")
	}
	if opt.consecFailures != 0 {
		t.Errorf("expected consecFailures=0, got %d", opt.consecFailures)
	}
}

func TestFormatSignals(t *testing.T) {
	signals := []db.QualitySignal{
		{SignalType: "supersede_rate", Category: "decision", Value: 0.35},
		{SignalType: "supersede_rate", Category: "decision", Value: 0.30},
		{SignalType: "citation_rate", Category: "rule", Value: 0.10},
	}
	result := formatSignals(signals)
	if result == "" {
		t.Fatal("expected non-empty result")
	}
	lines := len(result) - len(result[:0])
	_ = lines
	if !contains(result, "supersede_rate [decision]: 0.3500") {
		t.Errorf("expected supersede_rate line, got: %s", result)
	}
	if !contains(result, "citation_rate [rule]: 0.1000") {
		t.Errorf("expected citation_rate line, got: %s", result)
	}
	if countOccurrences(result, "supersede_rate [decision]") != 1 {
		t.Error("expected dedup of same signal_type+category")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsStr(s, sub))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func countOccurrences(s, sub string) int {
	count := 0
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			count++
		}
	}
	return count
}
