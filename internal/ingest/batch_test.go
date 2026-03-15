package ingest

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aegis-alpha/imprint-mace/internal/config"
	"github.com/aegis-alpha/imprint-mace/internal/db"
	"github.com/aegis-alpha/imprint-mace/internal/extraction"
	"github.com/aegis-alpha/imprint-mace/internal/imprint"
	"github.com/aegis-alpha/imprint-mace/internal/provider"
	"github.com/aegis-alpha/imprint-mace/internal/transcript"
)

// --- mock sender ---

type mockSender struct {
	response *provider.Response
	err      error
	calls    int
}

func (m *mockSender) Send(_ context.Context, _ provider.Request) (*provider.Response, error) {
	m.calls++
	return m.response, m.err
}

// --- helpers ---

func testSetup(t *testing.T, sender *mockSender) (*BatchAdapter, db.Store, string) {
	t.Helper()
	store, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	ext, err := extraction.New(
		sender,
		promptPath(t),
		config.DefaultTypes(),
		slog.Default(),
	)
	if err != nil {
		t.Fatalf("create extractor: %v", err)
	}

	eng := imprint.New(ext, store, nil, 0, 0, slog.Default())
	dir := t.TempDir()
	adapter := NewBatchAdapter(eng, store, slog.Default())
	return adapter, store, dir
}

func promptPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "prompt.md")
	os.WriteFile(path, []byte("Extract facts. Return JSON.\n## Fact Types ({{len .FactTypes}})\n"), 0644)
	return path
}

var mockJSON = `{
  "facts": [{"fact_type": "decision", "subject": "Acme", "content": "Acme uses Go.", "confidence": 0.9, "validity": {"valid_from": null, "valid_until": null}}],
  "entities": [{"name": "Acme", "entity_type": "project", "aliases": []}],
  "relationships": []
}`

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
}

// --- tests ---

func TestProcessDir_SingleFile(t *testing.T) {
	sender := &mockSender{response: &provider.Response{
		Content: mockJSON, ProviderName: "mock", Model: "test", TokensUsed: 50,
	}}
	adapter, _, dir := testSetup(t, sender)

	writeFile(t, dir, "notes.md", "Alice decided to use Go for Acme.")

	result, err := adapter.ProcessDir(context.Background(), dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.FilesProcessed != 1 {
		t.Errorf("expected 1 file processed, got %d", result.FilesProcessed)
	}
	if result.FactsTotal != 1 {
		t.Errorf("expected 1 fact, got %d", result.FactsTotal)
	}
	if result.EntitiesTotal != 1 {
		t.Errorf("expected 1 entity, got %d", result.EntitiesTotal)
	}
	if len(result.Errors) != 0 {
		t.Errorf("expected 0 errors, got %d", len(result.Errors))
	}
}

func TestProcessDir_SkipsDuplicate(t *testing.T) {
	sender := &mockSender{response: &provider.Response{
		Content: mockJSON, ProviderName: "mock", Model: "test", TokensUsed: 50,
	}}
	adapter, _, dir := testSetup(t, sender)

	writeFile(t, dir, "notes.md", "Alice decided to use Go for Acme.")

	// First run
	_, err := adapter.ProcessDir(context.Background(), dir)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}

	callsBefore := sender.calls

	// Second run -- same content, should skip
	result, err := adapter.ProcessDir(context.Background(), dir)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if result.FilesProcessed != 0 {
		t.Errorf("expected 0 files processed on second run, got %d", result.FilesProcessed)
	}
	if result.FilesSkipped != 1 {
		t.Errorf("expected 1 file skipped, got %d", result.FilesSkipped)
	}
	if sender.calls != callsBefore {
		t.Errorf("expected no LLM calls on second run, got %d extra", sender.calls-callsBefore)
	}
}

func TestProcessDir_ReprocessesChangedFile(t *testing.T) {
	sender := &mockSender{response: &provider.Response{
		Content: mockJSON, ProviderName: "mock", Model: "test", TokensUsed: 50,
	}}
	adapter, _, dir := testSetup(t, sender)

	writeFile(t, dir, "notes.md", "Version 1 content.")

	_, err := adapter.ProcessDir(context.Background(), dir)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}

	// Change the file
	writeFile(t, dir, "notes.md", "Version 2 content -- completely different.")

	result, err := adapter.ProcessDir(context.Background(), dir)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if result.FilesProcessed != 1 {
		t.Errorf("expected 1 file reprocessed after change, got %d", result.FilesProcessed)
	}
}

