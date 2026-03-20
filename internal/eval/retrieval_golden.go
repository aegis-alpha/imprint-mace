package eval

import "github.com/aegis-alpha/imprint-mace/internal/model"

// RetrievalGoldenExample is one retrieval eval question with expected results.
type RetrievalGoldenExample struct {
	Question         string   `json:"question"`
	ExpectedFacts    []string `json:"expected_facts"`
	ExpectedEntities []string `json:"expected_entities"`
	Category         string   `json:"category"`
}

// RetrievalSeedData contains the facts, entities, and relationships
// to populate a test DB for retrieval evaluation.
type RetrievalSeedData struct {
	Facts         []model.Fact
	Entities      []model.Entity
	Relationships []model.Relationship
}

// BuiltinRetrievalSeed returns the seed data for the retrieval eval DB.
// Facts reuse the Acme/Alice/mars universe from the extraction golden set.
func BuiltinRetrievalSeed() RetrievalSeedData {
	return RetrievalSeedData{
		Facts:         builtinSeedFacts,
		Entities:      builtinSeedEntities,
		Relationships: builtinSeedRelationships,
	}
}

// BuiltinRetrievalExamples returns the built-in retrieval golden questions.
func BuiltinRetrievalExamples() []RetrievalGoldenExample {
	return builtinRetrievalExamples
}

var builtinSeedFacts = []model.Fact{
	{ID: "f-acme-go", FactType: "decision", Subject: "Acme", Content: "Acme will be written in Go for single-binary deployment with no runtime dependencies."},
	{ID: "f-acme-toml", FactType: "decision", Subject: "Acme", Content: "Acme uses TOML for configuration because it supports comments and nested structures."},
	{ID: "f-acme-layout", FactType: "decision", Subject: "Acme", Content: "Acme uses standard Go project layout with cmd/ and internal/ directories."},
	{ID: "f-alice-telegram", FactType: "preference", Subject: "Alice", Content: "Alice prefers receiving updates via Telegram rather than email."},
	{ID: "f-bob-api", FactType: "contact", Subject: "Bob", Content: "Bob is on the API team."},
	{ID: "f-acme-mvp", FactType: "goal", Subject: "Acme", Content: "Goal is to have the Acme MVP running on mars by end of March."},
	{ID: "f-no-friday", FactType: "rule", Subject: "deployment", Content: "Deployments are never done on Fridays, no exceptions."},
	{ID: "f-ci-green", FactType: "rule", Subject: "CI", Content: "Merges to main require passing CI (green build)."},
	{ID: "f-mars-oom", FactType: "event", Subject: "mars", Content: "mars server went down due to memory exhaustion running local-llm at 27B parameters."},
	{ID: "f-mars-cap", FactType: "lesson", Subject: "mars", Content: "Concurrent model loads on mars must be capped at 2 to prevent memory exhaustion."},
	{ID: "f-node1-inference", FactType: "rule", Subject: "inference", Content: "node-1 should take over inference tasks when mars is overloaded."},
	{ID: "f-node2-routing", FactType: "project", Subject: "node-2", Content: "node-2 depends on node-1 for routing."},
	{ID: "f-phoenix-dashboard", FactType: "project", Subject: "Phoenix", Content: "Phoenix is an internal dashboard application."},
	{ID: "f-phoenix-aws", FactType: "project", Subject: "Phoenix", Content: "Phoenix runs on three nodes in AWS us-east-1."},
	{ID: "f-sarah-platform", FactType: "contact", Subject: "Sarah", Content: "Sarah leads the platform team."},
	{ID: "f-marcus-devops", FactType: "contact", Subject: "Marcus", Content: "Marcus is the DevOps lead."},
	{ID: "f-david-vp", FactType: "contact", Subject: "David Chen", Content: "David Chen is the VP of Engineering."},
	{ID: "f-deploy-workflow", FactType: "workflow", Subject: "deployment", Content: "Deploy procedure: merge to main, wait for CI green, run db:migrate on staging, smoke test staging, promote to production, monitor for 30 minutes."},
	{ID: "f-v2api-q2", FactType: "goal", Subject: "v2 API", Content: "The v2 API must ship by end of Q2."},
	{ID: "f-mobile-depends", FactType: "project", Subject: "mobile app", Content: "The mobile app launch depends on the v2 API being complete."},
	{ID: "f-nosql-lesson", FactType: "lesson", Subject: "database", Content: "Relational databases should be used for any workload requiring ACID transactions; NoSQL was a disaster for transactional data."},
	{ID: "f-user-go-rust", FactType: "skill", Subject: "User", Content: "User is fluent in Go, Rust, and Python."},
	{ID: "f-ana-k8s", FactType: "skill", Subject: "Ana", Content: "Ana is the Kubernetes expert on the team."},
	{ID: "f-meridian-analytics", FactType: "project", Subject: "Meridian", Content: "Meridian is a real-time analytics platform."},
	{ID: "f-meridian-go-react", FactType: "decision", Subject: "Meridian", Content: "Meridian is written in Go with a React frontend."},
	{ID: "f-meridian-gcp", FactType: "project", Subject: "Meridian", Content: "Meridian is deployed on GCP in europe-west1."},
	{ID: "f-meridian-clickhouse", FactType: "decision", Subject: "Meridian", Content: "Meridian uses ClickHouse for analytics and Redis for caching."},
	{ID: "f-meridian-1m", FactType: "goal", Subject: "Meridian", Content: "The goal is to process 1 million events per second by Q3."},
	{ID: "f-checkout-deps", FactType: "project", Subject: "checkout service", Content: "The checkout service depends on the inventory API and the payment gateway; if either goes down, checkout fails."},
	{ID: "f-prod1-server", FactType: "project", Subject: "prod-1", Content: "prod-1 is an application server behind a load balancer."},
	{ID: "f-dbprimary-pg", FactType: "project", Subject: "db-primary", Content: "db-primary is the PostgreSQL master database server."},
	{ID: "f-hetzner-dc", FactType: "project", Subject: "infrastructure", Content: "All servers are hosted in the Hetzner Falkenstein datacenter."},
}

