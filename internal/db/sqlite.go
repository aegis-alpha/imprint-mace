package db

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"strings"
	"time"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	_ "github.com/mattn/go-sqlite3"

	"github.com/aegis-alpha/imprint-mace/internal/fts"
	"github.com/aegis-alpha/imprint-mace/internal/model"
	"github.com/aegis-alpha/imprint-mace/internal/vecindex"
)

//go:embed migrations/*.sql
var migrations embed.FS

type SQLiteStore struct {
	db               *sql.DB
	dbPath           string
	vecIndex         vecindex.VectorIndex
	vectorCapability vecindex.Capability
}

func Open(path string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite3", path+"?_journal_mode=wal&_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	s := &SQLiteStore{
		db:               db,
		dbPath:           path,
		vectorCapability: vecindex.DisabledCapability("vector backend not configured"),
	}
	if err := s.migrate(); err != nil {
		db.Close() //nolint:gosec // best-effort cleanup on migration failure
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *SQLiteStore) Close() error {
	var errs []error
	if s.vecIndex != nil {
		if err := s.vecIndex.Close(); err != nil {
			errs = append(errs, err)
		}
		s.vecIndex = nil
	}
	if err := s.db.Close(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// SetVectorIndex attaches a USearch (or nil) index for vector search and updates.
func (s *SQLiteStore) SetVectorIndex(v vecindex.VectorIndex) {
	s.vecIndex = v
	if v == nil {
		s.vectorCapability = vecindex.DisabledCapability("vector backend detached")
		return
	}
	if !s.vectorCapability.ReadAvailable && !s.vectorCapability.WriteSafe {
		s.vectorCapability = vecindex.Capability{
			Backend:       "custom",
			Mode:          vecindex.ModeReadWrite,
			Status:        vecindex.HealthHealthy,
			ReadAvailable: true,
			WriteSafe:     true,
		}
	}
}

func (s *SQLiteStore) SetVectorCapability(cap vecindex.Capability) {
	s.vectorCapability = cap
}

func (s *SQLiteStore) VectorCapability() vecindex.Capability {
	return s.vectorCapability
}

// AttachVectorIndex opens the on-disk USearch cache next to this database.
// No-op for :memory: or non-positive dimensions.
func (s *SQLiteStore) AttachVectorIndex(dims int) error {
	if dims <= 0 {
		s.vectorCapability = vecindex.DisabledCapability("embedding dimensions not configured")
		return nil
	}
	if s.dbPath == "" || s.dbPath == ":memory:" {
		s.vectorCapability = vecindex.DisabledCapability("vector cache disabled for in-memory database")
		return nil
	}
	if s.vecIndex != nil {
		return errors.New("db: vector index already attached")
	}
	cachePath := strings.TrimSuffix(s.dbPath, ".db") + ".vecindex"
	vi, err := vecindex.OpenVectorIndex(cachePath, dims, func() (map[string][]float32, error) {
		return s.CollectEmbeddingsForVectorIndex(context.Background())
	})
	if err != nil {
		return err
	}
	s.vecIndex = vi
	s.vectorCapability = vecindex.Capability{
		Backend:       "usearch",
		Mode:          vecindex.ModeReadWrite,
		Status:        vecindex.HealthHealthy,
		ReadAvailable: true,
		WriteSafe:     true,
	}
	return nil
}

// CollectEmbeddingsForVectorIndex loads all embeddings from SQLite for USearch rebuild.
func (s *SQLiteStore) CollectEmbeddingsForVectorIndex(ctx context.Context) (map[string][]float32, error) {
	out := make(map[string][]float32)
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, embedding FROM facts
		WHERE embedding IS NOT NULL AND length(embedding) > 0`)
	if err != nil {
		return nil, fmt.Errorf("collect fact embeddings: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var blob []byte
		if err := rows.Scan(&id, &blob); err != nil {
			return nil, err
		}
		v := blobToFloat32(blob)
		if len(v) > 0 {
			out["fact:"+id] = v
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows2, err := s.db.QueryContext(ctx, `
		SELECT id, embedding FROM transcript_chunks
		WHERE embedding IS NOT NULL AND length(embedding) > 0`)
	if err != nil {
		return nil, fmt.Errorf("collect chunk embeddings: %w", err)
	}
	defer rows2.Close()
	for rows2.Next() {
		var id string
		var blob []byte
		if err := rows2.Scan(&id, &blob); err != nil {
			return nil, err
		}
		v := blobToFloat32(blob)
		if len(v) > 0 {
			out["chunk:"+id] = v
		}
	}
	if err := rows2.Err(); err != nil {
		return nil, err
	}

	rows3, err := s.db.QueryContext(ctx, `
		SELECT id, embedding FROM hot_messages
		WHERE embedding IS NOT NULL AND length(embedding) > 0`)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return out, nil
		}
		return nil, fmt.Errorf("collect hot embeddings: %w", err)
	}
	defer rows3.Close()
	for rows3.Next() {
		var id string
		var blob []byte
		if err := rows3.Scan(&id, &blob); err != nil {
			return nil, err
		}
		v := blobToFloat32(blob)
		if len(v) > 0 {
			out["hot:"+id] = v
		}
	}
	if err := rows3.Err(); err != nil {
		return nil, err
	}

	rows4, err := s.db.QueryContext(ctx, `
		SELECT id, embedding FROM cooldown_messages
		WHERE embedding IS NOT NULL AND length(embedding) > 0`)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return out, nil
		}
		return nil, fmt.Errorf("collect cooldown embeddings: %w", err)
	}
	defer rows4.Close()
	for rows4.Next() {
		var id string
		var blob []byte
		if err := rows4.Scan(&id, &blob); err != nil {
			return nil, err
		}
		v := blobToFloat32(blob)
		if len(v) > 0 {
			out["cool:"+id] = v
		}
	}
	return out, rows4.Err()
}

// ReloadVectorIndex rebuilds the in-memory ANN index from SQLite (e.g. after admin reset).
func (s *SQLiteStore) ReloadVectorIndex(ctx context.Context) error {
	if s.vecIndex == nil {
		return nil
	}
	m, err := s.CollectEmbeddingsForVectorIndex(ctx)
	if err != nil {
		return err
	}
	if err := s.vecIndex.ResetFromEmbeddings(m); err != nil {
		return err
	}
	return s.vecIndex.Save()
}

// RawDB returns the underlying *sql.DB for aggregate queries that
// don't fit the Store interface (e.g. taxonomy signal collection).
func (s *SQLiteStore) RawDB() *sql.DB {
	return s.db
}

func (s *SQLiteStore) migrate() error {
	sqlite_vec.Auto()
	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		return err
	}
	for _, e := range entries {
		data, err := migrations.ReadFile("migrations/" + e.Name())
		if err != nil {
			return err
		}
		if _, err := s.db.Exec(string(data)); err != nil {
			if !isIgnorableMigrationError(err) {
				return fmt.Errorf("apply %s: %w", e.Name(), err)
			}
			for _, stmt := range splitMigrationStatements(string(data)) {
				if _, err := s.db.Exec(stmt); err != nil {
					if !isIgnorableMigrationError(err) {
						return fmt.Errorf("apply %s: %w", e.Name(), err)
					}
				}
			}
		}
	}
	if err := s.backfillChunkEmbeddingsIfNeeded(); err != nil {
		return fmt.Errorf("backfill chunk embeddings: %w", err)
	}
	return nil
}

func (s *SQLiteStore) backfillChunkEmbeddingsIfNeeded() error {
	ctx := context.Background()
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='chunks_vec'`).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT chunk_id, embedding FROM chunks_vec`)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return nil
		}
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var chunkID string
		var blob []byte
		if err := rows.Scan(&chunkID, &blob); err != nil {
			return err
		}
		vec := blobToFloat32(blob)
		if len(vec) == 0 {
			continue
		}
		emb := float32ToBlob(vec)
		if _, err := s.db.ExecContext(ctx,
			`UPDATE transcript_chunks SET embedding = ? WHERE id = ? AND embedding IS NULL`,
			emb, chunkID); err != nil {
			return err
		}
	}
	return rows.Err()
}

func splitMigrationStatements(sql string) []string {
	var stmts []string
	var current strings.Builder
	for _, line := range strings.Split(sql, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "--") {
			continue
		}
		current.WriteString(line)
		current.WriteString("\n")
		if strings.HasSuffix(trimmed, ";") {
			s := strings.TrimSpace(current.String())
			if s != "" && s != ";" {
				stmts = append(stmts, s)
			}
			current.Reset()
		}
	}
	if s := strings.TrimSpace(current.String()); s != "" && s != ";" {
		stmts = append(stmts, s)
	}
	return stmts
}

func isIgnorableMigrationError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "duplicate column name") ||
		strings.Contains(msg, "already exists")
}

// --- Facts ---

func (s *SQLiteStore) CreateFact(ctx context.Context, f *model.Fact) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	var lines *string
	if f.Source.LineRange != nil {
		v := fmt.Sprintf("%d-%d", f.Source.LineRange[0], f.Source.LineRange[1])
		lines = &v
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO facts (id, source_file, source_lines, source_ts, fact_type, subject,
			content, confidence, valid_from, valid_until, superseded_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		f.ID, f.Source.TranscriptFile, lines, timePtr(f.Source.Timestamp),
		string(f.FactType), f.Subject, f.Content, f.Confidence,
		timePtr(f.Validity.ValidFrom), timePtr(f.Validity.ValidUntil),
		nilIfEmpty(f.SupersededBy), timeStr(f.CreatedAt),
	); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO facts_fts(fact_id, content) VALUES (?, ?)", f.ID, f.Content,
	); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLiteStore) GetFact(ctx context.Context, id string) (*model.Fact, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, source_file, source_lines, source_ts, fact_type, subject,
			content, confidence, valid_from, valid_until, superseded_by, created_at, supersede_reason
		FROM facts WHERE id = ?`, id)
	f, err := scanFact(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	return f, err
}

func (s *SQLiteStore) ListFacts(ctx context.Context, filter FactFilter) ([]model.Fact, error) {
	q := "SELECT id, source_file, source_lines, source_ts, fact_type, subject, content, confidence, valid_from, valid_until, superseded_by, created_at, supersede_reason FROM facts WHERE 1=1"
	var args []any
	if filter.FactType != "" {
		q += " AND fact_type = ?"
		args = append(args, filter.FactType)
	}
	if filter.Subject != "" {
		q += " AND subject = ?"
		args = append(args, filter.Subject)
	}
	if filter.NotSuperseded {
		q += " AND superseded_by IS NULL AND supersede_reason IS NULL"
	}
	if filter.CreatedAfter != nil {
		q += " AND created_at > ?"
		args = append(args, filter.CreatedAfter.Format(time.RFC3339))
	}
	q += " ORDER BY created_at DESC"
	if filter.Limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", filter.Limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var facts []model.Fact
	for rows.Next() {
		f, err := scanFactRow(rows)
		if err != nil {
			return nil, err
		}
		facts = append(facts, *f)
	}
	return facts, rows.Err()
}

func (s *SQLiteStore) SupersedeFact(ctx context.Context, oldID, newID string) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE facts SET superseded_by = ? WHERE id = ?", newID, oldID)
	return err
}

func (s *SQLiteStore) SupersedeFactByContradiction(ctx context.Context, oldID, newID string, reason string, validUntil time.Time) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE facts SET superseded_by = ?, supersede_reason = ?, valid_until = ?
		WHERE id = ? AND (superseded_by IS NULL OR superseded_by = '')`,
		newID, reason, timeStr(validUntil.UTC()), oldID)
	if err != nil {
		return fmt.Errorf("supersede fact by contradiction: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLiteStore) UpdateFact(ctx context.Context, factID string, update FactUpdate) error {
	var setClauses []string
	var args []any

	if update.Confidence != nil {
		setClauses = append(setClauses, "confidence = ?")
		args = append(args, *update.Confidence)
	}
	if update.ValidUntil != nil {
		setClauses = append(setClauses, "valid_until = ?")
		args = append(args, timeStr(*update.ValidUntil))
	}
	if update.Subject != nil {
		setClauses = append(setClauses, "subject = ?")
		args = append(args, *update.Subject)
	}

	if len(setClauses) == 0 {
		return nil
	}

	args = append(args, factID)
	q := "UPDATE facts SET " + strings.Join(setClauses, ", ") + " WHERE id = ?"
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("update fact: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLiteStore) SupersedeWithContent(ctx context.Context, oldFactID string, newContent string, source string) (*model.Fact, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	row := tx.QueryRowContext(ctx, `
		SELECT id, source_file, source_lines, source_ts, fact_type, subject,
			content, confidence, valid_from, valid_until, superseded_by, created_at, supersede_reason
		FROM facts WHERE id = ?`, oldFactID)
	old, err := scanFact(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get old fact %s: %w", oldFactID, err)
	}

	newFact := &model.Fact{
		ID:         NewID(),
		Source:     model.Source{TranscriptFile: source},
		FactType:   old.FactType,
		Subject:    old.Subject,
		Content:    newContent,
		Confidence: old.Confidence,
		CreatedAt:  time.Now().UTC(),
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO facts (id, source_file, source_lines, source_ts, fact_type, subject,
			content, confidence, valid_from, valid_until, superseded_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		newFact.ID, newFact.Source.TranscriptFile, nil, nil,
		string(newFact.FactType), newFact.Subject, newFact.Content, newFact.Confidence,
		nil, nil, nil, timeStr(newFact.CreatedAt),
	); err != nil {
		return nil, fmt.Errorf("insert new fact: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		"INSERT INTO facts_fts(fact_id, content) VALUES (?, ?)",
		newFact.ID, newFact.Content,
	); err != nil {
		return nil, fmt.Errorf("insert new fact fts: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		"UPDATE facts SET superseded_by = ? WHERE id = ?",
		newFact.ID, oldFactID,
	); err != nil {
		return nil, fmt.Errorf("supersede old fact: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return newFact, nil
}

func (s *SQLiteStore) UpdateFactEmbedding(ctx context.Context, factID string, embedding []float32, modelName string) error {
	if s.vecIndex != nil && !s.vectorCapability.WriteSafe {
		return fmt.Errorf("%w: backend=%s mode=%s detail=%s",
			ErrVectorWriteUnsafe, s.vectorCapability.Backend, s.vectorCapability.Mode, s.vectorCapability.Detail)
	}
	blob := float32ToBlob(embedding)
	_, err := s.db.ExecContext(ctx,
		"UPDATE facts SET embedding = ?, embedding_model = ? WHERE id = ?", blob, modelName, factID)
	if err != nil {
		return err
	}
	// Vector index updated after DB commit (best-effort, index is cache)
	if s.vecIndex != nil {
		if err := s.vecIndex.Add("fact:"+factID, embedding); err != nil {
			// Log but don't fail -- index can be rebuilt from DB
			return fmt.Errorf("vector index add fact: %w (index out of sync, rebuild recommended)", err)
		}
	}
	return nil
}

func float32ToBlob(v []float32) []byte {
	buf := make([]byte, len(v)*4)
	for i, f := range v {
		bits := math.Float32bits(f)
		buf[i*4] = byte(bits)
		buf[i*4+1] = byte(bits >> 8)
		buf[i*4+2] = byte(bits >> 16)
		buf[i*4+3] = byte(bits >> 24)
	}
	return buf
}

// --- Embeddings ---

func (s *SQLiteStore) ListFactEmbeddings(ctx context.Context, factType string) ([][]float32, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT f.embedding FROM facts f
		WHERE f.fact_type = ? AND f.embedding IS NOT NULL AND length(f.embedding) > 0`, factType)
	if err != nil {
		return nil, fmt.Errorf("list fact embeddings: %w", err)
	}
	defer rows.Close()

	var vecs [][]float32
	for rows.Next() {
		var blob []byte
		if err := rows.Scan(&blob); err != nil {
			return nil, err
		}
		vec := blobToFloat32(blob)
		if len(vec) > 0 {
			vecs = append(vecs, vec)
		}
	}
	return vecs, rows.Err()
}

