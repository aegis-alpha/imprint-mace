package transcript

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aegis-alpha/imprint-mace/internal/model"
)

func TestGetSourceContext_ReturnsLines(t *testing.T) {
	dir := t.TempDir()
	content := "Line 1: Alpha.\nLine 2: Bravo.\nLine 3: Charlie.\nLine 4: Delta.\nLine 5: Echo.\n"
	os.WriteFile(filepath.Join(dir, "notes.md"), []byte(content), 0644)

	fact := model.Fact{
		Source: model.Source{
			TranscriptFile: "notes.md",
			LineRange:      &[2]int{2, 4},
		},
	}

	got, err := GetSourceContext(fact, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, "Bravo") {
		t.Errorf("expected line 2 content, got %q", got)
	}
	if !strings.Contains(got, "Delta") {
		t.Errorf("expected line 4 content, got %q", got)
	}
	if strings.Contains(got, "Alpha") {
		t.Errorf("should not contain line 1, got %q", got)
	}
	if strings.Contains(got, "Echo") {
		t.Errorf("should not contain line 5, got %q", got)
	}
}

func TestGetSourceContext_FileMissing(t *testing.T) {
	dir := t.TempDir()

	fact := model.Fact{
		Source: model.Source{
			TranscriptFile: "nonexistent.md",
			LineRange:      &[2]int{1, 5},
		},
	}

	_, err := GetSourceContext(fact, dir)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestGetSourceContext_NoLineRange(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "notes.md"), []byte("some content\n"), 0644)

	fact := model.Fact{
		Source: model.Source{
			TranscriptFile: "notes.md",
			LineRange:      nil,
		},
	}

	_, err := GetSourceContext(fact, dir)
	if err == nil {
		t.Fatal("expected error for nil LineRange")
	}
	if !strings.Contains(err.Error(), "no line reference") {
		t.Errorf("expected 'no line reference' error, got %q", err.Error())
	}
}

func TestGetSourceContext_OutOfRange(t *testing.T) {
	dir := t.TempDir()
	content := "Line 1.\nLine 2.\nLine 3.\n"
	os.WriteFile(filepath.Join(dir, "short.md"), []byte(content), 0644)

	fact := model.Fact{
		Source: model.Source{
			TranscriptFile: "short.md",
			LineRange:      &[2]int{2, 10},
		},
	}

	got, err := GetSourceContext(fact, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, "Line 2") {
		t.Errorf("expected line 2 content, got %q", got)
	}
	if !strings.Contains(got, "Line 3") {
		t.Errorf("expected line 3 content, got %q", got)
	}
}
