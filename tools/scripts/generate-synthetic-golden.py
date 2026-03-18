#!/usr/bin/env python3
"""Generate synthetic golden eval examples for Imprint.

Covers all 12 fact types, 9 entity types, 9 relationship types,
and various noise patterns. All examples are fictional AI-agent
conversations -- no real data.

Usage:
    python3 tools/scripts/generate-synthetic-golden.py testdata/golden/
"""

import json
import os
import sys

EXAMPLES = [
    # --- Fact type coverage: preference ---
    {
        "stem": "010-pref-dark-mode",
        "text": "User: I always want dark mode in every editor. Light themes give me headaches.\nAssistant: Noted. I'll default to dark themes in all suggestions.",
        "expected": {
            "facts": [{"fact_type": "preference", "subject": "User", "content": "User prefers dark mode in all editors because light themes cause headaches.", "confidence": 0.95}],
            "entities": [{"name": "User", "entity_type": "person", "aliases": []}],
            "relationships": [],
        },
        "category": "personal",
    },
    {
        "stem": "011-pref-communication",
        "text": "User: Don't send me emails. Use Slack for everything. If it's urgent, text me.\nAssistant: Got it -- Slack by default, text for urgent.",
        "expected": {
            "facts": [
                {"fact_type": "preference", "subject": "User", "content": "User prefers Slack over email for all communications.", "confidence": 0.95},
                {"fact_type": "preference", "subject": "User", "content": "User wants text messages for urgent matters.", "confidence": 0.9},
            ],
            "entities": [{"name": "User", "entity_type": "person", "aliases": []}],
            "relationships": [],
        },
        "category": "personal",
    },
    # --- Fact type coverage: decision ---
    {
        "stem": "012-decision-database",
        "text": "User: We're going with PostgreSQL for the main database. SQLite for local dev and testing.\nAssistant: PostgreSQL for prod, SQLite for dev. Should I set up the schema?",
        "expected": {
            "facts": [
                {"fact_type": "decision", "subject": "database", "content": "PostgreSQL is the main production database.", "confidence": 0.95},
                {"fact_type": "decision", "subject": "database", "content": "SQLite is used for local development and testing.", "confidence": 0.9},
            ],
            "entities": [{"name": "User", "entity_type": "person", "aliases": []}, {"name": "PostgreSQL", "entity_type": "tool", "aliases": ["Postgres"]}, {"name": "SQLite", "entity_type": "tool", "aliases": []}],
            "relationships": [],
        },
        "category": "technical",
    },
    {
        "stem": "013-decision-architecture",
        "text": "User: The API gateway will be written in Rust. Backend services stay in Python. Frontend is React with TypeScript.\nAssistant: Three-language stack. Any concerns about maintenance?",
        "expected": {
            "facts": [
                {"fact_type": "decision", "subject": "API gateway", "content": "The API gateway will be written in Rust.", "confidence": 0.95},
                {"fact_type": "decision", "subject": "backend", "content": "Backend services are written in Python.", "confidence": 0.9},
                {"fact_type": "decision", "subject": "frontend", "content": "Frontend uses React with TypeScript.", "confidence": 0.9},
            ],
            "entities": [{"name": "User", "entity_type": "person", "aliases": []}],
            "relationships": [],
        },
        "category": "technical",
    },
    # --- Fact type coverage: rule ---
    {
        "stem": "014-rule-deployment",
        "text": "User: Never deploy on Fridays. No exceptions. If CI is red, no merge to main either.\nAssistant: Understood. Friday deploy freeze and green CI required for merges.",
        "expected": {
            "facts": [
                {"fact_type": "rule", "subject": "deployment", "content": "Deployments are never done on Fridays, no exceptions.", "confidence": 1.0},
                {"fact_type": "rule", "subject": "CI", "content": "Merges to main require passing CI (green build).", "confidence": 0.95},
            ],
            "entities": [{"name": "User", "entity_type": "person", "aliases": []}],
            "relationships": [],
        },
        "category": "technical",
    },
    # --- Fact type coverage: project ---
    {
        "stem": "015-project-info",
        "text": "User: Phoenix is our internal dashboard. It runs on three nodes in AWS us-east-1. The repo is github.com/acme/phoenix.\nAssistant: Got it. Phoenix dashboard, 3 nodes, us-east-1.",
        "expected": {
            "facts": [
                {"fact_type": "project", "subject": "Phoenix", "content": "Phoenix is an internal dashboard application.", "confidence": 0.95},
                {"fact_type": "project", "subject": "Phoenix", "content": "Phoenix runs on three nodes in AWS us-east-1.", "confidence": 0.9},
            ],
            "entities": [
                {"name": "Phoenix", "entity_type": "project", "aliases": []},
                {"name": "AWS", "entity_type": "organization", "aliases": []},
                {"name": "us-east-1", "entity_type": "location", "aliases": []},
            ],
            "relationships": [{"from_entity": "Phoenix", "to_entity": "us-east-1", "relation_type": "located_at"}],
        },
        "category": "technical",
    },
    # --- Fact type coverage: contact ---
    {
        "stem": "016-contact-team",
        "text": "User: Sarah leads the platform team. Marcus is the DevOps lead. They both report to VP of Engineering, David Chen.\nAssistant: Org structure noted.",
        "expected": {
            "facts": [
                {"fact_type": "contact", "subject": "Sarah", "content": "Sarah leads the platform team.", "confidence": 0.95},
                {"fact_type": "contact", "subject": "Marcus", "content": "Marcus is the DevOps lead.", "confidence": 0.95},
                {"fact_type": "contact", "subject": "David Chen", "content": "David Chen is the VP of Engineering.", "confidence": 0.9},
            ],
            "entities": [
                {"name": "Sarah", "entity_type": "person", "aliases": []},
                {"name": "Marcus", "entity_type": "person", "aliases": []},
                {"name": "David Chen", "entity_type": "person", "aliases": []},
            ],
            "relationships": [
                {"from_entity": "David Chen", "to_entity": "Sarah", "relation_type": "manages"},
                {"from_entity": "David Chen", "to_entity": "Marcus", "relation_type": "manages"},
            ],
        },
        "category": "personal",
    },
    # --- Fact type coverage: bio ---
    {
        "stem": "017-bio-background",
        "text": "User: I studied computer science at MIT, graduated 2018. Before this job I was at Google for 3 years working on Cloud Spanner.\nAssistant: MIT CS 2018, then Google Cloud Spanner. Impressive background.",
        "expected": {
            "facts": [
                {"fact_type": "bio", "subject": "User", "content": "User studied computer science at MIT, graduating in 2018.", "confidence": 0.95},
                {"fact_type": "bio", "subject": "User", "content": "User worked at Google for 3 years on Cloud Spanner before current job.", "confidence": 0.9},
            ],
            "entities": [
                {"name": "User", "entity_type": "person", "aliases": []},
                {"name": "MIT", "entity_type": "organization", "aliases": []},
                {"name": "Google", "entity_type": "organization", "aliases": []},
                {"name": "Cloud Spanner", "entity_type": "tool", "aliases": ["Spanner"]},
            ],
            "relationships": [{"from_entity": "User", "to_entity": "Google", "relation_type": "works_on"}],
        },
        "category": "personal",
    },
    # --- Fact type coverage: context ---
    {
        "stem": "018-context-situation",
        "text": "User: The staging server is down right now. We're waiting for the AWS team to fix the EBS volume. Should be back in 2 hours.\nAssistant: Staging down, EBS issue, ETA 2 hours.",
        "expected": {
            "facts": [
                {"fact_type": "context", "subject": "staging server", "content": "The staging server is currently down due to an EBS volume issue.", "confidence": 0.95},
                {"fact_type": "context", "subject": "staging server", "content": "The staging server is expected to be back in approximately 2 hours.", "confidence": 0.7},
            ],
            "entities": [
                {"name": "staging server", "entity_type": "server", "aliases": []},
                {"name": "AWS", "entity_type": "organization", "aliases": []},
            ],
            "relationships": [],
        },
        "category": "technical",
    },
    # --- Fact type coverage: lesson ---
    {
        "stem": "019-lesson-learned",
        "text": "User: Last time we used a NoSQL database for transactional data it was a disaster. Lesson learned: use relational databases for anything with ACID requirements.\nAssistant: Noted. RDBMS for ACID workloads.",
        "expected": {
            "facts": [{"fact_type": "lesson", "subject": "database", "content": "Relational databases should be used for any workload requiring ACID transactions; NoSQL was a disaster for transactional data.", "confidence": 0.95}],
            "entities": [{"name": "User", "entity_type": "person", "aliases": []}],
            "relationships": [],
        },
        "category": "technical",
    },
    # --- Fact type coverage: workflow ---
    {
        "stem": "020-workflow-deploy",
        "text": "User: Deploy procedure is: 1) merge to main, 2) wait for CI green, 3) run db:migrate on staging, 4) smoke test staging, 5) promote to production, 6) monitor for 30 minutes.\nAssistant: Six-step deploy pipeline. Got it.",
        "expected": {
            "facts": [{"fact_type": "workflow", "subject": "deployment", "content": "Deploy procedure: merge to main, wait for CI green, run db:migrate on staging, smoke test staging, promote to production, monitor for 30 minutes.", "confidence": 0.95}],
            "entities": [{"name": "User", "entity_type": "person", "aliases": []}],
            "relationships": [],
        },
        "category": "technical",
    },
    # --- Fact type coverage: goal ---
    {
        "stem": "021-goal-timeline",
        "text": "User: We need to ship the v2 API by end of Q2. The mobile app launch depends on it. If we miss Q2, the whole product launch slips to September.\nAssistant: Hard deadline: v2 API by end of Q2. Mobile app blocked on it.",
        "expected": {
            "facts": [
                {"fact_type": "goal", "subject": "v2 API", "content": "The v2 API must ship by end of Q2.", "confidence": 0.95},
                {"fact_type": "project", "subject": "mobile app", "content": "The mobile app launch depends on the v2 API being complete.", "confidence": 0.9},
            ],
            "entities": [
                {"name": "v2 API", "entity_type": "project", "aliases": []},
                {"name": "mobile app", "entity_type": "project", "aliases": []},
            ],
            "relationships": [{"from_entity": "mobile app", "to_entity": "v2 API", "relation_type": "depends_on"}],
        },
        "category": "technical",
    },
    # --- Fact type coverage: event ---
    {
        "stem": "022-event-outage",
        "text": "User: Production went down at 3am last night. Root cause was a memory leak in the payment service. It took 4 hours to restore.\nAssistant: 3am outage, payment service memory leak, 4-hour recovery.",
        "expected": {
            "facts": [
                {"fact_type": "event", "subject": "production", "content": "Production experienced an outage at 3am caused by a memory leak in the payment service.", "confidence": 0.95},
                {"fact_type": "event", "subject": "production", "content": "The production outage took 4 hours to restore.", "confidence": 0.9},
            ],
            "entities": [
                {"name": "payment service", "entity_type": "tool", "aliases": []},
            ],
            "relationships": [],
        },
        "category": "technical",
    },
    # --- Fact type coverage: skill ---
    {
        "stem": "023-skill-expertise",
        "text": "User: I'm fluent in Go, Rust, and Python. I also know some Haskell but wouldn't call myself proficient. My team lead, Ana, is the Kubernetes expert.\nAssistant: Your stack: Go, Rust, Python (fluent), Haskell (basic). Ana is the K8s expert.",
        "expected": {
            "facts": [
                {"fact_type": "skill", "subject": "User", "content": "User is fluent in Go, Rust, and Python.", "confidence": 0.95},
                {"fact_type": "skill", "subject": "User", "content": "User has basic Haskell knowledge but is not proficient.", "confidence": 0.7},
                {"fact_type": "skill", "subject": "Ana", "content": "Ana is the Kubernetes expert on the team.", "confidence": 0.9},
            ],
            "entities": [
                {"name": "User", "entity_type": "person", "aliases": []},
                {"name": "Ana", "entity_type": "person", "aliases": []},
            ],
            "relationships": [{"from_entity": "Ana", "to_entity": "User", "relation_type": "manages"}],
        },
        "category": "personal",
    },
    # --- Entity type coverage: agent ---
    {
        "stem": "024-entity-agent",
        "text": "User: We have three AI agents running: Clawd handles customer support, Archie does code review, and Scout monitors infrastructure.\nAssistant: Three agents: Clawd (support), Archie (code review), Scout (infra monitoring).",
        "expected": {
            "facts": [
                {"fact_type": "project", "subject": "Clawd", "content": "Clawd is an AI agent that handles customer support.", "confidence": 0.95},
                {"fact_type": "project", "subject": "Archie", "content": "Archie is an AI agent that performs code review.", "confidence": 0.95},
                {"fact_type": "project", "subject": "Scout", "content": "Scout is an AI agent that monitors infrastructure.", "confidence": 0.95},
            ],
            "entities": [
                {"name": "Clawd", "entity_type": "agent", "aliases": []},
                {"name": "Archie", "entity_type": "agent", "aliases": []},
                {"name": "Scout", "entity_type": "agent", "aliases": []},
            ],
            "relationships": [],
        },
        "category": "technical",
    },
    # --- Entity type coverage: document ---
    {
        "stem": "025-entity-document",
        "text": "User: The architecture is documented in ARCHITECTURE.md. The API spec is in api-spec.yaml. Both live in the docs/ folder of the main repo.\nAssistant: Two key docs: ARCHITECTURE.md and api-spec.yaml in docs/.",
        "expected": {
            "facts": [
                {"fact_type": "project", "subject": "ARCHITECTURE.md", "content": "ARCHITECTURE.md contains the system architecture documentation.", "confidence": 0.9},
                {"fact_type": "project", "subject": "api-spec.yaml", "content": "api-spec.yaml contains the API specification.", "confidence": 0.9},
            ],
            "entities": [
                {"name": "ARCHITECTURE.md", "entity_type": "document", "aliases": []},
                {"name": "api-spec.yaml", "entity_type": "document", "aliases": []},
            ],
            "relationships": [],
        },
        "category": "technical",
    },
    # --- Entity type coverage: concept ---
    {
        "stem": "026-entity-concept",
        "text": "User: We use event sourcing for the order system. CQRS separates reads from writes. The saga pattern handles distributed transactions across services.\nAssistant: Event sourcing + CQRS + saga pattern for the order system.",
        "expected": {
            "facts": [
                {"fact_type": "decision", "subject": "order system", "content": "The order system uses event sourcing.", "confidence": 0.9},
                {"fact_type": "decision", "subject": "order system", "content": "CQRS is used to separate reads from writes in the order system.", "confidence": 0.9},
                {"fact_type": "decision", "subject": "order system", "content": "The saga pattern handles distributed transactions across services.", "confidence": 0.9},
            ],
            "entities": [
                {"name": "event sourcing", "entity_type": "concept", "aliases": []},
                {"name": "CQRS", "entity_type": "concept", "aliases": []},
                {"name": "saga pattern", "entity_type": "concept", "aliases": []},
                {"name": "order system", "entity_type": "project", "aliases": []},
            ],
            "relationships": [
                {"from_entity": "order system", "to_entity": "event sourcing", "relation_type": "uses"},
                {"from_entity": "order system", "to_entity": "CQRS", "relation_type": "uses"},
            ],
        },
        "category": "technical",
    },
    # --- Multi-fact complex example ---
    {
        "stem": "027-complex-multi",
        "text": "User: Let me give you the full picture. The project is called Meridian. It's a real-time analytics platform. Written in Go with a React frontend. Deployed on GCP in europe-west1. The team is me, Jake (backend), and Lisa (frontend). Jake owns the ingestion pipeline. Lisa built the dashboard. We use ClickHouse for analytics and Redis for caching. The goal is to process 1M events per second by Q3.\nAssistant: Comprehensive overview received. Meridian: Go+React analytics platform on GCP, team of 3, ClickHouse+Redis, 1M eps target by Q3.",
        "expected": {
            "facts": [
                {"fact_type": "project", "subject": "Meridian", "content": "Meridian is a real-time analytics platform.", "confidence": 0.95},
                {"fact_type": "decision", "subject": "Meridian", "content": "Meridian is written in Go with a React frontend.", "confidence": 0.95},
                {"fact_type": "project", "subject": "Meridian", "content": "Meridian is deployed on GCP in europe-west1.", "confidence": 0.9},
                {"fact_type": "contact", "subject": "Jake", "content": "Jake is a backend developer who owns the ingestion pipeline.", "confidence": 0.9},
                {"fact_type": "contact", "subject": "Lisa", "content": "Lisa is a frontend developer who built the dashboard.", "confidence": 0.9},
                {"fact_type": "decision", "subject": "Meridian", "content": "Meridian uses ClickHouse for analytics and Redis for caching.", "confidence": 0.9},
                {"fact_type": "goal", "subject": "Meridian", "content": "The goal is to process 1 million events per second by Q3.", "confidence": 0.9},
            ],
            "entities": [
                {"name": "Meridian", "entity_type": "project", "aliases": []},
                {"name": "User", "entity_type": "person", "aliases": []},
                {"name": "Jake", "entity_type": "person", "aliases": []},
                {"name": "Lisa", "entity_type": "person", "aliases": []},
                {"name": "GCP", "entity_type": "organization", "aliases": ["Google Cloud"]},
                {"name": "europe-west1", "entity_type": "location", "aliases": []},
                {"name": "ClickHouse", "entity_type": "tool", "aliases": []},
                {"name": "Redis", "entity_type": "tool", "aliases": []},
            ],
            "relationships": [
                {"from_entity": "Jake", "to_entity": "Meridian", "relation_type": "works_on"},
                {"from_entity": "Lisa", "to_entity": "Meridian", "relation_type": "works_on"},
                {"from_entity": "Meridian", "to_entity": "europe-west1", "relation_type": "located_at"},
                {"from_entity": "Meridian", "to_entity": "ClickHouse", "relation_type": "uses"},
                {"from_entity": "Meridian", "to_entity": "Redis", "relation_type": "uses"},
            ],
        },
        "category": "technical",
    },
    # --- Noise: cron output ---
    {
        "stem": "030-noise-cron",
        "text": "CRON[12345]: (root) CMD (/usr/local/bin/backup.sh)\nbackup.sh: Starting backup at 2026-03-18 04:00:00\nbackup.sh: Dumping database...\nbackup.sh: Compressing...\nbackup.sh: Upload to S3 complete.\nbackup.sh: Backup finished in 142 seconds.",
        "expected": {"facts": [], "entities": [], "relationships": []},
        "category": "noise",
    },
    # --- Noise: stack trace ---
    {
        "stem": "031-noise-stacktrace",
        "text": "panic: runtime error: index out of range [3] with length 3\n\ngoroutine 1 [running]:\nmain.processItems(0xc0000b4000, 0x3, 0x4)\n\t/app/main.go:42 +0x1a5\nmain.main()\n\t/app/main.go:15 +0x85\nexit status 2",
        "expected": {"facts": [], "entities": [], "relationships": []},
        "category": "noise",
    },
    # --- Noise: git diff ---
    {
        "stem": "032-noise-gitdiff",
        "text": "diff --git a/main.go b/main.go\nindex 1234567..abcdefg 100644\n--- a/main.go\n+++ b/main.go\n@@ -10,7 +10,7 @@\n func main() {\n-    fmt.Println(\"hello\")\n+    fmt.Println(\"world\")\n }",
        "expected": {"facts": [], "entities": [], "relationships": []},
        "category": "noise",
    },
    # --- Noise: CI log ---
    {
        "stem": "033-noise-cilog",
        "text": "Run actions/checkout@v4\nSyncing repository: acme/phoenix\nChecking out revision abc123\nRun actions/setup-go@v5\nGo version: 1.22.1\nRun go test ./...\nok   acme/phoenix/internal/api  1.234s\nok   acme/phoenix/internal/db   2.567s\nPASS",
        "expected": {"facts": [], "entities": [], "relationships": []},
        "category": "noise",
    },
    # --- Noise: heartbeat / health check ---
    {
        "stem": "034-noise-heartbeat",
        "text": "Assistant: Performing scheduled health check.\nChecking API endpoint: 200 OK (45ms)\nChecking database connection: OK (12ms)\nChecking Redis: OK (3ms)\nChecking disk space: 67% used\nAll systems operational. Next check in 5 minutes.",
        "expected": {"facts": [], "entities": [], "relationships": []},
        "category": "noise",
    },
    # --- Noise: assistant inability ---
    {
        "stem": "035-noise-inability",
        "text": "Assistant: I tried to read the file /var/log/app.log but I don't have permission to access it. I also attempted to connect to the database at localhost:5432 but the connection was refused. Without access to these resources, I cannot complete the diagnostic.",
        "expected": {"facts": [], "entities": [], "relationships": []},
        "category": "noise",
    },
    # --- Noise: pure meta-conversation ---
    {
        "stem": "036-noise-meta",
        "text": "User: wait, go back\nAssistant: Sure, going back to the previous topic.\nUser: no, the one before that\nAssistant: The database migration discussion?\nUser: yes that one. actually hold on, let me check something first\nAssistant: Take your time.",
        "expected": {"facts": [], "entities": [], "relationships": []},
        "category": "noise",
    },
    # --- Noise: tool output ---
    {
        "stem": "037-noise-tooloutput",
        "text": "$ kubectl get pods -n production\nNAME                          READY   STATUS    RESTARTS   AGE\napi-7d8f9c6b5-x2k4l          1/1     Running   0          3d\nworker-5c4d3e2f1-m8n7p        1/1     Running   2          5d\nredis-master-0                1/1     Running   0          12d",
        "expected": {"facts": [], "entities": [], "relationships": []},
        "category": "noise",
    },
    # --- Noise: empty/minimal ---
    {
        "stem": "038-noise-minimal",
        "text": "User: ok\nAssistant: Understood.",
        "expected": {"facts": [], "entities": [], "relationships": []},
        "category": "noise",
    },
    # --- Noise: repeated errors ---
    {
        "stem": "039-noise-retryfail",
        "text": "Attempt 1/5: POST https://api.example.com/v1/extract - 503 Service Unavailable\nRetrying in 2s...\nAttempt 2/5: POST https://api.example.com/v1/extract - 503 Service Unavailable\nRetrying in 4s...\nAttempt 3/5: POST https://api.example.com/v1/extract - 503 Service Unavailable\nAll retries exhausted. Giving up.",
        "expected": {"facts": [], "entities": [], "relationships": []},
        "category": "noise",
    },
    # --- Edge case: contradictory info ---
    {
        "stem": "040-edge-contradiction",
        "text": "User: The API runs on port 8080. Wait, actually I changed it last week. It's on port 9090 now.\nAssistant: Port 9090, updated from 8080.",
        "expected": {
            "facts": [{"fact_type": "project", "subject": "API", "content": "The API runs on port 9090, changed from the previous port 8080.", "confidence": 0.85}],
            "entities": [],
            "relationships": [],
        },
        "category": "edge_case",
    },
    # --- Edge case: implicit info ---
    {
        "stem": "041-edge-implicit",
        "text": "User: Can you check why the Berlin office can't reach the API? They've been complaining since Monday.\nAssistant: I'll look into the connectivity issue from Berlin.",
        "expected": {
            "facts": [
                {"fact_type": "event", "subject": "Berlin office", "content": "The Berlin office has been unable to reach the API since Monday.", "confidence": 0.85},
            ],
            "entities": [
                {"name": "Berlin office", "entity_type": "location", "aliases": ["Berlin"]},
            ],
            "relationships": [],
        },
        "category": "edge_case",
    },
    # --- Edge case: very low confidence ---
    {
        "stem": "042-edge-lowconf",
        "text": "User: I vaguely remember someone mentioning that we might switch from AWS to Azure next year, but I could be wrong. It might have been a joke.\nAssistant: Noted as unconfirmed. I won't act on it unless you confirm.",
        "expected": {
            "facts": [{"fact_type": "project", "subject": "cloud provider", "content": "There may be a potential switch from AWS to Azure next year, but this is unconfirmed and possibly not serious.", "confidence": 0.35}],
            "entities": [
                {"name": "AWS", "entity_type": "organization", "aliases": []},
                {"name": "Azure", "entity_type": "tool", "aliases": []},
            ],
            "relationships": [],
        },
        "category": "edge_case",
    },
    # --- Edge case: temporal validity ---
    {
        "stem": "043-edge-temporal",
        "text": "User: Starting next Monday, all PRs need two approvals instead of one. This is temporary until the security audit is complete, probably end of April.\nAssistant: Two-approval requirement starting Monday through end of April.",
        "expected": {
            "facts": [{"fact_type": "rule", "subject": "PR review", "content": "All pull requests require two approvals instead of one, effective next Monday until the security audit completes (approximately end of April).", "confidence": 0.9}],
            "entities": [{"name": "User", "entity_type": "person", "aliases": []}],
            "relationships": [],
        },
        "category": "edge_case",
    },
    # --- Relationship type coverage: owns ---
    {
        "stem": "044-rel-owns",
        "text": "User: I own the payment service and the notification service. Jake owns the user service.\nAssistant: Ownership noted.",
        "expected": {
            "facts": [
                {"fact_type": "contact", "subject": "User", "content": "User owns the payment service and the notification service.", "confidence": 0.95},
                {"fact_type": "contact", "subject": "Jake", "content": "Jake owns the user service.", "confidence": 0.9},
            ],
            "entities": [
                {"name": "User", "entity_type": "person", "aliases": []},
                {"name": "Jake", "entity_type": "person", "aliases": []},
                {"name": "payment service", "entity_type": "project", "aliases": []},
                {"name": "user service", "entity_type": "project", "aliases": []},
            ],
            "relationships": [
                {"from_entity": "User", "to_entity": "payment service", "relation_type": "owns"},
                {"from_entity": "Jake", "to_entity": "user service", "relation_type": "owns"},
            ],
        },
        "category": "technical",
    },
    # --- Relationship type coverage: created_by ---
    {
        "stem": "045-rel-createdby",
        "text": "User: The monitoring dashboard was built by Elena. The alerting rules were written by the SRE team.\nAssistant: Elena built the dashboard, SRE team wrote the alerts.",
        "expected": {
            "facts": [
                {"fact_type": "contact", "subject": "Elena", "content": "Elena built the monitoring dashboard.", "confidence": 0.9},
                {"fact_type": "project", "subject": "alerting rules", "content": "The alerting rules were written by the SRE team.", "confidence": 0.9},
            ],
            "entities": [
                {"name": "Elena", "entity_type": "person", "aliases": []},
                {"name": "monitoring dashboard", "entity_type": "project", "aliases": []},
                {"name": "SRE team", "entity_type": "organization", "aliases": []},
            ],
            "relationships": [
                {"from_entity": "monitoring dashboard", "to_entity": "Elena", "relation_type": "created_by"},
            ],
        },
        "category": "technical",
    },
    # --- Relationship type coverage: depends_on ---
    {
        "stem": "046-rel-dependson",
        "text": "User: The checkout service depends on the inventory API and the payment gateway. If either goes down, checkout fails.\nAssistant: Hard dependencies: checkout -> inventory API, checkout -> payment gateway.",
        "expected": {
            "facts": [
                {"fact_type": "project", "subject": "checkout service", "content": "The checkout service depends on the inventory API and the payment gateway; if either goes down, checkout fails.", "confidence": 0.95},
            ],
            "entities": [
                {"name": "checkout service", "entity_type": "project", "aliases": ["checkout"]},
                {"name": "inventory API", "entity_type": "project", "aliases": []},
                {"name": "payment gateway", "entity_type": "project", "aliases": []},
            ],
            "relationships": [
                {"from_entity": "checkout service", "to_entity": "inventory API", "relation_type": "depends_on"},
                {"from_entity": "checkout service", "to_entity": "payment gateway", "relation_type": "depends_on"},
            ],
        },
        "category": "technical",
    },
    # --- Mixed: some knowledge + some noise ---
    {
        "stem": "047-mixed-partial",
        "text": "User: hmm let me think... ok so the config file is at /etc/myapp/config.yaml\nAssistant: Got it.\nUser: wait no, that's the old path. It's /opt/myapp/config.yaml now.\nAssistant: Updated: /opt/myapp/config.yaml",
        "expected": {
            "facts": [{"fact_type": "project", "subject": "myapp", "content": "The myapp configuration file is located at /opt/myapp/config.yaml.", "confidence": 0.85}],
            "entities": [{"name": "myapp", "entity_type": "project", "aliases": []}],
            "relationships": [],
        },
        "category": "edge_case",
    },
    # --- Single fact, high confidence ---
    {
        "stem": "048-single-highconf",
        "text": "User: Our company name is Nexus Technologies.\nAssistant: Noted.",
        "expected": {
            "facts": [{"fact_type": "bio", "subject": "User", "content": "User works at Nexus Technologies.", "confidence": 0.95}],
            "entities": [
                {"name": "User", "entity_type": "person", "aliases": []},
                {"name": "Nexus Technologies", "entity_type": "organization", "aliases": ["Nexus"]},
            ],
            "relationships": [{"from_entity": "User", "to_entity": "Nexus Technologies", "relation_type": "part_of"}],
        },
        "category": "personal",
    },
    # --- Server infrastructure ---
    {
        "stem": "049-infra-servers",
        "text": "User: Our infrastructure: prod-1 and prod-2 are the application servers behind a load balancer. db-primary is the PostgreSQL master, db-replica-1 and db-replica-2 are read replicas. cache-1 runs Redis. All in Hetzner Falkenstein datacenter.\nAssistant: 6 servers total in Hetzner Falkenstein. 2 app, 1 db primary, 2 db replicas, 1 cache.",
        "expected": {
            "facts": [
                {"fact_type": "project", "subject": "prod-1", "content": "prod-1 is an application server behind a load balancer.", "confidence": 0.9},
                {"fact_type": "project", "subject": "db-primary", "content": "db-primary is the PostgreSQL master database server.", "confidence": 0.9},
                {"fact_type": "project", "subject": "infrastructure", "content": "All servers are hosted in the Hetzner Falkenstein datacenter.", "confidence": 0.9},
            ],
            "entities": [
                {"name": "prod-1", "entity_type": "server", "aliases": []},
                {"name": "prod-2", "entity_type": "server", "aliases": []},
                {"name": "db-primary", "entity_type": "server", "aliases": []},
                {"name": "db-replica-1", "entity_type": "server", "aliases": []},
                {"name": "db-replica-2", "entity_type": "server", "aliases": []},
                {"name": "cache-1", "entity_type": "server", "aliases": []},
                {"name": "Hetzner", "entity_type": "organization", "aliases": []},
                {"name": "Falkenstein", "entity_type": "location", "aliases": []},
            ],
            "relationships": [
                {"from_entity": "db-replica-1", "to_entity": "db-primary", "relation_type": "depends_on"},
                {"from_entity": "db-replica-2", "to_entity": "db-primary", "relation_type": "depends_on"},
            ],
        },
        "category": "technical",
    },
]


def main():
    if len(sys.argv) < 2:
        print("Usage: python3 generate-synthetic-golden.py <output-dir>", file=sys.stderr)
        sys.exit(1)

    output_dir = sys.argv[1]
    os.makedirs(output_dir, exist_ok=True)

    written = 0
    for ex in EXAMPLES:
        stem = ex["stem"]
        txt_path = os.path.join(output_dir, f"{stem}.txt")
        json_path = os.path.join(output_dir, f"{stem}.json")

        with open(txt_path, "w") as f:
            f.write(ex["text"] + "\n")

        expected = dict(ex["expected"])
        expected["_metadata"] = {
            "source": "synthetic",
            "annotator": "human",
            "category": ex["category"],
        }

        with open(json_path, "w") as f:
            json.dump(expected, f, indent=2)
            f.write("\n")

        written += 1

    print(f"Generated {written} synthetic golden examples in {output_dir}/")


if __name__ == "__main__":
    main()
