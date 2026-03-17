#!/usr/bin/env bash
set -euo pipefail

echo "=== Pre-commit checks ==="

echo "--- Lint ---"
golangci-lint run --timeout=2m

echo "--- Tests (short) ---"
CGO_ENABLED=1 go test -tags sqlite_fts5 -short -count=1 ./...

echo "=== All checks passed ==="
