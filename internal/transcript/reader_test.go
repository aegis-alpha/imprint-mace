package transcript

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadContext_ReturnsLines(t *testing.T) {
	dir := t.TempDir()
	content := "Line 1: Alpha.\nLine 2: Bravo.\nLine 3: Charlie.\nLine 4: Delta.\nLine 5: Echo.\n"
	os.WriteFile(filepath.Join(dir, "notes.md"), []byte(content), 0644)

	got, err := ReadContext(filepath.Join(dir, "notes.md"), 2, 4, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, "Bravo") {
		t.Errorf("expected line 2, got %q", got)
	}
	if !strings.Contains(got, "Delta") {
		t.Errorf("expected line 4, got %q", got)
	}
	if strings.Contains(got, "Alpha") {
		t.Errorf("should not contain line 1, got %q", got)
	}
	if strings.Contains(got, "Echo") {
		t.Errorf("should not contain line 5, got %q", got)
	}
}

func TestReadContext_WithSurroundingContext(t *testing.T) {
	dir := t.TempDir()
	content := "Line 1.\nLine 2.\nLine 3.\nLine 4.\nLine 5.\nLine 6.\nLine 7.\n"
	os.WriteFile(filepath.Join(dir, "ctx.md"), []byte(content), 0644)

	got, err := ReadContext(filepath.Join(dir, "ctx.md"), 4, 4, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, "Line 2") {
		t.Errorf("expected context line 2, got %q", got)
	}
	if !strings.Contains(got, "Line 6") {
		t.Errorf("expected context line 6, got %q", got)
	}
}

func TestReadContext_MissingFile(t *testing.T) {
	_, err := ReadContext("/nonexistent/path.md", 1, 5, 0)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}
