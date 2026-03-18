// Package api provides an HTTP REST API for Imprint.
//
// Endpoints:
//   - POST   /ingest                      -- extract facts from text
//   - GET    /query?q=...                 -- ask a question (LLM synthesis)
//   - GET    /context?hint=...            -- retrieval-only context (no LLM)
//   - GET    /status                      -- database statistics
//   - GET    /entities                    -- list entities (?type=, ?limit=)
//   - GET    /facts                       -- list facts (?type=, ?subject=, ?limit=)
//   - GET    /relationships               -- list relationships (?type=, ?entity=, ?limit=)
//   - GET    /graph/{id}                  -- entity subgraph (?depth=)
//   - POST   /admin/reset                 -- wipe DB and recreate schema
//   - DELETE /admin/facts                 -- delete facts by source pattern
//   - POST   /admin/deduplicate-entities  -- merge duplicate entities
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	impctx "github.com/aegis-alpha/imprint-mace/internal/context"
	"github.com/aegis-alpha/imprint-mace/internal/db"
	"github.com/aegis-alpha/imprint-mace/internal/imprint"
	"github.com/aegis-alpha/imprint-mace/internal/query"
)

type Handler struct {
	engine  *imprint.Engine
	store   db.Store
	querier *query.Querier
	builder *impctx.Builder
	logger  *slog.Logger
	version string
	mux     *http.ServeMux
}

func NewHandler(engine *imprint.Engine, store db.Store, querier *query.Querier, version string, logger *slog.Logger) *Handler {
	return NewHandlerWithBuilder(engine, store, querier, nil, version, logger)
}

func NewHandlerWithBuilder(engine *imprint.Engine, store db.Store, querier *query.Querier, builder *impctx.Builder, version string, logger *slog.Logger) *Handler {
	h := &Handler{
		engine:  engine,
		store:   store,
		querier: querier,
		builder: builder,
		logger:  logger,
		version: version,
		mux:     http.NewServeMux(),
	}
	h.mux.HandleFunc("/status", h.methodGET(h.handleStatus))
	h.mux.HandleFunc("/entities", h.methodGET(h.handleEntities))
	h.mux.HandleFunc("/facts", h.methodGET(h.handleFacts))
	h.mux.HandleFunc("/relationships", h.methodGET(h.handleRelationships))
	h.mux.HandleFunc("/graph/", h.methodGET(h.handleGraph))
	h.mux.HandleFunc("/query", h.methodGET(h.handleQuery))
	h.mux.HandleFunc("/ingest", h.methodPOST(h.handleIngest))
	h.mux.HandleFunc("/facts/", h.handleFactsRoute)
	if builder != nil {
		h.mux.HandleFunc("/context", h.methodGET(h.handleContext))
	}
	h.mux.HandleFunc("/admin/reset", h.methodPOST(h.handleAdminReset))
	h.mux.HandleFunc("/admin/facts", h.methodDELETE(h.handleAdminDeleteFacts))
	h.mux.HandleFunc("/admin/deduplicate-entities", h.methodPOST(h.handleAdminDedup))
	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

func (h *Handler) methodGET(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		handler(w, r)
	}
}

func (h *Handler) methodPOST(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		handler(w, r)
	}
}

func (h *Handler) methodDELETE(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		handler(w, r)
	}
}

type statusResponse struct {
	Version string    `json:"version"`
	Stats   *db.DBStats `json:"stats"`
}

func (h *Handler) handleStatus(w http.ResponseWriter, r *http.Request) {
	stats, err := h.store.Stats(r.Context())
	if err != nil {
		h.logger.Error("stats failed", "error", err)
		writeError(w, http.StatusInternalServerError, "stats failed")
		return
	}
	writeJSON(w, http.StatusOK, statusResponse{Version: h.version, Stats: stats})
}

func (h *Handler) handleEntities(w http.ResponseWriter, r *http.Request) {
	filter := db.EntityFilter{Limit: 50}
	if v := r.URL.Query().Get("type"); v != "" {
		filter.EntityType = v
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			filter.Limit = n
		}
	}

	entities, err := h.store.ListEntities(r.Context(), filter)
	if err != nil {
		h.logger.Error("list entities failed", "error", err)
		writeError(w, http.StatusInternalServerError, "list entities failed")
		return
	}
	if entities == nil {
		writeJSON(w, http.StatusOK, []struct{}{})
		return
	}
	writeJSON(w, http.StatusOK, entities)
}

