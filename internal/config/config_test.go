package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_ValidConfig(t *testing.T) {
	content := `
[db]
path = "test.db"

[consolidation]
interval_minutes = 30
min_facts = 5
max_group_size = 10
dedup_similarity_threshold = 0.85

[prompts]
extraction = "prompts/extraction.md"

[[providers.extraction]]
name = "openai"
base_url = "https://api.openai.com/v1"
model = "gpt-5-nano"
api_key_env = "OPENAI_API_KEY"
timeout_seconds = 30
priority = 1
`
	cfg := loadFromString(t, content)

	if cfg.DB.Path != "test.db" {
		t.Errorf("expected db.path 'test.db', got %q", cfg.DB.Path)
	}
	if cfg.Consolidation.IntervalMinutes != 30 {
		t.Errorf("expected interval 30, got %d", cfg.Consolidation.IntervalMinutes)
	}
	if cfg.Consolidation.DedupSimilarityThreshold != 0.85 {
		t.Errorf("expected threshold 0.85, got %f", cfg.Consolidation.DedupSimilarityThreshold)
	}
	if len(cfg.Providers.Extraction) != 1 {
		t.Fatalf("expected 1 extraction provider, got %d", len(cfg.Providers.Extraction))
	}
	if cfg.Providers.Extraction[0].Model != "gpt-5-nano" {
		t.Errorf("expected model gpt-5-nano, got %q", cfg.Providers.Extraction[0].Model)
	}
	if cfg.Prompts.Extraction != "prompts/extraction.md" {
		t.Errorf("expected prompts path, got %q", cfg.Prompts.Extraction)
	}
}

func TestLoad_MultipleProviders(t *testing.T) {
	content := `
[db]
path = "test.db"

[[providers.extraction]]
name = "google"
base_url = "https://google.com"
model = "gemini"
api_key_env = "GOOGLE_KEY"
timeout_seconds = 30
priority = 1

[[providers.extraction]]
name = "anthropic"
base_url = "https://api.anthropic.com"
model = "claude"
api_key_env = "ANTHROPIC_KEY"
timeout_seconds = 30
priority = 2

[[providers.consolidation]]
name = "ollama"
base_url = "http://localhost:11434"
model = "qwen"
api_key_env = ""
timeout_seconds = 120
priority = 1
`
	cfg := loadFromString(t, content)

	if len(cfg.Providers.Extraction) != 2 {
		t.Fatalf("expected 2 extraction providers, got %d", len(cfg.Providers.Extraction))
	}
	if cfg.Providers.Extraction[0].Name != "google" {
		t.Errorf("expected first provider 'google', got %q", cfg.Providers.Extraction[0].Name)
	}
	if cfg.Providers.Extraction[1].Name != "anthropic" {
		t.Errorf("expected second provider 'anthropic', got %q", cfg.Providers.Extraction[1].Name)
	}
	if len(cfg.Providers.Consolidation) != 1 {
		t.Fatalf("expected 1 consolidation provider, got %d", len(cfg.Providers.Consolidation))
	}
}

func TestLoad_Types(t *testing.T) {
	content := `
[db]
path = "test.db"

[[providers.extraction]]
name = "openai"
base_url = "https://api.openai.com/v1"
model = "gpt"
api_key_env = "KEY"
timeout_seconds = 30
priority = 1

[[types.fact_types]]
name = "workflow"
description = "Multi-step procedure"
example = "To deploy: push, wait, restart"

[[types.fact_types]]
name = "skill"
description = "A capability"
example = "Alice knows Go"

[[types.entity_types]]
name = "document"
description = "A file or artifact"
example = "README.md"

[[types.relation_types]]
name = "created_by"
description = "Authorship"
example = "README.md -> Alice"
`
	cfg := loadFromString(t, content)

	if len(cfg.Types.FactTypes) != 2 {
		t.Fatalf("expected 2 fact types, got %d", len(cfg.Types.FactTypes))
	}
	if cfg.Types.FactTypes[0].Name != "workflow" {
		t.Errorf("expected 'workflow', got %q", cfg.Types.FactTypes[0].Name)
	}
	if cfg.Types.FactTypes[0].Description != "Multi-step procedure" {
		t.Errorf("expected description, got %q", cfg.Types.FactTypes[0].Description)
	}
	if len(cfg.Types.EntityTypes) != 1 {
		t.Fatalf("expected 1 entity type, got %d", len(cfg.Types.EntityTypes))
	}
}

