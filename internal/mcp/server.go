package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	impctx "github.com/aegis-alpha/imprint-mace/internal/context"
	"github.com/aegis-alpha/imprint-mace/internal/db"
	"github.com/aegis-alpha/imprint-mace/internal/imprint"
	"github.com/aegis-alpha/imprint-mace/internal/ingest"
	"github.com/aegis-alpha/imprint-mace/internal/model"
	"github.com/aegis-alpha/imprint-mace/internal/provider"
	"github.com/aegis-alpha/imprint-mace/internal/query"
)

type Server struct {
	engine           *imprint.Engine
	store            db.Store
	querier          *query.Querier
	builder          *impctx.Builder
	logger           *slog.Logger
	mcp              *server.MCPServer
	embedder         provider.Embedder
	hotEnabled       bool
	hotEmbedMinChars int
}

func New(engine *imprint.Engine, store db.Store, querier *query.Querier, version string, logger *slog.Logger) *Server {
	return NewWithBuilder(engine, store, querier, nil, version, logger)
}

func NewWithBuilder(engine *imprint.Engine, store db.Store, querier *query.Querier, builder *impctx.Builder, version string, logger *slog.Logger) *Server {
	s := &Server{
		engine:           engine,
		store:            store,
		querier:          querier,
		builder:          builder,
		logger:           logger,
		hotEmbedMinChars: 50,
	}

	opts := []server.ServerOption{
		server.WithToolCapabilities(false),
	}
	if builder != nil {
		opts = append(opts, server.WithResourceCapabilities(false, true))
	}

	s.mcp = server.NewMCPServer(
		"imprint",
		version,
		opts...,
	)

	s.mcp.AddTool(
		mcp.NewTool("imprint_ingest",
			mcp.WithDescription("Store knowledge from text. When hot phase is enabled, stores the raw message for immediate search (no LLM). When disabled, extracts facts, entities, and relationships via LLM."),
			mcp.WithString("text",
				mcp.Required(),
				mcp.Description("Text to extract knowledge from"),
			),
			mcp.WithString("source",
				mcp.Description("Source identifier (e.g. realtime:sessionId, filename)"),
			),
			mcp.WithString("mode",
				mcp.Description(`Optional. Set to "extract" to force LLM extraction when hot phase is enabled.`),
			),
		),
		s.handleIngest,
	)

	s.mcp.AddTool(
		mcp.NewTool("imprint_status",
			mcp.WithDescription("Show knowledge base statistics: counts of facts, entities, relationships, consolidations."),
		),
		s.handleStatus,
	)

	s.mcp.AddTool(
		mcp.NewTool("imprint_entities",
			mcp.WithDescription("List entities in the knowledge graph, optionally filtered by type."),
			mcp.WithString("type",
				mcp.Description("Filter by entity type (person, project, tool, server, concept, organization, location, document, agent)"),
			),
			mcp.WithNumber("limit",
				mcp.Description("Max results (default 50)"),
			),
		),
		s.handleEntities,
	)

	s.mcp.AddTool(
		mcp.NewTool("imprint_graph",
			mcp.WithDescription("Get the subgraph around an entity: connected entities and relationships up to a given depth."),
			mcp.WithString("entity",
				mcp.Required(),
				mcp.Description("Entity name (case-insensitive lookup)"),
			),
			mcp.WithNumber("depth",
				mcp.Description("Traversal depth (default 2, max 5)"),
			),
		),
		s.handleGraph,
	)

	if querier != nil {
		s.mcp.AddTool(
			mcp.NewTool("imprint_query",
				mcp.WithDescription("Ask a question against the knowledge base. Returns an answer with citations to supporting facts."),
				mcp.WithString("question",
					mcp.Required(),
					mcp.Description("Natural language question"),
				),
			),
			s.handleQuery,
		)
	}

	s.mcp.AddTool(
		mcp.NewTool("imprint_update_fact",
			mcp.WithDescription("Update metadata on an existing fact (confidence, expiry, subject). Does not change the fact content -- use imprint_supersede_fact for that."),
			mcp.WithString("fact_id", mcp.Required(), mcp.Description("ID of the fact to update")),
			mcp.WithNumber("confidence", mcp.Description("New confidence score (0.0 to 1.0)")),
			mcp.WithString("valid_until", mcp.Description("Expiry date (ISO-8601). Set to mark fact as time-limited.")),
			mcp.WithString("subject", mcp.Description("Corrected subject")),
		),
		s.handleUpdateFact,
	)

	s.mcp.AddTool(
		mcp.NewTool("imprint_supersede_fact",
			mcp.WithDescription("Replace a fact with updated content. The old fact is marked as superseded; a new fact is created with the corrected content."),
			mcp.WithString("old_fact_id", mcp.Required(), mcp.Description("ID of the fact to supersede")),
			mcp.WithString("new_content", mcp.Required(), mcp.Description("The corrected/updated fact content")),
			mcp.WithString("source", mcp.Description("Source identifier (default: mcp)")),
		),
		s.handleSupersedeFact,
	)

	s.mcp.AddTool(
		mcp.NewTool("imprint_relationships",
			mcp.WithDescription("List relationships in the knowledge graph, optionally filtered by type or entity."),
			mcp.WithString("type",
				mcp.Description("Filter by relation type (owns, uses, works_on, depends_on, related_to, created_by, part_of, manages, located_at)"),
			),
			mcp.WithString("entity",
				mcp.Description("Filter by entity ID (matches from_entity or to_entity)"),
			),
			mcp.WithNumber("limit",
				mcp.Description("Max results (default 50)"),
			),
		),
		s.handleRelationships,
	)

	if builder != nil {
		s.mcp.AddResource(
			mcp.NewResource("imprint://context/relevant", "Relevant Context",
				mcp.WithResourceDescription("Relevant facts, preferences, and recent knowledge"),
				mcp.WithMIMEType("text/plain"),
			),
			s.handleContextRelevant,
		)
		s.mcp.AddResource(
			mcp.NewResource("imprint://context/preferences", "User Preferences",
				mcp.WithResourceDescription("All stored user preferences"),
				mcp.WithMIMEType("text/plain"),
			),
			s.handleContextPreferences,
		)
		s.mcp.AddResource(
			mcp.NewResource("imprint://context/recent", "Recent Facts",
				mcp.WithResourceDescription("Facts created in the last N hours"),
				mcp.WithMIMEType("text/plain"),
			),
			s.handleContextRecent,
		)
		s.mcp.AddResourceTemplate(
			mcp.NewResourceTemplate("imprint://context/entities/{name}", "Entity Context",
				mcp.WithTemplateDescription("Context about a specific entity"),
				mcp.WithTemplateMIMEType("text/plain"),
			),
			s.handleContextEntity,
		)
	}

	return s
}

