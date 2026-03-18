package eval

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// GenerateResult reports what was generated.
type GenerateResult struct {
	Dir      string
	Total    int
	Positive int
	Noise    int
}

// Generate writes the built-in golden dataset to dir.
// Creates the directory if it doesn't exist.
func Generate(dir string) (*GenerateResult, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create dir %s: %w", dir, err)
	}

	positive, noise := 0, 0
	for _, ex := range builtinExamples {
		txtPath := filepath.Join(dir, ex.stem+".txt")
		jsonPath := filepath.Join(dir, ex.stem+".json")

		if err := os.WriteFile(txtPath, []byte(ex.text+"\n"), 0644); err != nil {
			return nil, fmt.Errorf("write %s: %w", txtPath, err)
		}

		data, err := json.MarshalIndent(ex.expected, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("marshal %s: %w", jsonPath, err)
		}
		if err := os.WriteFile(jsonPath, append(data, '\n'), 0644); err != nil {
			return nil, fmt.Errorf("write %s: %w", jsonPath, err)
		}

		if ex.expected.IsNoise() {
			noise++
		} else {
			positive++
		}
	}

	return &GenerateResult{
		Dir:      dir,
		Total:    len(builtinExamples),
		Positive: positive,
		Noise:    noise,
	}, nil
}

type builtinExample struct {
	stem     string
	text     string
	expected GoldenExpected
}

