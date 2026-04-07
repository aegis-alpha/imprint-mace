// Package db defines the storage interface and SQLite implementation.
package db

import (
	"context"
	"errors"
	"time"

	"github.com/aegis-alpha/imprint-mace/internal/model"
)

// ErrNotFound is returned when a requested record does not exist.
var ErrNotFound = errors.New("not found")

// ErrVectorWriteUnsafe is returned when the configured vector backend is not safe for writes.
var ErrVectorWriteUnsafe = errors.New("vector backend write path is unsafe")

// ErrVectorReadUnavailable is returned when vector search is disabled or unavailable.
var ErrVectorReadUnavailable = errors.New("vector backend read path is unavailable")

// Store is the persistence interface for all OpenClaw-Memory data.
// Each method operates on a single record or a filtered list.
type Store interface {
	// Facts
	CreateFact(ctx context.Context, f *model.Fact) error
	GetFact(ctx context.Context, id string) (*model.Fact, error)
	ListFacts(ctx context.Context, filter FactFilter) ([]model.Fact, error)
	SupersedeFact(ctx context.Context, oldID, newID string) error
	// SupersedeFactByContradiction marks oldID as superseded by newID with reason and valid_until.
	// Only updates rows that are not already superseded (superseded_by NULL or empty).
	SupersedeFactByContradiction(ctx context.Context, oldID, newID string, reason string, validUntil time.Time) error
	UpdateFact(ctx context.Context, factID string, update FactUpdate) error
	SupersedeWithContent(ctx context.Context, oldFactID string, newContent string, source string) (*model.Fact, error)
	UpdateFactEmbedding(ctx context.Context, factID string, embedding []float32, modelName string) error

	// Entities
	CreateEntity(ctx context.Context, e *model.Entity) error
	GetEntity(ctx context.Context, id string) (*model.Entity, error)
	GetEntityByName(ctx context.Context, name string) (*model.Entity, error)
	// UpdateEntityAliases replaces the alias list for an entity (JSON array in DB).
	UpdateEntityAliases(ctx context.Context, entityID string, aliases []string) error
	ListEntities(ctx context.Context, filter EntityFilter) ([]model.Entity, error)

	// Relationships
	CreateRelationship(ctx context.Context, r *model.Relationship) error
	ListRelationships(ctx context.Context, filter RelFilter) ([]model.Relationship, error)

	// Consolidations
	CreateConsolidation(ctx context.Context, c *model.Consolidation) error
	ListConsolidations(ctx context.Context, limit int) ([]model.Consolidation, error)
	ListUnconsolidatedFacts(ctx context.Context, limit int) ([]model.Fact, error)

	// Fact connections
	CreateFactConnection(ctx context.Context, fc *model.FactConnection) error
	ListFactConnections(ctx context.Context, factID string) ([]model.FactConnection, error)
	ListAllFactConnections(ctx context.Context, limit int) ([]model.FactConnection, error)

	// Graph traversal
	GetEntityGraph(ctx context.Context, entityID string, depth int) (*EntityGraph, error)
	FindPath(ctx context.Context, fromEntityID, toEntityID string) ([]PathStep, error)

	// Taxonomy signals (D17)
	CreateTaxonomySignal(ctx context.Context, s *TaxonomySignal) error
	ListTaxonomySignals(ctx context.Context, resolved bool, limit int) ([]TaxonomySignal, error)

	// Taxonomy proposals (D17)
	CreateTaxonomyProposal(ctx context.Context, p *TaxonomyProposal) error
	ListTaxonomyProposals(ctx context.Context, status string, limit int) ([]TaxonomyProposal, error)
	UpdateTaxonomyProposalStatus(ctx context.Context, id, status, shadowResults string, resolvedAt *time.Time) error

	// Extraction log (D19)
	CreateExtractionLog(ctx context.Context, l *ExtractionLog) error
	ListExtractionLogs(ctx context.Context, limit int) ([]ExtractionLog, error)
	UpdateExtractionLogCollisions(ctx context.Context, logID string, collisions, creations int) error

	// Quality signals (BVP-279)
	CreateQualitySignal(ctx context.Context, s *QualitySignal) error
	ListQualitySignals(ctx context.Context, signalType string, limit int) ([]QualitySignal, error)

	// Query log (GOALS-AND-METRICS 4.3)
	CreateQueryLog(ctx context.Context, l *QueryLog) error
	ListQueryLogs(ctx context.Context, limit int) ([]QueryLog, error)
	QueryLogStats(ctx context.Context, windowDays int) (*QueryLogStatsResult, error)

	// Eval runs (BVP-308, BVP-315)
	CreateEvalRun(ctx context.Context, r *EvalRun) error
	LatestEvalRun(ctx context.Context, evalType string) (*EvalRun, error)
	ListEvalRuns(ctx context.Context, evalType string, limit int) ([]EvalRun, error)
	GetBaselineEvalRun(ctx context.Context, evalType string) (*EvalRun, error)
	SetBaseline(ctx context.Context, id string, evalType string) error

	// Fact citations (BVP-279, D-Q1)
	CreateFactCitation(ctx context.Context, factID, queryID string) error

	// Ingested files (batch adapter)
	GetIngestedFile(ctx context.Context, path string) (*IngestedFile, error)
	UpsertIngestedFile(ctx context.Context, f *IngestedFile) error

	// Transcripts (D22)
	CreateTranscript(ctx context.Context, t *model.Transcript) error
	GetTranscript(ctx context.Context, id string) (*model.Transcript, error)
	GetTranscriptByPath(ctx context.Context, filePath string) (*model.Transcript, error)
	CreateTranscriptChunk(ctx context.Context, c *model.TranscriptChunk, text string) error
	ListTranscriptChunks(ctx context.Context, transcriptID string) ([]model.TranscriptChunk, error)
	DeleteTranscriptChunks(ctx context.Context, transcriptID string) error
	UpdateChunkEmbedding(ctx context.Context, chunkID string, embedding []float32, modelName string) error
	SearchChunksByVector(ctx context.Context, embedding []float32, limit int) ([]ScoredChunk, error)
	SearchChunksByText(ctx context.Context, query string, limit int) ([]ScoredChunk, error)
	ListChunksWithoutEmbedding(ctx context.Context) ([]model.TranscriptChunk, error)
	ListChunksByEmbeddingModel(ctx context.Context, modelName string) ([]model.TranscriptChunk, error)

	// Embeddings
	ListFactEmbeddings(ctx context.Context, factType string) ([][]float32, error)

	// Search
	SearchByVector(ctx context.Context, embedding []float32, limit int) ([]ScoredFact, error)
	SearchByText(ctx context.Context, query string, limit int) ([]ScoredFact, error)
	ListFactsByEmbeddingModel(ctx context.Context, model string) ([]model.Fact, error)
	ListFactsWithoutEmbedding(ctx context.Context) ([]model.Fact, error)

	// Session-based supersede (dual-layer memory)
	SupersedeRealtimeBySession(ctx context.Context, sessionID string) (int64, error)

	// Retention / GC
	DeleteExpiredFacts(ctx context.Context, olderThan time.Time) (int64, error)

	// Admin
	Reset(ctx context.Context) error
	DeleteFactsBySourcePattern(ctx context.Context, pattern string) (int64, error)
	DeduplicateEntities(ctx context.Context) (groups int, removed int, err error)

	// Provider models (BVP-303)
	UpsertProviderModel(ctx context.Context, m *ProviderModel) error
	ListProviderModels(ctx context.Context, providerName string) ([]ProviderModel, error)

	// Provider health (BVP-303)
	UpsertProviderHealth(ctx context.Context, h *ProviderHealth) error
	ListProviderHealth(ctx context.Context) ([]ProviderHealth, error)
	GetProviderHealth(ctx context.Context, providerName, taskType string) (*ProviderHealth, error)

	// Provider ops (BVP-305)
	UpsertProviderOps(ctx context.Context, ops *ProviderOps) error
	GetProviderOps(ctx context.Context, providerName string) (*ProviderOps, error)
	ListProviderOps(ctx context.Context) ([]ProviderOps, error)

	// Retry queue (BVP-305)
	EnqueueRetry(ctx context.Context, entry *RetryEntry) error
	DequeueRetries(ctx context.Context, limit int) ([]RetryEntry, error)
	UpdateRetryStatus(ctx context.Context, id, status, lastError string) error
	ExpireOldRetries(ctx context.Context, olderThan time.Time) (int64, error)
	RetryQueueDepth(ctx context.Context) (int, error)

	// Stats
	Stats(ctx context.Context) (*DBStats, error)

	// Hot / cooldown pipeline (BVP-352, HOT-PHASE-SPEC)
	InsertHotMessage(ctx context.Context, msg *model.HotMessage, embedding []float32) error
	ListHotMessages(ctx context.Context, filter HotMessageFilter) ([]model.HotMessage, error)
	SearchHotByText(ctx context.Context, query string, limit int) ([]ScoredHotMessage, error)
	SearchHotByVector(ctx context.Context, embedding []float32, limit int) ([]ScoredHotMessage, error)
	SearchCooldownByText(ctx context.Context, query string, limit int) ([]ScoredCooldownMessage, error)
	SearchCooldownByVector(ctx context.Context, embedding []float32, limit int) ([]ScoredCooldownMessage, error)
	MoveHotToCooldown(ctx context.Context, olderThan time.Time, batchSize int) (moved int64, err error)
	DeleteExpiredHot(ctx context.Context, olderThan time.Time) (int64, error)
	CountHotMessages(ctx context.Context) (int, error)
	// GetRecentHotMessages returns hot rows for a platform session newest-first (timestamp, then created_at, then id).
	GetRecentHotMessages(ctx context.Context, platformSessionID string, limit int) ([]model.HotMessage, error)
	// GetLinkedMessages walks linker_ref from messageID through hot and cooldown rows.
	// Returned order is chronological (oldest first); the requested message is last. Cycles stop traversal; a missing linker_ref target returns an error once at least one row was loaded.
	GetLinkedMessages(ctx context.Context, messageID string) ([]model.HotMessage, error)

	// Cool pipeline (Phase 2)
	ListCooldownUnclustered(ctx context.Context, platformSessionID string, limit int) ([]model.CooldownMessage, error)
	ListSessionsWithUnclusteredCooldown(ctx context.Context) ([]string, error)
	AssignCooldownCluster(ctx context.Context, clusterID string, messageIDs []string) error
	ListClustersReadyForExtraction(ctx context.Context, silenceHours int, maxClusterSize int) ([]CooldownCluster, error)
	ListClusterMessages(ctx context.Context, clusterID string) ([]model.CooldownMessage, error)
	MarkClusterProcessed(ctx context.Context, clusterID string, processedAt time.Time) (int64, error)
	// ClearClusterProcessed resets processed_at for all messages in the cluster (rollback after failed ingest).
	ClearClusterProcessed(ctx context.Context, clusterID string) (int64, error)
	LinkCooldownToTranscript(ctx context.Context, platformSessionID, transcriptFile string) (int64, error)
	MarkCooldownProcessedBySession(ctx context.Context, platformSessionID string) (int64, error)
	CoolPipelineStats(ctx context.Context) (*CoolStats, error)

	// Lint (BVP-368): SQL-only integrity diagnostics for CLI `imprint lint`.
	LintStaleFacts(ctx context.Context) ([]LintStaleFact, error)
	LintOrphanEntities(ctx context.Context) ([]LintOrphanEntity, error)
	LintBrokenSupersedeChains(ctx context.Context) ([]LintBrokenSupersedeChain, error)
	LintEntityDedupExact(ctx context.Context) ([]LintEntityDedupPair, error)
	LintEntityDedupSubstring(ctx context.Context) ([]LintEntityDedupPair, error)
	LintFactsMissingEmbeddingsByType(ctx context.Context) ([]LintMissingEmbeddingByType, error)
	LintDistinctNonEmptySourceFiles(ctx context.Context) ([]string, error)
	LintUnconsolidatedActiveFactsCount(ctx context.Context) (int, error)

	// Lifecycle
	Close() error
}

