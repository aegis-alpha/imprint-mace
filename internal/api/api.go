// Package api provides an HTTP REST API for OpenClaw-Memory.
//
// Endpoints:
//   - POST /ingest         -- extract facts from text
//   - GET  /query?q=...    -- ask a question
//   - GET  /status         -- database statistics
//   - GET  /entities       -- list entities (?type=, ?limit=)
//   - GET  /facts          -- list facts (?type=, ?subject=, ?limit=)
//   - GET  /graph/{id}     -- entity subgraph (?depth=)
package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aegis-alpha/imprint-mace/internal/db"
	"github.com/aegis-alpha/imprint-mace/internal/imprint"
	"github.com/aegis-alpha/imprint-mace/internal/query"
)

type Handler struct {
	engine  *imprint.Engine
	store   db.Store
	querier *query.Querier
	logger  *slog.Logger
	version string
	mux     *http.ServeMux
}

func NewHandler(engine *imprint.Engine, store db.Store, querier *query.Querier, version string, logger *slog.Logger) *Handler {
	h := &Handler{
		engine:  engine,
		store:   store,
		querier: querier,
		logger:  logger,
		version: version,
		mux:     http.NewServeMux(),
	}
	h.mux.HandleFunc("/status", h.methodGET(h.handleStatus))
	h.mux.HandleFunc("/entities", h.methodGET(h.handleEntities))
	h.mux.HandleFunc("/facts", h.methodGET(h.handleFacts))
	h.mux.HandleFunc("/graph/", h.methodGET(h.handleGraph))
	h.mux.HandleFunc("/query", h.methodGET(h.handleQuery))
	h.mux.HandleFunc("/ingest", h.methodPOST(h.handleIngest))
	h.mux.HandleFunc("/facts/", h.handleFactsRoute)
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
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorResponse{Error: msg})
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