func TestDefaultTypes(t *testing.T) {
	defaults := DefaultTypes()

	if len(defaults.FactTypes) != 12 {
		t.Errorf("expected 12 default fact types, got %d", len(defaults.FactTypes))
	}
	if len(defaults.EntityTypes) != 9 {
		t.Errorf("expected 9 default entity types, got %d", len(defaults.EntityTypes))
	}
	if len(defaults.RelationTypes) != 9 {
		t.Errorf("expected 9 default relation types, got %d", len(defaults.RelationTypes))
	}
	if len(defaults.ConnectionTypes) != 6 {
		t.Errorf("expected 6 default connection types, got %d", len(defaults.ConnectionTypes))
	}
}

func TestEffectiveTypes_UsesConfigWhenPresent(t *testing.T) {
	content := `
[db]
path = "test.db"

[[providers.extraction]]
name = "openai"
base_url = "https://api.openai.com/v1"
model = "gpt"
api_key_env = "KEY"
timeout_seconds = 30
priority = 1

[[types.fact_types]]
name = "preference"
description = "Personal taste"
example = "Prefers dark mode"

[[types.fact_types]]
name = "decision"
description = "A choice"
example = "Chose Go"

[[types.entity_types]]
name = "person"
description = "A human"
example = "Alice"
`
	cfg := loadFromString(t, content)
	eff := cfg.EffectiveTypes()

	if len(eff.FactTypes) != 2 {
		t.Errorf("expected 2 fact types from config, got %d", len(eff.FactTypes))
	}
	if len(eff.EntityTypes) != 1 {
		t.Errorf("expected 1 entity type from config, got %d", len(eff.EntityTypes))
	}
	if len(eff.RelationTypes) != 9 {
		t.Errorf("expected 9 default relation types (not in config), got %d", len(eff.RelationTypes))
	}
}

func TestEffectiveTypes_FallsBackToDefaults(t *testing.T) {
	content := `
[db]
path = "test.db"

[[providers.extraction]]
name = "openai"
base_url = "https://api.openai.com/v1"
model = "gpt"
api_key_env = "KEY"
timeout_seconds = 30
priority = 1
`
	cfg := loadFromString(t, content)
	eff := cfg.EffectiveTypes()
	defaults := DefaultTypes()

	if len(eff.FactTypes) != len(defaults.FactTypes) {
		t.Errorf("expected %d default fact types, got %d", len(defaults.FactTypes), len(eff.FactTypes))
	}
	if len(eff.EntityTypes) != len(defaults.EntityTypes) {
		t.Errorf("expected %d default entity types, got %d", len(defaults.EntityTypes), len(eff.EntityTypes))
	}
	if len(eff.RelationTypes) != len(defaults.RelationTypes) {
		t.Errorf("expected %d default relation types, got %d", len(defaults.RelationTypes), len(eff.RelationTypes))
	}
	if len(eff.ConnectionTypes) != len(defaults.ConnectionTypes) {
		t.Errorf("expected %d default connection types, got %d", len(defaults.ConnectionTypes), len(eff.ConnectionTypes))
	}
}

func TestLoad_MissingDBPath(t *testing.T) {
	content := `
[[providers.extraction]]
name = "openai"
base_url = "https://api.openai.com/v1"
model = "gpt"
api_key_env = "KEY"
timeout_seconds = 30
priority = 1
`
	_, err := loadStringConfig(t, content)
	if err == nil {
		t.Fatal("expected validation error for missing db.path")
	}
}

func TestLoad_NoProviders(t *testing.T) {
	content := `
[db]
path = "test.db"
`
	_, err := loadStringConfig(t, content)
	if err == nil {
		t.Fatal("expected validation error for no extraction providers")
	}
}

func TestLoad_FileNotFound(t *testing.T) {
	_, err := Load("/nonexistent/path/config.toml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoad_InvalidTOML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.toml")
	os.WriteFile(path, []byte("this is not [valid toml"), 0644)

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for invalid TOML")
	}
}

// --- ContextConfig ---