// SetEmbedder sets the embedding provider for hot-path ingest (nil disables hot embeddings).
func (s *Server) SetEmbedder(e provider.Embedder) {
	s.embedder = e
}

// SetHotEnabled switches imprint_ingest to raw hot storage when true.
func (s *Server) SetHotEnabled(enabled bool) {
	s.hotEnabled = enabled
}

// SetHotEmbedMinChars sets the minimum text length for synchronous embedding on hot ingest (default 50).
func (s *Server) SetHotEmbedMinChars(n int) {
	if n <= 0 {
		n = 50
	}
	s.hotEmbedMinChars = n
}

// Run starts the MCP server on stdin/stdout.
// ctx is accepted for interface compatibility but not used --
// mcp-go ServeStdio does not support context cancellation.
func (s *Server) Run(_ context.Context) error {
	return server.ServeStdio(s.mcp)
}

func (s *Server) handleIngest(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	text, err := req.RequireString("text")
	if err != nil {
		return mcp.NewToolResultError("text is required"), nil
	}

	source := "mcp"
	if v, err := req.RequireString("source"); err == nil && v != "" {
		source = v
	}

	mode := ""
	if v, err := req.RequireString("mode"); err == nil {
		mode = strings.TrimSpace(v)
	}

	if s.hotEnabled && !strings.EqualFold(mode, "extract") {
		return s.handleHotIngest(ctx, text, source)
	}

	result, err := s.engine.Ingest(ctx, text, source)
	if err != nil {
		s.logger.Error("ingest failed", "error", err)
		return mcp.NewToolResultError(fmt.Sprintf("ingest failed: %v", err)), nil
	}

	data, _ := json.Marshal(result)
	return mcp.NewToolResultText(string(data)), nil
}

type hotIngestMCPResponse struct {
	ID           string `json:"id"`
	HasEmbedding bool   `json:"has_embedding"`
	Hot          bool   `json:"hot"`
}

func (s *Server) handleHotIngest(ctx context.Context, text, source string) (*mcp.CallToolResult, error) {
	now := time.Now().UTC()
	platform, psid := ingest.ParseHotIngestSource(source)

	minChars := s.hotEmbedMinChars
	if minChars <= 0 {
		minChars = 50
	}

	msg := &model.HotMessage{
		ID:                db.NewID(),
		Speaker:           "user",
		Content:           text,
		Timestamp:         now,
		Platform:          platform,
		PlatformSessionID: psid,
		HasEmbedding:      false,
		CreatedAt:         now,
	}

	var embedding []float32
	if len(text) >= minChars && s.embedder != nil {
		vec, err := s.embedder.Embed(ctx, text)
		if err != nil {
			s.logger.Warn("hot ingest embedding failed", "error", err)
		} else {
			embedding = vec
			msg.HasEmbedding = true
		}
	}

	if err := ingest.ApplyHotLinkerRef(ctx, s.store, msg); err != nil {
		s.logger.Warn("hot linker ref failed", "error", err)
	}

	if err := s.store.InsertHotMessage(ctx, msg, embedding); err != nil {
		s.logger.Error("hot ingest insert failed", "error", err)
		return mcp.NewToolResultError(fmt.Sprintf("ingest failed: %v", err)), nil
	}

	out, _ := json.Marshal(hotIngestMCPResponse{ID: msg.ID, HasEmbedding: msg.HasEmbedding, Hot: true})
	return mcp.NewToolResultText(string(out)), nil
}

