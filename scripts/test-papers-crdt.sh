#!/usr/bin/env bash
# Validate the pinned real Yjs engine and the store against isolated PostgreSQL.
set -euo pipefail
cd "$(dirname "$0")/.."
export PAPERS_CRDT_TEST_RUNTIME="$PWD/internal/platform/paperscrdt/runtime"
(
 cd "$PAPERS_CRDT_TEST_RUNTIME"
 npm ci --ignore-scripts --no-audit --no-fund
 npm test
)
./scripts/test-isolated.sh ./internal/modules/papers -run '^TestDurableCollab' -count=1
