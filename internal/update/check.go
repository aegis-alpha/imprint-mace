// Package update checks GitHub Releases for newer versions of Imprint
// and prints a one-line notice to stderr. The check runs at most once
// per day (cached in ~/.imprint/update-check) and never blocks startup.
package update

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var releasesURL = "https://api.github.com/repos/aegis-alpha/imprint-mace/releases/latest"

const (
	checkFile   = "update-check"
	checkMaxAge = 24 * time.Hour
	httpTimeout = 2 * time.Second
)

type cachedCheck struct {
	Version   string    `json:"version"`
	CheckedAt time.Time `json:"checked_at"`
}

type githubRelease struct {
	TagName string `json:"tag_name"`
}

// Check queries GitHub for the latest release and prints a notice to
// stderr if a newer version is available. Safe to call from a goroutine.
// Silently returns on any error (network, parse, file I/O).
func Check(currentVersion string) {
	if currentVersion == "" || isDevVersion(currentVersion) {
		return
	}
	if os.Getenv("IMPRINT_NO_UPDATE_CHECK") != "" {
		return
	}

	cachePath := cacheFilePath()

	if cached, ok := readCache(cachePath); ok {
		if time.Since(cached.CheckedAt) < checkMaxAge {
			printIfNewer(currentVersion, cached.Version)
			return
		}
	}

	latest, err := fetchLatest()
	if err != nil {
		return
	}

	writeCache(cachePath, latest)
	printIfNewer(currentVersion, latest)
}

func cacheFilePath() string {
	return filepath.Join(os.Getenv("HOME"), ".imprint", checkFile)
}

func readCache(path string) (*cachedCheck, bool) {
	data, err := os.ReadFile(path) //nolint:gosec // cache file path from XDG, not user input
	if err != nil {
		return nil, false
	}
	var c cachedCheck
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, false
	}
	return &c, true
}

func writeCache(path, version string) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return
	}
	c := cachedCheck{Version: version, CheckedAt: time.Now().UTC()}
	data, err := json.Marshal(c)
	if err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0600)
}

func fetchLatest() (string, error) {
	client := &http.Client{Timeout: httpTimeout}
	resp, err := client.Get(releasesURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}

	var rel githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return "", err
	}
	if rel.TagName == "" {
		return "", fmt.Errorf("empty tag_name")
	}
	return rel.TagName, nil
}

func printIfNewer(current, latest string) {
	if latest == "" {
		return
	}
	c := normalizeVersion(current)
	l := normalizeVersion(latest)
	if l > c {
		fmt.Fprintf(os.Stderr, "Update available: %s (current: %s)\nhttps://github.com/aegis-alpha/imprint-mace/releases\n", latest, current)
	}
}

func normalizeVersion(v string) string {
	v = strings.TrimPrefix(v, "v")
	if idx := strings.Index(v, "+"); idx != -1 {
		v = v[:idx]
	}
	return v
}

func isDevVersion(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" {
		return false
	}
	return v == "dev" || strings.HasPrefix(v, "dev+") || strings.Contains(v, "-dev+")
}
