// Package ingest provides a batch adapter that processes directories of
// text/markdown files through the extraction pipeline.
package ingest

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aegis-alpha/imprint-mace/internal/db"
	"github.com/aegis-alpha/imprint-mace/internal/imprint"
	"github.com/aegis-alpha/imprint-mace/internal/model"
	"github.com/aegis-alpha/imprint-mace/internal/transcript"
)

const (
	maxChunkSize = 8000
	chunkTarget  = 4000
	chunkOverlap = 200
)

// Result summarizes what a batch run produced.
type Result struct {
	FilesProcessed int
	FilesSkipped   int
	ChunksTotal    int
	FactsTotal     int
	EntitiesTotal  int
	RelsTotal      int
	Errors         []FileError
}

// FileError records a per-file failure.
type FileError struct {
	Path string
	Err  error
}

// BatchAdapter reads files from a directory, checks dedup via content hash,
// chunks large files, and delegates extraction + storage to Engine.Ingest().
type BatchAdapter struct {
	engine *imprint.Engine
	store  db.Store
	logger *slog.Logger
}

// NewBatchAdapter creates a BatchAdapter. The Engine handles extraction
// and storage; the adapter handles file I/O, dedup, and chunking.
func NewBatchAdapter(engine *imprint.Engine, store db.Store, logger *slog.Logger) *BatchAdapter {
	return &BatchAdapter{
		engine: engine,
		store:  store,
		logger: logger,
	}
}

// ProcessDir walks a directory and processes all .txt and .md files.
func (b *BatchAdapter) ProcessDir(ctx context.Context, dir string) (*Result, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read dir %s: %w", dir, err)
	}

	result := &Result{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if ext != ".txt" && ext != ".md" {
			continue
		}

		path := filepath.Join(dir, name)
		processed, err := b.processFile(ctx, dir, path, result)
		if err != nil {
			result.Errors = append(result.Errors, FileError{Path: path, Err: err})
			b.logger.Error("batch ingest file failed", "path", path, "error", err)
			continue
		}
		if !processed {
			result.FilesSkipped++
		} else {
			result.FilesProcessed++
		}
	}

	b.logger.Info("batch ingest complete",
		"files_processed", result.FilesProcessed,
		"files_skipped", result.FilesSkipped,
		"chunks", result.ChunksTotal,
		"facts", result.FactsTotal,
		"errors", len(result.Errors),
	)
	return result, nil
}

