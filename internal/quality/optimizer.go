package quality

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/aegis-alpha/imprint-mace/internal/config"
	"github.com/aegis-alpha/imprint-mace/internal/db"
	"github.com/aegis-alpha/imprint-mace/internal/eval"
	"github.com/aegis-alpha/imprint-mace/internal/extraction"
	"github.com/aegis-alpha/imprint-mace/internal/provider"
)

const (
	SignalOptimizationAttempt = "optimization_attempt"

	defaultMinOptimizeInterval = time.Hour
	defaultMaxConsecFailures   = 3
	failurePauseDuration       = 24 * time.Hour
)

// LoopOptimizer implements the Karpathy loop: mutate prompt -> eval -> keep/discard.
type LoopOptimizer struct {
	sender           extraction.Sender
	store            db.Store
	cfg              config.QualityConfig
	promptPath       string
	optimizedPath    string
	mutationPrompt   string
	goldenDir        string
	extractionTypes  config.TypesConfig
	logger           *slog.Logger

	mu               sync.Mutex
	lastAttempt      time.Time
	consecFailures   int
	pauseUntil       time.Time
}

// OptimizerConfig holds paths and settings for the optimizer.
type OptimizerConfig struct {
	Sender          extraction.Sender
	Store           db.Store
	QualityCfg      config.QualityConfig
	PromptPath      string
	OptimizedPath   string
	MutationPrompt  string
	GoldenDir       string
	Types           config.TypesConfig
	Logger          *slog.Logger
}

// NewOptimizer creates a LoopOptimizer.
func NewOptimizer(cfg OptimizerConfig) *LoopOptimizer {
	return &LoopOptimizer{
		sender:          cfg.Sender,
		store:           cfg.Store,
		cfg:             cfg.QualityCfg,
		promptPath:      cfg.PromptPath,
		optimizedPath:   cfg.OptimizedPath,
		mutationPrompt:  cfg.MutationPrompt,
		goldenDir:       cfg.GoldenDir,
		extractionTypes: cfg.Types,
		logger:          cfg.Logger,
	}
}

// ShouldOptimize checks whether any signal exceeds warning thresholds.
func (o *LoopOptimizer) ShouldOptimize(signals []db.QualitySignal) bool {
	temporalTypes := map[string]bool{"context": true, "event": true}
	for _, s := range signals {
		if s.SignalType == SignalSupersedeRate && !temporalTypes[s.Category] && s.Value > o.cfg.SupersedeRateWarning {
			return true
		}
		if s.SignalType == SignalConfidenceCalibration && s.Value > 0.15 {
			return true
		}
		if s.SignalType == SignalConfidenceCitationCalibration && s.Value > 0.15 {
			return true
		}
	}
	return false
}

// OptimizeResult reports what happened during one optimization cycle.
type OptimizeResult struct {
	Attempted     bool
	Skipped       string
	BaselineScore float64
	CandidateScore float64
	Kept          bool
	Error         error
}

// Optimize runs one optimization cycle: mutate -> eval -> gate.
func (o *LoopOptimizer) Optimize(ctx context.Context) *OptimizeResult {
	o.mu.Lock()
	now := time.Now()
	if now.Before(o.pauseUntil) {
		o.mu.Unlock()
		return &OptimizeResult{Skipped: fmt.Sprintf("paused until %s after %d consecutive failures", o.pauseUntil.Format(time.RFC3339), defaultMaxConsecFailures)}
	}
	if now.Sub(o.lastAttempt) < defaultMinOptimizeInterval {
		o.mu.Unlock()
		return &OptimizeResult{Skipped: "rate limited (1 attempt per hour)"}
	}
	o.lastAttempt = now
	o.mu.Unlock()

	result := &OptimizeResult{Attempted: true}

	currentPrompt, err := o.readCurrentPrompt()
	if err != nil {
		result.Error = fmt.Errorf("read current prompt: %w", err)
		o.recordFailure()
		return result
	}

	signals, err := o.store.ListQualitySignals(ctx, "", 50)
	if err != nil {
		result.Error = fmt.Errorf("list signals: %w", err)
		o.recordFailure()
		return result
	}

	candidate, err := o.mutate(ctx, currentPrompt, signals)
	if err != nil {
		result.Error = fmt.Errorf("mutate: %w", err)
		o.recordFailure()
		return result
	}

	examples, err := o.loadGolden()
	if err != nil {
		result.Error = fmt.Errorf("load golden: %w", err)
		o.recordFailure()
		return result
	}

	baselineScore, err := o.evaluate(ctx, currentPrompt, examples)
	if err != nil {
		result.Error = fmt.Errorf("baseline eval: %w", err)
		o.recordFailure()
		return result
	}
	result.BaselineScore = baselineScore

	candidateScore, err := o.evaluate(ctx, candidate, examples)
	if err != nil {
		result.Error = fmt.Errorf("candidate eval: %w", err)
		o.recordFailure()
		return result
	}
	result.CandidateScore = candidateScore

	if candidateScore > baselineScore {
		if err := os.WriteFile(o.optimizedPath, []byte(candidate), 0644); err != nil {
			result.Error = fmt.Errorf("write optimized prompt: %w", err)
			o.recordFailure()
			return result
		}
		result.Kept = true
		o.logger.Info("optimization: candidate kept",
			"baseline", baselineScore, "candidate", candidateScore,
			"path", o.optimizedPath)
		o.resetFailures()
		o.execOnKept(result)
	} else {
		result.Kept = false
		o.logger.Info("optimization: candidate discarded",
			"baseline", baselineScore, "candidate", candidateScore)
		o.recordFailure()
	}

	o.recordAttempt(ctx, result)
	return result
}

