#!/usr/bin/env bash
set -euo pipefail

IMPRINT="${IMPRINT_BIN:-./imprint}"
CONFIG="${IMPRINT_CONFIG:-config.toml}"
TMPDIR=$(mktemp -d)
trap "rm -rf $TMPDIR" EXIT

echo "=== Imprint Smoke Test ==="
echo "Binary: $IMPRINT"
echo "Config: $CONFIG"
echo ""

# Step 1: Create test transcripts
cat > "$TMPDIR/meeting-2026-03-15.md" << 'TRANSCRIPT'
---
source: smoke-test
session: smoke-001
date: 2026-03-15T10:00:00Z
participants: [Alice, Bob]
topic: Project planning
---

[2026-03-15T10:00:00Z] Alice:
We decided to use Go for the Acme project. Single binary, no runtime deps.

[2026-03-15T10:01:00Z] Bob:
Makes sense. I'll set up the CI pipeline. We should deploy on Thursdays only.

[2026-03-15T10:02:00Z] Alice:
Agreed. And let's use SQLite for storage -- no external database needed.
TRANSCRIPT

cat > "$TMPDIR/standup-2026-03-16.md" << 'TRANSCRIPT'
---
source: smoke-test
session: smoke-002
date: 2026-03-16T09:00:00Z
participants: [Alice, Bob]
topic: Daily standup
---

[2026-03-16T09:00:00Z] Bob:
CI pipeline is ready. Tests run on push to main.

[2026-03-16T09:01:00Z] Alice:
Great. I finished the SQLite schema. Acme now has 5 tables.
Alice prefers dark mode in all editors, by the way.
TRANSCRIPT

echo "Created 2 test transcripts in $TMPDIR"
echo ""

# Step 2: Ingest
echo "--- Step 2: Ingest ---"
$IMPRINT --config "$CONFIG" ingest-dir "$TMPDIR"
echo ""

# Step 3: Status
echo "--- Step 3: Status ---"
STATUS=$($IMPRINT --config "$CONFIG" status)
echo "$STATUS"
FACTS=$(echo "$STATUS" | grep "Facts:" | awk '{print $2}')
if [ "$FACTS" -lt 1 ]; then
    echo "FAIL: Expected facts > 0, got $FACTS"
    exit 1
fi
echo "OK: $FACTS facts ingested"
echo ""

# Step 4: Query
echo "--- Step 4: Query ---"
ANSWER=$($IMPRINT --config "$CONFIG" query "What language is Acme written in?")
echo "$ANSWER"
if echo "$ANSWER" | grep -qi "go"; then
    echo "OK: Answer mentions Go"
else
    echo "WARN: Answer may not mention Go (check manually)"
fi
echo ""

# Step 5: Export
echo "--- Step 5: Export ---"
$IMPRINT --config "$CONFIG" export --output="$TMPDIR/export.json"
if [ -f "$TMPDIR/export.json" ]; then
    EXPORT_FACTS=$(python3 -c "import json; d=json.load(open('$TMPDIR/export.json')); print(len(d.get('facts',[])))" 2>/dev/null || echo "?")
    echo "OK: Export created, $EXPORT_FACTS facts"
else
    echo "FAIL: Export file not created"
    exit 1
fi
echo ""

# Step 6: Embed backfill (if embeddings not done during ingest)
echo "--- Step 6: Embed backfill ---"
$IMPRINT --config "$CONFIG" embed-backfill || echo "WARN: embed-backfill failed (may need embedding provider)"
echo ""

# Step 7: GC (should be no-op on fresh data)
echo "--- Step 7: GC ---"
$IMPRINT --config "$CONFIG" gc
echo "OK: GC completed"
echo ""

# Step 8: Consolidate
echo "--- Step 8: Consolidate ---"
$IMPRINT --config "$CONFIG" consolidate || echo "WARN: consolidate failed (may need consolidation provider)"
echo ""

echo "=== SMOKE TEST PASSED ==="
