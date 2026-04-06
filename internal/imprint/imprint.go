package imprint

import (
	"context"
	"errors"
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
	FactsCount           int
	EntitiesCount        int
	RelationshipsCount   int
	EntityCollisions     int
	EntityCreations      int
	FactIDs              []string
	EntityIDs            []string
	RelationshipIDs      []string
	ContradictionChecked bool
	FactsSuperseded      int
}

// Engine is the core library entry point (D6). It holds an Extractor,
// a Store, and an optional Embedder. Provides Ingest as the single
// path for extract-then-embed-then-store.
type Engine struct {
	extractor      *extraction.Extractor
	store          db.Store
	embedder       provider.Embedder // nil = embeddings disabled (D11)
	dedupThreshold float64             // 0 = dedup disabled
	contextTTLDays int                 // 0 = no auto-expiry for context facts
	logger         *slog.Logger
	contraEnabled  bool
	contraSender   extraction.Sender
	contraPrompt   string
	mergeOnDedup   bool
	mergeSender    extraction.Sender
	mergePrompt    string // full merge-prompt.md body (Go template for user message)
}

// EngineOption configures Engine beyond the constructor parameters.
type EngineOption func(*Engine)

// WithContradiction enables batch LLM contradiction review after facts are stored.
func WithContradiction(enabled bool, sender extraction.Sender, systemPrompt string) EngineOption {
	return func(e *Engine) {
		e.contraEnabled = enabled
		e.contraSender = sender
		e.contraPrompt = strings.TrimSpace(systemPrompt)
	}
}

// WithDedupMerge enables LLM classification (skip/supersede/merge) for near-duplicate
// facts detected by smart dedup. mergePromptTemplate is the full prompt template text.
func WithDedupMerge(enabled bool, sender extraction.Sender, mergePromptTemplate string) EngineOption {
	return func(e *Engine) {
		e.mergeOnDedup = enabled
		e.mergeSender = sender
		e.mergePrompt = strings.TrimSpace(mergePromptTemplate)
	}
}

