package update

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCheck_DevVersion_Skips(t *testing.T) {
	Check("dev")
	Check("")
}

func TestCheck_EnvDisabled_Skips(t *testing.T) {
	t.Setenv("IMPRINT_NO_UPDATE_CHECK", "1")
	Check("v0.1.0")
}

func TestFetchLatest_ParsesTagName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(githubRelease{TagName: "v0.9.0"})
	}))
	defer srv.Close()

	orig := releasesURL
	releasesURL = srv.URL
	defer func() { releasesURL = orig }()

	tag, err := fetchLatest()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tag != "v0.9.0" {
		t.Errorf("expected v0.9.0, got %s", tag)
	}
}

func TestFetchLatest_ServerError_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	orig := releasesURL
	releasesURL = srv.URL
	defer func() { releasesURL = orig }()

	_, err := fetchLatest()
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
}

func TestFetchLatest_NetworkFailure_ReturnsError(t *testing.T) {
	orig := releasesURL
	releasesURL = "http://127.0.0.1:1"
	defer func() { releasesURL = orig }()

	_, err := fetchLatest()
	if err == nil {
		t.Fatal("expected error for unreachable host")
	}
}

func TestCacheReadWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "update-check")

	writeCache(path, "v0.5.0")

	cached, ok := readCache(path)
	if !ok {
		t.Fatal("expected cache to be readable")
	}
	if cached.Version != "v0.5.0" {
		t.Errorf("expected v0.5.0, got %s", cached.Version)
	}
	if time.Since(cached.CheckedAt) > time.Minute {
		t.Errorf("checked_at too old: %v", cached.CheckedAt)
	}
}

func TestCacheReadWrite_MissingFile(t *testing.T) {
	_, ok := readCache("/nonexistent/path")
	if ok {
		t.Fatal("expected cache miss for nonexistent file")
	}
}

func TestCacheReadWrite_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "update-check")
	os.WriteFile(path, []byte("not json"), 0600)

	_, ok := readCache(path)
	if ok {
		t.Fatal("expected cache miss for invalid JSON")
	}
}

func TestPrintIfNewer_NewerVersion(t *testing.T) {
	printIfNewer("v0.1.0", "v0.2.0")
}

func TestPrintIfNewer_SameVersion(t *testing.T) {
	printIfNewer("v0.1.0", "v0.1.0")
}

func TestPrintIfNewer_OlderVersion(t *testing.T) {
	printIfNewer("v0.2.0", "v0.1.0")
}

func TestPrintIfNewer_EmptyLatest(t *testing.T) {
	printIfNewer("v0.1.0", "")
}

func TestNormalizeVersion(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"v0.1.0", "0.1.0"},
		{"0.1.0", "0.1.0"},
		{"v1.2.3", "1.2.3"},
	}
	for _, tt := range tests {
		got := normalizeVersion(tt.input)
		if got != tt.want {
			t.Errorf("normalizeVersion(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
