// Package config handles loading and validating OpenClaw-Memory configuration.
package config

import (
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/aegis-alpha/imprint-mace/internal/model"
)

type Config struct {
	Providers     ProviderChains      `toml:"providers"`
	LLM           LLMConfig           `toml:"llm"`
	Consolidation ConsolidationConfig `toml:"consolidation"`
	Embedding     EmbeddingConfig     `toml:"embedding"`
	Retention     RetentionConfig     `toml:"retention"`
	Watcher       WatcherConfig       `toml:"watcher"`
	API           APIConfig           `toml:"api"`
	DB            DBConfig            `toml:"db"`
	Types         TypesConfig         `toml:"types"`
	Prompts       PromptPaths         `toml:"prompts"`
	Context       ContextConfig       `toml:"context"`
	Quality       QualityConfig       `toml:"quality"`
	Health        HealthConfig        `toml:"health"`
	OpenClaw      OpenClawConfig      `toml:"openclaw"`
	Hot           HotConfig           `toml:"hot"`
	Cool          CoolConfig          `toml:"cool"`
	Rerank        RerankConfig        `toml:"rerank"`
}

// LLMConfig controls Prism mode: single-endpoint routing for all LLM tasks.
type LLMConfig struct {
	BaseURL string `toml:"base_url"`
}

// LLMEnabled reports whether Prism mode is active.
func (c *Config) LLMEnabled() bool {
	return strings.TrimSpace(c.LLM.BaseURL) != ""
}

// RerankConfig controls optional post-merge reranking; see [[providers.reranker]].
type RerankConfig struct {
	// TopN: if > 0, only the first N merged items may participate in reranking (fact-only prefix).
	// 0 means use the full merged list length for the rerank window.
	TopN int `toml:"top_n"`
}

// HotConfig controls the hot-phase raw message store (HOT-PHASE-SPEC, BVP-353).
type HotConfig struct {
	Enabled       *bool `toml:"enabled"`
	TTLMinutes    int   `toml:"ttl_minutes"`
	TickSeconds   int   `toml:"tick_seconds"`
	BatchSize     int   `toml:"batch_size"`
	EmbedMinChars int   `toml:"embed_min_chars"`
}

// EffectiveHotConfig returns hot settings with defaults applied.
func (c *Config) EffectiveHotConfig() HotConfig {
	h := c.Hot
	if h.TTLMinutes == 0 {
		h.TTLMinutes = 60
	}
	if h.TickSeconds == 0 {
		h.TickSeconds = 60
	}
	if h.BatchSize == 0 {
		h.BatchSize = 100
	}
	if h.EmbedMinChars == 0 {
		h.EmbedMinChars = 50
	}
	return h
}

// HotEnabled reports whether the hot ingest path is active.
func (c *Config) HotEnabled() bool {
	return c.Hot.Enabled != nil && *c.Hot.Enabled
}

// CoolConfig controls the cool pipeline (Phase 2 clustering + extraction).
type CoolConfig struct {
	Enabled        *bool `toml:"enabled"`
	TickSeconds    int   `toml:"tick_seconds"`
	SilenceHours   int   `toml:"silence_hours"`
	MaxClusterSize int   `toml:"max_cluster_size"`
}

// EffectiveCoolConfig returns cool settings with defaults applied.
func (c *Config) EffectiveCoolConfig() CoolConfig {
	cc := c.Cool
	if cc.TickSeconds == 0 {
		cc.TickSeconds = 300
	}
	if cc.SilenceHours == 0 {
		cc.SilenceHours = 8
	}
	if cc.MaxClusterSize == 0 {
		cc.MaxClusterSize = 50
	}
	if cc.Enabled == nil {
		enabled := false
		cc.Enabled = &enabled
	}
	return cc
}

// CoolEnabled reports whether the cool pipeline is active.
func (c *Config) CoolEnabled() bool {
	return c.Cool.Enabled != nil && *c.Cool.Enabled
}

type APIConfig struct {
	Host string `toml:"host"`
	Port int    `toml:"port"`
}

func (c *Config) EffectiveAPIAddr() string {
	host := c.API.Host
	if host == "" {
		host = "127.0.0.1"
	}
	port := c.API.Port
	if port == 0 {
		port = 8080
	}
	return fmt.Sprintf("%s:%d", host, port)
}

