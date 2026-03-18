package imprint

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/aegis-alpha/imprint-mace/internal/db"
	"github.com/aegis-alpha/imprint-mace/internal/extraction"
	"github.com/aegis-alpha/imprint-mace/internal/model"
	"github.com/aegis-alpha/imprint-mace/internal/provider"
)

// IngestResult summarizes what a single Ingest call produced.
type IngestResult struct {
	FactsCount         int
	EntitiesCount      int
	RelationshipsCount int
	FactIDs            []string
	EntityIDs          []string
	RelationshipIDs    []string
}

// Engine is the core library entry point (D6). It holds an Extractor,
// a Store, and an optional Embedder. Provides Ingest as the single
// path for extract-then-embed-then-store.
type Engine struct {
	extractor      *extraction.Extractor
	store          db.Store
	embedder       provider.Embedder // nil = embeddings disabled (D11)
	dedupThreshold float64           // 0 = dedup disabled
	contextTTLDays int               // 0 = no auto-expiry for context facts
	logger         *slog.Logger
}

// New creates an Engine. Pass nil for embedder to disable embeddings.
// dedupThreshold > 0 enables dedup: facts with cosine similarity above
// the threshold are skipped during ingest.
// contextTTLDays > 0 auto-sets valid_until on context-type facts.
func New(extractor *extraction.Extractor, store db.Store, embedder provider.Embedder, dedupThreshold float64, contextTTLDays int, logger *slog.Logger) *Engine {
	return &Engine{
		extractor:      extractor,
		store:          store,
		embedder:       embedder,
		dedupThreshold: dedupThreshold,
		contextTTLDays: contextTTLDays,
		logger:         logger,
	}
}

// IngestOption configures a single Ingest call.
type IngestOption func(*ingestOpts)

type ingestOpts struct {
	lineStart int
	lineEnd   int
	sessionID string
}

// WithLineOffset sets the source line range on all extracted facts.
// Used by the batch adapter to record which lines of the original
// file each chunk came from (D20: precise source references).
func WithLineOffset(start, end int) IngestOption {
	return func(o *ingestOpts) {
		o.lineStart = start
		o.lineEnd = end
	}
}

// WithSessionID marks all extracted facts with a realtime session prefix.
// Used by the realtime ingest path so batch ingest can later supersede
// these facts by session boundary.
func WithSessionID(id string) IngestOption {
	return func(o *ingestOpts) {
		o.sessionID = id
	}
}

// Ingest extracts facts, entities, and relationships from text and
// stores them in the database. This is the single canonical path --
// both the CLI and the batch adapter delegate here.
func (e *Engine) Ingest(ctx context.Context, text, sourceFile string, opts ...IngestOption) (*IngestResult, error) {
	var o ingestOpts
	for _, fn := range opts {
		fn(&o)
	}

	extracted, err := e.extractor.Extract(ctx, text, sourceFile)
	if err != nil {
		return nil, fmt.Errorf("extract: %w", err)
	}

	if o.lineStart > 0 {
		lr := [2]int{o.lineStart, o.lineEnd}
		for i := range extracted.Facts {
			extracted.Facts[i].Source.LineRange = &lr
		}
	}

	if o.sessionID != "" {
		realtimeSource := "realtime:" + o.sessionID
		for i := range extracted.Facts {
			if extracted.Facts[i].Source.TranscriptFile == "" || extracted.Facts[i].Source.TranscriptFile == sourceFile {
				extracted.Facts[i].Source.TranscriptFile = realtimeSource
			}
		}
	}

	if e.contextTTLDays > 0 {
		for i := range extracted.Facts {
			if extracted.Facts[i].FactType == model.FactContext && extracted.Facts[i].Validity.ValidUntil == nil {
				ttl := time.Now().UTC().Add(time.Duration(e.contextTTLDays) * 24 * time.Hour)
				extracted.Facts[i].Validity.ValidUntil = &ttl
			}
		}
	}

	result := &IngestResult{}

	nameToID := make(map[string]string, len(extracted.Entities))
	for i := range extracted.Entities {
		name := strings.TrimSpace(extracted.Entities[i].Name)
		existing, err := e.store.GetEntityByName(ctx, name)
		if err == nil && existing != nil {
			nameToID[extracted.Entities[i].Name] = existing.ID
			result.EntityIDs = append(result.EntityIDs, existing.ID)
			continue
		}
		if err := e.store.CreateEntity(ctx, &extracted.Entities[i]); err != nil {
			e.logger.Warn("failed to store entity",
				"name", extracted.Entities[i].Name, "error", err)
			continue
		}
		nameToID[extracted.Entities[i].Name] = extracted.Entities[i].ID
		result.EntityIDs = append(result.EntityIDs, extracted.Entities[i].ID)
	}
	result.EntitiesCount = len(result.EntityIDs)

	for i := range extracted.Facts {
		var vec []float32
		if e.embedder != nil {
			var err error
			vec, err = e.embedder.Embed(ctx, extracted.Facts[i].Content)
			if err != nil {
				e.logger.Warn("failed to generate embedding",
					"fact_id", extracted.Facts[i].ID, "error", err)
			}
		}

		if vec != nil && e.dedupThreshold > 0 {
			similar, err := e.store.SearchByVector(ctx, vec, 1)
			if err == nil && len(similar) > 0 && similar[0].Score > e.dedupThreshold {
				e.logger.Info("dedup: skipping similar fact",
					"new_content", extracted.Facts[i].Content,
					"similar_to", similar[0].Fact.ID,
					"similarity", similar[0].Score)
				continue
			}
		}

		if err := e.store.CreateFact(ctx, &extracted.Facts[i]); err != nil {
			e.logger.Warn("failed to store fact",
				"content", extracted.Facts[i].Content, "error", err)
			continue
		}
		result.FactIDs = append(result.FactIDs, extracted.Facts[i].ID)

		if vec != nil {
			if err := e.store.UpdateFactEmbedding(ctx, extracted.Facts[i].ID, vec, e.embedder.ModelName()); err != nil {
				e.logger.Warn("failed to store embedding",
					"fact_id", extracted.Facts[i].ID, "error", err)
			}
		}
	}
	result.FactsCount = len(result.FactIDs)

	for i := range extracted.Relationships {
		r := &extracted.Relationships[i]
		fromID, okFrom := nameToID[r.FromEntity]
		toID, okTo := nameToID[r.ToEntity]
		if !okFrom || !okTo {
			e.logger.Warn("relationship references unknown entity",
				"from", r.FromEntity, "to", r.ToEntity)
			continue
		}
		r.FromEntity = fromID
		r.ToEntity = toID
		if err := e.store.CreateRelationship(ctx, r); err != nil {
			e.logger.Warn("failed to store relationship", "error", err)
			continue
		}
		result.RelationshipIDs = append(result.RelationshipIDs, r.ID)
	}
	result.RelationshipsCount = len(result.RelationshipIDs)

	e.logger.Info("ingest complete",
		"source", sourceFile,
		"facts", result.FactsCount,
		"entities", result.EntitiesCount,
		"relationships", result.RelationshipsCount,
	)

	return result, nil
}