// New creates an Engine. Pass nil for embedder to disable embeddings.
// dedupThreshold > 0 enables dedup: facts with cosine similarity above
// the threshold are skipped during ingest when content Jaccard is also high.
// contextTTLDays > 0 auto-sets valid_until on context-type facts.
func New(extractor *extraction.Extractor, store db.Store, embedder provider.Embedder, dedupThreshold float64, contextTTLDays int, logger *slog.Logger, opts ...EngineOption) *Engine {
	e := &Engine{
		extractor:      extractor,
		store:          store,
		embedder:       embedder,
		dedupThreshold: dedupThreshold,
		contextTTLDays: contextTTLDays,
		logger:         logger,
	}
	for _, o := range opts {
		o(e)
	}
	return e
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
		for i := range extracted.Facts {
			lr := [2]int{o.lineStart, o.lineEnd}
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
		extracted.Entities[i].Name = strings.TrimSpace(extracted.Entities[i].Name)
		name := extracted.Entities[i].Name
		existing, err := e.store.GetEntityByName(ctx, name)
		if err == nil && existing != nil {
			nameToID[name] = existing.ID
			result.EntityIDs = append(result.EntityIDs, existing.ID)
			result.EntityCollisions++
			mergedAliases := mergeEntityAliasLists(existing.Aliases, extracted.Entities[i].Aliases)
			if len(mergedAliases) > len(existing.Aliases) {
				if uerr := e.store.UpdateEntityAliases(ctx, existing.ID, mergedAliases); uerr != nil {
					e.logger.Warn("failed to merge entity aliases",
						"name", name, "error", uerr)
				}
			}
			if extracted.Entities[i].EntityType != existing.EntityType {
				e.logger.Warn("entity type conflict on ingest",
					"name", name,
					"existing_type", existing.EntityType,
					"incoming_type", extracted.Entities[i].EntityType)
			}
			continue
		}
		if err := e.store.CreateEntity(ctx, &extracted.Entities[i]); err != nil {
			e.logger.Warn("failed to store entity",
				"name", name, "error", err)
			continue
		}
		nameToID[name] = extracted.Entities[i].ID
		result.EntityIDs = append(result.EntityIDs, extracted.Entities[i].ID)
		result.EntityCreations++
	}
	result.EntitiesCount = len(result.EntityIDs)

	if extracted.ExtractionLogID != "" && (result.EntityCollisions > 0 || result.EntityCreations > 0) {
		if err := e.store.UpdateExtractionLogCollisions(ctx, extracted.ExtractionLogID, result.EntityCollisions, result.EntityCreations); err != nil {
			e.logger.Warn("failed to update extraction log collisions", "error", err)
		}
	}

	batchFactIDs := make(map[string]struct{})
	var contraGroups []contradictionGroup

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

		skipAsDuplicate := false
		var pendingDedupSupersedeOldID string
		var pendingDedupReason string

		if vec != nil && e.dedupThreshold > 0 {
			similar, serr := e.store.SearchByVector(ctx, vec, 1)
			if serr != nil {
				e.logger.Warn("dedup: vector search failed", "error", serr)
			} else if len(similar) > 0 && similar[0].Score > e.dedupThreshold {
				oldF := similar[0].Fact
				if isActiveFactForDedup(oldF) && jaccardWords(extracted.Facts[i].Content, oldF.Content) >= dedupContentJaccardMin {
					if e.mergeOnDedup && e.mergeSender != nil && e.mergePrompt != "" {
						dec, _, _, _, derr := runDedupMergeClassify(ctx, e.mergeSender, mergeLLMSystemPrompt, e.mergePrompt, oldF, extracted.Facts[i])
						if derr != nil {
							e.logger.Warn("dedup merge classify failed, falling back to skip", "error", derr)
							skipAsDuplicate = true
						} else {
							switch dec.Action {
							case "skip":
								skipAsDuplicate = true
							case "supersede":
								pendingDedupSupersedeOldID = oldF.ID
								pendingDedupReason = dec.Reason
							case "merge":
								merged := mergedFactFromLLM(oldF, extracted.Facts[i], dec.MergedContent)
								if !confidenceAllowsSupersede(merged, oldF) {
									e.logger.Info("dedup merge: blocked by confidence guard, skipping new fact")
									skipAsDuplicate = true
									break
								}
								mvec, mErr := e.embedder.Embed(ctx, merged.Content)
								if mErr != nil {
									e.logger.Warn("dedup merge: embed failed", "error", mErr)
									skipAsDuplicate = true
									break
								}
								if err := e.store.CreateFact(ctx, &merged); err != nil {
									e.logger.Warn("dedup merge: create fact failed", "error", err)
									skipAsDuplicate = true
									break
								}
								result.FactIDs = append(result.FactIDs, merged.ID)
								batchFactIDs[merged.ID] = struct{}{}
								if err := e.store.UpdateFactEmbedding(ctx, merged.ID, mvec, e.embedder.ModelName()); err != nil {
									e.logger.Warn("dedup merge: store embedding failed", "error", err)
								}
								if err := e.store.SupersedeFactByContradiction(ctx, oldF.ID, merged.ID, formatDedupReason("merge", dec.Reason), merged.CreatedAt); err != nil {
									if !errors.Is(err, db.ErrNotFound) {
										e.logger.Warn("dedup merge: supersede failed", "error", err)
									}
								} else {
									result.FactsSuperseded++
								}
								var mcands []contradictionCandidate
								if e.contraEnabled && e.contraSender != nil && e.contraPrompt != "" {
									hits, cerr := e.store.SearchByVector(ctx, mvec, 5)
									if cerr != nil {
										e.logger.Warn("contradiction: vector search failed", "error", cerr)
									} else {
										mcands = filterContradictionCandidates(hits, merged.Subject, batchFactIDs)
									}
								}
								if len(mcands) > 0 {
									contraGroups = append(contraGroups, contradictionGroup{NewFact: merged, Candidates: mcands})
								}
								skipAsDuplicate = true
							default:
								skipAsDuplicate = true
							}
						}
					} else {
						e.logger.Info("dedup: skipping similar fact",
							"new_content", extracted.Facts[i].Content,
							"similar_to", similar[0].Fact.ID,
							"similarity", similar[0].Score)
						skipAsDuplicate = true
					}
				}
			}
		}

		if skipAsDuplicate {
			continue
		}

		var candidates []contradictionCandidate
		if e.contraEnabled && e.contraSender != nil && e.contraPrompt != "" && vec != nil {
			hits, serr := e.store.SearchByVector(ctx, vec, 5)
			if serr != nil {
				e.logger.Warn("contradiction: vector search failed", "error", serr)
			} else {
				candidates = filterContradictionCandidates(hits, extracted.Facts[i].Subject, batchFactIDs)
			}
		}

		if err := e.store.CreateFact(ctx, &extracted.Facts[i]); err != nil {
			e.logger.Warn("failed to store fact",
				"content", extracted.Facts[i].Content, "error", err)
			continue
		}
		result.FactIDs = append(result.FactIDs, extracted.Facts[i].ID)
		batchFactIDs[extracted.Facts[i].ID] = struct{}{}

		if vec != nil {
			if err := e.store.UpdateFactEmbedding(ctx, extracted.Facts[i].ID, vec, e.embedder.ModelName()); err != nil {
				e.logger.Warn("failed to store embedding",
					"fact_id", extracted.Facts[i].ID, "error", err)
			}
		}

		if pendingDedupSupersedeOldID != "" {
			newF, errN := e.store.GetFact(ctx, extracted.Facts[i].ID)
			oldF2, errO := e.store.GetFact(ctx, pendingDedupSupersedeOldID)
			if errN != nil || errO != nil || newF == nil || oldF2 == nil {
				e.logger.Warn("dedup supersede: missing fact for apply",
					"new_id", extracted.Facts[i].ID, "old_id", pendingDedupSupersedeOldID)
			} else if !confidenceAllowsSupersede(*newF, *oldF2) {
				e.logger.Info("dedup supersede: blocked by confidence guard")
			} else if err := e.store.SupersedeFactByContradiction(ctx, pendingDedupSupersedeOldID, extracted.Facts[i].ID, formatDedupReason("supersede", pendingDedupReason), newF.CreatedAt); err != nil {
				if !errors.Is(err, db.ErrNotFound) {
					e.logger.Warn("dedup supersede: failed", "old_id", pendingDedupSupersedeOldID, "error", err)
				}
			} else {
				result.FactsSuperseded++
			}
		}

		if len(candidates) > 0 {
			contraGroups = append(contraGroups, contradictionGroup{
				NewFact:    extracted.Facts[i],
				Candidates: candidates,
			})
		}
	}
	result.FactsCount = len(result.FactIDs)

	if e.contraEnabled && e.contraSender != nil && e.contraPrompt != "" && len(contraGroups) > 0 {
		result.ContradictionChecked = true
		decisions, modelName, tokens, durationMs, cerr := runContradictionBatch(ctx, e.contraSender, e.contraPrompt, contraGroups, e.logger)
		if cerr != nil {
			e.logger.Warn("contradiction batch failed", "error", cerr)
			e.writeContradictionLog(ctx, false, modelName, len(contraGroups), 0, 0, tokens, durationMs, "batch_error", cerr.Error())
		} else {
			applied := 0
			for _, d := range decisions {
				newF, errN := e.store.GetFact(ctx, d.NewFactID)
				oldF, errO := e.store.GetFact(ctx, d.OldFactID)
				if errN != nil || errO != nil || newF == nil || oldF == nil {
					e.logger.Warn("contradiction: missing fact for apply",
						"new_id", d.NewFactID, "old_id", d.OldFactID)
					continue
				}
				if !confidenceAllowsSupersede(*newF, *oldF) {
					e.logger.Info("contradiction: skipped by confidence guard",
						"new_id", d.NewFactID, "old_id", d.OldFactID)
					continue
				}
				err := e.store.SupersedeFactByContradiction(ctx, d.OldFactID, d.NewFactID, formatContradictionReason(d.Rationale), newF.CreatedAt)
				if err != nil {
					if errors.Is(err, db.ErrNotFound) {
						continue
					}
					e.logger.Warn("contradiction: supersede failed", "old_id", d.OldFactID, "error", err)
					continue
				}
				applied++
			}
			result.FactsSuperseded += applied
			e.writeContradictionLog(ctx, true, modelName, len(contraGroups), applied, len(decisions), tokens, durationMs, "", "")
		}
	}

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
		"contradiction_checked", result.ContradictionChecked,
		"facts_superseded", result.FactsSuperseded,
	)

	return result, nil
}