var builtinSeedEntities = []model.Entity{
	{ID: "e-alice", Name: "Alice", EntityType: "person"},
	{ID: "e-bob", Name: "Bob", EntityType: "person"},
	{ID: "e-acme", Name: "Acme", EntityType: "project"},
	{ID: "e-mars", Name: "mars", EntityType: "server"},
	{ID: "e-local-llm", Name: "local-llm", EntityType: "tool"},
	{ID: "e-node1", Name: "node-1", EntityType: "server"},
	{ID: "e-node2", Name: "node-2", EntityType: "server"},
	{ID: "e-phoenix", Name: "Phoenix", EntityType: "project"},
	{ID: "e-useast1", Name: "us-east-1", EntityType: "location"},
	{ID: "e-sarah", Name: "Sarah", EntityType: "person"},
	{ID: "e-marcus", Name: "Marcus", EntityType: "person"},
	{ID: "e-david", Name: "David Chen", EntityType: "person"},
	{ID: "e-v2api", Name: "v2 API", EntityType: "project"},
	{ID: "e-mobile", Name: "mobile app", EntityType: "project"},
	{ID: "e-user", Name: "User", EntityType: "person"},
	{ID: "e-ana", Name: "Ana", EntityType: "person"},
	{ID: "e-meridian", Name: "Meridian", EntityType: "project"},
	{ID: "e-jake", Name: "Jake", EntityType: "person"},
	{ID: "e-lisa", Name: "Lisa", EntityType: "person"},
	{ID: "e-europewest1", Name: "europe-west1", EntityType: "location"},
	{ID: "e-clickhouse", Name: "ClickHouse", EntityType: "tool"},
	{ID: "e-redis", Name: "Redis", EntityType: "tool"},
	{ID: "e-checkout", Name: "checkout service", EntityType: "project"},
	{ID: "e-inventory", Name: "inventory API", EntityType: "project"},
	{ID: "e-paygateway", Name: "payment gateway", EntityType: "project"},
	{ID: "e-prod1", Name: "prod-1", EntityType: "server"},
	{ID: "e-prod2", Name: "prod-2", EntityType: "server"},
	{ID: "e-dbprimary", Name: "db-primary", EntityType: "server"},
	{ID: "e-dbreplica1", Name: "db-replica-1", EntityType: "server"},
	{ID: "e-dbreplica2", Name: "db-replica-2", EntityType: "server"},
	{ID: "e-cache1", Name: "cache-1", EntityType: "server"},
	{ID: "e-hetzner", Name: "Hetzner", EntityType: "organization"},
	{ID: "e-falkenstein", Name: "Falkenstein", EntityType: "location"},
}