type ProviderChains struct {
	Extraction    []model.ProviderConfig `toml:"extraction"`
	Consolidation []model.ProviderConfig `toml:"consolidation"`
	Query         []model.ProviderConfig `toml:"query"`
	Embedding     []model.ProviderConfig `toml:"embedding"`
	Reranker      []model.ProviderConfig `toml:"reranker"`
}

type ConsolidationConfig struct {
	IntervalMinutes            int     `toml:"interval_minutes"`
	MinFacts                   int     `toml:"min_facts"`
	MaxGroupSize               int     `toml:"max_group_size"`
	DedupSimilarityThreshold   float64 `toml:"dedup_similarity_threshold"`
	ClusterSimilarityThreshold float64 `toml:"cluster_similarity_threshold"`
}

func (c *Config) EffectiveClusterSimilarityThreshold() float64 {
	if c.Consolidation.ClusterSimilarityThreshold > 0 {
		return c.Consolidation.ClusterSimilarityThreshold
	}
	return 0.40
}

type DBConfig struct {
	Path string `toml:"path"`
}

type EmbeddingConfig struct {
	Dimensions     int    `toml:"dimensions"`
	DistanceMetric string `toml:"distance_metric"`
}

func (c *Config) EffectiveEmbeddingDims() int {
	if c.Embedding.Dimensions > 0 {
		return c.Embedding.Dimensions
	}
	return 1536
}

// TypeDef is a single type in the taxonomy: name, description, and example.
// Used to render extraction prompts and validate extracted data.
type TypeDef struct {
	Name        string `toml:"name"`
	Description string `toml:"description"`
	Example     string `toml:"example"`
}

// TypesConfig holds the taxonomy. This is the runtime source of truth (D18).
// If empty, DefaultTypes() values are used as fallback.
type TypesConfig struct {
	FactTypes       []TypeDef `toml:"fact_types"`
	EntityTypes     []TypeDef `toml:"entity_types"`
	RelationTypes   []TypeDef `toml:"relation_types"`
	ConnectionTypes []TypeDef `toml:"connection_types"`
}

type RetentionConfig struct {
	DefaultContextTTLDays int `toml:"default_context_ttl_days"`
	GCAfterDays           int `toml:"gc_after_days"`
}

func (c *Config) EffectiveContextTTLDays() int {
	if c.Retention.DefaultContextTTLDays > 0 {
		return c.Retention.DefaultContextTTLDays
	}
	return 7
}

func (c *Config) EffectiveGCAfterDays() int {
	if c.Retention.GCAfterDays > 0 {
		return c.Retention.GCAfterDays
	}
	return 30
}

type WatcherConfig struct {
	Path                   string `toml:"path"`
	PollIntervalSeconds    int    `toml:"poll_interval_seconds"`
	DebounceSeconds        int    `toml:"debounce_seconds"`
	ConsolidateAfterIngest bool   `toml:"consolidate_after_ingest"`
}

type PromptPaths struct {
	Extraction    string `toml:"extraction"`
	Consolidation string `toml:"consolidation"`
	Query         string `toml:"query"`
}

type ContextConfig struct {
	Enabled            *bool `toml:"enabled"`
	RecentHours        int   `toml:"recent_hours"`
	MaxFacts           int   `toml:"max_facts"`
	IncludePreferences *bool `toml:"include_preferences"`
}

// ContextEnabled reports whether the context delivery endpoint is active (D37: default false).
func (c *Config) ContextEnabled() bool {
	return c.Context.Enabled != nil && *c.Context.Enabled
}

func (c *Config) EffectiveContextConfig() ContextConfig {
	hours := c.Context.RecentHours
	if hours == 0 {
		hours = 24
	}
	maxFacts := c.Context.MaxFacts
	if maxFacts == 0 {
		maxFacts = 20
	}
	inclPrefs := true
	if c.Context.IncludePreferences != nil {
		inclPrefs = *c.Context.IncludePreferences
	}
	return ContextConfig{
		RecentHours:        hours,
		MaxFacts:           maxFacts,
		IncludePreferences: &inclPrefs,
	}
}

