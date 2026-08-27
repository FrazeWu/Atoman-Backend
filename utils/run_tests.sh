#!/bin/bash

set -euo pipefail

# Connection Test Runner
# This script runs the connection tests by temporarily renaming the test file

TMPDIR="${TMPDIR:-/tmp}"
TEST_FILE="test/test_connections.go"
BACKUP_FILE="${TEST_FILE}.backup"
TEMP_TEST_CREATED=0

cleanup() {
    if [ "${TEMP_TEST_CREATED}" -eq 1 ] && [ -f "${TEST_FILE}" ]; then
        rm -f "${TEST_FILE}"
    fi
}

trap cleanup EXIT

echo "========================================"
echo "Running Connection Tests"
echo "========================================\n"

# Temporarily restore the test file
if [ -f "${BACKUP_FILE}" ]; then
    cp "${BACKUP_FILE}" "${TEST_FILE}"
    TEMP_TEST_CREATED=1

    echo "Running tests..."
    GOCACHE="${TMPDIR}/atoman-gocache" GOPROXY=off GOSUMDB=off go run "${TEST_FILE}"
    echo "\n✅ Tests completed"
else
    echo "❌ Test file not found. Please run: go run test/test_connections.go.backup"
    exit 1
fi