var builtinSeedRelationships = []model.Relationship{
	{ID: "r-alice-acme", FromEntity: "e-alice", ToEntity: "e-acme", RelationType: "works_on", SourceFact: "f-acme-go"},
	{ID: "r-bob-acme", FromEntity: "e-bob", ToEntity: "e-acme", RelationType: "works_on", SourceFact: "f-bob-api"},
	{ID: "r-acme-mars", FromEntity: "e-acme", ToEntity: "e-mars", RelationType: "located_at", SourceFact: "f-acme-mvp"},
	{ID: "r-mars-llm", FromEntity: "e-mars", ToEntity: "e-local-llm", RelationType: "uses", SourceFact: "f-mars-oom"},
	{ID: "r-node2-node1", FromEntity: "e-node2", ToEntity: "e-node1", RelationType: "depends_on", SourceFact: "f-node2-routing"},
	{ID: "r-phoenix-useast1", FromEntity: "e-phoenix", ToEntity: "e-useast1", RelationType: "located_at", SourceFact: "f-phoenix-aws"},
	{ID: "r-david-sarah", FromEntity: "e-david", ToEntity: "e-sarah", RelationType: "manages", SourceFact: "f-david-vp"},
	{ID: "r-david-marcus", FromEntity: "e-david", ToEntity: "e-marcus", RelationType: "manages", SourceFact: "f-david-vp"},
	{ID: "r-mobile-v2api", FromEntity: "e-mobile", ToEntity: "e-v2api", RelationType: "depends_on", SourceFact: "f-mobile-depends"},
	{ID: "r-jake-meridian", FromEntity: "e-jake", ToEntity: "e-meridian", RelationType: "works_on", SourceFact: "f-meridian-analytics"},
	{ID: "r-lisa-meridian", FromEntity: "e-lisa", ToEntity: "e-meridian", RelationType: "works_on", SourceFact: "f-meridian-analytics"},
	{ID: "r-meridian-ch", FromEntity: "e-meridian", ToEntity: "e-clickhouse", RelationType: "uses", SourceFact: "f-meridian-clickhouse"},
	{ID: "r-meridian-redis", FromEntity: "e-meridian", ToEntity: "e-redis", RelationType: "uses", SourceFact: "f-meridian-clickhouse"},
	{ID: "r-checkout-inv", FromEntity: "e-checkout", ToEntity: "e-inventory", RelationType: "depends_on", SourceFact: "f-checkout-deps"},
	{ID: "r-checkout-pay", FromEntity: "e-checkout", ToEntity: "e-paygateway", RelationType: "depends_on", SourceFact: "f-checkout-deps"},
	{ID: "r-dbreplica1-primary", FromEntity: "e-dbreplica1", ToEntity: "e-dbprimary", RelationType: "depends_on", SourceFact: "f-dbprimary-pg"},
	{ID: "r-dbreplica2-primary", FromEntity: "e-dbreplica2", ToEntity: "e-dbprimary", RelationType: "depends_on", SourceFact: "f-dbprimary-pg"},
}