type DBStats struct {
	Facts            int `json:"facts"`
	Entities         int `json:"entities"`
	Relationships    int `json:"relationships"`
	Consolidations   int `json:"consolidations"`
	IngestedFiles    int `json:"ingested_files"`
	HotMessages      int `json:"hot_messages"`
	CooldownMessages int `json:"cooldown_messages"`
}

type ScoredFact struct {
	Fact  model.Fact
	Score float64
}

type ScoredChunk struct {
	Chunk model.TranscriptChunk
	Score float64
}

type HotMessageFilter struct {
	PlatformSessionID string
	After             *time.Time
	Before            *time.Time
	Limit             int
}

type ScoredHotMessage struct {
	Message model.HotMessage
	Score   float64
}

type ScoredCooldownMessage struct {
	Message model.CooldownMessage
	Score   float64
}

type CooldownCluster struct {
	ClusterID         string
	PlatformSessionID string
	MessageCount      int
	LastMessageAt     time.Time
	TriggerKind       string // "silence" or "size"
}

type CoolStats struct {
	ClustersPending   int `json:"clusters_pending"`
	ClustersExtracted int `json:"clusters_extracted"`
	MessagesProcessed int `json:"messages_processed"`
}

type EntityGraph struct {
	Center        model.Entity
	Entities      []model.Entity
	Relationships []model.Relationship
}