func blobToFloat32(b []byte) []float32 {
	if len(b)%4 != 0 {
		return nil
	}
	vec := make([]float32, len(b)/4)
	for i := range vec {
		bits := uint32(b[i*4]) | uint32(b[i*4+1])<<8 | uint32(b[i*4+2])<<16 | uint32(b[i*4+3])<<24
		vec[i] = math.Float32frombits(bits)
	}
	return vec
}

// --- Search ---

func (s *SQLiteStore) SearchByVector(ctx context.Context, embedding []float32, limit int) ([]ScoredFact, error) {
	if s.vecIndex == nil {
		return nil, nil
	}
	if !s.vectorCapability.ReadAvailable {
		return nil, ErrVectorReadUnavailable
	}
	const pref = "fact:"
	hits, err := s.vecIndex.SearchWithPrefix(embedding, limit, pref)
	if err != nil {
		return nil, fmt.Errorf("vector search: %w", err)
	}
	var results []ScoredFact
	for _, h := range hits {
		factID := strings.TrimPrefix(h.ID, pref)
		if factID == h.ID {
			continue
		}
		fact, err := s.GetFact(ctx, factID)
		if err != nil {
			continue
		}
		results = append(results, ScoredFact{
			Fact:  *fact,
			Score: 1 - h.Distance,
		})
	}
	return results, nil
}

func (s *SQLiteStore) SearchByText(ctx context.Context, query string, limit int) ([]ScoredFact, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT fts.fact_id, bm25(facts_fts) AS score
		FROM facts_fts fts
		WHERE fts.content MATCH ?
		ORDER BY score
		LIMIT ?`, query, limit)
	if err != nil {
		return nil, fmt.Errorf("text search: %w", err)
	}
	defer rows.Close()

	var results []ScoredFact
	for rows.Next() {
		var factID string
		var bm25score float64
		if err := rows.Scan(&factID, &bm25score); err != nil {
			return nil, err
		}
		fact, err := s.GetFact(ctx, factID)
		if err != nil {
			continue
		}
		results = append(results, ScoredFact{
			Fact:  *fact,
			Score: -bm25score,
		})
	}
	return results, rows.Err()
}

func (s *SQLiteStore) ListFactsWithoutEmbedding(ctx context.Context) ([]model.Fact, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, source_file, source_lines, source_ts, fact_type, subject,
			content, confidence, valid_from, valid_until, superseded_by, created_at, supersede_reason
		FROM facts WHERE embedding IS NULL
		ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var facts []model.Fact
	for rows.Next() {
		f, err := scanFactRow(rows)
		if err != nil {
			return nil, err
		}
		facts = append(facts, *f)
	}
	return facts, rows.Err()
}

func (s *SQLiteStore) ListFactsByEmbeddingModel(ctx context.Context, modelName string) ([]model.Fact, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, source_file, source_lines, source_ts, fact_type, subject,
			content, confidence, valid_from, valid_until, superseded_by, created_at, supersede_reason
		FROM facts WHERE embedding_model = ?
		ORDER BY created_at DESC`, modelName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var facts []model.Fact
	for rows.Next() {
		f, err := scanFactRow(rows)
		if err != nil {
			return nil, err
		}
		facts = append(facts, *f)
	}
	return facts, rows.Err()
}

// --- Entities ---

func (s *SQLiteStore) CreateEntity(ctx context.Context, e *model.Entity) error {
	aliasJSON, _ := json.Marshal(e.Aliases)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO entities (id, name, entity_type, aliases, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		e.ID, e.Name, string(e.EntityType), string(aliasJSON), timeStr(e.CreatedAt),
	)
	return err
}

func (s *SQLiteStore) GetEntity(ctx context.Context, id string) (*model.Entity, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, entity_type, aliases, created_at
		FROM entities WHERE id = ?`, id)
	e, err := scanEntity(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	return e, err
}

func (s *SQLiteStore) GetEntityByName(ctx context.Context, name string) (*model.Entity, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, entity_type, aliases, created_at
		FROM entities WHERE name = ? COLLATE NOCASE`, name)
	e, err := scanEntity(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	return e, err
}