var builtinRetrievalExamples = []RetrievalGoldenExample{
	// --- direct_lookup: single fact, keyword match ---
	{
		Question:         "What language is Acme written in?",
		ExpectedFacts:    []string{"f-acme-go"},
		ExpectedEntities: []string{"Acme"},
		Category:         "direct_lookup",
	},
	{
		Question:         "What config format does Acme use?",
		ExpectedFacts:    []string{"f-acme-toml"},
		ExpectedEntities: []string{"Acme"},
		Category:         "direct_lookup",
	},
	{
		Question:         "How does Alice prefer to receive updates?",
		ExpectedFacts:    []string{"f-alice-telegram"},
		ExpectedEntities: []string{"Alice"},
		Category:         "direct_lookup",
	},
	{
		Question:         "Can we deploy on Fridays?",
		ExpectedFacts:    []string{"f-no-friday"},
		ExpectedEntities: []string{},
		Category:         "direct_lookup",
	},
	{
		Question:         "What is the deployment workflow?",
		ExpectedFacts:    []string{"f-deploy-workflow"},
		ExpectedEntities: []string{},
		Category:         "direct_lookup",
	},
	{
		Question:         "What happened to the mars server?",
		ExpectedFacts:    []string{"f-mars-oom"},
		ExpectedEntities: []string{"mars"},
		Category:         "direct_lookup",
	},
	{
		Question:         "What is Phoenix?",
		ExpectedFacts:    []string{"f-phoenix-dashboard"},
		ExpectedEntities: []string{"Phoenix"},
		Category:         "direct_lookup",
	},
	{
		Question:         "What programming languages does the User know?",
		ExpectedFacts:    []string{"f-user-go-rust"},
		ExpectedEntities: []string{"User"},
		Category:         "direct_lookup",
	},
	{
		Question:         "What lesson was learned about NoSQL?",
		ExpectedFacts:    []string{"f-nosql-lesson"},
		ExpectedEntities: []string{},
		Category:         "direct_lookup",
	},
	// --- graph_traversal: requires following entity relationships ---
	{
		Question:         "What servers does Alice manage through Acme?",
		ExpectedFacts:    []string{"f-acme-mvp", "f-acme-go"},
		ExpectedEntities: []string{"Alice", "Acme", "mars"},
		Category:         "graph_traversal",
	},
	{
		Question:         "Who reports to David Chen?",
		ExpectedFacts:    []string{"f-sarah-platform", "f-marcus-devops", "f-david-vp"},
		ExpectedEntities: []string{"David Chen", "Sarah", "Marcus"},
		Category:         "graph_traversal",
	},
	{
		Question:         "What does the checkout service depend on?",
		ExpectedFacts:    []string{"f-checkout-deps"},
		ExpectedEntities: []string{"checkout service", "inventory API", "payment gateway"},
		Category:         "graph_traversal",
	},
	{
		Question:         "What tools does Meridian use?",
		ExpectedFacts:    []string{"f-meridian-clickhouse"},
		ExpectedEntities: []string{"Meridian", "ClickHouse", "Redis"},
		Category:         "graph_traversal",
	},
	{
		Question:         "What depends on node-1?",
		ExpectedFacts:    []string{"f-node2-routing", "f-node1-inference"},
		ExpectedEntities: []string{"node-1", "node-2"},
		Category:         "graph_traversal",
	},
	// --- temporal: time-sensitive questions ---
	{
		Question:         "When must the v2 API ship?",
		ExpectedFacts:    []string{"f-v2api-q2"},
		ExpectedEntities: []string{"v2 API"},
		Category:         "temporal",
	},
	{
		Question:         "What is the Meridian performance target and deadline?",
		ExpectedFacts:    []string{"f-meridian-1m"},
		ExpectedEntities: []string{"Meridian"},
		Category:         "temporal",
	},
	// --- multi_hop: requires combining facts from multiple sources ---
	{
		Question:         "What blocks the mobile app launch and when is the deadline?",
		ExpectedFacts:    []string{"f-mobile-depends", "f-v2api-q2"},
		ExpectedEntities: []string{"mobile app", "v2 API"},
		Category:         "multi_hop",
	},
	{
		Question:         "What infrastructure does Meridian use and where is it deployed?",
		ExpectedFacts:    []string{"f-meridian-gcp", "f-meridian-clickhouse", "f-meridian-go-react"},
		ExpectedEntities: []string{"Meridian", "europe-west1", "ClickHouse", "Redis"},
		Category:         "multi_hop",
	},
	{
		Question:         "What are the rules for merging and deploying code?",
		ExpectedFacts:    []string{"f-ci-green", "f-no-friday", "f-deploy-workflow"},
		ExpectedEntities: []string{},
		Category:         "multi_hop",
	},
	// --- noise: questions with no answer in KB ---
	{
		Question:         "What is the company's revenue for Q4?",
		ExpectedFacts:    []string{},
		ExpectedEntities: []string{},
		Category:         "noise",
	},
	{
		Question:         "How do I configure Kubernetes ingress controllers?",
		ExpectedFacts:    []string{},
		ExpectedEntities: []string{},
		Category:         "noise",
	},
}