type PathStep struct {
	EntityID       string
	RelationshipID string
	RelationType   string
}

type FactUpdate struct {
	Confidence *float64
	ValidUntil *time.Time
	Subject    *string
}

type FactFilter struct {
	FactType      string
	Subject       string
	NotSuperseded bool
	CreatedAfter  *time.Time
	Limit         int
}

type EntityFilter struct {
	EntityType string
	Limit      int
}

type RelFilter struct {
	EntityID     string // matches from_entity OR to_entity
	RelationType string
	Limit        int
}

type ExtractionLog struct {
	ID                 string
	ProviderName       string
	Model              string
	InputLength        int
	TokensUsed         int
	DurationMs         int64
	Success            bool
	FactsCount         int
	EntitiesCount      int
	RelationshipsCount int
	EntityCollisions   int
	EntityCreations    int
	ErrorType          string
	ErrorMessage       string
	CreatedAt          time.Time
}

type QualitySignal struct {
	ID         string
	SignalType string
	Category   string
	Value      float64
	Details    string
	CreatedAt  time.Time
}

type IngestedFile struct {
	Path        string
	ContentHash string
	Chunks      int
	FactsCount  int
	ProcessedAt time.Time
}

type TaxonomySignal struct {
	ID           string
	SignalType   string // "custom_frequency", "type_unused", "low_confidence", "type_overlap"
	TypeCategory string // "fact", "entity", "relation", "connection"
	TypeName     string
	Count        int
	Details      string // JSON
	CreatedAt    time.Time
	ResolvedBy   string
}

