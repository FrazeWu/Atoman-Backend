#!/usr/bin/env bash
# Production-shaped SYNTHETIC-only test, no production credentials or content.
set -euo pipefail
if [[ "$#" != 1 ]]; then echo 'Usage: test-feed-prodshape-isolated.sh 10000|100000|360000' >&2; exit 2; fi
export FEED_SCALE_PROFILE=prodshape
exec "$(dirname "$0")/test-feed-scale-isolated.sh" "$1"