func (e *Engine) writeContradictionLog(ctx context.Context, success bool, modelName string, groupsSent, supersedesApplied, rawDecisions, tokens int, durationMs int64, errType, errMsg string) {
	l := &db.ExtractionLog{
		ID:                 db.NewID(),
		ProviderName:       "contradiction-check",
		Model:              modelName,
		InputLength:        groupsSent,
		TokensUsed:         tokens,
		DurationMs:         durationMs,
		Success:            success,
		FactsCount:         supersedesApplied,
		EntitiesCount:      rawDecisions,
		RelationshipsCount: 0,
		EntityCollisions:    0,
		EntityCreations:     0,
		ErrorType:           errType,
		ErrorMessage:        errMsg,
		CreatedAt:           time.Now(),
	}
	if err := e.store.CreateExtractionLog(ctx, l); err != nil {
		e.logger.Warn("failed to write contradiction extraction log", "error", err)
	}
}

// mergeEntityAliasLists returns existing aliases plus incoming aliases not yet present (case-insensitive).
func mergeEntityAliasLists(existing, incoming []string) []string {
	have := make(map[string]struct{}, len(existing)+len(incoming))
	out := make([]string, 0, len(existing)+len(incoming))
	for _, a := range existing {
		k := strings.ToLower(strings.TrimSpace(a))
		if k == "" {
			continue
		}
		have[k] = struct{}{}
		out = append(out, strings.TrimSpace(a))
	}
	for _, a := range incoming {
		k := strings.ToLower(strings.TrimSpace(a))
		if k == "" {
			continue
		}
		if _, ok := have[k]; ok {
			continue
		}
		have[k] = struct{}{}
		out = append(out, strings.TrimSpace(a))
	}
	return out
}