var builtinExamples = []builtinExample{
	// --- Technical decisions ---
	{
		stem: "001-decision-language",
		text: "Alice: I've decided we're going with Go for Acme. Single binary, no runtime deps.\nAssistant: Makes sense. Should I use the standard cmd/internal layout?\nAlice: Yes, standard Go layout. And use TOML for config -- it supports comments and nested structures.",
		expected: GoldenExpected{
			Facts: []GoldenFact{
				{FactType: "decision", Subject: "Acme", Content: "Acme will be written in Go for single-binary deployment with no runtime dependencies.", Confidence: 1.0},
				{FactType: "decision", Subject: "Acme", Content: "Acme uses standard Go project layout with cmd/ and internal/ directories.", Confidence: 0.95},
				{FactType: "decision", Subject: "Acme", Content: "Acme uses TOML for configuration because it supports comments and nested structures.", Confidence: 1.0},
			},
			Entities: []GoldenEntity{
				{Name: "Alice", EntityType: "person"},
				{Name: "Acme", EntityType: "project"},
			},
			Relationships: []GoldenRelationship{
				{FromEntity: "Alice", ToEntity: "Acme", RelationType: "works_on"},
			},
		},
	},
	{
		stem: "002-decision-database",
		text: "User: We're going with PostgreSQL for the main database. SQLite for local dev and testing.\nAssistant: PostgreSQL for prod, SQLite for dev. Should I set up the schema?",
		expected: GoldenExpected{
			Facts: []GoldenFact{
				{FactType: "decision", Subject: "database", Content: "PostgreSQL is the main production database.", Confidence: 0.95},
				{FactType: "decision", Subject: "database", Content: "SQLite is used for local development and testing.", Confidence: 0.9},
			},
			Entities: []GoldenEntity{
				{Name: "PostgreSQL", EntityType: "tool", Aliases: []string{"Postgres"}},
				{Name: "SQLite", EntityType: "tool"},
			},
		},
	},
	{
		stem: "003-decision-architecture",
		text: "User: The API gateway will be written in Rust. Backend services stay in Python. Frontend is React with TypeScript.\nAssistant: Three-language stack. Any concerns about maintenance?",
		expected: GoldenExpected{
			Facts: []GoldenFact{
				{FactType: "decision", Subject: "API gateway", Content: "The API gateway will be written in Rust.", Confidence: 0.95},
				{FactType: "decision", Subject: "backend", Content: "Backend services are written in Python.", Confidence: 0.9},
				{FactType: "decision", Subject: "frontend", Content: "Frontend uses React with TypeScript.", Confidence: 0.9},
			},
		},
	},
	// --- Preferences ---
	{
		stem: "004-pref-communication",
		text: "Alice: Can you send the summary to my Telegram? I prefer getting updates there rather than email.\nAssistant: Sure. By the way, Bob from the API team asked about the deployment timeline.\nAlice: Tell him we're targeting end of March. The goal is to have the MVP running on mars by then.",
		expected: GoldenExpected{
			Facts: []GoldenFact{
				{FactType: "preference", Subject: "Alice", Content: "Alice prefers receiving updates via Telegram rather than email.", Confidence: 1.0},
				{FactType: "contact", Subject: "Bob", Content: "Bob is on the API team.", Confidence: 0.9},
				{FactType: "goal", Subject: "Acme", Content: "Goal is to have the Acme MVP running on mars by end of March.", Confidence: 0.9},
			},
			Entities: []GoldenEntity{
				{Name: "Alice", EntityType: "person"},
				{Name: "Bob", EntityType: "person"},
				{Name: "Acme", EntityType: "project"},
				{Name: "mars", EntityType: "server"},
			},
			Relationships: []GoldenRelationship{
				{FromEntity: "Bob", ToEntity: "Acme", RelationType: "works_on"},
				{FromEntity: "Acme", ToEntity: "mars", RelationType: "located_at"},
			},
		},
	},
	{
		stem: "005-pref-darkmode",
		text: "User: I always want dark mode in every editor. Light themes give me headaches.\nAssistant: Noted. I'll default to dark themes in all suggestions.",
		expected: GoldenExpected{
			Facts: []GoldenFact{
				{FactType: "preference", Subject: "User", Content: "User prefers dark mode in all editors because light themes cause headaches.", Confidence: 0.95},
			},
			Entities: []GoldenEntity{
				{Name: "User", EntityType: "person"},
			},
		},
	},
	// --- Rules ---
	{
		stem: "006-rule-deployment",
		text: "User: Never deploy on Fridays. No exceptions. If CI is red, no merge to main either.\nAssistant: Understood. Friday deploy freeze and green CI required for merges.",
		expected: GoldenExpected{
			Facts: []GoldenFact{
				{FactType: "rule", Subject: "deployment", Content: "Deployments are never done on Fridays, no exceptions.", Confidence: 1.0},
				{FactType: "rule", Subject: "CI", Content: "Merges to main require passing CI (green build).", Confidence: 0.95},
			},
		},
	},
	// --- Infrastructure / events ---
	{
		stem: "007-event-outage",
		text: "Assistant: mars is down again. It ran out of memory running local-llm at 27B params.\nAlice: That's the third time this week. Lesson learned -- we need to cap concurrent model loads to 2 max on mars. Also, node-1 should take over inference when mars is overloaded. node-2 depends on node-1 for routing anyway.",
		expected: GoldenExpected{
			Facts: []GoldenFact{
				{FactType: "event", Subject: "mars", Content: "mars server went down due to memory exhaustion running local-llm at 27B parameters.", Confidence: 1.0},
				{FactType: "lesson", Subject: "mars", Content: "Concurrent model loads on mars must be capped at 2 to prevent memory exhaustion.", Confidence: 0.95},
				{FactType: "rule", Subject: "inference", Content: "node-1 should take over inference tasks when mars is overloaded.", Confidence: 0.85},
				{FactType: "project", Subject: "node-2", Content: "node-2 depends on node-1 for routing.", Confidence: 0.9},
			},
			Entities: []GoldenEntity{
				{Name: "mars", EntityType: "server"},
				{Name: "local-llm", EntityType: "tool"},
				{Name: "node-1", EntityType: "server"},
				{Name: "node-2", EntityType: "server"},
			},
			Relationships: []GoldenRelationship{
				{FromEntity: "mars", ToEntity: "local-llm", RelationType: "uses"},
				{FromEntity: "node-2", ToEntity: "node-1", RelationType: "depends_on"},
			},
		},
	},
	{
		stem: "008-event-production",
		text: "User: Production went down at 3am last night. Root cause was a memory leak in the payment service. It took 4 hours to restore.\nAssistant: 3am outage, payment service memory leak, 4-hour recovery.",
		expected: GoldenExpected{
			Facts: []GoldenFact{
				{FactType: "event", Subject: "production", Content: "Production experienced an outage at 3am caused by a memory leak in the payment service.", Confidence: 0.95},
				{FactType: "event", Subject: "production", Content: "The production outage took 4 hours to restore.", Confidence: 0.9},
			},
			Entities: []GoldenEntity{
				{Name: "payment service", EntityType: "tool"},
			},
		},
	},
	// --- Project info ---
	{
		stem: "009-project-info",
		text: "User: Phoenix is our internal dashboard. It runs on three nodes in AWS us-east-1. The repo is github.com/acme/phoenix.\nAssistant: Got it. Phoenix dashboard, 3 nodes, us-east-1.",
		expected: GoldenExpected{
			Facts: []GoldenFact{
				{FactType: "project", Subject: "Phoenix", Content: "Phoenix is an internal dashboard application.", Confidence: 0.95},
				{FactType: "project", Subject: "Phoenix", Content: "Phoenix runs on three nodes in AWS us-east-1.", Confidence: 0.9},
			},
			Entities: []GoldenEntity{
				{Name: "Phoenix", EntityType: "project"},
				{Name: "us-east-1", EntityType: "location"},
			},
			Relationships: []GoldenRelationship{
				{FromEntity: "Phoenix", ToEntity: "us-east-1", RelationType: "located_at"},
			},
		},
	},
	// --- Contact / team ---
	{
		stem: "010-contact-team",
		text: "User: Sarah leads the platform team. Marcus is the DevOps lead. They both report to VP of Engineering, David Chen.\nAssistant: Org structure noted.",
		expected: GoldenExpected{
			Facts: []GoldenFact{
				{FactType: "contact", Subject: "Sarah", Content: "Sarah leads the platform team.", Confidence: 0.95},
				{FactType: "contact", Subject: "Marcus", Content: "Marcus is the DevOps lead.", Confidence: 0.95},
				{FactType: "contact", Subject: "David Chen", Content: "David Chen is the VP of Engineering.", Confidence: 0.9},
			},
			Entities: []GoldenEntity{
				{Name: "Sarah", EntityType: "person"},
				{Name: "Marcus", EntityType: "person"},
				{Name: "David Chen", EntityType: "person"},
			},
			Relationships: []GoldenRelationship{
				{FromEntity: "David Chen", ToEntity: "Sarah", RelationType: "manages"},
				{FromEntity: "David Chen", ToEntity: "Marcus", RelationType: "manages"},
			},
		},
	},
	// --- Bio ---
	{
		stem: "011-bio-background",
		text: "User: I studied computer science at MIT, graduated 2018. Before this job I was at Google for 3 years working on Cloud Spanner.\nAssistant: MIT CS 2018, then Google Cloud Spanner. Impressive background.",
		expected: GoldenExpected{
			Facts: []GoldenFact{
				{FactType: "bio", Subject: "User", Content: "User studied computer science at MIT, graduating in 2018.", Confidence: 0.95},
				{FactType: "bio", Subject: "User", Content: "User worked at Google for 3 years on Cloud Spanner before current job.", Confidence: 0.9},
			},
			Entities: []GoldenEntity{
				{Name: "User", EntityType: "person"},
				{Name: "MIT", EntityType: "organization"},
				{Name: "Google", EntityType: "organization"},
				{Name: "Cloud Spanner", EntityType: "tool", Aliases: []string{"Spanner"}},
			},
		},
	},
	// --- Context (situational) ---
	{
		stem: "012-context-situation",
		text: "User: The staging server is down right now. We're waiting for the AWS team to fix the EBS volume. Should be back in 2 hours.\nAssistant: Staging down, EBS issue, ETA 2 hours.",
		expected: GoldenExpected{
			Facts: []GoldenFact{
				{FactType: "context", Subject: "staging server", Content: "The staging server is currently down due to an EBS volume issue.", Confidence: 0.95},
				{FactType: "context", Subject: "staging server", Content: "The staging server is expected to be back in approximately 2 hours.", Confidence: 0.7},
			},
			Entities: []GoldenEntity{
				{Name: "staging server", EntityType: "server"},
			},
		},
	},
	// --- Lesson ---
	{
		stem: "013-lesson-nosql",
		text: "User: Last time we used a NoSQL database for transactional data it was a disaster. Lesson learned: use relational databases for anything with ACID requirements.\nAssistant: Noted. RDBMS for ACID workloads.",
		expected: GoldenExpected{
			Facts: []GoldenFact{
				{FactType: "lesson", Subject: "database", Content: "Relational databases should be used for any workload requiring ACID transactions; NoSQL was a disaster for transactional data.", Confidence: 0.95},
			},
		},
	},
	// --- Workflow ---
	{
		stem: "014-workflow-deploy",
		text: "User: Deploy procedure is: 1) merge to main, 2) wait for CI green, 3) run db:migrate on staging, 4) smoke test staging, 5) promote to production, 6) monitor for 30 minutes.\nAssistant: Six-step deploy pipeline. Got it.",
		expected: GoldenExpected{
			Facts: []GoldenFact{
				{FactType: "workflow", Subject: "deployment", Content: "Deploy procedure: merge to main, wait for CI green, run db:migrate on staging, smoke test staging, promote to production, monitor for 30 minutes.", Confidence: 0.95},
			},
		},
	},
	// --- Goal ---
	{
		stem: "015-goal-timeline",
		text: "User: We need to ship the v2 API by end of Q2. The mobile app launch depends on it. If we miss Q2, the whole product launch slips to September.\nAssistant: Hard deadline: v2 API by end of Q2. Mobile app blocked on it.",
		expected: GoldenExpected{
			Facts: []GoldenFact{
				{FactType: "goal", Subject: "v2 API", Content: "The v2 API must ship by end of Q2.", Confidence: 0.95},
				{FactType: "project", Subject: "mobile app", Content: "The mobile app launch depends on the v2 API being complete.", Confidence: 0.9},
			},
			Entities: []GoldenEntity{
				{Name: "v2 API", EntityType: "project"},
				{Name: "mobile app", EntityType: "project"},
			},
			Relationships: []GoldenRelationship{
				{FromEntity: "mobile app", ToEntity: "v2 API", RelationType: "depends_on"},
			},
		},
	},
	// --- Skill ---
	{
		stem: "016-skill-expertise",
		text: "User: I'm fluent in Go, Rust, and Python. I also know some Haskell but wouldn't call myself proficient. My team lead, Ana, is the Kubernetes expert.\nAssistant: Your stack: Go, Rust, Python (fluent), Haskell (basic). Ana is the K8s expert.",
		expected: GoldenExpected{
			Facts: []GoldenFact{
				{FactType: "skill", Subject: "User", Content: "User is fluent in Go, Rust, and Python.", Confidence: 0.95},
				{FactType: "skill", Subject: "User", Content: "User has basic Haskell knowledge but is not proficient.", Confidence: 0.7},
				{FactType: "skill", Subject: "Ana", Content: "Ana is the Kubernetes expert on the team.", Confidence: 0.9},
			},
			Entities: []GoldenEntity{
				{Name: "User", EntityType: "person"},
				{Name: "Ana", EntityType: "person"},
			},
		},
	},
	// --- Entity types: agent, document, concept ---
	{
		stem: "017-entity-agents",
		text: "User: We have three AI agents running: Clawd handles customer support, Archie does code review, and Scout monitors infrastructure.\nAssistant: Three agents: Clawd (support), Archie (code review), Scout (infra monitoring).",
		expected: GoldenExpected{
			Facts: []GoldenFact{
				{FactType: "project", Subject: "Clawd", Content: "Clawd is an AI agent that handles customer support.", Confidence: 0.95},
				{FactType: "project", Subject: "Archie", Content: "Archie is an AI agent that performs code review.", Confidence: 0.95},
				{FactType: "project", Subject: "Scout", Content: "Scout is an AI agent that monitors infrastructure.", Confidence: 0.95},
			},
			Entities: []GoldenEntity{
				{Name: "Clawd", EntityType: "agent"},
				{Name: "Archie", EntityType: "agent"},
				{Name: "Scout", EntityType: "agent"},
			},
		},
	},
	{
		stem: "018-entity-concepts",
		text: "User: We use event sourcing for the order system. CQRS separates reads from writes. The saga pattern handles distributed transactions across services.\nAssistant: Event sourcing + CQRS + saga pattern for the order system.",
		expected: GoldenExpected{
			Facts: []GoldenFact{
				{FactType: "decision", Subject: "order system", Content: "The order system uses event sourcing.", Confidence: 0.9},
				{FactType: "decision", Subject: "order system", Content: "CQRS is used to separate reads from writes in the order system.", Confidence: 0.9},
				{FactType: "decision", Subject: "order system", Content: "The saga pattern handles distributed transactions across services.", Confidence: 0.9},
			},
			Entities: []GoldenEntity{
				{Name: "event sourcing", EntityType: "concept"},
				{Name: "CQRS", EntityType: "concept"},
				{Name: "saga pattern", EntityType: "concept"},
				{Name: "order system", EntityType: "project"},
			},
			Relationships: []GoldenRelationship{
				{FromEntity: "order system", ToEntity: "event sourcing", RelationType: "uses"},
				{FromEntity: "order system", ToEntity: "CQRS", RelationType: "uses"},
			},
		},
	},
	// --- Relationship types: owns, created_by, depends_on ---
	{
		stem: "019-rel-owns",
		text: "User: I own the payment service and the notification service. Jake owns the user service.\nAssistant: Ownership noted.",
		expected: GoldenExpected{
			Facts: []GoldenFact{
				{FactType: "contact", Subject: "User", Content: "User owns the payment service and the notification service.", Confidence: 0.95},
				{FactType: "contact", Subject: "Jake", Content: "Jake owns the user service.", Confidence: 0.9},
			},
			Entities: []GoldenEntity{
				{Name: "User", EntityType: "person"},
				{Name: "Jake", EntityType: "person"},
				{Name: "payment service", EntityType: "project"},
				{Name: "user service", EntityType: "project"},
			},
			Relationships: []GoldenRelationship{
				{FromEntity: "User", ToEntity: "payment service", RelationType: "owns"},
				{FromEntity: "Jake", ToEntity: "user service", RelationType: "owns"},
			},
		},
	},
	{
		stem: "020-rel-dependson",
		text: "User: The checkout service depends on the inventory API and the payment gateway. If either goes down, checkout fails.\nAssistant: Hard dependencies: checkout -> inventory API, checkout -> payment gateway.",
		expected: GoldenExpected{
			Facts: []GoldenFact{
				{FactType: "project", Subject: "checkout service", Content: "The checkout service depends on the inventory API and the payment gateway; if either goes down, checkout fails.", Confidence: 0.95},
			},
			Entities: []GoldenEntity{
				{Name: "checkout service", EntityType: "project", Aliases: []string{"checkout"}},
				{Name: "inventory API", EntityType: "project"},
				{Name: "payment gateway", EntityType: "project"},
			},
			Relationships: []GoldenRelationship{
				{FromEntity: "checkout service", ToEntity: "inventory API", RelationType: "depends_on"},
				{FromEntity: "checkout service", ToEntity: "payment gateway", RelationType: "depends_on"},
			},
		},
	},
	// --- Complex multi-fact ---
	{
		stem: "021-complex-multi",
		text: "User: Let me give you the full picture. The project is called Meridian. It's a real-time analytics platform. Written in Go with a React frontend. Deployed on GCP in europe-west1. The team is me, Jake (backend), and Lisa (frontend). We use ClickHouse for analytics and Redis for caching. The goal is to process 1M events per second by Q3.\nAssistant: Comprehensive overview received.",
		expected: GoldenExpected{
			Facts: []GoldenFact{
				{FactType: "project", Subject: "Meridian", Content: "Meridian is a real-time analytics platform.", Confidence: 0.95},
				{FactType: "decision", Subject: "Meridian", Content: "Meridian is written in Go with a React frontend.", Confidence: 0.95},
				{FactType: "project", Subject: "Meridian", Content: "Meridian is deployed on GCP in europe-west1.", Confidence: 0.9},
				{FactType: "decision", Subject: "Meridian", Content: "Meridian uses ClickHouse for analytics and Redis for caching.", Confidence: 0.9},
				{FactType: "goal", Subject: "Meridian", Content: "The goal is to process 1 million events per second by Q3.", Confidence: 0.9},
			},
			Entities: []GoldenEntity{
				{Name: "Meridian", EntityType: "project"},
				{Name: "Jake", EntityType: "person"},
				{Name: "Lisa", EntityType: "person"},
				{Name: "europe-west1", EntityType: "location"},
				{Name: "ClickHouse", EntityType: "tool"},
				{Name: "Redis", EntityType: "tool"},
			},
			Relationships: []GoldenRelationship{
				{FromEntity: "Jake", ToEntity: "Meridian", RelationType: "works_on"},
				{FromEntity: "Lisa", ToEntity: "Meridian", RelationType: "works_on"},
				{FromEntity: "Meridian", ToEntity: "ClickHouse", RelationType: "uses"},
				{FromEntity: "Meridian", ToEntity: "Redis", RelationType: "uses"},
			},
		},
	},
	// --- Edge cases ---
	{
		stem: "022-edge-weak-signal",
		text: "Alice: I think Bob mentioned something about switching to Kubernetes, but I'm not sure if that was decided or just an idea he had.\nAssistant: Noted as unconfirmed.",
		expected: GoldenExpected{
			Facts: []GoldenFact{
				{FactType: "project", Subject: "Kubernetes", Content: "Bob may be considering a switch to Kubernetes, but no decision has been made.", Confidence: 0.4},
			},
			Entities: []GoldenEntity{
				{Name: "Bob", EntityType: "person"},
			},
		},
	},
	{
		stem: "023-edge-contradiction",
		text: "User: The API runs on port 8080. Wait, actually I changed it last week. It's on port 9090 now.\nAssistant: Port 9090, updated from 8080.",
		expected: GoldenExpected{
			Facts: []GoldenFact{
				{FactType: "project", Subject: "API", Content: "The API runs on port 9090, changed from the previous port 8080.", Confidence: 0.85},
			},
		},
	},
	{
		stem: "024-edge-temporal",
		text: "User: Starting next Monday, all PRs need two approvals instead of one. This is temporary until the security audit is complete, probably end of April.\nAssistant: Two-approval requirement starting Monday through end of April.",
		expected: GoldenExpected{
			Facts: []GoldenFact{
				{FactType: "rule", Subject: "PR review", Content: "All pull requests require two approvals instead of one, effective next Monday until the security audit completes (approximately end of April).", Confidence: 0.9},
			},
		},
	},
	{
		stem: "025-edge-implicit",
		text: "User: Can you check why the Berlin office can't reach the API? They've been complaining since Monday.\nAssistant: I'll look into the connectivity issue from Berlin.",
		expected: GoldenExpected{
			Facts: []GoldenFact{
				{FactType: "event", Subject: "Berlin office", Content: "The Berlin office has been unable to reach the API since Monday.", Confidence: 0.85},
			},
			Entities: []GoldenEntity{
				{Name: "Berlin office", EntityType: "location", Aliases: []string{"Berlin"}},
			},
		},
	},
	// --- Noise examples ---
	{
		stem: "030-noise-filler",
		text: "Alice: ok sounds good, let me check\nAssistant: Sure, take your time.\nAlice: alright, back. Where were we?",
		expected: GoldenExpected{},
	},
	{
		stem: "031-noise-operational",
		text: "Assistant: I cannot read /home/ubuntu/clawd/HEARTBEAT.md because I do not have access to the workspace files from this execution environment. Since I cannot determine whether anything needs attention, I'll respond with the default.",
		expected: GoldenExpected{},
	},
	{
		stem: "032-noise-errors",
		text: "Error: connection refused to localhost:5432\nRetrying in 5 seconds...\nError: connection refused to localhost:5432\nFailed after 3 attempts. Exiting.",
		expected: GoldenExpected{},
	},
	{
		stem: "033-noise-cron",
		text: "CRON[12345]: (root) CMD (/usr/local/bin/backup.sh)\nbackup.sh: Starting backup at 2026-03-18 04:00:00\nbackup.sh: Dumping database...\nbackup.sh: Compressing...\nbackup.sh: Upload to S3 complete.\nbackup.sh: Backup finished in 142 seconds.",
		expected: GoldenExpected{},
	},
	{
		stem: "034-noise-stacktrace",
		text: "panic: runtime error: index out of range [3] with length 3\n\ngoroutine 1 [running]:\nmain.processItems(0xc0000b4000, 0x3, 0x4)\n\t/app/main.go:42 +0x1a5\nmain.main()\n\t/app/main.go:15 +0x85\nexit status 2",
		expected: GoldenExpected{},
	},
	{
		stem: "035-noise-cilog",
		text: "Run actions/checkout@v4\nSyncing repository: acme/phoenix\nRun actions/setup-go@v5\nGo version: 1.22.1\nRun go test ./...\nok   acme/phoenix/internal/api  1.234s\nok   acme/phoenix/internal/db   2.567s\nPASS",
		expected: GoldenExpected{},
	},
	{
		stem: "036-noise-heartbeat",
		text: "Assistant: Performing scheduled health check.\nChecking API endpoint: 200 OK (45ms)\nChecking database connection: OK (12ms)\nChecking Redis: OK (3ms)\nAll systems operational. Next check in 5 minutes.",
		expected: GoldenExpected{},
	},
	{
		stem: "037-noise-meta",
		text: "User: wait, go back\nAssistant: Sure, going back to the previous topic.\nUser: no, the one before that\nAssistant: The database migration discussion?\nUser: yes that one. actually hold on, let me check something first\nAssistant: Take your time.",
		expected: GoldenExpected{},
	},
	{
		stem: "038-noise-kubectl",
		text: "$ kubectl get pods -n production\nNAME                          READY   STATUS    RESTARTS   AGE\napi-7d8f9c6b5-x2k4l          1/1     Running   0          3d\nworker-5c4d3e2f1-m8n7p        1/1     Running   2          5d\nredis-master-0                1/1     Running   0          12d",
		expected: GoldenExpected{},
	},
	{
		stem: "039-noise-minimal",
		text: "User: ok\nAssistant: Understood.",
		expected: GoldenExpected{},
	},
	{
		stem: "040-noise-gitdiff",
		text: "diff --git a/main.go b/main.go\nindex 1234567..abcdefg 100644\n--- a/main.go\n+++ b/main.go\n@@ -10,7 +10,7 @@\n func main() {\n-    fmt.Println(\"hello\")\n+    fmt.Println(\"world\")\n }",
		expected: GoldenExpected{},
	},
	{
		stem: "041-noise-retry",
		text: "Attempt 1/5: POST https://api.example.com/v1/extract - 503 Service Unavailable\nRetrying in 2s...\nAttempt 2/5: POST https://api.example.com/v1/extract - 503 Service Unavailable\nAll retries exhausted. Giving up.",
		expected: GoldenExpected{},
	},
	// --- Infrastructure ---
	{
		stem: "042-infra-servers",
		text: "User: Our infrastructure: prod-1 and prod-2 are the application servers behind a load balancer. db-primary is the PostgreSQL master, db-replica-1 and db-replica-2 are read replicas. cache-1 runs Redis. All in Hetzner Falkenstein datacenter.\nAssistant: 6 servers total in Hetzner Falkenstein.",
		expected: GoldenExpected{
			Facts: []GoldenFact{
				{FactType: "project", Subject: "prod-1", Content: "prod-1 is an application server behind a load balancer.", Confidence: 0.9},
				{FactType: "project", Subject: "db-primary", Content: "db-primary is the PostgreSQL master database server.", Confidence: 0.9},
				{FactType: "project", Subject: "infrastructure", Content: "All servers are hosted in the Hetzner Falkenstein datacenter.", Confidence: 0.9},
			},
			Entities: []GoldenEntity{
				{Name: "prod-1", EntityType: "server"},
				{Name: "prod-2", EntityType: "server"},
				{Name: "db-primary", EntityType: "server"},
				{Name: "db-replica-1", EntityType: "server"},
				{Name: "db-replica-2", EntityType: "server"},
				{Name: "cache-1", EntityType: "server"},
				{Name: "Hetzner", EntityType: "organization"},
				{Name: "Falkenstein", EntityType: "location"},
			},
			Relationships: []GoldenRelationship{
				{FromEntity: "db-replica-1", ToEntity: "db-primary", RelationType: "depends_on"},
				{FromEntity: "db-replica-2", ToEntity: "db-primary", RelationType: "depends_on"},
			},
		},
	},
}
