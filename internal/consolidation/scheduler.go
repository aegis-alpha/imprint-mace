package consolidation

import (
	"context"
	"log/slog"
	"time"

	"github.com/aegis-alpha/imprint-mace/internal/db"
)

// Runner can execute a consolidation pass. Consolidator implements this.
type Runner interface {
	Consolidate(ctx context.Context, limit int) (*ConsolidateResult, error)
}

// Scheduler runs consolidation at a configurable interval in a
// background loop. It checks the min_facts threshold before each
// run and shuts down gracefully via context cancellation.
type Scheduler struct {
	runner   Runner
	store    db.Store
	interval time.Duration
	minFacts int
	maxGroup int
	logger   *slog.Logger
}

// NewScheduler creates a Scheduler.
func NewScheduler(runner Runner, store db.Store, interval time.Duration, minFacts, maxGroup int, logger *slog.Logger) *Scheduler {
	return &Scheduler{
		runner:   runner,
		store:    store,
		interval: interval,
		minFacts: minFacts,
		maxGroup: maxGroup,
		logger:   logger,
	}
}

// Run blocks until ctx is cancelled, running consolidation on each tick.
func (s *Scheduler) Run(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	s.logger.Info("consolidation scheduler started",
		"interval", s.interval.String(),
		"min_facts", s.minFacts,
	)

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("consolidation scheduler stopped")
			return
		case <-ticker.C:
			s.tick(ctx)
		}
	}
}

func (s *Scheduler) tick(ctx context.Context) {
	facts, err := s.store.ListUnconsolidatedFacts(ctx, 0)
	if err != nil {
		s.logger.Error("scheduler: failed to count unconsolidated facts", "error", err)
		return
	}

	if len(facts) < s.minFacts {
		s.logger.Debug("scheduler: below threshold",
			"unconsolidated", len(facts),
			"min_facts", s.minFacts,
		)
		return
	}

	s.logger.Info("scheduler: running consolidation",
		"unconsolidated", len(facts),
	)

	result, err := s.runner.Consolidate(ctx, s.maxGroup)
	if err != nil {
		s.logger.Error("scheduler: consolidation failed", "error", err)
		return
	}
	if result != nil {
		s.logger.Info("scheduler: consolidation complete",
			"connections", len(result.FactConnections),
		)
	}
}