func (h *Handler) handleFacts(w http.ResponseWriter, r *http.Request) {
	filter := db.FactFilter{Limit: 50}
	if v := r.URL.Query().Get("type"); v != "" {
		filter.FactType = v
	}
	if v := r.URL.Query().Get("subject"); v != "" {
		filter.Subject = v
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			filter.Limit = n
		}
	}

	facts, err := h.store.ListFacts(r.Context(), filter)
	if err != nil {
		h.logger.Error("list facts failed", "error", err)
		writeError(w, http.StatusInternalServerError, "list facts failed")
		return
	}
	if facts == nil {
		writeJSON(w, http.StatusOK, []struct{}{})
		return
	}
	writeJSON(w, http.StatusOK, facts)
}

func (h *Handler) handleRelationships(w http.ResponseWriter, r *http.Request) {
	filter := db.RelFilter{Limit: 50}
	if v := r.URL.Query().Get("type"); v != "" {
		filter.RelationType = v
	}
	if v := r.URL.Query().Get("entity"); v != "" {
		filter.EntityID = v
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			filter.Limit = n
		}
	}
	rels, err := h.store.ListRelationships(r.Context(), filter)
	if err != nil {
		h.logger.Error("list relationships failed", "error", err)
		writeError(w, http.StatusInternalServerError, "list relationships failed")
		return
	}
	if rels == nil {
		writeJSON(w, http.StatusOK, []struct{}{})
		return
	}
	writeJSON(w, http.StatusOK, rels)
}

func (h *Handler) handleGraph(w http.ResponseWriter, r *http.Request) {
	entityID := strings.TrimPrefix(r.URL.Path, "/graph/")
	if entityID == "" {
		writeError(w, http.StatusBadRequest, "entity ID required: /graph/{id}")
		return
	}

	depth := 2
	if v := r.URL.Query().Get("depth"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			depth = n
		}
	}
	if depth > 5 {
		depth = 5
	}

	graph, err := h.store.GetEntityGraph(r.Context(), entityID, depth)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "entity not found")
			return
		}
		h.logger.Error("graph query failed", "entity_id", entityID, "error", err)
		writeError(w, http.StatusInternalServerError, "graph query failed")
		return
	}
	writeJSON(w, http.StatusOK, graph)
}

func (h *Handler) handleQuery(w http.ResponseWriter, r *http.Request) {
	question := r.URL.Query().Get("q")
	if question == "" {
		writeError(w, http.StatusBadRequest, "query parameter 'q' is required")
		return
	}

	result, err := h.querier.Query(r.Context(), question)
	if err != nil {
		h.logger.Error("query failed", "error", err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) handleContext(w http.ResponseWriter, r *http.Request) {
	hint := r.URL.Query().Get("hint")
	text, err := h.builder.Build(r.Context(), hint)
	if err != nil {
		h.logger.Error("context build failed", "error", err)
		writeError(w, http.StatusInternalServerError, "context build failed")
		return
	}
	writeJSON(w, http.StatusOK, contextResponse{Context: text})
}

type contextResponse struct {
	Context string `json:"context"`
}

type ingestRequest struct {
	Text   string `json:"text"`
	Source string `json:"source"`
}

func (h *Handler) handleIngest(w http.ResponseWriter, r *http.Request) {
	var req ingestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Text == "" {
		writeError(w, http.StatusBadRequest, "'text' field is required")
		return
	}
	if req.Source == "" {
		req.Source = "api"
	}

	result, err := h.engine.Ingest(r.Context(), req.Text, req.Source)
	if err != nil {
		h.logger.Error("ingest failed", "error", err)
		writeError(w, http.StatusInternalServerError, "ingest failed")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

type errorResponse struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorResponse{Error: msg})
}

// --- Admin endpoints ---

func (h *Handler) handleAdminReset(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Confirm-Reset") != "yes" {
		writeError(w, http.StatusBadRequest, "missing X-Confirm-Reset: yes header")
		return
	}

	if err := h.store.Reset(r.Context()); err != nil {
		h.logger.Error("admin reset failed", "error", err)
		writeError(w, http.StatusInternalServerError, "reset failed: "+err.Error())
		return
	}

	if sqlStore, ok := h.store.(interface {
		EnsureVecTable(ctx context.Context, dims int) error
		EnsureChunkVecTable(ctx context.Context, dims int) error
	}); ok {
		ctx := r.Context()
		_ = sqlStore.EnsureVecTable(ctx, 0)      //nolint:errcheck // best-effort after reset; dims unknown
		_ = sqlStore.EnsureChunkVecTable(ctx, 0) //nolint:errcheck // best-effort after reset; dims unknown
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "reset complete"})
}