type QueryLog struct {
	ID                 string
	Endpoint           string
	Question           string
	TotalLatencyMs     int64
	RetrievalLatencyMs int64
	SynthesisLatencyMs int64
	FactsFound         int
	FactsByVector      int
	FactsByText        int
	FactsByGraph       int
	ChunksByVector     int
	ChunksByText       int
	HotByVector        int
	HotByText          int
	CooldownByVector   int
	CooldownByText     int
	CitationsCount     int
	EmbedderAvailable  bool
	Error              string
	CreatedAt          time.Time
}

type QueryLogStatsResult struct {
	TotalQueries      int     `json:"total_queries"`
	TotalContext      int     `json:"total_context"`
	AvgQueryLatency   float64 `json:"avg_query_latency"`
	AvgContextLatency float64 `json:"avg_context_latency"`
	ErrorCount        int     `json:"error_count"`
	EmbedderAvailPct  float64 `json:"embedder_avail_pct"`
}

type EvalRun struct {
	ID            string
	EvalType      string // "extraction" or "retrieval"
	Score         float64
	Score2        float64 // MRR for retrieval, 0 for extraction
	Report        string  // full JSON
	PromptHash    string
	ExamplesCount int
	IsBaseline    bool
	GitCommit     string
	CreatedAt     time.Time
}

type TaxonomyProposal struct {
	ID            string
	Action        string // "add", "remove", "merge", "rename"
	TypeCategory  string // "fact", "entity", "relation", "connection"
	TypeName      string
	Definition    string // JSON: {name, description, example}
	Rationale     string
	Status        string // "proposed", "validated", "applied", "rejected"
	ShadowResults string // JSON
	SignalIDs     string // JSON array of signal IDs
	CreatedAt     time.Time
	ResolvedAt    *time.Time
}

type ProviderModel struct {
	ProviderName  string
	ModelID       string
	ContextWindow int
	Available     bool
	LastChecked   time.Time
}

type ProviderHealth struct {
	ProviderName    string
	TaskType        string
	ConfiguredModel string
	ActiveModel     string
	Status          string // "ok", "degraded", "unavailable"
	LastError       string
	LastChecked     time.Time
	SwitchedAt      *time.Time
}

type ProviderOps struct {
	ProviderName string
	Status       string // "ok", "transient_error", "auth_error", "exhausted"
	RetryCount   int
	MaxRetries   int
	LastError    string
	ErrorType    string
	NextCheckAt  *time.Time
	LastSuccess  *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type RetryEntry struct {
	ID         string
	TaskType   string
	Payload    string // JSON
	CreatedAt  time.Time
	RetryCount int
	LastError  string
	Status     string // "pending", "processing", "completed", "expired"
}