func (s *SQLiteStore) UpdateEntityAliases(ctx context.Context, entityID string, aliases []string) error {
	aliasJSON, err := json.Marshal(aliases)
	if err != nil {
		return fmt.Errorf("marshal aliases: %w", err)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE entities SET aliases = ? WHERE id = ?`, string(aliasJSON), entityID)
	if err != nil {
		return fmt.Errorf("update entity aliases: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLiteStore) ListEntities(ctx context.Context, filter EntityFilter) ([]model.Entity, error) {
	q := "SELECT id, name, entity_type, aliases, created_at FROM entities WHERE 1=1"
	var args []any
	if filter.EntityType != "" {
		q += " AND entity_type = ?"
		args = append(args, filter.EntityType)
	}
	q += " ORDER BY name"
	if filter.Limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", filter.Limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entities []model.Entity
	for rows.Next() {
		e, err := scanEntityRow(rows)
		if err != nil {
			return nil, err
		}
		entities = append(entities, *e)
	}
	return entities, rows.Err()
}

// --- Relationships ---

func (s *SQLiteStore) CreateRelationship(ctx context.Context, r *model.Relationship) error {
	propsJSON, _ := json.Marshal(r.Properties)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO relationships (id, from_entity, to_entity, relation_type, properties, source_fact, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.FromEntity, r.ToEntity, string(r.RelationType),
		string(propsJSON), nilIfEmpty(r.SourceFact), timeStr(r.CreatedAt),
	)
	return err
}

func (s *SQLiteStore) ListRelationships(ctx context.Context, filter RelFilter) ([]model.Relationship, error) {
	q := "SELECT id, from_entity, to_entity, relation_type, properties, source_fact, created_at FROM relationships WHERE 1=1"
	var args []any
	if filter.EntityID != "" {
		q += " AND (from_entity = ? OR to_entity = ?)"
		args = append(args, filter.EntityID, filter.EntityID)
	}
	if filter.RelationType != "" {
		q += " AND relation_type = ?"
		args = append(args, filter.RelationType)
	}
	q += " ORDER BY created_at DESC"
	if filter.Limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", filter.Limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var rels []model.Relationship
	for rows.Next() {
		var r model.Relationship
		var propsJSON, sourceFact sql.NullString
		var createdAtStr string
		if err := rows.Scan(&r.ID, &r.FromEntity, &r.ToEntity, &r.RelationType, &propsJSON, &sourceFact, &createdAtStr); err != nil {
			return nil, err
		}
		if t := parseTime(createdAtStr); t != nil {
			r.CreatedAt = *t
		}
		if propsJSON.Valid {
			if err := json.Unmarshal([]byte(propsJSON.String), &r.Properties); err != nil {
				return nil, fmt.Errorf("unmarshal relationship properties: %w", err)
			}
		}
		r.SourceFact = sourceFact.String
		rels = append(rels, r)
	}
	return rels, rows.Err()
}

// --- Consolidations ---

func (s *SQLiteStore) CreateConsolidation(ctx context.Context, c *model.Consolidation) error {
	idsJSON, _ := json.Marshal(c.SourceFactIDs)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO consolidations (id, source_fact_ids, summary, insight, importance, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		c.ID, string(idsJSON), c.Summary, c.Insight, c.Importance, timeStr(c.CreatedAt),
	)
	return err
}

func (s *SQLiteStore) ListConsolidations(ctx context.Context, limit int) ([]model.Consolidation, error) {
	q := "SELECT id, source_fact_ids, summary, insight, importance, created_at FROM consolidations ORDER BY created_at DESC"
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var cons []model.Consolidation
	for rows.Next() {
		var c model.Consolidation
		var idsJSON, createdAtStr string
		if err := rows.Scan(&c.ID, &idsJSON, &c.Summary, &c.Insight, &c.Importance, &createdAtStr); err != nil {
			return nil, err
		}
		if t := parseTime(createdAtStr); t != nil {
			c.CreatedAt = *t
		}
		if err := json.Unmarshal([]byte(idsJSON), &c.SourceFactIDs); err != nil {
			return nil, fmt.Errorf("unmarshal consolidation source_fact_ids: %w", err)
		}
		cons = append(cons, c)
	}
	return cons, rows.Err()
}

func (s *SQLiteStore) ListUnconsolidatedFacts(ctx context.Context, limit int) ([]model.Fact, error) {
	q := `SELECT id, source_file, source_lines, source_ts, fact_type, subject,
		content, confidence, valid_from, valid_until, superseded_by, created_at, supersede_reason
		FROM facts
		WHERE id NOT IN (
			SELECT value FROM consolidations, json_each(consolidations.source_fact_ids)
		)
		ORDER BY created_at DESC`
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var facts []model.Fact
	for rows.Next() {
		f, err := scanFactRow(rows)
		if err != nil {
			return nil, err
		}
		facts = append(facts, *f)
	}
	return facts, rows.Err()
}

// --- Fact connections ---

func (s *SQLiteStore) CreateFactConnection(ctx context.Context, fc *model.FactConnection) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO fact_connections (id, fact_a, fact_b, connection_type, strength, consolidation_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		fc.ID, fc.FactA, fc.FactB, string(fc.ConnectionType),
		fc.Strength, nilIfEmpty(fc.ConsolidationID), timeStr(fc.CreatedAt),
	)
	return err
}

func (s *SQLiteStore) ListFactConnections(ctx context.Context, factID string) ([]model.FactConnection, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, fact_a, fact_b, connection_type, strength, consolidation_id, created_at
		FROM fact_connections WHERE fact_a = ? OR fact_b = ?
		ORDER BY created_at DESC`, factID, factID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var fcs []model.FactConnection
	for rows.Next() {
		var fc model.FactConnection
		var consID sql.NullString
		var createdAtStr string
		if err := rows.Scan(&fc.ID, &fc.FactA, &fc.FactB, &fc.ConnectionType, &fc.Strength, &consID, &createdAtStr); err != nil {
			return nil, err
		}
		if t := parseTime(createdAtStr); t != nil {
			fc.CreatedAt = *t
		}
		fc.ConsolidationID = consID.String
		fcs = append(fcs, fc)
	}
	return fcs, rows.Err()
}

func (s *SQLiteStore) ListAllFactConnections(ctx context.Context, limit int) ([]model.FactConnection, error) {
	q := "SELECT id, fact_a, fact_b, connection_type, strength, consolidation_id, created_at FROM fact_connections ORDER BY created_at DESC"
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var fcs []model.FactConnection
	for rows.Next() {
		var fc model.FactConnection
		var consID sql.NullString
		var createdAtStr string
		if err := rows.Scan(&fc.ID, &fc.FactA, &fc.FactB, &fc.ConnectionType, &fc.Strength, &consID, &createdAtStr); err != nil {
			return nil, err
		}
		if t := parseTime(createdAtStr); t != nil {
			fc.CreatedAt = *t
		}
		fc.ConsolidationID = consID.String
		fcs = append(fcs, fc)
	}
	return fcs, rows.Err()
}

// --- Ingested files ---

func (s *SQLiteStore) GetIngestedFile(ctx context.Context, path string) (*IngestedFile, error) {
	row := s.db.QueryRowContext(ctx,
		"SELECT path, content_hash, chunks, facts_count, processed_at FROM ingested_files WHERE path = ?", path)
	var f IngestedFile
	var processedAtStr string
	err := row.Scan(&f.Path, &f.ContentHash, &f.Chunks, &f.FactsCount, &processedAtStr)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if t := parseTime(processedAtStr); t != nil {
		f.ProcessedAt = *t
	}
	return &f, nil
}

func (s *SQLiteStore) UpsertIngestedFile(ctx context.Context, f *IngestedFile) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO ingested_files (path, content_hash, chunks, facts_count, processed_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(path) DO UPDATE SET
			content_hash = excluded.content_hash,
			chunks = excluded.chunks,
			facts_count = excluded.facts_count,
			processed_at = excluded.processed_at`,
		f.Path, f.ContentHash, f.Chunks, f.FactsCount, timeStr(f.ProcessedAt),
	)
	return err
}

// --- Extraction log ---

func (s *SQLiteStore) CreateExtractionLog(ctx context.Context, l *ExtractionLog) error {
	success := 0
	if l.Success {
		success = 1
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO extraction_log (id, provider_name, model, input_length, tokens_used,
			duration_ms, success, facts_count, entities_count, relationships_count,
			entity_collisions, entity_creations,
			error_type, error_message, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		l.ID, l.ProviderName, l.Model, l.InputLength, l.TokensUsed,
		l.DurationMs, success, l.FactsCount, l.EntitiesCount, l.RelationshipsCount,
		l.EntityCollisions, l.EntityCreations,
		nilIfEmpty(l.ErrorType), nilIfEmpty(l.ErrorMessage), timeStr(l.CreatedAt),
	)
	return err
}

func (s *SQLiteStore) ListExtractionLogs(ctx context.Context, limit int) ([]ExtractionLog, error) {
	q := "SELECT id, provider_name, model, input_length, tokens_used, duration_ms, success, facts_count, entities_count, relationships_count, entity_collisions, entity_creations, error_type, error_message, created_at FROM extraction_log ORDER BY created_at DESC"
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []ExtractionLog
	for rows.Next() {
		var l ExtractionLog
		var success int
		var errType, errMsg sql.NullString
		var createdAtStr string
		if err := rows.Scan(&l.ID, &l.ProviderName, &l.Model, &l.InputLength, &l.TokensUsed,
			&l.DurationMs, &success, &l.FactsCount, &l.EntitiesCount, &l.RelationshipsCount,
			&l.EntityCollisions, &l.EntityCreations,
			&errType, &errMsg, &createdAtStr); err != nil {
			return nil, err
		}
		l.Success = success == 1
		l.ErrorType = errType.String
		l.ErrorMessage = errMsg.String
		if t := parseTime(createdAtStr); t != nil {
			l.CreatedAt = *t
		}
		logs = append(logs, l)
	}
	return logs, rows.Err()
}

func (s *SQLiteStore) UpdateExtractionLogCollisions(ctx context.Context, logID string, collisions, creations int) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE extraction_log SET entity_collisions = ?, entity_creations = ? WHERE id = ?",
		collisions, creations, logID)
	return err
}

// --- Taxonomy signals ---

func (s *SQLiteStore) CreateTaxonomySignal(ctx context.Context, sig *TaxonomySignal) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO taxonomy_signals (id, signal_type, type_category, type_name, count, details, created_at, resolved_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		sig.ID, sig.SignalType, sig.TypeCategory, sig.TypeName,
		sig.Count, sig.Details, timeStr(sig.CreatedAt), nilIfEmpty(sig.ResolvedBy),
	)
	return err
}

func (s *SQLiteStore) ListTaxonomySignals(ctx context.Context, resolved bool, limit int) ([]TaxonomySignal, error) {
	q := "SELECT id, signal_type, type_category, type_name, count, details, created_at, resolved_by FROM taxonomy_signals"
	if resolved {
		q += " WHERE resolved_by IS NOT NULL"
	} else {
		q += " WHERE resolved_by IS NULL"
	}
	q += " ORDER BY created_at DESC"
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var signals []TaxonomySignal
	for rows.Next() {
		var sig TaxonomySignal
		var createdAtStr string
		var resolvedBy sql.NullString
		if err := rows.Scan(&sig.ID, &sig.SignalType, &sig.TypeCategory, &sig.TypeName,
			&sig.Count, &sig.Details, &createdAtStr, &resolvedBy); err != nil {
			return nil, err
		}
		if t := parseTime(createdAtStr); t != nil {
			sig.CreatedAt = *t
		}
		sig.ResolvedBy = resolvedBy.String
		signals = append(signals, sig)
	}
	return signals, rows.Err()
}

// --- Taxonomy proposals ---

func (s *SQLiteStore) CreateTaxonomyProposal(ctx context.Context, p *TaxonomyProposal) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO taxonomy_proposals (id, action, type_category, type_name, definition,
			rationale, status, shadow_results, signal_ids, created_at, resolved_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Action, p.TypeCategory, p.TypeName, p.Definition,
		p.Rationale, p.Status, p.ShadowResults, p.SignalIDs,
		timeStr(p.CreatedAt), timePtr(p.ResolvedAt),
	)
	return err
}

func (s *SQLiteStore) ListTaxonomyProposals(ctx context.Context, status string, limit int) ([]TaxonomyProposal, error) {
	q := "SELECT id, action, type_category, type_name, definition, rationale, status, shadow_results, signal_ids, created_at, resolved_at FROM taxonomy_proposals"
	var args []any
	if status != "" {
		q += " WHERE status = ?"
		args = append(args, status)
	}
	q += " ORDER BY created_at DESC"
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var proposals []TaxonomyProposal
	for rows.Next() {
		var p TaxonomyProposal
		var createdAtStr string
		var resolvedAt sql.NullString
		if err := rows.Scan(&p.ID, &p.Action, &p.TypeCategory, &p.TypeName,
			&p.Definition, &p.Rationale, &p.Status, &p.ShadowResults,
			&p.SignalIDs, &createdAtStr, &resolvedAt); err != nil {
			return nil, err
		}
		if t := parseTime(createdAtStr); t != nil {
			p.CreatedAt = *t
		}
		p.ResolvedAt = parseTimePtr(resolvedAt)
		proposals = append(proposals, p)
	}
	return proposals, rows.Err()
}

func (s *SQLiteStore) UpdateTaxonomyProposalStatus(ctx context.Context, id, status, shadowResults string, resolvedAt *time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE taxonomy_proposals
		SET status = ?, shadow_results = ?, resolved_at = ?
		WHERE id = ?`,
		status, shadowResults, timePtr(resolvedAt), id,
	)
	return err
}

// --- Transcripts (D22) ---

func (s *SQLiteStore) CreateTranscript(ctx context.Context, t *model.Transcript) error {
	participantsJSON, _ := json.Marshal(t.Participants)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO transcripts (id, file_path, date, participants, topic, platform_session_id, chunk_count, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.FilePath, timePtr(t.Date), string(participantsJSON),
		nilIfEmpty(t.Topic), nilIfEmpty(t.PlatformSessionID), t.ChunkCount, timeStr(t.CreatedAt),
	)
	return err
}

func (s *SQLiteStore) GetTranscript(ctx context.Context, id string) (*model.Transcript, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, file_path, date, participants, topic, platform_session_id, chunk_count, created_at
		FROM transcripts WHERE id = ?`, id)
	return scanTranscript(row)
}

func (s *SQLiteStore) GetTranscriptByPath(ctx context.Context, filePath string) (*model.Transcript, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, file_path, date, participants, topic, platform_session_id, chunk_count, created_at
		FROM transcripts WHERE file_path = ?`, filePath)
	tr, err := scanTranscript(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return tr, err
}

func (s *SQLiteStore) CreateTranscriptChunk(ctx context.Context, c *model.TranscriptChunk, text string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO transcript_chunks (id, transcript_id, line_start, line_end, content_hash, embedding_model)
		VALUES (?, ?, ?, ?, ?, ?)`,
		c.ID, c.TranscriptID, c.LineStart, c.LineEnd, c.ContentHash, nilIfEmpty(c.EmbeddingModel),
	); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO transcript_chunks_fts(chunk_id, content) VALUES (?, ?)", c.ID, text,
	); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLiteStore) ListTranscriptChunks(ctx context.Context, transcriptID string) ([]model.TranscriptChunk, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, transcript_id, line_start, line_end, content_hash, embedding_model
		FROM transcript_chunks WHERE transcript_id = ?
		ORDER BY line_start`, transcriptID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var chunks []model.TranscriptChunk
	for rows.Next() {
		var c model.TranscriptChunk
		var embModel sql.NullString
		if err := rows.Scan(&c.ID, &c.TranscriptID, &c.LineStart, &c.LineEnd, &c.ContentHash, &embModel); err != nil {
			return nil, err
		}
		c.EmbeddingModel = embModel.String
		chunks = append(chunks, c)
	}
	return chunks, rows.Err()
}

func (s *SQLiteStore) DeleteTranscriptChunks(ctx context.Context, transcriptID string) error {
	rows, err := s.db.QueryContext(ctx,
		"SELECT id FROM transcript_chunks WHERE transcript_id = ?", transcriptID)
	if err != nil {
		return err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx,
		"DELETE FROM transcript_chunks WHERE transcript_id = ?", transcriptID); err != nil {
		return err
	}
	if s.vecIndex != nil {
		const pref = "chunk:"
		for _, id := range ids {
			_ = s.vecIndex.Remove(pref + id) //nolint:errcheck // best-effort
		}
	}
	return nil
}

func (s *SQLiteStore) UpdateChunkEmbedding(ctx context.Context, chunkID string, embedding []float32, modelName string) error {
	if s.vecIndex != nil && !s.vectorCapability.WriteSafe {
		return fmt.Errorf("%w: backend=%s mode=%s detail=%s",
			ErrVectorWriteUnsafe, s.vectorCapability.Backend, s.vectorCapability.Mode, s.vectorCapability.Detail)
	}
	blob := float32ToBlob(embedding)
	_, err := s.db.ExecContext(ctx,
		"UPDATE transcript_chunks SET embedding = ?, embedding_model = ? WHERE id = ?", blob, modelName, chunkID)
	if err != nil {
		return err
	}
	// Vector index updated after DB commit (best-effort, index is cache)
	if s.vecIndex != nil {
		if err := s.vecIndex.Add("chunk:"+chunkID, embedding); err != nil {
			return fmt.Errorf("vector index add chunk: %w (index out of sync, rebuild recommended)", err)
		}
	}
	return nil
}

func (s *SQLiteStore) SearchChunksByVector(ctx context.Context, embedding []float32, limit int) ([]ScoredChunk, error) {
	if s.vecIndex == nil {
		return nil, nil
	}
	if !s.vectorCapability.ReadAvailable {
		return nil, ErrVectorReadUnavailable
	}
	const pref = "chunk:"
	hits, err := s.vecIndex.SearchWithPrefix(embedding, limit, pref)
	if err != nil {
		return nil, fmt.Errorf("chunk vector search: %w", err)
	}
	var results []ScoredChunk
	for _, h := range hits {
		chunkID := strings.TrimPrefix(h.ID, pref)
		if chunkID == h.ID {
			continue
		}
		row := s.db.QueryRowContext(ctx, `
			SELECT id, transcript_id, line_start, line_end, content_hash, embedding_model
			FROM transcript_chunks WHERE id = ?`, chunkID)
		var c model.TranscriptChunk
		var embModel sql.NullString
		if err := row.Scan(&c.ID, &c.TranscriptID, &c.LineStart, &c.LineEnd, &c.ContentHash, &embModel); err != nil {
			continue
		}
		c.EmbeddingModel = embModel.String
		results = append(results, ScoredChunk{
			Chunk: c,
			Score: 1 - h.Distance,
		})
	}
	return results, nil
}

func (s *SQLiteStore) SearchChunksByText(ctx context.Context, query string, limit int) ([]ScoredChunk, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT fts.chunk_id, bm25(transcript_chunks_fts) AS score
		FROM transcript_chunks_fts fts
		WHERE fts.content MATCH ?
		ORDER BY score
		LIMIT ?`, query, limit)
	if err != nil {
		return nil, fmt.Errorf("chunk text search: %w", err)
	}
	defer rows.Close()

	var results []ScoredChunk
	for rows.Next() {
		var chunkID string
		var bm25score float64
		if err := rows.Scan(&chunkID, &bm25score); err != nil {
			return nil, err
		}
		row := s.db.QueryRowContext(ctx, `
			SELECT id, transcript_id, line_start, line_end, content_hash, embedding_model
			FROM transcript_chunks WHERE id = ?`, chunkID)
		var c model.TranscriptChunk
		var embModel sql.NullString
		if err := row.Scan(&c.ID, &c.TranscriptID, &c.LineStart, &c.LineEnd, &c.ContentHash, &embModel); err != nil {
			continue
		}
		c.EmbeddingModel = embModel.String
		results = append(results, ScoredChunk{
			Chunk: c,
			Score: -bm25score,
		})
	}
	return results, rows.Err()
}

func (s *SQLiteStore) ListChunksWithoutEmbedding(ctx context.Context) ([]model.TranscriptChunk, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, transcript_id, line_start, line_end, content_hash, embedding_model
		FROM transcript_chunks WHERE embedding_model IS NULL
		ORDER BY line_start`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanChunkRows(rows)
}

