package transcript

import (
	"testing"
	"time"
)

func TestParseFrontmatter_Full(t *testing.T) {
	input := "---\nsource: cursor\nsession: abc-123\ndate: 2026-03-15T14:30:00Z\nparticipants: [user, assistant]\ntopic: Database configuration\n---\n\n[2026-03-15T14:30:00Z] user:\nHow do I configure the database?\n"

	fm, body, err := ParseFrontmatter(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fm.Source != "cursor" {
		t.Errorf("source = %q, want %q", fm.Source, "cursor")
	}
	if fm.Session != "abc-123" {
		t.Errorf("session = %q, want %q", fm.Session, "abc-123")
	}
	wantDate := time.Date(2026, 3, 15, 14, 30, 0, 0, time.UTC)
	if fm.Date == nil || !fm.Date.Equal(wantDate) {
		t.Errorf("date = %v, want %v", fm.Date, wantDate)
	}
	if len(fm.Participants) != 2 || fm.Participants[0] != "user" || fm.Participants[1] != "assistant" {
		t.Errorf("participants = %v, want [user assistant]", fm.Participants)
	}
	if fm.Topic != "Database configuration" {
		t.Errorf("topic = %q, want %q", fm.Topic, "Database configuration")
	}
	if body != "\n[2026-03-15T14:30:00Z] user:\nHow do I configure the database?\n" {
		t.Errorf("body = %q", body)
	}
}

func TestParseFrontmatter_RequiredOnly(t *testing.T) {
	input := "---\nsource: openclaw\nsession: sess-xyz\n---\nSome body text.\n"

	fm, body, err := ParseFrontmatter(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fm.Source != "openclaw" {
		t.Errorf("source = %q, want %q", fm.Source, "openclaw")
	}
	if fm.Session != "sess-xyz" {
		t.Errorf("session = %q, want %q", fm.Session, "sess-xyz")
	}
	if fm.Date != nil {
		t.Errorf("date = %v, want nil", fm.Date)
	}
	if len(fm.Participants) != 0 {
		t.Errorf("participants = %v, want empty", fm.Participants)
	}
	if fm.Topic != "" {
		t.Errorf("topic = %q, want empty", fm.Topic)
	}
	if body != "Some body text.\n" {
		t.Errorf("body = %q", body)
	}
}

func TestParseFrontmatter_NoFrontmatter(t *testing.T) {
	input := "Just plain text without frontmatter.\nSecond line.\n"

	fm, body, err := ParseFrontmatter(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fm != nil {
		t.Errorf("expected nil frontmatter, got %+v", fm)
	}
	if body != input {
		t.Errorf("body should equal input, got %q", body)
	}
}

func TestParseFrontmatter_DateOnly(t *testing.T) {
	input := "---\nsource: claude-code\nsession: s-1\ndate: 2026-03-15\n---\nBody.\n"

	fm, _, err := ParseFrontmatter(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantDate := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	if fm.Date == nil || !fm.Date.Equal(wantDate) {
		t.Errorf("date = %v, want %v", fm.Date, wantDate)
	}
}

func TestParseFrontmatter_ColonInTopic(t *testing.T) {
	input := "---\nsource: cursor\nsession: s-2\ntopic: \"Meeting: architecture review\"\n---\nBody.\n"

	fm, _, err := ParseFrontmatter(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fm.Topic != "Meeting: architecture review" {
		t.Errorf("topic = %q, want %q", fm.Topic, "Meeting: architecture review")
	}
}

func TestParseFrontmatter_QuotedParticipants(t *testing.T) {
	input := "---\nsource: openclaw\nsession: s-3\nparticipants: [Alice, \"Bob Smith\"]\n---\nBody.\n"

	fm, _, err := ParseFrontmatter(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fm.Participants) != 2 || fm.Participants[0] != "Alice" || fm.Participants[1] != "Bob Smith" {
		t.Errorf("participants = %v, want [Alice, Bob Smith]", fm.Participants)
	}
}

func TestParseFrontmatter_EmptyContent(t *testing.T) {
	fm, body, err := ParseFrontmatter("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fm != nil {
		t.Errorf("expected nil frontmatter for empty input, got %+v", fm)
	}
	if body != "" {
		t.Errorf("body = %q, want empty", body)
	}
}

func TestParseFrontmatter_InvalidYAML(t *testing.T) {
	input := "---\nsource: [invalid yaml\nsession: broken\n---\nBody.\n"

	_, _, err := ParseFrontmatter(input)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}
