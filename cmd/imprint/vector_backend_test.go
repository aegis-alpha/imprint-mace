package main

import (
	"testing"

	"github.com/aegis-alpha/imprint-mace/internal/config"
	"github.com/aegis-alpha/imprint-mace/internal/vecindex"
)

func TestConfiguredVectorMode(t *testing.T) {
	tests := []struct {
		name string
		cfg  *config.Config
		want vecindex.Mode
	}{
		{name: "default", cfg: &config.Config{}, want: vecindex.ModeReadWrite},
		{name: "read-only", cfg: &config.Config{Vector: config.VectorConfig{Mode: "read-only"}}, want: vecindex.ModeReadOnly},
		{name: "disabled", cfg: &config.Config{Vector: config.VectorConfig{Mode: "disabled"}}, want: vecindex.ModeDisabled},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := configuredVectorMode(tc.cfg); got != tc.want {
				t.Fatalf("configuredVectorMode() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRunUSearchWriteSelfTest(t *testing.T) {
	skipIfUSearchBroken(t)

	report, err := runUSearchWriteSelfTest(8)
	if err != nil {
		t.Fatalf("runUSearchWriteSelfTest(): %v", err)
	}
	if report.Backend != "usearch" {
		t.Fatalf("backend = %q, want usearch", report.Backend)
	}
	if report.Check == "" {
		t.Fatal("expected non-empty self-test check name")
	}
}