func TestEffectiveContextConfig_Defaults(t *testing.T) {
	content := `
[db]
path = "test.db"

[[providers.extraction]]
name = "openai"
base_url = "https://api.openai.com/v1"
model = "gpt"
api_key_env = "KEY"
timeout_seconds = 30
priority = 1
`
	cfg := loadFromString(t, content)
	eff := cfg.EffectiveContextConfig()

	if eff.RecentHours != 24 {
		t.Errorf("expected default recent_hours=24, got %d", eff.RecentHours)
	}
	if eff.MaxFacts != 20 {
		t.Errorf("expected default max_facts=20, got %d", eff.MaxFacts)
	}
	if eff.IncludePreferences == nil || !*eff.IncludePreferences {
		t.Error("expected default include_preferences=true")
	}
}

func TestEffectiveContextConfig_Overrides(t *testing.T) {
	content := `
[db]
path = "test.db"

[[providers.extraction]]
name = "openai"
base_url = "https://api.openai.com/v1"
model = "gpt"
api_key_env = "KEY"
timeout_seconds = 30
priority = 1

[context]
recent_hours = 48
max_facts = 50
include_preferences = false
`
	cfg := loadFromString(t, content)
	eff := cfg.EffectiveContextConfig()

	if eff.RecentHours != 48 {
		t.Errorf("expected recent_hours=48, got %d", eff.RecentHours)
	}
	if eff.MaxFacts != 50 {
		t.Errorf("expected max_facts=50, got %d", eff.MaxFacts)
	}
	if eff.IncludePreferences == nil || *eff.IncludePreferences {
		t.Error("expected include_preferences=false")
	}
}

func TestEffectiveContextConfig_OnlyIncludePreferencesFalse(t *testing.T) {
	content := `
[db]
path = "test.db"

[[providers.extraction]]
name = "openai"
base_url = "https://api.openai.com/v1"
model = "gpt"
api_key_env = "KEY"
timeout_seconds = 30
priority = 1

[context]
include_preferences = false
`
	cfg := loadFromString(t, content)
	eff := cfg.EffectiveContextConfig()

	if eff.RecentHours != 24 {
		t.Errorf("expected default recent_hours=24, got %d", eff.RecentHours)
	}
	if eff.MaxFacts != 20 {
		t.Errorf("expected default max_facts=20, got %d", eff.MaxFacts)
	}
	if eff.IncludePreferences == nil || *eff.IncludePreferences {
		t.Error("expected include_preferences=false even without other context fields")
	}
}

// --- ClusterSimilarityThreshold ---

func TestEffectiveClusterSimilarityThreshold_Default(t *testing.T) {
	content := `
[db]
path = "test.db"

[[providers.extraction]]
name = "openai"
base_url = "https://api.openai.com/v1"
model = "gpt"
api_key_env = "KEY"
timeout_seconds = 30
priority = 1
`
	cfg := loadFromString(t, content)
	got := cfg.EffectiveClusterSimilarityThreshold()
	if got != 0.40 {
		t.Errorf("expected default 0.40, got %f", got)
	}
}

func TestEffectiveClusterSimilarityThreshold_Override(t *testing.T) {
	content := `
[db]
path = "test.db"

[consolidation]
cluster_similarity_threshold = 0.55

[[providers.extraction]]
name = "openai"
base_url = "https://api.openai.com/v1"
model = "gpt"
api_key_env = "KEY"
timeout_seconds = 30
priority = 1
`
	cfg := loadFromString(t, content)
	got := cfg.EffectiveClusterSimilarityThreshold()
	if got != 0.55 {
		t.Errorf("expected 0.55, got %f", got)
	}
}

// --- OpenClawConfig ---

func TestEffectiveOpenClawMode(t *testing.T) {
	tests := []struct {
		name string
		mode string
		want string
	}{
		{"empty defaults to off", "", "off"},
		{"explicit off", "off", "off"},
		{"parallel", "parallel", "parallel"},
		{"replace", "replace", "replace"},
		{"invalid defaults to off", "banana", "off"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{OpenClaw: OpenClawConfig{MemoryBackendMode: tt.mode}}
			if got := cfg.EffectiveOpenClawMode(); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// --- helpers ---

func loadFromString(t *testing.T, content string) *Config {
	t.Helper()
	cfg, err := loadStringConfig(t, content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return cfg
}

func loadStringConfig(t *testing.T, content string) (*Config, error) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	return Load(path)
}
