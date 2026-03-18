package db

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	_ "github.com/mattn/go-sqlite3"

	"github.com/aegis-alpha/imprint-mace/internal/model"
)

//go:embed migrations/*.sql
var migrations embed.FS

type SQLiteStore struct {
	db *sql.DB
}

func Open(path string) (*SQLiteStore, error) {
	sqlite_vec.Auto()
	db, err := sql.Open("sqlite3", path+"?_journal_mode=wal&_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	s := &SQLiteStore{db: db}
	if err := s.migrate(); err != nil {
		db.Close() //nolint:gosec // best-effort cleanup on migration failure
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

// RawDB returns the underlying *sql.DB for aggregate queries that
// don't fit the Store interface (e.g. taxonomy signal collection).
func (s *SQLiteStore) RawDB() *sql.DB {
	return s.db
}

func (s *SQLiteStore) migrate() error {
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
	return nil
}

func splitMigrationStatements(sql string) []string {
	var stmts []string
	var current strings.Builder
	inComment := false
	for _, line := range strings.Split(sql, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "--") {
			inComment = false
			continue
		}
		_ = inComment
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
	blob := float32ToBlob(embedding)
	_, err := s.db.ExecContext(ctx,
		"UPDATE facts SET embedding = ?, embedding_model = ? WHERE id = ?", blob, modelName, factID)
	if err != nil {
		return err
	}
	vecBlob, err := sqlite_vec.SerializeFloat32(embedding)
	if err != nil {
		return fmt.Errorf("serialize embedding: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		"INSERT OR REPLACE INTO facts_vec(fact_id, embedding) VALUES (?, ?)", factID, vecBlob)
	return err
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

// --- Vec table management (D23) ---

func (s *SQLiteStore) EnsureVecTable(ctx context.Context, dims int) error {
	var createSQL string
	err := s.db.QueryRowContext(ctx,
		"SELECT sql FROM sqlite_master WHERE type='table' AND name='facts_vec'").Scan(&createSQL)
	if err == nil {
		expected := fmt.Sprintf("float[%d]", dims)
		if !strings.Contains(createSQL, expected) {
			if _, err := s.db.ExecContext(ctx, "DROP TABLE facts_vec"); err != nil {
				return fmt.Errorf("drop facts_vec with wrong dimensions: %w", err)
			}
		} else {
			return nil
		}
	}
	_, err = s.db.ExecContext(ctx, fmt.Sprintf(
		"CREATE VIRTUAL TABLE IF NOT EXISTS facts_vec USING vec0(fact_id TEXT PRIMARY KEY, embedding float[%d] distance_metric=cosine)", dims))
	return err
}

// --- Embeddings ---

func (s *SQLiteStore) ListFactEmbeddings(ctx context.Context, factType string) ([][]float32, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT fv.embedding FROM facts f
		JOIN facts_vec fv ON f.id = fv.fact_id
		WHERE f.fact_type = ?`, factType)
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
	vecBlob, err := sqlite_vec.SerializeFloat32(embedding)
	if err != nil {
		return nil, fmt.Errorf("serialize query embedding: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT v.fact_id, v.distance
		FROM facts_vec v
		WHERE v.embedding MATCH ?
		  AND k = ?`, vecBlob, limit)
	if err != nil {
		return nil, fmt.Errorf("vector search: %w", err)
	}
	defer rows.Close()

	var results []ScoredFact
	for rows.Next() {
		var factID string
		var distance float64
		if err := rows.Scan(&factID, &distance); err != nil {
			return nil, err
		}
		fact, err := s.GetFact(ctx, factID)
		if err != nil {
			continue
		}
		results = append(results, ScoredFact{
			Fact:  *fact,
			Score: 1 - distance,
		})
	}
	return results, rows.Err()
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
			error_type, error_message, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		l.ID, l.ProviderName, l.Model, l.InputLength, l.TokensUsed,
		l.DurationMs, success, l.FactsCount, l.EntitiesCount, l.RelationshipsCount,
		nilIfEmpty(l.ErrorType), nilIfEmpty(l.ErrorMessage), timeStr(l.CreatedAt),
	)
	return err
}

func (s *SQLiteStore) ListExtractionLogs(ctx context.Context, limit int) ([]ExtractionLog, error) {
	q := "SELECT id, provider_name, model, input_length, tokens_used, duration_ms, success, facts_count, entities_count, relationships_count, error_type, error_message, created_at FROM extraction_log ORDER BY created_at DESC"
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
		INSERT INTO transcripts (id, file_path, date, participants, topic, chunk_count, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.FilePath, timePtr(t.Date), string(participantsJSON),
		nilIfEmpty(t.Topic), t.ChunkCount, timeStr(t.CreatedAt),
	)
	return err
}

func (s *SQLiteStore) GetTranscript(ctx context.Context, id string) (*model.Transcript, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, file_path, date, participants, topic, chunk_count, created_at
		FROM transcripts WHERE id = ?`, id)
	return scanTranscript(row)
}

func (s *SQLiteStore) GetTranscriptByPath(ctx context.Context, filePath string) (*model.Transcript, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, file_path, date, participants, topic, chunk_count, created_at
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
	_, err := s.db.ExecContext(ctx,
		"DELETE FROM transcript_chunks WHERE transcript_id = ?", transcriptID)
	return err
}

func (s *SQLiteStore) EnsureChunkVecTable(ctx context.Context, dims int) error {
	var createSQL string
	err := s.db.QueryRowContext(ctx,
		"SELECT sql FROM sqlite_master WHERE type='table' AND name='chunks_vec'").Scan(&createSQL)
	if err == nil {
		expected := fmt.Sprintf("float[%d]", dims)
		if !strings.Contains(createSQL, expected) {
			if _, err := s.db.ExecContext(ctx, "DROP TABLE chunks_vec"); err != nil {
				return fmt.Errorf("drop chunks_vec with wrong dimensions: %w", err)
			}
		} else {
			return nil
		}
	}
	_, err = s.db.ExecContext(ctx, fmt.Sprintf(
		"CREATE VIRTUAL TABLE IF NOT EXISTS chunks_vec USING vec0(chunk_id TEXT PRIMARY KEY, embedding float[%d] distance_metric=cosine)", dims))
	return err
}

func (s *SQLiteStore) UpdateChunkEmbedding(ctx context.Context, chunkID string, embedding []float32, modelName string) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE transcript_chunks SET embedding_model = ? WHERE id = ?", modelName, chunkID)
	if err != nil {
		return err
	}
	vecBlob, err := sqlite_vec.SerializeFloat32(embedding)
	if err != nil {
		return fmt.Errorf("serialize chunk embedding: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		"INSERT OR REPLACE INTO chunks_vec(chunk_id, embedding) VALUES (?, ?)", chunkID, vecBlob)
	return err
}

func (s *SQLiteStore) SearchChunksByVector(ctx context.Context, embedding []float32, limit int) ([]ScoredChunk, error) {
	vecBlob, err := sqlite_vec.SerializeFloat32(embedding)
	if err != nil {
		return nil, fmt.Errorf("serialize query embedding: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT v.chunk_id, v.distance
		FROM chunks_vec v
		WHERE v.embedding MATCH ?
		  AND k = ?`, vecBlob, limit)
	if err != nil {
		return nil, fmt.Errorf("chunk vector search: %w", err)
	}
	defer rows.Close()

	var results []ScoredChunk
	for rows.Next() {
		var chunkID string
		var distance float64
		if err := rows.Scan(&chunkID, &distance); err != nil {
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
			Score: 1 - distance,
		})
	}
	return results, rows.Err()
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
	var date, topic sql.NullString
	var participantsJSON, createdAtStr string
	err := row.Scan(&t.ID, &t.FilePath, &date, &participantsJSON, &topic, &t.ChunkCount, &createdAtStr)
	if err != nil {
		return nil, err
	}
	if tm := parseTime(createdAtStr); tm != nil {
		t.CreatedAt = *tm
	}
	t.Date = parseTimePtr(date)
	t.Topic = topic.String
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
		"facts_fts", "transcript_chunks_fts",
		"fact_connections", "relationships", "consolidations",
		"taxonomy_signals", "taxonomy_proposals",
		"extraction_log", "ingested_files",
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

func (s *SQLiteStore) Stats(ctx context.Context) (*DBStats, error) {
	var stats DBStats
	row := s.db.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM facts),
			(SELECT COUNT(*) FROM entities),
			(SELECT COUNT(*) FROM relationships),
			(SELECT COUNT(*) FROM consolidations),
			(SELECT COUNT(*) FROM ingested_files)
	`)
	if err := row.Scan(
		&stats.Facts,
		&stats.Entities,
		&stats.Relationships,
		&stats.Consolidations,
		&stats.IngestedFiles,
	); err != nil {
		return nil, fmt.Errorf("stats: %w", err)
	}
	return &stats, nil
}