func (o *LoopOptimizer) readCurrentPrompt() (string, error) {
	if o.optimizedPath != "" {
		if data, err := os.ReadFile(o.optimizedPath); err == nil {
			return string(data), nil
		}
	}
	data, err := os.ReadFile(o.promptPath)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (o *LoopOptimizer) mutate(ctx context.Context, currentPrompt string, signals []db.QualitySignal) (string, error) {
	signalSummary := formatSignals(signals)

	userPrompt := fmt.Sprintf("<current_prompt>\n%s\n</current_prompt>\n\n<signals>\n%s\n</signals>",
		currentPrompt, signalSummary)

	resp, err := o.sender.Send(ctx, provider.Request{
		SystemPrompt: o.mutationPrompt,
		UserPrompt:   userPrompt,
		MaxTokens:    8192,
	})
	if err != nil {
		return "", err
	}

	candidate := strings.TrimSpace(resp.Content)
	if len(candidate) < 100 {
		return "", fmt.Errorf("candidate prompt too short (%d chars)", len(candidate))
	}

	return candidate, nil
}

func (o *LoopOptimizer) loadGolden() ([]eval.GoldenExample, error) {
	if o.goldenDir != "" {
		return eval.LoadGoldenDir(o.goldenDir)
	}
	dir, err := os.MkdirTemp("", "imprint-golden-*")
	if err != nil {
		return nil, err
	}
	if _, err := eval.Generate(dir); err != nil {
		return nil, err
	}
	return eval.LoadGoldenDir(dir)
}

func (o *LoopOptimizer) evaluate(ctx context.Context, promptText string, examples []eval.GoldenExample) (float64, error) {
	tmpDir, err := os.MkdirTemp("", "imprint-eval-prompt-*")
	if err != nil {
		return 0, err
	}
	tmpPath := tmpDir + "/prompt.md"
	if err := os.WriteFile(tmpPath, []byte(promptText), 0644); err != nil {
		return 0, err
	}

	extractor, err := extraction.New(o.sender, tmpPath, o.extractionTypes, o.logger)
	if err != nil {
		return 0, fmt.Errorf("create extractor: %w", err)
	}

	report, err := eval.Run(ctx, extractor, examples, eval.DefaultConfig())
	if err != nil {
		return 0, err
	}

	return report.Composite, nil
}

func (o *LoopOptimizer) recordFailure() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.consecFailures++
	if o.consecFailures >= defaultMaxConsecFailures {
		o.pauseUntil = time.Now().Add(failurePauseDuration)
		o.logger.Warn("optimization paused after consecutive failures",
			"failures", o.consecFailures, "pause_until", o.pauseUntil)
	}
}

func (o *LoopOptimizer) resetFailures() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.consecFailures = 0
	o.pauseUntil = time.Time{}
}

func (o *LoopOptimizer) execOnKept(result *OptimizeResult) {
	cmd := o.cfg.OnKeptCommand
	if cmd == "" {
		return
	}
	go func() {
		args := strings.Fields(cmd)
		c := exec.CommandContext(context.Background(), args[0], args[1:]...) //nolint:gosec // configurable by operator
		c.Env = append(os.Environ(),
			fmt.Sprintf("IMPRINT_KEPT_SCORE=%.4f", result.CandidateScore),
			fmt.Sprintf("IMPRINT_BASELINE_SCORE=%.4f", result.BaselineScore),
		)
		if out, err := c.CombinedOutput(); err != nil {
			o.logger.Warn("on_kept_command failed", "cmd", cmd, "error", err, "output", string(out))
		} else {
			o.logger.Info("on_kept_command completed", "cmd", cmd)
		}
	}()
}

func (o *LoopOptimizer) recordAttempt(ctx context.Context, result *OptimizeResult) {
	errStr := ""
	if result.Error != nil {
		errStr = result.Error.Error()
	}
	details := fmt.Sprintf(`{"baseline":%.4f,"candidate":%.4f,"kept":%t,"error":%q}`,
		result.BaselineScore, result.CandidateScore, result.Kept, errStr)

	sig := &db.QualitySignal{
		ID:        db.NewID(),
		SignalType: SignalOptimizationAttempt,
		Category:  "global",
		Value:     result.CandidateScore,
		Details:   details,
		CreatedAt: time.Now(),
	}
	if err := o.store.CreateQualitySignal(ctx, sig); err != nil {
		o.logger.Warn("failed to record optimization attempt", "error", err)
	}
}

func formatSignals(signals []db.QualitySignal) string {
	type key struct{ signalType, category string }
	seen := map[key]bool{}
	var lines []string
	for _, s := range signals {
		k := key{s.SignalType, s.Category}
		if seen[k] {
			continue
		}
		seen[k] = true
		lines = append(lines, fmt.Sprintf("- %s [%s]: %.4f", s.SignalType, s.Category, s.Value))
	}
	return strings.Join(lines, "\n")
}