func (s *SQLiteStore) ListChunksByEmbeddingModel(ctx context.Context, modelName string) ([]model.TranscriptChunk, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, transcript_id, line_start, line_end, content_hash, embedding_model
		FROM transcript_chunks WHERE embedding_model = ?
		ORDER BY line_start`, modelName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanChunkRows(rows)
}

func scanChunkRows(rows *sql.Rows) ([]model.TranscriptChunk, error) {
	var chunks []model.TranscriptChunk
	for rows.Next() {
		var c model.TranscriptChunk
		var embModel sql.NullString
		if err := rows.Scan(&c.ID, &c.TranscriptID, &c.LineStart, &c.LineEnd, &c.ContentHash, &embModel); err != nil {
			return nil, err
		}
		c.EmbeddingModel = embModel.String
		chunks = append(chunks, c)
	}
	return chunks, rows.Err()
}

// --- Scan helpers ---

type scanner interface {
	Scan(dest ...any) error
}

func scanFact(row scanner) (*model.Fact, error) {
	var f model.Fact
	var lines, sourceTS, validFrom, validUntil, superseded, supersedeReason sql.NullString
	var createdAtStr string
	err := row.Scan(&f.ID, &f.Source.TranscriptFile, &lines, &sourceTS,
		&f.FactType, &f.Subject, &f.Content, &f.Confidence,
		&validFrom, &validUntil, &superseded, &createdAtStr, &supersedeReason)
	if err != nil {
		return nil, err
	}
	if t := parseTime(createdAtStr); t != nil {
		f.CreatedAt = *t
	}
	if lines.Valid {
		f.Source.LineRange = parseLineRange(lines.String)
	}
	if sourceTS.Valid {
		f.Source.Timestamp = parseTime(sourceTS.String)
	}
	f.Validity.ValidFrom = parseTimePtr(validFrom)
	f.Validity.ValidUntil = parseTimePtr(validUntil)
	f.SupersededBy = superseded.String
	f.SupersedeReason = supersedeReason.String
	return &f, nil
}

func scanFactRow(rows *sql.Rows) (*model.Fact, error) { return scanFact(rows) }

func scanEntity(row scanner) (*model.Entity, error) {
	var e model.Entity
	var aliasJSON, createdAtStr string
	err := row.Scan(&e.ID, &e.Name, &e.EntityType, &aliasJSON, &createdAtStr)
	if err != nil {
		return nil, err
	}
	if t := parseTime(createdAtStr); t != nil {
		e.CreatedAt = *t
	}
	if err := json.Unmarshal([]byte(aliasJSON), &e.Aliases); err != nil {
		return nil, fmt.Errorf("unmarshal entity aliases: %w", err)
	}
	return &e, nil
}

func scanEntityRow(rows *sql.Rows) (*model.Entity, error) { return scanEntity(rows) }

func scanTranscript(row scanner) (*model.Transcript, error) {
	var t model.Transcript
	var date, topic, platSess sql.NullString
	var participantsJSON, createdAtStr string
	err := row.Scan(&t.ID, &t.FilePath, &date, &participantsJSON, &topic, &platSess, &t.ChunkCount, &createdAtStr)
	if err != nil {
		return nil, err
	}
	if tm := parseTime(createdAtStr); tm != nil {
		t.CreatedAt = *tm
	}
	t.Date = parseTimePtr(date)
	t.Topic = topic.String
	t.PlatformSessionID = platSess.String
	if err := json.Unmarshal([]byte(participantsJSON), &t.Participants); err != nil {
		return nil, fmt.Errorf("unmarshal transcript participants: %w", err)
	}
	return &t, nil
}

// --- Helpers ---

func timeStr(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

func timePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := timeStr(*t)
	return &s
}

func parseTime(s string) *time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil
	}
	return &t
}

func parseTimePtr(ns sql.NullString) *time.Time {
	if !ns.Valid {
		return nil
	}
	return parseTime(ns.String)
}

func parseLineRange(s string) *[2]int {
	parts := strings.SplitN(s, "-", 2)
	if len(parts) != 2 {
		return nil
	}
	var r [2]int
	fmt.Sscanf(parts[0], "%d", &r[0]) //nolint:gosec // best-effort parse; zero is safe default
	fmt.Sscanf(parts[1], "%d", &r[1]) //nolint:gosec // best-effort parse; zero is safe default
	return &r
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// --- Graph Traversal ---

func (s *SQLiteStore) GetEntityGraph(ctx context.Context, entityID string, depth int) (*EntityGraph, error) {
	center, err := s.GetEntity(ctx, entityID)
	if err != nil {
		return nil, fmt.Errorf("center entity %s: %w", entityID, err)
	}

	rows, err := s.db.QueryContext(ctx, `
		WITH RECURSIVE reachable(id, depth) AS (
			SELECT ?, 0
			UNION
			SELECT CASE
				WHEN r.from_entity = reachable.id THEN r.to_entity
				ELSE r.from_entity
			END, reachable.depth + 1
			FROM relationships r
			JOIN reachable ON r.from_entity = reachable.id OR r.to_entity = reachable.id
			WHERE reachable.depth < ?
		)
		SELECT DISTINCT e.id, e.name, e.entity_type, e.aliases, e.created_at
		FROM entities e
		JOIN reachable ON e.id = reachable.id
	`, entityID, depth)
	if err != nil {
		return nil, fmt.Errorf("graph query: %w", err)
	}
	defer rows.Close()

	entityIDs := map[string]bool{}
	var entities []model.Entity
	for rows.Next() {
		var e model.Entity
		var aliasesJSON, createdStr string
		if err := rows.Scan(&e.ID, &e.Name, &e.EntityType, &aliasesJSON, &createdStr); err != nil {
			return nil, fmt.Errorf("scan entity: %w", err)
		}
		if err := json.Unmarshal([]byte(aliasesJSON), &e.Aliases); err != nil {
			return nil, fmt.Errorf("unmarshal graph entity aliases: %w", err)
		}
		e.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
		entities = append(entities, e)
		entityIDs[e.ID] = true
	}

	relRows, err := s.db.QueryContext(ctx, `
		SELECT r.id, r.from_entity, r.to_entity, r.relation_type, r.properties, r.source_fact, r.created_at
		FROM relationships r
		WHERE r.from_entity IN (SELECT id FROM (
			WITH RECURSIVE reachable(id, depth) AS (
				SELECT ?, 0
				UNION
				SELECT CASE
					WHEN r2.from_entity = reachable.id THEN r2.to_entity
					ELSE r2.from_entity
				END, reachable.depth + 1
				FROM relationships r2
				JOIN reachable ON r2.from_entity = reachable.id OR r2.to_entity = reachable.id
				WHERE reachable.depth < ?
			)
			SELECT DISTINCT id FROM reachable
		))
		AND r.to_entity IN (SELECT id FROM (
			WITH RECURSIVE reachable(id, depth) AS (
				SELECT ?, 0
				UNION
				SELECT CASE
					WHEN r3.from_entity = reachable.id THEN r3.to_entity
					ELSE r3.from_entity
				END, reachable.depth + 1
				FROM relationships r3
				JOIN reachable ON r3.from_entity = reachable.id OR r3.to_entity = reachable.id
				WHERE reachable.depth < ?
			)
			SELECT DISTINCT id FROM reachable
		))
	`, entityID, depth, entityID, depth)
	if err != nil {
		return nil, fmt.Errorf("relationships query: %w", err)
	}
	defer relRows.Close()

	var relationships []model.Relationship
	for relRows.Next() {
		var r model.Relationship
		var propsJSON, createdStr string
		var sourceFact sql.NullString
		if err := relRows.Scan(&r.ID, &r.FromEntity, &r.ToEntity, &r.RelationType, &propsJSON, &sourceFact, &createdStr); err != nil {
			return nil, fmt.Errorf("scan relationship: %w", err)
		}
		if err := json.Unmarshal([]byte(propsJSON), &r.Properties); err != nil {
			return nil, fmt.Errorf("unmarshal graph relationship properties: %w", err)
		}
		if sourceFact.Valid {
			r.SourceFact = sourceFact.String
		}
		r.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
		relationships = append(relationships, r)
	}

	return &EntityGraph{
		Center:        *center,
		Entities:      entities,
		Relationships: relationships,
	}, nil
}

func (s *SQLiteStore) FindPath(ctx context.Context, fromEntityID, toEntityID string) ([]PathStep, error) {
	rows, err := s.db.QueryContext(ctx, `
		WITH RECURSIVE path(entity_id, rel_id, rel_type, depth, visited) AS (
			SELECT ?, '', '', 0, ?
			UNION ALL
			SELECT
				CASE WHEN r.from_entity = path.entity_id THEN r.to_entity ELSE r.from_entity END,
				r.id,
				r.relation_type,
				path.depth + 1,
				path.visited || ',' || CASE WHEN r.from_entity = path.entity_id THEN r.to_entity ELSE r.from_entity END
			FROM relationships r
			JOIN path ON (r.from_entity = path.entity_id OR r.to_entity = path.entity_id)
			WHERE path.depth < 10
			AND path.visited NOT LIKE '%' || CASE WHEN r.from_entity = path.entity_id THEN r.to_entity ELSE r.from_entity END || '%'
		)
		SELECT entity_id, rel_id, rel_type, depth
		FROM path
		WHERE entity_id = ?
		ORDER BY depth ASC
		LIMIT 1
	`, fromEntityID, fromEntityID, toEntityID)
	if err != nil {
		return nil, fmt.Errorf("find path: %w", err)
	}
	defer rows.Close()

	if !rows.Next() {
		return []PathStep{}, nil
	}

	var targetID, relID, relType string
	var depth int
	if err := rows.Scan(&targetID, &relID, &relType, &depth); err != nil {
		return nil, fmt.Errorf("scan path: %w", err)
	}

	if depth == 0 {
		return []PathStep{{EntityID: fromEntityID}}, nil
	}

	return s.reconstructPath(ctx, fromEntityID, toEntityID, depth)
}

func (s *SQLiteStore) reconstructPath(ctx context.Context, fromID, toID string, maxDepth int) ([]PathStep, error) {
	rows, err := s.db.QueryContext(ctx, `
		WITH RECURSIVE path(entity_id, rel_id, rel_type, depth, visited, trail) AS (
			SELECT ?, '', '', 0, ?, ?
			UNION ALL
			SELECT
				CASE WHEN r.from_entity = path.entity_id THEN r.to_entity ELSE r.from_entity END,
				r.id,
				r.relation_type,
				path.depth + 1,
				path.visited || ',' || CASE WHEN r.from_entity = path.entity_id THEN r.to_entity ELSE r.from_entity END,
				path.trail || '|' || CASE WHEN r.from_entity = path.entity_id THEN r.to_entity ELSE r.from_entity END || ':' || r.id || ':' || r.relation_type
			FROM relationships r
			JOIN path ON (r.from_entity = path.entity_id OR r.to_entity = path.entity_id)
			WHERE path.depth < ?
			AND path.visited NOT LIKE '%' || CASE WHEN r.from_entity = path.entity_id THEN r.to_entity ELSE r.from_entity END || '%'
		)
		SELECT trail
		FROM path
		WHERE entity_id = ?
		ORDER BY depth ASC
		LIMIT 1
	`, fromID, fromID, fromID+":::", maxDepth, toID)
	if err != nil {
		return nil, fmt.Errorf("reconstruct path: %w", err)
	}
	defer rows.Close()

	if !rows.Next() {
		return []PathStep{}, nil
	}

	var trail string
	if err := rows.Scan(&trail); err != nil {
		return nil, fmt.Errorf("scan trail: %w", err)
	}

	parts := strings.Split(trail, "|")
	steps := []PathStep{}
	for _, part := range parts {
		fields := strings.SplitN(part, ":", 3)
		if len(fields) != 3 {
			continue
		}
		steps = append(steps, PathStep{
			EntityID:       fields[0],
			RelationshipID: fields[1],
			RelationType:   fields[2],
		})
	}

	return steps, nil
}

func (s *SQLiteStore) SupersedeRealtimeBySession(ctx context.Context, sessionID string) (int64, error) {
	prefix := "realtime:" + sessionID
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx,
		"UPDATE facts SET supersede_reason = 'batch-replaced' WHERE source_file = ? AND (superseded_by IS NULL OR superseded_by = '') AND supersede_reason IS NULL",
		prefix)
	if err != nil {
		return 0, fmt.Errorf("supersede realtime facts for session %s: %w", sessionID, err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return res.RowsAffected()
}

func (s *SQLiteStore) DeleteExpiredFacts(ctx context.Context, olderThan time.Time) (int64, error) {
	cutoff := olderThan.UTC().Format(time.RFC3339)
	res, err := s.db.ExecContext(ctx,
		"DELETE FROM facts WHERE valid_until IS NOT NULL AND valid_until != '' AND valid_until < ?", cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// --- Admin ---

func (s *SQLiteStore) Reset(ctx context.Context) error {
	tables := []string{
		"facts_vec", "chunks_vec",
		"hot_messages_fts", "cooldown_messages_fts",
		"facts_fts", "transcript_chunks_fts",
		"fact_connections", "relationships", "consolidations",
		"taxonomy_signals", "taxonomy_proposals",
		"extraction_log", "ingested_files", "query_log",
		"quality_signals", "fact_citations", "eval_runs",
		"provider_ops", "retry_queue", "provider_health", "provider_models",
		"hot_messages", "cooldown_messages",
		"transcript_chunks", "transcripts",
		"facts", "entities",
		"schema_migrations",
	}
	for _, t := range tables {
		if _, err := s.db.ExecContext(ctx, "DROP TABLE IF EXISTS "+t); err != nil {
			return fmt.Errorf("drop %s: %w", t, err)
		}
	}
	return s.migrate()
}

func (s *SQLiteStore) DeleteFactsBySourcePattern(ctx context.Context, pattern string) (int64, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT id FROM facts WHERE source_file LIKE ?", pattern)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var factIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return 0, err
		}
		factIDs = append(factIDs, id)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		"DELETE FROM facts_fts WHERE fact_id IN (SELECT id FROM facts WHERE source_file LIKE ?)", pattern); err != nil {
		return 0, fmt.Errorf("delete fts: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		"DELETE FROM facts_vec WHERE fact_id IN (SELECT id FROM facts WHERE source_file LIKE ?)", pattern); err != nil {
		if !strings.Contains(err.Error(), "no such table") {
			return 0, fmt.Errorf("delete vec: %w", err)
		}
	}

	res, err := tx.ExecContext(ctx,
		"DELETE FROM facts WHERE source_file LIKE ?", pattern)
	if err != nil {
		return 0, fmt.Errorf("delete facts: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	if s.vecIndex != nil {
		const pref = "fact:"
		for _, id := range factIDs {
			_ = s.vecIndex.Remove(pref + id) //nolint:errcheck // best-effort
		}
	}
	return n, nil
}

func (s *SQLiteStore) DeduplicateEntities(ctx context.Context) (int, int, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT LOWER(name) AS lname, COUNT(*) AS cnt
		FROM entities
		GROUP BY lname
		HAVING cnt > 1`)
	if err != nil {
		return 0, 0, fmt.Errorf("find duplicates: %w", err)
	}
	defer rows.Close()

	var dupeNames []string
	for rows.Next() {
		var name string
		var cnt int
		if err := rows.Scan(&name, &cnt); err != nil {
			return 0, 0, err
		}
		dupeNames = append(dupeNames, name)
	}
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}

	groups := 0
	totalRemoved := 0

	for _, lname := range dupeNames {
		entRows, err := s.db.QueryContext(ctx, `
			SELECT id, name, entity_type, aliases, created_at
			FROM entities
			WHERE LOWER(name) = ?
			ORDER BY created_at ASC`, lname)
		if err != nil {
			return groups, totalRemoved, fmt.Errorf("list dupes for %s: %w", lname, err)
		}

		var ids []string
		for entRows.Next() {
			var e struct{ id, name, etype, aliases, created string }
			if err := entRows.Scan(&e.id, &e.name, &e.etype, &e.aliases, &e.created); err != nil {
				entRows.Close() //nolint:gosec // error path; returning scan error
				return groups, totalRemoved, err
			}
			ids = append(ids, e.id)
		}
		entRows.Close() //nolint:gosec // rows fully consumed above

		if len(ids) < 2 {
			continue
		}

		keepID := ids[0]
		removeIDs := ids[1:]

		for _, oldID := range removeIDs {
			if _, err := s.db.ExecContext(ctx,
				"UPDATE relationships SET from_entity = ? WHERE from_entity = ?", keepID, oldID); err != nil {
				return groups, totalRemoved, fmt.Errorf("update from_entity %s->%s: %w", oldID, keepID, err)
			}
			if _, err := s.db.ExecContext(ctx,
				"UPDATE relationships SET to_entity = ? WHERE to_entity = ?", keepID, oldID); err != nil {
				return groups, totalRemoved, fmt.Errorf("update to_entity %s->%s: %w", oldID, keepID, err)
			}
			if _, err := s.db.ExecContext(ctx, "DELETE FROM entities WHERE id = ?", oldID); err != nil {
				return groups, totalRemoved, fmt.Errorf("delete entity %s: %w", oldID, err)
			}
		}

		groups++
		totalRemoved += len(removeIDs)
	}

	return groups, totalRemoved, nil
}