func (s *Server) handleStatus(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	stats, err := s.store.Stats(ctx)
	if err != nil {
		s.logger.Error("stats failed", "error", err)
		return mcp.NewToolResultError(fmt.Sprintf("stats failed: %v", err)), nil
	}

	data, _ := json.Marshal(stats)
	return mcp.NewToolResultText(string(data)), nil
}

func (s *Server) handleEntities(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	filter := db.EntityFilter{Limit: 50}

	if v, err := req.RequireString("type"); err == nil && v != "" {
		filter.EntityType = v
	}
	if v, err := req.RequireFloat("limit"); err == nil && v > 0 {
		filter.Limit = int(v)
	}

	entities, err := s.store.ListEntities(ctx, filter)
	if err != nil {
		s.logger.Error("list entities failed", "error", err)
		return mcp.NewToolResultError(fmt.Sprintf("list entities failed: %v", err)), nil
	}
	if entities == nil {
		entities = []model.Entity{}
	}

	data, _ := json.Marshal(entities)
	return mcp.NewToolResultText(string(data)), nil
}

func (s *Server) handleGraph(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name, err := req.RequireString("entity")
	if err != nil {
		return mcp.NewToolResultError("entity is required"), nil
	}

	depth := 2
	if v, err := req.RequireFloat("depth"); err == nil && v > 0 {
		depth = int(v)
	}
	if depth > 5 {
		depth = 5
	}

	entity, err := s.store.GetEntityByName(ctx, name)
	if err != nil {
		s.logger.Error("entity lookup failed", "name", name, "error", err)
		return mcp.NewToolResultError(fmt.Sprintf("entity %q not found", name)), nil
	}

	graph, err := s.store.GetEntityGraph(ctx, entity.ID, depth)
	if err != nil {
		s.logger.Error("graph query failed", "entity", name, "error", err)
		return mcp.NewToolResultError(fmt.Sprintf("graph query failed: %v", err)), nil
	}

	data, _ := json.Marshal(graph)
	return mcp.NewToolResultText(string(data)), nil
}

func (s *Server) handleQuery(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	question, err := req.RequireString("question")
	if err != nil {
		return mcp.NewToolResultError("question is required"), nil
	}

	result, err := s.querier.Query(ctx, question)
	if err != nil {
		s.logger.Error("query failed", "error", err)
		return mcp.NewToolResultError(fmt.Sprintf("query failed: %v", err)), nil
	}

	data, _ := json.Marshal(result)
	return mcp.NewToolResultText(string(data)), nil
}

func (s *Server) handleUpdateFact(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	factID, err := req.RequireString("fact_id")
	if err != nil {
		return mcp.NewToolResultError("fact_id is required"), nil
	}

	var update db.FactUpdate
	if v, err := req.RequireFloat("confidence"); err == nil {
		update.Confidence = &v
	}
	if v, err := req.RequireString("valid_until"); err == nil && v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("invalid valid_until format: %v", err)), nil
		}
		update.ValidUntil = &t
	}
	if v, err := req.RequireString("subject"); err == nil && v != "" {
		update.Subject = &v
	}

	if err := s.store.UpdateFact(ctx, factID, update); err != nil {
		s.logger.Error("update fact failed", "fact_id", factID, "error", err)
		return mcp.NewToolResultError(fmt.Sprintf("update fact failed: %v", err)), nil
	}

	fact, err := s.store.GetFact(ctx, factID)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("get updated fact: %v", err)), nil
	}

	data, _ := json.Marshal(fact)
	return mcp.NewToolResultText(string(data)), nil
}

func (s *Server) handleSupersedeFact(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	oldFactID, err := req.RequireString("old_fact_id")
	if err != nil {
		return mcp.NewToolResultError("old_fact_id is required"), nil
	}

	newContent, err := req.RequireString("new_content")
	if err != nil {
		return mcp.NewToolResultError("new_content is required"), nil
	}

	source := "mcp"
	if v, err := req.RequireString("source"); err == nil && v != "" {
		source = v
	}

	newFact, err := s.store.SupersedeWithContent(ctx, oldFactID, newContent, source)
	if err != nil {
		s.logger.Error("supersede fact failed", "old_fact_id", oldFactID, "error", err)
		return mcp.NewToolResultError(fmt.Sprintf("supersede fact failed: %v", err)), nil
	}

	data, _ := json.Marshal(newFact)
	return mcp.NewToolResultText(string(data)), nil
}