func TestProcessDir_SkipsNonTextFiles(t *testing.T) {
	sender := &mockSender{response: &provider.Response{
		Content: mockJSON, ProviderName: "mock", Model: "test", TokensUsed: 50,
	}}
	adapter, _, dir := testSetup(t, sender)

	writeFile(t, dir, "notes.md", "Some content.")
	writeFile(t, dir, "image.png", "not real png data")
	writeFile(t, dir, "data.json", `{"key": "value"}`)

	result, err := adapter.ProcessDir(context.Background(), dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.FilesProcessed != 1 {
		t.Errorf("expected 1 file processed (only .md), got %d", result.FilesProcessed)
	}
}

func TestProcessDir_HandlesEmptyDir(t *testing.T) {
	sender := &mockSender{response: &provider.Response{
		Content: mockJSON, ProviderName: "mock", Model: "test", TokensUsed: 50,
	}}
	adapter, _, dir := testSetup(t, sender)

	result, err := adapter.ProcessDir(context.Background(), dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.FilesProcessed != 0 {
		t.Errorf("expected 0 files, got %d", result.FilesProcessed)
	}
}

func TestProcessDir_ProviderError(t *testing.T) {
	sender := &mockSender{err: fmt.Errorf("rate limited")}
	adapter, _, dir := testSetup(t, sender)

	writeFile(t, dir, "notes.md", "Some content.")

	result, err := adapter.ProcessDir(context.Background(), dir)
	if err != nil {
		t.Fatalf("unexpected top-level error: %v", err)
	}
	if len(result.Errors) != 1 {
		t.Fatalf("expected 1 error, got %d", len(result.Errors))
	}
	if !strings.Contains(result.Errors[0].Err.Error(), "rate limited") {
		t.Errorf("expected error about rate limit, got %q", result.Errors[0].Err.Error())
	}
	if result.FilesProcessed != 0 {
		t.Errorf("expected 0 processed, got %d", result.FilesProcessed)
	}
}

func TestProcessDir_MultipleFiles(t *testing.T) {
	sender := &mockSender{response: &provider.Response{
		Content: mockJSON, ProviderName: "mock", Model: "test", TokensUsed: 50,
	}}
	adapter, _, dir := testSetup(t, sender)

	writeFile(t, dir, "day1.md", "Day 1 notes about Acme.")
	writeFile(t, dir, "day2.txt", "Day 2 notes about Acme.")
	writeFile(t, dir, "day3.md", "Day 3 notes about Acme.")

	result, err := adapter.ProcessDir(context.Background(), dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.FilesProcessed != 3 {
		t.Errorf("expected 3 files processed, got %d", result.FilesProcessed)
	}
	if result.FactsTotal != 3 {
		t.Errorf("expected 3 facts (1 per file), got %d", result.FactsTotal)
	}
}

func TestChunkText_SmallFile(t *testing.T) {
	text := "Short text."
	chunks := chunkText(text)
	if len(chunks) != 1 {
		t.Errorf("expected 1 chunk, got %d", len(chunks))
	}
	if chunks[0].Text != text {
		t.Errorf("expected original text, got %q", chunks[0].Text)
	}
}

func TestChunkText_LargeFile(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 300; i++ {
		fmt.Fprintf(&b, "Line %d: This is a line of text that adds some content.\n", i)
	}
	text := b.String()
	if len(text) <= maxChunkSize {
		t.Fatalf("test text too short: %d chars", len(text))
	}

	chunks := chunkText(text)
	if len(chunks) < 2 {
		t.Errorf("expected at least 2 chunks for %d chars, got %d", len(text), len(chunks))
	}
	for i, c := range chunks {
		if len(c.Text) == 0 {
			t.Errorf("chunk[%d] is empty", i)
		}
	}
}

func TestChunkText_SmallFile_HasLineOffsets(t *testing.T) {
	text := "Line one.\nLine two.\nLine three.\n"
	chunks := chunkText(text)
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if chunks[0].StartLine != 1 {
		t.Errorf("expected StartLine=1, got %d", chunks[0].StartLine)
	}
	if chunks[0].EndLine != 3 {
		t.Errorf("expected EndLine=3, got %d", chunks[0].EndLine)
	}
}

func TestChunkText_LargeFile_HasLineOffsets(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 300; i++ {
		fmt.Fprintf(&b, "Line %d: This is a line of text that adds some content.\n", i)
	}
	text := b.String()

	chunks := chunkText(text)
	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(chunks))
	}

	if chunks[0].StartLine != 1 {
		t.Errorf("first chunk: expected StartLine=1, got %d", chunks[0].StartLine)
	}

	for i, c := range chunks {
		if c.StartLine < 1 {
			t.Errorf("chunk[%d]: StartLine=%d, must be >= 1", i, c.StartLine)
		}
		if c.EndLine < c.StartLine {
			t.Errorf("chunk[%d]: EndLine=%d < StartLine=%d", i, c.EndLine, c.StartLine)
		}
	}

	last := chunks[len(chunks)-1]
	if last.EndLine != 300 {
		t.Errorf("last chunk: expected EndLine=300, got %d", last.EndLine)
	}
}

func TestBatchIngest_SetsSourceLines(t *testing.T) {
	sender := &mockSender{response: &provider.Response{
		Content: mockJSON, ProviderName: "mock", Model: "test", TokensUsed: 50,
	}}
	adapter, store, dir := testSetup(t, sender)

	writeFile(t, dir, "notes.md", "Line one.\nLine two.\nLine three.\n")

	_, err := adapter.ProcessDir(context.Background(), dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	facts, err := store.ListFacts(context.Background(), db.FactFilter{Limit: 10})
	if err != nil {
		t.Fatalf("list facts: %v", err)
	}
	if len(facts) == 0 {
		t.Fatal("expected facts in DB")
	}
	for i, f := range facts {
		if f.Source.LineRange == nil {
			t.Errorf("fact[%d]: source_lines is nil, expected non-nil", i)
		} else {
			if f.Source.LineRange[0] != 1 {
				t.Errorf("fact[%d]: expected StartLine=1, got %d", i, f.Source.LineRange[0])
			}
			if f.Source.LineRange[1] != 3 {
				t.Errorf("fact[%d]: expected EndLine=3, got %d", i, f.Source.LineRange[1])
			}
		}
	}
}

func TestBatchIngest_LargeFile_CorrectLines(t *testing.T) {
	sender := &mockSender{response: &provider.Response{
		Content: mockJSON, ProviderName: "mock", Model: "test", TokensUsed: 50,
	}}
	adapter, store, dir := testSetup(t, sender)

	var b strings.Builder
	for i := 1; i <= 300; i++ {
		fmt.Fprintf(&b, "Line %d: This is a line of text that adds some content.\n", i)
	}
	writeFile(t, dir, "transcript.md", b.String())

	result, err := adapter.ProcessDir(context.Background(), dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ChunksTotal < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", result.ChunksTotal)
	}

	facts, err := store.ListFacts(context.Background(), db.FactFilter{Limit: 100})
	if err != nil {
		t.Fatalf("list facts: %v", err)
	}

	for i, f := range facts {
		if f.Source.LineRange == nil {
			t.Errorf("fact[%d]: source_lines is nil", i)
			continue
		}
		if f.Source.LineRange[0] < 1 || f.Source.LineRange[1] > 300 {
			t.Errorf("fact[%d]: line range [%d, %d] out of bounds [1, 300]",
				i, f.Source.LineRange[0], f.Source.LineRange[1])
		}
	}

	hasLaterChunk := false
	for _, f := range facts {
		if f.Source.LineRange != nil && f.Source.LineRange[0] > 1 {
			hasLaterChunk = true
			break
		}
	}
	if !hasLaterChunk {
		t.Error("all facts start at line 1 -- line offsets not propagated from later chunks")
	}
}

func TestChunkText_OverlapLines(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 300; i++ {
		fmt.Fprintf(&b, "Line %d: This is a line of text that adds some content.\n", i)
	}
	text := b.String()

	chunks := chunkText(text)
	if len(chunks) < 2 {
		t.Fatalf("need at least 2 chunks for overlap test, got %d", len(chunks))
	}

	for i := 1; i < len(chunks); i++ {
		prev := chunks[i-1]
		curr := chunks[i]
		if curr.StartLine > prev.EndLine {
			t.Errorf("chunk[%d] StartLine=%d > chunk[%d] EndLine=%d -- no overlap",
				i, curr.StartLine, i-1, prev.EndLine)
		}
	}
}

func TestProcessDir_CreatesTranscript(t *testing.T) {
	sender := &mockSender{response: &provider.Response{
		Content: mockJSON, ProviderName: "mock", Model: "test", TokensUsed: 50,
	}}
	adapter, store, dir := testSetup(t, sender)

	writeFile(t, dir, "standup.md", "Alice discussed the Acme project.\n")

	_, err := adapter.ProcessDir(context.Background(), dir)
	if err != nil {
		t.Fatalf("ProcessDir: %v", err)
	}

	tr, err := store.GetTranscriptByPath(context.Background(), "standup.md")
	if err != nil {
		t.Fatalf("GetTranscriptByPath: %v", err)
	}
	if tr == nil {
		t.Fatal("expected transcript record, got nil")
	}
	if tr.FilePath != "standup.md" {
		t.Errorf("file_path = %q, want %q", tr.FilePath, "standup.md")
	}
	if tr.ChunkCount != 1 {
		t.Errorf("chunk_count = %d, want 1", tr.ChunkCount)
	}
}

func TestProcessDir_CreatesTranscriptChunks(t *testing.T) {
	sender := &mockSender{response: &provider.Response{
		Content: mockJSON, ProviderName: "mock", Model: "test", TokensUsed: 50,
	}}
	adapter, store, dir := testSetup(t, sender)

	writeFile(t, dir, "notes.md", "Line 1.\nLine 2.\nLine 3.\n")

	_, err := adapter.ProcessDir(context.Background(), dir)
	if err != nil {
		t.Fatalf("ProcessDir: %v", err)
	}

	tr, err := store.GetTranscriptByPath(context.Background(), "notes.md")
	if err != nil || tr == nil {
		t.Fatalf("GetTranscriptByPath: %v (tr=%v)", err, tr)
	}

	chunks, err := store.ListTranscriptChunks(context.Background(), tr.ID)
	if err != nil {
		t.Fatalf("ListTranscriptChunks: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("got %d chunks, want 1", len(chunks))
	}
	if chunks[0].LineStart != 1 || chunks[0].LineEnd != 3 {
		t.Errorf("chunk lines = [%d, %d], want [1, 3]", chunks[0].LineStart, chunks[0].LineEnd)
	}
	if chunks[0].ContentHash == "" {
		t.Error("chunk content_hash is empty")
	}
}

func TestEndToEnd_FactToSource(t *testing.T) {
	sender := &mockSender{response: &provider.Response{
		Content: mockJSON, ProviderName: "mock", Model: "test", TokensUsed: 50,
	}}
	adapter, store, dir := testSetup(t, sender)

	var b strings.Builder
	for i := 1; i <= 300; i++ {
		fmt.Fprintf(&b, "Line %d: Alice discussed Acme project details.\n", i)
	}
	writeFile(t, dir, "transcript.md", b.String())

	_, err := adapter.ProcessDir(context.Background(), dir)
	if err != nil {
		t.Fatalf("ProcessDir: %v", err)
	}

	facts, err := store.ListFacts(context.Background(), db.FactFilter{Limit: 100})
	if err != nil {
		t.Fatalf("ListFacts: %v", err)
	}
	if len(facts) == 0 {
		t.Fatal("expected facts in DB after ProcessDir")
	}

	for i, f := range facts {
		if f.Source.LineRange == nil {
			t.Errorf("fact[%d]: no line range", i)
			continue
		}

		sourceCtx, err := transcript.GetSourceContext(f, dir)
		if err != nil {
			t.Errorf("fact[%d]: transcript.GetSourceContext error: %v", i, err)
			continue
		}
		if len(sourceCtx) == 0 {
			t.Errorf("fact[%d]: transcript.GetSourceContext returned empty string", i)
			continue
		}
		if !strings.Contains(sourceCtx, "Alice discussed Acme") {
			t.Errorf("fact[%d]: source context missing expected content, got %q",
				i, sourceCtx[:min(len(sourceCtx), 80)])
		}
	}
}