// --- Quality signals (BVP-279) ---

func (s *SQLiteStore) CreateQualitySignal(ctx context.Context, sig *QualitySignal) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO quality_signals (id, signal_type, category, value, details, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		sig.ID, sig.SignalType, sig.Category, sig.Value, sig.Details, timeStr(sig.CreatedAt),
	)
	return err
}

func (s *SQLiteStore) ListQualitySignals(ctx context.Context, signalType string, limit int) ([]QualitySignal, error) {
	q := "SELECT id, signal_type, category, value, details, created_at FROM quality_signals"
	var args []any
	if signalType != "" {
		q += " WHERE signal_type = ?"
		args = append(args, signalType)
	}
	q += " ORDER BY created_at DESC"
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var signals []QualitySignal
	for rows.Next() {
		var sig QualitySignal
		var createdAtStr string
		if err := rows.Scan(&sig.ID, &sig.SignalType, &sig.Category, &sig.Value, &sig.Details, &createdAtStr); err != nil {
			return nil, err
		}
		if t := parseTime(createdAtStr); t != nil {
			sig.CreatedAt = *t
		}
		signals = append(signals, sig)
	}
	return signals, rows.Err()
}

func (s *SQLiteStore) CreateFactCitation(ctx context.Context, factID, queryID string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO fact_citations (fact_id, query_id, cited_at)
		VALUES (?, ?, datetime('now'))`,
		factID, queryID,
	)
	return err
}

// --- Eval runs ---

func (s *SQLiteStore) CreateEvalRun(ctx context.Context, r *EvalRun) error {
	baseline := 0
	if r.IsBaseline {
		baseline = 1
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO eval_runs (id, eval_type, score, score2, report, prompt_hash, examples_count, is_baseline, git_commit, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.EvalType, r.Score, r.Score2, r.Report, r.PromptHash, r.ExamplesCount,
		baseline, nilIfEmpty(r.GitCommit),
		r.CreatedAt.Format("2006-01-02T15:04:05Z"),
	)
	return err
}

func (s *SQLiteStore) LatestEvalRun(ctx context.Context, evalType string) (*EvalRun, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, eval_type, score, score2, prompt_hash, examples_count, is_baseline, git_commit, created_at
		FROM eval_runs WHERE eval_type = ? ORDER BY created_at DESC LIMIT 1`, evalType)

	var r EvalRun
	var createdAt string
	var score2 *float64
	var isBaseline int
	var gitCommit *string
	err := row.Scan(&r.ID, &r.EvalType, &r.Score, &score2, &r.PromptHash, &r.ExamplesCount, &isBaseline, &gitCommit, &createdAt)
	if err != nil {
		return nil, err
	}
	if score2 != nil {
		r.Score2 = *score2
	}
	r.IsBaseline = isBaseline == 1
	if gitCommit != nil {
		r.GitCommit = *gitCommit
	}
	r.CreatedAt, _ = time.Parse("2006-01-02T15:04:05Z", createdAt)
	return &r, nil
}

func (s *SQLiteStore) ListEvalRuns(ctx context.Context, evalType string, limit int) ([]EvalRun, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, eval_type, score, score2, prompt_hash, examples_count, is_baseline, git_commit, created_at
		FROM eval_runs
		WHERE (eval_type = ? OR ? = '')
		ORDER BY created_at DESC
		LIMIT ?`, evalType, evalType, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var runs []EvalRun
	for rows.Next() {
		var r EvalRun
		var createdAt string
		var score2 *float64
		var isBaseline int
		var gitCommit *string
		if err := rows.Scan(&r.ID, &r.EvalType, &r.Score, &score2, &r.PromptHash, &r.ExamplesCount, &isBaseline, &gitCommit, &createdAt); err != nil {
			return nil, err
		}
		if score2 != nil {
			r.Score2 = *score2
		}
		r.IsBaseline = isBaseline == 1
		if gitCommit != nil {
			r.GitCommit = *gitCommit
		}
		r.CreatedAt, _ = time.Parse("2006-01-02T15:04:05Z", createdAt)
		runs = append(runs, r)
	}
	return runs, rows.Err()
}

func (s *SQLiteStore) GetBaselineEvalRun(ctx context.Context, evalType string) (*EvalRun, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, eval_type, score, score2, prompt_hash, examples_count, is_baseline, git_commit, created_at
		FROM eval_runs WHERE eval_type = ? AND is_baseline = 1 LIMIT 1`, evalType)

	var r EvalRun
	var createdAt string
	var score2 *float64
	var isBaseline int
	var gitCommit *string
	err := row.Scan(&r.ID, &r.EvalType, &r.Score, &score2, &r.PromptHash, &r.ExamplesCount, &isBaseline, &gitCommit, &createdAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	if score2 != nil {
		r.Score2 = *score2
	}
	r.IsBaseline = isBaseline == 1
	if gitCommit != nil {
		r.GitCommit = *gitCommit
	}
	r.CreatedAt, _ = time.Parse("2006-01-02T15:04:05Z", createdAt)
	return &r, nil
}

