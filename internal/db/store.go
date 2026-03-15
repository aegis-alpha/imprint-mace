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

// Store is the persistence interface for all OpenClaw-Memory data.
// Each method operates on a single record or a filtered list.
type Store interface {
	// Facts
	CreateFact(ctx context.Context, f *model.Fact) error
	GetFact(ctx context.Context, id string) (*model.Fact, error)
	ListFacts(ctx context.Context, filter FactFilter) ([]model.Fact, error)
	SupersedeFact(ctx context.Context, oldID, newID string) error
	UpdateFact(ctx context.Context, factID string, update FactUpdate) error
	SupersedeWithContent(ctx context.Context, oldFactID string, newContent string, source string) (*model.Fact, error)
	UpdateFactEmbedding(ctx context.Context, factID string, embedding []float32, modelName string) error

	// Entities
	CreateEntity(ctx context.Context, e *model.Entity) error
	GetEntity(ctx context.Context, id string) (*model.Entity, error)
	GetEntityByName(ctx context.Context, name string) (*model.Entity, error)
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
	EnsureChunkVecTable(ctx context.Context, dims int) error

	// Vec tables
	EnsureVecTable(ctx context.Context, dims int) error

	// Embeddings
	ListFactEmbeddings(ctx context.Context, factType string) ([][]float32, error)

	// Search
	SearchByVector(ctx context.Context, embedding []float32, limit int) ([]ScoredFact, error)
	SearchByText(ctx context.Context, query string, limit int) ([]ScoredFact, error)
	ListFactsByEmbeddingModel(ctx context.Context, model string) ([]model.Fact, error)
	ListFactsWithoutEmbedding(ctx context.Context) ([]model.Fact, error)

	// Retention / GC
	DeleteExpiredFacts(ctx context.Context, olderThan time.Time) (int64, error)

	// Stats
	Stats(ctx context.Context) (*DBStats, error)

	// Lifecycle
	Close() error
}

type DBStats struct {
	Facts          int
	Entities       int
	Relationships  int
	Consolidations int
	IngestedFiles  int
}

type ScoredFact struct {
	Fact  model.Fact
	Score float64
}

type ScoredChunk struct {
	Chunk model.TranscriptChunk
	Score float64
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
	FactType     string
	Subject      string
	NotSuperseded bool
	Limit        int
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
	ErrorType          string
	ErrorMessage       string
	CreatedAt          time.Time
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
