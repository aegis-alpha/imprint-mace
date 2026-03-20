// Package model defines the core data types for OpenClaw-Memory.
//
// These types are the canonical type definitions for the system.
// SQL schema, API contracts, and prompts must match these types.
// (Originally translated from spec/types.rs in S001, now sole source of truth.)
package model

import "time"

// --- Temporal ---

type TimeRange struct {
	ValidFrom  *time.Time `json:"valid_from,omitempty"`
	ValidUntil *time.Time `json:"valid_until,omitempty"`
}

// --- Facts ---

type FactType string

const (
	FactPreference FactType = "preference"
	FactDecision   FactType = "decision"
	FactRule       FactType = "rule"
	FactProject    FactType = "project"
	FactContact    FactType = "contact"
	FactBio        FactType = "bio"
	FactContext    FactType = "context"
	FactLesson    FactType = "lesson"
	FactWorkflow  FactType = "workflow"
	FactGoal      FactType = "goal"
	FactEvent     FactType = "event"
	FactSkill     FactType = "skill"
)

type Source struct {
	TranscriptFile string     `json:"transcript_file"`
	LineRange      *[2]int    `json:"line_range,omitempty"`
	Timestamp      *time.Time `json:"timestamp,omitempty"`
}

type Fact struct {
	ID           string    `json:"id"`
	Source       Source    `json:"source"`
	FactType     FactType  `json:"fact_type"`
	Subject      string    `json:"subject,omitempty"`
	Content      string    `json:"content"`
	Confidence   float64   `json:"confidence"`
	Validity     TimeRange `json:"validity"`
	SupersededBy    string    `json:"superseded_by,omitempty"`
	SupersedeReason string    `json:"supersede_reason,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	Embedding      []float32 `json:"embedding,omitempty"`
	EmbeddingModel string    `json:"embedding_model,omitempty"`
}

// --- Entities ---

type EntityType string

const (
	EntityPerson       EntityType = "person"
	EntityProject      EntityType = "project"
	EntityTool         EntityType = "tool"
	EntityServer       EntityType = "server"
	EntityConcept      EntityType = "concept"
	EntityOrganization EntityType = "organization"
	EntityLocation     EntityType = "location"
	EntityDocument     EntityType = "document"
	EntityAgent        EntityType = "agent"
)

type Entity struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	EntityType EntityType `json:"entity_type"`
	Aliases    []string   `json:"aliases,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

// --- Relationships ---

type RelationType string

const (
	RelOwns      RelationType = "owns"
	RelUses      RelationType = "uses"
	RelWorksOn   RelationType = "works_on"
	RelDependsOn RelationType = "depends_on"
	RelRelatedTo RelationType = "related_to"
	RelCreatedBy RelationType = "created_by"
	RelPartOf    RelationType = "part_of"
	RelManages   RelationType = "manages"
	RelLocatedAt RelationType = "located_at"
)

type Relationship struct {
	ID           string            `json:"id"`
	FromEntity   string            `json:"from_entity"`
	ToEntity     string            `json:"to_entity"`
	RelationType RelationType      `json:"relation_type"`
	Properties   map[string]string `json:"properties,omitempty"`
	SourceFact   string            `json:"source_fact,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`
}

// --- Consolidation ---

type ConnectionType string

const (
	ConnSupports    ConnectionType = "supports"
	ConnContradicts ConnectionType = "contradicts"
	ConnElaborates  ConnectionType = "elaborates"
	ConnCausedBy    ConnectionType = "caused_by"
	ConnSupersedes  ConnectionType = "supersedes"
	ConnPrecedes    ConnectionType = "precedes"
)

type FactConnection struct {
	ID              string         `json:"id"`
	FactA           string         `json:"fact_a"`
	FactB           string         `json:"fact_b"`
	ConnectionType  ConnectionType `json:"connection_type"`
	Strength        float64        `json:"strength"`
	ConsolidationID string         `json:"consolidation_id,omitempty"`
	CreatedAt       time.Time      `json:"created_at"`
}

type Consolidation struct {
	ID            string    `json:"id"`
	SourceFactIDs []string  `json:"source_fact_ids"`
	Summary       string    `json:"summary"`
	Insight       string    `json:"insight"`
	Importance    float64   `json:"importance"`
	CreatedAt     time.Time `json:"created_at"`
}

// --- Extraction ---

type ExtractionResult struct {
	Facts           []Fact         `json:"facts"`
	Entities        []Entity       `json:"entities"`
	Relationships   []Relationship `json:"relationships"`
	ExtractionLogID string         `json:"-"`
}

// --- Query ---

type Citation struct {
	FactID          string `json:"fact_id,omitempty"`
	ConsolidationID string `json:"consolidation_id,omitempty"`
}

type QueryResult struct {
	Answer         string     `json:"answer"`
	Citations      []Citation `json:"citations"`
	FactsConsulted int        `json:"facts_consulted"`
}

// --- Provider ---

type TaskType string

const (
	TaskExtraction    TaskType = "extraction"
	TaskConsolidation TaskType = "consolidation"
	TaskQuery         TaskType = "query"
	TaskEmbedding     TaskType = "embedding"
)

type ProviderConfig struct {
	Name           string  `json:"name" toml:"name"`
	BaseURL        string  `json:"base_url" toml:"base_url"`
	Model          string  `json:"model" toml:"model"`
	APIKeyEnv      string  `json:"api_key_env" toml:"api_key_env"`
	TokenEnv       string  `json:"token_env" toml:"token_env"`
	TimeoutSeconds int     `json:"timeout_seconds" toml:"timeout_seconds"`
	Priority       int     `json:"priority" toml:"priority"`
	Temperature    float64 `json:"temperature" toml:"temperature"`
}

// --- Transcripts (D22) ---

type Transcript struct {
	ID           string     `json:"id"`
	FilePath     string     `json:"file_path"`
	Date         *time.Time `json:"date,omitempty"`
	Participants []string   `json:"participants,omitempty"`
	Topic        string     `json:"topic,omitempty"`
	ChunkCount   int        `json:"chunk_count"`
	CreatedAt    time.Time  `json:"created_at"`
}

type TranscriptChunk struct {
	ID             string `json:"id"`
	TranscriptID   string `json:"transcript_id"`
	LineStart      int    `json:"line_start"`
	LineEnd        int    `json:"line_end"`
	ContentHash    string `json:"content_hash"`
	EmbeddingModel string `json:"embedding_model,omitempty"`
}