// processFile reads, hashes, dedup-checks, chunks, registers transcript, extracts, and stores.
// Returns true if the file was processed, false if skipped (already ingested).
func (b *BatchAdapter) processFile(ctx context.Context, dir, path string, result *Result) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("read file: %w", err)
	}

	content := string(data)
	if strings.TrimSpace(content) == "" {
		return false, nil
	}

	fm, body, err := transcript.ParseFrontmatter(content)
	if err != nil {
		return false, fmt.Errorf("parse frontmatter: %w", err)
	}
	if fm != nil {
		content = body
	}

	relPath, err := filepath.Rel(dir, path)
	if err != nil {
		return false, fmt.Errorf("relative path: %w", err)
	}

	hash := contentHash(data)

	existing, err := b.store.GetIngestedFile(ctx, relPath)
	if err != nil {
		return false, fmt.Errorf("check ingested: %w", err)
	}
	if existing != nil && existing.ContentHash == hash {
		return false, nil
	}

	chunks := chunkText(content)
	result.ChunksTotal += len(chunks)

	existingTr, err := b.store.GetTranscriptByPath(ctx, relPath)
	if err != nil {
		return false, fmt.Errorf("check transcript: %w", err)
	}

	var transcriptID string
	if existingTr != nil {
		transcriptID = existingTr.ID
		if err := b.store.DeleteTranscriptChunks(ctx, transcriptID); err != nil {
			return false, fmt.Errorf("delete old chunks: %w", err)
		}
	} else {
		transcriptID = db.NewID()
		tr := &model.Transcript{
			ID:         transcriptID,
			FilePath:   relPath,
			ChunkCount: len(chunks),
			CreatedAt:  time.Now().UTC(),
		}
		if fm != nil {
			tr.Date = fm.Date
			tr.Participants = fm.Participants
			tr.Topic = fm.Topic
		}
		if err := b.store.CreateTranscript(ctx, tr); err != nil {
			return false, fmt.Errorf("create transcript: %w", err)
		}
	}

	var totalFacts int
	for _, chunk := range chunks {
		if err := b.store.CreateTranscriptChunk(ctx, &model.TranscriptChunk{
			ID:           db.NewID(),
			TranscriptID: transcriptID,
			LineStart:    chunk.StartLine,
			LineEnd:      chunk.EndLine,
			ContentHash:  contentHash([]byte(chunk.Text)),
		}, chunk.Text); err != nil {
			return false, fmt.Errorf("create chunk record: %w", err)
		}

		ir, err := b.engine.Ingest(ctx, chunk.Text, relPath,
			imprint.WithLineOffset(chunk.StartLine, chunk.EndLine))
		if err != nil {
			return false, fmt.Errorf("ingest chunk: %w", err)
		}
		totalFacts += ir.FactsCount
		result.FactsTotal += ir.FactsCount
		result.EntitiesTotal += ir.EntitiesCount
		result.RelsTotal += ir.RelationshipsCount
	}

	if err := b.store.UpsertIngestedFile(ctx, &db.IngestedFile{
		Path:        relPath,
		ContentHash: hash,
		Chunks:      len(chunks),
		FactsCount:  totalFacts,
		ProcessedAt: time.Now(),
	}); err != nil {
		return false, fmt.Errorf("record ingested file: %w", err)
	}

	b.logger.Info("file ingested",
		"path", relPath,
		"chunks", len(chunks),
		"facts", totalFacts,
	)
	return true, nil
}

func contentHash(data []byte) string {
	h := sha256.Sum256(data)
	return fmt.Sprintf("%x", h)
}

// Chunk is a piece of text with its line positions in the original file.
type Chunk struct {
	Text      string
	StartLine int // 1-based
	EndLine   int // inclusive
}

func countLines(s string) int {
	n := strings.Count(s, "\n")
	if len(s) > 0 && !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}

// chunkText splits text into chunks if it exceeds maxChunkSize.
// Small files are returned as a single chunk. Each chunk carries
// the 1-based line range it covers in the original text.
func chunkText(text string) []Chunk {
	totalLines := countLines(text)

	if len(text) <= maxChunkSize {
		return []Chunk{{Text: text, StartLine: 1, EndLine: totalLines}}
	}

	var chunks []Chunk
	lineOffset := 1
	remaining := text

	for len(remaining) > 0 {
		end := chunkTarget
		if end > len(remaining) {
			end = len(remaining)
		}

		if end < len(remaining) {
			if idx := strings.LastIndex(remaining[:end], "\n"); idx > chunkTarget/2 {
				end = idx + 1
			}
		}

		chunkStr := remaining[:end]
		endLine := lineOffset + countLines(chunkStr) - 1

		chunks = append(chunks, Chunk{
			Text:      chunkStr,
			StartLine: lineOffset,
			EndLine:   endLine,
		})

		next := end - chunkOverlap
		if next < 0 {
			next = 0
		}
		if next >= len(remaining) {
			break
		}

		lineOffset += strings.Count(remaining[:next], "\n")
		remaining = remaining[next:]

		if len(remaining) < chunkOverlap*2 {
			lastEndLine := lineOffset + countLines(remaining) - 1
			chunks = append(chunks, Chunk{
				Text:      remaining,
				StartLine: lineOffset,
				EndLine:   lastEndLine,
			})
			break
		}
	}
	return chunks
}
