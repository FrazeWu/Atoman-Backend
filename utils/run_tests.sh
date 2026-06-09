#!/bin/bash

# Connection Test Runner
# This script runs the connection tests by temporarily renaming the test file

TMPDIR="${TMPDIR:-/tmp}"

echo "========================================"
echo "Running Connection Tests"
echo "========================================\n"

# Temporarily restore the test file
if [ -f "test/test_connections.go.backup" ]; then
    cp test/test_connections.go.backup test/test_connections.go

    echo "Running tests..."
    GOCACHE="${TMPDIR}/atoman-gocache" GOPROXY=off GOSUMDB=off go run test/test_connections.go

    # Clean up
    rm test/test_connections.go
    echo "\n✅ Tests completed"
else
    echo "❌ Test file not found. Please run: go run test/test_connections.go.backup"
fi