type QualityConfig struct {
	Enabled              *bool   `toml:"enabled"`
	CollectionThreshold  int     `toml:"collection_threshold"`
	SupersedeRateWarning float64 `toml:"supersede_rate_warning"`
	WindowDays           int     `toml:"window_days"`
	DecayHalfLifeDays    int     `toml:"decay_half_life_days"`
	OptimizedPromptPath  string  `toml:"optimized_prompt_path"`
	MutationPromptPath   string  `toml:"mutation_prompt_path"`
	GoldenDir            string  `toml:"golden_dir"`
	OnKeptCommand        string  `toml:"on_kept_command"`
}

func (c *Config) EffectiveQualityConfig() QualityConfig {
	q := c.Quality
	if q.Enabled == nil {
		enabled := true
		q.Enabled = &enabled
	}
	if q.CollectionThreshold == 0 {
		q.CollectionThreshold = 50
	}
	if q.SupersedeRateWarning == 0 {
		q.SupersedeRateWarning = 0.30
	}
	if q.WindowDays == 0 {
		q.WindowDays = 30
	}
	if q.DecayHalfLifeDays == 0 {
		q.DecayHalfLifeDays = 14
	}
	if q.MutationPromptPath == "" {
		if c.Prompts.Extraction != "" {
			q.MutationPromptPath = filepath.Join(filepath.Dir(c.Prompts.Extraction), "mutation-prompt.md")
		} else {
			q.MutationPromptPath = "prompts/mutation-prompt.md"
		}
	}
	if q.OptimizedPromptPath == "" {
		if c.Prompts.Extraction != "" {
			q.OptimizedPromptPath = filepath.Join(filepath.Dir(c.Prompts.Extraction), "extraction-prompt-optimized.md")
		} else {
			q.OptimizedPromptPath = "prompts/extraction-prompt-optimized.md"
		}
	}
	return q
}

type HealthConfig struct {
	CatalogRefreshDays int `toml:"catalog_refresh_days"`
	MaxRetries         int `toml:"max_retries"`
	RetryIntervalHours int `toml:"retry_interval_hours"`
	QueueTTLHours      int `toml:"queue_ttl_hours"`
}

func (c *Config) EffectiveHealthConfig() HealthConfig {
	h := c.Health
	if h.CatalogRefreshDays == 0 {
		h.CatalogRefreshDays = 3
	}
	if h.MaxRetries == 0 {
		h.MaxRetries = 5
	}
	if h.RetryIntervalHours == 0 {
		h.RetryIntervalHours = 1
	}
	if h.QueueTTLHours == 0 {
		h.QueueTTLHours = 24
	}
	return h
}

type OpenClawConfig struct {
	MemoryBackendMode string `toml:"memory_backend_mode"`
}

func (c *Config) EffectiveOpenClawMode() string {
	mode := c.OpenClaw.MemoryBackendMode
	if mode == "" {
		return "off"
	}
	if mode != "off" && mode != "parallel" && mode != "replace" {
		return "off"
	}
	return mode
}

