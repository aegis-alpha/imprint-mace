package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/aegis-alpha/imprint-mace/internal/db"
	"github.com/aegis-alpha/imprint-mace/internal/imprint"
	"github.com/aegis-alpha/imprint-mace/internal/model"
	"github.com/aegis-alpha/imprint-mace/internal/query"
)

type Server struct {
	engine  *imprint.Engine
	store   db.Store
	querier *query.Querier
	logger  *slog.Logger
	mcp     *server.MCPServer
}

func New(engine *imprint.Engine, store db.Store, querier *query.Querier, version string, logger *slog.Logger) *Server {
	s := &Server{
		engine:  engine,
		store:   store,
		querier: querier,
		logger:  logger,
	}

	s.mcp = server.NewMCPServer(
		"imprint",
		version,
		server.WithToolCapabilities(false),
	)

	s.mcp.AddTool(
		mcp.NewTool("imprint_ingest",
			mcp.WithDescription("Extract facts, entities, and relationships from text and store in the knowledge graph."),
			mcp.WithString("text",
				mcp.Required(),
				mcp.Description("Text to extract knowledge from"),
			),
			mcp.WithString("source",
				mcp.Description("Source identifier (e.g. session ID, filename)"),
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

	return s
}

func (s *Server) Run(ctx context.Context) error {
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

	result, err := s.engine.Ingest(ctx, text, source)
	if err != nil {
		s.logger.Error("ingest failed", "error", err)
		return mcp.NewToolResultError(fmt.Sprintf("ingest failed: %v", err)), nil
	}

	data, _ := json.Marshal(result)
	return mcp.NewToolResultText(string(data)), nil
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