func (s *SQLiteStore) SetBaseline(ctx context.Context, id string, evalType string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.ExecContext(ctx, `UPDATE eval_runs SET is_baseline = 0 WHERE eval_type = ?`, evalType); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE eval_runs SET is_baseline = 1 WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// --- Query log ---

func (s *SQLiteStore) CreateQueryLog(ctx context.Context, l *QueryLog) error {
	embedder := 0
	if l.EmbedderAvailable {
		embedder = 1
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO query_log (id, endpoint, question, total_latency_ms, retrieval_latency_ms,
			synthesis_latency_ms, facts_found, facts_by_vector, facts_by_text, facts_by_graph,
			chunks_by_vector, chunks_by_text, hot_by_vector, hot_by_text,
			cooldown_by_vector, cooldown_by_text,
			citations_count, embedder_available, error, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		l.ID, l.Endpoint, l.Question, l.TotalLatencyMs, l.RetrievalLatencyMs,
		l.SynthesisLatencyMs, l.FactsFound, l.FactsByVector, l.FactsByText, l.FactsByGraph,
		l.ChunksByVector, l.ChunksByText, l.HotByVector, l.HotByText,
		l.CooldownByVector, l.CooldownByText,
		l.CitationsCount, embedder, nilIfEmpty(l.Error),
		timeStr(l.CreatedAt),
	)
	return err
}

func (s *SQLiteStore) ListQueryLogs(ctx context.Context, limit int) ([]QueryLog, error) {
	q := `SELECT id, endpoint, question, total_latency_ms, retrieval_latency_ms,
		synthesis_latency_ms, facts_found, facts_by_vector, facts_by_text, facts_by_graph,
		chunks_by_vector, chunks_by_text, hot_by_vector, hot_by_text,
		cooldown_by_vector, cooldown_by_text,
		citations_count, embedder_available, error, created_at
		FROM query_log ORDER BY created_at DESC`
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []QueryLog
	for rows.Next() {
		var l QueryLog
		var embedder int
		var errStr sql.NullString
		var createdAtStr string
		if err := rows.Scan(&l.ID, &l.Endpoint, &l.Question, &l.TotalLatencyMs,
			&l.RetrievalLatencyMs, &l.SynthesisLatencyMs, &l.FactsFound,
			&l.FactsByVector, &l.FactsByText, &l.FactsByGraph,
			&l.ChunksByVector, &l.ChunksByText,
			&l.HotByVector, &l.HotByText, &l.CooldownByVector, &l.CooldownByText,
			&l.CitationsCount,
			&embedder, &errStr, &createdAtStr); err != nil {
			return nil, err
		}
		l.EmbedderAvailable = embedder == 1
		l.Error = errStr.String
		if t := parseTime(createdAtStr); t != nil {
			l.CreatedAt = *t
		}
		logs = append(logs, l)
	}
	return logs, rows.Err()
}

func (s *SQLiteStore) QueryLogStats(ctx context.Context, windowDays int) (*QueryLogStatsResult, error) {
	var result QueryLogStatsResult
	err := s.db.QueryRowContext(ctx, `
		SELECT
			COALESCE(SUM(CASE WHEN endpoint = 'query' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN endpoint = 'context' THEN 1 ELSE 0 END), 0),
			COALESCE(AVG(CASE WHEN endpoint = 'query' THEN total_latency_ms END), 0),
			COALESCE(AVG(CASE WHEN endpoint = 'context' THEN total_latency_ms END), 0),
			COALESCE(SUM(CASE WHEN error IS NOT NULL AND error != '' THEN 1 ELSE 0 END), 0),
			COALESCE(AVG(embedder_available) * 100, 0)
		FROM query_log
		WHERE created_at > datetime('now', '-' || ? || ' days')`,
		windowDays,
	).Scan(&result.TotalQueries, &result.TotalContext,
		&result.AvgQueryLatency, &result.AvgContextLatency,
		&result.ErrorCount, &result.EmbedderAvailPct)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (s *SQLiteStore) UpsertProviderModel(ctx context.Context, m *ProviderModel) error {
	avail := 0
	if m.Available {
		avail = 1
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO provider_models (provider_name, model_id, context_window, available, last_checked)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(provider_name, model_id) DO UPDATE SET
			context_window = excluded.context_window,
			available = excluded.available,
			last_checked = excluded.last_checked`,
		m.ProviderName, m.ModelID, m.ContextWindow, avail, timeStr(m.LastChecked),
	)
	return err
}

func (s *SQLiteStore) ListProviderModels(ctx context.Context, providerName string) ([]ProviderModel, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT provider_name, model_id, context_window, available, last_checked
		FROM provider_models
		WHERE provider_name = ? AND available = 1
		ORDER BY model_id`, providerName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var models []ProviderModel
	for rows.Next() {
		var m ProviderModel
		var avail int
		var lastCheckedStr string
		if err := rows.Scan(&m.ProviderName, &m.ModelID, &m.ContextWindow, &avail, &lastCheckedStr); err != nil {
			return nil, err
		}
		m.Available = avail == 1
		m.LastChecked, _ = time.Parse("2006-01-02 15:04:05", lastCheckedStr)
		models = append(models, m)
	}
	return models, rows.Err()
}

func (s *SQLiteStore) UpsertProviderHealth(ctx context.Context, h *ProviderHealth) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO provider_health (provider_name, task_type, configured_model, active_model, status, last_error, last_checked, switched_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(provider_name, task_type) DO UPDATE SET
			configured_model = excluded.configured_model,
			active_model = excluded.active_model,
			status = excluded.status,
			last_error = excluded.last_error,
			last_checked = excluded.last_checked,
			switched_at = excluded.switched_at`,
		h.ProviderName, h.TaskType, h.ConfiguredModel, h.ActiveModel,
		h.Status, nilIfEmpty(h.LastError), timeStr(h.LastChecked), timePtr(h.SwitchedAt),
	)
	return err
}

func (s *SQLiteStore) ListProviderHealth(ctx context.Context) ([]ProviderHealth, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT provider_name, task_type, configured_model, active_model, status, last_error, last_checked, switched_at
		FROM provider_health
		ORDER BY provider_name, task_type`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var results []ProviderHealth
	for rows.Next() {
		h, err := scanProviderHealth(rows)
		if err != nil {
			return nil, err
		}
		results = append(results, h)
	}
	return results, rows.Err()
}

func (s *SQLiteStore) GetProviderHealth(ctx context.Context, providerName, taskType string) (*ProviderHealth, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT provider_name, task_type, configured_model, active_model, status, last_error, last_checked, switched_at
		FROM provider_health
		WHERE provider_name = ? AND task_type = ?`, providerName, taskType)
	h, err := scanProviderHealth(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &h, nil
}

type scannable interface {
	Scan(dest ...any) error
}

func scanProviderHealth(s scannable) (ProviderHealth, error) {
	var h ProviderHealth
	var lastError sql.NullString
	var lastCheckedStr string
	var switchedAtStr sql.NullString
	err := s.Scan(&h.ProviderName, &h.TaskType, &h.ConfiguredModel, &h.ActiveModel,
		&h.Status, &lastError, &lastCheckedStr, &switchedAtStr)
	if err != nil {
		return h, err
	}
	if lastError.Valid {
		h.LastError = lastError.String
	}
	h.LastChecked, _ = time.Parse("2006-01-02 15:04:05", lastCheckedStr)
	if switchedAtStr.Valid {
		t, _ := time.Parse("2006-01-02 15:04:05", switchedAtStr.String)
		h.SwitchedAt = &t
	}
	return h, nil
}

// --- Provider Ops (BVP-305) ---

func (s *SQLiteStore) UpsertProviderOps(ctx context.Context, ops *ProviderOps) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO provider_ops (provider_name, status, retry_count, max_retries, last_error, error_type, next_check_at, last_success, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(provider_name) DO UPDATE SET
			status = excluded.status,
			retry_count = excluded.retry_count,
			max_retries = excluded.max_retries,
			last_error = excluded.last_error,
			error_type = excluded.error_type,
			next_check_at = excluded.next_check_at,
			last_success = COALESCE(excluded.last_success, provider_ops.last_success),
			updated_at = excluded.updated_at`,
		ops.ProviderName, ops.Status, ops.RetryCount, ops.MaxRetries,
		nilIfEmpty(ops.LastError), nilIfEmpty(ops.ErrorType),
		timePtr(ops.NextCheckAt), timePtr(ops.LastSuccess),
		timeStr(ops.CreatedAt), timeStr(ops.UpdatedAt),
	)
	return err
}

func (s *SQLiteStore) GetProviderOps(ctx context.Context, providerName string) (*ProviderOps, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT provider_name, status, retry_count, max_retries, last_error, error_type,
			next_check_at, last_success, created_at, updated_at
		FROM provider_ops WHERE provider_name = ?`, providerName)
	ops, err := scanProviderOps(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &ops, nil
}

func (s *SQLiteStore) ListProviderOps(ctx context.Context) ([]ProviderOps, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT provider_name, status, retry_count, max_retries, last_error, error_type,
			next_check_at, last_success, created_at, updated_at
		FROM provider_ops ORDER BY provider_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []ProviderOps
	for rows.Next() {
		ops, err := scanProviderOps(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, ops)
	}
	return result, rows.Err()
}

func scanProviderOps(s scannable) (ProviderOps, error) {
	var ops ProviderOps
	var lastError, errorType, nextCheckAt, lastSuccess sql.NullString
	var createdAtStr, updatedAtStr string
	err := s.Scan(&ops.ProviderName, &ops.Status, &ops.RetryCount, &ops.MaxRetries,
		&lastError, &errorType, &nextCheckAt, &lastSuccess, &createdAtStr, &updatedAtStr)
	if err != nil {
		return ops, err
	}
	ops.LastError = lastError.String
	ops.ErrorType = errorType.String
	if nextCheckAt.Valid {
		if t := parseTime(nextCheckAt.String); t != nil {
			ops.NextCheckAt = t
		}
	}
	if lastSuccess.Valid {
		if t := parseTime(lastSuccess.String); t != nil {
			ops.LastSuccess = t
		}
	}
	ops.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdAtStr)
	ops.UpdatedAt, _ = time.Parse("2006-01-02 15:04:05", updatedAtStr)
	return ops, nil
}

// --- Retry Queue (BVP-305) ---

func (s *SQLiteStore) EnqueueRetry(ctx context.Context, entry *RetryEntry) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO retry_queue (id, task_type, payload, created_at, retry_count, last_error, status)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		entry.ID, entry.TaskType, entry.Payload, timeStr(entry.CreatedAt),
		entry.RetryCount, nilIfEmpty(entry.LastError), entry.Status,
	)
	return err
}

func (s *SQLiteStore) DequeueRetries(ctx context.Context, limit int) ([]RetryEntry, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck

	rows, err := tx.QueryContext(ctx, `
		SELECT id, task_type, payload, created_at, retry_count, last_error, status
		FROM retry_queue WHERE status = 'pending'
		ORDER BY created_at ASC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []RetryEntry
	for rows.Next() {
		var e RetryEntry
		var createdAtStr string
		var lastError sql.NullString
		if err := rows.Scan(&e.ID, &e.TaskType, &e.Payload, &createdAtStr,
			&e.RetryCount, &lastError, &e.Status); err != nil {
			return nil, err
		}
		e.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdAtStr)
		e.LastError = lastError.String
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for i := range entries {
		if _, err := tx.ExecContext(ctx,
			"UPDATE retry_queue SET status = 'processing' WHERE id = ?", entries[i].ID); err != nil {
			return nil, err
		}
		entries[i].Status = "processing"
	}

	return entries, tx.Commit()
}

func (s *SQLiteStore) UpdateRetryStatus(ctx context.Context, id, status, lastError string) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE retry_queue SET status = ?, last_error = ? WHERE id = ?",
		status, nilIfEmpty(lastError), id)
	return err
}

func (s *SQLiteStore) ExpireOldRetries(ctx context.Context, olderThan time.Time) (int64, error) {
	result, err := s.db.ExecContext(ctx,
		"UPDATE retry_queue SET status = 'expired' WHERE status = 'pending' AND created_at < ?",
		timeStr(olderThan))
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *SQLiteStore) RetryQueueDepth(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM retry_queue WHERE status IN ('pending', 'processing')").Scan(&count)
	return count, err
}

func (s *SQLiteStore) Stats(ctx context.Context) (*DBStats, error) {
	var stats DBStats
	row := s.db.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM facts),
			(SELECT COUNT(*) FROM entities),
			(SELECT COUNT(*) FROM relationships),
			(SELECT COUNT(*) FROM consolidations),
			(SELECT COUNT(*) FROM ingested_files),
			(SELECT COUNT(*) FROM hot_messages),
			(SELECT COUNT(*) FROM cooldown_messages)
	`)
	if err := row.Scan(
		&stats.Facts,
		&stats.Entities,
		&stats.Relationships,
		&stats.Consolidations,
		&stats.IngestedFiles,
		&stats.HotMessages,
		&stats.CooldownMessages,
	); err != nil {
		return nil, fmt.Errorf("stats: %w", err)
	}
	return &stats, nil
}

// --- Hot / cooldown pipeline (BVP-352) ---

func scanHotMessage(row scanner) (*model.HotMessage, error) {
	var m model.HotMessage
	var tsStr, createdStr string
	var platform, platSess, linker sql.NullString
	var hasEmb int64
	if err := row.Scan(&m.ID, &m.Speaker, &m.Content, &tsStr,
		&platform, &platSess, &linker, &hasEmb, &createdStr); err != nil {
		return nil, err
	}
	ts, err := time.Parse(time.RFC3339, tsStr)
	if err != nil {
		return nil, err
	}
	m.Timestamp = ts
	ca, err := time.Parse(time.RFC3339, createdStr)
	if err != nil {
		return nil, err
	}
	m.CreatedAt = ca
	m.Platform = platform.String
	m.PlatformSessionID = platSess.String
	m.LinkerRef = linker.String
	m.HasEmbedding = hasEmb != 0
	return &m, nil
}

func (s *SQLiteStore) GetRecentHotMessages(ctx context.Context, platformSessionID string, limit int) ([]model.HotMessage, error) {
	if platformSessionID == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, speaker, content, timestamp, platform, platform_session_id, linker_ref, has_embedding, created_at
		FROM hot_messages
		WHERE platform_session_id = ?
		ORDER BY timestamp DESC, created_at DESC, id DESC
		LIMIT ?`, platformSessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.HotMessage
	for rows.Next() {
		m, err := scanHotMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

// getHotOrCooldownMessage loads a message by id from hot_messages, then cooldown_messages.
func (s *SQLiteStore) getHotOrCooldownMessage(ctx context.Context, id string) (*model.HotMessage, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, speaker, content, timestamp, platform, platform_session_id, linker_ref, has_embedding, created_at
		FROM hot_messages WHERE id = ?`, id)
	m, err := scanHotMessage(row)
	if err == nil {
		return m, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	row2 := s.db.QueryRowContext(ctx, `
		SELECT id, speaker, content, timestamp, platform, platform_session_id, linker_ref, has_embedding, created_at
		FROM cooldown_messages WHERE id = ?`, id)
	return scanHotMessage(row2)
}

const maxLinkerChainHops = 10000

func (s *SQLiteStore) GetLinkedMessages(ctx context.Context, messageID string) ([]model.HotMessage, error) {
	if strings.TrimSpace(messageID) == "" {
		return nil, fmt.Errorf("db: empty message id")
	}
	seen := make(map[string]struct{})
	var rev []model.HotMessage
	currID := messageID
	for currID != "" && len(rev) < maxLinkerChainHops {
		if _, dup := seen[currID]; dup {
			break
		}
		seen[currID] = struct{}{}
		m, err := s.getHotOrCooldownMessage(ctx, currID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				if len(rev) == 0 {
					return nil, ErrNotFound
				}
				return nil, fmt.Errorf("db: linker_ref target not found: %q", currID)
			}
			return nil, err
		}
		rev = append(rev, *m)
		currID = strings.TrimSpace(m.LinkerRef)
	}
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return rev, nil
}

func (s *SQLiteStore) InsertHotMessage(ctx context.Context, msg *model.HotMessage, embedding []float32) error {
	if msg == nil {
		return errors.New("db: nil hot message")
	}
	if len(embedding) > 0 && s.vecIndex != nil && !s.vectorCapability.WriteSafe {
		return fmt.Errorf("%w: backend=%s mode=%s detail=%s",
			ErrVectorWriteUnsafe, s.vectorCapability.Backend, s.vectorCapability.Mode, s.vectorCapability.Detail)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	hasEmb := int64(0)
	if msg.HasEmbedding {
		hasEmb = 1
	}
	var embBlob any
	if len(embedding) > 0 {
		embBlob = float32ToBlob(embedding)
	} else {
		embBlob = nil
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO hot_messages (id, speaker, content, timestamp, platform, platform_session_id,
			linker_ref, has_embedding, created_at, embedding)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		msg.ID, msg.Speaker, msg.Content, msg.Timestamp.UTC().Format(time.RFC3339),
		nilIfEmpty(msg.Platform), nilIfEmpty(msg.PlatformSessionID), nilIfEmpty(msg.LinkerRef),
		hasEmb, msg.CreatedAt.UTC().Format(time.RFC3339), embBlob)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO hot_messages_fts (content, message_id) VALUES (?, ?)`,
		msg.Content, msg.ID,
	); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	// Vector index updated after commit (best-effort, index is cache)
	// If this fails, index is out of sync but can be rebuilt from DB
	if len(embedding) > 0 && s.vecIndex != nil {
		_ = s.vecIndex.Add("hot:"+msg.ID, embedding) //nolint:errcheck
	}
	return nil
}

func (s *SQLiteStore) ListHotMessages(ctx context.Context, filter HotMessageFilter) ([]model.HotMessage, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	q := `SELECT id, speaker, content, timestamp, platform, platform_session_id, linker_ref, has_embedding, created_at
		FROM hot_messages WHERE 1=1`
	var args []any
	if filter.PlatformSessionID != "" {
		q += " AND platform_session_id = ?"
		args = append(args, filter.PlatformSessionID)
	}
	if filter.After != nil {
		q += " AND timestamp > ?"
		args = append(args, filter.After.UTC().Format(time.RFC3339))
	}
	if filter.Before != nil {
		q += " AND timestamp < ?"
		args = append(args, filter.Before.UTC().Format(time.RFC3339))
	}
	q += " ORDER BY timestamp ASC"
	q += fmt.Sprintf(" LIMIT %d", limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.HotMessage
	for rows.Next() {
		m, err := scanHotMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) SearchHotByText(ctx context.Context, query string, limit int) ([]ScoredHotMessage, error) {
	sanitized := fts.SanitizeQuery(strings.TrimSpace(query))
	if sanitized == "" || limit <= 0 {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT hm.id, hm.speaker, hm.content, hm.timestamp, hm.platform, hm.platform_session_id,
			hm.linker_ref, hm.has_embedding, hm.created_at, bm25(hot_messages_fts) AS score
		FROM hot_messages_fts
		JOIN hot_messages hm ON hot_messages_fts.message_id = hm.id
		WHERE hot_messages_fts MATCH ?
		ORDER BY score
		LIMIT ?`, sanitized, limit)
	if err != nil {
		log.Printf("db: hot FTS5 search: %v", err)
		return nil, nil
	}
	defer rows.Close()

	var out []ScoredHotMessage
	for rows.Next() {
		var bm25score float64
		var m model.HotMessage
		var tsStr, createdStr string
		var platform, platSess, linker sql.NullString
		var hasEmb int64
		if err := rows.Scan(&m.ID, &m.Speaker, &m.Content, &tsStr,
			&platform, &platSess, &linker, &hasEmb, &createdStr, &bm25score); err != nil {
			return nil, err
		}
		ts, err := time.Parse(time.RFC3339, tsStr)
		if err != nil {
			continue
		}
		ca, err := time.Parse(time.RFC3339, createdStr)
		if err != nil {
			continue
		}
		m.Timestamp = ts
		m.CreatedAt = ca
		m.Platform = platform.String
		m.PlatformSessionID = platSess.String
		m.LinkerRef = linker.String
		m.HasEmbedding = hasEmb != 0
		out = append(out, ScoredHotMessage{Message: m, Score: -bm25score})
	}
	return out, rows.Err()
}

func (s *SQLiteStore) SearchHotByVector(ctx context.Context, embedding []float32, limit int) ([]ScoredHotMessage, error) {
	if len(embedding) == 0 || limit <= 0 || s.vecIndex == nil {
		return nil, nil
	}
	if !s.vectorCapability.ReadAvailable {
		return nil, ErrVectorReadUnavailable
	}
	const pref = "hot:"
	hits, err := s.vecIndex.SearchWithPrefix(embedding, limit, pref)
	if err != nil {
		return nil, fmt.Errorf("hot vector search: %w", err)
	}
	var out []ScoredHotMessage
	for _, h := range hits {
		id := strings.TrimPrefix(h.ID, pref)
		if id == h.ID {
			continue
		}
		row := s.db.QueryRowContext(ctx, `
			SELECT id, speaker, content, timestamp, platform, platform_session_id, linker_ref, has_embedding, created_at
			FROM hot_messages WHERE id = ?`, id)
		m, err := scanHotMessage(row)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			return nil, err
		}
		out = append(out, ScoredHotMessage{Message: *m, Score: 1 - h.Distance})
	}
	return out, nil
}

func scanCooldownMessage(row scanner) (*model.CooldownMessage, error) {
	var m model.CooldownMessage
	var tsStr, createdStr string
	var platform, platSess, linker, clusterID, transcriptFile, processedAt, movedFrom sql.NullString
	var hasEmb int64
	var transcriptLine sql.NullInt64
	if err := row.Scan(&m.ID, &m.Speaker, &m.Content, &tsStr,
		&platform, &platSess, &linker, &hasEmb,
		&clusterID, &transcriptFile, &transcriptLine, &processedAt,
		&movedFrom, &createdStr); err != nil {
		return nil, err
	}
	ts, err := time.Parse(time.RFC3339, tsStr)
	if err != nil {
		return nil, err
	}
	m.Timestamp = ts
	ca, err := time.Parse(time.RFC3339, createdStr)
	if err != nil {
		return nil, err
	}
	m.CreatedAt = ca
	m.Platform = platform.String
	m.PlatformSessionID = platSess.String
	m.LinkerRef = linker.String
	m.HasEmbedding = hasEmb != 0
	m.ClusterID = clusterID.String
	m.TranscriptFile = transcriptFile.String
	if transcriptLine.Valid {
		m.TranscriptLine = int(transcriptLine.Int64)
	}
	m.ProcessedAt = parseTimePtr(processedAt)
	m.MovedFromHot = movedFrom.String
	return &m, nil
}

func (s *SQLiteStore) SearchCooldownByText(ctx context.Context, query string, limit int) ([]ScoredCooldownMessage, error) {
	sanitized := fts.SanitizeQuery(strings.TrimSpace(query))
	if sanitized == "" || limit <= 0 {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT cm.id, cm.speaker, cm.content, cm.timestamp, cm.platform, cm.platform_session_id,
			cm.linker_ref, cm.has_embedding,
			cm.cluster_id, cm.transcript_file, cm.transcript_line, cm.processed_at,
			cm.moved_from_hot, cm.created_at,
			bm25(cooldown_messages_fts) AS score
		FROM cooldown_messages_fts
		JOIN cooldown_messages cm ON cooldown_messages_fts.message_id = cm.id
		WHERE cooldown_messages_fts MATCH ?
		ORDER BY score
		LIMIT ?`, sanitized, limit)
	if err != nil {
		log.Printf("db: cooldown FTS5 search: %v", err)
		return nil, nil
	}
	defer rows.Close()

	var out []ScoredCooldownMessage
	for rows.Next() {
		var bm25score float64
		var m model.CooldownMessage
		var tsStr, createdStr string
		var platform, platSess, linker, clusterID, transcriptFile, processedAt, movedFrom sql.NullString
		var hasEmb int64
		var transcriptLine sql.NullInt64
		if err := rows.Scan(&m.ID, &m.Speaker, &m.Content, &tsStr,
			&platform, &platSess, &linker, &hasEmb,
			&clusterID, &transcriptFile, &transcriptLine, &processedAt,
			&movedFrom, &createdStr, &bm25score); err != nil {
			return nil, err
		}
		ts, err := time.Parse(time.RFC3339, tsStr)
		if err != nil {
			continue
		}
		ca, err := time.Parse(time.RFC3339, createdStr)
		if err != nil {
			continue
		}
		m.Timestamp = ts
		m.CreatedAt = ca
		m.Platform = platform.String
		m.PlatformSessionID = platSess.String
		m.LinkerRef = linker.String
		m.HasEmbedding = hasEmb != 0
		m.ClusterID = clusterID.String
		m.TranscriptFile = transcriptFile.String
		if transcriptLine.Valid {
			m.TranscriptLine = int(transcriptLine.Int64)
		}
		m.ProcessedAt = parseTimePtr(processedAt)
		m.MovedFromHot = movedFrom.String
		out = append(out, ScoredCooldownMessage{Message: m, Score: -bm25score})
	}
	return out, rows.Err()
}

func (s *SQLiteStore) SearchCooldownByVector(ctx context.Context, embedding []float32, limit int) ([]ScoredCooldownMessage, error) {
	if len(embedding) == 0 || limit <= 0 || s.vecIndex == nil {
		return nil, nil
	}
	if !s.vectorCapability.ReadAvailable {
		return nil, ErrVectorReadUnavailable
	}
	const pref = "cool:"
	hits, err := s.vecIndex.SearchWithPrefix(embedding, limit, pref)
	if err != nil {
		return nil, fmt.Errorf("cooldown vector search: %w", err)
	}
	var out []ScoredCooldownMessage
	for _, h := range hits {
		id := strings.TrimPrefix(h.ID, pref)
		if id == h.ID {
			continue
		}
		row := s.db.QueryRowContext(ctx, `
			SELECT id, speaker, content, timestamp, platform, platform_session_id,
				linker_ref, has_embedding,
				cluster_id, transcript_file, transcript_line, processed_at,
				moved_from_hot, created_at
			FROM cooldown_messages WHERE id = ?`, id)
		m, err := scanCooldownMessage(row)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			return nil, err
		}
		out = append(out, ScoredCooldownMessage{Message: *m, Score: 1 - h.Distance})
	}
	return out, nil
}

func (s *SQLiteStore) MoveHotToCooldown(ctx context.Context, olderThan time.Time, batchSize int) (int64, error) {
	if batchSize <= 0 {
		return 0, nil
	}
	if s.vecIndex != nil && !s.vectorCapability.WriteSafe {
		return 0, fmt.Errorf("%w: backend=%s mode=%s detail=%s",
			ErrVectorWriteUnsafe, s.vectorCapability.Backend, s.vectorCapability.Mode, s.vectorCapability.Detail)
	}
	cutoff := olderThan.UTC().Format(time.RFC3339)
	movedAt := time.Now().UTC().Format(time.RFC3339)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	resIns, err := tx.ExecContext(ctx, `
		INSERT INTO cooldown_messages (id, speaker, content, timestamp, platform,
			platform_session_id, linker_ref, has_embedding, embedding, cluster_id,
			transcript_file, transcript_line, processed_at, moved_from_hot, created_at)
		SELECT id, speaker, content, timestamp, platform,
			platform_session_id, linker_ref, has_embedding, embedding, NULL, NULL,
			NULL, NULL, ?, created_at
		FROM hot_messages
		WHERE timestamp < ?
		ORDER BY timestamp ASC
		LIMIT ?`, movedAt, cutoff, batchSize)
	if err != nil {
		return 0, err
	}
	nMoved, err := resIns.RowsAffected()
	if err != nil {
		return 0, err
	}
	if nMoved == 0 {
		return 0, tx.Commit()
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO cooldown_messages_fts (content, message_id)
		SELECT hf.content, hf.message_id
		FROM hot_messages_fts hf
		JOIN hot_messages hm ON hf.message_id = hm.id
		WHERE hm.timestamp < ?
		ORDER BY hm.timestamp ASC
		LIMIT ?`, cutoff, batchSize); err != nil {
		return 0, err
	}

	if _, err := tx.ExecContext(ctx, `
		DELETE FROM hot_messages_fts WHERE message_id IN (
			SELECT id FROM hot_messages WHERE timestamp < ? ORDER BY timestamp ASC LIMIT ?
		)`, cutoff, batchSize); err != nil {
		return 0, err
	}

	if _, err := tx.ExecContext(ctx, `
		DELETE FROM hot_messages WHERE id IN (
			SELECT id FROM hot_messages WHERE timestamp < ? ORDER BY timestamp ASC LIMIT ?
		)`, cutoff, batchSize); err != nil {
		return 0, err
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}

	// Vector index re-prefix after commit (best-effort, index is cache)
	// Query committed data to get embeddings for re-prefixing
	if s.vecIndex != nil {
		rows, err := s.db.QueryContext(ctx, `
			SELECT id, embedding FROM cooldown_messages
			WHERE moved_from_hot = ? AND has_embedding = 1`, movedAt)
		if err != nil {
			// DB commit succeeded, return nMoved with warning
			return nMoved, fmt.Errorf("load moved embeddings for index update: %w (index may be out of sync)", err)
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			var blob []byte
			if err := rows.Scan(&id, &blob); err != nil {
				return nMoved, fmt.Errorf("scan moved embedding: %w (index may be out of sync)", err)
			}
			vec := blobToFloat32(blob)
			if len(vec) == 0 {
				continue
			}
			_ = s.vecIndex.Remove("hot:" + id) //nolint:errcheck
			if err := s.vecIndex.Add("cool:"+id, vec); err != nil {
				return nMoved, fmt.Errorf("vector index re-prefix %s: %w (index may be out of sync)", id, err)
			}
		}
		if err := rows.Err(); err != nil {
			return nMoved, fmt.Errorf("scan moved embeddings: %w (index may be out of sync)", err)
		}
	}

	return nMoved, nil
}

func (s *SQLiteStore) DeleteExpiredHot(ctx context.Context, olderThan time.Time) (int64, error) {
	cutoff := olderThan.UTC().Format(time.RFC3339)

	var hotVecIDs []string
	if s.vecIndex != nil {
		rows, err := s.db.QueryContext(ctx, `
			SELECT id FROM hot_messages WHERE timestamp < ?
			AND embedding IS NOT NULL AND length(embedding) > 0`, cutoff)
		if err != nil {
			return 0, err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return 0, err
			}
			hotVecIDs = append(hotVecIDs, id)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return 0, err
		}
		rows.Close()
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		DELETE FROM hot_messages_fts WHERE message_id IN (
			SELECT id FROM hot_messages WHERE timestamp < ?
		)`, cutoff); err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM hot_messages WHERE timestamp < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}

	if s.vecIndex != nil {
		for _, id := range hotVecIDs {
			_ = s.vecIndex.Remove("hot:" + id) //nolint:errcheck
		}
	}
	return n, nil
}

func (s *SQLiteStore) CountHotMessages(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM hot_messages`).Scan(&n)
	return n, err
}

// --- Cool pipeline (Phase 2) ---

func (s *SQLiteStore) ListCooldownUnclustered(ctx context.Context, platformSessionID string, limit int) ([]model.CooldownMessage, error) {
	if platformSessionID == "" || limit <= 0 {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, speaker, content, timestamp, platform, platform_session_id,
			linker_ref, has_embedding,
			cluster_id, transcript_file, transcript_line, processed_at,
			moved_from_hot, created_at
		FROM cooldown_messages
		WHERE platform_session_id = ? AND cluster_id IS NULL
		ORDER BY timestamp ASC
		LIMIT ?`, platformSessionID, limit)
	if err != nil {
		return nil, fmt.Errorf("list unclustered: %w", err)
	}
	defer rows.Close()

	var out []model.CooldownMessage
	for rows.Next() {
		m, err := scanCooldownMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) ListSessionsWithUnclusteredCooldown(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT platform_session_id
		FROM cooldown_messages
		WHERE platform_session_id IS NOT NULL AND TRIM(platform_session_id) != ''
		AND cluster_id IS NULL
		ORDER BY platform_session_id`)
	if err != nil {
		return nil, fmt.Errorf("list sessions with unclustered cooldown: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var sid string
		if err := rows.Scan(&sid); err != nil {
			return nil, err
		}
		out = append(out, sid)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) AssignCooldownCluster(ctx context.Context, clusterID string, messageIDs []string) error {
	if clusterID == "" || len(messageIDs) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	placeholders := make([]string, len(messageIDs))
	args := make([]any, 0, len(messageIDs)+1)
	args = append(args, clusterID)
	for i, id := range messageIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}
	q := fmt.Sprintf("UPDATE cooldown_messages SET cluster_id = ? WHERE cluster_id IS NULL AND id IN (%s)",
		strings.Join(placeholders, ","))
	if _, err := tx.ExecContext(ctx, q, args...); err != nil {
		return fmt.Errorf("assign cluster: %w", err)
	}
	return tx.Commit()
}

func (s *SQLiteStore) ListClustersReadyForExtraction(ctx context.Context, silenceHours int, maxClusterSize int) ([]CooldownCluster, error) {
	var out []CooldownCluster

	// Size trigger: clusters with >= maxClusterSize messages
	if maxClusterSize > 0 {
		rows, err := s.db.QueryContext(ctx, `
			SELECT cluster_id, platform_session_id, COUNT(*) AS cnt, MAX(timestamp) AS last_ts
			FROM cooldown_messages
			WHERE cluster_id IS NOT NULL AND processed_at IS NULL
			GROUP BY cluster_id
			HAVING cnt >= ?`, maxClusterSize)
		if err != nil {
			return nil, fmt.Errorf("size trigger query: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var c CooldownCluster
			var lastTS string
			if err := rows.Scan(&c.ClusterID, &c.PlatformSessionID, &c.MessageCount, &lastTS); err != nil {
				return nil, err
			}
			if t := parseTime(lastTS); t != nil {
				c.LastMessageAt = *t
			}
			c.TriggerKind = "size"
			out = append(out, c)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}

	sizeIDs := make(map[string]bool, len(out))
	for _, c := range out {
		sizeIDs[c.ClusterID] = true
	}

	// Silence trigger: cluster has no new messages for silenceHours AND at least
	// one other cluster in the same session received a message more recently.
	if silenceHours > 0 {
		rows2, err := s.db.QueryContext(ctx, `
			SELECT c.cluster_id, c.platform_session_id, c.cnt, c.last_ts
			FROM (
				SELECT cluster_id, platform_session_id, COUNT(*) AS cnt, MAX(timestamp) AS last_ts
				FROM cooldown_messages
				WHERE cluster_id IS NOT NULL AND processed_at IS NULL
				GROUP BY cluster_id
			) c
			WHERE datetime(c.last_ts, '+' || ? || ' hours') < datetime('now')
			AND EXISTS (
				SELECT 1 FROM cooldown_messages o
				WHERE o.platform_session_id = c.platform_session_id
				AND o.cluster_id IS NOT NULL
				AND o.cluster_id != c.cluster_id
				AND o.timestamp > c.last_ts
			)`, silenceHours)
		if err != nil {
			return nil, fmt.Errorf("silence trigger query: %w", err)
		}
		defer rows2.Close()
		for rows2.Next() {
			var c CooldownCluster
			var lastTS string
			if err := rows2.Scan(&c.ClusterID, &c.PlatformSessionID, &c.MessageCount, &lastTS); err != nil {
				return nil, err
			}
			if sizeIDs[c.ClusterID] {
				continue
			}
			if t := parseTime(lastTS); t != nil {
				c.LastMessageAt = *t
			}
			c.TriggerKind = "silence"
			out = append(out, c)
		}
		if err := rows2.Err(); err != nil {
			return nil, err
		}
	}

	return out, nil
}

func (s *SQLiteStore) ListClusterMessages(ctx context.Context, clusterID string) ([]model.CooldownMessage, error) {
	if clusterID == "" {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, speaker, content, timestamp, platform, platform_session_id,
			linker_ref, has_embedding,
			cluster_id, transcript_file, transcript_line, processed_at,
			moved_from_hot, created_at
		FROM cooldown_messages
		WHERE cluster_id = ?
		ORDER BY timestamp ASC`, clusterID)
	if err != nil {
		return nil, fmt.Errorf("list cluster messages: %w", err)
	}
	defer rows.Close()

	var out []model.CooldownMessage
	for rows.Next() {
		m, err := scanCooldownMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) MarkClusterProcessed(ctx context.Context, clusterID string, processedAt time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE cooldown_messages SET processed_at = ? WHERE cluster_id = ? AND processed_at IS NULL`,
		timeStr(processedAt), clusterID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *SQLiteStore) ClearClusterProcessed(ctx context.Context, clusterID string) (int64, error) {
	if clusterID == "" {
		return 0, nil
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE cooldown_messages SET processed_at = NULL WHERE cluster_id = ?`,
		clusterID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *SQLiteStore) LinkCooldownToTranscript(ctx context.Context, platformSessionID, transcriptFile string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE cooldown_messages SET transcript_file = ?, transcript_line = 1
		WHERE platform_session_id = ? AND processed_at IS NULL`,
		transcriptFile, platformSessionID)
	if err != nil {
		return 0, fmt.Errorf("link cooldown to transcript: %w", err)
	}
	return res.RowsAffected()
}

func (s *SQLiteStore) MarkCooldownProcessedBySession(ctx context.Context, platformSessionID string) (int64, error) {
	now := timeStr(time.Now().UTC())
	res, err := s.db.ExecContext(ctx, `
		UPDATE cooldown_messages SET processed_at = ?
		WHERE platform_session_id = ? AND processed_at IS NULL AND transcript_file IS NOT NULL`,
		now, platformSessionID)
	if err != nil {
		return 0, fmt.Errorf("mark cooldown processed by session: %w", err)
	}
	return res.RowsAffected()
}

func (s *SQLiteStore) CoolPipelineStats(ctx context.Context) (*CoolStats, error) {
	var stats CoolStats
	err := s.db.QueryRowContext(ctx, `
		SELECT
			COUNT(DISTINCT CASE WHEN cluster_id IS NOT NULL AND processed_at IS NULL THEN cluster_id END),
			COUNT(DISTINCT CASE WHEN cluster_id IS NOT NULL AND processed_at IS NOT NULL THEN cluster_id END),
			COUNT(CASE WHEN processed_at IS NOT NULL THEN 1 END)
		FROM cooldown_messages`).Scan(&stats.ClustersPending, &stats.ClustersExtracted, &stats.MessagesProcessed)
	if err != nil {
		return nil, fmt.Errorf("cool pipeline stats: %w", err)
	}
	return &stats, nil
}

// --- Lint (BVP-368) ---

func (s *SQLiteStore) LintStaleFacts(ctx context.Context) ([]LintStaleFact, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, subject, content, valid_until
		FROM facts
		WHERE valid_until IS NOT NULL AND valid_until != ''
		  AND valid_until < ?
		  AND (superseded_by IS NULL OR superseded_by = '')`, now)
	if err != nil {
		return nil, fmt.Errorf("lint stale facts: %w", err)
	}
	defer rows.Close()

	var out []LintStaleFact
	for rows.Next() {
		var r LintStaleFact
		var subj, content sql.NullString
		if err := rows.Scan(&r.ID, &subj, &content, &r.ValidUntil); err != nil {
			return nil, err
		}
		r.Subject = subj.String
		r.Content = content.String
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) LintOrphanEntities(ctx context.Context) ([]LintOrphanEntity, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT e.id, e.name, e.entity_type
		FROM entities e
		WHERE NOT EXISTS (
			SELECT 1 FROM relationships r
			WHERE r.from_entity = e.id OR r.to_entity = e.id
		)
		AND NOT EXISTS (
			SELECT 1 FROM facts f WHERE f.subject = e.name
		)`)
	if err != nil {
		return nil, fmt.Errorf("lint orphan entities: %w", err)
	}
	defer rows.Close()

	var out []LintOrphanEntity
	for rows.Next() {
		var r LintOrphanEntity
		if err := rows.Scan(&r.ID, &r.Name, &r.EntityType); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) LintBrokenSupersedeChains(ctx context.Context) ([]LintBrokenSupersedeChain, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT f.id, f.subject, f.superseded_by
		FROM facts f
		WHERE f.superseded_by IS NOT NULL AND f.superseded_by != ''
		AND NOT EXISTS (
			SELECT 1 FROM facts f2 WHERE f2.id = f.superseded_by
		)`)
	if err != nil {
		return nil, fmt.Errorf("lint broken supersede chains: %w", err)
	}
	defer rows.Close()

	var out []LintBrokenSupersedeChain
	for rows.Next() {
		var r LintBrokenSupersedeChain
		var subj sql.NullString
		if err := rows.Scan(&r.ID, &subj, &r.SupersededBy); err != nil {
			return nil, err
		}
		r.Subject = subj.String
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) LintEntityDedupExact(ctx context.Context) ([]LintEntityDedupPair, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT e1.id, e1.name, e2.id, e2.name
		FROM entities e1
		JOIN entities e2 ON e1.id < e2.id
		  AND LOWER(TRIM(e1.name)) = LOWER(TRIM(e2.name))`)
	if err != nil {
		return nil, fmt.Errorf("lint entity dedup exact: %w", err)
	}
	defer rows.Close()

	return scanDedupPairs(rows, "exact")
}

func (s *SQLiteStore) LintEntityDedupSubstring(ctx context.Context) ([]LintEntityDedupPair, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT e1.id, e1.name, e2.id, e2.name
		FROM entities e1
		JOIN entities e2 ON e1.id < e2.id
		  AND LENGTH(e1.name) >= 4
		  AND LENGTH(e2.name) >= 4
		  AND (
			LOWER(e1.name) LIKE '%' || LOWER(e2.name) || '%'
			OR LOWER(e2.name) LIKE '%' || LOWER(e1.name) || '%'
		  )
		  AND LOWER(TRIM(e1.name)) != LOWER(TRIM(e2.name))`)
	if err != nil {
		return nil, fmt.Errorf("lint entity dedup substring: %w", err)
	}
	defer rows.Close()

	return scanDedupPairs(rows, "substring")
}