func DefaultTypes() TypesConfig {
	return TypesConfig{
		FactTypes: []TypeDef{
			{"preference", "Personal taste, style choice, tool preference", "Alice prefers dark mode in all editors"},
			{"decision", "A choice was made, a direction was chosen", "Decided to use Go instead of Rust"},
			{"rule", "Standing instruction, constraint, policy", "Never deploy on Fridays"},
			{"project", "Information about a project, its state, config", "Acme uses SQLite for storage"},
			{"contact", "A person's role, title, team, relation to others", "Bob leads the API team"},
			{"bio", "Biographical or personal information", "Alice is an engineer based in Berlin"},
			{"context", "Situational or temporal state (may expire)", "node-2 is offline since 04:00 UTC"},
			{"lesson", "Something learned from experience or failure", "Local models hallucinate on consolidation tasks"},
			{"workflow", "Multi-step procedure or process", "To deploy: push, wait for CI, run migrate, restart"},
			{"goal", "A desired outcome or objective", "Ship the MVP by end of Q2"},
			{"event", "A time-bound occurrence", "node-2 outage on 2026-03-14 at 04:00 UTC"},
			{"skill", "A capability or competency of an entity", "Alice knows Rust, Go, and SQL"},
		},
		EntityTypes: []TypeDef{
			{"person", "A human", "Alice, Bob"},
			{"project", "A software project or initiative", "Acme, DataSync"},
			{"tool", "A software tool, library, or service", "Postgres, Ollama, Ghost"},
			{"server", "A machine, VM, or infrastructure component", "mars, node-1"},
			{"concept", "An abstract idea, pattern, or technique", "consolidation, heartbeat loop"},
			{"organization", "A company, team, or group", "Acme Corp, DevOps team"},
			{"location", "A physical or logical place", "Berlin, us-east-1"},
			{"document", "A specific file, document, or artifact", "README.md, config.toml"},
			{"agent", "An AI agent or model (has agency, not just a tool)", "assistant, local-llm"},
		},
		RelationTypes: []TypeDef{
			{"owns", "Ownership or possession", "Alice -> mars"},
			{"uses", "Active usage of a tool or resource", "assistant -> local-llm"},
			{"works_on", "Active involvement in a project", "Alice -> Acme"},
			{"depends_on", "Technical or operational dependency", "node-2 -> node-1"},
			{"related_to", "Generic association (use sparingly)", "Ghost -> Lexical"},
			{"created_by", "Authorship or creation", "README.md -> Alice"},
			{"part_of", "Composition or membership", "node-1 -> Acme cluster"},
			{"manages", "Administration or oversight", "Alice -> production servers"},
			{"located_at", "Physical or logical location", "mars -> Berlin office"},
		},
		ConnectionTypes: []TypeDef{
			{"supports", "Fact A reinforces fact B", ""},
			{"contradicts", "Fact A conflicts with fact B", ""},
			{"elaborates", "Fact A adds detail to fact B", ""},
			{"caused_by", "Fact A was caused by fact B", ""},
			{"supersedes", "Fact B replaces fact A", ""},
			{"precedes", "Fact A happened before fact B", ""},
		},
	}
}

// EffectiveTypes returns the taxonomy to use at runtime.
// If config has types, returns those. Otherwise returns defaults.
func (c *Config) EffectiveTypes() TypesConfig {
	defaults := DefaultTypes()
	result := c.Types

	if len(result.FactTypes) == 0 {
		result.FactTypes = defaults.FactTypes
	}
	if len(result.EntityTypes) == 0 {
		result.EntityTypes = defaults.EntityTypes
	}
	if len(result.RelationTypes) == 0 {
		result.RelationTypes = defaults.RelationTypes
	}
	if len(result.ConnectionTypes) == 0 {
		result.ConnectionTypes = defaults.ConnectionTypes
	}
	return result
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path from CLI flag or env, not user input
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	var cfg Config
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	if cfg.LLMEnabled() && hasAnyDirectProviders(cfg.Providers) {
		slog.Warn("Prism mode active, ignoring [[providers.*]] configuration")
	}

	return &cfg, nil
}

func (c *Config) validate() error {
	if !c.LLMEnabled() && len(c.Providers.Extraction) == 0 {
		return fmt.Errorf("at least one extraction provider required")
	}
	if c.DB.Path == "" {
		return fmt.Errorf("db.path is required")
	}
	if c.LLMEnabled() {
		u, err := url.Parse(strings.TrimSpace(c.LLM.BaseURL))
		if err != nil || u.Scheme == "" || u.Host == "" {
			return fmt.Errorf("llm.base_url must be a valid URL")
		}
	}
	if c.CoolEnabled() && !c.HotEnabled() {
		return fmt.Errorf("cool.enabled requires hot.enabled")
	}
	if c.CoolEnabled() {
		ec := c.EffectiveCoolConfig()
		if ec.TickSeconds <= 0 {
			return fmt.Errorf("cool.tick_seconds must be > 0")
		}
		if ec.SilenceHours <= 0 {
			return fmt.Errorf("cool.silence_hours must be > 0")
		}
		if ec.MaxClusterSize <= 0 {
			return fmt.Errorf("cool.max_cluster_size must be > 0")
		}
	}
	return nil
}

func hasAnyDirectProviders(p ProviderChains) bool {
	return len(p.Extraction) > 0 ||
		len(p.Consolidation) > 0 ||
		len(p.Query) > 0 ||
		len(p.Embedding) > 0 ||
		len(p.Reranker) > 0
}
