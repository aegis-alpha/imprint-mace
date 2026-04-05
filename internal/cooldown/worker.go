// Package cooldown implements the cool extraction pipeline worker.
// It periodically scans for cooldown message clusters that are ready
// for extraction (by silence or size trigger) and feeds them through
// the Imprint engine for fact extraction.
package cooldown

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/aegis-alpha/imprint-mace/internal/config"
	"github.com/aegis-alpha/imprint-mace/internal/db"
	"github.com/aegis-alpha/imprint-mace/internal/imprint"
	"github.com/aegis-alpha/imprint-mace/internal/model"
)

// Ingester abstracts the ingest capability so the worker can be tested
// without a full provider chain.
type Ingester interface {
	Ingest(ctx context.Context, text, sourceFile string, opts ...imprint.IngestOption) (*imprint.IngestResult, error)
}

// Worker polls for cooldown clusters ready for extraction and ingests them.
type Worker struct {
	store     db.Store
	engine    Ingester
	segmenter func(ctx context.Context, sessionID string) error
	coolCfg   config.CoolConfig
	logger    *slog.Logger
}

// NewWorker creates a cool extraction worker. If segmenter is nil, the worker
// skips the segmentation step and only processes already-clustered messages.
func NewWorker(store db.Store, engine Ingester, segmenter func(ctx context.Context, sessionID string) error, coolCfg config.CoolConfig, logger *slog.Logger) *Worker {
	return &Worker{
		store:     store,
		engine:    engine,
		segmenter: segmenter,
		coolCfg:   coolCfg,
		logger:    logger,
	}
}

// Run starts the ticker loop. It blocks until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Duration(w.coolCfg.TickSeconds) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.tick(ctx)
		}
	}
}

func (w *Worker) tick(ctx context.Context) {
	if w.segmenter != nil {
		sessions, err := w.store.ListSessionsWithUnclusteredCooldown(ctx)
		if err != nil {
			w.logger.Error("cool tick: list sessions with unclustered cooldown failed", "error", err)
		} else {
			for _, sid := range sessions {
				if ctx.Err() != nil {
					return
				}
				if err := w.segmenter(ctx, sid); err != nil {
					w.logger.Warn("cool: segment session failed", "session", sid, "error", err)
				}
			}
		}
	}

	clusters, err := w.store.ListClustersReadyForExtraction(ctx, w.coolCfg.SilenceHours, w.coolCfg.MaxClusterSize)
	if err != nil {
		w.logger.Error("cool tick: list clusters failed", "error", err)
		return
	}
	if len(clusters) == 0 {
		return
	}

	for _, cl := range clusters {
		if ctx.Err() != nil {
			return
		}
		w.processCluster(ctx, cl)
	}
}

func (w *Worker) processCluster(ctx context.Context, cl db.CooldownCluster) {
	start := time.Now()

	msgs, err := w.store.ListClusterMessages(ctx, cl.ClusterID)
	if err != nil {
		w.logger.Error("cool: load cluster messages failed",
			"cluster_id", cl.ClusterID, "error", err)
		return
	}
	if len(msgs) == 0 {
		w.logger.Warn("cool: cluster has no messages",
			"cluster_id", cl.ClusterID)
		return
	}

	claimedAt := time.Now().UTC()
	affected, err := w.store.MarkClusterProcessed(ctx, cl.ClusterID, claimedAt)
	if err != nil {
		w.logger.Error("cool: claim cluster failed",
			"cluster_id", cl.ClusterID, "error", err)
		return
	}
	if affected == 0 {
		w.logger.Info("cool: cluster already claimed or processed by another tick",
			"cluster_id", cl.ClusterID)
		return
	}

	text := buildClusterText(msgs)
	sourceFile := "cooldown-cluster/" + cl.ClusterID

	_, err = w.engine.Ingest(ctx, text, sourceFile)
	if err != nil {
		if _, clrErr := w.store.ClearClusterProcessed(ctx, cl.ClusterID); clrErr != nil {
			w.logger.Error("cool: rollback processed_at after ingest failure failed",
				"cluster_id", cl.ClusterID, "error", clrErr)
		}
		w.logger.Error("cool: ingest failed -- will retry next tick",
			"cluster_id", cl.ClusterID,
			"trigger", cl.TriggerKind,
			"messages", len(msgs),
			"error", err)
		return
	}

	w.logger.Info("cool: cluster extracted",
		"cluster_id", cl.ClusterID,
		"trigger", cl.TriggerKind,
		"messages", len(msgs),
		"duration_ms", time.Since(start).Milliseconds())
}

func buildClusterText(msgs []model.CooldownMessage) string {
	var b strings.Builder
	for _, m := range msgs {
		fmt.Fprintf(&b, "[%s, %s]: %s\n",
			m.Speaker,
			m.Timestamp.Format("2006-01-02 15:04"),
			m.Content)
	}
	return b.String()
}