func scanDedupPairs(rows *sql.Rows, kind string) ([]LintEntityDedupPair, error) {
	var out []LintEntityDedupPair
	for rows.Next() {
		var r LintEntityDedupPair
		r.Kind = kind
		if err := rows.Scan(&r.ID1, &r.Name1, &r.ID2, &r.Name2); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) LintFactsMissingEmbeddingsByType(ctx context.Context) ([]LintMissingEmbeddingByType, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT fact_type, COUNT(*) AS cnt
		FROM facts
		WHERE embedding IS NULL OR LENGTH(embedding) = 0
		GROUP BY fact_type
		ORDER BY cnt DESC`)
	if err != nil {
		return nil, fmt.Errorf("lint missing embeddings: %w", err)
	}
	defer rows.Close()

	var out []LintMissingEmbeddingByType
	for rows.Next() {
		var r LintMissingEmbeddingByType
		if err := rows.Scan(&r.FactType, &r.Count); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) LintDistinctNonEmptySourceFiles(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT source_file FROM facts
		WHERE source_file IS NOT NULL AND source_file != ''
		ORDER BY source_file`)
	if err != nil {
		return nil, fmt.Errorf("lint source files: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) LintUnconsolidatedActiveFactsCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM facts f
		WHERE (f.superseded_by IS NULL OR f.superseded_by = '')
		AND f.id NOT IN (
			SELECT value FROM consolidations, json_each(consolidations.source_fact_ids)
		)`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("lint unconsolidated facts: %w", err)
	}
	return n, nil
}
