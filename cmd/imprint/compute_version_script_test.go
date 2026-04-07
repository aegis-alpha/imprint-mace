package main

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestComputeVersionScript(t *testing.T) {
	script := computeVersionScriptPath(t)

	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{
			name: "exact tag clean returns release version",
			env: map[string]string{
				"IMPRINT_TEST_EXACT_TAG": "v0.7.1",
				"IMPRINT_TEST_BASE_TAG":  "v0.7.1",
				"IMPRINT_TEST_SHORT_SHA": "abc1234",
				"IMPRINT_TEST_DIRTY":     "false",
			},
			want: "v0.7.1",
		},
		{
			name: "dirty exact tag advances to next patch dev build",
			env: map[string]string{
				"IMPRINT_TEST_EXACT_TAG": "v0.7.1",
				"IMPRINT_TEST_BASE_TAG":  "v0.7.1",
				"IMPRINT_TEST_SHORT_SHA": "abc1234",
				"IMPRINT_TEST_DIRTY":     "true",
			},
			want: "v0.7.2-dev+abc1234.dirty",
		},
		{
			name: "ahead of v0.7.0 defaults to next patch dev version",
			env: map[string]string{
				"IMPRINT_TEST_EXACT_TAG": "",
				"IMPRINT_TEST_BASE_TAG":  "v0.7.0",
				"IMPRINT_TEST_SHORT_SHA": "abc1234",
				"IMPRINT_TEST_DIRTY":     "false",
			},
			want: "v0.7.1-dev+abc1234",
		},
		{
			name: "explicit base version override wins",
			env: map[string]string{
				"IMPRINT_BASE_VERSION":   "v0.8.0",
				"IMPRINT_TEST_BASE_TAG":  "v0.7.0",
				"IMPRINT_TEST_SHORT_SHA": "abc1234",
				"IMPRINT_TEST_DIRTY":     "true",
			},
			want: "v0.8.0-dev+abc1234.dirty",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command("bash", script)
			cmd.Env = append([]string{}, "PATH="+strings.TrimSpace(mustPathEnv(t)))
			for k, v := range tc.env {
				cmd.Env = append(cmd.Env, k+"="+v)
			}

			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("compute-version.sh failed: %v\n%s", err, out)
			}
			got := strings.TrimSpace(string(out))
			if got != tc.want {
				t.Fatalf("compute-version.sh = %q, want %q", got, tc.want)
			}
		})
	}
}

func computeVersionScriptPath(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "tools", "scripts", "compute-version.sh")
}

func mustPathEnv(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("bash")
	if err != nil {
		t.Fatalf("bash not found: %v", err)
	}
	_ = path
	out, err := exec.Command("bash", "-lc", "printf %s \"$PATH\"").Output()
	if err != nil {
		t.Fatalf("read PATH: %v", err)
	}
	return string(out)
}
