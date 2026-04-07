package main

import "testing"

func TestDeriveVersion(t *testing.T) {
	tests := []struct {
		name     string
		stamped  string
		settings map[string]string
		want     string
	}{
		{
			name:    "release stamp wins",
			stamped: "v0.7.1",
			settings: map[string]string{
				"vcs.revision": "abcdef1234567890",
				"vcs.modified": "true",
			},
			want: "v0.7.1",
		},
		{
			name:    "explicit dev stamp wins",
			stamped: "v0.7.1-dev+abc1234",
			settings: map[string]string{
				"vcs.revision": "deadbeefcafebabe",
			},
			want: "v0.7.1-dev+abc1234",
		},
		{
			name:    "fallback to build info revision",
			stamped: "dev",
			settings: map[string]string{
				"vcs.revision": "abcdef1234567890",
			},
			want: "dev+abcdef1",
		},
		{
			name:    "dirty build info suffix",
			stamped: "dev",
			settings: map[string]string{
				"vcs.revision": "abcdef1234567890",
				"vcs.modified": "true",
			},
			want: "dev+abcdef1.dirty",
		},
		{
			name:     "plain dev when no metadata",
			stamped:  "dev",
			settings: map[string]string{},
			want:     "dev",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := deriveVersion(tc.stamped, tc.settings); got != tc.want {
				t.Fatalf("deriveVersion() = %q, want %q", got, tc.want)
			}
		})
	}
}