type deleteFactsRequest struct {
	SourcePattern string `json:"source_pattern"`
}

func (h *Handler) handleAdminDeleteFacts(w http.ResponseWriter, r *http.Request) {
	var req deleteFactsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.SourcePattern == "" {
		writeError(w, http.StatusBadRequest, "'source_pattern' field is required")
		return
	}

	n, err := h.store.DeleteFactsBySourcePattern(r.Context(), req.SourcePattern)
	if err != nil {
		h.logger.Error("admin delete facts failed", "pattern", req.SourcePattern, "error", err)
		writeError(w, http.StatusInternalServerError, "delete facts failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"deleted": n})
}

func (h *Handler) handleAdminDedup(w http.ResponseWriter, r *http.Request) {
	groups, removed, err := h.store.DeduplicateEntities(r.Context())
	if err != nil {
		h.logger.Error("admin deduplicate failed", "error", err)
		writeError(w, http.StatusInternalServerError, "deduplicate failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{
		"merged_groups":    groups,
		"entities_removed": removed,
	})
}

// handleFactsRoute dispatches /facts/{id} and /facts/{id}/supersede
func (h *Handler) handleFactsRoute(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/facts/")
	if path == "" {
		writeError(w, http.StatusBadRequest, "fact ID required")
		return
	}

	if strings.HasSuffix(path, "/supersede") {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		factID := strings.TrimSuffix(path, "/supersede")
		h.handleSupersedeFact(w, r, factID)
		return
	}

	if r.Method != http.MethodPatch {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	h.handlePatchFact(w, r, path)
}

type patchFactRequest struct {
	Confidence *float64 `json:"confidence"`
	ValidUntil *string  `json:"valid_until"`
	Subject    *string  `json:"subject"`
}

func (h *Handler) handlePatchFact(w http.ResponseWriter, r *http.Request, factID string) {
	var req patchFactRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	var update db.FactUpdate
	update.Confidence = req.Confidence
	update.Subject = req.Subject
	if req.ValidUntil != nil {
		t, err := time.Parse(time.RFC3339, *req.ValidUntil)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid valid_until format (expected ISO-8601)")
			return
		}
		update.ValidUntil = &t
	}

	if err := h.store.UpdateFact(r.Context(), factID, update); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "fact not found")
			return
		}
		h.logger.Error("update fact failed", "fact_id", factID, "error", err)
		writeError(w, http.StatusInternalServerError, "update fact failed")
		return
	}

	fact, err := h.store.GetFact(r.Context(), factID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "get updated fact failed")
		return
	}
	writeJSON(w, http.StatusOK, fact)
}

type supersedeRequest struct {
	NewContent string `json:"new_content"`
	Source     string `json:"source"`
}

func (h *Handler) handleSupersedeFact(w http.ResponseWriter, r *http.Request, factID string) {
	var req supersedeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.NewContent == "" {
		writeError(w, http.StatusBadRequest, "'new_content' field is required")
		return
	}
	if req.Source == "" {
		req.Source = "api"
	}

	newFact, err := h.store.SupersedeWithContent(r.Context(), factID, req.NewContent, req.Source)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "fact not found")
			return
		}
		h.logger.Error("supersede fact failed", "fact_id", factID, "error", err)
		writeError(w, http.StatusInternalServerError, "supersede fact failed")
		return
	}
	writeJSON(w, http.StatusOK, newFact)
}