func (s *Server) handleRelationships(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	filter := db.RelFilter{Limit: 50}
	if v, err := req.RequireString("type"); err == nil && v != "" {
		filter.RelationType = v
	}
	if v, err := req.RequireString("entity"); err == nil && v != "" {
		filter.EntityID = v
	}
	if v, err := req.RequireFloat("limit"); err == nil && v > 0 {
		filter.Limit = int(v)
	}
	rels, err := s.store.ListRelationships(ctx, filter)
	if err != nil {
		s.logger.Error("list relationships failed", "error", err)
		return mcp.NewToolResultError(fmt.Sprintf("list relationships failed: %v", err)), nil
	}
	if rels == nil {
		rels = []model.Relationship{}
	}
	data, _ := json.Marshal(rels)
	return mcp.NewToolResultText(string(data)), nil
}

// --- MCP Resource handlers ---

func (s *Server) handleContextRelevant(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
	text, err := s.builder.Build(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("build context: %w", err)
	}
	return []mcp.ResourceContents{
		mcp.TextResourceContents{
			URI:      "imprint://context/relevant",
			MIMEType: "text/plain",
			Text:     text,
		},
	}, nil
}

func (s *Server) handleContextPreferences(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
	prefs, err := s.store.ListFacts(ctx, db.FactFilter{FactType: "preference"})
	if err != nil {
		return nil, fmt.Errorf("list preferences: %w", err)
	}
	var lines []string
	for i := range prefs {
		lines = append(lines, fmt.Sprintf("- %s: %s", prefs[i].Subject, prefs[i].Content))
	}
	text := strings.Join(lines, "\n")
	return []mcp.ResourceContents{
		mcp.TextResourceContents{
			URI:      "imprint://context/preferences",
			MIMEType: "text/plain",
			Text:     text,
		},
	}, nil
}

func (s *Server) handleContextRecent(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
	cutoff := time.Now().UTC().Add(-24 * time.Hour)
	facts, err := s.store.ListFacts(ctx, db.FactFilter{CreatedAfter: &cutoff})
	if err != nil {
		return nil, fmt.Errorf("list recent facts: %w", err)
	}
	var lines []string
	for i := range facts {
		date := facts[i].CreatedAt.Format("2006-01-02 15:04")
		lines = append(lines, fmt.Sprintf("- [%s] %s: %s (%s)", facts[i].FactType, facts[i].Subject, facts[i].Content, date))
	}
	text := strings.Join(lines, "\n")
	return []mcp.ResourceContents{
		mcp.TextResourceContents{
			URI:      "imprint://context/recent",
			MIMEType: "text/plain",
			Text:     text,
		},
	}, nil
}

func (s *Server) handleContextEntity(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
	uri := req.Params.URI
	prefix := "imprint://context/entities/"
	if !strings.HasPrefix(uri, prefix) {
		return nil, fmt.Errorf("invalid entity resource URI: %s", uri)
	}
	name := strings.TrimPrefix(uri, prefix)
	if name == "" {
		return nil, fmt.Errorf("entity name is required")
	}

	entity, err := s.store.GetEntityByName(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("entity %q not found: %w", name, err)
	}

	graph, err := s.store.GetEntityGraph(ctx, entity.ID, 2)
	if err != nil {
		return nil, fmt.Errorf("entity graph: %w", err)
	}

	var lines []string
	lines = append(lines, fmt.Sprintf("# %s (%s)", entity.Name, entity.EntityType))
	if len(entity.Aliases) > 0 {
		lines = append(lines, fmt.Sprintf("Aliases: %s", strings.Join(entity.Aliases, ", ")))
	}
	lines = append(lines, "")

	if len(graph.Relationships) > 0 {
		lines = append(lines, "## Relationships")
		entityNames := map[string]string{}
		for _, e := range graph.Entities {
			entityNames[e.ID] = e.Name
		}
		for _, r := range graph.Relationships {
			from := entityNames[r.FromEntity]
			to := entityNames[r.ToEntity]
			lines = append(lines, fmt.Sprintf("- %s -[%s]-> %s", from, r.RelationType, to))
		}
	}

	text := strings.Join(lines, "\n")
	return []mcp.ResourceContents{
		mcp.TextResourceContents{
			URI:      uri,
			MIMEType: "text/plain",
			Text:     text,
		},
	}, nil
}
